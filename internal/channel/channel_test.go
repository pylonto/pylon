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
