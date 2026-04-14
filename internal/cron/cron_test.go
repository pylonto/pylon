package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "every 5 minutes", expr: "*/5 * * * *"},
		{name: "weekday 9am", expr: "0 9 * * 1-5"},
		{name: "midnight daily", expr: "0 0 * * *"},
		{name: "empty string", expr: "", wantErr: true},
		{name: "invalid text", expr: "not a cron", wantErr: true},
		{name: "too few fields", expr: "* * *", wantErr: true},
		{name: "six fields rejected", expr: "0 */5 * * * *", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.expr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNextFire(t *testing.T) {
	t.Run("every-minute expression fires within the next 60s", func(t *testing.T) {
		now := time.Now().In(time.UTC)
		result := NextFire("* * * * *", time.UTC)
		require.False(t, result.IsZero(), "expected non-zero time")
		// "* * * * *" fires every minute, so next fire must be within 60s of now
		assert.True(t, result.After(now) || result.Equal(now.Truncate(time.Minute).Add(time.Minute)))
		assert.True(t, result.Before(now.Add(61*time.Second)),
			"next fire should be within 61s, got %v (now=%v)", result, now)
		assert.Equal(t, 0, result.Second(), "fire time should be at second 0")
	})

	t.Run("top-of-hour expression fires at minute 0", func(t *testing.T) {
		now := time.Now().In(time.UTC)
		result := NextFire("0 * * * *", time.UTC)
		require.False(t, result.IsZero())
		assert.Equal(t, 0, result.Minute())
		assert.Equal(t, 0, result.Second())
		// Should be within the next hour
		assert.True(t, result.Before(now.Add(61*time.Minute)),
			"next top-of-hour should be within 61m")
	})

	t.Run("invalid expression returns zero time", func(t *testing.T) {
		result := NextFire("invalid", time.UTC)
		assert.True(t, result.IsZero())
	})

	t.Run("respects timezone", func(t *testing.T) {
		loc, err := time.LoadLocation("America/New_York")
		require.NoError(t, err)
		result := NextFire("0 * * * *", loc)
		require.False(t, result.IsZero())
		assert.Equal(t, 0, result.Minute())
		// Verify the result is actually in the requested timezone's frame
		resultInLoc := result.In(loc)
		assert.Equal(t, 0, resultInLoc.Minute())
	})
}

func TestSchedule(t *testing.T) {
	t.Run("valid expression", func(t *testing.T) {
		sched, err := Schedule("*/5 * * * *")
		assert.NoError(t, err)
		assert.NotNil(t, sched)
	})

	t.Run("invalid expression", func(t *testing.T) {
		_, err := Schedule("bad")
		assert.Error(t, err)
	})
}
