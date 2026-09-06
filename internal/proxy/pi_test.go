package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/require"
)

func piRequest(p *Pi, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	p.ServeHTTP(r, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return r
}
func TestPiProxyIsNarrowBoundedAndFinal(t *testing.T) {
	calls := 0
	p := NewPi(context.Background(), PiJob{Tokens: 400000}, func(context.Context, []byte) ([]byte, error) {
		calls++
		return []byte(`{"content":[{"type":"text","text":"fixture"}]}`), nil
	})
	for _, input := range []string{`{"name":"callback","args":{}}`, `{"name":"bash","args":{},"credentials":true}`, `{} {}`, strings.Repeat("x", MaxPiToolBytes+1)} {
		require.Equal(t, 400, piRequest(p, "/tool", input).Code)
	}
	require.Zero(t, calls)
	require.Equal(t, 404, piRequest(p, "/callback", "{}").Code)
	require.Equal(t, 410, piRequest(p, "/tool?container=other", "{}").Code)
	require.Equal(t, 200, piRequest(p, "/tool", `{"name":"read","args":{"path":"file.txt"}}`).Code)
	result := PiResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{Input: 10}, Requests: 1, Tools: 1, Provider: "openai-codex", Model: "gpt-6-astra", Thinking: "max"}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, 200, piRequest(p, "/result", string(raw)).Code)
	require.Equal(t, 200, piRequest(p, "/result", string(raw)).Code)
	require.Equal(t, 409, piRequest(p, "/tool", `{"name":"bash","args":{}}`).Code)
	result.Usage.Input = 11
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, 409, piRequest(p, "/result", string(raw)).Code)
	require.EqualValues(t, 10, p.Result().Usage.Input)
	require.Equal(t, 1, calls)
}
func TestPiProxyUsageAndIdentityDoNotComeFromTools(t *testing.T) {
	job := PiJob{Tokens: 400000}
	valid := PiResult{Outcome: "executor_returned", Usage: &store.SubscriptionUsage{}, Requests: 1, Provider: "openai-codex", Model: "gpt-6-astra", Thinking: "max"}
	require.True(t, validPiResult(valid, job, 0))
	for _, mutate := range []func(*PiResult){
		func(r *PiResult) { r.Provider = "openai" }, func(r *PiResult) { r.Thinking = "high" }, func(r *PiResult) { r.Usage = nil }, func(r *PiResult) { r.Tools = 1 }, func(r *PiResult) { r.Fixture = true }, func(r *PiResult) { r.Requests = 251 }, func(r *PiResult) { r.Pause = "operator" }, func(r *PiResult) { r.Usage = &store.SubscriptionUsage{Input: 400000, Output: 1} }, func(r *PiResult) { r.Usage = &store.SubscriptionUsage{Input: -1} },
	} {
		r := valid
		mutate(&r)
		require.False(t, validPiResult(r, job, 0))
	}
	valid.Outcome = "executor_failed"
	valid.Usage = nil
	valid.Failure = "usage_unknown"
	require.True(t, validPiResult(valid, job, 0), "unknown usage is an explicit receipt, not zero")
}
func TestPiProxyRefusesCall65AndCancelledRole(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	p := NewPi(ctx, PiJob{}, func(context.Context, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	for range 64 {
		require.Equal(t, 200, piRequest(p, "/tool", `{"name":"bash","args":{}}`).Code)
	}
	require.Equal(t, 409, piRequest(p, "/tool", `{"name":"bash","args":{}}`).Code)
	require.Equal(t, 64, calls)
	cancel()
	require.Equal(t, 410, piRequest(p, "/result", `{}`).Code)
}
