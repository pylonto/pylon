package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Cancelable is implemented by steps that hold long-running background work
// (goroutines, HTTP polling, etc.) that should be stopped when the wizard
// navigates away from them. The wizard calls Cancel() before advancing past
// or backing out of such a step.
type Cancelable interface {
	Cancel()
}

type waitingAsyncStep struct {
	title          string
	description    string
	waitingMessage string
	fn             func(ctx context.Context) (string, error)
	ctx            context.Context
	cancel         context.CancelFunc
	running        bool
	done           bool
	result         string
	err            error
	spin           spinner.Model
}

// NewWaitingAsyncStep is for long-running async operations whose duration is
// user-controlled (e.g. polling for a Telegram chat message). The step shows
// a persistent "waiting..." message alongside a spinner. If the wizard backs
// out of this step, its context is canceled so fn can return promptly.
//
// fn receives a context that is canceled when the step is abandoned.
func NewWaitingAsyncStep(title, description, waitingMessage string,
	fn func(ctx context.Context) (string, error)) Step {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorAccent)
	return &waitingAsyncStep{
		title:          title,
		description:    description,
		waitingMessage: waitingMessage,
		fn:             fn,
		spin:           s,
	}
}

func (s *waitingAsyncStep) Title() string       { return s.title }
func (s *waitingAsyncStep) Description() string { return s.description }
func (s *waitingAsyncStep) Value() string       { return s.result }
func (s *waitingAsyncStep) IsDone() bool        { return s.done }

func (s *waitingAsyncStep) Init() tea.Cmd {
	s.running = true
	s.err = nil
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ctx := s.ctx
	fn := s.fn
	return tea.Batch(s.spin.Tick, func() tea.Msg {
		result, err := fn(ctx)
		return asyncResultMsg{result: result, err: err}
	})
}

// Cancel aborts the inflight fn goroutine so it can return promptly when the
// wizard navigates away from this step.
func (s *waitingAsyncStep) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *waitingAsyncStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	switch msg := msg.(type) {
	case asyncResultMsg:
		s.running = false
		s.result = msg.result
		s.err = msg.err
		if s.err == nil {
			s.done = true
		}
		return s, nil
	case spinner.TickMsg:
		if s.running {
			var cmd tea.Cmd
			s.spin, cmd = s.spin.Update(msg)
			return s, cmd
		}
	case tea.KeyMsg:
		if !s.running && s.err != nil && msg.String() == keyEnter {
			return s, s.Init()
		}
	}
	return s, nil
}

func (s *waitingAsyncStep) View(width int) string {
	if s.running {
		return s.spin.View() + " " + subtextStyle.Render(s.waitingMessage) + "\n\n" +
			mutedStyle.Render("  [shift+tab] cancel and go back")
	}
	if s.err != nil {
		return statusFailed.Render(fmt.Sprintf("Error: %v", s.err)) + "\n" +
			mutedStyle.Render("  [enter] retry  [shift+tab] back")
	}
	return statusActive.Render("OK") + "  " + subtextStyle.Render(s.result)
}
