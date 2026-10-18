package ui

import (
	"iter"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// This file holds LineInput, the one-line text input of the console, and
// runeEdit, the editing core that LineInput and TextArea (textarea.go)
// share. So a key edits one line and many lines with the same rules.
//
// The console draws its own widgets and adopts no charmbracelet/bubbles
// widget. Read these five reasons before you propose one again:
//
//   - textinput shows its cursor in reverse video, not as a glyph. The
//     Ascii profile of NO_COLOR and of the tests drops that attribute, so
//     the cursor vanishes. LineInput shows the "_" and "|" glyphs instead.
//     The Validate of textinput reports an invalid value but stores it
//     too, so the digit filter and the length bound need their own check.
//   - textarea has the same reverse-video cursor. Its sanitizer changes a
//     tab into four spaces in the value. It changes each CR and each LF
//     into a line break, so a CRLF paste gets two breaks. It keeps the
//     direction overrides that MultiLine drops, and it drops the other
//     controls without a report. Its CharLimit cuts a paste to the free
//     space, and its line bound of 10000 cuts more lines without a word.
//     A form text must keep its tabs and must never lose a part silently,
//     so TextArea stays instead.
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

// runeEdit is the text and the cursor of an input. The cursor is a rune
// index between 0 and len(runes); typing inserts at it. LineInput and
// TextArea embed it, so the edits of both inputs follow one rule.
//
// The cursor stands only between two grapheme clusters. A grapheme
// cluster is the runes that a terminal draws as one character. Examples
// are e with a combining accent, and an emoji with its variation
// selector. The box and the terminal measure text per cluster, so a
// cursor inside a cluster would split one character into two.
type runeEdit struct {
	runes []rune
	pos   int
}

// grapheme is one grapheme cluster of the text: the runes [start, end).
// cells is its display width by the grapheme rule of x/ansi, which the
// box also uses.
type grapheme struct {
	start, end int
	cells      int
}

// graphemes yields the grapheme clusters of rs[from:to]. from must be
// the start of a cluster: 0, the index after a line break, or the start
// of a row. A line break always ends a cluster, and a row starts between
// two clusters. The runes convert to a string once, so a long line takes
// linear time.
func graphemes(rs []rune, from, to int) iter.Seq[grapheme] {
	return func(yield func(grapheme) bool) {
		s := string(rs[from:to])

		for i := from; s != ""; {
			c, cells := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
			n := utf8.RuneCountInString(c)

			if !yield(grapheme{start: i, end: i + n, cells: cells}) {
				return
			}

			s, i = s[len(c):], i+n
		}
	}
}

// lineStart returns the index of the first rune of the line that holds
// index p: the index after the line break before p, or 0.
func (e *runeEdit) lineStart(p int) int {
	for i := p - 1; i >= 0; i-- {
		if e.runes[i] == '\n' {
			return i + 1
		}
	}

	return 0
}

// lineEnd returns the index of the line break at or after index p, or
// the end of the text.
func (e *runeEdit) lineEnd(p int) int {
	if i := slices.Index(e.runes[p:], '\n'); i >= 0 {
		return p + i
	}

	return len(e.runes)
}

// clusterAt returns the cluster that holds the rune at index p, or the
// cluster that ends at p when before is true. A line break is a cluster
// of its own. ok is false at the start or the end of the text.
func (e *runeEdit) clusterAt(p int, before bool) (g grapheme, ok bool) {
	if before {
		p--
	}

	if p < 0 || p >= len(e.runes) {
		return grapheme{}, false
	}

	if e.runes[p] == '\n' {
		return grapheme{start: p, end: p + 1}, true
	}

	for g := range graphemes(e.runes, e.lineStart(p), e.lineEnd(p)) {
		if g.end > p {
			return g, true
		}
	}

	return grapheme{}, false
}

// set replaces the text and puts the cursor at its end.
func (e *runeEdit) set(rs []rune) {
	e.runes = rs
	e.pos = len(rs)
}

// insert puts rs at the cursor and moves the cursor after them. Typed
// runes can join a cluster after the cursor, for example a letter before
// a combining accent. The cursor then goes after the joined cluster.
func (e *runeEdit) insert(rs []rune) {
	e.runes = slices.Insert(e.runes, e.pos, rs...)
	e.pos += len(rs)
	e.snap()
}

// backspace deletes the cluster before the cursor, so no orphan accent or
// variation selector stays.
func (e *runeEdit) backspace() {
	if g, ok := e.clusterAt(e.pos, true); ok {
		e.runes = slices.Delete(e.runes, g.start, g.end)
		e.pos = g.start
		e.snap()
	}
}

// deleteForward deletes the cluster at the cursor.
func (e *runeEdit) deleteForward() {
	if g, ok := e.clusterAt(e.pos, false); ok {
		e.runes = slices.Delete(e.runes, g.start, g.end)
		e.snap()
	}
}

// left moves the cursor before the cluster before it.
func (e *runeEdit) left() {
	if g, ok := e.clusterAt(e.pos, true); ok {
		e.pos = g.start
	}
}

// right moves the cursor after the cluster at it.
func (e *runeEdit) right() {
	if g, ok := e.clusterAt(e.pos, false); ok {
		e.pos = g.end
	}
}

// snap moves a cursor that stands inside a cluster to the end of that
// cluster. An edit can join the runes on both sides of the cursor into
// one cluster, and the cursor must stay between two clusters.
func (e *runeEdit) snap() {
	if g, ok := e.clusterAt(e.pos, false); ok && g.start < e.pos {
		e.pos = g.end
	}
}

// deleteFrom deletes the runes from index from up to the cursor. ctrl+u
// uses it: LineInput deletes from the start of the value, and TextArea
// from the start of the line.
func (e *runeEdit) deleteFrom(from int) {
	from = max(0, min(from, e.pos))
	e.runes = slices.Delete(e.runes, from, e.pos)
	e.pos = from
}

// moveTo puts the cursor at index p, inside the text. p is a cluster
// boundary: 0, the end, a line start or end, or the start of a cluster.
func (e *runeEdit) moveTo(p int) {
	e.pos = max(0, min(p, len(e.runes)))
}

// cursorView renders the runes with the cursor as a glyph. NO_COLOR
// removes every style, reverse video too, so only a glyph stays visible.
// At the end of the text the cursor is an underscore. Inside the text it
// is a bar between two characters, where typing inserts. A bar is rare
// in a path, but an underscore is common there. at is the cursor index
// inside rs; -1 renders no cursor.
func cursorView(t *Theme, rs []rune, at int) string {
	switch {
	case at < 0:
		return t.Body.Render(string(rs))
	case at >= len(rs):
		return t.Body.Render(string(rs)) + t.Accent.Render("_")
	}

	return t.Body.Render(string(rs[:at])) + t.Accent.Render("|") + t.Body.Render(string(rs[at:]))
}

// LineInput is a minimal single-line text input. The filter bars, the
// editors and the forms of the console use it. A
// filter or a count needs only typing, deletion and cursor movement. The
// optional fields let each form set its own hint, bound and runes. Their
// zero values keep the path filter behaviour, because the path filter
// was the first user.
type LineInput struct {
	runeEdit

	// Placeholder is the faint hint of an empty input. "" keeps the
	// path hint.
	Placeholder string
	// Limit bounds the value in runes. 0 keeps lineInputLimit.
	Limit int
	// Accept filters typed runes, for example digits only for a day
	// count. nil accepts every rune that the sanitizer keeps.
	Accept func(rune) bool
	// RefuseMultiline refuses a whole input that holds a line break, for
	// example a paste of two lines, and Notice says why. Without it, the
	// line breaks go away and the lines join, which a filter accepts.
	RefuseMultiline bool
	// RefuseOverLimit refuses a whole input that would pass Limit, and
	// Notice says why. Without it, the runes past the limit go away
	// silently. With it, a value longer than the limit from Set is read
	// only (ReadOnly), because an edit could only shorten it by a cut.
	RefuseOverLimit bool

	// notice explains the last key that changed less than the user typed.
	// Only a field with an opt-in sets it; a filter drops runes silently.
	notice string
}

// MaxRunes returns the length bound of the value in runes: Limit, or
// lineInputLimit when Limit is 0. A form names this bound when it locks
// a stored value, so the form never copies the number.
func (in *LineInput) MaxRunes() int {
	if in.Limit > 0 {
		return in.Limit
	}

	return lineInputLimit
}

// accepts reports whether the sanitizer and the accept filter let r
// into the value.
func (in *LineInput) accepts(r rune) bool {
	return oneLineKeeps(r) && (in.Accept == nil || in.Accept(r))
}

// oneLineKeeps reports whether the input keeps r: the sanitizer OneLine
// keeps it, and it is not a hidden rune (hiddenRune). OneLine rejects
// control characters and direction overrides, so the input cannot print
// an escape sequence or reorder text.
func oneLineKeeps(r rune) bool {
	s := string(r)

	return !hiddenRune(r) && OneLine(s) == s
}

// hiddenRune reports U+2028 LINE SEPARATOR, U+2029 PARAGRAPH SEPARATOR,
// and the direction marks U+200E, U+200F, and U+061C. The sanitizer
// keeps them, but a terminal shows nothing for them. The shared text
// check of a pin refuses them. So LineInput and TextArea drop them, and
// the drop notice counts them. The rule stays here and not in
// render.SanitizeBlock, because other callers of the sanitizer keep
// these runes.
func hiddenRune(r rune) bool {
	switch r {
	case '\u2028', '\u2029', '\u200e', '\u200f', '\u061c':
		return true
	}

	return false
}

// Set replaces the value and puts the cursor at its end. It keeps s as
// it is, so a caller checks s with Holds first when s is untrusted.
func (in *LineInput) Set(s string) {
	in.set([]rune(s))
	in.notice = ""
}

// Value returns the typed text.
func (in *LineInput) Value() string {
	return string(in.runes)
}

// Holds reports whether the input can show and edit s exactly: the
// sanitizer and the accept filter keep every rune. A form opens a value
// that fails it as read-only, because an edit would change runes that
// the user never touched.
func (in *LineInput) Holds(s string) bool {
	for _, r := range s {
		if !in.accepts(r) {
			return false
		}
	}

	return true
}

// ReadOnly reports a value that is longer than the limit while
// RefuseOverLimit is set. Only Set can make such a value, for example
// with a text from a file. Key then moves the cursor but changes no
// rune.
func (in *LineInput) ReadOnly() bool {
	return in.RefuseOverLimit && len(in.runes) > in.MaxRunes()
}

// Notice returns why the last key changed less than the user typed, or
// "". A form shows it under the input, because a refused paste must be
// visible. A field without an opt-in always returns "".
func (in *LineInput) Notice() string {
	return in.notice
}

// Key applies one key press. The sanitizer, the accept filter and the
// length bound drop runes, unless an opt-in field refuses the whole
// input instead. A read-only value takes only the movement keys.
func (in *LineInput) Key(msg tea.KeyMsg) {
	in.notice = ""

	if in.ReadOnly() && in.edits(msg) {
		in.notice = "This text is longer than " + strconv.Itoa(in.MaxRunes()) +
			" characters, so it cannot change here."

		return
	}

	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace:
		in.typeRunes(msg.Runes)
	case tea.KeyBackspace:
		in.backspace()
	case tea.KeyDelete:
		in.deleteForward()
	case tea.KeyCtrlU:
		in.deleteFrom(0)
	case tea.KeyLeft:
		in.left()
	case tea.KeyRight:
		in.right()
	case tea.KeyHome, tea.KeyCtrlA:
		in.moveTo(0)
	case tea.KeyEnd, tea.KeyCtrlE:
		in.moveTo(len(in.runes))
	}
}

