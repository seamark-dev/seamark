package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// evSnap is a readable evidence snapshot of a ready index: a GitHub
// origin, gh on PATH, the default window, and both sources refreshed.
// The stamps are relative to now, so the ages stay stable.
func evSnap() learning.Snapshot {
	now := time.Now()

	return learning.Snapshot{
		Probe: workspace.IndexProbe{
			State: workspace.IndexReady, Root: "/work/repo", Path: "/work/repo/.seamark/index.db",
		},
		Findings:    map[string]int{"review": 118, "fix:conventional": 18, "fix:subject": 4, "revert": 2},
		FixFindings: 24, ReviewFindings: 118, Lessons: 12,
		FixesMinedAt:   now.Add(-50 * time.Hour).Unix(),
		ReviewsMinedAt: now.Add(-6*time.Hour - time.Minute).Unix(),
		Repository:     reviews.Repository{Remote: "origin", Owner: "acme", Name: "seamark"},
		RepoState:      reviews.RepositoryGitHub,
		GHPath:         "/usr/local/bin/gh",
		Window:         learning.Window{Effective: 730, Floor: 200},
		ObservedAt:     now,
	}
}

// evPlanOf builds a plan of mode over snap, as the learning service
// does. The plan has the effects of the mode, no blocker, and snap as
// its current state.
func evPlanOf(snap learning.Snapshot, mode index.SourceMode) *learning.Plan {
	p := &learning.Plan{
		Mode: mode, Workspace: workspace.Workspace{Root: "/work/repo", DBPath: "/work/repo/.seamark/index.db"},
		Probe: snap.Probe, Root: snap.Probe.Root, Repository: snap.Repository, RepoState: snap.RepoState,
		GHPath: snap.GHPath, Window: snap.Window, FixWindowDays: 365, Head: "1a2b3c4d5e6f7a8b",
		Network: mode == index.SourcesReviewsAndLocal && snap.RepoState == reviews.RepositoryGitHub,
		Current: snap, PlannedAt: time.Now(),
	}

	p.Affects = []learning.Effect{{Source: index.SourceFixes, Kind: learning.EffectReplace, Text: "replaces fixes"}}

	if mode == index.SourcesReviewsAndLocal {
		p.Affects = append(p.Affects, learning.Effect{Source: index.SourceReviews, Kind: learning.EffectReplace,
			Text: "replaces reviews"})
	} else {
		p.Affects = append(p.Affects, learning.Effect{Source: index.SourceReviews, Kind: learning.EffectKeep,
			Text: "keeps reviews"})
	}

	p.Affects = append(p.Affects,
		learning.Effect{Kind: learning.EffectKeep, Text: "keeps proposals"},
		learning.Effect{Kind: learning.EffectLater, Text: "the next distillation sees the changed evidence groups"},
	)

	return p
}

// evFake returns services with the evidence snapshot and plans that
// bind it.
func evFake() *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.learning = evSnap()
	f.planEvidence = func(mode index.SourceMode) (*learning.Plan, error) {
		return evPlanOf(f.learning, mode), nil
	}

	return f
}

// evOpen opens Lessons → Evidence at w by h.
func evOpen(t *testing.T, f *fakeServices, w, h int) (*harness, *lessons) {
	t.Helper()

	hs := lesOpen(t, f, w, h)
	hs.press("e")

	les, ok := hs.m.screens[destLessons].(*lessons)
	require.True(t, ok)
	require.Equal(t, lesEvidence, les.sub)

	return hs, les
}

// evText renders the context line, the list and the detail of the
// Evidence view without styles, flattened. Two boxes side by side mix
// their lines in a frame, so prose assertions read the panes instead.
func evText(les *lessons, w int) string {
	v := les.evid
	snap := v.st.learning.value

	lines := []string{v.contextLine(200)}
	for _, l := range v.listLines(snap, w) {
		lines = append(lines, l.text)
	}

	lines = append(lines, v.detailLines(snap, w)...)

	return lesFlat(ansi.Strip(strings.Join(lines, "\n")))
}

// evFrameLine returns the frame line that contains text, or "".
func evFrameLine(frame, text string) string {
	for _, l := range strings.Split(frame, "\n") {
		if strings.Contains(l, text) {
			return l
		}
	}

	return ""
}

