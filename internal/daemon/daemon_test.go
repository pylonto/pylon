package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain sets runner.JobsDir once for all daemon tests. Individual tests
// must NOT mutate runner.JobsDir because integration/cron tests spawn
// background goroutines (via runJob) that read it concurrently.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "daemon-test-jobs-*")
	if err != nil {
		panic(err)
	}
	runner.JobsDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestFormatToolEvent(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		want     string
	}{
		{"bash command", "Bash", `{"command":"ls -la"}`, "$ ls -la"},
		{"bash long command", "Bash", `{"command":"` + strings.Repeat("x", 210) + `"}`, "$ " + strings.Repeat("x", 200) + "..."},
		{"edit file_path", "Edit", `{"file_path":"/foo.go"}`, "Editing /foo.go"},
		{"edit filePath", "Edit", `{"filePath":"/foo.go"}`, "Editing /foo.go"},
		{"write", "Write", `{"file_path":"/bar.go"}`, "Writing /bar.go"},
		{"read", "Read", `{"file_path":"/baz.go"}`, "Reading /baz.go"},
		{"glob", "Glob", `{"pattern":"*.go"}`, "Glob *.go"},
		{"grep", "Grep", `{"pattern":"TODO"}`, "Grep TODO"},
		{"unknown tool", "UnknownTool", `{}`, "UnknownTool"},
		{"empty tool", "", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatToolEvent(tt.toolName, json.RawMessage(tt.input)))
		})
	}
}

// TestFormatToolEvent_hookPayloads verifies formatToolEvent handles the
// payloads that agent hook processors POST to the /hooks/ endpoint from
// both Claude and OpenCode agents.
func TestFormatToolEvent_hookPayloads(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		want     string
	}{
		// Claude stream-json style payloads (tool.input fields)
		{"claude read", "Read", `{"file_path":"/workspace/src/main.go"}`, "Reading /workspace/src/main.go"},
		{"claude edit", "Edit", `{"file_path":"/workspace/src/main.go","old_string":"foo","new_string":"bar"}`, "Editing /workspace/src/main.go"},
		{"claude write", "Write", `{"file_path":"/workspace/new.go","content":"package main"}`, "Writing /workspace/new.go"},
		{"claude bash", "Bash", `{"command":"go test ./..."}`, "$ go test ./..."},
		{"claude glob", "Glob", `{"pattern":"**/*.go"}`, "Glob **/*.go"},
		{"claude grep", "Grep", `{"pattern":"TODO","path":"/workspace"}`, "Grep TODO"},
		{"claude multiedit", "MultiEdit", `{"file_path":"/workspace/main.go"}`, "Editing /workspace/main.go"},

		// OpenCode style payloads
		{"opencode read", "Read", `{"file_path":"/workspace/pkg/api.go"}`, "Reading /workspace/pkg/api.go"},
		{"opencode bash", "Bash", `{"command":"npm test"}`, "$ npm test"},

		// Edge cases from stream processors
		{"empty input object", "Read", `{}`, "Reading "},
		{"null input", "Bash", `null`, "$ "},
		{"agent tool", "Agent", `{"prompt":"do something"}`, "Agent"},
		{"todowrite tool", "TodoWrite", `{"todos":[]}`, "TodoWrite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatToolEvent(tt.toolName, json.RawMessage(tt.input)))
		})
	}
}

func TestAppendEventToLog(t *testing.T) {
	// runner.JobsDir is set once by TestMain -- never mutate it here.
	jobID := "test-job-append-1234"

	// Create the log file (runner normally creates it when the container starts).
	logPath := runner.LogPath(jobID)
	require.NoError(t, os.WriteFile(logPath, []byte("--- run ---\n"), 0644))
	t.Cleanup(func() { os.Remove(logPath) })

	appendEventToLog(jobID, "$ git status")
	appendEventToLog(jobID, "Reading /workspace/main.go")
	appendEventToLog(jobID, "Editing /workspace/main.go")

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "--- run ---")
	assert.Contains(t, content, "[agent] [test-job] > $ git status")
	assert.Contains(t, content, "[agent] [test-job] > Reading /workspace/main.go")
	assert.Contains(t, content, "[agent] [test-job] > Editing /workspace/main.go")

	// Verify each event is on its own line
	lines := strings.Split(strings.TrimSpace(content), "\n")
	assert.Len(t, lines, 4) // header + 3 events
}

