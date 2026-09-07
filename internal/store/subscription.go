package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type subscriptionStateError string

func (e subscriptionStateError) Error() string { return string(e) }

const (
	ErrSubscriptionInvalid      subscriptionStateError = "subscription_limits_or_result_invalid"
	ErrSubscriptionConflict     subscriptionStateError = "subscription_identity_or_policy_conflict"
	ErrSubscriptionBusy         subscriptionStateError = "subscription_execution_unresolved"
	ErrSubscriptionDailyJobs    subscriptionStateError = "subscription_daily_jobs_exhausted"
	ErrSubscriptionDailyUsage   subscriptionStateError = "subscription_daily_usage_exhausted"
	ErrSubscriptionPaused       subscriptionStateError = "subscription_paused"
	ErrSubscriptionClock        subscriptionStateError = "subscription_clock_reconciliation_required"
	ErrSubscriptionCapacity     subscriptionStateError = "subscription_ledger_full"
	ErrSubscriptionStorage      subscriptionStateError = "subscription_state_unavailable"
	ErrSubscriptionUnconfigured subscriptionStateError = "subscription_not_configured"
	ErrSubscriptionOwned        subscriptionStateError = "subscription_executor_owns_settlement"
	ErrSubscriptionUsageUnknown subscriptionStateError = "subscription_usage_unknown_reservation_retained"
)

const subscriptionSchema = `
CREATE TABLE IF NOT EXISTS subscription_policy (
 id INTEGER PRIMARY KEY CHECK(id=1), limits TEXT NOT NULL,
 watermark INTEGER NOT NULL, paused TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS subscription_claims (
 job_id TEXT PRIMARY KEY, digest TEXT NOT NULL, day TEXT NOT NULL,
 started_at INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('reserved','finished')),
 result TEXT NOT NULL DEFAULT ''
);`

// SubscriptionLimits contains explicit local usage bounds, not dollars or a claim
// about the provider's remaining subscription allowance. Zero never means unlimited.
// The dedicated worker role must have one Store and one OAuth identity; these limits
// deliberately do not aggregate unrelated ordinary Pylon daemons or provider usage.
// Ceilings constrain configuration, not authorize an allocation of this size.
type SubscriptionLimits struct {
	DailyJobs   int   `json:"daily_jobs" yaml:"daily_jobs"`
	DailyTokens int64 `json:"daily_tokens" yaml:"daily_tokens"`
	JobTokens   int64 `json:"job_tokens" yaml:"job_tokens"`
	JobSeconds  int   `json:"job_seconds" yaml:"job_seconds"`
}

func (p SubscriptionLimits) Validate() error {
	if p.DailyJobs < 1 || p.DailyJobs > 24 || p.JobTokens < 1 ||
		p.DailyTokens < p.JobTokens || p.DailyTokens > 100_000_000 ||
		p.JobSeconds < 1 || p.JobSeconds > 3600 {
		return ErrSubscriptionInvalid
	}
	return nil
}

// Input excludes cache tokens in Pi's usage contract; all four counters consume
// this local budget. Never derive these facts from model text or a job callback.
type SubscriptionUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (u SubscriptionUsage) total(maximum int64) (int64, error) {
	var total int64
	for _, n := range []int64{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if n < 0 || n > maximum-total {
			return 0, ErrSubscriptionInvalid
		}
		total += n
	}
	return total, nil
}

// A nil Usage is not evidence of zero use. Even a known process exit cannot refund
// its reservation when the provider's usage or transport outcome remains unknown.
type SubscriptionResult struct {
	Outcome string             `json:"outcome"`
	Usage   *SubscriptionUsage `json:"usage"`
	Pause   string             `json:"pause"`
}

func subscriptionPauseValid(reason string) bool {
	switch reason {
	case "", "operator", "auth_unavailable", "quota_exhausted":
		return true
	default:
		return false
	}
}

func (r SubscriptionResult) validate(maximum int64) error {
	if (r.Outcome != "executor_returned" && r.Outcome != "executor_failed") ||
		!subscriptionPauseValid(r.Pause) || (r.Pause != "" && r.Outcome != "executor_failed") {
		return ErrSubscriptionInvalid
	}
	if r.Usage == nil {
		return ErrSubscriptionUsageUnknown
	}
	_, err := r.Usage.total(maximum)
	return err
}

