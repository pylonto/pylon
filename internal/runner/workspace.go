package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// JobsDir is the workspace root for agent job files.
var JobsDir string

// ReposDir is the cache root for bare repos used by git-worktree.
var ReposDir string

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		JobsDir = filepath.Join(home, ".pylon", "jobs")
		ReposDir = filepath.Join(home, ".pylon", "repos")
	} else {
		JobsDir = filepath.Join(os.TempDir(), "pylon-jobs")
		ReposDir = filepath.Join(os.TempDir(), "pylon-repos")
	}
}

// SetupWorkspace prepares the workspace directory based on the configured type.
func SetupWorkspace(ctx context.Context, p RunParams) (string, error) {
	switch p.WorkspaceType {
	case "git-worktree":
		return setupWorktree(ctx, p)
	case "local":
		return setupLocal(p)
	case "none":
		return setupNone(p)
	default: // "git-clone" or empty
		return setupClone(ctx, p)
	}
}

func setupClone(ctx context.Context, p RunParams) (string, error) {
	workDir := WorkDir(p.JobID)
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		if p.Repo != "" {
			log.Printf("[pylon] [%s] cloning %s@%s", p.JobID[:8], p.Repo, p.Ref)
			if err := CloneRepo(ctx, p.Repo, p.Ref, workDir); err != nil {
				return "", err
			}
		} else {
			os.MkdirAll(workDir, 0755)
		}
	} else {
		log.Printf("[pylon] [%s] reusing workspace", p.JobID[:8])
	}
	return workDir, nil
}

func setupWorktree(ctx context.Context, p RunParams) (string, error) {
	if p.Repo == "" {
		return setupNone(p)
	}

	sshRepo := ToSSHURL(p.Repo)
	bareDir := filepath.Join(ReposDir, repoHash(sshRepo))
	workDir := WorkDir(p.JobID)

	// Reuse existing worktree for follow-ups
	if _, err := os.Stat(filepath.Join(workDir, ".git")); err == nil {
		log.Printf("[pylon] [%s] reusing worktree", p.JobID[:8])
		return workDir, nil
	}

	if _, err := os.Stat(filepath.Join(bareDir, "HEAD")); os.IsNotExist(err) {
		log.Printf("[pylon] [%s] initial bare clone of %s", p.JobID[:8], sshRepo)
		os.MkdirAll(filepath.Dir(bareDir), 0755)
		var cloneErr bytes.Buffer
		cmd := exec.CommandContext(ctx, "git", "clone", "--bare", sshRepo, bareDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = &cloneErr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(cloneErr.String())
			if strings.Contains(msg, "Could not read from remote") || strings.Contains(msg, "Permission denied") {
				return "", fmt.Errorf("bare clone of %s failed -- check that the repo exists and your SSH key is configured: %s", sshRepo, msg)
			}
			if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") {
				return "", fmt.Errorf("bare clone of %s failed -- repo not found: %s", sshRepo, msg)
			}
			return "", fmt.Errorf("bare clone of %s failed: %s", sshRepo, msg)
		}
	} else {
		log.Printf("[pylon] [%s] fetching %s", p.JobID[:8], sshRepo)
		cmd := exec.CommandContext(ctx, "git", "fetch", "--all")
		cmd.Dir = bareDir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Run() // best-effort; if offline, use stale
	}

	// Clean up stale git worktree tracking from a previous run
	pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
	pruneCmd.Dir = bareDir
	pruneCmd.Run() // best-effort

	log.Printf("[pylon] [%s] creating worktree for %s", p.JobID[:8], p.Ref)
	var wtErr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", workDir, p.Ref)
	cmd.Dir = bareDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = &wtErr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(wtErr.String())
		// Check if the ref simply doesn't exist in the bare repo.
		refCheck := exec.CommandContext(ctx, "git", "rev-parse", "--verify", p.Ref)
		refCheck.Dir = bareDir
		if refCheck.Run() != nil {
			return "", fmt.Errorf("ref %q not found in %s -- check that the branch/tag exists and run 'git fetch' in the bare repo at %s", p.Ref, sshRepo, bareDir)
		}
		if strings.Contains(msg, "already checked out") || strings.Contains(msg, "is already a worktree") {
			return "", fmt.Errorf("worktree for %q already exists -- a previous job may not have cleaned up; remove %s and retry", p.Ref, workDir)
		}
		return "", fmt.Errorf("worktree add for ref %q failed: %s", p.Ref, msg)
	}
	return workDir, nil
}

