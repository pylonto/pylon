package tui

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pylonto/pylon/internal/config"
	"github.com/pylonto/pylon/internal/store"
)

// pylonRow holds display data for one pylon in the table.
type pylonRow struct {
	name        string
	trigger     string
	endpoint    string
	status      string
	lastJob     string
	description string
}

// homeModel is the dashboard view showing all pylons.
type homeModel struct {
	rows          []pylonRow
	cursor        int
	daemonRunning bool
	version       string
	glyph         pylonGlyph
	width, height int
	err           error
}

func newHomeModel(version string) homeModel {
	return homeModel{version: version}
}

// Init loads pylon data and checks daemon status.
func (m homeModel) Init() tea.Cmd {
	return tea.Batch(loadPylonsCmd(), checkDaemonCmd(), glyphTickCmd())
}

// pylonsLoadedMsg carries loaded pylon data.
type pylonsLoadedMsg struct {
	rows []pylonRow
	err  error
}

// daemonStatusMsg carries daemon health check result.
type daemonStatusMsg struct {
	running bool
}

// tickMsg triggers a periodic refresh.
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func loadPylonsCmd() tea.Cmd {
	return func() tea.Msg {
		names, err := config.ListPylons()
		if err != nil {
			return pylonsLoadedMsg{err: err}
		}

		var rows []pylonRow
		for _, name := range names {
			pyl, err := config.LoadPylon(name)
			if err != nil {
				rows = append(rows, pylonRow{name: name, status: "?"})
				continue
			}

			row := pylonRow{
				name:        name,
				trigger:     pyl.Trigger.Type,
				description: pyl.Description,
			}

			switch pyl.Trigger.Type {
			case "webhook":
				row.endpoint = pyl.Trigger.Path
			case "cron":
				row.endpoint = pyl.Trigger.Cron
			}

			// Load last job
			dbPath := config.PylonDBPath(name)
			if _, statErr := os.Stat(dbPath); statErr == nil {
				if s, openErr := store.Open(dbPath); openErr == nil {
					jobs, _ := s.RecentJobs(name, 1)
					if len(jobs) > 0 && !jobs[0].CreatedAt.IsZero() {
						row.lastJob = timeAgo(jobs[0].CreatedAt)
						if jobs[0].Status == "running" || jobs[0].Status == "active" {
							row.status = "active"
						}
					}
					_ = s.Close()
				}
			}

			if row.status == "" {
				row.status = "idle"
			}

			rows = append(rows, row)
		}
		return pylonsLoadedMsg{rows: rows}
	}
}

func checkDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		global, err := config.LoadGlobal()
		if err != nil {
			return daemonStatusMsg{running: false}
		}
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/callback/doctor-ping", global.Server.Port))
		if err != nil {
			return daemonStatusMsg{running: false}
		}
		resp.Body.Close()
		return daemonStatusMsg{running: resp.StatusCode == http.StatusMethodNotAllowed}
	}
}

func (m homeModel) Update(msg tea.Msg) (homeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pylonsLoadedMsg:
		m.rows = msg.rows
		m.err = msg.err
		return m, tickCmd()

	case daemonStatusMsg:
		m.daemonRunning = msg.running
		if !m.daemonRunning {
			for i := range m.rows {
				if m.rows[i].status != "?" {
					m.rows[i].status = "stopped"
				}
			}
		}
		return m, nil

	case tickMsg:
		return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())

	case glyphTickMsg:
		return m, m.glyph.Update(msg)

	case tea.KeyMsg:
		switch msg.String() {
		case keyUp, keyK:
			if m.cursor > 0 {
				m.cursor--
			}
		case keyDown, keyJ:
			if m.cursor < len(m.rows)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

// leftPanelWidth is the fixed width of the branding column.
const leftPanelWidth = 26

func (m homeModel) View(width, height int) string {
	if m.err != nil {
		return statusFailed.Render(fmt.Sprintf("Error: %v", m.err))
	}

	// Build left panel: title + art + stats
	left := m.renderLeftPanel(height)

	// Build right panel: table
	rightWidth := width - leftPanelWidth - 1
	if rightWidth < 30 {
		rightWidth = 30
	}
	right := m.renderTable(rightWidth)

	leftStyled := lipgloss.NewStyle().
		Width(leftPanelWidth).
		Render(left)

	rightStyled := lipgloss.NewStyle().
		Width(rightWidth).
		Render(right)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, rightStyled)
}

