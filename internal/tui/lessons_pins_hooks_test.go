package tui

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the shell side of installed lesson maintenance: the Pins
// sub-view and its switch key, the sub-view line at every width, the
// Pins source, and the hooks that open a maintenance preview and an
// extraction record. They assert the shell contract only, so they hold
// after the screen slices replace the stubs.

// hookPrune is a prune request of one entry, as the Pins sub-view sends.
func hookPrune() maintain.Request {
	return maintain.Request{
		Action: maintain.ActionPrune, Cursor: true,
		Refs: []pinfile.EntryRef{{Index: 0, Key: reviews.NewPinKey("close-db-handles", "api", nil)}},
	}
}

// hookLessons returns the Lessons screen of h.
func hookLessons(h *harness) *lessons {
	return h.m.screens[destLessons].(*lessons)
}

// hookMutations returns the recorded calls that commit or run work.
func hookMutations(f *fakeServices) []string {
	var out []string

	for _, c := range f.called() {
		if strings.HasPrefix(c, "Commit") || strings.HasPrefix(c, "Run") || strings.HasPrefix(c, "Save") {
			out = append(out, c)
		}
	}

	return out
}

func TestLessonsPinsSubViewOpensWithM(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)
	reads := f.readCount("Pins")
	require.Positive(t, reads, "the console reads the pins at start")

	h.press("m")
	require.Equal(t, lesPins, les.sub)
	assert.True(t, les.showsIntent("pins"))
	assert.Contains(t, lesSubTabs(h), " Proposals   Pins   Stats   Evidence   p / m / s / e")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	h.press("p")
	h.send(navigateMsg{dest: destLessons, intent: "pins"})
	assert.Equal(t, lesPins, les.sub, "the pins intent opens the sub-view")

	h.press("r")
	assert.Equal(t, reads+1, f.readCount("Pins"), "r reads the pins again")
	assert.Empty(t, f.called(), "browsing Pins plans and writes nothing")
}

func TestLessonsPinsKeyWorksInEverySubView(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)
	h.send(openRunMsg{rec: runRec(h, "Evidence refresh")})

	for _, from := range []string{"p", "s", "e", "u"} {
		h.press(from)
		require.NotEqual(t, lesPins, les.sub)

		h.press("m")
		assert.Equal(t, lesPins, les.sub, "m opens Pins from %q", from)
	}

	// A capturing input receives the letter as text.
	h.press("p", "/")
	h.typeText("m")
	assert.Equal(t, lesProposals, les.sub)
	assert.Equal(t, "m", les.props.input.Value())
	assert.Empty(t, f.called())
}

func TestLessonsSubTabsKeepTheGateAtEveryWidth(t *testing.T) {
	gates := []struct {
		name  string
		label string
		set   func(l *proposals.Ledger)
	}{
		{"on", "distill.write on", func(l *proposals.Ledger) { l.Write = true }},
		{"off", "distill.write off", func(l *proposals.Ledger) { l.Write = false }},
		{"unreadable", "distill.write ! unreadable", func(l *proposals.Ledger) { l.WriteErr = errors.New("bad") }},
	}

	for _, g := range gates {
		for _, run := range []bool{false, true} {
			f := newFake()
			f.ledger = lesLedger()
			g.set(f.ledger)

			h := lesOpen(t, f, 120, 24)
			if run {
				h.m.st.lastRunKind = runEvidence
			}

			for w := ui.MinWidth; w <= 120; w++ {
				h.resize(w, 24)

				line := lesSubTabs(h)
				tabs, _, found := strings.Cut(line, "distill.write")

				require.True(t, found, "gate %s, run %t, width %d: %q", g.name, run, w, line)
				assert.True(t, strings.HasSuffix(line, g.label), "gate %s, run %t, width %d: %q", g.name, run, w, line)
				assert.LessOrEqual(t, ui.Width(line), w)
				assert.True(t, strings.HasSuffix(tabs, "   "), "three cells before the gate at %d: %q", w, line)
				assert.Contains(t, tabs, "Pins")
				assert.Equal(t, run, strings.Contains(tabs, "Run"), "width %d: %q", w, line)
				assert.NotContains(t, tabs, "…", "no tab is clipped at %d: %q", w, line)
			}
		}
	}
}

