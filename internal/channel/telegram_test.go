package channel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTelegram constructs a Telegram struct for testing without starting
// pollUpdates or calling setCommands (no network).
func newTestTelegram(chatID int64) *Telegram {
	return &Telegram{
		token:        "test-token",
		chatID:       chatID,
		allowedUsers: map[int64]bool{},
		client:       &http.Client{Timeout: 1 * time.Second},
	}
}

func TestTelegram_Ready(t *testing.T) {
	t.Run("false when chatID is 0", func(t *testing.T) {
		tg := newTestTelegram(0)
		assert.False(t, tg.Ready())
	})

	t.Run("true when chatID is set", func(t *testing.T) {
		tg := newTestTelegram(12345)
		assert.True(t, tg.Ready())
	})
}

func TestTelegram_getChatID(t *testing.T) {
	t.Run("returns error when chatID is 0", func(t *testing.T) {
		tg := newTestTelegram(0)
		id, err := tg.getChatID()
		assert.Equal(t, int64(0), id)
		assert.ErrorIs(t, err, errChatNotDetected)
	})

	t.Run("returns ID when chatID is set", func(t *testing.T) {
		tg := newTestTelegram(12345)
		id, err := tg.getChatID()
		assert.Equal(t, int64(12345), id)
		assert.NoError(t, err)
	})
}

func TestTelegram_getChatID_blocksAPIMethods(t *testing.T) {
	tg := newTestTelegram(0)

	_, err := tg.CreateTopic("test")
	assert.ErrorIs(t, err, errChatNotDetected)

	_, err = tg.SendMessage("0", "hello")
	assert.ErrorIs(t, err, errChatNotDetected)

	_, err = tg.ReplyMessage("0", "hello", "1")
	assert.ErrorIs(t, err, errChatNotDetected)

	_, err = tg.SendApproval("0", "hello", "job-1")
	assert.ErrorIs(t, err, errChatNotDetected)

	assert.ErrorIs(t, tg.EditMessage("0", "1", "hello"), errChatNotDetected)
	assert.ErrorIs(t, tg.CloseTopic("123"), errChatNotDetected)
	assert.ErrorIs(t, tg.SendTyping("0"), errChatNotDetected)
}

func TestTelegram_autoDetect(t *testing.T) {
	tg := newTestTelegram(0)

	var detected int64
	var callCount int
	var mu sync.Mutex

	tg.OnChatDetected(func(chatID int64) {
		mu.Lock()
		detected = chatID
		callCount++
		mu.Unlock()
	})

	require.False(t, tg.Ready())

	// Simulate what pollUpdates does when it sees the first message.
	tg.detectOnce.Do(func() {
		tg.mu.Lock()
		tg.chatID = 99999
		cb := tg.onChatDetected
		tg.mu.Unlock()
		if cb != nil {
			cb(99999)
		}
	})

	assert.True(t, tg.Ready())
	id, err := tg.getChatID()
	assert.NoError(t, err)
	assert.Equal(t, int64(99999), id)

	mu.Lock()
	assert.Equal(t, int64(99999), detected)
	assert.Equal(t, 1, callCount)
	mu.Unlock()

	// Second call to detectOnce.Do should be a no-op.
	tg.detectOnce.Do(func() {
		mu.Lock()
		callCount++
		mu.Unlock()
	})

	mu.Lock()
	assert.Equal(t, 1, callCount, "callback must fire exactly once")
	mu.Unlock()
}