type SubscriptionClaim struct {
	JobID     string              `json:"job_id"`
	Digest    string              `json:"digest"`
	Day       string              `json:"day"`
	StartedAt int64               `json:"started_at"`
	Deadline  int64               `json:"deadline"`
	State     string              `json:"state"`
	Result    *SubscriptionResult `json:"result,omitempty"`
}

type SubscriptionStatus struct {
	Limits      SubscriptionLimits `json:"limits"`
	Day         string             `json:"day"`
	Paused      string             `json:"paused"`
	JobsToday   int                `json:"jobs_today"`
	TokensToday int64              `json:"tokens_today"`
	Unresolved  int                `json:"unresolved"`
	Records     int                `json:"records"`
}

type subscriptionPolicy struct {
	limits    SubscriptionLimits
	watermark int64
	paused    string
}

// None of the errors below contain SQL, provider descriptions, prompts or credentials.
func subscriptionError(err error) error {
	if err == nil {
		return nil
	}
	var code subscriptionStateError
	if errors.As(err, &code) {
		return code
	}
	return ErrSubscriptionStorage
}

func (s *Store) subscriptionWrite(fn func(*sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrSubscriptionStorage
	}
	defer tx.Rollback() //nolint:errcheck // also runs after a successful commit
	// Acquire SQLite's writer reservation before reading capacity. The Store mutex
	// alone cannot serialize another process or another connection to this ledger.
	if _, err = tx.Exec("UPDATE subscription_policy SET watermark=watermark WHERE id=1"); err == nil {
		err = fn(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	return subscriptionError(err)
}

func readSubscriptionPolicy(tx *sql.Tx) (subscriptionPolicy, error) {
	var p subscriptionPolicy
	var raw string
	err := tx.QueryRow("SELECT limits,watermark,paused FROM subscription_policy WHERE id=1").Scan(&raw, &p.watermark, &p.paused)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrSubscriptionUnconfigured
	}
	if err != nil {
		return p, err
	}
	if len(raw) > 512 || json.Unmarshal([]byte(raw), &p.limits) != nil || p.limits.Validate() != nil ||
		!subscriptionPauseValid(p.paused) || p.watermark <= 0 {
		return p, ErrSubscriptionStorage
	}
	canonical, _ := json.Marshal(p.limits)
	if raw != string(canonical) {
		return p, ErrSubscriptionStorage
	}
	return p, nil
}

func readSubscriptionClaim(tx *sql.Tx, jobID string, limits SubscriptionLimits) (*SubscriptionClaim, error) {
	c := &SubscriptionClaim{JobID: jobID}
	var result string
	err := tx.QueryRow("SELECT digest,day,started_at,state,result FROM subscription_claims WHERE job_id=?", jobID).
		Scan(&c.Digest, &c.Day, &c.StartedAt, &c.State, &result)
	if err != nil {
		return nil, err
	}
	c.Deadline = c.StartedAt + int64(limits.JobSeconds)
	if c.State == "finished" {
		r, err := readSubscriptionResult(result, limits.JobTokens)
		if err != nil {
			return nil, err
		}
		c.Result = &r
	} else if c.State != "reserved" || result != "" {
		return nil, ErrSubscriptionStorage
	}
	return c, nil
}

func readSubscriptionResult(raw string, maximum int64) (SubscriptionResult, error) {
	var r SubscriptionResult
	if len(raw) > 512 || json.Unmarshal([]byte(raw), &r) != nil || r.validate(maximum) != nil {
		return r, ErrSubscriptionStorage
	}
	canonical, _ := json.Marshal(r)
	if raw != string(canonical) {
		return r, ErrSubscriptionStorage
	}
	return r, nil
}

func subscriptionCounts(tx *sql.Tx, p subscriptionPolicy, now time.Time) (*SubscriptionStatus, error) {
	if now.Unix() < p.watermark {
		return nil, ErrSubscriptionClock
	}
	status := &SubscriptionStatus{Limits: p.limits, Day: now.UTC().Format(time.DateOnly), Paused: p.paused}
	rows, err := tx.Query("SELECT day,state,result FROM subscription_claims LIMIT ?", MaxDeliveries+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		status.Records++
		if status.Records > MaxDeliveries {
			return nil, ErrSubscriptionCapacity
		}
		var day, state, result string
		if err := rows.Scan(&day, &state, &result); err != nil {
			return nil, err
		}
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return nil, ErrSubscriptionStorage
		}
		tokens := p.limits.JobTokens
		switch state {
		case "reserved":
			if result != "" {
				return nil, ErrSubscriptionStorage
			}
			status.Unresolved++
		case "finished":
			r, err := readSubscriptionResult(result, p.limits.JobTokens)
			if err != nil {
				return nil, err
			}
			tokens, err = r.Usage.total(p.limits.JobTokens)
			if err != nil {
				return nil, err
			}
		default:
			return nil, ErrSubscriptionStorage
		}
		if day == status.Day {
			status.JobsToday++
			status.TokensToday += tokens
		}
	}
	return status, rows.Err()
}

