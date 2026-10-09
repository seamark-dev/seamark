package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// The tests of the run and the result of the trigger extraction record:
// the result counts, the stops, the conflicts, the token usage, and the
// earlier record in the Run sub-view. They hold review findings. Each
// test drives the record through the harness and checks that the text
// says what the engine does.

// extResultOf opens the review of opts, starts the run with y, and
// returns the harness at the result. exec builds the execution.
func extResultOf(t *testing.T, opts extPreviewOpts, w, hh int,
	exec func(p *learning.ExtractPreview) *distill.ExtractExecution,
) *harness {
	t.Helper()

	var (
		seen []*learning.ExtractPreview
		ctxs []context.Context
	)

	p := extPreview(opts)
	f := extFake(p, nil)
	f.runExtract = extReturns(&seen, &ctxs, exec)

	h := extOpen(t, f, p, w, hh)
	h.press("y")
	require.Equal(t, stepResult, extRecOf(t, h).step)

	return h
}

func TestExtractCompactListsKeepTheirLabels(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
		p := extPreview(extMixed())
		h := extOpen(t, extFake(p, extNoRun(t)), p, size[0], size[1])

		// The review opens on its first row, under the labels.
		h.press("tab")

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "State", "%v: the column header shows", size)
		assert.Contains(t, frame, "BATCH 1", "%v: the batch heading shows", size)
		assert.Contains(t, frame, "› p23", "%v: the cursor row shows", size)

		// The result opens on p21, the first row of batch 2.
		r := extResultOf(t, extThreeBatches(), size[0], size[1], extStoppedExec)
		require.Equal(t, 2, extRecOf(t, r).run.cursor)
		r.press("tab")

		frame = r.view()
		assert.Contains(t, frame, "BATCH 2", "%v: the batch of the cursor row shows", size)
		assert.Contains(t, frame, "p21", "%v", size)
	}
}

func TestExtractLiveNoteIsAWordNotTheStateGlyph(t *testing.T) {
	legend := "note edited: the live note in lessons.yaml differs from the recorded note that the question sends"

	// The review table names the edited note beside the rule.
	p := extPreview(extMixed())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 80, 120)
	h.press("tab")

	review := lesBoxFlat(h.view())
	assert.Contains(t, review, "p4 applied wrap-sql-errors · note edited never asked")
	assert.Contains(t, review, legend)
	assert.NotContains(t, review, "! the live note differs", "no legend explains a bare glyph")

	// A pane at the minimum width drops the reason column, so the rule of
	// the edited row stays readable beside the word.
	h.resize(minWidth, 120)
	assert.Contains(t, lesBoxFlat(h.view()), "p4 applied wrap-sql-errors · note edited")
	h.resize(80, 120)

	// In the stopped result, only p4 has an edited note. The cancelled
	// rows keep their own state glyph and no note word.
	r := extResultOf(t, extThreeBatches(), 80, 120, extStoppedExec)
	flat := lesBoxFlat(r.view())

	assert.Contains(t, flat, "! p21 pending cancelled")
	assert.Contains(t, flat, "! p6 applied cancelled")
	assert.Contains(t, flat, "! p4 applied not attempted · note edited")
	assert.NotContains(t, flat, "! ! p4", "the state glyph is the only glyph of the row")
	assert.NotContains(t, flat, "cancelled · note edited")
	assert.Contains(t, flat, legend)
	assert.NotContains(t, flat, "review the advice against it", "a row without advice gets no advice note")

	// A narrow cell gives up its outcome words first, so the word stays.
	tm := newTheme(false)
	cut := extWithNote(tm, "negative: 2 paths named, none valid", 24)
	assert.True(t, strings.HasSuffix(cut, " · note edited"), cut)
	assert.LessOrEqual(t, width(cut), 24)
	assert.Equal(t, "note edited", extWithNote(tm, "stored: 2 paths", 12))
}