func TestLessonsSubTabsShortenOnlyWhenNeeded(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false

	h := lesOpen(t, f, 120, 24)
	h.m.st.lastRunKind = runEvidence

	assert.Equal(t, " Proposals   Pins   Stats   Evidence   Run   p / m / s / e / u",
		regexp.MustCompile(` +distill\.write off$`).ReplaceAllString(lesSubTabs(h), ""))

	// Review fix: two-cell gaps keep the key hint at 80 columns, because
	// the 80-column footer drops the switch keys, and only the hint
	// names m for Pins.
	h.resize(80, 24)
	assert.Regexp(t, `^ Proposals  Pins  Stats  Evidence  Run   p / m / s / e / u {3,}distill\.write off$`,
		lesSubTabs(h), "the hint stays before any rung drops it")

	h.resize(63, 24)
	assert.Regexp(t, `^ Proposals   Pins   Stats   Evidence   Run   distill\.write off$`, lesSubTabs(h),
		"full labels while three cells stay before the gate")

	h.resize(62, 24)
	assert.Regexp(t, `^ Proposals  Pins  Stats  Evidence  Run {3,}distill\.write off$`, lesSubTabs(h),
		"full labels take two-cell gaps before they shorten")

	f.ledger.WriteErr = errors.New("bad")
	h.press("r")

	h.resize(68, 24)
	assert.Regexp(t, `^ Proposals  Pins  Stats  Evidence  Run {3,}distill\.write ! unreadable$`, lesSubTabs(h))

	h.resize(65, 24)
	assert.Regexp(t, `^ Props   Pins   Stats   Evid   Run {3,}distill\.write ! unreadable$`, lesSubTabs(h))

	h.resize(60, 20)
	assert.Equal(t, " Props  Pins  Stats  Evid  Run   distill.write ! unreadable", lesSubTabs(h),
		"the narrowest rung keeps even the unreadable gate")
}

func TestPinsHostileTextStaysInert(t *testing.T) {
	e := pinfile.Entry{Index: 0, Line: 2, Rule: "evil-rule\x1b]0;pwned\x07", Region: "\x1b[31mapi\u009b2J", Note: "n"}

	f := newFake()
	f.ledger = lesLedger()
	f.pins = &maintain.Inspection{
		Doc:  &pinfile.Document{Entries: []pinfile.Entry{e}},
		Pins: []maintain.Pin{{Entry: e}},
	}

	for _, ascii := range []bool{false, true} {
		h := &harness{t: t, m: newModel(f, ui.NewTheme(ascii))}
		h.run(h.m.Init())

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			h.press("3", "m")

			raw := h.m.View()
			assert.NotContains(t, raw, "\x1b", "no escape byte leaks at %v", size)
			assert.NotContains(t, raw, "\x07")
			assert.NotContains(t, raw, "\u009b")
			assert.Contains(t, h.view(), "evil-rule", "the rule stays visible as text")
			assertFrame(t, h.view(), size[0], size[1])

			if ascii {
				for _, r := range h.view() {
					require.Less(t, r, rune(128), "ASCII frame at %v", size)
				}
			}
		}
	}
}

func TestMaintPreviewOpensOverPinsAndCaptures(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	h.send(openMaint(hookPrune())())
	require.Equal(t, lesPins, les.sub, "the preview opens over Pins")
	require.True(t, les.pins.host.open())
	assert.True(t, les.capturing(), "the preview takes every key")

	_, shown := les.modal(120, 35)
	assert.True(t, shown, "the shell draws the preview")

	// A second request never replaces the preview under review.
	first := les.pins.host.modal
	h.send(openMaintMsg{req: maintain.Request{Action: maintain.ActionRetarget}})
	assert.Same(t, first, les.pins.host.modal)

	h.press("p", "s", "1")
	assert.Equal(t, lesPins, les.sub, "switch keys wait while the preview is open")
	assert.Equal(t, destLessons, h.m.active, "global keys wait too")

	h.press("esc")
	assert.False(t, les.pins.host.open(), "esc closes the preview")
	assert.False(t, les.capturing())
	assert.Empty(t, hookMutations(f), "opening and closing a preview commits nothing")
}

func TestSettingsSaveClosesTheMaintPreview(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	h.send(openMaintMsg{req: hookPrune()})
	require.True(t, les.pins.host.open())

	// The save arrives while the user looks at another destination.
	h.m.active = destSettings
	h.send(settingsSavedMsg{group: settings.GroupPinWrite})
	assert.False(t, les.pins.host.open(), "a save of the write gate drops the preview (D-14)")
	assert.Empty(t, hookMutations(f))
}

