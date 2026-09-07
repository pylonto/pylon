package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func budgetFixture(t *testing.T) (*Store, SubscriptionLimits, SubscriptionLimits) {
	t.Helper()
	s, _ := subscriptionFixture(t)
	old := SubscriptionLimits{DailyJobs: 1, DailyTokens: 100, JobTokens: 100, JobSeconds: 60}
	for i, at := range []time.Time{subscriptionNow.Add(-24 * time.Hour), subscriptionNow} {
		key := fmt.Sprintf("prior-%d", i)
		claim := subscriptionClaim(t, s, old, key, at)
		require.NoError(t, s.FinishSubscription(claim.JobID, subscriptionResult(int64(55+11*i)), at))
		changed, err := s.TransitionDelivery(key, "claimed", "submitted")
		require.NoError(t, err)
		require.True(t, changed)
	}
	require.NoError(t, s.PauseSubscription("operator", subscriptionNow))
	desired := old
	desired.DailyJobs, desired.DailyTokens = 2, 166
	return s, old, desired
}

func budgetHistory(t *testing.T, s *Store) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, table := range []struct{ name, query string }{
		{"subscription_claims", "SELECT * FROM subscription_claims ORDER BY rowid"},
		{"deliveries", "SELECT * FROM deliveries ORDER BY rowid"},
		{"execution_outcomes", "SELECT * FROM execution_outcomes ORDER BY rowid"},
		{"jobs", "SELECT * FROM jobs ORDER BY rowid"},
	} {
		rows, err := s.db.Query(table.query)
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		var values [][]interface{}
		for rows.Next() {
			row := make([]interface{}, len(columns))
			pointers := make([]interface{}, len(row))
			for i := range row {
				pointers[i] = &row[i]
			}
			require.NoError(t, rows.Scan(pointers...))
			values = append(values, row)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		raw, err := json.Marshal(values)
		require.NoError(t, err)
		out[table.name] = string(raw)
	}
	return out
}

func TestSubscriptionBudgetRaisesOnlyCapsAndRetainsPriorUsage(t *testing.T) {
	s, old, desired := budgetFixture(t)
	before := budgetHistory(t, s)
	status, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, 2, status.Records)
	require.Equal(t, 1, status.JobsToday)
	require.EqualValues(t, 66, status.TokensToday)
	var watermark int64
	require.NoError(t, s.db.QueryRow("SELECT watermark FROM subscription_policy").Scan(&watermark))
	require.ErrorIs(t, s.ConfigureSubscription(desired, subscriptionNow), ErrSubscriptionConflict)
	require.NoError(t, s.RaiseSubscriptionBudget(old, desired, subscriptionNow.Add(time.Minute)))
	require.Equal(t, before, budgetHistory(t, s))
	var afterWatermark int64
	require.NoError(t, s.db.QueryRow("SELECT watermark FROM subscription_policy").Scan(&afterWatermark))
	require.Equal(t, watermark, afterWatermark)
	after, err := s.SubscriptionStatus(subscriptionNow.Add(time.Minute))
	require.NoError(t, err)
	expected := *status
	expected.Limits = desired
	require.Equal(t, expected, *after)
	require.ErrorIs(t, s.RaiseSubscriptionBudget(old, desired, subscriptionNow.Add(time.Minute)), ErrSubscriptionConflict)
	require.Error(t, s.RaiseSubscriptionBudget(desired, desired, subscriptionNow.Add(time.Minute)))
	require.ErrorIs(t, s.ConfigureSubscription(old, subscriptionNow), ErrSubscriptionConflict)
	require.NoError(t, s.ConfigureSubscription(desired, subscriptionNow))
	require.NoError(t, s.PauseSubscription("", subscriptionNow))
	claim := subscriptionClaim(t, s, desired, "new-approved", subscriptionNow)
	reserved, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, 3, reserved.Records)
	require.Equal(t, 2, reserved.JobsToday)
	require.EqualValues(t, 166, reserved.TokensToday)
	require.NoError(t, s.FinishSubscription(claim.JobID, subscriptionResult(50), subscriptionNow))
	d, digest := subscriptionDelivery(t, s, "fourth-not-authorized")
	_, fresh, err := s.ClaimSubscription(d.JobID, digest, desired, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionDailyJobs)
	require.False(t, fresh)
}

