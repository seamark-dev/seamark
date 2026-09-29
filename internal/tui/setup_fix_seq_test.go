package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

// The tests in this file prove that a plan or apply result reaches only
// the wizard that asked for it. Every new wizard counts seq from zero,
// so seq alone cannot tell two wizards apart.

// setupHold sends one key press and returns its commands without
// running them. A test runs them later to deliver a late result.
func setupHold(h *harness, k string) tea.Cmd {
	h.t.Helper()

	_, cmd := h.m.Update(keyMsg(k))

	return cmd
}

// setupCloseWithHeldPlan opens a Codex wizard, checks the managed
// skills, starts its plan without running it, and closes the wizard.
// It returns the held plan command and the closed wizard.
func setupCloseWithHeldPlan(t *testing.T, h *harness) (tea.Cmd, *setupWizard) {
	t.Helper()

	old := setupScreen(h).wizard
	require.NotNil(t, old)

	h.press("down", "down", "space")
	held := setupHold(h, "enter")
	require.True(t, old.planning)

	h.press("esc", "esc")
	require.Nil(t, setupScreen(h).wizard)

	return held, old
}

func TestSetupLatePlanOfClosedWizardArrivesAfterTheNewPlan(t *testing.T) {
	for _, next := range []string{"claude", "codex"} {
		t.Run(next, func(t *testing.T) {
			f, requests := setupFake()

			var applied *SetupPreview

			f.applySetup = func(p *SetupPreview) (integration.SetupResult, error) {
				applied = p

				return integration.SetupResult{}, nil
			}

			h := setupOpen(t, f, 120, 32)
			held, old := setupCloseWithHeldPlan(t, h)

			h.send(navigateMsg{dest: destIntegrations, intent: "setup:" + next})
			wz := setupScreen(h).wizard
			require.NotSame(t, old, wz)

			// The new wizard plans first; the closed wizard's plan
			// arrives last.
			h.press("enter")
			require.NotNil(t, wz.preview)

			reviewed := wz.preview

			h.run(held)
			require.Len(t, *requests, 2)
			require.NotEqual(t, (*requests)[0], (*requests)[1], "the two wizards asked for different plans")

			assert.Same(t, reviewed, wz.preview, "the late plan of a closed wizard is dropped")
			assert.Equal(t, (*requests)[0], wz.preview.Setups)
			assert.Contains(t, h.view(), "Set up "+wz.client.Name)

			h.press("y")
			require.NotNil(t, applied)
			assert.Same(t, reviewed, applied, "apply takes the plan that the new wizard reviewed")
			assert.Equal(t, next, applied.Setups[0].ClientID)
		})
	}
}

func TestSetupLatePlanOfClosedWizardArrivesBeforeTheNewPlan(t *testing.T) {
	for _, next := range []string{"claude", "codex"} {
		t.Run(next, func(t *testing.T) {
			f, requests := setupFake()
			h := setupOpen(t, f, 120, 32)
			heldOld, _ := setupCloseWithHeldPlan(t, h)

			h.send(navigateMsg{dest: destIntegrations, intent: "setup:" + next})
			wz := setupScreen(h).wizard

			heldNew := setupHold(h, "enter")
			require.True(t, wz.planning)

			// The closed wizard's plan arrives while the new one plans.
			h.run(heldOld)
			assert.True(t, wz.planning, "a foreign plan does not end the planning")
			assert.Nil(t, wz.preview)
			assert.Contains(t, h.view(), "Planning the setup")

			h.run(heldNew)
			require.Len(t, *requests, 2)
			require.NotNil(t, wz.preview)
			assert.False(t, wz.planning)
			assert.Equal(t, (*requests)[1], wz.preview.Setups, "the preview is the new wizard's own plan")
			assert.Equal(t, next, wz.preview.Setups[0].ClientID)
		})
	}
}

func TestSetupApplyResultOfAnotherWizardIsDropped(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	wz := setupScreen(h).wizard
	require.Equal(t, setupStepPreview, wz.step)

	wz.finished(setupAppliedMsg{owner: newSetupWizard(h.m.st, f.clients), seq: wz.seq}, 0)
	assert.Equal(t, setupStepPreview, wz.step)
	assert.Nil(t, wz.result)

	wz.finished(setupAppliedMsg{owner: wz, seq: wz.seq}, 0)
	assert.Equal(t, setupStepResult, wz.step, "the wizard's own result arrives")
}
