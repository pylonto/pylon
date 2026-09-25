package runner

// Adopted independent synthetic fixture. Calls the actual piReceiptBytes owner;
// never executes RunPiJob, a model, a role or any OAuth path.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/pylonto/pylon/internal/proxy"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
)

func reviewReceiptKeys(t *testing.T, raw json.RawMessage, required, optional []string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.NotNil(t, fields)
	allowed := map[string]bool{}
	for _, k := range append(append([]string{}, required...), optional...) {
		allowed[k] = true
	}
	for _, k := range required {
		_, ok := fields[k]
		require.True(t, ok, "required key: %s", k)
	}
	for k := range fields {
		require.True(t, allowed[k], "unknown key: %s", k)
	}
	return fields
}

func TestReviewActualReceiptProducerMatchesPinnedContract(t *testing.T) {
	var contract struct {
		Format struct {
			Summary []string       `json:"summary"`
			Export  map[string]any `json:"summary_export"`
			Normal  struct {
				Required, Optional []string
				Subscription       []string `json:"subscription_fields"`
				Usage              []string `json:"usage_fields"`
				Runtime            []string `json:"runtime_fields"`
			} `json:"normal_receipt"`
		} `json:"format"`
	}
	raw, err := os.ReadFile("testdata/pi-debug-contract.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.Len(t, contract.Format.Summary, 17)
	require.Len(t, contract.Format.Normal.Runtime, 10)
	exportKeys := []string{}
	for k := range contract.Format.Export {
		exportKeys = append(exportKeys, k)
	}
	type example struct {
		Name    string          `json:"case"`
		Receipt json.RawMessage `json:"receipt"`
	}
	examples := []example{}
	for _, name := range []string{"disabled_setup_failure", "enabled_setup_failure", "success", "failed_complete", "empty_complete", "capped_diff", "truncated_empty", "runtime_missing_incomplete"} {
		t.Run(name, func(t *testing.T) {
			p := PiParams{Pylon: "fixture", JobID: uuid.NewString(), Base: strings.Repeat("a", 40)}
			usage := &store.SubscriptionUsage{Input: 2, Output: 3}
			out := PiOutcome{Result: store.SubscriptionResult{Outcome: "executor_failed", Usage: usage}, Failure: "pi_patch_empty", Runtime: &proxy.PiResult{Outcome: "executor_returned", Usage: usage, Requests: 2, Tools: 1, Provider: "openai-codex", Model: "gpt-6-astra", Thinking: "max"}}
			switch name {
			case "disabled_setup_failure", "enabled_setup_failure":
				out.Runtime = nil
				out.Result.Usage = &store.SubscriptionUsage{}
				out.Failure = "pi_setup_failed"
			case "runtime_missing_incomplete":
				out.Runtime = nil
				out.Result.Usage = nil
				out.Failure = "pi_runtime_unknown"
			case "failed_complete":
				out.Runtime.Outcome = "executor_failed"
				out.Runtime.Failure = "provider_failed"
				out.Failure = "provider_failed"
			case "capped_diff":
				out.Failure = "pi_patch_output_bound"
			case "success":
				out.Result.Outcome = "executor_returned"
				out.Failure = ""
				out.Patch = filepath.Join(string(os.PathSeparator), "synthetic-evidence", p.JobID+".patch")
				out.PatchSHA256 = strings.Repeat("e", 64)
			}
			if name != "disabled_setup_failure" {
				id := pidebug.Identity{V: 1, Pylon: p.Pylon, Job: p.JobID, Base: p.Base, Image: "sha256:" + strings.Repeat("b", 64), Context: pidebug.Context(p.Pylon, "/synthetic-repo", "/synthetic-auth", "/synthetic-evidence")}
				root := t.TempDir()
				require.NoError(t, os.Chmod(root, 0700))
				r, err := pidebug.Start(root, id, "/synthetic-repo", "/synthetic-auth", "/synthetic-evidence")
				require.NoError(t, err)
				t.Cleanup(func() { r.Close("executor_failed", "pi_fixture_closed", true) })
				if out.Runtime != nil {
					text := "PRIVATE_PRODUCER_CANARY"
					if name == "truncated_empty" {
						text = strings.Repeat("\x00", 3000)
					}
					require.NoError(t, r.Accept(pidebug.Frame{Sequence: 1, Event: &pidebug.Event{Kind: "assistant_text", Text: text}}))
					require.NoError(t, r.Accept(pidebug.Frame{Sequence: 2, Close: &pidebug.Closure{Events: 1}}))
					r.ConfirmRuntimeClosure(true)
					if name != "failed_complete" {
						r.Host("collection", "returned", map[string]int{"allowed": 2, "collected": 1})
						r.Host("staging", "returned", map[string]int{"staged": 1})
						category, n, known := "empty", 0, 1
						if name == "success" {
							category, n = "nonempty", 132
						}
						if name == "capped_diff" {
							category, n, known = "output_bound", MaxPiPatch, 0
						}
						r.Host("diff", category, map[string]int{"bytes": n, "total_known": known})
					}
				}
				summary := r.Close(out.Result.Outcome, out.Failure, name != "runtime_missing_incomplete")
				out.Debug = &summary
				if name == "truncated_empty" {
					require.True(t, summary.Truncated)
					require.Positive(t, summary.DroppedEvents)
					require.False(t, summary.Incomplete)
				}
			}
			raw, err := piReceiptBytes(p, out)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "PRIVATE_PRODUCER_CANARY")
			var decoded piReceipt
			require.NoError(t, pidebug.Decode(raw, &decoded))
			require.Equal(t, out.Debug, decoded.Debug)
			fields := reviewReceiptKeys(t, raw, contract.Format.Normal.Required, contract.Format.Normal.Optional)
			sub := reviewReceiptKeys(t, fields["subscription"], contract.Format.Normal.Subscription, nil)
			if out.Result.Usage == nil {
				require.Equal(t, "null", string(sub["usage"]))
			} else {
				reviewReceiptKeys(t, sub["usage"], contract.Format.Normal.Usage, nil)
			}
			if out.Runtime == nil {
				require.Equal(t, "null", string(fields["runtime"]))
			} else {
				rt := reviewReceiptKeys(t, fields["runtime"], contract.Format.Normal.Runtime, nil)
				reviewReceiptKeys(t, rt["usage"], contract.Format.Normal.Usage, nil)
			}
			if out.Debug == nil {
				_, ok := fields["debug"]
				require.False(t, ok)
			} else {
				debug := reviewReceiptKeys(t, fields["debug"], contract.Format.Summary, nil)
				reviewReceiptKeys(t, debug["export"], exportKeys, nil)
			}
			_, patch := fields["patch"]
			_, digest := fields["patch_sha256"]
			require.Equal(t, name == "success", patch)
			require.Equal(t, patch, digest)
			examples = append(examples, example{name, raw})
		})
	}
	require.Len(t, examples, 8)
	payload := struct {
		Kind  string    `json:"kind"`
		Cases []example `json:"cases"`
	}{"synthetic Go serialization only; not execution/qualification evidence", examples}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_PRODUCER_CANARY")
	require.NotEmpty(t, encoded)
}
