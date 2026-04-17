package cmd

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slackPylonWithRefs returns a pylon that depends on ${SLACK_*} env vars.
// Loading it without those vars set should produce a validation error that
// mentions the missing env var, not a spurious "not found".
func slackPylonWithRefs(name string) *config.PylonConfig {
	return &config.PylonConfig{
		Name:    name,
		Trigger: config.TriggerConfig{Type: "webhook", Path: "/" + name},
		Channel: &config.PylonChannel{
			Type: "slack",
			Slack: &config.SlackConfig{
				BotToken:  "${SLACK_BOT_TOKEN}",
				AppToken:  "${SLACK_APP_TOKEN}",
				ChannelID: "C1234567890",
			},
		},
	}
}

func TestResolveTestPylon_missing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := resolveTestPylon("ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pylon "ghost" not found`)
}

func TestResolveTestPylon_validationDoesNotSayNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Ensure no inherited SLACK_* env vars make this succeed.
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	require.NoError(t, config.SavePylon(slackPylonWithRefs("linear-dev")))

	_, err := resolveTestPylon("linear-dev")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not found",
		"existing-but-invalid pylons must not be reported as missing")
	assert.Contains(t, err.Error(), "SLACK_BOT_TOKEN",
		"error should cite the unset env var")
}

func TestResolveTestPylon_globalEnvFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	// Construct persists tokens to per-pylon .env; verify that path works.
	require.NoError(t, config.SavePylon(slackPylonWithRefs("linear-dev")))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".pylon", "pylons", "linear-dev", ".env"),
		[]byte("SLACK_BOT_TOKEN=xoxb-per-pylon\nSLACK_APP_TOKEN=xapp-per-pylon\n"),
		0600,
	))

	pyl, err := resolveTestPylon("linear-dev")
	require.NoError(t, err)
	assert.Equal(t, "linear-dev", pyl.Name)
}

// fakeDaemon spins up an httptest server that matches the real daemon's
// host:port so fireCron / fireWebhook can be exercised without a live daemon.
// Returns the global config pointing at it and a cleanup/request-capture handle.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) *config.GlobalConfig {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	portNum, _ := strconv.Atoi(port)
	return &config.GlobalConfig{
		Server: config.ServerConfig{Host: host, Port: portNum},
	}
}

func TestFireCron_postsToTriggerEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	global := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, `{"job_id":"abc","status":"triggered"}`)
	})

	err := fireCron("nightly-audit", global)
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/trigger/nightly-audit", gotPath)
}

func TestFireCron_reportsDaemonDown(t *testing.T) {
	// Point at a port that's almost certainly closed.
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 1},
	}
	err := fireCron("whatever", global)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pylon start")
}

func TestFireWebhook_postsToPylonPath(t *testing.T) {
	var gotPath string
	var gotBody string
	global := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, `{"status":"accepted"}`)
	})

	pyl := &config.PylonConfig{
		Name:    "sentry-triage",
		Trigger: config.TriggerConfig{Type: "webhook", Path: "/api/sentry"},
	}
	err := fireWebhook(pyl, global, `{"custom":"payload"}`)
	require.NoError(t, err)
	assert.Equal(t, "/api/sentry", gotPath)
	assert.Equal(t, `{"custom":"payload"}`, strings.TrimSpace(gotBody))
}

func TestResolveTestPylon_loadEnvFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	require.NoError(t, config.SavePylon(slackPylonWithRefs("linear-dev")))
	// Seed GLOBAL .env only; runTest must call LoadEnv before calling resolveTestPylon
	// for this path to succeed.
	require.NoError(t, config.SaveEnvVar("SLACK_BOT_TOKEN", "xoxb-global"))
	require.NoError(t, config.SaveEnvVar("SLACK_APP_TOKEN", "xapp-global"))

	config.LoadEnv()
	pyl, err := resolveTestPylon("linear-dev")
	require.NoError(t, err)
	assert.Equal(t, "linear-dev", pyl.Name)
}
