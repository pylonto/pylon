package daemon

import (
	"bytes"
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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/channel"
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
	Global    *config.GlobalConfig
	Pylons    map[string]*config.PylonConfig
	Store    *store.MultiStore
	Channel  channel.Channel            // global default
	Channels map[string]channel.Channel // per-pylon overrides
	Limiter   *AgentLimiter
	Mux       *http.ServeMux

	hooksMu   sync.Mutex
	hookLog   map[string][]string // jobID -> recent tool-use descriptions
}

// New creates a Daemon from global config and loaded pylons.
func New(global *config.GlobalConfig, pylons map[string]*config.PylonConfig, st *store.MultiStore, ch channel.Channel, perPylon map[string]channel.Channel) *Daemon {
	d := &Daemon{
		Global:   global,
		Pylons:   pylons,
		Store:    st,
		Channel:  ch,
		Channels: perPylon,
		Limiter:   NewAgentLimiter(global.Docker.MaxConcurrent),
		Mux:       http.NewServeMux(),
		hookLog: make(map[string][]string),
	}
	d.registerRoutes()
	return d
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
	d.registerCallbackRoute()
	d.registerHooksRoute()
	d.registerExecRoute()
	d.registerApprovalHandler()
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
		log.Printf("[pylon] [%s] %q triggered, payload: %s", jobID[:8], name, string(rawBody))

		n := d.channelFor(name)
		needsApproval := n != nil && pyl.Channel != nil && pyl.Channel.Approval

		if needsApproval {
			topicID, _ := n.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
			msg := runner.ResolveTemplate(pyl.Channel.Message, body)
			msgID, err := n.SendApproval(topicID, msg, jobID)
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
				topicID, _ = n.CreateTopic(fmt.Sprintf("%s -- %s", name, jobID[:8]))
				n.SendMessage(topicID, runner.ResolveTemplate(pyl.Channel.Message, body))
			}
			d.runJob(name, pyl, jobID, body, callbackURL, topicID, "", "")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "accepted"})
	})
}

