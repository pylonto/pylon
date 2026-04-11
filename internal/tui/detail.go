package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
)

// detailModel shows a single pylon's config and recent jobs.
type detailModel struct {
	name           string
	pylon          *config.PylonConfig
	global         *config.GlobalConfig
	jobs           []*store.Job
	cursor         int
	focused        bool // true when the detail pane has keyboard focus
	showFullPrompt bool
	showJobs       bool // toggle jobs section visibility
	confirmKill    bool // when true, waiting for y/n to confirm kill
	confirmDismiss bool // when true, waiting for y/n to confirm dismiss
	copyFlash      copyFlashModel
	err            error

	// Alert template builder
	alertBuilder  bool              // true when in builder mode
	alertPaths    []string          // available {{ .body.X }} paths
	alertValues   map[string]string // path -> sample value from last payload
	alertCursor   int
	alertChecked  map[int]bool
}

func newDetailModel(name string) detailModel {
	return detailModel{name: name, showJobs: true}
}

type detailLoadedMsg struct {
	pylon  *config.PylonConfig
	global *config.GlobalConfig
	jobs   []*store.Job
	err    error
}

// jobsRefreshedMsg carries only updated job data (no config reload).
type jobsRefreshedMsg struct {
	jobs []*store.Job
}

func (m detailModel) Init() tea.Cmd {
	name := m.name
	return func() tea.Msg {
		pyl, err := config.LoadPylon(name)
		if err != nil {
			return detailLoadedMsg{err: err}
		}
		global, _ := config.LoadGlobal()

		var jobs []*store.Job
		dbPath := config.PylonDBPath(name)
		if _, statErr := os.Stat(dbPath); statErr == nil {
			if s, openErr := store.Open(dbPath); openErr == nil {
				jobs, _ = s.RecentJobs(name, 20)
				_ = s.Close()
			}
		}

		// Check running/active jobs against live containers.
		// If the container is gone, mark as stale.
		for _, j := range jobs {
			if !isRunningStatus(j.Status) {
				continue
			}
			out, cerr := exec.Command("docker", "ps", "-a", "--filter",
				fmt.Sprintf("label=pylon.job=%s", j.ID), "--format", "{{.ID}}").Output()
			if cerr != nil || strings.TrimSpace(string(out)) == "" {
				j.Status = "stale"
			}
		}

		return detailLoadedMsg{pylon: pyl, global: global, jobs: jobs}
	}
}

