package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

// frPreview is the plan of the shared documents in a fresh repository:
// three new starter files and a changed .gitignore.
func frPreview() *SetupPreview {
	return &SetupPreview{
		Plan: &integration.SetupPlan{Kept: []integration.FileKeep{}},
		Writes: []integration.WritePreview{
			{Path: ".seamark/policy.yaml", Created: true, Added: 40, Detail: "starter"},
			{Path: ".seamark/lessons.yaml", Created: true, Added: 20, Detail: "starter"},
			{Path: ".seamark/config.yaml", Created: true, Added: 30, Detail: "starter"},
			{Path: ".gitignore", Added: 5, Removed: 1, Detail: "seamark carve-outs"},
		},
	}
}

// frApplied reports every write of frPreview as applied.
func frAppliedResult() integration.SetupResult {
	var ops []integration.OpResult
	for _, w := range frPreview().Writes {
		ops = append(ops, integration.OpResult{Path: w.Path, Status: integration.OpApplied, Created: w.Created})
	}

	return integration.SetupResult{Ops: ops}
}

// frFake returns a fresh workspace whose plan is frPreview. It records
// the client list that each plan call receives.
func frFake(planned *[][]integration.ClientSetup) *fakeServices {
	f := newFake()
	f.fresh = true
	f.agentsOnPath = []string{"claude", "codex"}
	f.planSetup = func(setups []integration.ClientSetup) (*SetupPreview, error) {
		if planned != nil {
			*planned = append(*planned, setups)
		}

		return frPreview(), nil
	}
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) { return frAppliedResult(), nil }

	return f
}

// frIntentScreen is a stub destination that records the intents it
// opens with, so a test sees what a first-run step asks for.
type frIntentScreen struct {
	stubScreen

	intents []string
}

func (s *frIntentScreen) open(intent string) tea.Cmd {
	s.intents = append(s.intents, intent)

	return nil
}

func TestFirstRunShowsTheThreeStepsOfTheDesign(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	require.NotNil(t, h.m.welcome)

	assert.Contains(t, frame, "╭─ Set up seamark in this repository ")
	assert.Contains(t, frame, "No .seamark/ directory in /work/repo.")
	assert.Regexp(t, `│ › 1  Create config +\.seamark/ policy, lessons, config · \.gitignore`, frame)
	assert.Regexp(t, `│   2  Build the index +local parse · git history`, frame)
	assert.Regexp(t, `│   3  Connect agents +found on PATH: claude, codex`, frame)
	assert.Contains(t, frame, "Each step shows its plan first. Nothing is written until you press y.")
	assert.Contains(t, frame, "enter start   v read-only view   q quit")

	// The box is 76 cells wide and centered, as in the 1a design.
	lines := strings.Split(frame, "\n")
	top := lines[lineIndex(lines, "Set up seamark")]
	assert.Equal(t, 22, strings.Index(top, "╭"), "the box starts at column 22")
	assert.Equal(t, 12, lineIndex(lines, "Set up seamark"), "the box starts at row 12")
	assert.Len(t, []rune(strings.TrimSpace(top)), 76)

	// The tabs are disabled: no active rule and no tab count.
	assert.Equal(t, "  1 Overview   2 Integrations   3 Lessons   4 Doctor   5 Settings", strings.TrimRight(lines[1], " "))
	assert.NotContains(t, lines[2], "━", "no tab is active while the guide shows")

	footer := footerLine(frame)
	assert.Contains(t, footer, "↑↓ step   enter start")
	assert.Contains(t, footer, "q quit")
	assert.NotContains(t, footer, "? help")
	assert.NotContains(t, footer, "r refresh")

	assert.Empty(t, f.called(), "showing the guide plans and writes nothing")
}

func TestFirstRunSaysWhenNoAgentIsOnPath(t *testing.T) {
	f := frFake(nil)
	f.agentsOnPath = nil

	h := newHarness(t, f, 120, 40)
	assert.Regexp(t, `3  Connect agents +no agent command on PATH`, h.view())
}

