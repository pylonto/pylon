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

	"github.com/pylonto/pylon/internal/agentimage"
	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/cron"
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
	fmt.Println("\nPylon Doctor")

	issues := 0
	recommendations := 0

	// ── System ──────────────────────────────────────────────
	fmt.Println("\nSystem")

	global, err := config.LoadGlobal()
	if err != nil {
		drLine("Config", "FAIL", config.GlobalPath()+" not found")
		issues++
	} else {
		drLine("Config", "ok", config.GlobalPath()+" found")
	}

	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
		drLine("Docker", "ok", "running (v"+strings.TrimSpace(string(out))+")")
	} else {
		drLine("Docker", "FAIL", "not found -- https://docs.docker.com/engine/install/")
		issues++
	}

	agentType := "claude"
	if global != nil && global.Defaults.Agent.Type != "" {
		agentType = global.Defaults.Agent.Type
	}
	agentImage := agentimage.ImageName(agentType)
	if out, err := exec.Command("docker", "images", agentImage, "--format", "{{.Tag}}").Output(); err == nil {
		tag := firstNonEmptyLine(string(out))
		if tag != "" && tag != "<none>" {
			drLine("Agent image", "ok", agentImage+":"+tag)
		} else {
			drLine("Agent image", "--", agentImage+" not pulled")
			recommendations++
		}
	} else {
		drLine("Agent image", "--", agentImage+" not pulled")
		recommendations++
	}

	if _, err := exec.Command("systemctl", "--user", "is-active", "pylon").Output(); err == nil {
		drLine("Service", "ok", "systemd unit active")
	} else {
		drLine("Service", "--", "not installed (run pylon service install)")
		recommendations++
	}

	// ── Auth ────────────────────────────────────────────────
	fmt.Println("\nAuth")

	if out, err := exec.Command("ssh", "-T", "git@github.com").CombinedOutput(); err != nil {
		outStr := string(out)
		if strings.Contains(outStr, "successfully authenticated") {
			drLine("Git", "ok", "GitHub SSH key configured")
		} else if err2 := exec.Command("gh", "auth", "status").Run(); err2 == nil {
			drLine("Git", "ok", "gh CLI authenticated")
		} else {
			drLine("Git", "WARN", "no SSH key or gh CLI auth found")
			recommendations++
		}
	} else {
		drLine("Git", "ok", "GitHub SSH key configured")
	}

	switch agentType {
	case "claude":
		home, _ := os.UserHomeDir()
		claudeDir := filepath.Join(home, ".claude")
		if _, err := os.Stat(claudeDir); err == nil {
			drLine("OAuth session", "ok", claudeDir+" found")
		} else {
			drLine("OAuth session", "--", "~/.claude not found")
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
				drLine("API key", "ok", envVar+" is set")
			} else {
				drLine("API key", "FAIL", envVar+" not set")
				issues++
			}
		} else {
			drLine("OpenCode auth", "ok", "using built-in (Zen)")
		}
	}

	// ── Network ─────────────────────────────────────────────
	fmt.Println("\nNetwork")

	if global != nil {
		label := fmt.Sprintf("Port %d", global.Server.Port)
		addr := fmt.Sprintf(":%d", global.Server.Port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			drLine(label, "ok", "available")
		} else {
			resp, pingErr := http.Get(fmt.Sprintf("http://localhost:%d/callback/doctor-ping", global.Server.Port))
			if pingErr == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusMethodNotAllowed {
					drLine(label, "ok", "pylon is running")
				} else {
					drLine(label, "WARN", "in use (not by pylon)")
					recommendations++
				}
			} else {
				drLine(label, "WARN", "in use")
				recommendations++
			}
		}
	}

	if global != nil && global.Defaults.Channel.Type == "telegram" && global.Defaults.Channel.Telegram != nil {
		token := os.ExpandEnv(global.Defaults.Channel.Telegram.BotToken)
		if token == "" || token == global.Defaults.Channel.Telegram.BotToken {
			drLine("Global channel", "FAIL", "TELEGRAM_BOT_TOKEN not set")
			issues++
		} else if username, err := channel.GetBotUsername(token); err == nil {
			chatID := global.Defaults.Channel.Telegram.ChatID
			if chatID == 0 {
				drLine("Global channel", "WARN", fmt.Sprintf("telegram @%s, chat_id not set -- will auto-detect on start; send /start to @%s", username, username))
				recommendations++
			} else if err := channel.CheckChatAccess(token, chatID); err == nil {
				drLine("Global channel", "ok", fmt.Sprintf("telegram @%s, chat %d", username, chatID))
			} else {
				drLine("Global channel", "FAIL", fmt.Sprintf("telegram @%s, chat %d: %v", username, chatID, err))
				issues++
			}
		} else {
			drLine("Global channel", "FAIL", "could not connect (invalid token?)")
			issues++
		}
	} else if global != nil && global.Defaults.Channel.Type == "slack" && global.Defaults.Channel.Slack != nil {
		botToken := os.ExpandEnv(global.Defaults.Channel.Slack.BotToken)
		if botToken == "" || botToken == global.Defaults.Channel.Slack.BotToken {
			drLine("Global channel", "FAIL", "SLACK_BOT_TOKEN not set")
			issues++
		} else if username, err := channel.ValidateSlackToken(botToken); err == nil {
			channelID := global.Defaults.Channel.Slack.ChannelID
			if name, err := channel.CheckSlackAccess(botToken, channelID); err == nil {
				drLine("Global channel", "ok", fmt.Sprintf("slack @%s, #%s", username, name))
			} else {
				drLine("Global channel", "FAIL", fmt.Sprintf("slack @%s, %s: %v", username, channelID, err))
				issues++
			}
		} else {
			drLine("Global channel", "FAIL", "could not connect (invalid token?)")
			issues++
		}
	} else if global != nil {
		drLine("Global channel", "--", "not configured")
	}

	// ── Pylons ──────────────────────────────────────────────
	names, _ := config.ListPylons()
	if len(names) > 0 {
		fmt.Printf("\nPylons (%d)\n", len(names))
	} else {
		fmt.Println("\nPylons (0)")
	}

	if global != nil && len(names) > 0 {
		client := &http.Client{Timeout: 5 * time.Second}
		for _, name := range names {
			fmt.Printf("  %s\n", name)
			pyl, err := config.LoadPylon(name)
			if err != nil {
				drSub("config", "FAIL", fmt.Sprintf("%v", err))
				issues++
				continue
			}

			if pyl.Trigger.Type == "cron" {
				loc := pyl.ResolveTimezone(global)
				next := cron.NextFire(pyl.Trigger.Cron, loc)
				if next.IsZero() {
					drSub("cron", "FAIL", fmt.Sprintf("%q did not produce a next fire time", pyl.Trigger.Cron))
					issues++
				} else {
					drSub("cron", "ok", fmt.Sprintf("%s [%s] next: %s", pyl.Trigger.Cron, loc, next.Format("Jan 02 15:04")))
				}
			}

			if pyl.Trigger.Type == "webhook" {
				url := pyl.ResolvePublicURL(global)
				resp, err := client.Get(url)
				if err != nil {
					drSub("webhook", "FAIL", url+" unreachable")
					proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
					issues++
				} else {
					resp.Body.Close()
					if resp.StatusCode == http.StatusAccepted ||
						resp.StatusCode == http.StatusMethodNotAllowed ||
						resp.StatusCode == http.StatusBadRequest ||
						resp.StatusCode == http.StatusUnauthorized {
						drSub("webhook", "ok", url+" reachable")
					} else {
						drSub("webhook", "WARN", fmt.Sprintf("%s returned %d (may not be routed to pylon)", url, resp.StatusCode))
						proxy.PrintHints(pyl.Trigger.Path, global.Server.Port)
						recommendations++
					}
				}
			}

			if pyl.Channel != nil && pyl.Channel.Type == "telegram" && pyl.Channel.Telegram != nil {
				tg := pyl.Channel.Telegram
				pylonEnv := config.LoadPylonEnvFile(name)
				token := config.ExpandWithPylonEnv(tg.BotToken, pylonEnv)
				if token == "" || token == tg.BotToken {
					drSub("channel", "FAIL", "bot token not set")
					issues++
				} else if username, err := channel.GetBotUsername(token); err == nil {
					if tg.ChatID == -1 {
						drSub("channel", "FAIL", fmt.Sprintf("telegram @%s, chat_id not configured", username))
						issues++
					} else if tg.ChatID == 0 {
						drSub("channel", "WARN", fmt.Sprintf("telegram @%s, chat_id not set -- will auto-detect on start; send /start to @%s", username, username))
						recommendations++
					} else if err := channel.CheckChatAccess(token, tg.ChatID); err == nil {
						drSub("channel", "ok", fmt.Sprintf("telegram @%s, chat %d", username, tg.ChatID))
					} else {
						drSub("channel", "FAIL", fmt.Sprintf("telegram @%s, chat %d: %v", username, tg.ChatID, err))
						issues++
					}
				} else {
					drSub("channel", "FAIL", "bot token invalid")
					issues++
				}
			}
		}
	}

	// ── Summary ─────────────────────────────────────────────
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

// drLine prints a top-level doctor check with dot-leader alignment.
func drLine(label, status, detail string) {
	const width = 22
	dots := width - len(label)
	if dots < 3 {
		dots = 3
	}
	fmt.Printf("  %s %s %-4s  %s\n", label, strings.Repeat(".", dots), status, detail)
}

// drSub prints a per-pylon sub-check with dot-leader alignment.
func drSub(label, status, detail string) {
	const width = 18
	dots := width - len(label)
	if dots < 3 {
		dots = 3
	}
	fmt.Printf("    %s %s %-4s  %s\n", label, strings.Repeat(".", dots), status, detail)
}

// firstNonEmptyLine returns the first non-empty, non-<none> line from s.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != "<none>" {
			return line
		}
	}
	return ""
}
