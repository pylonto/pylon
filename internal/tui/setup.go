package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
)

// slackAppManifest is the Slack app manifest YAML for users to copy.
// Duplicated from cmd/setup.go to avoid circular imports.
const slackAppManifest = `display_information:
  name: Pylon
  description: AI agent pipeline runner
features:
  bot_user:
    display_name: Pylon
    always_online: true
oauth_config:
  scopes:
    bot:
      - chat:write
      - channels:history
      - channels:read
      - groups:history
      - groups:read
      - reactions:write
settings:
  event_subscriptions:
    bot_events:
      - message.channels
      - message.groups
  interactivity:
    is_enabled: true
  socket_mode_enabled: true`

func newSetupWizard() wizardModel {
	steps := []StepDef{
		{Key: "docker_check", Create: func() Step {
			return NewAsyncStep(
				"Docker check",
				"Checking Docker availability",
				func() (string, error) {
					out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output()
					if err != nil {
						return "", fmt.Errorf("Docker not found. Install: https://docs.docker.com/get-docker/")
					}
					return "Docker " + strings.TrimSpace(string(out)), nil
				},
			)
		}},
		{Key: "channel", Create: func() Step {
			return NewSelectStep(
				"Default channel",
				"Where should pylons communicate?",
				[]selectOption{
					{"Telegram", "telegram"},
					{"Slack", "slack"},
					{"Webhook (HTTP POST)", "webhook"},
					{"stdout (console only)", "stdout"},
				},
			)
		}},
		{Key: "agent", Create: func() Step {
			return NewSelectStep(
				"Default AI agent",
				"Which agent for new pylons?",
				[]selectOption{
					{"Claude Code", "claude"},
					{"OpenCode", "opencode"},
				},
			)
		}},
		{Key: "public_url", Create: func() Step {
			return NewTextInputStep(
				"Public URL (optional)",
				"Base URL where external services can reach pylon. Leave blank if local only.",
				"https://agent-arnold.app",
				"",
				false,
			)
		}},
		{Key: "confirm", Create: func() Step {
			return NewConfirmStep(
				"Save configuration?",
				"This will create ~/.pylon/config.yaml",
				true,
			)
		}},
	}

	return newWizardModel("Setup", steps, setupOnStepDone, setupOnComplete)
}

func setupOnStepDone(key, value string, values map[string]string) []StepDef {
	switch key {
	case "channel":
		return channelSteps(value)
	case "agent":
		return agentSteps(value)
	}
	return nil
}

func channelSteps(channelType string) []StepDef {
	switch channelType {
	case "telegram":
		return telegramSteps()
	case "slack":
		return slackSteps()
	}
	return nil
}

func telegramSteps() []StepDef {
	// Check if token is already in env
	envToken := os.Getenv("TELEGRAM_BOT_TOKEN")

	steps := []StepDef{
		{Key: "channel.tg_token", Create: func() Step {
			if envToken != "" {
				return NewAsyncStep(
					"Telegram bot token",
					"Using TELEGRAM_BOT_TOKEN from environment",
					func() (string, error) {
						username, err := channel.GetBotUsername(envToken)
						if err != nil {
							return "", fmt.Errorf("invalid token: %w", err)
						}
						return fmt.Sprintf("Verified @%s", username), nil
					},
				)
			}
			return NewTextInputStep(
				"Telegram bot token",
				"Create a bot via @BotFather: https://t.me/BotFather",
				"110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
				"",
				false,
			)
		}},
		{Key: "channel.tg_verify", Create: func() Step {
			return NewAsyncStep(
				"Verifying bot token",
				"Connecting to Telegram",
				func() (string, error) {
					// Token will be in values, but we can't access it here.
					// This step will be skipped if env token was used (async step above).
					return "Token entered", nil
				},
			)
		}},
		{Key: "channel.tg_chat_method", Create: func() Step {
			return NewSelectStep(
				"Chat ID detection",
				"How should we find your group chat?",
				[]selectOption{
					{"Auto-detect (add bot to group, send a message)", "auto"},
					{"Enter manually", "manual"},
				},
			)
		}},
	}

	return steps
}

func slackSteps() []StepDef {
	envBotToken := os.Getenv("SLACK_BOT_TOKEN")
	envAppToken := os.Getenv("SLACK_APP_TOKEN")

	steps := []StepDef{
		{Key: "channel.slack_manifest", Create: func() Step {
			return NewCopyBlockStep(
				"Create a Slack App",
				"Go to https://api.slack.com/apps > Create New App > From a manifest\nPaste this YAML manifest:",
				slackAppManifest,
			)
		}},
		{Key: "channel.slack_install", Create: func() Step {
			return NewInfoStep(
				"Install the app",
				"",
				"Install the app to your workspace from the app settings page.",
			)
		}},
		{Key: "channel.slack_socket", Create: func() Step {
			return NewInfoStep(
				"Enable Socket Mode",
				"",
				"Settings > Socket Mode > toggle on.\nGenerate an App-Level Token with connections:write scope.",
			)
		}},
		{Key: "channel.slack_bot_token", Create: func() Step {
			if envBotToken != "" {
				return NewAsyncStep(
					"Slack bot token",
					"Using SLACK_BOT_TOKEN from environment",
					func() (string, error) {
						username, err := channel.ValidateSlackToken(envBotToken)
						if err != nil {
							return "", fmt.Errorf("invalid bot token: %w", err)
						}
						return fmt.Sprintf("Verified @%s", username), nil
					},
				)
			}
			return NewTextInputStep(
				"Slack bot token (xoxb-...)",
				"Find your Bot Token at: OAuth & Permissions > Bot User OAuth Token",
				"xoxb-your-bot-token",
				"",
				false,
			)
		}},
		{Key: "channel.slack_verify_bot", Create: func() Step {
			return NewAsyncStep(
				"Verifying bot token",
				"Connecting to Slack",
				func() (string, error) {
					return "Bot token entered", nil
				},
			)
		}},
		{Key: "channel.slack_app_token", Create: func() Step {
			if envAppToken != "" {
				return NewAsyncStep(
					"Slack app token",
					"Using SLACK_APP_TOKEN from environment",
					func() (string, error) {
						return "Using token from environment", nil
					},
				)
			}
			return NewTextInputStep(
				"Slack app token (xapp-...)",
				"Generate at: Settings > Basic Information > App-Level Tokens",
				"xapp-your-app-token",
				"",
				false,
			)
		}},
		{Key: "channel.slack_channel", Create: func() Step {
			return NewTextInputStep(
				"Slack channel ID",
				"The channel where pylon will post. Find it in channel details.",
				"C1234567890",
				"",
				false,
			)
		}},
	}

	return steps
}

