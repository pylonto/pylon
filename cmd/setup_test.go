package cmd

import (
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGlobalConfig(t *testing.T) {
	tests := []struct {
		name  string
		input setupInputs
		check func(t *testing.T, cfg *config.GlobalConfig)
	}{
		{
			name: "telegram + claude",
			input: setupInputs{
				ChannelChoice: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   123456,
				},
				AgentChoice: "claude",
				Claude: &config.ClaudeDefaults{
					Image: "pylon-claude:latest",
					Auth:  "api_key",
				},
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "telegram", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Telegram)
				assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", cfg.Defaults.Channel.Telegram.BotToken)
				assert.Equal(t, int64(123456), cfg.Defaults.Channel.Telegram.ChatID)
				assert.Nil(t, cfg.Defaults.Channel.Slack)

				assert.Equal(t, "claude", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.Claude)
				assert.Equal(t, "api_key", cfg.Defaults.Agent.Claude.Auth)
				assert.Nil(t, cfg.Defaults.Agent.OpenCode)
			},
		},
		{
			name: "slack + opencode",
			input: setupInputs{
				ChannelChoice: "slack",
				Slack: &config.SlackConfig{
					BotToken:  "${SLACK_BOT_TOKEN}",
					AppToken:  "${SLACK_APP_TOKEN}",
					ChannelID: "C999",
				},
				AgentChoice: "opencode",
				OpenCode: &config.OpenCodeDefaults{
					Image: "pylon-opencode:latest",
					Auth:  "none",
				},
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "slack", cfg.Defaults.Channel.Type)
				require.NotNil(t, cfg.Defaults.Channel.Slack)
				assert.Equal(t, "${SLACK_BOT_TOKEN}", cfg.Defaults.Channel.Slack.BotToken)
				assert.Equal(t, "${SLACK_APP_TOKEN}", cfg.Defaults.Channel.Slack.AppToken)
				assert.Equal(t, "C999", cfg.Defaults.Channel.Slack.ChannelID)
				assert.Nil(t, cfg.Defaults.Channel.Telegram)

				assert.Equal(t, "opencode", cfg.Defaults.Agent.Type)
				require.NotNil(t, cfg.Defaults.Agent.OpenCode)
				assert.Equal(t, "none", cfg.Defaults.Agent.OpenCode.Auth)
				assert.Nil(t, cfg.Defaults.Agent.Claude)
			},
		},
		{
			name: "stdout + claude",
			input: setupInputs{
				ChannelChoice: "stdout",
				AgentChoice:   "claude",
				Claude: &config.ClaudeDefaults{
					Image: "pylon-claude:latest",
					Auth:  "oauth",
				},
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "stdout", cfg.Defaults.Channel.Type)
				assert.Nil(t, cfg.Defaults.Channel.Telegram)
				assert.Nil(t, cfg.Defaults.Channel.Slack)
			},
		},
		{
			name: "webhook channel",
			input: setupInputs{
				ChannelChoice: "webhook",
				AgentChoice:   "claude",
				Claude:        &config.ClaudeDefaults{Image: "pylon-claude:latest", Auth: "api_key"},
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "webhook", cfg.Defaults.Channel.Type)
			},
		},
		{
			name: "with public URL",
			input: setupInputs{
				ChannelChoice: "stdout",
				AgentChoice:   "claude",
				Claude:        &config.ClaudeDefaults{Image: "pylon-claude:latest", Auth: "api_key"},
				PublicURL:     "https://my-pylon.app/",
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "https://my-pylon.app", cfg.Server.PublicURL, "trailing slash should be trimmed")
			},
		},
		{
			name: "custom agent",
			input: setupInputs{
				ChannelChoice: "stdout",
				AgentChoice:   "custom",
			},
			check: func(t *testing.T, cfg *config.GlobalConfig) {
				assert.Equal(t, "custom", cfg.Defaults.Agent.Type)
				assert.Nil(t, cfg.Defaults.Agent.Claude)
				assert.Nil(t, cfg.Defaults.Agent.OpenCode)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := buildGlobalConfig(tt.input)

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
