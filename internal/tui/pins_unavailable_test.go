package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the unavailable action keys in Lessons › Pins (user
// decision of 2026-10-04: "Always list, mark unavailable"). The footer
// and the help list n, i, d, -, w, t and v (contracts §4.2). A key that
// the target cannot use reads n/a, and its press explains why and calls
// no service.

// helpParts returns the help body of the console on one line, split at
// the n/a label: the keys that work, then the unavailable keys. The help
// scrolls, so the test reads its rows instead of the visible window. It
// reads the one-column layout of an 80-column terminal, because the
// three columns of a wide help mix their keys on each line. The plain
// commands at the end stay out.
func helpParts(h *harness) (on, off string) {
	rows, _ := h.m.helpLayout(80, h.m.st.height-1)
	text := lesFlat(ansi.Strip(strings.Join(rows, "\n")))
	body, _, _ := strings.Cut(text, "PLAIN COMMANDS")
	on, off, _ = strings.Cut(body, ui.UnavailableWord+": not available now")

	return on, off
}

// pinsFooterActions returns the action keys among n, i, d, w, -, t and
// v that a footer names, as a hint or inside its n/a group.
func pinsFooterActions(footer string) []string {
	var got []string

	for _, f := range strings.Fields(footer) {
		if slices.Contains([]string{"n", "i", "d", "w", "-", "t", "v"}, f) && !slices.Contains(got, f) {
			got = append(got, f)
		}
	}

	return got
}

// pinsNoticeRow returns the notice row of a frame: the row under the
// tab rule that holds the toast.
func pinsNoticeRow(frame string) string {
	return strings.Split(frame, "\n")[3]
}

func TestPinsFooterListsEveryActionKeyAndMarksTheUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		row    int
		footer string
		// clock is false when the clock gives way, so the footer keeps
		// every n/a entry.
		clock bool
	}{
		// The case of the user report: current regions, an answered
		// trigger question, and no similar pin.
		{"current scope", 1, " space select   n new pin   i edit   - prune   n/a: d w t v   tab detail  …", true},
		{"every action but compare", 2, " space select   n new pin   i edit   - prune   w retarget   t extract   " +
			"n/a: d v  …", false},
		{"similar pin without evidence", 3, " space select   n new pin   i edit   - prune   v compare   n/a: d w t  …",
			true},
		{"ambiguous", 9, " space select   n new pin   n/a: i d - w t v   tab detail   f show  …", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := pinsOpen(t, pinsFake(), 120, 40)
			pinsCursorTo(h, c.row)

			// The reference footer cannot hold every hint, so the n/a group
			// shows its keys only. The seven action keys come first in the
			// order space n i d - w t v (contracts §4.2).
			footer := footerLine(h.view())
			assert.True(t, strings.HasPrefix(footer, c.footer), "footer %q", footer)
			assert.ElementsMatch(t, []string{"n", "i", "d", "-", "w", "t", "v"}, pinsFooterActions(footer))

			if c.clock {
				assert.Regexp(t, `\d\d:\d\d:\d\d $`, footer, "the clock stays at the reference size")
			} else {
				assert.NotRegexp(t, `\d\d:\d\d:\d\d`, footer, "the clock gives way to the n/a entries")
			}

			assert.Empty(t, h.m.toast.text, "browsing shows no refusal")
		})
	}

	// A footer with room for every hint keeps the actions of the group.
	// The seven action keys need more than 200 columns for that.
	h := pinsOpen(t, pinsFake(), 240, 50)
	assert.Contains(t, footerLine(h.view()), " space select   n new pin   i edit   - prune   "+
		"n/a: d remove, w retarget, t extract, v compare   tab detail   f show")
}

// pinsFooterList is one list of Pins whose footer a test reads: the
// inspection, the keys that show the list, and its number of rows.
type pinsFooterList struct {
	name string
	in   func() *maintain.Inspection
	keys []string
	rows int
}

// pinsFooterLists returns every list of Pins: each filter over a good
// inspection, and the lists of a file or a ledger that does not read.
func pinsFooterLists() []pinsFooterList {
	// unreadable is a pin file that does not parse, so no entry is
	// listed and no link is known.
	unreadable := func() *maintain.Inspection {
		in := fakePins()
		in.Doc.Guard.Exists = true
		in.Doc.DecodeErr = errors.New("yaml: line 3: mapping values are not allowed in this context")
		in.LinkErr = pinfile.ErrUnreadable
		in.Links = &pinfile.Links{NotPresentErr: pinfile.ErrUnreadable}

		return in
	}

	return []pinsFooterList{
		{"installed", pinsInspection, nil, len(pinsInspection().Pins)},
		{"not present", pinsInspection, []string{"f"}, 1},
		{"superseded", pinsInspection, []string{"f", "f"}, 1},
		{"malformed file", pinsMalformed, nil, 1},
		{"unreadable file", unreadable, nil, 1},
		{"no ledger", pinsNoLedger, nil, 1},
		{"no ledger, not present", pinsNoLedger, []string{"f"}, 1},
		{"no pin file", fakePins, nil, 1},
		{"no pin file, not present", fakePins, []string{"f"}, 1},
	}
}