func TestEvidenceShowsSourcesAndActions(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	lines := strings.Split(frame, "\n")
	assert.True(t, strings.HasPrefix(lines[7], "╭─ Evidence sources "), lines[7])
	assert.Contains(t, lines[5], "findings 142     reviews 118     fixes 24")
	assert.Contains(t, lines[5], "review window 730 days")

	assert.Contains(t, evFrameLine(frame, "› Local fixes"), "✓ refreshed 2d ago")
	assert.Contains(t, evFrameLine(frame, "  GitHub reviews"), "✓ refreshed 6h ago")
	assert.Contains(t, frame, "local git history · no network · 24 findings")
	assert.Contains(t, frame, "gh · network · 118 findings · window 730 days")
	assert.Contains(t, evFrameLine(frame, "Refresh local fixes "), "local · preview ›")
	assert.Contains(t, evFrameLine(frame, "Refresh reviews + local fixes"), "GitHub · preview ›")
	assert.Contains(t, evFrameLine(frame, "Prepare distillation"), "agent · preview ›")
	assert.Contains(t, evText(les, 58), "Nothing runs, fetches or writes until you press y.")

	// The detail pane shows the cursor source: its findings by kind and
	// the CLI route with its caveat.
	assert.Contains(t, frame, "╭─ Local fixes ")
	assert.Contains(t, frame, "FINDINGS  24")
	assert.Contains(t, evFrameLine(frame, "fix:conventional"), "18")
	assert.Contains(t, frame, "seamark index --fixes-only")
	assert.Contains(t, evText(les, 55), "The CLI route also updates the index first.")

	assert.Contains(t, shellAFooter(frame), "↑↓ move   enter open   tab detail   p proposals   m pins   s stats")
	assert.Empty(t, f.called())
}

func TestEvidenceBrowsingNeverPlansOrRuns(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	for range evItemCount {
		h.press("down")
	}

	assert.Equal(t, evDistill, les.evid.cursor)

	h.press("home")
	assert.Equal(t, evFixes, les.evid.cursor)

	h.press("down", "tab", "pgdown", "up", "tab", "enter", "esc", "end", "up")
	assert.Equal(t, evRefreshReviews, les.evid.cursor)

	reads := f.readCount("Learning")
	h.press("r")
	assert.Equal(t, reads+1, f.readCount("Learning"), "r reloads the evidence snapshot")

	h.press("s", "e", "p", "e")
	h.send(navigateMsg{dest: destLessons, intent: "evidence"})

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	assert.Empty(t, f.called(), "browsing and view refresh never plan, fetch or write")
	assert.Zero(t, f.callCount("RunEvidence"))
}

func TestEvidenceSourceStatesRenderDistinctly(t *testing.T) {
	stored := func(n int) *int { return &n }

	cases := []struct {
		name   string
		change func(s *learning.Snapshot)
		cursor int
		want   []string
	}{
		{name: "refreshed", change: func(*learning.Snapshot) {}, want: []string{"✓ refreshed 2d ago"}},
		{name: "never", change: func(s *learning.Snapshot) {
			s.FixesMinedAt, s.FixFindings = 0, 0
			s.Findings = map[string]int{"review": 118}
		}, want: []string{"· never refreshed", "local git history · no network · 0 findings",
			"Never: no refresh is on record"}},
		{name: "stamp unknown", change: func(s *learning.Snapshot) { s.ReviewsMinedAt = 0 }, cursor: 1,
			want: []string{"? last refresh unknown", "no refresh time is recorded"}},
		{name: "counts unknown", change: func(s *learning.Snapshot) {
			s.CountsErr = errors.New("database is locked")
			s.FixesMinedAt, s.ReviewsMinedAt = 0, 0
		}, want: []string{"findings ? unknown", "? last refresh unknown", "findings unknown",
			"the stored findings could not be read"}},
		{name: "no origin", change: func(s *learning.Snapshot) {
			s.RepoState, s.Repository = reviews.RepositoryNoOrigin, reviews.Repository{}
		}, cursor: 1, want: []string{"no origin remote", "unavailable: no origin remote"}},
		{name: "not github", change: func(s *learning.Snapshot) {
			s.RepoState = reviews.RepositoryNotGitHub
		}, cursor: 1, want: []string{"origin not on GitHub", "origin is not a github.com repository"}},
		{name: "gh found", change: func(*learning.Snapshot) {}, cursor: 1,
			want: []string{"gh found on PATH (/usr/local/bin/gh)", "? login not checked: only the fetch proves"}},
		{name: "gh missing", change: func(s *learning.Snapshot) { s.GHPath = "" }, cursor: 1,
			want: []string{"gh not on PATH", "gh not found on PATH."}},
		{name: "window configured", change: func(s *learning.Snapshot) {
			s.Window.Stored, s.Window.Effective = stored(90), 90
		}, cursor: 1, want: []string{"window 90 days", "90 days (config.yaml)", "the newest 200 comments always stay"}},
		{name: "window unlimited", change: func(s *learning.Snapshot) {
			s.Window.Stored, s.Window.Effective = stored(0), -1
		}, cursor: 1, want: []string{"window unlimited", "unlimited (config.yaml)"}},
		{name: "config error", change: func(s *learning.Snapshot) {
			s.Window = learning.Window{Floor: 200, ConfigErr: errors.New("yaml: line 3: bad indentation")}
		}, cursor: 1, want: []string{"review window ! config.yaml error", "! blocked · preview ›",
			"could not be read: yaml: line 3: bad indentation", "Settings (press 5)"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evFake()
			tc.change(&f.learning)

			h, les := evOpen(t, f, 120, 40)
			for range tc.cursor {
				h.press("down")
			}

			frame := h.view()
			assertFrame(t, frame, 120, 40)

			flat := evText(les, 57)
			for _, want := range tc.want {
				assert.Contains(t, flat, want)
			}

			assert.NotContains(t, frame, "findings 0     reviews 0", "an unknown count never reads as zero")
		})
	}
}

