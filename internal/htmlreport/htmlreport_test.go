package htmlreport

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// clock is the fixed generation time every test renders at, so a page
// can be compared byte for byte.
var clock = time.Date(2026, 7, 29, 14, 30, 0, 0, time.UTC)

// seed builds an index with the shape the report reads: two production
// files with different fix histories, a test file that must stay off
// the map, mined lessons with their findings, and one proposal of each
// interesting status.
func seed(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for _, file := range []string{"api/live.py", "api/calm.py", "api/live_test.py"} {
			sym := model.Symbol{
				FQN: file + ".run", Name: "run", Kind: model.KindFunction, File: file,
				Span: model.Span{StartLine: 1, EndLine: 5},
			}
			if err := tx.InsertSymbol(&sym); err != nil {
				return err
			}
		}

		// live.py: mostly corrections, so it colours hot.
		for i := range 8 {
			title := fmt.Sprintf("fix: guard the %d case", i)
			if i >= 6 {
				title = fmt.Sprintf("add feature %d", i)
			}

			d := model.Decision{Kind: model.DecisionCommit, Ref: fmt.Sprintf("live%d", i),
				TS: int64(1700000000 + i), Title: title,
				Files: []string{"api/live.py", "api/live_test.py"}}
			if err := tx.InsertDecision(&d); err != nil {
				return err
			}
		}

		// calm.py: plenty of history, no fixes.
		for i := range 6 {
			d := model.Decision{Kind: model.DecisionCommit, Ref: fmt.Sprintf("calm%d", i),
				TS: int64(1700000100 + i), Title: fmt.Sprintf("extend reader %d", i),
				Files: []string{"api/calm.py"}}
			if err := tx.InsertDecision(&d); err != nil {
				return err
			}
		}

		return nil
	}))

	require.NoError(t, st.ReplaceLessons(
		[]model.Lesson{
			{ClusterKey: "api\x00RUF001", Region: "api", Reviewer: "coderabbit",
				Symptom: "RUF001 ambiguous unicode", Occurrences: 7, LastTS: 200},
			{ClusterKey: "api\x00naming", Region: "api", Reviewer: "human",
				Symptom: "inconsistent naming", Occurrences: 1, LastTS: 100},
		},
		[]model.Finding{
			{ID: 11, LessonKey: "api\x00RUF001", Path: "api/live.py", PR: 4,
				Reviewer: "coderabbit", Body: "Use ASCII quotes here.\n\nSecond paragraph.",
				URL:       "https://github.com/o/r/pull/4#discussion_r11",
				CreatedAt: 200, Source: model.SourceReview},
			{ID: 12, LessonKey: "api\x00RUF001", Path: "api/live.py", PR: 5,
				Reviewer: "human", Body: "Same problem again.",
				CreatedAt: 210, Source: model.SourceReview},
		}))

	require.NoError(t, st.ReplaceFixFindings([]model.Finding{
		{ID: 21, Path: "api/live.py", Body: "fix: guard the empty case",
			CreatedAt: 220, Source: model.SourceFixConventional},
	}))

	pending := model.Proposal{Signature: "sig-a", Rule: "ascii-quotes", Region: "api",
		Note: "Write quotes as ASCII in api/.", Members: []int64{11, 12},
		Agent: "claude/v3", Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&pending))

	// Two applied pins saying the same thing, which the duplicate audit
	// must notice.
	for _, rule := range []string{"error-context", "error-context-wrapping"} {
		p := model.Proposal{Signature: "sig-" + rule, Rule: rule, Region: "api",
			Note:    "Wrap returned errors with context describing the failed operation.",
			Members: []int64{21}, Agent: "claude/v3", Status: model.ProposalProposed}
		require.NoError(t, st.InsertProposal(&p))

		_, err := st.SetProposalStatus([]int64{p.ID}, model.ProposalApplied)
		require.NoError(t, err)
	}

	require.NoError(t, st.SetMeta("indexed_state", "abcdef0123456789"))

	return st, root
}

func renderPage(t *testing.T, st *store.Store, root string) string {
	t.Helper()

	var b strings.Builder
	require.NoError(t, Generate(&b, st, root, clock))

	return b.String()
}