// refreshJobs re-queries only the job list without reloading config.
func (m detailModel) refreshJobs() tea.Cmd {
	name := m.name
	return func() tea.Msg {
		var jobs []*store.Job
		dbPath := config.PylonDBPath(name)
		if _, statErr := os.Stat(dbPath); statErr == nil {
			if s, openErr := store.Open(dbPath); openErr == nil {
				jobs, _ = s.RecentJobs(name, 20)
				_ = s.Close()
			}
		}
		for _, j := range jobs {
			if !isRunningStatus(j.Status) {
				continue
			}
			out, cerr := exec.Command("docker", "ps", "-a", "--filter",
				fmt.Sprintf("label=pylon.job=%s", j.ID), "--format", "{{.ID}}").Output()
			if cerr != nil || strings.TrimSpace(string(out)) == "" {
				j.Status = "stale"
			}
		}
		return jobsRefreshedMsg{jobs: jobs}
	}
}

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case detailLoadedMsg:
		m.pylon = msg.pylon
		m.global = msg.global
		m.jobs = msg.jobs
		m.err = msg.err

	case jobsRefreshedMsg:
		m.jobs = msg.jobs

	case detailEditorDoneMsg:
		// Reload config after editing
		return m, m.Init()

	case copiedMsg:
		var cmd tea.Cmd
		m.copyFlash, cmd = m.copyFlash.show(msg.label)
		return m, cmd

	case copyFlashClearMsg:
		m.copyFlash = m.copyFlash.Update(msg)

	case containerFoundMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		switch msg.action {
		case "logs":
			c := exec.Command("docker", "logs", "-f", "--tail", "50", msg.containerID)
			return m, tea.ExecProcess(c, func(err error) tea.Msg {
				return detailEditorDoneMsg{err: err}
			})
		case "kill":
			return m, killContainerCmd(msg.containerID)
		}
		return m, nil

	case jobKilledMsg:
		m.confirmKill = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.Init()

	case jobDismissedMsg:
		m.confirmDismiss = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.Init()

	case tea.KeyMsg:
		// Confirmation mode intercepts all keys
		if m.confirmKill || m.confirmDismiss {
			switch msg.String() {
			case "y":
				j := m.selectedJob()
				if j != nil {
					if m.confirmKill {
						m.confirmKill = false
						return m, findContainerCmd(j.ID, "kill")
					}
					m.confirmDismiss = false
					return m, dismissJobCmd(m.name, j.ID)
				}
				m.confirmKill = false
				m.confirmDismiss = false
			default:
				m.confirmKill = false
				m.confirmDismiss = false
			}
			return m, nil
		}

		// Alert builder mode
		if m.alertBuilder {
			switch msg.String() {
			case keyUp, keyK:
				if m.alertCursor > 0 {
					m.alertCursor--
				}
			case keyDown, keyJ:
				if m.alertCursor < len(m.alertPaths)-1 {
					m.alertCursor++
				}
			case " ":
				m.alertChecked[m.alertCursor] = !m.alertChecked[m.alertCursor]
			case keyEnter:
				// Build template from selected fields and save
				var lines []string
				for i, p := range m.alertPaths {
					if m.alertChecked[i] {
						lines = append(lines, "{{ .body."+p+" }}")
					}
				}
				if len(lines) > 0 && m.pylon != nil {
					if m.pylon.Channel == nil {
						m.pylon.Channel = &config.PylonChannel{}
					}
					m.pylon.Channel.Message = strings.Join(lines, "\n")
					config.SavePylon(m.pylon)
				}
				m.alertBuilder = false
				return m, m.Init()
			case keyEsc:
				m.alertBuilder = false
			}
			return m, nil
		}

		switch msg.String() {
		case keyUp, keyK:
			if m.showJobs && m.cursor > 0 {
				m.cursor--
			}
		case keyDown, keyJ:
			if m.showJobs && m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case keyY:
			if url := m.webhookURL(); url != "" {
				return m, copyToClipboard(url, "webhook URL")
			}
		case keyP:
			m.showFullPrompt = !m.showFullPrompt
		case keyT:
			m.showJobs = !m.showJobs
		case keyE:
			return m, m.openEditor()
		case keyA:
			if paths, values := m.loadPayloadPaths(); len(paths) > 0 {
				m.alertBuilder = true
				m.alertPaths = paths
				m.alertValues = values
				m.alertCursor = 0
				m.alertChecked = make(map[int]bool)
				// Pre-check fields that are already in the template
				if m.pylon != nil && m.pylon.Channel != nil {
					for i, p := range paths {
						if strings.Contains(m.pylon.Channel.Message, ".body."+p) {
							m.alertChecked[i] = true
						}
					}
				}
			}
		case keyL:
			if j := m.selectedJob(); j != nil && isRunningStatus(j.Status) {
				return m, findContainerCmd(j.ID, "logs")
			}
		case keyX:
			if j := m.selectedJob(); j != nil && !isTerminalStatus(j.Status) {
				if isRunningStatus(j.Status) {
					m.confirmKill = true
				} else {
					m.confirmDismiss = true
				}
			}
		}
	}
	return m, nil
}

// webhookURL returns the full public webhook URL, or empty if not a webhook pylon.
func (m detailModel) webhookURL() string {
	if m.pylon == nil || m.pylon.Trigger.Type != "webhook" || m.global == nil {
		return ""
	}
	return m.pylon.ResolvePublicURL(m.global)
}

