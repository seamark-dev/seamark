package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the domain widgets: the radio glyphs, the phase rows, the
// activity and effect lines, and the empty block with a command. Most
// tests call the widget directly, because many screens draw the same
// widget. The tests of the primitives live in package ui.

func TestRadioGlyphs(t *testing.T) {
	u := ui.NewTheme(false)
	assert.Equal(t, "(●) codex", ansi.Strip(radio(u, true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(radio(u, false, "claude")))

	a := ui.NewTheme(true)
	assert.Equal(t, "(*) codex", ansi.Strip(radio(a, true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(radio(a, false, "claude")))

	assert.Equal(t, "( ) a b", ansi.Strip(radio(u, false, "a\nb")), "a label stays on one line")
}

func TestStatsAndRunBoxesPageTheirBodyTheSameWay(t *testing.T) {
	st := &state{theme: ui.NewTheme(false)}
	stats := newStatsView(st)
	run := &evRun{st: st}

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + strconv.Itoa(i+1)
	}

	o := ui.BoxOpts{W: 30, H: 10, Title: "Box"}

	render := func() []string {
		a := stats.scrollBox(statsPaneLessons, o, body)
		b := run.scrollBox(0, o, body)
		require.Equal(t, a, b, "the Stats box and the run box render the same frame")

		return strings.Split(ansi.Strip(a), "\n")
	}

	page := func() {
		require.True(t, stats.scroll[statsPaneLessons].Key(keyMsg("pgdown")))
		require.True(t, run.scroll[0].Key(keyMsg("pgdown")))
	}

	lines := render()
	require.Len(t, lines, 10)
	assert.Contains(t, lines[1], "line 1 ")
	assert.Contains(t, lines[8], "line 8 ")
	assert.Contains(t, lines[9], "1–8 of 30")

	page()

	lines = render()
	assert.Contains(t, lines[1], "line 9 ")
	assert.Contains(t, lines[9], "9–16 of 30", "a page moves by the 8 rows of the box")

	page()
	page()

	lines = render()
	assert.Contains(t, lines[1], "line 23")
	assert.Contains(t, lines[8], "line 30")
	assert.Contains(t, lines[9], "23–30 of 30", "the offset stops at the last page")

	// A body that fits shows no position and moves back to the top.
	lines = strings.Split(ansi.Strip(stats.scrollBox(statsPaneLessons, o, body[:5])), "\n")
	assert.Contains(t, lines[1], "line 1 ")
	assert.NotContains(t, lines[9], "of 5")
	assert.Equal(t, stats.scrollBox(statsPaneLessons, o, body[:5]), run.scrollBox(0, o, body[:5]))
}

func TestPhaseRows(t *testing.T) {
	th := ui.NewTheme(false)

	rows := phaseRows(th, []phaseRow{
		{label: "Revalidate the plan", state: phaseDone},
		{label: "Send groups", note: "3 of 10 done", state: phaseStopped},
		{label: "Remove stale pending", state: phaseSkipped},
		{label: "Report", note: "store locked", state: phaseFailed},
		{label: "Wait", state: phasePending},
		{label: "Run", state: phaseActive},
	}, 0, 80)

	plain := make([]string, len(rows))
	for i, r := range rows {
		plain[i] = strings.TrimRight(ansi.Strip(r), " ")
	}

	assert.Equal(t, []string{
		"✓ Revalidate the plan",
		"! Send groups           stopped, 3 of 10 done",
		"· Remove stale pending  skipped",
		"! Report                failed, store locked",
		"· Wait",
		"○ Run",
	}, plain)
}

func TestActivityLinesStayBoundedAndSafe(t *testing.T) {
	th := ui.NewTheme(false)

	lines := activityLines(th, []string{
		"first",
		"fetch page 2\x1b[31m red",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 used, then a very long tail of text",
	}, 2, 40)

	require.Len(t, lines, 2, "only the last lines stay")

	joined := ansi.Strip(strings.Join(lines, "\n"))
	assert.NotContains(t, joined, "first")
	assert.NotContains(t, joined, "\x1b", "control sequences stay inert")
	assert.NotContains(t, joined, "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "secrets are redacted")

	for _, l := range lines {
		assert.LessOrEqual(t, ui.Width(l), 40)
	}

	assert.Len(t, activityLines(th, []string{"a", "b", "c"}, 0, 40), 3, "keep 0 keeps every line")
}

func TestEffectLines(t *testing.T) {
	th := ui.NewTheme(false)

	lines := effectLines(th, []effect{{glyph: "+", tone: ui.ToneSuccess, text: "write .seamark/config.yaml"}},
		[]string{"run a model"}, 60)

	assert.Equal(t, []string{"", "WILL", "+ write .seamark/config.yaml", "", "WON'T", "· run a model"},
		strings.Split(ansi.Strip(strings.Join(lines, "\n")), "\n"))
	assert.Empty(t, effectLines(th, nil, nil, 60))
}

// longEmptyBody is an explanation that needs many lines at 58 cells.
const longEmptyBody = "Proposals come from review evidence in two CLI steps. The first command fetches " +
	"review comments through gh. The second invokes the configured agent to distill the evidence " +
	"into proposals. Nothing starts from this message; run the commands in a shell when you choose to."

// TestEmptyKeepsTitleAndNextAction proves finding 12: when the
// block is taller than the area, the body gives up lines and the title
// and the next action stay visible.
func TestEmptyKeepsTitleAndNextAction(t *testing.T) {
	th := ui.NewTheme(false)
	next := commandText(th, "seamark index --reviews") + "\n" + commandText(th, "seamark lessons --distill")

	for _, h := range []int{14, 9, 6, 4, 3} {
		out := ansi.Strip(th.Empty(ui.ToneNeutral, "No proposals yet", longEmptyBody, next, 58, h))
		lines := strings.Split(out, "\n")

		require.LessOrEqual(t, len(lines), h, "height %d: the block is taller than the area", h)

		for _, l := range lines {
			require.LessOrEqual(t, lipgloss.Width(l), 58, "height %d: line is too wide: %q", h, l)
		}

		assert.Contains(t, out, "No proposals yet", "height %d: the title is hidden", h)
		assert.Contains(t, out, "seamark index --reviews", "height %d: the next action is hidden", h)
		assert.Contains(t, out, "seamark lessons --distill", "height %d: the next action is hidden", h)
	}

	// A clipped body ends with the ellipsis, so the cut is visible.
	out := ansi.Strip(th.Empty(ui.ToneNeutral, "No proposals yet", longEmptyBody, next, 58, 9))
	assert.Contains(t, out, "…")
	assert.NotContains(t, out, "choose to.")

	// A block that fits keeps the whole body.
	out = ansi.Strip(th.Empty(ui.ToneNeutral, "No proposals yet", longEmptyBody, next, 58, 20))
	assert.Contains(t, strings.Join(strings.Fields(out), " "), "when you choose to.")
}

// TestEmptyClipsTheNextActionOnlyAtItsEnd proves the last resort:
// a next action taller than the area keeps its first lines, which hold
// the command.
func TestEmptyClipsTheNextActionOnlyAtItsEnd(t *testing.T) {
	th := ui.NewTheme(false)
	next := commandText(th, "seamark index") + "\n\n" + strings.Repeat("detail line\n", 10)

	out := ansi.Strip(th.Empty(ui.ToneAttention, "No index yet", longEmptyBody, next, 58, 4))
	lines := strings.Split(out, "\n")

	require.LessOrEqual(t, len(lines), 4)
	assert.Contains(t, out, "No index yet")
	assert.Contains(t, out, "seamark index")
}

// TestEmptyStatesAtTheMinimumSize proves finding 12 through the
// shell: at 60x20 the Lessons empty states keep the commands that fill
// them. The check names the commands, not the headline copy, because
// the next action is the part that must never be clipped.
func TestEmptyStatesAtTheMinimumSize(t *testing.T) {
	f := newFake()
	f.ledger = &proposals.Ledger{}

	h := newHarness(t, f, ui.MinWidth, ui.MinHeight)
	h.press("3")

	frame := h.view()
	assertFrame(t, frame, ui.MinWidth, ui.MinHeight)
	assert.Contains(t, frame, "seamark lessons --distill")

	f2 := newFake()
	f2.ledgerErr = services.ErrNoLedger

	h2 := newHarness(t, f2, ui.MinWidth, ui.MinHeight)
	h2.press("3")

	frame = h2.view()
	assertFrame(t, frame, ui.MinWidth, ui.MinHeight)
	assert.Contains(t, frame, "seamark index")
	assert.NotContains(t, frame, "0 pending", "an unreadable ledger never renders as zero")
}
