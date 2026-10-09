package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// evStarted confirms a refresh of mode and keeps its worker from
// running. The test sends the events and the result itself, so the
// record stays active between the steps.
func evStarted(t *testing.T, h *harness, les *lessons, mode index.SourceMode) *evRun {
	t.Helper()

	it := evRefreshLocal
	if mode == index.SourcesReviewsAndLocal {
		it = evRefreshReviews
	}

	evPress(h, les, it)
	require.True(t, les.evid.confirmable())

	_ = les.evid.start()

	r := les.evid.run
	require.NotNil(t, r)
	h.send(openRunMsg{rec: r, show: true})

	return r
}

// evEvent sends one progress event of the first run.
func evEvent(h *harness, phase index.RefreshPhase, source string, fill func(*index.RefreshEvent)) {
	ev := index.RefreshEvent{Phase: phase, Source: source}
	if fill != nil {
		fill(&ev)
	}

	h.send(evEventMsg{id: 1, ev: ev})
}

// evNotice returns the notice row of a frame.
func evNotice(frame string) string {
	return strings.Split(frame, "\n")[3]
}

// evRecordText renders a record in one tall compact box and flattens
// it, so prose assertions see every line.
func evRecordText(r *evRun) string {
	return lesBoxFlat(ansi.Strip(r.view(80, 400)))
}

// The reasons of a failed review fetch, as the engine writes them. The
// review miner adds its prefix to the first stderr line of gh api.
const (
	evBadToken    = "review mining skipped: gh api: gh: Bad credentials (HTTP 401)"
	evRateLimited = "review mining skipped: gh api: gh: API rate limit exceeded for user ID 1. (HTTP 403)"
)

// evOutcome is a source outcome with a state, a cause and a reason.
func evOutcome(state index.SourceState, cause index.SourceCause, reason string) index.SourceOutcome {
	return index.SourceOutcome{State: state, Cause: cause, Reason: reason}
}

func TestEvidenceRunStartsExactlyOnce(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	var ran *learning.Plan

	f.runEvidence = func(ctx context.Context, p *learning.Plan, progress func(index.RefreshEvent)) (learning.Outcome, error) {
		ran = p

		progress(index.RefreshEvent{Phase: index.PhaseMineFixes, Source: index.SourceFixes})
		progress(index.RefreshEvent{Phase: index.PhasePersist, Source: index.SourceFixes})
		progress(index.RefreshEvent{Phase: index.PhaseSourceDone, Source: index.SourceFixes, State: index.SourceRefreshed})
		progress(index.RefreshEvent{Phase: index.PhaseFetchReviews, Source: index.SourceReviews})
		progress(index.RefreshEvent{Phase: index.PhaseFetched, Source: index.SourceReviews, Comments: 287, Kept: 250})
		// The engine groups every non-empty window before the write.
		progress(index.RefreshEvent{Phase: index.PhaseCluster, Source: index.SourceReviews})
		progress(index.RefreshEvent{Phase: index.PhasePersist, Source: index.SourceReviews})
		progress(index.RefreshEvent{Phase: index.PhaseSourceDone, Source: index.SourceReviews,
			State: index.SourceRefreshed})

		after := evSnap()
		after.FixFindings, after.ReviewFindings = 26, 120

		return learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 26, MinedAt: time.Now().Unix()},
			Reviews: index.SourceOutcome{State: index.SourceRefreshed, Stored: 120, Lessons: 13},
			After:   after, FinishedAt: time.Now(),
		}, nil
	}

	evPress(h, les, evRefreshReviews)
	preview := les.evid.prev.preview
	reads := f.readCount("Learning")

	h.press("y")
	h.press("y")

	assert.Equal(t, []string{"PlanEvidence:reviews-and-local", "RunEvidence"}, f.called(),
		"y starts exactly once; a repeated y reaches the record, which ignores it")
	assert.Same(t, preview, ran, "the run gets the confirmed plan")

	st := h.m.st
	assert.False(t, st.busy(), "the result frees the mutation slot")
	assert.Nil(t, st.run)
	assert.Equal(t, runEvidence, st.lastRunKind)
	assert.Equal(t, lesRun, les.sub, "the run opens Lessons › Run")
	assert.Equal(t, reads+1, f.readCount("Learning"), "the result reloads the evidence")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, evNotice(frame), "Evidence refreshed: Local fixes 26 findings, GitHub reviews 120 findings.")
	assert.Contains(t, frame, "✓ Evidence refreshed")
	assert.Contains(t, frame, "✓ refreshed")

	text := evRecordText(les.evid.run)
	for _, want := range []string{
		"Local fixes were refreshed: 26 findings are stored.",
		"GitHub reviews were refreshed: 120 findings and 13 lessons are stored.",
		"✓ Mine local fixes", "✓ Save local fixes 26 findings", "✓ Fetch GitHub reviews 287 comments · 250 in the window",
		"✓ Save review findings 120 findings, 13 lessons",
		"✓ Local fixes refreshed 26 findings stored", "✓ GitHub reviews refreshed 120 findings · 13 lessons stored",
		"enter prepare a distillation from the evidence", "n prepare this refresh again",
	} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, "grouping the comments into lessons", "a done fetch step shows its counts")

	assert.Contains(t, footerLine(frame), "enter prepare distillation   n plan again")

	// enter opens the scope step of a distillation; it plans nothing.
	h.press("enter")
	require.NotNil(t, les.runv.cur)
	assert.IsType(t, &distRecord{}, les.runv.cur)
	assert.Equal(t, []string{"PlanEvidence:reviews-and-local", "RunEvidence"}, f.called())
}

