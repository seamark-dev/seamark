package tui

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests of the summary item of the Overview. In the collapsed
// layouts the summary item replaces the cards, so it must hold every
// fact at every size.

// ovSummarySizes are the collapsed layouts, where the summary item must
// hold every fact: wide but too short for the cards, the common 80×24,
// and the documented minimum.
var ovSummarySizes = [][2]int{{110, ui.MinHeight}, {80, 24}, {ui.MinWidth, ui.MinHeight}}

// ovOpenSummary selects the summary item and opens its detail: enter
// focuses the Detail box in the wide layout and opens it in the narrow
// layout.
func ovOpenSummary(t *testing.T, h *harness) {
	t.Helper()

	_ = h.view()
	h.press("home")
	require.Equal(t, ovSummaryKey, ovScreen(h).selKey, "home reaches the summary item")

	h.press("enter")
}

// ovReadDetail scrolls the open detail from its top to its end and
// returns every detail line it showed, in order. Only the text inside
// the Detail box counts: the second box in the wide layout, the only
// box in the narrow layout, so list rows never match.
func ovReadDetail(t *testing.T, h *harness, w, hgt int) []string {
	t.Helper()

	h.press("home")

	var (
		out  []string
		last string
	)

	for range 300 {
		frame := h.view()
		assertFrame(t, frame, w, hgt)

		if frame == last {
			return out
		}

		last = frame
		lines := strings.Split(frame, "\n")

		// The chrome takes four rows above the content and one below.
		for _, l := range lines[4 : len(lines)-1] {
			parts := strings.Split(l, "│")

			switch {
			case w >= ui.WideWidth && len(parts) >= 5:
				out = append(out, parts[3])
			case w < ui.WideWidth && len(parts) >= 3:
				out = append(out, parts[1])
			}
		}

		h.press("down")
	}

	require.Fail(t, "the detail never stops scrolling")

	return nil
}

// ovFlat joins lines and collapses runs of spaces, so a value that
// wraps under its kv column reads as one phrase.
func ovFlat(lines []string) string {
	return strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
}

func TestOverviewSummaryItemHoldsEveryFactAtEverySize(t *testing.T) {
	for _, size := range ovSummarySizes {
		w, hgt := size[0], size[1]

		t.Run(strconv.Itoa(w)+"x"+strconv.Itoa(hgt), func(t *testing.T) {
			f := ovPopulated()
			h := newHarness(t, f, w, hgt)

			ovOpenSummary(t, h)

			lines := ovReadDetail(t, h, w, hgt)
			flat := ovFlat(lines)

			for _, want := range []string{
				// Index: freshness, age, coverage, parse errors, database.
				"Workspace summary", "Freshness ✓ current", "Indexed 2h ago",
				"Files 1,200 of 1,240 parsed, 40 skipped", "Parse errors none",
				"Database /work/repo/.seamark/index.db", "Schema v6, this seamark reads v6",
				// Navigate: the facts the one-line summary drops.
				"Symbols 12,345", "Edges 45,678", "Decisions 321 from history",
				// Learn.
				"Proposals 3 proposals pending", "Lessons 12", "Reviews mined 3d ago",
				"Distill agent claude", "Lessons hook Claude Code, Codex",
				// Guard.
				"Policy mode warn, compiles", "Rules 2 deny, 3 approval rules",
				"Gate hooks Claude Code warn, Codex warn",
				// Clients: each brief in full, including the tail that the
				// summary rows cut.
				"Claude Code ! needs attention", "skills need refresh · tool grants not configured",
				"Codex ! needs attention", "skills not installed · tool grants conflict",
				"Shared Agent ✓ configured", "tool grants not supported",
				"seamark status",
			} {
				assert.Contains(t, flat, want)
			}

			for _, l := range lines {
				assert.NotContains(t, l, "…", "the summary detail never cuts a fact")
			}

			assert.Empty(t, f.called(), "reading the summary must not plan, preview, or mutate")
		})
	}
}

func TestOverviewSummaryItemKeepsProblemsFirst(t *testing.T) {
	h := newHarness(t, ovPopulated(), 80, 24)
	o := ovScreen(h)

	rows := ovRowTexts(o, 76)
	require.NotEmpty(t, rows)
	assert.Contains(t, rows[0], "Workspace summary", "the summary item is the first row")
	assert.Less(t, 0, ovIndexOf(rows, "ACTION"))

	_ = h.view()
	assert.Equal(t, "doctor:2:policy", o.selKey, "the cursor starts on the first actionable item")

	// Moving up from the first actionable item reaches the summary.
	h.press("up")
	assert.Equal(t, ovSummaryKey, o.selKey)

	// A refresh keeps the summary selected.
	h.press("r")
	assert.Equal(t, ovSummaryKey, o.selKey)
}

func TestOverviewSummaryItemWaitsForEverySource(t *testing.T) {
	m := newModel(ovPopulated(), ui.NewTheme(false))

	// Start the loads without running them, so every source is loading.
	_ = m.st.load(allSections...)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	o, ok := m.screens[destOverview].(*overview)
	require.True(t, ok)

	for _, it := range o.attention() {
		assert.NotEqual(t, ovSummaryKey, it.key, "the summary item cannot take the first selection while loading")
	}
}

