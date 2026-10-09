package tui

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the Lessons keys: the region cursor without color, the
// labels of the apply and record keys, and the switch keys.

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
	cur, _ := p.cursorRow(p.rows())

	return lesFlat(ansi.Strip(strings.Join(p.detailLines(cur, 200), "\n")))
}

// TestLessonsFixRegionCursorVisibleWithoutColor covers finding 18: the
// test profile is Ascii, as NO_COLOR selects, so only a glyph can show
// the cursor inside the text.
func TestLessonsFixRegionCursorVisibleWithoutColor(t *testing.T) {
	th := ui.NewTheme(false)

	var in ui.LineInput

	in.Set("internal")
	assert.Equal(t, "internal_", ansi.Strip(in.View(th)), "the end of the text shows an underscore")

	for range 3 {
		in.Key(tea.KeyMsg{Type: tea.KeyLeft})
	}

	assert.Equal(t, "inter|nal", ansi.Strip(in.View(th)), "the bar marks where typing inserts")

	in.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	assert.Equal(t, "interX|nal", ansi.Strip(in.View(th)))

	in.Key(tea.KeyMsg{Type: tea.KeyHome})
	assert.Equal(t, "|interXnal", ansi.Strip(in.View(th)))

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
// the footer offers c only when every target has a pin that a preview
// found. Without one, the press shows the reason and opens no preview
// (user decision of 2026-10-04). r reads a pin that a hand edit added.
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

	// The help names every Proposals action (user decision of
	// 2026-10-04). It lists c only as unavailable, so it does not offer
	// a record that fails for a missing pin.
	h.press("?")

	on, off := helpParts(h)
	assert.NotContains(t, on, "record applied")
	assert.Contains(t, off, "c n/a record applied")
	h.press("esc")

	// The press follows the help: it names the reason and opens nothing.
	h.press("c")
	assert.Empty(t, f.called(), "c without a known pin asks for no preview")
	assert.Nil(t, lesProps(h).dec)
	assert.Equal(t, "No record applied: no pin in .seamark/lessons.yaml carries p1 yet. Press r after you edit the "+
		"file.", h.m.toast.text)

	// An apply preview finds the pin of p1.
	h.press("a", "esc")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called())
	assert.True(t, lesFixBinding(t, h, "c").Enabled(), "the cursor row has a known pin")
	assert.Contains(t, lesFixDetail(h), "A pin for p1 already exists in .seamark/lessons.yaml. Press c to record it")

	h.press("down")
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "p2 has no known pin")

	// A selection needs a known pin for every mark: the record refuses
	// the whole selection when one pin is missing. So p1 and p2 hide c,
	// and the press counts the marks without a pin.
	h.press("up", "space", "down", "space")
	assert.False(t, lesFixBinding(t, h, "c").Enabled(), "p2 has no known pin")

	h.press("c")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "c asks for no preview")
	assert.Equal(t, "No record applied: 1 of 2 marked proposals has no pin in .seamark/lessons.yaml yet. Press r "+
		"after you edit the file.", h.m.toast.text)

	// p1 alone shows c again.
	h.press("space")
	require.Equal(t, []int64{1}, lesProps(h).selectedIDs())
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

// The tests below hold review findings of Lessons: the switch keys in
// the help and the sub-view line at the minimum width.

// lesHelpKeys returns the enabled key names that Lessons lists for the
// help and the footer.
func lesHelpKeys(les *lessons) []string {
	var out []string

	for _, b := range les.keys() {
		if b.Enabled() && b.Help().Key != "" {
			out = append(out, b.Help().Key)
		}
	}

	return out
}

func TestLessonsHelpListsEverySwitchKeyThatWorks(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	// Before the first run, u does nothing, so no list names it.
	assert.Subset(t, lesHelpKeys(les), []string{"m", "s", "e"})
	assert.NotContains(t, lesHelpKeys(les), "u")

	h.send(openRunMsg{rec: runRec(h, "Evidence refresh")})
	require.True(t, les.runShown())

	names := map[string]string{"p": "p proposals", "m": "m pins", "s": "s stats", "e": "e evidence", "u": "u run"}

	for _, c := range []struct {
		key  string
		sub  lesSub
		want []string
	}{
		{"p", lesProposals, []string{"m", "s", "e", "u"}},
		{"m", lesPins, []string{"p", "s", "e", "u"}},
		{"s", lesStats, []string{"p", "m", "e", "u"}},
		{"e", lesEvidence, []string{"p", "m", "s", "u"}},
		{"u", lesRun, []string{"p", "m", "s", "e"}},
	} {
		h.press(c.key)
		require.Equal(t, c.sub, les.sub)

		keys := lesHelpKeys(les)
		assert.Subset(t, keys, c.want, "sub-view %q lists every switch key", c.key)
		assert.NotContains(t, keys, c.key, "sub-view %q does not name itself", c.key)

		// The help overlay reads the same list.
		h.press("?")

		help := lesFlat(h.view())
		for _, k := range c.want {
			assert.Contains(t, help, names[k], "the help of %q names %q", c.key, k)
		}

		h.press("esc")
	}

	// The switch keys go last, so the compact footer keeps its first hints.
	h.press("p")
	h.resize(60, 20)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(h.view())), "space select"),
		footerLine(h.view()))
}

func TestLessonsSubTabsKeepTheGapBeforeTheGate(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false

	h := lesOpen(t, f, 60, 20)

	// At the minimum width the hint drops, so the faint hint and the
	// faint gate never read as one phrase.
	line := lesSubTabs(h)
	assert.True(t, strings.HasSuffix(line, "distill.write off"), line)
	assert.NotContains(t, line, "p / m / s / e", line)

	// A wider line keeps the hint with at least three cells before the gate.
	for _, w := range []int{61, 64, 80, 120} {
		h.resize(w, 24)

		line = lesSubTabs(h)
		if strings.Contains(line, "p / m / s / e") {
			assert.Regexp(t, regexp.MustCompile(`p / m / s / e {3,}distill\.write off$`), line, "width %d", w)
		}
	}

	assert.Contains(t, lesSubTabs(h), "p / m / s / e", "a wide line shows the hint")
}
