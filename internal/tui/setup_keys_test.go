package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests in this file cover the key routing of the setup wizard:
// the global keys act on every step except the apply, only "y" starts a
// durable apply, and the footer names what the open key shows.

func TestSetupGlobalKeysActOnTheChooseAndReviewSteps(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	s := setupScreen(h)
	wz := s.wizard

	require.Equal(t, setupStepChoices, wz.step)
	assert.False(t, s.capturing(), "the choose step has no text input and no dialog")
	assert.Contains(t, h.view(), "? help", "the footer lists the global keys")

	h.press("down", "down", "space")
	require.Contains(t, h.view(), "[✓] Managed skills")

	h.press("?")
	assert.True(t, h.m.help)

	h.press("esc")
	assert.False(t, h.m.help)
	assert.Same(t, wz, s.wizard, "esc closes the help, not the wizard")

	// A destination switch keeps the wizard with its choices.
	h.press("1")
	assert.Equal(t, destOverview, h.m.active)

	h.press("2")
	assert.Equal(t, destIntegrations, h.m.active)
	assert.Same(t, wz, s.wizard)
	assert.Equal(t, setupStepChoices, wz.step)
	assert.Contains(t, h.view(), "[✓] Managed skills", "a destination switch keeps the choices")

	// The review step releases the global keys too.
	h.press("enter")
	require.Equal(t, setupStepPreview, wz.step)
	assert.False(t, s.capturing())

	h.press("?")
	assert.True(t, h.m.help)

	h.press("esc", "1", "2")
	assert.Equal(t, setupStepPreview, wz.step, "the reviewed plan survives a destination switch")
	assert.Contains(t, h.view(), "╭─ Plan ─")

	// n steps back to the choices.
	h.press("n")
	assert.Equal(t, setupStepChoices, wz.step)

	h.press("q")
	assert.True(t, h.quit)
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "global keys never apply")
}

func TestSetupGlobalKeysActOnTheClientStep(t *testing.T) {
	f, _ := setupFake()
	h := newHarness(t, f, 120, 32)
	h.press("2")
	h.send(navigateMsg{dest: destIntegrations, intent: "setup"})

	s := setupScreen(h)
	require.Equal(t, setupStepClient, s.wizard.step)
	assert.False(t, s.capturing())

	h.press("?")
	assert.True(t, h.m.help)

	h.press("esc", "r")
	require.NotNil(t, s.wizard, "refresh keeps the wizard")
	assert.Equal(t, setupStepClient, s.wizard.step)

	h.press("esc")
	assert.Nil(t, s.wizard, "esc still closes the wizard from the client step")
	assert.Empty(t, f.called())
}

func TestSetupOnlyTheApplyStepCapturesGlobalKeys(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	s := setupScreen(h)
	wz := s.wizard
	require.Equal(t, setupStepPreview, wz.step)
	assert.False(t, s.capturing(), "the review has no dialog: it releases the global keys")

	// The apply step cannot stop safely, so it keeps the keys.
	wz.step = setupStepCommitting
	assert.True(t, s.capturing())
	assert.NotContains(t, h.view(), "? help", "the footer hides keys that do not act")

	h.press("?", "1", "q", "esc", "n")
	assert.False(t, h.m.help)
	assert.Equal(t, destIntegrations, h.m.active)
	assert.False(t, h.quit)
	assert.Equal(t, setupStepCommitting, wz.step, "no key leaves a running apply")
}

func TestSetupEnterNeverApplies(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)

	h.press("enter", "enter", "enter")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "enter on the review writes nothing")
	assert.Equal(t, setupStepPreview, setupScreen(h).wizard.step)

	h.press("a")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "the old apply key does nothing")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup", "ApplySetup"}, f.called(), "only y applies")
}