func (d *Daemon) runJob(pylonName string, pyl *config.PylonConfig, jobID string, body map[string]interface{}, callbackURL, topicID, promptOverride, sessionID string) {
	n := d.channelFor(pylonName)
	if !d.Limiter.Acquire() {
		log.Printf("[pylon] [%s] at capacity (%d), queued", jobID[:8], d.Global.Docker.MaxConcurrent)
		if n != nil && topicID != "" {
			n.SendMessage(topicID,
				fmt.Sprintf("Queued -- %d/%d agent slots in use.", d.Limiter.Active(), d.Global.Docker.MaxConcurrent))
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

	// Resolve available tools and inject into prompt + env
	tools := pyl.ResolveTools(d.Global)
	if len(tools) > 0 {
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Name
		}
		prompt += fmt.Sprintf(
			"\n\nYou have access to the following host CLI tools via the pylon tool gateway: %s. "+
				"To execute a tool, send a POST request to $PYLON_EXEC_URL with JSON body "+
				`{"tool": "<name>", "args": ["arg1", "arg2"]}. `+
				"The response contains {exit_code, stdout, stderr}. "+
				"Example: curl -s -X POST \"$PYLON_EXEC_URL\" -H 'Content-Type: application/json' "+
				`-d '{"tool":"<name>","args":[...]}'`,
			strings.Join(names, ", "),
		)
	}

	d.Store.Put(&store.Job{
		ID: jobID, PylonName: pylonName, Status: "running",
		Body: body, TopicID: topicID, CallbackURL: callbackURL,
		SessionID: sessionID, CreatedAt: time.Now(),
	})

	go func() {
		defer d.Limiter.Release()
		defer runner.CleanupWorkspace(jobID)
		var apiKey string
		var extraEnv map[string]string
		if pyl.Agent != nil {
			apiKey = pyl.Agent.APIKey
			extraEnv = pyl.Agent.Env
		}

		// Inject tool gateway env vars
		if len(tools) > 0 {
			if extraEnv == nil {
				extraEnv = make(map[string]string)
			}
			names := make([]string, len(tools))
			for i, t := range tools {
				names[i] = t.Name
			}
			extraEnv["PYLON_TOOLS"] = strings.Join(names, ",")
			extraEnv["PYLON_EXEC_URL"] = fmt.Sprintf("http://host.docker.internal:%d/exec/%s", d.Global.Server.Port, jobID)
		}

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
			Channel:       n,
			TopicID:       topicID,
		})
		if err != nil {
			log.Printf("[pylon] [%s] failed: %v", jobID[:8], err)
			d.Store.SetFailed(jobID, err.Error())
		}
		d.Store.UpdateStatus(jobID, "active")
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
			n.EditMessage(job.TopicID, job.MessageID, "Spinning up agent...")
			d.runJob(job.PylonName, pyl, jobID, job.Body, job.CallbackURL, job.TopicID, "", job.SessionID)
		case "ignore":
			log.Printf("[pylon] [%s] dismissed", jobID[:8])
			n.EditMessage(job.TopicID, job.MessageID, "Dismissed")
			d.Store.UpdateStatus(jobID, "dismissed")
			d.Store.Delete(jobID)
		}
	}

	makeMessageFn := func(source channel.Channel) func(string, string, string) {
		return func(topicID, text, incomingMsgID string) {
			// Normalize: accept both "/done" and "done" (Slack intercepts slash commands)
			cmd := strings.TrimPrefix(strings.TrimSpace(text), "/")

			if cmd == "help" || strings.HasPrefix(text, "/help@") {
				source.ReplyMessage(topicID, commandHint(source), incomingMsgID)
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
				source.SendMessage(topicID, msg)
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
					source.ReplyMessage(topicID, "No agents currently running.", incomingMsgID)
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
				source.ReplyMessage(topicID, strings.TrimSpace(b.String()), incomingMsgID)
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
			n := d.channelFor(job.PylonName)

			if cmd == "done" || strings.HasPrefix(text, "/done@") {
				runner.CleanupWorkspace(job.ID)
				n.SendMessage(topicID, "Job closed.")
				n.CloseTopic(topicID)
				d.Store.Delete(job.ID)
				return
			}

			if job.Status == "running" {
				n.SendMessage(topicID, "Agent is still working, please wait.")
				return
			}
			if job.Status == "active" {
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
					n.SendMessage(job.TopicID, msg)
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
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (d *Daemon) registerExecRoute() {
	d.Mux.HandleFunc("/exec/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jobID := strings.TrimPrefix(r.URL.Path, "/exec/")
		rawBody, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		var req struct {
			Tool string   `json:"tool"`
			Args []string `json:"args"`
		}
		if json.Unmarshal(rawBody, &req) != nil || req.Tool == "" {
			http.Error(w, `{"error":"invalid request: tool is required"}`, http.StatusBadRequest)
			return
		}

		job, ok := d.Store.Get(jobID)
		if !ok {
			http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
			return
		}
		if job.Status != "running" {
			http.Error(w, `{"error":"job is not running"}`, http.StatusConflict)
			return
		}

		pyl, exists := d.Pylons[job.PylonName]
		if !exists {
			http.Error(w, `{"error":"pylon not found"}`, http.StatusNotFound)
			return
		}

		tools := pyl.ResolveTools(d.Global)
		var toolCfg *config.ToolConfig
		for i := range tools {
			if tools[i].Name == req.Tool {
				toolCfg = &tools[i]
				break
			}
		}
		if toolCfg == nil {
			http.Error(w, `{"error":"tool not allowed"}`, http.StatusForbidden)
			return
		}

		if _, err := os.Stat(toolCfg.Path); err != nil {
			http.Error(w, `{"error":"tool binary not found on host"}`, http.StatusInternalServerError)
			return
		}

		timeout := toolCfg.TimeoutDuration()
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, toolCfg.Path, req.Args...)
		cmd.Dir = runner.WorkDir(jobID)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		exitCode := 0
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		log.Printf("[pylon] [%s] exec %s %v -> exit %d", jobID[:8], req.Tool, req.Args, exitCode)

		// Record in hook log for /status visibility
		d.hooksMu.Lock()
		d.hookLog[jobID] = append(d.hookLog[jobID], fmt.Sprintf("exec %s %v -> %d", req.Tool, req.Args, exitCode))
		if len(d.hookLog[jobID]) > 8 {
			d.hookLog[jobID] = d.hookLog[jobID][len(d.hookLog[jobID])-8:]
		}
		d.hooksMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"exit_code": exitCode,
			"stdout":    stdout.String(),
			"stderr":    stderr.String(),
		})
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