func (m detailModel) View(width, height int) string {
	if m.alertBuilder {
		return m.renderAlertBuilder()
	}
	if m.err != nil {
		return statusFailed.Render(fmt.Sprintf("  Error: %v", m.err))
	}
	if m.pylon == nil {
		return mutedStyle.Render("  Loading...")
	}

	out := m.renderConfig(width)

	if m.showJobs {
		out += "\n" + m.renderJobs(width)
	}

	flash := m.copyFlash.View()
	if flash != "" {
		out += "\n  " + flash
	}
	return out
}

func (m detailModel) renderConfig(width int) string {
	pyl := m.pylon
	global := m.global

	s := ""
	s += "  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(pyl.Name) + "\n"
	if pyl.Description != "" {
		s += "  " + subtextStyle.Render(pyl.Description) + "\n"
	}
	s += "\n"

	row := func(label, value string) string {
		return fmt.Sprintf("  %s  %s\n",
			mutedStyle.Width(12).Render(label),
			lipgloss.NewStyle().Foreground(colorText).Render(value))
	}

	// Trigger
	trigger := pyl.Trigger.Type
	if pyl.Trigger.Path != "" {
		trigger += " " + pyl.Trigger.Path
	}
	if pyl.Trigger.Cron != "" {
		trigger += " " + pyl.Trigger.Cron
	}
	s += row("Trigger", trigger)

	// Webhook URL
	if pyl.Trigger.Type == "webhook" && global != nil {
		url := pyl.ResolvePublicURL(global)
		s += row("Webhook", url+"  "+mutedStyle.Render("[y] copy"))
	}

	// Workspace
	ws := pyl.Workspace.Type
	if pyl.Workspace.Repo != "" {
		ws += " " + pyl.Workspace.Repo
		if pyl.Workspace.Ref != "" {
			ws += " (" + pyl.Workspace.Ref + ")"
		}
	}
	if pyl.Workspace.Path != "" {
		ws += " " + pyl.Workspace.Path
	}
	s += row("Workspace", ws)

	// Agent
	if pyl.Agent != nil {
		agent := pyl.Agent.Type
		if agent == "" && global != nil {
			agent = global.Defaults.Agent.Type
		}
		if pyl.Agent.Auth != "" {
			agent += " (" + pyl.Agent.Auth + ")"
		}
		s += row("Agent", agent)
	}

	// Channel
	channelType := "default"
	if pyl.Channel != nil && pyl.Channel.Type != "" {
		channelType = pyl.Channel.Type
	}
	s += row("Channel", channelType)

	// Topic template
	if pyl.Channel != nil && pyl.Channel.Topic != "" {
		topic := strings.ReplaceAll(pyl.Channel.Topic, "\n", " ")
		maxLen := 36
		if len(topic) > maxLen {
			topic = topic[:maxLen-3] + "..."
		}
		s += row("Topic", topic)
	}

	// Message template
	if pyl.Channel != nil && pyl.Channel.Message != "" {
		msg := strings.ReplaceAll(pyl.Channel.Message, "\n", " ")
		maxLen := 36
		if len(msg) > maxLen {
			msg = msg[:maxLen-3] + "..."
		}
		s += row("Message", msg)
	}

	// Auto-run (inverted from Approval)
	if pyl.Channel != nil && pyl.Channel.Approval {
		s += row("Auto-run", "no")
	} else {
		s += row("Auto-run", "yes")
	}

	// Prompt
	if pyl.Agent != nil && pyl.Agent.Prompt != "" {
		if m.showFullPrompt {
			s += row("Prompt", mutedStyle.Render("[p] collapse"))
			s += "  " + subtextStyle.Render(pyl.Agent.Prompt) + "\n"
		} else {
			// Flatten to single line
			prompt := strings.ReplaceAll(pyl.Agent.Prompt, "\n", " ")
			maxLen := 24
			if len(prompt) > maxLen {
				prompt = prompt[:maxLen-3] + "..."
			}
			s += row("Prompt", prompt+"  "+mutedStyle.Render("[p] expand"))
		}
	}

	return s
}

