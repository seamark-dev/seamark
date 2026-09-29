package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/status"
)

// lesDesignFake is a ledger with the gate off and a gate-off preview.
func lesDesignFake(write bool) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = write
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, write), nil
	}

	return f
}

// lesDesignSizes are the sizes that every Lessons frame must fit: the
// design size, the common terminal, and the documented minimum.
var lesDesignSizes = [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}}

func TestLessonsDesignFramesFitEverySize(t *testing.T) {
	for _, size := range lesDesignSizes {
		w, hgt := size[0], size[1]

		for _, keys := range [][]string{
			nil,
			{"space"},
			{"enter"},
			{"tab", "pgdown"},
			{"space", "a"},
			{"space", "down", "space", "d"},
			{"a", "esc", "f"},
			{"s"},
			{"s", "tab", "pgdown"},
			{"s", "f", "f"},
		} {
			f := lesDesignFake(false)
			f.stats = statsSnapshot(statsMixedFirings(), statsSurfaced())

			h := lesOpen(t, f, w, hgt)
			h.press(keys...)
			assertFrame(t, h.view(), w, hgt)
		}
	}
}

func TestLessonsDesignLayoutAt120Columns(t *testing.T) {
	h := lesOpen(t, lesDesignFake(false), 120, 40)
	frame := h.view()
	lines := strings.Split(frame, "\n")

	// Row 5 holds the sub-tabs and the gate; row 6 the filter line.
	tabs, _, found := strings.Cut(lines[4], "distill")
	require.True(t, found, lines[4])
	assert.Equal(t, " Proposals   Stats   p / s", strings.TrimRight(tabs, " "))
	assert.True(t, strings.HasSuffix(strings.TrimRight(lines[4], " "), "distill.write off"))
	assert.Equal(t, " state [pending 3] applied 1  dismissed 1     region all", strings.TrimRight(lines[5], " "))

	// Row 8 opens both boxes: 62 cells, one gap, 57 cells.
	top := lines[7]
	assert.True(t, strings.HasPrefix(top, "╭─ Pending proposals"), top)
	assert.Equal(t, 62, width(top[:strings.Index(top, "╮")+len("╮")]))
	assert.Contains(t, top, "╭─ Proposal p1 ")
	assert.Equal(t, 120, width(top))

	// The column header and a row follow the 1a column widths.
	assert.True(t, strings.HasPrefix(lines[8], "│       ID  RULE                      REGION    CITED TIER   │ │"), lines[8])
	assert.Contains(t, lines[9], "│ › [ ] p1  check-nil-errors          internal/…  2   strong │")
	assert.Contains(t, frame, "tab detail")
	assert.Contains(t, frame, "a YAML")
}

func TestLessonsDesignNarrowOpensOneBoxAtATime(t *testing.T) {
	h := lesOpen(t, lesDesignFake(false), 80, 24)

	frame := h.view()
	assert.Contains(t, frame, "╭─ Pending proposals")
	assert.NotContains(t, frame, "Proposal p1")
	assert.Contains(t, frame, "enter for detail")

	h.press("enter")
	frame = h.view()
	assert.Contains(t, frame, "╭─ Proposal p1")
	assert.NotContains(t, frame, "Pending proposals")
	assert.Contains(t, frame, "↓ more · pgdn", "the detail scrolls at this height")

	h.press("pgdown")
	assert.Positive(t, lesProps(h).detail.off)
	assert.Contains(t, h.view(), "more · pg")

	h.press("esc")
	assert.Contains(t, h.view(), "╭─ Pending proposals")
}

func TestLessonsDesignYAMLModal(t *testing.T) {
	h := lesOpen(t, lesDesignFake(false), 120, 40)
	h.press("a")

	block, ok := lesProps(h).modal(120, 39)
	require.True(t, ok)

	lines := strings.Split(ansi.Strip(block), "\n")
	assert.Equal(t, 80, width(lines[0]), "the modal is 80 cells wide at 120 columns")
	assert.True(t, strings.HasPrefix(lines[0], "╭─ YAML p1 · check-nil-errors "), lines[0])
	assert.True(t, strings.HasSuffix(lines[0], " 4 lines ─╮"), lines[0])

	// The key row sits under a rule at the bottom of the modal.
	assert.Equal(t, "c copy to clipboard   esc close", strings.TrimSpace(strings.Trim(lines[len(lines)-2], "│")))
	assert.Contains(t, lines[len(lines)-3], strings.Repeat("─", 20))

	// The footer names the modal keys, and the frame keeps its size.
	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "c copy   esc close")
	assert.True(t, h.m.screens[destLessons].capturing())
}

func TestLessonsDesignApplyModalOffersYWhenTheGateIsOn(t *testing.T) {
	h := lesOpen(t, lesDesignFake(true), 120, 40)
	h.press("a")

	lines := lesModalLines(t, h)
	assert.True(t, strings.HasPrefix(lines[0], "╭─ Apply p1 · check-nil-errors "), lines[0])
	assert.Contains(t, lines, "y apply 1 pin   c copy to clipboard   esc cancel")
	assert.Contains(t, h.view(), "y apply   c copy   esc cancel")
}

