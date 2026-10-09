package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Tone is the semantic state of a value. Every tone has a glyph and a
// text label as well as a color, because color alone must never carry
// meaning (NO_COLOR, monochrome terminals, color vision).
type Tone int

const (
	// ToneNeutral is plain information.
	ToneNeutral Tone = iota
	// ToneSuccess is a current or healthy state.
	ToneSuccess
	// ToneAttention is an actionable problem, a pending decision, or a
	// stale state.
	ToneAttention
	// ToneFailure is a broken state or a failed operation.
	ToneFailure
	// ToneUnknown is missing evidence. It is never shown as success.
	ToneUnknown
	// ToneAccent marks the primary action or the active destination.
	ToneAccent
	// ToneUnsupported marks a capability that setup does not provide.
	ToneUnsupported
)

// Glyphs is one glyph set. The Unicode set uses only the single-width
// symbols of the 1a design, so width arithmetic stays exact. The ASCII
// set exists for fonts without these symbols. In each set, Sep differs
// from Vertical, because without color the glyph is the only signal
// between them.
type Glyphs struct {
	BrandLeft, BrandMid, BrandRight string

	Cursor, Check, Uncheck, Locked, Kept, Bullet, Arrow, Sep, Ellipsis string
	Dash, OK, Warn, Fail, Info, Unknown, Unsupported                   string
	Dot, Hollow                                                        string
	RadioOn, RadioOff                                                  string

	RuleLight, RuleHeavy, Vertical string
	CornerTL, CornerTR, CornerBL   string
	CornerBR                       string

	BarFull, BarEmpty string
	Spark             []string

	Add, Del string
	Spinner  []string
}

var unicodeGlyphs = Glyphs{
	BrandLeft: "▶", BrandMid: "┊", BrandRight: "◀",
	Cursor: "›", Check: "[✓]", Uncheck: "[ ]", Locked: "[-]", Kept: "[=]",
	Bullet: "·", Arrow: "›", Sep: "·", Ellipsis: "…", Dash: "—",
	OK: "✓", Warn: "!", Fail: "!", Info: "·", Unknown: "?", Unsupported: "◌",
	Dot: "●", Hollow: "○",
	RadioOn: "(●)", RadioOff: "( )",
	RuleLight: "─", RuleHeavy: "━", Vertical: "│",
	CornerTL: "╭", CornerTR: "╮", CornerBL: "╰", CornerBR: "╯",
	BarFull: "█", BarEmpty: "░",
	Spark: []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"},
	Add:   "+", Del: "−",
	Spinner: []string{"○", "◌", "●", "◌"},
}

var asciiGlyphs = Glyphs{
	BrandLeft: ">", BrandMid: "|", BrandRight: "<",
	Cursor: ">", Check: "[x]", Uncheck: "[ ]", Locked: "[-]", Kept: "[=]",
	Bullet: "-", Arrow: ">", Sep: "/", Ellipsis: "...", Dash: "-",
	OK: "+", Warn: "!", Fail: "!", Info: "-", Unknown: "?", Unsupported: "~",
	Dot: "*", Hollow: "o",
	RadioOn: "(*)", RadioOff: "( )",
	RuleLight: "-", RuleHeavy: "=", Vertical: "|",
	CornerTL: "+", CornerTR: "+", CornerBL: "+", CornerBR: "+",
	BarFull: "#", BarEmpty: ".",
	Spark: []string{".", ".", ":", ":", "=", "=", "#", "#"},
	Add:   "+", Del: "-",
	Spinner: []string{"-", "\\", "|", "/"},
}