// eachPinsFooter calls check with the footer of every row of every
// list of Pins, in both glyph sets, at the size w by h.
func eachPinsFooter(t *testing.T, w, hgt int, check func(footer, where string)) {
	for _, ascii := range []bool{false, true} {
		for _, l := range pinsFooterLists() {
			f := pinsFake()
			f.pins = l.in()

			h := lesOpenTheme(t, f, ascii)
			h.press("m")
			h.resize(w, hgt)
			h.press(l.keys...)

			for row := 1; row <= l.rows; row++ {
				pinsCursorTo(h, row)

				frame := h.view()
				assertFrame(t, frame, w, hgt)
				check(footerLine(frame), l.name+", row "+strconv.Itoa(row)+", ascii "+strconv.FormatBool(ascii))
			}
		}
	}
}

func TestPinsFooterKeepsTheActionKeysInEveryListAtTheReferenceSize(t *testing.T) {
	eachPinsFooter(t, 120, 40, func(footer, where string) {
		assert.ElementsMatch(t, []string{"n", "i", "d", "w", "-", "t", "v"}, pinsFooterActions(footer),
			"%s: %q", where, footer)
	})
}

// TestPinsMinimumFooterKeepsEveryActionKey covers L15 of the gate 7
// review. At the minimum size even the keys alone do not fit, so the
// footer gives up the keys that work from the end. Every n/a entry and
// every action key stay, and the clock gives way. A history list also
// keeps f, its route back to the installed pins. No rule keeps f: it
// stays because it fits. So this test guards it.
func TestPinsMinimumFooterKeepsEveryActionKey(t *testing.T) {
	eachPinsFooter(t, ui.MinWidth, ui.MinHeight, func(footer, where string) {
		assert.ElementsMatch(t, []string{"n", "i", "d", "w", "-", "t", "v"}, pinsFooterActions(footer),
			"%s: %q", where, footer)
		assert.NotRegexp(t, `\d\d:\d\d:\d\d`, footer, where)
		assert.Contains(t, footer, "r refresh   ? help   q quit", where)

		if strings.Contains(where, "not present") || strings.HasPrefix(where, "superseded") {
			assert.Contains(t, strings.Fields(footer), "f", "%s: %q", where, footer)
		}
	})
}

func TestPinsCompactFooterKeepsEveryActionKey(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		h := lesOpenTheme(t, pinsFake(), ascii)
		h.press("m")
		h.resize(80, 24)

		// The case of the user report at 80x24 (decision of 2026-10-04:
		// "Always list, mark unavailable"). The eight action keys with
		// their actions do not fit 80 columns. So the footer drops the
		// clock and shows the hints by their keys alone. The help names
		// each action (TestPinsHelpListsEveryPinsKeyWithTheMarker).
		frame := h.view()
		assertFrame(t, frame, 80, 24)

		footer := footerLine(frame)

		assert.True(t, strings.HasPrefix(footer, " space n i -   n/a: d w t v   "), "footer %q", footer)
		assert.NotRegexp(t, `\d\d:\d\d:\d\d $`, footer, "ascii %t", ascii)
		assert.Contains(t, footer, "r refresh   ? help   q quit")

		// With a few more cells, the leading hints get their actions back.
		h.resize(90, 24)

		footer = footerLine(h.view())
		assert.True(t, strings.HasPrefix(footer, " space select   n i -   n/a: d w t v   "), "footer %q", footer)
	}
}

func TestPinsUnavailableEntriesAreNeverOfferedKeys(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)
	h.view()

	// An unavailable entry is not an offered key, so every check of the
	// offered keys still leaves it out.
	assert.Equal(t, []string{"space", "n", "i", "-", "tab", "f", "/", "z", "p", "s", "e"}, pinsKeys(h))

	for _, b := range hookLessons(h).keys() {
		if ui.IsUnavailable(b) {
			assert.False(t, key.Matches(keyMsg(b.Help().Key), b), "an unavailable entry never matches %q", b.Help().Key)
		}
	}
}

func TestPinsUnavailableMarkerReadsWithoutColor(t *testing.T) {
	f := pinsFake()
	f.pins = pinsASCIIInspection()

	// The test profile has no colors, as under NO_COLOR; the theme uses
	// the ASCII glyph set.
	h := lesOpenTheme(t, f, true)
	h.press("m")

	footer := footerLine(h.view())
	assert.True(t, strings.HasPrefix(footer, " space select   n new pin   i edit   - prune   n/a: d w t v   tab detail  ..."),
		"footer %q", footer)

	for _, r := range h.view() {
		require.Less(t, r, rune(128), "the ASCII frame holds %q", r)
	}

	h.press("?")

	// Each unavailable row carries the word itself, not only the label
	// row above it.
	on, off := helpParts(h)
	assert.Contains(t, on, "- prune")
	assert.Contains(t, off, "d n/a remove w n/a retarget t n/a extract v n/a compare x n/a clear marks")

	for _, r := range h.view() {
		require.Less(t, r, rune(128), "the ASCII help holds %q", r)
	}

	// The ASCII movement label fits the key column, so its action starts
	// in the same column as every other action.
	rows, _ := h.m.helpLayout(80, h.m.st.height-1)
	assert.Contains(t, rows, ui.PadRight("up/dn j k", helpKeyW)+"move")
	assert.Contains(t, rows, ui.PadRight("g  G", helpKeyW)+"top / bottom")
}

