package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// shellBEmptyBody is an explanation that needs many lines at 58 cells.
const shellBEmptyBody = "Proposals come from review evidence in two CLI steps. The first command fetches " +
	"review comments through gh. The second invokes the configured agent to distill the evidence " +
	"into proposals. Nothing starts from this message; run the commands in a shell when you choose to."

// TestShellBEmptyKeepsTitleAndNextAction proves finding 12: when the
// block is taller than the area, the body gives up lines and the title
// and the next action stay visible.
func TestShellBEmptyKeepsTitleAndNextAction(t *testing.T) {
	th := newTheme(false)
	next := th.command("seamark index --reviews") + "\n" + th.command("seamark lessons --distill")

	for _, h := range []int{14, 9, 6, 4, 3} {
		out := ansi.Strip(th.empty(ToneNeutral, "No proposals yet", shellBEmptyBody, next, 58, h))
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
	out := ansi.Strip(th.empty(ToneNeutral, "No proposals yet", shellBEmptyBody, next, 58, 9))
	assert.Contains(t, out, "…")
	assert.NotContains(t, out, "choose to.")

	// A block that fits keeps the whole body.
	out = ansi.Strip(th.empty(ToneNeutral, "No proposals yet", shellBEmptyBody, next, 58, 20))
	assert.Contains(t, strings.Join(strings.Fields(out), " "), "when you choose to.")
}

// TestShellBEmptyClipsTheNextActionOnlyAtItsEnd proves the last resort:
// a next action taller than the area keeps its first lines, which hold
// the command.
func TestShellBEmptyClipsTheNextActionOnlyAtItsEnd(t *testing.T) {
	th := newTheme(false)
	next := th.command("seamark index") + "\n\n" + strings.Repeat("detail line\n", 10)

	out := ansi.Strip(th.empty(ToneAttention, "No index yet", shellBEmptyBody, next, 58, 4))
	lines := strings.Split(out, "\n")

	require.LessOrEqual(t, len(lines), 4)
	assert.Contains(t, out, "No index yet")
	assert.Contains(t, out, "seamark index")
}

// TestShellBEmptyStatesAtTheMinimumSize proves finding 12 through the
// shell: at 60x20 the Lessons empty states keep the commands that fill
// them. The check names the commands, not the headline copy, because
// the next action is the part that must never be clipped.
func TestShellBEmptyStatesAtTheMinimumSize(t *testing.T) {
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
