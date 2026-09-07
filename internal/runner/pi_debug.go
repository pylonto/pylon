package runner

import (
	"context"
	"errors"
	"strings"
)

func piDiffOutcome(patch []byte, err error) string {
	switch {
	case errors.Is(err, errPiOutputBound):
		return "output_bound"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case err != nil:
		return "git_failed"
	case len(patch) == 0:
		return "empty"
	default:
		return "nonempty"
	}
}

// The signed brief cannot select these cases. They are internal, network-none
// rehearsals of the actual SDK/adapter; fixture receipts still claim zero spend.
func piSDKFixture(name string) bool {
	if strings.HasPrefix(name, "sdk_session_") {
		name = strings.TrimPrefix(name, "sdk_session_")
	} else if strings.HasPrefix(name, "sdk_agent_") {
		name = strings.TrimPrefix(name, "sdk_agent_")
	} else {
		return false
	}
	switch name {
	case "read", "edit", "write", "bash", "read_missing", "edit_mismatch", "edit_array", "edit_array_mismatch", "privacy", "recover", "hang", "git_failure", "test_failure", "workspace_prompt":
		return true
	default:
		return false
	}
}