func TestExtractResultCountsNamedPathsAsTheModelWroteThem(t *testing.T) {
	cases := []struct {
		name     string
		raw      int
		clean    []string
		cell     string
		sentence string
	}{
		{
			name: "every entry fails the syntax check", raw: 2,
			cell: "p21 pending negative: 2 paths named, none valid", sentence: "no named entry is a valid repository path",
		},
		{
			name: "one clean entry that does not exist", raw: 3, clean: []string{"internal/api/gone.go"},
			cell: "p21 pending negative: 3 paths named, none valid", sentence: "no named entry is a valid repository path",
		},
		{
			name: "every entry is clean and missing", raw: 1, clean: []string{"internal/api/gone.go"},
			cell: "p21 pending negative: 1 path named, not found", sentence: "no named path exists in the working tree",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := extResultOf(t, extTwoBatches(), 80, 120, func(p *learning.ExtractPreview) *distill.ExtractExecution {
				x := extConflictExec(p)
				it := &x.Batches[0].Items[2]
				it.NamedRaw, it.Named = c.raw, c.clean

				return x
			})

			// p23, p21 and p4 named paths; p22 was omitted from the reply.
			flat := lesBoxFlat(h.view())
			assert.Contains(t, flat, "Paths named for 3 proposals, valid for 2, stored for 2")
			assert.Contains(t, flat, c.cell)

			// The detail of p21 counts the same paths.
			h.press("up", "tab")
			require.Equal(t, 2, extRecOf(t, h).run.cursor)

			detail := lesBoxFlat(h.view())
			assert.Contains(t, detail, "Answer "+itoa(c.raw)+" named · 0 valid")
			assert.Contains(t, detail, c.sentence)
		})
	}
}

func TestExtractResultCountsOnlyStartedCallsAsAsked(t *testing.T) {
	cases := []struct {
		name string
		// chars is the prompt traffic of the cancelled batch 2.
		chars int
		want  string
	}{
		{name: "stop during the call", chars: 2300, want: "4 of 5 asked"},
		{name: "stop before the call", chars: 0, want: "2 of 5 asked"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := extResultOf(t, extThreeBatches(), 120, 40, func(p *learning.ExtractPreview) *distill.ExtractExecution {
				x := extStoppedExec(p)
				x.Batches[1].PromptChars = c.chars

				return x
			})

			assert.Contains(t, h.view(), c.want)
		})
	}
}

func TestExtractStoreErrorInTheOnlyBatchLeavesNoBatchBehind(t *testing.T) {
	stored := errors.New("database is locked")

	h := extResultOf(t, extMixed(), 80, 120, func(p *learning.ExtractPreview) *distill.ExtractExecution {
		return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
			b := &bs[0]
			b.PromptChars, b.ReplyChars = 4400, 600

			extStored(&b.Items[0], "internal/index/schema.go")
			extNegative(&b.Items[1], false)

			// The store fails on the third item; the engine fails it and
			// the unsaved rest of the batch.
			for k := 2; k < len(b.Items); k++ {
				b.Items[k].Status, b.Items[k].Failure = distill.ExtractFailed, distill.FailStore
				b.Items[k].Err = fmt.Errorf("not saved: %w", stored)
			}

			b.Status, b.Failure, b.Err = distill.BatchFailed, distill.FailStore, stored
			x.Stop, x.StopErr = distill.StopStoreError, stored
		})
	})

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "Trigger extraction stopped on a store error")
	assert.Contains(t, flat, "A store read or write failed: database is locked. 2 answers are saved")
	assert.Contains(t, flat, "Batches 0 completed · 1 failed")
	assert.NotContains(t, flat, "remaining batches", "no batch was left out")
	assert.NotContains(t, flat, "not attempted")
}

func TestExtractStoreErrorNamesTheProposalsLeftOut(t *testing.T) {
	h := extResultOf(t, extThreeBatches(), 80, 120, extStoreErrorExec)

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "Trigger extraction stopped on a store error")
	assert.Contains(t, flat, "1 proposal was not attempted.")
	assert.Contains(t, flat, "Not attempted p4")
}

