package tui

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/atotto/clipboard"
	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

// copiedMsg is sent after a clipboard copy attempt.
type copiedMsg struct {
	label string
}

// copyToClipboard returns a tea.Cmd that copies text to clipboard via OSC52 + native.
func copyToClipboard(text, label string) tea.Cmd {
	return func() tea.Msg {
		// OSC52 -- works over SSH if terminal supports it
		osc52.New(text).WriteTo(os.Stderr)
		// Native clipboard -- works locally, fails silently over SSH
		_ = clipboard.WriteAll(text)
		return copiedMsg{label: label}
	}
}

// copyFlashModel manages a brief "Copied!" notification.
type copyFlashModel struct {
	label   string
	visible bool
}

type copyFlashClearMsg struct{}

func (m copyFlashModel) show(label string) (copyFlashModel, tea.Cmd) {
	m.label = label
	m.visible = true
	return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return copyFlashClearMsg{}
	})
}

func (m copyFlashModel) Update(msg tea.Msg) copyFlashModel {
	if _, ok := msg.(copyFlashClearMsg); ok {
		m.visible = false
	}
	return m
}

func (m copyFlashModel) View() string {
	if !m.visible {
		return ""
	}
	style := lipgloss.NewStyle().
		Foreground(colorSuccess).
		Bold(true)
	return style.Render(m.label)
}