func TestOpenExtractMsgOpensTheRunRecord(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := newHarness(t, f, 120, 40)
	req := learning.ExtractRequest{IDs: []int64{4, 1}}

	h.send(openExtract(req, false)())
	require.Equal(t, destLessons, h.m.active, "the review opens from any destination")

	les := hookLessons(h)
	require.Equal(t, lesRun, les.sub)

	rec, ok := les.runv.cur.(*extRecord)
	require.True(t, ok, "the Run sub-view holds an extraction record")
	assert.False(t, rec.active(), "the review starts no work")
	assert.True(t, les.runShown())
	assertFrame(t, h.view(), 120, 40)
	assert.Empty(t, hookMutations(f), "a review runs no agent")
}

func TestOpenExtractMsgWaitsForAnActiveRun(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	busy := runRec(h, "Distillation")
	busy.running = true
	h.send(openRunMsg{rec: busy, show: true})

	h.send(openExtractMsg{req: learning.ExtractRequest{IDs: []int64{1}}})
	assert.Same(t, busy, les.runv.cur, "an active record keeps the view")
	assert.Contains(t, h.view(), "A run is in progress")
	assert.Empty(t, hookMutations(f))
}

func TestMutationsRefreshThePins(t *testing.T) {
	assert.Contains(t, maintRefresh, secPins)
	assert.Contains(t, maintRefresh, secLedger)
	assert.Contains(t, extractRefresh, secPins)
	assert.Contains(t, extractRefresh, secLedger)
	assert.Contains(t, allSections, secPins)

	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, true), nil
	}

	h := lesOpen(t, f, 120, 40)
	reads := f.readCount("Pins")

	h.press("d", "y")
	require.Equal(t, 1, f.callCount("CommitDecision:dismiss"))
	assert.Equal(t, reads+1, f.readCount("Pins"), "a decision reads the pins again")
	assert.False(t, h.m.st.pins.loading)
	assert.False(t, h.m.latestSnapshot().Before(h.m.st.pins.at), "the snapshot clock counts the pins")
}

func TestOpenPinsShowsPinsWithoutAPreview(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := newHarness(t, f, 120, 40)
	h.run(openPins(f.ledger.Applied[0].ID))

	les := hookLessons(h)
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesPins, les.sub, "a link to a pin opens Pins")
	assert.False(t, les.pins.host.open(), "a link never opens a preview")
	assert.Empty(t, f.called(), "a link plans nothing")
}

func TestMaintActedChangesNoViewAndPlansNothing(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	les.pins.host.acted = hookPrune().Refs
	h.run(les.pins.update(struct{}{}))
	assert.Equal(t, lesProposals, les.sub, "the note of a commit moves no view")
	assert.False(t, les.pins.host.open())
	assert.Empty(t, les.pins.host.acted, "the next update takes the changed entries")
	assert.Empty(t, f.called())
}

// AC-1: Pins and Proposals keep their context across the other
// sub-views and destinations. Only a refresh or a decision may change
// it, so a round trip reads nothing and calls no service.
func TestPinsAndProposalsContextSurvivesARoundTrip(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)
	les := hookLessons(h)
	p := pinsOf(h)

	// Pins: a region filter, a text search and a cursor row.
	h.press("/")
	h.typeText("internal")
	h.press("enter", "z")
	h.typeText("e")
	h.press("enter", "down")

	require.Equal(t, "internal", p.region)
	require.Equal(t, "e", p.text)
	require.Positive(t, p.list.Cursor, "the cursor left the first row")

	region, text, row, cursor := p.region, p.text, p.list.Cursor, p.cursorID

	// Proposals: a cursor row below the first.
	h.press("p", "down")

	props := lesProps(h)
	require.Equal(t, int64(2), props.cursorID)

	reads := f.readCount("Pins")

	for _, k := range []string{"s", "e", "1", "2", "4", "5", "3", "m", "p", "m"} {
		h.press(k)
	}

	require.Equal(t, lesPins, les.sub)
	assert.Equal(t, region, p.region, "the region filter stays")
	assert.Equal(t, text, p.text, "the text search stays")
	assert.Equal(t, row, p.list.Cursor, "the Pins cursor row stays")
	assert.Equal(t, cursor, p.cursorID, "the Pins cursor names the same pin")
	assert.Equal(t, int64(2), props.cursorID, "the Proposals cursor stays")
	assert.Equal(t, reads, f.readCount("Pins"), "a round trip reads the pins only on r or after a change")
	assert.Empty(t, f.called(), "a round trip plans and writes nothing")
}
