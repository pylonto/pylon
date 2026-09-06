package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
)

const controlSchema = `
CREATE TABLE IF NOT EXISTS control_notices (
 key TEXT PRIMARY KEY, body BLOB NOT NULL, state TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS execution_outcomes (
 job_id TEXT PRIMARY KEY, outcome TEXT NOT NULL
);`

type Notice struct {
	Key       string `json:"delivery_key"`
	State     string `json:"status"`
	MessageID string `json:"message_id,omitempty"`
}

// ClaimNotice commits before the channel call. A claim surviving a crash is unknown,
// never replayable: Telegram cannot deduplicate a lost sendMessage acknowledgement.
func (s *Store) ClaimNotice(key string, body []byte) (*Notice, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	n := &Notice{Key: key, State: "outcome_unknown"}
	var old []byte
	err = tx.QueryRow("SELECT body,state,message_id FROM control_notices WHERE key=?", key).Scan(&old, &n.State, &n.MessageID)
	if err == nil {
		if !bytes.Equal(old, body) {
			return nil, false, ErrDeliveryConflict
		}
		return n, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM control_notices").Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= MaxDeliveries {
		return nil, false, ErrDeliveryCapacity
	}
	if _, err = tx.Exec("INSERT INTO control_notices(key,body,state) VALUES(?,?,'outcome_unknown')", key, body); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return n, true, nil
}
func (m *MultiStore) ClaimNotice(name, key string, body []byte) (*Notice, bool, error) {
	s := m.storeFor(name)
	if s == nil {
		return nil, false, fmt.Errorf("store unavailable")
	}
	return s.ClaimNotice(key, body)
}
func (m *MultiStore) CompleteNotice(name, key, messageID string) error {
	s := m.storeFor(name)
	if s == nil {
		return fmt.Errorf("store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE control_notices SET state='delivered',message_id=? WHERE key=? AND state='outcome_unknown'", messageID, key)
	return err
}

type DeliveryStatus struct {
	DeliveryKey string `json:"delivery_key"`
	JobID       string `json:"job_id"`
	Admission   string `json:"admission"`
	Execution   string `json:"execution"`
}

func (m *MultiStore) DeliveryStatus(name, key string) (*DeliveryStatus, error) {
	s := m.storeFor(name)
	if s == nil {
		return nil, fmt.Errorf("store unavailable")
	}
	d := &DeliveryStatus{DeliveryKey: key}
	err := s.db.QueryRow(`SELECT d.job_id,d.state,coalesce(e.outcome,CASE WHEN d.state='queued' THEN 'not_started' ELSE 'outcome_unknown' END)
 FROM deliveries d LEFT JOIN execution_outcomes e ON e.job_id=d.job_id WHERE d.delivery_key=?`, key).Scan(&d.JobID, &d.Admission, &d.Execution)
	return d, err
}

// StartExecution/FinishExecution are called by the executor, never the agent callback.
// They do not imply a successful patch, validation, PR, or release. Legacy jobs keep
// their follow-up lifecycle; durable deliveries gain a separate conservative fact.
func (m *MultiStore) StartExecution(name, job string) (bool, error) {
	s := m.storeFor(name)
	if s == nil {
		return false, fmt.Errorf("store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found int
	if err := s.db.QueryRow("SELECT 1 FROM deliveries WHERE job_id=?", job).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	result, err := s.db.Exec("INSERT OR IGNORE INTO execution_outcomes VALUES(?,'outcome_unknown')", job)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, fmt.Errorf("execution already claimed; reconcile before replay")
	}
	return true, nil
}
func (m *MultiStore) FinishExecution(name, job, outcome string) error {
	if outcome != "executor_returned" && outcome != "executor_failed" {
		return fmt.Errorf("invalid executor outcome")
	}
	s := m.storeFor(name)
	if s == nil {
		return fmt.Errorf("store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Fence at the write, not only a preceding SELECT: another connection may
	// atomically admit a subscription job between a check and this update.
	_, err := s.db.Exec(`UPDATE execution_outcomes SET outcome=? WHERE job_id=? AND outcome='outcome_unknown'
 AND NOT EXISTS(SELECT 1 FROM subscription_claims WHERE job_id=?)`, outcome, job, job)
	if err != nil {
		return err
	}
	var subscription bool
	if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM subscription_claims WHERE job_id=?)", job).Scan(&subscription); err != nil {
		return ErrSubscriptionStorage
	}
	if subscription {
		return ErrSubscriptionOwned
	}
	return nil
}
