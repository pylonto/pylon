package cron

import (
	"time"

	robfigcron "github.com/robfig/cron/v3"
)

var parser = robfigcron.NewParser(
	robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow,
)

// Validate checks whether a 5-field cron expression is syntactically valid.
func Validate(expr string) error {
	_, err := parser.Parse(expr)
	return err
}

// NextFire computes the next fire time for a cron expression in the given timezone.
// Returns zero time if the expression is invalid.
func NextFire(expr string, loc *time.Location) time.Time {
	schedule, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(time.Now().In(loc))
}

// Schedule parses a cron expression and returns the robfig Schedule.
// Used by the daemon scheduler to avoid re-parsing on every tick.
func Schedule(expr string) (robfigcron.Schedule, error) {
	return parser.Parse(expr)
}