func TestEvidenceRunKeepsTheFetchCountsWhileGrouping(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	r := evStarted(t, h, les, index.SourcesReviewsAndLocal)
	evEvent(h, index.PhaseFetchReviews, index.SourceReviews, nil)
	evEvent(h, index.PhaseFetched, index.SourceReviews, func(e *index.RefreshEvent) { e.Comments, e.Kept = 287, 250 })
	evEvent(h, index.PhaseCluster, index.SourceReviews, nil)

	assert.Contains(t, evNotice(h.view()), "Refreshing evidence · GitHub reviews · grouping",
		"the run note shows the grouping")

	text := evRecordText(r)
	assert.Contains(t, text, "Fetch GitHub reviews 287 comments · 250 in the window", "the fetch step keeps its counts")
	assert.Contains(t, text, "grouping the review comments into lessons", "the activity shows the grouping")
	assert.NotContains(t, text, "grouping the comments into lessons")

	evEvent(h, index.PhasePersist, index.SourceReviews, nil)
	evEvent(h, index.PhaseSourceDone, index.SourceReviews, func(e *index.RefreshEvent) { e.State = index.SourceStopped })
	h.send(evDoneMsg{id: 1, out: learning.Outcome{
		Fixes:    evOutcomeStored(24),
		Reviews:  evOutcome(index.SourceStopped, index.CauseStopped, "stopped before the review write"),
		Comments: 287, Kept: 250, After: evSnap(),
	}})

	text = evRecordText(r)
	assert.Contains(t, text, "✓ Fetch GitHub reviews 287 comments · 250 in the window",
		"a stop after the grouping keeps the counts on the done fetch step")
	assert.Contains(t, text, "! Save review findings stopped")
}

