package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pylonGlyph is a simple cycling spinner using crystal/energy unicode characters.
type pylonGlyph struct {
	frame       int
	shimmerTick int // monotonic counter for shimmer animation (faster than glyph frames)
}

type glyphTickMsg struct{}
type shimmerTickMsg struct{}

const glyphInterval   = 150 * time.Millisecond
const shimmerInterval = 50 * time.Millisecond

// Crystal-themed spinner frames: diamond forms and pulses.
var glyphFrames = []string{
	"·", // dot -- pause
	"✧", // white four-pointed star
	"◇", // open diamond
	"◈", // diamond with dot -- filling
	"◆", // solid diamond -- full
	"◈", // diamond with dot -- fading
	"◇", // open diamond
	"✧", // white four-pointed star
}

func glyphTickCmd() tea.Cmd {
	return tea.Tick(glyphInterval, func(time.Time) tea.Msg {
		return glyphTickMsg{}
	})
}

func shimmerTickCmd() tea.Cmd {
	return tea.Tick(shimmerInterval, func(time.Time) tea.Msg {
		return shimmerTickMsg{}
	})
}

func (g *pylonGlyph) Update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case glyphTickMsg:
		g.frame = (g.frame + 1) % len(glyphFrames)
		return glyphTickCmd()
	case shimmerTickMsg:
		g.shimmerTick++
		return shimmerTickCmd()
	}
	return nil
}

func (g *pylonGlyph) View() string {
	style := lipgloss.NewStyle().Foreground(colorAccent)
	return style.Render(glyphFrames[g.frame])
}
