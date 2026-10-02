package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellCRadioGlyphs(t *testing.T) {
	u := newTheme(false)
	assert.Equal(t, "(●) codex", ansi.Strip(u.radio(true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(u.radio(false, "claude")))

	a := newTheme(true)
	assert.Equal(t, "(*) codex", ansi.Strip(a.radio(true, "codex")))
	assert.Equal(t, "( ) claude", ansi.Strip(a.radio(false, "claude")))

	assert.Equal(t, "( ) a b", ansi.Strip(u.radio(false, "a\nb")), "a label stays on one line")
}

func TestShellCModalScrollsItsBodyAndKeepsTheActions(t *testing.T) {
	th := newTheme(false)

	body := make([]string, 30)
	for i := range body {
		body[i] = "line " + itoa(i+1)
	}

	var sc scrollState

	o := modalOpts{
		Title: "Preview", Right: "draft", Body: body,
		Actions: []key.Binding{withHelp(nkeys.Back, "esc", "close")},
	}

	lines := strings.Split(ansi.Strip(th.modal(o, &sc, 120, 20)), "\n")

	// 20 rows lose the margin, the border and the three action rows.
	require.Len(t, lines, 13+3+2)
	assert.Equal(t, 80, width(lines[0]), "the modal is 80 cells wide at 120 columns")
	assert.Contains(t, lines[0], "Preview")
	assert.Contains(t, lines[0], "draft")
	assert.Contains(t, lines[1], "line 1 ")
	assert.Contains(t, lines[len(lines)-2], "esc close", "the actions stay under the body")
	assert.Contains(t, lines[len(lines)-1], "1–13 of 30")
	assert.Equal(t, 13, sc.page)
	assert.Equal(t, 17, sc.last)

	sc.by(100)
	lines = strings.Split(ansi.Strip(th.modal(o, &sc, 120, 20)), "\n")
	assert.Contains(t, lines[13], "line 30", "the offset stops at the last page")
	assert.Contains(t, lines[len(lines)-1], "18–30 of 30")

	// A smaller floor gives a narrower modal on a mid-size terminal.
	o.MinW = 64
	assert.Equal(t, 64, width(strings.Split(ansi.Strip(th.modal(o, nil, 90, 20)), "\n")[0]))
	assert.Equal(t, 76, modalBodyWidth(0, 120), "the body width is the inner width of the box")
}

func TestShellCPhaseRows(t *testing.T) {
	th := newTheme(false)

	rows := th.phaseRows([]phaseRow{
		{label: "Revalidate the plan", state: phaseDone},
		{label: "Send groups", note: "3 of 10 done", state: phaseStopped},
		{label: "Remove stale pending", state: phaseSkipped},
		{label: "Report", note: "store locked", state: phaseFailed},
		{label: "Wait", state: phasePending},
		{label: "Run", state: phaseActive},
	}, 0, 80)

	plain := make([]string, len(rows))
	for i, r := range rows {
		plain[i] = strings.TrimRight(ansi.Strip(r), " ")
	}

	assert.Equal(t, []string{
		"✓ Revalidate the plan",
		"! Send groups           stopped, 3 of 10 done",
		"· Remove stale pending  skipped",
		"! Report                failed, store locked",
		"· Wait",
		"○ Run",
	}, plain)
}

func TestShellCActivityLinesStayBoundedAndSafe(t *testing.T) {
	th := newTheme(false)

	lines := th.activityLines([]string{
		"first",
		"fetch page 2\x1b[31m red",
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 used, then a very long tail of text",
	}, 2, 40)

	require.Len(t, lines, 2, "only the last lines stay")

	joined := ansi.Strip(strings.Join(lines, "\n"))
	assert.NotContains(t, joined, "first")
	assert.NotContains(t, joined, "\x1b", "control sequences stay inert")
	assert.NotContains(t, joined, "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "secrets are redacted")

	for _, l := range lines {
		assert.LessOrEqual(t, width(l), 40)
	}

	assert.Len(t, th.activityLines([]string{"a", "b", "c"}, 0, 40), 3, "keep 0 keeps every line")
}

func TestShellCEffectLines(t *testing.T) {
	th := newTheme(false)

	lines := th.effectLines([]effect{{glyph: "+", tone: ToneSuccess, text: "write .seamark/config.yaml"}},
		[]string{"run a model"}, 60)

	assert.Equal(t, []string{"", "WILL", "+ write .seamark/config.yaml", "", "WON'T", "· run a model"},
		strings.Split(ansi.Strip(strings.Join(lines, "\n")), "\n"))
	assert.Empty(t, th.effectLines(nil, nil, 60))
}

func TestShellCInputOptions(t *testing.T) {
	th := newTheme(false)

	var region propInput
	assert.Equal(t, "_ path, for example internal/api", ansi.Strip(region.view(th)), "zero values keep the region hint")

	days := propInput{
		placeholder: "days, for example 730",
		limit:       4,
		accept:      func(r rune) bool { return r >= '0' && r <= '9' },
	}
	assert.Equal(t, "_ days, for example 730", ansi.Strip(days.view(th)))

	days.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7a3-0x12")})
	assert.Equal(t, "7301", days.value(), "the filter drops other runes and the limit stops the value")

	region.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("a", propInputLimit+5))})
	assert.Len(t, region.value(), propInputLimit, "zero values keep the region limit")
}