func TestSetupReviewKeysNameWhatTheRowShows(t *testing.T) {
	f, _ := setupFake()
	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	// The wide review shows both boxes, so it has no open key.
	frame := h.view()
	assert.Contains(t, frame, "↑↓ file")
	assert.Contains(t, frame, "pgup/pgdn scroll")
	assert.Contains(t, frame, "y apply")
	assert.Contains(t, frame, "n back")
	assert.NotContains(t, frame, "enter diff")

	// The narrow review names what enter shows for the selected row.
	h.resize(80, 24)
	assert.Contains(t, h.view(), "enter diff", "a planned write has a diff")

	h.press("end")
	assert.Contains(t, h.view(), "enter details", "a skill directory has no diff")

	h.press("home", "down", "down", "down")
	assert.Contains(t, h.view(), "enter details", "a kept file has no diff")
}

func TestSetupReviewUnreadableWriteOffersDetailsNotDiff(t *testing.T) {
	f, _ := setupFake()
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		p := setupPlanFor(setups)
		p.Writes[0].Err = assert.AnError
		p.Writes[0].Stale = true

		return p, nil
	}

	h := setupOpen(t, f, 120, 32)
	h.press("enter")

	frame := h.view()
	assert.NotEmpty(t, setupLineWith(frame, "! .codex/config.toml", "unreadable"))
	assert.Contains(t, frame, "The current file cannot be read")
	assert.Contains(t, setupBoxText(frame, 0, 42), "! A current file cannot be read. Fix it, then press p to plan again.")

	h.resize(80, 24)
	assert.Contains(t, h.view(), "enter details", "an unreadable file shows no diff")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup"}, f.called())
}

func TestSetupASCIIThemeRendersOnlyASCII(t *testing.T) {
	f, _ := setupFake()

	insps := integMatrix()
	codex := &insps[1]
	codex.Capabilities[1].State = integration.StateCurrent // MCP registration: a kept row
	codex.Capabilities[3].State = integration.StatePartial // edit lessons hook: a repair row
	f.integrations = insps

	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		p := setupPlanFor(setups)
		p.Writes[1].Stale = true

		return p, nil
	}

	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		return integration.SetupResult{Ops: []integration.OpResult{
			{Path: ".codex/hooks.json", Status: integration.OpApplied, Created: true, Consumers: []string{"codex"}},
		}}, nil
	}

	h := newHarness(t, f, 120, 32)
	h.m.st.theme = ui.NewTheme(true)
	h.press("2")

	s := setupScreen(h)

	// check renders the content, the notice row and the footer of the
	// current step at a wide and a narrow size, because each size has
	// its own strings.
	check := func(step string) {
		t.Helper()

		for _, size := range [][2]int{{118, 27}, {58, 13}} {
			h.m.st.width = size[0]

			text := ansi.Strip(s.view(size[0], size[1])) + "\n" + ansi.Strip(h.m.st.theme.Hints(s.keys()))
			if banner, ok := s.banner(size[0]); ok {
				text += "\n" + ansi.Strip(banner)
			}

			for _, r := range text {
				require.Less(t, r, rune(128), "the ASCII theme renders %q on the %s step:\n%s", r, step, text)
			}
		}

		h.m.st.width = 120
	}

	check("integrations")

	h.send(navigateMsg{dest: destIntegrations, intent: "setup"})
	check("client")

	h.press("down", "enter")
	require.Contains(t, h.view(), "[=] MCP registration")
	check("choose")

	// Uncheck the repair row and ask for a plan: the hint explains the
	// empty choice.
	h.press("down", "space", "enter")
	require.Contains(t, h.view(), "Select at least one addition")
	check("choose with nothing selected")

	h.press("space", "i")
	check("details")

	h.press("esc", "enter")
	require.Equal(t, setupStepPreview, s.wizard.step)

	for range setupEntries(s.wizard.preview) {
		check("review")
		h.press("down")
	}

	// The stale plan blocks apply; plan again with a clean plan.
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) { return setupPlanFor(setups), nil }

	h.press("p")
	require.True(t, s.wizard.canApply())
	check("review of a clean plan")

	h.press("y")
	require.Equal(t, setupStepResult, s.wizard.step)
	check("result")
}
