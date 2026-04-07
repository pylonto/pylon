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

type JobResult struct {
	JobID  string          `json:"job_id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type JobStore struct {
	mu      sync.RWMutex
	results map[string]JobResult
}

func NewJobStore() *JobStore { return &JobStore{results: make(map[string]JobResult)} }

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

type Job struct {
	ID, PipeName, TopicID, MessageID, CallbackURL, Status, SessionID string
	Pipeline                                                         PipelineConfig
	Body                                                             map[string]interface{}
}

type PendingJobs struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewPendingJobs() *PendingJobs      { return &PendingJobs{jobs: make(map[string]*Job)} }
func (p *PendingJobs) Put(j *Job)       { p.mu.Lock(); p.jobs[j.ID] = j; p.mu.Unlock() }
func (p *PendingJobs) Delete(id string) { p.mu.Lock(); delete(p.jobs, id); p.mu.Unlock() }
func (p *PendingJobs) Get(id string) (*Job, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	j, ok := p.jobs[id]
	return j, ok
}
func (p *PendingJobs) GetByTopic(topicID string) (*Job, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, j := range p.jobs {
		if j.TopicID == topicID {
			return j, true
		}
	}
	return nil, false
}
func (p *PendingJobs) List() []*Job {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Job, 0, len(p.jobs))
	for _, j := range p.jobs {
		out = append(out, j)
	}
	return out
}

type AgentLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewAgentLimiter() *AgentLimiter {
	return &AgentLimiter{counts: make(map[string]int)}
}

func (l *AgentLimiter) Acquire(pipeline string, max int) bool {
	if max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[pipeline] >= max {
		return false
	}
	l.counts[pipeline]++
	return true
}

func (l *AgentLimiter) Release(pipeline string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[pipeline] > 0 {
		l.counts[pipeline]--
	}
}

func RegisterPipelineRoutes(mux *http.ServeMux, cfg *Config, store *JobStore, notifier Notifier, pending *PendingJobs, limiter *AgentLimiter) {
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

			needsApproval := notifier != nil && pipeline.Notify != nil &&
				pipeline.Notify.Actions.Investigate && !pipeline.Notify.Actions.Auto

			if needsApproval {
				topicID, _ := notifier.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
				notifyMsg := resolveTemplateEscaped(pipeline.Notify.Message, body)
				msgID, err := notifier.SendApproval(topicID, notifyMsg, jobID)
				if err != nil {
					log.Printf("[pylon] [%s] approval failed, running immediately: %v", jobID[:8], err)
					if !limiter.Acquire(name, pipeline.Agent.MaxAgents) {
						log.Printf("[pylon] [%s] pipeline %q at max agents (%d), dropping", jobID[:8], name, pipeline.Agent.MaxAgents)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusTooManyRequests)
						json.NewEncoder(w).Encode(map[string]string{"error": "pipeline at capacity"})
						return
					}
					go func() {
						defer limiter.Release(name)
						if err := RunAgentJob(context.Background(), pipeline, jobID, body, callbackURL, nil, "", "", ""); err != nil {
							log.Printf("[pylon] [%s] pipeline %q failed: %v", jobID[:8], name, err)
						}
					}()
				} else {
					pending.Put(&Job{
						ID: jobID, Pipeline: pipeline, PipeName: name, Body: body,
						Status: "awaiting_approval", TopicID: topicID, MessageID: msgID, CallbackURL: callbackURL,
					})
				}
			} else {
				if !limiter.Acquire(name, pipeline.Agent.MaxAgents) {
					log.Printf("[pylon] [%s] pipeline %q at max agents (%d), rejecting", jobID[:8], name, pipeline.Agent.MaxAgents)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					json.NewEncoder(w).Encode(map[string]string{"error": "pipeline at capacity"})
					return
				}
				var topicID string
				if notifier != nil && pipeline.Notify != nil && pipeline.Notify.Actions.Auto {
					topicID, _ = notifier.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
					notifier.SendMessage(topicID, resolveTemplateEscaped(pipeline.Notify.Message, body))
				}
				go func() {
					defer limiter.Release(name)
					if err := RunAgentJob(context.Background(), pipeline, jobID, body, callbackURL, notifier, topicID, "", ""); err != nil {
						log.Printf("[pylon] [%s] pipeline %q failed: %v", jobID[:8], name, err)
					}
				}()
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "accepted"})
		})
	}
}

func RegisterApprovalHandler(notifier Notifier, pending *PendingJobs, store *JobStore, port int, limiter *AgentLimiter) {
	runAgent := func(job *Job, prompt string) bool {
		if !limiter.Acquire(job.PipeName, job.Pipeline.Agent.MaxAgents) {
			log.Printf("[pylon] [%s] pipeline %q at max agents (%d)", job.ID[:8], job.PipeName, job.Pipeline.Agent.MaxAgents)
			notifier.SendMessage(job.TopicID, escapeMarkdownV2(fmt.Sprintf("Pipeline at capacity (%d agents). Try again later.", job.Pipeline.Agent.MaxAgents)))
			return false
		}
		job.Status = "running"
		go func() {
			defer limiter.Release(job.PipeName)
			err := RunAgentJob(context.Background(), job.Pipeline, job.ID, job.Body, job.CallbackURL, notifier, job.TopicID, prompt, job.SessionID)
			if err != nil {
				log.Printf("[pylon] [%s] pipeline %q failed: %v", job.ID[:8], job.PipeName, err)
			}
			job.Status = "active"
		}()
		return true
	}

	notifier.OnAction(func(jobID string, action string) {
		job, ok := pending.Get(jobID)
		if !ok {
			log.Printf("[pylon] [%s] callback for unknown or already-handled job", jobID[:8])
			return
		}
		switch action {
		case "investigate":
			log.Printf("[pylon] [%s] approved — spinning up agent", jobID[:8])
			if !runAgent(job, "") {
				return
			}
			notifier.EditMessage(job.TopicID, job.MessageID, escapeMarkdownV2("Spinning up agent..."))
		case "ignore":
			job.Status = "ignored"
			log.Printf("[pylon] [%s] dismissed", jobID[:8])
			notifier.EditMessage(job.TopicID, job.MessageID, escapeMarkdownV2("Dismissed"))
			pending.Delete(jobID)
		default:
			log.Printf("[pylon] [%s] unknown action %q", jobID[:8], action)
		}
	})

	notifier.OnMessage(func(topicID string, text string) {
		if text == "/agents" || strings.HasPrefix(text, "/agents@") {
			jobs := pending.List()
			if len(jobs) == 0 {
				notifier.SendMessage(topicID, escapeMarkdownV2("No active agents."))
				return
			}
			var b strings.Builder
			for _, j := range jobs {
				fmt.Fprintf(&b, "%s [%s] %s\n", j.ID[:8], j.Status, j.PipeName)
			}
			notifier.SendMessage(topicID, escapeMarkdownV2(b.String()))
			return
		}

		job, ok := pending.GetByTopic(topicID)
		if !ok {
			return
		}

		if text == "/done" || strings.HasPrefix(text, "/done@") {
			log.Printf("[pylon] [%s] closed by user", job.ID[:8])
			notifier.SendMessage(topicID, escapeMarkdownV2("Job closed. Workspace preserved at ~/.pylon/jobs/"+job.ID))
			notifier.CloseTopic(topicID)
			pending.Delete(job.ID)
			return
		}

		if job.Status == "running" {
			notifier.SendMessage(topicID, escapeMarkdownV2("Agent is still working, please wait."))
			return
		}

		if job.Status == "active" {
			log.Printf("[pylon] [%s] follow-up: %s", job.ID[:8], text)
			runAgent(job, text)
		}
	})
}

func RegisterCallbackRoute(mux *http.ServeMux, store *JobStore, notifier Notifier, pending *PendingJobs) {
	mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
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

		// Post the agent's result to the Telegram topic and store session ID.
		if job, ok := pending.Get(jobID); ok {
			if result.Status == "completed" {
				if sid := extractSessionID(result.Output); sid != "" {
					job.SessionID = sid
				}
			}
			if notifier != nil {
				var msg string
				if result.Status == "completed" {
					msg = extractResultText(result.Output)
				} else {
					msg = "Agent error: " + result.Error
				}
				if msg != "" {
					notifier.SendMessage(job.TopicID, escapeMarkdownV2(msg))
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok\n")
	})
}

// RegisterHooksRoute receives PostToolUse events from Claude Code's HTTP hooks
// and forwards formatted messages to the Telegram topic for the job.
func RegisterHooksRoute(mux *http.ServeMux, notifier Notifier, pending *PendingJobs) {
	mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/hooks/")
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

		var event struct {
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
		}
		if err := json.Unmarshal(rawBody, &event); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		msg := formatToolEvent(event.ToolName, event.ToolInput)
		if msg != "" {
			if job, ok := pending.Get(jobID); ok && notifier != nil {
				notifier.SendMessage(job.TopicID, escapeMarkdownV2(msg))
			}
		}

		w.WriteHeader(http.StatusOK)
	})
}

func formatToolEvent(toolName string, input json.RawMessage) string {
	var parsed map[string]interface{}
	json.Unmarshal(input, &parsed)

	switch toolName {
	case "Bash":
		cmd, _ := parsed["command"].(string)
		if len(cmd) > 200 {
			cmd = cmd[:200] + "..."
		}
		return "$ " + cmd
	case "Edit", "MultiEdit":
		fp, _ := parsed["file_path"].(string)
		return "Editing " + fp
	case "Write":
		fp, _ := parsed["file_path"].(string)
		return "Writing " + fp
	default:
		return ""
	}
}

func extractSessionID(output json.RawMessage) string {
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(output, &parsed)
	return parsed.SessionID
}

func extractResultText(output json.RawMessage) string {
	var parsed struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(output, &parsed); err == nil && parsed.Result != "" {
		return parsed.Result
	}
	// Fallback: return raw output truncated.
	s := string(output)
	if len(s) > 4000 {
		s = s[:4000]
	}
	return s
}
