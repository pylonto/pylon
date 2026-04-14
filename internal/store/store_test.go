package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func makeJob(id, pylonName, status string) *Job {
	return &Job{
		ID:        id,
		PylonName: pylonName,
		Status:    status,
		CreatedAt: time.Now(),
	}
}

func TestOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	require.NoError(t, err)
	assert.NoError(t, s.Close())
}

func TestPutAndGet(t *testing.T) {
	s := openTestStore(t)

	job := makeJob("job-1", "pylon-a", "pending")
	job.TopicID = "topic-1"
	job.Body = map[string]interface{}{"key": "val"}
	s.Put(job)

	got, ok := s.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "job-1", got.ID)
	assert.Equal(t, "pylon-a", got.PylonName)
	assert.Equal(t, "pending", got.Status)
	assert.Equal(t, "topic-1", got.TopicID)

	_, ok = s.Get("nonexistent")
	assert.False(t, ok)
}

func TestGetByTopic(t *testing.T) {
	s := openTestStore(t)

	job := makeJob("job-1", "pylon-a", "running")
	job.TopicID = "topic-42"
	s.Put(job)

	got, ok := s.GetByTopic("topic-42")
	require.True(t, ok)
	assert.Equal(t, "job-1", got.ID)

	_, ok = s.GetByTopic("unknown-topic")
	assert.False(t, ok)
}

func TestDelete(t *testing.T) {
	s := openTestStore(t)

	s.Put(makeJob("job-1", "pylon-a", "pending"))
	s.Delete("job-1")

	_, ok := s.Get("job-1")
	assert.False(t, ok)

	// Deleting nonexistent should not panic
	s.Delete("nonexistent")
}

func TestUpdateStatus(t *testing.T) {
	s := openTestStore(t)

	s.Put(makeJob("job-1", "pylon-a", "pending"))
	s.UpdateStatus("job-1", "running")

	got, ok := s.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "running", got.Status)

	// Updating nonexistent should not panic
	s.UpdateStatus("nonexistent", "failed")
}

func TestUpdateSessionID(t *testing.T) {
	s := openTestStore(t)

	s.Put(makeJob("job-1", "pylon-a", "running"))
	s.UpdateSessionID("job-1", "sess-abc")

	got, ok := s.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "sess-abc", got.SessionID)
}

func TestSetCompleted(t *testing.T) {
	s := openTestStore(t)

	before := time.Now()
	s.Put(makeJob("job-1", "pylon-a", "running"))
	s.SetCompleted("job-1", json.RawMessage(`{"result":"done"}`))
	after := time.Now()

	got, ok := s.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "completed", got.Status)
	require.NotNil(t, got.CompletedAt)
	assert.False(t, got.CompletedAt.Before(before), "CompletedAt should not be before the call")
	assert.False(t, got.CompletedAt.After(after), "CompletedAt should not be after the call returned")
}

func TestSetFailed(t *testing.T) {
	s := openTestStore(t)

	before := time.Now()
	s.Put(makeJob("job-1", "pylon-a", "running"))
	s.SetFailed("job-1", "something broke")
	after := time.Now()

	got, ok := s.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "failed", got.Status)
	assert.Equal(t, "something broke", got.Error)
	require.NotNil(t, got.CompletedAt)
	assert.False(t, got.CompletedAt.Before(before), "CompletedAt should not be before the call")
	assert.False(t, got.CompletedAt.After(after), "CompletedAt should not be after the call returned")
}

func TestList(t *testing.T) {
	s := openTestStore(t)

	t.Run("empty", func(t *testing.T) {
		assert.Empty(t, s.List())
	})

	s.Put(makeJob("job-1", "pylon-a", "pending"))
	s.Put(makeJob("job-2", "pylon-b", "running"))

	t.Run("returns all", func(t *testing.T) {
		assert.Len(t, s.List(), 2)
	})
}

func TestListByPylon(t *testing.T) {
	s := openTestStore(t)

	s.Put(makeJob("job-1", "pylon-a", "pending"))
	s.Put(makeJob("job-2", "pylon-b", "running"))
	s.Put(makeJob("job-3", "pylon-a", "completed"))

	assert.Len(t, s.ListByPylon("pylon-a"), 2)
	assert.Len(t, s.ListByPylon("pylon-b"), 1)
	assert.Empty(t, s.ListByPylon("pylon-c"))
}

