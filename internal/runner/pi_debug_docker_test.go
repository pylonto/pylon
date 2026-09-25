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
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/stretchr/testify/require"
)

func piDebugNative(t *testing.T, fixture string) (*client.Client, PiParams) {
	t.Helper()
	cli, p := piNative(t)
	p.Pylon = "fixture"
	p.FixtureCase = fixture
	p.Config.DebugDir = t.TempDir()
	require.NoError(t, os.Chmod(p.Config.DebugDir, 0700))
	p.Config.Limits.JobTokens = 1600000
	p.Config.Limits.DailyTokens = 1600000
	return cli, p
}
func piInspectNative(p PiParams) (pidebug.Inspection, error) {
	return pidebug.Inspect(p.Config.DebugDir, p.Pylon, p.JobID, pidebug.Context(p.Pylon, p.Repository, p.Config.AuthDir, p.PatchRoot), p.Repository, p.Config.AuthDir, p.PatchRoot)
}
func piNativeEvent(got pidebug.Inspection, kind string) []pidebug.Record {
	var rows []pidebug.Record
	for i := range got.Events {
		if got.Events[i].Kind == kind {
			rows = append(rows, got.Events[i])
		}
	}
	return rows
}
func piAssertPrivateOnly(t *testing.T, p PiParams, out PiOutcome) pidebug.Inspection {
	t.Helper()
	got, err := piInspectNative(p)
	require.NoError(t, err)
	require.Equal(t, *out.Debug, got.Summary)
	require.True(t, got.Private)
	require.True(t, got.Untrusted)
	require.False(t, got.Publishable)
	raw, err := os.ReadFile(out.Receipt)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "Visible synthetic")
	require.NotContains(t, string(raw), "Permitted tool")
	require.NotContains(t, string(raw), "\"events\"")
	var normal piReceipt
	require.NoError(t, pidebug.Decode(raw, &normal))
	require.Equal(t, out.Debug, normal.Debug)
	return got
}

func TestPiDockerDebugSyntheticPromptToolMatrix(t *testing.T) {
	for _, mode := range []string{"session", "agent"} {
		for _, fixture := range []string{"read", "edit", "write", "bash", "read_missing", "edit_mismatch", "edit_array", "edit_array_mismatch"} {
			t.Run(mode+"_"+fixture, func(t *testing.T) {
				cli, p := piDebugNative(t, "sdk_"+mode+"_"+fixture)
				out := RunPiJob(context.Background(), p)
				piNativeRemoved(t, cli, p)
				require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
				require.Equal(t, "executor_returned", out.Runtime.Outcome, "failure=%s", out.Runtime.Failure)
				require.True(t, out.Runtime.Fixture)
				require.Zero(t, out.Runtime.Requests, "only synthetic adapter HTTP, not provider requests")
				require.Equal(t, 1, out.Runtime.Tools)
				require.NotNil(t, out.Debug)
				got := piAssertPrivateOnly(t, p, out)
				require.False(t, got.Summary.Incomplete)
				require.False(t, got.Summary.Truncated)
				require.Zero(t, got.Summary.DroppedEvents)
				visible := piNativeEvent(got, "assistant_text")
				require.Len(t, visible, 2)
				require.Contains(t, visible[1].Text, "Visible synthetic completion")
				entry := false
				for _, r := range piNativeEvent(got, "lifecycle") {
					entry = entry || r.Outcome == "prompt_"+mode
				}
				require.True(t, entry, "the selected public prompt API must execute")
				starts, ends := piNativeEvent(got, "tool_start"), piNativeEvent(got, "tool_end")
				require.Len(t, starts, 1)
				require.Len(t, ends, 1)
				tool := strings.Split(fixture, "_")[0]
				require.Equal(t, tool, starts[0].Tool)
				require.Equal(t, tool, ends[0].Tool)
				require.Equal(t, starts[0].ToolIndex, ends[0].ToolIndex)
				require.NotEmpty(t, starts[0].Arguments)
				failed := strings.Contains(fixture, "missing") || strings.Contains(fixture, "mismatch")
				if failed {
					require.Equal(t, "failed", ends[0].Outcome)
					details := piNativeEvent(got, "tool_error")
					require.Len(t, details, 1)
					require.NotEmpty(t, details[0].Error)
					if tool == "read" {
						require.Equal(t, "ENOENT", details[0].ErrorCategory)
						require.Contains(t, details[0].Error, "missing.txt")
					} else {
						require.Contains(t, string(starts[0].Arguments), "not_present")
						require.Contains(t, details[0].Error, "Could not find the exact text")
					}
				} else {
					require.Equal(t, "returned", ends[0].Outcome)
					require.NotEmpty(t, ends[0].Result)
				}
				if strings.Contains(fixture, "array") {
					require.Contains(t, string(starts[0].Arguments), "\"edits\"")
				}
				mutation := !failed && fixture != "read"
				require.Equal(t, "returned", got.Summary.Export.Collection)
				require.Equal(t, "returned", got.Summary.Export.Staging)
				require.True(t, got.Summary.Export.TotalBytesKnown)
				if mutation {
					require.Equal(t, "executor_returned", out.Result.Outcome, "failure=%s", out.Failure)
					require.Equal(t, "nonempty", got.Summary.Export.Diff)
					require.Positive(t, got.Summary.Export.ObservedBytes)
					patch, err := os.ReadFile(out.Patch)
					require.NoError(t, err)
					require.Contains(t, string(patch), "+repaired")
					require.NotContains(t, string(patch), "Visible synthetic")
				} else {
					require.Equal(t, "pi_patch_empty", out.Failure)
					require.Empty(t, out.Patch)
					require.Equal(t, "empty", got.Summary.Export.Diff)
					require.Zero(t, got.Summary.Export.ObservedBytes)
				}
			})
		}
	}
}

