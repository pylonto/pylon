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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/channel"
	"github.com/pylonto/pylon/internal/config"
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
	Global   *config.GlobalConfig
	Pylons   map[string]*config.PylonConfig
	Store    *store.MultiStore
	Channel  channel.Channel            // global default
	Channels map[string]channel.Channel // per-pylon overrides
	Limiter  *AgentLimiter
	Mux      *http.ServeMux

	pylonsMu sync.RWMutex
	hooksMu  sync.Mutex
	hookLog  map[string][]string // jobID -> recent tool-use descriptions
}

// New creates a Daemon from global config and loaded pylons.
func New(global *config.GlobalConfig, pylons map[string]*config.PylonConfig, st *store.MultiStore, ch channel.Channel, perPylon map[string]channel.Channel) *Daemon {
	d := &Daemon{
		Global:   global,
		Pylons:   pylons,
		Store:    st,
		Channel:  ch,
		Channels: perPylon,
		Limiter:  NewAgentLimiter(global.Docker.MaxConcurrent),
		Mux:      http.NewServeMux(),
		hookLog:  make(map[string][]string),
	}
	d.registerRoutes()
	return d
}

// pylonConfig returns the config for a pylon (thread-safe).
func (d *Daemon) pylonConfig(name string) (*config.PylonConfig, bool) {
	d.pylonsMu.RLock()
	defer d.pylonsMu.RUnlock()
	pyl, ok := d.Pylons[name]
	return pyl, ok
}

// channelFor returns the per-pylon channel if configured, otherwise the global one.
func (d *Daemon) channelFor(pylonName string) channel.Channel {
	if n, ok := d.Channels[pylonName]; ok {
		return n
	}
	return d.Channel
}

func (d *Daemon) registerRoutes() {
	registered := make(map[string]string) // path -> pylon name
	for name, pyl := range d.Pylons {
		if pyl.Trigger.Type != "webhook" {
			continue
		}
		if existing, ok := registered[pyl.Trigger.Path]; ok {
			log.Printf("[pylon] WARNING: %q skipped -- path %s already registered by %q", name, pyl.Trigger.Path, existing)
			continue
		}
		registered[pyl.Trigger.Path] = name
		d.registerWebhook(name, pyl)
	}
	d.registerTriggerRoute()
	d.registerCallbackRoute()
	d.registerHooksRoute()
	d.registerApprovalHandler()
}

func (d *Daemon) registerWebhook(name string, pyl *config.PylonConfig) {
	log.Printf("[pylon] registered %q on POST %s", name, pyl.Trigger.Path)
	// Capture only the trigger config for signature verification (immutable).
	// All other config is read fresh via d.pylonConfig() on each request.
	trigger := pyl.Trigger
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

		if trigger.Secret != "" {
			if !verifySignature(trigger, r.Header, rawBody) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		pyl, ok := d.pylonConfig(name)
		if !ok {
			http.Error(w, "pylon not found", http.StatusNotFound)
			return
		}

		var body map[string]interface{}
		if json.Unmarshal(rawBody, &body) != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		d.Store.SavePayloadSample(name, body)

		jobID := uuid.New().String()
		callbackURL := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", d.Global.Server.Port, jobID)
		log.Printf("[pylon] [%s] %q triggered, payload: %s", jobID[:8], name, string(rawBody))

		n := d.channelFor(name)
		needsApproval := n != nil && pyl.Channel != nil && pyl.Channel.Approval

		// Resolve topic name from template or fall back to default
		topicName := fmt.Sprintf("%s -- %s", name, jobID[:8])
		if pyl.Channel != nil && pyl.Channel.Topic != "" {
			topicName = runner.ResolveTemplate(pyl.Channel.Topic, body)
		}

		if needsApproval {
			topicID, _ := n.CreateTopic(topicName)
			msg := runner.ResolveTemplate(pyl.Channel.Message, body)
			msgID, err := n.SendApproval(topicID, n.FormatText(msg), jobID)
			if err != nil {
				log.Printf("[pylon] [%s] approval failed, running immediately: %v", jobID[:8], err)
				d.runJob(name, pyl, jobID, body, callbackURL, "", "", "")
			} else {
				d.Store.Put(&store.Job{
					ID: jobID, PylonName: name, Body: body, Status: "awaiting_approval",
					TopicID: topicID, MessageID: msgID, CallbackURL: callbackURL,
					CreatedAt: time.Now(),
				})
			}
		} else {
			var topicID string
			if n != nil && pyl.Channel != nil && pyl.Channel.Message != "" {
				topicID, _ = n.CreateTopic(topicName)
				n.SendMessage(topicID, n.FormatText(runner.ResolveTemplate(pyl.Channel.Message, body))) //nolint:errcheck // best-effort notification
			}
			d.runJob(name, pyl, jobID, body, callbackURL, topicID, "", "")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "accepted"})
	})
}

