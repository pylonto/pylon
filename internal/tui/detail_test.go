package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsRunningStatus(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"running", true},
		{"active", true},
		{"bootstrapping", true},
		{"completed", false},
		{"failed", false},
		{"timeout", false},
		{"dismissed", false},
		{"stale", false},
		{"awaiting_approval", false},
		{"pending", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			assert.Equal(t, tt.want, isRunningStatus(tt.status))
		})
	}
}

func TestOrphanJobStatus(t *testing.T) {
	tests := []struct {
		name      string
		createdAt time.Time
		want      string
	}{
		{"just created", time.Now(), "bootstrapping"},
		{"30 seconds ago", time.Now().Add(-30 * time.Second), "bootstrapping"},
		{"1 minute ago", time.Now().Add(-1 * time.Minute), "bootstrapping"},
		{"119 seconds ago", time.Now().Add(-119 * time.Second), "bootstrapping"},
		{"2 minutes ago", time.Now().Add(-2 * time.Minute), "stale"},
		{"5 minutes ago", time.Now().Add(-5 * time.Minute), "stale"},
		{"1 hour ago", time.Now().Add(-1 * time.Hour), "stale"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, orphanJobStatus(tt.createdAt))
		})
	}
}

func TestRenderJobStatus(t *testing.T) {
	tests := []struct {
		status   string
		contains string
	}{
		{"completed", "completed"},
		{"failed", "failed"},
		{"timeout", "timeout"},
		{"dismissed", "dismissed"},
		{"running", "running"},
		{"active", "running"},
		{"awaiting_approval", "approval"},
		{"bootstrapping", "bootstrapping"},
		{"stale", "stale"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := renderJobStatus(tt.status)
			assert.True(t, strings.Contains(got, tt.contains),
				"renderJobStatus(%q) = %q, want it to contain %q", tt.status, got, tt.contains)
		})
	}
}
