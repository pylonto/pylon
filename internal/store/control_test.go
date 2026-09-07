package store

import (
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestControlNoticeRestartRetainsUnknownAndCannotReuseDifferentBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	require.NoError(t, err)
	n, fresh, err := s.ClaimNotice("key", []byte("bounded synthetic notice"))
	require.NoError(t, err)
	require.True(t, fresh)
	require.Equal(t, "outcome_unknown", n.State)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	n, fresh, err = s.ClaimNotice("key", []byte("bounded synthetic notice"))
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, "outcome_unknown", n.State)
	_, _, err = s.ClaimNotice("key", []byte("different"))
	require.ErrorIs(t, err, ErrDeliveryConflict)
	require.NoError(t, s.Close())
	_, _, err = s.ClaimNotice("new", []byte("unavailable"))
	require.Error(t, err)
}
func TestExecutionClaimCannotBeReplayedAfterRestartOrCallbackCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	require.NoError(t, err)
	d, _, err := s.AcceptDelivery("vendor", "key", []byte(`{}`))
	require.NoError(t, err)
	m := NewMulti(map[string]*Store{"vendor": s})
	keyed, err := m.StartExecution("vendor", d.JobID)
	require.NoError(t, err)
	require.True(t, keyed)
	s.SetCompleted(d.JobID, []byte(`{"claim":"green"}`))
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	m = NewMulti(map[string]*Store{"vendor": s})
	state, err := m.DeliveryStatus("vendor", "key")
	require.NoError(t, err)
	require.Equal(t, "outcome_unknown", state.Execution)
	_, err = m.StartExecution("vendor", d.JobID)
	require.Error(t, err)
	require.NoError(t, m.FinishExecution("vendor", d.JobID, "executor_failed"))
	state, err = m.DeliveryStatus("vendor", "key")
	require.NoError(t, err)
	require.Equal(t, "executor_failed", state.Execution)
}
