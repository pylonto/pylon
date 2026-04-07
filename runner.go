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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

// Use a directory under $HOME so it's accessible inside the Colima/Docker VM.
// Colima mounts the home directory by default but not /tmp.
var jobsDir = filepath.Join(os.TempDir(), "pylon-jobs")

func init() {
	if home, err := os.UserHomeDir(); err == nil {
		jobsDir = filepath.Join(home, ".pylon", "jobs")
	}
}

// RunAgentJob clones a repo, spins up an agent container with the workspace
// mounted, streams output, enforces a timeout, and cleans up everything.
func RunAgentJob(ctx context.Context, pipeline PipelineConfig, jobID string, body map[string]interface{}, callbackURL string) error {
	// Resolve template values from the webhook JSON body.
	repo := resolveTemplate(pipeline.Workspace.Repo, body)
	ref := resolveTemplate(pipeline.Workspace.Ref, body)
	prompt := resolveTemplate(pipeline.Agent.Prompt, body)

	workDir := filepath.Join(jobsDir, jobID)
	os.MkdirAll(jobsDir, 0755)

	// Clone the repo to the host. The runner owns git — the agent image doesn't
	// need git installed, keeping agent images simple and swappable.
	log.Printf("[pylon] [%s] cloning %s@%s to %s", jobID[:8], repo, ref, workDir)
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, repo, workDir)
	cloneCmd.Stdout = os.Stdout
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	log.Printf("[pylon] [%s] clone complete", jobID[:8])

	// Ensure the workspace is cleaned up when the job finishes.
	defer func() {
		log.Printf("[pylon] [%s] cleaning up workspace %s", jobID[:8], workDir)
		os.RemoveAll(workDir)
	}()

	// Create a Docker client from environment (DOCKER_HOST) or default socket.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	// Pull the agent image so it's available locally.
	log.Printf("[pylon] [%s] pulling image %s...", jobID[:8], pipeline.Agent.Image)
	pullOut, err := cli.ImagePull(ctx, pipeline.Agent.Image, image.PullOptions{})
	if err != nil {
		// If pull fails, the image might be local-only (e.g. built with make image).
		log.Printf("[pylon] [%s] pull failed (may be local): %v", jobID[:8], err)
	} else {
		// Must consume the reader for the pull to complete.
		io.Copy(io.Discard, pullOut)
		pullOut.Close()
	}

	// Build container environment variables.
	envList := []string{
		"PROMPT=" + prompt,
		"JOB_ID=" + jobID,
		"CALLBACK_URL=" + callbackURL,
	}

	// Auth: pass API key from host env, or mount OAuth session directory.
	var mounts []mount.Mount
	if pipeline.Agent.Auth == "oauth" {
		// Mount the host's ~/.claude directory and ~/.claude.json into the container.
		// Read-write because Claude Code needs to refresh OAuth tokens.
		homeDir, _ := os.UserHomeDir()
		claudeDir := filepath.Join(homeDir, ".claude")
		claudeJSON := filepath.Join(homeDir, ".claude.json")
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: claudeDir,
			Target: "/home/pylon/.claude",
		})
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: claudeJSON,
			Target: "/home/pylon/.claude.json",
		})
	} else {
		// Default: pass ANTHROPIC_API_KEY from the host environment.
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey != "" {
			envList = append(envList, "ANTHROPIC_API_KEY="+apiKey)
		}
	}

	// Bind-mount the cloned repo into /workspace inside the container.
	mounts = append(mounts, mount.Mount{
		Type:   mount.TypeBind,
		Source: workDir,
		Target: "/workspace",
	})

	// Apply timeout from config — kills the container if it runs too long.
	if pipeline.Agent.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pipeline.Agent.Timeout)
		defer cancel()
	}

	// Create the container with config and host binds.
	containerCfg := &container.Config{
		Image: pipeline.Agent.Image,
		Env:   envList,
	}
	hostCfg := &container.HostConfig{
		Mounts: mounts,
		// host.docker.internal lets the container reach the host's network.
		// This works natively on macOS Docker; on Linux we need the extra-host mapping.
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
		// AutoRemove cleans up the container after it exits.
		AutoRemove: true,
	}

	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	containerID := resp.ID
	log.Printf("[pylon] [%s] created container %s", jobID[:8], containerID[:12])

	// With AutoRemove, we only need to force-remove if the container hasn't exited.
	defer func() {
		cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	}()

	// Start the container — non-blocking, it runs in the background.
	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[pylon] [%s] container started", jobID[:8])

	// Stream container stdout/stderr to the terminal in real time.
	// Follow=true keeps the stream open until the container exits.
	logReader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return fmt.Errorf("attaching to container logs: %w", err)
	}
	defer logReader.Close()
	io.Copy(os.Stdout, logReader)

	// Wait for the container to finish. Returns exit code via statusCh.
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("waiting for container: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("container exited with code %d", status.StatusCode)
		}
	case <-ctx.Done():
		// Timeout hit — kill the container.
		log.Printf("[pylon] [%s] timeout reached, killing container", jobID[:8])
		cli.ContainerKill(context.Background(), containerID, "SIGKILL")
		return fmt.Errorf("job timed out")
	}

	log.Printf("[pylon] [%s] job completed", jobID[:8])
	return nil
}

// resolveTemplate does simple {{ .body.field }} substitution using the parsed
// JSON body from the webhook. Supports top-level string fields only.
// Example: "{{ .body.repo }}" with body {"repo": "https://..."} → "https://..."
func resolveTemplate(tmpl string, body map[string]interface{}) string {
	result := tmpl
	for key, val := range body {
		placeholder := fmt.Sprintf("{{ .body.%s }}", key)
		switch v := val.(type) {
		case string:
			result = strings.ReplaceAll(result, placeholder, v)
		default:
			// For non-string values, marshal back to JSON.
			b, _ := json.Marshal(v)
			result = strings.ReplaceAll(result, placeholder, string(b))
		}
	}
	return result
}
