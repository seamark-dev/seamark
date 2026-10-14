package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests run the production engine over a real indexed
// workspace. Only the two paid or network boundaries are fakes: the
// GitHub fetch and the agent invoker.

// engNew builds the engine over ws with the test clock.
func engNew(ws workspace.Workspace, deps Deps) *engine {
	if deps.Now == nil {
		deps.Now = func() time.Time { return servicestest.Now }
	}

	return newEngine(ws, "test", "/usr/local/bin/seamark", deps)
}

// engUnlocked checks that no mutation holds the engine lock.
func engUnlocked(t *testing.T, e *engine) {
	t.Helper()

	require.True(t, e.mu.TryLock(), "the engine lock is free again")
	e.mu.Unlock()
}

func TestEngineReadsNeverWriteFetchOrInvoke(t *testing.T) {
	ws, marker := servicestest.Workspace(t, servicestest.Origin, "api", "web")
	fetch := &servicestest.Fetch{Raw: servicestest.Page}
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Fetch: fetch.Fetch, Invoke: agent})

	before := servicestest.Tree(t, ws.Root)

	snap := e.Learning()
	require.NoError(t, snap.CountsErr)
	assert.Equal(t, workspace.IndexReady, snap.Probe.State)
	assert.Equal(t, 4, snap.ReviewFindings)
	assert.Equal(t, "o/r", snap.Repository.Slug())

	set, err := e.Settings()
	require.NoError(t, err)
	require.NoError(t, set.Blocked)

	for _, mode := range []index.SourceMode{index.SourcesLocal, index.SourcesReviewsAndLocal} {
		plan, err := e.PlanEvidence(mode)
		require.NoError(t, err)
		assert.True(t, plan.Ready(), "mode %s", mode)
	}

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	assert.Len(t, preview.Plan.Groups, 2)

	plan, err := e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)
	assert.False(t, plan.NoOp)

	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "reads and plans write nothing")
	assert.Empty(t, fetch.Called(), "reads and plans never fetch")
	assert.Zero(t, agent.Count(), "reads and plans never invoke the agent")
	assert.NoFileExists(t, marker, "no configured command runs")
	engUnlocked(t, e)
}

func TestEngineRunsTheReviewedEvidencePlan(t *testing.T) {
	t.Run("reviews and local fixes", func(t *testing.T) {
		ws, _ := servicestest.Workspace(t, servicestest.Origin)
		fetch := &servicestest.Fetch{Raw: servicestest.Page}
		e := engNew(ws, Deps{Fetch: fetch.Fetch})

		plan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
		require.NoError(t, err)
		require.True(t, plan.Network)

		var events []index.RefreshEvent

		out, err := e.RunEvidence(context.Background(), plan, func(ev index.RefreshEvent) { events = append(events, ev) })
		require.NoError(t, err)

		assert.Equal(t, []string{"o/r"}, fetch.Called(), "the fake fetch replaces gh")
		assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
		assert.Equal(t, index.SourceRefreshed, out.Reviews.State)
		assert.Equal(t, 1, out.After.Lessons, "the fetched comment is stored as one lesson")
		assert.NotEmpty(t, events, "the progress sink receives typed events")
		engUnlocked(t, e)
	})

	t.Run("local fixes only", func(t *testing.T) {
		ws, _ := servicestest.Workspace(t, servicestest.Origin)
		fetch := &servicestest.Fetch{Raw: servicestest.Page}
		e := engNew(ws, Deps{Fetch: fetch.Fetch})

		plan, err := e.PlanEvidence(index.SourcesLocal)
		require.NoError(t, err)
		assert.False(t, plan.Network)

		out, err := e.RunEvidence(context.Background(), plan, nil)
		require.NoError(t, err)

		assert.Empty(t, fetch.Called(), "a local refresh never fetches")
		assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
		assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)
		engUnlocked(t, e)
	})
}

