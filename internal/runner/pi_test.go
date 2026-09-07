package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
)

func piTestConfig() config.PiConfig {
	return config.PiConfig{Image: "sha256:" + strings.Repeat("a", 64), AuthDir: "/private/role", AllowedPaths: []string{"src/repair.txt"},
		Limits: store.SubscriptionLimits{DailyJobs: 1, DailyTokens: 800000, JobTokens: 800000, JobSeconds: 60}}
}

func TestPiContainerCredentialAndNetworkBoundary(t *testing.T) {
	p := PiParams{JobID: "job", Config: piTestConfig(), Deadline: time.Now().Add(time.Minute)}
	for _, role := range []string{"runtime", "tools", "export"} {
		cfg, host := piContainerConfig(p, p.Config.Image, role, "worker.mjs", nil)
		require.True(t, host.ReadonlyRootfs)
		require.Equal(t, []string{"ALL"}, []string(host.CapDrop))
		require.Equal(t, []string{"no-new-privileges"}, host.SecurityOpt)
		require.Empty(t, host.ExtraHosts)
		require.Empty(t, host.Mounts)
		require.Equal(t, "none", host.LogConfig.Type)
		require.Positive(t, host.Memory)
		require.Positive(t, *host.PidsLimit)
		for _, entry := range cfg.Env {
			require.NotContains(t, entry, "TOKEN")
			require.NotContains(t, entry, "KEY")
			require.NotContains(t, entry, "AUTH")
		}
		if role != "runtime" {
			require.Equal(t, "none", string(host.NetworkMode))
		} else {
			require.Equal(t, "bridge", string(host.NetworkMode))
		}
	}
	p.Fixture = true
	_, host := piContainerConfig(p, p.Config.Image, "runtime", "worker.mjs", nil)
	require.Equal(t, "none", string(host.NetworkMode))
	_, mounts := BuildAgentEnv(RunParams{AgentType: "pi", Auth: "oauth"}, "/ignored")
	require.Empty(t, mounts)
	require.ErrorContains(t, RunAgentJob(context.Background(), RunParams{AgentType: "pi"}), "subscription_executor")
}

func TestPiExportRejectsUntrustedModesAndBounds(t *testing.T) {
	for _, mode := range []int64{0644, 0755, 0600} {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "repair.txt", Mode: mode, Size: 4, Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte("text"))
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		data, err := readPiFile(&archive, 4)
		if mode == 0644 {
			require.NoError(t, err)
			require.Equal(t, []byte("text"), data)
		} else {
			require.Error(t, err)
		}
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeFifo} {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "repair.txt", Mode: 0644, Typeflag: kind, Linkname: "../outside"}))
		require.NoError(t, tw.Close())
		_, err := readPiFile(&archive, 0)
		require.Error(t, err)
	}
	_, err := readPiFile(strings.NewReader(""), MaxPiPatch+1)
	require.Error(t, err)
	for _, data := range [][]byte{{0}, {0xff}} {
		var archive bytes.Buffer
		tw := tar.NewWriter(&archive)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "repair.txt", Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(data)
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		_, err = readPiFile(&archive, int64(len(data)))
		require.Error(t, err, "only UTF-8 without NUL is text")
	}
}

func TestPiBriefAndPatchPersistence(t *testing.T) {
	body := map[string]any{"v": 1, "kind": "ciao.vendor.maintenance", "purpose": "repair", "source_revision": strings.Repeat("a", 40),
		"qualification_id": strings.Repeat("b", 64), "publication": map[string]any{"mode": "none"}, "contract": []string{"fixture"}, "report": map[string]any{"fixture": true}}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	base, err := PiBrief(raw)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 40), base)
	body["publication"] = map[string]any{"mode": "draft_pr"}
	raw, err = json.Marshal(body)
	require.NoError(t, err)
	_, err = PiBrief(raw)
	require.Error(t, err)
	root := filepath.Join(t.TempDir(), "patches")
	path, err := savePiPatch(root, "job", []byte("patch"))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.EqualValues(t, 0600, info.Mode().Perm())
	_, err = savePiPatch(root, "job", []byte("replacement"))
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "patch", string(data))
	_, err = savePiPatch(root, "other", make([]byte, MaxPiPatch+1))
	require.Error(t, err)
}

func TestPiExportDestinationRefusesSymlinkBeforeDeletion(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "file.txt"), []byte("private"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "src")))
	_, err := piDestination(root, "src/file.txt")
	require.Error(t, err)
	_, err = piDestination(root, "../file.txt")
	require.Error(t, err)
	data, err := os.ReadFile(filepath.Join(outside, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "private", string(data))
	path, err := piDestination(root, "new/file.txt")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "new", "file.txt"), path)
}

func TestPiAuthRoleRefusesAuxiliaryExecutables(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	for _, name := range []string{"auth.json", "settings.json", "models-store.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("{}"), 0600))
	}
	require.NoError(t, privatePiAuth(root), "file-only CLI metadata control")
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0700))
	for _, name := range []string{"fd", "rg"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "bin", name), []byte("fixture, never executed"), 0755))
	}
	require.ErrorContains(t, privatePiAuth(root), "pi_auth_role_invalid", "fix the login command, not the executable-storage fence")
}

func TestPiAuthRoleRefusesPersonalOrSymlinkStorage(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "auth.json"), []byte("{}"), 0600))
	require.NoError(t, privatePiAuth(root))
	require.NoError(t, os.WriteFile(filepath.Join(root, "settings.json"), []byte("{}"), 0600))
	require.NoError(t, privatePiAuth(root), "fresh CLI metadata is ignored, not inherited")
	require.NoError(t, os.WriteFile(filepath.Join(root, "personal-settings.json"), []byte("{}"), 0600))
	require.Error(t, privatePiAuth(root))
	require.NoError(t, os.Remove(filepath.Join(root, "personal-settings.json")))
	require.NoError(t, os.Chmod(filepath.Join(root, "auth.json"), 0644))
	require.Error(t, privatePiAuth(root))
}
