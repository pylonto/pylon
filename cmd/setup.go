package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/proxy"
)

func init() {
	rootCmd.AddCommand(setupCmd)
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive first-time global config",
	RunE:  runSetup,
}

func runSetup(cmd *cobra.Command, args []string) error {
	fmt.Printf("\nPylon Setup\n\n")

	// Check Docker
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		fmt.Printf("Docker .............. ok, found (v%s)\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Println("Docker .............. NOT FOUND")
		fmt.Println("  Install: https://docs.docker.com/engine/install/")
	}

	fmt.Println()

	// Channel selection
	var channelChoice string
	err := huh.NewSelect[string]().
		Title("Default channel -- where should alerts go?").
		Options(
			huh.NewOption("Telegram", "telegram"),
			huh.NewOption("Slack", "slack"),
			huh.NewOption("Webhook (generic HTTP POST)", "webhook"),
			huh.NewOption("stdout (console only)", "stdout"),
		).
		Value(&channelChoice).
		Run()
	if err != nil {
		return err
	}

	cfg := &config.GlobalConfig{
		Version: 1,
		Server:  config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker:  config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}

	switch channelChoice {
	case "telegram":
		tg, err := setupTelegram()
		if err != nil {
			return err
		}
		cfg.Defaults.Channel = config.ChannelDefaults{Type: "telegram", Telegram: tg}
	case "slack":
		sl, err := setupSlack()
		if err != nil {
			return err
		}
		cfg.Defaults.Channel = config.ChannelDefaults{Type: "slack", Slack: sl}
	case "stdout":
		cfg.Defaults.Channel = config.ChannelDefaults{Type: "stdout"}
	case "webhook":
		cfg.Defaults.Channel = config.ChannelDefaults{Type: "webhook"}
	default:
		comingSoon(channelChoice)
		cfg.Defaults.Channel = config.ChannelDefaults{Type: "stdout"}
	}

	fmt.Println()

	// Agent selection
	var agentChoice string
	err = huh.NewSelect[string]().
		Title("Default AI agent for new pylons:").
		Options(
			huh.NewOption("Claude Code", "claude"),
			huh.NewOption("OpenCode", "opencode"),
			huh.NewOption("Custom", "custom"),
		).
		Value(&agentChoice).
		Run()
	if err != nil {
		return err
	}

	switch agentChoice {
	case "claude":
		claude, err := setupClaude()
		if err != nil {
			return err
		}
		cfg.Defaults.Agent = config.AgentDefaults{Type: "claude", Claude: claude}
	case "opencode":
		oc, err := setupOpenCode()
		if err != nil {
			return err
		}
		cfg.Defaults.Agent = config.AgentDefaults{Type: "opencode", OpenCode: oc}
	case "custom":
		cfg.Defaults.Agent = config.AgentDefaults{Type: "custom"}
	default:
		comingSoon(agentChoice)
		cfg.Defaults.Agent = config.AgentDefaults{Type: "claude", Claude: &config.ClaudeDefaults{
			Image: agentimage.ImageName("claude"), Auth: "oauth",
		}}
	}

	agentimage.Ensure(cfg.Defaults.Agent.Type)

	fmt.Println()

	// Public URL (optional)
	var publicURL string
	if err := huh.NewInput().
		Title("Public URL (optional):").
		Description("Base URL where external services (Sentry, GitHub, etc.) can reach pylon.\nLeave blank if you only run locally.").
		Placeholder("https://agent-arnold.app").
		Value(&publicURL).Run(); err != nil {
		return err
	}
	if publicURL != "" {
		cfg.Server.PublicURL = strings.TrimRight(publicURL, "/")
		fmt.Println()
		proxy.PrintHints("/<pylon-path>", cfg.Server.Port)
	}

	if err := config.SaveGlobal(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("\nSetup complete.\n")
	fmt.Printf("  Config saved to %s\n", config.GlobalPath())
	fmt.Printf("  Run `pylon construct <name>` to create your first pylon.\n")

	offerCompletion()

	return nil
}

func setupTelegram() (*config.TelegramConfig, error) {
	var token string
	if envToken := os.Getenv("TELEGRAM_BOT_TOKEN"); envToken != "" {
		fmt.Println("  Using TELEGRAM_BOT_TOKEN from environment")
		token = envToken
	} else {
		fmt.Println("  Create a bot via @BotFather: https://t.me/BotFather")
		fmt.Println("  Send /newbot, pick a name, and copy the token.")
		err := huh.NewInput().
			Title("Telegram bot token:").
			Placeholder("110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw").
			Value(&token).
			Run()
		if err != nil {
			return nil, err
		}
	}

	username, err := channel.GetBotUsername(token)
	if err != nil {
		return nil, fmt.Errorf("invalid bot token: %w", err)
	}
	fmt.Printf("  Verified: @%s\n\n", username)

	var method string
	err = huh.NewSelect[string]().
		Title("How do you want to set the chat ID?").
		Options(
			huh.NewOption("Auto-detect (add bot to group, send a message)", "auto"),
			huh.NewOption("Enter manually", "manual"),
		).
		Value(&method).
		Run()
	if err != nil {
		return nil, err
	}

	var chatID int64
	if method == "auto" {
		chatID, err = detectChatID(token, username)
		if err != nil {
			return nil, err
		}
	} else {
		var chatIDStr string
		err = huh.NewInput().
			Title("Telegram chat ID:").
			Placeholder("-1001234567890").
			Value(&chatIDStr).
			Run()
		if err != nil {
			return nil, err
		}
		fmt.Sscanf(chatIDStr, "%d", &chatID)
	}

	// Save token to ~/.pylon/.env so pylon start can load it
	config.SaveEnvVar("TELEGRAM_BOT_TOKEN", token)
	os.Setenv("TELEGRAM_BOT_TOKEN", token)
	fmt.Printf("  Token saved to %s\n", config.EnvPath())

	return &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   chatID,
	}, nil
}

