package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/stretchr/testify/require"
)

// Exercise the documented invocation, not a second login helper. This is only
// startup/quit in an offline disposable container: never /login or a model prompt.
func TestPiDockerDocumentedLoginStartupDoesNotFetch(t *testing.T) {
	cli, p := piNative(t)
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "running", "pi-worker.mdx"))
	require.NoError(t, err)
	var commands []string
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(line, "/opt/pylon/node_modules/.bin/pi ") {
			commands = append(commands, line)
		}
	}
	require.Len(t, commands, 1, "one authoritative login CLI invocation in the guide")
	args := strings.Fields(commands[0])
	var withoutOffline []string
	for _, arg := range args {
		if arg != "--offline" {
			withoutOffline = append(withoutOffline, arg)
		}
	}
	require.Equal(t, []string{"/opt/pylon/node_modules/.bin/pi", "--provider", "openai-codex", "--no-tools", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes"}, withoutOffline,
		"the smoke must never acquire prompt, login, session-resume or custom-code arguments")

	role, observer := t.TempDir(), filepath.Join(t.TempDir(), "observe.mjs")
	require.NoError(t, os.Chmod(role, 0700))
	entries, err := os.ReadDir(role)
	require.NoError(t, err)
	require.Empty(t, entries, "mount fresh storage only, never any existing credentials")
	// A failed connection is not proof that startup avoided fetching. Count and
	// deny actual fetch attempts as well as disabling the container's network.
	require.NoError(t, os.WriteFile(observer, []byte(`let calls = 0;
const deny = async () => { calls++; throw new Error("pi_cli_fixture_fetch_denied"); };
Object.defineProperty(globalThis, "fetch", { configurable: true, get: () => deny, set: () => {} });
process.on("exit", () => process.stdout.write("\nPI_CLI_FETCH_ATTEMPTS=" + calls + "\n"));
`), 0644))
	cfg, host := piContainerConfig(p, p.Config.Image, "runtime", "worker.mjs", []mount.Mount{
		{Type: mount.TypeBind, Source: role, Target: "/role"},
		{Type: mount.TypeBind, Source: observer, Target: "/observe.mjs", ReadOnly: true},
	})
	// Use the owner's CLI environment, not the worker's PI_OFFLINE default: the
	// documented argument must be the thing that suppresses auxiliary discovery.
	cfg.Env = []string{"HOME=/tmp/home", "PI_CODING_AGENT_DIR=/role", "TERM=xterm-256color", "NODE_OPTIONS=--import=/observe.mjs"}
	cfg.Entrypoint = []string{"/bin/sh", "-ceu", "umask 077; exec \"$@\"", "sh"}
	cfg.Cmd = args
	cfg.Tty, cfg.OpenStdin, cfg.StdinOnce = true, true, true
	require.Equal(t, "none", string(host.NetworkMode))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	created, err := cli.ContainerCreate(ctx, cfg, host, nil, nil, "pylon-pi-"+p.JobID+"-cli-smoke")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, cli.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true}))
		piNativeRemoved(t, cli, p)
	})
	conn, err := cli.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	require.NoError(t, err)
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	require.NoError(t, cli.ContainerStart(ctx, created.ID, container.StartOptions{}))
	require.NoError(t, cli.ContainerResize(ctx, created.ID, container.ResizeOptions{Height: 40, Width: 140}))
	var output piBuffer
	output.max = 256 * 1024
	chunk := make([]byte, 4096)
	quitSent, cursorReplies := false, 0
	for {
		n, readErr := conn.Reader.Read(chunk)
		_, err := output.Write(chunk[:n])
		require.NoError(t, err)
		text := output.String()
		for count := strings.Count(text, "\x1b[6n"); cursorReplies < count; cursorReplies++ {
			_, err = conn.Conn.Write([]byte("\x1b[1;1R"))
			require.NoError(t, err)
		}
		if !quitSent && strings.Contains(text, "No models available") {
			_, err = conn.Conn.Write([]byte("/quit\r"))
			require.NoError(t, err)
			quitSent = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		require.NoError(t, readErr)
	}
	require.True(t, quitSent, "the actual CLI must reach its fresh no-model UI and receive /quit")
	states, errs := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case state := <-states:
		require.Zero(t, state.StatusCode, "normal /quit, not forced termination")
	case err := <-errs:
		t.Fatalf("CLI wait failed: %v", err)
	case <-ctx.Done():
		t.Fatal("CLI startup/quit exceeded its independent fixture bound")
	}
	counts := regexp.MustCompile(`PI_CLI_FETCH_ATTEMPTS=([0-9]+)`).FindAllStringSubmatch(output.String(), -1)
	require.Len(t, counts, 1, "the observer must report, not silently disappear")
	t.Logf("actual CLI: /quit sent, exit=0, fetch attempts=%s, offline auxiliary skips=%d", counts[0][1], strings.Count(output.String(), "Offline mode enabled, skipping download"))
	require.Equal(t, "0", counts[0][1], "no auxiliary or catalog fetch, not merely a denied network")
	require.Equal(t, 2, strings.Count(output.String(), "Offline mode enabled, skipping download"), "both absent fd and rg must take the offline branch")
	require.NoDirExists(t, filepath.Join(role, "bin"))
	require.NoError(t, privatePiAuth(role), "actual fresh CLI storage must pass the unchanged worker gate")
	// This is filesystem compatibility, not an OAuth/availability claim. Inspect
	// only the empty store this test created; never print credential-shaped data.
	auth, err := os.ReadFile(filepath.Join(role, "auth.json"))
	require.NoError(t, err)
	var empty map[string]any
	require.NoError(t, json.Unmarshal(auth, &empty))
	require.Empty(t, empty)
}
