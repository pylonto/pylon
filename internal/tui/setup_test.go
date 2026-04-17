package tui

import (
	"os"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSetupValues builds a complete values map for a setup wizard run.
// channel: "telegram", "slack", "stdout", "webhook"
// agent: "claude", "opencode"
func makeSetupValues(channel, agent string) map[string]string {
	v := map[string]string{
		"docker_check": "Docker 24.0",
		"channel":      channel,
		"agent":        agent,
		"public_url":   "",
		"confirm":      "yes",
	}

	switch channel {
	case "telegram":
		v["channel.tg_token"] = "test-tg-token-123"
		v["channel.tg_verify"] = "Verified @testbot"
		v["channel.tg_chat_method"] = "manual"
	case "slack":
		v["channel.slack_manifest"] = ""
		v["channel.slack_install"] = ""
		v["channel.slack_socket"] = ""
		v["channel.slack_bot_token"] = "xoxb-test-bot-token"
		v["channel.slack_verify_bot"] = "Bot token entered"
		v["channel.slack_app_token"] = "xapp-test-app-token"
		v["channel.slack_channel"] = "C1234567890"
	}

	switch agent {
	case "claude":
		v["agent.claude_auth"] = "api_key"
	case "opencode":
		v["agent.opencode_auth"] = "none"
	}

	return v
}

func TestSetupOnComplete(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		agent   string
		check   func(t *testing.T, cfg *config.GlobalConfig)
	}{
		{
			name:    "telegram + claude api_key",
			channel: "telegram",
			agent:   "claude",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "telegram", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Telegram)
				assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", cfg.Defaults.Channel.Telegram.BotToken)
				assert.Equal(t, "claude", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.Claude)
				assert.Equal(t, "api_key", cfg.Defaults.Agent.Claude.Auth)
			},
		},
		{
			name:    "telegram + opencode",
			channel: "telegram",
			agent:   "opencode",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "telegram", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Telegram)
				assert.Equal(t, "opencode", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.OpenCode)
				assert.Equal(t, "none", cfg.Defaults.Agent.OpenCode.Auth)
			},
		},
		{
			name:    "slack + claude api_key",
			channel: "slack",
			agent:   "claude",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "slack", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Slack)
				assert.Equal(t, "${SLACK_BOT_TOKEN}", cfg.Defaults.Channel.Slack.BotToken)
				assert.Equal(t, "${SLACK_APP_TOKEN}", cfg.Defaults.Channel.Slack.AppToken)
				assert.Equal(t, "C1234567890", cfg.Defaults.Channel.Slack.ChannelID)
				assert.Equal(t, "claude", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.Claude)
			},
		},
		{
			name:    "slack + opencode",
			channel: "slack",
			agent:   "opencode",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "slack", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Slack)
				assert.Equal(t, "opencode", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.OpenCode)
			},
		},
		{
			name:    "stdout + claude",
			channel: "stdout",
			agent:   "claude",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "stdout", cfg.Defaults.Channel.Type)
				assert.Nil(t, cfg.Defaults.Channel.Telegram)
				assert.Nil(t, cfg.Defaults.Channel.Slack)
				assert.Equal(t, "claude", cfg.Defaults.Agent.Type)
			},
		},
		{
			name:    "webhook + claude",
			channel: "webhook",
			agent:   "claude",
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "webhook", cfg.Defaults.Channel.Type)
				assert.Nil(t, cfg.Defaults.Channel.Telegram)
				assert.Nil(t, cfg.Defaults.Channel.Slack)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			// Clear env tokens so setupOnComplete uses values from the map,
			// but set them to a non-empty value for validation to pass.
			t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
			t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")
			t.Setenv("SLACK_APP_TOKEN", "xapp-test")

			values := makeSetupValues(tt.channel, tt.agent)
			err := setupOnComplete(values)
			require.NoError(t, err)

			cfg, err := config.LoadGlobal()
			require.NoError(t, err)

			// Common assertions
			assert.Equal(t, 1, cfg.Version)
			assert.Equal(t, 8080, cfg.Server.Port)
			assert.Equal(t, "0.0.0.0", cfg.Server.Host)
			assert.Equal(t, 3, cfg.Docker.MaxConcurrent)
			assert.Equal(t, "15m", cfg.Docker.DefaultTimeout)

			tt.check(t, cfg)
		})
	}
}

func TestSetupOnComplete_PublicURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	values := makeSetupValues("stdout", "claude")
	values["public_url"] = "https://agent-arnold.app/"

	err := setupOnComplete(values)
	require.NoError(t, err)

	cfg, err := config.LoadGlobal()
	require.NoError(t, err)
	assert.Equal(t, "https://agent-arnold.app", cfg.Server.PublicURL, "trailing slash should be trimmed")
}

func TestSetupOnComplete_ConfirmNo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	values := makeSetupValues("telegram", "claude")
	values["confirm"] = "no"

	err := setupOnComplete(values)
	require.NoError(t, err)

	// Config should not exist
	assert.False(t, config.GlobalExists(), "config file should not be created when confirm=no")
}