func ensureSubscriptionPolicy(tx *sql.Tx, limits SubscriptionLimits, now time.Time) (subscriptionPolicy, error) {
	p, err := readSubscriptionPolicy(tx)
	if errors.Is(err, ErrSubscriptionUnconfigured) {
		raw, _ := json.Marshal(limits)
		_, err = tx.Exec("INSERT INTO subscription_policy(id,limits,watermark) VALUES(1,?,?)", string(raw), now.Unix())
		p = subscriptionPolicy{limits: limits, watermark: now.Unix()}
	}
	if err != nil {
		return p, err
	}
	if p.limits != limits {
		return p, ErrSubscriptionConflict
	}
	return p, nil
}

// ConfigureSubscription lets an operator inspect or pause a fresh role before
// any delivery. It cannot change policy, reset counters, or reconcile unknown work.
func (s *Store) ConfigureSubscription(limits SubscriptionLimits, now time.Time) error {
	if limits.Validate() != nil || now.Unix() <= 0 {
		return ErrSubscriptionInvalid
	}
	return s.subscriptionWrite(func(tx *sql.Tx) error {
		p, err := ensureSubscriptionPolicy(tx, limits, now)
		if err == nil && now.Unix() < p.watermark {
			return ErrSubscriptionClock
		}
		return err
	})
}

