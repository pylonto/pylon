package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAgentDeadlineIncludesWorkspaceSetup(t *testing.T) {
	root := t.TempDir()
	previous := JobsDir
	JobsDir = filepath.Join(root, "jobs")
	defer func() { JobsDir = previous }()
	require.NoError(t, os.WriteFile(filepath.Join(root, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0700))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	err := RunAgentJob(ctx, RunParams{JobID: "timeout-probe", Repo: "synthetic", Ref: "main", Timeout: 50 * time.Millisecond})
	require.Error(t, err)
	require.Less(t, time.Since(started), time.Second, "the agent budget starts before clone, not after it")
}

func TestCloneRepoAcceptsAnExactCommitWithoutFollowingTheBranch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(repo, 0700))
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null"}, args...)...)
		b, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", b)
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "version"), []byte("baseline"), 0600))
	git("add", "version")
	git("commit", "-m", "baseline")
	baseline := git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "version"), []byte("newer"), 0600))
	git("add", "version")
	git("commit", "-m", "newer")
	dest := filepath.Join(root, "clone")
	require.NoError(t, CloneRepo(context.Background(), repo, baseline, dest))
	contents, err := os.ReadFile(filepath.Join(dest, "version"))
	require.NoError(t, err)
	require.Equal(t, "baseline", string(contents))
	bad := filepath.Join(root, "bad-clone")
	require.Error(t, CloneRepo(context.Background(), repo, strings.Repeat("f", 40), bad))
	require.NoDirExists(t, bad, "a failed clone must not be reused as a valid workspace")
}
