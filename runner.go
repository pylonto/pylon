package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// RunContainer spins up a Docker container for the given pipeline config,
// streams its output, waits for it to finish, and cleans up.
func RunContainer(ctx context.Context, pipeline PipelineConfig, env map[string]string) error {
	// Create a Docker client using environment variables (DOCKER_HOST, etc.)
	// or the default local socket at /var/run/docker.sock.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("creating docker client: %w", err)
	}
	defer cli.Close()

	// Pull the image so we have it locally. If it already exists this is fast.
	log.Printf("[pylon] pulling image %s...", pipeline.Container.Image)
	pullOut, err := cli.ImagePull(ctx, pipeline.Container.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pulling image %s: %w", pipeline.Container.Image, err)
	}
	// ImagePull returns a reader that must be consumed for the pull to complete.
	io.Copy(io.Discard, pullOut)
	pullOut.Close()
	log.Printf("[pylon] image %s ready", pipeline.Container.Image)

	// Build the environment variable list in KEY=VALUE format for the container.
	var envList []string
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}

	// Create the container with the image, command, and env vars from config.
	containerCfg := &container.Config{
		Image: pipeline.Container.Image,
		Cmd:   pipeline.Container.Command,
		Env:   envList,
	}
	resp, err := cli.ContainerCreate(ctx, containerCfg, nil, nil, nil, "")
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	containerID := resp.ID
	log.Printf("[pylon] created container %s", containerID[:12])

	// Ensure cleanup: remove the container when we're done, regardless of outcome.
	defer func() {
		log.Printf("[pylon] removing container %s...", containerID[:12])
		// Force remove so we don't leave stopped containers behind.
		cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
		log.Printf("[pylon] container %s removed", containerID[:12])
	}()

	// Start the container. This is non-blocking — the container runs in the background.
	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[pylon] container %s started", containerID[:12])

	// Attach to the container's stdout and stderr so we can stream logs in real time.
	// ContainerLogs returns a multiplexed stream (stdout + stderr interleaved).
	logReader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true, // Follow keeps the stream open until the container exits.
	})
	if err != nil {
		return fmt.Errorf("attaching to container logs: %w", err)
	}
	defer logReader.Close()

	// Stream container output to our stdout. The Docker log stream has an 8-byte
	// header per frame (stream type + size), but stdcopy handles demuxing for us.
	// For simplicity, we just copy raw — the headers are small and mostly invisible.
	// In production you'd use stdcopy.StdCopy(os.Stdout, os.Stderr, logReader).
	io.Copy(os.Stdout, logReader)

	// Wait for the container to finish. ContainerWait returns two channels:
	// one for the result (exit code) and one for errors.
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
	}

	log.Printf("[pylon] job completed successfully")
	return nil
}

// resolveEnv takes the env map from the pipeline config and substitutes
// template placeholders like "{{ .body }}" with the actual webhook body.
func resolveEnv(templates map[string]string, body string) map[string]string {
	resolved := make(map[string]string, len(templates))
	for k, v := range templates {
		// Simple template substitution: replace {{ .body }} with the raw request body.
		v = strings.ReplaceAll(v, "{{ .body }}", body)
		resolved[k] = v
	}
	return resolved
}
