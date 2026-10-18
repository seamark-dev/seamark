package learning

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// extEventLog records the events of one run.
type extEventLog struct {
	events []distill.ExtractEvent
}

func (l *extEventLog) record(ev distill.ExtractEvent) { l.events = append(l.events, ev) }

func (l *extEventLog) kinds() []distill.ExtractEventKind {
	out := make([]distill.ExtractEventKind, len(l.events))
	for i, ev := range l.events {
		out[i] = ev.Kind
	}

	return out
}

// extTwoBatches seeds ten pending rows and one linked applied row. The
// CLI order sends the pending rows in the first batch and the applied row
// alone in the second batch.
func extTwoBatches(t *testing.T, ws workspace.Workspace) (pending []model.Proposal, applied model.Proposal) {
	t.Helper()

	var ps []model.Proposal
	for i := range 10 {
		ps = append(ps, extPending(fmt.Sprintf("pend-%02d", i), "api", 1))
	}

	pending = extSeed(t, ws, ps...)
	applied = extSeed(t, ws, extApplied("installed", "web", 2))[0]
	extPins(t, ws, extPin("installed", "web", "Note for installed."))

	return pending, applied
}

// extInsert stores one row while a run is in progress.
func extInsert(t *testing.T, ws workspace.Workspace, p model.Proposal) {
	t.Helper()

	distSeed(t, ws, p)
}

// extDismiss dismisses pending rows while a run is in progress.
func extDismiss(t *testing.T, ws workspace.Workspace, ids ...int64) {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	n, err := st.SetProposalStatus(ids, model.ProposalDismissed)
	require.NoError(t, err)
	require.Equal(t, len(ids), n)
}

// extSupersede supersedes applied rows while a run is in progress. It
// writes through the guarded writer of a prune, as another seamark
// command does.
func extSupersede(t *testing.T, ws workspace.Workspace, ids ...int64) {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	rows, err := st.ProposalsWithIDs(ids)
	require.NoError(t, err)
	require.Len(t, rows, len(ids))

	guards := make([]store.RowGuard, len(rows))
	for i, p := range rows {
		guards[i] = store.NewRowGuard(p)
	}

	require.NoError(t, st.SupersedeExact(guards))
}

func TestRunExtractStoresTheAnswersOfThePreview(t *testing.T) {
	ws, marker := extWorkspace(t)

	// The write gate is off. Extraction never writes a pin, so it does
	// not read the gate.
	distWrite(t, ws, ".seamark/config.yaml", extMarkerArgv(marker)+"distill:\n  write: false\n")

	rows := extSeed(t, ws, extPending("pend", "api", 1), extApplied("installed", "web", 2))
	extPins(t, ws, extPin("installed", "web", "Note for installed."))

	lessonsBefore := distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml"))

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Empty(t, p.Blockers)

	agent := &extAgent{fn: func(_ context.Context, _ int, ids []int64) (string, error) {
		return extReply(map[int64][]string{
			rows[0].ID: {"api/handlers.go"},
			rows[1].ID: {"web/schema.ts", "web/ghost.ts"},
		}, ids), nil
	}}

	log := &extEventLog{}

	x, err := RunExtract(context.Background(), ws, integration.Builtin(), p, agent, log.record)
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.Equal(t, distill.StopNone, x.Stop)
	assert.Equal(t, []string{p.Plan.Batches[0].Prompt}, agent.sent(), "the run sends exactly the reviewed prompt")
	assert.Equal(t, []distill.ExtractEventKind{distill.ExtractBatchStarted, distill.ExtractBatchDone}, log.kinds())
	assert.Equal(t, extIDs(rows...), x.Persisted)
	assert.Empty(t, x.Failed)
	assert.Empty(t, x.Conflicts)
	assert.Empty(t, x.Unattempted)

	pend, inst := x.Batches[0].Items[0], x.Batches[0].Items[1]

	assert.Equal(t, distill.ExtractStored, pend.Status)
	assert.Equal(t, []string{"api/handlers.go"}, pend.Valid)
	assert.True(t, pend.RegionsChanged, "a pending row takes the confirmed trigger scope")

	assert.Equal(t, distill.ExtractStored, inst.Status)
	assert.Equal(t, []string{"web/schema.ts", "web/ghost.ts"}, inst.Named)
	assert.Equal(t, []string{"web/schema.ts"}, inst.Valid)
	assert.False(t, inst.RegionsChanged, "an installed scope changes only through a retarget")

	row := extRow(t, ws, rows[0].ID)
	assert.Equal(t, []string{"api/handlers.go"}, row.TriggerPaths)
	assert.Equal(t, pend.RegionsAfter, row.RegionSet())
	assert.Equal(t, distill.TriggerPromptVersion, row.TriggerPromptVersion)
	assert.NotZero(t, row.TriggerChecked)

	row = extRow(t, ws, rows[1].ID)
	assert.Equal(t, []string{"web/schema.ts"}, row.TriggerPaths)
	assert.Equal(t, "web", row.Region)
	assert.Empty(t, row.Regions)
	assert.Equal(t, model.ProposalApplied, row.Status)

	assert.Equal(t, lessonsBefore, distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml")),
		"extraction never edits a pin")
	assert.NoFileExists(t, marker, "the fake invoker replaces the configured command")
}

