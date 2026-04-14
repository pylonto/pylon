package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openMultiTestStore(t *testing.T) (*MultiStore, *Store) {
	t.Helper()
	sa, err := Open(filepath.Join(t.TempDir(), "a.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sa.Close() })

	sb, err := Open(filepath.Join(t.TempDir(), "b.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sb.Close() })

	ms := NewMulti(map[string]*Store{"pylon-a": sa, "pylon-b": sb})
	return ms, sa
}

func TestMultiStorePutAndGet(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	ms.Put(makeJob("job-1", "pylon-a", "pending"))
	ms.Put(makeJob("job-2", "pylon-b", "running"))

	got, ok := ms.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "pylon-a", got.PylonName)

	got, ok = ms.Get("job-2")
	require.True(t, ok)
	assert.Equal(t, "pylon-b", got.PylonName)

	_, ok = ms.Get("nonexistent")
	assert.False(t, ok)
}

func TestMultiStoreGetByTopic(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	job := makeJob("job-1", "pylon-a", "running")
	job.TopicID = "topic-42"
	ms.Put(job)

	got, ok := ms.GetByTopic("topic-42")
	require.True(t, ok)
	assert.Equal(t, "job-1", got.ID)

	_, ok = ms.GetByTopic("unknown")
	assert.False(t, ok)
}

func TestMultiStoreList(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	ms.Put(makeJob("job-1", "pylon-a", "pending"))
	ms.Put(makeJob("job-2", "pylon-b", "running"))
	ms.Put(makeJob("job-3", "pylon-a", "completed"))

	all := ms.List()
	assert.Len(t, all, 3)
}

func TestMultiStoreUpdateStatus(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	ms.Put(makeJob("job-1", "pylon-a", "pending"))
	ms.UpdateStatus("job-1", "running")

	got, ok := ms.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "running", got.Status)
}

func TestMultiStoreSetCompleted(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	ms.Put(makeJob("job-1", "pylon-a", "running"))
	ms.SetCompleted("job-1", json.RawMessage(`{"result":"ok"}`))

	got, ok := ms.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "completed", got.Status)
	assert.NotNil(t, got.CompletedAt)
}

func TestMultiStoreDelete(t *testing.T) {
	ms, _ := openMultiTestStore(t)

	ms.Put(makeJob("job-1", "pylon-a", "pending"))
	ms.Delete("job-1")

	_, ok := ms.Get("job-1")
	assert.False(t, ok)
}

func TestMultiStoreGetFallback(t *testing.T) {
	// Put a job directly into the underlying store (bypassing index)
	ms, sa := openMultiTestStore(t)

	sa.Put(makeJob("job-direct", "pylon-a", "pending"))
	// The index doesn't know about this job, but Get should find it via fallback
	got, ok := ms.Get("job-direct")
	require.True(t, ok)
	assert.Equal(t, "job-direct", got.ID)
}

func TestMultiStoreRecoverFromDB(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: populate stores directly
	pathA := filepath.Join(dir, "a.db")
	sa, err := Open(pathA)
	require.NoError(t, err)
	sa.Put(makeJob("job-1", "pylon-a", "pending"))
	sa.Put(makeJob("job-2", "pylon-a", "completed"))
	sa.Close()

	pathB := filepath.Join(dir, "b.db")
	sb, err := Open(pathB)
	require.NoError(t, err)
	sb.Put(makeJob("job-3", "pylon-b", "running"))
	sb.Close()

	// Phase 2: open fresh stores and recover
	sa2, err := Open(pathA)
	require.NoError(t, err)
	defer sa2.Close()

	sb2, err := Open(pathB)
	require.NoError(t, err)
	defer sb2.Close()

	ms := NewMulti(map[string]*Store{"pylon-a": sa2, "pylon-b": sb2})
	total := ms.RecoverFromDB()

	// job-1 (pending) + job-3 (running->active) = 2 recovered
	assert.Equal(t, 2, total)

	// Verify index is populated
	got, ok := ms.Get("job-1")
	require.True(t, ok)
	assert.Equal(t, "pending", got.Status)

	got, ok = ms.Get("job-3")
	require.True(t, ok)
	assert.Equal(t, "active", got.Status)

	// Completed job not recovered
	_, ok = ms.Get("job-2")
	assert.False(t, ok)
}
