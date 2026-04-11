package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	cron "github.com/lnquy/cron"
)

// Step is the interface all wizard step components implement.
type Step interface {
	Title() string
	Description() string
	Init() tea.Cmd
	Update(msg tea.Msg) (Step, tea.Cmd)
	View(width int) string
	Value() string
	IsDone() bool
}

// --- TextInputStep ---

type textInputStep struct {
	title       string
	description string
	input       textinput.Model
	done        bool
	password    bool
}

func NewTextInputStep(title, description, placeholder string, defaultValue string, password bool) Step {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Width = 50
	if password {
		ti.EchoMode = textinput.EchoPassword
	}
	if defaultValue != "" {
		ti.SetValue(defaultValue)
	}
	ti.Focus()
	return &textInputStep{
		title:       title,
		description: description,
		input:       ti,
		password:    password,
	}
}

func (s *textInputStep) Title() string       { return s.title }
func (s *textInputStep) Description() string  { return s.description }
func (s *textInputStep) Value() string        { return s.input.Value() }
func (s *textInputStep) IsDone() bool         { return s.done }

func (s *textInputStep) Init() tea.Cmd {
	return textinput.Blink
}

func (s *textInputStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == keyEnter {
			s.done = true
			return s, nil
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s *textInputStep) View(width int) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(min(width-4, 60))
	return style.Render(s.input.View())
}

// --- SelectStep ---

type selectOption struct {
	Label string
	Value string
}

type selectStep struct {
	title       string
	description string
	options     []selectOption
	cursor      int
	done        bool
}

func NewSelectStep(title, description string, options []selectOption) Step {
	return &selectStep{
		title:       title,
		description: description,
		options:     options,
	}
}

func (s *selectStep) Title() string       { return s.title }
func (s *selectStep) Description() string { return s.description }
func (s *selectStep) IsDone() bool        { return s.done }

func (s *selectStep) Value() string {
	if s.cursor < len(s.options) {
		return s.options[s.cursor].Value
	}
	return ""
}

func (s *selectStep) Init() tea.Cmd { return nil }

func (s *selectStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case keyUp, keyK:
			if s.cursor > 0 {
				s.cursor--
			}
		case keyDown, keyJ:
			if s.cursor < len(s.options)-1 {
				s.cursor++
			}
		case keyEnter:
			s.done = true
		}
	}
	return s, nil
}

func (s *selectStep) View(width int) string {
	var b strings.Builder
	for i, opt := range s.options {
		cursor := "  "
		style := lipgloss.NewStyle().Foreground(colorText)
		if i == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorGold).Render("> ")
			style = style.Foreground(colorGold).Bold(true)
		}
		b.WriteString(cursor + style.Render(opt.Label) + "\n")
	}
	return b.String()
}

// --- MultiSelectStep ---

type multiSelectStep struct {
	title       string
	description string
	options     []selectOption
	selected    map[int]bool
	cursor      int
	done        bool
}

func NewMultiSelectStep(title, description string, options []selectOption) Step {
	return &multiSelectStep{
		title:       title,
		description: description,
		options:     options,
		selected:    make(map[int]bool),
	}
}

func (s *multiSelectStep) Title() string       { return s.title }
func (s *multiSelectStep) Description() string { return s.description }
func (s *multiSelectStep) IsDone() bool        { return s.done }

func (s *multiSelectStep) Value() string {
	var vals []string
	for i, opt := range s.options {
		if s.selected[i] {
			vals = append(vals, opt.Value)
		}
	}
	return strings.Join(vals, ",")
}

func (s *multiSelectStep) Init() tea.Cmd { return nil }

func (s *multiSelectStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case keyUp, keyK:
			if s.cursor > 0 {
				s.cursor--
			}
		case keyDown, keyJ:
			if s.cursor < len(s.options)-1 {
				s.cursor++
			}
		case " ":
			s.selected[s.cursor] = !s.selected[s.cursor]
		case keyEnter:
			s.done = true
		}
	}
	return s, nil
}

func (s *multiSelectStep) View(width int) string {
	var b strings.Builder
	for i, opt := range s.options {
		check := "[ ] "
		if s.selected[i] {
			check = lipgloss.NewStyle().Foreground(colorAccent).Render("[x] ")
		}
		cursor := "  "
		style := lipgloss.NewStyle().Foreground(colorText)
		if i == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("> ")
			style = style.Bold(true)
		}
		b.WriteString(cursor + check + style.Render(opt.Label) + "\n")
	}
	b.WriteString("\n" + mutedStyle.Render("  space to toggle, enter to confirm"))
	return b.String()
}

// --- ConfirmStep ---

type confirmStep struct {
	title       string
	description string
	value       bool
	done        bool
}

func NewConfirmStep(title, description string, defaultValue bool) Step {
	return &confirmStep{
		title:       title,
		description: description,
		value:       defaultValue,
	}
}

func (s *confirmStep) Title() string       { return s.title }
func (s *confirmStep) Description() string { return s.description }
func (s *confirmStep) IsDone() bool        { return s.done }

func (s *confirmStep) Value() string {
	if s.value {
		return "yes"
	}
	return "no"
}

func (s *confirmStep) Init() tea.Cmd { return nil }

func (s *confirmStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case keyTab, "left", "right", "h", "l":
			s.value = !s.value
		case keyEnter:
			s.done = true
		case "y":
			s.value = true
			s.done = true
		case "n":
			s.value = false
			s.done = true
		}
	}
	return s, nil
}

