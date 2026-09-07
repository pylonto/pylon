package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestPiBudgetUsesConfiguredCapsAndRetainsActualLedgerUsage(t *testing.T) {
	for _, thinking := range []config.PiThinking{"", config.PiThinkingMedium, config.PiThinkingMax} {
		t.Run("thinking_"+string(thinking), func(t *testing.T) { piBudgetConfiguredThinking(t, thinking) })
	}
}

func piBudgetConfiguredThinking(t *testing.T, thinking config.PiThinking) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Chmod(home, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(home, ".pylon"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".pylon", "pi-only"), []byte("pylon-pi-v1\n"), 0600))
	require.NoError(t, config.SaveGlobal(&config.GlobalConfig{Server: config.ServerConfig{Host: "127.0.0.1", Port: 18473}}))
	old := store.SubscriptionLimits{DailyJobs: 1, DailyTokens: 1600000, JobTokens: 1600000, JobSeconds: 300}
	p := &config.PylonConfig{Name: "repair", Trigger: config.TriggerConfig{Type: "webhook", Path: "/repair", Secret: "fixture-only", SignatureHeader: "X-Pylon-Signature"}, Workspace: config.WorkspaceConfig{Type: "git-clone", Repo: "/trusted/fixture", Ref: "{{ .body.source_revision }}"}, Agent: &config.PylonAgent{Type: "pi", Pi: &config.PiConfig{Image: "sha256:" + strings.Repeat("a", 64), AuthDir: filepath.Join(home, ".pylon", "pi-auth"), AllowedPaths: []string{"src/repair.txt"}, Limits: old}}}
	p.Agent.Pi.Thinking = thinking
	require.NoError(t, config.SavePylon(p))
	s, err := store.Open(config.PylonDBPath(p.Name))
	require.NoError(t, err)
	now := time.Now().Add(-time.Second)
	body := []byte(`{"fixture":true}`)
	d, fresh, err := s.AcceptDelivery(p.Name, "old", body)
	require.NoError(t, err)
	require.True(t, fresh)
	hash := sha256.Sum256(body)
	claim, fresh, err := s.ClaimSubscription(d.JobID, hex.EncodeToString(hash[:]), old, now)
	require.NoError(t, err)
	require.True(t, fresh)
	require.NoError(t, s.FinishSubscription(claim.JobID, store.SubscriptionResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{Input: 3660}}, now))
	changed, err := s.TransitionDelivery(d.Key, "claimed", "submitted")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, s.PauseSubscription("operator", now))
	require.NoError(t, s.Close())
	p.Agent.Pi.Limits.DailyJobs, p.Agent.Pi.Limits.DailyTokens = 2, 1603660
	require.NoError(t, config.SavePylon(p))
	command := &cobra.Command{}
	command.Flags().String("home", home, "")
	command.Flags().Int("expect-daily-jobs", 0, "")
	command.Flags().Int64("expect-daily-tokens", 0, "")
	var output bytes.Buffer
	command.SetOut(&output)
	require.ErrorIs(t, runPiWorker(command, []string{"status", p.Name}), store.ErrSubscriptionConflict)
	require.ErrorContains(t, runPiWorker(command, []string{"budget", p.Name}), "expected_limits_required")
	require.NoError(t, command.Flags().Set("expect-daily-jobs", "1"))
	require.ErrorContains(t, runPiWorker(command, []string{"budget", p.Name}), "expected_limits_required")
	require.NoError(t, command.Flags().Set("expect-daily-tokens", "1600000"))
	require.ErrorContains(t, runPiWorker(command, []string{"resume", p.Name}), "expected_limits_required")
	require.Empty(t, output.String())
	require.NoError(t, runPiWorker(command, []string{"budget", p.Name}))
	var status store.SubscriptionStatus
	require.NoError(t, json.Unmarshal(output.Bytes(), &status))
	require.Equal(t, p.Agent.Pi.Limits, status.Limits)
	require.Equal(t, "operator", status.Paused)
	require.Equal(t, 1, status.Records)
	require.Equal(t, 1, status.JobsToday)
	require.EqualValues(t, 3660, status.TokensToday)
	require.Zero(t, status.Unresolved)
	require.ErrorIs(t, runPiWorker(command, []string{"budget", p.Name}), store.ErrSubscriptionConflict)
	_, err = os.Stat(p.Agent.Pi.AuthDir)
	require.True(t, os.IsNotExist(err), "budget must not create OAuth storage")
}
