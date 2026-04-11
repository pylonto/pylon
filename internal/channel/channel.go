package channel

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

// Channel abstracts a messaging backend that supports topic-based
// conversations and inline-action buttons (approve/reject).
type Channel interface {
	CreateTopic(name string) (topicID string, err error)
	SendMessage(topicID string, text string) (messageID string, err error)
	ReplyMessage(topicID string, text string, replyTo string) (messageID string, err error)
	SendApproval(topicID string, text string, jobID string) (messageID string, err error)
	EditMessage(topicID string, messageID string, text string) error
	SendTyping(topicID string) error
	CloseTopic(topicID string) error
	OnAction(callback func(jobID string, action string))
	OnMessage(callback func(topicID string, text string, messageID string))
	Commands() []Command
}