func TestPiDockerDebugRecoverableToolErrorDoesNotPreventRepair(t *testing.T) {
	cli, p := piDebugNative(t, "sdk_session_recover")
	out := RunPiJob(context.Background(), p)
	piNativeRemoved(t, cli, p)
	require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
	require.Equal(t, "executor_returned", out.Result.Outcome, "runtime failure=%s", out.Runtime.Failure)
	require.Equal(t, 2, out.Runtime.Tools)
	got := piAssertPrivateOnly(t, p, out)
	ends := piNativeEvent(got, "tool_end")
	require.Len(t, ends, 2)
	require.Equal(t, "failed", ends[0].Outcome)
	require.Equal(t, "returned", ends[1].Outcome)
	require.Len(t, piNativeEvent(got, "tool_error"), 1)
	require.False(t, got.Summary.Incomplete)
	require.Equal(t, "nonempty", got.Summary.Export.Diff)
}

func TestPiDockerDebugPrivacyUsesActualSDKAndRefreshedSyntheticAuth(t *testing.T) {
	cli, p := piDebugNative(t, "sdk_session_privacy")
	out := RunPiJob(context.Background(), p)
	piNativeRemoved(t, cli, p)
	require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
	require.Equal(t, "executor_returned", out.Runtime.Outcome, "failure=%s", out.Runtime.Failure)
	require.Equal(t, 2, out.Runtime.Tools)
	require.Equal(t, "pi_patch_empty", out.Failure)
	require.Empty(t, out.Patch)
	got := piAssertPrivateOnly(t, p, out)
	require.False(t, got.Summary.Incomplete)
	require.False(t, got.Summary.Truncated)
	visible := piNativeEvent(got, "assistant_text")
	require.Len(t, visible, 2)
	for _, r := range visible {
		require.Contains(t, r.Text, "Visible permitted text")
		require.True(t, r.Redacted)
	}
	ends := piNativeEvent(got, "tool_end")
	require.Len(t, ends, 2)
	require.Contains(t, ends[0].Result, "Permitted tool result")
	require.True(t, ends[0].Redacted)
	detail := piNativeEvent(got, "tool_error")
	require.Len(t, detail, 1)
	require.Contains(t, detail[0].Error, "Permitted tool error")
	require.True(t, detail[0].Redacted)
	for _, root := range []string{filepath.Join(p.Config.DebugDir, p.JobID), p.PatchRoot} {
		files, err := os.ReadDir(root)
		require.NoError(t, err)
		for _, f := range files {
			raw, err := os.ReadFile(filepath.Join(root, f.Name()))
			require.NoError(t, err)
			for _, secret := range []string{"CANARY_", "fixture.", "fixture-only"} {
				require.NotContains(t, string(raw), secret, "synthetic credential/reasoning/envelope content must never persist")
			}
		}
	}
}

// A later runtime event proves the one-at-a-time pump observed acknowledgement
// of the preceding known text. Poll a state predicate, never sleep to guess order.
func piAwaitAcknowledgedText(t *testing.T, p PiParams, done <-chan PiOutcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case out := <-done:
			t.Fatalf("fixture ended before acknowledged-text precondition: %s", out.Failure)
		case <-ctx.Done():
			t.Fatal("known text and later runtime event were not observed")
		case <-tick.C:
			got, err := piInspectNative(p)
			if err != nil || got.Inspection.SummaryStale {
				continue
			}
			text, tool := piNativeEvent(got, "assistant_text"), piNativeEvent(got, "tool_start")
			if len(text) > 0 && len(tool) > 0 && strings.Contains(text[0].Text, "Visible synthetic tool inspection") && tool[0].Seq > text[0].Seq {
				return
			}
		}
	}
}