func TestExtractConflictBeforeTheLastBatchReportsTheStop(t *testing.T) {
	h := extResultOf(t, extThreeBatches(), 80, 120, extInputChangedExec)

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "Trigger extraction stopped: an input changed")
	assert.Contains(t, flat, "Stop an input changed", "batches were left out, so the stop is real")
	assert.Contains(t, flat, "3 proposals were not attempted.")
}

func TestExtractEarlierRecordShowsNoResultKeys(t *testing.T) {
	h := extResultOf(t, extTwoBatches(), 120, 40, extConflictExec)
	rec := extRecOf(t, h)

	// p4 is applied and stored trigger paths, so its result links Pins.
	// One tall compact box shows the summary, and tab shows the detail.
	h.press("down")
	h.resize(80, 120)

	summary := lesBoxFlat(rec.view(80, 117))
	require.Contains(t, summary, "NEXT enter show p4 in Lessons › Pins")
	require.Contains(t, summary, "prepare a new preview of the 1 open question (p6)")

	// The Run sub-view passes an earlier record only the movement keys,
	// so the earlier view offers neither enter nor n.
	earlier := lesBoxFlat(rec.readOnlyView(80, 117))
	assert.NotContains(t, earlier, "NEXT")
	assert.NotContains(t, earlier, "enter show")
	assert.NotContains(t, earlier, "prepare a new preview")
	assert.Contains(t, earlier, "Trigger extraction finished with a conflict", "the result itself stays")

	h.press("tab")

	detail := lesBoxFlat(rec.view(80, 117))
	require.Contains(t, detail, "enter shows p4 in Lessons › Pins. It opens no preview")
	assert.NotContains(t, lesBoxFlat(rec.readOnlyView(80, 117)), "enter shows")
	assert.Equal(t, detail, lesBoxFlat(rec.view(80, 117)), "the read-only flag lasts only for one render")
}

// The tests below hold the findings of the full review, the real-binary
// trial and the security lens of the trigger extraction record. Each
// test drives the record through the harness and checks that the screen
// says what the engine and the ledger hold.

// extRunTo opens the review of p, starts the run with y, and returns the
// harness at the result. exec builds the execution from the preview.
func extRunTo(t *testing.T, f *fakeServices, p *learning.ExtractPreview, w, hh int,
	exec func(p *learning.ExtractPreview) *distill.ExtractExecution,
) *harness {
	t.Helper()

	f.runExtract = func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		return exec(p), nil
	}

	h := extOpen(t, f, p, w, hh)
	h.press("y")
	require.Equal(t, stepResult, extRecOf(t, h).step)

	return h
}

// extDismissed returns the ledger row of proposal id after another
// writer dismissed it.
func extDismissed(id int64) domain.Proposal {
	return domain.Proposal{ID: id, Rule: "dismissed-elsewhere", Status: domain.ProposalDismissed}
}

// extAllConflictExec is a run of extTwoBatches whose first batch came
// back after another writer changed all three of its rows. The engine
// stops before batch 2, so p6 and p4 are not attempted.
func extAllConflictExec(p *learning.ExtractPreview) *distill.ExtractExecution {
	return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
		extAnswered(&bs[0])

		for i := range bs[0].Items {
			it := &bs[0].Items[i]
			it.Status = distill.ExtractConflict
			it.Err = fmt.Errorf("%w: p%d changed during the agent call; its answer was not saved",
				distill.ErrExtractStale, it.Proposal.ID)
		}

		x.Stop, x.StopErr = distill.StopConflict, bs[0].Items[0].Err
	})
}