func TestReportRendersEverySection(t *testing.T) {
	st, root := seed(t)
	page := renderPage(t, st, root)

	assert.Contains(t, page, "<title>seamark report — "+filepath.Base(root)+"</title>")
	assert.Contains(t, page, "index state abcdef012345", "the index state is stamped, untruncated")
	assert.Contains(t, page, "generated 2026-07-29 14:30")

	// Decision queue: the pending proposal, its evidence, its commands.
	assert.Contains(t, page, "ascii-quotes")
	assert.Contains(t, page, "Write quotes as ASCII in api/.")
	assert.Contains(t, page, "seamark lessons --apply p1")
	assert.Contains(t, page, "seamark lessons --dismiss p1")
	assert.Contains(t, page, "Use ASCII quotes here.", "the cited finding is quoted")
	assert.Contains(t, page, "2 findings · 2 events")
	assert.Contains(t, page, "1 finding · 1 event", "counts read as English, not as a template")

	// An applied pin is shown, but is not offered apply/dismiss again.
	assert.Contains(t, page, "error-context")
	assert.NotContains(t, page, "seamark lessons --apply p2")

	// Near-duplicate audit.
	assert.Contains(t, page, "2 pins, one theme")
	assert.Contains(t, page, "1 of 2 applied pins restate a theme already pinned")

	// Hotspot map.
	assert.Contains(t, page, `<svg class="map"`)
	assert.Contains(t, page, "live.py")
	assert.Contains(t, page, "calm.py")
	assert.NotContains(t, page, "live_test.py", "test churn stays off the map")

	// Lessons explorer, one-offs included.
	assert.Contains(t, page, "RUF001 ambiguous unicode")
	assert.Contains(t, page, "inconsistent naming")
}

func TestReportEvidenceMixCountsEverySource(t *testing.T) {
	st, root := seed(t)

	assert.Contains(t, renderPage(t, st, root), "evidence: 2 review · 1 fix:conventional")
}

func TestHotFilesColourHotterThanCalmOnes(t *testing.T) {
	st, root := seed(t)

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	fills := map[string]string{}
	for _, c := range r.Cells {
		fills[c.File] = c.Fill
	}

	require.Contains(t, fills, "api/live.py")
	require.Contains(t, fills, "api/calm.py")
	assert.Equal(t, "#C4553D", fills["api/live.py"], "6 fixes in 8 commits is a hotspot")
	assert.Equal(t, "#4B7A6B", fills["api/calm.py"], "no fixes is calm")
}

func TestCellsBelowTheHistoryFloorStayCalm(t *testing.T) {
	st, root := seed(t)

	// One file, one fix commit: a 100% ratio on a single commit is noise.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		sym := model.Symbol{FQN: "solo.run", Name: "run", Kind: model.KindFunction,
			File: "pkg/solo.py", Span: model.Span{StartLine: 1, EndLine: 2}}
		if err := tx.InsertSymbol(&sym); err != nil {
			return err
		}

		return tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit, Ref: "solo",
			TS: 1700000500, Title: "fix: solo", Files: []string{"pkg/solo.py"}})
	}))

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	for _, c := range r.Cells {
		if c.File != "pkg/solo.py" {
			continue
		}

		assert.Equal(t, "#4B7A6B", c.Fill, "one commit is not a hotspot")
		assert.Empty(t, c.Metric, "no fix ratio is claimed below the floor")
		assert.Contains(t, c.Tooltip, "too little history to judge")

		return
	}

	t.Fatal("pkg/solo.py was not drawn")
}

func TestCellsScopeToWhereLessonsLive(t *testing.T) {
	st, root := seed(t)

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	for _, c := range r.Cells {
		if c.File == "api/live.py" {
			assert.Equal(t, "api", c.Scope, "clicking a file filters to the region its lessons use")
			assert.Contains(t, c.Tooltip, "2 lessons in api")

			return
		}
	}

	t.Fatal("api/live.py was not drawn")
}