func TestPinsScrolledHelpMarksEveryUnavailableRow(t *testing.T) {
	f := pinsFake()
	f.pins = pinsASCIIInspection()

	// NO_COLOR and the ASCII glyph set: no style can mark a row.
	h := lesOpenTheme(t, f, true)
	h.press("m")
	h.resize(60, 20)
	h.press("?")

	// Scroll until the label row "n/a: not available now" shows. Then
	// scroll on until it leaves the window at the top, while the
	// unavailable rows below it still show.
	for range 60 {
		if strings.Contains(h.view(), "not available now") {
			break
		}

		h.press("down")
	}

	require.Contains(t, h.view(), "not available now", "the label row scrolled into the window")

	for range 60 {
		if !strings.Contains(h.view(), "not available now") {
			break
		}

		h.press("down")
	}

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	require.NotContains(t, frame, "not available now", "the label row scrolled out")

	shown := 0

	for _, line := range strings.Split(frame, "\n") {
		for _, action := range []string{"remove", "retarget", "extract", "compare", "clear marks"} {
			if strings.Contains(line, action) {
				shown++

				assert.Contains(t, line, ui.UnavailableWord, "the row of %q reads n/a on its own", action)
			}
		}
	}

	assert.Positive(t, shown, "an unavailable row is in the window")
}

func TestHintsRenderTheUnavailableGroupMuted(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	th := ui.NewTheme(false)

	hidden := pinKeys.Compare
	hidden.SetEnabled(false)

	bindings := []key.Binding{
		pinKeys.Prune, ui.Unavailable(pinKeys.Retarget), ui.Unavailable(pinKeys.Compare), hidden, lesKeys.State,
	}
	out := th.Hints(bindings)

	// The group is faint and has no key style, so it never looks like an
	// enabled key. The word n/a carries the state without color.
	assert.Contains(t, out, th.Faint.Render("n/a: w retarget, v compare"))
	assert.Contains(t, out, th.Key.Render("-"))
	assert.Contains(t, out, th.Key.Render("f"))
	assert.NotContains(t, out, th.Key.Render("w"))
	assert.Equal(t, "- prune   n/a: w retarget, v compare   f state filter", ansi.Strip(out))

	// The short form keeps the word, the faint style and the keys.
	short := th.ShortHints(bindings)
	assert.Contains(t, short, th.Faint.Render("n/a: w v"))
	assert.NotContains(t, short, th.Key.Render("w"))
	assert.Equal(t, "- prune   n/a: w v   f state filter", ansi.Strip(short))

	// A binding that a screen only disables stays hidden, so no other
	// screen changes.
	assert.Empty(t, th.Hints([]key.Binding{hidden}))
}

func TestPinsHelpRendersUnavailableRowsMuted(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	h := pinsOpen(t, pinsFake(), 120, 40)
	h.view()

	th := h.m.st.theme
	rows, _ := h.m.helpLayout(80, h.m.st.height-1)

	// row returns the help row whose first word is the key k.
	row := func(k string) string {
		for _, r := range rows {
			if fields := strings.Fields(ansi.Strip(r)); len(fields) > 0 && fields[0] == k {
				return r
			}
		}

		require.Failf(t, "no help row", "key %q", k)

		return ""
	}

	// The escape sequence that starts the key style. The faint style must
	// start with another one, or the test could not tell them apart.
	keyStart, _, _ := strings.Cut(th.Key.Render("w"), "w")
	faintStart, _, _ := strings.Cut(th.Faint.Render("w"), "w")
	require.NotEmpty(t, keyStart, "the profile renders the key style")
	require.NotEqual(t, keyStart, faintStart)

	// Pin 1 cannot retarget: its row is faint as a whole, with the word
	// after the key, and no part of it has the key style.
	w := row("w")
	assert.Contains(t, w, th.Faint.Render(ui.PadRight("w n/a", helpKeyW)+"retarget"))
	assert.NotContains(t, w, keyStart)

	// Pin 1 can prune: its row keeps the key style.
	assert.Contains(t, row("-"), th.Key.Render(ui.PadRight("-", helpKeyW)))
}

func TestPinsUnavailableKeyExplainsAndCallsNothing(t *testing.T) {
	cases := []struct {
		name  string
		row   int
		key   string
		toast string
		// next follows the toast. The notice row can cut it at its end, so
		// only the toast text must hold it.
		next string
	}{
		{"retarget", 1, "w", `No retarget: its regions are already current ("wrap-sql-errors").`, " " + pinsReplaceSentence},
		{"extraction", 1, "t", `No trigger extraction: its trigger paths are already stored ("wrap-sql-errors").`, ""},
		{"compare", 1, "v", `No comparison: no other linked pin has similar wording ("wrap-sql-errors").`, ""},
		{"prune", 9, "-", `No prune: it has an ambiguous link ("two-proposals").`, ""},
		{"compare unlinked", 5, "v",
			`No comparison: it has no linked proposal, and only linked pins are compared ("keep-changelog-current").`, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pinsFake()
			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, c.row)

			h.press(c.key)

			assert.Empty(t, f.called(), "an unavailable key plans and writes nothing")
			assert.Equal(t, c.toast+c.next, h.m.toast.text)
			assert.Contains(t, pinsNoticeRow(h.view()), c.toast)

			_, open := hookLessons(h).modal(120, 39)
			assert.False(t, open, "an unavailable key opens no preview, review or comparison")
			assert.False(t, hookLessons(h).capturing())
			assert.Equal(t, lesPins, hookLessons(h).sub, "the review never opens the Run sub-view")
		})
	}
}

// pinsNoRetargetRoute is the scope route of p4 in pinsInspection: w reads
// n/a, because the scope is current.
const pinsNoRetargetRoute = "no retarget: its regions are already current; " + pinRouteReplace

// pinsReplaceSentence is pinRouteReplace as the last sentence of a line.
const pinsReplaceSentence = "To choose other regions, use - Prune and add a new pin with n."

