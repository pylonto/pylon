package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const MaxDeliveries = 4096
const MaxDeliveryBytes = 64 * 1024

var ErrDeliveryShape = errors.New("delivery must be a JSON object of at most 64 KiB")

var ErrDeliveryConflict = errors.New("delivery key already names different bytes")
var ErrDeliveryCapacity = errors.New("delivery ledger full; archive it deliberately before accepting more work")

// Delivery is durable admission, not proof that an agent completed or its patch is valid.
// Claimed work is never automatically replayed: a crash may have occurred after an external
// side effect. The operator reconciles that job before deliberately creating another delivery.
type Delivery struct {
	Key       string `json:"delivery_key"`
	JobID     string `json:"job_id"`
	PylonName string `json:"pylon_name"`
	State     string `json:"status"`
	Body      []byte `json:"-"`
}

const deliverySchema = `CREATE TABLE IF NOT EXISTS deliveries (
 delivery_key TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE, pylon_name TEXT NOT NULL,
 body BLOB NOT NULL, state TEXT NOT NULL DEFAULT 'queued', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`

func (s *Store) AcceptDelivery(name, key string, body []byte) (*Delivery, bool, error) {
	var decoded map[string]interface{}
	if len(body) > MaxDeliveryBytes || json.Unmarshal(body, &decoded) != nil || decoded == nil {
		return nil, false, ErrDeliveryShape
	}
	// Serialize with other admissions and fail on every SQL error. Unlike the historical job
	// cache, SQLite is authoritative here; a 202 must not survive a failed persistence write.
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback() //nolint:errcheck // also runs after commit
	d := &Delivery{Key: key, PylonName: name, Body: body, State: "queued"}
	err = tx.QueryRow("SELECT job_id, pylon_name, body, state FROM deliveries WHERE delivery_key = ?", key).Scan(&d.JobID, &d.PylonName, &d.Body, &d.State)
	if err == nil {
		if d.PylonName != name || !bytes.Equal(d.Body, body) {
			return nil, false, ErrDeliveryConflict
		}
		return d, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM deliveries").Scan(&count); err != nil {
		return nil, false, err
	}
	if count >= MaxDeliveries {
		return nil, false, ErrDeliveryCapacity
	}
	d.JobID = uuid.NewString()
	if _, err := tx.Exec("INSERT INTO deliveries(delivery_key,job_id,pylon_name,body) VALUES(?,?,?,?)", key, d.JobID, name, body); err != nil {
		return nil, false, err
	}
	now := time.Now()
	if _, err := tx.Exec("INSERT INTO jobs(id,pylon_name,status,trigger_payload,created_at) VALUES(?,?,'queued',?,?)", d.JobID, name, string(body), now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	// Execution is visible through the existing jobs/Nexus surfaces. The delivery row
	// separately remembers admission even after an operator dismisses/deletes that job.
	s.jobs[d.JobID] = &Job{ID: d.JobID, PylonName: name, Status: "queued", Body: decoded, CreatedAt: now}
	return d, true, nil
}

const pendingDeliveryPredicate = "d.state='queued' AND j.status='queued'"

func (s *Store) PendingDeliveries() ([]Delivery, error) {
	rows, err := s.db.Query("SELECT d.delivery_key,d.job_id,d.pylon_name,d.body,d.state FROM deliveries d JOIN jobs j ON j.id=d.job_id WHERE " + pendingDeliveryPredicate + " ORDER BY d.created_at,d.rowid LIMIT 16")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.Key, &d.JobID, &d.PylonName, &d.Body, &d.State); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) TransitionDelivery(key, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.db.Exec(`UPDATE deliveries SET state = ? WHERE delivery_key = ? AND state = ?
        AND (? != 'claimed' OR EXISTS (SELECT 1 FROM jobs WHERE jobs.id=deliveries.job_id AND jobs.status='queued'))`, to, key, from, to)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (m *MultiStore) AcceptDelivery(name, key string, body []byte) (*Delivery, bool, error) {
	s := m.storeFor(name)
	if s == nil {
		return nil, false, fmt.Errorf("store unavailable")
	}
	d, fresh, err := s.AcceptDelivery(name, key, body)
	if err == nil {
		m.mu.Lock()
		m.index[d.JobID] = name
		m.mu.Unlock()
	}
	return d, fresh, err
}

func (m *MultiStore) PendingDeliveries() ([]Delivery, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Delivery
	for _, s := range m.stores {
		ds, err := s.PendingDeliveries()
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}
	return out, nil
}

func (m *MultiStore) TransitionDelivery(d Delivery, from, to string) (bool, error) {
	s := m.storeFor(d.PylonName)
	if s == nil {
		return false, fmt.Errorf("store unavailable")
	}
	return s.TransitionDelivery(d.Key, from, to)
}