// The row, the card and the detail name the reach that the preview of
// the same row shows. The plan binds the origin, so no GitHub origin
// means no fetch. It does not bind gh, so a missing gh only warns.
func TestEvidenceReachMatchesThePreview(t *testing.T) {
	cases := []struct {
		name   string
		change func(s *learning.Snapshot)
		hint   string
		card   string
		detail []string
		avoid  []string
		label  string
	}{
		{name: "no origin", change: func(s *learning.Snapshot) {
			s.RepoState, s.Repository = reviews.RepositoryNoOrigin, reviews.Repository{}
		}, hint: "local only · preview ›", card: "no fetch · 118 findings · window 730 days",
			detail: []string{"local only · no network", "origin names no GitHub repository",
				"The local fix mine cannot stop part way."},
			avoid: []string{"gh · network", "GitHub · preview", "GitHub · network", "review comments of no origin",
				"stops the GitHub fetch"},
			label: "local only"},
		{name: "not github", change: func(s *learning.Snapshot) {
			s.RepoState = reviews.RepositoryNotGitHub
		}, hint: "local only · preview ›", card: "no fetch · 118 findings",
			detail: []string{"local only · no network"},
			avoid:  []string{"gh · network", "GitHub · preview", "review comments of origin not on GitHub"},
			label:  "local only"},
		{name: "gh missing", change: func(s *learning.Snapshot) { s.GHPath = "" },
			hint: "! no gh · preview ›", card: "gh · network · 118 findings",
			detail: []string{"GitHub · network if gh is on PATH at the start",
				"gh is not on PATH now. The refresh looks for gh again when it starts",
				"x in Lessons › Run stops the GitHub fetch."},
			avoid: []string{"GitHub · preview", "gh is not on PATH, so"},
			label: "network"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evFake()
			tc.change(&f.learning)

			h, les := evOpen(t, f, 120, 40)
			les.evid.cursor = evRefreshReviews

			frame := h.view()
			assertFrame(t, frame, 120, 40)
			assert.Contains(t, evFrameLine(frame, "› Refresh reviews + local fixes"), tc.hint)

			flat := evText(les, 57)
			assert.Contains(t, flat, tc.card)

			for _, want := range tc.detail {
				assert.Contains(t, flat, want)
			}

			for _, avoid := range tc.avoid {
				assert.NotContains(t, flat, avoid)
			}

			// The whole row name and the hint fit at the minimum size.
			h.resize(60, 20)
			row := evFrameLine(h.view(), "› Refresh reviews")
			assert.Contains(t, row, "Refresh reviews + local fixes")
			assert.Contains(t, row, tc.hint)

			h.press("enter")

			block, ok := les.evid.modal(120, 400)
			require.True(t, ok)
			assert.Contains(t, strings.Split(ansi.Strip(block), "\n")[0], tc.label+" ─╮",
				"the preview names the same reach as the row")
		})
	}
}

