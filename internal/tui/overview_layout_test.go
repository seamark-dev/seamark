package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovLayoutSizes are the sizes the 1a design names: the primary size, the
// common narrow terminal, and the documented minimum.
var ovLayoutSizes = [][2]int{{120, 40}, {80, 24}, {minWidth, minHeight}}

// ovStale is a populated workspace whose index is stale and whose doctor
// report is clean, so the stale item is the most urgent one.
func ovStale() *fakeServices {
	f := ovPopulated()
	f.doctor = &doctor.Report{Checks: []doctor.Check{{Name: "binary", State: doctor.StateOK, Detail: "ok"}}}
	f.index.Status.Freshness = status.FreshStale

	return f
}

// ovLines returns the frame lines with the trailing spaces removed.
func ovLines(frame string) []string {
	lines := strings.Split(frame, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}

	return lines
}

// ovAt returns the text of a frame line from cell col on. Box glyphs
// take several bytes, so the cut counts runes.
func ovAt(line string, col int) string {
	r := []rune(line)
	if col >= len(r) {
		return ""
	}

	return string(r[col:])
}

func TestOverviewFramesFitAtEverySize(t *testing.T) {
	scenes := map[string]func() *fakeServices{
		"populated": ovPopulated,
		"stale":     ovStale,
		"absent": func() *fakeServices {
			f := newFake()
			f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent, Path: f.dbPath}}
			f.integrations = []integration.Inspection{ovClaude()}
			f.ledgerErr = ErrNoLedger

			return f
		},
		"policy error": func() *fakeServices {
			f := ovPopulated()
			f.index.Policy = PolicySummary{Err: errors.New("gate: .seamark/policy.yaml: mode must be warn or enforce")}

			return f
		},
	}

	for name, scene := range scenes {
		for _, size := range ovLayoutSizes {
			w, hgt := size[0], size[1]

			t.Run(name+" "+itoa(w)+"x"+itoa(hgt), func(t *testing.T) {
				f := scene()
				h := newHarness(t, f, w, hgt)

				assertFrame(t, h.view(), w, hgt)

				h.press("enter")
				assertFrame(t, h.view(), w, hgt)

				h.press("esc", "b")
				assertFrame(t, h.view(), w, hgt)
				assert.Contains(t, h.view(), "Build the index?", "the confirmation fits at %dx%d", w, hgt)

				h.press("enter")
				assertFrame(t, h.view(), w, hgt)
				assert.Contains(t, h.view(), "enter close", "the result keeps its keys at %dx%d", w, hgt)
			})
		}
	}
}

func TestOverviewWideLayoutMatchesTheDesignGeometry(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)

	lines := ovLines(h.view())
	require.Len(t, lines, 40)

	// Row 4 of the design is the notice row; the cards take rows 5-9.
	assert.Empty(t, lines[3], "the notice row stays blank without a message")

	for i, card := range []string{"╭─ INDEX", "╭─ NAVIGATE", "╭─ LEARN", "╭─ GUARD"} {
		assert.True(t, strings.HasPrefix(ovAt(lines[4], 30*i), card), "card %d starts at cell %d: %q", i, 30*i, lines[4])
	}

	assert.True(t, strings.HasSuffix(lines[4], "mode warn ─╮"), "the GUARD card ends at cell 120: %q", lines[4])
	assert.True(t, strings.HasPrefix(lines[8], "╰"))
	assert.Empty(t, lines[9])

	// The Attention box is 64 cells wide, the Detail box 55, both 28 rows.
	assert.True(t, strings.HasPrefix(lines[10], "╭─ Attention"))
	assert.True(t, strings.HasPrefix(ovAt(lines[10], 65), "╭─ Detail"), "the Detail box starts at cell 65: %q", lines[10])
	assert.Equal(t, "╯", string([]rune(lines[37])[63]), "the Attention box ends at cell 64")
	assert.True(t, strings.HasSuffix(lines[37], "╯"))
	assert.Empty(t, lines[38])

	assert.Contains(t, lines[39], "↑↓ move   enter open   tab pane   b build index")
	assert.NotContains(t, lines[39], "s set up", "setup is offered only while nothing is set up")
}