func TestEngineRunEvidenceRefusesWithoutWork(t *testing.T) {
	ws, _ := servicestest.Workspace(t, servicestest.Origin)
	fetch := &servicestest.Fetch{Raw: servicestest.Page}
	e := engNew(ws, Deps{Fetch: fetch.Fetch})

	plan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	before := servicestest.Tree(t, ws.Root)

	// Another mutation holds the lock.
	e.mu.Lock()
	out, err := e.RunEvidence(context.Background(), plan, nil)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
	assert.Equal(t, index.CauseRefused, out.Reviews.Cause)
	require.ErrorIs(t, out.After.CountsErr, ErrBusy, "a refused run reads unknown counts, not zero counts")
	assert.Equal(t, servicestest.Now, out.FinishedAt)

	// A plan of another workspace never runs here.
	other, _ := servicestest.Workspace(t, servicestest.Origin)
	foreign, err := engNew(other, Deps{}).PlanEvidence(index.SourcesLocal)
	require.NoError(t, err)

	out, err = e.RunEvidence(context.Background(), foreign, nil)
	require.ErrorIs(t, err, errForeignPlan)
	assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
	engUnlocked(t, e)

	_, err = e.RunEvidence(context.Background(), nil, nil)
	require.Error(t, err, "a run needs a plan")
	engUnlocked(t, e)

	assert.Empty(t, fetch.Called())
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a refused run writes nothing")
}

func TestEngineRunsTheReviewedDistillation(t *testing.T) {
	ws, marker := servicestest.Workspace(t, "", "api", "web")
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.Len(t, preview.Plan.Groups, 2)

	var events []distill.Event

	x, err := e.RunDistill(context.Background(), preview, func(ev distill.Event) { events = append(events, ev) })
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.Equal(t, distill.StopNone, x.Stop)
	assert.Equal(t, 2, agent.Count(), "the fake invoker replaces the configured command")
	assert.NoFileExists(t, marker, "the configured command never runs")
	assert.NotEmpty(t, events)

	for _, g := range x.Groups {
		assert.Equal(t, distill.GroupCompleted, g.Status)
		assert.Len(t, g.Proposals, 1)
	}

	ledger, err := e.Ledger()
	require.NoError(t, err)
	assert.Len(t, ledger.Pending, 2, "each group stores one pending proposal")
	engUnlocked(t, e)
}

func TestEngineRunDistillWithoutAFakeRunsThePreviewedCommand(t *testing.T) {
	ws, marker := servicestest.Workspace(t, "", "api")
	e := engNew(ws, Deps{})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.Len(t, preview.Plan.Groups, 1)

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.FileExists(t, marker, "a nil invoker runs the command of the preview")
	assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status)
	engUnlocked(t, e)
}