func TestFirstRunTabsAndGlobalKeysAreDisabled(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	h.press("2", "3", "4", "r", "?")
	assert.Equal(t, destOverview, h.m.active)
	assert.False(t, h.m.help)
	assert.NotNil(t, h.m.welcome)
	assert.Empty(t, f.called())
}

func TestFirstRunCreateConfigPlansThenAppliesOnY(t *testing.T) {
	var planned [][]integration.ClientSetup

	f := frFake(&planned)
	h := newHarness(t, f, 120, 40)

	h.press("enter")
	require.Equal(t, []string{"PlanSetup"}, f.called(), "enter plans first; nothing is written")
	require.Len(t, planned, 1)
	assert.Nil(t, planned[0], "the step plans the shared Seamark documents only")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "1  Create config  · plan")
	assert.Regexp(t, `\+ \.seamark/policy\.yaml +new · starter`, frame)
	assert.Regexp(t, `M \.gitignore +\+5 −1 · seamark carve-outs`, frame)
	assert.Contains(t, frame, "WON'T touch agent files or the index.")
	assert.Contains(t, frame, "Nothing is written until you press y.")
	assert.Contains(t, frame, "y apply   esc back   q quit")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup", "ApplySetup"}, f.called())
	assert.Empty(t, h.m.st.mutation, "the apply result frees the mutation slot")

	frame = h.view()
	assert.Contains(t, frame, "1  Create config  · result")
	assert.Regexp(t, `✓ applied +\.seamark/policy\.yaml`, frame)
	assert.Contains(t, frame, "The config is written. Next: build the index.")

	// Back at the steps, step 1 is done and the cursor is on step 2.
	h.press("enter")

	frame = h.view()
	assert.Regexp(t, `1  Create config +✓ done · 4 files written`, frame)
	assert.Regexp(t, `› 2  Build the index`, frame)
	assert.NotNil(t, h.m.welcome, "the guide stays for the next steps")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup", "ApplySetup"}, f.called(), "y does nothing outside a review")
}

func TestFirstRunEscFromTheReviewWritesNothing(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	h.press("enter", "esc")
	assert.Equal(t, frSteps, h.m.welcome.stage)

	h.press("enter", "n")
	assert.Equal(t, frSteps, h.m.welcome.stage, "n also goes back")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup", "PlanSetup"}, f.called(), "no apply without a review on screen")
}

func TestFirstRunDropsALatePlan(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)
	fr := h.m.welcome

	fr.stage = frSteps
	h.send(frPlanMsg{preview: frPreview()})
	assert.Equal(t, frSteps, fr.stage, "a plan after esc is not shown for review")
}

func TestFirstRunApplyWaitsForTheMutationSlot(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	h.press("enter")
	require.True(t, h.m.st.begin("Building the index"))

	h.press("y")
	assert.Equal(t, []string{"PlanSetup"}, f.called(), "a second mutation does not start")
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
	assert.Equal(t, frReview, h.m.welcome.stage)
}

func TestFirstRunStalePlanCannotApply(t *testing.T) {
	f := frFake(nil)
	f.planSetup = func([]integration.ClientSetup) (*SetupPreview, error) {
		p := frPreview()
		p.Writes[3].Stale = true

		return p, nil
	}

	h := newHarness(t, f, 120, 40)
	h.press("enter")

	frame := h.view()
	assert.Contains(t, frame, "! changed after planning")
	assert.Contains(t, frame, "A file changed after planning. esc and start again.")
	assert.NotContains(t, frame, "y apply")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup"}, f.called())
}

func TestFirstRunNothingToWriteOffersNoApply(t *testing.T) {
	f := frFake(nil)
	f.planSetup = nil // the fake plans no write

	h := newHarness(t, f, 120, 40)
	h.press("enter")

	frame := h.view()
	assert.Contains(t, frame, "Every shared document is present. Nothing to write.")
	assert.NotContains(t, frame, "y apply")

	h.press("y")
	assert.Equal(t, []string{"PlanSetup"}, f.called())
}

