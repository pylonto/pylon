package store

import (
	"database/sql"
	"encoding/json"
	"log"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    pylon_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    trigger_payload TEXT,
    agent_output TEXT,
    telegram_topic_id TEXT DEFAULT '',
    telegram_message_id TEXT DEFAULT '',
    container_id TEXT DEFAULT '',
    callback_url TEXT DEFAULT '',
    session_id TEXT DEFAULT '',
    error TEXT DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_pylon ON jobs(pylon_name);
`

// Job represents a pipeline job.
type Job struct {
	ID          string
	PylonName   string
	Status      string // pending, awaiting_approval, approved, running, completed, failed, timeout, dismissed
	TopicID     string
	MessageID   string
	CallbackURL string
	SessionID   string
	Body        map[string]interface{}
	ContainerID string
	Error       string
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
}

// Store manages job persistence with an in-memory cache backed by SQLite.
type Store struct {
	mu   sync.RWMutex
	db   *sql.DB
	jobs map[string]*Job
}

// Open creates a new Store backed by the given SQLite database path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")
	db.Exec("PRAGMA foreign_keys=ON")

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, jobs: make(map[string]*Job)}, nil
}

func migrate(db *sql.DB) error {
	db.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)")

	var version int
	if err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		version = 0
	}
	if version < 1 {
		if _, err := db.Exec(schema); err != nil {
			return err
		}
		db.Exec("DELETE FROM schema_version")
		db.Exec("INSERT INTO schema_version (version) VALUES (1)")
	}
	return nil
}

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }

// Put adds or updates a job in memory and SQLite.
func (s *Store) Put(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = j
	s.persist(j)
}

// Get returns a job by ID.
func (s *Store) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

// GetByTopic finds a job by its Telegram topic ID.
func (s *Store) GetByTopic(topicID string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, j := range s.jobs {
		if j.TopicID == topicID {
			return j, true
		}
	}
	return nil, false
}

// Delete removes a job from memory and SQLite.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	s.db.Exec("DELETE FROM jobs WHERE id = ?", id)
}

// UpdateStatus updates job status in memory and SQLite.
func (s *Store) UpdateStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = status
	}
	s.db.Exec("UPDATE jobs SET status = ? WHERE id = ?", status, id)
}

// UpdateSessionID updates the session ID for follow-up conversations.
func (s *Store) UpdateSessionID(id, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.SessionID = sessionID
	}
	s.db.Exec("UPDATE jobs SET session_id = ? WHERE id = ?", sessionID, id)
}

// SetCompleted marks a job as completed with output.
func (s *Store) SetCompleted(id string, output json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if j, ok := s.jobs[id]; ok {
		j.Status = "completed"
		j.CompletedAt = &now
	}
	s.db.Exec("UPDATE jobs SET status = 'completed', agent_output = ?, completed_at = ? WHERE id = ?",
		string(output), now, id)
}

// SetFailed marks a job as failed with an error message.
func (s *Store) SetFailed(id, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if j, ok := s.jobs[id]; ok {
		j.Status = "failed"
		j.Error = errMsg
		j.CompletedAt = &now
	}
	s.db.Exec("UPDATE jobs SET status = 'failed', error = ?, completed_at = ? WHERE id = ?",
		errMsg, now, id)
}

// List returns all in-memory jobs.
func (s *Store) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// ListByPylon returns jobs for a specific pylon.
func (s *Store) ListByPylon(name string) []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Job
	for _, j := range s.jobs {
		if j.PylonName == name {
			out = append(out, j)
		}
	}
	return out
}

// RecentJobs queries the database for recent jobs (not just in-memory).
func (s *Store) RecentJobs(pylonName string, limit int) ([]*Job, error) {
	query := "SELECT id, pylon_name, status, error, created_at, started_at, completed_at FROM jobs"
	var args []interface{}
	if pylonName != "" {
		query += " WHERE pylon_name = ?"
		args = append(args, pylonName)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j := &Job{}
		var startedAt, completedAt sql.NullTime
		if err := rows.Scan(&j.ID, &j.PylonName, &j.Status, &j.Error, &j.CreatedAt, &startedAt, &completedAt); err != nil {
			continue
		}
		if startedAt.Valid {
			j.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			j.CompletedAt = &completedAt.Time
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// RecoverFromDB loads all non-terminal jobs from SQLite into memory.
func (s *Store) RecoverFromDB() int {
	rows, err := s.db.Query(
		`SELECT id, pylon_name, status, telegram_topic_id, telegram_message_id,
		        callback_url, session_id, trigger_payload, error, created_at
		 FROM jobs WHERE status NOT IN ('completed', 'failed', 'timeout', 'dismissed')`,
	)
	if err != nil {
		log.Printf("[store] recovery query failed: %v", err)
		return 0
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		j := &Job{}
		var payload sql.NullString
		if err := rows.Scan(&j.ID, &j.PylonName, &j.Status, &j.TopicID, &j.MessageID,
			&j.CallbackURL, &j.SessionID, &payload, &j.Error, &j.CreatedAt); err != nil {
			log.Printf("[store] failed to scan job: %v", err)
			continue
		}
		if payload.Valid {
			json.Unmarshal([]byte(payload.String), &j.Body)
		}
		if j.Status == "running" {
			j.Status = "active"
			s.db.Exec("UPDATE jobs SET status = 'active' WHERE id = ?", j.ID)
		}
		s.mu.Lock()
		s.jobs[j.ID] = j
		s.mu.Unlock()
		count++
	}
	return count
}

func (s *Store) persist(j *Job) {
	bodyJSON, _ := json.Marshal(j.Body)
	s.db.Exec(
		`INSERT INTO jobs (id, pylon_name, status, telegram_topic_id, telegram_message_id,
		                   callback_url, session_id, trigger_payload, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   status=excluded.status, session_id=excluded.session_id,
		   telegram_topic_id=excluded.telegram_topic_id,
		   telegram_message_id=excluded.telegram_message_id`,
		j.ID, j.PylonName, j.Status, j.TopicID, j.MessageID,
		j.CallbackURL, j.SessionID, string(bodyJSON), j.Error, j.CreatedAt,
	)
}
