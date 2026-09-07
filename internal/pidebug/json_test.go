package pidebug

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummaryRequiresEveryDeclaredFieldAndRejectsNullDefaults(t *testing.T) {
	root, r, id := testStart(t)
	require.NoError(t, r.Accept(Frame{Sequence: 1, Close: &Closure{Events: 0}}))
	r.ConfirmRuntimeClosure(true)
	r.Close("executor_failed", "pi_patch_empty", true)
	control := inspect(t, root, id)
	require.False(t, control.Summary.Incomplete)
	require.Equal(t, "pi_patch_empty", control.Summary.Failure)
	path := filepath.Join(root, id.Job, "summary.json")
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	var template map[string]any
	require.NoError(t, json.Unmarshal(original, &template))
	check := func(field string, nested bool, null bool) {
		t.Helper()
		var value map[string]any
		require.NoError(t, json.Unmarshal(original, &value))
		target := value
		if nested {
			target = value["export"].(map[string]any)
		}
		if null {
			target[field] = nil
		} else {
			delete(target, field)
		}
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
		_, err = Inspect(root, id.Pylon, id.Job, id.Context, "/repo")
		require.ErrorIs(t, err, ErrInvalid)
	}
	for _, null := range []bool{false, true} {
		for field := range template {
			check(field, false, null)
		}
		for field := range template["export"].(map[string]any) {
			check(field, true, null)
		}
	}
	require.NoError(t, os.WriteFile(path, original, 0600)) // #nosec G703 -- Restore this test's own private fixture, not operator data.
	require.Equal(t, control, inspect(t, root, id))
}

func TestStrictDecodeRejectsRepeatedEscapedCaseAliasedAndNullMembers(t *testing.T) {
	for _, raw := range []string{
		`{"sequence":1,"sequence":2,"event":{"kind":"assistant_text","text":"safe"}}`,
		`{"sequence":1,"Sequence":2,"event":{"kind":"assistant_text","text":"safe"}}`,
		`{"sequence":1,"event":{"kind":"tool_start","arguments":{"path":"first","pa\u0074h":"second"}}}`,
		`{"sequence":null,"event":{"kind":"assistant_text"}}`,
		`{"sequence":1,"event":null}`,
		`{"sequence":1,"close":{"events":0,"dropped_events":0,"truncated":null,"incomplete":false}}`,
	} {
		var frame Frame
		require.ErrorIs(t, Decode([]byte(raw), &frame), ErrInvalid)
	}
	var frame Frame
	require.NoError(t, Decode([]byte(`{"sequence":1,"event":{"kind":"assistant_text","text":"Visible control"}}`), &frame))
	require.Equal(t, "Visible control", frame.Event.Text)
}

func TestStrictDecodePreservesRequiredNullableUsageWithoutAdmittingMissingKeys(t *testing.T) {
	type usage struct {
		Input int64 `json:"input"`
	}
	type result struct {
		Usage    *usage `json:"usage"`
		Optional *usage `json:"optional,omitempty"`
	}
	var out result
	require.NoError(t, Decode([]byte(`{"usage":null}`), &out))
	require.Nil(t, out.Usage)
	for _, raw := range []string{`{}`, `{"usage":{"input":null}}`, `{"usage":{},"optional":null}`, `{"usage":null,"optional":null}`} {
		require.Error(t, Decode([]byte(raw), &out))
	}
}
