package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPiProxyDebugIsOperatorOwnedAndOffByDefault(t *testing.T) {
	limits := pidebug.FixedLimits()
	p := NewPi(context.Background(), PiJob{Debug: &limits, Brief: json.RawMessage(`{"debug_dir":"/untrusted","debug":{"events":9999}}`)}, nil, nil)
	response := httptest.NewRecorder()
	p.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/job", nil))
	var job PiJob
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &job))
	require.Nil(t, job.Debug)
	require.Equal(t, http.StatusNotFound, piRequest(p, "/debug", `{"sequence":1,"event":{"kind":"assistant_text","text":"not captured"}}`).Code)
}

func TestPiProxyCaptureClosureCannotChangeAccountingOrBeUpgradedByReplay(t *testing.T) {
	for _, ack := range []string{"closed", "incomplete", ""} {
		t.Run("ack_"+ack, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			id := pidebug.Identity{V: 1, Pylon: "fixture", Job: uuid.NewString(), Base: strings.Repeat("a", 40), Image: "sha256:" + strings.Repeat("b", 64), Context: pidebug.Context("fixture", "/repo", "/auth", "/evidence")}
			r, err := pidebug.Start(root, id, "/repo", "/auth", "/evidence")
			require.NoError(t, err)
			t.Cleanup(func() { r.Close("executor_failed", "pi_fixture", true) })
			p := NewPi(context.Background(), PiJob{Tokens: 400000}, nil, r)
			require.Equal(t, 200, piRequest(p, "/debug", `{"sequence":1,"event":{"kind":"assistant_text","text":"Private text"}}`).Code)
			require.Equal(t, 200, piRequest(p, "/debug", `{"sequence":2,"close":{"events":1,"dropped_events":0,"truncated":false,"incomplete":false}}`).Code)
			result := PiResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{Input: 5}, Requests: 1, Provider: "openai-codex", Model: "gpt-6-astra", Thinking: "max"}
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			send := func(header string) {
				request := httptest.NewRequest(http.MethodPost, "/result", strings.NewReader(string(raw)))
				if header != "" {
					request.Header.Set("X-Pylon-Pi-Capture", header)
				}
				response := httptest.NewRecorder()
				p.ServeHTTP(response, request)
				require.Equal(t, 200, response.Code)
			}
			send(ack)
			send("closed")
			summary := r.Close("executor_returned", "", true)
			require.Equal(t, ack == "closed", summary.RuntimeClosed)
			require.Equal(t, ack != "closed", summary.Incomplete)
			require.Equal(t, result, *p.Result(), "capture status cannot alter the validated usage/outcome")
		})
	}
}

func TestPiProxyDebugRejectsOversizedUnknownAndForgedHostFrames(t *testing.T) {
	for _, body := range []string{
		`{"sequence":1,"source":"host","event":{"kind":"assistant_text","text":"unknown"}}`,
		`{"sequence":1,"event":{"kind":"diff","outcome":"nonempty"}}`,
		`{"sequence":1,"event":{"kind":"tool_start","tool":"read","arguments":{"path":{"authorization":"CANARY"},"path":"safe"}}}`,
		strings.Repeat("x", pidebug.MaxEventBytes+1),
	} {
		root := t.TempDir()
		require.NoError(t, os.Chmod(root, 0700))
		id := pidebug.Identity{V: 1, Pylon: "fixture", Job: uuid.NewString(), Base: strings.Repeat("a", 40), Image: "sha256:" + strings.Repeat("b", 64), Context: pidebug.Context("fixture", "/repo", "/auth", "/evidence")}
		r, err := pidebug.Start(root, id, "/repo")
		require.NoError(t, err)
		p := NewPi(context.Background(), PiJob{}, nil, r)
		require.Equal(t, 400, piRequest(p, "/debug", body).Code)
		summary := r.Close("executor_failed", "pi_fixture", true)
		require.Equal(t, 1, summary.EventCount)
	}
}
