package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/pylonto/pylon/internal/proxy"
	"github.com/pylonto/pylon/internal/store"
)

// This transport ceiling mirrors Ciao patching.MAX_PATCH, not a second scope
// policy. The cross-repository rehearsal passes these bytes to prepared_patch.
const MaxPiPatch = 1024 * 1024
const piArchiveBound = 128 * 1024 * 1024
const piTermination = 10 * time.Second

var errPiRun = errors.New("pi_execution_failed")

type PiParams struct {
	Pylon      string
	JobID      string
	Brief      []byte
	Base       string
	Repository string
	Config     config.PiConfig
	Deadline   time.Time
	PatchRoot  string
	// Fixture is executor/test configuration, NEVER a field accepted from a brief.
	Fixture     bool
	FixtureCase string
}

type PiOutcome struct {
	Result      store.SubscriptionResult
	Runtime     *proxy.PiResult
	Patch       string
	PatchSHA256 string
	Failure     string
	Receipt     string
	Debug       *pidebug.Summary
}

// PiBrief checks the existing Ciao envelope before durable admission. Source,
// purpose and publication cannot be chosen by prompt interpolation or tool output.
func PiBrief(raw []byte) (string, error) {
	var brief struct {
		Version       int    `json:"v"`
		Kind          string `json:"kind"`
		Purpose       string `json:"purpose"`
		Source        string `json:"source_revision"`
		Qualification string `json:"qualification_id"`
		Publication   struct {
			Mode string `json:"mode"`
		} `json:"publication"`
		Report   json.RawMessage `json:"report"`
		Contract []string        `json:"contract"`
	}
	if len(raw) > store.MaxDeliveryBytes || json.Unmarshal(raw, &brief) != nil || brief.Version != 1 ||
		brief.Kind != "ciao.vendor.maintenance" || brief.Purpose != "repair" || brief.Publication.Mode != "none" ||
		len(brief.Source) != 40 || !gitObjectID.MatchString(brief.Source) || strings.ToLower(brief.Source) != brief.Source ||
		len(brief.Qualification) != 64 || len(brief.Report) == 0 || len(brief.Contract) == 0 {
		return "", errors.New("pi_signed_repair_brief_required")
	}
	if _, err := hex.DecodeString(brief.Qualification); err != nil {
		return "", errPiRun
	}
	return brief.Source, nil
}

// Composition is intentional: embedding bytes.Buffer promotes ReadFrom and
// WriteString, allowing io.Copy (including a source WriterTo) to bypass Write.
type piBuffer struct {
	buffer   bytes.Buffer
	max      int
	overflow bool
}

var errPiOutputBound = errors.New("pi_output_bound")

func (b *piBuffer) Len() int       { return b.buffer.Len() }
func (b *piBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *piBuffer) String() string { return b.buffer.String() }

func (b *piBuffer) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		b.overflow = true
		return 0, errPiOutputBound
	}
	return b.buffer.Write(p)
}

func piGit(ctx context.Context, home, repo string, max int, args ...string) ([]byte, error) {
	data, _, err := piGitWithStats(ctx, home, repo, max, args...)
	return data, err
}

func piGitWithStats(ctx context.Context, home, repo string, max int, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = repo
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	cmd.WaitDelay = time.Second
	var output piBuffer
	output.max = max
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err := cmd.Run()
	// An output-bound write may also make Git exit through SIGPIPE. Preserve
	// the writer's verdict rather than losing it behind the process exit error.
	if output.overflow {
		return nil, output.Len(), errPiOutputBound
	}
	if ctx.Err() != nil {
		return nil, output.Len(), ctx.Err()
	}
	if err != nil {
		return nil, output.Len(), errors.New("pi_git_operation_failed")
	}
	return output.Bytes(), output.Len(), nil
}

// Bound the exact tree before fetching, and fetch only that commit rather than
// cloning unbounded repository history. No source config enters the new repo.
func piSourceBound(ctx context.Context, home, source, base string) error {
	listing, err := piGit(ctx, home, source, 4*1024*1024, "ls-tree", "-r", "-l", "-z", base)
	if err != nil {
		return err
	}
	var total int64
	for _, row := range bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0}) {
		metadata, _, ok := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !ok || len(fields) != 4 || fields[1] != "blob" {
			return errPiRun
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || size > piArchiveBound-total {
			return errPiRun
		}
		total += size
	}
	return nil
}