// ClaimSubscription atomically claims an existing durable delivery, execution fact,
// and its maximum token reservation. Only fresh=true authorizes one executor start.
// It replaces (not follows) TransitionDelivery/StartExecution in the isolated Pi
// subscription dispatcher. Ordinary triggers cannot use this path.
// Policy is immutable here: changing limits must not reset usage or unknown work.
func (s *Store) ClaimSubscription(jobID, digest string, limits SubscriptionLimits, now time.Time) (*SubscriptionClaim, bool, error) {
	parsed, err := uuid.Parse(jobID)
	digestBytes, digestErr := hex.DecodeString(digest)
	if err != nil || parsed.String() != jobID || digestErr != nil || len(digestBytes) != sha256.Size ||
		hex.EncodeToString(digestBytes) != digest || limits.Validate() != nil || now.Unix() <= 0 {
		return nil, false, ErrSubscriptionInvalid
	}
	var claim *SubscriptionClaim
	fresh := false
	err = s.subscriptionWrite(func(tx *sql.Tx) error {
		p, err := ensureSubscriptionPolicy(tx, limits, now)
		if err != nil {
			return err
		}
		claim, err = readSubscriptionClaim(tx, jobID, limits)
		if err == nil {
			if claim.Digest != digest {
				return ErrSubscriptionConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		status, err := subscriptionCounts(tx, p, now)
		if err != nil {
			return err
		}
		switch {
		case p.paused != "":
			return ErrSubscriptionPaused
		case status.Unresolved != 0:
			return ErrSubscriptionBusy
		case status.Records >= MaxDeliveries:
			return ErrSubscriptionCapacity
		case status.JobsToday >= limits.DailyJobs:
			return ErrSubscriptionDailyJobs
		case status.TokensToday > limits.DailyTokens-limits.JobTokens:
			return ErrSubscriptionDailyUsage
		}
		var body []byte
		var deliveryState, jobState string
		err = tx.QueryRow(`SELECT d.body,d.state,j.status FROM deliveries d JOIN jobs j ON j.id=d.job_id
 WHERE d.job_id=?`, jobID).Scan(&body, &deliveryState, &jobState)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubscriptionConflict
		}
		if err != nil {
			return err
		}
		hash := sha256.Sum256(body)
		if hex.EncodeToString(hash[:]) != digest || deliveryState != "queued" || jobState != "queued" {
			return ErrSubscriptionConflict
		}
		claim = &SubscriptionClaim{JobID: jobID, Digest: digest, Day: status.Day,
			StartedAt: now.Unix(), Deadline: now.Unix() + int64(limits.JobSeconds), State: "reserved"}
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO subscription_claims(job_id,digest,day,started_at,state) VALUES(?,?,?,?,'reserved')", []any{jobID, digest, status.Day, now.Unix()}},
			{"UPDATE deliveries SET state='claimed' WHERE job_id=?", []any{jobID}},
			{"INSERT INTO execution_outcomes(job_id,outcome) VALUES(?,'outcome_unknown')", []any{jobID}},
			{"UPDATE subscription_policy SET watermark=? WHERE id=1", []any{now.Unix()}},
		} {
			if _, err := tx.Exec(statement.query, statement.args...); err != nil {
				return err
			}
		}
		fresh = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return claim, fresh, nil
}

// FinishSubscription accepts trusted executor usage, not an agent callback. A lost
// acknowledgement is recoverable with identical facts, but cannot revise a receipt,
// re-pause resumed work, refund an unknown send, or settle through the legacy API.
func (s *Store) FinishSubscription(jobID string, result SubscriptionResult, now time.Time) error {
	return s.subscriptionWrite(func(tx *sql.Tx) error {
		p, err := readSubscriptionPolicy(tx)
		if err != nil {
			return err
		}
		if err := result.validate(p.limits.JobTokens); err != nil {
			return err
		}
		claim, err := readSubscriptionClaim(tx, jobID, p.limits)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubscriptionConflict
		}
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		if claim.Result != nil {
			old, _ := json.Marshal(claim.Result)
			if !bytes.Equal(old, raw) {
				return ErrSubscriptionConflict
			}
			return nil
		}
		if now.Unix() < p.watermark || now.Unix() < claim.StartedAt {
			return ErrSubscriptionClock
		}
		if _, err := tx.Exec("UPDATE subscription_claims SET state='finished',result=? WHERE job_id=?", string(raw), jobID); err != nil {
			return err
		}
		updated, err := tx.Exec("UPDATE execution_outcomes SET outcome=? WHERE job_id=? AND outcome='outcome_unknown'", result.Outcome, jobID)
		if err != nil {
			return err
		}
		n, err := updated.RowsAffected()
		if err != nil || n != 1 {
			return ErrSubscriptionConflict
		}
		pause := p.paused
		if result.Pause != "" {
			pause = result.Pause
		}
		_, err = tx.Exec("UPDATE subscription_policy SET watermark=?,paused=? WHERE id=1", now.Unix(), pause)
		return err
	})
}

// Resume is explicit (reason=""). It never deletes a claim or releases unresolved
// execution. There is no quota reset timer and no expiry-based recovery path.
func (s *Store) PauseSubscription(reason string, now time.Time) error {
	if !subscriptionPauseValid(reason) {
		return ErrSubscriptionInvalid
	}
	return s.subscriptionWrite(func(tx *sql.Tx) error {
		p, err := readSubscriptionPolicy(tx)
		if err != nil {
			return err
		}
		if now.Unix() < p.watermark {
			return ErrSubscriptionClock
		}
		_, err = tx.Exec("UPDATE subscription_policy SET paused=?,watermark=? WHERE id=1", reason, now.Unix())
		return err
	})
}

func (s *Store) SubscriptionStatus(now time.Time) (*SubscriptionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, ErrSubscriptionStorage
	}
	defer tx.Rollback() //nolint:errcheck // read-only snapshot
	p, err := readSubscriptionPolicy(tx)
	if err != nil {
		return nil, subscriptionError(err)
	}
	status, err := subscriptionCounts(tx, p, now)
	return status, subscriptionError(err)
}