// TestPinRetargetRouteFollowsTheRetargetBlock holds one rule for each state
// of a linked pin. The words name w only while w works, and every refused
// state gives its actual reason and a next action. The cases change the
// fixture of each state, so the check reads the rule that the keys use.
func TestPinRetargetRouteFollowsTheRetargetBlock(t *testing.T) {
	const replace = pinRouteReplace

	// pinTwo is p9, the one fixture pin that offers a retarget.
	const pinTwo = 1

	cases := []struct {
		name string
		row  int
		edit func(in *maintain.Inspection)
		// reason is the retargetBlock text. It is empty for a pin that
		// offers a retarget.
		reason string
		// next is the next action, also a route that the pin must offer.
		next string
		// prune says that the next action is the prune route.
		prune bool
	}{
		{name: "a computed scope", row: 2},
		{name: "a current scope", row: 1, reason: "its regions are already current", next: replace, prune: true},
		{
			name: "no living finding", row: 3, next: replace, prune: true,
			reason: "no cited finding is living, so no scope can be computed",
		},
		{
			name: "an unknown scope", row: 4, next: replace, prune: true,
			reason: "its scope is unknown: scope advice needs an indexed database (the index is stale); " +
				"build it with `seamark index`",
		},
		{
			name: "computed regions that another pin holds", row: 2, next: replace, prune: true,
			edit: func(in *maintain.Inspection) {
				in.Pins[2].Entry.Rule = in.Pins[pinTwo].Entry.Rule
				in.Pins[2].Entry.Key = reviews.NewPinKey(in.Pins[pinTwo].Entry.Rule, "",
					in.Pins[pinTwo].Readings.Scope.Advice.Regions)
			},
			reason: "pin 3 (line 12) already has this rule with the computed regions",
		},
		{
			name: "an unsafe entry", row: 2, next: pinsHandRoute,
			edit:   func(in *maintain.Inspection) { in.Pins[pinTwo].Entry.Block = "a multi-line rule value" },
			reason: "the entry cannot be edited safely (a multi-line rule value)",
		},
		{
			name: "a pin file that cannot be edited", row: 2, next: pinsHandRoute,
			edit:   func(in *maintain.Inspection) { in.Doc.FileBlock = "the file is read-only" },
			reason: "the pin file cannot be edited (the file is read-only)",
		},
		{
			name: "a foreign workspace", row: 2, next: pinRouteIndex,
			edit:   func(in *maintain.Inspection) { in.Index.Root = "/work/original" },
			reason: "the index records another repository, /work/original",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pinsFake()
			if c.edit != nil {
				c.edit(f.pins)
			}

			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, c.row)

			p := pinsOf(h)
			pin := p.rows()[c.row-1].pin
			require.NotNil(t, pin)
			require.True(t, pinLinked(pin), "the rule covers linked pins")

			assert.Equal(t, c.reason, p.retargetBlock(pin))

			if c.reason == "" {
				assert.Equal(t, pinRouteRetarget, p.scopeRoute(pin))
				assert.True(t, p.offers(p.retargetBlock), "the route names a key that works")

				return
			}

			assert.Equal(t, c.next, p.retargetNext(pin))
			assert.Equal(t, "no retarget: "+c.reason+"; "+c.next, p.scopeRoute(pin))
			assert.NotContains(t, p.scopeRoute(pin), "w Retarget", "the route never points at a key that reads n/a")
			assert.False(t, pinStoredLock(p.scopeRoute(pin)), "a reload must keep the lock of a linked pin")
			assert.False(t, p.offers(p.retargetBlock))

			h.press("w")
			assert.Empty(t, f.called(), "an unavailable key plans and writes nothing")
			assert.True(t, strings.HasSuffix(h.m.toast.text, pinRouteSentence(c.next)), "toast: %s", h.m.toast.text)
			assert.Contains(t, h.m.toast.text, "No retarget: "+c.reason)

			// The prune route must work, not only read well. The press opens
			// the preview that a new pin then follows.
			if c.prune {
				assert.True(t, p.offers(p.editBlock), "the route names a key that works")
				assert.Contains(t, pinsKeys(h), "n", "the route names a key that works")
				h.press("-")
				assert.Contains(t, f.called(), "PlanMaintenance:prune")
			}
		})
	}
}

