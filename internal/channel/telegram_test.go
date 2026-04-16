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

func TestTelegram_isAllowed(t *testing.T) {
	t.Run("empty allowlist allows everyone", func(t *testing.T) {
		tg := newTestTelegram(12345)
		assert.True(t, tg.isAllowed(999))
		assert.True(t, tg.isAllowed(0))
	})

	t.Run("non-empty allowlist restricts", func(t *testing.T) {
		tg := &Telegram{
			token:        "test-token",
			chatID:       12345,
			allowedUsers: map[int64]bool{111: true, 222: true},
			client:       &http.Client{Timeout: 1 * time.Second},
		}
		assert.True(t, tg.isAllowed(111))
		assert.True(t, tg.isAllowed(222))
		assert.False(t, tg.isAllowed(333))
		assert.False(t, tg.isAllowed(0))
	})
}

func TestTelegram_OnAction(t *testing.T) {
	tg := newTestTelegram(12345)

	var gotJobID, gotAction string
	tg.OnAction(func(jobID, action string) {
		gotJobID = jobID
		gotAction = action
	})

	tg.mu.Lock()
	fn := tg.actionFn
	tg.mu.Unlock()

	assert.NotNil(t, fn)
	fn("job-1", "investigate")
	assert.Equal(t, "job-1", gotJobID)
	assert.Equal(t, "investigate", gotAction)
}

func TestTelegram_OnMessage(t *testing.T) {
	tg := newTestTelegram(12345)

	var gotTopic, gotText, gotMsgID string
	tg.OnMessage(func(topicID, text, messageID string) {
		gotTopic = topicID
		gotText = text
		gotMsgID = messageID
	})

	tg.mu.Lock()
	fn := tg.messageFn
	tg.mu.Unlock()

	assert.NotNil(t, fn)
	fn("topic-1", "hello", "msg-1")
	assert.Equal(t, "topic-1", gotTopic)
	assert.Equal(t, "hello", gotText)
	assert.Equal(t, "msg-1", gotMsgID)
}

func TestTelegram_Commands(t *testing.T) {
	tg := newTestTelegram(12345)
	cmds := tg.Commands()
	assert.Equal(t, BotCommands, cmds)
	assert.True(t, len(cmds) > 0)
}

func TestIsGroup(t *testing.T) {
	assert.True(t, isGroup("group"))
	assert.True(t, isGroup("supergroup"))
	assert.False(t, isGroup("private"))
	assert.False(t, isGroup("channel"))
	assert.False(t, isGroup(""))
}

func TestTelegram_CreateTopic(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedParams)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":{"message_thread_id":42}}`)
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	topicID, err := tg.CreateTopic("Test Topic")
	require.NoError(t, err)
	assert.Equal(t, "42", topicID)
	assert.Equal(t, "Test Topic", receivedParams["name"])
}

func TestTelegram_CreateTopic_truncatesLongNames(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	longName := strings.Repeat("a", 200)
	_, err := tg.CreateTopic(longName)
	require.NoError(t, err)

	name, _ := receivedParams["name"].(string)
	assert.Equal(t, 128, len(name), "topic name should be truncated to 128 chars")
}

func TestTelegram_ReplyMessage(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		mu.Lock()
		requests = append(requests, params)
		mu.Unlock()
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	_, err := tg.ReplyMessage("42", "reply text", "100")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, len(requests) >= 1)
	// First chunk should have reply_to_message_id
	assert.Equal(t, float64(100), requests[0]["reply_to_message_id"])
	assert.Equal(t, float64(42), requests[0]["message_thread_id"])
}

func TestTelegram_SendApproval(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	_, err := tg.SendApproval("42", "Approve this?", "job-123")
	require.NoError(t, err)

	assert.NotNil(t, receivedParams["reply_markup"], "should include inline keyboard")
	markup := receivedParams["reply_markup"].(map[string]interface{})
	keyboard := markup["inline_keyboard"].([]interface{})
	assert.Len(t, keyboard, 1, "should have one row of buttons")
	row := keyboard[0].([]interface{})
	assert.Len(t, row, 2, "should have Investigate and Ignore buttons")
}

func TestTelegram_EditMessage(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	err := tg.EditMessage("42", "100", "updated text")
	assert.NoError(t, err)
	assert.Equal(t, float64(100), receivedParams["message_id"])
	assert.Equal(t, "MarkdownV2", receivedParams["parse_mode"])
}

func TestTelegram_EditMessage_fallsBackToPlaintext(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// First call (MarkdownV2) fails
			fmt.Fprintf(w, `{"ok":false,"description":"can't parse entities"}`)
		} else {
			// Second call (plaintext) succeeds
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":100}}`)
		}
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	err := tg.EditMessage("0", "100", "**broken** markdown")
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount, "should retry with plaintext after MarkdownV2 failure")
}

func TestTelegram_CloseTopic(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	err := tg.CloseTopic("42")
	assert.NoError(t, err)
	assert.Equal(t, float64(42), receivedParams["message_thread_id"])
}

func TestTelegram_CloseTopic_zeroIsNoop(t *testing.T) {
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		// Should not be called
		assert.Fail(t, "API should not be called for topicID 0")
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)
	err := tg.CloseTopic("0")
	assert.NoError(t, err)
}

func TestTelegram_SendTyping(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	err := tg.SendTyping("42")
	assert.NoError(t, err)
	assert.Equal(t, "typing", receivedParams["action"])
	assert.Equal(t, float64(42), receivedParams["message_thread_id"])
}

func TestTelegram_SendTyping_noTopic(t *testing.T) {
	var receivedParams map[string]interface{}
	ts := newTelegramTestServer(t, func(params map[string]interface{}) {
		receivedParams = params
	})
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	err := tg.SendTyping("0")
	assert.NoError(t, err)
	assert.Equal(t, "typing", receivedParams["action"])
	_, hasThreadID := receivedParams["message_thread_id"]
	assert.False(t, hasThreadID, "should not include message_thread_id for topicID 0")
}

func TestTelegram_TestConnection(t *testing.T) {
	ts := newTelegramTestServer(t, nil)
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)
	assert.NoError(t, tg.TestConnection())
}

func TestTelegram_TestConnection_failure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":false,"description":"Unauthorized"}`)
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)
	assert.Error(t, tg.TestConnection())
}

func TestTelegram_callAPI_failure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":false,"description":"Bad Request: chat not found"}`)
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)
	_, err := tg.callAPI("sendMessage", map[string]interface{}{"chat_id": 99999})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "chat not found")
}

func TestTelegram_callAPI_networkError(t *testing.T) {
	tg := newTestTelegramWithURL(12345, "http://localhost:1") // nothing listening
	_, err := tg.callAPI("getMe", map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "calling getMe")
}

func TestTelegram_callAPI_invalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)
	_, err := tg.callAPI("getMe", map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing")
}

func TestTelegram_SendMessage_fallsBackToPlaintext(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// MarkdownV2 fails
			fmt.Fprintf(w, `{"ok":false,"description":"can't parse entities"}`)
		} else {
			// Plaintext succeeds
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":1}}`)
		}
	}))
	defer ts.Close()

	tg := newTestTelegramWithURL(12345, ts.URL)

	_, err := tg.SendMessage("0", "test message")
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount, "should retry with plaintext")
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
