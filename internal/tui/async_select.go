package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// asyncSelectState tracks which sub-view of the step is active.
type asyncSelectState int

const (
	asyncSelectRunning asyncSelectState = iota
	asyncSelectPicking
	asyncSelectFallback
	asyncSelectError
)

// asyncSelectResultMsg delivers the fetched options to the step.
type asyncSelectResultMsg struct {
	options []selectOption
	err     error
}

type asyncSelectStep struct {
	title               string
	description         string
	fetch               func() ([]selectOption, error)
	allowManualFallback bool
	manualPlaceholder   string
	state               asyncSelectState
	spin                spinner.Model
	picker              *selectStep
	fallback            *textInputStep
	err                 error
}

// NewAsyncSelectStep runs an async fetch that yields select options, then
// presents them to the user. If the fetch returns zero options and
// allowManualFallback is true, the step transitions to a text input so the
// user can still provide a value. Errors show a retry hint on [enter] and,
// when allowed, an [m] hint to jump into manual entry.
func NewAsyncSelectStep(title, description string,
	fetch func() ([]selectOption, error),
	allowManualFallback bool,
	manualPlaceholder string) Step {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorAccent)
	return &asyncSelectStep{
		title:               title,
		description:         description,
		fetch:               fetch,
		allowManualFallback: allowManualFallback,
		manualPlaceholder:   manualPlaceholder,
		spin:                s,
	}
}

func (s *asyncSelectStep) Title() string       { return s.title }
func (s *asyncSelectStep) Description() string { return s.description }

func (s *asyncSelectStep) Value() string {
	switch s.state {
	case asyncSelectPicking:
		if s.picker != nil {
			return s.picker.Value()
		}
	case asyncSelectFallback:
		if s.fallback != nil {
			return s.fallback.Value()
		}
	}
	return ""
}

func (s *asyncSelectStep) IsDone() bool {
	switch s.state {
	case asyncSelectPicking:
		return s.picker != nil && s.picker.IsDone()
	case asyncSelectFallback:
		return s.fallback != nil && s.fallback.IsDone()
	}
	return false
}

func (s *asyncSelectStep) Init() tea.Cmd {
	s.state = asyncSelectRunning
	s.err = nil
	fetch := s.fetch
	return tea.Batch(s.spin.Tick, func() tea.Msg {
		opts, err := fetch()
		return asyncSelectResultMsg{options: opts, err: err}
	})
}

func (s *asyncSelectStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	switch m := msg.(type) {
	case asyncSelectResultMsg:
		if m.err != nil {
			s.err = m.err
			s.state = asyncSelectError
			return s, nil
		}
		if len(m.options) == 0 {
			if s.allowManualFallback {
				s.fallback = newTextInputStepConcrete("", "", s.manualPlaceholder, "")
				s.state = asyncSelectFallback
				return s, s.fallback.Init()
			}
			s.err = fmt.Errorf("no options found")
			s.state = asyncSelectError
			return s, nil
		}
		s.picker = &selectStep{options: m.options}
		s.state = asyncSelectPicking
		return s, s.picker.Init()

	case spinner.TickMsg:
		if s.state == asyncSelectRunning {
			var cmd tea.Cmd
			s.spin, cmd = s.spin.Update(msg)
			return s, cmd
		}

	case tea.KeyMsg:
		switch s.state {
		case asyncSelectError:
			switch m.String() {
			case keyEnter:
				return s, s.Init()
			case "m":
				if s.allowManualFallback {
					s.fallback = newTextInputStepConcrete("", "", s.manualPlaceholder, "")
					s.state = asyncSelectFallback
					s.err = nil
					return s, s.fallback.Init()
				}
			}
		case asyncSelectPicking:
			step, cmd := s.picker.Update(msg)
			s.picker = step.(*selectStep)
			return s, cmd
		case asyncSelectFallback:
			step, cmd := s.fallback.Update(msg)
			s.fallback = step.(*textInputStep)
			return s, cmd
		}

	default:
		switch s.state {
		case asyncSelectPicking:
			if s.picker != nil {
				step, cmd := s.picker.Update(msg)
				s.picker = step.(*selectStep)
				return s, cmd
			}
		case asyncSelectFallback:
			if s.fallback != nil {
				step, cmd := s.fallback.Update(msg)
				s.fallback = step.(*textInputStep)
				return s, cmd
			}
		}
	}
	return s, nil
}

func (s *asyncSelectStep) View(width int) string {
	switch s.state {
	case asyncSelectRunning:
		return s.spin.View() + " " + subtextStyle.Render(s.description+"...")
	case asyncSelectError:
		out := statusFailed.Render(fmt.Sprintf("Error: %v", s.err))
		hint := "  [enter] retry"
		if s.allowManualFallback {
			hint += "  [m] enter manually"
		}
		return out + "\n" + mutedStyle.Render(hint)
	case asyncSelectPicking:
		return s.picker.View(width)
	case asyncSelectFallback:
		return mutedStyle.Render("  No results -- enter a value manually:") + "\n" + s.fallback.View(width)
	}
	return ""
}

// newTextInputStepConcrete returns a *textInputStep directly (not the Step
// interface) so asyncSelectStep can drive it as an inner field.
func newTextInputStepConcrete(title, description, placeholder, defaultValue string) *textInputStep {
	s := NewTextInputStep(title, description, placeholder, defaultValue, false)
	return s.(*textInputStep)
}