func TestFirstRunPlanFailureIsShown(t *testing.T) {
	f := frFake(nil)
	f.planSetup = func([]integration.ClientSetup) (*SetupPreview, error) {
		return nil, errors.New("read .gitignore: permission denied")
	}

	h := newHarness(t, f, 120, 40)
	h.press("enter")

	frame := h.view()
	assert.Contains(t, frame, "! Could not plan the config.")
	assert.Contains(t, frame, "read .gitignore: permission denied")
	assert.NotContains(t, frame, "y apply")
}

func TestFirstRunApplyFailureSaysWhatLanded(t *testing.T) {
	f := frFake(nil)
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		return integration.SetupResult{Ops: []integration.OpResult{
			{Path: ".seamark/policy.yaml", Status: integration.OpApplied, Created: true},
			{Path: ".seamark/lessons.yaml", Status: integration.OpFailed, Err: errors.New("disk full")},
			{Path: ".seamark/config.yaml", Status: integration.OpNotAttempted},
		}}, errors.New(".seamark/lessons.yaml: disk full")
	}

	h := newHarness(t, f, 120, 40)
	h.press("enter", "y")

	frame := h.view()
	assert.Regexp(t, `✓ applied +\.seamark/policy\.yaml`, frame)
	assert.Regexp(t, `! failed +\.seamark/lessons\.yaml`, frame)
	assert.Contains(t, frame, "disk full")
	assert.Regexp(t, `! not attempted +\.seamark/config\.yaml`, frame)
	assert.Contains(t, docFlat(frame), "Seamark does not roll back")
	assert.Empty(t, h.m.st.mutation)

	h.press("enter")
	assert.Equal(t, frStepConfig, h.m.welcome.cursor, "an incomplete step stays selected")
	assert.NotContains(t, h.view(), "✓ done")
}

func TestFirstRunStalePlanAtApplyWritesNothing(t *testing.T) {
	f := frFake(nil)
	f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
		return integration.SetupResult{}, integration.ErrStalePlan
	}

	h := newHarness(t, f, 120, 40)
	h.press("enter", "y")

	assert.Contains(t, docFlat(h.view()), "A file changed after the plan. Nothing was written.")
}

func TestFirstRunBuildStepOpensTheOverviewBuild(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	stub := &frIntentScreen{}
	h.m.screens[destOverview] = stub

	h.press("down", "enter")
	assert.Nil(t, h.m.welcome)
	assert.Equal(t, destOverview, h.m.active)
	assert.Equal(t, []string{"build"}, stub.intents)
	assert.Empty(t, f.called(), "the Overview asks before it builds")
}

func TestFirstRunConnectStepOpensTheSetupWizard(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	stub := &frIntentScreen{}
	h.m.screens[destIntegrations] = stub

	h.press("down", "down", "enter")
	assert.Nil(t, h.m.welcome)
	assert.Equal(t, destIntegrations, h.m.active)
	assert.Equal(t, []string{"setup"}, stub.intents)
	assert.Empty(t, f.called())
}

func TestFirstRunReadOnlyViewAndQuit(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	h.press("v")
	assert.Nil(t, h.m.welcome)
	assert.Contains(t, h.view(), "━", "the tabs work again")

	h.press("2")
	assert.Equal(t, destIntegrations, h.m.active)

	h2 := newHarness(t, frFake(nil), 120, 40)
	h2.press("q")
	assert.True(t, h2.quit)

	// esc also leaves the steps, as it closes every other layer.
	h3 := newHarness(t, frFake(nil), 120, 40)
	h3.press("esc")
	assert.Nil(t, h3.m.welcome)
}