func TestEvidenceRunSentencesAgreeWithTheCount(t *testing.T) {
	one := evSnap()
	one.FixFindings, one.ReviewFindings = 1, 1

	unknown := evSnap()
	unknown.CountsErr = errors.New("database is locked")

	cases := []struct {
		name  string
		mode  index.SourceMode
		plan  learning.Snapshot
		out   learning.Outcome
		want  []string
		avoid []string
	}{
		{name: "one stored finding", mode: index.SourcesReviewsAndLocal, plan: one, out: learning.Outcome{
			Fixes:   evOutcomeStored(1),
			Reviews: evOutcome(index.SourceStopped, index.CauseStopped, "stopped during the GitHub fetch"),
			After:   one,
		}, want: []string{
			"Local fixes were refreshed: 1 finding is stored.",
			"The GitHub fetch stopped before the write, so the 1 stored review finding stays as it was. " +
				"A partial fetch is never saved.",
			"the 1 stored review finding kept",
		}, avoid: []string{"1 finding are", "1 stored review findings"}},
		{name: "one kept finding in a local refresh", mode: index.SourcesLocal, plan: one, out: learning.Outcome{
			Fixes: evOutcome(index.SourceFailed, index.CauseStore, "database is locked"),
			After: one,
		}, want: []string{
			"The local fix refresh failed (database), so the 1 stored local fix finding stays.",
			"the 1 stored local fix finding kept", "the 1 stored review finding stays",
		}, avoid: []string{"1 stored local fix findings", "1 stored review findings"}},
		{name: "unknown counts", mode: index.SourcesReviewsAndLocal, plan: unknown, out: learning.Outcome{
			Fixes:   evOutcome(index.SourceFailed, index.CauseStore, "database is locked"),
			Reviews: index.SourceOutcome{State: index.SourceNotAttempted},
			After:   unknown,
		}, want: []string{
			"so the stored local fix findings stay.", "GitHub reviews were not attempted, so the stored review " +
				"findings stay.",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, evFake(), 120, 40)

			r := newEvRun(h.m.st, 1, evPlanOf(tc.plan, tc.mode), nil, time.Now().Add(-time.Minute))
			r.finish(tc.out, nil, time.Now())

			text := evRecordText(r)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}

			for _, avoid := range tc.avoid {
				assert.NotContains(t, text, avoid)
			}
		})
	}
}

func TestEvidenceRunIsRefusedWhileTheSlotIsBusy(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	evPress(h, les, evRefreshLocal)
	require.True(t, h.m.st.begin("Building the index"))

	frame := h.view()
	assert.False(t, les.evid.confirmable(), "a ready plan is not confirmable while the slot is busy")
	assert.NotContains(t, footerLine(frame), "y refresh", "y is off while the slot is busy")
	assert.Contains(t, evPreviewText(t, les), "Building the index is running. The refresh can start after it ends")

	h.press("y")
	assert.Zero(t, f.callCount("RunEvidence"))
	assert.Contains(t, h.m.toast.text, "Another operation is running: Building the index")
	assert.NotNil(t, les.evid.prev, "the preview stays, so the user can start after the build")

	h.send(mutationDoneMsg{})
	assert.Contains(t, footerLine(h.view()), "y refresh", "y is on again once the slot is free")

	h.press("y")
	assert.Equal(t, 1, f.callCount("RunEvidence"))
}

