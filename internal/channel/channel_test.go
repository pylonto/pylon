package channel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channelLimit pairs a channel's max message length with its name for table-driven tests.
// Every channel implementation must register here so the contract test catches
// missing split logic early.
var channelLimits = []struct {
	name   string
	maxLen int
}{
	{"telegram", telegramMaxLen},
	{"slack", slackMaxLen},
}

func TestSplitAndFormat(t *testing.T) {
	identity := func(s string) string { return s }

	t.Run("short text returns single chunk", func(t *testing.T) {
		chunks := splitAndFormat("hello world", 100, identity)
		require.Len(t, chunks, 1)
		assert.Equal(t, "hello world", chunks[0].Formatted)
		assert.Equal(t, "hello world", chunks[0].Raw)
	})

	t.Run("splits at paragraph boundaries", func(t *testing.T) {
		para1 := strings.Repeat("a", 50)
		para2 := strings.Repeat("b", 50)
		text := para1 + "\n\n" + para2
		chunks := splitAndFormat(text, 60, identity)
		require.Len(t, chunks, 2)
		assert.Equal(t, para1, chunks[0].Formatted)
		assert.Equal(t, para2, chunks[1].Formatted)
	})

	t.Run("keeps paragraphs together when they fit", func(t *testing.T) {
		text := "short\n\nalso short"
		chunks := splitAndFormat(text, 100, identity)
		require.Len(t, chunks, 1)
		assert.Equal(t, text, chunks[0].Formatted)
	})

	t.Run("falls back to line splitting for large paragraphs", func(t *testing.T) {
		line1 := strings.Repeat("x", 30)
		line2 := strings.Repeat("y", 30)
		block := line1 + "\n" + line2 // single paragraph, 61 bytes
		chunks := splitAndFormat(block, 40, identity)
		require.Len(t, chunks, 2)
		assert.Equal(t, line1, chunks[0].Formatted)
		assert.Equal(t, line2, chunks[1].Formatted)
	})

	t.Run("hard splits oversized single line", func(t *testing.T) {
		line := strings.Repeat("z", 100)
		chunks := splitAndFormat(line, 40, identity)
		require.True(t, len(chunks) >= 3, "should produce multiple chunks")
		for _, c := range chunks {
			assert.LessOrEqual(t, len(c.Formatted), 40)
		}
	})

	t.Run("accounts for format expansion", func(t *testing.T) {
		// Simulate a format function that doubles the length (like MarkdownV2 escaping).
		doubler := func(s string) string { return s + s }
		text := strings.Repeat("a", 30) + "\n\n" + strings.Repeat("b", 30)
		// Raw is 63 bytes but formatted is 126. With limit 80, each paragraph
		// formatted is 60 bytes which fits.
		chunks := splitAndFormat(text, 80, doubler)
		require.Len(t, chunks, 2)
		assert.Equal(t, 60, len(chunks[0].Formatted))
		assert.Equal(t, 60, len(chunks[1].Formatted))
	})

	t.Run("code block stays intact across paragraph split", func(t *testing.T) {
		// A code block between paragraphs should not be split.
		before := strings.Repeat("a", 20)
		code := "```go\nfmt.Println(\"hello\")\nfmt.Println(\"world\")\n```"
		after := strings.Repeat("b", 20)
		text := before + "\n\n" + code + "\n\n" + after
		// With a generous limit, everything fits in one chunk.
		chunks := splitAndFormat(text, 200, identity)
		require.Len(t, chunks, 1)
		assert.Contains(t, chunks[0].Formatted, "```go")
		assert.Contains(t, chunks[0].Formatted, "```")
	})

	t.Run("code block preserved when paragraphs are split", func(t *testing.T) {
		before := strings.Repeat("a", 80)
		code := "```\nline1\nline2\n```"
		after := strings.Repeat("b", 80)
		text := before + "\n\n" + code + "\n\n" + after
		// Limit forces splitting but code block should stay in one chunk.
		chunks := splitAndFormat(text, 90, identity)
		require.True(t, len(chunks) >= 2)
		// Find the chunk containing the code block and verify it's complete.
		var codeChunk string
		for _, c := range chunks {
			if strings.Contains(c.Formatted, "```") {
				codeChunk = c.Formatted
				break
			}
		}
		require.NotEmpty(t, codeChunk, "should have a chunk with a code block")
		assert.Equal(t, 2, strings.Count(codeChunk, "```"), "code block delimiters should be balanced")
	})

	t.Run("raw and formatted differ", func(t *testing.T) {
		upper := func(s string) string { return strings.ToUpper(s) }
		chunks := splitAndFormat("hello", 100, upper)
		require.Len(t, chunks, 1)
		assert.Equal(t, "hello", chunks[0].Raw)
		assert.Equal(t, "HELLO", chunks[0].Formatted)
	})

	t.Run("works with MarkdownToTelegramV2", func(t *testing.T) {
		text := "**bold** and `code`\n\nSecond paragraph with a [link](https://example.com)."
		chunks := splitAndFormat(text, telegramMaxLen, MarkdownToTelegramV2)
		require.Len(t, chunks, 1)
		// Should have Telegram formatting, not raw markdown.
		assert.Contains(t, chunks[0].Formatted, "*bold*")
		assert.NotContains(t, chunks[0].Formatted, "**bold**")
	})
}

func TestSplitMessage_allChannelLimits(t *testing.T) {
	for _, ch := range channelLimits {
		t.Run(ch.name+" splits oversized text", func(t *testing.T) {
			text := strings.Repeat("x", ch.maxLen+500)
			chunks := splitMessage(text, ch.maxLen)
			require.True(t, len(chunks) >= 2, "text over %d bytes should produce multiple chunks", ch.maxLen)
			for i, chunk := range chunks {
				assert.LessOrEqual(t, len(chunk), ch.maxLen, "chunk %d exceeds %s limit of %d", i, ch.name, ch.maxLen)
			}
		})

		t.Run(ch.name+" fits in single chunk", func(t *testing.T) {
			text := strings.Repeat("x", ch.maxLen)
			chunks := splitMessage(text, ch.maxLen)
			require.Len(t, chunks, 1)
		})
	}
}
