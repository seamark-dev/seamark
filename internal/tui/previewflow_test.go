package tui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of previewFlow and its two modals. The host tests of
// Proposals, Pins, Settings and the evidence preview cover the words
// and the keys of each flow. These tests cover the shared stage rules.

// flowTestPlan is the preview type of these tests.
type flowTestPlan struct{ name string }

// allFlowStages lists every stage, so a truth table covers each one.
var allFlowStages = []flowStage{flowClosed, flowPreparing, flowPreview, flowCommitting, flowResult}

// flowScroll is a scroll position with an offset: a 14-line body in a
// window of 5 lines, scrolled to line 3. Fit records the bounds, as a
// render does.
func flowScroll() ui.ScrollState {
	var sc ui.ScrollState
	sc.Fit(14, 5)
	sc.Off = 3

	return sc
}

// flowAt returns a flow in stage with a shown preview, a read failure
// and a scroll offset. Each test then sees what a call keeps or forgets.
func flowAt(stage flowStage) *previewFlow[flowTestPlan] {
	return &previewFlow[flowTestPlan]{
		stage:   stage,
		preview: &flowTestPlan{name: "old"},
		err:     errors.New("old failure"),
		scroll:  flowScroll(),
	}
}

func TestPreviewFlowZeroValueIsClosed(t *testing.T) {
	var f previewFlow[flowTestPlan]

	assert.Equal(t, flowClosed, f.stage)
	assert.False(t, f.active())
	assert.False(t, f.showsPreview())
	assert.False(t, f.beforeCommit())
}

func TestPreviewFlowStartReadForgetsThePreview(t *testing.T) {
	f := flowAt(flowResult)

	f.startRead()

	assert.Equal(t, flowPreparing, f.stage)
	assert.True(t, f.active())
	assert.Nil(t, f.preview)
	assert.NoError(t, f.err)
	assert.Equal(t, ui.ScrollState{}, f.scroll, "the read starts at the top")
}

func TestPreviewFlowReadDoneLandsOnlyInPreparing(t *testing.T) {
	plan := &flowTestPlan{name: "new"}

	for _, stage := range allFlowStages {
		f := flowAt(stage)
		before := *f

		landed := f.readDone(true, plan, nil, "empty")

		if stage == flowPreparing {
			assert.True(t, landed)
			assert.Equal(t, flowPreview, f.stage)
			assert.Same(t, plan, f.preview)
			assert.NoError(t, f.err)
			assert.Equal(t, ui.ScrollState{}, f.scroll, "the preview starts at the top")

			continue
		}

		assert.False(t, landed, "stage %d", stage)
		assert.Equal(t, before, *f, "a late read changes nothing in stage %d", stage)
	}
}

func TestPreviewFlowReadDoneDropsAnOldTicket(t *testing.T) {
	f := flowAt(flowPreparing)
	before := *f

	assert.False(t, f.readDone(false, &flowTestPlan{}, nil, "empty"))
	assert.Equal(t, before, *f, "the owner check decides first")
}

func TestPreviewFlowReadDoneShowsTheFailure(t *testing.T) {
	boom := errors.New("boom")

	f := flowAt(flowPreparing)
	require.True(t, f.readDone(true, nil, boom, "empty"))
	assert.Equal(t, flowPreview, f.stage)
	assert.Nil(t, f.preview)
	assert.Same(t, boom, f.err)
	assert.False(t, f.showsPreview())

	f = flowAt(flowPreparing)
	require.True(t, f.readDone(true, nil, nil, "the planner returned no plan"))
	require.Error(t, f.err, "an empty read is a failure, so the preview never shows nothing")
	assert.Equal(t, "the planner returned no plan", f.err.Error())
	assert.False(t, f.showsPreview())
}

func TestPreviewFlowShowsPreviewAndBeforeCommitByStage(t *testing.T) {
	for _, stage := range allFlowStages {
		f := &previewFlow[flowTestPlan]{stage: stage, preview: &flowTestPlan{}}

		assert.Equal(t, stage == flowPreview, f.showsPreview(), "showsPreview in stage %d", stage)
		assert.Equal(t, stage == flowPreparing || stage == flowPreview, f.beforeCommit(),
			"beforeCommit in stage %d", stage)
		assert.Equal(t, stage != flowClosed, f.active(), "active in stage %d", stage)
	}

	failed := &previewFlow[flowTestPlan]{stage: flowPreview, preview: &flowTestPlan{}, err: errors.New("boom")}
	assert.False(t, failed.showsPreview(), "a read failure shows no preview")

	empty := &previewFlow[flowTestPlan]{stage: flowPreview}
	assert.False(t, empty.showsPreview(), "a missing preview shows no preview")
}

