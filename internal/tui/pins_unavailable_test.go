package tui

import (
	"errors"
	"slices"
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
)

// The tests of the unavailable action keys in Lessons › Pins (user
// decision of 2026-10-04: "Always list, mark unavailable"). The footer
// and the help list w, -, t and v. A key that the target cannot use
// reads n/a, and its press explains why and calls no service.

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
	on, off, _ = strings.Cut(body, unavailableWord+": not available now")

	return on, off
}

// pinsFooterActions returns the action keys among w, -, t and v that a
// footer names, as a hint or inside its n/a group.
func pinsFooterActions(footer string) []string {
	var got []string

	for _, f := range strings.Fields(footer) {
		if slices.Contains([]string{"w", "-", "t", "v"}, f) && !slices.Contains(got, f) {
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
	}{
		// The case of the user report: current regions, an answered
		// trigger question, and no similar pin.
		{"current scope", 1, " space select   - prune   n/a: w t v   tab detail   f show   / region  …"},
		{"every action but compare", 2, " space select   w retarget   - prune   t extract triggers   n/a: v  …"},
		{"similar pin without evidence", 3, " space select   - prune   v compare   n/a: w t   tab detail   f show  …"},
		{"ambiguous", 9, " space select   n/a: w - t v   tab detail   f show   / region   z search  …"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := pinsOpen(t, pinsFake(), 120, 40)
			pinsCursorTo(h, c.row)

			// The reference footer cannot hold every hint, so the n/a group
			// shows its keys only. The four action keys come first, and tab,
			// f and / still fit after them.
			footer := shellAFooter(h.view())
			assert.True(t, strings.HasPrefix(footer, c.footer), "footer %q", footer)
			assert.Regexp(t, `\d\d:\d\d:\d\d $`, footer, "the clock stays at the reference size")
			assert.Empty(t, h.m.toast.text, "browsing shows no refusal")
		})
	}

	// A footer with room for every hint keeps the actions of the group.
	h := pinsOpen(t, pinsFake(), 200, 50)
	assert.Contains(t, shellAFooter(h.view()), " space select   - prune   "+
		"n/a: w retarget, t extract triggers, v compare   tab detail   f show")
}

func TestPinsFooterKeepsTheActionKeysInEveryListAtTheReferenceSize(t *testing.T) {
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

	lists := []struct {
		name string
		in   func() *maintain.Inspection
		keys []string
		rows int
	}{
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

	for _, ascii := range []bool{false, true} {
		for _, l := range lists {
			f := pinsFake()
			f.pins = l.in()

			h := lesOpenTheme(t, f, ascii)
			h.press("m")
			h.press(l.keys...)

			for row := 1; row <= l.rows; row++ {
				pinsCursorTo(h, row)

				footer := shellAFooter(h.view())
				assert.ElementsMatch(t, []string{"w", "-", "t", "v"}, pinsFooterActions(footer),
					"%s, row %d, ascii %t: %q", l.name, row, ascii, footer)
			}
		}
	}
}

func TestPinsCompactFooterGivesTheClockToTheUnavailableKeys(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		h := lesOpenTheme(t, pinsFake(), ascii)
		h.press("m")
		h.resize(80, 24)

		// The case of the user report at 80x24. The short group needs one
		// cell more than the clock leaves, so the clock gives way and w, t
		// and v stay in view.
		frame := h.view()
		assertFrame(t, frame, 80, 24)

		footer := shellAFooter(frame)
		ellipsis := h.m.st.theme.Glyphs.Ellipsis
		assert.True(t, strings.HasPrefix(footer, " space select   - prune   n/a: w t v  "+ellipsis), "footer %q", footer)
		assert.NotRegexp(t, `\d\d:\d\d:\d\d`, footer, "ascii %t", ascii)
		assert.Contains(t, footer, "r refresh   ? help   q quit")

		// Pin 2 has working actions that already fill the room, so the
		// n/a group cannot fit even without the clock. The clock then
		// stays, and the footer drops hints from the end as before.
		pinsCursorTo(h, 2)

		footer = shellAFooter(h.view())
		assert.True(t, strings.HasPrefix(footer, " space select   w retarget  "+ellipsis), "footer %q", footer)
		assert.Regexp(t, `\d\d:\d\d:\d\d $`, footer, "ascii %t", ascii)
	}
}

