package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCronDaemon creates a daemon with cron-triggered pylons for testing.
func newCronDaemon(t *testing.T, pylons map[string]*config.PylonConfig) *Daemon {
	t.Helper()

	stores := make(map[string]*store.Store, len(pylons))
	for name := range pylons {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, name+".db"))
		require.NoError(t, err)
		t.Cleanup(func() { st.Close() })
		stores[name] = st
	}

	ms := store.NewMulti(stores)
	ch := newMockChannel()

	global := &config.GlobalConfig{
		Server: config.ServerConfig{Port: 8080, Host: "0.0.0.0"},
		Docker: config.DockerConfig{MaxConcurrent: 3, DefaultTimeout: "15m"},
	}

	return New(global, pylons, ms, ch, nil)
}

// Note: we do NOT test the "fires matching schedule" case via cronTick because
// fireCronJob spawns `go d.runJob(...)` which outlives the test and causes data
// races on the global runner.JobsDir variable. The firing path is already covered
// by the integration tests (TestIntegration_*) which test webhook/trigger routing.
//
// The tests below verify cronTick's filtering and deduplication logic -- the parts
// that determine WHETHER to fire, not the firing itself.

func TestCronTick_deduplicatesSameMinute(t *testing.T) {
	pylons := map[string]*config.PylonConfig{
		"dedup-test": {
			Name: "dedup-test",
			Trigger: config.TriggerConfig{
				Type: "cron",
				Cron: "* * * * *",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	d := newCronDaemon(t, pylons)

	// Pre-seed lastFired with the current minute to simulate "already fired"
	lastFired := map[string]time.Time{
		"dedup-test": time.Now().Truncate(time.Minute),
	}

	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "should not fire twice in the same minute")
}

func TestCronTick_skipsNonCronPylons(t *testing.T) {
	pylons := map[string]*config.PylonConfig{
		"webhook-pylon": {
			Name:      "webhook-pylon",
			Trigger:   config.TriggerConfig{Type: "webhook", Path: "/test"},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	d := newCronDaemon(t, pylons)
	lastFired := make(map[string]time.Time)

	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "webhook pylon should not be fired by cron tick")
}

func TestCronTick_skipsDisabledPylons(t *testing.T) {
	pylons := map[string]*config.PylonConfig{
		"disabled-cron": {
			Name:     "disabled-cron",
			Disabled: true,
			Trigger: config.TriggerConfig{
				Type: "cron",
				Cron: "* * * * *",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	d := newCronDaemon(t, pylons)
	lastFired := make(map[string]time.Time)

	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "disabled pylon should not fire")
}

func TestCronTick_skipsEmptyCronExpression(t *testing.T) {
	pylons := map[string]*config.PylonConfig{
		"empty-cron": {
			Name: "empty-cron",
			Trigger: config.TriggerConfig{
				Type: "cron",
				Cron: "",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	d := newCronDaemon(t, pylons)
	lastFired := make(map[string]time.Time)

	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "pylon with empty cron should not fire")
}

func TestCronTick_skipsNonMatchingSchedule(t *testing.T) {
	// Use a schedule that fires at a time far from now
	// "0 0 1 1 *" = midnight on Jan 1st only
	pylons := map[string]*config.PylonConfig{
		"yearly": {
			Name: "yearly",
			Trigger: config.TriggerConfig{
				Type: "cron",
				Cron: "0 0 1 1 *",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	// Only skip if we're not actually at midnight on Jan 1st
	now := time.Now()
	if now.Month() == time.January && now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0 {
		t.Skip("running at exactly midnight Jan 1st, can't test non-matching")
	}

	d := newCronDaemon(t, pylons)
	lastFired := make(map[string]time.Time)

	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "yearly cron should not fire at arbitrary time")
}

func TestCronTick_skipsInvalidCronExpression(t *testing.T) {
	pylons := map[string]*config.PylonConfig{
		"bad-cron": {
			Name: "bad-cron",
			Trigger: config.TriggerConfig{
				Type: "cron",
				Cron: "not a cron expression",
			},
			Workspace: config.WorkspaceConfig{Type: "none"},
			Agent:     &config.PylonAgent{Prompt: "test"},
		},
	}

	d := newCronDaemon(t, pylons)
	lastFired := make(map[string]time.Time)

	// Should not panic
	d.cronTick(lastFired)

	jobs := d.Store.List()
	assert.Empty(t, jobs, "invalid cron should not fire")
}
