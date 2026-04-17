package tui

import (
	"context"
	"fmt"
	"os"

	"github.com/pylonto/pylon/internal/channel"
)

// SlackValidators exposes the Slack-side network calls used by SlackSteps
// so tests can inject stubs. DefaultSlackValidators wraps the real
// internal/channel functions.
type SlackValidators struct {
	// ValidateBotToken calls auth.test and returns the bot's username.
	ValidateBotToken func(token string) (username string, err error)
	// ListChannels returns channels the bot has been invited to, as select
	// options (Label="#channel-name", Value=channel ID).
	ListChannels func(token string) ([]selectOption, error)
}

// DefaultSlackValidators delegates to internal/channel.
var DefaultSlackValidators = SlackValidators{
	ValidateBotToken: channel.ValidateSlackToken,
	ListChannels: func(token string) ([]selectOption, error) {
		chs, err := channel.ListBotChannels(token)
		if err != nil {
			return nil, err
		}
		opts := make([]selectOption, 0, len(chs))
		for i := range chs {
			opts = append(opts, selectOption{
				Label: "#" + chs[i].Name,
				Value: chs[i].ID,
			})
		}
		return opts, nil
	},
}

// TelegramValidators exposes the Telegram-side network calls used by
// TelegramSteps so tests can inject stubs.
type TelegramValidators struct {
	GetBotUsername func(token string) (string, error)
	PollForChat    func(ctx context.Context, token string) (chatID int64, title string, err error)
}

// DefaultTelegramValidators delegates to internal/channel.
var DefaultTelegramValidators = TelegramValidators{
	GetBotUsername: channel.GetBotUsername,
	PollForChat:    channel.PollForChatCtx,
}

// SlackSteps returns the full Slack onboarding flow: manifest copy block,
// install/socket info, bot token entry + live validation, app token entry,
// and channel selection (auto-detect with manual fallback). The keys are
// prefixed with keyPrefix so the same builder can serve `pylon construct`
// and `pylon setup` without collisions.
func SlackSteps(keyPrefix string) []StepDef {
	return SlackStepsWith(keyPrefix, DefaultSlackValidators)
}