func TestEvidenceRunShowsProgressAndStops(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	var stopped error

	f.runEvidence = func(ctx context.Context, _ *learning.Plan, _ func(index.RefreshEvent)) (learning.Outcome, error) {
		evEvent(h, index.PhaseMineFixes, index.SourceFixes, nil)
		evEvent(h, index.PhasePersist, index.SourceFixes, nil)
		evEvent(h, index.PhaseSourceDone, index.SourceFixes, func(e *index.RefreshEvent) { e.State = index.SourceRefreshed })
		evEvent(h, index.PhaseFetchReviews, index.SourceReviews, nil)
		evEvent(h, index.PhaseFetchBytes, index.SourceReviews, func(e *index.RefreshEvent) { e.Bytes = 3 << 19 })

		h.press("u")
		require.Equal(t, lesRun, les.sub)

		frame := h.view()
		assertFrame(t, frame, 120, 40)
		assert.Contains(t, evNotice(frame), "Refreshing evidence · GitHub reviews · 1.5 MiB received")
		assert.NotContains(t, evNotice(frame), " of ", "a fetch has no known total")
		assert.Contains(t, frame, "Fetch GitHub reviews · acme/seamark")
		assert.Contains(t, footerLine(frame), "x stop")

		text := evRecordText(les.evid.run)
		assert.Contains(t, text, "✓ Mine local fixes")
		assert.Contains(t, text, "✓ Local fixes refreshed", "the pane uses the word that the source reported")
		assert.Contains(t, text, "Local fixes: refreshed")
		assert.NotContains(t, text, "Local fixes saved")
		assert.Contains(t, text, "GitHub reviews running 1.5 MiB received")

		h.press("x")
		assert.Contains(t, h.view(), "Stop the evidence refresh?")
		assert.Contains(t, footerLine(h.view()), "y stop   esc keep running")

		h.press("esc")
		require.NoError(t, ctx.Err(), "esc keeps the run going")

		h.press("x", "y")
		stopped = ctx.Err()

		frame = h.view()
		assert.Contains(t, evNotice(frame), "Stopping")
		assert.Contains(t, frame, "stopping")
		assert.Contains(t, frame, "Stopping at a safe point")
		assert.NotContains(t, footerLine(frame), "x stop", "a second stop is not offered")

		h.press("x")
		assert.False(t, les.evid.run.confirm)

		return learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceStopped, index.CauseStopped, "stopped during the GitHub fetch"),
			After:   evSnap(), FinishedAt: time.Now(),
		}, nil
	}

	evPress(h, les, evRefreshReviews)
	h.press("y")

	require.ErrorIs(t, stopped, context.Canceled, "y in the stop question cancels the context of the work")
	assert.False(t, h.m.st.busy())

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "! partial")
	assert.Contains(t, evNotice(frame), "! Evidence refresh finished with a stopped source")
	assert.NotContains(t, evNotice(frame), "✓", "a partial result never reads as a success")

	text := evRecordText(les.evid.run)
	for _, want := range []string{
		"The GitHub fetch stopped before the write, so the 118 stored review findings stay as they were.",
		"A partial fetch is never saved.",
		"! Fetch GitHub reviews stopped", "! GitHub reviews stopped the 118 stored review findings kept",
		"stopped during the GitHub fetch", "Press n to prepare the refresh again when you want the rest.",
	} {
		assert.Contains(t, text, want)
	}
}

func TestEvidenceRunStopsBeforeExit(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	f.runEvidence = func(ctx context.Context, _ *learning.Plan, _ func(index.RefreshEvent)) (learning.Outcome, error) {
		h.press("q")
		assert.Contains(t, h.view(), "Stop and exit?")

		h.press("enter")
		require.Error(t, ctx.Err(), "stop and exit cancels the fetch")
		assert.True(t, les.evid.run.stopping(), "the record shows the stop of the quit question")
		assert.False(t, h.quit, "the console waits for the work to end")

		return learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceStopped, index.CauseStopped, "stopped before the GitHub fetch"),
		}, nil
	}

	evPress(h, les, evRefreshReviews)
	h.press("y")

	assert.True(t, h.quit, "the console exits after the stopped work returns")
}

func TestEvidenceRunConflictPreparesAgain(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	conflict := fmt.Errorf("%w: HEAD moved", learning.ErrConflict)
	f.runEvidence = func(context.Context, *learning.Plan, func(index.RefreshEvent)) (learning.Outcome, error) {
		refused := index.SourceOutcome{State: index.SourceNotAttempted, Cause: index.CauseRefused,
			Reason: conflict.Error(), Err: conflict}

		return learning.Outcome{Fixes: refused, Reviews: refused, Conflict: conflict, After: evSnap()}, conflict
	}

	evPress(h, les, evRefreshLocal)
	h.press("y")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "! out of date")
	assert.Contains(t, frame, "The refresh preview is out of date")
	assert.NotContains(t, footerLine(frame), "enter prepare", "stale evidence offers no distillation")

	text := evRecordText(les.evid.run)
	assert.Contains(t, text, "The workspace changed after the preview: HEAD moved.")
	assert.Contains(t, text, "Nothing was fetched or written.")
	assert.Contains(t, text, "not attempted: refused")

	h.press("enter")
	assert.Equal(t, lesRun, les.sub, "enter does nothing on a stale result")

	h.press("n")
	assert.Equal(t, lesEvidence, les.sub)
	assert.Equal(t, 2, f.callCount("PlanEvidence:local"), "n prepares a new preview from the current state")
	require.NotNil(t, les.evid.prev)
	assert.True(t, les.evid.ready())
	assert.Equal(t, 1, f.callCount("RunEvidence"), "nothing reruns without a new y")
}

