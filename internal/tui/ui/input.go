package ui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// This file holds LineInput, the one-line text input of the console.
//
// The console draws its own widgets and adopts no charmbracelet/bubbles
// widget. Read these four reasons before you propose one again:
//
//   - textinput shows its cursor in reverse video, not as a glyph. The
//     Ascii profile of NO_COLOR and of the tests drops that attribute, so
//     the cursor vanishes. LineInput shows the "_" and "|" glyphs instead.
//     The Validate of textinput reports an invalid value but stores it
//     too, so the digit filter and the length bound need their own check.
//   - spinner sets its next frame through tea.Tick, outside the
//     injectable after of app.go. The synchronous test harness then never
//     settles. SpinnerFrame and the one tick of the shell stay instead.
//   - viewport pads its view to its height and cuts a wide line without
//     an ellipsis. It clamps the offset only past the last line, so a box
//     that grows shows blank rows. Its default keys b and u clash with two
//     screen keys of the console. ScrollState, TextPager and the boxes of
//     box.go stay instead. TextPager still copies the clamp rule and the
//     hard cut. It replaced a viewport, and the copy keeps every frame the
//     same.
//   - table truncates a cell without regard to escape sequences, so a
//     styled cell breaks at the wrong place. It shows the selection by
//     color only, which NO_COLOR removes. Each view keeps its own column
//     calculator and the cursor glyph.

// lineInputLimit bounds the value of a LineInput that sets no limit, for
// example a path filter. A path longer than this is a paste accident,
// not a filter.
const lineInputLimit = 256

// LineInput is a minimal single-line text input. The filter bars, the
// editors and the forms of the console use it. A
// filter or a count needs only typing, deletion and cursor movement. The
// optional fields let each form set its own hint, bound and runes. Their
// zero values keep the path filter behaviour, because the path filter
// was the first user.
type LineInput struct {
	runes []rune
	pos   int

	// Placeholder is the faint hint of an empty input. "" keeps the
	// path hint.
	Placeholder string
	// Limit bounds the value in runes. 0 keeps lineInputLimit.
	Limit int
	// Accept filters typed runes, for example digits only for a day
	// count. nil accepts every rune that the sanitizer keeps.
	Accept func(rune) bool
}

// maxRunes returns the length bound of the value.
func (in *LineInput) maxRunes() int {
	if in.Limit > 0 {
		return in.Limit
	}

	return lineInputLimit
}

// accepts reports whether the accept filter lets r into the value.
func (in *LineInput) accepts(r rune) bool {
	return in.Accept == nil || in.Accept(r)
}

// Set replaces the value and puts the cursor at its end.
func (in *LineInput) Set(s string) {
	in.runes = []rune(s)
	in.pos = len(in.runes)
}

// Value returns the typed text.
func (in *LineInput) Value() string {
	return string(in.runes)
}

// Key applies one key press. The sanitizer rejects control and
// direction characters, so the filter bar cannot print an escape
// sequence or reorder text. The length bound and the accept filter
// drop further runes.
func (in *LineInput) Key(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		for _, r := range msg.Runes {
			if s := string(r); OneLine(s) != s || len(in.runes) >= in.maxRunes() || !in.accepts(r) {
				continue
			}

			in.runes = slices.Insert(in.runes, in.pos, r)
			in.pos++
		}
	case tea.KeyBackspace:
		if in.pos > 0 {
			in.runes = slices.Delete(in.runes, in.pos-1, in.pos)
			in.pos--
		}
	case tea.KeyDelete:
		if in.pos < len(in.runes) {
			in.runes = slices.Delete(in.runes, in.pos, in.pos+1)
		}
	case tea.KeyCtrlU:
		in.runes = in.runes[in.pos:]
		in.pos = 0
	case tea.KeyLeft:
		in.pos = max(0, in.pos-1)
	case tea.KeyRight:
		in.pos = min(len(in.runes), in.pos+1)
	case tea.KeyHome, tea.KeyCtrlA:
		in.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		in.pos = len(in.runes)
	}
}

// View renders the value with the cursor as a glyph. NO_COLOR removes
// every style, reverse video too, so only a glyph stays visible. At the
// end of the text the cursor is an underscore. Inside the text it is a
// bar between two characters, where typing inserts. A bar is rare in a
// path, but an underscore is common there.
func (in *LineInput) View(t *Theme) string {
	if len(in.runes) == 0 {
		hint := in.Placeholder
		if hint == "" {
			hint = "path, for example internal/api"
		}

		return t.Accent.Render("_") + t.Faint.Render(" "+OneLine(hint))
	}

	before := t.Body.Render(string(in.runes[:in.pos]))

	if in.pos == len(in.runes) {
		return before + t.Accent.Render("_")
	}

	return before + t.Accent.Render("|") + t.Body.Render(string(in.runes[in.pos:]))
}
