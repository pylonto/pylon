package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/pylonto/pylon/internal/notifier"
)

// RunParams holds everything needed to run an agent job.
type RunParams struct {
	Image       string
	Auth        string
	Prompt      string
	Timeout     time.Duration
	JobID       string
	CallbackURL string
	SessionID   string
	Repo        string
	Ref         string
	Notifier    notifier.Notifier
	TopicID     string
}

// RunAgentJob clones a repo, starts an agent container, streams output,
// enforces a timeout, and cleans up.
func RunAgentJob(ctx context.Context, p RunParams) error {
	workDir := WorkDir(p.JobID)

	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		if p.Repo != "" {
			log.Printf("[pylon] [%s] cloning %s@%s", p.JobID[:8], p.Repo, p.Ref)
			if err := CloneRepo(ctx, p.Repo, p.Ref, workDir); err != nil {
				return err
			}
		} else {
			os.MkdirAll(workDir, 0755)
		}
	} else {
		log.Printf("[pylon] [%s] reusing workspace", p.JobID[:8])
	}

	// Typing indicator while agent works.
	if p.Notifier != nil && p.TopicID != "" {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			p.Notifier.SendTyping(p.TopicID)
			ticker := time.NewTicker(4 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					p.Notifier.SendTyping(p.TopicID)
				case <-stop:
					return
				}
			}
		}()
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	// Pull image (best-effort).
	if pullOut, err := cli.ImagePull(ctx, p.Image, image.PullOptions{}); err == nil {
		io.Copy(io.Discard, pullOut)
		pullOut.Close()
	}

	envList := []string{
		"PROMPT=" + p.Prompt,
		"JOB_ID=" + p.JobID,
		"CALLBACK_URL=" + p.CallbackURL,
	}
	if p.SessionID != "" {
		envList = append(envList, "SESSION_ID="+p.SessionID)
	}

	var mounts []mount.Mount
	if p.Auth == "oauth" {
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

	if p.Notifier != nil && p.TopicID != "" {
		hooksURL := strings.Replace(p.CallbackURL, "/callback/", "/hooks/", 1)
		WriteHooksConfig(workDir, hooksURL)
	}

	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: p.Image, Env: envList,
			Labels: map[string]string{"pylon.job": p.JobID},
		},
		&container.HostConfig{
			Mounts: mounts, ExtraHosts: []string{"host.docker.internal:host-gateway"},
			AutoRemove: true,
		}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	containerID := resp.ID

	defer cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[pylon] [%s] container %s started", p.JobID[:8], containerID[:12])

	if logReader, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	}); err == nil {
		stdcopy.StdCopy(os.Stdout, os.Stderr, logReader)
		logReader.Close()
	}

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
		cli.ContainerKill(context.Background(), containerID, "SIGKILL")
		jobErr = fmt.Errorf("job timed out")
	}

	if jobErr != nil && p.Notifier != nil && p.TopicID != "" {
		msg := "Agent failed: " + jobErr.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("Agent timed out after %s", p.Timeout)
		}
		p.Notifier.SendMessage(p.TopicID, notifier.EscapeMarkdownV2(msg))
	}
	return jobErr
}

// ResolveTemplate substitutes {{ .body.KEY }} placeholders with values from body.
func ResolveTemplate(tmpl string, body map[string]interface{}) string {
	return resolveTemplateFn(tmpl, body, nil)
}

// ResolveTemplateEscaped resolves templates with MarkdownV2 escaping.
func ResolveTemplateEscaped(tmpl string, body map[string]interface{}) string {
	return resolveTemplateFn(tmpl, body, notifier.EscapeMarkdownV2)
}

func resolveTemplateFn(tmpl string, body map[string]interface{}, escapeFn func(string) string) string {
	// Flatten nested maps into dot-separated keys:
	// {"issue": {"title": "x"}} -> {"issue.title": "x", "issue": {...}}
	flat := make(map[string]interface{})
	flattenMap("", body, flat)

	result := tmpl
	for key, val := range flat {
		placeholder := fmt.Sprintf("{{ .body.%s }}", key)
		if !strings.Contains(result, placeholder) {
			continue
		}
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

func flattenMap(prefix string, m map[string]interface{}, out map[string]interface{}) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		out[key] = v
		if nested, ok := v.(map[string]interface{}); ok {
			flattenMap(key, nested, out)
		}
	}
}
