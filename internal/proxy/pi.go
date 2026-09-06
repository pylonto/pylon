package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/pylonto/pylon/internal/store"
)

const MaxPiToolBytes = 256 * 1024

// PiJob is supplied only by the executor over its private Unix socket. The
// signed brief is unchanged; these fields describe execution, not patch policy.
type PiJob struct {
	Brief       json.RawMessage `json:"brief"`
	Deadline    int64           `json:"deadline"`
	Tokens      int64           `json:"tokens"`
	Fixture     bool            `json:"fixture"`
	FixtureCase string          `json:"fixture_case,omitempty"`
}

type PiResult struct {
	Outcome  string                   `json:"outcome"`
	Usage    *store.SubscriptionUsage `json:"usage"`
	Pause    string                   `json:"pause"`
	Failure  string                   `json:"failure"`
	Requests int                      `json:"requests"`
	Tools    int                      `json:"tools"`
	Provider string                   `json:"provider"`
	Model    string                   `json:"model"`
	Thinking string                   `json:"thinking"`
	Fixture  bool                     `json:"fixture"`
}

// Pi exposes no daemon route, credentials, arbitrary container ID, destination,
// shell-on-host operation or publication callback. Only the immutable runtime
// container mounts this socket; the untrusted tool container cannot reach it.
type Pi struct {
	ctx    context.Context
	job    PiJob
	tool   func(context.Context, []byte) ([]byte, error)
	mu     sync.Mutex
	result *PiResult
	toolMu sync.Mutex
	calls  int
}

func NewPi(ctx context.Context, job PiJob, tool func(context.Context, []byte) ([]byte, error)) *Pi {
	return &Pi{ctx: ctx, job: job, tool: tool}
}

func decodePi(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("pi_invalid_json")
	}
	return nil
}

func (p *Pi) Result() *PiResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.result == nil {
		return nil
	}
	copy := *p.result
	return &copy
}

func (p *Pi) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.ctx.Err() != nil || r.URL.RawQuery != "" {
		http.Error(w, "pi_transport_closed", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/job":
		json.NewEncoder(w).Encode(p.job)
	case r.Method == http.MethodPost && r.URL.Path == "/tool":
		p.toolMu.Lock()
		defer p.toolMu.Unlock()
		if p.Result() != nil || p.calls >= 64 {
			http.Error(w, "pi_tool_admission_refused", http.StatusConflict)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPiToolBytes))
		var input struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if err != nil || decodePi(raw, &input) != nil || len(input.Args) == 0 ||
			(input.Name != "read" && input.Name != "write" && input.Name != "edit" && input.Name != "bash") {
			http.Error(w, "pi_tool_input_refused", http.StatusBadRequest)
			return
		}
		p.calls++
		result, err := p.tool(p.ctx, raw)
		if err != nil || len(result) > MaxPiToolBytes || !json.Valid(result) {
			http.Error(w, "pi_tool_execution_failed", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(result)
	case r.Method == http.MethodPost && r.URL.Path == "/result":
		p.toolMu.Lock()
		defer p.toolMu.Unlock()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
		var result PiResult
		if err != nil || decodePi(raw, &result) != nil || !validPiResult(result, p.job, p.calls) {
			http.Error(w, "pi_result_refused", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.result != nil {
			old, _ := json.Marshal(p.result)
			next, _ := json.Marshal(result)
			if !bytes.Equal(old, next) {
				http.Error(w, "pi_result_conflict", http.StatusConflict)
				return
			}
		}
		p.result = &result
		_, _ = w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func validPiResult(r PiResult, job PiJob, tools int) bool {
	if r.Fixture != job.Fixture || r.Requests < 0 || r.Requests > 250 || r.Tools != tools ||
		r.Provider != "openai-codex" || r.Model != "gpt-6-astra" || r.Thinking != "max" {
		return false
	}
	if r.Outcome != "executor_returned" && r.Outcome != "executor_failed" {
		return false
	}
	if r.Pause != "" && r.Pause != "auth_unavailable" && r.Pause != "quota_exhausted" {
		return false
	}
	switch r.Failure {
	case "", "auth_unavailable", "quota_exhausted", "model_unavailable", "request_budget_exhausted", "provider_failed", "usage_unknown", "runtime_failed", "output_bound", "deadline":
	default:
		return false
	}
	if r.Outcome == "executor_returned" && (r.Failure != "" || r.Pause != "" || r.Usage == nil) {
		return false
	}
	if r.Usage != nil {
		remaining := job.Tokens
		for _, n := range []int64{r.Usage.Input, r.Usage.Output, r.Usage.CacheRead, r.Usage.CacheWrite} {
			if n < 0 || n > remaining {
				return false
			}
			remaining -= n
		}
	}
	return !job.Fixture || r.Requests == 0
}