func detectChatID(token, username string) (int64, error) {
	fmt.Println("  Option A: Direct message")
	fmt.Printf("    Send /start to https://t.me/%s\n", username)
	fmt.Println()
	fmt.Println("  Option B: Group chat")
	fmt.Println("    1. Create a Telegram group (or use an existing one)")
	fmt.Println("    2. Enable Topics: Group Settings > Topics > toggle on")
	fmt.Printf("    3. Add the bot as admin: https://t.me/%s?startgroup=setup&admin=manage_topics\n", username)
	fmt.Println("    4. Send a /command in the group (e.g. /hello)")
	fmt.Println()
	fmt.Println("  Waiting for message...")

	chatID, title, err := channel.PollForChat(token)
	if err != nil {
		return 0, err
	}
	fmt.Printf("  Detected: %s (ID: %d)\n\n", title, chatID)
	return chatID, nil
}

func setupSlack() (*config.SlackConfig, error) {
	fmt.Println("  Step 1: Create a Slack App")
	fmt.Println("    Go to https://api.slack.com/apps > Create New App > From a manifest")
	fmt.Println("    Paste this YAML manifest:")
	fmt.Println()
	fmt.Println(slackAppManifest)
	fmt.Println("  Step 2: Install the app to your workspace")
	fmt.Println("  Step 3: Enable Socket Mode (Settings > Socket Mode > toggle on)")
	fmt.Println("    Generate an App-Level Token with connections:write scope")
	fmt.Println()

	var botToken string
	if envToken := os.Getenv("SLACK_BOT_TOKEN"); envToken != "" {
		fmt.Println("  Using SLACK_BOT_TOKEN from environment")
		botToken = envToken
	} else {
		fmt.Println("  Find your Bot Token at: OAuth & Permissions > Bot User OAuth Token")
		err := huh.NewInput().
			Title("Slack bot token (xoxb-...):").
			Placeholder("xoxb-your-bot-token").
			Value(&botToken).
			Run()
		if err != nil {
			return nil, err
		}
	}

	username, err := channel.ValidateSlackToken(botToken)
	if err != nil {
		return nil, fmt.Errorf("invalid bot token: %w", err)
	}
	fmt.Printf("  Verified: @%s\n\n", username)

	var appToken string
	if envToken := os.Getenv("SLACK_APP_TOKEN"); envToken != "" {
		fmt.Println("  Using SLACK_APP_TOKEN from environment")
		appToken = envToken
	} else {
		fmt.Println("  Find your App Token at: Settings > Basic Information > App-Level Tokens")
		err := huh.NewInput().
			Title("Slack app token (xapp-...):").
			Placeholder("xapp-your-app-token").
			Value(&appToken).
			Run()
		if err != nil {
			return nil, err
		}
	}

	var method string
	err = huh.NewSelect[string]().
		Title("How do you want to set the channel?").
		Options(
			huh.NewOption("Auto-detect (list channels the bot is in)", "auto"),
			huh.NewOption("Enter channel ID manually", "manual"),
		).
		Value(&method).
		Run()
	if err != nil {
		return nil, err
	}

	var channelID string
	if method == "auto" {
		channels, err := channel.ListBotChannels(botToken)
		if err != nil {
			return nil, fmt.Errorf("listing channels: %w", err)
		}
		if len(channels) == 0 {
			fmt.Println("  No channels found. Invite the bot to a channel first, then enter the ID manually.")
			method = "manual"
		} else {
			options := make([]huh.Option[string], 0, len(channels))
			for i := range channels {
				label := fmt.Sprintf("#%s", channels[i].Name)
				options = append(options, huh.NewOption(label, channels[i].ID))
			}
			err = huh.NewSelect[string]().
				Title("Select a channel:").
				Options(options...).
				Value(&channelID).
				Run()
			if err != nil {
				return nil, err
			}
		}
	}
	if method == "manual" {
		err = huh.NewInput().
			Title("Slack channel ID:").
			Placeholder("C1234567890").
			Value(&channelID).
			Run()
		if err != nil {
			return nil, err
		}
	}

	config.SaveEnvVar("SLACK_BOT_TOKEN", botToken)
	os.Setenv("SLACK_BOT_TOKEN", botToken)
	config.SaveEnvVar("SLACK_APP_TOKEN", appToken)
	os.Setenv("SLACK_APP_TOKEN", appToken)
	fmt.Printf("  Tokens saved to %s\n", config.EnvPath())

	return &config.SlackConfig{
		BotToken:  "${SLACK_BOT_TOKEN}",
		AppToken:  "${SLACK_APP_TOKEN}",
		ChannelID: channelID,
	}, nil
}

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

