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

	"github.com/charmbracelet/huh"
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
	if global != nil && global.Defaults.Channel.Type == "telegram" && global.Defaults.Channel.Telegram != nil {
		token := os.ExpandEnv(global.Defaults.Channel.Telegram.BotToken)
		if token == "" || token == global.Defaults.Channel.Telegram.BotToken {
			fmt.Println("Telegram bot ........ FAIL  TELEGRAM_BOT_TOKEN not set")
			fmt.Println("  export TELEGRAM_BOT_TOKEN=<your token>")
			issues++
		} else if username, err := notifier.GetBotUsername(token); err == nil {
			fmt.Printf("Telegram bot ........ ok    connected (@%s)\n", username)
			chatID := global.Defaults.Channel.Telegram.ChatID
			if chatID == 0 {
				fmt.Println("Telegram chat ....... FAIL  chat_id not configured")
				if fixGlobalChatID(token, username, global) {
					fmt.Printf("Telegram chat ....... ok    chat %d saved\n", global.Defaults.Channel.Telegram.ChatID)
				} else {
					issues++
				}
			} else if err := notifier.CheckChatAccess(token, chatID); err == nil {
				fmt.Printf("Telegram chat ....... ok    chat %d accessible\n", chatID)
			} else {
				fmt.Printf("Telegram chat ....... FAIL  chat %d: %v\n", chatID, err)
				if fixGlobalChatID(token, username, global) {
					fmt.Printf("Telegram chat ....... ok    chat %d saved\n", global.Defaults.Channel.Telegram.ChatID)
				} else {
					fmt.Println("  Make sure the bot is an admin in the group with topic management permissions")
					issues++
				}
			}
		} else {
			fmt.Println("Telegram bot ........ FAIL  could not connect (invalid token?)")
			issues++
		}
	} else if global != nil && global.Defaults.Channel.Type == "slack" && global.Defaults.Channel.Slack != nil {
		botToken := os.ExpandEnv(global.Defaults.Channel.Slack.BotToken)
		if botToken == "" || botToken == global.Defaults.Channel.Slack.BotToken {
			fmt.Println("Slack bot ........... FAIL  SLACK_BOT_TOKEN not set")
			fmt.Println("  export SLACK_BOT_TOKEN=<your token>")
			issues++
		} else if username, err := notifier.ValidateSlackToken(botToken); err == nil {
			fmt.Printf("Slack bot ........... ok    connected (@%s)\n", username)
			channelID := global.Defaults.Channel.Slack.ChannelID
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
	} else if global != nil {
		fmt.Println("Channel ............. --    not configured")
		var fix string
		if err := huh.NewSelect[string]().
			Title("Configure a default channel now?").
			Options(
				huh.NewOption("Telegram", "telegram"),
				huh.NewOption("Slack", "slack"),
				huh.NewOption("Skip", "skip"),
			).
			Value(&fix).
			Run(); err == nil && fix != "skip" {
			switch fix {
			case "telegram":
				if tg, err := setupTelegram(); err == nil {
					global.Defaults.Channel = config.ChannelDefaults{Type: "telegram", Telegram: tg}
					if err := config.SaveGlobal(global); err != nil {
						fmt.Printf("  Could not save config: %v\n", err)
						issues++
					} else {
						fmt.Println("Channel ............. ok    telegram saved")
					}
				} else {
					fmt.Printf("  Telegram setup failed: %v\n", err)
					issues++
				}
			case "slack":
				if sl, err := setupSlack(); err == nil {
					global.Defaults.Channel = config.ChannelDefaults{Type: "slack", Slack: sl}
					if err := config.SaveGlobal(global); err != nil {
						fmt.Printf("  Could not save config: %v\n", err)
						issues++
					} else {
						fmt.Println("Channel ............. ok    slack saved")
					}
				} else {
					fmt.Printf("  Slack setup failed: %v\n", err)
					issues++
				}
			}
		}
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

	// Per-pylon checks
	if global != nil && len(names) > 0 {
		client := &http.Client{Timeout: 5 * time.Second}
		for _, name := range names {
			pyl, err := config.LoadPylon(name)
			if err != nil {
				continue
			}

			// Webhook reachability
			if pyl.Trigger.Type == "webhook" {
				url := pyl.ResolvePublicURL(global)
				resp, err := client.Get(url)
				if err != nil {
					fmt.Printf("  %s webhook ... FAIL  %s unreachable\n", name, url)
					proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
					issues++
				} else {
					resp.Body.Close()
					if resp.StatusCode == http.StatusAccepted ||
						resp.StatusCode == http.StatusMethodNotAllowed ||
						resp.StatusCode == http.StatusBadRequest ||
						resp.StatusCode == http.StatusUnauthorized {
						fmt.Printf("  %s webhook ... ok    %s reachable\n", name, url)
					} else {
						fmt.Printf("  %s webhook ... WARN  %s returned %d (may not be routed to pylon)\n", name, url, resp.StatusCode)
						proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
						recommendations++
					}
				}
			}

			// Per-pylon channel check
			if pyl.Channel != nil && pyl.Channel.Type == "telegram" && pyl.Channel.Telegram != nil {
				tg := pyl.Channel.Telegram
				token := os.ExpandEnv(tg.BotToken)
				if token == "" || token == tg.BotToken {
					fmt.Printf("  %s channel ... FAIL  bot token not set\n", name)
					issues++
				} else if username, err := notifier.GetBotUsername(token); err == nil {
					if tg.ChatID == 0 || tg.ChatID == -1 {
						fmt.Printf("  %s channel ... FAIL  chat_id not configured\n", name)
						if fixPylonChatID(token, username, name, pyl) {
							fmt.Printf("  %s channel ... ok    chat %d saved\n", name, pyl.Channel.Telegram.ChatID)
						} else {
							issues++
						}
					} else if err := notifier.CheckChatAccess(token, tg.ChatID); err == nil {
						fmt.Printf("  %s channel ... ok    chat %d accessible\n", name, tg.ChatID)
					} else {
						fmt.Printf("  %s channel ... FAIL  chat %d: %v\n", name, tg.ChatID, err)
						if fixPylonChatID(token, username, name, pyl) {
							fmt.Printf("  %s channel ... ok    chat %d saved\n", name, pyl.Channel.Telegram.ChatID)
						} else {
							issues++
						}
					}
				} else {
					fmt.Printf("  %s channel ... FAIL  bot token invalid\n", name)
					issues++
				}
			}
		}
	}

	// systemd
	if _, err := exec.Command("systemctl", "--user", "is-active", "pylon").Output(); err == nil {
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

// offerDetectChatID prompts the user and auto-detects a Telegram chat ID.
// Returns the detected chat ID, or 0 if skipped/failed.
func offerDetectChatID(token, username string) int64 {
	var fix string
	err := huh.NewSelect[string]().
		Title("Fix it now?").
		Options(
			huh.NewOption("Auto-detect (add bot to group, send a message)", "auto"),
			huh.NewOption("Skip", "skip"),
		).
		Value(&fix).
		Run()
	if err != nil || fix != "auto" {
		return 0
	}
	chatID, err := detectChatID(token, username)
	if err != nil {
		fmt.Printf("  Detection failed: %v\n", err)
		return 0
	}
	return chatID
}

// fixGlobalChatID auto-detects a chat ID and saves it to the global config.
func fixGlobalChatID(token, username string, global *config.GlobalConfig) bool {
	chatID := offerDetectChatID(token, username)
	if chatID == 0 {
		return false
	}
	global.Defaults.Channel.Telegram.ChatID = chatID
	if err := config.SaveGlobal(global); err != nil {
		fmt.Printf("  Could not save: %v\n", err)
		return false
	}
	return true
}

// fixPylonChatID auto-detects a chat ID and saves it to a pylon config.
func fixPylonChatID(token, username, name string, pyl *config.PylonConfig) bool {
	chatID := offerDetectChatID(token, username)
	if chatID == 0 {
		return false
	}
	pyl.Channel.Telegram.ChatID = chatID
	if err := config.SavePylon(pyl); err != nil {
		fmt.Printf("  Could not save: %v\n", err)
		return false
	}
	return true
}