func TestPinsUnavailableEntriesAreNeverOfferedKeys(t *testing.T) {
	h := pinsOpen(t, pinsFake(), 120, 40)
	h.view()

	// An unavailable entry is not an offered key, so every check of the
	// offered keys still leaves it out.
	assert.Equal(t, []string{"space", "-", "tab", "f", "/", "z", "p", "s", "e"}, pinsKeys(h))

	for _, b := range hookLessons(h).keys() {
		if isUnavailable(b) {
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

	footer := shellAFooter(h.view())
	assert.True(t, strings.HasPrefix(footer, " space select   - prune   n/a: w t v   tab detail   f show   / region  ..."),
		"footer %q", footer)

	for _, r := range h.view() {
		require.Less(t, r, rune(128), "the ASCII frame holds %q", r)
	}

	h.press("?")

	// Each unavailable row carries the word itself, not only the label
	// row above it.
	on, off := helpParts(h)
	assert.Contains(t, on, "- prune")
	assert.Contains(t, off, "w n/a retarget t n/a extract triggers v n/a compare x n/a clear marks")

	for _, r := range h.view() {
		require.Less(t, r, rune(128), "the ASCII help holds %q", r)
	}

	// The ASCII movement label fits the key column, so its action starts
	// in the same column as every other action.
	rows, _ := h.m.helpLayout(80, h.m.st.height-1)
	assert.Contains(t, rows, padRight("up/dn j k", helpKeyW)+"move")
	assert.Contains(t, rows, padRight("g  G", helpKeyW)+"top / bottom")
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
		for _, action := range []string{"retarget", "extract triggers", "compare", "clear marks"} {
			if strings.Contains(line, action) {
				shown++

				assert.Contains(t, line, unavailableWord, "the row of %q reads n/a on its own", action)
			}
		}
	}

	assert.Positive(t, shown, "an unavailable row is in the window")
}

func TestHintsRenderTheUnavailableGroupMuted(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	th := newTheme(false)

	hidden := pinKeys.Compare
	hidden.SetEnabled(false)

	bindings := []key.Binding{
		pinKeys.Prune, unavailable(pinKeys.Retarget), unavailable(pinKeys.Compare), hidden, lesKeys.State,
	}
	out := th.hints(bindings)

	// The group is faint and has no key style, so it never looks like an
	// enabled key. The word n/a carries the state without color.
	assert.Contains(t, out, th.Faint.Render("n/a: w retarget, v compare"))
	assert.Contains(t, out, th.Key.Render("-"))
	assert.Contains(t, out, th.Key.Render("f"))
	assert.NotContains(t, out, th.Key.Render("w"))
	assert.Equal(t, "- prune   n/a: w retarget, v compare   f state filter", ansi.Strip(out))

	// The short form keeps the word, the faint style and the keys.
	short := th.shortHints(bindings)
	assert.Contains(t, short, th.Faint.Render("n/a: w v"))
	assert.NotContains(t, short, th.Key.Render("w"))
	assert.Equal(t, "- prune   n/a: w v   f state filter", ansi.Strip(short))

	// A binding that a screen only disables stays hidden, so no other
	// screen changes.
	assert.Empty(t, th.hints([]key.Binding{hidden}))
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
	assert.Contains(t, w, th.Faint.Render(padRight("w n/a", helpKeyW)+"retarget"))
	assert.NotContains(t, w, keyStart)

	// Pin 1 can prune: its row keeps the key style.
	assert.Contains(t, row("-"), th.Key.Render(padRight("-", helpKeyW)))
}

func TestPinsUnavailableKeyExplainsAndCallsNothing(t *testing.T) {
	cases := []struct {
		name  string
		row   int
		key   string
		toast string
	}{
		{"retarget", 1, "w", `No retarget: its regions are already current ("wrap-sql-errors").`},
		{"extraction", 1, "t", `No trigger extraction: its trigger paths are already stored ("wrap-sql-errors").`},
		{"compare", 1, "v", `No comparison: no other linked pin has similar wording ("wrap-sql-errors").`},
		{"prune", 9, "-", `No prune: it has an ambiguous link ("two-proposals").`},
		{"compare unlinked", 5, "v",
			`No comparison: it has no linked proposal, and only linked pins are compared ("keep-changelog-current").`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := pinsFake()
			h := pinsOpen(t, f, 120, 40)
			pinsCursorTo(h, c.row)

			h.press(c.key)

			assert.Empty(t, f.called(), "an unavailable key plans and writes nothing")
			assert.Equal(t, c.toast, h.m.toast.text)
			assert.Contains(t, pinsNoticeRow(h.view()), c.toast)

			_, open := hookLessons(h).modal(120, 39)
			assert.False(t, open, "an unavailable key opens no preview, review or comparison")
			assert.False(t, hookLessons(h).capturing())
			assert.Equal(t, lesPins, hookLessons(h).sub, "the review never opens the Run sub-view")
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

	// A history list offers no target. Its list keys lead, because f is
	// the route back. The n/a group follows them, before the switch keys.
	footer := shellAFooter(h.view())
	assert.Contains(t, footer, "tab detail   f show   / region   z search   "+
		"n/a: w retarget, - prune, t extract triggers, v compare   p proposals   s stats   e evidence")

	// The reference footer keeps the group by its keys.
	h.resize(120, 40)
	assert.True(t, strings.HasPrefix(shellAFooter(h.view()),
		" tab detail   f show   / region   z search   n/a: w - t v   p proposals  …"), shellAFooter(h.view()))

	// The help names the selection key as well, because history rows
	// cannot be selected.
	_, off := helpParts(h)
	assert.Equal(t, "space n/a select w n/a retarget - n/a prune t n/a extract triggers v n/a compare x n/a clear marks",
		strings.TrimSpace(off))

	h.press("v")
	assert.Equal(t, "No comparison in a history list. Press f to show installed pins.", h.m.toast.text)

	h.press("w")
	assert.Equal(t, "No retarget in a history list. Press f to show installed pins.", h.m.toast.text)
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

	// Pin 1 offers select and prune; nothing is selected yet.
	on, off := helpParts(h)

	for _, want := range []string{
		"space select", "- prune", "tab detail", "f show", "/ region", "z search",
		"p proposals", "m pins", "s stats", "e evidence",
	} {
		assert.Contains(t, on, want)
	}

	assert.Equal(t, "w n/a retarget t n/a extract triggers v n/a compare x n/a clear marks", strings.TrimSpace(off))

	// A selection makes x work, and pin 2 offers every action but v.
	h.press("esc")
	pinsCursorTo(h, 2)
	h.press("space", "?")

	on, off = helpParts(h)
	for _, want := range []string{"w retarget", "- prune", "t extract triggers", "x clear marks", "m pins"} {
		assert.Contains(t, on, want)
	}

	assert.Equal(t, "v n/a compare", strings.TrimSpace(off))

	// The help lists every key at the compact and minimum sizes too.
	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])

		on, off = helpParts(h)
		for _, want := range []string{"space select", "w retarget", "- prune", "t extract triggers", "x clear marks",
			"f show", "/ region", "z search", "m pins"} {
			assert.Contains(t, on, want, "%dx%d", size[0], size[1])
		}

		assert.Equal(t, "v n/a compare", strings.TrimSpace(off), "%dx%d", size[0], size[1])
	}
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
	assert.NotContains(t, shellAFooter(h.view()), "n/a")
}