// The 1a design tokens. Every role adapts to light and dark terminal
// backgrounds, because the console never sets a background of its own.
var (
	colorFg     = lipgloss.AdaptiveColor{Light: "#1B2023", Dark: "#E4E2DC"}
	colorDim    = lipgloss.AdaptiveColor{Light: "#565C5A", Dark: "#9EA19C"}
	colorFaint  = lipgloss.AdaptiveColor{Light: "#8E928E", Dark: "#686E6B"}
	colorLine   = lipgloss.AdaptiveColor{Light: "#D0CDC5", Dark: "#353B3D"}
	colorAcc    = lipgloss.AdaptiveColor{Light: "#8F6200", Dark: "#E8B23A"}
	colorOk     = lipgloss.AdaptiveColor{Light: "#2B7A4B", Dark: "#84C59B"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#B0521A", Dark: "#EE9A5C"}
	colorErr    = lipgloss.AdaptiveColor{Light: "#B42F38", Dark: "#EC6B70"}
	colorInfo   = lipgloss.AdaptiveColor{Light: "#2A659A", Dark: "#80B3DA"}
	colorSel    = lipgloss.AdaptiveColor{Light: "#E5E1D5", Dark: "#262C2F"}
	colorSurf   = lipgloss.AdaptiveColor{Light: "#EBE9E3", Dark: "#1B2023"}
	colorWarnBg = lipgloss.AdaptiveColor{Light: "#F3DFCD", Dark: "#3A2A1C"}
	colorAddBg  = lipgloss.AdaptiveColor{Light: "#DBEDDE", Dark: "#1A2E22"}
	colorDelBg  = lipgloss.AdaptiveColor{Light: "#F5DADA", Dark: "#361F22"}
)

// Theme holds the glyphs and styles of one session. Screens take every
// style from here so the console keeps one visual vocabulary.
type Theme struct {
	ASCII  bool
	Glyphs Glyphs

	Brand     lipgloss.Style // the word seamark and the outer marks
	BrandMark lipgloss.Style // the channel mark between the wedges
	Workspace lipgloss.Style
	TabActive lipgloss.Style
	TabIdle   lipgloss.Style
	TabNum    lipgloss.Style
	Rule      lipgloss.Style // unfocused borders, rules, dimmed backdrop
	RuleOn    lipgloss.Style // focused borders, the active tab rule

	Title   lipgloss.Style // item titles
	Heading lipgloss.Style // upper-case section labels
	Body    lipgloss.Style
	Muted   lipgloss.Style // secondary text and labels
	Faint   lipgloss.Style // tertiary text, column headers, hints
	Accent  lipgloss.Style
	Key     lipgloss.Style // key names in hints
	Code    lipgloss.Style // selectable commands and paths
	Marked  lipgloss.Style // multi-selected rows

	Success   lipgloss.Style
	Attention lipgloss.Style
	Failure   lipgloss.Style
	Unknown   lipgloss.Style
	Info      lipgloss.Style

	Button lipgloss.Style // the label of a primary next action
	Ghost  lipgloss.Style // the label of a secondary next action
}

// newTheme builds the session theme. lipgloss downgrades the colors to
// the terminal profile and drops them under NO_COLOR, so the glyphs and
// labels carry every state on their own.
func newTheme(ascii bool) *Theme {
	g := unicodeGlyphs
	if ascii {
		g = asciiGlyphs
	}

	fg := lipgloss.NewStyle().Foreground(colorFg)

	return &Theme{
		ASCII:  ascii,
		Glyphs: g,

		Brand:     fg.Bold(true),
		BrandMark: lipgloss.NewStyle().Foreground(colorAcc),
		Workspace: lipgloss.NewStyle().Foreground(colorDim),
		TabActive: lipgloss.NewStyle().Foreground(colorAcc).Bold(true),
		TabIdle:   lipgloss.NewStyle().Foreground(colorDim),
		TabNum:    lipgloss.NewStyle().Foreground(colorFaint),
		Rule:      lipgloss.NewStyle().Foreground(colorLine),
		RuleOn:    lipgloss.NewStyle().Foreground(colorAcc),

		Title:   fg.Bold(true),
		Heading: lipgloss.NewStyle().Foreground(colorDim).Bold(true),
		Body:    fg,
		Muted:   lipgloss.NewStyle().Foreground(colorDim),
		Faint:   lipgloss.NewStyle().Foreground(colorFaint),
		Accent:  lipgloss.NewStyle().Foreground(colorAcc),
		Key:     lipgloss.NewStyle().Foreground(colorAcc).Bold(true),
		Code:    lipgloss.NewStyle().Foreground(colorAcc),
		Marked:  lipgloss.NewStyle().Foreground(colorAcc),

		Success:   lipgloss.NewStyle().Foreground(colorOk),
		Attention: lipgloss.NewStyle().Foreground(colorWarn),
		Failure:   lipgloss.NewStyle().Foreground(colorErr),
		Unknown:   lipgloss.NewStyle().Foreground(colorInfo),
		Info:      lipgloss.NewStyle().Foreground(colorInfo),

		Button: lipgloss.NewStyle().Foreground(colorAcc).Bold(true),
		Ghost:  lipgloss.NewStyle().Foreground(colorDim),
	}
}

// ToneStyle returns the style of a semantic tone.
func (t *Theme) ToneStyle(tone Tone) lipgloss.Style {
	switch tone {
	case ToneSuccess:
		return t.Success
	case ToneAttention:
		return t.Attention
	case ToneFailure:
		return t.Failure
	case ToneUnknown:
		return t.Unknown
	case ToneAccent:
		return t.Accent
	case ToneUnsupported:
		return t.Muted
	default:
		return t.Body
	}
}

// ToneGlyph returns the glyph of a semantic tone.
func (t *Theme) ToneGlyph(tone Tone) string {
	switch tone {
	case ToneSuccess:
		return t.Glyphs.OK
	case ToneAttention:
		return t.Glyphs.Warn
	case ToneFailure:
		return t.Glyphs.Fail
	case ToneUnknown:
		return t.Glyphs.Unknown
	case ToneUnsupported:
		return t.Glyphs.Unsupported
	default:
		return t.Glyphs.Info
	}
}

// Badge renders a glyph and a text label in the tone color, for example
// "✓ current". The label keeps the meaning when color is off.
func (t *Theme) Badge(tone Tone, label string) string {
	return t.ToneStyle(tone).Render(t.ToneGlyph(tone) + " " + label)
}

// Row backgrounds of the 1a design. The selection spans the full inner
// width of a pane; diff rows and the stale banner use tinted rows.
const (
	bgSel = iota
	bgSurf
	bgWarn
	bgAdd
	bgDel
)

var rowBackgrounds = map[int]lipgloss.AdaptiveColor{
	bgSel: colorSel, bgSurf: colorSurf, bgWarn: colorWarnBg, bgAdd: colorAddBg, bgDel: colorDelBg,
}

// backgroundSeq returns the SGR sequence that sets a row background in
// the current color profile. It is empty without color support, so a
// highlighted row is plain text under NO_COLOR and in tests.
func backgroundSeq(bg int) string {
	c, ok := rowBackgrounds[bg]
	if !ok {
		return ""
	}

	hex := c.Dark
	if !lipgloss.HasDarkBackground() {
		hex = c.Light
	}

	seq := lipgloss.ColorProfile().Color(hex).Sequence(true)
	if seq == "" {
		return ""
	}

	return termenv.CSI + seq + "m"
}

// highlight paints a background under a rendered row and pads it to w
// cells. The row keeps its own foreground styles: every reset inside it
// sets the background again, so the fill never breaks at a styled word.
func (t *Theme) highlight(row string, w, bg int) string {
	row = padRight(truncate(row, w, t.Glyphs.Ellipsis), w)

	seq := backgroundSeq(bg)
	if seq == "" {
		return row
	}

	return seq + strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+seq) + "\x1b[0m"
}