func TestRunExtractRejectsChangedInputs(t *testing.T) {
	type fixture struct {
		ws      workspace.Workspace
		p       *ExtractPreview
		pending model.Proposal
		applied model.Proposal
	}

	cases := []struct {
		name string
		// change edits the workspace or the preview after the plan and
		// returns the workspace to run against and the expected reason.
		change func(t *testing.T, f *fixture) (workspace.Workspace, string)
	}{
		{"row status", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			extDismiss(t, f.ws, f.pending.ID)

			return f.ws, fmt.Sprintf("p%d is dismissed now", f.pending.ID)
		}},
		{"living evidence", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			st, err := store.Open(f.ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, st.ReplaceLessons(nil, extFindings[1:]))
			require.NoError(t, st.Close())

			return f.ws, fmt.Sprintf("the living evidence of p%d changed", f.pending.ID)
		}},
		{"pins", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			extPins(t, f.ws, extPin("installed", "web", "Note for installed."), extPin("new", "api", "A new pin."))

			return f.ws, ".seamark/lessons.yaml changed since the preview"
		}},
		{"invocation settings", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			distWrite(t, f.ws, ".seamark/config.yaml", "agent:\n  cli: codex\n")

			return f.ws, ".seamark/config.yaml changed since the preview"
		}},
		{"resolved command", func(_ *testing.T, f *fixture) (workspace.Workspace, string) {
			// The same bytes can resolve to another command, for example
			// through another registry. The run compares.
			f.p.Invocation.Argv = []string{"sh", "-c", "something else"}

			return f.ws, "the agent command changed since the preview"
		}},
		{"workspace database", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			ws := f.ws
			ws.DBPath = filepath.Join(t.TempDir(), "other.db")

			return ws, "the workspace database changed"
		}},
		{"working directory", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			extSetGetwd(t, func() (string, error) { return "/somewhere/else", nil })

			return f.ws, "the working directory of the agent changed since the preview"
		}},
		{"pin link", func(t *testing.T, f *fixture) (workspace.Workspace, string) {
			// A second applied row with the same pin makes the link
			// ambiguous, although neither file changes.
			twin := extApplied("installed", "web", 2)
			twin.Signature = "sig-twin"
			extInsert(t, f.ws, twin)

			return f.ws, fmt.Sprintf("p%d is excluded now: ambiguous: several applied proposals", f.applied.ID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := extWorkspace(t)
			extSetGetwd(t, func() (string, error) { return "/console/dir", nil })

			rows := extSeed(t, ws, extPending("pend", "api", 1), extApplied("installed", "web", 2))
			extPins(t, ws, extPin("installed", "web", "Note for installed."))

			f := &fixture{ws: ws, pending: rows[0], applied: rows[1]}
			f.p = extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
			require.Len(t, f.p.Plan.Batches, 1)

			runWS, reason := tc.change(t, f)
			agent := &extAgent{}

			x, err := RunExtract(context.Background(), runWS, nil, f.p, agent, nil)
			require.ErrorIs(t, err, distill.ErrStale)
			require.ErrorIs(t, err, distill.ErrExtractStale)
			assert.Contains(t, err.Error(), reason)
			assert.Contains(t, err.Error(), "trigger extraction inputs changed", "the message names extraction")
			assert.Nil(t, x, "a refused run returns no execution")
			assert.Empty(t, agent.sent(), "the run rejects changed intent before any call")
			assert.NoFileExists(t, marker)
			assert.False(t, extStamped(t, ws, f.pending.ID))
			assert.False(t, extStamped(t, ws, f.applied.ID))
		})
	}
}