func TestAppendEventToLog_noFile(t *testing.T) {
	// runner.JobsDir is set by TestMain to a valid temp dir, but this jobID
	// has no log file -- appendEventToLog should silently return.
	appendEventToLog("no-such-job-12345678", "$ ls")
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"valid", `{"session_id":"abc123"}`, "abc123"},
		{"missing key", `{"other":"val"}`, ""},
		{"empty json", `{}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractSessionID(json.RawMessage(tt.output)))
		})
	}
}

func TestExtractResultText(t *testing.T) {
	t.Run("result key present", func(t *testing.T) {
		assert.Equal(t, "success", extractResultText(json.RawMessage(`{"result":"success"}`)))
	})

	t.Run("no result key returns raw json", func(t *testing.T) {
		raw := `{"other":"value"}`
		assert.Equal(t, raw, extractResultText(json.RawMessage(raw)))
	})

	t.Run("output at exactly 4000 bytes not truncated", func(t *testing.T) {
		// raw JSON is exactly 4000 bytes (including quotes): "xx...x"
		raw := `"` + strings.Repeat("x", 3998) + `"`
		assert.Equal(t, 4000, len(raw))
		result := extractResultText(json.RawMessage(raw))
		assert.Equal(t, 4000, len(result), "output at exactly 4000 bytes should not be truncated")
	})

	t.Run("output at 4001 bytes is truncated to 4000", func(t *testing.T) {
		raw := `"` + strings.Repeat("x", 3999) + `"`
		assert.Equal(t, 4001, len(raw))
		result := extractResultText(json.RawMessage(raw))
		assert.Equal(t, 4000, len(result), "output at 4001 bytes should be truncated to 4000")
	})

	t.Run("large output truncated", func(t *testing.T) {
		raw := `"` + strings.Repeat("x", 5000) + `"`
		result := extractResultText(json.RawMessage(raw))
		assert.Equal(t, 4000, len(result))
		assert.True(t, len(result) < len(raw), "truncated result should be shorter than input")
	})
}

func TestVerifySignature(t *testing.T) {
	secret := "mysecret"
	body := []byte(`{"test":"data"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	validSig := hex.EncodeToString(mac.Sum(nil))

	t.Run("valid signature", func(t *testing.T) {
		trigger := config.TriggerConfig{Secret: secret, SignatureHeader: "X-Signature"}
		header := http.Header{"X-Signature": {validSig}}
		assert.True(t, verifySignature(trigger, header, body))
	})

	t.Run("empty signature header name bypasses check", func(t *testing.T) {
		trigger := config.TriggerConfig{Secret: secret, SignatureHeader: ""}
		assert.True(t, verifySignature(trigger, http.Header{}, body))
	})

	t.Run("missing header value", func(t *testing.T) {
		trigger := config.TriggerConfig{Secret: secret, SignatureHeader: "X-Signature"}
		assert.False(t, verifySignature(trigger, http.Header{}, body))
	})

	t.Run("wrong signature", func(t *testing.T) {
		trigger := config.TriggerConfig{Secret: secret, SignatureHeader: "X-Signature"}
		header := http.Header{"X-Signature": {"deadbeef"}}
		assert.False(t, verifySignature(trigger, header, body))
	})

	t.Run("secret expanded from env var", func(t *testing.T) {
		t.Setenv("TEST_SECRET", secret)
		trigger := config.TriggerConfig{Secret: "$TEST_SECRET", SignatureHeader: "X-Signature"}
		// Signature computed against the actual secret value should pass
		header := http.Header{"X-Signature": {validSig}}
		assert.True(t, verifySignature(trigger, header, body))

		// Signature computed against the literal string "$TEST_SECRET" should fail
		wrongMAC := hmac.New(sha256.New, []byte("$TEST_SECRET"))
		wrongMAC.Write(body)
		literalSig := hex.EncodeToString(wrongMAC.Sum(nil))
		wrongHeader := http.Header{"X-Signature": {literalSig}}
		assert.False(t, verifySignature(trigger, wrongHeader, body),
			"should use expanded env var, not literal $TEST_SECRET")
	})
}

// simpleChannel implements channel.Channel for commandHint testing.
type simpleChannel struct{}

func (c *simpleChannel) Ready() bool                                { return true }
func (c *simpleChannel) CreateTopic(string) (string, error)         { return "", nil }
func (c *simpleChannel) SendMessage(string, string) (string, error) { return "", nil }
func (c *simpleChannel) ReplyMessage(string, string, string) (string, error) {
	return "", nil
}
func (c *simpleChannel) SendApproval(string, string, string) (string, error) {
	return "", nil
}
func (c *simpleChannel) EditMessage(string, string, string) error { return nil }
func (c *simpleChannel) FormatText(s string) string               { return s }
func (c *simpleChannel) SendTyping(string) error                  { return nil }
func (c *simpleChannel) CloseTopic(string) error                  { return nil }
func (c *simpleChannel) OnAction(func(string, string))            {}
func (c *simpleChannel) OnMessage(func(string, string, string))   {}
func (c *simpleChannel) Commands() []channel.Command {
	return channel.BotCommands
}

func TestCommandHint(t *testing.T) {
	hint := commandHint(&simpleChannel{})
	assert.Contains(t, hint, "`done`")
	assert.Contains(t, hint, "`status`")
	assert.Contains(t, hint, "`agents`")
	assert.Contains(t, hint, "`help`")
	assert.True(t, strings.HasPrefix(hint, "Commands: "))
}
