package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// RaiseSubscriptionBudget is an explicit operator action, never admission/configure
// fallback. Only daily ceilings may rise; existing reservations keep their original
// per-job envelope. Expected policy and pause are checked under the SQLite writer
// reservation, including across independent processes. No history or clock is reset.
func (s *Store) RaiseSubscriptionBudget(expected, desired SubscriptionLimits, now time.Time) error {
	if expected.Validate() != nil || desired.Validate() != nil || now.Unix() <= 0 ||
		expected.JobTokens != desired.JobTokens || expected.JobSeconds != desired.JobSeconds ||
		desired.DailyJobs < expected.DailyJobs || desired.DailyTokens < expected.DailyTokens || expected == desired {
		return ErrSubscriptionInvalid
	}
	return s.subscriptionWrite(func(tx *sql.Tx) error {
		policy, err := readSubscriptionPolicy(tx)
		if err != nil {
			return err
		}
		if policy.limits != expected {
			return ErrSubscriptionConflict
		}
		if policy.paused != "operator" {
			return ErrSubscriptionPaused
		}
		status, err := subscriptionCounts(tx, policy, now)
		if err != nil {
			return err
		}
		if status.Unresolved != 0 {
			return ErrSubscriptionBusy
		}
		var pending bool
		// Historical jobs may still say queued after their delivery was submitted.
		// Use the dispatcher's eligibility rule, plus any already-claimed delivery.
		err = tx.QueryRow("SELECT EXISTS (SELECT 1 FROM deliveries d LEFT JOIN jobs j ON j.id=d.job_id WHERE d.state='claimed' OR (" + pendingDeliveryPredicate + "))").Scan(&pending)
		if err != nil {
			return err
		}
		if pending {
			return ErrSubscriptionBusy
		}
		raw, err := json.Marshal(desired)
		if err != nil {
			return err
		}
		result, err := tx.Exec("UPDATE subscription_policy SET limits=? WHERE id=1", string(raw))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrSubscriptionConflict
		}
		return nil
	})
}
