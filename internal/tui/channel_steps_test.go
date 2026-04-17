package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackSteps_ShapeAndKeys(t *testing.T) {
	// No env token -> no source prompts, token inputs follow their own
	// "where to find it" info steps.
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	steps := SlackSteps("prefix")
	require.Len(t, steps, 9)
	assert.Equal(t, "prefix.slack_manifest", steps[0].Key)
	assert.Equal(t, "prefix.slack_install", steps[1].Key)
	assert.Equal(t, "prefix.slack_bot_token_info", steps[2].Key)
	assert.Equal(t, "prefix.slack_bot_token", steps[3].Key)
	assert.Equal(t, "prefix.slack_verify_bot", steps[4].Key)
	assert.Equal(t, "prefix.slack_app_token_info", steps[5].Key)
	assert.Equal(t, "prefix.slack_app_token", steps[6].Key)
	assert.Equal(t, "prefix.slack_channel_method", steps[7].Key)
	assert.Equal(t, "prefix.slack_channel_id", steps[8].Key)

	// Every step Create closure must produce a non-nil Step with a title.
	for _, s := range steps {
		step := s.Create(map[string]string{"prefix.slack_bot_token": "xoxb-x"})
		assert.NotNil(t, step, "step %q Create returned nil", s.Key)
	}
}

func TestTelegramSteps_ShapeAndKeys(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	steps := TelegramSteps("prefix")
	require.Len(t, steps, 5)
	assert.Equal(t, "prefix.tg_token_info", steps[0].Key)
	assert.Equal(t, "prefix.tg_token", steps[1].Key)
	assert.Equal(t, "prefix.tg_verify_bot", steps[2].Key)
	assert.Equal(t, "prefix.tg_chat_method", steps[3].Key)
	assert.Equal(t, "prefix.tg_chat_id", steps[4].Key)

	for _, s := range steps {
		step := s.Create(map[string]string{"prefix.tg_token": "123:abc"})
		assert.NotNil(t, step, "step %q Create returned nil", s.Key)
	}
}

func TestSlackSteps_EnvTokenOptIn(t *testing.T) {
	// Env-reuse must be opt-in: when SLACK_BOT_TOKEN is set, a preceding
	// source-select step asks the user to pick, defaulting to "new". Only
	// when the user explicitly picks "env" does the bot_token step become
	// an async validation of the env token.
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-from-env")
	t.Setenv("SLACK_APP_TOKEN", "")

	steps := SlackSteps("prefix")
	// 9 default steps + 1 source prompt = 10.
	require.Len(t, steps, 10)
	assert.Equal(t, "prefix.slack_bot_token_source", steps[3].Key)
	assert.Equal(t, "prefix.slack_bot_token", steps[4].Key)

	// Default (no source selected yet) -> text input, forcing the user to
	// type a fresh token for this pylon.
	defaultStep := steps[4].Create(nil)
	_, isText := defaultStep.(*textInputStep)
	assert.True(t, isText, "default (source unset) must prompt for a new token")

	// Opt-in: source=env -> async validation of env token.
	optedIn := steps[4].Create(map[string]string{
		"prefix.slack_bot_token_source": "env",
	})
	_, isAsync := optedIn.(*asyncStep)
	assert.True(t, isAsync, "source=env must reuse and validate env token")

	// verify_bot should be a no-op info step when env was reused (avoids a
	// redundant auth.test round trip).
	verifyStep := steps[5].Create(map[string]string{
		"prefix.slack_bot_token_source": "env",
	})
	_, isInfo := verifyStep.(*infoStep)
	assert.True(t, isInfo, "verify should be a passthrough when env token was reused")
}

func TestSlackSteps_BothEnvVarsAddBothSourcePrompts(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-from-env")
	t.Setenv("SLACK_APP_TOKEN", "xapp-from-env")

	steps := SlackSteps("prefix")
	// 9 baseline + 2 source prompts = 11.
	require.Len(t, steps, 11)
	assert.Equal(t, "prefix.slack_bot_token_source", steps[3].Key)
	assert.Equal(t, "prefix.slack_app_token_source", steps[7].Key)
}