func TestEvidenceRunRendersEverySourceState(t *testing.T) {
	stale := fmt.Errorf("%w: another refresh changed the stored evidence", learning.ErrConflict)

	dbReplaced := fmt.Errorf("%w: the index database file was replaced", learning.ErrConflict)
	refusedWrite := evOutcome(index.SourceFailed, index.CauseRefused, dbReplaced.Error())
	fetched := []index.RefreshEvent{
		{Phase: index.PhaseMineFixes, Source: index.SourceFixes},
		{Phase: index.PhaseSourceDone, Source: index.SourceFixes, State: index.SourceUnavailable},
		{Phase: index.PhaseFetchReviews, Source: index.SourceReviews},
		{Phase: index.PhaseFetched, Source: index.SourceReviews, Comments: 287, Kept: 250},
		{Phase: index.PhaseSourceDone, Source: index.SourceReviews, State: index.SourceFailed},
	}

	cases := []struct {
		name   string
		mode   index.SourceMode
		events []index.RefreshEvent
		out    learning.Outcome
		err    error
		want   []string
		avoid  []string
	}{
		{name: "refreshed", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: index.SourceOutcome{State: index.SourceRefreshed, Stored: 118, Lessons: 12},
		}, want: []string{"✓ Evidence refreshed", "✓ refreshed", "✓ GitHub reviews refreshed 118 findings · 12 lessons stored"}},
		{name: "successful empty", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceEmpty},
			Reviews: index.SourceOutcome{State: index.SourceEmpty},
		}, want: []string{"✓ Evidence refreshed", "✓ GitHub reviews empty no review lessons · stored reviews replaced",
			"✓ Local fixes empty no fix commits · stored fix findings replaced",
			"GitHub reviews were refreshed with no review lesson, so the stored reviews were replaced."}},
		{name: "unavailable", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceUnavailable, index.CauseGHMissing, "gh is not installed"),
		}, want: []string{"! Evidence refresh finished with an unavailable source", "! partial",
			"! GitHub reviews unavailable: gh not on PATH", "Install gh and log in with it", "› gh auth login"},
			avoid: []string{"✓ Evidence refreshed"}},
		{name: "failed auth", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceFailed, index.CauseAuth, evBadToken),
		}, want: []string{"! Evidence refresh finished with a failed source", "! GitHub reviews failed: gh login",
			"the 118 stored review findings kept", "gh: Bad credentials (HTTP 401)",
			"Seamark never logs in for you.", "! Fetch GitHub reviews failed, gh login"}},
		{name: "failed rate limit", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceFailed, index.CauseRateLimited, evRateLimited),
		}, want: []string{"! Evidence refresh finished with a failed source", "! GitHub reviews failed: rate limit",
			"the 118 stored review findings kept", "API rate limit exceeded for user ID 1. (HTTP 403)",
			"Wait until the limit resets, then refresh again.", "› gh api rate_limit",
			"! Fetch GitHub reviews failed, rate limit"},
			avoid: []string{"gh auth login", "Check the network"}},
		{name: "failed unreadable", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: evOutcome(index.SourceFailed, index.CauseUnreadable, "invalid character"),
		}, want: []string{"failed: unreadable reply", "not the GitHub API JSON"}},
		{name: "failed store", mode: index.SourcesLocal, out: learning.Outcome{
			Fixes:   evOutcome(index.SourceFailed, index.CauseStore, "database is locked"),
			Reviews: index.SourceOutcome{State: index.SourceNotAttempted},
		}, err: errors.New("database is locked"),
			want: []string{"! Evidence refresh failed", "! failed", "! Local fixes failed: database",
				"the 24 stored local fix findings kept", "Doctor (press 4)", "· GitHub reviews not part of this refresh"}},
		{name: "no history", mode: index.SourcesLocal, out: learning.Outcome{
			Fixes: evOutcome(index.SourceUnavailable, index.CauseNoHistory, "local git history is unavailable"),
		}, want: []string{"! No evidence source could run", "! Local fixes unavailable: no git history",
			"· Mine local fixes skipped, no git history"}},
		{name: "stopped", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   evOutcome(index.SourceStopped, index.CauseStopped, "stopped before the start"),
			Reviews: evOutcome(index.SourceStopped, index.CauseStopped, "stopped before the start"),
		}, want: []string{"! Evidence refresh stopped", "! stopped", "! Local fixes stopped"}},
		{name: "refused", mode: index.SourcesLocal, out: learning.Outcome{
			Fixes:   evOutcome(index.SourceNotAttempted, index.CauseRefused, "another operation is running"),
			Reviews: evOutcome(index.SourceNotAttempted, index.CauseRefused, "another operation is running"),
		}, err: services.ErrBusy, want: []string{"! Evidence refresh did not start", "! not started",
			"! Local fixes not attempted: refused", "another operation is running"},
			avoid: []string{"Details:"}},
		{name: "error without a reason", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceNotAttempted},
			Reviews: index.SourceOutcome{State: index.SourceNotAttempted},
		}, err: errors.New("resolve root: /work/repo is not a git repository"),
			want: []string{"! Evidence refresh failed", "! failed",
				"Details: resolve root: /work/repo is not a git repository"},
			avoid: []string{"did not start"}},
		{name: "conflict after the fetch", mode: index.SourcesReviewsAndLocal, events: fetched,
			out: learning.Outcome{
				Fixes:    evOutcome(index.SourceUnavailable, index.CauseNoHistory, "local git history is unavailable"),
				Reviews:  refusedWrite,
				Comments: 287, Kept: 250, Conflict: dbReplaced,
			}, err: dbReplaced, want: []string{
				"The workspace changed after the preview: the index database file was replaced. The review " +
					"comments were fetched but not saved. Nothing was written.",
				"✓ Fetch GitHub reviews 287 comments · 250 in the window", "! Save review findings failed, refused",
			}, avoid: []string{"Nothing was fetched", "Details:"}},
		{name: "conflict after the fetch, events dropped", mode: index.SourcesReviewsAndLocal,
			out: learning.Outcome{
				Fixes:    evOutcome(index.SourceUnavailable, index.CauseNoHistory, "local git history is unavailable"),
				Reviews:  refusedWrite,
				Comments: 287, Kept: 250, Conflict: dbReplaced,
			}, err: dbReplaced, want: []string{"The review comments were fetched but not saved. Nothing was written."},
			avoid: []string{"Nothing was fetched"}},
		{name: "conflict after the fix write", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
			Reviews: refusedWrite, Comments: 287, Kept: 250, Conflict: dbReplaced,
		}, err: dbReplaced, want: []string{
			"The workspace changed after the preview: the index database file was replaced. Local fixes were " +
				"refreshed: 24 findings are stored. The GitHub fetch failed (refused), so the 118 stored review " +
				"findings stay as they were.",
			"! The refresh preview is out of date",
		}, avoid: []string{"Nothing was", "enter prepare"}},
		{name: "conflict", mode: index.SourcesReviewsAndLocal, out: learning.Outcome{
			Fixes:    evOutcome(index.SourceNotAttempted, index.CauseRefused, stale.Error()),
			Reviews:  evOutcome(index.SourceNotAttempted, index.CauseRefused, stale.Error()),
			Conflict: stale,
		}, err: stale, want: []string{"! The refresh preview is out of date", "! out of date",
			"another refresh changed the stored evidence", "n prepare this refresh again"},
			avoid: []string{"enter prepare"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evFake()
			h := newHarness(t, f, 120, 40)

			r := newEvRun(h.m.st, 1, evPlanOf(f.learning, tc.mode), nil, time.Now().Add(-time.Minute))
			for _, ev := range tc.events {
				r.apply(ev, time.Now())
			}

			tc.out.After = evSnap()
			r.finish(tc.out, tc.err, time.Now())

			text := evRecordText(r)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}

			for _, avoid := range tc.avoid {
				assert.NotContains(t, text, avoid)
			}

			for _, size := range [][2]int{{120, 36}, {80, 20}, {60, 15}} {
				for _, line := range strings.Split(r.view(size[0], size[1]), "\n") {
					assert.LessOrEqual(t, ansi.StringWidth(line), size[0])
				}
			}
		})
	}
}

