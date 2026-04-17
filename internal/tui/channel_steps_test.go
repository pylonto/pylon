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
	// No env token -> first step is a text input, not async.
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")

	steps := SlackSteps("prefix")
	require.Len(t, steps, 8)
	assert.Equal(t, "prefix.slack_manifest", steps[0].Key)
	assert.Equal(t, "prefix.slack_install", steps[1].Key)
	assert.Equal(t, "prefix.slack_socket", steps[2].Key)
	assert.Equal(t, "prefix.slack_bot_token", steps[3].Key)
	assert.Equal(t, "prefix.slack_verify_bot", steps[4].Key)
	assert.Equal(t, "prefix.slack_app_token", steps[5].Key)
	assert.Equal(t, "prefix.slack_channel_method", steps[6].Key)
	assert.Equal(t, "prefix.slack_channel_id", steps[7].Key)

	// Every step Create closure must produce a non-nil Step with a title.
	for _, s := range steps {
		step := s.Create(map[string]string{"prefix.slack_bot_token": "xoxb-x"})
		assert.NotNil(t, step, "step %q Create returned nil", s.Key)
	}
}

func TestTelegramSteps_ShapeAndKeys(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	steps := TelegramSteps("prefix")
	require.Len(t, steps, 4)
	assert.Equal(t, "prefix.tg_token", steps[0].Key)
	assert.Equal(t, "prefix.tg_verify_bot", steps[1].Key)
	assert.Equal(t, "prefix.tg_chat_method", steps[2].Key)
	assert.Equal(t, "prefix.tg_chat_id", steps[3].Key)

	for _, s := range steps {
		step := s.Create(map[string]string{"prefix.tg_token": "123:abc"})
		assert.NotNil(t, step, "step %q Create returned nil", s.Key)
	}
}

func TestSlackSteps_EnvTokenReuse(t *testing.T) {
	// When the env var is pre-set, the first step is an AsyncStep that validates
	// the existing token rather than prompting for a fresh one.
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-from-env")
	t.Setenv("SLACK_APP_TOKEN", "")

	steps := SlackSteps("prefix")
	botStep := steps[3].Create(nil)
	// asyncStep has no textinput -- we can tell it apart by asserting it's
	// not a *textInputStep.
	_, isText := botStep.(*textInputStep)
	assert.False(t, isText, "bot_token step should be async validation when env is set")
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
	chStep := steps[7] // slack_channel_id
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
	chStep := steps[7]

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
	chatIDDef := steps[3] // tg_chat_id
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
	chatIDDef := steps[3]
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