// Trial e2e-extraction steps 36-39: p19 was dismissed during its agent
// call. The result kept "pending", promised an open question, and n
// offered a preview that had nothing to ask.
func TestExtractResultReadsTheLedgerForAConflictRow(t *testing.T) {
	p := extPreview(extTwoBatches())
	f := extFake(p, nil)

	// The end of the run reloads the ledger, which then holds p6 as
	// dismissed by another writer.
	f.ledger.Dismissed = append(f.ledger.Dismissed, extDismissed(6))

	h := extRunTo(t, f, p, 120, 40, extConflictExec)
	rec := extRecOf(t, h)

	assert.Empty(t, rec.remaining(), "a dismissed row has no open question")
	assert.NotContains(t, footerLine(h.view()), "n prepare remaining", "n offers no preview without a question")

	// The p6 row shows the current ledger state in the State column.
	var row string

	for line := range strings.SplitSeq(h.view(), "\n") {
		if strings.Contains(line, "p6") && strings.Contains(line, "conflict: not saved") {
			row = line
		}
	}

	require.NotEmpty(t, row, "the run table lists p6")
	assert.Contains(t, row, "dismissed")
	assert.NotContains(t, row, "applied")

	h.resize(80, 120)

	summary := lesBoxFlat(h.view())
	assert.Contains(t, summary, "Closed now p6 (dismissed)")
	assert.Contains(t, summary, "No question stays open.")
	assert.NotContains(t, summary, "prepare a new preview")

	// The result cursor opens on p6, the first row without an answer.
	h.press("tab")

	detail := lesBoxFlat(h.view())
	assert.Contains(t, detail, "● dismissed now · planned as applied")
	assert.Contains(t, detail, "conflict: p6 changed during the agent call; its answer was not saved. Its question "+
		"is closed now (dismissed), so a new preview does not ask it.")
	assert.NotContains(t, detail, "stays open")
}

func TestExtractConflictNoteLeavesTheDecisionToANewPreview(t *testing.T) {
	// p6 is not in the ledger snapshot, so its state is unknown, and a
	// new preview checks every rule again.
	h := extRunTo(t, extFake(extPreview(extTwoBatches()), nil), extPreview(extTwoBatches()), 80, 120,
		extConflictExec)

	h.press("tab")

	detail := lesBoxFlat(h.view())
	assert.Contains(t, detail, "conflict: p6 changed during the agent call; its answer was not saved. A new preview "+
		"decides whether it can be asked.")
	assert.NotContains(t, detail, "stays open")
	assert.Contains(t, detail, "● applied batch 2", "an unknown row keeps its planned state")
}

// Trial step 38: four rows of one batch conflicted, and the lead named
// only the first one.
func TestExtractConflictLeadNamesEveryConflictingProposal(t *testing.T) {
	var reqs []learning.ExtractRequest

	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.ledger.Dismissed = append(f.ledger.Dismissed, extDismissed(23), extDismissed(22), extDismissed(21))

	plan := f.planExtract
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		reqs = append(reqs, req)

		return plan(req)
	}

	h := extRunTo(t, f, p, 120, 40, extAllConflictExec)
	rec := extRecOf(t, h)

	assert.Equal(t, []int64{6, 4}, rec.remaining(), "only the rows that a new preview can ask")

	h.resize(80, 120)

	summary := lesBoxFlat(h.view())
	assert.Contains(t, summary, "3 proposals changed during the agent call (p23, p22, p21); their answers were "+
		"not saved. 0 answers are saved. 2 proposals were not attempted.")
	assert.Contains(t, summary, "Conflict p23, p22, p21: changed during the agent call; not saved")
	assert.Contains(t, summary, "Closed now p23, p22, p21 (dismissed)")
	assert.Contains(t, summary, "prepare a new preview of the 2 open questions (p6, p4); it starts only with y")

	h.press("n")
	require.Len(t, reqs, 2)
	assert.Equal(t, []int64{6, 4}, reqs[1].IDs, "n plans only the open questions")
}

func TestExtractOpenTextCountsTheQuestionsThatClosed(t *testing.T) {
	c := extCounts{failed: 2, closedFailed: 1, cancelled: 1}

	assert.Equal(t, " Of the other proposals, 2 failed and 1 was cancelled. 1 proposal is closed now, so a new "+
		"preview does not ask it. The other questions stay open, so a new preview can include them.",
		extOpenText(c, false))

	c = extCounts{conflict: 2, closedConflict: 2}
	assert.Equal(t, " Of the other proposals, 2 changed during the agent call. 2 proposals are closed now, so a "+
		"new preview does not ask them.", extOpenText(c, true))

	assert.Equal(t, " Its question stays open, so a new preview can include it.", extStaysText(1, 0))
}

