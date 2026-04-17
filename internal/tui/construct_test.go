package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeConstructValues builds a complete values map for a construct wizard run.
// channel: "telegram", "slack", "default"
// trigger: "webhook", "cron"
func makeConstructValues(name, channel, trigger string) map[string]string {
	v := map[string]string{
		"name":           name,
		"description":    "Test pylon for " + channel + " " + trigger,
		"trigger":        trigger,
		"workspace":      "none",
		"channel_choice": channel,
		"agent_choice":   "default",
		"prompt":         "Test prompt: {{ .body.error }}",
		"approval":       "yes",
		"confirm":        "yes",
	}

	switch trigger {
	case "webhook":
		v["trigger.path"] = "/" + name
	case "cron":
		v["trigger.cron"] = "0 9 * * 1-5"
		v["trigger.timezone"] = "America/New_York"
	}

	switch channel {
	case "telegram":
		// Raw token: wizard now collects real values and writes them to the
		// per-pylon .env. The YAML still references ${TELEGRAM_BOT_TOKEN}.
		v["channel_choice.tg_token"] = "123:abc-telegram-test"
		v["channel_choice.tg_verify_bot"] = "Verified @testbot"
		v["channel_choice.tg_chat_method"] = "manual"
		v["channel_choice.tg_chat_id"] = "123456789"
	case "slack":
		v["channel_choice.slack_manifest"] = ""
		v["channel_choice.slack_install"] = ""
		v["channel_choice.slack_socket"] = ""
		v["channel_choice.slack_bot_token"] = "xoxb-test-bot"
		v["channel_choice.slack_verify_bot"] = "Verified @testbot"
		v["channel_choice.slack_app_token"] = "xapp-test-app"
		v["channel_choice.slack_channel_method"] = "manual"
		v["channel_choice.slack_channel_id"] = "C9876543210"
	}

	return v
}

// saveTestGlobal creates a minimal global config in the temp HOME for construct tests.
func saveTestGlobal(t *testing.T) {
	t.Helper()
	cfg := &config.GlobalConfig{
		Version: 1,
		Server:  config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker:  config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}
	cfg.Defaults.Channel.Type = "stdout"
	cfg.Defaults.Agent.Type = "claude"
	require.NoError(t, config.SaveGlobal(cfg))
}

func TestConstructOnComplete(t *testing.T) {
	tests := []struct {
		name    string
		pylon   string
		channel string
		trigger string
		check   func(t *testing.T, pyl *config.PylonConfig)
	}{
		{
			name:    "telegram + webhook",
			pylon:   "tg-webhook",
			channel: "telegram",
			trigger: "webhook",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/tg-webhook", pyl.Trigger.Path)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "telegram", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Telegram)
				assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", pyl.Channel.Telegram.BotToken)
				assert.Equal(t, int64(123456789), pyl.Channel.Telegram.ChatID)
			},
		},
		{
			name:    "telegram + cron",
			pylon:   "tg-cron",
			channel: "telegram",
			trigger: "cron",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "cron", pyl.Trigger.Type)
				assert.Equal(t, "0 9 * * 1-5", pyl.Trigger.Cron)
				assert.Equal(t, "America/New_York", pyl.Trigger.Timezone)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "telegram", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Telegram)
			},
		},
		{
			name:    "slack + webhook",
			pylon:   "sl-webhook",
			channel: "slack",
			trigger: "webhook",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				assert.Equal(t, "/sl-webhook", pyl.Trigger.Path)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "slack", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Slack)
				assert.Equal(t, "${SLACK_BOT_TOKEN}", pyl.Channel.Slack.BotToken)
				assert.Equal(t, "${SLACK_APP_TOKEN}", pyl.Channel.Slack.AppToken)
				assert.Equal(t, "C9876543210", pyl.Channel.Slack.ChannelID)
			},
		},
		{
			name:    "slack + cron",
			pylon:   "sl-cron",
			channel: "slack",
			trigger: "cron",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "cron", pyl.Trigger.Type)
				assert.Equal(t, "0 9 * * 1-5", pyl.Trigger.Cron)
				assert.Equal(t, "America/New_York", pyl.Trigger.Timezone)

				require.NotNil(t, pyl.Channel)
				assert.Equal(t, "slack", pyl.Channel.Type)
				require.NotNil(t, pyl.Channel.Slack)
			},
		},
		{
			name:    "default channel + webhook",
			pylon:   "default-webhook",
			channel: "default",
			trigger: "webhook",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "webhook", pyl.Trigger.Type)
				// Default channel -- no per-pylon channel override at all
				assert.Nil(t, pyl.Channel, "default channel with auto-run should not create channel section")
			},
		},
		{
			name:    "default channel + cron",
			pylon:   "default-cron",
			channel: "default",
			trigger: "cron",
			check: func(t *testing.T, pyl *config.PylonConfig) {
				assert.Equal(t, "cron", pyl.Trigger.Type)
				assert.Equal(t, "0 9 * * 1-5", pyl.Trigger.Cron)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			saveTestGlobal(t)

			values := makeConstructValues(tt.pylon, tt.channel, tt.trigger)
			err := constructOnComplete(values)
			require.NoError(t, err)

			// Use LoadPylonRaw to avoid validation of env var references
			// (e.g. ${TELEGRAM_BOT_TOKEN}) which aren't set in test env.
			pyl, err := config.LoadPylonRaw(tt.pylon)
			require.NoError(t, err)

			// Common assertions
			assert.Equal(t, tt.pylon, pyl.Name)
			assert.Contains(t, pyl.Description, tt.channel)
			assert.Equal(t, "none", pyl.Workspace.Type)
			require.NotNil(t, pyl.Agent)
			assert.Equal(t, "Test prompt: {{ .body.error }}", pyl.Agent.Prompt)

			tt.check(t, pyl)
		})
	}
}