func setupLocal(p RunParams) (string, error) {
	if p.LocalPath == "" {
		return "", fmt.Errorf("workspace type 'local' requires a path")
	}
	absPath, err := filepath.Abs(p.LocalPath)
	if err != nil {
		return "", fmt.Errorf("resolving local path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", fmt.Errorf("local path %q does not exist: %w", absPath, err)
	}
	// Symlink so WorkDir(jobID) resolves to the actual local path
	linkPath := WorkDir(p.JobID)
	os.MkdirAll(filepath.Dir(linkPath), 0755)
	if err := os.Symlink(absPath, linkPath); err != nil {
		log.Printf("[pylon] [%s] symlink %s -> %s failed: %v", p.JobID[:8], linkPath, absPath, err)
	}
	log.Printf("[pylon] [%s] using local workspace: %s", p.JobID[:8], absPath)
	return absPath, nil
}

func setupNone(p RunParams) (string, error) {
	workDir := WorkDir(p.JobID)
	os.MkdirAll(workDir, 0755)
	log.Printf("[pylon] [%s] empty workspace", p.JobID[:8])
	return workDir, nil
}

func repoHash(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(h[:8])
}

// CloneRepo performs a shallow git clone of a repo at a specific ref.
// HTTPS GitHub/GitLab URLs are auto-converted to SSH to avoid interactive auth prompts.
func CloneRepo(ctx context.Context, repo, ref, dest string) error {
	repo = ToSSHURL(repo)
	os.MkdirAll(filepath.Dir(dest), 0755)
	cmd := exec.CommandContext(ctx, "git", "clone", "--branch", ref, repo, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// WorkDir returns the workspace directory for a job.
func WorkDir(jobID string) string {
	return filepath.Join(JobsDir, jobID)
}

// LogPath returns the path to the persistent log file for a job.
func LogPath(jobID string) string {
	return filepath.Join(JobsDir, jobID+".log")
}

// CleanupWorkspace removes the workspace directory and log file for a job.
// For local workspaces (symlinks), only the symlink is removed, not the target.
func CleanupWorkspace(jobID string) {
	os.Remove(LogPath(jobID)) // remove persistent log file

	dir := WorkDir(jobID)
	fi, err := os.Lstat(dir)
	if err != nil {
		return // already gone
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		// local workspace -- remove symlink only, never the user's directory
		os.Remove(dir)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[pylon] [%s] failed to remove workspace: %v", jobID[:8], err)
	}
}

// WriteHooksConfig creates a .claude/settings.json in the workspace so
// Claude Code POSTs tool-use events back to the Pylon server.
func WriteHooksConfig(workDir, hooksURL string) {
	dir := filepath.Join(workDir, ".claude")
	os.MkdirAll(dir, 0755)
	settings := fmt.Sprintf(`{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": ".*",
        "hooks": [
          {"type": "command", "command": "curl -s -o /dev/null -X POST -H 'Content-Type: application/json' -d @- '%s'"}
        ]
      }
    ]
  }
}`, hooksURL)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644)
}

// PeekContainerLogs returns the last few lines of logs from containers
// matching the given job IDs. Returns a map of jobID -> log tail.
func PeekContainerLogs(jobIDs []string, tailLines int) map[string]string {
	result := make(map[string]string)
	if len(jobIDs) == 0 {
		return result
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return result
	}
	defer cli.Close()

	ctx := context.Background()
	containers, err := cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return result
	}

	wanted := make(map[string]bool, len(jobIDs))
	for _, id := range jobIDs {
		wanted[id] = true
	}

	tail := fmt.Sprintf("%d", tailLines)
	for i := range containers {
		jobID, ok := containers[i].Labels["pylon.job"]
		if !ok || !wanted[jobID] {
			continue
		}
		logReader, err := cli.ContainerLogs(ctx, containers[i].ID, container.LogsOptions{
			ShowStdout: true, ShowStderr: true, Tail: tail,
		})
		if err != nil {
			continue
		}
		// Docker multiplexed stream: use stdcopy to extract plain text.
		var buf bytes.Buffer
		stdcopy.StdCopy(&buf, &buf, logReader)
		logReader.Close()
		result[jobID] = strings.TrimSpace(buf.String())
	}
	return result
}

// toSSHURL converts HTTPS GitHub/GitLab URLs to SSH format.
// e.g. https://github.com/user/repo -> git@github.com:user/repo.git
// Leaves SSH URLs, template strings, and other URLs untouched.
func ToSSHURL(repo string) string {
	if strings.Contains(repo, "{{") {
		return repo
	}
	for _, host := range []string{"github.com", "gitlab.com"} {
		prefix := "https://" + host + "/"
		if strings.HasPrefix(repo, prefix) {
			path := strings.TrimPrefix(repo, prefix)
			path = strings.TrimSuffix(path, ".git")
			return fmt.Sprintf("git@%s:%s.git", host, path)
		}
	}
	return repo
}

// PruneOrphanedWorkspaces removes workspace dirs (and symlinks) without active jobs.
func PruneOrphanedWorkspaces(activeJobIDs map[string]bool) int {
	entries, err := os.ReadDir(JobsDir)
	if err != nil {
		return 0
	}
	pruned := 0
	for _, e := range entries {
		name := e.Name()
		// Log files are named <jobID>.log -- derive the job ID.
		jobID := strings.TrimSuffix(name, ".log")
		if activeJobIDs[jobID] {
			continue
		}
		p := filepath.Join(JobsDir, name)
		// Check for symlink (local workspace) -- remove link only
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(p)
			pruned++
			continue
		}
		if !e.IsDir() {
			// Regular file (e.g. .log) -- remove it.
			os.Remove(p)
			continue
		}
		os.RemoveAll(p)
		pruned++
	}
	return pruned
}

// PruneWorktreeMetadata runs `git worktree prune` on all cached bare repos
// to clean up stale worktree entries whose directories no longer exist.
func PruneWorktreeMetadata() {
	entries, err := os.ReadDir(ReposDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoDir := filepath.Join(ReposDir, e.Name())
		cmd := exec.Command("git", "worktree", "prune")
		cmd.Dir = repoDir
		cmd.Run() // best-effort
	}
}

// PruneOrphanedContainers kills Docker containers labeled pylon.job
// that don't match any active job.
func PruneOrphanedContainers(activeJobIDs map[string]bool) int {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return 0
	}
	defer cli.Close()

	ctx := context.Background()
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return 0
	}

	pruned := 0
	for i := range containers {
		jobID, ok := containers[i].Labels["pylon.job"]
		if !ok {
			continue
		}
		if !activeJobIDs[jobID] {
			cli.ContainerKill(ctx, containers[i].ID, "SIGKILL")
			cli.ContainerRemove(ctx, containers[i].ID, container.RemoveOptions{Force: true})
			pruned++
		}
	}
	return pruned
}
