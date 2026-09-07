package runner

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiDockerSDKFailureReachesModelContextAndRecovers(t *testing.T) {
	for _, fixture := range []string{"git_failure", "test_failure"} {
		for _, capture := range []bool{false, true} {
			label := "debug_off"
			if capture {
				label = "debug_on"
			}
			t.Run(fixture+"/"+label, func(t *testing.T) {
				cli, p := piDebugNative(t, "sdk_session_"+fixture)
				debugRoot := p.Config.DebugDir
				if !capture {
					p.Config.DebugDir = ""
				}
				out := RunPiJob(context.Background(), p)
				piNativeRemoved(t, cli, p)
				require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
				require.True(t, out.Runtime.Fixture)
				require.Zero(t, out.Runtime.Requests, "HTTP is fail-closed synthetic fixture input, not real model use")
				require.GreaterOrEqual(t, out.Runtime.Tools, 1, "the first actual SDK tool must execute before a model-context assertion")
				if capture {
					got := piAssertPrivateOnly(t, p, out)
					details := piNativeEvent(got, "tool_error")
					require.Len(t, details, 1)
					if fixture == "git_failure" {
						require.Contains(t, details[0].Error, "Before Git check")
						require.Contains(t, details[0].Error, "fatal: not a git repository")
						require.Contains(t, details[0].Error, "Command exited with code 128")
					} else {
						require.Contains(t, details[0].Error, "Before negative test")
						require.Contains(t, details[0].Error, "not ok 1 - requires repaired text")
						require.Contains(t, details[0].Error, "Command exited with code 1")
					}
					require.NotContains(t, details[0].Error, "UNREACHED_AFTER_FAILURE")
				} else {
					require.Nil(t, out.Debug)
					entries, err := os.ReadDir(debugRoot)
					require.NoError(t, err)
					require.Empty(t, entries, "capture disabled means no debug storage")
				}
				// fixture.mjs checks both the actual SDK toolResult and the next
				// Codex adapter input before providing the repair response. The old
				// generic collapse fails there even though private detail is present.
				require.Equal(t, "executor_returned", out.Runtime.Outcome, "model-context fence refused: %s", out.Runtime.Failure)
				require.Equal(t, 2, out.Runtime.Tools, "failed command then successful SDK repair")
				require.Equal(t, "executor_returned", out.Result.Outcome, "failure=%s", out.Failure)
				patch, err := os.ReadFile(out.Patch)
				require.NoError(t, err)
				require.Contains(t, string(patch), "+repaired")
				require.NotContains(t, string(patch), "Before ")
			})
		}
	}
}

func TestPiDockerSDKPromptExplainsPlainFilesAndControllerExport(t *testing.T) {
	for _, capture := range []bool{false, true} {
		label := "debug_off"
		if capture {
			label = "debug_on"
		}
		t.Run(label, func(t *testing.T) {
			cli, p := piDebugNative(t, "sdk_session_workspace_prompt")
			if !capture {
				p.Config.DebugDir = ""
			}
			out := RunPiJob(context.Background(), p)
			piNativeRemoved(t, cli, p)
			require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
			require.Equal(t, "executor_returned", out.Runtime.Outcome, "actual adapter instructions must describe plain files, no .git, and controller export")
			require.Equal(t, 1, out.Runtime.Tools)
			require.Zero(t, out.Runtime.Requests)
			require.Equal(t, "pi_patch_empty", out.Failure, "read-only control must not invent a patch")
		})
	}
}
