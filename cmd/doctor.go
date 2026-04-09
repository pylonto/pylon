package cmd

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/notifier"
	"github.com/pylonto/pylon/internal/proxy"
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
	config.LoadEnv()
	fmt.Printf("\nPylon Doctor\n\n")

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
	agentType := "claude"
	if global != nil && global.Defaults.Agent.Type != "" {
		agentType = global.Defaults.Agent.Type
	}
	agentImage := "pylon/agent-" + agentType
	if out, err := exec.Command("docker", "images", agentImage, "--format", "{{.Tag}}").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		fmt.Printf("Agent image ......... ok    %s:%s built\n", agentImage, strings.TrimSpace(string(out)))
	} else {
		fmt.Printf("Agent image ......... --    %s not built\n", agentImage)
		fmt.Printf("  Run: docker build -t %s agent/%s/\n", agentImage, agentType)
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
			chatID := global.Defaults.Notifier.Telegram.ChatID
			if err := notifier.CheckChatAccess(token, chatID); err == nil {
				fmt.Printf("Telegram chat ....... ok    chat %d accessible\n", chatID)
			} else {
				fmt.Printf("Telegram chat ....... FAIL  chat %d: %v\n", chatID, err)
				fmt.Println("  Make sure the bot is an admin in the group with topic management permissions")
				issues++
			}
		} else {
			fmt.Println("Telegram bot ........ FAIL  could not connect (invalid token?)")
			issues++
		}
	} else if global != nil && global.Defaults.Notifier.Type == "slack" && global.Defaults.Notifier.Slack != nil {
		botToken := os.ExpandEnv(global.Defaults.Notifier.Slack.BotToken)
		if botToken == "" || botToken == global.Defaults.Notifier.Slack.BotToken {
			fmt.Println("Slack bot ........... FAIL  SLACK_BOT_TOKEN not set")
			fmt.Println("  export SLACK_BOT_TOKEN=<your token>")
			issues++
		} else if username, err := notifier.ValidateSlackToken(botToken); err == nil {
			fmt.Printf("Slack bot ........... ok    connected (@%s)\n", username)
			channelID := global.Defaults.Notifier.Slack.ChannelID
			if name, err := notifier.CheckSlackAccess(botToken, channelID); err == nil {
				fmt.Printf("Slack channel ....... ok    #%s accessible\n", name)
			} else {
				fmt.Printf("Slack channel ....... FAIL  %s: %v\n", channelID, err)
				fmt.Println("  Make sure the bot is invited to the channel")
				issues++
			}
		} else {
			fmt.Println("Slack bot ........... FAIL  could not connect (invalid token?)")
			issues++
		}
	} else {
		fmt.Println("Notifier ............ --    not configured")
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

	// Agent auth
	switch agentType {
	case "claude":
		home, _ := os.UserHomeDir()
		claudeDir := filepath.Join(home, ".claude")
		if _, err := os.Stat(claudeDir); err == nil {
			fmt.Printf("OAuth session ....... ok    %s found\n", claudeDir)
		} else {
			fmt.Println("OAuth session ....... --    ~/.claude/ not found")
			recommendations++
		}
	case "opencode":
		auth := "none"
		if global != nil && global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Auth != "" {
			auth = global.Defaults.Agent.OpenCode.Auth
		}
		if auth == "api-key" {
			provider := "anthropic"
			if global.Defaults.Agent.OpenCode != nil && global.Defaults.Agent.OpenCode.Provider != "" {
				provider = global.Defaults.Agent.OpenCode.Provider
			}
			envVar := config.ProviderEnvVar(provider)
			if os.Getenv(envVar) != "" {
				fmt.Printf("API key ............. ok    %s is set\n", envVar)
			} else {
				fmt.Printf("API key ............. FAIL  %s not set\n", envVar)
				issues++
			}
		} else {
			fmt.Println("OpenCode auth ....... ok    using built-in (Zen)")
		}
	}

	// Port
	if global != nil {
		addr := fmt.Sprintf(":%d", global.Server.Port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			fmt.Printf("Port %d ........... ok    available\n", global.Server.Port)
		} else {
			// Check if pylon itself owns the port by hitting the callback route
			resp, pingErr := http.Get(fmt.Sprintf("http://localhost:%d/callback/doctor-ping", global.Server.Port))
			if pingErr == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusMethodNotAllowed {
					fmt.Printf("Port %d ........... ok    pylon is running\n", global.Server.Port)
				} else {
					fmt.Printf("Port %d ........... WARN  in use (not by pylon)\n", global.Server.Port)
					recommendations++
				}
			} else {
				fmt.Printf("Port %d ........... WARN  in use\n", global.Server.Port)
				recommendations++
			}
		}
	}

	// Pylons
	names, _ := config.ListPylons()
	if len(names) > 0 {
		fmt.Printf("Pylons .............. %d constructed (%s)\n", len(names), strings.Join(names, ", "))
	} else {
		fmt.Println("Pylons .............. 0 constructed")
	}

	// Webhook reachability per pylon
	if global != nil && len(names) > 0 {
		client := &http.Client{Timeout: 5 * time.Second}
		for _, name := range names {
			pyl, err := config.LoadPylon(name)
			if err != nil || pyl.Trigger.Type != "webhook" {
				continue
			}
			url := pyl.ResolvePublicURL(global)
			// Use GET so pylon returns 405 without triggering a real job
			resp, err := client.Get(url)
			if err != nil {
				fmt.Printf("  %s webhook ... FAIL  %s unreachable\n", name, url)
				proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
				issues++
				continue
			}
			resp.Body.Close()
			// Pylon returns 202 for valid webhooks; anything else means
			// the request went somewhere else or pylon isn't running
			if resp.StatusCode == http.StatusAccepted {
				fmt.Printf("  %s webhook ... ok    %s reachable\n", name, url)
			} else if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
				// 405/400/401 from pylon means it received the request (just rejected our test)
				fmt.Printf("  %s webhook ... ok    %s reachable\n", name, url)
			} else {
				fmt.Printf("  %s webhook ... WARN  %s returned %d (may not be routed to pylon)\n", name, url, resp.StatusCode)
				proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
				recommendations++
			}
		}
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
