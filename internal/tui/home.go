package tui

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

// focusArea tracks which pane has keyboard focus.
type focusArea int

const (
	focusList   focusArea = iota // pylon sidebar
	focusDetail                  // detail pane
)

// homeModel is the dashboard view showing all pylons.
type homeModel struct {
	rows          []pylonRow
	cursor        int
	daemonRunning bool
	focus         focusArea
	detail        detailModel
	detailLoaded  bool // true once the first pylon detail has been loaded
	width, height int
	err           error
}

func newHomeModel() homeModel {
	return homeModel{}
}

// Init loads pylon data and checks daemon status.
func (m homeModel) Init() tea.Cmd {
	return tea.Batch(loadPylonsCmd(), checkDaemonCmd())
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
		// Auto-load detail for the first pylon if we haven't yet
		if !m.detailLoaded && len(m.rows) > 0 {
			m.detailLoaded = true
			return m, tea.Batch(tickCmd(), m.loadDetailForCursor())
		}
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

	case pylonEditDoneMsg:
		// Reload after editing
		return m, tea.Batch(loadPylonsCmd(), m.loadDetailForCursor())
	}

	// Non-key messages (detailLoadedMsg, containerFoundMsg, etc.)
	// always go to the detail model regardless of focus.
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	}

	// When detail pane has focus, delegate key presses there
	if m.focus == focusDetail {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "h", keyEsc:
				m.focus = focusList
				m.detail.focused = false
				return m, nil
			default:
				var cmd tea.Cmd
				m.detail, cmd = m.detail.Update(msg)
				return m, cmd
			}
		}
		return m, nil
	}

	// List navigation (focusList)
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case keyUp, keyK:
			if m.cursor > 0 {
				m.cursor--
				return m, m.loadDetailForCursor()
			}
		case keyDown, keyJ:
			if m.cursor < len(m.rows)-1 {
				m.cursor++
				return m, m.loadDetailForCursor()
			}
		case keyEnter, "l":
			if len(m.rows) > 0 {
				m.focus = focusDetail
				m.detail.focused = true
				return m, nil
			}
		case keyE:
			name := m.selectedPylon()
			if name != "" {
				return m, m.openEditorForPylon(name)
			}
		}
	}

	return m, nil
}

// pylonEditDoneMsg is sent when the editor closes.
type pylonEditDoneMsg struct{ err error }

// openEditorForPylon opens the pylon config in $EDITOR.
func (m *homeModel) openEditorForPylon(name string) tea.Cmd {
	editor := resolveEditor()
	path := config.PylonPath(name)
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return pylonEditDoneMsg{err: err}
	})
}

// loadDetailForCursor loads the detail for the currently selected pylon.
func (m *homeModel) loadDetailForCursor() tea.Cmd {
	name := m.selectedPylon()
	if name == "" {
		return nil
	}
	m.detail = newDetailModel(name)
	return m.detail.Init()
}

// sidebarWidth is the width of the pylon name sidebar.
const sidebarWidth = 16

func (m homeModel) View(width, height int) string {
	if m.err != nil {
		return statusFailed.Render(fmt.Sprintf("Error: %v", m.err))
	}

	if len(m.rows) == 0 {
		msg := mutedStyle.Render("No pylons constructed yet.\n\n") +
			subtextStyle.Render("Press ") + keyStyle.Render("c") + subtextStyle.Render(" to construct,\n") +
			subtextStyle.Render("or ") + keyStyle.Render("s") + subtextStyle.Render(" to run setup.")
		return "\n" + msg + "\n"
	}

	// Sidebar (pylon names)
	sidebar := m.renderSidebar(height)
	sidebarStyled := lipgloss.NewStyle().
		Width(sidebarWidth).
		Render(sidebar)

	// Separator
	sep := renderGoldSeparator(height)

	// Detail pane
	detailWidth := width - sidebarWidth - 1
	if detailWidth < 30 {
		detailWidth = 30
	}
	detailContent := m.detail.View(detailWidth, height)
	detailStyled := lipgloss.NewStyle().
		Width(detailWidth).
		Render(detailContent)

	return lipgloss.JoinHorizontal(lipgloss.Top, sidebarStyled, sep, detailStyled)
}

// renderSidebar renders the pylon name list.
func (m homeModel) renderSidebar(height int) string {
	var b strings.Builder

	b.WriteString(tableHeaderStyle.Render(" Pylons") + "\n")

	for i, r := range m.rows {
		name := r.name
		maxLen := sidebarWidth - 3
		if len(name) > maxLen {
			name = name[:maxLen-1] + "~"
		}

		cursor := " "
		style := tableRowStyle
		if i == m.cursor {
			if m.focus == focusList {
				cursor = cursorStyle.Render("◆")
			} else {
				cursor = cursorStyle.Render("◇")
			}
			style = selectedRowStyle
		}

		b.WriteString(style.Render(cursor+" "+name) + "\n")
	}

	return b.String()
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

	if m.focus == focusDetail {
		bindings = append(bindings, keyBinding{"h", "back"})
		bindings = append(bindings, m.detail.footerBindings()...)
	} else if len(m.rows) > 0 {
		bindings = append(bindings, keyBinding{"l", "detail"})
		bindings = append(bindings, keyBinding{"e", "edit"})
	}

	bindings = append(bindings, keyBinding{"q", "quit"})
	return bindings
}

// renderGoldSeparator renders a vertical gold bar.
func renderGoldSeparator(height int) string {
	style := lipgloss.NewStyle().Foreground(colorGoldDim)
	var b strings.Builder
	for i := 0; i < height; i++ {
		b.WriteString(style.Render("│"))
		if i < height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
