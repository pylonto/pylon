package channel

import (
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/assert"
)

// newTestSlack constructs a Slack struct for testing without starting
// Socket Mode (no network).
func newTestSlack(channelID string, allowedUsers []string) *Slack {
	allowed := make(map[string]bool, len(allowedUsers))
	for _, u := range allowedUsers {
		allowed[u] = true
	}
	return &Slack{
		channelID:    channelID,
		allowedUsers: allowed,
	}
}

func TestSlack_Ready(t *testing.T) {
	t.Run("false when channelID is empty", func(t *testing.T) {
		s := newTestSlack("", nil)
		assert.False(t, s.Ready())
	})

	t.Run("true when channelID is set", func(t *testing.T) {
		s := newTestSlack("C123", nil)
		assert.True(t, s.Ready())
	})
}

func TestSlack_FormatText(t *testing.T) {
	s := newTestSlack("C123", nil)
	assert.Equal(t, "**bold**", s.FormatText("**bold**"), "FormatText should return input unchanged")
}

func TestSlack_SendTyping(t *testing.T) {
	s := newTestSlack("C123", nil)
	assert.NoError(t, s.SendTyping("some-topic"), "SendTyping should be a no-op for Slack")
}

func TestSlack_Commands(t *testing.T) {
	s := newTestSlack("C123", nil)
	cmds := s.Commands()
	assert.Equal(t, BotCommands, cmds)
	assert.True(t, len(cmds) > 0, "should have at least one command")
}

func TestSlack_isAllowed(t *testing.T) {
	t.Run("empty allowlist allows everyone", func(t *testing.T) {
		s := newTestSlack("C123", nil)
		assert.True(t, s.isAllowed("U999"))
		assert.True(t, s.isAllowed(""))
	})

	t.Run("non-empty allowlist restricts", func(t *testing.T) {
		s := newTestSlack("C123", []string{"U111", "U222"})
		assert.True(t, s.isAllowed("U111"))
		assert.True(t, s.isAllowed("U222"))
		assert.False(t, s.isAllowed("U333"))
		assert.False(t, s.isAllowed(""))
	})
}

func TestSlack_OnAction(t *testing.T) {
	s := newTestSlack("C123", nil)

	var gotJobID, gotAction string
	s.OnAction(func(jobID, action string) {
		gotJobID = jobID
		gotAction = action
	})

	s.mu.Lock()
	fn := s.actionFn
	s.mu.Unlock()

	assert.NotNil(t, fn)
	fn("job-1", "investigate")
	assert.Equal(t, "job-1", gotJobID)
	assert.Equal(t, "investigate", gotAction)
}

func TestSlack_OnMessage(t *testing.T) {
	s := newTestSlack("C123", nil)

	var gotTopic, gotText, gotMsgID string
	s.OnMessage(func(topicID, text, messageID string) {
		gotTopic = topicID
		gotText = text
		gotMsgID = messageID
	})

	s.mu.Lock()
	fn := s.messageFn
	s.mu.Unlock()

	assert.NotNil(t, fn)
	fn("topic-1", "hello", "msg-1")
	assert.Equal(t, "topic-1", gotTopic)
	assert.Equal(t, "hello", gotText)
	assert.Equal(t, "msg-1", gotMsgID)
}

func TestSlack_CloseTopic_emptyIsNoop(t *testing.T) {
	s := newTestSlack("C123", nil)
	// Empty topicID should return nil without calling API
	assert.NoError(t, s.CloseTopic(""))
}

func TestSlack_handleEvent_interactive(t *testing.T) {
	s := newTestSlack("C123", nil)

	var gotJobID, gotAction string
	done := make(chan struct{})
	s.OnAction(func(jobID, action string) {
		gotJobID = jobID
		gotAction = action
		close(done)
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slack.InteractionCallback{
			Type: slack.InteractionTypeBlockActions,
			User: slack.User{ID: "U123"},
			ActionCallback: slack.ActionCallbacks{
				BlockActions: []*slack.BlockAction{
					{ActionID: "investigate:job-42"},
				},
			},
		},
	}

	s.handleEvent(evt)

	// Wait for async callback
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("action callback not called within timeout")
	}

	assert.Equal(t, "job-42", gotJobID)
	assert.Equal(t, "investigate", gotAction)
}