func piHeadroom(path string) error {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil || fs.Bavail*uint64(fs.Bsize) < 2304*1024*1024 {
		return errors.New("pi_disk_reserve")
	}
	return nil
}

func privatePiAuth(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("pi_auth_role_invalid")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("pi_auth_role_invalid")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.New("pi_auth_role_invalid")
	}
	for _, entry := range entries {
		// The existing Pi CLI can create these two role-local metadata files at
		// login. The worker never loads them: it fixes modelsPath:null, supplies
		// its image catalog and uses in-memory settings and no discovery.
		if entry.Name() != "auth.json" && entry.Name() != "settings.json" && entry.Name() != "models-store.json" {
			return errors.New("pi_auth_role_invalid")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8*1024*1024 {
			return errors.New("pi_auth_role_invalid")
		}
	}
	info, err = os.Lstat(filepath.Join(path, "auth.json"))
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return errors.New("pi_auth_unavailable")
	}
	return nil
}

// RunPiJob supervises a credentialed immutable Pi runtime and a separate offline
// tool sandbox. It never executes repository code on the host/runtime, mounts a
// Docker socket, or consumes tool-reported usage. Setup and cleanup share the
// durable claim deadline; incomplete termination retains the unresolved claim.
func RunPiJob(parent context.Context, p PiParams) (out PiOutcome) {
	out.Result = store.SubscriptionResult{Outcome: "executor_failed", Usage: &store.SubscriptionUsage{}}
	out.Failure = "pi_setup_failed"
	if id, err := uuid.Parse(p.JobID); err != nil || id.String() != p.JobID {
		return out
	}
	if p.FixtureCase != "" {
		if !p.Fixture {
			return out
		}
		switch p.FixtureCase {
		case "hang", "symlink", "mode", "oversized", "rename":
		default:
			if !piSDKFixture(p.FixtureCase) {
				return out
			}
		}
	}
	base, err := PiBrief(p.Brief)
	if err != nil || base != p.Base || p.Config.Validate() != nil || !filepath.IsAbs(p.Repository) || !p.Deadline.After(time.Now().Add(piTermination)) {
		return out
	}
	// This is executor evidence only, not qualification or permission to publish.
	// Register first so it records the final cleanup/usage verdict, never a receipt
	// written before writers are known to have stopped.
	defer func() {
		raw, err := piReceiptBytes(p, out)
		if err == nil {
			out.Receipt, err = savePiArtifact(p.PatchRoot, p.JobID+".json", raw)
		}
		if err != nil {
			out.Result.Outcome, out.Failure = "executor_failed", "pi_receipt_storage_failed"
		}
	}()
	ctx, cancel := context.WithDeadline(parent, p.Deadline.Add(-piTermination))
	defer cancel()
	var debug *pidebug.Recorder
	if p.Config.DebugDir != "" {
		identity := pidebug.Identity{V: 1, Pylon: p.Pylon, Job: p.JobID, Base: p.Base, Image: p.Config.Image,
			Context: pidebug.Context(p.Pylon, p.Repository, p.Config.AuthDir, p.PatchRoot)}
		debug, err = pidebug.Start(p.Config.DebugDir, identity, p.Repository, p.Config.AuthDir, p.PatchRoot)
		if err != nil {
			out.Failure = "pi_debug_unavailable"
			return out
		}
		defer func() {
			summary := debug.Close(out.Result.Outcome, out.Failure, out.Failure != "pi_termination_unknown")
			out.Debug = &summary
		}()
	}
	if err := piHeadroom(os.TempDir()); err != nil {
		out.Failure = "pi_disk_reserve"
		return out
	}
	if !p.Fixture && privatePiAuth(p.Config.AuthDir) != nil {
		out.Failure, out.Result.Pause = "pi_auth_unavailable", "auth_unavailable"
		return out
	}
	tmp, err := os.MkdirTemp("", "pylon-pi-")
	if err != nil {
		return out
	}
	defer os.RemoveAll(tmp)
	repo := filepath.Join(tmp, "repo")
	out.Failure = "pi_source_bound_failed"
	if piSourceBound(ctx, tmp, p.Repository, p.Base) != nil {
		return out
	}
	out.Failure = "pi_clone_failed"
	if _, err = piGit(ctx, tmp, "", 4096, "init", "--template=", "--", repo); err != nil {
		return out
	}
	if _, err = piGit(ctx, tmp, repo, 4096, "fetch", "--depth=1", "--no-tags", "--no-write-fetch-head", "--", p.Repository, p.Base); err != nil {
		return out
	}
	out.Failure = "pi_checkout_failed"
	if _, err = piGit(ctx, tmp, repo, 4096, "checkout", "--detach", p.Base, "--"); err != nil {
		return out
	}
	out.Failure = "pi_archive_failed"
	archive, err := piGit(ctx, tmp, repo, piArchiveBound, "archive", "--format=tar", p.Base)
	if err != nil {
		return out
	}
	out.Failure = "pi_docker_client_failed"
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return out
	}
	defer cli.Close()
	if goruntime.GOOS != "linux" || !strings.HasPrefix(cli.DaemonHost(), "unix://") {
		out.Failure = "pi_local_linux_docker_required"
		return out
	}
	imageInfo, _, err := cli.ImageInspectWithRaw(ctx, p.Config.Image)
	if err != nil {
		out.Failure = "pi_immutable_image_unavailable"
		return out
	}
	// Always create by immutable local ID, never re-pull or follow a mutable tag.
	imageID := imageInfo.ID
	var ids []string
	volumeName := "pylon-pi-" + p.JobID
	volumeOwned := false
	defer func() {
		until := time.Now().Add(piTermination)
		if p.Deadline.Before(until) {
			until = p.Deadline
		}
		cleanup, stop := context.WithDeadline(context.Background(), until)
		defer stop()
		for i := len(ids) - 1; i >= 0; i-- {
			if err := cli.ContainerRemove(cleanup, ids[i], container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				out.Result.Usage = nil
				out.Result.Outcome = "executor_failed"
				out.Failure = "pi_termination_unknown"
			}
		}
		if volumeOwned {
			if err := cli.VolumeRemove(cleanup, volumeName, false); err != nil && !errdefs.IsNotFound(err) {
				out.Result.Usage = nil
				out.Result.Outcome = "executor_failed"
				out.Failure = "pi_termination_unknown"
			}
		}
	}()
	// Docker's archive API cannot see HostConfig.Tmpfs mounts, even when the
	// container is live. A named local-driver tmpfs has the same size bound and
	// no host workspace bind, but a read-only holder can preserve it after the
	// untrusted writer container is destroyed.
	out.Failure = "pi_workspace_volume_failed"
	if _, err := cli.VolumeInspect(ctx, volumeName); !errdefs.IsNotFound(err) {
		return out
	}
	_, err = cli.VolumeCreate(ctx, volume.CreateOptions{Name: volumeName, Driver: "local", Labels: map[string]string{"pylon.pi.job": p.JobID},
		DriverOpts: map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=128m,nosuid,nodev,uid=" + strconv.Itoa(os.Getuid()) + ",gid=" + strconv.Itoa(os.Getgid()) + ",mode=0700"}})
	if err != nil {
		return out
	}
	volumeOwned = true
	create := func(role, script string, mounts []mount.Mount) (string, error) {
		cfg, host := piContainerConfig(p, imageID, role, script, mounts)
		response, err := cli.ContainerCreate(ctx, cfg, host, nil, nil, "pylon-pi-"+p.JobID+"-"+role)
		if err != nil {
			return "", errPiRun
		}
		ids = append(ids, response.ID)
		return response.ID, nil
	}
	out.Failure = "pi_sandbox_create_failed"
	sandbox, err := create("tools", "sandbox.mjs", []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/workspace"}})
	if err != nil {
		return out
	}
	out.Failure = "pi_sandbox_start_failed"
	if err = cli.ContainerStart(ctx, sandbox, container.StartOptions{}); err != nil {
		return out
	}
	out.Failure = "pi_workspace_copy_failed"
	// Docker's archive-copy API refuses read-only rootfs even for a tmpfs
	// destination. Extract the trusted Git archive as the sandbox uid instead;
	// keep read-only rootfs rather than weakening it to accommodate docker cp.
	if _, err = piExec(ctx, cli, sandbox, []string{"/bin/tar", "--no-same-owner", "-xf", "-", "-C", "/workspace"}, archive, 4096); err != nil {
		out.Failure = err.Error() // piExec returns categorical errors only.
		return out
	}
	out.Failure = "pi_transport_setup_failed"
	transport := filepath.Join(tmp, "transport")
	if os.Mkdir(transport, 0700) != nil {
		return out
	}
	listener, err := net.Listen("unix", filepath.Join(transport, "pi.sock"))
	if err != nil {
		return out
	}
	defer listener.Close()
	bridge := proxy.NewPi(ctx, proxy.PiJob{Thinking: p.Config.WorkerThinking(), Brief: p.Brief, Deadline: p.Deadline.Add(-piTermination).Unix(), Tokens: p.Config.Limits.JobTokens, Fixture: p.Fixture, FixtureCase: p.FixtureCase},
		func(callCtx context.Context, raw []byte) ([]byte, error) { return piTool(callCtx, cli, sandbox, raw) }, debug)
	server := &http.Server{Handler: bridge, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	mounts := []mount.Mount{{Type: mount.TypeBind, Source: transport, Target: "/transport", ReadOnly: true}}
	if !p.Fixture {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: p.Config.AuthDir, Target: "/role"})
	}
	out.Failure = "pi_runtime_create_failed"
	runtime, err := create("runtime", "worker.mjs", mounts)
	if err != nil {
		return out
	}
	// From this point a lost process/receipt is not evidence of zero provider use.
	out.Result.Usage = nil
	out.Failure = "pi_runtime_start_failed"
	if cli.ContainerStart(ctx, runtime, container.StartOptions{}) != nil {
		return out
	}
	debug.Host("lifecycle", "runtime_started", nil)
	status, errs := cli.ContainerWait(ctx, runtime, container.WaitConditionNotRunning)
	select {
	case <-ctx.Done():
		out.Failure = "pi_deadline"
		return out
	case <-errs:
		out.Failure = "pi_runtime_unknown"
		return out
	case ended := <-status:
		debug.Host("lifecycle", "runtime_stopped", map[string]int{"exit_code": int(ended.StatusCode)})
		if ended.StatusCode != 0 {
			out.Failure = "pi_runtime_failed"
			return out
		}
	}
	r := bridge.Result()
	if r == nil {
		out.Failure = "pi_runtime_receipt_missing"
		return out
	}
	out.Runtime = r
	out.Result = store.SubscriptionResult{Outcome: r.Outcome, Usage: r.Usage, Pause: r.Pause}
	out.Failure = r.Failure
	if r.Outcome != "executor_returned" {
		return out
	}
	out.Result.Outcome = "executor_failed"
	// A paused sandbox would also freeze its independent deadline watchdog if
	// the supervisor died. Instead, hold its tmpfs read-only in a tiny offline
	// export container, then destroy ALL untrusted writers before reading. The
	// holder executes only the immutable watchdog, with no credentials or socket.
	out.Failure = "pi_export_holder_failed"
	holder, err := create("export", "sandbox.mjs", []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/workspace", ReadOnly: true}})
	if err != nil || cli.ContainerStart(ctx, holder, container.StartOptions{}) != nil {
		return out
	}
	if cli.ContainerRemove(ctx, sandbox, container.RemoveOptions{Force: true}) != nil {
		return out
	}
	collected, err := piCollect(ctx, cli, holder, repo, p.Config.AllowedPaths)
	collection := "returned"
	if err != nil {
		collection = "failed"
	}
	debug.Host("collection", collection, map[string]int{"allowed": len(p.Config.AllowedPaths), "collected": len(collected)})
	if err != nil {
		out.Failure = "pi_patch_export_failed"
		return out
	}
	// An allowed new path can remain absent. Stage only collected additions or
	// previously tracked deletions; Git rejects an unused new pathspec. Ciao still
	// validates the full resulting path set, modes and exact base independently.
	if len(collected) > 0 {
		args := append([]string{"add", "--"}, collected...)
		if _, err = piGit(ctx, tmp, repo, 4096, args...); err != nil {
			debug.Host("staging", "failed", map[string]int{"staged": 0})
			out.Failure = "pi_patch_export_failed"
			return out
		}
	}
	debug.Host("staging", "returned", map[string]int{"staged": len(collected)})
	patch, observed, err := piGitWithStats(ctx, tmp, repo, MaxPiPatch, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-renames", "--full-index", p.Base, "--")
	category := piDiffOutcome(patch, err)
	known := 0
	if err == nil {
		known = 1
	}
	debug.Host("diff", category, map[string]int{"bytes": observed, "total_known": known})
	if category != "nonempty" {
		out.Failure = "pi_patch_" + category
		return out
	}
	path, err := savePiPatch(p.PatchRoot, p.JobID, patch)
	if err != nil {
		out.Failure = "pi_patch_storage_failed"
		return out
	}
	digest := sha256.Sum256(patch)
	out.Patch, out.PatchSHA256 = path, hex.EncodeToString(digest[:])
	out.Result.Outcome, out.Failure = "executor_returned", ""
	return out
}

func piContainerConfig(p PiParams, imageID, role, script string, mounts []mount.Mount) (*container.Config, *container.HostConfig) {
	pids := int64(128)
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	cfg := &container.Config{Image: imageID, User: uid + ":" + gid, WorkingDir: "/workspace",
		Entrypoint: []string{"/usr/local/bin/node", "/opt/pylon/" + script}, Cmd: []string{strconv.FormatInt(p.Deadline.Unix(), 10)},
		Env:    []string{"HOME=/tmp/home", "PATH=/usr/local/bin:/usr/bin:/bin", "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0", "PI_OFFLINE=1"},
		Labels: map[string]string{"pylon.pi.job": p.JobID, "pylon.pi.role": role},
	}
	host := &container.HostConfig{ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		NetworkMode: "none", Mounts: mounts, LogConfig: container.LogConfig{Type: "none"},
		Resources: container.Resources{Memory: 1024 * 1024 * 1024, MemorySwap: 1024 * 1024 * 1024, NanoCPUs: 1_000_000_000, PidsLimit: &pids},
		Tmpfs:     map[string]string{"/tmp": "rw,nosuid,nodev,size=64m,mode=1777"},
	}
	if role == "runtime" {
		host.NetworkMode = "bridge"
		host.Memory = 768 * 1024 * 1024
		host.MemorySwap = host.Memory
		cfg.WorkingDir = "/tmp"
	}
	if role == "export" {
		host.Memory = 128 * 1024 * 1024
		host.MemorySwap = host.Memory
	}
	if p.Fixture {
		host.NetworkMode = "none"
	}
	return cfg, host
}

func piTool(ctx context.Context, cli *client.Client, id string, raw []byte) ([]byte, error) {
	return piExec(ctx, cli, id, []string{"/usr/local/bin/node", "/opt/pylon/tool.mjs"}, raw, proxy.MaxPiToolBytes)
}

func piExec(ctx context.Context, cli *client.Client, id string, args []string, raw []byte, maximum int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	execution, err := cli.ContainerExecCreate(ctx, id, container.ExecOptions{User: strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), WorkingDir: "/workspace", AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Cmd: args, Env: []string{"HOME=/tmp/home", "PATH=/usr/local/bin:/usr/bin:/bin"}})
	if err != nil {
		return nil, errors.New("pi_exec_create_failed")
	}
	conn, err := cli.ContainerExecAttach(ctx, execution.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, errors.New("pi_exec_attach_failed")
	}
	defer conn.Close()
	// Hijacked Docker connections outlive request cancellation unless explicitly
	// closed. Cancellation must unblock both writes and reads, not just ExecCreate.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = io.Copy(conn.Conn, bytes.NewReader(raw)); err != nil {
		return nil, errors.New("pi_exec_input_failed")
	}
	if err = conn.CloseWrite(); err != nil {
		return nil, errors.New("pi_exec_close_input_failed")
	}
	var out piBuffer
	out.max = maximum
	if _, err = stdcopy.StdCopy(&out, io.Discard, conn.Reader); err != nil {
		return nil, errors.New("pi_exec_output_failed")
	}
	status, err := cli.ContainerExecInspect(ctx, execution.ID)
	if err != nil {
		return nil, errors.New("pi_exec_inspect_failed")
	}
	if status.Running {
		return nil, errors.New("pi_exec_still_running")
	}
	if status.ExitCode != 0 {
		return nil, errors.New("pi_exec_nonzero_" + strconv.Itoa(status.ExitCode))
	}
	return out.Bytes(), nil
}

func piDestination(repo, path string) (string, error) {
	dest := filepath.Join(repo, path)
	if !strings.HasPrefix(dest, repo+string(os.PathSeparator)) {
		return "", errPiRun
	}
	for part := dest; part != repo; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || (part == dest && !info.Mode().IsRegular())) {
			return "", errPiRun
		}
		if err != nil && !os.IsNotExist(err) {
			return "", errPiRun
		}
	}
	return dest, nil
}