// registerTriggerRoute adds POST /trigger/{name} to manually fire any pylon.
func (d *Daemon) registerTriggerRoute() {
	d.Mux.HandleFunc("/trigger/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/trigger/")
		if name == "" {
			http.Error(w, "missing pylon name", http.StatusBadRequest)
			return
		}
		pyl, ok := d.pylonConfig(name)
		if !ok {
			http.Error(w, "pylon not found", http.StatusNotFound)
			return
		}

		jobID := uuid.New().String()
		callbackURL := fmt.Sprintf("http://host.docker.internal:%d/callback/%s", d.Global.Server.Port, jobID)
		log.Printf("[pylon] [%s] %q manually triggered", jobID[:8], name)

		n := d.channelFor(name)
		topicName := fmt.Sprintf("%s -- %s", name, jobID[:8])
		if pyl.Channel != nil && pyl.Channel.Topic != "" {
			topicName = runner.ResolveTemplate(pyl.Channel.Topic, nil)
		}
		var topicID string
		if n != nil {
			topicID, _ = n.CreateTopic(topicName)
		}

		d.Store.Put(&store.Job{
			ID: jobID, PylonName: name, Status: "triggered",
			TopicID: topicID, CallbackURL: callbackURL,
			CreatedAt: time.Now(),
		})

		go d.runJob(name, pyl, jobID, map[string]interface{}{}, callbackURL, topicID, "", "")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "triggered"})
	})
}

