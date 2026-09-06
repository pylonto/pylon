package cmd

import (
	"bytes"
	"encoding/json"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiWorkerStatusPauseResumeLoadsTheActualRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Chmod(home, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(home, ".pylon"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", "pi-only"), []byte("pylon-pi-v1\n"), 0600))
	require.NoError(t, config.SaveGlobal(&config.GlobalConfig{Server: config.ServerConfig{Host: "127.0.0.1", Port: 18473}}))
	p := &config.PylonConfig{Name: "repair", Trigger: config.TriggerConfig{Type: "webhook", Path: "/repair", Secret: "fixture-only", SignatureHeader: "X-Pylon-Signature"}, Workspace: config.WorkspaceConfig{Type: "git-clone", Repo: "/trusted/ciao", Ref: "{{ .body.source_revision }}"}, Agent: &config.PylonAgent{Type: "pi", Pi: &config.PiConfig{Image: "sha256:" + strings.Repeat("a", 64), AuthDir: filepath.Join(home, ".pylon", "pi-auth"), AllowedPaths: []string{"src/repair.txt"}, Limits: store.SubscriptionLimits{DailyJobs: 1, DailyTokens: 800000, JobTokens: 800000, JobSeconds: 60}}}}
	require.NoError(t, config.SavePylon(p))
	command := &cobra.Command{}
	command.Flags().String("home", home, "")
	for _, op := range []string{"status", "pause", "resume"} {
		var output bytes.Buffer
		command.SetOut(&output)
		require.NoError(t, runPiWorker(command, []string{op, "repair"}))
		var status store.SubscriptionStatus
		require.NoError(t, json.Unmarshal(output.Bytes(), &status))
		require.Zero(t, status.Records)
		require.Zero(t, status.TokensToday)
		if op == "pause" {
			require.Equal(t, "operator", status.Paused)
		} else {
			require.Empty(t, status.Paused)
		}
	}
}

func TestPiHomeRequiresExplicitPrivateRoleMarker(t *testing.T) {
	t.Setenv("HOME", os.Getenv("HOME"))
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0700))
	require.Error(t, piHome("relative"))
	require.Error(t, piHome(home))
	root := filepath.Join(home, ".pylon")
	require.NoError(t, os.Mkdir(root, 0700))
	marker := filepath.Join(root, "pi-only")
	require.NoError(t, os.WriteFile(marker, []byte("pylon-pi-v1\n"), 0600))
	require.NoError(t, piHome(home))
	require.Equal(t, home, os.Getenv("HOME"))
	require.NoError(t, os.Chmod(marker, 0644))
	require.Error(t, piHome(home))
	require.NoError(t, os.Chmod(marker, 0600))
	require.NoError(t, os.Chmod(home, 0755))
	require.Error(t, piHome(home))
}
