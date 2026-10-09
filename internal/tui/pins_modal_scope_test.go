package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests of the pinned scope summary of the maintenance preview. The
// modal never scrolls its tail, so a long summary must keep the actions,
// the bottom border, and the clause that says what y does. They hold
// review fixes. Each test drives the real model through the harness, so
// it proves the fix where the user sees it.

// Review fix: the scope summary of a modal never scrolls, so a long one
// pushed the actions and the bottom border out of the area.
func TestModalTailKeepsTheActionsAndTheBorder(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		th := newTheme(ascii)

		tail := make([]string, 30)
		for i := range tail {
			tail[i] = "scope line " + itoa(i+1)
		}

		o := modalOpts{
			Title: "Prune", Body: []string{"line 1", "line 2"}, Tail: tail,
			Actions: []key.Binding{withHelp(nkeys.Back, "esc", "cancel")},
		}

		// The modal area of a 60×20 frame is 19 rows above the footer.
		lines := strings.Split(ansi.Strip(th.modal(o, &scrollState{}, 60, 19)), "\n")
		n := len(lines)

		assert.LessOrEqual(t, n, 19-modalMargin, "the modal keeps its margin")
		assert.True(t, strings.HasPrefix(lines[n-1], th.Glyphs.CornerBL), "the bottom border stays: %q", lines[n-1])
		assert.Contains(t, lines[n-2], "esc cancel", "the actions stay last")
		assert.Contains(t, lines[n-3], th.Glyphs.Ellipsis, "a cut summary says that it goes on")
		assert.Contains(t, lines[1], "line 1", "one body row stays")
		assert.Contains(t, strings.Join(lines, "\n"), "scope line 1 ")
	}
}

// The same overflow end to end: a prune of 151 ledger rows names every
// row in its scope summary at 60×20.
func TestMaintPreviewOfAHugePruneKeepsItsActions(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintPrune(t, false)

	for i := range 150 {
		p.Supersede = append(p.Supersede, store.NewRowGuard(domain.Proposal{ID: int64(1000 + i)}))
	}

	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 60, 20)
	require.True(t, maintHostOf(h).open())

	assertFrame(t, h.view(), 60, 20)

	block, ok := h.m.modalBlock(60, 19)
	require.True(t, ok)

	lines := strings.Split(ansi.Strip(block), "\n")
	n := len(lines)
	g := h.m.st.theme.Glyphs

	assert.LessOrEqual(t, n, 19-modalMargin, "the modal keeps its margin")
	assert.True(t, strings.HasPrefix(lines[n-1], g.CornerBL), "the bottom border stays: %q", lines[n-1])
	assert.Contains(t, lines[n-2], "esc cancel", "the actions stay in the modal")
	assert.Contains(t, lines[n-2], "y prune")
	assert.Empty(t, maintMutations(f))
}

// The tests below cover items that join two parts of the console. So
// each test drives the real model through the harness, from the key
// that the user presses to the frame that the user sees.

// C0 item 3: the pinned scope summary of a prune names a bounded ID list,
// so the clause that says what y does stays visible at the minimum size.
func TestMaintPreviewOfAHugePruneKeepsTheScopeClause(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		fx := maintFixture(t)
		p := fx.maintPrune(t, false)

		for i := range 150 {
			p.Supersede = append(p.Supersede, store.NewRowGuard(domain.Proposal{ID: int64(1000 + i)}))
		}

		f := maintFakeOf(fx, p, nil)
		h := maintOpen(t, f, p.Request, 60, 20)
		h.m.st.theme = newTheme(ascii)
		require.True(t, maintHostOf(h).open())

		frame := h.view()
		assertFrame(t, frame, 60, 20)

		// maintShown drops the Unicode side borders; the ASCII border is
		// a plain bar.
		g := h.m.st.theme.Glyphs
		shown := lesFlat(strings.ReplaceAll(maintShown(h), "|", " "))
		assert.Contains(t, shown, "Prune 1 pin "+g.Sep+" p16, p1000, p1001, "+g.Ellipsis+" (+148) "+g.Arrow+
			" superseded "+g.Sep+" y writes lessons.yaml and the ledger", "ascii %v", ascii)
		assert.NotContains(t, shown, "p1149 "+g.Arrow, "the summary names no full list")
		assert.Contains(t, maintContent(t, h), "p1149", "the LEDGER section of the body lists every row")

		if ascii {
			for _, r := range ansi.Strip(frame) {
				require.Less(t, r, rune(128), "an ASCII frame has no wide glyph")
			}
		}

		assert.Empty(t, maintMutations(f))
	}
}

// C0 item 3: a short ID list stays whole in the summary.
func TestMaintTailIDListBoundsOnlyALongList(t *testing.T) {
	th := newTheme(false)

	assert.Equal(t, "p1, p2, p3", maintTailIDList(th, []int64{1, 2, 3}))
	assert.Equal(t, "p1, p2, p3, … (+1)", maintTailIDList(th, []int64{1, 2, 3, 4}))
	assert.Equal(t, "p1, p2, p3, ... (+2)", maintTailIDList(newTheme(true), []int64{1, 2, 3, 4, 5}))
	assert.Empty(t, maintTailIDList(th, nil))
	assert.False(t, strings.Contains(maintTailIDList(th, []int64{7}), ","))
}
