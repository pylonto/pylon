package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

	content := string(data)

	// Verify the hooks URL is in the settings
	assert.Contains(t, content, hooksURL)
	assert.Contains(t, content, "PostToolUse")

	// Verify command-type hook with curl (not http type, which silently fails)
	assert.Contains(t, content, `"type": "command"`)
	assert.Contains(t, content, "curl")
	assert.Contains(t, content, "-d @-")
	assert.NotContains(t, content, `"type": "http"`)

	// Verify catch-all matcher
	assert.Contains(t, content, `"matcher": ".*"`)
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

func withTempReposDir(t *testing.T) string {
	t.Helper()
	orig := ReposDir
	ReposDir = t.TempDir()
	t.Cleanup(func() { ReposDir = orig })
	return ReposDir
}

func TestSetupNone(t *testing.T) {
	jobsDir := withTempJobsDir(t)
	jobID := "00000000-0000-0000-0000-000000000001"

	workDir, err := setupNone(RunParams{JobID: jobID})
	require.NoError(t, err)

	expected := filepath.Join(jobsDir, jobID)
	assert.Equal(t, expected, workDir)

	// Directory should exist
	info, err := os.Stat(workDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestSetupLocal(t *testing.T) {
	jobsDir := withTempJobsDir(t)

	t.Run("creates symlink to existing path", func(t *testing.T) {
		target := t.TempDir()
		os.WriteFile(filepath.Join(target, "file.txt"), []byte("content"), 0644)
		jobID := "00000000-0000-0000-0000-000000000002"

		workDir, err := setupLocal(RunParams{JobID: jobID, LocalPath: target})
		require.NoError(t, err)
		assert.Equal(t, target, workDir)

		// Verify symlink was created
		linkPath := filepath.Join(jobsDir, jobID)
		linkTarget, err := os.Readlink(linkPath)
		require.NoError(t, err)
		assert.Equal(t, target, linkTarget)
	})

	t.Run("fails for empty path", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000003"
		_, err := setupLocal(RunParams{JobID: jobID, LocalPath: ""})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "requires a path")
	})

	t.Run("fails for nonexistent path", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000004"
		_, err := setupLocal(RunParams{JobID: jobID, LocalPath: "/nonexistent/path/xyz"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})
}

func TestSetupClone(t *testing.T) {
	jobsDir := withTempJobsDir(t)

	t.Run("creates workspace dir for empty repo", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000005"
		workDir, err := setupClone(context.Background(), RunParams{JobID: jobID})
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(jobsDir, jobID), workDir)

		info, err := os.Stat(workDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("reuses existing workspace", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000006"
		workDir := filepath.Join(jobsDir, jobID)
		os.MkdirAll(workDir, 0755)
		os.WriteFile(filepath.Join(workDir, "existing.txt"), []byte("data"), 0644)

		result, err := setupClone(context.Background(), RunParams{JobID: jobID, Repo: "git@github.com:test/repo.git"})
		require.NoError(t, err)
		assert.Equal(t, workDir, result)

		// Existing file should still be there (not overwritten)
		_, err = os.Stat(filepath.Join(workDir, "existing.txt"))
		assert.NoError(t, err)
	})

	t.Run("clones local repo", func(t *testing.T) {
		// Create a local bare git repo to clone from
		bareDir := t.TempDir()
		runGit(t, bareDir, "init", "--bare")
		// We need at least one commit to clone
		tmpClone := t.TempDir()
		runGit(t, tmpClone, "clone", bareDir, ".")
		os.WriteFile(filepath.Join(tmpClone, "README.md"), []byte("# test"), 0644)
		runGit(t, tmpClone, "add", ".")
		runGit(t, tmpClone, "commit", "-m", "initial")
		runGit(t, tmpClone, "push")

		jobID := "00000000-0000-0000-0000-000000000007"
		workDir, err := setupClone(context.Background(), RunParams{
			JobID: jobID,
			Repo:  bareDir,
			Ref:   "master",
		})
		require.NoError(t, err)

		// Should have cloned the repo
		_, err = os.Stat(filepath.Join(workDir, "README.md"))
		assert.NoError(t, err, "cloned repo should contain README.md")
	})
}

func TestSetupWorktree(t *testing.T) {
	jobsDir := withTempJobsDir(t)
	withTempReposDir(t)

	t.Run("empty repo falls back to setupNone", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000008"
		workDir, err := setupWorktree(context.Background(), RunParams{JobID: jobID})
		require.NoError(t, err)

		expected := filepath.Join(jobsDir, jobID)
		assert.Equal(t, expected, workDir)

		info, err := os.Stat(workDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("creates bare clone and worktree", func(t *testing.T) {
		// Create a local bare git repo
		sourceDir := t.TempDir()
		runGit(t, sourceDir, "init", "--bare")
		tmpClone := t.TempDir()
		runGit(t, tmpClone, "clone", sourceDir, ".")
		os.WriteFile(filepath.Join(tmpClone, "main.go"), []byte("package main"), 0644)
		runGit(t, tmpClone, "add", ".")
		runGit(t, tmpClone, "commit", "-m", "initial")
		runGit(t, tmpClone, "push")

		jobID := "00000000-0000-0000-0000-000000000009"
		workDir, err := setupWorktree(context.Background(), RunParams{
			JobID: jobID,
			Repo:  sourceDir,
			Ref:   "master",
		})
		require.NoError(t, err)

		// Should have the file from the repo
		_, err = os.Stat(filepath.Join(workDir, "main.go"))
		assert.NoError(t, err, "worktree should contain main.go")

		// Bare repo should exist in ReposDir
		entries, _ := os.ReadDir(ReposDir)
		assert.True(t, len(entries) > 0, "bare repo cache should be populated")
	})

	t.Run("reuses existing worktree", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000010"
		workDir := filepath.Join(jobsDir, jobID)
		os.MkdirAll(workDir, 0755)
		// Create a fake .git file to simulate an existing worktree
		os.WriteFile(filepath.Join(workDir, ".git"), []byte("gitdir: /tmp/fake"), 0644)

		result, err := setupWorktree(context.Background(), RunParams{
			JobID: jobID,
			Repo:  "git@github.com:test/repo.git",
			Ref:   "main",
		})
		require.NoError(t, err)
		assert.Equal(t, workDir, result)
	})
}

func TestSetupWorkspace_dispatch(t *testing.T) {
	withTempJobsDir(t)

	t.Run("none type", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000011"
		workDir, err := SetupWorkspace(context.Background(), RunParams{
			JobID:         jobID,
			WorkspaceType: "none",
		})
		require.NoError(t, err)
		assert.Contains(t, workDir, jobID)
	})

	t.Run("local type", func(t *testing.T) {
		target := t.TempDir()
		jobID := "00000000-0000-0000-0000-000000000012"
		workDir, err := SetupWorkspace(context.Background(), RunParams{
			JobID:         jobID,
			WorkspaceType: "local",
			LocalPath:     target,
		})
		require.NoError(t, err)
		assert.Equal(t, target, workDir)
	})

	t.Run("default type with no repo creates empty dir", func(t *testing.T) {
		jobID := "00000000-0000-0000-0000-000000000013"
		workDir, err := SetupWorkspace(context.Background(), RunParams{
			JobID:         jobID,
			WorkspaceType: "git-clone",
		})
		require.NoError(t, err)
		info, err := os.Stat(workDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})
}

func TestPruneWorktreeMetadata(t *testing.T) {
	reposDir := withTempReposDir(t)

	// Create a fake bare repo directory with a git init
	fakeRepo := filepath.Join(reposDir, "fakerepo")
	os.MkdirAll(fakeRepo, 0755)
	runGit(t, fakeRepo, "init", "--bare")

	// Should not panic -- just runs git worktree prune
	PruneWorktreeMetadata()

	// Repo should still exist
	_, err := os.Stat(fakeRepo)
	assert.NoError(t, err)
}

func TestPruneWorktreeMetadata_noReposDir(t *testing.T) {
	orig := ReposDir
	ReposDir = "/nonexistent/path/xyz"
	defer func() { ReposDir = orig }()

	// Should not panic on nonexistent dir
	PruneWorktreeMetadata()
}

// runGit executes a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
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
