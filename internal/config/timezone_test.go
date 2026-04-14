package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDetectSystemTimezone(t *testing.T) {
	t.Run("respects TZ env var", func(t *testing.T) {
		t.Setenv("TZ", "America/Chicago")
		assert.Equal(t, "America/Chicago", DetectSystemTimezone())
	})

	t.Run("invalid TZ falls through", func(t *testing.T) {
		t.Setenv("TZ", "Fake/Zone")
		tz := DetectSystemTimezone()
		// Should fall through to /etc/localtime or UTC -- either way, must be valid
		_, err := time.LoadLocation(tz)
		assert.NoError(t, err)
	})
}

func TestTimezoneList(t *testing.T) {
	list := TimezoneList()

	t.Run("non-empty", func(t *testing.T) {
		assert.NotEmpty(t, list)
	})

	t.Run("starts with UTC", func(t *testing.T) {
		assert.Equal(t, "UTC", list[0])
	})

	t.Run("all entries valid", func(t *testing.T) {
		for _, tz := range list {
			_, err := time.LoadLocation(tz)
			assert.NoError(t, err, "invalid timezone: %s", tz)
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		seen := make(map[string]bool)
		for _, tz := range list {
			assert.False(t, seen[tz], "duplicate timezone: %s", tz)
			seen[tz] = true
		}
	})
}