func TestAgedOutEvidenceIsNotReportedAsNoEvidence(t *testing.T) {
	st, root := seed(t)

	// A pin applied months ago, citing review comments that have since
	// fallen out of the mining window. The proposal record survives; its
	// findings do not.
	old := model.Proposal{Signature: "ancient", Rule: "old-rule", Region: "api",
		Note:    "Guidance distilled before the window moved on.",
		Members: []int64{900, 901, 902}, Agent: "claude/v3",
		Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&old))

	_, err := st.SetProposalStatus([]int64{old.ID}, model.ProposalApplied)
	require.NoError(t, err)

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	var card *Card

	for i := range r.Cards {
		if r.Cards[i].ID == old.ID {
			card = &r.Cards[i]
		}
	}

	require.NotNil(t, card, "a decided proposal still appears in the ledger")
	assert.Equal(t, 3, card.Cited, "what it cited is a fact of the record")
	assert.Zero(t, card.Retrieved, "none of it is still in the index")

	var b strings.Builder
	require.NoError(t, Render(&b, r))

	page := b.String()
	assert.Contains(t, page, "3 findings cited · evidence has aged out of the mining window")
	assert.NotContains(t, page, "0 findings",
		"a well-founded pin must never read as resting on nothing")
}

func TestPartlyAgedEvidenceSaysHowMuchSurvives(t *testing.T) {
	st, root := seed(t)

	// Cites one finding that exists and one that does not.
	p := model.Proposal{Signature: "half", Rule: "half-gone", Region: "api",
		Note: "Half its evidence is still here.", Members: []int64{11, 999},
		Agent: "claude/v3", Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	var b strings.Builder
	require.NoError(t, Render(&b, r))

	assert.Contains(t, b.String(), "2 findings cited · 1 still in the index")
}

func TestLessonScopeMembershipIsRegionsNotSubstrings(t *testing.T) {
	// The bug this replaces: a substring filter put a web lesson under
	// scope "api" because its symptom mentioned the word.
	assert.True(t, LessonInScope("api", "api"), "the region itself")
	assert.True(t, LessonInScope("api", "api/services"), "an ancestor governs the scope")
	assert.True(t, LessonInScope("api/services", "api"), "a descendant lives inside it")
	assert.True(t, LessonInScope("", "api"), "a repo-wide lesson governs everything")

	assert.False(t, LessonInScope("web/src", "api"), "an unrelated region")
	assert.False(t, LessonInScope("apiary", "api"),
		"a prefix that is not a path boundary is a different directory")
}

func TestRowsCarryTheScopesTheyBelongTo(t *testing.T) {
	st, root := seed(t)

	// Three lessons that a substring filter would get wrong: one in an
	// unrelated directory that merely says "api", one repo-wide, and one
	// below the api directory.
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "a", Region: "web/src", Reviewer: "human",
			Symptom: "call the API with a timeout", Occurrences: 5, LastTS: 10},
		{ClusterKey: "b", Region: "", Reviewer: "human",
			Symptom: "repo-wide guidance", Occurrences: 4, LastTS: 10},
		{ClusterKey: "c", Region: "api/services", Reviewer: "human",
			Symptom: "inside the api tree", Occurrences: 3, LastTS: 10},
	}, nil))

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	scopes := map[string]string{}
	for _, row := range r.Lessons {
		scopes[row.Symptom] = row.Scopes
	}

	require.Contains(t, scopes, "call the API with a timeout")
	assert.NotContains(t, scopes["call the API with a timeout"], "api",
		"mentioning a directory is not belonging to it")
	assert.Contains(t, scopes["repo-wide guidance"], "api",
		"a repo-wide lesson applies wherever you click")
	assert.Contains(t, scopes["inside the api tree"], "api")
}

func TestMapSaysHowMuchOfTheRepoItDraws(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Files the map has no room for must still be counted somewhere: a
	// picture that silently omits most of the repo reads as the repo.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for i := range 40 {
			file := fmt.Sprintf("bulk/mod%d.py", i)

			sym := model.Symbol{FQN: file + ".run", Name: "run", Kind: model.KindFunction,
				File: file, Span: model.Span{StartLine: 1, EndLine: 2}}
			if err := tx.InsertSymbol(&sym); err != nil {
				return err
			}

			if err := tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit,
				Ref: fmt.Sprintf("bulk%d", i), TS: int64(1700001000 + i),
				Title: "touch", Files: []string{file}}); err != nil {
				return err
			}
		}

		return nil
	}))

	r, err := Build(st, root, clock)
	require.NoError(t, err)

	assert.Equal(t, 40, r.FilesWithHistory, "every file with history is counted")
	assert.Less(t, len(r.Cells), r.FilesWithHistory, "the map draws only the busiest")

	var b strings.Builder
	require.NoError(t, Render(&b, r))
	assert.Contains(t, b.String(), fmt.Sprintf("Drawing %d of %d files with history",
		len(r.Cells), r.FilesWithHistory))

	// The directory box names the whole directory's size, not the drawn
	// subset, so a four-cell box does not read as a four-file directory.
	for _, g := range r.Groups {
		if g.Key == "bulk" {
			assert.Equal(t, 40, g.Files)

			return
		}
	}

	t.Fatal("the bulk directory was not drawn")
}