func (s *confirmStep) View(width int) string {
	yes := "  Yes  "
	no := "  No  "

	activeStyle := lipgloss.NewStyle().
		Background(colorHighlight).
		Foreground(colorGold).
		Bold(true)
	inactiveStyle := lipgloss.NewStyle().
		Foreground(colorMuted)

	if s.value {
		yes = activeStyle.Render(yes)
		no = inactiveStyle.Render(no)
	} else {
		yes = inactiveStyle.Render(yes)
		no = activeStyle.Render(no)
	}

	return yes + "  " + no + "\n\n" + mutedStyle.Render("  tab to toggle, enter to confirm")
}

// --- CopyBlockStep ---

type copyBlockStep struct {
	title       string
	description string
	content     string
	copied      bool
	done        bool
	flash       copyFlashModel
}

func NewCopyBlockStep(title, description, content string) Step {
	return &copyBlockStep{
		title:       title,
		description: description,
		content:     content,
	}
}

func (s *copyBlockStep) Title() string       { return s.title }
func (s *copyBlockStep) Description() string { return s.description }
func (s *copyBlockStep) Value() string       { return s.content }
func (s *copyBlockStep) IsDone() bool        { return s.done }

func (s *copyBlockStep) Init() tea.Cmd { return nil }

func (s *copyBlockStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	switch msg := msg.(type) {
	case copiedMsg:
		s.copied = true
		var cmd tea.Cmd
		s.flash, cmd = s.flash.show(msg.label)
		return s, cmd
	case copyFlashClearMsg:
		s.flash = s.flash.Update(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case keyY:
			return s, copyToClipboard(s.content, "content")
		case keyEnter:
			s.done = true
		}
	}
	return s, nil
}

func (s *copyBlockStep) View(width int) string {
	blockWidth := min(width-4, 80)
	block := copyBlockStyle.Width(blockWidth).Render(s.content)
	hint := mutedStyle.Render("  [y] copy to clipboard  [enter] continue")
	flash := s.flash.View()
	if flash != "" {
		hint = "  " + flash
	}
	return block + "\n" + hint
}

// --- AsyncStep ---

type asyncStep struct {
	title       string
	description string
	fn          func() (string, error)
	result      string
	err         error
	done        bool
	running     bool
	spin        spinner.Model
}

// asyncResultMsg carries the result of an async operation.
type asyncResultMsg struct {
	result string
	err    error
}

func NewAsyncStep(title, description string, fn func() (string, error)) Step {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorAccent)
	return &asyncStep{
		title:       title,
		description: description,
		fn:          fn,
		spin:        s,
	}
}

func (s *asyncStep) Title() string       { return s.title }
func (s *asyncStep) Description() string { return s.description }
func (s *asyncStep) Value() string       { return s.result }
func (s *asyncStep) IsDone() bool        { return s.done }

func (s *asyncStep) Init() tea.Cmd {
	s.running = true
	fn := s.fn
	return tea.Batch(s.spin.Tick, func() tea.Msg {
		result, err := fn()
		return asyncResultMsg{result: result, err: err}
	})
}

func (s *asyncStep) Update(msg tea.Msg) (Step, tea.Cmd) {
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
			// Allow retry on error
			return s, s.Init()
		}
	}
	return s, nil
}

func (s *asyncStep) View(width int) string {
	if s.running {
		return s.spin.View() + " " + subtextStyle.Render(s.description+"...")
	}
	if s.err != nil {
		return statusFailed.Render(fmt.Sprintf("Error: %v", s.err)) + "\n" +
			mutedStyle.Render("  [enter] retry")
	}
	return statusActive.Render("OK") + "  " + subtextStyle.Render(s.result)
}

// --- InfoStep ---

type infoStep struct {
	title       string
	description string
	content     string
	done        bool
}

func NewInfoStep(title, description, content string) Step {
	return &infoStep{
		title:       title,
		description: description,
		content:     content,
	}
}

func (s *infoStep) Title() string       { return s.title }
func (s *infoStep) Description() string { return s.description }
func (s *infoStep) Value() string       { return "" }
func (s *infoStep) IsDone() bool        { return s.done }
func (s *infoStep) Init() tea.Cmd       { return nil }

func (s *infoStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == keyEnter {
		s.done = true
	}
	return s, nil
}

func (s *infoStep) View(width int) string {
	return subtextStyle.Render(s.content) + "\n\n" + mutedStyle.Render("  [enter] continue")
}

// --- CronInputStep ---

type cronInputStep struct {
	title       string
	description string
	input       textinput.Model
	done        bool
}

func NewCronInputStep(title, description, placeholder, defaultValue string) Step {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Width = 50
	if defaultValue != "" {
		ti.SetValue(defaultValue)
	}
	ti.Focus()
	return &cronInputStep{
		title:       title,
		description: description,
		input:       ti,
	}
}

func (s *cronInputStep) Title() string       { return s.title }
func (s *cronInputStep) Description() string { return s.description }
func (s *cronInputStep) Value() string       { return s.input.Value() }
func (s *cronInputStep) IsDone() bool        { return s.done }
func (s *cronInputStep) Init() tea.Cmd       { return textinput.Blink }

func (s *cronInputStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == keyEnter {
			s.done = true
			return s, nil
		}
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s *cronInputStep) View(width int) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1).
		Width(min(width-4, 60))
	view := style.Render(s.input.View())

	// Show live human-readable description
	val := strings.TrimSpace(s.input.Value())
	if val != "" {
		if desc := describeCronExpr(val); desc != val {
			view += "\n" + mutedStyle.Render(desc)
		}
	}
	return view
}

func describeCronExpr(expr string) string {
	d, err := cron.NewDescriptor()
	if err != nil {
		return expr
	}
	desc, err := d.ToDescription(expr, cron.Locale_en)
	if err != nil {
		return expr
	}
	return desc
}

