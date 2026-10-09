package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// evPreviewText renders the open preview modal without styles and
// flattens it, so prose assertions do not depend on the wrap. The
// border glyphs go away, because the modal is one box.
func evPreviewText(t *testing.T, les *lessons) string {
	t.Helper()

	block, ok := les.evid.modal(120, 400)
	require.True(t, ok, "a preview is open")

	return lesFlat(strings.ReplaceAll(ansi.Strip(block), "│", " "))
}

// evPress moves the cursor to row it and presses enter.
func evPress(h *harness, les *lessons, it evItem) {
	les.evid.cursor = it
	h.press("enter")
}

func TestEvidencePreviewOpensOnEnterAndEscCallsNothing(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	h.press("down", "down")
	require.Equal(t, evRefreshLocal, les.evid.cursor)

	h.press("enter")
	assert.Equal(t, []string{"PlanEvidence:local"}, f.called(), "enter plans; the plan only reads")
	require.True(t, les.capturing(), "the preview captures every key")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Refresh local fixes")
	assert.Contains(t, frame, "local only")
	assert.Contains(t, footerLine(frame), "y refresh   esc cancel")
	assert.Contains(t, footerLine(frame), "ctrl+c quit")

	text := evPreviewText(t, les)
	for _, want := range []string{
		"This refresh makes no network call and keeps the stored review findings. Nothing runs before you press y.",
		"Workspace /work/repo", "Database /work/repo/.seamark/index.db",
		"History fix commits of the last 365 days reachable from HEAD 1a2b3c4", "Network no",
		"WILL ~ replace the 24 stored local fix findings with a new mine of the history reachable from HEAD",
		"WON'T · contact GitHub", "· change the 118 stored review findings or the review lessons",
		"· change proposals, pins or the record of distilled evidence groups", "· run the agent or create proposals",
		"LATER · the next distillation sees the changed evidence groups",
		"The local fix mine cannot stop part way.",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "Repository", "a local refresh names no GitHub target")

	// Keys of the other views do nothing while the preview is open.
	h.press("p", "s", "u", "1", "r")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesEvidence, les.sub)
	assert.NotNil(t, les.evid.prev)

	h.press("esc")
	assert.Nil(t, les.evid.prev)
	assert.False(t, les.capturing())
	assert.Equal(t, []string{"PlanEvidence:local"}, f.called(), "esc runs nothing")
	assert.NotContains(t, h.view(), "y refresh")
}

func TestEvidencePreviewDisclosesTheGitHubFetch(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshReviews)
	assert.Equal(t, []string{"PlanEvidence:reviews-and-local"}, f.called())

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Refresh reviews + local fixes")
	assert.Contains(t, frame, "network")

	text := evPreviewText(t, les)
	for _, want := range []string{
		"Seamark mines the local fix commits first, then fetches the review comments through gh.",
		"The two sources succeed or fail on their own.",
		"Repository acme/seamark (remote origin)",
		"Access gh found on PATH (/usr/local/bin/gh)", "? login not checked: only the fetch proves access",
		"Window 730 days (default) · the newest 200 comments always stay",
		"Network yes: the GitHub API through gh",
		"+ fetch the review comments of acme/seamark through gh",
		"~ replace the stored review findings and lessons, only after a complete fetch",
		"· contact GitHub before you press y",
		"· keep a partial fetch: a failed, unavailable or stopped fetch keeps the 118 stored review findings",
		"Counts from GitHub are known only after the fetch.",
	} {
		assert.Contains(t, text, want)
	}

	h.press("esc")

	// Without a GitHub origin the local source still runs, and the
	// preview says that no fetch happens.
	f.learning.RepoState, f.learning.Repository = reviews.RepositoryNoOrigin, reviews.Repository{}
	evPress(h, les, evRefreshReviews)

	text = evPreviewText(t, les)
	assert.Contains(t, text, "local only")
	assert.Contains(t, text, "the review fetch cannot run")
	assert.Contains(t, text, "Repository unavailable: no origin remote")
	assert.Contains(t, text, "Network no")
	assert.Contains(t, text, "· change the 118 stored review findings or the review lessons",
		"the plan binds the origin, so no review write can follow")
	assert.NotContains(t, text, "+ fetch")
	assert.Contains(t, footerLine(h.view()), "y refresh", "the local source still runs")
}