func TestMapFallsBackToHistoryWhenNothingWasParsed(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// A repo in a language seamark cannot parse yet: no symbols at all,
	// but plenty of history. An empty map would be the wrong answer.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for i := range 8 {
			if err := tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit,
				Ref: fmt.Sprintf("c%d", i), TS: int64(1700000000 + i), Title: "change",
				Files: []string{fmt.Sprintf("src/mod%d.rb", i%3)}}); err != nil {
				return err
			}
		}

		return nil
	}))

	r, err := Build(st, root, clock)
	require.NoError(t, err)
	assert.NotEmpty(t, r.Cells, "history alone still draws a map")
}

func TestRootFilesGetAReadableDirectoryLabel(t *testing.T) {
	assert.Equal(t, "repository root", dirLabel("."))
	assert.Equal(t, "api/services", dirLabel("api/services"))
}

func TestReportIsByteIdenticalForAnUnchangedIndex(t *testing.T) {
	st, root := seed(t)

	first := renderPage(t, st, root)
	for range 3 {
		assert.Equal(t, first, renderPage(t, st, root),
			"the same index must render the same file, or every regeneration is a diff")
	}
}

func TestOnlyTheTimestampChangesBetweenRuns(t *testing.T) {
	// The determinism the docs claim, stated exactly: an unchanged index
	// re-renders identically apart from the generation time it stamps.
	st, root := seed(t)

	later := clock.Add(97 * time.Minute)

	var first, second strings.Builder
	require.NoError(t, Generate(&first, st, root, clock))
	require.NoError(t, Generate(&second, st, root, later))

	got := strings.ReplaceAll(second.String(),
		later.Format("2006-01-02 15:04"), clock.Format("2006-01-02 15:04"))

	assert.NotEqual(t, first.String(), second.String(), "the stamped time does differ")
	assert.Equal(t, first.String(), got,
		"and nothing else does — no map iteration order leaks into the page")
}

func TestRepoNameIsTheDirectoryNotThePath(t *testing.T) {
	// A shared report should say "seamark", not the reader's home
	// directory. Uses filepath so the separator is the running OS's:
	// the Windows-backslash case this guards cannot be exercised from a
	// POSIX test run, only from a Windows one.
	assert.Equal(t, "repo", repoName(filepath.Join("home", "someone", "repo")))
	assert.Equal(t, "repo", repoName(filepath.Join("home", "someone", "repo")+string(filepath.Separator)))
	assert.Equal(t, "workspace", repoName(""))
	assert.Equal(t, "workspace", repoName(string(filepath.Separator)))
}