// TestPinRetargetRouteOfAnUnlinkedPin gives the state of each other link
// a route that exists, because such a pin has no prune either.
func TestPinRetargetRouteOfAnUnlinkedPin(t *testing.T) {
	// refusedRegion is a region that the form refuses to edit.
	refusedRegion := func(f *fakeServices) {
		f.pins.Pins[4].Entry.Region, f.pins.Pins[4].Entry.Regions = "internal/\u0085api", nil
	}

	cases := []struct {
		name string
		row  int
		edit func(f *fakeServices)
		want string
	}{
		{name: "no proposal", row: 5, want: pinRouteForm},
		{
			name: "no proposal in a file that cannot be edited", row: 5, want: pinsHandRoute,
			edit: func(f *fakeServices) { f.pins.Doc.FileBlock = "the file is read-only" },
		},
		{
			name: "a pending match", row: 5, want: pinRouteRecord,
			edit: func(f *fakeServices) { f.pins.Pins[4].Link.Link = pinfile.LinkPendingMatch },
		},
		{
			// The record preview reads the pin file of the recorded root.
			name: "a pending match in a copied checkout", row: 5, want: pinRouteIndex,
			edit: func(f *fakeServices) {
				pinsCopiedCheckout(f)
				f.pins.Pins[4].Link.Link = pinfile.LinkPendingMatch
			},
		},
		{name: "an ambiguous link", row: 9, want: pinsHandRoute},
		{name: "no proposal in a copied checkout", row: 5, want: pinRouteFormIndex, edit: pinsCopiedCheckout},
		{name: "no proposal under --db of another root", row: 5, want: pinsOpenFormRoute, edit: pinsAnotherDB},
		{
			// A record reads the pin that the list shows, so --db keeps it.
			name: "a pending match under --db of another root", row: 5, want: pinRouteRecord,
			edit: func(f *fakeServices) {
				pinsAnotherDB(f)
				f.pins.Pins[4].Link.Link = pinfile.LinkPendingMatch
			},
		},
		{name: "a region that the form refuses", row: 5, want: pinsHandRoute, edit: refusedRegion},
		{
			name: "a region above the length of the form", row: 5, want: pinsHandRoute,
			edit: func(f *fakeServices) {
				region := pathInput()
				f.pins.Pins[4].Entry.Region = strings.Repeat("a", region.MaxRunes()+1)
			},
		},
		{
			// The build comes first, because the file of the recorded root
			// belongs to the original repository.
			name: "a refused region in a copied checkout", row: 5, want: pinRouteIndex + ", then " + pinsHandRoute,
			edit: func(f *fakeServices) {
				pinsCopiedCheckout(f)
				refusedRegion(f)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pinsFake()
			if c.edit != nil {
				c.edit(f)
			}

			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, c.row)

			p := pinsOf(h)
			pin := p.rows()[c.row-1].pin
			require.NotNil(t, pin)
			require.False(t, pinLinked(pin))

			assert.Equal(t, c.want, p.retargetNext(pin))

			h.press("w")
			assert.Contains(t, h.m.toast.text, pinRouteSentence(c.want))
		})
	}
}

// pinsCopiedCheckout makes the fake a copied checkout: the default
// database records another root, so a prune and a save both stop.
func pinsCopiedCheckout(f *fakeServices) {
	f.pins.Index.Root = "/work/original"
}

// pinsAnotherDB makes the fake a --db of another repository. A prune runs
// there, and the authoring engine still refuses a save.
func pinsAnotherDB(f *fakeServices) {
	f.dbPath = "/elsewhere/index.db"
	f.pins.Index.Root = "/work/original"
}

// The words of a hand edit when the recorded root is the workspace. They
// are literals, so a change of the production words fails the tests.
const (
	pinsHandRoute = "edit the pin by hand in .seamark/lessons.yaml"
	pinsHandLock  = "; edit .seamark/lessons.yaml by hand"
)

// The routes of a --db of another repository. The two name the recorded
// root of pinsAnotherDB and no index build.
const (
	pinsOpenFormRoute    = pinGoalForm + ", open the console in /work/original"
	pinsOpenReplaceRoute = pinGoalReplace + ", open the console in /work/original"
)

// pinsKeptTail is a pin file whose last entry keeps its trailing line
// breaks, so the planner of a new pin refuses to append after it.
const pinsKeptTail = "pin:\n  - rule: kept\n    region: docs\n    note: |+\n      Kept.\n\n"

// TestPinReplaceRouteNeverStartsAPruneThatCannotFinish holds the rule of
// the destructive route. A prune cannot be undone, so no state may name it
// while the new pin would be refused. Each case keeps w refused for a scope
// reason, the state that names the route.
func TestPinReplaceRouteNeverStartsAPruneThatCannotFinish(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *fakeServices)
		next string
		// pruneRuns says that - still works, so the wrong route would run.
		pruneRuns bool
	}{
		{"--db of another root", pinsAnotherDB, pinsOpenReplaceRoute, true},
		{"a copied checkout", pinsCopiedCheckout, pinRouteIndex, false},
		{
			"a pin file whose end takes no new pin",
			func(f *fakeServices) { f.pins.Doc.Bytes = []byte(pinsKeptTail) }, pinsHandRoute, true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pinsFake()
			c.edit(f)

			h := pinsOpen(t, f, 120, 40)

			p := pinsOf(h)
			pin := p.rows()[0].pin
			require.NotNil(t, pin)
			require.NotEmpty(t, p.retargetBlock(pin), "w is refused")
			require.Equal(t, c.pruneRuns, p.editBlock(pin) == "")

			assert.Equal(t, c.next, p.retargetNext(pin))
			assert.NotContains(t, p.scopeRoute(pin), "Prune")

			h.press("w")
			assert.True(t, strings.HasSuffix(h.m.toast.text, pinRouteSentence(c.next)), "toast: %s", h.m.toast.text)
			assert.NotContains(t, h.m.toast.text, "Prune")
			assert.NotContains(t, pinsDetail(t, h), pinsReplaceSentence)
		})
	}

	t.Run("a new read asks the planners again", func(t *testing.T) {
		f := pinsFake()
		h := pinsOpen(t, f, 120, 40)

		p := pinsOf(h)
		require.Equal(t, pinRouteReplace, p.retargetNext(p.rows()[0].pin))

		in := pinsInspection()
		in.Doc.Bytes = []byte(pinsKeptTail)
		authorReload(h, f, in)

		assert.Equal(t, pinsHandRoute, p.retargetNext(p.rows()[0].pin))
	})
}