func TestEvidenceRunLayoutsAtEverySize(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	r := evStarted(t, h, les, index.SourcesReviewsAndLocal)
	require.Equal(t, lesRun, les.sub)
	assert.True(t, r.active())
	assert.True(t, h.m.st.busy())

	evEvent(h, index.PhaseMineFixes, index.SourceFixes, nil)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "Evidence refresh")
		assert.Contains(t, frame, "Mine local fixes")
		assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(frame)), "x stop"), footerLine(frame))
		assert.Contains(t, evNotice(frame), "Refreshing evidence")
	}

	h.resize(120, 40)
	h.press("tab")
	assert.True(t, r.right, "tab moves the focus to the Sources pane")

	h.send(evDoneMsg{id: 1, out: learning.Outcome{
		Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
		Reviews: evOutcome(index.SourceFailed, index.CauseAuth, evBadToken),
	}})
	assert.False(t, r.active())
	assert.False(t, h.m.st.busy())

	// A late event or a second result of the same run changes nothing.
	evEvent(h, index.PhaseFetchReviews, index.SourceReviews, nil)
	h.send(evDoneMsg{id: 1, out: learning.Outcome{}})
	assert.Equal(t, index.SourceFailed, r.out.Reviews.State)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}
}

func TestEvidenceRunHostileTextIsInert(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	r := evStarted(t, h, les, index.SourcesReviewsAndLocal)
	h.send(evDoneMsg{id: 1, out: learning.Outcome{
		Fixes: index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
		Reviews: evOutcome(index.SourceFailed, index.CauseFetchFailed,
			"gh: \x1b]0;evil\x07 \x1b[31mHTTP 502\x1b[0m \u009b1m"),
	}, err: errors.New("fetch \x1b[2J failed")})

	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		h.resize(size[0], size[1])

		raw := h.m.View()
		assert.NotContains(t, raw, "\x1b")
		assert.NotContains(t, raw, "\x07")
		assert.NotContains(t, raw, "\u009b")
	}

	assert.Contains(t, evRecordText(r), "[31mHTTP 502[0m", "the reason stays visible, only inert")
}