func TestOverviewNarrowLayoutCollapsesTheCards(t *testing.T) {
	h := newHarness(t, ovPopulated(), 80, 24)
	o := ovScreen(h)

	lines := ovLines(h.view())
	require.Len(t, lines, 24)

	assert.Equal(t, " INDEX ✓ 1,200/1,240   NAV 12,345 sym   LEARN 3 pending   GUARD warn", lines[4])
	assert.Empty(t, lines[5])
	assert.True(t, strings.HasPrefix(lines[6], "╭─ Attention"))
	assert.Contains(t, lines[21], "enter for detail")
	assert.True(t, strings.HasPrefix(lines[21], "╰"))
	assert.Contains(t, lines[23], "↑↓ move   enter detail   b build")

	found := false

	for _, it := range o.attention() {
		found = found || it.key == ovSummaryKey
	}

	assert.True(t, found, "the narrow layout keeps the summary item, so every card fact stays reachable")

	h.resize(120, 40)

	for _, it := range o.attention() {
		assert.NotEqual(t, ovSummaryKey, it.key, "the cards show every fact in the wide layout")
	}
}

func TestOverviewStaleIndexShowsTheBanner(t *testing.T) {
	f := ovStale()
	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	lines := ovLines(h.view())
	assert.True(t, strings.HasPrefix(lines[3], " ! Index is stale. The workspace changed since the last build 2h ago."),
		"the notice row holds the banner: %q", lines[3])
	assert.True(t, strings.HasSuffix(lines[3], "b rebuild now"))
	assert.NotContains(t, h.view(), "commits", "the index records no commit count, so none is shown")
	assert.Contains(t, lines[4], "! stale")

	_ = h.view()
	assert.Equal(t, "index:freshness", o.selKey, "the stale item is first under ACTION and selected")

	frame := h.view()
	assert.Contains(t, frame, "› ! Index is stale")
	assert.Contains(t, frame, "b build")
	assert.Contains(t, frame, "! action  stale · index · built 2h ago")
	assert.Contains(t, frame, "b build the index · same as seamark index")

	banner, ok := o.banner(minWidth)
	require.True(t, ok)
	assert.Contains(t, banner, "b rebuild now", "the key survives a narrow banner")
	assert.LessOrEqual(t, width(banner), minWidth)

	h.press("b")
	assert.Contains(t, h.view(), "Build the index?", "b opens the rebuild confirmation")

	h.press("esc")

	f.index = ovReadyIndex(f.root, f.dbPath)
	h.press("r")

	_, ok = o.banner(120)
	assert.False(t, ok, "a current index has no banner")
	assert.Empty(t, f.called())
}

func TestOverviewTabCountCountsTheAttentionItems(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)
	o := ovScreen(h)

	n := 0

	for _, it := range o.attention() {
		if it.group != ovSummary {
			n++
		}
	}

	require.Positive(t, n)
	assert.Equal(t, itoa(n), o.tabCount())

	lines := ovLines(h.view())
	assert.Contains(t, lines[1], "1 Overview "+itoa(n))
	assert.Contains(t, lines[10], "─ "+itoa(n)+" ─╮", "the Attention box repeats the count")

	// The narrow layout adds the summary item, but not to the count.
	h.resize(80, 24)
	assert.Equal(t, itoa(n), o.tabCount())
}

func TestOverviewOpenBuildIntentShowsTheConfirmation(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)

	h.press("2")
	h.send(navigateMsg{dest: destOverview, intent: ovIntentBuild})

	assert.Equal(t, destOverview, h.m.active)
	assert.True(t, ovScreen(h).confirm)
	assert.Contains(t, h.view(), "Build the index?")

	h.press("esc")
	assert.NotContains(t, h.view(), "Build the index?")
	assert.Empty(t, f.called(), "the intent opens the confirmation; it never builds")

	h.send(navigateMsg{dest: destOverview, intent: ""})
	assert.False(t, ovScreen(h).confirm, "other intents open the Overview normally")
}

