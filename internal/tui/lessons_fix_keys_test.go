package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// lesFixBinding returns the Lessons footer binding of one key.
func lesFixBinding(t *testing.T, h *harness, k string) key.Binding {
	t.Helper()

	for _, b := range h.m.screens[destLessons].keys() {
		if b.Help().Key == k {
			return b
		}
	}

	require.Fail(t, "no binding for key "+k)

	return key.Binding{}
}

// lesFixDetail returns the detail text of the proposal under the
// cursor on one line. The wide layout puts the list beside the detail,
// so a flattened frame would mix the two panes.
func lesFixDetail(h *harness) string {
	p := lesProps(h)
	cur, _ := p.current(p.rows())

	return lesFlat(ansi.Strip(strings.Join(p.detailLines(cur, 200), "\n")))
}

// TestLessonsFixRegionCursorVisibleWithoutColor covers finding 18: the
// test profile is Ascii, as NO_COLOR selects, so only a glyph can show
// the cursor inside the text.
func TestLessonsFixRegionCursorVisibleWithoutColor(t *testing.T) {
	th := newTheme(false)

	var in propInput

	in.set("internal")
	assert.Equal(t, "internal_", ansi.Strip(in.view(th)), "the end of the text shows an underscore")

	for range 3 {
		in.key(tea.KeyMsg{Type: tea.KeyLeft})
	}

	assert.Equal(t, "inter|nal", ansi.Strip(in.view(th)), "the bar marks where typing inserts")

	in.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	assert.Equal(t, "interX|nal", ansi.Strip(in.view(th)))

	in.key(tea.KeyMsg{Type: tea.KeyHome})
	assert.Equal(t, "|interXnal", ansi.Strip(in.view(th)))

	// The filter bar shows the same glyph.
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 32)
	h.press("/")
	h.typeText("internal")
	h.press("left", "left", "left")
	assert.Contains(t, h.view(), "region inter|nal")
}

// TestLessonsFixApplyLabelFollowsTheGate covers finding 19: with the
// gate known to be off, the footer, the help and the detail name the
// action "YAML", not "apply".
func TestLessonsFixApplyLabelFollowsTheGate(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false

	h := lesOpen(t, f, 120, 60)

	assert.Equal(t, "YAML", lesFixBinding(t, h, "a").Help().Desc)
	assert.Contains(t, h.view(), "a YAML")
	assert.NotContains(t, h.view(), "a apply")
	assert.Contains(t, lesFixDetail(h), "a shows the YAML block and d previews a dismissal")
	assert.Contains(t, lesFixDetail(h), "distill.write is off: a shows the YAML block to copy and changes nothing.")

	h.press("?")
	assert.Regexp(t, `a {10}YAML`, h.view())
	assert.NotRegexp(t, `a {10}apply`, h.view())

	// An open gate, or a gate that is not known, keeps "apply".
	for _, l := range []func(*proposals.Ledger){
		func(l *proposals.Ledger) { l.Write = true },
		func(l *proposals.Ledger) { l.Write, l.WriteErr = false, errors.New("distill config: yaml: line 3") },
	} {
		f := newFake()
		f.ledger = lesLedger()
		l(f.ledger)

		h := lesOpen(t, f, 120, 60)
		assert.Equal(t, "apply", lesFixBinding(t, h, "a").Help().Desc)
		assert.Contains(t, lesFixDetail(h), "a previews an apply")
	}
}

// TestLessonsFixRecordAppliedShownOnlyForKnownPins covers finding 31:
// the footer offers c only for a target whose pin a preview found. The
// key keeps working for a pin that a hand edit added.
func TestLessonsFixRecordAppliedShownOnlyForKnownPins(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	pinned := []int64{1}

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, true)
		if a == proposals.ActionApply {
			pv.AlreadyPinned = pinned
		}

		if a == proposals.ActionReconcile {
			pv.AlreadyPinned = ids
		}

		return pv, nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{Action: pv.Action, Moved: []int64{1}}, nil
	}

	h := lesOpen(t, f, 120, 60)
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "no pin is known yet")

	h.press("?")
	assert.NotContains(t, h.view(), "record applied")
	h.press("esc")

	// The key still works, but it is not advertised.
	h.press("c")
	assert.Equal(t, []string{"PreviewDecision:reconcile"}, f.called())
	h.press("esc")

	// An apply preview finds the pin of p1.
	h.press("a", "esc")
	assert.True(t, lesFixBinding(t, h, "c").Enabled(), "the cursor row has a known pin")
	assert.Contains(t, lesFixDetail(h), "A pin for p1 already exists in .seamark/lessons.yaml. Press c to record it")

	h.press("down")
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "p2 has no known pin")

	// A selection that includes p1 shows c again.
	h.press("up", "space", "down", "space")
	assert.True(t, lesFixBinding(t, h, "c").Enabled())

	// A new apply preview that finds no pin clears the hint.
	pinned = nil

	h.press("x", "up", "a", "esc")
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "the last preview found no pin for p1")

	// The record removes the hint as well.
	pinned = []int64{1}

	h.press("a", "c", "y", "enter")
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "p1 is recorded as applied")
	assert.Empty(t, lesProps(h).pinned)
}

// TestLessonsFixRecordAppliedShownAfterPartialApply covers finding 31:
// after a partial apply, the not moved proposals have their pins, so
// the footer offers c for them.
func TestLessonsFixRecordAppliedShownAfterPartialApply(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{
			Action: pv.Action, FileWritten: true, Moved: []int64{1}, NotMoved: []int64{3},
			LedgerErr: errors.New("database is locked"),
		}, nil
	}

	h := lesOpen(t, f, 120, 32)
	assert.False(t, lesFixBinding(t, h, "c").Enabled())

	h.press("space", "down", "down", "space", "a", "y", "enter")
	require.Equal(t, []int64{3}, lesProps(h).selectedIDs())
	assert.True(t, lesFixBinding(t, h, "c").Enabled(), "p3 has its pin but no ledger record")

	// A refresh where p3 is no longer pending drops the hint.
	next := lesLedger()
	next.Applied = append(next.Applied, next.Pending[2])
	next.Pending = next.Pending[:2]
	f.ledger = next

	h.press("r")
	assert.Empty(t, lesProps(h).pinned)
	assert.False(t, lesFixBinding(t, h, "c").Enabled())
}
