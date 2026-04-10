package tui

import "github.com/charmbracelet/bubbletea"

// Key constants used across views.
const (
	keyEnter    = "enter"
	keyEsc      = "esc"
	keyQ        = "q"
	keyS        = "s"
	keyC        = "c"
	keyD        = "d"
	keyY        = "y"
	keyP        = "p"
	keyE        = "e"
	keyUp       = "up"
	keyDown     = "down"
	keyK        = "k"
	keyJ        = "j"
	keyCtrlC    = "ctrl+c"
	keyTab      = "tab"
	keyShiftTab = "shift+tab"
)

// isQuit returns true for quit key combos.
func isQuit(msg tea.KeyMsg) bool {
	return msg.String() == keyCtrlC
}