// evNoCommitPlan is the plan of a repository without a commit, in the
// shape of the engine: no Head, a kept fix source, and no LATER line
// without a fetch, because such a refresh changes no evidence.
// TestEvidencePreviewOfAnEngineNoCommitPlan checks the engine rule.
func evNoCommitPlan(snap learning.Snapshot, mode index.SourceMode) *learning.Plan {
	p := evPlanOf(snap, mode)
	p.Head = ""
	p.Affects[0] = learning.Effect{Source: index.SourceFixes, Kind: learning.EffectKeep,
		Text: "keeps the stored fix-commit findings: no commit is reachable from HEAD, " +
			"so the local fix source reads unavailable"}

	if !p.Network {
		p.Affects = slices.DeleteFunc(p.Affects, func(e learning.Effect) bool {
			return e.Kind == learning.EffectLater
		})
	}

	return p
}

// Review fix: without a commit, the preview promised a fix mine and
// printed the raw plan text in WON'T.
func TestEvidencePreviewWithoutACommitPromisesNoFixMine(t *testing.T) {
	f := evFake()
	f.planEvidence = func(mode index.SourceMode) (*learning.Plan, error) {
		return evNoCommitPlan(f.learning, mode), nil
	}

	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshLocal)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	text := evPreviewText(t, les)
	for _, want := range []string{
		"No commit is reachable from HEAD, so the local fix source reads unavailable and keeps the stored " +
			"fix findings. This refresh makes no network call and keeps the stored review findings.",
		"History no commit yet: the local fix source reads unavailable",
		"WON'T · contact GitHub · change the 24 stored local fix findings: no commit is reachable from HEAD",
		"· change the 118 stored review findings or the review lessons",
	} {
		assert.Contains(t, text, want)
	}

	for _, avoid := range []string{"WILL", "Seamark mines", "LATER", "keeps the stored fix-commit findings"} {
		assert.NotContains(t, text, avoid, "no fix mine and no raw plan text")
	}

	assert.Contains(t, footerLine(h.view()), "y refresh", "the plan is ready; the run reads unavailable")

	// The combined refresh still fetches, so only the fix side changes.
	h.press("esc")
	evPress(h, les, evRefreshReviews)

	text = evPreviewText(t, les)
	for _, want := range []string{
		"No commit is reachable from HEAD, so the local fix source reads unavailable and keeps the stored " +
			"fix findings. Seamark fetches the review comments through gh.",
		"+ fetch the review comments of acme/seamark through gh",
		"· change the 24 stored local fix findings: no commit is reachable from HEAD",
		"LATER · the next distillation sees the changed evidence groups",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "Seamark mines")
	assert.NotContains(t, text, "~ replace the 24 stored local fix findings")

	// Without a GitHub origin, neither source can change evidence, so
	// the preview promises no later effect either.
	h.press("esc")
	f.learning.RepoState, f.learning.Repository = reviews.RepositoryNoOrigin, reviews.Repository{}
	evPress(h, les, evRefreshReviews)

	text = evPreviewText(t, les)
	for _, want := range []string{
		"the review fetch cannot run either",
		"· change the 24 stored local fix findings: no commit is reachable from HEAD",
		"· change the 118 stored review findings or the review lessons",
	} {
		assert.Contains(t, text, want)
	}

	for _, avoid := range []string{"WILL", "LATER", "changed evidence groups"} {
		assert.NotContains(t, text, avoid, "no source can change evidence")
	}

	// gh missing now: the lead names the condition of the fetch.
	h.press("esc")
	f.learning = evSnap()
	f.learning.GHPath = ""
	evPress(h, les, evRefreshReviews)
	assert.Contains(t, evPreviewText(t, les), "gh is not on PATH now. If gh is on PATH when the refresh starts, "+
		"Seamark fetches the review comments through it")
	assert.Equal(t, []string{"PlanEvidence:local", "PlanEvidence:reviews-and-local", "PlanEvidence:reviews-and-local",
		"PlanEvidence:reviews-and-local"}, f.called(), "a preview only plans")
}

// Integration check fix: a combined plan without a commit and without a
// GitHub origin promised that the next distillation sees changed
// evidence groups. The real engine plans it here, so the fixture above
// cannot drift from the engine rule.
func TestEvidencePreviewOfAnEngineNoCommitPlan(t *testing.T) {
	ws, _ := engWorkspace(t, "", "pkg")
	engGit(t, ws.Root, "checkout", "--orphan", "fresh")
	e := engNew(ws, engineDeps{fetch: (&engFetch{raw: engPage}).fetch})

	plan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)
	require.True(t, plan.Ready(), "%v", plan.Blockers)
	require.Empty(t, plan.Head)
	require.False(t, plan.Network)

	will, wont, later := evEffects(newTheme(false), plan)
	assert.Empty(t, will, "no source can change evidence")
	assert.Empty(t, later, "no later effect without a change")
	assert.Contains(t, wont, "change the "+itoa(plan.Current.FixFindings)+
		" stored local fix findings: no commit is reachable from HEAD")
	assert.Contains(t, wont, "change the 2 stored review findings or the review lessons")

	// With an origin, the fetch can still change the review corpus.
	ws, _ = engWorkspace(t, engOrigin, "pkg")
	engGit(t, ws.Root, "checkout", "--orphan", "fresh")
	e = engNew(ws, engineDeps{fetch: (&engFetch{raw: engPage}).fetch})

	plan, err = e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)
	require.True(t, plan.Network)

	_, _, later = evEffects(newTheme(false), plan)
	assert.NotEmpty(t, later, "the fetch can change evidence groups")
}