func TestRecentJobs(t *testing.T) {
	s := openTestStore(t)

	// Put jobs with slightly different creation times
	for i, name := range []string{"job-1", "job-2", "job-3"} {
		j := makeJob(name, "pylon-a", "completed")
		j.CreatedAt = time.Now().Add(time.Duration(i) * time.Second)
		s.Put(j)
	}
	s.Put(makeJob("job-4", "pylon-b", "completed"))

	t.Run("all pylons with limit", func(t *testing.T) {
		jobs, err := s.RecentJobs("", 2)
		require.NoError(t, err)
		assert.Len(t, jobs, 2)
	})

	t.Run("filter by pylon", func(t *testing.T) {
		jobs, err := s.RecentJobs("pylon-a", 10)
		require.NoError(t, err)
		assert.Len(t, jobs, 3)
	})

	t.Run("ordered by created_at desc", func(t *testing.T) {
		jobs, err := s.RecentJobs("", 10)
		require.NoError(t, err)
		require.True(t, len(jobs) >= 2)
		for i := 0; i < len(jobs)-1; i++ {
			assert.True(t, !jobs[i].CreatedAt.Before(jobs[i+1].CreatedAt),
				"job[%d] (%v) should be >= job[%d] (%v)",
				i, jobs[i].CreatedAt, i+1, jobs[i+1].CreatedAt)
		}
	})
}

func TestRecoverFromDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Phase 1: populate
	s1, err := Open(path)
	require.NoError(t, err)
	s1.Put(makeJob("job-pending", "pylon-a", "pending"))
	s1.Put(makeJob("job-running", "pylon-a", "running"))
	s1.Put(makeJob("job-completed", "pylon-a", "completed"))
	s1.Put(makeJob("job-failed", "pylon-a", "failed"))
	s1.Put(makeJob("job-timeout", "pylon-a", "timeout"))
	s1.Put(makeJob("job-dismissed", "pylon-a", "dismissed"))
	s1.Put(makeJob("job-approved", "pylon-a", "approved"))
	s1.Close()

	// Phase 2: recover
	s2, err := Open(path)
	require.NoError(t, err)
	defer s2.Close()

	count := s2.RecoverFromDB()
	// Should recover: pending, running (as active), approved = 3
	assert.Equal(t, 3, count)

	// Running job should be recovered as "active"
	got, ok := s2.Get("job-running")
	require.True(t, ok)
	assert.Equal(t, "active", got.Status)

	// Terminal jobs should not be recovered
	_, ok = s2.Get("job-completed")
	assert.False(t, ok)
	_, ok = s2.Get("job-failed")
	assert.False(t, ok)
}

func TestSaveAndLoadPayloadSample(t *testing.T) {
	s := openTestStore(t)

	t.Run("save and load", func(t *testing.T) {
		s.SavePayloadSample("pylon-a", map[string]interface{}{"key": "val"})
		payload := s.LoadPayloadSample("pylon-a")
		assert.Contains(t, payload, `"key"`)
		assert.Contains(t, payload, `"val"`)
	})

	t.Run("unknown pylon returns empty", func(t *testing.T) {
		assert.Equal(t, "", s.LoadPayloadSample("unknown"))
	})

	t.Run("empty body not saved", func(t *testing.T) {
		s.SavePayloadSample("pylon-empty", map[string]interface{}{})
		assert.Equal(t, "", s.LoadPayloadSample("pylon-empty"))
	})
}

func TestJobIDsFromDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	s, err := Open(path)
	require.NoError(t, err)
	s.Put(makeJob("job-1", "pylon-a", "pending"))
	s.Put(makeJob("job-2", "pylon-a", "running"))
	s.Close()

	ids := JobIDsFromDB(path)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "job-1")
	assert.Contains(t, ids, "job-2")

	t.Run("invalid path returns nil", func(t *testing.T) {
		assert.Nil(t, JobIDsFromDB("/nonexistent/path.db"))
	})
}
