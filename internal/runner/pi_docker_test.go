package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/testutil"
	"github.com/stretchr/testify/require"
)

func piNative(t *testing.T) (*client.Client, PiParams) {
	t.Helper()
	image := os.Getenv("PYLON_PI_REHEARSAL_IMAGE")
	if image == "" {
		t.Skip("explicit disposable Docker rehearsal only")
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	home, repo := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
		cmd.Dir = repo
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost"}
		raw, err := cmd.CombinedOutput()
		require.NoError(t, err, string(raw))
		return strings.TrimSpace(string(raw))
	}
	git("init", "--template=")
	require.NoError(t, os.Mkdir(filepath.Join(repo, "src"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "src", "repair.txt"), []byte("broken\n"), 0644))
	git("add", "--", "src/repair.txt")
	git("commit", "-m", "Controlled fixture")
	p := PiParams{JobID: uuid.NewString(), Repository: repo, Base: git("rev-parse", "HEAD"), Config: piTestConfig(), Deadline: time.Now().Add(90 * time.Second), PatchRoot: t.TempDir(), Fixture: true}
	require.NoError(t, os.Chmod(p.PatchRoot, 0700))
	p.Config.Image = image
	p.Brief, err = json.Marshal(map[string]any{"v": 1, "kind": "ciao.vendor.maintenance", "purpose": "repair", "source_revision": p.Base, "qualification_id": strings.Repeat("a", 64), "publication": map[string]string{"mode": "none"}, "contract": []string{"fixture"}, "report": map[string]bool{"fixture": true}})
	require.NoError(t, err)
	return cli, p
}

func piNativeRemoved(t *testing.T, cli *client.Client, p PiParams) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "pylon.pi.job="+p.JobID))})
	require.NoError(t, err)
	require.Empty(t, containers, "no running, stopped or paused owned containers may remain")
	_, err = cli.VolumeInspect(ctx, "pylon-pi-"+p.JobID)
	require.True(t, errdefs.IsNotFound(err), "owned tmpfs volume must be removed: %v", err)
	source, err := os.ReadFile(filepath.Join(p.Repository, "src", "repair.txt"))
	require.NoError(t, err)
	require.Equal(t, "broken\n", string(source))
}

func TestPiDockerExportRefusesHostileFiles(t *testing.T) {
	for _, fixture := range []string{"symlink", "mode", "oversized"} {
		t.Run(fixture, func(t *testing.T) {
			cli, p := piNative(t)
			p.FixtureCase = fixture
			out := RunPiJob(context.Background(), p)
			require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
			require.Equal(t, "executor_returned", out.Runtime.Outcome, "the SDK must execute the hostile fixture, not fail before export")
			require.Equal(t, 3, out.Runtime.Tools)
			require.Zero(t, out.Runtime.Requests)
			require.Equal(t, "executor_failed", out.Result.Outcome)
			require.Equal(t, "pi_patch_export_failed", out.Failure)
			require.Empty(t, out.Patch)
			require.NotEmpty(t, out.Receipt)
			piNativeRemoved(t, cli, p)
		})
	}
}

func TestPiDockerUnusedAllowedPathsDoNotDiscardRepair(t *testing.T) {
	cli, p := piNative(t)
	ciao := testutil.CiaoSource(t)
	p.Config.AllowedPaths = []string{"src/repair.txt", "src/optional.txt", "new/nested/optional.txt"}
	for _, path := range p.Config.AllowedPaths[1:] {
		_, err := os.Lstat(filepath.Join(p.Repository, path))
		require.True(t, os.IsNotExist(err), "optional new paths must be absent from the base")
	}
	out := RunPiJob(context.Background(), p)
	piNativeRemoved(t, cli, p)
	require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
	require.Equal(t, "executor_returned", out.Runtime.Outcome, "the SDK must finish before the export assertion")
	require.Equal(t, 3, out.Runtime.Tools)
	require.Zero(t, out.Runtime.Requests)
	require.Equal(t, "executor_returned", out.Result.Outcome, "unused permission must not require a file: %s", out.Failure)
	require.NotEmpty(t, out.Receipt)
	patch, err := os.ReadFile(out.Patch)
	require.NoError(t, err)
	require.NotEmpty(t, patch)
	require.LessOrEqual(t, len(patch), MaxPiPatch)
	require.NotContains(t, string(patch), "optional.txt")
	// The independently pinned importer must see only the actual repair, not
	// placeholder files invented to satisfy the allowlist's unused permissions.
	script := `import sys
from pathlib import Path
from vendor_maintenance_lib.patching import prepared_patch
with prepared_patch(sys.argv[1],sys.argv[2],Path(sys.argv[3]).read_bytes(),["src/repair.txt","src/optional.txt","new/nested/optional.txt"]) as result:
 assert result["paths"]==["src/repair.txt"]
 assert (result["repository"]/"src/repair.txt").read_text()=="repaired"+chr(10)
 assert not (result["repository"]/"src/optional.txt").exists()
 assert not (result["repository"]/"new").exists()
print("existing repair accepted with absent optional permissions")`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-c", script, p.Repository, p.Base, out.Patch)
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ciao, "scripts"))
	raw, err := command.CombinedOutput()
	require.NoError(t, err, string(raw))
	t.Log(string(raw))
}

