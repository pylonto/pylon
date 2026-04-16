package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oobeDaemon creates a daemon from realistic OOBE-style configs.
// Simulates the config-to-daemon pipeline that happens after setup + construct.
func oobeDaemon(t *testing.T, global *config.GlobalConfig, pylons map[string]*config.PylonConfig) *Daemon {
	t.Helper()

	stores := make(map[string]*store.Store, len(pylons))
	for name := range pylons {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, name+".db"))
		require.NoError(t, err)
		t.Cleanup(func() { st.Close() })
		stores[name] = st
	}

	ms := store.NewMulti(stores)
	ch := newMockChannel()
	return New(global, pylons, ms, ch, nil)
}

func TestIntegration_TelegramWebhook(t *testing.T) {
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "telegram"
	global.Defaults.Channel.Telegram = &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   123456,
	}
	global.Defaults.Agent.Type = "claude"

	pylons := map[string]*config.PylonConfig{
		"sentry-triage": {
			Name:      "sentry-triage",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/sentry"},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "Investigate this Sentry error"},
		},
	}

	d := oobeDaemon(t, global, pylons)

	// Verify webhook route works
	body := `{"event":"error","title":"NullPointerException"}`
	req := httptest.NewRequest(http.MethodPost, "/sentry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["job_id"])
	_, err := uuid.Parse(resp["job_id"])
	assert.NoError(t, err, "job_id should be a valid UUID")

	// Verify channel resolution -- should use the global default channel
	ch := d.channelFor("sentry-triage")
	assert.True(t, ch.Ready())

	// Verify config resolution
	pyl := pylons["sentry-triage"]
	chType, tg, sl := pyl.ResolveChannel(global)
	assert.Equal(t, "telegram", chType)
	assert.NotNil(t, tg)
	assert.Nil(t, sl)
}