// TestPinsAnotherDatabaseNeverRoutesToABuild walks each surface that can
// name the index build. Under --db a build writes this workspace into the
// database of the other repository, so each surface names the recorded root
// instead. A copied checkout keeps the build, because there it is the fix.
func TestPinsAnotherDatabaseNeverRoutesToABuild(t *testing.T) {
	const (
		text    = "the index records another repository root (/work/original), not this workspace (/work/repo)"
		build   = "build the index of this workspace in Overview, then review again"
		openNew = "open the console in that repository, because a save works there"
	)

	blocker := maintain.AuthorBlocker{Kind: maintain.BlockForeignRoot, Text: text, Route: build}

	open := func(t *testing.T, setup func(*fakeServices)) (*harness, *fakeServices) {
		t.Helper()

		f := authorFake(func(p *maintain.AuthorPlan) {
			p.Confirmable = false
			p.Blockers = []maintain.AuthorBlocker{blocker}
		})
		f.pins = pinsInspection()
		setup(f)

		return pinsOpen(t, f, 200, 50), f
	}

	t.Run("the detail and the form under --db", func(t *testing.T) {
		h, _ := open(t, pinsAnotherDB)
		pinsCursorTo(h, 2)

		detail := pinsDetail(t, h)
		assert.Contains(t, detail, "i and d still draft a change; to save it, open the console in /work/original.")
		assert.NotContains(t, detail, "readable index")

		h.press("i")
		require.NotNil(t, authorOf(h).form)
		assert.Contains(t, lesBoxFlat(h.view()), "; to save, open the console in /work/original.")
		assert.NotContains(t, lesBoxFlat(h.view()), pinUnknownTail)
	})

	t.Run("a review under --db has no build route", func(t *testing.T) {
		h, _ := open(t, pinsAnotherDB)
		pinsCursorTo(h, 5)
		h.press("d")

		flat := authorReviewFlat(t, h)
		assert.Contains(t, flat, "not this workspace (/work/repo); "+openNew+".")
		assert.NotContains(t, flat, "Overview")

		review := authorOf(h).review
		assert.Equal(t, -1, review.routed(), "o must not reach the build")

		h.press("o")
		assert.Same(t, review, authorOf(h).review, "o does nothing here")
	})

	t.Run("a review of a copied checkout keeps the build", func(t *testing.T) {
		h, _ := open(t, pinsCopiedCheckout)
		pinsCursorTo(h, 5)
		h.press("d")

		flat := authorReviewFlat(t, h)
		assert.Contains(t, flat, build)
		assert.Contains(t, flat, "Press o to open Overview")
		assert.Equal(t, 0, authorOf(h).review.routed())
	})

	t.Run("a blocker line reads the database", func(t *testing.T) {
		other := maintain.AuthorBlocker{Kind: maintain.BlockFile, Text: "a file reason", Route: "a file route"}

		h, _ := open(t, pinsAnotherDB)
		assert.Equal(t, text+"; "+openNew, pinBlockerLine(pinsOf(h).st, blocker))
		assert.Equal(t, other.String(), pinBlockerLine(pinsOf(h).st, other), "only the root blocker moves")

		h, _ = open(t, pinsCopiedCheckout)
		assert.Equal(t, blocker.String(), pinBlockerLine(pinsOf(h).st, blocker))
	})

	t.Run("a refused commit reads the database", func(t *testing.T) {
		refused := fmt.Errorf("wrapped: %w", services.ErrForeignRoot)

		h, _ := open(t, pinsAnotherDB)
		text := pinCommitRefusal(pinsOf(h).st, refused)
		assert.Contains(t, text, "Open the console in that repository")
		assert.NotContains(t, text, "Overview")

		h, _ = open(t, pinsCopiedCheckout)
		assert.Contains(t, pinCommitRefusal(pinsOf(h).st, refused), "Build the index of this workspace in Overview")
	})
}

// TestPinTailRefusedReadsTheFileAfterThePrune runs the real planners. A
// prune decides which entry comes last, so the answer depends on the pin.
func TestPinTailRefusedReadsTheFileAfterThePrune(t *testing.T) {
	const (
		first  = "  - rule: plain-first\n    region: api\n    note: Plain.\n"
		second = "  - rule: plain-second\n    region: db\n    note: Plain too.\n"
		kept   = "  - rule: kept\n    region: docs\n    note: |+\n      Kept.\n\n"
	)

	cases := []struct {
		name  string
		pins  string
		prune string
		want  bool
	}{
		{"a plain file", first + second, "plain-second", false},
		{"a kept block that stays at the end", first + kept, "plain-first", true},
		{"a kept block that the prune removes", first + kept, "kept", false},
		{"a kept block that the prune moves to the end", kept + first, "plain-first", true},
		{"a kept block that stays inside", kept + first + second, "plain-second", false},
		{"the only pin", first, "plain-first", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := pinfile.Parse([]byte("pin:\n" + c.pins))

			i := slices.IndexFunc(doc.Entries, func(e pinfile.Entry) bool { return e.Rule == c.prune })
			require.GreaterOrEqual(t, i, 0)

			assert.Equal(t, c.want, pinTailRefused(doc, doc.Entries[i].Ref()))
		})
	}
}

