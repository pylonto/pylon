package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
)

// detailModel shows a single pylon's config and recent jobs.
type detailModel struct {
	name      string
	pylon     *config.PylonConfig
	global    *config.GlobalConfig
	jobs      []*store.Job
	cursor    int
	copyFlash copyFlashModel
	err       error
}

func newDetailModel(name string) detailModel {
	return detailModel{name: name}
}

type detailLoadedMsg struct {
	pylon  *config.PylonConfig
	global *config.GlobalConfig
	jobs   []*store.Job
	err    error
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

		return detailLoadedMsg{pylon: pyl, global: global, jobs: jobs}
	}
}

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case detailLoadedMsg:
		m.pylon = msg.pylon
		m.global = msg.global
		m.jobs = msg.jobs
		m.err = msg.err

	case copiedMsg:
		var cmd tea.Cmd
		m.copyFlash, cmd = m.copyFlash.show(msg.label)
		return m, cmd

	case copyFlashClearMsg:
		m.copyFlash = m.copyFlash.Update(msg)

	case tea.KeyMsg:
		switch msg.String() {
		case keyUp, keyK:
			if m.cursor > 0 {
				m.cursor--
			}
		case keyDown, keyJ:
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case keyY:
			if url := m.webhookURL(); url != "" {
				return m, copyToClipboard(url, "webhook URL")
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
	if m.err != nil {
		return statusFailed.Render(fmt.Sprintf("  Error: %v", m.err))
	}
	if m.pylon == nil {
		return mutedStyle.Render("  Loading...")
	}

	// Determine layout: side-by-side or stacked
	sideBySide := width >= 100

	configPanel := m.renderConfig(width, sideBySide)
	jobsPanel := m.renderJobs(width, sideBySide)

	flash := m.copyFlash.View()

	if sideBySide {
		leftWidth := width/2 - 1
		rightWidth := width - leftWidth - 1

		left := lipgloss.NewStyle().Width(leftWidth).Render(configPanel)
		right := lipgloss.NewStyle().Width(rightWidth).Render(jobsPanel)

		out := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
		if flash != "" {
			out += "\n  " + flash
		}
		return out
	}

	out := configPanel + "\n\n" + jobsPanel
	if flash != "" {
		out += "\n  " + flash
	}
	return out
}

func (m detailModel) renderConfig(width int, sideBySide bool) string {
	pyl := m.pylon
	global := m.global

	s := ""
	s += lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(pyl.Name) + "\n"
	if pyl.Description != "" {
		s += subtextStyle.Render(pyl.Description) + "\n"
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

	// Notifier
	notifyType := "default"
	if pyl.Notify != nil && pyl.Notify.Type != "" {
		notifyType = pyl.Notify.Type
	}
	s += row("Notifier", notifyType)

	// Approval
	if pyl.Notify != nil && pyl.Notify.Approval {
		s += row("Approval", "yes")
	}

	// Prompt (truncated)
	if pyl.Agent != nil && pyl.Agent.Prompt != "" {
		prompt := pyl.Agent.Prompt
		maxLen := 60
		if sideBySide {
			maxLen = width/2 - 16
		}
		if len(prompt) > maxLen {
			prompt = prompt[:maxLen-3] + "..."
		}
		s += row("Prompt", prompt)
	}

	return s
}

func (m detailModel) renderJobs(width int, sideBySide bool) string {
	if len(m.jobs) == 0 {
		return mutedStyle.Render("  No jobs yet.")
	}

	colID := 10
	colStatus := 12
	colTriggered := 14

	header := tableHeaderStyle.Render(
		fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
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
			triggered = timeAgo(j.CreatedAt)
		}
		duration := "-"
		if j.CompletedAt != nil {
			start := j.CreatedAt
			if j.StartedAt != nil {
				start = *j.StartedAt
			}
			d := j.CompletedAt.Sub(start)
			duration = formatDuration(d)
		}

		if i == m.cursor {
			line := fmt.Sprintf("  %-*s  %-*s  %-*s  %s",
				colID, id,
				colStatus, j.Status,
				colTriggered, triggered,
				duration)
			rows += selectedRowStyle.Width(width).Render(line) + "\n"
		} else {
			statusPad := colStatus - lipgloss.Width(status)
			if statusPad < 0 {
				statusPad = 0
			}
			line := fmt.Sprintf("  %-*s  ", colID, id) +
				status + spaces(statusPad) +
				fmt.Sprintf("  %-*s  %s", colTriggered, triggered, duration)
			rows += tableRowStyle.Render(line) + "\n"
		}
	}

	return header + "\n" + rows
}

func renderJobStatus(status string) string {
	switch status {
	case "completed":
		return statusActive.Render("completed")
	case "failed", "timeout":
		return statusFailed.Render(status)
	case "running", "active":
		return statusActive.Render("running")
	case "awaiting_approval":
		return lipgloss.NewStyle().Foreground(colorWarning).Render("approval")
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

func (m detailModel) footerBindings() []keyBinding {
	bindings := []keyBinding{
		{"esc", "back"},
	}
	if m.pylon != nil && m.pylon.Trigger.Type == "webhook" {
		bindings = append(bindings, keyBinding{"y", "copy url"})
	}
	bindings = append(bindings, keyBinding{"e", "edit"})
	return bindings
}
