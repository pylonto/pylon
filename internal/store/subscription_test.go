package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var subscriptionNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func subscriptionFixture(t *testing.T) (*Store, SubscriptionLimits) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, SubscriptionLimits{DailyJobs: 3, DailyTokens: 250, JobTokens: 100, JobSeconds: 60}
}

func subscriptionDelivery(t *testing.T, s *Store, key string) (*Delivery, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"fixture": key})
	require.NoError(t, err)
	d, fresh, err := s.AcceptDelivery("vendor", key, body)
	require.NoError(t, err)
	require.True(t, fresh)
	digest := sha256.Sum256(body)
	return d, hex.EncodeToString(digest[:])
}

func subscriptionClaim(t *testing.T, s *Store, limits SubscriptionLimits, key string, now time.Time) *SubscriptionClaim {
	t.Helper()
	d, digest := subscriptionDelivery(t, s, key)
	claim, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, now)
	require.NoError(t, err)
	require.True(t, fresh)
	return claim
}

func subscriptionResult(tokens int64) SubscriptionResult {
	return SubscriptionResult{Outcome: "executor_returned", Usage: &SubscriptionUsage{Input: tokens}}
}

func TestSubscriptionLimitsNeverInterpretZeroAsUnlimited(t *testing.T) {
	_, valid := subscriptionFixture(t)
	require.NoError(t, valid.Validate())
	for name, mutate := range map[string]func(*SubscriptionLimits){
		"jobs_zero":           func(p *SubscriptionLimits) { p.DailyJobs = 0 },
		"jobs_excessive":      func(p *SubscriptionLimits) { p.DailyJobs = 25 },
		"daily_zero":          func(p *SubscriptionLimits) { p.DailyTokens = 0 },
		"daily_less_than_job": func(p *SubscriptionLimits) { p.DailyTokens = 99 },
		"daily_excessive":     func(p *SubscriptionLimits) { p.DailyTokens = 100_000_001 },
		"job_zero":            func(p *SubscriptionLimits) { p.JobTokens = 0 },
		"job_negative":        func(p *SubscriptionLimits) { p.JobTokens = -1 },
		"seconds_zero":        func(p *SubscriptionLimits) { p.JobSeconds = 0 },
		"seconds_excessive":   func(p *SubscriptionLimits) { p.JobSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			require.ErrorIs(t, p.Validate(), ErrSubscriptionInvalid)
		})
	}
}

func TestSubscriptionClaimBindsExactAdmissionAndRecoversWithoutExecution(t *testing.T) {
	s, limits := subscriptionFixture(t)
	d, digest := subscriptionDelivery(t, s, "first")
	_, _, err := s.ClaimSubscription(d.JobID, fmt.Sprintf("%064d", 0), limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionConflict)
	claim, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.NoError(t, err)
	require.True(t, fresh)
	require.Equal(t, "reserved", claim.State)
	require.Equal(t, subscriptionNow.Add(time.Minute).Unix(), claim.Deadline)
	m := NewMulti(map[string]*Store{"vendor": s})
	status, err := m.DeliveryStatus("vendor", d.Key)
	require.NoError(t, err)
	require.Equal(t, "claimed", status.Admission)
	require.Equal(t, "outcome_unknown", status.Execution)
	// A lost caller acknowledgement returns facts, never another execution grant.
	recovered, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, claim, recovered)
	_, _, err = s.ClaimSubscription(d.JobID, fmt.Sprintf("%064d", 0), limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionConflict)
	limits.DailyJobs++
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionConflict)
}

func TestSubscriptionRestartCallbackAndMidnightCannotReleaseUnknownWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path)
	require.NoError(t, err)
	limits := SubscriptionLimits{DailyJobs: 2, DailyTokens: 200, JobTokens: 100, JobSeconds: 60}
	claim := subscriptionClaim(t, s, limits, "unknown", subscriptionNow)
	s.SetCompleted(claim.JobID, []byte(`{"claim":"green","tokens":0}`))
	m := NewMulti(map[string]*Store{"vendor": s})
	require.ErrorIs(t, m.FinishExecution("vendor", claim.JobID, "executor_returned"), ErrSubscriptionOwned)
	fact, err := m.DeliveryStatus("vendor", "unknown")
	require.NoError(t, err)
	require.Equal(t, "outcome_unknown", fact.Execution, "a refusal after an unguarded UPDATE is too late")
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	d, digest := subscriptionDelivery(t, s, "tomorrow")
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(24*time.Hour))
	require.ErrorIs(t, err, ErrSubscriptionBusy)
	status, err := s.SubscriptionStatus(subscriptionNow.Add(24 * time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, status.Unresolved)
	require.Zero(t, status.JobsToday)
	// Operator pause/resume changes neither the retained claim nor its capacity.
	require.NoError(t, s.PauseSubscription("operator", subscriptionNow.Add(24*time.Hour)))
	require.NoError(t, s.PauseSubscription("", subscriptionNow.Add(24*time.Hour)))
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(24*time.Hour))
	require.ErrorIs(t, err, ErrSubscriptionBusy)
}