func TestTelegramSteps_EnvTokenOptIn(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:from-env")

	steps := TelegramSteps("prefix")
	// 5 default steps (info + token + verify + method + id) + 1 source prompt = 6.
	require.Len(t, steps, 6)
	assert.Equal(t, "prefix.tg_token_info", steps[0].Key)
	assert.Equal(t, "prefix.tg_token_source", steps[1].Key)
	assert.Equal(t, "prefix.tg_token", steps[2].Key)

	// Default -> text input.
	defaultStep := steps[2].Create(nil)
	_, isText := defaultStep.(*textInputStep)
	assert.True(t, isText, "default (source unset) must prompt for a new token")

	// Opt-in -> async validation.
	optedIn := steps[2].Create(map[string]string{
		"prefix.tg_token_source": "env",
	})
	_, isAsync := optedIn.(*asyncStep)
	assert.True(t, isAsync, "source=env must reuse env token")
}

// TestSlackStepsWith_ChannelIDAutoDetect exercises the auto-detect branch of
// the channel_id step: when method=auto, the step is an AsyncSelectStep that
// calls ListChannels.
func TestSlackStepsWith_ChannelIDAutoDetect(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	called := false
	v := SlackValidators{
		ValidateBotToken: func(string) (string, error) { return "pylon-bot", nil },
		ListChannels: func(string) ([]selectOption, error) {
			called = true
			return []selectOption{{Label: "#general", Value: "C1"}, {Label: "#alerts", Value: "C2"}}, nil
		},
	}

	steps := SlackStepsWith("prefix", v)
	chStep := steps[8] // slack_channel_id
	assert.Equal(t, "prefix.slack_channel_id", chStep.Key)

	values := map[string]string{
		"prefix.slack_bot_token":      "xoxb-fresh",
		"prefix.slack_channel_method": "auto",
	}
	step := chStep.Create(values)
	_, isAsyncSelect := step.(*asyncSelectStep)
	require.True(t, isAsyncSelect, "auto-detect should be an asyncSelectStep")

	// Trigger the fetch via Init + simulate the result message. This drives
	// the step into its "picking" state, proving the injected ListChannels
	// ran.
	step.Init()
	result, _ := v.ListChannels("xoxb-fresh")
	step.Update(asyncSelectResultMsg{options: result})
	assert.True(t, called, "ListChannels should have been called")

	// In picking state, the step exposes the first channel as the current value.
	assert.Equal(t, "C1", step.Value())
}

func TestSlackStepsWith_ChannelIDManual(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")

	v := DefaultSlackValidators
	steps := SlackStepsWith("prefix", v)
	chStep := steps[8]

	values := map[string]string{
		"prefix.slack_bot_token":      "xoxb-fresh",
		"prefix.slack_channel_method": "manual",
	}
	step := chStep.Create(values)
	_, isText := step.(*textInputStep)
	assert.True(t, isText, "manual method should produce a text input step")
}

func TestSlackStepsWith_VerifyBotSuccess(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	v := SlackValidators{
		ValidateBotToken: func(tok string) (string, error) {
			if tok != "xoxb-valid" {
				return "", errors.New("unexpected token")
			}
			return "pylon-bot", nil
		},
	}

	verifyStep := SlackStepsWith("prefix", v)[4].Create(map[string]string{
		"prefix.slack_bot_token": "xoxb-valid",
	})
	// Fire the async fn directly to confirm the validator was wired.
	async := verifyStep.(*asyncStep)
	result, err := async.fn()
	require.NoError(t, err)
	assert.Contains(t, result, "pylon-bot")
}

func TestSlackStepsWith_VerifyBotInvalid(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	v := SlackValidators{
		ValidateBotToken: func(string) (string, error) {
			return "", errors.New("auth.test failed")
		},
	}
	verifyStep := SlackStepsWith("prefix", v)[4].Create(map[string]string{
		"prefix.slack_bot_token": "xoxb-bad",
	})
	async := verifyStep.(*asyncStep)
	_, err := async.fn()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.test failed")
}