func TestSubscriptionBudgetRefusalsDoNotAlterHistoryOrPolicy(t *testing.T) {
	for _, name := range []string{"stale", "job_tokens", "job_seconds", "resume", "quota", "reserved", "unknown", "pending", "claimed", "clock", "lower", "no-op", "invalid"} {
		t.Run(name, func(t *testing.T) {
			s, old, desired := budgetFixture(t)
			now := subscriptionNow
			switch name {
			case "stale":
				old.DailyTokens++
			case "job_tokens":
				desired.JobTokens++
			case "job_seconds":
				desired.JobSeconds++
			case "resume":
				require.NoError(t, s.PauseSubscription("", now))
			case "quota":
				require.NoError(t, s.PauseSubscription("quota_exhausted", now))
			case "reserved":
				_, err := s.db.Exec("UPDATE subscription_claims SET state='reserved',result='' WHERE day=?", now.Format(time.DateOnly))
				require.NoError(t, err)
			case "unknown":
				_, err := s.db.Exec(`UPDATE subscription_claims SET result='{"outcome":"executor_failed","usage":null,"pause":""}' WHERE day=?`, now.Format(time.DateOnly))
				require.NoError(t, err)
			case "pending", "claimed":
				subscriptionDelivery(t, s, "queued-new")
				if name == "claimed" {
					changed, err := s.TransitionDelivery("queued-new", "queued", "claimed")
					require.NoError(t, err)
					require.True(t, changed)
				}
			case "clock":
				now = now.Add(-time.Second)
			case "lower":
				desired.DailyTokens = old.DailyTokens - 1
			case "no-op":
				desired = old
			case "invalid":
				desired.DailyJobs = 0
			}
			before := budgetHistory(t, s)
			var limits, pause string
			var watermark int64
			require.NoError(t, s.db.QueryRow("SELECT limits,paused,watermark FROM subscription_policy").Scan(&limits, &pause, &watermark))
			require.Error(t, s.RaiseSubscriptionBudget(old, desired, now))
			require.Equal(t, before, budgetHistory(t, s))
			var afterLimits, afterPause string
			var afterWatermark int64
			require.NoError(t, s.db.QueryRow("SELECT limits,paused,watermark FROM subscription_policy").Scan(&afterLimits, &afterPause, &afterWatermark))
			require.Equal(t, limits, afterLimits)
			require.Equal(t, pause, afterPause)
			require.Equal(t, watermark, afterWatermark)
		})
	}
}

func TestSubscriptionBudgetCrossConnectionCASHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	other, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	old := SubscriptionLimits{DailyJobs: 1, DailyTokens: 100, JobTokens: 100, JobSeconds: 60}
	desired := old
	desired.DailyJobs++
	require.NoError(t, s.ConfigureSubscription(old, subscriptionNow))
	require.NoError(t, s.PauseSubscription("operator", subscriptionNow))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*Store{s, other} {
		wg.Add(1)
		go func(candidate *Store) {
			defer wg.Done()
			<-start
			results <- candidate.RaiseSubscriptionBudget(old, desired, subscriptionNow)
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	passed, refused := 0, 0
	for result := range results {
		if result == nil {
			passed++
		} else {
			// A contender can be refused at SQLite's writer reservation before it
			// reaches the policy CAS. Both explicit refusals preserve one winner.
			require.True(t, errors.Is(result, ErrSubscriptionConflict) || errors.Is(result, ErrSubscriptionStorage), "%v", result)
			refused++
		}
	}
	require.Equal(t, 1, passed)
	require.Equal(t, 1, refused)
	status, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, desired, status.Limits)
	require.Equal(t, "operator", status.Paused)
	require.Zero(t, status.Records)
	require.Zero(t, status.TokensToday)
}