func TestRunExtractStopsOnAConflictBetweenBatches(t *testing.T) {
	cases := []struct {
		name string
		// during edits the workspace in the first agent call and returns
		// the expected stop reason.
		during func(t *testing.T, ws workspace.Workspace, applied model.Proposal) string
	}{
		{"pin file edited", func(t *testing.T, ws workspace.Workspace, _ model.Proposal) string {
			extPins(t, ws, extPin("installed", "web", "Note for installed."), extPin("new", "api", "A new pin."))

			return ".seamark/lessons.yaml changed since the preview"
		}},
		{"config edited", func(t *testing.T, ws workspace.Workspace, _ model.Proposal) string {
			distWrite(t, ws, ".seamark/config.yaml", "agent:\n  cli: codex\n")

			return ".seamark/config.yaml changed since the preview"
		}},
		{"another applied row makes the pin ambiguous", func(t *testing.T, ws workspace.Workspace,
			applied model.Proposal,
		) string {
			twin := extApplied("installed", "web", 2)
			twin.Signature = "sig-twin"
			extInsert(t, ws, twin)

			return fmt.Sprintf("p%d is excluded now: ambiguous: several applied proposals", applied.ID)
		}},
		{"the next row is superseded", func(t *testing.T, ws workspace.Workspace, applied model.Proposal) string {
			extSupersede(t, ws, applied.ID)

			// The row check names the change; the link check leaves a
			// changed row to it instead of saying "pin not present".
			return fmt.Sprintf("p%d is superseded now", applied.ID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, _ := extWorkspace(t)
			pending, applied := extTwoBatches(t, ws)

			p := extPlan(t, ws, ExtractRequest{IDs: append(extIDs(pending...), applied.ID)})
			require.Len(t, p.Plan.Batches, 2)
			require.Equal(t, []int64{applied.ID}, extIDs(p.Plan.Batches[1].Proposals...))

			var reason string

			agent := &extAgent{fn: func(_ context.Context, call int, ids []int64) (string, error) {
				if call == 1 {
					reason = tc.during(t, ws, applied)
				}

				return extNegative(ids), nil
			}}

			log := &extEventLog{}

			x, err := RunExtract(context.Background(), ws, nil, p, agent, log.record)
			require.NoError(t, err, "a conflict is a typed stop, not an error")

			assert.Len(t, agent.sent(), 1, "no call starts after the conflict")
			assert.Equal(t, distill.StopConflict, x.Stop)
			require.ErrorIs(t, x.StopErr, distill.ErrStale)
			assert.Contains(t, x.StopErr.Error(), reason)
			assert.ElementsMatch(t, extIDs(pending...), x.Persisted, "the committed batch stays")
			assert.Equal(t, []int64{applied.ID}, x.Unattempted)
			assert.Equal(t, distill.BatchUnattempted, x.Batches[1].Status)
			assert.Zero(t, x.Batches[1].PromptChars, "an unattempted batch meters nothing")
			assert.False(t, extStamped(t, ws, applied.ID))

			last := log.events[len(log.events)-1]
			assert.Equal(t, distill.ExtractStopped, last.Kind)
			assert.Equal(t, 1, last.Index)
		})
	}
}

// An answer that this run stored can give its own pending row the key of
// a live pin. That row is done, so the next batch must not stop on it.
func TestRunExtractOwnAnswerIsNotAConflict(t *testing.T) {
	ws, _ := extWorkspace(t)

	var ps []model.Proposal
	for i := range 11 {
		ps = append(ps, extPending(fmt.Sprintf("pend-%02d", i), "api", 1))
	}

	rows := extSeed(t, ws, ps...)
	newest := rows[len(rows)-1]

	// A hand-written pin already holds the scope that the answer gives
	// the newest row. Before the answer the keys differ.
	extPins(t, ws, extPin(newest.Rule, "api/handlers.go", "A hand-written pin."))

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Len(t, p.Plan.Batches, 2)
	require.Equal(t, newest.ID, p.Plan.Batches[0].Proposals[0].ID, "the newest row is in the first batch")

	agent := &extAgent{fn: func(_ context.Context, _ int, ids []int64) (string, error) {
		return extReply(map[int64][]string{newest.ID: {"api/handlers.go"}}, ids), nil
	}}

	x, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
	require.NoError(t, err)

	require.Equal(t, []string{"api/handlers.go"}, x.Batches[0].Items[0].RegionsAfter,
		"the answer moves the row onto the pin's key")
	assert.Equal(t, distill.StopNone, x.Stop, "the stored answer is not a conflict")
	assert.Len(t, agent.sent(), 2)
	assert.Len(t, x.Persisted, 11)
}

// A link that changes for a row of a finished batch does not stop the
// run: that row is done, and before each batch only that batch is
// checked, as the plan's row checks do.
func TestRunExtractChecksTheLinksOfTheNextBatchOnly(t *testing.T) {
	ws, _ := extWorkspace(t)

	var (
		ps   []model.Proposal
		pins []string
	)

	for i := range 11 {
		rule := fmt.Sprintf("inst-%02d", i)
		ps = append(ps, extApplied(rule, "web", 2))
		pins = append(pins, extPin(rule, "web", "Note for "+rule+"."))
	}

	rows := extSeed(t, ws, ps...)
	extPins(t, ws, pins...)

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Len(t, p.Plan.Batches, 2)

	first := p.Plan.Batches[0].Proposals[0]

	agent := &extAgent{fn: func(_ context.Context, call int, ids []int64) (string, error) {
		if call == 1 {
			twin := extApplied(first.Rule, "web", 2)
			twin.Signature = "sig-twin"
			extInsert(t, ws, twin)
		}

		return extNegative(ids), nil
	}}

	x, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
	require.NoError(t, err)

	assert.Equal(t, distill.StopNone, x.Stop, "the answered row's new ambiguity stops nothing")
	assert.Len(t, agent.sent(), 2)
	assert.Len(t, x.Persisted, 11)
}

func TestRunExtractStops(t *testing.T) {
	t.Run("before the next batch", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		pending, applied := extTwoBatches(t, ws)
		p := extPlan(t, ws, ExtractRequest{IDs: append(extIDs(pending...), applied.ID)})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent := &extAgent{}

		x, err := RunExtract(ctx, ws, nil, p, agent, func(ev distill.ExtractEvent) {
			if ev.Kind == distill.ExtractBatchDone {
				cancel()
			}
		})
		require.NoError(t, err, "a stop is data, not an error")

		assert.Len(t, agent.sent(), 1, "no invocation starts after the stop")
		assert.Equal(t, distill.StopCancelled, x.Stop)
		require.ErrorIs(t, x.StopErr, context.Canceled)
		assert.Equal(t, distill.BatchCompleted, x.Batches[0].Status)
		assert.Equal(t, distill.BatchUnattempted, x.Batches[1].Status)
		assert.Equal(t, []int64{applied.ID}, x.Unattempted)
		assert.False(t, extStamped(t, ws, applied.ID))
	})

	t.Run("during a call", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		pending, applied := extTwoBatches(t, ws)
		p := extPlan(t, ws, ExtractRequest{IDs: append(extIDs(pending...), applied.ID)})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent := &extAgent{fn: func(callCtx context.Context, _ int, _ []int64) (string, error) {
			cancel()
			<-callCtx.Done()

			return "", fmt.Errorf("agent fake: %w", callCtx.Err())
		}}

		x, err := RunExtract(ctx, ws, nil, p, agent, nil)
		require.NoError(t, err)

		assert.Len(t, agent.sent(), 1)
		assert.Equal(t, distill.StopCancelled, x.Stop)
		assert.Equal(t, distill.BatchCancelled, x.Batches[0].Status, "a stopped call is cancelled, never failed")
		assert.Equal(t, distill.BatchUnattempted, x.Batches[1].Status)
		assert.ElementsMatch(t, extIDs(pending...), x.Cancelled)
		assert.Empty(t, x.Persisted)

		for _, row := range pending {
			assert.False(t, extStamped(t, ws, row.ID), "a cancelled item is not stamped")
		}
	})

	t.Run("late reply", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		pending, applied := extTwoBatches(t, ws)
		p := extPlan(t, ws, ExtractRequest{IDs: append(extIDs(pending...), applied.ID)})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent := &extAgent{fn: func(_ context.Context, _ int, ids []int64) (string, error) {
			cancel()

			return extNegative(ids), nil
		}}

		x, err := RunExtract(ctx, ws, nil, p, agent, nil)
		require.NoError(t, err)

		assert.Equal(t, distill.BatchCompleted, x.Batches[0].Status, "the reply passed the commit boundary")
		assert.ElementsMatch(t, extIDs(pending...), x.Persisted)
		assert.Equal(t, distill.StopCancelled, x.Stop)
		assert.Equal(t, []int64{applied.ID}, x.Unattempted)
	})
}