func TestSubscriptionDailyJobsAndUsageReserveBeforeExecution(t *testing.T) {
	t.Run("daily jobs include failures", func(t *testing.T) {
		s, limits := subscriptionFixture(t)
		limits.DailyJobs = 2
		for i := range 2 {
			c := subscriptionClaim(t, s, limits, fmt.Sprint(i), subscriptionNow)
			result := subscriptionResult(0)
			result.Outcome = "executor_failed"
			require.NoError(t, s.FinishSubscription(c.JobID, result, subscriptionNow))
		}
		d, digest := subscriptionDelivery(t, s, "third")
		_, _, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
		require.ErrorIs(t, err, ErrSubscriptionDailyJobs)
		status, err := s.SubscriptionStatus(subscriptionNow)
		require.NoError(t, err)
		require.Equal(t, 2, status.JobsToday)
		require.Zero(t, status.TokensToday)
	})
	t.Run("reserved maximum not claimed estimate", func(t *testing.T) {
		s, limits := subscriptionFixture(t)
		limits.DailyTokens = 150
		c := subscriptionClaim(t, s, limits, "first", subscriptionNow)
		status, err := s.SubscriptionStatus(subscriptionNow)
		require.NoError(t, err)
		require.EqualValues(t, 100, status.TokensToday)
		require.NoError(t, s.FinishSubscription(c.JobID, subscriptionResult(51), subscriptionNow))
		d, digest := subscriptionDelivery(t, s, "second")
		_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
		require.ErrorIs(t, err, ErrSubscriptionDailyUsage)
		_, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(24*time.Hour))
		require.NoError(t, err)
		require.True(t, fresh)
	})
}

func TestSubscriptionUsageIncludesCachesAndSettlementIsImmutable(t *testing.T) {
	s, limits := subscriptionFixture(t)
	c := subscriptionClaim(t, s, limits, "count-caches", subscriptionNow)
	r := subscriptionResult(10)
	r.Usage.Output, r.Usage.CacheRead, r.Usage.CacheWrite = 20, 30, 40
	require.NoError(t, s.FinishSubscription(c.JobID, r, subscriptionNow))
	require.NoError(t, s.FinishSubscription(c.JobID, r, subscriptionNow.Add(time.Hour)))
	status, err := s.SubscriptionStatus(subscriptionNow.Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 100, status.TokensToday)
	require.Zero(t, status.Unresolved)
	r.Usage.Input--
	require.ErrorIs(t, s.FinishSubscription(c.JobID, r, subscriptionNow), ErrSubscriptionConflict)
	s.Delete(c.JobID)
	status, err = s.SubscriptionStatus(subscriptionNow.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, status.JobsToday)
	require.EqualValues(t, 100, status.TokensToday)
}

func TestSubscriptionUnknownOrInvalidUsageNeverRefundsOrFreesCapacity(t *testing.T) {
	for name, result := range map[string]SubscriptionResult{
		"absent usage":      {Outcome: "executor_failed"},
		"negative":          {Outcome: "executor_returned", Usage: &SubscriptionUsage{Input: -1}},
		"above reservation": subscriptionResult(101),
		"overflow":          {Outcome: "executor_returned", Usage: &SubscriptionUsage{Input: 1 << 62, Output: 1 << 62}},
		"callback verdict":  {Outcome: "green", Usage: &SubscriptionUsage{}},
		"unknown pause":     {Outcome: "executor_failed", Usage: &SubscriptionUsage{}, Pause: "raw-provider-error"},
	} {
		t.Run(name, func(t *testing.T) {
			s, limits := subscriptionFixture(t)
			c := subscriptionClaim(t, s, limits, "unknown", subscriptionNow)
			require.Error(t, s.FinishSubscription(c.JobID, result, subscriptionNow))
			status, err := s.SubscriptionStatus(subscriptionNow)
			require.NoError(t, err)
			require.Equal(t, 1, status.Unresolved)
			require.EqualValues(t, 100, status.TokensToday)
		})
	}
}