func TestPreviewFlowStartCommitClaimsTheSlot(t *testing.T) {
	type doneMsg struct{}

	work := func() tea.Msg { return doneMsg{} }

	st := &state{}
	f := flowAt(flowPreview)
	f.err = nil

	cmd, ok := f.startCommit(st, "Saving settings", work)
	require.True(t, ok)
	require.NotNil(t, cmd)
	assert.Equal(t, doneMsg{}, cmd(), "the returned command is the commit work")
	assert.Equal(t, "Saving settings", st.mutation)
	assert.Equal(t, flowCommitting, f.stage)
	assert.Equal(t, flowScroll(), f.scroll, "the holder decides the scroll position")
	assert.NotNil(t, f.preview, "the commit still names the preview")

	busy := &state{mutation: "Pruning 1 pin"}
	g := flowAt(flowPreview)

	cmd, ok = g.startCommit(busy, "Saving settings", work)
	assert.False(t, ok)
	assert.Nil(t, cmd)
	assert.Equal(t, "Pruning 1 pin", busy.mutation, "the running mutation keeps the slot")
	assert.Equal(t, flowPreview, g.stage, "a refused commit keeps the preview")
}

func TestPreviewFlowCommitDoneOnlyFromCommitting(t *testing.T) {
	for _, stage := range allFlowStages {
		f := flowAt(stage)
		before := *f

		done := f.commitDone()

		if stage == flowCommitting {
			assert.True(t, done)
			assert.Equal(t, flowResult, f.stage)
			assert.Nil(t, f.preview, "the holder keeps the outcome, not the preview")
			assert.NoError(t, f.err)
			assert.Equal(t, ui.ScrollState{}, f.scroll)

			continue
		}

		assert.False(t, done, "stage %d", stage)
		assert.Equal(t, before, *f, "a commit result outside Committing changes nothing in stage %d", stage)
	}
}

func TestPreviewFlowToResultAndCloseForgetThePreview(t *testing.T) {
	f := flowAt(flowPreview)
	f.toResult()

	assert.Equal(t, flowResult, f.stage, "toResult has no stage check")
	assert.Nil(t, f.preview)
	assert.NoError(t, f.err)
	assert.Equal(t, ui.ScrollState{}, f.scroll)

	g := flowAt(flowResult)
	g.close()

	assert.Equal(t, flowClosed, g.stage)
	assert.False(t, g.active())
	assert.Nil(t, g.preview)
	assert.NoError(t, g.err)
	assert.Equal(t, ui.ScrollState{}, g.scroll)
}

func TestFlowWaitModal(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		st := &state{theme: ui.NewTheme(ascii), tick: 2}
		th := st.theme

		read := flowWaitModal(st, "Preparing the save preview", "Seamark reads the file. Nothing is written.", true, 60)

		assert.Equal(t, th.SpinnerFrame(2)+" Preparing the save preview"+th.Glyphs.Ellipsis, ansi.Strip(read.Title))
		assert.Equal(t, []string{"Seamark reads the file. Nothing is written."}, stripLines(read.Body))
		require.Len(t, read.Actions, 1, "a read offers esc")
		assert.Equal(t, ui.Nav.Back.Keys(), read.Actions[0].Keys())
		assert.Equal(t, "esc", read.Actions[0].Help().Key)
		assert.Equal(t, "cancel", read.Actions[0].Help().Desc)

		commit := flowWaitModal(st, "Saving settings", "Seamark writes the file.", false, 60)

		assert.Equal(t, th.SpinnerFrame(2)+" Saving settings"+th.Glyphs.Ellipsis, ansi.Strip(commit.Title))
		assert.Nil(t, commit.Actions, "a commit cannot stop, so it offers no key")
		assert.Empty(t, commit.Right)
		assert.Nil(t, commit.Tail)
	}
}

func TestPreviewFailedModal(t *testing.T) {
	th := ui.NewTheme(true)
	actions := []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "back to the list")}
	glyph := th.ToneGlyph(ui.ToneFailure)

	withHint := previewFailedModal(th, "The apply preview", errors.New("boom"), "Fix the cause.",
		"Nothing was changed.", actions, 60)

	assert.Equal(t, "Preview failed", ansi.Strip(withHint.Title))
	assert.Equal(t, []string{
		glyph + " The apply preview could not be built: boom",
		"",
		"Fix the cause.",
		"",
		"Nothing was changed.",
	}, stripLines(withHint.Body))
	assert.Equal(t, actions, withHint.Actions)

	noHint := previewFailedModal(th, "The save preview", errors.New("boom"), "", "Nothing was written.", nil, 60)

	assert.Equal(t, []string{
		glyph + " The save preview could not be built: boom",
		"",
		"Nothing was written.",
	}, stripLines(noHint.Body), "without a hint the cause is the only advice")
	assert.Nil(t, noHint.Actions)
}

// stripLines returns lines without escape sequences.
func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}

	return out
}

// TestPreviewFlowHoldersShadowNothing reads the syntax trees of the
// package. A holder that embeds previewFlow must not declare a field or
// a method with the name of a previewFlow member. Go embedding has no
// virtual dispatch, so previewFlow would keep its own member, and the
// holder would read another value than the flow.
func TestPreviewFlowHoldersShadowNothing(t *testing.T) {
	scanEmbedding(t).assertShadowsNothing(t, "previewFlow",
		[]string{"stage", "preview", "err", "scroll", "readDone", "close"},
		[]string{"propDecision", "maintModal", "settingsFlow", "evPreview", "pinReview"})
}
