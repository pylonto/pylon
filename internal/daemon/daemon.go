package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/notifier"
	"github.com/pylonto/pylon/internal/runner"
	"github.com/pylonto/pylon/internal/store"
)

// AgentLimiter controls max concurrent agent containers.
type AgentLimiter struct {
	mu    sync.Mutex
	count int
	max   int
}

func NewAgentLimiter(max int) *AgentLimiter {
	return &AgentLimiter{max: max}
}

func (l *AgentLimiter) Acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max > 0 && l.count >= l.max {
		return false
	}
	l.count++
	return true
}

func (l *AgentLimiter) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count > 0 {
		l.count--
	}
}

func (l *AgentLimiter) Active() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// Daemon is the main Pylon server.
type Daemon struct {
	Global  *config.GlobalConfig
	Pylons  map[string]*config.PylonConfig
	Store   *store.Store
	Notify  notifier.Notifier
	Limiter *AgentLimiter
	Mux     *http.ServeMux
}

// New creates a Daemon from global config and loaded pylons.
func New(global *config.GlobalConfig, pylons map[string]*config.PylonConfig, st *store.Store, n notifier.Notifier) *Daemon {
	d := &Daemon{
		Global:  global,
		Pylons:  pylons,
		Store:   st,
		Notify:  n,
		Limiter: NewAgentLimiter(global.Docker.MaxConcurrent),
		Mux:     http.NewServeMux(),
	}
	d.registerRoutes()
	return d
}

func (d *Daemon) registerRoutes() {
	for name, pyl := range d.Pylons {
		if pyl.Trigger.Type != "webhook" {
			continue
		}
		d.registerWebhook(name, pyl)
	}
	d.registerCallbackRoute()
	d.registerHooksRoute()

	if d.Notify != nil {
		d.registerApprovalHandler()
	}
}

func (d *Daemon) registerWebhook(name string, pyl *config.PylonConfig) {
	log.Printf("[pylon] registered %q on POST %s", name, pyl.Trigger.Path)
	d.Mux.HandleFunc(pyl.Trigger.Path, func(w http.ResponseWriter, r *http.Request) {
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

		if pyl.Trigger.Secret != "" {
			if !verifySignature(pyl.Trigger, r.Header, rawBody) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		var body map[string]interface{}
		if json.Unmarshal(rawBody, &body) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		jobID := uuid.New().String()
		callbackURL := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", d.Global.Server.Port, jobID)
		log.Printf("[pylon] [%s] %q triggered", jobID[:8], name)

		needsApproval := d.Notify != nil && pyl.Notify != nil && pyl.Notify.Approval

		if needsApproval {
			topicID, _ := d.Notify.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
			msg := runner.ResolveTemplateEscaped(pyl.Notify.Message, body)
			msgID, err := d.Notify.SendApproval(topicID, msg, jobID)
			if err != nil {
				log.Printf("[pylon] [%s] approval failed, running immediately: %v", jobID[:8], err)
				d.runJob(name, pyl, jobID, body, callbackURL, "", "", "")
			} else {
				d.Store.Put(&store.Job{
					ID: jobID, PylonName: name, Body: body, Status: "awaiting_approval",
					TopicID: topicID, MessageID: msgID, CallbackURL: callbackURL,
				})
			}
		} else {
			var topicID string
			if d.Notify != nil && pyl.Notify != nil && pyl.Notify.Message != "" {
				topicID, _ = d.Notify.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
				d.Notify.SendMessage(topicID, runner.ResolveTemplateEscaped(pyl.Notify.Message, body))
			}
			d.runJob(name, pyl, jobID, body, callbackURL, topicID, "", "")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "accepted"})
	})
}

func (d *Daemon) runJob(pylonName string, pyl *config.PylonConfig, jobID string, body map[string]interface{}, callbackURL, topicID, promptOverride, sessionID string) {
	if !d.Limiter.Acquire() {
		log.Printf("[pylon] [%s] at capacity (%d), queued", jobID[:8], d.Global.Docker.MaxConcurrent)
		if d.Notify != nil && topicID != "" {
			d.Notify.SendMessage(topicID, notifier.EscapeMarkdownV2(
				fmt.Sprintf("Queued -- %d/%d agent slots in use.", d.Limiter.Active(), d.Global.Docker.MaxConcurrent)))
		}
		// TODO: implement proper queue. For now, reject.
		return
	}

	prompt := promptOverride
	if prompt == "" && pyl.Agent != nil {
		prompt = runner.ResolveTemplate(pyl.Agent.Prompt, body)
	}

	repo := runner.ResolveTemplate(pyl.Workspace.Repo, body)
	ref := runner.ResolveTemplate(pyl.Workspace.Ref, body)
	if ref == "" {
		ref = "main"
	}

	go func() {
		defer d.Limiter.Release()
		err := runner.RunAgentJob(context.Background(), runner.RunParams{
			Image:       pyl.ResolveAgentImage(d.Global),
			Auth:        pyl.ResolveAuth(d.Global),
			Prompt:      prompt,
			Timeout:     pyl.ResolveTimeout(d.Global),
			JobID:       jobID,
			CallbackURL: callbackURL,
			SessionID:   sessionID,
			Repo:        repo,
			Ref:         ref,
			Notifier:    d.Notify,
			TopicID:     topicID,
		})
		if err != nil {
			log.Printf("[pylon] [%s] failed: %v", jobID[:8], err)
			d.Store.SetFailed(jobID, err.Error())
		}
		d.Store.UpdateStatus(jobID, "active")
	}()
}

