package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

var jobsDir = filepath.Join(os.TempDir(), "pylon-jobs")

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		jobsDir = filepath.Join(home, ".pylon", "jobs")
	}
}

// RunAgentJob clones a repo, spins up an agent container with the workspace
// mounted, streams output, enforces a timeout, and cleans up everything.
func RunAgentJob(ctx context.Context, pipeline PipelineConfig, jobID string, body map[string]interface{}, callbackURL string, notifier Notifier, topicID string, promptOverride string, sessionID string) error {
	prompt := promptOverride
	if prompt == "" {
		prompt = resolveTemplate(pipeline.Agent.Prompt, body)
	}

	workDir := filepath.Join(jobsDir, jobID)

	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		repo := resolveTemplate(pipeline.Workspace.Repo, body)
		ref := resolveTemplate(pipeline.Workspace.Ref, body)
		os.MkdirAll(jobsDir, 0755)
		log.Printf("[pylon] [%s] cloning %s@%s to %s", jobID[:8], repo, ref, workDir)
		cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, repo, workDir)
		cloneCmd.Stdout = os.Stdout
		cloneCmd.Stderr = os.Stderr
		if err := cloneCmd.Run(); err != nil {
			return fmt.Errorf("git clone failed: %w", err)
		}
		log.Printf("[pylon] [%s] clone complete", jobID[:8])
	} else {
		log.Printf("[pylon] [%s] reusing workspace %s", jobID[:8], workDir)
	}

	// Start typing indicator while agent is working.
	if notifier != nil && topicID != "" {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			notifier.SendTyping(topicID)
			ticker := time.NewTicker(4 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					notifier.SendTyping(topicID)
				case <-stop:
					return
				}
			}
		}()
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	pullOut, err := cli.ImagePull(ctx, pipeline.Agent.Image, image.PullOptions{})
	if err == nil {
		io.Copy(io.Discard, pullOut)
		pullOut.Close()
	}

	envList := []string{
		"PROMPT=" + prompt,
		"JOB_ID=" + jobID,
		"CALLBACK_URL=" + callbackURL,
	}
	if sessionID != "" {
		envList = append(envList, "SESSION_ID="+sessionID)
	}

	var mounts []mount.Mount
	if pipeline.Agent.Auth == "oauth" {
		homeDir, _ := os.UserHomeDir()
		mounts = append(mounts,
			mount.Mount{Type: mount.TypeBind, Source: filepath.Join(homeDir, ".claude"), Target: "/home/pylon/.claude"},
			mount.Mount{Type: mount.TypeBind, Source: filepath.Join(homeDir, ".claude.json"), Target: "/home/pylon/.claude.json"},
		)
	} else {
		if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
			envList = append(envList, "ANTHROPIC_API_KEY="+apiKey)
		}
	}
	mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: workDir, Target: "/workspace"})

	// Write project-level Claude Code settings with an HTTP hook so tool-use
	// events are forwarded to the Pylon server and then to Telegram.
	if notifier != nil && topicID != "" {
		hooksURL := strings.Replace(callbackURL, "/callback/", "/hooks/", 1)
		writeHooksConfig(workDir, hooksURL)
	}

	if pipeline.Agent.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pipeline.Agent.Timeout)
		defer cancel()
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:  pipeline.Agent.Image,
			Env:    envList,
			Labels: map[string]string{"pylon.job": jobID},
		},
		&container.HostConfig{
			Mounts:     mounts,
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
			AutoRemove: true,
		}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	containerID := resp.ID
	log.Printf("[pylon] [%s] created container %s", jobID[:8], containerID[:12])

	defer func() {
		cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[pylon] [%s] container started", jobID[:8])

	logReader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if err != nil {
		return fmt.Errorf("attaching to container logs: %w", err)
	}
	defer logReader.Close()

	stdcopy.StdCopy(os.Stdout, os.Stderr, logReader)

	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	var jobErr error
	select {
	case err := <-errCh:
		if err != nil {
			jobErr = fmt.Errorf("waiting for container: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			jobErr = fmt.Errorf("container exited with code %d", status.StatusCode)
		}
	case <-ctx.Done():
		log.Printf("[pylon] [%s] timeout reached, killing container", jobID[:8])
		cli.ContainerKill(context.Background(), containerID, "SIGKILL")
		jobErr = fmt.Errorf("job timed out")
	}

	if jobErr != nil {
		if notifier != nil && topicID != "" {
			if ctx.Err() == context.DeadlineExceeded {
				notifier.SendMessage(topicID, escapeMarkdownV2("Agent timed out after "+pipeline.Agent.Timeout.String()))
			} else {
				notifier.SendMessage(topicID, escapeMarkdownV2("Agent failed: "+jobErr.Error()))
			}
		}
		return jobErr
	}
	return nil
}

func resolveTemplate(tmpl string, body map[string]interface{}) string {
	return resolveTemplateFn(tmpl, body, nil)
}

func resolveTemplateEscaped(tmpl string, body map[string]interface{}) string {
	return resolveTemplateFn(tmpl, body, escapeMarkdownV2)
}

func resolveTemplateFn(tmpl string, body map[string]interface{}, escapeFn func(string) string) string {
	result := tmpl
	for key, val := range body {
		placeholder := fmt.Sprintf("{{ .body.%s }}", key)
		var s string
		switch v := val.(type) {
		case string:
			s = v
		default:
			b, _ := json.Marshal(v)
			s = string(b)
		}
		if escapeFn != nil {
			s = escapeFn(s)
		}
		result = strings.ReplaceAll(result, placeholder, s)
	}
	return result
}

// writeHooksConfig creates a project-level .claude/settings.json in the workspace
// so Claude Code POSTs tool-use events to the Pylon server.
func writeHooksConfig(workDir string, hooksURL string) {
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

func shortID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// CleanupWorkspace removes the workspace directory for a completed job.
func CleanupWorkspace(jobID string) {
	dir := filepath.Join(jobsDir, jobID)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[pylon] [%s] failed to remove workspace: %v", jobID[:8], err)
	} else {
		log.Printf("[pylon] [%s] workspace cleaned up", jobID[:8])
	}
}

// PruneOrphanedWorkspaces removes workspace directories that don't have
// a corresponding pending job. These accumulate from crashes, completed jobs
// that were never /done'd, or jobs dismissed via "ignore".
func PruneOrphanedWorkspaces(pending PendingJobStore) int {
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return 0
	}
	pruned := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := pending.Get(e.Name()); !ok {
			if err := os.RemoveAll(filepath.Join(jobsDir, e.Name())); err != nil {
				log.Printf("[pylon] failed to prune workspace %s: %v", shortID(e.Name(), 8), err)
			} else {
				log.Printf("[pylon] pruned orphaned workspace %s", shortID(e.Name(), 8))
				pruned++
			}
		}
	}
	return pruned
}

// PruneOrphanedContainers kills Docker containers labeled with "pylon.job"
// that don't match any pending job. These can linger if pylon crashes mid-run.
func PruneOrphanedContainers(pending PendingJobStore) int {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Printf("[pylon] failed to create docker client for pruning: %v", err)
		return 0
	}
	defer cli.Close()

	ctx := context.Background()
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		log.Printf("[pylon] failed to list containers for pruning: %v", err)
		return 0
	}

	pruned := 0
	for _, c := range containers {
		jobID, ok := c.Labels["pylon.job"]
		if !ok {
			continue
		}
		if _, exists := pending.Get(jobID); !exists {
			log.Printf("[pylon] killing orphaned container %s (job %s)", shortID(c.ID, 12), shortID(jobID, 8))
			cli.ContainerKill(ctx, c.ID, "SIGKILL")
			cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
			pruned++
		}
	}
	return pruned
}