func TestOverviewClientAndDoctorLines(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)
	o := ovScreen(h)

	rows := ovRowTexts(o, 60)

	assert.GreaterOrEqual(t, ovIndexOf(rows, "  ! Claude Code   gate warn · lessons · MCP · skills partial"), 0)
	assert.GreaterOrEqual(t, ovIndexOf(rows, "  ! Codex         gate warn · lessons · grants conflict"), 0)
	assert.GreaterOrEqual(t, ovIndexOf(rows, "  ✓ Shared Agent  skills"), 0)

	doctorAt := ovIndexOf(rows, "DOCTOR  5 checks")
	require.GreaterOrEqual(t, doctorAt, 0)
	assert.Equal(t, "  ✓ 1 ok  · 1 info  ! 2 warn  ! 1 failing · 4 for all checks", rows[doctorAt+1],
		"a narrow box tightens the gaps before it cuts a word")

	wide := ovRowTexts(o, 76)
	assert.Equal(t, "  ✓ 1 ok   · 1 info   ! 2 warn   ! 1 failing · 4 for all checks", wide[doctorAt+1])

	ruleAt := ovIndexOf(rows, strings.Repeat("─", 60))
	assert.Equal(t, ovIndexOf(rows, "CLIENTS")-1, ruleAt, "a rule separates the groups from the clients")

	// The last item scrolls the client and doctor lines into view.
	h.press("end")
	assert.Contains(t, h.view(), "4 for all checks")
}

func TestOverviewDetailShowsTheDesignSections(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)
	o := ovScreen(h)

	resets := ovFind(t, o, "cap:codex:resets")
	flat := ovFlat(ovLines(ovDetail(o, resets, 51)))

	for _, want := range []string{
		"Codex · context reset hook",
		"◌ unsupported not installed by setup",
		"Seamark setup does not install the context reset hook for Codex.",
		"EFFECT No reset hook is installed.",
		"ABOUT THE HOOK The context reset hook tells Seamark that the agent compacted its context.",
		"NEXT No action needed.",
	} {
		assert.Contains(t, flat, want)
	}

	skills := ovFind(t, o, "cap:claude:skills")
	flat = ovFlat(ovLines(ovDetail(o, skills, 51)))
	assert.Contains(t, flat, "! action partial · Claude Code · skills")
	assert.Contains(t, flat, "STATE .claude/skills: 1 stale.")
	assert.Contains(t, flat, "enter refresh Claude Code skills")
	assert.Contains(t, flat, "Or by hand: re-run `seamark init --skills` to refresh the managed skills")
}

func TestOverviewGuardCardShowsThePolicyError(t *testing.T) {
	f := ovPopulated()
	f.index.Policy = PolicySummary{Err: errors.New("gate: bad mode")}

	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assert.Contains(t, frame, "! policy error")
	assert.Contains(t, frame, "rule counts unknown")
	assert.Contains(t, frame, "gate: bad mode")
	assert.NotContains(t, frame, "0 deny", "a broken policy never counts zero rules")
	assert.NotContains(t, frame, "policy compiles")

	h.resize(80, 24)
	assert.Contains(t, h.view(), "GUARD ! policy error")
}

func TestOverviewCardsSayWhatIsMissing(t *testing.T) {
	f := ovPopulated()
	f.index = IndexView{Probe: f.index.Probe, StatusErr: errors.New("open: permission denied"),
		Policy: f.index.Policy}

	h := newHarness(t, f, 120, 40)

	frame := h.view()
	assert.Contains(t, frame, "? unknown")
	assert.Contains(t, frame, "health unknown: open:")
	assert.NotContains(t, frame, "0 symbols", "missing health is never zero")
	assert.NotContains(t, frame, "0 lessons")
	assert.Contains(t, frame, "Semantic health unreadable", "the attention list holds the full reason")
}