func TestLessonsDesignCopySendsTheExactBlock(t *testing.T) {
	f := lesDesignFake(false)
	f.ledger.Pending[0].Note = lesFixLongNote

	var pv *proposals.Preview

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv = lesFixRealPreview(t, a, ids, false, f.ledger)

		return pv, nil
	}

	h := lesOpen(t, f, 80, 40)

	var copied []string

	h.m.st.copyText = func(text string) error {
		copied = append(copied, text)

		return nil
	}

	h.press("a")
	require.NotNil(t, pv)
	require.True(t, propFoldPins(pv.YAML, 60).folded, "the display folds the note")

	h.press("c")
	require.Equal(t, []string{pv.YAML}, copied, "c copies Preview.YAML, not the folded display")
	assert.Contains(t, h.view(), "Sent to the terminal clipboard (OSC 52)")
	assert.NotNil(t, lesProps(h).dec, "the modal stays open after a copy")
	assert.Equal(t, []string{"PreviewDecision:apply"}, f.called(), "a copy writes nothing")

	// A failed copy says so.
	h.m.st.copyText = func(string) error { return errors.New("terminal went away") }
	h.press("c")
	assert.Contains(t, h.view(), "Could not copy the pin block: terminal went away")

	// Without a terminal output the copy is unavailable.
	h.m.st.copyText = nil
	h.press("c")
	assert.Contains(t, h.view(), "Could not copy the pin block: "+errNoClipboard.Error())
}

func TestLessonsDesignOpenLinkOnlyOnTheKey(t *testing.T) {
	f := lesDesignFake(false)

	h := lesOpen(t, f, 120, 40)

	var opened []string

	h.m.st.openURL = func(url string) error {
		opened = append(opened, url)

		return nil
	}

	// Browsing never opens a link.
	h.press("down", "up", "enter", "tab", "space", "x")
	assert.Empty(t, opened)
	assert.True(t, lesFixBinding(t, h, "o").Enabled())
	assert.Equal(t, "open PR", lesFixBinding(t, h, "o").Help().Desc)

	h.press("o")
	assert.Equal(t, []string{"https://example.com/pr/45#c101"}, opened, "the first link of the cited findings")
	assert.Contains(t, h.view(), "Opened https://example.com/pr/45#c101 in the browser.")

	// A failed opener says so.
	h.m.st.openURL = func(string) error { return errors.New("only http and https links open in the browser") }
	h.press("o")
	assert.Contains(t, h.view(), "Could not open https://example.com/pr/45#c101: only http and https links open")

	// A session without an opener names the link instead.
	h.m.st.openURL = nil
	h.press("o")
	assert.Contains(t, h.view(), "This session cannot open a browser. The link is https://example.com/pr/45#c101")

	// A proposal without a link has no o key, and o says why.
	h.press("down")
	assert.False(t, lesFixBinding(t, h, "o").Enabled())
	h.press("o")
	assert.Contains(t, h.view(), "No cited finding of p2 has a link.")
	assert.Empty(t, f.called(), "opening a link previews and mutates nothing")
}

func TestLessonsDesignTabCountIsThePendingCount(t *testing.T) {
	h := lesOpen(t, lesDesignFake(false), 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	assert.Equal(t, "3", les.tabCount())
	assert.Contains(t, h.view(), "3 Lessons 3")

	f := newFake()
	f.ledgerErr = errors.New("database is locked")

	h = lesOpen(t, f, 120, 40)
	assert.Equal(t, "", h.m.screens[destLessons].(*lessons).tabCount(), "an unknown count is hidden, never zero")
	assert.NotContains(t, h.view(), "3 Lessons 0")
}

func TestLessonsDesignNoPendingState(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Pending = nil
	f.stats = statsSnapshot(statsMixedFirings(), statsSurfaced())
	f.index.Status = &status.Status{ReviewsMinedAt: time.Now().Add(-6 * time.Hour).Unix()}

	h := lesOpen(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "╭─ Pending proposals")
	assert.Contains(t, frame, "✓ No pending proposals")
	assert.Contains(t, frame, "Every distilled proposal has been applied or dismissed.")
	assert.Contains(t, frame, "3 lessons active · reviews mined 6h ago")
	assert.Contains(t, frame, "run seamark index --reviews to fetch new review findings,")
	assert.Contains(t, frame, "then seamark lessons --distill to propose more")

	// The title line is centered in the box.
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "No pending proposals") {
			inner := strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
			left := len(inner) - len(strings.TrimLeft(inner, " "))
			right := width(inner) - width(strings.TrimRight(inner, " "))
			assert.InDelta(t, left, right, 1, "centered: %q", line)
		}
	}

	// Unknown facts are left out, never shown as zero.
	g := newFake()
	g.ledger = lesLedger()
	g.ledger.Pending = nil

	h = lesOpen(t, g, 120, 40)
	assert.Contains(t, h.view(), "No pending proposals")
	assert.NotContains(t, h.view(), "lessons active")
	assert.NotContains(t, h.view(), "reviews mined")
}