func TestEvidenceBlockedStatesLinkToTheBuild(t *testing.T) {
	cases := []struct {
		state   workspace.IndexState
		head    string
		link    string
		confirm bool
	}{
		{state: workspace.IndexAbsent, head: "No index yet", link: "enter build in Overview", confirm: true},
		{state: workspace.IndexNotIndexed, head: "No index yet", link: "enter build in Overview", confirm: true},
		{state: workspace.IndexOlder, head: "The index needs a rebuild", link: "enter build in Overview", confirm: true},
		{state: workspace.IndexNewer, head: "A newer seamark wrote this index", link: "enter open Overview"},
		{state: workspace.IndexUnreadable, head: "The index could not be read", link: "enter open Overview"},
	}

	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			f := evFake()
			f.learning = learning.Snapshot{Probe: workspace.IndexProbe{State: tc.state, Err: errors.New("probe")}}
			f.index = ovReadyIndex(f.root, f.dbPath)
			f.index.Probe.State = tc.state

			h, _ := evOpen(t, f, 120, 40)
			frame := h.view()
			assertFrame(t, frame, 120, 40)

			lines := strings.Split(frame, "\n")
			assert.True(t, strings.HasPrefix(lines[7], "╭─ Evidence sources "), lines[7])
			assert.Contains(t, frame, tc.head)
			assert.NotContains(t, frame, "Refresh local fixes", "a blocked view offers no refresh")
			assert.Contains(t, shellAFooter(frame), tc.link+"   p proposals   m pins   s stats")

			if tc.confirm {
				assert.Contains(t, frame, "seamark index --fixes-only")
			}

			for _, k := range []string{"down", "tab", "y"} {
				h.press(k)
			}

			assert.Equal(t, destLessons, h.m.active, "only enter follows the link")

			h.press("enter")
			assert.Equal(t, destOverview, h.m.active)
			assert.Equal(t, tc.confirm, ovScreen(h).confirm, "the link opens the build preview, never the build")
			assert.Empty(t, f.called(), "the link plans and builds nothing")
		})
	}
}

func TestEvidenceCompactLayouts(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 80, 24)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ Evidence sources ")
	assert.NotContains(t, frame, "╭─ Local fixes", "a compact terminal shows one pane")
	assert.Contains(t, frame, "enter for detail")
	assert.Contains(t, frame, "Prepare distillation")

	h.press("enter")
	frame = h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ Local fixes ")
	assert.Contains(t, shellAFooter(frame), "esc back")

	h.press("esc")
	assert.False(t, les.evid.detail)

	h.resize(60, 20)

	for i := range evItemCount {
		les.evid.cursor = i
		frame = h.view()
		assertFrame(t, frame, 60, 20)
		assert.Contains(t, evFrameLine(frame, "│ › "), evItemName(i), "the cursor row stays visible at the minimum size")
	}

	les.evid.cursor = evFixes
	frame = h.view()

	for _, want := range []string{"Local fixes", "GitHub reviews", "ACTIONS", "Refresh local fixes",
		"Refresh reviews + local fixes", "Prepare distillation"} {
		assert.Contains(t, frame, want, "every row fits at 60x20")
	}

	assert.Empty(t, f.called())
}

func TestEvidenceHostileTextIsInert(t *testing.T) {
	f := evFake()
	f.learning.Repository = reviews.Repository{Owner: "\x1b]0;evil\x07acme", Name: "re\x1b[31mpo"}
	f.learning.GHPath = "/bin/\x1b[2Jgh"
	f.learning.Findings["fix:\x1b[5mconv"] = 3
	f.learning.Window.ConfigErr = errors.New("yaml: \x1b[31mbad\x1b[0m \u009b1m")

	h, les := evOpen(t, f, 120, 40)

	for i := range evItemCount {
		les.evid.cursor = i

		for _, size := range [][2]int{{120, 40}, {80, 24}} {
			h.resize(size[0], size[1])

			raw := h.m.View()
			assert.NotContains(t, raw, "\x1b")
			assert.NotContains(t, raw, "\x07")
			assert.NotContains(t, raw, "\u009b")
		}
	}

	h.resize(120, 40)
	les.evid.cursor = evReviews
	assert.Contains(t, h.view(), "[31mbad[0m", "the text stays visible, only inert")
}

func TestEvidenceASCIIAndNoColor(t *testing.T) {
	f := evFake()

	h := &harness{t: t, m: newModel(f, newTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(120, 40))
	h.press("3", "e")

	for i := range evItemCount {
		h.m.screens[destLessons].(*lessons).evid.cursor = i

		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			raw := h.m.View()
			assert.NotContains(t, raw, "\x1b", "no color sequence without a color profile")

			for _, r := range ansi.Strip(raw) {
				require.Less(t, r, rune(0x80), "non-ASCII rune %q", r)
			}
		}
	}
}