// edits reports a key that changes the value, as opposed to a key that
// only moves the cursor.
func (in *LineInput) edits(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyRunes, tea.KeySpace, tea.KeyBackspace, tea.KeyDelete, tea.KeyCtrlU:
		return true
	}

	return false
}

// typeRunes inserts the runes of one key press. The opt-in fields
// refuse the whole input before a rune goes in, so a refused paste never
// leaves a part behind. A field with an opt-in also reports the runes
// that the filters left out, because a paste must never change silently.
// A field without one, for example a filter, keeps the silent drop.
func (in *LineInput) typeRunes(rs []rune) {
	if in.RefuseMultiline && slices.ContainsFunc(rs, func(r rune) bool { return r == '\n' || r == '\r' }) {
		in.notice = "This field takes one line, and the input has line breaks, so nothing was added."

		return
	}

	kept := slices.DeleteFunc(slices.Clone(rs), func(r rune) bool { return !in.accepts(r) })
	room := in.MaxRunes() - len(in.runes)

	if len(kept) > room {
		if in.RefuseOverLimit {
			in.notice = "This field holds at most " + strconv.Itoa(in.MaxRunes()) +
				" characters, so nothing was added."

			return
		}

		kept = kept[:max(0, room)]
	}

	in.insert(kept)

	if in.RefuseMultiline || in.RefuseOverLimit {
		in.notice = in.leftOutNotice(rs)
	}
}