func (d *Daemon) runJob(pylonName string, pyl *config.PylonConfig, jobID string, body map[string]interface{}, callbackURL, topicID, promptOverride, sessionID string) {
	n := d.channelFor(pylonName)
	if !d.Limiter.Acquire() {
		log.Printf("[pylon] [%s] at capacity (%d), queued", jobID[:8], d.Global.Docker.MaxConcurrent)
		if n != nil && topicID != "" {
			n.SendMessage(topicID, n.FormatText(fmt.Sprintf("Queued -- %d/%d agent slots in use.", d.Limiter.Active(), d.Global.Docker.MaxConcurrent))) //nolint:errcheck // best-effort notification
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
	wsType := pyl.Workspace.Type
	if wsType == "" {
		wsType = "git-clone"
	}

	d.Store.Put(&store.Job{
		ID: jobID, PylonName: pylonName, Status: "running",
		Body: body, TopicID: topicID, CallbackURL: callbackURL,
		SessionID: sessionID, CreatedAt: time.Now(),
	})

	go func() {
		defer d.Limiter.Release()
		// Workspace persists across follow-ups; cleaned up on /done or orphan pruning.
		var apiKey string
		var extraEnv map[string]string
		var volumes []string
		if pyl.Agent != nil {
			apiKey = pyl.Agent.APIKey
			extraEnv = pyl.Agent.Env
			volumes = pyl.Agent.Volumes
		}

		pylonEnv := config.LoadPylonEnvFile(pylonName)

		err := runner.RunAgentJob(context.Background(), runner.RunParams{
			AgentType:     pyl.ResolveAgentType(d.Global),
			Image:         pyl.ResolveAgentImage(d.Global),
			Auth:          pyl.ResolveAuth(d.Global),
			APIKey:        apiKey,
			Provider:      pyl.ResolveProvider(d.Global),
			ExtraEnv:      extraEnv,
			Prompt:        prompt,
			Timeout:       pyl.ResolveTimeout(d.Global),
			JobID:         jobID,
			CallbackURL:   callbackURL,
			SessionID:     sessionID,
			Repo:          repo,
			Ref:           ref,
			WorkspaceType: wsType,
			LocalPath:     pyl.Workspace.Path,
			Volumes:       volumes,
			PylonEnv:      pylonEnv,
			Channel:       n,
			TopicID:       topicID,
		})
		if err != nil {
			log.Printf("[pylon] [%s] failed: %v", jobID[:8], err)
			// Only mark failed if the callback hasn't already set a terminal status
			if j, ok := d.Store.Get(jobID); ok && j.Status == "running" {
				d.Store.SetFailed(jobID, err.Error())
			}
		}
		// Transition to active (ready for follow-ups) unless already completed/failed by callback
		if j, ok := d.Store.Get(jobID); ok && j.Status == "running" {
			d.Store.UpdateStatus(jobID, "active")
		}
	}()
}

func (d *Daemon) registerApprovalHandler() {
	actionFn := func(jobID, action string) {
		job, ok := d.Store.Get(jobID)
		if !ok {
			return
		}
		pyl, exists := d.Pylons[job.PylonName]
		if !exists {
			return
		}
		n := d.channelFor(job.PylonName)
		switch action {
		case "investigate":
			log.Printf("[pylon] [%s] approved", jobID[:8])
			d.Store.UpdateStatus(jobID, "running")
			original := runner.ResolveTemplate(pyl.Channel.Message, job.Body)
			n.EditMessage(job.TopicID, job.MessageID, n.FormatText(original+"\n\n-- Approved, spinning up agent...")) //nolint:errcheck // best-effort
			d.runJob(job.PylonName, pyl, jobID, job.Body, job.CallbackURL, job.TopicID, "", job.SessionID)
		case "ignore":
			log.Printf("[pylon] [%s] dismissed", jobID[:8])
			original := runner.ResolveTemplate(pyl.Channel.Message, job.Body)
			n.EditMessage(job.TopicID, job.MessageID, n.FormatText(original+"\n\n-- Dismissed")) //nolint:errcheck // best-effort
			d.Store.UpdateStatus(jobID, "dismissed")
			d.Store.Delete(jobID)
		}
	}

	makeMessageFn := func(source channel.Channel) func(string, string, string) {
		return func(topicID, text, incomingMsgID string) {
			// Normalize: accept both "/done" and "done" (Slack intercepts slash commands)
			cmd := strings.TrimPrefix(strings.TrimSpace(text), "/")

			if cmd == "help" || strings.HasPrefix(text, "/help@") {
				source.ReplyMessage(topicID, source.FormatText(commandHint(source)), incomingMsgID) //nolint:errcheck // best-effort
				return
			}

			if cmd == "agents" || strings.HasPrefix(text, "/agents@") {
				jobs := d.Store.List()
				msg := "No active agents."
				if len(jobs) > 0 {
					var b strings.Builder
					for _, j := range jobs {
						fmt.Fprintf(&b, "%s [%s] %s\n", j.ID[:8], j.Status, j.PylonName)
					}
					msg = b.String()
				}
				source.ReplyMessage(topicID, source.FormatText(msg), incomingMsgID) //nolint:errcheck // best-effort
				return
			}

			if cmd == "status" || strings.HasPrefix(text, "/status@") {
				jobs := d.Store.List()
				var running []*store.Job
				for _, j := range jobs {
					if j.Status == "running" {
						running = append(running, j)
					}
				}
				if len(running) == 0 {
					source.ReplyMessage(topicID, source.FormatText("No agents currently running."), incomingMsgID) //nolint:errcheck // best-effort
					return
				}
				d.hooksMu.Lock()
				hooksCopy := make(map[string][]string, len(d.hookLog))
				for k, v := range d.hookLog {
					cp := make([]string, len(v))
					copy(cp, v)
					hooksCopy[k] = cp
				}
				d.hooksMu.Unlock()

				var b strings.Builder
				for _, j := range running {
					elapsed := time.Since(j.CreatedAt).Truncate(time.Second)
					fmt.Fprintf(&b, "%s [%s] (%s)\n", j.ID[:8], j.PylonName, elapsed)
					if events, ok := hooksCopy[j.ID]; ok && len(events) > 0 {
						for _, e := range events {
							fmt.Fprintf(&b, "  > %s\n", e)
						}
					} else {
						b.WriteString("  (starting up)\n")
					}
					b.WriteString("\n")
				}
				source.ReplyMessage(topicID, source.FormatText(strings.TrimSpace(b.String())), incomingMsgID) //nolint:errcheck // best-effort
				return
			}

			job, ok := d.Store.GetByTopic(topicID)
			if !ok {
				log.Printf("[channel] no job found for topic %s, ignoring message", topicID)
				return
			}
			pyl, exists := d.pylonConfig(job.PylonName)
			if !exists {
				log.Printf("[channel] pylon %q not found for job %s, ignoring message", job.PylonName, job.ID[:8])
				return
			}
			n := d.channelFor(job.PylonName)

			if cmd == "done" || strings.HasPrefix(text, "/done@") {
				runner.CleanupWorkspace(job.ID)
				n.ReplyMessage(topicID, n.FormatText("Job closed."), incomingMsgID) //nolint:errcheck // best-effort
				n.CloseTopic(topicID)                                               //nolint:errcheck // best-effort
				d.Store.Delete(job.ID)
				return
			}

			if job.Status == "running" {
				n.ReplyMessage(topicID, n.FormatText("Agent is still working, please wait."), incomingMsgID) //nolint:errcheck // best-effort
				return
			}
			if job.Status == "active" || job.Status == "completed" || job.Status == "failed" {
				log.Printf("[pylon] [%s] follow-up from topic %s (was %s)", job.ID[:8], topicID, job.Status)
				d.Store.UpdateStatus(job.ID, "running")
				d.runJob(job.PylonName, pyl, job.ID, job.Body, job.CallbackURL, job.TopicID, text, job.SessionID)
			}
		}
	}

	// Register handlers on all channels
	for _, n := range d.allChannels() {
		n.OnAction(actionFn)
		n.OnMessage(makeMessageFn(n))
	}
}

// allChannels returns all unique channels (global + per-pylon).
func (d *Daemon) allChannels() []channel.Channel {
	seen := make(map[channel.Channel]bool)
	var all []channel.Channel
	if d.Channel != nil {
		seen[d.Channel] = true
		all = append(all, d.Channel)
	}
	for _, n := range d.Channels {
		if n != nil && !seen[n] {
			seen[n] = true
			all = append(all, n)
		}
	}
	return all
}

// WatchConfigs polls pylon config files for changes and hot-reloads them.
// Call this in a goroutine. It stops when ctx is cancelled.
func (d *Daemon) WatchConfigs(ctx context.Context) {
	modTimes := make(map[string]time.Time)

	// Seed initial mod times
	d.pylonsMu.RLock()
	for name := range d.Pylons {
		path := config.PylonPath(name)
		if fi, err := os.Stat(path); err == nil {
			modTimes[name] = fi.ModTime()
		}
	}
	d.pylonsMu.RUnlock()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.pylonsMu.RLock()
			names := make([]string, 0, len(d.Pylons))
			for name := range d.Pylons {
				names = append(names, name)
			}
			d.pylonsMu.RUnlock()

			for _, name := range names {
				path := config.PylonPath(name)
				fi, err := os.Stat(path)
				if err != nil {
					continue
				}
				if fi.ModTime().Equal(modTimes[name]) {
					continue
				}
				modTimes[name] = fi.ModTime()

				pyl, err := config.LoadPylon(name)
				if err != nil {
					log.Printf("[pylon] config reload failed for %q: %v", name, err)
					continue
				}

				d.pylonsMu.Lock()
				d.Pylons[name] = pyl
				d.pylonsMu.Unlock()
				log.Printf("[pylon] config reloaded: %q", name)
			}
		}
	}
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
			if n := d.channelFor(job.PylonName); n != nil {
				var msg string
				if result.Status == "completed" {
					msg = extractResultText(result.Output)
				} else {
					msg = "Agent error: " + result.Error
				}
				if msg != "" {
					if _, err := n.SendMessage(job.TopicID, n.FormatText(msg)); err != nil {
						log.Printf("[pylon] [%s] failed to send result to channel: %v", jobID[:8], err)
					}
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
			d.hooksMu.Lock()
			d.hookLog[jobID] = append(d.hookLog[jobID], msg)
			if len(d.hookLog[jobID]) > 8 {
				d.hookLog[jobID] = d.hookLog[jobID][len(d.hookLog[jobID])-8:]
			}
			d.hooksMu.Unlock()

			// Append to the persistent log file so events appear in the
			// "l" (tail) view alongside container output.
			appendEventToLog(jobID, msg)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// appendEventToLog appends a formatted tool event to the persistent log file
// for a job. This lets the TUI "l" (tail) view show tool events alongside
// container output in real time.
func appendEventToLog(jobID, msg string) {
	logPath := runner.LogPath(jobID)
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return // log file may not exist yet; best-effort
	}
	defer f.Close()
	short := jobID
	if len(short) > 8 {
		short = short[:8]
	}
	fmt.Fprintf(f, "[agent] [%s] > %s\n", short, msg)
}

func verifySignature(trigger config.TriggerConfig, header http.Header, body []byte) bool {
	if trigger.SignatureHeader == "" {
		return true
	}
	sig := header.Get(trigger.SignatureHeader)
	if sig == "" {
		return false
	}
	secret := os.ExpandEnv(trigger.Secret)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

func formatToolEvent(toolName string, input json.RawMessage) string {
	var parsed map[string]interface{}
	json.Unmarshal(input, &parsed)
	switch strings.ToLower(toolName) {
	case "bash":
		cmd, _ := parsed["command"].(string)
		if len(cmd) > 200 {
			cmd = cmd[:200] + "..."
		}
		return "$ " + cmd
	case "edit", "multiedit":
		fp, _ := parsed["file_path"].(string)
		if fp == "" {
			fp, _ = parsed["filePath"].(string)
		}
		return "Editing " + fp
	case "write":
		fp, _ := parsed["file_path"].(string)
		if fp == "" {
			fp, _ = parsed["filePath"].(string)
		}
		return "Writing " + fp
	case "read":
		fp, _ := parsed["file_path"].(string)
		if fp == "" {
			fp, _ = parsed["filePath"].(string)
		}
		return "Reading " + fp
	case "glob":
		pattern, _ := parsed["pattern"].(string)
		return "Glob " + pattern
	case "grep":
		pattern, _ := parsed["pattern"].(string)
		return "Grep " + pattern
	}
	if toolName != "" {
		return toolName
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

func commandHint(n channel.Channel) string {
	cmds := n.Commands()
	parts := make([]string, len(cmds))
	for i, c := range cmds {
		parts[i] = "`" + c.Name + "`"
	}
	return "Commands: " + strings.Join(parts, "  ")
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
