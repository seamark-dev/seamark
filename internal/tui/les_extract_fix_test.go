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
)

// The tests in this file hold the review findings of the trigger
// extraction record (Phase 3 review). Each test drives the record
// through the harness and checks that the text says what the engine
// does.

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
	require.Equal(t, extResult, extRecOf(t, h).step)

	return h
}

func TestExtractReviewTellsHowAnAnswerChangesPendingRegions(t *testing.T) {
	p := extPreview(extMixed())
	h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

	// p22 is the second row; its coverage differs from its regions.
	h.press("down")
	h.resize(80, 120)
	h.press("tab")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "If answered a confirmed trigger replaces the regions with its own region; a negative "+
		"or unconfirmed answer sets internal/apix, internal/db")
	assert.NotContains(t, flat, "narrow")
}

func TestExtractReviewNamesTheCursorRow(t *testing.T) {
	p := extPreview(extPreviewOpts{rows: extRows()[4:]})
	h := newHarness(t, extFake(p, extNoRun(t)), 120, 40)

	// The sender of the request sets the flag when no row is marked.
	h.send(openExtractMsg{req: p.Request, cursor: true})

	rec := extRecOf(t, h)
	require.True(t, rec.cursor)
	require.Equal(t, extReview, rec.step)

	assert.Contains(t, h.view(), "selected 1 (cursor row)")
	assert.Contains(t, extTallFlat(h), "Selected 1 eligible of 1 · the cursor row; no row is marked")

	h.resize(80, 24)
	assert.Contains(t, h.view(), "1 selected (cursor row)", "the compact context line names it too")

	// A request of marked rows names no cursor row.
	h.resize(120, 40)
	h.send(openExtractMsg{req: p.Request})
	require.NotSame(t, rec, extRecOf(t, h))
	assert.NotContains(t, h.view(), "cursor row")
}

func TestExtractWorkDirBlockerOffersNoSettingsLink(t *testing.T) {
	// The engine builds this line from WorkDirErr (learning.workDirBlocker).
	const cwd = "the agent's working directory is unknown: getwd: no such file or directory"

	cases := []struct {
		name     string
		blockers []string
		link     bool
	}{
		{name: "working directory only", blockers: []string{cwd}},
		{name: "with an agent blocker", blockers: []string{cwd, `agent cli "claude" not found on PATH`}, link: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := extMixed()
			o.blockers = c.blockers
			o.workDirErr = errors.New("getwd: no such file or directory")

			p := extPreview(o)
			h := extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)

			flat := extTallFlat(h)
			assert.Contains(t, flat, cwd+". The agent runs in the directory of the console, so Settings cannot "+
				"change it. Quit, then start Seamark again from an existing directory.")
			assert.NotContains(t, flat, "directory. Install it", "the install advice belongs to the agent blocker")

			footer := shellAFooter(h.view())
			h.press("o")

			if !c.link {
				assert.NotContains(t, footer, "o open", "Settings cannot fix the working directory")
				assert.Equal(t, destLessons, h.m.active, "o opens nothing")
				assert.Equal(t, lesRun, hookLessons(h).sub)

				return
			}

			assert.Contains(t, flat, `agent cli "claude" not found on PATH. Install it, or press o`)
			assert.Contains(t, footer, "o open Settings › Inference")
			assert.Equal(t, destSettings, h.m.active)
		})
	}
}

func TestExtractNotReadyKeepsItsLastSentenceAtTheMinimumSize(t *testing.T) {
	f := extFake(extPreview(extMixed()), extNoRun(t))
	f.planExtract = func(learning.ExtractRequest) (*learning.ExtractPreview, error) {
		return nil, fmt.Errorf("%w: index database /work/repo/.seamark/index.db is absent: build the index first",
			learning.ErrExtractNotReady)
	}

	h := extOpen(t, f, extPreview(extMixed()), minWidth, minHeight)

	frame := h.view()
	assertFrame(t, frame, minWidth, minHeight)

	flat := lesBoxFlat(frame)
	assert.Contains(t, flat, "The index is not ready")
	assert.Contains(t, flat, "index database /work/repo/.seamark/index.db is absent: build the index first")
	assert.NotContains(t, flat, "not ready for trigger extraction", "the title already says it")
	assert.Contains(t, flat, "Press o to build it from Overview, then plan again. Nothing was sent or written.")
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

func TestExtractEmptySelectionListsNoItem(t *testing.T) {
	p := extPreview(extPreviewOpts{none: true})
	h := newHarness(t, extFake(p, extNoRun(t)), 80, 40)
	h.send(openExtractMsg{req: learning.ExtractRequest{}})

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "Nothing was selected, so nothing can be asked.")
	assert.NotContains(t, flat, "every selected proposal is asked", "an empty selection has no item to list")
	assert.Contains(t, flat, "Nothing was sent to the agent.")
}
