package runner

import (
	"encoding/json"
	"path/filepath"

	"github.com/pylonto/pylon/internal/pidebug"
	"github.com/pylonto/pylon/internal/proxy"
	"github.com/pylonto/pylon/internal/store"
)

// One producer owns the normal receipt schema. Debug has only closed categorical
// summary fields; the private event type cannot be serialized through this seam.
type piReceipt struct {
	Job          string                   `json:"job_id"`
	Base         string                   `json:"base"`
	Subscription store.SubscriptionResult `json:"subscription"`
	Runtime      *proxy.PiResult          `json:"runtime"`
	Failure      string                   `json:"failure"`
	Patch        string                   `json:"patch,omitempty"`
	Digest       string                   `json:"patch_sha256,omitempty"`
	Debug        *pidebug.Summary         `json:"debug,omitempty"`
}

func piReceiptBytes(p PiParams, out PiOutcome) ([]byte, error) {
	r := piReceipt{Job: p.JobID, Base: p.Base, Subscription: out.Result, Runtime: out.Runtime, Failure: out.Failure, Digest: out.PatchSHA256, Debug: out.Debug}
	if out.Patch != "" {
		r.Patch = filepath.Base(out.Patch)
	}
	return json.Marshal(r)
}
