package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pylonto/pylon/internal/store"
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

// execCmd runs a tea.Cmd and returns the resulting message, or nil if cmd is nil.
func execCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestEscNavBackWhenNoOverlay(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEscape},
		{Type: tea.KeyRunes, Runes: []rune{'h'}},
	} {
		t.Run(fmt.Sprintf("key=%s", key), func(t *testing.T) {
			m := newDetailModel("test")
			updated, cmd := m.Update(key)
			msg := execCmd(cmd)
			assert.IsType(t, detailNavBackMsg{}, msg, "expected detailNavBackMsg")
			// Model unchanged (no side effects)
			assert.Equal(t, m.name, updated.name)
		})
	}
}

func TestEscDismissesConfirmation(t *testing.T) {
	for _, field := range []string{"confirmKill", "confirmDismiss", "confirmRetry", "confirmFire"} {
		t.Run(field, func(t *testing.T) {
			m := newDetailModel("test")
			switch field {
			case "confirmKill":
				m.confirmKill = true
			case "confirmDismiss":
				m.confirmDismiss = true
			case "confirmRetry":
				m.confirmRetry = true
			case "confirmFire":
				m.confirmFire = true
			}

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
			// Confirmation should be cleared
			assert.False(t, updated.confirmKill)
			assert.False(t, updated.confirmDismiss)
			assert.False(t, updated.confirmRetry)
			assert.False(t, updated.confirmFire)
			// Should NOT produce a nav-back command
			msg := execCmd(cmd)
			assert.Nil(t, msg, "expected no nav-back when dismissing confirmation")
		})
	}
}

func TestEscClearsError(t *testing.T) {
	m := newDetailModel("test")
	m.err = fmt.Errorf("something broke")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	assert.Nil(t, updated.err, "error should be cleared")
	// The cmd is m.Init() which reloads config -- not a nav-back
	assert.NotNil(t, cmd, "expected reload command")
	// Verify it's not a nav-back
	msg := execCmd(cmd)
	_, isNavBack := msg.(detailNavBackMsg)
	assert.False(t, isNavBack, "error dismiss should reload, not nav back")
}

func TestEscClosesAlertBuilder(t *testing.T) {
	m := newDetailModel("test")
	m.alertBuilder = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	assert.False(t, updated.alertBuilder, "alertBuilder should be cleared")
	msg := execCmd(cmd)
	assert.Nil(t, msg, "expected no nav-back when closing alert builder")
}

func TestJobWindow(t *testing.T) {
	tests := []struct {
		name      string
		cursor    int
		total     int
		window    int
		wantStart int
		wantEnd   int
	}{
		{"all fit", 0, 5, 10, 0, 5},
		{"cursor at top", 0, 20, 5, 0, 5},
		{"cursor at bottom", 19, 20, 5, 15, 20},
		{"cursor centered", 10, 20, 5, 8, 13},
		{"cursor near top", 1, 20, 5, 0, 5},
		{"cursor near bottom", 18, 20, 5, 15, 20},
		{"window size 1", 5, 20, 1, 5, 6},
		{"single job", 0, 1, 5, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := jobWindow(tt.cursor, tt.total, tt.window)
			assert.Equal(t, tt.wantStart, start, "start")
			assert.Equal(t, tt.wantEnd, end, "end")
		})
	}
}

func TestRenderJobsRespectsMaxRows(t *testing.T) {
	// Regression: when the first-pass window starts at 0 but shrinking it
	// shifts it off the edge, a new "above" indicator appears that wasn't
	// budgeted, pushing the total 1 line over maxRows.
	makeJobs := func(n int) []*store.Job {
		jobs := make([]*store.Job, n)
		for i := range n {
			jobs[i] = &store.Job{ID: fmt.Sprintf("job-%04d", i), Status: "completed", CreatedAt: time.Now()}
		}
		return jobs
	}

	tests := []struct {
		name    string
		cursor  int
		total   int
		maxRows int
	}{
		{"cursor near edge triggers extra indicator", 4, 20, 9},
		{"cursor at top", 0, 20, 9},
		{"cursor at bottom", 19, 20, 9},
		{"all fit", 0, 5, 20},
		{"tight fit", 3, 10, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := detailModel{
				jobs:    makeJobs(tt.total),
				cursor:  tt.cursor,
				focused: true,
			}
			out := m.renderJobs(tt.maxRows)
			lines := strings.Count(out, "\n")
			assert.LessOrEqual(t, lines, tt.maxRows,
				"renderJobs output %d lines, max allowed %d", lines, tt.maxRows)
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