func TestConstructOnComplete_WritesPerPylonEnv(t *testing.T) {
	// Regression for the unified wizard: construct now collects real tokens
	// and persists them to the per-pylon .env so the pylon is self-contained.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Clear env so secretsFromValues doesn't mistake inherited tokens for new ones.
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")
	saveTestGlobal(t)

	values := makeConstructValues("nexus-slack", "slack", "webhook")
	require.NoError(t, constructOnComplete(values))

	envPath := filepath.Join(home, ".pylon", "pylons", "nexus-slack", ".env")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err, "per-pylon .env should be scaffolded")
	assert.Contains(t, string(data), "SLACK_BOT_TOKEN=xoxb-test-bot")
	assert.Contains(t, string(data), "SLACK_APP_TOKEN=xapp-test-app")

	// YAML keeps ${VAR} references so runtime resolves tokens through the
	// .env rather than baking the raw secret into the config file.
	pyl, err := config.LoadPylonRaw("nexus-slack")
	require.NoError(t, err)
	require.NotNil(t, pyl.Channel)
	require.NotNil(t, pyl.Channel.Slack)
	assert.Equal(t, "${SLACK_BOT_TOKEN}", pyl.Channel.Slack.BotToken)
	assert.Equal(t, "${SLACK_APP_TOKEN}", pyl.Channel.Slack.AppToken)
	assert.Equal(t, "C9876543210", pyl.Channel.Slack.ChannelID)
}

func TestConstructOnComplete_WritesPerPylonEnv_Telegram(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	saveTestGlobal(t)

	values := makeConstructValues("nexus-tg", "telegram", "cron")
	require.NoError(t, constructOnComplete(values))

	envPath := filepath.Join(home, ".pylon", "pylons", "nexus-tg", ".env")
	data, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "TELEGRAM_BOT_TOKEN=123:abc-telegram-test")

	pyl, err := config.LoadPylonRaw("nexus-tg")
	require.NoError(t, err)
	require.NotNil(t, pyl.Channel)
	require.NotNil(t, pyl.Channel.Telegram)
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", pyl.Channel.Telegram.BotToken)
	assert.Equal(t, int64(123456789), pyl.Channel.Telegram.ChatID)
}

func TestConstructOnComplete_ApprovalEnabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveTestGlobal(t)

	values := makeConstructValues("approval-test", "telegram", "webhook")
	values["approval"] = "no" // "no" to auto-run means approval IS required
	values["approval.topic"] = "{{ .body.issue.title }}"
	values["approval.message"] = "Alert: {{ .body.error }}"

	err := constructOnComplete(values)
	require.NoError(t, err)

	pyl, err := config.LoadPylonRaw("approval-test")
	require.NoError(t, err)

	require.NotNil(t, pyl.Channel)
	assert.True(t, pyl.Channel.Approval)
	assert.Equal(t, "{{ .body.issue.title }}", pyl.Channel.Topic)
	assert.Equal(t, "Alert: {{ .body.error }}", pyl.Channel.Message)
}

func TestConstructWizardModel_ExitState(t *testing.T) {
	// Contract: Saved is true only when the user confirmed save; Err is set
	// when constructOnComplete fails. The CLI depends on these flags to
	// distinguish success, cancel, and failure without scraping stdout.
	t.Run("Saved=false when confirm=no", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		saveTestGlobal(t)

		m := NewConstructWizard("declined")
		m.wiz.values = makeConstructValues("declined", "default", "webhook")
		m.wiz.values["confirm"] = "no"
		// Synthesize the completion message the wizard would emit after
		// running onComplete with confirm=no (which returns nil without
		// saving).
		_, _ = m.Update(wizardCompleteMsg{})
		assert.False(t, m.Saved, "confirm=no must not report Saved")
		assert.NoError(t, m.Err)
	})

	t.Run("Saved=true when confirm=yes", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		saveTestGlobal(t)

		m := NewConstructWizard("accepted")
		m.wiz.values = makeConstructValues("accepted", "default", "webhook")
		_, _ = m.Update(wizardCompleteMsg{})
		assert.True(t, m.Saved)
		assert.NoError(t, m.Err)
	})
}