func setupClaude() (*config.ClaudeDefaults, error) {
	var authChoice string
	if runtime.GOOS == "darwin" {
		fmt.Println("  Note: OAuth is not supported on macOS. Credentials are stored in")
		fmt.Println("  Keychain, which cannot be mounted into Docker containers.")
		fmt.Println("  Use an API key instead.")
		fmt.Println()
		authChoice = "api_key"
	} else {
		err := huh.NewSelect[string]().
			Title("Default authentication for Claude Code:").
			Options(
				huh.NewOption("API Key (ANTHROPIC_API_KEY)", "api_key"),
				huh.NewOption("OAuth (existing ~/.claude/ session)", "oauth"),
			).
			Value(&authChoice).
			Run()
		if err != nil {
			return nil, err
		}
	}

	claude := &config.ClaudeDefaults{
		Image: agentimage.ImageName("claude"),
		Auth:  authChoice,
	}

	if authChoice == "oauth" {
		home, _ := os.UserHomeDir()
		claudeDir := filepath.Join(home, ".claude")
		if _, err := os.Stat(claudeDir); err == nil {
			fmt.Printf("  ok, OAuth session found at %s\n", claudeDir)
			claude.OAuthPath = "~/.claude"
		} else {
			fmt.Println("  Warning: ~/.claude/ not found. Run `claude` first to authenticate.")
		}
	}

	return claude, nil
}

func setupOpenCode() (*config.OpenCodeDefaults, error) {
	var authChoice string
	err := huh.NewSelect[string]().
		Title("Authentication for OpenCode:").
		Options(
			huh.NewOption("Built-in (OpenCode Zen, no key needed)", "none"),
			huh.NewOption("API Key (bring your own provider key)", "api-key"),
		).
		Value(&authChoice).
		Run()
	if err != nil {
		return nil, err
	}

	oc := &config.OpenCodeDefaults{
		Image: agentimage.ImageName("opencode"),
		Auth:  authChoice,
	}

	if authChoice == "api-key" {
		var provider string
		err = huh.NewSelect[string]().
			Title("LLM provider:").
			Options(
				huh.NewOption("Anthropic (Claude)", "anthropic"),
				huh.NewOption("OpenAI (GPT)", "openai"),
				huh.NewOption("Google (Gemini)", "google"),
			).
			Value(&provider).
			Run()
		if err != nil {
			return nil, err
		}
		oc.Provider = provider

		envVar := config.ProviderEnvVar(provider)
		if os.Getenv(envVar) == "" {
			var apiKey string
			err = huh.NewInput().
				Title(fmt.Sprintf("%s:", envVar)).
				Placeholder("sk-...").
				EchoMode(huh.EchoModePassword).
				Value(&apiKey).
				Run()
			if err != nil {
				return nil, err
			}
			config.SaveEnvVar(envVar, apiKey)
			os.Setenv(envVar, apiKey)
			fmt.Printf("  Saved to %s\n", config.EnvPath())
		} else {
			fmt.Printf("  Using %s from environment\n", envVar)
		}
	}

	return oc, nil
}

func offerCompletion() {
	shell := filepath.Base(os.Getenv("SHELL"))
	var rcFile, evalLine string
	switch shell {
	case "zsh":
		rcFile = "~/.zshrc"
		evalLine = `eval "$(pylon completion zsh)"`
	case "bash":
		rcFile = "~/.bashrc"
		evalLine = `eval "$(pylon completion bash)"`
	case "fish":
		rcFile = "~/.config/fish/config.fish"
		evalLine = "pylon completion fish | source"
	default:
		return
	}

	fmt.Println()
	var install bool
	if err := huh.NewConfirm().
		Title("Enable tab completion?").
		Description(fmt.Sprintf("Appends to %s", rcFile)).
		Value(&install).
		Run(); err != nil || !install {
		return
	}

	// Expand ~ to home directory.
	home, _ := os.UserHomeDir()
	path := strings.Replace(rcFile, "~", home, 1)

	// Check if already present.
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "pylon completion") {
		fmt.Printf("  Already configured in %s\n", rcFile)
		return
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Printf("  Could not write to %s: %v\n", rcFile, err)
		return
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "\n# Pylon tab completion\n%s\n", evalLine); err != nil {
		fmt.Printf("  Could not write to %s: %v\n", rcFile, err)
		return
	}
	fmt.Printf("  Added to %s. Restart your shell or run: source %s\n", rcFile, rcFile)
}
