package channel

import (
	"net/http"
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
