package main

// Notifier abstracts a messaging backend that supports topic-based
// conversations and inline-action buttons (approve/reject).
type Notifier interface {
	CreateTopic(name string) (topicID string, err error)
	SendMessage(topicID string, text string) (messageID string, err error)
	SendApproval(topicID string, text string, jobID string) (messageID string, err error)
	EditMessage(topicID string, messageID string, text string) error
	SendTyping(topicID string) error
	CloseTopic(topicID string) error
	OnAction(callback func(jobID string, action string))
	OnMessage(callback func(topicID string, text string))
}
