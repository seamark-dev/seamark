package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// This file holds lineInput, the one-line text input of the console.
//
// The console draws its own widgets and adopts no charmbracelet/bubbles
// widget. Read these four reasons before you propose one again:
//
//   - textinput shows its cursor in reverse video, not as a glyph. The
//     Ascii profile of NO_COLOR and of the tests drops that attribute, so
//     the cursor vanishes. lineInput shows the "_" and "|" glyphs instead.
//     The Validate of textinput reports an invalid value but stores it
//     too, so the digit filter and the length bound need their own check.
//   - spinner sets its next frame through tea.Tick, outside the
//     injectable after of app.go. The synchronous test harness then never
//     settles. spinnerFrame and the one tick of the shell stay instead.
//   - viewport pads its view to its height and cuts a wide line without
//     an ellipsis. It clamps the offset only past the last line, so a box
//     that grows shows blank rows. Its default keys b and u clash with the
//     Overview key b and the Integrations key u. scrollState, textPager
//     and the boxes of box.go stay instead. textPager still copies the
//     clamp rule and the hard cut. It replaced a viewport, and the copy
//     keeps every frame the same.
//   - table truncates a cell without regard to escape sequences, so a
//     styled cell breaks at the wrong place. It shows the selection by
//     color only, which NO_COLOR removes. Each view keeps its own column
//     calculator and the cursor glyph.

// lineInputLimit bounds the value of a lineInput that sets no limit, for
// example the region filter. A path scope longer than this is a paste
// accident, not a filter.
const lineInputLimit = 256

// lineInput is a minimal single-line text input. The Proposals and Pins
// filter bars, the Settings editor and the Distill scope form use it. A
// filter or a count needs only typing, deletion and cursor movement. The
// optional fields let each form set its own hint, bound and runes. Their
// zero values keep the region filter behaviour, because the region filter
// was the first user.
type lineInput struct {
	runes []rune
	pos   int

	// placeholder is the faint hint of an empty input. "" keeps the
	// region filter hint.
	placeholder string
	// limit bounds the value in runes. 0 keeps lineInputLimit.
	limit int
	// accept filters typed runes, for example digits only for a day
	// count. nil accepts every rune that the sanitizer keeps.
	accept func(rune) bool
}

// maxRunes returns the length bound of the value.
func (in *lineInput) maxRunes() int {
	if in.limit > 0 {
		return in.limit
	}

	return lineInputLimit
}

// accepts reports whether the accept filter lets r into the value.
func (in *lineInput) accepts(r rune) bool {
	return in.accept == nil || in.accept(r)
}

// set replaces the value and puts the cursor at its end.
func (in *lineInput) set(s string) {
	in.runes = []rune(s)
	in.pos = len(in.runes)
}

// value returns the typed text.
func (in *lineInput) value() string {
	return string(in.runes)
}

// key applies one key press. The sanitizer rejects control and
// direction characters, so the filter bar cannot print an escape
// sequence or reorder text. The length bound and the accept filter
// drop further runes.
func (in *lineInput) key(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		for _, r := range msg.Runes {
			if s := string(r); oneLine(s) != s || len(in.runes) >= in.maxRunes() || !in.accepts(r) {
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

// view renders the value with the cursor as a glyph. NO_COLOR removes
// every style, reverse video too, so only a glyph stays visible. At the
// end of the text the cursor is an underscore. Inside the text it is a
// bar between two characters, where typing inserts. A bar is rare in a
// path, but an underscore is common there.
func (in *lineInput) view(t *Theme) string {
	if len(in.runes) == 0 {
		hint := in.placeholder
		if hint == "" {
			hint = "path, for example internal/api"
		}

		return t.Accent.Render("_") + t.Faint.Render(" "+oneLine(hint))
	}

	before := t.Body.Render(string(in.runes[:in.pos]))

	if in.pos == len(in.runes) {
		return before + t.Accent.Render("_")
	}

	return before + t.Accent.Render("|") + t.Body.Render(string(in.runes[in.pos:]))
}