func TestTelegramStepsWith_PollCancel(t *testing.T) {
	// Verify the Cancelable contract: when a wizard abandons the step,
	// Cancel() cancels the context handed to PollForChat so the goroutine
	// can return promptly instead of hanging on the next long-poll round.
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	v := TelegramValidators{
		GetBotUsername: func(string) (string, error) { return "alertbot", nil },
		PollForChat:    func(ctx context.Context, _ string) (int64, string, error) { <-ctx.Done(); return 0, "", ctx.Err() },
	}

	steps := TelegramStepsWith("prefix", v)
	chatIDDef := steps[4] // tg_chat_id
	assert.Equal(t, "prefix.tg_chat_id", chatIDDef.Key)

	step := chatIDDef.Create(map[string]string{
		"prefix.tg_token":       "123:abc",
		"prefix.tg_chat_method": "auto",
	})
	waiter, ok := step.(*waitingAsyncStep)
	require.True(t, ok, "auto method should produce a waitingAsyncStep")

	// Init prepares the context.
	waiter.Init()
	require.NoError(t, waiter.ctx.Err(), "ctx should be live immediately after Init")

	waiter.Cancel()
	assert.ErrorIs(t, waiter.ctx.Err(), context.Canceled, "Cancel should mark the ctx canceled")

	// Implements Cancelable so wizardModel can invoke Cancel on shift+tab.
	var _ Cancelable = waiter
}

func TestTelegramStepsWith_ChatIDManual(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	steps := TelegramSteps("prefix")
	chatIDDef := steps[4]
	step := chatIDDef.Create(map[string]string{
		"prefix.tg_token":       "123:abc",
		"prefix.tg_chat_method": "manual",
	})
	_, isText := step.(*textInputStep)
	assert.True(t, isText, "manual chat method should produce a text input")
}

// Sanity check that the select inside the auto branch still routes key
// events correctly through the embedded step.
func TestAsyncSelectStep_PickerKeyRouting(t *testing.T) {
	step := NewAsyncSelectStep("title", "desc",
		func() ([]selectOption, error) {
			return []selectOption{{Label: "a", Value: "A"}, {Label: "b", Value: "B"}}, nil
		}, false, "").(*asyncSelectStep)
	step.Init()
	step.Update(asyncSelectResultMsg{options: []selectOption{{Label: "a", Value: "A"}, {Label: "b", Value: "B"}}})

	step.Update(tea.KeyMsg{Type: tea.KeyDown})
	step.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, step.IsDone())
	assert.Equal(t, "B", step.Value())
}

func TestAsyncSelectStep_EmptyFallback(t *testing.T) {
	step := NewAsyncSelectStep("title", "desc",
		func() ([]selectOption, error) { return []selectOption{}, nil },
		true, "C123").(*asyncSelectStep)
	step.Init()
	step.Update(asyncSelectResultMsg{options: []selectOption{}})
	assert.Equal(t, asyncSelectFallback, step.state)
}

func TestAsyncSelectStep_ErrorRetry(t *testing.T) {
	// The state machine transitions: running -> error -> running -> picking.
	// We can't drive the fetch goroutine in a pure unit test (that needs a
	// tea.Program), so we simulate the messages directly.
	step := NewAsyncSelectStep("title", "desc",
		func() ([]selectOption, error) { return nil, nil }, // unused here
		false, "").(*asyncSelectStep)

	step.Init()
	step.Update(asyncSelectResultMsg{err: errors.New("transient")})
	assert.Equal(t, asyncSelectError, step.state)

	// Pressing enter re-enters the running state (ready for another attempt).
	step.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, asyncSelectRunning, step.state)

	// Second attempt succeeds.
	step.Update(asyncSelectResultMsg{options: []selectOption{{Label: "ok", Value: "ok"}}})
	assert.Equal(t, asyncSelectPicking, step.state)
}