func TestRunExtractRunsThePreviewedCommand(t *testing.T) {
	// extWorkspace configures extMarkerArgv, which prints the literal
	// {"triggers":[]} that the prompt asks for when no item qualifies.
	ws, marker := extWorkspace(t)
	rows := extSeed(t, ws, extPending("open", "api", 1), extPending("other", "web", 2))

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})

	// No fake: the run builds the invoker from the previewed command.
	x, err := RunExtract(context.Background(), ws, nil, p, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.FileExists(t, marker, "the previewed command ran")
	assert.Equal(t, distill.StopNone, x.Stop)
	assert.ElementsMatch(t, extIDs(rows...), x.Persisted)
	assert.Empty(t, x.Failed)

	for _, item := range x.Batches[0].Items {
		assert.Equal(t, distill.ExtractNegative, item.Status, "p%d is a negative answer", item.Proposal.ID)
		assert.True(t, item.Omitted, "p%d shows that the reply left it out", item.Proposal.ID)
		require.NoError(t, item.Err)
		assert.True(t, extStamped(t, ws, item.Proposal.ID), "a negative answer is a completed question")
	}
}

func TestRunExtractRefusesAMissingPreview(t *testing.T) {
	ws, _ := extWorkspace(t)
	agent := &extAgent{}

	for _, p := range []*ExtractPreview{nil, {}} {
		x, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
		require.Error(t, err)
		assert.Nil(t, x)
	}

	assert.Empty(t, agent.sent())
}