func TestLessonsDesignFindingSummaryAndMeta(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Evidence[101] = domain.Finding{
		ID: 101, Path: "internal/cli/lessons.go", PR: 2, Reviewer: "coderabbit", Source: "review",
		Body: "_⚠️ Potential issue_ | _🟠 Major_\n\n**A malformed `lessons.yaml` silently disables pin-based " +
			"dedup.** More text.\n\n<!-- fingerprinting:phantom -->\n<details>prompt</details>",
		CreatedAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).Unix(),
	}
	f.ledger.Evidence[102] = domain.Finding{
		ID: 102, Paths: []string{"internal/store/store.go", "internal/store/a.go"}, Source: "fix:conventional",
		Body: "fix: close rows on every path",
	}
	f.ledger.ObservedAt = time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	h := lesOpen(t, f, 120, 60)

	detail := lesFixDetail(h)
	assert.Contains(t, detail, "EVIDENCE 2 cited findings · newest 16d")
	assert.Contains(t, detail, "1 A malformed lessons.yaml silently disables pin-based dedup. PR #2 · coderabbit · internal/cli/lessons.go")
	assert.Contains(t, detail, "2 fix: close rows on every path fix:conventional · internal/store/store.go +1")

	frame := h.view()
	assert.NotContains(t, frame, "**")
	assert.NotContains(t, frame, "Potential issue")
	assert.NotContains(t, frame, "fingerprinting")
	assert.NotContains(t, frame, "⚠")
}

func TestLessonsDesignSummaryStripsMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"bold title after a tagline", "_⚠️ Potential issue_ | _🟠 Major_\n\n**Return unexpected .codex inspection errors.**\n\nDetails.",
			"Return unexpected .codex inspection errors."},
		{"first sentence", "The error from Get is ignored here. Check it first.", "The error from Get is ignored here."},
		{"period inside a word", "Treat only os.ErrNotExist as missing", "Treat only os.ErrNotExist as missing"},
		{"link keeps its text", "See [the guide](https://example.com/guide) for the rule.", "See the guide for the rule."},
		{"html comment and tags", "<!-- hidden --><b>Close</b> the rows.<br/>", "Close the rows."},
		{"inline code and italic", "Use `errors.Is` _always_ here.", "Use errors.Is always here."},
		{"snake case stays", "Rename my_var_name to x.", "Rename my_var_name to x."},
		{"dunder stays", "Call __init__ once.", "Call __init__ once."},
		{"bold inside a sentence", "Consider **errors.Is** here.", "Consider errors.Is here."},
		{"heading and list markers", "## Summary\n- Wrap the error.", "Summary"},
		{"emoji only line", "🚀🔥\n\nReal text here.", "Real text here."},
		{"fenced code", "```go\nx := 1\n```\nNo fence text.", "No fence text."},
		{"empty", "  \n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lesSummary(tc.body)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, len([]rune(got)), width(got), "every rune takes one cell: %q", got)
		})
	}
}

func TestLessonsDesignModalControlSequencesStayInert(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Evidence[101] = domain.Finding{ID: 101, PR: 1, Body: "**\x1b[31mred\x1b[0m title**", URL: "https://x/\x1b[2J"}
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, false)
		pv.YAML = "  # distilled \x1b]0;evil\x07\n  - rule: x\x1b[5m\n    note: y\u009b1m\n"

		return pv, nil
	}

	h := lesOpen(t, f, 120, 40)

	var toasts []string

	h.m.st.openURL = func(url string) error {
		toasts = append(toasts, url)

		return nil
	}

	h.press("o")
	assert.NotContains(t, h.m.View(), "\x1b")

	h.press("a")

	for _, frame := range []string{h.m.View(), func() string { b, _ := lesProps(h).modal(120, 39); return b }()} {
		assert.NotContains(t, frame, "\x1b")
		assert.NotContains(t, frame, "\x07")
		assert.NotContains(t, frame, "\u009b")
	}
}

func TestLessonsDesignASCIIModalAndStats(t *testing.T) {
	f := lesDesignFake(false)
	f.stats = statsSnapshot(statsMixedFirings(), statsSurfaced())

	h := &harness{t: t, m: newModel(f, newTheme(true))}
	h.run(h.m.Init())
	h.resize(120, 40)
	h.press("3")

	for _, keys := range [][]string{{"space", "a"}, {"esc", "s"}, {"tab", "f"}} {
		h.press(keys...)

		frame := h.view()
		for _, r := range frame {
			require.Less(t, r, rune(128), "non-ASCII rune %q in the ASCII theme:\n%s", r, frame)
		}
	}
}
