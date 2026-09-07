package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDurableDeliverySurvivesRestartAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path)
	require.NoError(t, err)
	a, fresh, err := s.AcceptDelivery("watch", "key", []byte(`{"version":"1"}`))
	require.NoError(t, err)
	require.True(t, fresh)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	b, fresh, err := s.AcceptDelivery("watch", "key", []byte(`{"version":"1"}`))
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, a.JobID, b.JobID)
	_, _, err = s.AcceptDelivery("watch", "key", []byte(`{"version":"2"}`))
	require.ErrorIs(t, err, ErrDeliveryConflict)
	pending, err := s.PendingDeliveries()
	require.NoError(t, err)
	require.Len(t, pending, 1)
	ok, err := s.TransitionDelivery(a.Key, "queued", "claimed")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.TransitionDelivery(a.Key, "queued", "claimed")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	pending, err = s.PendingDeliveries()
	require.NoError(t, err)
	require.Empty(t, pending, "a claim interrupted by a crash must never spawn another agent automatically")
}

func TestDismissedDeliveryCannotBeClaimed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	defer s.Close()
	d, _, err := s.AcceptDelivery("watch", "key", []byte(`{}`))
	require.NoError(t, err)
	job, ok := s.Get(d.JobID)
	require.True(t, ok)
	require.Equal(t, "queued", job.Status)
	s.UpdateStatus(d.JobID, "dismissed")
	pending, err := s.PendingDeliveries()
	require.NoError(t, err)
	require.Empty(t, pending)
	claimed, err := s.TransitionDelivery(d.Key, "queued", "claimed")
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestDeliveryBoundsAreEnforcedAtTheOwner(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	defer s.Close()
	_, _, err = s.AcceptDelivery("watch", "large", make([]byte, MaxDeliveryBytes+1))
	require.ErrorIs(t, err, ErrDeliveryShape)
	_, err = s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
 INSERT INTO deliveries(delivery_key,job_id,pylon_name,body) SELECT 'key-'||i,'job-'||i,'watch','{}' FROM n`, MaxDeliveries)
	require.NoError(t, err)
	_, _, err = s.AcceptDelivery("watch", "new-key", []byte(`{}`))
	require.ErrorIs(t, err, ErrDeliveryCapacity)
}

func TestDeliveryStorageFailureCannotAcknowledge(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, _, err = s.AcceptDelivery("watch", "key", []byte(`{}`))
	require.Error(t, err)
}