func TestEvidenceRunASCII(t *testing.T) {
	f := evFake()

	h := &harness{t: t, m: newModel(f, ui.NewTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(120, 40))
	h.press("3", "e")

	les := h.m.screens[destLessons].(*lessons)
	evStarted(t, h, les, index.SourcesReviewsAndLocal)
	evEvent(h, index.PhaseFetchBytes, index.SourceReviews, func(e *index.RefreshEvent) { e.Bytes = 2048 })
	h.press("x")

	check := func() {
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
			h.resize(size[0], size[1])

			for _, r := range ansi.Strip(h.m.View()) {
				require.Less(t, r, rune(0x80), "non-ASCII rune %q", r)
			}
		}
	}

	check()

	h.press("esc")
	h.send(evDoneMsg{id: 1, out: learning.Outcome{
		Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
		Reviews: evOutcome(index.SourceStopped, index.CauseStopped, "stopped"),
	}})

	check()
}

func TestEvidenceRunShowsAPanicAsAFailure(t *testing.T) {
	f := evFake()
	h, les := evOpen(t, f, 120, 40)

	f.runEvidence = func(context.Context, *learning.Plan, func(index.RefreshEvent)) (learning.Outcome, error) {
		panic("kaboom \x1b[31mred\x1b[0m")
	}

	evPress(h, les, evRefreshReviews)
	h.press("y")

	assert.False(t, h.m.st.busy(), "the panic frees the mutation slot")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "! failed")
	assert.Contains(t, evNotice(frame), "! Evidence refresh failed")
	assert.NotContains(t, h.m.View(), "\x1b[31m", "the error text stays inert")

	text := evRecordText(les.evid.run)
	assert.Contains(t, text, "! Evidence refresh failed")
	assert.Contains(t, text, "Details: the evidence refresh stopped on an internal error: kaboom [31mred[0m")
	assert.NotContains(t, text, "did not start")
}

