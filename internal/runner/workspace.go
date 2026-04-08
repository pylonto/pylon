package runner

import (
	"bytes"
	"context"
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

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		JobsDir = filepath.Join(home, ".pylon", "jobs")
	} else {
		JobsDir = filepath.Join(os.TempDir(), "pylon-jobs")
	}
}

// CloneRepo performs a shallow git clone of a repo at a specific ref.
// HTTPS GitHub/GitLab URLs are auto-converted to SSH to avoid interactive auth prompts.
func CloneRepo(ctx context.Context, repo, ref, dest string) error {
	repo = ToSSHURL(repo)
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
	for _, c := range containers {
		jobID, ok := c.Labels["pylon.job"]
		if !ok || !wanted[jobID] {
			continue
		}
		logReader, err := cli.ContainerLogs(ctx, c.ID, container.LogsOptions{
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