// The plan does not bind the gh path, so gh can appear between the
// preview and y, and the run then fetches. The preview must state that
// condition and make no claim that such a run breaks.
func TestEvidencePreviewWithoutGHMakesNoOfflinePromise(t *testing.T) {
	f := evFake()
	f.learning.GHPath = ""
	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshReviews)
	plan := les.evid.prev.plan
	require.True(t, plan.Network, "a GitHub origin makes the plan a network plan, with or without gh")

	block, ok := les.evid.modal(120, 400)
	require.True(t, ok)

	top := strings.Split(ansi.Strip(block), "\n")[0]
	assert.Contains(t, top, "network ─╮", "the right label names the possible network use")
	assert.NotContains(t, top, "local only")

	text := evPreviewText(t, les)
	for _, want := range []string{
		"gh is not on PATH now. If gh is on PATH when the refresh starts, Seamark then fetches the review " +
			"comments through it; else the review source reads unavailable.",
		"Access gh not found on PATH now. The refresh looks for gh again when it starts: with gh it fetches, " +
			"without gh the review source reads unavailable.",
		"Network yes, if gh is on PATH at the start: the GitHub API through gh",
		"+ fetch the review comments of acme/seamark through gh, if gh is on PATH at the start",
		"~ replace the stored review findings and lessons, only after a complete fetch",
		"· contact GitHub before you press y",
		"· keep a partial fetch: a failed, unavailable or stopped fetch keeps the 118 stored review findings",
		"Counts from GitHub are known only after the fetch.",
	} {
		assert.Contains(t, text, want)
	}

	for _, avoid := range []string{"Network no", "the review fetch cannot run", "login not checked"} {
		assert.NotContains(t, text, avoid)
	}

	_, wont, _ := evEffects(h.m.st.theme, plan)
	assert.NotContains(t, wont, "contact GitHub", "no promise without the y condition")

	for _, line := range wont {
		assert.NotContains(t, line, "or the review lessons", "no promise that the review corpus stays")
	}

	// gh appears before y, and the run fetches and replaces the reviews.
	// The record repeats the condition that the user confirmed.
	f.runEvidence = func(context.Context, *learning.Plan, func(index.RefreshEvent)) (learning.Outcome, error) {
		return learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: index.SourceOutcome{State: index.SourceRefreshed, Stored: 1, Lessons: 1},
			After:   evSnap(),
		}, nil
	}

	h.press("y")
	require.Equal(t, 1, f.callCount("RunEvidence"))

	text = lesFlat(ansi.Strip(strings.Join(les.evid.run.confirmedLines(80), "\n")))
	assert.Contains(t, text, "Network yes, if gh is on PATH at the start: the GitHub API through gh")
	assert.NotContains(t, text, "Network no")
}

func TestEvidencePreviewWhilePlanningCanBeCancelled(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	// The test drops the plan command, so the preview stays in its
	// planning stage, as while the plan reads.
	_ = les.evid.plan(index.SourcesLocal)
	seq := les.evid.seq.last

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Preparing the refresh preview")
	assert.Contains(t, footerLine(frame), "esc cancel")

	h.press("y")
	assert.Empty(t, f.called(), "y does nothing before the plan arrives")

	h.press("esc")
	assert.Nil(t, les.evid.prev)

	// The view drops the late plan of the closed preview.
	h.send(evPlanMsg{seq: seq, plan: evPlanOf(f.learning, index.SourcesLocal)})
	assert.Nil(t, les.evid.prev)
	assert.NotContains(t, h.view(), "Refresh local fixes ─", "no preview comes back")
	assert.Zero(t, f.callCount("RunEvidence"))
}