func TestEvidenceRunSourcesShowTheReportedState(t *testing.T) {
	cases := []struct {
		name  string
		state index.SourceState
		want  []string
	}{
		{name: "unavailable", state: index.SourceUnavailable, want: []string{
			"! Local fixes unavailable", "the 24 stored local fix findings kept", "Local fixes: unavailable",
			"· Mine local fixes skipped",
		}},
		{name: "failed", state: index.SourceFailed, want: []string{
			"! Local fixes failed", "the 24 stored local fix findings kept", "Local fixes: failed",
			"! Mine local fixes failed",
		}},
		{name: "empty", state: index.SourceEmpty, want: []string{
			"✓ Local fixes empty", "no fix commits · stored fix findings replaced", "Local fixes: empty",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evFake()
			h, les := evOpen(t, f, 120, 40)

			r := evStarted(t, h, les, index.SourcesReviewsAndLocal)
			evEvent(h, index.PhaseMineFixes, index.SourceFixes, nil)
			evEvent(h, index.PhaseSourceDone, index.SourceFixes, func(e *index.RefreshEvent) { e.State = tc.state })
			evEvent(h, index.PhaseFetchReviews, index.SourceReviews, nil)
			require.True(t, r.active())

			text := evRecordText(r)
			for _, want := range tc.want {
				assert.Contains(t, text, want)
			}

			for _, avoid := range []string{"ended", "the result shows its state", "Local fixes saved"} {
				assert.NotContains(t, text, avoid, "the pane never defers a state that it knows")
			}

			assert.Contains(t, text, "GitHub reviews running", "a source without a reported end still runs")
			assertFrame(t, h.view(), 120, 40)
		})
	}
}

func TestEvidenceResultFooterKeepsAKeyAtEverySize(t *testing.T) {
	refreshed := learning.Outcome{
		Fixes:   index.SourceOutcome{State: index.SourceRefreshed, Stored: 24},
		Reviews: evOutcome(index.SourceFailed, index.CauseAuth, evBadToken),
		After:   evSnap(),
	}
	stale := fmt.Errorf("%w: HEAD moved", learning.ErrConflict)
	refused := evOutcome(index.SourceNotAttempted, index.CauseRefused, "HEAD moved")
	conflict := learning.Outcome{Fixes: refused, Reviews: refused, Conflict: stale, After: evSnap()}

	cases := []struct {
		name string
		out  learning.Outcome
		err  error
		want map[int]string
	}{
		{name: "refreshed", out: refreshed, want: map[int]string{
			120: "enter prepare distillation   n plan again", 80: "enter distill   n plan again", 60: "enter distill",
		}},
		{name: "conflict", out: conflict, err: stale, want: map[int]string{
			120: "n plan again", 80: "n plan again", 60: "n plan again",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evFake()
			h, les := evOpen(t, f, 120, 40)

			evStarted(t, h, les, index.SourcesReviewsAndLocal)
			h.send(evDoneMsg{id: 1, out: tc.out, err: tc.err})

			for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
				h.resize(size[0], size[1])

				frame := h.view()
				assertFrame(t, frame, size[0], size[1])
				assert.Contains(t, footerLine(frame), tc.want[size[0]], "size %dx%d", size[0], size[1])
			}
		})
	}
}
