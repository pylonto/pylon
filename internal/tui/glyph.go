package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pylonGlyph is a simple cycling spinner using crystal/energy unicode characters.
type pylonGlyph struct {
	frame int
}

type glyphTickMsg struct{}

const glyphInterval = 240 * time.Millisecond

// Crystal-themed spinner frames: diamond forms and pulses.
var glyphFrames = []string{
	"◇", // open diamond
	"◈", // diamond with dot -- filling
	"◆", // solid diamond -- full
	"◈", // diamond with dot -- fading
	"◇", // open diamond
	"·", // dot -- pause
}

func glyphTickCmd() tea.Cmd {
	return tea.Tick(glyphInterval, func(time.Time) tea.Msg {
		return glyphTickMsg{}
	})
}

func (g *pylonGlyph) Update(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(glyphTickMsg); ok {
		g.frame = (g.frame + 1) % len(glyphFrames)
		return glyphTickCmd()
	}
	return nil
}

func (g *pylonGlyph) View() string {
	style := lipgloss.NewStyle().Foreground(colorAccent)
	return style.Render(glyphFrames[g.frame])
}
