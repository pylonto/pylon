package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func inspectFixtureSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	directory, err := os.OpenRoot(root)
	require.NoError(t, err)
	defer directory.Close()
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		result[path] = info.Mode().String()
		if info.Mode().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			raw, err := directory.ReadFile(relative)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			result[path] += " " + hex.EncodeToString(hash[:])
		}
		return nil
	}))
	return result
}

func TestPiWorkerInspectIsOfflinePrivateReadOnlyAndIgnoresRoleState(t *testing.T) {
	home, debug := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(home, 0700))
	require.NoError(t, os.Chmod(debug, 0700))
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_HOST", "unix:///absent-inspection-docker.sock")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PYLON_INSPECT_POISON", "")
	require.NoError(t, os.Mkdir(filepath.Join(home, ".pylon"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", "pi-only"), []byte("pylon-pi-v1\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", ".env"), []byte("PYLON_INSPECT_POISON=must_not_load\n"), 0600))
	p := &config.PylonConfig{Name: "repair", Workspace: config.WorkspaceConfig{Repo: filepath.Join(home, "absent-repo")}, Agent: &config.PylonAgent{Type: "pi", Pi: &config.PiConfig{AuthDir: filepath.Join(home, ".pylon", "pi-auth"), DebugDir: debug}}}
	require.NoError(t, config.SavePylon(p))
	require.NoError(t, os.Mkdir(config.PylonDBPath(p.Name), 0700))
	evidence := filepath.Join(home, ".pylon", "pi-patches")
	id := pidebug.Identity{V: 1, Pylon: p.Name, Job: uuid.NewString(), Base: strings.Repeat("a", 40), Image: "sha256:" + strings.Repeat("b", 64), Context: pidebug.Context(p.Name, p.Workspace.Repo, p.Agent.Pi.AuthDir, evidence)}
	r, err := pidebug.Start(debug, id, p.Workspace.Repo, p.Agent.Pi.AuthDir, evidence)
	require.NoError(t, err)
	require.NoError(t, r.Accept(pidebug.Frame{Sequence: 1, Event: &pidebug.Event{Kind: "assistant_text", Text: "Private visible text \x1b]0;not a terminal instruction\a"}}))
	require.NoError(t, r.Accept(pidebug.Frame{Sequence: 2, Close: &pidebug.Closure{Events: 1}}))
	r.ConfirmRuntimeClosure(true)
	r.Close("executor_failed", "pi_patch_empty", true)
	beforeHome, beforeDebug := inspectFixtureSnapshot(t, home), inspectFixtureSnapshot(t, debug)
	command := &cobra.Command{}
	command.Flags().String("home", home, "")
	command.Flags().String("job", id.Job, "")
	var previous []byte
	for range 2 {
		var output bytes.Buffer
		command.SetOut(&output)
		require.NoError(t, runPiWorker(command, []string{"inspect", "repair"}))
		require.NotContains(t, output.String(), "\x1b")
		require.Contains(t, output.String(), `\u001b`)
		var got pidebug.Inspection
		require.NoError(t, json.Unmarshal(output.Bytes(), &got))
		require.True(t, got.Private)
		require.True(t, got.Untrusted)
		require.False(t, got.Publishable)
		require.False(t, got.Summary.Incomplete)
		require.Equal(t, id.Context, got.Summary.Context)
		if previous != nil {
			require.Equal(t, previous, output.Bytes())
		}
		previous = append([]byte(nil), output.Bytes()...)
	}
	require.Empty(t, os.Getenv("PYLON_INSPECT_POISON"))
	require.Equal(t, beforeHome, inspectFixtureSnapshot(t, home))
	require.Equal(t, beforeDebug, inspectFixtureSnapshot(t, debug))
	for _, path := range []string{p.Agent.Pi.AuthDir, p.Workspace.Repo, evidence} {
		_, err := os.Lstat(path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestPiWorkerInspectRefusesMissingHomeWithoutCreatingIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	command := &cobra.Command{}
	command.Flags().String("home", home, "")
	command.Flags().String("job", uuid.NewString(), "")
	require.Error(t, runPiWorker(command, []string{"inspect", "repair"}))
	_, err := os.Lstat(home)
	require.True(t, os.IsNotExist(err))
}

func TestPiOperatorReadRefusesUnsafeObjectsAndBounds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(path, []byte("control"), 0600))
	raw, err := readPiOperatorFile(path, 7, true)
	require.NoError(t, err)
	require.Equal(t, "control", string(raw))
	_, err = readPiOperatorFile(path, 6, true)
	require.Error(t, err)
	require.NoError(t, os.Link(path, path+"-hard"))
	_, err = readPiOperatorFile(path, 7, true)
	require.Error(t, err)
	require.NoError(t, os.Remove(path+"-hard"))
	require.NoError(t, os.Symlink(path, path+"-link"))
	_, err = readPiOperatorFile(path+"-link", 7, true)
	require.Error(t, err)
	require.NoError(t, syscall.Mkfifo(path+"-fifo", 0600))
	_, err = readPiOperatorFile(path+"-fifo", 7, true)
	require.Error(t, err)
}