// TestEngineRefusesEveryMutationDuringADistillation proves that the
// distillation holds the engine lock for its whole run. Every other
// mutation fails with ErrBusy and changes nothing, reads still work,
// and the lock is free again after the run.
func TestEngineRefusesEveryMutationDuringADistillation(t *testing.T) {
	ws, _ := servicestest.Workspace(t, servicestest.Origin, "api")
	fetch := &servicestest.Fetch{Raw: servicestest.Page}
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Fetch: fetch.Fetch, Invoke: agent})

	// A pin without a proposal gives the maintenance read an entry.
	servicestest.Write(t, ws, pinfile.File, "pin:\n  - rule: keep-docs-current\n    note: Keep the docs current.\n")

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)

	evPlan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	set, err := e.Settings()
	require.NoError(t, err)

	setPlan, err := e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)

	before := servicestest.Tree(t, ws.Root)

	var (
		errs      = map[string]error{}
		nested    *distill.Execution
		nestedExt *distill.ExtractExecution
		readErr   error
		readPlan  *settings.Plan
		readCount int
		pins      *maintain.Inspection
		maintPlan *maintain.Plan
		maintErr  error
		extPlan   *learning.ExtractPreview
		extErr    error
	)

	agent.Fn = func(_ context.Context, prompt string) (string, error) {
		nested, errs["RunDistill"] = e.RunDistill(context.Background(), preview, nil)
		_, errs["CleanupPending"] = e.CleanupPending(preview)
		_, errs["RunEvidence"] = e.RunEvidence(context.Background(), evPlan, nil)
		_, errs["SaveSettings"] = e.SaveSettings(setPlan)
		_, errs["BuildIndex"] = e.BuildIndex(false, func(IndexEvent) {})
		_, errs["CommitDecision"] = e.CommitDecision(&proposals.Preview{Action: proposals.ActionDismiss})
		_, errs["CommitMaintenance"] = e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionPrune})
		_, errs["CommitRecovery"] = e.CommitRecovery(&maintain.Plan{Action: maintain.ActionPrune})
		nestedExt, errs["RunExtract"] = e.RunExtract(context.Background(), &learning.ExtractPreview{}, nil)
		_, errs["CommitAuthor"] = e.CommitAuthor(&maintain.AuthorPlan{})

		// Reads take no lock, so the views stay responsive.
		readPlan, readErr = e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
		readCount = e.Learning().ReviewFindings
		pins = e.Pins()
		maintPlan, maintErr = e.PlanMaintenance(maintain.Request{
			Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{pins.Pins[0].Entry.Ref()}, Cursor: true,
		})
		extPlan, extErr = e.PlanExtract(learning.ExtractRequest{})

		return servicestest.Reply(prompt), nil
	}

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	for name, err := range errs {
		require.ErrorIs(t, err, ErrBusy, name)
	}

	assert.Len(t, errs, 10)
	assert.Nil(t, nested, "a refused run returns no execution")
	assert.Nil(t, nestedExt, "a refused extraction returns no execution")
	assert.Equal(t, 1, agent.Count(), "the refused runs start no agent call")
	assert.Empty(t, fetch.Called(), "the refused refresh never fetches")
	require.NoError(t, readErr)
	assert.NotNil(t, readPlan)
	assert.Equal(t, 2, readCount)

	require.Len(t, pins.Pins, 1, "the Pins read works during the run")
	require.NoError(t, maintErr)
	require.Len(t, maintPlan.Items, 1, "a maintenance plan works during the run")
	assert.Equal(t, maintain.ItemBlocked, maintPlan.Items[0].Status, "a pin without a proposal stays read-only")
	require.NoError(t, extErr)
	assert.True(t, extPlan.NoOp(), "an extraction plan works during the run")

	// The run changed only the database; the refused save changed no file.
	after := servicestest.Tree(t, ws.Root)
	delete(before, ws.DBPath)
	delete(after, ws.DBPath)
	assert.Equal(t, before, after)

	// The lock is free again, so the reviewed save now succeeds.
	res, err := e.SaveSettings(setPlan)
	require.NoError(t, err)
	assert.True(t, res.Written)
}