// The clean checkout contains only tracked base files, and only this collector
// mutates it. Return the exact written/deleted subset for staging, not permissions
// for paths that stayed absent in both the base and sandbox.
func piCollect(ctx context.Context, cli *client.Client, id, repo string, paths []string) ([]string, error) {
	var collected []string
	for _, path := range paths {
		// Check before deletion too: a missing sandbox path must never unlink
		// through a symlink parent in the otherwise trusted clean base.
		dest, err := piDestination(repo, path)
		if err != nil {
			return nil, err
		}
		stream, stat, err := cli.CopyFromContainer(ctx, id, "/workspace/"+path)
		if errdefs.IsNotFound(err) {
			if removeErr := os.Remove(dest); removeErr == nil {
				collected = append(collected, path)
			} else if !os.IsNotExist(removeErr) {
				return nil, errPiRun
			}
			continue
		}
		if err != nil {
			return nil, errPiRun
		}
		if !stat.Mode.IsRegular() || stat.LinkTarget != "" {
			stream.Close()
			return nil, errPiRun
		}
		data, readErr := readPiFile(stream, stat.Size)
		stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return nil, errPiRun
		}
		if err := os.WriteFile(dest, data, 0600); err != nil {
			return nil, errPiRun
		}
		if err := os.Chmod(dest, 0644); err != nil {
			return nil, errPiRun
		}
		collected = append(collected, path)
	}
	return collected, nil
}

