package tui

import (
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// editorStep spawns $EDITOR for multiline text editing.
type editorStep struct {
	title       string
	description string
	result      string
	tmpPath     string
	done        bool
}

// editorFinishedMsg carries the result of an $EDITOR session.
type editorFinishedMsg struct {
	content string
	err     error
}

func NewEditorStep(title, description, defaultValue string) Step {
	return &editorStep{
		title:       title,
		description: description,
		result:      defaultValue,
	}
}

func (s *editorStep) Title() string       { return s.title }
func (s *editorStep) Description() string { return s.description }
func (s *editorStep) Value() string       { return s.result }
func (s *editorStep) IsDone() bool        { return s.done }

// Init does not auto-launch. The user presses Enter to open the editor.
func (s *editorStep) Init() tea.Cmd { return nil }

// launchEditor writes the content to a temp file and returns a tea.Exec
// command that properly suspends the TUI to run $EDITOR.
func (s *editorStep) launchEditor() tea.Cmd {
	tmpFile, err := os.CreateTemp("", "pylon-*.md")
	if err != nil {
		return func() tea.Msg { return editorFinishedMsg{err: err} }
	}
	s.tmpPath = tmpFile.Name()
	if _, writeErr := tmpFile.WriteString(s.result); writeErr != nil {
		tmpFile.Close()
		os.Remove(s.tmpPath)
		return func() tea.Msg { return editorFinishedMsg{err: writeErr} }
	}
	tmpFile.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	c := exec.Command(editor, s.tmpPath)
	tmpPath := s.tmpPath
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return editorFinishedMsg{err: err}
		}
		data, readErr := os.ReadFile(tmpPath)
		if readErr != nil {
			return editorFinishedMsg{err: readErr}
		}
		return editorFinishedMsg{content: string(data)}
	})
}

func (s *editorStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	switch msg := msg.(type) {
	case editorFinishedMsg:
		if msg.err != nil {
			s.done = true
			return s, nil
		}
		s.result = msg.content
		s.done = true
	case tea.KeyMsg:
		if msg.String() == keyEnter {
			return s, s.launchEditor()
		}
	}
	return s, nil
}

func (s *editorStep) View(width int) string {
	preview := s.result
	if len(preview) > 200 {
		preview = preview[:197] + "..."
	}

	block := copyBlockStyle.Width(min(width-4, 70)).Render(preview)
	return block + "\n\n" + mutedStyle.Render("  [enter] open in $EDITOR")
}
