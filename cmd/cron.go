package cmd

import cron "github.com/lnquy/cron"

// describeCron returns a human-readable description of a cron expression,
// or the raw expression if parsing fails.
func describeCron(expr string) string {
	d, err := cron.NewDescriptor()
	if err != nil {
		return expr
	}
	desc, err := d.ToDescription(expr, cron.Locale_en)
	if err != nil {
		return expr
	}
	return desc
}