func TestSplitMessage(t *testing.T) {
	t.Run("short message returns single chunk", func(t *testing.T) {
		chunks := splitMessage("hello world", telegramMaxLen)
		require.Len(t, chunks, 1)
		assert.Equal(t, "hello world", chunks[0])
	})

	t.Run("exact limit returns single chunk", func(t *testing.T) {
		text := strings.Repeat("a", telegramMaxLen)
		chunks := splitMessage(text, telegramMaxLen)
		require.Len(t, chunks, 1)
		assert.Equal(t, text, chunks[0])
	})

	t.Run("splits at newline when possible", func(t *testing.T) {
		// Build a message just over the limit with a newline in the last 25%.
		line1 := strings.Repeat("a", 3500)
		line2 := strings.Repeat("b", 400)
		line3 := strings.Repeat("c", 300)
		text := line1 + "\n" + line2 + "\n" + line3
		chunks := splitMessage(text, telegramMaxLen)
		require.Len(t, chunks, 2)
		// First chunk should end right after the newline following line1+line2.
		assert.True(t, strings.HasSuffix(chunks[0], line2+"\n"))
		assert.Equal(t, line3, chunks[1])
	})

	t.Run("hard splits when no newline in range", func(t *testing.T) {
		text := strings.Repeat("x", 5000)
		chunks := splitMessage(text, telegramMaxLen)
		require.Len(t, chunks, 2)
		assert.Equal(t, telegramMaxLen, len(chunks[0]))
		assert.Equal(t, 904, len(chunks[1]))
	})

	t.Run("multiple chunks for very long text", func(t *testing.T) {
		text := strings.Repeat("z", 10000)
		chunks := splitMessage(text, telegramMaxLen)
		require.Len(t, chunks, 3)
		assert.Equal(t, telegramMaxLen, len(chunks[0]))
		assert.Equal(t, telegramMaxLen, len(chunks[1]))
		assert.Equal(t, 1808, len(chunks[2]))
	})

	t.Run("respects different maxLen", func(t *testing.T) {
		text := strings.Repeat("x", 100)
		chunks := splitMessage(text, 30)
		require.Len(t, chunks, 4)
		assert.Equal(t, 30, len(chunks[0]))
		assert.Equal(t, 30, len(chunks[1]))
		assert.Equal(t, 30, len(chunks[2]))
		assert.Equal(t, 10, len(chunks[3]))
	})
}

func TestTelegram_SendMessage_formatsInternally(t *testing.T) {
	// Start a test server that records requests and responds with success.
	var mu sync.Mutex
	var requests []map[string]interface{}

	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		mu.Lock()
		requests = append(requests, params)
		mu.Unlock()
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	_, err := tg.SendMessage("0", "**bold** text")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Equal(t, "MarkdownV2", requests[0]["parse_mode"])
	// The text should be MarkdownV2-formatted, not raw markdown.
	assert.Contains(t, requests[0]["text"], "*bold*")
	assert.NotContains(t, requests[0]["text"], "**bold**")
}

func TestTelegram_SendMessage_splitsLongMessages(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]interface{}

	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		mu.Lock()
		requests = append(requests, params)
		mu.Unlock()
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	// Build a message with two paragraphs that exceed the limit when combined.
	para1 := strings.Repeat("word ", 500) // ~2500 chars
	para2 := strings.Repeat("text ", 500) // ~2500 chars
	msg := para1 + "\n\n" + para2

	_, err := tg.SendMessage("0", msg)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, len(requests) >= 2, "long message should be split into multiple sends")
	for i, req := range requests {
		text, _ := req["text"].(string)
		assert.LessOrEqual(t, len(text), telegramMaxLen, "chunk %d exceeds limit", i)
		assert.Equal(t, "MarkdownV2", req["parse_mode"])
	}
}

func TestTelegram_FormatText_passthrough(t *testing.T) {
	tg := newTestTelegram(12345)
	assert.Equal(t, "**bold**", tg.FormatText("**bold**"), "FormatText should return input unchanged")
}

// newTestTelegramWithURL creates a Telegram struct that talks to a local test server.
func newTestTelegramWithURL(chatID int64, baseURL string) *Telegram {
	return &Telegram{
		token:        "test-token",
		chatID:       chatID,
		allowedUsers: map[int64]bool{},
		client:       &http.Client{Timeout: 2 * time.Second},
		baseURL:      baseURL,
	}
}

// newTelegramTestServer starts an HTTP server that mimics the Telegram Bot API.
// The onRequest callback receives the parsed request body for assertions.
func newTelegramTestServer(t *testing.T, onRequest func(params map[string]interface{})) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var params map[string]interface{}
		json.Unmarshal(body, &params)
		if onRequest != nil {
			onRequest(params)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
}

func TestTelegram_autoDetect_nilCallback(t *testing.T) {
	tg := newTestTelegram(0)
	// No OnChatDetected registered -- should not panic.
	tg.detectOnce.Do(func() {
		tg.mu.Lock()
		tg.chatID = 88888
		cb := tg.onChatDetected
		tg.mu.Unlock()
		if cb != nil {
			cb(88888)
		}
	})

	assert.True(t, tg.Ready())
	id, err := tg.getChatID()
	assert.NoError(t, err)
	assert.Equal(t, int64(88888), id)
}