func TestEvidencePreviewShowsBlockersWithTheirRoutes(t *testing.T) {
	f := evFake()
	f.planEvidence = func(mode index.SourceMode) (*learning.Plan, error) {
		p := evPlanOf(f.learning, mode)
		p.Blockers = []learning.Blocker{{
			Kind: learning.BlockConfig, Reason: "yaml: line 3: mapping values are not allowed",
			Path: "/work/repo/.seamark/config.yaml",
		}}

		return p, nil
	}

	h, les := evOpen(t, f, 120, 40)
	evPress(h, les, evRefreshLocal)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Refresh blocked")
	assert.Contains(t, footerLine(frame), "enter open Settings › evidence   esc close")
	assert.NotContains(t, footerLine(frame), "y refresh")

	text := evPreviewText(t, les)
	assert.Contains(t, text, "This refresh cannot start. Nothing was fetched or written.")
	assert.Contains(t, text, "! yaml: line 3: mapping values are not allowed")
	assert.Contains(t, text, "file /work/repo/.seamark/config.yaml")
	assert.Contains(t, text, "The console never falls back to a default for a network run.")

	h.press("y")
	assert.Zero(t, f.callCount("RunEvidence"), "a blocked plan never starts")
	assert.False(t, h.m.st.busy())

	h.press("enter")
	assert.Equal(t, destSettings, h.m.active, "enter follows the route to the evidence settings")
	assert.Nil(t, les.evid.prev)

	// An index blocker routes to the build preview in Overview.
	f.planEvidence = func(mode index.SourceMode) (*learning.Plan, error) {
		p := evPlanOf(f.learning, mode)
		p.Probe.State = workspace.IndexOlder
		p.Blockers = []learning.Blocker{{Kind: learning.BlockIndex, Reason: "the index database is older",
			Path: "/work/repo/.seamark/index.db"}}

		return p, nil
	}

	h.press("3", "e")
	evPress(h, les, evRefreshReviews)
	assert.Contains(t, evPreviewText(t, les), "A refresh never builds, migrates or rewrites the index.")

	h.press("enter")
	assert.Equal(t, destOverview, h.m.active)
	assert.True(t, ovScreen(h).confirm, "the route opens the build preview, not the build")
	assert.Zero(t, f.callCount("RunEvidence"))
}

func TestEvidencePreviewFailureChangesNothing(t *testing.T) {
	f := evFake()
	f.planEvidence = func(index.SourceMode) (*learning.Plan, error) {
		return nil, errors.New("git: \x1b[31mfatal\x1b[0m: not a repository")
	}

	h, les := evOpen(t, f, 80, 24)
	evPress(h, les, evRefreshLocal)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "Preview failed")

	text := evPreviewText(t, les)
	assert.Contains(t, text, "The refresh preview could not be built: git: [31mfatal[0m: not a repository")
	assert.Contains(t, text, "Nothing was changed.")
	assert.NotContains(t, h.m.View(), "\x1b")

	h.press("y", "enter")
	assert.Zero(t, f.callCount("RunEvidence"))

	h.press("esc")
	assert.Nil(t, les.evid.prev)
}

func TestEvidencePreviewClosesAfterASettingsSave(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshReviews)
	require.NotNil(t, les.evid.prev)

	h.send(settingsSavedMsg{group: settings.GroupEvidence, at: time.Now()})
	assert.Nil(t, les.evid.prev, "the plan bound the old config.yaml")
	assert.Contains(t, h.view(), "Settings › evidence changed .seamark/config.yaml")

	h.press("y")
	assert.Zero(t, f.callCount("RunEvidence"))

	// A save of another group changes the same file, so it closes the
	// preview too.
	evPress(h, les, evRefreshLocal)
	h.send(settingsSavedMsg{group: settings.GroupIndexing, at: time.Now()})
	assert.Nil(t, les.evid.prev)
}

func TestEvidencePreviewClosesWhenTheObservationChanges(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshLocal)
	require.NotNil(t, les.evid.prev)

	// A reload that shows the same state keeps the preview.
	h.send(refreshMsg{kinds: []sectionKind{secLearning}})
	require.NotNil(t, les.evid.prev)

	// Another refresh stored new evidence meanwhile.
	f.learning.ReviewsMinedAt++
	h.send(refreshMsg{kinds: []sectionKind{secLearning}})
	assert.Nil(t, les.evid.prev)
	assert.Contains(t, h.view(), "The evidence changed after the preview")

	h.press("y")
	assert.Zero(t, f.callCount("RunEvidence"))

	// A changed origin closes it too.
	evPress(h, les, evRefreshReviews)
	f.learning.Repository.Owner = "fork"
	h.send(refreshMsg{kinds: []sectionKind{secLearning}})
	assert.Nil(t, les.evid.prev)
}
