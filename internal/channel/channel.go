package channel

import "strings"

// Command describes a bot command shown to users.
type Command struct {
	Name        string // e.g. "done"
	Description string // e.g. "Close the current job and stop the agent"
}

// BotCommands is the canonical list of commands supported by all channels.
var BotCommands = []Command{
	{Name: "done", Description: "Close the current job and stop the agent"},
	{Name: "status", Description: "Peek at what running agents are doing"},
	{Name: "agents", Description: "List all active agents"},
	{Name: "help", Description: "Show available commands"},
}

// splitMessage splits text into chunks of at most maxLen bytes,
// breaking at newlines when possible to preserve readability.
func splitMessage(text string, maxLen int) []string {
	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}
		cut := maxLen
		// Try to break at a newline within the last 25% of the chunk.
		if idx := strings.LastIndex(text[:cut], "\n"); idx > cut*3/4 {
			cut = idx + 1
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

// Channel abstracts a messaging backend that supports topic-based
// conversations and inline-action buttons (approve/reject).
type Channel interface {
	// Ready reports whether the channel can send messages.
	// Returns false when the channel is waiting for runtime setup
	// (e.g. Telegram auto-detecting chat_id from the first inbound message).
	Ready() bool
	CreateTopic(name string) (topicID string, err error)
	SendMessage(topicID string, text string) (messageID string, err error)
	ReplyMessage(topicID string, text string, replyTo string) (messageID string, err error)
	SendApproval(topicID string, text string, jobID string) (messageID string, err error)
	EditMessage(topicID string, messageID string, text string) error
	FormatText(text string) string
	SendTyping(topicID string) error
	CloseTopic(topicID string) error
	OnAction(callback func(jobID string, action string))
	OnMessage(callback func(topicID string, text string, messageID string))
	Commands() []Command
}