// brand renders the mark "▶┊◀ seamark".
func (t *Theme) brand() string {
	g := t.Glyphs

	return t.Brand.Render(g.BrandLeft) + t.BrandMark.Render(g.BrandMid) +
		t.BrandMark.Bold(true).Render(g.BrandRight) + " " + t.Brand.Render("seamark")
}

// asciiFrameSymbols maps each Unicode symbol that screens write as a
// literal to one ASCII character. Each pair has a width of one cell on
// both sides, so a frame keeps its layout after the swap. The list
// holds only symbols of the console itself. Text in other scripts, for
// example a file name, stays as it is.
var asciiFrameSymbols = strings.NewReplacer(asciiFramePairs()...)

// asciiFramePairs returns the old and new strings for asciiFrameSymbols.
// The spinner and spark frames come from the Unicode glyph set, so the
// lists cannot drift apart.
func asciiFramePairs() []string {
	pairs := []string{
		"—", "-", "–", "-", "…", "~", "×", "x", "−", "-",
		"→", ">", "←", "<", "↑", "^", "↓", "v",
		"›", ">", "‹", "<", "•", "*", "·", ":",
		"✓", "+", "✗", "x", "▍", "#", "▌", "#",
		"●", "*", "○", "o", "◌", "~", "▶", ">", "◀", "<", "┊", "|",
		"│", "|", "─", "-", "━", "=", "░", ".",
		"╭", "+", "╮", "+", "╰", "+", "╯", "+",
	}

	for _, frame := range unicodeGlyphs.Spinner {
		pairs = append(pairs, frame, "-")
	}

	for _, s := range unicodeGlyphs.Spark {
		pairs = append(pairs, s, "#")
	}

	return dedupePairs(pairs)
}

// dedupePairs drops a later pair whose old string repeats an earlier
// one. strings.NewReplacer uses the first match, so a duplicate would
// only hide a mistake.
func dedupePairs(pairs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(pairs))

	for i := 0; i+1 < len(pairs); i += 2 {
		if seen[pairs[i]] {
			continue
		}

		seen[pairs[i]] = true
		out = append(out, pairs[i], pairs[i+1])
	}

	return out
}

// asciiFrame replaces the known Unicode symbols in a rendered frame
// when the session uses the ASCII glyph set. It is a safety net for
// literals that do not come from Glyphs, because a font without these
// symbols shows a replacement box. Escape sequences are ASCII, so the
// swap never changes a style.
func (t *Theme) asciiFrame(s string) string {
	if !t.ASCII {
		return s
	}

	return asciiFrameSymbols.Replace(s)
}
