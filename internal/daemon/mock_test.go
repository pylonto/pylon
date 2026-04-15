package daemon

import (
	"fmt"
	"sync"

	"github.com/pylonto/pylon/internal/channel"
)

// mockMessage records a message sent through the mock channel.
type mockMessage struct {
	TopicID   string
	Text      string
	ReplyTo   string
	MessageID string
	IsEdit    bool
	JobID     string // for SendApproval
}

// mockChannel implements channel.Channel for testing.
type mockChannel struct {
	mu       sync.Mutex
	topicSeq int
	msgSeq   int
	messages []mockMessage
	actionFn func(jobID, action string)
	msgFn    func(topicID, text, messageID string)
}

func newMockChannel() *mockChannel {
	return &mockChannel{}
}

func (c *mockChannel) Ready() bool { return true }

func (c *mockChannel) CreateTopic(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topicSeq++
	return fmt.Sprintf("topic-%d", c.topicSeq), nil
}

func (c *mockChannel) SendMessage(topicID, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgSeq++
	msgID := fmt.Sprintf("msg-%d", c.msgSeq)
	c.messages = append(c.messages, mockMessage{TopicID: topicID, Text: text, MessageID: msgID})
	return msgID, nil
}

func (c *mockChannel) ReplyMessage(topicID, text, replyTo string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgSeq++
	msgID := fmt.Sprintf("msg-%d", c.msgSeq)
	c.messages = append(c.messages, mockMessage{TopicID: topicID, Text: text, ReplyTo: replyTo, MessageID: msgID})
	return msgID, nil
}

func (c *mockChannel) SendApproval(topicID, text, jobID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgSeq++
	msgID := fmt.Sprintf("msg-%d", c.msgSeq)
	c.messages = append(c.messages, mockMessage{TopicID: topicID, Text: text, JobID: jobID, MessageID: msgID})
	return msgID, nil
}

func (c *mockChannel) EditMessage(topicID, messageID, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, mockMessage{TopicID: topicID, Text: text, MessageID: messageID, IsEdit: true})
	return nil
}

func (c *mockChannel) FormatText(s string) string { return s }
func (c *mockChannel) SendTyping(string) error    { return nil }
func (c *mockChannel) CloseTopic(string) error    { return nil }

func (c *mockChannel) OnAction(fn func(string, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actionFn = fn
}

func (c *mockChannel) OnMessage(fn func(string, string, string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgFn = fn
}

func (c *mockChannel) Commands() []channel.Command {
	return channel.BotCommands
}