func (m detailModel) renderJobs(width int) string {
	if len(m.jobs) == 0 {
		return mutedStyle.Render("  No jobs yet.")
	}

	colID := 10
	colStatus := 12
	colTriggered := 14

	header := tableHeaderStyle.Render(
		fmt.Sprintf("   %-*s  %-*s  %-*s  %s",
			colID, "ID",
			colStatus, "STATUS",
			colTriggered, "TRIGGERED",
			"DURATION"))

	var rows string
	for i, j := range m.jobs {
		id := j.ID
		if len(id) > 8 {
			id = id[:8]
		}

		status := renderJobStatus(j.Status)
		triggered := "-"
		if !j.CreatedAt.IsZero() {
			triggered = j.CreatedAt.Local().Format("Jan 2 15:04")
		}
		duration := "-"
		if j.CompletedAt != nil && !j.CompletedAt.IsZero() {
			start := j.CreatedAt
			if j.StartedAt != nil && !j.StartedAt.IsZero() {
				start = *j.StartedAt
			}
			if !start.IsZero() {
				d := j.CompletedAt.Sub(start)
				if d >= 0 {
					duration = formatDuration(d)
				}
			}
		}

		cursor := " "
		style := tableRowStyle
		if i == m.cursor && m.focused {
			cursor = cursorStyle.Render("◆")
			style = selectedRowStyle
		}

		statusPad := colStatus - lipgloss.Width(status)
		if statusPad < 0 {
			statusPad = 0
		}

		line := cursor + fmt.Sprintf(" %-*s  ", colID, id) +
			status + spaces(statusPad) +
			fmt.Sprintf("  %-*s  %s", colTriggered, triggered, duration)
		rows += style.Render(line) + "\n"
	}

	out := header + "\n" + rows

	if m.confirmKill {
		out += "\n  " + statusFailed.Render("Kill this job?") + " " + mutedStyle.Render("y/n")
	}
	if m.confirmDismiss {
		out += "\n  " + statusFailed.Render("Dismiss this job?") + " " + mutedStyle.Render("y/n")
	}

	return out
}

// jobStatusLabel returns the plain display text for a job status.
func jobStatusLabel(status string) string {
	switch status {
	case "active":
		return "running"
	case "awaiting_approval":
		return "approval"
	case "stale":
		return "stale"
	default:
		return status
	}
}

func renderJobStatus(status string) string {
	switch status {
	case "completed":
		return statusActive.Render("completed")
	case "failed", "timeout":
		return statusFailed.Render(status)
	case "dismissed":
		return mutedStyle.Render("dismissed")
	case "running", "active":
		return statusActive.Render("running")
	case "awaiting_approval":
		return lipgloss.NewStyle().Foreground(colorWarning).Render("approval")
	case "stale":
		return lipgloss.NewStyle().Foreground(colorWarning).Render("stale")
	default:
		return mutedStyle.Render(status)
	}
}

func formatDuration(d interface{ Seconds() float64 }) string {
	secs := d.Seconds()
	if secs < 60 {
		return fmt.Sprintf("%.0fs", secs)
	}
	mins := int(secs) / 60
	remSecs := int(secs) % 60
	if mins < 60 {
		return fmt.Sprintf("%dm%ds", mins, remSecs)
	}
	hours := mins / 60
	remMins := mins % 60
	return fmt.Sprintf("%dh%dm", hours, remMins)
}

// selectedJob returns the currently selected job, or nil.
func (m detailModel) selectedJob() *store.Job {
	if len(m.jobs) == 0 || m.cursor >= len(m.jobs) {
		return nil
	}
	return m.jobs[m.cursor]
}

func isRunningStatus(status string) bool {
	return status == "running" || status == "active"
}

func isTerminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "timeout" || status == "dismissed"
}

