package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type viewID int

const (
	viewHome viewID = iota
	viewDetail
	viewSetup
	viewConstruct
)

// Navigation messages emitted by child views.
type (
	navigateMsg      struct{ target viewID }
	navigateBackMsg  struct{}
	pylonSelectedMsg struct{ name string }
)

// AppModel is the top-level bubbletea model.
type AppModel struct {
	version string

	activeView viewID
	viewStack  []viewID

	home   homeModel
	detail detailModel
	wizard wizardModel
	glyph  pylonGlyph

	width, height int
}

// NewApp creates the top-level TUI model.
func NewApp(version string) AppModel {
	return AppModel{
		version:    version,
		activeView: viewHome,
		home:       newHomeModel(),
	}
}

func (m AppModel) Init() tea.Cmd {
	return tea.Batch(m.home.Init(), glyphTickCmd())
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case glyphTickMsg:
		return m, m.glyph.Update(msg)

	case wizardCompleteMsg:
		m.popView()
		return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())

	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}

		// Global navigation from home
		if m.activeView == viewHome {
			switch msg.String() {
			case keyQ:
				return m, tea.Quit
			case keyEnter, "l":
				name := m.home.selectedPylon()
				if name != "" {
					m.detail = newDetailModel(name)
					m.pushView(viewDetail)
					return m, m.detail.Init()
				}
			case keyS:
				m.wizard = newSetupWizard()
				m.pushView(viewSetup)
				return m, m.wizard.Init()
			case keyC:
				m.wizard = newConstructWizard("")
				m.pushView(viewConstruct)
				return m, m.wizard.Init()
			}
		}

		// Back navigation (Esc/h from detail, Esc from wizard)
		if m.activeView != viewHome {
			if msg.String() == keyEsc {
				m.popView()
				if m.activeView == viewHome {
					return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())
				}
				return m, nil
			}
			if m.activeView == viewDetail && (msg.String() == keyQ || msg.String() == "h") {
				m.popView()
				return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())
			}
		}
	}

	// Delegate to active view
	var cmd tea.Cmd
	switch m.activeView {
	case viewHome:
		m.home, cmd = m.home.Update(msg)
	case viewDetail:
		m.detail, cmd = m.detail.Update(msg)
	case viewSetup, viewConstruct:
		m.wizard, cmd = m.wizard.Update(msg)
	}

	return m, cmd
}

func (m AppModel) View() string {
	if m.width < 60 {
		return "\n  Terminal too narrow. Please resize to at least 60 columns.\n"
	}

	contentHeight := m.height - 2 // reserve for footer

	// Left panel is always visible
	left := m.renderLeftPanel()
	leftStyled := lipgloss.NewStyle().
		Width(leftPanelWidth).
		Render(left)

	// Separator + right panel
	sep := m.renderSeparator(contentHeight)
	rightWidth := m.width - leftPanelWidth - 1 // 1 for separator
	if rightWidth < 30 {
		rightWidth = 30
	}

	var rightContent string
	var footer string

	switch m.activeView {
	case viewHome:
		rightContent = m.home.View(rightWidth, contentHeight)
		footer = renderFooter(m.home.footerBindings(), m.width)
	case viewDetail:
		rightContent = m.detail.View(rightWidth, contentHeight)
		footer = renderFooter(m.detail.footerBindings(), m.width)
	case viewSetup, viewConstruct:
		rightContent = m.wizard.View(rightWidth, contentHeight)
		footer = renderFooter(m.wizard.footerBindings(), m.width)
	default:
		rightContent = fmt.Sprintf("  View %d not yet implemented.", m.activeView)
	}

	rightStyled := lipgloss.NewStyle().
		Width(rightWidth).
		Render(rightContent)

	rendered := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, sep, rightStyled)

	// Pad to fill screen
	lines := countLines(rendered)
	for lines < m.height-1 {
		rendered += "\n"
		lines++
	}

	return rendered + footer
}

// renderLeftPanel renders the persistent branding sidebar.
func (m AppModel) renderLeftPanel() string {
	var b strings.Builder

	// Line 1: spinner + title + version
	spinner := m.glyph.View()
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Pylon Nexus")
	ver := lipgloss.NewStyle().Foreground(colorGold).Render(m.version)
	b.WriteString("  " + spinner + " " + title + " " + ver + "\n")

	// Line 2: daemon status
	if m.home.daemonRunning {
		b.WriteString("    " + statusActive.Render("Daemon ON") + "\n")
	} else {
		b.WriteString("    " + mutedStyle.Render("Daemon OFF") + "\n")
	}

	// Line 3: pylon/service count + active agents
	pylonCount := len(m.home.rows)
	countStyle := lipgloss.NewStyle().Foreground(colorText).Bold(true)
	pylonLabel := "services"
	if pylonCount == 1 {
		pylonLabel = "service"
	}
	info := "    " + countStyle.Render(fmt.Sprintf("%d", pylonCount)) + " " + subtextStyle.Render(pylonLabel)

	active := 0
	for _, r := range m.home.rows {
		if r.status == "active" {
			active++
		}
	}
	if active > 0 {
		agentLabel := "active"
		info += mutedStyle.Render(", ") + statusActive.Render(fmt.Sprintf("%d", active)) + " " + subtextStyle.Render(agentLabel)
	}
	b.WriteString(info + "\n")

	return b.String()
}

// renderSeparator renders a vertical gold bar spanning the given height.
func (m AppModel) renderSeparator(height int) string {
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

func (m *AppModel) pushView(v viewID) {
	m.viewStack = append(m.viewStack, m.activeView)
	m.activeView = v
}

func (m *AppModel) popView() {
	if len(m.viewStack) > 0 {
		m.activeView = m.viewStack[len(m.viewStack)-1]
		m.viewStack = m.viewStack[:len(m.viewStack)-1]
	}
}

func countLines(s string) int {
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}