func TestUntrustedTextNeverBecomesMarkup(t *testing.T) {
	st, root := seed(t)

	const payload = `<script>alert(1)</script>`

	require.NoError(t, st.ReplaceLessons(
		[]model.Lesson{{ClusterKey: "k", Region: "api\" onmouseover=\"x", Reviewer: "human",
			Symptom: payload, Occurrences: 3, LastTS: 10}},
		[]model.Finding{{ID: 31, LessonKey: "k", Path: payload, Body: payload,
			URL: "javascript:alert(1)", CreatedAt: 10, Source: model.SourceReview}}))

	p := model.Proposal{Signature: "evil", Rule: payload, Region: "api",
		Note: "</p>" + payload, Members: []int64{31}, Agent: payload,
		Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	page := renderPage(t, st, root)

	assert.NotContains(t, page, payload, "the payload never reaches the page as markup")
	assert.Contains(t, page, "&lt;script&gt;alert(1)&lt;/script&gt;", "it is shown as text")
	assert.NotContains(t, page, `onmouseover="x"`, "a crafted region cannot close an attribute")
	assert.NotContains(t, page, "javascript:alert", "a non-http provenance link is dropped")
}

func TestControlCharactersAreStrippedButLineBreaksSurvive(t *testing.T) {
	st, root := seed(t)

	require.NoError(t, st.ReplaceLessons(
		[]model.Lesson{{ClusterKey: "k", Region: "api", Reviewer: "human",
			Symptom: "colour \x1b[31mred\x1b[0m escape", Occurrences: 2, LastTS: 10}},
		[]model.Finding{{ID: 41, LessonKey: "k", Path: "api/live.py",
			Body: "first line\nsecond line\x1b[0m", CreatedAt: 10, Source: model.SourceReview}}))

	p := model.Proposal{Signature: "esc", Rule: "r", Region: "api", Note: "n",
		Members: []int64{41}, Agent: "claude/v3", Status: model.ProposalProposed}
	require.NoError(t, st.InsertProposal(&p))

	page := renderPage(t, st, root)

	assert.NotContains(t, page, "\x1b", "terminal escapes are stripped even in a browser page")
	assert.Contains(t, page, "colour [31mred[0m escape")
	assert.Contains(t, page, "first line\nsecond line",
		"a review comment keeps the paragraphs that make it readable")
}

func TestEmptyIndexRendersEveryEmptyState(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	page := renderPage(t, st, root)

	assert.Contains(t, page, "Nothing distilled yet")
	assert.Contains(t, page, "No near-duplicates among 0 applied pins")
	assert.Contains(t, page, "No file history indexed yet")
	assert.Contains(t, page, "No lessons mined yet")
	assert.Contains(t, page, "no findings mined yet")
	assert.Contains(t, page, "</html>", "an empty index still produces a whole page")
}

func TestTruncationIsAlwaysDeclared(t *testing.T) {
	st, root := seed(t)

	lessons := make([]model.Lesson, maxLessonRows+5)
	for i := range lessons {
		lessons[i] = model.Lesson{ClusterKey: fmt.Sprintf("k%d", i), Region: "api",
			Reviewer: "human", Symptom: fmt.Sprintf("symptom %d", i),
			Occurrences: len(lessons) - i, LastTS: int64(i)}
	}

	require.NoError(t, st.ReplaceLessons(lessons, nil))

	for range maxCards + 3 {
		p := model.Proposal{Signature: "s", Rule: "r", Region: "api", Note: "n",
			Agent: "claude/v3", Status: model.ProposalProposed}
		require.NoError(t, st.InsertProposal(&p))
	}

	page := renderPage(t, st, root)

	assert.Contains(t, page, fmt.Sprintf("Showing the %d most frequent of %d lessons",
		maxLessonRows, len(lessons)))
	assert.Contains(t, page, fmt.Sprintf("Showing %d of", maxCards))
}

func TestStaleIndexWarnsInsideTheFile(t *testing.T) {
	// A report outlives the terminal that produced it, so the warning
	// has to live in the page rather than only on stderr.
	var fresh, stale strings.Builder

	require.NoError(t, Render(&fresh, &Report{IndexState: "abc"}))
	require.NoError(t, Render(&stale, &Report{IndexState: "abc", Stale: true}))

	assert.NotContains(t, fresh.String(), "The workspace has changed")
	assert.Contains(t, stale.String(), "The workspace has changed")
}

func TestInteractiveControlsAreKeyboardOperable(t *testing.T) {
	// Everything clickable must be reachable and operable without a
	// mouse: focusable in the markup, driven by the shared Enter/Space
	// handler in the script.
	st, root := seed(t)
	page := renderPage(t, st, root)

	assert.Contains(t, page, `<th data-sort="num" scope="col" tabindex="0" aria-sort="none">`,
		"sortable headers are focusable and announce their sort state")
	assert.Contains(t, page,
		`<code tabindex="0" role="button" aria-label="Copy the apply command">seamark lessons --apply p1</code>`,
		"copy controls are focusable buttons")
	// Anchored to the cell element: a bare attribute substring would
	// also match the copy controls above and prove nothing about cells.
	assert.Regexp(t, `<g class="cell"[^>]* tabindex="0"[^>]* role="button"[^>]* aria-label="`,
		page, "map cells are focusable buttons")
	assert.Contains(t, page, "pressable(cell, () => scopeTo(cell))",
		"the script wires cells through the keyboard helper, not just click")
}

func TestMapIsWellFormedMarkup(t *testing.T) {
	st, root := seed(t)
	page := renderPage(t, st, root)

	start := strings.Index(page, `<svg class="map"`)
	require.Positive(t, start, "the map is drawn")

	end := strings.Index(page[start:], "</svg>")
	require.Positive(t, end)

	svg := page[start : start+end+len("</svg>")]
	decoder := xml.NewDecoder(strings.NewReader(svg))

	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}

		require.NoError(t, err, "the generated SVG must parse")
	}
}