func TestFirstRunQuitDuringTheApplyWaits(t *testing.T) {
	f := frFake(nil)
	h := newHarness(t, f, 120, 40)

	h.press("enter")

	// Start the apply without running its command, so the write is
	// still running when q arrives.
	cmd := h.m.welcome.apply()
	require.NotNil(t, cmd)
	require.Equal(t, frApplying, h.m.welcome.stage)

	h.press("v", "esc", "enter")
	assert.Equal(t, frApplying, h.m.welcome.stage, "the running write cannot be left")

	h.press("q")
	assert.False(t, h.quit)
	assert.True(t, h.m.quitConfirm)

	h.press("enter")
	assert.True(t, h.m.quitAfter)

	h.run(cmd)
	assert.True(t, h.quit, "the console exits after the write")
}

func TestFirstRunApplyResultFreesTheSlotWithoutTheGuide(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	require.Nil(t, h.m.welcome)
	require.True(t, h.m.st.begin(frMutation))

	h.send(frApplyMsg{res: frAppliedResult()})
	assert.Empty(t, h.m.st.mutation)
}

func TestFirstRunFreshCheckFailureIsANotice(t *testing.T) {
	f := newFake()
	f.freshErr = errors.New("stat .seamark: permission denied")

	h := newHarness(t, f, 120, 40)
	assert.Nil(t, h.m.welcome)
	assert.Contains(t, h.view(), "Could not check for existing setup: stat .seamark: permission denied")
}

func TestFirstRunFramesFitAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {200, 50}, {80, 24}, {minWidth, minHeight}} {
		w, hgt := size[0], size[1]
		h := newHarness(t, frFake(nil), w, hgt)

		frame := h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "enter start", "size %v: the key row stays", size)
		assert.Contains(t, frame, "3  Connect agents", "size %v", size)

		h.press("enter")

		frame = h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "y apply", "size %v: the review keeps its key row", size)
		assert.Contains(t, frame, "╰", "size %v", size)

		h.press("y")

		frame = h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "back to the steps", "size %v", size)
	}
}

func TestFirstRunKeysMatchTheStage(t *testing.T) {
	fr := &firstRun{st: &state{svc: newFake(), theme: newTheme(false)}}

	help := func(bs []key.Binding) []string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Help().Key+" "+b.Help().Desc)
		}

		return out
	}

	assert.Equal(t, []string{"↑↓ step", "enter start"}, help(fr.keys()))

	fr.stage = frReview
	fr.preview = frPreview()
	assert.Equal(t, []string{"y apply", "esc back"}, help(fr.keys()))

	fr.preview = &SetupPreview{Plan: &integration.SetupPlan{}}
	assert.Equal(t, []string{"esc back"}, help(fr.keys()), "no y without a write")

	fr.stage = frApplying
	assert.Empty(t, fr.keys())
}

// The tests below hold review fixes. Each test drives the real model
// through the harness, so it proves the fix where the user sees it.

// Review fix: a setup and the first-run guide create the starter
// lessons.yaml, but Pins kept "No pin file yet" until the user pressed r.
func TestSetupAndFirstRunApplyReloadThePins(t *testing.T) {
	assert.Contains(t, frRefreshKinds, secPins)

	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	h.press("m")
	require.Contains(t, h.view(), "No pin file yet")

	// The setup writes the pin file, so the next read finds it.
	f.pins = pinsInspection()
	reads := f.readCount("Pins")

	h.send(setupAppliedMsg{})
	assert.Equal(t, reads+1, f.readCount("Pins"), "a setup apply reads the pins again")
	assert.NotContains(t, h.view(), "No pin file yet")
	assert.Contains(t, h.view(), "wrap-sql-errors")

	// The result of the guide reloads the pins without the guide too.
	h.send(frApplyMsg{res: frAppliedResult()})
	assert.Equal(t, reads+2, f.readCount("Pins"), "a first-run apply reads the pins again")

	// The guide itself reloads them with its result.
	g := frFake(nil)
	hg := newHarness(t, g, 120, 40)
	require.NotNil(t, hg.m.welcome)

	reads = g.readCount("Pins")
	hg.send(frApplyMsg{res: frAppliedResult()})
	assert.Equal(t, reads+1, g.readCount("Pins"), "the guide reads the pins again")
}