func (m homeModel) renderLeftPanel(height int) string {
	var b strings.Builder

	// Line 1: spinner + title + version
	spinner := m.glyph.View()
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Pylon Nexus")
	ver := lipgloss.NewStyle().Foreground(colorGold).Render(m.version)
	b.WriteString("  " + spinner + " " + title + " " + ver + "\n")

	// Line 2: pylon count
	pylonCount := len(m.rows)
	pylonLabel := "pylons"
	if pylonCount == 1 {
		pylonLabel = "pylon"
	}
	countStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true)
	b.WriteString("    " + countStyle.Render(fmt.Sprintf("%d", pylonCount)) + " " + subtextStyle.Render(pylonLabel) + "\n")

	// Line 3: daemon status (+ active agents if any)
	daemonStatus := mutedStyle.Render("stopped")
	if m.daemonRunning {
		daemonStatus = statusActive.Render("running")
	}

	active := 0
	for _, r := range m.rows {
		if r.status == "active" {
			active++
		}
	}

	statusLine := "    " + daemonStatus
	if active > 0 {
		agentLabel := "agent"
		if active > 1 {
			agentLabel = "agents"
		}
		statusLine += mutedStyle.Render(" / ") + statusActive.Render(fmt.Sprintf("%d", active)) + " " + subtextStyle.Render(agentLabel)
	}
	b.WriteString(statusLine + "\n")

	return b.String()
}

func (m homeModel) renderTable(width int) string {
	if len(m.rows) == 0 {
		msg := mutedStyle.Render("No pylons constructed yet.\n\n") +
			subtextStyle.Render("Press ") + keyStyle.Render("c") + subtextStyle.Render(" to construct,\n") +
			subtextStyle.Render("or ") + keyStyle.Render("s") + subtextStyle.Render(" to run setup.")
		return "\n" + msg + "\n"
	}

	// Column widths -- adapt to available space
	colName := 18
	colTrigger := 9
	colStatus := 9
	colLastJob := 10

	// Give remaining space to name
	remaining := width - colTrigger - colStatus - colLastJob - 8
	if remaining > colName {
		colName = remaining
	}

	// Header
	header := tableHeaderStyle.Render(
		fmt.Sprintf(" %-*s  %-*s  %-*s  %s",
			colName, "NAME",
			colTrigger, "TRIGGER",
			colStatus, "STATUS",
			"LAST JOB"))

	// Rows
	var rows string
	for i, r := range m.rows {
		name := r.name
		if len(name) > colName {
			name = name[:colName-1] + "~"
		}

		if i == m.cursor {
			// Selected row: plain text so the highlight is uniform (no inner ANSI fighting it)
			line := fmt.Sprintf(" %-*s  %-*s  %-*s  %s",
				colName, name,
				colTrigger, r.trigger,
				colStatus, r.status,
				r.lastJob)
			rows += selectedRowStyle.Width(width).Render(line) + "\n"
		} else {
			// Normal row: styled status
			status := renderStatus(r.status)
			statusPad := colStatus - lipgloss.Width(status)
			if statusPad < 0 {
				statusPad = 0
			}
			line := fmt.Sprintf(" %-*s  %-*s  ",
				colName, name,
				colTrigger, r.trigger) +
				status + spaces(statusPad) + "  " + r.lastJob
			rows += tableRowStyle.Render(line) + "\n"
		}
	}

	return header + "\n" + rows
}

// selectedPylon returns the name of the currently selected pylon.
func (m homeModel) selectedPylon() string {
	if len(m.rows) == 0 || m.cursor >= len(m.rows) {
		return ""
	}
	return m.rows[m.cursor].name
}

func renderStatus(status string) string {
	switch status {
	case "active":
		return statusActive.Render("active")
	case "idle":
		return statusIdle.Render("idle")
	case "stopped":
		return statusStopped.Render("stopped")
	case "failed":
		return statusFailed.Render("failed")
	default:
		return mutedStyle.Render(status)
	}
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// footerBindings returns the keybind hints for the home view.
func (m homeModel) footerBindings() []keyBinding {
	bindings := []keyBinding{
		{"s", "setup"},
		{"c", "construct"},
	}
	if len(m.rows) > 0 {
		bindings = append(bindings, keyBinding{"enter", "detail"})
	}
	bindings = append(bindings,
		keyBinding{"d", "doctor"},
		keyBinding{"q", "quit"},
	)
	return bindings
}
