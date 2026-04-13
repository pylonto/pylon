package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type viewID int

const (
	viewHome viewID = iota
	viewSetup
	viewConstruct
)

// Navigation messages emitted by child views.
type (
	navigateMsg      struct{ target viewID }
	navigateBackMsg  struct{}
	pylonSelectedMsg struct{ name string }
)

// daemonStartedMsg is sent after attempting to start/stop the daemon.
type daemonStartedMsg struct{ err error }

// recheckDaemonMsg triggers another health check while waiting for daemon state change.
type recheckDaemonMsg struct{}

// leftPanelWidth is the fixed width of the branding column.
const leftPanelWidth = 24

// AppModel is the top-level bubbletea model.
type AppModel struct {
	version       string
	latestVersion string // set after checking GitHub

	activeView viewID
	viewStack  []viewID

	home           homeModel
	wizard         wizardModel
	glyph          pylonGlyph
	daemonStarting bool
	confirmStop    bool

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
	return tea.Batch(m.home.Init(), glyphTickCmd(), shimmerTickCmd(), checkUpdateCmd(m.version))
}

// updateAvailableMsg carries the latest version from GitHub.
type updateAvailableMsg struct{ version string }

// checkUpdateCmd checks GitHub for a newer release.
func checkUpdateCmd(current string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("https://api.github.com/repos/pylonto/pylon/releases/latest")
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil
		}
		var r struct {
			TagName string `json:"tag_name"`
		}
		if json.NewDecoder(resp.Body).Decode(&r) != nil || r.TagName == "" {
			return nil
		}
		latest := strings.TrimPrefix(r.TagName, "v")
		cur := strings.TrimPrefix(current, "v")
		if latest != cur && cur != "dev" {
			return updateAvailableMsg{version: r.TagName}
		}
		return nil
	}
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case updateAvailableMsg:
		m.latestVersion = msg.version
		return m, nil

	case glyphTickMsg:
		return m, m.glyph.Update(msg)

	case shimmerTickMsg:
		return m, m.glyph.Update(msg)

	case wizardCompleteMsg:
		m.popView()
		return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())

	case daemonStartedMsg:
		if msg.err != nil {
			m.daemonStarting = false
			m.home.err = msg.err
			return m, checkDaemonCmd()
		}
		// Keep daemonStarting=true until health check confirms state change
		return m, checkDaemonCmd()

	case upgradeDoneMsg:
		if m.latestVersion != "" {
			m.version = m.latestVersion
		}
		m.latestVersion = ""
		return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())

	case doctorDoneMsg:
		return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())

	case daemonStatusMsg:
		if m.daemonStarting {
			if msg.running != m.home.daemonRunning {
				// State changed to what we expected -- done starting/stopping
				m.daemonStarting = false
			} else {
				// Not ready yet -- poll again shortly
				return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
					return recheckDaemonMsg{}
				})
			}
		}

	case recheckDaemonMsg:
		if m.daemonStarting {
			return m, checkDaemonCmd()
		}

	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}

		if m.activeView == viewHome {
			// Stop daemon confirmation intercepts all keys
			if m.confirmStop {
				switch msg.String() {
				case "y":
					m.confirmStop = false
					m.daemonStarting = true
					return m, stopDaemonCmd()
				default:
					m.confirmStop = false
				}
				return m, nil
			}

			switch msg.String() {
			case keyQ:
				return m, tea.Quit
			case keyG:
				m.wizard = newSetupWizard()
				m.pushView(viewSetup)
				return m, m.wizard.Init()
			case keyC:
				m.wizard = newConstructWizard("")
				m.pushView(viewConstruct)
				return m, m.wizard.Init()
			case keyD:
				if m.daemonStarting {
					break
				}
				if m.home.daemonRunning {
					m.confirmStop = true
					return m, nil
				}
				m.daemonStarting = true
				return m, startDaemonCmd()
			case keyQuestion:
				return m, runDoctorCmd()
			case keyU:
				if m.latestVersion != "" {
					return m, runUpgradeCmd()
				}
			}
		}

		// Cancel confirmation intercepts all keys when active
		if m.activeView != viewHome && m.wizard.confirmCancel {
			switch msg.String() {
			case keyY, keyEsc:
				m.wizard.confirmCancel = false
				m.popView()
				if m.activeView == viewHome {
					return m, tea.Batch(loadPylonsCmd(), checkDaemonCmd())
				}
				return m, nil
			default:
				m.wizard.confirmCancel = false
				return m, nil
			}
		}

		// Back from wizard -- first press shows confirmation
		if m.activeView != viewHome {
			if msg.String() == keyEsc {
				m.wizard.confirmCancel = true
				return m, nil
			}
		}
	}

	// Delegate to active view
	var cmd tea.Cmd
	switch m.activeView {
	case viewHome:
		m.home, cmd = m.home.Update(msg)
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

	// Left branding panel -- always visible
	left := m.renderLeftPanel()
	leftStyled := lipgloss.NewStyle().
		Width(leftPanelWidth).
		Render(left)

	sep := m.renderSeparator(contentHeight)

	rightWidth := m.width - leftPanelWidth - 1
	if rightWidth < 30 {
		rightWidth = 30
	}

	var rightContent string
	var footer string

	switch m.activeView {
	case viewHome:
		rightContent = m.home.View(rightWidth, contentHeight)
		bindings := m.home.footerBindings()
		if m.latestVersion != "" {
			bindings = append(bindings, keyBinding{"u", "upgrade"})
		}
		footer = renderFooter(bindings, m.width)
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

	spinner := m.glyph.View()
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Pylon Nexus")
	b.WriteString(" " + spinner + " " + title + "\n")
	displayVer := m.version
	if !strings.HasPrefix(displayVer, "v") {
		displayVer = "v" + displayVer
	}
	if m.latestVersion != "" {
		displayLatest := m.latestVersion
		if !strings.HasPrefix(displayLatest, "v") {
			displayLatest = "v" + displayLatest
		}
		ver := lipgloss.NewStyle().Foreground(colorGold).Render(displayVer + " > ")
		b.WriteString("   " + ver + renderShimmer(displayLatest, m.glyph.shimmerTick) + "\n")
	} else {
		ver := lipgloss.NewStyle().Foreground(colorGold).Render(displayVer)
		b.WriteString("   " + ver + "\n")
	}

	// Daemon status
	if m.confirmStop {
		b.WriteString("   " + statusFailed.Render("Stop daemon?") + " " + mutedStyle.Render("y/n") + "\n")
	} else if m.daemonStarting {
		greenSpinner := lipgloss.NewStyle().Foreground(colorSuccess).Render(glyphFrames[m.glyph.frame])
		b.WriteString("   " + greenSpinner + " " + subtextStyle.Render("Starting...") + "\n")
	} else if m.home.daemonRunning {
		b.WriteString("   " + statusActive.Render("Daemon ON") + "\n")
	} else {
		b.WriteString("   " + mutedStyle.Render("Daemon OFF") + "\n")
	}

	b.WriteString("\n")

	// Pylon list
	if len(m.home.rows) == 0 {
		b.WriteString("   " + mutedStyle.Render("No pylons") + "\n")
	} else {
		for i, r := range m.home.rows {
			name := r.name
			maxLen := leftPanelWidth - 7 // cursor + space + name + padding + space + dot
			if len(name) > maxLen {
				name = name[:maxLen-1] + "~"
			}

			// Status dot: filled when enabled, outlined when disabled
			var dot string
			if r.disabled {
				dot = mutedStyle.Render("◇")
			} else if r.status == "active" {
				dot = statusActive.Render("◆")
			} else {
				dot = subtextStyle.Render("◆")
			}

			cursor := " "
			style := tableRowStyle
			if i == m.home.cursor {
				if m.home.focus == focusList {
					cursor = cursorStyle.Render("◆")
				} else {
					cursor = cursorStyle.Render("◇")
				}
				if !r.disabled {
					style = selectedRowStyle
				}
			}

			// Align: cursor at col 1 (same as glyph), name at col 3 (same as info text)
			padded := fmt.Sprintf("%-*s", maxLen, name)
			b.WriteString(" " + cursor + " " + style.Render(padded) + " " + dot + "\n")
		}
		if m.home.confirmDelete {
			b.WriteString("\n   " + statusFailed.Render("Delete pylon?") + " " + mutedStyle.Render("y/n") + "\n")
		}
	}

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

// startDaemonCmd installs the systemd user service if needed and starts it.
func startDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		u, _ := user.Current()
		userDir := filepath.Join(u.HomeDir, ".config", "systemd", "user")
		unitPath := filepath.Join(userDir, "pylon.service")

		// Install if not present
		if _, err := os.Stat(unitPath); os.IsNotExist(err) {
			binPath, _ := exec.LookPath("pylon")
			if binPath == "" {
				binPath = "/usr/local/bin/pylon"
			}
			unit := fmt.Sprintf("[Unit]\nDescription=Pylon Agent Daemon\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=%s start\nRestart=on-failure\nRestartSec=5\nEnvironment=HOME=%s\n\n[Install]\nWantedBy=default.target\n",
				binPath, u.HomeDir)
			os.MkdirAll(userDir, 0755)
			if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
				return daemonStartedMsg{err: err}
			}
			exec.Command("systemctl", "--user", "daemon-reload").Run()
			exec.Command("systemctl", "--user", "enable", "pylon").Run()
		}

		// Start
		if err := exec.Command("systemctl", "--user", "start", "pylon").Run(); err != nil {
			return daemonStartedMsg{err: err}
		}
		return daemonStartedMsg{}
	}
}

// stopDaemonCmd stops the systemd user service.
func stopDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		if err := exec.Command("systemctl", "--user", "stop", "pylon").Run(); err != nil {
			return daemonStartedMsg{err: err}
		}
		return daemonStartedMsg{}
	}
}

// upgradeDoneMsg is sent when the upgrade subprocess exits.
type upgradeDoneMsg struct{ err error }

// runUpgradeCmd launches pylon upgrade as a subprocess.
func runUpgradeCmd() tea.Cmd {
	c := exec.Command("sh", "-c", os.Args[0]+" upgrade; printf '\\nPress enter to return to nexus...'; read _")
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return upgradeDoneMsg{err: err}
	})
}

// doctorDoneMsg is sent when the doctor subprocess exits.
type doctorDoneMsg struct{ err error }

// runDoctorCmd launches pylon doctor as a subprocess, pausing before returning.
func runDoctorCmd() tea.Cmd {
	c := exec.Command("sh", "-c", os.Args[0]+" doctor; printf '\\nPress enter to return to nexus...'; read _")
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return doctorDoneMsg{err: err}
	})
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