func readPiFile(stream io.Reader, size int64) ([]byte, error) {
	if size < 0 || size > MaxPiPatch {
		return nil, errPiRun
	}
	tarReader := tar.NewReader(io.LimitReader(stream, MaxPiPatch+8192))
	header, err := tarReader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Mode != 0644 || header.Size != size {
		return nil, errPiRun
	}
	data, err := io.ReadAll(io.LimitReader(tarReader, MaxPiPatch+1))
	if err != nil || int64(len(data)) != size || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, errPiRun
	}
	if _, err = tarReader.Next(); !errors.Is(err, io.EOF) {
		return nil, errPiRun
	}
	return data, nil
}

var piPatchMu sync.Mutex

func savePiPatch(root, job string, patch []byte) (string, error) {
	return savePiArtifact(root, job+".patch", patch)
}

func savePiArtifact(root, name string, patch []byte) (string, error) {
	piPatchMu.Lock()
	defer piPatchMu.Unlock()
	if !filepath.IsAbs(root) || filepath.Base(name) != name || strings.HasPrefix(name, ".") || len(patch) == 0 || len(patch) > MaxPiPatch {
		return "", errPiRun
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", errPiRun
	}
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return "", errPiRun
	}
	if err := piHeadroom(root); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) >= 4096 {
		return "", errPiRun
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return "", errPiRun
		}
		total += info.Size()
	}
	if total+int64(len(patch)) > 64*1024*1024 {
		return "", errPiRun
	}
	path := filepath.Join(root, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errPiRun
	}
	_, err = file.Write(patch)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", errPiRun
	}
	return path, nil
}
