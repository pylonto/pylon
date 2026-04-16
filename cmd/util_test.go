package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDescribeCron(t *testing.T) {
	tests := []struct {
		expr string
		want string // substring that should appear
	}{
		{"0 9 * * 1-5", "09:00"},
		{"*/5 * * * *", "5 minutes"},
		{"0 0 * * 0", "Sunday"},
		{"invalid", "invalid"}, // returns raw expression on failure
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got := describeCron(tt.expr)
			assert.Contains(t, got, tt.want)
		})
	}
}

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"seconds", time.Now().Add(-30 * time.Second), "30 sec ago"},
		{"minutes", time.Now().Add(-5 * time.Minute), "5 min ago"},
		{"hours", time.Now().Add(-3 * time.Hour), "3 hours ago"},
		{"days", time.Now().Add(-48 * time.Hour), "2 days ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, timeAgo(tt.when))
		})
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello\nworld", "hello"},
		{"leading empty", "\n\nhello", "hello"},
		{"whitespace lines", "  \n  \nactual", "actual"},
		{"skips <none>", "<none>\nreal", "real"},
		{"all empty", "\n\n", ""},
		{"single line", "just this", "just this"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, firstNonEmptyLine(tt.input))
		})
	}
}

func TestDrLine(t *testing.T) {
	// drLine prints formatted output -- just verify it doesn't panic
	drLine("Docker", "ok", "v24.0")
	drLine("VeryLongLabelThatExceedsWidth", "ok", "detail")
}

func TestDrSub(t *testing.T) {
	drSub("my-pylon", "ok", "webhook /test")
	drSub("VeryLongLabelOverflow", "FAIL", "missing config")
}
