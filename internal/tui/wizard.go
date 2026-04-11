package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// wizardCompleteMsg is sent when the wizard finishes all steps.
type wizardCompleteMsg struct {
	err error
}

// StepDef defines a wizard step and optional post-step logic.
type StepDef struct {
	// Create returns a fresh Step instance.
	Create func() Step
	// Key is a unique identifier for retrieving the step's value.
	Key string
}

// wizardModel drives a multi-step form wizard.
type wizardModel struct {
	title   string
	steps   []StepDef
	current int
	active  Step
	values  map[string]string
	err     error

	// confirmCancel is true when the user pressed ESC and is being asked to confirm.
	confirmCancel bool

	// onStepDone is called after each step completes. It receives the current
	// values map and can return new steps to insert after the current position.
	// This enables dynamic step insertion based on user choices.
	onStepDone func(key, value string, values map[string]string) []StepDef

	// onComplete is called when all steps are done.
	onComplete func(values map[string]string) error
}

func newWizardModel(title string, steps []StepDef, onStepDone func(string, string, map[string]string) []StepDef, onComplete func(map[string]string) error) wizardModel {
	return wizardModel{
		title:      title,
		steps:      steps,
		values:     make(map[string]string),
		onStepDone: onStepDone,
		onComplete: onComplete,
	}
}

func (m wizardModel) Init() tea.Cmd {
	if len(m.steps) == 0 {
		return func() tea.Msg { return wizardCompleteMsg{} }
	}
	m.active = m.steps[0].Create()
	return m.active.Init()
}

func (m wizardModel) Update(msg tea.Msg) (wizardModel, tea.Cmd) {
	// Initialize active step if needed
	if m.active == nil && m.current < len(m.steps) {
		m.active = m.steps[m.current].Create()
		return m, m.active.Init()
	}

	// Handle back navigation
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == keyShiftTab && m.current > 0 {
			m.current--
			m.active = m.steps[m.current].Create()
			// Restore previous value if we have it
			if val, exists := m.values[m.steps[m.current].Key]; exists {
				_ = val // The step is recreated fresh; we don't restore values for now
			}
			return m, m.active.Init()
		}
	}

	// Delegate to active step
	if m.active != nil {
		var cmd tea.Cmd
		m.active, cmd = m.active.Update(msg)

		// Check if step completed
		if m.active.IsDone() {
			key := m.steps[m.current].Key
			value := m.active.Value()
			m.values[key] = value

			// Dynamic step insertion
			if m.onStepDone != nil {
				newSteps := m.onStepDone(key, value, m.values)
				if len(newSteps) > 0 {
					// Remove any previously inserted dynamic steps for this key
					m.steps = removeDynamicSteps(m.steps, m.current+1, key)
					// Insert new steps after current
					m.steps = insertSteps(m.steps, m.current+1, newSteps)
				}
			}

			// Advance to next step
			m.current++
			if m.current >= len(m.steps) {
				// All done
				if m.onComplete != nil {
					if err := m.onComplete(m.values); err != nil {
						m.err = err
						return m, nil
					}
				}
				return m, func() tea.Msg { return wizardCompleteMsg{} }
			}

			m.active = m.steps[m.current].Create()
			return m, m.active.Init()
		}

		return m, cmd
	}

	return m, nil
}

func (m wizardModel) View(width, height int) string {
	var b strings.Builder

	// Progress bar
	progress := renderProgress(m.current+1, len(m.steps), width-4)
	stepLabel := fmt.Sprintf("Step %d of %d", m.current+1, len(m.steps))
	b.WriteString("  " + subtextStyle.Render(m.title) + spaces(width-4-lipgloss.Width(m.title)-lipgloss.Width(stepLabel)) + mutedStyle.Render(stepLabel) + "\n")
	b.WriteString("  " + progress + "\n\n")

	// Step title and description
	if m.active != nil {
		title := m.active.Title()
		if title != "" {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(title) + "\n")
		}
		desc := m.active.Description()
		if desc != "" {
			b.WriteString(indentBlock(subtextStyle.Render(desc), "  ") + "\n")
		}
		b.WriteString("\n")

		// Step content
		b.WriteString(indentBlock(m.active.View(width-4), "  ") + "\n")
	}

	// Cancel confirmation
	if m.confirmCancel {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("Discard progress?") +
			"  " + mutedStyle.Render("y to confirm, any key to continue") + "\n")
	}

	// Error display
	if m.err != nil {
		b.WriteString("\n  " + statusFailed.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	return b.String()
}

func (m wizardModel) footerBindings() []keyBinding {
	if m.confirmCancel {
		return []keyBinding{
			{"y/esc", "discard"},
			{"any", "continue"},
		}
	}
	bindings := []keyBinding{
		{"enter", "next"},
	}
	if m.current > 0 {
		bindings = append(bindings, keyBinding{"shift+tab", "back"})
	}
	bindings = append(bindings, keyBinding{"esc", "cancel"})
	return bindings
}

// indentBlock prepends prefix to every non-empty line in s.
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

// renderProgress draws a progress bar.
func renderProgress(current, total, width int) string {
	if total == 0 {
		return ""
	}

	barWidth := width
	if barWidth < 10 {
		barWidth = 10
	}

	filled := barWidth * current / total
	if filled > barWidth {
		filled = barWidth
	}

	filledStyle := lipgloss.NewStyle().Foreground(colorAccent)
	emptyStyle := lipgloss.NewStyle().Foreground(colorSurface)

	bar := filledStyle.Render(strings.Repeat("=", filled)) +
		emptyStyle.Render(strings.Repeat("-", barWidth-filled))

	return bar
}

// insertSteps inserts new steps at the given position.
func insertSteps(steps []StepDef, pos int, newSteps []StepDef) []StepDef {
	if pos >= len(steps) {
		return append(steps, newSteps...)
	}
	result := make([]StepDef, 0, len(steps)+len(newSteps))
	result = append(result, steps[:pos]...)
	result = append(result, newSteps...)
	result = append(result, steps[pos:]...)
	return result
}

// removeDynamicSteps removes consecutive steps after pos that were inserted
// dynamically (identified by having a Key prefixed with the parent key).
func removeDynamicSteps(steps []StepDef, pos int, parentKey string) []StepDef {
	prefix := parentKey + "."
	end := pos
	for end < len(steps) && strings.HasPrefix(steps[end].Key, prefix) {
		end++
	}
	if end == pos {
		return steps
	}
	result := make([]StepDef, 0, len(steps)-(end-pos))
	result = append(result, steps[:pos]...)
	result = append(result, steps[end:]...)
	return result
}