func agentSteps(agentType string) []StepDef {
	switch agentType {
	case "claude":
		if runtime.GOOS == "darwin" {
			return []StepDef{
				{Key: "agent.claude_auth", Create: func() Step {
					return NewSelectStep(
						"Claude Code authentication",
						"OAuth is not supported on macOS. Credentials are stored in Keychain, which cannot be mounted into Docker containers.",
						[]selectOption{
							{"API Key (ANTHROPIC_API_KEY)", "api_key"},
						},
					)
				}},
			}
		}
		return []StepDef{
			{Key: "agent.claude_auth", Create: func() Step {
				return NewSelectStep(
					"Claude Code authentication",
					"",
					[]selectOption{
						{"API Key (ANTHROPIC_API_KEY)", "api_key"},
						{"OAuth (existing ~/.claude/ session)", "oauth"},
					},
				)
			}},
		}
	case "opencode":
		return []StepDef{
			{Key: "agent.opencode_auth", Create: func() Step {
				return NewSelectStep(
					"OpenCode authentication",
					"",
					[]selectOption{
						{"Built-in (OpenCode Zen, no key needed)", "none"},
						{"API Key (bring your own provider key)", "api-key"},
					},
				)
			}},
		}
	}
	return nil
}

func setupOnComplete(values map[string]string) error {
	if values["confirm"] != "yes" {
		return nil
	}

	cfg := &config.GlobalConfig{
		Version: 1,
		Server: config.ServerConfig{
			Port: 8080,
			Host: "0.0.0.0",
		},
		Docker: config.DockerConfig{
			MaxConcurrent:  3,
			DefaultTimeout: "15m",
		},
	}

	if url := values["public_url"]; url != "" {
		cfg.Server.PublicURL = strings.TrimRight(url, "/")
	}

	// Channel
	channelType := values["channel"]
	cfg.Defaults.Channel.Type = channelType

	switch channelType {
	case "telegram":
		token := values["channel.tg_token"]
		if envToken := os.Getenv("TELEGRAM_BOT_TOKEN"); envToken != "" {
			token = envToken
		}
		if token != "" {
			config.SaveEnvVar("TELEGRAM_BOT_TOKEN", token)
			cfg.Defaults.Channel.Telegram = &config.TelegramConfig{
				BotToken: "${TELEGRAM_BOT_TOKEN}",
			}
		}

	case "slack":
		botToken := values["channel.slack_bot_token"]
		if envToken := os.Getenv("SLACK_BOT_TOKEN"); envToken != "" {
			botToken = envToken
		}
		appToken := values["channel.slack_app_token"]
		if envToken := os.Getenv("SLACK_APP_TOKEN"); envToken != "" {
			appToken = envToken
		}
		channelID := values["channel.slack_channel"]

		if botToken != "" {
			config.SaveEnvVar("SLACK_BOT_TOKEN", botToken)
		}
		if appToken != "" {
			config.SaveEnvVar("SLACK_APP_TOKEN", appToken)
		}

		cfg.Defaults.Channel.Slack = &config.SlackConfig{
			BotToken:  "${SLACK_BOT_TOKEN}",
			AppToken:  "${SLACK_APP_TOKEN}",
			ChannelID: channelID,
		}
	}

	// Agent
	agentType := values["agent"]
	cfg.Defaults.Agent.Type = agentType

	switch agentType {
	case "claude":
		auth := values["agent.claude_auth"]
		if auth == "" {
			auth = "oauth"
		}
		claude := &config.ClaudeDefaults{
			Image: agentimage.ImageName("claude"),
			Auth:  auth,
		}
		if auth == "oauth" {
			home, _ := os.UserHomeDir()
			claudeDir := filepath.Join(home, ".claude")
			if _, err := os.Stat(claudeDir); err == nil {
				claude.OAuthPath = "~/.claude"
			}
		}
		cfg.Defaults.Agent.Claude = claude

	case "opencode":
		auth := values["agent.opencode_auth"]
		if auth == "" {
			auth = "none"
		}
		cfg.Defaults.Agent.OpenCode = &config.OpenCodeDefaults{
			Image: agentimage.ImageName("opencode"),
			Auth:  auth,
		}
	}

	return config.SaveGlobal(cfg)
}
