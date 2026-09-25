package runner

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPiDockerThinkingConfigReachesActualSessionSDKAndReceipt(t *testing.T) {
	for _, selection := range []struct {
		name, value, expected string
		debug                 bool
	}{
		{"default_max", "", "max", false}, {"explicit_max", "max", "max", false},
		{"medium", "medium", "medium", false}, {"medium_debug", "medium", "medium", true},
	} {
		t.Run(selection.name, func(t *testing.T) {
			cli, p := piDebugNative(t, "sdk_session_test_failure")
			if !selection.debug {
				p.Config.DebugDir = ""
			}
			raw, err := yaml.Marshal(p.Config)
			require.NoError(t, err)
			if selection.value != "" {
				raw = append(raw, []byte("thinking: "+selection.value+"\n")...)
			}
			var decoded config.PiConfig
			require.NoError(t, yaml.Unmarshal(raw, &decoded))
			require.NoError(t, decoded.Validate())
			p.Config = decoded
			// A signed brief may contain data resembling a setting; it owns no effort.
			var brief map[string]any
			require.NoError(t, json.Unmarshal(p.Brief, &brief))
			brief["thinking"] = "not_authority"
			p.Brief, err = json.Marshal(brief)
			require.NoError(t, err)
			out := RunPiJob(context.Background(), p)
			piNativeRemoved(t, cli, p)
			require.NotNil(t, out.Runtime, "failure=%s", out.Failure)
			require.Equal(t, selection.expected, out.Runtime.Thinking)
			require.Equal(t, "executor_returned", out.Runtime.Outcome, "actual encoded effort/model-context fence: %s", out.Runtime.Failure)
			require.Equal(t, "executor_returned", out.Result.Outcome, "failure=%s", out.Failure)
			require.True(t, out.Runtime.Fixture)
			require.Zero(t, out.Runtime.Requests)
			require.Equal(t, 2, out.Runtime.Tools, "actual failed test followed by repair and passing retest")
			patch, err := os.ReadFile(out.Patch)
			require.NoError(t, err)
			require.Contains(t, string(patch), "+repaired")
			if selection.debug {
				piAssertPrivateOnly(t, p, out)
			} else {
				require.Nil(t, out.Debug)
			}
			t.Logf("THINKING_MAPPING model=%s effort=%s fixture_http_requests=3 live_requests=0", out.Runtime.Model, out.Runtime.Thinking)
		})
	}
}