func TestIntegration_TelegramCron(t *testing.T) {
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "telegram"
	global.Defaults.Channel.Telegram = &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   123456,
	}
	global.Defaults.Agent.Type = "claude"

	pylons := map[string]*config.PylonConfig{
		"weekly-audit": {
			Name: "weekly-audit",
			Trigger: config.TriggerConfig{
				Type:     "cron",
				Cron:     "0 9 * * 1",
				Timezone: "America/New_York",
			},
			Workspace: config.WorkspaceConfig{Type: "git-clone", Repo: "git@github.com:test/repo.git", Ref: "main"},
			Agent:     &config.PylonAgent{Prompt: "Audit this codebase"},
		},
	}

	d := oobeDaemon(t, global, pylons)

	// Cron pylons don't register webhook routes, so verify the trigger config
	pyl := pylons["weekly-audit"]
	assert.Equal(t, "cron", pyl.Trigger.Type)
	assert.Equal(t, "0 9 * * 1", pyl.Trigger.Cron)
	assert.Equal(t, "America/New_York", pyl.Trigger.Timezone)

	// Verify timezone resolves correctly
	loc := pyl.ResolveTimezone(global)
	assert.Equal(t, "America/New_York", loc.String())

	// Verify channel resolution
	ch := d.channelFor("weekly-audit")
	assert.True(t, ch.Ready())

	chType, tg, _ := pyl.ResolveChannel(global)
	assert.Equal(t, "telegram", chType)
	assert.NotNil(t, tg)

	// Verify the trigger route does NOT exist (no webhook path)
	req := httptest.NewRequest(http.MethodPost, "/weekly-audit", nil)
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// But the /trigger/<name> route should work
	req = httptest.NewRequest(http.MethodPost, "/trigger/weekly-audit", nil)
	w = httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestIntegration_SlackWebhook(t *testing.T) {
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "slack"
	global.Defaults.Channel.Slack = &config.SlackConfig{
		BotToken:  "${SLACK_BOT_TOKEN}",
		AppToken:  "${SLACK_APP_TOKEN}",
		ChannelID: "C123",
	}
	global.Defaults.Agent.Type = "claude"

	pylons := map[string]*config.PylonConfig{
		"pr-reviewer": {
			Name:      "pr-reviewer",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/github-pr"},
			Workspace: config.WorkspaceConfig{Type: "git-clone"},
			Agent:     &config.PylonAgent{Prompt: "Review this PR"},
		},
	}

	d := oobeDaemon(t, global, pylons)

	// Verify webhook route
	body := `{"action":"opened","pull_request":{"title":"Fix bug"}}`
	req := httptest.NewRequest(http.MethodPost, "/github-pr", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Verify config resolution
	pyl := pylons["pr-reviewer"]
	chType, _, sl := pyl.ResolveChannel(global)
	assert.Equal(t, "slack", chType)
	assert.NotNil(t, sl)
	assert.Equal(t, "C123", sl.ChannelID)
}

func TestIntegration_SlackCron(t *testing.T) {
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "slack"
	global.Defaults.Channel.Slack = &config.SlackConfig{
		BotToken:  "${SLACK_BOT_TOKEN}",
		AppToken:  "${SLACK_APP_TOKEN}",
		ChannelID: "C123",
	}
	global.Defaults.Agent.Type = "opencode"

	pylons := map[string]*config.PylonConfig{
		"daily-digest": {
			Name: "daily-digest",
			Trigger: config.TriggerConfig{
				Type:     "cron",
				Cron:     "0 8 * * 1-5",
				Timezone: "UTC",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "Summarize daily activity"},
		},
	}

	d := oobeDaemon(t, global, pylons)

	pyl := pylons["daily-digest"]
	assert.Equal(t, "cron", pyl.Trigger.Type)

	chType, _, sl := pyl.ResolveChannel(global)
	assert.Equal(t, "slack", chType)
	assert.NotNil(t, sl)

	// Verify trigger route works
	req := httptest.NewRequest(http.MethodPost, "/trigger/daily-digest", nil)
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Verify the channel is reachable
	ch := d.channelFor("daily-digest")
	assert.True(t, ch.Ready())
}

func TestIntegration_PerPylonChannelOverride(t *testing.T) {
	// Global defaults to Slack, but one pylon overrides to Telegram
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "slack"
	global.Defaults.Channel.Slack = &config.SlackConfig{
		BotToken:  "${SLACK_BOT_TOKEN}",
		AppToken:  "${SLACK_APP_TOKEN}",
		ChannelID: "C123",
	}

	pylons := map[string]*config.PylonConfig{
		"slack-pylon": {
			Name:      "slack-pylon",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/slack-pylon"},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
			// No channel override -- uses global Slack
		},
		"telegram-pylon": {
			Name:    "telegram-pylon",
			Trigger: config.TriggerConfig{Type: "webhook", Path: "/telegram-pylon"},
			Channel: &config.PylonChannel{
				Type: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   999,
				},
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	_ = oobeDaemon(t, global, pylons)

	// slack-pylon should resolve to Slack (from global)
	chType, _, sl := pylons["slack-pylon"].ResolveChannel(global)
	assert.Equal(t, "slack", chType)
	assert.NotNil(t, sl)
	assert.Equal(t, "C123", sl.ChannelID)

	// telegram-pylon should resolve to Telegram (per-pylon override)
	chType, tg, _ := pylons["telegram-pylon"].ResolveChannel(global)
	assert.Equal(t, "telegram", chType)
	assert.NotNil(t, tg)
	assert.Equal(t, int64(999), tg.ChatID)
}

func TestIntegration_ConfigRoundTrip(t *testing.T) {
	// Simulate the full OOBE flow: save config -> load config -> create daemon
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	// Step 1: Save global config (like setup wizard does)
	global := &config.GlobalConfig{
		Version: 1,
		Server:  config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker:  config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "telegram"
	global.Defaults.Channel.Telegram = &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   111,
	}
	global.Defaults.Agent.Type = "claude"
	require.NoError(t, config.SaveGlobal(global))

	// Step 2: Save pylon config (like construct wizard does)
	pyl := &config.PylonConfig{
		Name:      "roundtrip-test",
		Trigger:   config.TriggerConfig{Type: "webhook", Path: "/roundtrip"},
		Workspace: config.WorkspaceConfig{Type: "none"},
		Agent:     &config.PylonAgent{Prompt: "Test prompt"},
	}
	require.NoError(t, config.SavePylon(pyl))

	// Step 3: Load configs back (validates YAML round-trip)
	loadedGlobal, err := config.LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "telegram", loadedGlobal.Defaults.Channel.Type)
	assert.Equal(t, 8080, loadedGlobal.Server.Port)

	loadedPylon, err := config.LoadPylon("roundtrip-test")
	require.NoError(t, err)
	assert.Equal(t, "webhook", loadedPylon.Trigger.Type)
	assert.Equal(t, "/roundtrip", loadedPylon.Trigger.Path)

	// Step 4: Create daemon from loaded configs
	d := oobeDaemon(t, loadedGlobal, map[string]*config.PylonConfig{
		"roundtrip-test": loadedPylon,
	})

	// Step 5: Verify the daemon works
	body := `{"test":"data"}`
	req := httptest.NewRequest(http.MethodPost, "/roundtrip", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestIntegration_MultiplePylons(t *testing.T) {
	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	global.Defaults.Channel.Type = "telegram"
	global.Defaults.Channel.Telegram = &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   123,
	}
	global.Defaults.Agent.Type = "claude"

	pylons := map[string]*config.PylonConfig{
		"webhook-pylon": {
			Name:      "webhook-pylon",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/webhook"},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "webhook prompt"},
		},
		"cron-pylon": {
			Name: "cron-pylon",
			Trigger: config.TriggerConfig{
				Type:     "cron",
				Cron:     "0 9 * * *",
				Timezone: "UTC",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "cron prompt"},
		},
	}

	d := oobeDaemon(t, global, pylons)

	// Webhook pylon should be reachable
	body := `{"event":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Cron pylon has no webhook route, but trigger route works
	req = httptest.NewRequest(http.MethodPost, "/trigger/cron-pylon", nil)
	w = httptest.NewRecorder()
	d.Mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)

	// Both pylons share the same channel
	assert.Equal(t, d.channelFor("webhook-pylon"), d.channelFor("cron-pylon"))
}
