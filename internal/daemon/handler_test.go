package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDaemon creates a Daemon with a mock channel and temp stores.
// The returned daemon has one pylon "test-pylon" registered at /webhook/test.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	ms := store.NewMulti(map[string]*store.Store{"test-pylon": st})

	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}

	pylons := map[string]*config.PylonConfig{
		"test-pylon": {
			Name:      "test-pylon",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/webhook/test"},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test prompt"},
		},
	}

	ch := newMockChannel()
	d := New(global, pylons, ms, ch, nil)
	return d
}

// newTestDaemonWithSignature creates a daemon with webhook signature verification.
func newTestDaemonWithSignature(t *testing.T, secret, header string) *Daemon {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	ms := store.NewMulti(map[string]*store.Store{"signed-pylon": st})

	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}

	pylons := map[string]*config.PylonConfig{
		"signed-pylon": {
			Name: "signed-pylon",
			Trigger: config.TriggerConfig{
				Type:            "webhook",
				Path:            "/webhook/signed",
				Secret:          secret,
				SignatureHeader: header,
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	ch := newMockChannel()
	return New(global, pylons, ms, ch, nil)
}

func TestWebhookHandler(t *testing.T) {
	d := newTestDaemon(t)

	t.Run("POST valid JSON returns 202", func(t *testing.T) {
		body := `{"repo":"https://github.com/test/repo","ref":"main"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook/test", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)

		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEmpty(t, resp["job_id"])
		_, err := uuid.Parse(resp["job_id"])
		assert.NoError(t, err, "job_id should be a valid UUID")
		assert.Equal(t, "accepted", resp["status"])
	})

	t.Run("POST non-JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhook/test", strings.NewReader("not json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/webhook/test", nil)
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestWebhookSignatureVerification(t *testing.T) {
	d := newTestDaemonWithSignature(t, "mysecret", "X-Hub-Signature-256")

	t.Run("missing signature returns 401", func(t *testing.T) {
		body := `{"test":"data"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook/signed", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wrong signature returns 401", func(t *testing.T) {
		body := `{"test":"data"}`
		req := httptest.NewRequest(http.MethodPost, "/webhook/signed", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature-256", "deadbeef")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestTriggerHandler(t *testing.T) {
	d := newTestDaemon(t)

	t.Run("POST existing pylon returns 202", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/trigger/test-pylon", nil)
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusAccepted, w.Code)

		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.NotEmpty(t, resp["job_id"])
		_, err := uuid.Parse(resp["job_id"])
		assert.NoError(t, err, "job_id should be a valid UUID")
		assert.Equal(t, "triggered", resp["status"])
	})

	t.Run("POST nonexistent pylon returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/trigger/nonexistent", nil)
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/trigger/test-pylon", nil)
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestCallbackHandler(t *testing.T) {
	d := newTestDaemon(t)

	// Put a job in the store so the callback can find it
	d.Store.Put(&store.Job{
		ID: "cb-job-1", PylonName: "test-pylon", Status: "running",
		CreatedAt: time.Now(),
	})

	t.Run("completed callback", func(t *testing.T) {
		body := `{"status":"completed","output":{"result":"all good"}}`
		req := httptest.NewRequest(http.MethodPost, "/callback/cb-job-1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		job, ok := d.Store.Get("cb-job-1")
		require.True(t, ok)
		assert.Equal(t, "completed", job.Status)
	})

	t.Run("failed callback", func(t *testing.T) {
		d.Store.Put(&store.Job{
			ID: "cb-job-2", PylonName: "test-pylon", Status: "running",
			CreatedAt: time.Now(),
		})
		body := `{"status":"failed","error":"container crashed"}`
		req := httptest.NewRequest(http.MethodPost, "/callback/cb-job-2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		job, ok := d.Store.Get("cb-job-2")
		require.True(t, ok)
		assert.Equal(t, "failed", job.Status)
		assert.Equal(t, "container crashed", job.Error)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/callback/cb-job-1", strings.NewReader("not json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/callback/cb-job-1", nil)
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestHooksHandler(t *testing.T) {
	d := newTestDaemon(t)

	t.Run("stores tool event", func(t *testing.T) {
		body := `{"tool_name":"Bash","tool_input":{"command":"ls"}}`
		req := httptest.NewRequest(http.MethodPost, "/hooks/test-job-1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		d.hooksMu.Lock()
		events := d.hookLog["test-job-1"]
		d.hooksMu.Unlock()
		require.Len(t, events, 1)
		assert.Equal(t, "$ ls", events[0])
	})

	t.Run("caps at 8 entries", func(t *testing.T) {
		for i := range 10 {
			body := `{"tool_name":"Read","tool_input":{"file_path":"/file` + strings.Repeat("x", i) + `"}}`
			req := httptest.NewRequest(http.MethodPost, "/hooks/cap-job", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			d.Mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		}

		d.hooksMu.Lock()
		events := d.hookLog["cap-job"]
		d.hooksMu.Unlock()
		assert.Len(t, events, 8)
	})

	// Stream-processor style payloads: the NDJSON processors in both agent
	// entrypoints POST tool events with the raw tool.input object from the
	// agent's stream output.
	t.Run("claude stream-json tool payloads", func(t *testing.T) {
		jobID := "claude-stream-job"

		// Edit with nested input
		body := `{"tool_name":"Edit","tool_input":{"file_path":"/workspace/main.go","old_string":"foo","new_string":"bar"}}`
		req := httptest.NewRequest(http.MethodPost, "/hooks/"+jobID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		// Bash with command
		body = `{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
		req = httptest.NewRequest(http.MethodPost, "/hooks/"+jobID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		// Grep with pattern
		body = `{"tool_name":"Grep","tool_input":{"pattern":"handleSubmit","path":"/workspace"}}`
		req = httptest.NewRequest(http.MethodPost, "/hooks/"+jobID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		d.Mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		d.hooksMu.Lock()
		events := d.hookLog[jobID]
		d.hooksMu.Unlock()
		require.Len(t, events, 3)
		assert.Equal(t, "Editing /workspace/main.go", events[0])
		assert.Equal(t, "$ go test ./...", events[1])
		assert.Equal(t, "Grep handleSubmit", events[2])
	})

	t.Run("rapid events from same job", func(t *testing.T) {
		jobID := "rapid-job"
		tools := []struct {
			name  string
			input string
			want  string
		}{
			{"Read", `{"file_path":"/a.go"}`, "Reading /a.go"},
			{"Read", `{"file_path":"/b.go"}`, "Reading /b.go"},
			{"Glob", `{"pattern":"*.ts"}`, "Glob *.ts"},
			{"Edit", `{"file_path":"/a.go"}`, "Editing /a.go"},
			{"Bash", `{"command":"npm test"}`, "$ npm test"},
		}
		for _, tool := range tools {
			body := `{"tool_name":"` + tool.name + `","tool_input":` + tool.input + `}`
			req := httptest.NewRequest(http.MethodPost, "/hooks/"+jobID, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			d.Mux.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		}

		d.hooksMu.Lock()
		events := d.hookLog[jobID]
		d.hooksMu.Unlock()
		require.Len(t, events, len(tools))
		for i, tool := range tools {
			assert.Equal(t, tool.want, events[i])
		}
	})
}
