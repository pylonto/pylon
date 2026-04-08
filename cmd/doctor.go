package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/notifier"
)

func init() {
	rootCmd.AddCommand(doctorCmd)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check dependencies and health",
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("\nPylon Doctor\n")

	issues := 0
	recommendations := 0

	// Config
	global, err := config.LoadGlobal()
	if err != nil {
		fmt.Printf("Config .............. FAIL  %s not found\n", config.GlobalPath())
		fmt.Println("  Run `pylon setup` to create it.")
		issues++
	} else {
		fmt.Printf("Config .............. ok    %s found\n", config.GlobalPath())
	}

	// Docker
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		fmt.Printf("Docker .............. ok    running (v%s)\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Println("Docker .............. FAIL  not found")
		fmt.Println("  Install: https://docs.docker.com/engine/install/")
		issues++
	}

	// Agent image
	if out, err := exec.Command("docker", "images", "pylon/agent-claude", "--format", "{{.Tag}}").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		fmt.Printf("Agent image ......... ok    pylon/agent-claude:%s pulled\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Println("Agent image ......... --    pylon/agent-claude not pulled")
		fmt.Println("  Run: docker build -t pylon/agent-claude agent/claude/")
		recommendations++
	}

	// Telegram
	if global != nil && global.Defaults.Notifier.Type == "telegram" && global.Defaults.Notifier.Telegram != nil {
		token := os.ExpandEnv(global.Defaults.Notifier.Telegram.BotToken)
		if token == "" || token == global.Defaults.Notifier.Telegram.BotToken {
			fmt.Println("Telegram bot ........ FAIL  TELEGRAM_BOT_TOKEN not set")
			fmt.Println("  export TELEGRAM_BOT_TOKEN=<your token>")
			issues++
		} else if username, err := notifier.GetBotUsername(token); err == nil {
			fmt.Printf("Telegram bot ........ ok    connected (@%s)\n", username)
			fmt.Printf("Telegram chat ....... ok    chat %d\n", global.Defaults.Notifier.Telegram.ChatID)
		} else {
			fmt.Println("Telegram bot ........ FAIL  could not connect (invalid token?)")
			issues++
		}
	} else {
		fmt.Println("Telegram ............ --    not configured")
	}

	// Git auth
	if out, err := exec.Command("ssh", "-T", "git@github.com").CombinedOutput(); err != nil {
		outStr := string(out)
		if strings.Contains(outStr, "successfully authenticated") {
			fmt.Println("Git (SSH) ........... ok    GitHub SSH key configured")
		} else if err2 := exec.Command("gh", "auth", "status").Run(); err2 == nil {
			fmt.Println("Git (HTTPS) ......... ok    gh CLI authenticated")
		} else {
			fmt.Println("Git auth ............ WARN  no SSH key or gh CLI auth found")
			fmt.Println("  Private repos won't clone. Run: gh auth setup-git")
			recommendations++
		}
	} else {
		fmt.Println("Git (SSH) ........... ok    GitHub SSH key configured")
	}

	// OAuth
	home, _ := os.UserHomeDir()
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); err == nil {
		fmt.Printf("OAuth session ....... ok    %s found\n", claudeDir)
	} else {
		fmt.Println("OAuth session ....... --    ~/.claude/ not found")
		recommendations++
	}

	// Port
	if global != nil {
		addr := fmt.Sprintf(":%d", global.Server.Port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			fmt.Printf("Port %d ........... ok    available\n", global.Server.Port)
		} else {
			fmt.Printf("Port %d ........... WARN  in use\n", global.Server.Port)
			recommendations++
		}
	}

	// Pylons
	names, _ := config.ListPylons()
	if len(names) > 0 {
		fmt.Printf("Pylons .............. %d constructed (%s)\n", len(names), strings.Join(names, ", "))
	} else {
		fmt.Println("Pylons .............. 0 constructed")
	}

	// systemd
	if _, err := exec.Command("systemctl", "is-active", "pylon").Output(); err == nil {
		fmt.Println("systemd service ..... ok    active")
	} else {
		fmt.Println("systemd service ..... --    not installed (run `pylon service install`)")
		recommendations++
	}

	fmt.Println()
	if issues > 0 {
		fmt.Printf("%d issue(s) found.\n", issues)
	} else if recommendations > 0 {
		fmt.Printf("All checks passed. (%d recommendation(s))\n", recommendations)
	} else {
		fmt.Println("All checks passed.")
	}
	fmt.Println()
	return nil
}