func TestConstructOnComplete_ConfirmNo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveTestGlobal(t)

	values := makeConstructValues("no-save", "telegram", "webhook")
	values["confirm"] = "no"

	err := constructOnComplete(values)
	require.NoError(t, err)

	// Pylon should not be saved
	_, err = config.LoadPylonRaw("no-save")
	assert.Error(t, err, "pylon should not exist when confirm=no")
}

func TestConstructOnComplete_EmptyName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	values := makeConstructValues("", "telegram", "webhook")
	err := constructOnComplete(values)
	assert.Error(t, err, "empty name should fail")
}

func TestConstructOnComplete_WebhookPathDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveTestGlobal(t)

	t.Run("empty path defaults to /name", func(t *testing.T) {
		values := makeConstructValues("path-test", "default", "webhook")
		values["trigger.path"] = ""
		err := constructOnComplete(values)
		require.NoError(t, err)

		pyl, err := config.LoadPylonRaw("path-test")
		require.NoError(t, err)
		assert.Equal(t, "/path-test", pyl.Trigger.Path)
	})

	t.Run("path without leading slash gets one", func(t *testing.T) {
		values := makeConstructValues("slash-test", "default", "webhook")
		values["trigger.path"] = "my-webhook"
		err := constructOnComplete(values)
		require.NoError(t, err)

		pyl, err := config.LoadPylonRaw("slash-test")
		require.NoError(t, err)
		assert.Equal(t, "/my-webhook", pyl.Trigger.Path)
	})
}

func TestConstructOnComplete_WorkspaceTypes(t *testing.T) {
	t.Run("git-clone workspace", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		saveTestGlobal(t)

		values := makeConstructValues("git-pylon", "default", "webhook")
		values["workspace"] = "git-clone"
		values["workspace.repo"] = "git@github.com:test/repo.git"
		values["workspace.ref"] = "main"

		err := constructOnComplete(values)
		require.NoError(t, err)

		pyl, err := config.LoadPylonRaw("git-pylon")
		require.NoError(t, err)
		assert.Equal(t, "git-clone", pyl.Workspace.Type)
		assert.Equal(t, "git@github.com:test/repo.git", pyl.Workspace.Repo)
		assert.Equal(t, "main", pyl.Workspace.Ref)
	})

	t.Run("local workspace", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		saveTestGlobal(t)

		values := makeConstructValues("local-pylon", "default", "webhook")
		values["workspace"] = "local"
		values["workspace.path"] = "/home/user/project"

		err := constructOnComplete(values)
		require.NoError(t, err)

		pyl, err := config.LoadPylonRaw("local-pylon")
		require.NoError(t, err)
		assert.Equal(t, "local", pyl.Workspace.Type)
		assert.Equal(t, "/home/user/project", pyl.Workspace.Path)
	})
}

func TestConstructOnComplete_AgentOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saveTestGlobal(t)

	values := makeConstructValues("agent-override", "default", "webhook")
	values["agent_choice"] = "opencode"

	err := constructOnComplete(values)
	require.NoError(t, err)

	pyl, err := config.LoadPylonRaw("agent-override")
	require.NoError(t, err)
	require.NotNil(t, pyl.Agent)
	assert.Equal(t, "opencode", pyl.Agent.Type)
}

// --- onStepDone branching tests ---

