package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// JobResult stores the outcome POSTed back by the agent container.
type JobResult struct {
	JobID  string          `json:"job_id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// JobStore is a simple in-memory store for callback results.
type JobStore struct {
	mu      sync.RWMutex
	results map[string]JobResult
}

func NewJobStore() *JobStore {
	return &JobStore{results: make(map[string]JobResult)}
}

func (s *JobStore) Save(r JobResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[r.JobID] = r
}

func (s *JobStore) Get(jobID string) (JobResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[jobID]
	return r, ok
}

// RegisterPipelineRoutes sets up webhook trigger handlers for each pipeline.
func RegisterPipelineRoutes(mux *http.ServeMux, cfg *Config, store *JobStore) {
	for name, pipeline := range cfg.Pipelines {
		name := name
		pipeline := pipeline

		if pipeline.Trigger.Type != "webhook" {
			log.Printf("[pylon] skipping pipeline %q: unknown trigger type %q", name, pipeline.Trigger.Type)
			continue
		}

		log.Printf("[pylon] registered pipeline %q on POST %s", name, pipeline.Trigger.Path)

		mux.HandleFunc(pipeline.Trigger.Path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// Read the webhook body and parse it as JSON so we can template fields.
			rawBody, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read body", http.StatusBadRequest)
				return
			}
			defer r.Body.Close()

			var body map[string]interface{}
			if err := json.Unmarshal(rawBody, &body); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}

			jobID := uuid.New().String()
			callbackURL := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", cfg.Server.Port, jobID)

			log.Printf("[pylon] [%s] pipeline %q triggered", jobID[:8], name)

			// Run the agent job in the background so the webhook returns immediately.
			go func() {
				if err := RunAgentJob(context.Background(), pipeline, jobID, body, callbackURL); err != nil {
					log.Printf("[pylon] [%s] pipeline %q failed: %v", jobID[:8], name, err)
				}
			}()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"job_id": jobID,
				"status": "accepted",
			})
		})
	}
}

// RegisterCallbackRoute sets up POST /callback/{job_id} to receive agent results.
func RegisterCallbackRoute(mux *http.ServeMux, store *JobStore) {
	mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract the job ID from the URL path: /callback/{job_id}
		jobID := strings.TrimPrefix(r.URL.Path, "/callback/")
		if jobID == "" {
			http.Error(w, "missing job_id", http.StatusBadRequest)
			return
		}

		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var result JobResult
		if err := json.Unmarshal(rawBody, &result); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		result.JobID = jobID

		store.Save(result)

		log.Printf("[pylon] [%s] callback received — status: %s", jobID[:8], result.Status)
		if result.Status == "completed" {
			log.Printf("[pylon] [%s] output: %s", jobID[:8], string(result.Output))
		} else {
			log.Printf("[pylon] [%s] error: %s", jobID[:8], result.Error)
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok\n")
	})
}
