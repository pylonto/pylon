package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
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
	navigateMsg     struct{ target viewID }
	navigateBackMsg struct{}
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

	width, height int
}

// NewApp creates the top-level TUI model.
func NewApp(version string) AppModel {
	return AppModel{
		version:    version,
		activeView: viewHome,
		home:       newHomeModel(version),
	}
}

func (m AppModel) Init() tea.Cmd {
	return m.home.Init()
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

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
			case keyEnter:
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

		// Back navigation (Esc from wizard or detail)
		if m.activeView != viewHome {
			if msg.String() == keyEsc {
				m.popView()
				if m.activeView == viewHome {
					return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())
				}
				return m, nil
			}
			// Only allow q to quit from home, not from wizards
			if m.activeView == viewDetail && msg.String() == keyQ {
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

	contentHeight := m.height - 2 // reserve for footer + padding

	var content string
	var footer string

	switch m.activeView {
	case viewHome:
		// Home view renders its own title/branding in the left panel
		content = m.home.View(m.width, contentHeight)
		footer = renderFooter(m.home.footerBindings(), m.width)
	case viewDetail:
		title := renderTitle("PYLON NEXUS", m.version, m.width)
		content = title + "\n\n" + m.detail.View(m.width, contentHeight-3)
		footer = renderFooter(m.detail.footerBindings(), m.width)
	case viewSetup, viewConstruct:
		title := renderTitle("PYLON NEXUS", m.version, m.width)
		content = title + "\n\n" + m.wizard.View(m.width, contentHeight-3)
		footer = renderFooter(m.wizard.footerBindings(), m.width)
	default:
		content = fmt.Sprintf("  View %d not yet implemented.", m.activeView)
	}

	// Pad content to fill available space
	rendered := content
	lines := countLines(rendered)
	for lines < m.height-1 {
		rendered += "\n"
		lines++
	}

	return rendered + footer
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