func TestSafeURLKeepsOnlyWebLinks(t *testing.T) {
	assert.Equal(t, "https://example.com/a", safeURL("https://example.com/a"))
	assert.Equal(t, "http://example.com/a", safeURL("http://example.com/a"))
	assert.Empty(t, safeURL("javascript:alert(1)"))
	assert.Empty(t, safeURL("file:///etc/passwd"))
	assert.Empty(t, safeURL("/relative/path"))
	assert.Empty(t, safeURL(""))
	assert.Empty(t, safeURL("https://"), "a scheme without a host links nowhere")
}

func TestShortenPathKeepsTheIdentifyingEnd(t *testing.T) {
	assert.Equal(t, "api/services", shortenPath("api/services", 34))

	got := shortenPath("a/very/long/path/to/a/very/deep/directory", 22)
	assert.Len(t, []rune(got), 22)
	assert.True(t, strings.HasPrefix(got, "…"), got)
	assert.True(t, strings.HasSuffix(got, "very/deep/directory"),
		"the identifying tail survives, the leading path does not")
}

func TestSourceMixOrdersByFrequency(t *testing.T) {
	mix := sourceMix([]model.Finding{
		{Source: model.SourceReview}, {Source: model.SourceFixConventional},
		{Source: model.SourceReview}, {Source: model.SourceRevert},
		{Source: model.SourceReview},
	})

	assert.Equal(t, "3 review · 1 fix:conventional · 1 revert", mix)
	assert.Empty(t, sourceMix(nil))
}

func TestCardsCarryEvidenceHealth(t *testing.T) {
	// The HTML report is where the human decides, so the same
	// re-judgment the --proposals ledger prints must be on the card:
	// tier with facts, prompt era, and the regions today's inference
	// would assign — with the retarget command when they drifted.
	st, root := seed(t)

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "scripts/a.py", PR: 1, Body: "x", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "scripts/b.py", PR: 2, Body: "y", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-h", "", 1, []model.Proposal{{
		Signature: "sig-h", Rule: "guard-empty-datasets", Region: "",
		Note: "Guard datasets before reductions.", Members: []int64{1, 2},
		Agent: "claude/v1", Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)

	var b strings.Builder
	require.NoError(t, Generate(&b, st, root, time.Unix(1_700_000_000, 0)))

	page := b.String()
	assert.Contains(t, page, `class="badge weak"`, "the tier badge renders")
	assert.Contains(t, page, "prompt v1", "the grandfathering era is visible")
	assert.Contains(t, page, "regions now: scripts", "region drift is named")
	assert.Contains(t, page, "--retarget p", "the drifted applied pin hands over the retarget command")
}

// TestReportShowsPinOutcome wires the passive loop into the page: an
// applied pin with a recurrence after a real firing must render its
// not-landing sentence with the notlanding style, and the stats row
// must carry the measured and not-landing tiles.
func TestReportShowsPinOutcome(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// One cited finding before the firing, one uncited finding in the
	// same cluster after it: the review-side join reads "not landing".
	now := time.Now()

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "api\x00boundary", Path: "api/handler.py", PR: 11,
			Body: "guard the api boundary", CreatedAt: now.Add(-time.Hour).Unix(), Source: "review"},
		{ID: 2, LessonKey: "api\x00boundary", Path: "api/handler.py", PR: 12,
			Body: "api boundary unguarded again", CreatedAt: now.Add(time.Hour).Unix(), Source: "review"},
	}))

	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "boundary-guard", Region: "api", Note: "Guard it.",
		Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalApplied,
	}))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: boundary-guard\n    region: api\n    note: Guard it.\n"), 0o644))

	pin := reviews.PinRule{Rule: "boundary-guard", Region: "api", Note: "Guard it."}
	require.NoError(t, reviews.RecordFiring(root, "api/handler.py", "Edit",
		[]model.Lesson{reviews.SurfacedPin{Pin: pin}.Lesson()}))

	page := renderPage(t, st, root)

	// The exact Line() sentence, with the actionable style on it.
	assert.Contains(t, page, "not landing — recurred 1× since exposure (fired 1×)")
	assert.Contains(t, page, `class="health notlanding"`)

	// The stats row carries the aggregate.
	assert.Contains(t, page, "<b>1</b><span>pins measured</span>")
	assert.Contains(t, page, "<b>1</b><span>not landing</span>")
}

