package tui

import (
	"fmt"
	"math"

	catppuccin "github.com/catppuccin/go"
	"github.com/charmbracelet/lipgloss"
)

var palette = catppuccin.Mocha

// Colors derived from Catppuccin Mocha with blue/gold scheme.
var (
	colorAccent  = lipgloss.Color(palette.Blue().Hex)     // #89b4fa -- primary blue
	colorGold    = lipgloss.Color(palette.Peach().Hex)    // #fab387 -- warm gold accent
	colorSuccess = lipgloss.Color(palette.Green().Hex)    // #a6e3a1
	colorWarning = lipgloss.Color(palette.Yellow().Hex)   // #f9e2af
	colorError   = lipgloss.Color(palette.Red().Hex)      // #f38ba8
	colorMuted   = lipgloss.Color(palette.Overlay0().Hex) // #6c7086
	colorText    = lipgloss.Color(palette.Text().Hex)     // #cdd6f4
	colorSubtext = lipgloss.Color(palette.Subtext1().Hex) // #bac2de
	colorSurface = lipgloss.Color(palette.Surface0().Hex) // #313244
	colorBase    = lipgloss.Color(palette.Base().Hex)     // #1e1e2e
	colorTeal    = lipgloss.Color(palette.Teal().Hex)     // #94e2d5
	colorHighlight = lipgloss.Color("#1e3a5f") // dark navy -- subtle blue-tinted row highlight
	colorGoldDim   = lipgloss.Color("#7d6348") // dimmed gold for separators
)

// renderShimmer renders text with a single smooth bright spot that sweeps across.
func renderShimmer(text string, offset int) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return ""
	}

	// The bright spot position sweeps across the text and beyond.
	// Adding padding so the spot fully enters and exits.
	span := float64(n + 6)
	pos := math.Mod(float64(offset)*0.5, span) - 3

	var result string
	for i, r := range runes {
		// Gaussian falloff from the bright spot
		d := float64(i) - pos
		t := math.Exp(-d * d / 8)

		// Interpolate between dim gold and warm gold
		ri := 0x7d + int(float64(0xb9-0x7d)*t)
		gi := 0x63 + int(float64(0xa3-0x63)*t)
		bi := 0x48 + int(float64(0x74-0x48)*t)
		color := fmt.Sprintf("#%02x%02x%02x", ri, gi, bi)

		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
		result += style.Render(string(r))
	}
	return result
}

// Title bar spans the full terminal width.
var titleBarStyle = lipgloss.NewStyle().
	Background(colorAccent).
	Foreground(colorBase).
	Bold(true).
	Padding(0, 1)

// Table header row.
var tableHeaderStyle = lipgloss.NewStyle().
	Foreground(colorAccent).
	Bold(true)

// Selected row cursor indicator.
var cursorStyle = lipgloss.NewStyle().Foreground(colorGold)

// Selected row text -- just bold, no background.
var selectedRowStyle = lipgloss.NewStyle().
	Foreground(colorText).
	Bold(true)

// Normal table row.
var tableRowStyle = lipgloss.NewStyle().
	Foreground(colorText)

// Muted text for descriptions and hints.
var mutedStyle = lipgloss.NewStyle().
	Foreground(colorMuted)

// Subtext for secondary information.
var subtextStyle = lipgloss.NewStyle().
	Foreground(colorSubtext)

// Status styles.
var (
	statusActive  = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	statusIdle    = lipgloss.NewStyle().Foreground(colorSubtext)
	statusStopped = lipgloss.NewStyle().Foreground(colorMuted)
	statusFailed  = lipgloss.NewStyle().Foreground(colorError)
)

// Footer keybind styles -- gold keys for visibility.
var (
	keyStyle  = lipgloss.NewStyle().Foreground(colorGold).Bold(true)
	descStyle = lipgloss.NewStyle().Foreground(colorSubtext)
)

// Copy block border.
var copyBlockStyle = lipgloss.NewStyle().
	Border(lipgloss.DoubleBorder()).
	BorderForeground(colorTeal).
	Padding(0, 1)

// Version style in the title bar -- gold on blue.
var titleVersionStyle = lipgloss.NewStyle().
	Background(colorAccent).
	Foreground(colorGold).
	Bold(true)

// renderTitle renders the full-width title bar.
func renderTitle(title, version string, width int) string {
	ver := titleVersionStyle.Render(version)
	gap := width - lipgloss.Width(title) - lipgloss.Width(ver) - 2 // padding
	if gap < 1 {
		gap = 1
	}
	content := title + spaces(gap) + ver
	return titleBarStyle.Width(width).Render(content)
}

// renderFooter renders keybind hints at the bottom.
func renderFooter(bindings []keyBinding, width int) string {
	var parts []string
	for _, b := range bindings {
		parts = append(parts, keyStyle.Render(b.key)+" "+descStyle.Render(b.desc))
	}
	line := ""
	for i, p := range parts {
		if i > 0 {
			line += "  "
		}
		line += p
	}
	return mutedStyle.Width(width).Render(line)
}

type keyBinding struct {
	key  string
	desc string
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