func TestPiDockerDebugAcknowledgedTextSurvivesCancellationAndDeadline(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			cli, p := piDebugNative(t, "sdk_session_hang")
			if kind == "deadline" {
				p.Deadline = time.Now().Add(15 * time.Second)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan PiOutcome, 1)
			go func() { done <- RunPiJob(ctx, p) }()
			piAwaitAcknowledgedText(t, p, done)
			if kind == "cancel" {
				cancel()
			}
			select {
			case out := <-done:
				piNativeRemoved(t, cli, p)
				require.Equal(t, "executor_failed", out.Result.Outcome)
				require.Nil(t, out.Result.Usage)
				require.NotNil(t, out.Debug)
				got := piAssertPrivateOnly(t, p, out)
				require.True(t, got.Summary.Incomplete)
				require.False(t, got.Summary.RuntimeClosed)
				require.True(t, got.Summary.ExecutorClosed)
				require.NotEmpty(t, piNativeEvent(got, "assistant_text"))
			case <-time.After(20 * time.Second):
				t.Fatal("fixture failed to terminate within the original deadline reserve")
			}
		})
	}
}

func TestPiDockerDebugHostKillChild(t *testing.T) {
	path := os.Getenv("PYLON_PI_DEBUG_CHILD_PARAMS")
	if path == "" {
		t.Skip("only the disposable host-kill parent invokes this child")
	}
	raw, err := os.ReadFile(path) // #nosec G703 -- Only the exact disposable test child reads its parent's synthetic parameter file.
	require.NoError(t, err)
	require.Less(t, len(raw), 16384)
	var p PiParams
	require.NoError(t, json.Unmarshal(raw, &p))
	require.True(t, p.Fixture)
	require.Equal(t, "sdk_session_hang", p.FixtureCase)
	_ = RunPiJob(context.Background(), p)
	t.Fatal("parent must kill this disposable fixture host after acknowledged text")
}

func TestPiDockerDebugAcknowledgedCaptureSurvivesHostKill(t *testing.T) {
	cli, p := piDebugNative(t, "sdk_session_hang")
	p.Deadline = time.Now().Add(45 * time.Second)
	// Keep the Unix socket path short and make every abandoned temp file owned by
	// this test. Teardown below names only this random fixture UUID's resources.
	tmp, err := os.MkdirTemp("", "pd-kill-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(tmp)) })
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	params := filepath.Join(tmp, "params.json")
	require.NoError(t, os.WriteFile(params, raw, 0600))
	command := exec.Command(os.Args[0], "-test.run=^TestPiDockerDebugHostKillChild$", "-test.v")
	command.Env = append(os.Environ(), "PYLON_PI_DEBUG_CHILD_PARAMS="+params, "TMPDIR="+tmp)
	output := &piBuffer{max: 16384}
	command.Stdout = output
	command.Stderr = output
	require.NoError(t, command.Start())
	joined := false
	t.Cleanup(func() {
		if !joined {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	cleaned := false
	cleanupOwned := func() {
		if cleaned {
			return
		}
		cleaned = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, role := range []string{"runtime", "tools", "export"} {
			name := "pylon-pi-" + p.JobID + "-" + role
			info, err := cli.ContainerInspect(ctx, name)
			if errdefs.IsNotFound(err) {
				continue
			}
			require.NoError(t, err)
			require.Equal(t, p.JobID, info.Config.Labels["pylon.pi.job"])
			require.NoError(t, cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: true}))
		}
		err := cli.VolumeRemove(ctx, "pylon-pi-"+p.JobID, false)
		require.True(t, err == nil || errdefs.IsNotFound(err))
		piNativeRemoved(t, cli, p)
	}
	t.Cleanup(cleanupOwned)
	piAwaitAcknowledgedText(t, p, nil)
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	joined = true
	cleanupOwned()
	got, err := piInspectNative(p)
	require.NoError(t, err)
	require.True(t, got.Summary.Incomplete)
	require.False(t, got.Summary.RuntimeClosed)
	require.False(t, got.Summary.ExecutorClosed)
	require.NotEmpty(t, piNativeEvent(got, "assistant_text"))
	require.NotContains(t, output.String(), "Visible synthetic")
	require.NotContains(t, output.String(), "CANARY_")
}