func TestSetupOnComplete_SlackWritesGlobalEnv(t *testing.T) {
	// Regression: `pylon nexus` setup flow must continue to write Slack tokens
	// to the GLOBAL .env (not per-pylon). Per-pylon .env writing is the job
	// of `pylon construct`, which is a separate flow.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	values := makeSetupValues("slack", "claude")
	require.NoError(t, setupOnComplete(values))

	data, err := os.ReadFile(config.EnvPath())
	require.NoError(t, err)
	assert.Contains(t, string(data), "SLACK_BOT_TOKEN=xoxb-test-bot-token")
	assert.Contains(t, string(data), "SLACK_APP_TOKEN=xapp-test-app-token")
}

func TestSetupOnComplete_EnvTokenPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token-from-env")

	values := makeSetupValues("telegram", "claude")
	values["channel.tg_token"] = "token-from-wizard"

	err := setupOnComplete(values)
	require.NoError(t, err)

	// The env token should take precedence -- verify the env file was written
	// with the env token, not the wizard-entered token
	data, err := os.ReadFile(config.EnvPath())
	require.NoError(t, err)
	assert.Contains(t, string(data), "env-token-from-env")
}

// --- onStepDone branching tests ---

func TestSetupOnStepDone(t *testing.T) {
	t.Run("channel=telegram returns telegram steps", func(t *testing.T) {
		steps := setupOnStepDone("channel", "telegram", nil)
		require.Len(t, steps, 3)
		assert.Equal(t, "channel.tg_token", steps[0].Key)
		assert.Equal(t, "channel.tg_verify", steps[1].Key)
		assert.Equal(t, "channel.tg_chat_method", steps[2].Key)
	})

	t.Run("channel=slack returns slack steps", func(t *testing.T) {
		steps := setupOnStepDone("channel", "slack", nil)
		require.Len(t, steps, 7)
		assert.Equal(t, "channel.slack_manifest", steps[0].Key)
		assert.Equal(t, "channel.slack_install", steps[1].Key)
		assert.Equal(t, "channel.slack_socket", steps[2].Key)
		assert.Equal(t, "channel.slack_bot_token", steps[3].Key)
		assert.Equal(t, "channel.slack_verify_bot", steps[4].Key)
		assert.Equal(t, "channel.slack_app_token", steps[5].Key)
		assert.Equal(t, "channel.slack_channel", steps[6].Key)
	})

	t.Run("channel=stdout returns no steps", func(t *testing.T) {
		steps := setupOnStepDone("channel", "stdout", nil)
		assert.Nil(t, steps)
	})

	t.Run("channel=webhook returns no steps", func(t *testing.T) {
		steps := setupOnStepDone("channel", "webhook", nil)
		assert.Nil(t, steps)
	})

	t.Run("agent=claude returns auth step", func(t *testing.T) {
		steps := setupOnStepDone("agent", "claude", nil)
		require.Len(t, steps, 1)
		assert.Equal(t, "agent.claude_auth", steps[0].Key)
	})

	t.Run("agent=opencode returns auth step", func(t *testing.T) {
		steps := setupOnStepDone("agent", "opencode", nil)
		require.Len(t, steps, 1)
		assert.Equal(t, "agent.opencode_auth", steps[0].Key)
	})

	t.Run("unrelated key returns no steps", func(t *testing.T) {
		steps := setupOnStepDone("confirm", "yes", nil)
		assert.Nil(t, steps)
	})
}

func TestChannelSteps(t *testing.T) {
	t.Run("telegram steps create valid step instances", func(t *testing.T) {
		steps := channelSteps("telegram")
		require.Len(t, steps, 3)
		for _, s := range steps {
			step := s.Create(nil)
			assert.NotNil(t, step)
			assert.NotEmpty(t, step.Title())
		}
	})

	t.Run("slack steps create valid step instances", func(t *testing.T) {
		steps := channelSteps("slack")
		require.Len(t, steps, 7)
		for _, s := range steps {
			step := s.Create(nil)
			assert.NotNil(t, step)
			assert.NotEmpty(t, step.Title())
		}
	})
}

func TestAgentSteps(t *testing.T) {
	t.Run("claude step has correct options", func(t *testing.T) {
		steps := agentSteps("claude")
		require.Len(t, steps, 1)
		step := steps[0].Create(nil)
		assert.Contains(t, step.Title(), "Claude")
	})

	t.Run("opencode step has correct options", func(t *testing.T) {
		steps := agentSteps("opencode")
		require.Len(t, steps, 1)
		step := steps[0].Create(nil)
		assert.Contains(t, step.Title(), "OpenCode")
	})

	t.Run("unknown agent returns nil", func(t *testing.T) {
		steps := agentSteps("unknown")
		assert.Nil(t, steps)
	})
}
