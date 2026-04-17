package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInsertSteps(t *testing.T) {
	base := []StepDef{
		{Key: "a"},
		{Key: "b"},
		{Key: "c"},
	}

	t.Run("insert in middle", func(t *testing.T) {
		result := insertSteps(base, 1, []StepDef{{Key: "x"}, {Key: "y"}})
		require.Len(t, result, 5)
		assert.Equal(t, "a", result[0].Key)
		assert.Equal(t, "x", result[1].Key)
		assert.Equal(t, "y", result[2].Key)
		assert.Equal(t, "b", result[3].Key)
		assert.Equal(t, "c", result[4].Key)
	})

	t.Run("insert at beginning", func(t *testing.T) {
		result := insertSteps(base, 0, []StepDef{{Key: "x"}})
		require.Len(t, result, 4)
		assert.Equal(t, "x", result[0].Key)
		assert.Equal(t, "a", result[1].Key)
	})

	t.Run("insert at end", func(t *testing.T) {
		result := insertSteps(base, 3, []StepDef{{Key: "x"}})
		require.Len(t, result, 4)
		assert.Equal(t, "c", result[2].Key)
		assert.Equal(t, "x", result[3].Key)
	})

	t.Run("insert beyond end", func(t *testing.T) {
		result := insertSteps(base, 10, []StepDef{{Key: "x"}})
		require.Len(t, result, 4)
		assert.Equal(t, "x", result[3].Key)
	})
}

func TestRemoveDynamicSteps(t *testing.T) {
	t.Run("removes prefixed steps", func(t *testing.T) {
		steps := []StepDef{
			{Key: "channel"},
			{Key: "channel.tg_token"},
			{Key: "channel.tg_verify"},
			{Key: "agent"},
		}
		result := removeDynamicSteps(steps, 1, "channel")
		require.Len(t, result, 2)
		assert.Equal(t, "channel", result[0].Key)
		assert.Equal(t, "agent", result[1].Key)
	})

	t.Run("no matching steps", func(t *testing.T) {
		steps := []StepDef{
			{Key: "channel"},
			{Key: "agent"},
		}
		result := removeDynamicSteps(steps, 1, "channel")
		require.Len(t, result, 2)
	})

	t.Run("all remaining are dynamic", func(t *testing.T) {
		steps := []StepDef{
			{Key: "trigger"},
			{Key: "trigger.path"},
			{Key: "trigger.secret"},
		}
		result := removeDynamicSteps(steps, 1, "trigger")
		require.Len(t, result, 1)
		assert.Equal(t, "trigger", result[0].Key)
	})
}

// stubStep is a minimal Step for wizard framework tests.
type stubStep struct {
	title string
	value string
	done  bool
}

func newStubStep(title, value string) Step {
	return &stubStep{title: title, value: value}
}

func (s *stubStep) Title() string       { return s.title }
func (s *stubStep) Description() string { return "" }
func (s *stubStep) Value() string       { return s.value }
func (s *stubStep) IsDone() bool        { return s.done }
func (s *stubStep) Init() tea.Cmd       { return nil }
func (s *stubStep) View(int) string     { return "" }

func (s *stubStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == keyEnter {
		s.done = true
	}
	return s, nil
}

// advanceWizardStep sends an enter key to advance a wizard step.
// Because wizardModel is a value type, the first Update after Init
// re-initializes the active step (Init modifies a copy). We need
// to send a dummy message first to trigger initialization, then
// send enter to complete the step.
func advanceWizardStep(wiz wizardModel) (wizardModel, tea.Cmd) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}

	// If active is nil, first Update initializes the step
	if wiz.active == nil {
		wiz, _ = wiz.Update(enter)
	}

	// Now send enter to complete the current step
	return wiz.Update(enter)
}