// TestPinRuleLockFollowsTheReplaceGate covers the locked rule of a linked
// pin. It names the prune only while the prune and the new pin can finish.
func TestPinRuleLockFollowsTheReplaceGate(t *testing.T) {
	cases := []struct {
		name string
		edit func(in *maintain.Inspection)
		want string
	}{
		{
			"a pin file that cannot be edited",
			func(in *maintain.Inspection) { in.Doc.FileBlock = "the file is read-only" },
			"the pin file cannot be edited (the file is read-only); " + pinsHandRoute,
		},
		{
			"a pin file whose end takes no new pin",
			func(in *maintain.Inspection) { in.Doc.Bytes = []byte(pinsKeptTail) },
			"the pin file takes no new pin after this prune; " + pinsHandRoute,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			f.pins = pinsInspection()
			c.edit(f.pins)

			h := pinsOpen(t, f, 120, 40)
			h.press("i")

			form := authorOf(h).form
			require.NotNil(t, form)

			d := form.draft
			assert.Equal(t, "linked to proposal #4; "+c.want, d.ruleLock)
			assert.NotContains(t, d.ruleLock, "Prune")
			assert.NotContains(t, d.scopeLock, "Prune")
			assert.False(t, pinStoredLock(d.ruleLock), "a reload must keep this lock")

			authorReload(h, f, f.pins)
			assert.Equal(t, "linked to proposal #4; "+c.want, d.ruleLock, "a reload keeps the words")
		})
	}
}

// TestPinsRefusalOfAnUnreadableWorkspaceNamesTheFix covers the state with
// no target: the pins do not read, so the refusal names the workspace.
func TestPinsRefusalOfAnUnreadableWorkspaceNamesTheFix(t *testing.T) {
	const want = "No retarget here. The pin file or the ledger could not be read. Fix it, then press r."

	for name, in := range map[string]*maintain.Inspection{
		"a malformed file": pinsMalformed(), "no ledger": pinsNoLedger(),
	} {
		t.Run(name, func(t *testing.T) {
			f := pinsFake()
			f.pins = in

			h := pinsOpen(t, f, 120, 40)
			h.press("w")

			assert.Equal(t, want, h.m.toast.text)
			assert.Empty(t, f.called())
		})
	}
}

// TestPinRetargetRouteWithoutAnInspection covers the gate while no read has
// finished. Update can reach it, so the words must hold there too.
func TestPinRetargetRouteWithoutAnInspection(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)

	p := pinsOf(h)
	pin := p.rows()[0].pin
	require.NotNil(t, pin)
	require.True(t, pinLinked(pin))

	p.st.pins.loaded = false

	assert.Equal(t, "the pin file is not read", p.retargetBlock(pin))
	assert.Equal(t, pinRouteRead, p.retargetNext(pin))
	assert.Equal(t, "no retarget: the pin file is not read; "+pinRouteRead, p.scopeRoute(pin))
}

// TestPinScopeRouteReadsInTheDetailAndTheForm covers the surfaces of a
// linked pin that reads n/a on w. The note stays editable, because a
// retarget and a note edit have separate gates.
func TestPinScopeRouteReadsInTheDetailAndTheForm(t *testing.T) {
	for _, c := range []struct {
		name   string
		row    int
		reason string
	}{
		{"a current scope", 1, "its regions are already current"},
		{"no living finding", 3, "no cited finding is living, so no scope can be computed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := authorFake(nil)
			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, c.row)

			assert.Contains(t, pinsDetail(t, h), "No retarget: "+c.reason+". "+pinsReplaceSentence)

			h.press("i")

			form := authorOf(h).form
			require.NotNil(t, form)
			assert.Contains(t, form.draft.scopeLock, "; no retarget: "+c.reason+"; "+pinRouteReplace)
			assert.False(t, pinStoredLock(form.draft.scopeLock), "a reload must keep this lock")

			h.press("down", "enter")
			h.typeText(" More.")
			h.press("esc")

			req := authorReq(t, form.draft)
			assert.True(t, req.NoteSet, "a note edit stays available")
			assert.False(t, req.ScopeSet)
		})
	}
}

func TestPinsRefusalKeepsItsReasonAt80Columns(t *testing.T) {
	cases := []struct {
		row    int
		keys   []string
		reason string
	}{
		{1, []string{"w"}, "No retarget: its regions are already current"},
		{1, []string{"t"}, "No trigger extraction: its trigger paths are already stored"},
		{1, []string{"v"}, "No comparison: no other linked pin has similar wording"},
		{9, []string{"-"}, "No prune: it has an ambiguous link"},
		{5, []string{"v"}, "No comparison: it has no linked proposal, and only linked pins are compared"},
		{3, []string{"space", "w"}, "No retarget: no cited finding is living, so no scope can be computed"},
		{1, []string{"f", "t"}, "No trigger extraction in a history list. Press f to show installed pins."},
	}

	for _, c := range cases {
		f := pinsFake()
		h := pinsOpen(t, f, 80, 24)
		pinsCursorTo(h, c.row)
		h.press(c.keys...)

		// The notice row cuts a long line at its end. The reason comes
		// before the pin, so the cut never reaches it.
		frame := h.view()
		assertFrame(t, frame, 80, 24)
		assert.Contains(t, pinsNoticeRow(frame), c.reason, "%v on row %d", c.keys, c.row)
		assert.Empty(t, f.called())
	}
}