// Trial steps 45 and 50: the foot said that lessons.yaml was unchanged
// while the lead of the same result named its change.
func TestExtractResultFootSaysWhatTheRunDid(t *testing.T) {
	h := extRunTo(t, extFake(extPreview(extThreeBatches()), nil), extPreview(extThreeBatches()), 80, 120,
		func(p *learning.ExtractPreview) *distill.ExtractExecution {
			return extExec(p, func(bs []distill.ExtractBatchOutcome, x *distill.ExtractExecution) {
				extAnswered(&bs[0])
				extStored(&bs[0].Items[0], "internal/index/schema.go")
				extNegative(&bs[0].Items[1], false)
				x.Stop = distill.StopConflict
				x.StopErr = fmt.Errorf("%w: .seamark/lessons.yaml changed since the preview", distill.ErrExtractStale)
			})
		})

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, ".seamark/lessons.yaml changed since the preview.")
	assert.Contains(t, flat, "This run changed no installed pin scope and did not write lessons.yaml.")
	assert.NotContains(t, flat, "lessons.yaml are unchanged")
}

// Trial step 17: a reply without a "triggers" key showed the engine
// words "the batch retries" on a screen that retries nothing.
func TestExtractNoTriggersKeyFailureSaysNothingRetries(t *testing.T) {
	h := extRunTo(t, extFake(extPreview(extThreeBatches()), nil), extPreview(extThreeBatches()), 80, 120,
		func(p *learning.ExtractPreview) *distill.ExtractExecution {
			return extExec(p, func(bs []distill.ExtractBatchOutcome, _ *distill.ExtractExecution) {
				for i := range bs {
					extFailBatch(&bs[i], distill.FailInvalidReply, distill.ErrNoTriggersKey)
				}
			})
		})

	summary := lesBoxFlat(h.view())

	h.press("tab")

	detail := lesBoxFlat(h.view())
	assert.Contains(t, detail, `failed: invalid reply: the reply has no "triggers" key, so it is not a negative `+
		"answer. The question stays open")

	for _, text := range []string{summary, detail} {
		assert.NotContains(t, text, "retries")
	}

	assert.Contains(t, summary, "Nothing is retried automatically")
}

// Trial step 11: the 120x40 table cut a long negative outcome and its
// region mark with it.
func TestExtractResultKeepsTheRegionMarkOfALongOutcome(t *testing.T) {
	h := extRunTo(t, extFake(extPreview(extTwoBatches()), nil), extPreview(extTwoBatches()), 120, 40,
		func(p *learning.ExtractPreview) *distill.ExtractExecution {
			return extExec(p, func(bs []distill.ExtractBatchOutcome, _ *distill.ExtractExecution) {
				for i := range bs {
					extAnswered(&bs[i])

					for j := range bs[i].Items {
						extNegative(&bs[i].Items[j], false)
					}
				}

				// p22 named two paths that fail the syntax check.
				bs[0].Items[1].NamedRaw = 2
				extChange(&bs[0].Items[1], "internal/apix", "internal/db")
			})
		})

	frame := h.view()

	var row string

	// The ID cell is six cells wide, so the state follows the ID.
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.Contains(line, "p22   pending") {
			row = line
		}
	}

	require.NotEmpty(t, row, "the run table lists p22")
	assert.Contains(t, row, "· new regions", "the mark keeps its room; the reason gives way")
	assert.Contains(t, row, "negative: 2 paths")
	assertFrame(t, frame, 120, 40)
}