func TestPiDockerRenameExportsCompleteDeleteAdd(t *testing.T) {
	cli, p := piNative(t)
	ciao := testutil.CiaoSource(t)
	p.FixtureCase = "rename"
	p.Config.AllowedPaths = []string{"src/repair.txt", "src/renamed.txt", "src/optional.txt"}
	out := RunPiJob(context.Background(), p)
	piNativeRemoved(t, cli, p)
	require.Equal(t, "executor_returned", out.Result.Outcome, "failure=%s runtime=%+v", out.Failure, out.Runtime)
	require.Equal(t, 3, out.Runtime.Tools)
	require.Zero(t, out.Runtime.Requests)
	patch, err := os.ReadFile(out.Patch)
	require.NoError(t, err)
	require.Contains(t, string(patch), "deleted file mode 100644")
	require.Contains(t, string(patch), "new file mode 100644")
	require.NotContains(t, string(patch), "rename from")
	// This intentionally preserves 100% similarity: default Git rename inference
	// could hide the source deletion. Ciao must see and authorize BOTH paths.
	script := `import sys
from pathlib import Path
from vendor_maintenance_lib.patching import prepared_patch
from vendor_maintenance_lib.catalog import MaintenanceError
raw=Path(sys.argv[3]).read_bytes()
try:
 with prepared_patch(sys.argv[1],sys.argv[2],raw,["src/renamed.txt"]):
  raise AssertionError("underdeclared deletion was accepted")
except MaintenanceError as error:
 assert str(error)=="patch_outside_reviewed_scope"
with prepared_patch(sys.argv[1],sys.argv[2],raw,["src/repair.txt","src/renamed.txt"]) as result:
 assert set(result["paths"])=={"src/repair.txt","src/renamed.txt"}
 assert not (result["repository"]/"src/repair.txt").exists()
 assert (result["repository"]/"src/renamed.txt").read_text()=="broken"+chr(10)
print("complete delete/add accepted; underdeclared scope refused")`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-c", script, p.Repository, p.Base, out.Patch)
	command.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(ciao, "scripts"))
	raw, err := command.CombinedOutput()
	require.NoError(t, err, string(raw))
	t.Log(string(raw))
}

func TestPiDockerOnlyAbsentPermissionsCannotExportUnreviewedRepair(t *testing.T) {
	cli, p := piNative(t)
	p.Config.AllowedPaths = []string{"new/optional.txt"}
	out := RunPiJob(context.Background(), p)
	piNativeRemoved(t, cli, p)
	require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
	require.Equal(t, "executor_returned", out.Runtime.Outcome)
	require.Equal(t, 3, out.Runtime.Tools, "the fixture repairs an existing file OUTSIDE the allowlist")
	require.Zero(t, out.Runtime.Requests)
	require.Equal(t, "executor_failed", out.Result.Outcome)
	require.Equal(t, "pi_patch_empty", out.Failure, "no collected paths must not stage the sandbox's other edits")
	require.Empty(t, out.Patch)
	require.NotEmpty(t, out.Receipt)
}

func TestPiDockerCancellationAfterRuntimeStartRetainsUnknownUsage(t *testing.T) {
	cli, p := piNative(t)
	p.FixtureCase = "hang"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	messages, errs := cli.Events(observe, events.ListOptions{Filters: filters.NewArgs(filters.Arg("label", "pylon.pi.job="+p.JobID))})
	done := make(chan PiOutcome, 1)
	go func() { done <- RunPiJob(ctx, p) }()
started:
	for {
		select {
		case <-observe.Done():
			t.Fatal("runtime start was not observed")
		case err := <-errs:
			require.NoError(t, err)
			t.Fatal("Docker event stream closed")
		case out := <-done:
			t.Fatalf("executor ended before cancellation precondition: %s", out.Failure)
		case event := <-messages:
			if event.Action == events.ActionStart && event.Actor.Attributes["pylon.pi.role"] == "runtime" {
				break started
			}
		}
	}
	started := time.Now()
	cancel()
	select {
	case out := <-done:
		require.Less(t, time.Since(started), piTermination+3*time.Second)
		require.Equal(t, "executor_failed", out.Result.Outcome)
		require.Nil(t, out.Result.Usage)
		require.NotEmpty(t, out.Receipt)
		piNativeRemoved(t, cli, p)
	case <-time.After(piTermination + 5*time.Second):
		t.Fatal("cancellation did not terminate owned work")
	}
}

func TestPiDockerDeadlineAndIndependentSandboxWatchdog(t *testing.T) {
	cli, p := piNative(t)
	p.FixtureCase = "hang"
	p.Deadline = time.Now().Add(16 * time.Second)
	started := time.Now()
	out := RunPiJob(context.Background(), p)
	require.Less(t, time.Since(started), 17*time.Second)
	require.Equal(t, "executor_failed", out.Result.Outcome)
	require.Nil(t, out.Result.Usage)
	require.Contains(t, []string{"pi_deadline", "pi_runtime_failed", "pi_runtime_unknown"}, out.Failure)
	require.NotEmpty(t, out.Receipt)
	piNativeRemoved(t, cli, p)
	// No host timer or cancellation enforces this second lifetime. The immutable
	// PID 1 exits on its own and Docker terminates the namespace with it.
	p.JobID = uuid.NewString()
	p.Deadline = time.Now().Add(2 * time.Second)
	cfg, host := piContainerConfig(p, p.Config.Image, "tools", "sandbox.mjs", nil)
	cfg.WorkingDir = "/tmp"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	created, err := cli.ContainerCreate(ctx, cfg, host, nil, nil, "pylon-pi-"+p.JobID+"-watchdog")
	require.NoError(t, err)
	defer func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()
	require.NoError(t, cli.ContainerStart(ctx, created.ID, container.StartOptions{}))
	states, errs := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case state := <-states:
		require.EqualValues(t, 124, state.StatusCode)
	case err := <-errs:
		t.Fatalf("watchdog wait failed: %v", err)
	case <-ctx.Done():
		t.Fatal("independent watchdog did not stop")
	}
}
