package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempJobsDir overrides JobsDir to a temp directory for the test.
func withTempJobsDir(t *testing.T) string {
	t.Helper()
	orig := JobsDir
	JobsDir = t.TempDir()
	t.Cleanup(func() { JobsDir = orig })
	return JobsDir
}

func TestWriteHooksConfig(t *testing.T) {
	workDir := t.TempDir()
	hooksURL := "http://localhost:8080/hooks/job-123"

	WriteHooksConfig(workDir, hooksURL)

	settingsPath := filepath.Join(workDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	// Verify it's valid JSON
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	// Verify the hooks URL is in the settings
	assert.Contains(t, string(data), hooksURL)
	assert.Contains(t, string(data), "PostToolUse")
}

func TestCleanupWorkspace(t *testing.T) {
	t.Run("removes directory and log file", func(t *testing.T) {
		jobsDir := withTempJobsDir(t)
		jobID := "cleanup-test-1"

		// Create workspace dir and log file
		workDir := filepath.Join(jobsDir, jobID)
		os.MkdirAll(workDir, 0755)
		os.WriteFile(filepath.Join(workDir, "file.txt"), []byte("test"), 0644)
		logFile := filepath.Join(jobsDir, jobID+".log")
		os.WriteFile(logFile, []byte("log data"), 0644)

		CleanupWorkspace(jobID)

		_, err := os.Stat(workDir)
		assert.True(t, os.IsNotExist(err), "workspace dir should be removed")
		_, err = os.Stat(logFile)
		assert.True(t, os.IsNotExist(err), "log file should be removed")
	})

	t.Run("removes symlink but not target", func(t *testing.T) {
		jobsDir := withTempJobsDir(t)
		jobID := "cleanup-symlink"

		// Create a real directory (the target)
		target := t.TempDir()
		marker := filepath.Join(target, "marker.txt")
		os.WriteFile(marker, []byte("keep me"), 0644)

		// Create symlink in jobs dir
		linkPath := filepath.Join(jobsDir, jobID)
		require.NoError(t, os.Symlink(target, linkPath))

		CleanupWorkspace(jobID)

		// Symlink should be removed
		_, err := os.Lstat(linkPath)
		assert.True(t, os.IsNotExist(err), "symlink should be removed")

		// Target directory and its contents should still exist
		_, err = os.Stat(marker)
		assert.NoError(t, err, "target directory contents should be untouched")
	})

	t.Run("nonexistent workspace is a no-op", func(t *testing.T) {
		withTempJobsDir(t)
		// Should not panic
		CleanupWorkspace("nonexistent-job")
	})
}

func TestPruneOrphanedWorkspaces(t *testing.T) {
	jobsDir := withTempJobsDir(t)

	// Create workspaces for 3 jobs
	for _, id := range []string{"job-1", "job-2", "job-3"} {
		os.MkdirAll(filepath.Join(jobsDir, id), 0755)
		os.WriteFile(filepath.Join(jobsDir, id+".log"), []byte("log"), 0644)
	}

	// Create a symlink workspace for job-4
	target := t.TempDir()
	_ = os.Symlink(target, filepath.Join(jobsDir, "job-4"))

	// Only job-2 is active
	active := map[string]bool{"job-2": true}
	pruned := PruneOrphanedWorkspaces(active)

	// job-1, job-3 dirs pruned = 2, job-4 symlink pruned = 1 -> at least 3
	// Log files are removed but may or may not be counted depending on
	// whether their jobID (with .log stripped) is in the active set
	assert.Equal(t, 3, pruned, "should prune exactly 3 orphaned workspaces (job-1, job-3, job-4)")

	// job-2 should still exist
	_, err := os.Stat(filepath.Join(jobsDir, "job-2"))
	assert.NoError(t, err, "active job workspace should be preserved")
	_, err = os.Stat(filepath.Join(jobsDir, "job-2.log"))
	assert.NoError(t, err, "active job log should be preserved")

	// job-1 should be gone
	_, err = os.Stat(filepath.Join(jobsDir, "job-1"))
	assert.True(t, os.IsNotExist(err), "orphaned workspace should be removed")

	// Symlink target should be untouched
	_, err = os.Stat(target)
	assert.NoError(t, err, "symlink target should be untouched")
}