// SlackStepsWith is the injectable variant. Tests pass a SlackValidators
// whose functions return stub values so the wizard can be driven without
// hitting the real Slack API.
func SlackStepsWith(keyPrefix string, v SlackValidators) []StepDef {
	k := func(suffix string) string { return keyPrefix + "." + suffix }

	envBotToken := os.Getenv("SLACK_BOT_TOKEN")
	envAppToken := os.Getenv("SLACK_APP_TOKEN")

	steps := []StepDef{
		{Key: k("slack_manifest"), Create: func(_ map[string]string) Step {
			return NewCopyBlockStep(
				"Step 1: Create a Slack App",
				"Go to https://api.slack.com/apps -> Create New App -> From a manifest.\nPaste this YAML manifest:",
				slackAppManifest,
			)
		}},
		{Key: k("slack_install"), Create: func(_ map[string]string) Step {
			return NewInfoStep(
				"Step 2: Install to your workspace",
				"",
				"On the app settings page, click Install App in the left sidebar,\nthen Install to Workspace and approve the requested scopes.",
			)
		}},
		{Key: k("slack_bot_token_info"), Create: func(_ map[string]string) Step {
			return NewInfoStep(
				"Step 3: Get the bot token",
				"",
				"Open OAuth & Permissions in the left sidebar.\nCopy the Bot User OAuth Token (starts with xoxb-).",
			)
		}},
	}

	// When SLACK_BOT_TOKEN is already in the env, offer to reuse it. Default
	// is "enter a new token" so each pylon gets its own, self-contained
	// secrets in per-pylon .env rather than silently depending on the global.
	if envBotToken != "" {
		steps = append(steps, StepDef{
			Key: k("slack_bot_token_source"),
			Create: func(_ map[string]string) Step {
				return NewSelectStep(
					"Slack bot token",
					"SLACK_BOT_TOKEN is set in your environment.",
					[]selectOption{
						{"Enter a new token (recommended, per-pylon)", "new"},
						{"Reuse SLACK_BOT_TOKEN from environment", "env"},
					},
				)
			},
		})
	}
	steps = append(steps,
		StepDef{Key: k("slack_bot_token"), Create: func(values map[string]string) Step {
			if values[k("slack_bot_token_source")] == "env" && envBotToken != "" {
				return NewAsyncStep(
					"Slack bot token",
					"Using SLACK_BOT_TOKEN from environment",
					func() (string, error) {
						username, err := v.ValidateBotToken(envBotToken)
						if err != nil {
							return "", fmt.Errorf("invalid bot token -- check OAuth & Permissions > Bot User OAuth Token: %w", err)
						}
						return fmt.Sprintf("Verified @%s (from env)", username), nil
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
		StepDef{Key: k("slack_verify_bot"), Create: func(values map[string]string) Step {
			// When user reused env, the bot_token step already validated.
			// Skip the redundant round-trip with a silent-pass info step.
			if values[k("slack_bot_token_source")] == "env" {
				return NewInfoStep("Bot token verified", "", "(already validated above)")
			}
			token := slackBotToken(values, keyPrefix)
			return NewAsyncStep(
				"Verifying bot token",
				"Connecting to Slack",
				func() (string, error) {
					if token == "" {
						return "", fmt.Errorf("bot token is empty")
					}
					username, err := v.ValidateBotToken(token)
					if err != nil {
						return "", fmt.Errorf("invalid bot token: %w", err)
					}
					return fmt.Sprintf("Verified @%s", username), nil
				},
			)
		}},
	)

	steps = append(steps, StepDef{
		Key: k("slack_app_token_info"),
		Create: func(_ map[string]string) Step {
			return NewInfoStep(
				"Step 4: Get the app-level token",
				"",
				"1. Open Socket Mode in the left sidebar and toggle it on.\n"+
					"2. Open Basic Information -> App-Level Tokens -> Generate Token and Scopes.\n"+
					"3. Add the connections:write scope and generate the token.\n"+
					"4. Copy the token (starts with xapp-).",
			)
		},
	})
	if envAppToken != "" {
		steps = append(steps, StepDef{
			Key: k("slack_app_token_source"),
			Create: func(_ map[string]string) Step {
				return NewSelectStep(
					"Slack app token",
					"SLACK_APP_TOKEN is set in your environment.",
					[]selectOption{
						{"Enter a new token (recommended, per-pylon)", "new"},
						{"Reuse SLACK_APP_TOKEN from environment", "env"},
					},
				)
			},
		})
	}
	steps = append(steps, StepDef{Key: k("slack_app_token"), Create: func(values map[string]string) Step {
		if values[k("slack_app_token_source")] == "env" && envAppToken != "" {
			return NewAsyncStep(
				"Slack app token",
				"Using SLACK_APP_TOKEN from environment",
				func() (string, error) {
					return "Using SLACK_APP_TOKEN (from env)", nil
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
	}})

	steps = append(steps,
		StepDef{Key: k("slack_channel_method"), Create: func(_ map[string]string) Step {
			return NewSelectStep(
				"Slack channel",
				"Where should this pylon post?",
				[]selectOption{
					{"Auto-detect (list channels the bot is in)", "auto"},
					{"Enter channel ID manually", "manual"},
				},
			)
		}},
		StepDef{Key: k("slack_channel_id"), Create: func(values map[string]string) Step {
			method := values[k("slack_channel_method")]
			token := slackBotToken(values, keyPrefix)
			if method == "auto" {
				return NewAsyncSelectStep(
					"Select a channel",
					"Fetching channels the bot has access to",
					func() ([]selectOption, error) {
						return v.ListChannels(token)
					},
					true,
					"C1234567890",
				)
			}
			return NewTextInputStep(
				"Slack channel ID",
				"The channel where pylon will post. Find it in channel details.",
				"C1234567890",
				"",
				false,
			)
		}},
	)
	return steps
}

// TelegramSteps returns the Telegram onboarding flow: bot token entry +
// live validation + chat ID (auto via PollForChat or manual).
func TelegramSteps(keyPrefix string) []StepDef {
	return TelegramStepsWith(keyPrefix, DefaultTelegramValidators)
}

// TelegramStepsWith is the injectable variant for tests.
func TelegramStepsWith(keyPrefix string, v TelegramValidators) []StepDef {
	k := func(suffix string) string { return keyPrefix + "." + suffix }

	envToken := os.Getenv("TELEGRAM_BOT_TOKEN")

	var steps []StepDef

	if envToken != "" {
		steps = append(steps, StepDef{
			Key: k("tg_token_source"),
			Create: func(_ map[string]string) Step {
				return NewSelectStep(
					"Telegram bot token",
					"TELEGRAM_BOT_TOKEN is set in your environment.",
					[]selectOption{
						{"Enter a new token (recommended, per-pylon)", "new"},
						{"Reuse TELEGRAM_BOT_TOKEN from environment", "env"},
					},
				)
			},
		})
	}
	steps = append(steps,
		StepDef{Key: k("tg_token"), Create: func(values map[string]string) Step {
			if values[k("tg_token_source")] == "env" && envToken != "" {
				return NewAsyncStep(
					"Telegram bot token",
					"Using TELEGRAM_BOT_TOKEN from environment",
					func() (string, error) {
						username, err := v.GetBotUsername(envToken)
						if err != nil {
							return "", fmt.Errorf("invalid token -- create one via @BotFather: %w", err)
						}
						return fmt.Sprintf("Verified @%s (from env)", username), nil
					},
				)
			}
			return NewTextInputStep(
				"Telegram bot token",
				"Create a bot via @BotFather: https://t.me/BotFather. Send /newbot, pick a name, and copy the token.",
				"110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
				"",
				false,
			)
		}},
		StepDef{Key: k("tg_verify_bot"), Create: func(values map[string]string) Step {
			if values[k("tg_token_source")] == "env" {
				return NewInfoStep("Bot token verified", "", "(already validated above)")
			}
			token := telegramToken(values, keyPrefix)
			return NewAsyncStep(
				"Verifying bot token",
				"Connecting to Telegram",
				func() (string, error) {
					if token == "" {
						return "", fmt.Errorf("bot token is empty")
					}
					username, err := v.GetBotUsername(token)
					if err != nil {
						return "", fmt.Errorf("invalid bot token: %w", err)
					}
					return fmt.Sprintf("Verified @%s", username), nil
				},
			)
		}},
		StepDef{Key: k("tg_chat_method"), Create: func(_ map[string]string) Step {
			return NewSelectStep(
				"Chat ID detection",
				"How should pylon find the chat?",
				[]selectOption{
					{"Auto-detect (add bot to group, send a message)", "auto"},
					{"Enter manually", "manual"},
				},
			)
		}},
		StepDef{Key: k("tg_chat_id"), Create: func(values map[string]string) Step {
			method := values[k("tg_chat_method")]
			token := telegramToken(values, keyPrefix)
			if method == "auto" {
				waitMsg := "Waiting for a message to the bot..."
				return NewWaitingAsyncStep(
					"Auto-detect chat ID",
					"Option A: send /start to the bot in a DM.\nOption B: add the bot to a group (enable Topics, make admin), then send any /command.",
					waitMsg,
					func(ctx context.Context) (string, error) {
						chatID, title, err := v.PollForChat(ctx, token)
						if err != nil {
							return "", err
						}
						return fmt.Sprintf("%d (%s)", chatID, title), nil
					},
				)
			}
			return NewTextInputStep(
				"Telegram chat ID",
				"Numeric chat ID where the bot will post.",
				"-1001234567890",
				"",
				false,
			)
		}},
	)
	return steps
}

// slackBotToken returns the effective Slack bot token: the one entered in
// the wizard for this prefix, falling back to SLACK_BOT_TOKEN from the env
// when the env-reuse async step stored a verification string instead of a
// raw token.
func slackBotToken(values map[string]string, keyPrefix string) string {
	if v := values[keyPrefix+".slack_bot_token"]; v != "" && !isVerificationResult(v) {
		return v
	}
	return os.Getenv("SLACK_BOT_TOKEN")
}

// telegramToken returns the effective Telegram bot token with the same
// env-fallback logic as slackBotToken.
func telegramToken(values map[string]string, keyPrefix string) string {
	if v := values[keyPrefix+".tg_token"]; v != "" && !isVerificationResult(v) {
		return v
	}
	return os.Getenv("TELEGRAM_BOT_TOKEN")
}

// isVerificationResult detects values written by the env-reuse AsyncStep
// ("Verified @username (from env)") so callers don't mistake them for raw
// tokens.
func isVerificationResult(s string) bool {
	return len(s) > 0 && (s[0] == 'V' || s[0] == 'U')
}
