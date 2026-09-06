package channel

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type telegramRoundTrip func(*http.Request) (*http.Response, error)

func (f telegramRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramSenderDoesNotFollowRedirectsOrRequireAPoller(t *testing.T) {
	_, err := NewTelegramSender("synthetic", 0)
	require.Error(t, err)
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++ }))
	defer target.Close()
	calls := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/sendMessage", r.URL.Path)
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	tg, err := NewTelegramSender("synthetic", 42)
	require.NoError(t, err)
	tg.baseURL = origin.URL
	_, err = tg.SendMessage("0", "Synthetic notice")
	require.Error(t, err)
	require.Equal(t, 1, calls)
	require.Zero(t, redirected)
}

func TestTelegramAmbiguousSendIsNeverRetriedOrLeaked(t *testing.T) {
	tg := newTestTelegram(42)
	calls := 0
	tg.client.Transport = telegramRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("private token and transcript")
	})
	_, err := tg.SendMessage("0", "synthetic notice")
	require.Error(t, err)
	require.Equal(t, 1, calls, "a lost acknowledgement is not a formatting rejection")
	require.NotContains(t, err.Error(), "private token")
	require.NotContains(t, err.Error(), "test-token")
}

func TestTelegramRejectsUnboundedResponseAndMissingMessageReceipt(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 1024*1024+1), `{"ok":true,"result":{}}`, `{"ok":false,"description":"private transcript"}`} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) }))
			defer s.Close()
			tg := newTestTelegramWithURL(s.URL)
			id, err := tg.SendMessage("0", "synthetic")
			require.Error(t, err)
			require.Empty(t, id)
			require.Equal(t, 1, calls)
			require.NotContains(t, err.Error(), "private transcript")
		})
	}
}
