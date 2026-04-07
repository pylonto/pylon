package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"sync"

	_ "modernc.org/sqlite"
)

const schemaV1 = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS job_results (
	job_id     TEXT PRIMARY KEY,
	status     TEXT NOT NULL,
	output     TEXT,
	error      TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS jobs (
	id           TEXT PRIMARY KEY,
	pipe_name    TEXT NOT NULL,
	topic_id     TEXT DEFAULT '',
	message_id   TEXT DEFAULT '',
	callback_url TEXT DEFAULT '',
	status       TEXT NOT NULL,
	session_id   TEXT DEFAULT '',
	pipeline     TEXT NOT NULL,
	body         TEXT NOT NULL,
	created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_topic_id ON jobs(topic_id);
`

func OpenDB(path string) (*sql.DB, error) {
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
	return db, nil
}

func migrate(db *sql.DB) error {
	db.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)")

	var version int
	if err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		version = 0
	}

	if version < 1 {
		if _, err := db.Exec(schemaV1); err != nil {
			return err
		}
		db.Exec("DELETE FROM schema_version")
		db.Exec("INSERT INTO schema_version (version) VALUES (1)")
	}
	return nil
}

// SQLiteJobStore persists job results to SQLite.
type SQLiteJobStore struct {
	db *sql.DB
}

func NewSQLiteJobStore(db *sql.DB) *SQLiteJobStore {
	return &SQLiteJobStore{db: db}
}

func (s *SQLiteJobStore) Save(r JobResult) {
	_, err := s.db.Exec(
		`INSERT INTO job_results (job_id, status, output, error) VALUES (?, ?, ?, ?)
		 ON CONFLICT(job_id) DO UPDATE SET status=excluded.status, output=excluded.output, error=excluded.error`,
		r.JobID, r.Status, string(r.Output), r.Error,
	)
	if err != nil {
		log.Printf("[store] failed to save job result %s: %v", r.JobID, err)
	}
}

func (s *SQLiteJobStore) Get(jobID string) (JobResult, bool) {
	var r JobResult
	var output sql.NullString
	err := s.db.QueryRow(
		"SELECT job_id, status, output, error FROM job_results WHERE job_id = ?", jobID,
	).Scan(&r.JobID, &r.Status, &output, &r.Error)
	if err != nil {
		return JobResult{}, false
	}
	if output.Valid {
		r.Output = json.RawMessage(output.String)
	}
	return r, true
}

// SQLitePendingJobs uses an in-memory cache backed by SQLite for durability.
type SQLitePendingJobs struct {
	mu   sync.RWMutex
	db   *sql.DB
	jobs map[string]*Job
}

func NewSQLitePendingJobs(db *sql.DB) *SQLitePendingJobs {
	return &SQLitePendingJobs{db: db, jobs: make(map[string]*Job)}
}

func (p *SQLitePendingJobs) Put(j *Job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[j.ID] = j
	p.persistJob(j)
}

func (p *SQLitePendingJobs) Delete(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.jobs, id)
	p.db.Exec("DELETE FROM jobs WHERE id = ?", id)
}

func (p *SQLitePendingJobs) Get(id string) (*Job, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	j, ok := p.jobs[id]
	return j, ok
}

func (p *SQLitePendingJobs) GetByTopic(topicID string) (*Job, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, j := range p.jobs {
		if j.TopicID == topicID {
			return j, true
		}
	}
	return nil, false
}

func (p *SQLitePendingJobs) List() []*Job {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Job, 0, len(p.jobs))
	for _, j := range p.jobs {
		out = append(out, j)
	}
	return out
}

func (p *SQLitePendingJobs) UpdateStatus(id string, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j, ok := p.jobs[id]; ok {
		j.Status = status
	}
	p.db.Exec("UPDATE jobs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", status, id)
}

func (p *SQLitePendingJobs) UpdateSessionID(id string, sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j, ok := p.jobs[id]; ok {
		j.SessionID = sessionID
	}
	p.db.Exec("UPDATE jobs SET session_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", sessionID, id)
}

func (p *SQLitePendingJobs) persistJob(j *Job) {
	pipelineJSON, _ := json.Marshal(j.Pipeline)
	bodyJSON, _ := json.Marshal(j.Body)
	p.db.Exec(
		`INSERT INTO jobs (id, pipe_name, topic_id, message_id, callback_url, status, session_id, pipeline, body)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   status=excluded.status, session_id=excluded.session_id,
		   topic_id=excluded.topic_id, message_id=excluded.message_id,
		   updated_at=CURRENT_TIMESTAMP`,
		j.ID, j.PipeName, j.TopicID, j.MessageID, j.CallbackURL, j.Status, j.SessionID,
		string(pipelineJSON), string(bodyJSON),
	)
}

func (p *SQLitePendingJobs) RecoverFromDB() int {
	rows, err := p.db.Query(
		"SELECT id, pipe_name, topic_id, message_id, callback_url, status, session_id, pipeline, body FROM jobs",
	)
	if err != nil {
		log.Printf("[store] failed to query jobs for recovery: %v", err)
		return 0
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var j Job
		var pipelineJSON, bodyJSON string
		if err := rows.Scan(&j.ID, &j.PipeName, &j.TopicID, &j.MessageID, &j.CallbackURL,
			&j.Status, &j.SessionID, &pipelineJSON, &bodyJSON); err != nil {
			log.Printf("[store] failed to scan job row: %v", err)
			continue
		}
		json.Unmarshal([]byte(pipelineJSON), &j.Pipeline)
		json.Unmarshal([]byte(bodyJSON), &j.Body)

		if j.Status == "running" {
			j.Status = "active"
			p.db.Exec("UPDATE jobs SET status = 'active', updated_at = CURRENT_TIMESTAMP WHERE id = ?", j.ID)
			log.Printf("[store] recovered job %s (was running, now active)", j.ID[:8])
		}

		p.mu.Lock()
		p.jobs[j.ID] = &j
		p.mu.Unlock()
		count++
	}
	return count
}