func TestWizardProgression(t *testing.T) {
	var completed bool

	steps := []StepDef{
		{Key: "step1", Create: func(_ map[string]string) Step { return newStubStep("Step 1", "val1") }},
		{Key: "step2", Create: func(_ map[string]string) Step { return newStubStep("Step 2", "val2") }},
		{Key: "step3", Create: func(_ map[string]string) Step { return newStubStep("Step 3", "val3") }},
	}

	wiz := newWizardModel("Test", steps, nil, func(values map[string]string) error {
		completed = true
		assert.Equal(t, "val1", values["step1"])
		assert.Equal(t, "val2", values["step2"])
		assert.Equal(t, "val3", values["step3"])
		return nil
	})

	_ = wiz.Init()

	var cmd tea.Cmd
	for i := 0; i < 3; i++ {
		wiz, cmd = advanceWizardStep(wiz)
		msg := execCmd(cmd)
		if i < 2 {
			assert.Nil(t, msg, "step %d should not emit wizardCompleteMsg", i)
		} else {
			_, ok := msg.(wizardCompleteMsg)
			assert.True(t, ok, "expected wizardCompleteMsg after last step")
		}
	}

	assert.True(t, completed, "onComplete should have been called")
}

func TestWizardBackNavigation(t *testing.T) {
	steps := []StepDef{
		{Key: "step1", Create: func(_ map[string]string) Step { return newStubStep("Step 1", "val1") }},
		{Key: "step2", Create: func(_ map[string]string) Step { return newStubStep("Step 2", "val2") }},
	}

	wiz := newWizardModel("Test", steps, nil, nil)
	_ = wiz.Init()

	shiftTab := tea.KeyMsg{Type: tea.KeyShiftTab}

	// Advance to step 2
	wiz, _ = advanceWizardStep(wiz)
	assert.Equal(t, 1, wiz.current, "should be on step 2")

	// Go back to step 1
	wiz, _ = wiz.Update(shiftTab)
	assert.Equal(t, 0, wiz.current, "should be back on step 1")

	// Trying to go back from step 1 should stay at step 1
	wiz, _ = wiz.Update(shiftTab)
	assert.Equal(t, 0, wiz.current, "should stay on step 1")
}

func TestWizardDynamicStepInsertion(t *testing.T) {
	steps := []StepDef{
		{Key: "choice", Create: func(_ map[string]string) Step { return newStubStep("Choice", "option_a") }},
		{Key: "final", Create: func(_ map[string]string) Step { return newStubStep("Final", "done") }},
	}

	onStepDone := func(key, value string, values map[string]string) []StepDef {
		if key == "choice" && value == "option_a" {
			return []StepDef{
				{Key: "choice.sub1", Create: func(_ map[string]string) Step { return newStubStep("Sub 1", "sub_val1") }},
				{Key: "choice.sub2", Create: func(_ map[string]string) Step { return newStubStep("Sub 2", "sub_val2") }},
			}
		}
		return nil
	}

	var completedValues map[string]string
	wiz := newWizardModel("Test", steps, onStepDone, func(values map[string]string) error {
		completedValues = values
		return nil
	})

	_ = wiz.Init()

	// Complete "choice" step -- should insert 2 dynamic sub-steps
	wiz, _ = advanceWizardStep(wiz)
	assert.Equal(t, 4, len(wiz.steps), "should have 4 steps after dynamic insertion")
	assert.Equal(t, "choice.sub1", wiz.steps[1].Key)
	assert.Equal(t, "choice.sub2", wiz.steps[2].Key)
	assert.Equal(t, "final", wiz.steps[3].Key)

	// Complete sub1, sub2, final
	for i := 0; i < 3; i++ {
		wiz, _ = advanceWizardStep(wiz)
	}

	require.NotNil(t, completedValues)
	assert.Equal(t, "option_a", completedValues["choice"])
	assert.Equal(t, "sub_val1", completedValues["choice.sub1"])
	assert.Equal(t, "sub_val2", completedValues["choice.sub2"])
	assert.Equal(t, "done", completedValues["final"])
}