func TestConstructOnStepDone(t *testing.T) {
	t.Run("trigger=webhook returns path + public_url steps", func(t *testing.T) {
		steps := constructOnStepDone("trigger", "webhook", nil)
		require.Len(t, steps, 2)
		assert.Equal(t, "trigger.path", steps[0].Key)
		assert.Equal(t, "trigger.public_url", steps[1].Key)
	})

	t.Run("trigger=cron returns cron + timezone steps", func(t *testing.T) {
		steps := constructOnStepDone("trigger", "cron", nil)
		require.Len(t, steps, 2)
		assert.Equal(t, "trigger.cron", steps[0].Key)
		assert.Equal(t, "trigger.timezone", steps[1].Key)
	})

	t.Run("workspace=git-clone returns repo + ref steps", func(t *testing.T) {
		steps := constructOnStepDone("workspace", "git-clone", nil)
		require.Len(t, steps, 2)
		assert.Equal(t, "workspace.repo", steps[0].Key)
		assert.Equal(t, "workspace.ref", steps[1].Key)
	})

	t.Run("workspace=git-worktree returns repo + ref steps", func(t *testing.T) {
		steps := constructOnStepDone("workspace", "git-worktree", nil)
		require.Len(t, steps, 2)
		assert.Equal(t, "workspace.repo", steps[0].Key)
		assert.Equal(t, "workspace.ref", steps[1].Key)
	})

	t.Run("workspace=local returns path step", func(t *testing.T) {
		steps := constructOnStepDone("workspace", "local", nil)
		require.Len(t, steps, 1)
		assert.Equal(t, "workspace.path", steps[0].Key)
	})

	t.Run("workspace=none returns no steps", func(t *testing.T) {
		steps := constructOnStepDone("workspace", "none", nil)
		assert.Nil(t, steps)
	})

	t.Run("channel_choice=telegram returns full step-by-step flow", func(t *testing.T) {
		steps := constructOnStepDone("channel_choice", "telegram", nil)
		require.Len(t, steps, 5)
		assert.Equal(t, "channel_choice.tg_token_info", steps[0].Key)
		assert.Equal(t, "channel_choice.tg_token", steps[1].Key)
		assert.Equal(t, "channel_choice.tg_verify_bot", steps[2].Key)
		assert.Equal(t, "channel_choice.tg_chat_method", steps[3].Key)
		assert.Equal(t, "channel_choice.tg_chat_id", steps[4].Key)
	})

	t.Run("channel_choice=slack returns full step-by-step flow", func(t *testing.T) {
		steps := constructOnStepDone("channel_choice", "slack", nil)
		require.Len(t, steps, 9)
		assert.Equal(t, "channel_choice.slack_manifest", steps[0].Key)
		assert.Equal(t, "channel_choice.slack_install", steps[1].Key)
		assert.Equal(t, "channel_choice.slack_bot_token_info", steps[2].Key)
		assert.Equal(t, "channel_choice.slack_bot_token", steps[3].Key)
		assert.Equal(t, "channel_choice.slack_verify_bot", steps[4].Key)
		assert.Equal(t, "channel_choice.slack_app_token_info", steps[5].Key)
		assert.Equal(t, "channel_choice.slack_app_token", steps[6].Key)
		assert.Equal(t, "channel_choice.slack_channel_method", steps[7].Key)
		assert.Equal(t, "channel_choice.slack_channel_id", steps[8].Key)
	})

	t.Run("channel_choice=default returns no steps", func(t *testing.T) {
		steps := constructOnStepDone("channel_choice", "default", nil)
		assert.Nil(t, steps)
	})

	t.Run("approval=no returns topic + message steps", func(t *testing.T) {
		steps := constructOnStepDone("approval", "no", nil)
		require.Len(t, steps, 2)
		assert.Equal(t, "approval.topic", steps[0].Key)
		assert.Equal(t, "approval.message", steps[1].Key)
	})

	t.Run("approval=yes returns no steps", func(t *testing.T) {
		steps := constructOnStepDone("approval", "yes", nil)
		assert.Nil(t, steps)
	})
}

func TestTriggerSteps(t *testing.T) {
	t.Run("webhook step creates valid instance", func(t *testing.T) {
		steps := triggerSteps("webhook")
		require.Len(t, steps, 2)
		step := steps[0].Create(nil)
		assert.NotNil(t, step)
		assert.Contains(t, step.Title(), "Webhook")
	})

	t.Run("cron steps create valid instances", func(t *testing.T) {
		steps := triggerSteps("cron")
		require.Len(t, steps, 2)
		for _, s := range steps {
			step := s.Create(nil)
			assert.NotNil(t, step)
			assert.NotEmpty(t, step.Title())
		}
	})

	t.Run("unknown trigger returns nil", func(t *testing.T) {
		steps := triggerSteps("unknown")
		assert.Nil(t, steps)
	})
}

func TestConstructChannelSteps(t *testing.T) {
	t.Run("telegram steps create valid instances", func(t *testing.T) {
		steps := constructChannelSteps("telegram")
		require.Len(t, steps, 5)
		for _, s := range steps {
			step := s.Create(nil)
			assert.NotNil(t, step)
		}
	})

	t.Run("slack steps create valid instances", func(t *testing.T) {
		steps := constructChannelSteps("slack")
		require.Len(t, steps, 9)
		for _, s := range steps {
			step := s.Create(nil)
			assert.NotNil(t, step)
		}
	})

	t.Run("default returns nil", func(t *testing.T) {
		steps := constructChannelSteps("default")
		assert.Nil(t, steps)
	})
}