// Trial steps 28 and 33: the summary said "no agent traffic yet" while
// the prompt of batch 1 was with the agent.
func TestExtractUsageCountsThePromptThatIsWithTheAgent(t *testing.T) {
	p := extPreview(extThreeBatches())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)
	rec := extStartHeld(t, h)

	assert.Equal(t, "no agent traffic yet", rec.usageText(), "nothing is sent before the first batch starts")

	extSend(h, rec, extStartedEv(rec, 0))

	first := agentTokens(p.Plan.Batches[0].PromptChars)
	assert.Equal(t, first+" tokens sent to batch 1 (estimated); reply pending", rec.usageText())
	assert.NotContains(t, h.view(), "no agent traffic yet")

	b := rec.run.units[0]
	extAnswered(&b)
	extStored(&b.Items[0], "internal/index/schema.go")
	extSend(h, rec, extDoneEv(rec, 0, b))
	extSend(h, rec, extStartedEv(rec, 1))

	sent := agentTokens(b.PromptChars + p.Plan.Batches[1].PromptChars)
	assert.Equal(t, sent+" tokens sent · "+agentTokens(b.ReplyChars)+" received (estimated); batch 2 reply pending",
		rec.usageText())
}

// Trial steps 43-44: in ASCII the separator "/" made two counts read as
// a fraction.
func TestExtractASCIICountsReadAsCounts(t *testing.T) {
	p := extPreview(extTwoBatches())
	f := extFake(p, nil)
	f.runExtract = func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
		*distill.ExtractExecution, error,
	) {
		return extCompletedExec(p), nil
	}

	h := &harness{t: t, m: newModel(f, newTheme(true))}
	h.run(h.m.Init())
	h.send(teaSize(120, 40))
	h.send(openExtractMsg{req: p.Request})

	review := extTallFlat(h)
	assert.Contains(t, review, "Sends 5 proposals in 2 batches, 4 ineligible")
	assert.NotContains(t, review, "batches / 4")

	// The batch headings are in the Proposals pane.
	h.resize(80, 120)
	h.press("tab")

	items := lesFlat(h.view())
	assert.Contains(t, items, "3 proposals, ~")
	assert.NotContains(t, items, "proposals / ~")

	h.press("tab")
	h.resize(120, 40)

	h.press("y")
	require.Equal(t, stepResult, extRecOf(t, h).step)

	result := extTallFlat(h)
	assert.Contains(t, result, "tokens sent, ~")
	assert.Contains(t, result, "3 proposals, completed")
	assert.NotContains(t, result, "sent / ~")
	assert.NotContains(t, result, "proposals / completed")
}

// The tests below cover items that join two parts of the console. So
// each test drives the real model through the harness, from the key
// that the user presses to the frame that the user sees.

// C0 item 2: an earlier extraction record, shown with [, offers no enter
// and no n. The Run sub-view passes an earlier record only the movement
// keys, so the frame must not show those keys either.
func TestEarlierExtractionRecordShowsNoResultKeysInTheRunView(t *testing.T) {
	h := extResultOf(t, extTwoBatches(), 120, 40, extConflictExec)
	rec := extRecOf(t, h)

	// p4 stored trigger paths, so its result row links Pins.
	h.press("down")
	h.resize(80, 120)
	require.Contains(t, lesBoxFlat(h.view()), "NEXT enter show p4 in Lessons › Pins")

	// A later record makes the extraction an earlier run.
	h.send(openRunMsg{rec: runRec(h, "Distillation"), show: true})
	require.Len(t, runViewOf(h).past, 1)
	require.Same(t, rec, runViewOf(h).past[0])

	h.press("[")
	require.True(t, runViewOf(h).browsing())

	frame := h.view()
	flat := lesBoxFlat(frame)
	assert.Contains(t, frame, "Earlier run 1 of 1")
	assert.Contains(t, flat, "Trigger extraction finished with a conflict", "the result itself stays")
	assert.NotContains(t, flat, "NEXT")
	assert.NotContains(t, flat, "enter show")
	assert.NotContains(t, flat, "prepare a new preview")
	assert.NotContains(t, footerLine(frame), "enter", "an extraction has no proposals to review")

	// The detail pane is a movement away, and it names no enter either.
	h.press("tab")
	assert.NotContains(t, lesBoxFlat(h.view()), "enter shows")

	// The read-only form lasts for the render only.
	assert.False(t, rec.browsed)
}
