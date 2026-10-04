package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of the comparison widgets of installed lesson maintenance:
// regionDelta, pair, and the pinned scope tail of a modal. Each runs at
// the inner widths of a modal at 60, 80 and 120 columns: 52, 72 and 76
// cells, with the Unicode and the ASCII glyphs.

// widgetWidths are the modal body widths at 60×20, 80×24 and 120×40.
var widgetWidths = []int{52, 72, 76}

// widgetPlain strips styles and the trailing spaces of every line.
func widgetPlain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}

	return out
}

// widgetFits checks that every line fits w cells and holds no terminal
// control byte.
func widgetFits(t *testing.T, lines []string, w int) {
	t.Helper()

	for i, l := range lines {
		assert.LessOrEqual(t, width(l), w, "line %d is wider than %d: %q", i, w, l)
		assert.NotContains(t, l, "\x1b", "line %d", i)
		assert.NotContains(t, l, "\a", "line %d", i)
		assert.NotContains(t, l, "\u009b", "line %d", i)
	}
}

func TestRegionDeltaNamesEveryRowWithAWord(t *testing.T) {
	before := []string{"internal/store", "internal/api"}
	after := []string{"internal/store/sql", "internal/api"}

	for _, w := range widgetWidths {
		u := newTheme(false).regionDelta(before, after, w)
		assert.Equal(t, []string{
			"− internal/store       removed",
			"+ internal/store/sql   added",
			"  internal/api         kept",
		}, widgetPlain(u), "Unicode at %d", w)
		widgetFits(t, u, w)

		a := newTheme(true).regionDelta(before, after, w)
		assert.Equal(t, []string{
			"- internal/store       removed",
			"+ internal/store/sql   added",
			"  internal/api         kept",
		}, widgetPlain(a), "ASCII at %d", w)
		widgetFits(t, a, w)

		for _, l := range widgetPlain(a) {
			for _, r := range l {
				require.Less(t, r, rune(128), "ASCII at %d: %q", w, l)
			}
		}
	}
}

func TestRegionDeltaNeverMarksAKeptRegionAsAChange(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		th := newTheme(ascii)
		g := th.Glyphs

		rows := widgetPlain(th.regionDelta([]string{"api", "db"}, []string{"db", "api"}, 52))
		assert.Equal(t, []string{"  api   kept", "  db    kept"}, rows, "the order of a set is no change")

		for _, l := range rows {
			assert.False(t, strings.HasPrefix(l, g.OK), "a kept row has no OK glyph: %q", l)
			assert.False(t, strings.HasPrefix(l, g.Info), "a kept row has no Info glyph: %q", l)
			assert.False(t, strings.HasPrefix(l, g.Add), "a kept row reads as no addition: %q", l)
			assert.False(t, strings.HasPrefix(l, g.Del), "a kept row reads as no removal: %q", l)
		}
	}
}

func TestRegionDeltaShowsTheRepositoryWideSet(t *testing.T) {
	th := newTheme(false)

	assert.Equal(t, []string{
		"− repository-wide   removed",
		"+ internal/api      added",
	}, widgetPlain(th.regionDelta(nil, []string{"internal/api"}, 52)), "a narrower scope")

	assert.Equal(t, []string{
		"− internal/api      removed",
		"+ repository-wide   added",
	}, widgetPlain(th.regionDelta([]string{"internal/api"}, nil, 52)), "a repository-wide target")

	assert.Equal(t, []string{"  repository-wide   kept"}, widgetPlain(th.regionDelta(nil, []string{}, 52)))

	assert.Equal(t, []string{"  api   kept"}, widgetPlain(th.regionDelta([]string{"api", "api"}, []string{"api"}, 52)),
		"a repeated region shows once")
}

