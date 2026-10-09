package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// The tests of the shared widgets: the radio glyphs, the scrolling
// modal, the phase rows, the activity and effect lines, and the empty
// block. Most tests call the widget directly, because many screens draw
// the same widget. input_test.go holds the tests of the text input.

func TestRadioGlyphs(t *testing.T) {
	u := newTheme(false)
	assert.Equal(t, "(●) codex", ansi.Strip(u.radio(true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(u.radio(false, "claude")))

	a := newTheme(true)
	assert.Equal(t, "(*) codex", ansi.Strip(a.radio(true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(a.radio(false, "claude")))

	assert.Equal(t, "( ) a b", ansi.Strip(u.radio(false, "a\nb")), "a label stays on one line")
}

func TestModalScrollsItsBodyAndKeepsTheActions(t *testing.T) {
	th := newTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + itoa(i+1)
	}

	var sc scrollState

	o := modalOpts{
		Title: "Preview", Right: "draft", Body: body,
		Actions: []key.Binding{withHelp(nkeys.Back, "esc", "close")},
	}

	lines := strings.Split(ansi.Strip(th.modal(o, &sc, 120, 20)), "\n")

	// 20 rows lose the margin, the border and the three action rows.
	require.Len(t, lines, 13+3+2)
	assert.Equal(t, 80, width(lines[0]), "the modal is 80 cells wide at 120 columns")
	assert.Contains(t, lines[0], "Preview")
	assert.Contains(t, lines[0], "draft")
	assert.Contains(t, lines[1], "line 1 ")
	assert.Contains(t, lines[len(lines)-2], "esc close", "the actions stay under the body")
	assert.Contains(t, lines[len(lines)-1], "1–13 of 30")
	assert.Equal(t, 13, sc.page)
	assert.Equal(t, 17, sc.last)

	sc.by(100)
	lines = strings.Split(ansi.Strip(th.modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[13], "line 30", "the offset stops at the last page")
	assert.Contains(t, lines[len(lines)-1], "18–30 of 30")

	// A smaller floor gives a narrower modal on a mid-size terminal.
	o.MinW = 64
	assert.Equal(t, 64, width(strings.Split(ansi.Strip(th.modal(o, nil, 90, 20)), "\n")[0]))
	assert.Equal(t, 76, modalBodyWidth(0, 120), "the body width is the inner width of the box")
}

func TestStatsAndRunBoxesPageTheirBodyTheSameWay(t *testing.T) {
	st := &state{theme: newTheme(false)}
	stats := newStatsView(st)
	run := &evRun{st: st}

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + itoa(i+1)
	}

	o := boxOpts{W: 30, H: 10, Title: "Box"}

	render := func() []string {
		a := stats.scrollBox(statsPaneLessons, o, body)
		b := run.scrollBox(0, o, body)
		require.Equal(t, a, b, "the Stats box and the run box render the same frame")

		return strings.Split(ansi.Strip(a), "\n")
	}

	page := func() {
		require.True(t, stats.scroll[statsPaneLessons].key(keyMsg("pgdown")))
		require.True(t, run.scroll[0].key(keyMsg("pgdown")))
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
	th := newTheme(false)

	rows := th.phaseRows([]phaseRow{
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
	th := newTheme(false)

	lines := th.activityLines([]string{
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
		assert.LessOrEqual(t, width(l), 40)
	}

	assert.Len(t, th.activityLines([]string{"a", "b", "c"}, 0, 40), 3, "keep 0 keeps every line")
}

func TestEffectLines(t *testing.T) {
	th := newTheme(false)

	lines := th.effectLines([]effect{{glyph: "+", tone: ToneSuccess, text: "write .seamark/config.yaml"}},
		[]string{"run a model"}, 60)

	assert.Equal(t, []string{"", "WILL", "+ write .seamark/config.yaml", "", "WON'T", "· run a model"},
		strings.Split(ansi.Strip(strings.Join(lines, "\n")), "\n"))
	assert.Empty(t, th.effectLines(nil, nil, 60))
}

// longEmptyBody is an explanation that needs many lines at 58 cells.
const longEmptyBody = "Proposals come from review evidence in two CLI steps. The first command fetches " +
	"review comments through gh. The second invokes the configured agent to distill the evidence " +
	"into proposals. Nothing starts from this message; run the commands in a shell when you choose to."

// TestEmptyKeepsTitleAndNextAction proves finding 12: when the
// block is taller than the area, the body gives up lines and the title
// and the next action stay visible.
func TestEmptyKeepsTitleAndNextAction(t *testing.T) {
	th := newTheme(false)
	next := th.command("seamark index --reviews") + "\n" + th.command("seamark lessons --distill")

	for _, h := range []int{14, 9, 6, 4, 3} {
		out := ansi.Strip(th.empty(ToneNeutral, "No proposals yet", longEmptyBody, next, 58, h))
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
	out := ansi.Strip(th.empty(ToneNeutral, "No proposals yet", longEmptyBody, next, 58, 9))
	assert.Contains(t, out, "…")
	assert.NotContains(t, out, "choose to.")

	// A block that fits keeps the whole body.
	out = ansi.Strip(th.empty(ToneNeutral, "No proposals yet", longEmptyBody, next, 58, 20))
	assert.Contains(t, strings.Join(strings.Fields(out), " "), "when you choose to.")
}

// TestEmptyClipsTheNextActionOnlyAtItsEnd proves the last resort:
// a next action taller than the area keeps its first lines, which hold
// the command.
func TestEmptyClipsTheNextActionOnlyAtItsEnd(t *testing.T) {
	th := newTheme(false)
	next := th.command("seamark index") + "\n\n" + strings.Repeat("detail line\n", 10)

	out := ansi.Strip(th.empty(ToneAttention, "No index yet", longEmptyBody, next, 58, 4))
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

	h := newHarness(t, f, minWidth, minHeight)
	h.press("3")

	frame := h.view()
	assertFrame(t, frame, minWidth, minHeight)
	assert.Contains(t, frame, "seamark lessons --distill")

	f2 := newFake()
	f2.ledgerErr = ErrNoLedger

	h2 := newHarness(t, f2, minWidth, minHeight)
	h2.press("3")

	frame = h2.view()
	assertFrame(t, frame, minWidth, minHeight)
	assert.Contains(t, frame, "seamark index")
	assert.NotContains(t, frame, "0 pending", "an unreadable ledger never renders as zero")
}
