// Package pidebug owns opt-in, private Pi inspection. Captured prose never
// participates in execution, subscription settlement or publication evidence.
package pidebug

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxJobBytes     = 1024 * 1024
	MaxSummaryBytes = 4096
	MaxEventsBytes  = MaxJobBytes - 2*MaxSummaryBytes
	MaxEvents       = 128
	MaxEventBytes   = 16384 // Encoded JSONL, including newline and host metadata.
	MaxJobs         = 64
)

// Limits travel only over the private job socket. The signed brief cannot set them.
type Limits struct {
	EventBytes     int `json:"event_bytes"`
	Events         int `json:"events"`
	QueueEvents    int `json:"queue_events"`
	QueueBytes     int `json:"queue_bytes"`
	RPCTimeoutMS   int `json:"rpc_timeout_ms"`
	DrainTimeoutMS int `json:"drain_timeout_ms"`
	SecretValues   int `json:"secret_values"`
	SecretBytes    int `json:"secret_bytes"`
	ArgumentDepth  int `json:"argument_depth"`
	ArgumentItems  int `json:"argument_items"`
}

func FixedLimits() Limits {
	return Limits{MaxEventBytes, MaxEvents, 8, 65536, 1000, 2000, 32, 131072, 4, 32}
}

var ErrUnsafe = errors.New("pi_debug_unsafe_destination")
var ErrInvalid = errors.New("pi_debug_invalid_capture")
var ErrUnavailable = errors.New("pi_debug_unavailable")
var ErrFull = errors.New("pi_debug_full")
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var baseRE = regexp.MustCompile(`^[a-f0-9]{40}$`)
var imageRE = regexp.MustCompile(`^(?:sha256:|[a-zA-Z0-9./_-]+@sha256:)[a-f0-9]{64}$`)
var categoryRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func ValidJob(job string) bool { id, err := uuid.Parse(job); return err == nil && id.String() == job }

// Context binds NAME and the declared configuration without inspecting auth,
// environment or repository data. Image/debug changes do not change this binding.
func Context(name, repo, auth, evidence string) string {
	raw, _ := json.Marshal([]string{name, filepath.Clean(repo), filepath.Clean(auth), filepath.Clean(evidence)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Identity struct {
	V       int    `json:"v"`
	Pylon   string `json:"pylon"`
	Job     string `json:"job_id"`
	Base    string `json:"base"`
	Image   string `json:"image"`
	Context string `json:"context_sha256"`
}

func (i Identity) valid() bool {
	return i.V == 1 && nameRE.MatchString(i.Pylon) && ValidJob(i.Job) && baseRE.MatchString(i.Base) && len(i.Image) <= 256 && imageRE.MatchString(i.Image) && digestRE.MatchString(i.Context)
}

type Export struct {
	Collection      string `json:"collection"`
	Allowed         int    `json:"allowed_count"`
	Collected       int    `json:"collected_count"`
	Staging         string `json:"staging"`
	Staged          int    `json:"staged_count"`
	Diff            string `json:"diff"`
	ObservedBytes   int    `json:"observed_bytes"`
	TotalBytesKnown bool   `json:"total_bytes_known"`
}

type Summary struct {
	Identity
	EventCount      int    `json:"event_count"`
	Bytes           int    `json:"bytes"`
	Truncated       bool   `json:"truncated"`
	Incomplete      bool   `json:"incomplete"`
	DroppedEvents   int    `json:"dropped_events"`
	RuntimeClosed   bool   `json:"runtime_capture_closed"`
	ExecutorClosed  bool   `json:"executor_closed"`
	ExecutorOutcome string `json:"executor_outcome"`
	Failure         string `json:"failure"`
	CaptureFailure  string `json:"capture_failure"`
	Export          Export `json:"export"`
}

func initial(i Identity) Summary {
	return Summary{Identity: i, Incomplete: true, Export: Export{Collection: "not_attempted", Staging: "not_attempted", Diff: "not_attempted"}}
}

type Event struct {
	Kind          string          `json:"kind"`
	ToolIndex     int             `json:"tool_index,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Text          string          `json:"text,omitempty"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	Result        string          `json:"result,omitempty"`
	Error         string          `json:"error,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
	Outcome       string          `json:"outcome,omitempty"`
	Counts        map[string]int  `json:"counts,omitempty"`
	Truncated     bool            `json:"truncated,omitempty"`
	Redacted      bool            `json:"redacted,omitempty"`
	Partial       bool            `json:"partial,omitempty"`
}

type Record struct {
	Seq    int    `json:"seq"`
	Source string `json:"source"`
	Event
	Identity *Identity `json:"identity,omitempty"`
}

type Closure struct {
	Events     int  `json:"events"`
	Dropped    int  `json:"dropped_events"`
	Truncated  bool `json:"truncated"`
	Incomplete bool `json:"incomplete"`
}

type Frame struct {
	Sequence int      `json:"sequence"`
	Event    *Event   `json:"event,omitempty"`
	Close    *Closure `json:"close,omitempty"`
}

func member(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (e Event) valid(runtime bool) bool {
	if runtime && !member(e.Kind, "lifecycle", "assistant_text", "tool_start", "tool_end", "tool_error") {
		return false
	}
	if !runtime && !member(e.Kind, "capture_start", "lifecycle", "collection", "staging", "diff") {
		return false
	}
	if !member(e.Tool, "", "read", "write", "edit", "bash", "unknown") || e.ToolIndex < 0 || e.ToolIndex > 64 {
		return false
	}
	if !member(e.Outcome, "", "returned", "failed", "unknown", "prompt_session", "prompt_agent", "agent_start", "agent_end", "turn_start", "turn_end", "runtime_started", "runtime_stopped", "empty", "nonempty", "git_failed", "canceled", "deadline", "output_bound") {
		return false
	}
	if !member(e.ErrorCategory, "", "execution_failed", "validation_failed", "rpc_failed", "ENOENT", "EACCES", "EPERM", "EINVAL", "aborted", "unknown") {
		return false
	}
	for _, s := range []string{e.Text, e.Result, e.Error} {
		if !utf8.ValidString(s) || len(s) > MaxEventBytes {
			return false
		}
	}
	if len(e.Arguments) > MaxEventBytes || len(e.Arguments) > 0 && !validArguments(e.Arguments) {
		return false
	}
	if len(e.Counts) > 8 {
		return false
	}
	for k, n := range e.Counts {
		if !member(k, "allowed", "collected", "staged", "bytes", "exit_code", "total_known", "requests", "tools") || (n < 0 && (k != "exit_code" || n != -1)) || n > 2147483647 {
			return false
		}
	}
	return true
}

func validArguments(raw []byte) bool {
	var value any
	if Decode(raw, &value) != nil {
		return false
	}
	remaining := FixedLimits().ArgumentItems
	var visit func(any, int) bool
	visit = func(value any, depth int) bool {
		remaining--
		if remaining < 0 || depth > FixedLimits().ArgumentDepth+1 {
			return false
		}
		switch v := value.(type) {
		case map[string]any:
			for k, x := range v {
				if !member(k, "path", "command", "timeout", "offset", "limit", "content", "edits", "oldText", "newText") || !visit(x, depth+1) {
					return false
				}
			}
		case []any:
			for _, x := range v {
				if !visit(x, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return visit(value, 0)
}
