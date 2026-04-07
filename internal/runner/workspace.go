package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// JobsDir is the workspace root for agent job files.
var JobsDir string

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		JobsDir = filepath.Join(home, ".pylon", "jobs")
	} else {
		JobsDir = filepath.Join(os.TempDir(), "pylon-jobs")
	}
}

// CloneRepo performs a shallow git clone of a repo at a specific ref.
func CloneRepo(ctx context.Context, repo, ref, dest string) error {
	os.MkdirAll(filepath.Dir(dest), 0755)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, repo, dest)
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

// CleanupWorkspace removes the workspace directory for a job.
func CleanupWorkspace(jobID string) {
	dir := WorkDir(jobID)
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
        "matcher": "Bash|Edit|Write|MultiEdit",
        "hooks": [{"type": "http", "url": %q}]
      }
    ]
  }
}`, hooksURL)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0644)
}

// PruneOrphanedWorkspaces removes workspace dirs without active jobs.
func PruneOrphanedWorkspaces(activeJobIDs map[string]bool) int {
	entries, err := os.ReadDir(JobsDir)
	if err != nil {
		return 0
	}
	pruned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !activeJobIDs[e.Name()] {
			os.RemoveAll(filepath.Join(JobsDir, e.Name()))
			pruned++
		}
	}
	return pruned
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
	for _, c := range containers {
		jobID, ok := c.Labels["pylon.job"]
		if !ok {
			continue
		}
		if !activeJobIDs[jobID] {
			cli.ContainerKill(ctx, c.ID, "SIGKILL")
			cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
			pruned++
		}
	}
	return pruned
}
