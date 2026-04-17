package cmd

import (
	"errors"
	"testing"

	"github.com/pylonto/pylon/internal/config"
	"github.com/stretchr/testify/assert"
)

// stubChannelProbes is an injectable replacement for live Telegram/Slack calls.
type stubChannelProbes struct {
	tgUsername     string
	tgUsernameErr  error
	tgAccessErr    error
	slackUsername  string
	slackUserErr   error
	slackChanName  string
	slackAccessErr error
}

func (s stubChannelProbes) TelegramGetBotUsername(string) (string, error) {
	return s.tgUsername, s.tgUsernameErr
}
func (s stubChannelProbes) TelegramCheckChatAccess(string, int64) error {
	return s.tgAccessErr
}
func (s stubChannelProbes) SlackValidateToken(string) (string, error) {
	return s.slackUsername, s.slackUserErr
}
func (s stubChannelProbes) SlackCheckAccess(string, string) (string, error) {
	return s.slackChanName, s.slackAccessErr
}

func TestCheckChannel_Slack(t *testing.T) {
	slackPylon := func() *config.PylonConfig {
		return &config.PylonConfig{
			Name: "linear-dev",
			Channel: &config.PylonChannel{
				Type: "slack",
				Slack: &config.SlackConfig{
					BotToken:  "${SLACK_BOT_TOKEN}",
					AppToken:  "${SLACK_APP_TOKEN}",
					ChannelID: "C1234567890",
				},
			},
		}
	}

	t.Run("ok when token resolves and access succeeds", func(t *testing.T) {
		env := map[string]string{
			"SLACK_BOT_TOKEN": "xoxb-valid",
			"SLACK_APP_TOKEN": "xapp-valid",
		}
		probes := stubChannelProbes{slackUsername: "pylonbot", slackChanName: "general"}
		result := checkChannel(slackPylon(), env, probes)
		assert.Equal(t, "ok", result.Status)
		assert.Contains(t, result.Detail, "slack @pylonbot")
		assert.Contains(t, result.Detail, "#general")
	})

	t.Run("FAIL when bot token is unset", func(t *testing.T) {
		probes := stubChannelProbes{}
		result := checkChannel(slackPylon(), nil, probes)
		assert.Equal(t, "FAIL", result.Status)
		assert.Contains(t, result.Detail, "bot token not set")
	})

	t.Run("FAIL when app token is unset", func(t *testing.T) {
		// Bot token resolves but app token doesn't: buildChannels will drop
		// the channel at runtime, so doctor must flag this too.
		env := map[string]string{"SLACK_BOT_TOKEN": "xoxb-valid"}
		probes := stubChannelProbes{slackUsername: "pylonbot", slackChanName: "general"}
		result := checkChannel(slackPylon(), env, probes)
		assert.Equal(t, "FAIL", result.Status)
		assert.Contains(t, result.Detail, "app token not set")
	})

	t.Run("FAIL when token is invalid", func(t *testing.T) {
		env := map[string]string{"SLACK_BOT_TOKEN": "xoxb-bad", "SLACK_APP_TOKEN": "xapp-bad"}
		probes := stubChannelProbes{slackUserErr: errors.New("auth.test failed")}
		result := checkChannel(slackPylon(), env, probes)
		assert.Equal(t, "FAIL", result.Status)
		assert.Contains(t, result.Detail, "bot token invalid")
	})

	t.Run("FAIL when channel access denied", func(t *testing.T) {
		env := map[string]string{"SLACK_BOT_TOKEN": "xoxb-valid", "SLACK_APP_TOKEN": "xapp-valid"}
		probes := stubChannelProbes{
			slackUsername:  "pylonbot",
			slackAccessErr: errors.New("channel_not_found"),
		}
		result := checkChannel(slackPylon(), env, probes)
		assert.Equal(t, "FAIL", result.Status)
		assert.Contains(t, result.Detail, "C1234567890")
		assert.Contains(t, result.Detail, "channel_not_found")
	})

	t.Run("falls back to per-pylon env", func(t *testing.T) {
		// Token resolved via per-pylon env (not global) -- the whole point of
		// the linear-dev fix: doctor must read per-pylon .env too.
		env := map[string]string{"SLACK_BOT_TOKEN": "xoxb-perpylon", "SLACK_APP_TOKEN": "xapp-perpylon"}
		probes := stubChannelProbes{slackUsername: "pylonbot", slackChanName: "alerts"}
		result := checkChannel(slackPylon(), env, probes)
		assert.Equal(t, "ok", result.Status)
	})
}

func TestCheckChannel_Telegram(t *testing.T) {
	tgPylon := func(chatID int64) *config.PylonConfig {
		return &config.PylonConfig{
			Name: "alert-bot",
			Channel: &config.PylonChannel{
				Type: "telegram",
				Telegram: &config.TelegramConfig{
					BotToken: "${TELEGRAM_BOT_TOKEN}",
					ChatID:   chatID,
				},
			},
		}
	}

	t.Run("ok with chat access", func(t *testing.T) {
		env := map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc"}
		probes := stubChannelProbes{tgUsername: "alertbot"}
		result := checkChannel(tgPylon(42), env, probes)
		assert.Equal(t, "ok", result.Status)
		assert.Contains(t, result.Detail, "@alertbot")
		assert.Contains(t, result.Detail, "chat 42")
	})

	t.Run("WARN when chat_id=0", func(t *testing.T) {
		env := map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc"}
		probes := stubChannelProbes{tgUsername: "alertbot"}
		result := checkChannel(tgPylon(0), env, probes)
		assert.Equal(t, "WARN", result.Status)
		assert.Contains(t, result.Detail, "auto-detect")
	})

	t.Run("FAIL when token unset", func(t *testing.T) {
		result := checkChannel(tgPylon(42), nil, stubChannelProbes{})
		assert.Equal(t, "FAIL", result.Status)
	})
}

func TestCheckChannel_NoChannel(t *testing.T) {
	pyl := &config.PylonConfig{Name: "p"}
	result := checkChannel(pyl, nil, stubChannelProbes{})
	assert.Equal(t, "", result.Status, "no channel row should be emitted")
}