// jobKilledMsg is sent after a kill attempt.
type jobKilledMsg struct {
	err error
}

// containerFoundMsg carries the resolved container ID for a job.
type containerFoundMsg struct {
	containerID string
	action      string // "logs" or "kill"
	err         error
}

// findContainerCmd looks up the container ID for a job.
func findContainerCmd(jobID, action string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("docker", "ps", "-a", "--filter",
			fmt.Sprintf("label=pylon.job=%s", jobID), "--format", "{{.ID}}").Output()
		if err != nil {
			return containerFoundMsg{err: fmt.Errorf("docker not reachable: %w", err)}
		}
		containerID := strings.TrimSpace(string(out))
		if containerID == "" {
			id := jobID
			if len(id) > 8 {
				id = id[:8]
			}
			return containerFoundMsg{err: fmt.Errorf("container gone for job %s (status may be stale)", id)}
		}
		return containerFoundMsg{containerID: containerID, action: action}
	}
}

// killContainerCmd kills a container by ID.
func killContainerCmd(containerID string) tea.Cmd {
	return func() tea.Msg {
		c := exec.Command("docker", "kill", containerID)
		if err := c.Run(); err != nil {
			return jobKilledMsg{err: err}
		}
		return jobKilledMsg{}
	}
}

// jobDismissedMsg is sent after a dismiss attempt.
type jobDismissedMsg struct{ err error }

// dismissJobCmd marks a job as dismissed in the database.
func dismissJobCmd(pylonName, jobID string) tea.Cmd {
	return func() tea.Msg {
		s, err := store.Open(config.PylonDBPath(pylonName))
		if err != nil {
			return jobDismissedMsg{err: err}
		}
		defer s.Close()
		s.UpdateStatus(jobID, "dismissed")
		return jobDismissedMsg{}
	}
}

// loadPayloadPaths extracts flattened field paths from the most recent job's trigger payload.
func (m detailModel) loadPayloadPaths() ([]string, map[string]string) {
	dbPath := config.PylonDBPath(m.name)
	s, err := store.Open(dbPath)
	if err != nil {
		return nil, nil
	}
	defer s.Close()
	jobs, _ := s.RecentJobs(m.name, 1)
	if len(jobs) == 0 {
		return nil, nil
	}
	var payload string
	s.DB().QueryRow("SELECT trigger_payload FROM jobs WHERE id = ?", jobs[0].ID).Scan(&payload)
	if payload == "" {
		return nil, nil
	}
	var body map[string]interface{}
	if json.Unmarshal([]byte(payload), &body) != nil {
		return nil, nil
	}
	var paths []string
	values := make(map[string]string)
	flattenPaths(body, "", &paths, values)
	return paths, values
}

// flattenPaths recursively extracts dot-separated paths and sample values from a JSON object.
// Skips internal/noisy fields (underscore-prefixed), arrays, and deep nesting.
func flattenPaths(obj map[string]interface{}, prefix string, paths *[]string, values map[string]string) {
	for k, v := range obj {
		if strings.HasPrefix(k, "_") {
			continue
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]interface{}:
			if strings.Count(path, ".") < 3 {
				flattenPaths(val, path, paths, values)
			}
		case string:
			if val != "" && len(val) < 500 {
				*paths = append(*paths, path)
				values[path] = val
			}
		case float64:
			*paths = append(*paths, path)
			values[path] = fmt.Sprintf("%g", val)
		case bool:
			*paths = append(*paths, path)
			values[path] = fmt.Sprintf("%v", val)
		}
	}
	sort.Strings(*paths)
}

