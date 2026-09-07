package pidebug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testIdentity() Identity {
	return Identity{1, "fixture", uuid.NewString(), strings.Repeat("a", 40), "sha256:" + strings.Repeat("b", 64), Context("fixture", "/repo", "/auth", "/evidence")}
}
func testStart(t *testing.T) (string, *Recorder, Identity) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	id := testIdentity()
	r, err := Start(root, id, "/repo", "/auth", "/evidence")
	require.NoError(t, err)
	t.Cleanup(func() { r.Close("executor_failed", "pi_test_closed", true) })
	return root, r, id
}
func inspect(t *testing.T, root string, id Identity) Inspection {
	t.Helper()
	got, err := Inspect(root, id.Pylon, id.Job, id.Context, "/repo", "/auth", "/evidence")
	require.NoError(t, err)
	return got
}

func TestContextGolden(t *testing.T) {
	var cases []struct{ Name, Repo, Auth, Evidence, Encoded, SHA256 string }
	raw, err := os.ReadFile("testdata/context.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.Len(t, cases, 2)
	for _, c := range cases {
		encoded, err := json.Marshal([]string{c.Name, filepath.Clean(c.Repo), filepath.Clean(c.Auth), filepath.Clean(c.Evidence)})
		require.NoError(t, err)
		require.Equal(t, c.Encoded, string(encoded))
		require.Equal(t, c.SHA256, Context(c.Name, c.Repo, c.Auth, c.Evidence))
	}
}

func TestFixedLimitsFixtureMatchesTheProtocolOwner(t *testing.T) {
	raw, err := os.ReadFile("../../agent/pi/debug-limits.fixture.json")
	require.NoError(t, err)
	var fixture Limits
	require.NoError(t, Decode(raw, &fixture))
	require.Equal(t, FixedLimits(), fixture)
}

func TestCapturePersistsAcknowledgedContentAndClosedSummary(t *testing.T) {
	root, r, id := testStart(t)
	initial := inspect(t, root, id)
	require.True(t, initial.Summary.Incomplete)
	require.Len(t, initial.Events, 1)
	require.NoError(t, r.Accept(Frame{Sequence: 1, Event: &Event{Kind: "assistant_text", Text: "Visible fixture observation"}}))
	acknowledged := inspect(t, root, id)
	require.False(t, acknowledged.Inspection.SummaryStale)
	require.Equal(t, "Visible fixture observation", acknowledged.Events[1].Text)
	require.NoError(t, r.Accept(Frame{Sequence: 2, Close: &Closure{Events: 1}}))
	r.ConfirmRuntimeClosure(true)
	r.Host("collection", "returned", map[string]int{"allowed": 2, "collected": 1})
	r.Host("staging", "returned", map[string]int{"staged": 1})
	r.Host("diff", "nonempty", map[string]int{"bytes": 123, "total_known": 1})
	summary := r.Close("executor_returned", "", true)
	require.False(t, summary.Incomplete)
	got := inspect(t, root, id)
	require.Equal(t, summary, got.Summary)
	require.True(t, got.Private)
	require.True(t, got.Untrusted)
	require.False(t, got.Publishable)
	require.Equal(t, "nonempty", got.Summary.Export.Diff)
	for _, name := range []string{"summary.json", "events.jsonl"} {
		info, err := os.Stat(filepath.Join(root, id.Job, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestCaptureRequiresRuntimeObservedCloseAcknowledgement(t *testing.T) {
	root, r, id := testStart(t)
	require.NoError(t, r.Accept(Frame{Sequence: 1, Event: &Event{Kind: "assistant_text", Text: "Acknowledged fixture text"}}))
	// The sink accepts the close frame, but its response is lost. No subsequent
	// runtime confirmation arrives; host termination cannot fill in that fact.
	require.NoError(t, r.Accept(Frame{Sequence: 2, Close: &Closure{Events: 1}}))
	r.Close("executor_failed", "pi_execution_failed", true)
	require.True(t, inspect(t, root, id).Summary.Incomplete)
}

func TestCaptureIsIncompleteWithoutRuntimeClosureOrTermination(t *testing.T) {
	for _, kind := range []string{"missing_close", "sequence_gap", "runtime_incomplete", "unknown_termination"} {
		t.Run(kind, func(t *testing.T) {
			root, r, id := testStart(t)
			require.NoError(t, r.Accept(Frame{Sequence: 1, Event: &Event{Kind: "assistant_text", Text: "Acknowledged before interruption"}}))
			switch kind {
			case "sequence_gap":
				require.Error(t, r.Accept(Frame{Sequence: 3, Close: &Closure{Events: 1}}))
			case "runtime_incomplete":
				require.Error(t, r.Accept(Frame{Sequence: 2, Close: &Closure{Events: 1, Incomplete: true}}))
			case "unknown_termination":
				require.NoError(t, r.Accept(Frame{Sequence: 2, Close: &Closure{Events: 1}}))
				r.ConfirmRuntimeClosure(true)
			}
			r.Close("executor_failed", "pi_execution_failed", kind != "unknown_termination")
			got := inspect(t, root, id)
			require.True(t, got.Summary.Incomplete)
			require.Equal(t, "Acknowledged before interruption", got.Events[1].Text)
		})
	}
}

func TestCaptureRefusesUnsafeStorageWithoutAdoption(t *testing.T) {
	for _, kind := range []string{"public_root", "symlink_root", "writable_ancestor", "existing_job", "overlap_parent", "overlap_child", "symlink_forbidden"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "debug")
			require.NoError(t, os.Mkdir(root, 0700))
			id := testIdentity()
			forbidden := []string{"/repo", "/auth", "/evidence"}
			switch kind {
			case "public_root":
				require.NoError(t, os.Chmod(root, 0755))
			case "symlink_root":
				actual := filepath.Join(base, "actual")
				require.NoError(t, os.Rename(root, actual))
				require.NoError(t, os.Symlink(actual, root))
			case "writable_ancestor":
				require.NoError(t, os.Chmod(base, 0777))
			case "existing_job":
				require.NoError(t, os.Mkdir(filepath.Join(root, id.Job), 0700))
			case "overlap_parent":
				forbidden = []string{filepath.Join(root, "repo")}
			case "overlap_child":
				forbidden = []string{base}
			case "symlink_forbidden":
				alias := filepath.Join(base, "repo")
				require.NoError(t, os.Symlink(root, alias))
				forbidden = []string{alias}
			}
			_, err := Start(root, id, forbidden...)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(root, id.Job, "events.jsonl"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestCaptureRejectsChangedInodeAfterValidation(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "debug")
	require.NoError(t, os.Mkdir(path, 0700))
	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Rename(path, path+"-old"))
	require.NoError(t, os.Mkdir(path, 0700))
	_, err = openMatchingDirectory(path, before)
	require.ErrorIs(t, err, ErrUnsafe)
}

func TestCaptureBoundedCapacityIncludesPartialJobsAndSerializesLastSlot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	for range MaxJobs - 1 {
		require.NoError(t, os.Mkdir(filepath.Join(root, uuid.NewString()), 0700))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *Recorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; r, _ := Start(root, testIdentity(), "/repo"); results <- r }()
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for r := range results {
		if r != nil {
			accepted++
			r.Close("executor_failed", "pi_fixture", true)
		}
	}
	require.Equal(t, 1, accepted)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, MaxJobs)
	_, err = Start(root, testIdentity(), "/repo")
	require.ErrorIs(t, err, ErrFull)
}

func TestCaptureBudgetsKeepTerminalMetadata(t *testing.T) {
	for _, kind := range []string{"count", "bytes", "encoded_event"} {
		t.Run(kind, func(t *testing.T) {
			root, r, id := testStart(t)
			n := MaxEvents
			text := "fixture"
			if kind == "bytes" {
				text = strings.Repeat("x", 15000)
			}
			if kind == "encoded_event" {
				text = strings.Repeat("\x00", 3000)
				n = 1
			}
			for i := 1; i <= n; i++ {
				require.NoError(t, r.Accept(Frame{Sequence: i, Event: &Event{Kind: "assistant_text", Text: text}}))
			}
			require.NoError(t, r.Accept(Frame{Sequence: n + 1, Close: &Closure{Events: n}}))
			r.ConfirmRuntimeClosure(true)
			r.Host("diff", "output_bound", map[string]int{"bytes": 123, "total_known": 0})
			r.Close("executor_failed", "pi_patch_output_bound", true)
			got := inspect(t, root, id)
			require.True(t, got.Summary.Truncated)
			require.Positive(t, got.Summary.DroppedEvents)
			require.False(t, got.Summary.Incomplete)
			require.Equal(t, "output_bound", got.Summary.Export.Diff)
			require.False(t, got.Summary.Export.TotalBytesKnown)
			require.LessOrEqual(t, got.Summary.Bytes, MaxEventsBytes)
			require.LessOrEqual(t, got.Summary.EventCount, MaxEvents)
			entries, err := os.ReadDir(filepath.Join(root, id.Job))
			require.NoError(t, err)
			total := int64(0)
			for _, e := range entries {
				info, err := e.Info()
				require.NoError(t, err)
				total += info.Size()
			}
			require.LessOrEqual(t, total, int64(MaxJobBytes))
		})
	}
}

func TestInspectPreservesAndMarksPartialState(t *testing.T) {
	for _, kind := range []string{"missing_summary", "stale_summary", "partial_tail", "rename_failure", "write_failure"} {
		t.Run(kind, func(t *testing.T) {
			root, r, id := testStart(t)
			path := filepath.Join(root, id.Job, "summary.json")
			initial, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, r.Accept(Frame{Sequence: 1, Event: &Event{Kind: "assistant_text", Text: "Retained private text"}}))
			switch kind {
			case "missing_summary":
				require.NoError(t, os.Remove(path))
			case "stale_summary":
				require.NoError(t, os.WriteFile(path, initial, 0600)) // #nosec G703 -- Deliberately restore stale data inside this private test fixture.
			case "partial_tail":
				_, err = r.file.WriteString(`{"seq":3`)
				require.NoError(t, err)
				require.NoError(t, r.file.Sync())
			case "rename_failure":
				require.NoError(t, os.WriteFile(filepath.Join(root, id.Job, ".summary-next"), []byte("partial"), 0600))
				require.Error(t, r.Accept(Frame{Sequence: 2, Close: &Closure{Events: 1}}))
			case "write_failure":
				require.NoError(t, r.file.Close())
				require.Error(t, r.Accept(Frame{Sequence: 2, Event: &Event{Kind: "assistant_text", Text: "not stored"}}))
			}
			before, err := os.ReadFile(filepath.Join(root, id.Job, "events.jsonl"))
			require.NoError(t, err)
			got := inspect(t, root, id)
			require.True(t, got.Summary.Incomplete)
			require.Equal(t, "Retained private text", got.Events[1].Text)
			again := inspect(t, root, id)
			require.Equal(t, got, again)
			after, err := os.ReadFile(filepath.Join(root, id.Job, "events.jsonl"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestInspectRefusesUnsafeOrForgedSelectedCapture(t *testing.T) {
	for _, kind := range []string{"fifo", "hardlink", "symlink", "oversized", "malformed", "foreign_name", "foreign_context", "unknown_field"} {
		t.Run(kind, func(t *testing.T) {
			root, r, id := testStart(t)
			r.Close("executor_failed", "pi_fixture", true)
			path := filepath.Join(root, id.Job, "summary.json")
			switch kind {
			case "fifo":
				require.NoError(t, os.Remove(path))
				require.NoError(t, syscall.Mkfifo(path, 0600))
			case "hardlink":
				require.NoError(t, os.Link(path, path+"-link"))
			case "symlink":
				require.NoError(t, os.Rename(path, path+"-other"))
				require.NoError(t, os.Symlink(path+"-other", path))
			case "oversized":
				require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", MaxSummaryBytes+1)), 0600))
			case "malformed":
				require.NoError(t, os.WriteFile(path, []byte("{"), 0600))
			case "foreign_name":
				id.Pylon = "other"
			case "foreign_context":
				id.Context = strings.Repeat("c", 64)
			case "unknown_field":
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				bad := strings.Replace(string(raw), `"v":1`, `"v":1,"text":"NEVER_PUBLIC"`, 1)
				require.NoError(t, os.WriteFile(path, []byte(bad), 0600)) // #nosec G703 -- Deliberately forge only this private test fixture's summary.
			}
			_, err := Inspect(root, id.Pylon, id.Job, id.Context, "/repo")
			require.Error(t, err)
			require.NotContains(t, fmt.Sprint(err), "NEVER_PUBLIC")
		})
	}
}

func TestRuntimeCannotForgeHostFactsOrUnknownMetadata(t *testing.T) {
	for _, event := range []Event{{Kind: "diff", Outcome: "nonempty"}, {Kind: "capture_start"}, {Kind: "lifecycle", Outcome: "private prose"}, {Kind: "tool_end", ErrorCategory: "private prose"}, {Kind: "tool_start", Tool: "env"}, {Kind: "lifecycle", Counts: map[string]int{"oauth": 1}}} {
		_, r, _ := testStart(t)
		require.Error(t, r.Accept(Frame{Sequence: 1, Event: &event}))
	}
	for _, raw := range []string{`{"sequence":1,"source":"host"}`, `{"sequence":1,"event":{"kind":"assistant_text","headers":{}}}`, strings.Repeat("[", 13) + strings.Repeat("]", 13)} {
		var f Frame
		require.Error(t, Decode([]byte(raw), &f))
	}
}