func TestSlack_handleEvent_interactive_blockedUser(t *testing.T) {
	s := newTestSlack("C123", []string{"U111"})

	called := false
	s.OnAction(func(jobID, action string) {
		called = true
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slack.InteractionCallback{
			Type: slack.InteractionTypeBlockActions,
			User: slack.User{ID: "U999"}, // not in allowlist
			ActionCallback: slack.ActionCallbacks{
				BlockActions: []*slack.BlockAction{
					{ActionID: "investigate:job-1"},
				},
			},
		},
	}

	s.handleEvent(evt)
	time.Sleep(50 * time.Millisecond) // give any goroutine time to fire
	assert.False(t, called, "action callback should not be called for blocked user")
}

func TestSlack_handleEvent_message(t *testing.T) {
	s := newTestSlack("C123", nil)

	var gotTopic, gotText, gotMsgID string
	done := make(chan struct{})
	s.OnMessage(func(topicID, text, messageID string) {
		gotTopic = topicID
		gotText = text
		gotMsgID = messageID
		close(done)
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{
					User:             "U123",
					Text:             "hello pylon",
					TimeStamp:        "1234567890.123456",
					ThreadTimeStamp:  "1234567890.000000",
				},
			},
		},
	}

	s.handleEvent(evt)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("message callback not called within timeout")
	}

	assert.Equal(t, "1234567890.000000", gotTopic)
	assert.Equal(t, "hello pylon", gotText)
	assert.Equal(t, "1234567890.123456", gotMsgID)
}

func TestSlack_handleEvent_ignoresBotMessages(t *testing.T) {
	s := newTestSlack("C123", nil)

	called := false
	s.OnMessage(func(topicID, text, messageID string) {
		called = true
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{
					User:  "U123",
					BotID: "B999", // bot message
					Text:  "bot says hello",
				},
			},
		},
	}

	s.handleEvent(evt)
	time.Sleep(50 * time.Millisecond)
	assert.False(t, called, "should ignore bot messages")
}

func TestSlack_handleEvent_ignoresSubtypedMessages(t *testing.T) {
	s := newTestSlack("C123", nil)

	called := false
	s.OnMessage(func(topicID, text, messageID string) {
		called = true
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{
					User:    "U123",
					SubType: "message_changed", // edited message
					Text:    "edited text",
				},
			},
		},
	}

	s.handleEvent(evt)
	time.Sleep(50 * time.Millisecond)
	assert.False(t, called, "should ignore subtyped messages")
}

func TestSlack_handleEvent_blockedUserMessage(t *testing.T) {
	s := newTestSlack("C123", []string{"U111"})

	called := false
	s.OnMessage(func(topicID, text, messageID string) {
		called = true
	})

	evt := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			InnerEvent: slackevents.EventsAPIInnerEvent{
				Data: &slackevents.MessageEvent{
					User: "U999", // not in allowlist
					Text: "blocked",
				},
			},
		},
	}

	s.handleEvent(evt)
	time.Sleep(50 * time.Millisecond)
	assert.False(t, called, "should not call callback for blocked user")
}

func TestSlack_handleEvent_wrongDataType(t *testing.T) {
	s := newTestSlack("C123", nil)

	// Should not panic on wrong data type
	evt := socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: "not an InteractionCallback",
	}
	s.handleEvent(evt)

	evt2 := socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: "not an EventsAPIEvent",
	}
	s.handleEvent(evt2)
}

func TestSlack_handleEvent_noCallback(t *testing.T) {
	s := newTestSlack("C123", nil)
	// No OnAction or OnMessage registered

	evt := socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slack.InteractionCallback{
			Type: slack.InteractionTypeBlockActions,
			User: slack.User{ID: "U123"},
			ActionCallback: slack.ActionCallbacks{
				BlockActions: []*slack.BlockAction{
					{ActionID: "investigate:job-1"},
				},
			},
		},
	}

	// Should not panic with nil callbacks
	s.handleEvent(evt)
}
