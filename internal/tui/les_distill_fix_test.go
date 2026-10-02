package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
)

// This file holds the regression tests of the whole-phase review of the
// distillation record: the custom limit input, the refusal messages,
// the full finding IDs, and the plan wording.

func TestDistillScopeCustomLimitWaitsForInput(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	// A just opened input shows its placeholder and no error.
	h.press("down", "down", "space")
	require.True(t, rec.form.editCustom)

	frame := h.view()
	assert.Contains(t, frame, "custom: _ a whole number above 0")
	assert.NotContains(t, frame, "Enter a whole number above 0.", "nothing is typed yet")

	// An empty input that closes shows the rule.
	h.press("enter")
	assert.Contains(t, h.view(), "Enter a whole number above 0.")

	// A new edit starts without the error again.
	h.press("space")
	assert.NotContains(t, h.view(), "Enter a whole number above 0.")

	// One group reads in the singular, as everywhere else.
	h.typeText("1")
	h.press("enter")

	frame = h.view()
	assert.Contains(t, frame, "custom: 1 group")
	assert.NotContains(t, frame, "1 groups")
	assert.NotContains(t, frame, "Enter a whole number above 0.")

	rec.form.custom.set("12")
	assert.Contains(t, h.view(), "custom: 12 groups")

	// A plan request with an empty value shows the rule, too.
	h.press("space", "backspace", "backspace", "esc")
	rec.form.limit = distLimitCustom
	rec.form.custom.set("")
	rec.form.customChecked = false
	h.press("enter")
	assert.Contains(t, h.view(), "Enter a whole number above 0.")
	assert.Empty(t, f.called(), "an invalid limit never plans")
}

func TestDistillRefusedPlanMessageClearsAfterAPlan(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)

	h.press("/")
	h.typeText("../tmp")
	h.press("enter", "enter")
	require.Equal(t, "The region is outside the workspace.", h.m.toast.text)

	// A valid region plans, and the refusal no longer shows beside it.
	h.press("/")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.press("enter", "enter")
	require.Equal(t, distReview, distRecOf(t, h).step)
	assert.Empty(t, h.m.toast.text, "the stale refusal is gone")
	assert.NotContains(t, h.view(), "outside the workspace")
}

func TestDistillRefusedStartNamesTheCauseOnce(t *testing.T) {
	var calls []distRunCall

	stale := fmt.Errorf("%w: %s changed since the preview", distill.ErrStale, ".seamark/config.yaml")

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, func(*learning.DistillPreview) *distill.Execution { return nil }, stale)

	h := distOpen(t, f, 80, 60)
	h.press("enter", "y")

	assert.Equal(t, "The distillation did not start: .seamark/config.yaml changed since the preview.", h.m.toast.text)

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "The plan is out of date: .seamark/config.yaml changed since the preview. Nothing was sent.")
	assert.NotContains(t, flat, "changed since the plan:", "the cause is not said twice")

	// A new plan clears the refusal, so it never shows beside the run.
	h.press("enter")
	assert.Empty(t, h.m.toast.text)

	rec := distRecOf(t, h)
	rec.warned = time.Now()
	require.NotNil(t, rec.start())
	assert.True(t, rec.warned.IsZero(), "a start clears a recent refusal")
}

func TestDistillSettleClearsOnlyARecentRefusal(t *testing.T) {
	h := distOpen(t, distFake(distFullPreview(), nil), 120, 40)
	rec := distRecOf(t, h)

	assert.Nil(t, rec.settle(), "no refusal, nothing to clear")

	rec.warned = time.Now().Add(-distWarnTTL - time.Second)
	assert.Nil(t, rec.settle(), "an expired refusal is gone, and a clear would remove a newer message")

	rec.warned = time.Now()
	cleared := rec.settle()
	require.NotNil(t, cleared)
	assert.Equal(t, toastMsg{tone: ToneNeutral}, cleared())
	assert.Nil(t, rec.settle(), "one clear per refusal")
}

func TestDistillEvidenceShowsTheFullFindingIDs(t *testing.T) {
	distFixedDir(t, "/work/repo")

	// Real fix IDs have 19 digits, and review IDs share their first digits.
	p := distFullPreview()
	ev := p.Plan.Preflight.Groups[0].Evidence
	ev[0].ID, ev[1].ID, ev[2].ID = 3997123456, 3997123999, 1714358230123456789

	want := []string{"#3997123456", "#3997123999", "#1714358230123456789"}

	for _, c := range []struct {
		w, h int
		keys []string
	}{
		{120, 40, nil}, {80, 40, []string{"tab"}}, {60, 20, []string{"tab"}},
	} {
		h := distOpen(t, distFake(p, distNoRun(t)), c.w, c.h)
		h.press("enter")
		h.press(c.keys...)

		frame := h.view()
		assertFrame(t, frame, c.w, c.h)

		for _, id := range want {
			assert.Contains(t, frame, id+" ", "size %dx%d shows the full ID", c.w, c.h)
		}
	}

	// The rows keep the file name of the path and fit the evidence width
	// of the 60, 120 and 80 column layouts.
	for _, w := range []int{50, 61, 70} {
		for _, line := range distEvidenceLines(newTheme(false), ev, w+6, 6) {
			assert.LessOrEqual(t, width(line), w+6)
			assert.True(t, strings.HasSuffix(strings.TrimRight(line, " "), ".go"), line)
		}
	}
}

func TestDistillPlanWordsACustomAgentAndOneCleanup(t *testing.T) {
	distFixedDir(t, "/work/repo")

	p := distFullPreview()
	p.Invocation = agent.CommandSpec{Name: "custom", Argv: []string{"my-agent", "--json"}}
	p.Plan.Cleanup = p.Plan.Cleanup[:1]

	h := distOpen(t, distFake(p, distNoRun(t)), 80, 80)
	h.press("enter")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "send 6 groups to the custom agent.argv command, one call each")
	assert.Contains(t, flat, "first remove the stale pending p4: its evidence group is gone")

	// A cleanup-only plan offers one row in the singular.
	one := distPreview(distPreviewOpts{skipped: 14, cleanup: true})
	one.Plan.Cleanup = one.Plan.Cleanup[:1]

	h2 := distOpen(t, distFake(one, distNoRun(t)), 80, 60)
	h2.press("enter")

	flat = lesBoxFlat(h2.view())
	assert.Contains(t, flat, "Its evidence group no longer exists, so a run would remove it first. c removes it after")
	assert.False(t, strings.Contains(flat, "Their evidence group"), flat)
}
