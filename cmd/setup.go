package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/notifier"
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
	dockerVersion := "not found"
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		dockerVersion = strings.TrimSpace(string(out))
		fmt.Printf("Docker .............. ok, found (v%s)\n", dockerVersion)
	} else {
		fmt.Println("Docker .............. NOT FOUND")
		fmt.Println("  Install: https://docs.docker.com/engine/install/")
	}

	fmt.Println()

	// Notifier selection
	var notifierChoice string
	err := huh.NewSelect[string]().
		Title("Default notifier -- where should alerts go?").
		Options(
			huh.NewOption("Telegram", "telegram"),
			huh.NewOption("Slack (coming soon)", "slack"),
			huh.NewOption("Discord (coming soon)", "discord"),
			huh.NewOption("WhatsApp (coming soon)", "whatsapp"),
			huh.NewOption("iMessage (coming soon)", "imessage"),
			huh.NewOption("Webhook (generic HTTP POST)", "webhook"),
			huh.NewOption("stdout (console only)", "stdout"),
		).
		Value(&notifierChoice).
		Run()
	if err != nil {
		return err
	}

	cfg := &config.GlobalConfig{
		Version: 1,
		Server:  config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker:  config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}

	switch notifierChoice {
	case "telegram":
		tg, err := setupTelegram()
		if err != nil {
			return err
		}
		cfg.Defaults.Notifier = config.NotifierDefaults{Type: "telegram", Telegram: tg}
	case "stdout":
		cfg.Defaults.Notifier = config.NotifierDefaults{Type: "stdout"}
	case "webhook":
		cfg.Defaults.Notifier = config.NotifierDefaults{Type: "webhook"}
	default:
		comingSoon(notifierChoice)
		cfg.Defaults.Notifier = config.NotifierDefaults{Type: "stdout"}
	}

	fmt.Println()

	// Agent selection
	var agentChoice string
	err = huh.NewSelect[string]().
		Title("Default AI agent for new pylons:").
		Options(
			huh.NewOption("Claude Code", "claude"),
			huh.NewOption("Codex (coming soon)", "codex"),
			huh.NewOption("Aider (coming soon)", "aider"),
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
	case "custom":
		cfg.Defaults.Agent = config.AgentDefaults{Type: "custom"}
	default:
		comingSoon(agentChoice)
		cfg.Defaults.Agent = config.AgentDefaults{Type: "claude", Claude: &config.ClaudeDefaults{
			Image: "pylon/agent-claude", Auth: "oauth",
		}}
	}

	if err := config.SaveGlobal(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("\nSetup complete.\n")
	fmt.Printf("  Config saved to %s\n", config.GlobalPath())
	fmt.Printf("  Run `pylon construct <name>` to create your first pylon.\n\n")
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

	username, err := notifier.GetBotUsername(token)
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

	os.Setenv("TELEGRAM_BOT_TOKEN", token)

	return &config.TelegramConfig{
		BotToken: "${TELEGRAM_BOT_TOKEN}",
		ChatID:   chatID,
	}, nil
}

func detectChatID(token, username string) (int64, error) {
	fmt.Println("  1. Create a Telegram group (or use an existing one)")
	fmt.Println("  2. Enable Topics: Group Settings > Topics > toggle on")
	fmt.Printf("  3. Add the bot as admin: https://t.me/%s?startgroup=setup&admin=manage_topics\n", username)
	fmt.Println("  4. Send a /command in the group (e.g. /hello)")
	fmt.Println("     (Regular messages won't work unless you disable privacy mode in @BotFather)")
	fmt.Println("\n  Waiting for message...")

	chatID, title, err := notifier.PollForGroup(token)
	if err != nil {
		return 0, err
	}
	fmt.Printf("  Detected: %s (ID: %d)\n\n", title, chatID)
	return chatID, nil
}

func setupClaude() (*config.ClaudeDefaults, error) {
	var authChoice string
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

	claude := &config.ClaudeDefaults{
		Image: "pylon/agent-claude",
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