func TestSubscriptionAuthAndQuotaPauseRequireExplicitResume(t *testing.T) {
	for _, reason := range []string{"auth_unavailable", "quota_exhausted"} {
		t.Run(reason, func(t *testing.T) {
			s, limits := subscriptionFixture(t)
			c := subscriptionClaim(t, s, limits, "blocked", subscriptionNow)
			r := SubscriptionResult{Outcome: "executor_failed", Usage: &SubscriptionUsage{}, Pause: reason}
			require.NoError(t, s.FinishSubscription(c.JobID, r, subscriptionNow))
			d, digest := subscriptionDelivery(t, s, "later")
			_, _, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(24*time.Hour))
			require.ErrorIs(t, err, ErrSubscriptionPaused)
			require.NoError(t, s.PauseSubscription("", subscriptionNow.Add(24*time.Hour)))
			// Re-reading an old receipt must not re-pause or mutate current control.
			require.NoError(t, s.FinishSubscription(c.JobID, r, subscriptionNow.Add(24*time.Hour)))
			_, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow.Add(24*time.Hour))
			require.NoError(t, err)
			require.True(t, fresh)
		})
	}
}

func TestSubscriptionAtomicClaimAndSettlementOnStorageFailure(t *testing.T) {
	s, limits := subscriptionFixture(t)
	d, digest := subscriptionDelivery(t, s, "atomic")
	_, err := s.db.Exec(`CREATE TRIGGER break_claim BEFORE INSERT ON execution_outcomes BEGIN SELECT RAISE(ABORT,'private fixture'); END`)
	require.NoError(t, err)
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionStorage)
	var count int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM subscription_claims").Scan(&count))
	require.Zero(t, count)
	var admission string
	require.NoError(t, s.db.QueryRow("SELECT state FROM deliveries WHERE job_id=?", d.JobID).Scan(&admission))
	require.Equal(t, "queued", admission)
	_, err = s.db.Exec("DROP TRIGGER break_claim")
	require.NoError(t, err)
	c, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.NoError(t, err)
	require.True(t, fresh)
	_, err = s.db.Exec(`CREATE TRIGGER break_finish BEFORE UPDATE ON execution_outcomes BEGIN SELECT RAISE(ABORT,'private fixture'); END`)
	require.NoError(t, err)
	require.ErrorIs(t, s.FinishSubscription(c.JobID, subscriptionResult(1), subscriptionNow), ErrSubscriptionStorage)
	status, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, 1, status.Unresolved)
	require.EqualValues(t, 100, status.TokensToday)
}

