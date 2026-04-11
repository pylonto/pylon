package tui

import "github.com/charmbracelet/bubbletea"

// Key constants used across views.
const (
	keyA        = "a"
	keyC        = "c"
	keyCtrlC    = "ctrl+c"
	keyD        = "d"
	keyDown     = "down"
	keyE        = "e"
	keyEnter    = "enter"
	keyEsc      = "esc"
	keyG        = "g"
	keyJ        = "j"
	keyK        = "k"
	keyL        = "l"
	keyP        = "p"
	keyQ        = "q"
	keyQuestion = "?"
	keyS        = "s"
	keyShiftTab = "shift+tab"
	keyT        = "t"
	keyTab      = "tab"
	keyU        = "u"
	keyUp       = "up"
	keyX        = "x"
	keyY        = "y"
)

// isQuit returns true for quit key combos.
func isQuit(msg tea.KeyMsg) bool {
	return msg.String() == keyCtrlC
}