func (d *Daemon) registerApprovalHandler() {
	d.Notify.OnAction(func(jobID, action string) {
		job, ok := d.Store.Get(jobID)
		if !ok {
			return
		}
		pyl, exists := d.Pylons[job.PylonName]
		if !exists {
			return
		}
		switch action {
		case "investigate":
			log.Printf("[pylon] [%s] approved", jobID[:8])
			d.Store.UpdateStatus(jobID, "running")
			d.Notify.EditMessage(job.TopicID, job.MessageID, notifier.EscapeMarkdownV2("Spinning up agent..."))
			d.runJob(job.PylonName, pyl, jobID, job.Body, job.CallbackURL, job.TopicID, "", job.SessionID)
		case "ignore":
			log.Printf("[pylon] [%s] dismissed", jobID[:8])
			d.Notify.EditMessage(job.TopicID, job.MessageID, notifier.EscapeMarkdownV2("Dismissed"))
			d.Store.UpdateStatus(jobID, "dismissed")
			d.Store.Delete(jobID)
		}
	})

	d.Notify.OnMessage(func(topicID, text string) {
		if text == "/agents" || strings.HasPrefix(text, "/agents@") {
			jobs := d.Store.List()
			if len(jobs) == 0 {
				d.Notify.SendMessage(topicID, notifier.EscapeMarkdownV2("No active agents."))
				return
			}
			var b strings.Builder
			for _, j := range jobs {
				fmt.Fprintf(&b, "%s [%s] %s\n", j.ID[:8], j.Status, j.PylonName)
			}
			d.Notify.SendMessage(topicID, notifier.EscapeMarkdownV2(b.String()))
			return
		}

		job, ok := d.Store.GetByTopic(topicID)
		if !ok {
			return
		}
		pyl, exists := d.Pylons[job.PylonName]
		if !exists {
			return
		}

		if text == "/done" || strings.HasPrefix(text, "/done@") {
			runner.CleanupWorkspace(job.ID)
			d.Notify.SendMessage(topicID, notifier.EscapeMarkdownV2("Job closed."))
			d.Notify.CloseTopic(topicID)
			d.Store.Delete(job.ID)
			return
		}

		if job.Status == "running" {
			d.Notify.SendMessage(topicID, notifier.EscapeMarkdownV2("Agent is still working, please wait."))
			return
		}
		if job.Status == "active" {
			d.Store.UpdateStatus(job.ID, "running")
			d.runJob(job.PylonName, pyl, job.ID, job.Body, job.CallbackURL, job.TopicID, text, job.SessionID)
		}
	})
}

func (d *Daemon) registerCallbackRoute() {
	d.Mux.HandleFunc("/callback/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/callback/")
		rawBody, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var result struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(rawBody, &result) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		log.Printf("[pylon] [%s] callback: %s", jobID[:8], result.Status)

		if job, ok := d.Store.Get(jobID); ok {
			if result.Status == "completed" {
				d.Store.SetCompleted(jobID, result.Output)
				if sid := extractSessionID(result.Output); sid != "" {
					d.Store.UpdateSessionID(jobID, sid)
				}
			} else {
				d.Store.SetFailed(jobID, result.Error)
			}
			if d.Notify != nil {
				var msg string
				if result.Status == "completed" {
					msg = extractResultText(result.Output)
				} else {
					msg = "Agent error: " + result.Error
				}
				if msg != "" {
					d.Notify.SendMessage(job.TopicID, notifier.EscapeMarkdownV2(msg))
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (d *Daemon) registerHooksRoute() {
	d.Mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/hooks/")
		rawBody, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var event struct {
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
		}
		json.Unmarshal(rawBody, &event)

		msg := formatToolEvent(event.ToolName, event.ToolInput)
		if msg != "" {
			if job, ok := d.Store.Get(jobID); ok && d.Notify != nil {
				d.Notify.SendMessage(job.TopicID, notifier.EscapeMarkdownV2(msg))
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func verifySignature(trigger config.TriggerConfig, header http.Header, body []byte) bool {
	if trigger.SignatureHeader == "" {
		return true
	}
	sig := header.Get(trigger.SignatureHeader)
	if sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(trigger.Secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
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
	}
	return ""
}

func extractSessionID(output json.RawMessage) string {
	var p struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(output, &p)
	return p.SessionID
}

func extractResultText(output json.RawMessage) string {
	var p struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(output, &p) == nil && p.Result != "" {
		return p.Result
	}
	s := string(output)
	if len(s) > 4000 {
		s = s[:4000]
	}
	return s
}