func TestSubscriptionCrossConnectionConcurrencyHasOnlyOneExecutionGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer s.Close()
	other, err := Open(path)
	require.NoError(t, err)
	defer other.Close()
	limits := SubscriptionLimits{DailyJobs: 3, DailyTokens: 300, JobTokens: 100, JobSeconds: 60}
	a, ad := subscriptionDelivery(t, s, "a")
	b, bd := subscriptionDelivery(t, s, "b")
	type result struct {
		fresh bool
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, input := range []struct {
		s    *Store
		d    *Delivery
		hash string
	}{{s, a, ad}, {other, b, bd}} {
		wg.Go(func() {
			<-start
			_, fresh, err := input.s.ClaimSubscription(input.d.JobID, input.hash, limits, subscriptionNow)
			results <- result{fresh, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	grants := 0
	for r := range results {
		if r.fresh {
			require.NoError(t, r.err)
			grants++
		} else {
			require.Error(t, r.err)
		}
	}
	require.Equal(t, 1, grants)
	status, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, 1, status.Unresolved)
	require.Equal(t, 1, status.JobsToday)
}

func TestSubscriptionClockRollbackCannotReopenADayOrShortenAClaim(t *testing.T) {
	s, limits := subscriptionFixture(t)
	c := subscriptionClaim(t, s, limits, "today", subscriptionNow)
	require.ErrorIs(t, s.FinishSubscription(c.JobID, subscriptionResult(0), subscriptionNow.Add(-time.Second)), ErrSubscriptionClock)
	require.NoError(t, s.FinishSubscription(c.JobID, subscriptionResult(0), subscriptionNow.Add(time.Second)))
	d, digest := subscriptionDelivery(t, s, "backwards")
	_, _, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionClock)
	require.ErrorIs(t, s.PauseSubscription("", subscriptionNow), ErrSubscriptionClock)
	_, err = s.SubscriptionStatus(subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionClock)
}

func TestSubscriptionBoundedLedgerRetainsDeduplicationAfterJobDeletion(t *testing.T) {
	s, limits := subscriptionFixture(t)
	c := subscriptionClaim(t, s, limits, "retained", subscriptionNow)
	require.NoError(t, s.FinishSubscription(c.JobID, subscriptionResult(1), subscriptionNow))
	s.Delete(c.JobID)
	_, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < ?)
 INSERT INTO subscription_claims(job_id,digest,day,started_at,state,result)
 SELECT 'fixture-'||x,?, '2026-09-05', ?, 'finished', ? FROM n`, MaxDeliveries-1, c.Digest, subscriptionNow.Unix(), `{"outcome":"executor_failed","usage":{"input":0,"output":0,"cache_read":0,"cache_write":0},"pause":""}`)
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM subscription_claims").Scan(&count))
	require.Equal(t, MaxDeliveries, count)
	d, digest := subscriptionDelivery(t, s, "full")
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionCapacity)
	recovered, fresh, err := s.ClaimSubscription(c.JobID, c.Digest, limits, subscriptionNow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, "finished", recovered.State)
}

func TestSubscriptionStateUnavailableIsCategoricalAndDoesNotCreateBudget(t *testing.T) {
	s, limits := subscriptionFixture(t)
	_, err := s.SubscriptionStatus(subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionUnconfigured)
	d, digest := subscriptionDelivery(t, s, "closed")
	require.NoError(t, s.Close())
	_, _, err = s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionStorage)
	require.NotContains(t, err.Error(), "private")
}

func TestSubscriptionOnlyUnclaimedQueuedAdmissionCanReserve(t *testing.T) {
	for _, state := range []string{"dismissed", "claimed", "legacy_execution"} {
		t.Run(state, func(t *testing.T) {
			s, limits := subscriptionFixture(t)
			d, digest := subscriptionDelivery(t, s, state)
			switch state {
			case "dismissed":
				s.UpdateStatus(d.JobID, state)
			case "claimed":
				changed, err := s.TransitionDelivery(d.Key, "queued", "claimed")
				require.NoError(t, err)
				require.True(t, changed)
			case "legacy_execution":
				m := NewMulti(map[string]*Store{"vendor": s})
				fresh, err := m.StartExecution("vendor", d.JobID)
				require.NoError(t, err)
				require.True(t, fresh)
			}
			_, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
			require.Error(t, err)
			require.False(t, fresh)
			var count int
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM subscription_claims").Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestSubscriptionSettlementAndPolicySurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := Open(path)
	require.NoError(t, err)
	limits := SubscriptionLimits{DailyJobs: 2, DailyTokens: 150, JobTokens: 100, JobSeconds: 60}
	claim := subscriptionClaim(t, s, limits, "first", subscriptionNow)
	r := subscriptionResult(50)
	require.NoError(t, s.FinishSubscription(claim.JobID, r, subscriptionNow))
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.FinishSubscription(claim.JobID, r, subscriptionNow))
	recovered, fresh, err := s.ClaimSubscription(claim.JobID, claim.Digest, limits, subscriptionNow)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, "finished", recovered.State)
	subscriptionClaim(t, s, limits, "second", subscriptionNow)
	status, err := s.SubscriptionStatus(subscriptionNow)
	require.NoError(t, err)
	require.Equal(t, 2, status.JobsToday)
	require.EqualValues(t, 150, status.TokensToday)
	var version int
	require.NoError(t, s.db.QueryRow("SELECT version FROM schema_version").Scan(&version))
	require.Equal(t, 2, version)
}

func TestSubscriptionMalformedPrivatePolicyIsNotAQuotaReset(t *testing.T) {
	s, limits := subscriptionFixture(t)
	claim := subscriptionClaim(t, s, limits, "first", subscriptionNow)
	require.NoError(t, s.FinishSubscription(claim.JobID, subscriptionResult(99), subscriptionNow))
	_, err := s.db.Exec("UPDATE subscription_policy SET limits=?", `{"daily_jobs":3,"daily_tokens":250,"job_tokens":100,"job_seconds":60,"unexpected":"private fixture"}`)
	require.NoError(t, err)
	d, digest := subscriptionDelivery(t, s, "second")
	_, fresh, err := s.ClaimSubscription(d.JobID, digest, limits, subscriptionNow)
	require.ErrorIs(t, err, ErrSubscriptionStorage)
	require.False(t, fresh)
	require.NotContains(t, err.Error(), "private")
}