func TestPinsUnavailableKeysInAListWithoutTarget(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 200, 50)
	h.press("f")

	// A history list offers no target: only n works. Its list keys lead,
	// because f is the route out of a history list. The n/a group follows
	// them, before the switch keys.
	footer := footerLine(h.view())
	assert.Contains(t, footer, " n new pin   tab detail   f show   / region   z search   "+
		"n/a: i edit, d remove, - prune, w retarget, t extract, v compare   p proposals   s stats   e evidence")

	// The reference footer keeps the group by its keys.
	h.resize(120, 40)
	assert.True(t, strings.HasPrefix(footerLine(h.view()),
		" n new pin   tab detail   f show   / region   z search   n/a: i d - w t v  …"), footerLine(h.view()))

	// The help names the selection key as well, because history rows
	// cannot be selected.
	_, off := helpParts(h)
	assert.Equal(t, "space n/a select i n/a edit d n/a remove - n/a prune w n/a retarget t n/a extract v n/a compare "+
		"x n/a clear marks", strings.TrimSpace(off))

	h.press("v")
	assert.Equal(t, "No comparison in a history list. Press f to show installed pins.", h.m.toast.text)

	h.press("w")
	assert.Equal(t, "No retarget in a history list. Press f to show installed pins.", h.m.toast.text)

	h.press("i")
	assert.Equal(t, "No edit in a history list. Press f to show installed pins.", h.m.toast.text)

	h.press("d")
	assert.Equal(t, "No remove in a history list. Press f to show installed pins.", h.m.toast.text)
	assert.Empty(t, f.called())

	// Without a readable ledger every link is unknown, so no similar pin
	// is known.
	f2 := pinsFake()
	f2.pins = pinsNoLedger()
	h2 := pinsOpen(t, f2, 120, 40)

	h2.press("v")
	assert.Equal(t, `No comparison: its link is unknown, so no similar pin is known ("wrap-sql-errors").`,
		h2.m.toast.text)
	assert.Empty(t, f2.called())
}

func TestPinsAvailableKeysStillAct(t *testing.T) {
	f := pinsFake()
	h := pinsOpen(t, f, 120, 40)

	// p12 has a similar pin, so v opens the comparison as before.
	pinsCursorTo(h, 3)
	h.press("v")
	require.NotNil(t, pinsOf(h).compare)
	assert.Empty(t, f.called(), "the comparison reads nothing")
	h.press("esc")

	// p9 offers a retarget, so w plans its preview as before.
	pinsCursorTo(h, 2)
	h.press("w")
	assert.Equal(t, []string{"PlanMaintenance:retarget"}, f.called())
}

func TestPinsHelpListsEveryPinsKeyWithTheMarker(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)
	h.view()
	h.press("?")

	// The three-column help of the reference size shows the n/a group in
	// its first window.
	frame := lesFlat(h.view())
	assert.Contains(t, frame, "n/a: not available now")
	assert.Contains(t, frame, "w n/a retarget")
	assert.Contains(t, frame, "m pins")

	// Pin 1 is linked: it offers select, new pin, a note edit and prune;
	// nothing is selected yet. d names Prune for a linked pin.
	on, off := helpParts(h)

	for _, want := range []string{
		"space select", "n new pin", "i edit", "- prune", "tab detail", "f show", "/ region", "z search",
		"p proposals", "m pins", "s stats", "e evidence",
	} {
		assert.Contains(t, on, want)
	}

	assert.Equal(t, "d n/a remove w n/a retarget t n/a extract v n/a compare x n/a clear marks", strings.TrimSpace(off))

	// A selection makes x work, and pin 2 offers every action but d and v.
	h.press("esc")
	pinsCursorTo(h, 2)
	h.press("space", "?")

	on, off = helpParts(h)
	for _, want := range []string{"n new pin", "i edit", "w retarget", "- prune", "t extract", "x clear marks", "m pins"} {
		assert.Contains(t, on, want)
	}

	assert.Equal(t, "d n/a remove v n/a compare", strings.TrimSpace(off))

	// The help lists every key at the compact and minimum sizes too.
	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])

		on, off = helpParts(h)
		for _, want := range []string{"space select", "n new pin", "i edit", "w retarget", "- prune", "t extract",
			"x clear marks", "f show", "/ region", "z search", "m pins"} {
			assert.Contains(t, on, want, "%dx%d", size[0], size[1])
		}

		assert.Equal(t, "d n/a remove v n/a compare", strings.TrimSpace(off), "%dx%d", size[0], size[1])
	}

	// An unlinked pin offers i and d; a pending pin offers neither.
	h.resize(120, 40)
	h.press("esc", "x")
	pinsCursorTo(h, 5)
	h.press("?")

	on, off = helpParts(h)
	assert.Contains(t, on, "i edit")
	assert.Contains(t, on, "d remove")
	assert.NotContains(t, off, "i n/a")
	assert.NotContains(t, off, "d n/a")

	h.press("esc")
	pinsCursorTo(h, 6)
	h.press("?")

	_, off = helpParts(h)
	assert.Contains(t, off, "i n/a edit d n/a remove")
}

func TestLessonsHelpListsEverySwitchKeyInEverySubView(t *testing.T) {
	f := pinsFake()
	h := lesOpen(t, f, 120, 40)

	for _, sub := range []string{"p", "m", "s", "e"} {
		h.press(sub)

		on, off := helpParts(h)
		assert.Contains(t, on, "p proposals m pins s stats e evidence", "sub-view %s", sub)

		// Pins and Proposals list their unavailable keys. Stats and
		// Evidence keep their help.
		if sub == "s" || sub == "e" {
			assert.Empty(t, off, "sub-view %s", sub)
		}
	}

	// The footer of Proposals is unchanged: its keys and the other switch
	// keys, without p itself.
	h.press("p")
	assert.NotContains(t, footerLine(h.view()), "n/a")
}