func TestEngineMutationsReleaseTheLockOnEveryPath(t *testing.T) {
	ws, _ := servicestest.Workspace(t, "", "api")
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)

	_, err = e.RunDistill(context.Background(), nil, nil)
	require.Error(t, err)
	engUnlocked(t, e)

	_, err = e.CleanupPending(preview)
	require.ErrorIs(t, err, learning.ErrNotCleanupOnly)
	engUnlocked(t, e)

	// A changed config.yaml makes the preview stale; the run refuses it.
	servicestest.Write(t, ws, settings.File, "agent:\n  cli: claude\n")

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.ErrorIs(t, err, distill.ErrStale)
	assert.Nil(t, x)
	assert.Zero(t, agent.Count())
	engUnlocked(t, e)

	_, err = e.SaveSettings(nil)
	require.Error(t, err)
	engUnlocked(t, e)

	// A lessons plan whose file changed is stale; the refused save frees
	// the lock too.
	snap, err := e.Settings()
	require.NoError(t, err)

	lessonsPlan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.Threshold, Int: 3}})
	require.NoError(t, err)
	require.Equal(t, pinfile.File, lessonsPlan.File)

	servicestest.Write(t, ws, pinfile.File, "threshold: 4\n")

	saved, err := e.SaveSettings(lessonsPlan)
	require.ErrorIs(t, err, settings.ErrStale)
	assert.Equal(t, settings.NotWritten, saved.Outcome)
	engUnlocked(t, e)

	// An authoring commit refuses a missing or an unconfirmable plan, and
	// frees the lock on that path too.
	authored, err := e.CommitAuthor(nil)
	require.ErrorIs(t, err, errNoPlan)
	assert.Equal(t, maintain.NotWritten, authored.Outcome)
	engUnlocked(t, e)

	authored, err = e.CommitAuthor(&maintain.AuthorPlan{})
	require.Error(t, err, "a plan that was never confirmable never commits")
	assert.Equal(t, maintain.NotWritten, authored.Outcome)
	engUnlocked(t, e)

	// Each maintenance and extraction mutation refuses a missing or an
	// unusable plan, and frees the lock on that path too.
	_, err = e.CommitMaintenance(nil)
	require.ErrorIs(t, err, errNoPlan)
	engUnlocked(t, e)

	_, err = e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionPrune})
	require.Error(t, err, "a plan that was never confirmable never commits")
	engUnlocked(t, e)

	_, err = e.CommitRecovery(nil)
	require.ErrorIs(t, err, errNoPlan)
	engUnlocked(t, e)

	_, err = e.CommitRecovery(&maintain.Plan{Action: maintain.ActionPrune})
	require.Error(t, err, "a plan without a recovery proof never commits")
	engUnlocked(t, e)

	ext, err := e.RunExtract(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Nil(t, ext)
	engUnlocked(t, e)

	noop, err := e.PlanExtract(learning.ExtractRequest{})
	require.NoError(t, err)

	ext, err = e.RunExtract(context.Background(), noop, nil)
	require.ErrorIs(t, err, learning.ErrNothingToExtract)
	assert.Nil(t, ext)
	assert.Zero(t, agent.Count())
	engUnlocked(t, e)

	e.mu.Lock()
	x, err = e.RunDistill(context.Background(), preview, nil)
	_, cleanErr := e.CleanupPending(preview)
	_, maintErr := e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionRetarget})
	_, recoverErr := e.CommitRecovery(&maintain.Plan{Action: maintain.ActionRetarget})
	ext, extErr := e.RunExtract(context.Background(), noop, nil)
	authored, authorErr := e.CommitAuthor(&maintain.AuthorPlan{})
	saved, saveErr := e.SaveSettings(lessonsPlan)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	require.ErrorIs(t, cleanErr, ErrBusy)
	require.ErrorIs(t, maintErr, ErrBusy)
	require.ErrorIs(t, recoverErr, ErrBusy)
	require.ErrorIs(t, extErr, ErrBusy)
	require.ErrorIs(t, authorErr, ErrBusy)
	require.ErrorIs(t, saveErr, ErrBusy)
	assert.Nil(t, x)
	assert.Nil(t, ext)
	assert.Equal(t, maintain.NotWritten, authored.Outcome)
	assert.Equal(t, snap.Lessons.Path, saved.Path, "a busy lessons save names lessons.yaml")
	engUnlocked(t, e)
}

func TestEngineCleansUpACleanupOnlyPreview(t *testing.T) {
	ws, _ := servicestest.Workspace(t, "")

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, storetest.InsertProposal(st, &domain.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: domain.ProposalProposed,
	}))
	require.NoError(t, st.Close())

	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.True(t, preview.CleanupOnly())

	removed, err := e.CleanupPending(preview)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Zero(t, agent.Count(), "a cleanup runs no agent")

	ledger, err := e.Ledger()
	require.NoError(t, err)
	assert.Empty(t, ledger.Pending)
	engUnlocked(t, e)
}

func TestEngineClockSeam(t *testing.T) {
	e := engNew(workspace.Workspace{Root: t.TempDir()}, Deps{})

	out := e.refusedOutcome(errors.New("refused"))
	assert.Equal(t, servicestest.Now, out.FinishedAt)

	prod := newEngine(workspace.Workspace{Root: t.TempDir()}, "test", "", Deps{})
	require.NotNil(t, prod.deps.Now, "a zero clock reads the system clock")
	assert.WithinDuration(t, time.Now(), prod.deps.Now(), time.Minute)
}
