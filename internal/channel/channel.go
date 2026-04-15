package channel

import (
	"log"
	"strings"
)

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

// formattedChunk holds both the raw markdown and the channel-formatted version
// of a message chunk so callers can fall back to plaintext when formatted
// sending fails.
type formattedChunk struct {
	Raw       string
	Formatted string
}

// splitAndFormat splits raw markdown into chunks that each fit within maxLen
// when passed through the given format function. It splits at paragraph
// boundaries (\n\n) first -- these are natural markdown break points where
// inline formatting is always balanced -- then falls back to line boundaries,
// then to hard byte splits as a last resort.
//
// Each returned chunk contains both the raw source and the formatted output so
// callers can retry with plaintext on format failures.
func splitAndFormat(raw string, maxLen int, format func(string) string) []formattedChunk {
	// Fast path: everything fits in one message.
	if formatted := format(raw); len(formatted) <= maxLen {
		return []formattedChunk{{Raw: raw, Formatted: formatted}}
	}

	blocks := strings.Split(raw, "\n\n")
	var chunks []formattedChunk
	var current string

	for _, block := range blocks {
		candidate := current
		if candidate != "" {
			candidate += "\n\n" + block
		} else {
			candidate = block
		}

		if len(format(candidate)) <= maxLen {
			current = candidate
			continue
		}

		// Finalize the current accumulation before it overflows.
		if current != "" {
			chunks = append(chunks, formattedChunk{Raw: current, Formatted: format(current)})
			current = ""
		}

		// Try this block alone.
		if len(format(block)) <= maxLen {
			current = block
			continue
		}

		// Block too large -- split at line boundaries.
		chunks = append(chunks, splitBlockByLines(block, maxLen, format)...)
	}

	if current != "" {
		chunks = append(chunks, formattedChunk{Raw: current, Formatted: format(current)})
	}

	if len(chunks) == 0 {
		// Should not happen, but guard against empty input.
		return []formattedChunk{{Raw: raw, Formatted: format(raw)}}
	}
	return chunks
}

// splitBlockByLines splits a single paragraph-level block at line boundaries,
// falling back to hard byte splits for individual lines that still exceed the
// limit after formatting.
func splitBlockByLines(block string, maxLen int, format func(string) string) []formattedChunk {
	lines := strings.Split(block, "\n")
	var chunks []formattedChunk
	var current string

	for _, line := range lines {
		candidate := current
		if candidate != "" {
			candidate += "\n" + line
		} else {
			candidate = line
		}

		if len(format(candidate)) <= maxLen {
			current = candidate
			continue
		}

		if current != "" {
			chunks = append(chunks, formattedChunk{Raw: current, Formatted: format(current)})
			current = ""
		}

		if len(format(line)) <= maxLen {
			current = line
		} else {
			// Single line exceeds limit -- hard-split the formatted output.
			formatted := format(line)
			for _, part := range splitMessage(formatted, maxLen) {
				chunks = append(chunks, formattedChunk{Raw: line, Formatted: part})
			}
			log.Printf("[channel] hard-split a single line (%d bytes formatted) across chunks", len(formatted))
		}
	}

	if current != "" {
		chunks = append(chunks, formattedChunk{Raw: current, Formatted: format(current)})
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