// leftOutNotice says which runes of rs the filters left out, or "" for
// none. The sanitizer and the accept filter get separate words, because
// a digit field refuses a letter for another reason than a control.
func (in *LineInput) leftOutNotice(rs []rune) string {
	unsafe, refused := 0, 0

	for _, r := range rs {
		switch {
		case !oneLineKeeps(r):
			unsafe++
		case !in.accepts(r):
			refused++
		}
	}

	return strings.TrimSpace(dropNotice(unsafe) + " " +
		leftOut(refused, "character that this field does not accept", "characters that this field does not accept"))
}

// dropNotice says how many control or direction characters an input
// left out, or "" for none. LineInput and TextArea share it, so a
// dropped rune gets the same words in both inputs.
func dropNotice(n int) string {
	return leftOut(n, "control or direction character", "control or direction characters")
}

// leftOut says how many runes of one kind an input left out, or "" for
// none. one and many name the kind in the singular and in the plural.
func leftOut(n int, one, many string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "Left out 1 " + one + "."
	}

	return "Left out " + strconv.Itoa(n) + " " + many + "."
}

// View renders the value with the cursor as a glyph (cursorView).
func (in *LineInput) View(t *Theme) string {
	if len(in.runes) == 0 {
		hint := in.Placeholder
		if hint == "" {
			hint = "path, for example internal/api"
		}

		return t.Accent.Render("_") + t.Faint.Render(" "+OneLine(hint))
	}

	return cursorView(t, in.runes, in.pos)
}