func TestRegionDeltaWrapsALongRegionWithItsSign(t *testing.T) {
	long := "internal/" + strings.Repeat("deeply/nested/", 5) + "package"

	for _, w := range widgetWidths {
		for _, ascii := range []bool{false, true} {
			th := newTheme(ascii)
			lines := th.regionDelta([]string{long}, []string{"internal/api"}, w)
			rows := widgetPlain(lines)

			widgetFits(t, lines, w)
			require.Greater(t, len(rows), 2, "the long region wraps at %d", w)
			assert.True(t, strings.HasSuffix(rows[0], "removed"), rows[0])

			// Every row of the removed region keeps the removal sign. A
			// continuation row indents its text and carries no word, so
			// it never reads as one more region.
			for i, r := range rows[:len(rows)-1] {
				assert.True(t, strings.HasPrefix(r, th.Glyphs.Del+" "), "%q at %d", r, w)

				if i > 0 {
					text := strings.TrimPrefix(r, th.Glyphs.Del+" ")
					assert.True(t, strings.HasPrefix(text, "  ") && !strings.HasPrefix(text, "   "),
						"%q at %d", r, w)
					assert.NotContains(t, r, "removed")
				}
			}

			parts := widgetRegionParts(rows[:len(rows)-1])
			assert.Equal(t, long, strings.Join(parts, ""), "the wrapped rows hold the whole region")

			// Each row but the last ends at a path boundary.
			for _, part := range parts[:len(parts)-1] {
				assert.True(t, strings.HasSuffix(part, "/"), "%q at %d", part, w)
			}

			assert.True(t, strings.HasSuffix(rows[len(rows)-1], "added"))
		}
	}
}

// Review fix: a region wrapped at the cell limit read as a second
// region, for example "+ raps/around". It now breaks after a '/'.
func TestRegionDeltaWrapsAtPathBoundaries(t *testing.T) {
	// row is the first row of a region in a region column of regW cells.
	row := func(sign, region, word string, regW int) string {
		return sign + " " + padRight(region, regW) + "   " + word
	}

	for _, ascii := range []bool{false, true} {
		th := newTheme(ascii)
		add, del := th.Glyphs.Add, th.Glyphs.Del

		// The cell limit of the 19-cell column falls right after a '/':
		// no row starts with a separator.
		assert.Equal(t, []string{
			row(del, "internal/api", "removed", 19),
			row(add, "internal/store/sql/", "added", 19),
			add + "   migrations",
		}, widgetPlain(th.regionDelta([]string{"internal/api"}, []string{"internal/store/sql/migrations"}, 31)))

		// A file path of 39 cells in the Pins detail at 120×40, whose
		// value column is 43 cells: the file name moves whole.
		file := "internal/store/sql/migrations_runner.go"
		assert.Equal(t, []string{
			row(add, "internal/store/sql/", "added", 31),
			add + "   migrations_runner.go",
			row(" ", "internal/api", "kept", 31),
		}, widgetPlain(th.regionDelta([]string{"internal/api"}, []string{"internal/api", file}, 43)))

		// A single part wider than the column still breaks at the cell
		// limit, and no row overflows.
		wide := "a/" + strings.Repeat("x", 50)
		lines := th.regionDelta(nil, []string{wide}, 43)
		widgetFits(t, lines, 43)

		rows := widgetPlain(lines)
		require.Len(t, rows, 4, "%q", rows)
		assert.Equal(t, wide, strings.Join(widgetRegionParts(rows[1:]), ""))
	}
}

// widgetRegionParts returns the region text of delta rows: the text
// after the sign and the continuation indent, without the word of the
// first row.
func widgetRegionParts(rows []string) []string {
	out := make([]string, len(rows))

	for i, r := range rows {
		_, text, _ := strings.Cut(r, " ")
		text = strings.TrimSuffix(strings.TrimRight(text, " "), "removed")
		text = strings.TrimSuffix(text, "added")
		out[i] = strings.Trim(text, " ")
	}

	return out
}