// renderAlertBuilder renders the template field picker with a scrolling window.
func (m detailModel) renderAlertBuilder() string {
	var b strings.Builder
	b.WriteString("  " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Message Template Builder") + "\n")
	b.WriteString("  " + subtextStyle.Render("Select fields to include in the message") + "\n\n")

	// Scrolling window: show ~15 items centered on cursor
	visible := 15
	start := m.alertCursor - visible/2
	if start < 0 {
		start = 0
	}
	end := start + visible
	if end > len(m.alertPaths) {
		end = len(m.alertPaths)
		start = end - visible
		if start < 0 {
			start = 0
		}
	}

	// Find max path width for alignment
	maxPath := 0
	for _, p := range m.alertPaths {
		w := len("{{ .body." + p + " }}")
		if w > maxPath {
			maxPath = w
		}
	}

	if start > 0 {
		b.WriteString("  " + mutedStyle.Render(fmt.Sprintf("  ... %d more above", start)) + "\n")
	} else {
		b.WriteString("\n")
	}

	for i := start; i < end; i++ {
		p := m.alertPaths[i]
		check := "[ ] "
		if m.alertChecked[i] {
			check = lipgloss.NewStyle().Foreground(colorAccent).Render("[x] ")
		}
		cursor := "  "
		style := lipgloss.NewStyle().Foreground(colorText)
		if i == m.alertCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("> ")
			style = style.Bold(true)
		}
		raw := "{{ .body." + p + " }}"
		field := style.Render(raw) + spaces(maxPath-len(raw))
		preview := ""
		if val, ok := m.alertValues[p]; ok {
			if len(val) > 40 {
				val = val[:37] + "..."
			}
			preview = "  " + mutedStyle.Render(val)
		}
		b.WriteString("  " + cursor + check + field + preview + "\n")
	}

	if end < len(m.alertPaths) {
		b.WriteString("  " + mutedStyle.Render(fmt.Sprintf("  ... %d more below", len(m.alertPaths)-end)) + "\n")
	} else {
		b.WriteString("\n")
	}

	// Preview
	var selected []string
	for i, p := range m.alertPaths {
		if m.alertChecked[i] {
			selected = append(selected, "{{ .body."+p+" }}")
		}
	}
	if len(selected) > 0 {
		b.WriteString("\n  " + mutedStyle.Render("Preview:") + "\n")
		for _, s := range selected {
			b.WriteString("  " + subtextStyle.Render(s) + "\n")
		}
	}

	b.WriteString("\n  " + mutedStyle.Render("space toggle  enter save  esc cancel"))
	return b.String()
}

// detailEditorDoneMsg is sent when the editor closes so we can reload the config.
type detailEditorDoneMsg struct {
	err error
}

// resolveEditor returns the best available editor.
func resolveEditor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	// Fallback chain: vi is POSIX-mandated
	return "vi"
}

func (m detailModel) openEditor() tea.Cmd {
	editor := resolveEditor()
	path := config.PylonPath(m.name)
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return detailEditorDoneMsg{err: err}
	})
}

func (m detailModel) footerBindings() []keyBinding {
	var bindings []keyBinding
	if m.pylon != nil && m.pylon.Trigger.Type == "webhook" {
		bindings = append(bindings, keyBinding{"y", "copy url"})
	}
	if m.pylon != nil && m.pylon.Agent != nil && m.pylon.Agent.Prompt != "" {
		if m.showFullPrompt {
			bindings = append(bindings, keyBinding{"p", "collapse prompt"})
		} else {
			bindings = append(bindings, keyBinding{"p", "expand prompt"})
		}
	}
	if m.showJobs {
		bindings = append(bindings, keyBinding{"t", "hide jobs"})
	} else {
		bindings = append(bindings, keyBinding{"t", "show jobs"})
	}
	bindings = append(bindings, keyBinding{"e", "edit"})
	bindings = append(bindings, keyBinding{"a", "message builder"})
	if m.showJobs {
		if j := m.selectedJob(); j != nil {
			if isRunningStatus(j.Status) {
				bindings = append(bindings, keyBinding{"l", "logs"})
				bindings = append(bindings, keyBinding{"x", "kill"})
			} else if !isTerminalStatus(j.Status) {
				bindings = append(bindings, keyBinding{"x", "dismiss"})
			}
		}
	}
	return bindings
}