// TestReportShowsScopeAdvisory wires the trigger-scope audit into the
// page: an applied pin whose live note names a path outside its
// regions, with an agreeing co-change partner, must render the same
// advisory sentence the ledger prints.
func TestReportShowsScopeAdvisory(t *testing.T) {
	root := t.TempDir()

	for _, rel := range []string{"api/schemas.py", "web/src/api/schema.ts"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "regenerate the client",
			CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "regenerate-web-schema", Region: "web/src/api",
		Note: "Edit api/schemas.py first.", Members: []int64{1},
		Agent: "claude/v3", Status: model.ProposalApplied,
	}))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: regenerate-web-schema\n    region: web/src/api\n"+
			"    note: Edit api/schemas.py first.\n"), 0o644))

	page := renderPage(t, st, root)

	assert.Contains(t, page, "delivery may miss the trigger")
	assert.Contains(t, page, "consider regions: [web/src/api, api]")
	assert.Equal(t, 1, strings.Count(page, "delivery may miss the trigger"),
		"one advisory, on the flagged card only")
}

// TestReportShowsBlockedTrigger wires the confirmed-but-blocked case
// into the page: a capped region set with a confirmed outside trigger
// produces no drift line, so the card must carry the blocked sentence.
func TestReportShowsBlockedTrigger(t *testing.T) {
	root := t.TempDir()

	for _, rel := range []string{"api/schemas.py", "web/src/api/schema.ts", "cmd/a.go", "internal/b.go"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
		{ID: 2, Path: "cmd/a.go", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
		{ID: 3, Path: "internal/b.go", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "capped-pin", Region: "web/src/api",
		Regions:      []string{"web/src/api", "cmd", "internal"},
		TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: time.Now().Unix(),
		Note: "n", Members: []int64{1, 2, 3},
		Agent: "claude/v4", Status: model.ProposalApplied,
	}))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: capped-pin\n    region: web/src/api\n"+
			"    regions: [web/src/api, cmd, internal]\n    note: n\n"), 0o644))

	page := renderPage(t, st, root)

	assert.Contains(t, page, "confirmed by co-change (38 shared commits) but not deliverable")
	assert.NotContains(t, page, "regions now:", "the cap keeps recompute equal to stored — no drift")
}

func TestNotLandingColorsMeetWCAGContrast(t *testing.T) {
	assert.GreaterOrEqual(t, contrastRatio("#A33A2B", "#F4F3F0"), 4.5)
	assert.GreaterOrEqual(t, contrastRatio("#EF806B", "#14181A"), 4.5)
	assert.GreaterOrEqual(t, contrastRatio("#EF806B", "#1B2124"), 4.5)
	assert.Contains(t, pageSource, "--rust:#A33A2B")
	assert.Contains(t, pageSource, "--rust:#EF806B")
}

func contrastRatio(foreground, background string) float64 {
	lighter := relativeLuminance(foreground)
	darker := relativeLuminance(background)
	if lighter < darker {
		lighter, darker = darker, lighter
	}

	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(hex string) float64 {
	var red, green, blue uint8
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &red, &green, &blue)
	channel := func(value uint8) float64 {
		srgb := float64(value) / 255
		if srgb <= 0.04045 {
			return srgb / 12.92
		}

		return math.Pow((srgb+0.055)/1.055, 2.4)
	}

	return 0.2126*channel(red) + 0.7152*channel(green) + 0.0722*channel(blue)
}