func TestRegionDeltaSanitizesTheRegions(t *testing.T) {
	th := newTheme(false)

	lines := th.regionDelta([]string{"\x1b[31mapi\x07"}, []string{"db\u009b2J", "web\nevil"}, 52)
	widgetFits(t, lines, 52)

	rows := widgetPlain(lines)
	assert.Equal(t, "− [31mapi    removed", rows[0], "the escape byte goes; the text stays inert")
	assert.Contains(t, rows[1], "db2J")
	assert.Contains(t, rows[2], "web evil", "a line break never splits a row")
}

func TestPairStandsSideBySideFromSeventyTwoCells(t *testing.T) {
	left := []string{"Check every error returned by the", "store before you use the value."}
	right := []string{"Check every error returned by the store."}

	for _, w := range widgetWidths {
		for _, ascii := range []bool{false, true} {
			th := newTheme(ascii)
			lines := th.pair("LIVE  lessons.yaml:9", "PROPOSAL p9  recorded", left, right, w)
			rows := widgetPlain(lines)

			widgetFits(t, lines, w)

			if w < pairMinW {
				assert.Equal(t, []string{
					"LIVE  lessons.yaml:9", left[0], left[1], "", "PROPOSAL p9  recorded", right[0],
				}, rows, "the sides stack at %d", w)
				assert.Equal(t, w, pairWidth(w))

				continue
			}

			lw := pairWidth(w)
			assert.Equal(t, (w-pairGap)/2, lw)
			require.Len(t, rows, 3, "the head row and the longer side at %d", w)

			// The left text is ASCII, so a byte index is a cell index.
			div := " " + th.Glyphs.Vertical + " "
			for i, r := range rows {
				assert.Equal(t, lw, strings.Index(r, " "+th.Glyphs.Vertical), "row %d at %d keeps the divider column: %q",
					i, w, r)
			}

			assert.True(t, strings.HasPrefix(rows[0], "LIVE  lessons.yaml:9"))
			assert.True(t, strings.HasSuffix(rows[0], div+"PROPOSAL p9  recorded"))
			assert.True(t, strings.HasSuffix(rows[2], strings.TrimRight(div, " ")),
				"the shorter side leaves its cell empty: %q", rows[2])

			if ascii {
				for _, r := range strings.Join(rows, "") {
					require.Less(t, r, rune(128))
				}
			}
		}
	}
}

func TestPairClipsALineThatIgnoresItsWidth(t *testing.T) {
	long := strings.Repeat("word ", 30)

	for _, w := range widgetWidths {
		th := newTheme(false)
		lines := th.pair("A", "B", []string{long}, []string{long}, w)

		widgetFits(t, lines, w)
		assert.Contains(t, ansi.Strip(strings.Join(lines, "\n")), th.Glyphs.Ellipsis, "a clipped line shows the cut")
	}
}

func TestModalTailStaysAboveTheActions(t *testing.T) {
	th := newTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + itoa(i+1)
	}

	var sc scrollState

	o := modalOpts{
		Title: "Remove p16", Body: body,
		Tail:    []string{"Removes 1 pin · p16 › superseded"},
		Actions: []key.Binding{withHelp(nkeys.Back, "esc", "cancel")},
	}

	// The body area of a 60×20 frame is 15 rows under the chrome.
	for _, off := range []int{0, 100} {
		sc.by(off)

		lines := strings.Split(ansi.Strip(th.modal(o, &sc, 60, 15)), "\n")
		n := len(lines)

		assert.LessOrEqual(t, n, 15-modalMargin, "the modal keeps its margin")
		assert.Contains(t, lines[n-2], "esc cancel", "the actions stay last")
		assert.Contains(t, lines[n-3], "Removes 1 pin · p16 › superseded", "the scope stays above the actions")
		assert.Contains(t, lines[n-4], strings.Repeat(th.Glyphs.RuleLight, 10), "the rule separates the body")
		assert.True(t, sc.scrolls(), "the body scrolls under the fixed rows")
	}

	// Without actions the scope summary still stays under the rule.
	o.Actions = nil
	lines := strings.Split(ansi.Strip(th.modal(o, &scrollState{}, 60, 15)), "\n")
	assert.Contains(t, lines[len(lines)-2], "Removes 1 pin")
}