func TestOverviewSummaryItemShowsIndexFactsWithoutReadyIndex(t *testing.T) {
	f := ovPopulated()
	f.index = IndexView{Probe: workspace.IndexProbe{
		State: workspace.IndexOlder, Path: f.dbPath, Schema: 4, Supported: 6,
		IndexedAt: time.Now().Add(-50 * time.Hour).Unix(),
	}}

	h := newHarness(t, f, ui.MinWidth, ui.MinHeight)
	ovOpenSummary(t, h)

	flat := ovFlat(ovReadDetail(t, h, ui.MinWidth, ui.MinHeight))

	for _, want := range []string{
		"State ! needs upgrade", "Indexed 2d ago", "Database /work/repo/.seamark/index.db",
		"Schema v4, this seamark reads v6", "Health available after an index build",
		"Proposals 3 proposals pending", "Lessons hook Claude Code, Codex",
	} {
		assert.Contains(t, flat, want)
	}

	f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent, Path: f.dbPath}}
	h.press("r")

	flat = ovFlat(ovReadDetail(t, h, ui.MinWidth, ui.MinHeight))
	assert.Contains(t, flat, "State ! no index")
	assert.Contains(t, flat, "Database /work/repo/.seamark/index.db")
	assert.NotContains(t, flat, "index time unknown", "a missing database has no index time to report")
}

func TestOverviewSummaryShowsStaleNoteAfterFailedHealthReload(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 80, 24)

	// The index stays ready, but the semantic health read now fails.
	f.index = IndexView{Probe: f.index.Probe, StatusErr: errors.New("database is locked")}
	h.press("r")

	require.True(t, h.m.st.index.stale(), "a failed reload keeps the good snapshot")

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "INDEX ✓ 1,200/1,240 stale",
		"the one-line summary marks the stale index snapshot")

	summary := ovFind(t, ovScreen(h), ovSummaryKey)
	assert.Equal(t, ui.ToneAttention, summary.tone)
	assert.Equal(t, "stale", summary.badge)

	ovOpenSummary(t, h)

	flat := ovFlat(ovReadDetail(t, h, 80, 24))
	assert.Contains(t, flat, "stale: database is locked. The index facts come from the read")
	assert.Contains(t, flat, "Symbols 12,345", "the stale snapshot keeps its old facts")
	assert.Contains(t, flat, "Freshness ✓ current")

	// A good read clears the note.
	f.index = ovReadyIndex(f.root, f.dbPath)
	h.press("r")
	assert.NotContains(t, h.view(), "stale:")
	assert.Equal(t, "all facts", ovFind(t, ovScreen(h), ovSummaryKey).badge)
}

func TestOverviewSummaryRendersFirstLoadHealthFailure(t *testing.T) {
	f := ovPopulated()
	f.index = IndexView{Probe: f.index.Probe, StatusErr: errors.New("open: permission denied")}

	h := newHarness(t, f, 80, 24)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "INDEX ? health unknown")
	assert.Contains(t, frame, "Semantic health unreadable")
	assert.NotContains(t, frame, "reading", "a failed first read is not a loading state")

	ovOpenSummary(t, h)

	flat := ovFlat(ovReadDetail(t, h, 80, 24))
	assert.Contains(t, flat, "Health ! read failed open: permission denied")
	assert.Contains(t, flat, "Health health unknown: open: permission denied")
	assert.NotContains(t, flat, "stale:", "a first read has no older snapshot to call stale")
}

func TestOverviewSummaryRendersFailedFirstLoadWithoutSnapshot(t *testing.T) {
	h := newHarness(t, ovPopulated(), 80, 24)

	// A section whose first load failed has an error and no snapshot.
	h.m.st.index = section[IndexView]{}
	h.m.st.index.set(IndexView{}, errors.New("open: permission denied"), time.Now())

	o := ovScreen(h)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "INDEX ! read failed")
	assert.NotContains(t, frame, "reading", "a failed first read is not a loading state")
	assert.Equal(t, ovActionable, ovFind(t, o, "index:health").group)

	flat := ovFlat(strings.Split(ansi.Strip(ovDetail(o, ovFind(t, o, ovSummaryKey), 78)), "\n"))
	assert.Contains(t, flat, "State ! read failed")
	assert.Contains(t, flat, "Reason open: permission denied")
	assert.Contains(t, flat, "Health health unknown: open: permission denied")
}

func TestOverviewSummaryDetailRendersInertAndASCII(t *testing.T) {
	f := ovPopulated()
	f.index.Status.DistillAgent = "evil \x1b[31mcmd\x1b]0;title\x07 --token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	claude := f.integrations[0]
	claude.Name = "Claude \x1b[2JCode"
	f.integrations[0] = claude

	h := newHarness(t, f, 80, 24)
	ovOpenSummary(t, h)

	raw := h.m.View()
	assert.NotContains(t, raw, "\x1b[31m")
	assert.NotContains(t, raw, "\x1b[2J")
	assert.NotContains(t, raw, "\x07")

	o := ovScreen(h)
	body := ovDetail(o, ovFind(t, o, ovSummaryKey), 78)
	assert.NotContains(t, body, "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "the distill command is redacted")

	h.m.st.theme = ui.NewTheme(true)

	for _, r := range ansi.Strip(ovDetail(o, ovFind(t, o, ovSummaryKey), 78)) {
		require.Less(t, r, rune(128), "the ASCII theme renders %q in the summary detail", r)
	}
}

func TestOverviewDetailHomeAndEndReachBothEndsOfALongDetail(t *testing.T) {
	// One client and a clean doctor report keep the list short, so the
	// summary detail is longer than the list that selects it.
	f := newFake()
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.integrations = []integration.Inspection{ovClaude()}

	h := newHarness(t, f, ui.MinWidth, ui.MinHeight)
	o := ovScreen(h)

	ovOpenSummary(t, h)

	h.press("end")

	frame := h.view()
	assert.Contains(t, frame, "No action needed.", "end reaches the last detail line")
	require.Greater(t, o.detailOff, len(o.rows(o.attention())),
		"the detail scrolls further than the list has rows")

	h.press("home")
	assert.Equal(t, 0, o.detailOff, "home reaches the first detail line")
	assert.Contains(t, h.view(), "Workspace summary")
}
