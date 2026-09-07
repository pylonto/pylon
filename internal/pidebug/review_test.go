package pidebug

// Adopted independent reviewer probes: fixed synthetic input, no real auth/role.
// The exact early-snapshot probes remain in review evidence. These retain their
// assertions while allowing earlier Decode refusal and the newer close-ack fact.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewArgumentsCannotHideForbiddenFieldsBehindDuplicateKeys(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		allowed   bool
	}{
		{"allowed", `{"path":"src/fixture.txt"}`, true},
		{"forbidden", `{"path":{"authorization":"SYNTHETIC_PRIVATE_CANARY"}}`, false},
		{"duplicate_hides_forbidden", `{"path":{"authorization":"SYNTHETIC_PRIVATE_CANARY"},"path":"src/fixture.txt"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, r, id := testStart(t)
			wire := fmt.Sprintf(`{"sequence":1,"event":{"kind":"tool_start","tool":"read","tool_index":1,"arguments":%s}}`, tc.raw)
			var frame Frame
			err := Decode([]byte(wire), &frame)
			if err == nil {
				err = r.Accept(frame)
			}
			if tc.allowed {
				require.NoError(t, err)
				return
			}
			if err == nil {
				raw, readErr := os.ReadFile(filepath.Join(root, id.Job, "events.jsonl"))
				require.NoError(t, readErr)
				t.Logf("forbidden_structure_accepted=true forbidden_canary_persisted=%t", strings.Contains(string(raw), "SYNTHETIC_PRIVATE_CANARY"))
				t.Fatal("forbidden argument structure must be refused before persistence")
			}
		})
	}
}

func TestReviewInspectorRequiresDeclaredSummaryFields(t *testing.T) {
	for _, field := range []string{"failure", "truncated", "incomplete", "export.total_bytes_known"} {
		t.Run(field, func(t *testing.T) {
			root, r, id := testStart(t)
			require.NoError(t, r.Accept(Frame{Sequence: 1, Close: &Closure{Events: 0}}))
			r.ConfirmRuntimeClosure(true)
			r.Close("executor_failed", "pi_patch_empty", true)
			valid := inspect(t, root, id)
			require.False(t, valid.Summary.Incomplete)
			path := filepath.Join(root, id.Job, "summary.json")
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(raw, &fields))
			if strings.HasPrefix(field, "export.") {
				delete(fields["export"].(map[string]any), strings.TrimPrefix(field, "export."))
			} else {
				delete(fields, field)
			}
			raw, err = json.Marshal(fields)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			got, err := Inspect(root, id.Pylon, id.Job, id.Context, "/repo", "/auth", "/evidence")
			if err == nil {
				t.Logf("missing_required_field_accepted=%s incomplete=%t failure_preserved=%t", field, got.Summary.Incomplete, got.Summary.Failure == valid.Summary.Failure)
				t.Fatal("required summary fields cannot silently default")
			}
		})
	}
}

func TestPrivateDropAckChild(t *testing.T) {
	root := os.Getenv("REVIEW_DROP_ROOT")
	if root == "" {
		t.Skip("disposable drop-ack child only")
	}
	id := testIdentity()
	id.Job = os.Getenv("REVIEW_DROP_JOB")
	r, err := Start(root, id, "/repo", "/auth", "/evidence")
	require.NoError(t, err)
	for i := 1; i <= MaxEvents; i++ {
		require.NoError(t, r.Accept(Frame{Sequence: i, Event: &Event{Kind: "assistant_text", Text: "bounded fixture event"}}))
	}
	// The last event exceeds the count cap: Accept returned success. Parent kills
	// this isolated test process immediately after acknowledgement, before Close.
	_, err = fmt.Fprintln(os.Stdout, "ACK")
	require.NoError(t, err)
	select {}
}

func TestReviewAcknowledgedDropMarkerSurvivesProcessTermination(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	id := testIdentity()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivateDropAckChild$", "-test.timeout=5s")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GOMAXPROCS=2", "REVIEW_DROP_ROOT=" + root, "REVIEW_DROP_JOB=" + id.Job}
	pipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(pipe).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ACK\n", line)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NoError(t, ctx.Err())
	got := inspect(t, root, id)
	require.Len(t, got.Events, MaxEvents, "prove the child actually filled the cap before termination")
	require.True(t, got.Summary.Incomplete, "termination without runtime/executor closure remains incomplete")
	t.Logf("acknowledged_cap_drop_after_kill: truncated=%t dropped_events=%d retained_events=%d", got.Summary.Truncated, got.Summary.DroppedEvents, len(got.Events))
	require.True(t, got.Summary.Truncated, "acknowledged truncation must be durable, not only in dead process memory")
	require.Positive(t, got.Summary.DroppedEvents)
}
