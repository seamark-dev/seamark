package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
)

// TestCaptureExtract writes the trigger extraction scenes (90 to 99) at
// every capture size into SEAMARK_TUI_CAPTURE_DIR, for the maintainer
// visual review. It checks that every frame fits its terminal. It also
// checks the raw frame for escape bytes, because a capture file strips
// them, and every ASCII scene for non-ASCII runes (D-24).
func TestCaptureExtract(t *testing.T) {
	dir := captureDir()
	snap := fakeSettingsSnapshot(t, "agent:\n  cli: claude\n")

	for _, sc := range extCaptureScenes() {
		for _, size := range captureSizes {
			w, h := size[0], size[1]

			f := sc.fake()
			f.settings = snap

			hs := &harness{t: t, m: newModel(f, newTheme(sc.ascii))}
			hs.run(hs.m.Init())
			hs.send(teaSize(w, h))
			sc.drive(hs)

			frame := hs.view()
			assertFrame(t, frame, w, h)
			extAssertInert(t, hs.m.View(), sc.name, w, h)

			if sc.ascii {
				for _, r := range frame {
					require.Less(t, r, rune(128), "scene %s at %dx%d holds %q", sc.name, w, h, r)
				}
			}

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}

// extAssertInert checks a raw frame for the bytes that start a terminal
// control sequence: ESC, BEL and the C1 CSI. The test color profile
// renders no style, so any such byte comes from the data.
func extAssertInert(t *testing.T, raw, scene string, w, h int) {
	t.Helper()

	for _, b := range []string{"\x1b", "\x07", "\u009b"} {
		assert.NotContains(t, raw, b, "scene %s at %dx%d holds a control byte %q", scene, w, h, b)
	}
}

// extCaptureScenes lists the extraction scenes. They show the review
// with its failures and its empty case, the run, the stop and every
// result kind. They also show the superseded history of Proposals, t
// from an applied row, and ASCII variants. A compact layout shows one
// pane, so a scene of a detail opens that pane with tab below the wide
// width (contracts AC-12).
func extCaptureScenes() []captureScene {
	review := func(o func() extPreviewOpts) func() *fakeServices {
		return func() *fakeServices { return extFake(extPreview(o()), nil) }
	}

	open := func(o func() extPreviewOpts, keys ...string) func(h *harness) {
		return func(h *harness) {
			h.send(openExtractMsg{req: extPreview(o()).Request})
			h.press(keys...)
		}
	}

	held := func(then func(h *harness, rec *extRecord)) func(h *harness) {
		return func(h *harness) {
			open(extThreeBatches)(h)

			rec := extMidRun(h.t, h)
			if then != nil {
				then(h, rec)
			}
		}
	}

	result := func(o func() extPreviewOpts, x func(p *learning.ExtractPreview) *distill.ExtractExecution) func() *fakeServices {
		return func() *fakeServices {
			p := extPreview(o())

			return extFake(p, func(context.Context, *learning.ExtractPreview, func(distill.ExtractEvent)) (
				*distill.ExtractExecution, error,
			) {
				return x(p), nil
			})
		}
	}

	failed := func(p *learning.ExtractPreview) *distill.ExtractExecution {
		return extExec(p, func(bs []distill.ExtractBatchOutcome, _ *distill.ExtractExecution) {
			for i := range bs {
				extFailBatch(&bs[i], distill.FailInvalidReply,
					errors.New("parse reply: invalid character 'S' looking for beginning of value"))
			}
		})
	}

	superseded := func() *fakeServices {
		f := newFake()
		f.ledger = lesLedger()
		f.ledger.Superseded = []domain.Proposal{
			{
				ID: 8, Rule: "retire-old-flag", Region: "internal/cli", Note: "Remove the --legacy flag from new code.",
				Members: []int64{104}, Agent: "claude/v2", Status: domain.ProposalSuperseded,
			},
		}

		return f
	}

	// appliedAs is the applied p4 of Proposals with the pin link of the
	// Pins snapshot, so the DECISION text follows that link.
	appliedAs := func(link pinfile.Link) func() *fakeServices {
		return func() *fakeServices {
			p4 := lesLedger().Applied[0]
			p := extPreview(extPreviewOpts{
				rows: []domain.Proposal{p4},
				live: map[int64]string{4: "Wrap SQL errors with the query name and the table."},
			})

			f := extFake(p, nil)
			f.pins = propPinLinks(map[int64]pinfile.Link{4: link})

			return f
		}
	}

	applied := appliedAs(pinfile.LinkLinked)

	// detail moves the review cursor to p4, the row with a live note. A
	// compact layout first opens the Proposals pane, because there the
	// movement keys scroll the Plan pane. Its detail does not fit under
	// the list, so end scrolls to the stacked note pair.
	detail := func(h *harness) {
		compact := h.m.st.width < wideWidth
		if compact {
			h.press("tab")
		}

		h.press("down", "down", "down", "down")

		// A render records the scroll bounds that end moves to.
		if compact {
			h.view()
			h.press("end")
		}
	}

	// resultDetail opens the Proposals pane of a compact result after the
	// keys. The wide layout shows that pane beside the summary. A compact
	// pane cannot hold the table and the detail, so end scrolls to the
	// end of the detail; a render first records the scroll bounds.
	resultDetail := func(o func() extPreviewOpts, keys ...string) func(h *harness) {
		return func(h *harness) {
			open(o, keys...)(h)

			if h.m.st.width < wideWidth {
				h.press("tab")
				h.view()
				h.press("end")
			}
		}
	}

	settingsStale := func(h *harness) {
		open(extMixed)(h)
		h.send(settingsSavedMsg{group: settings.GroupInference, at: extPlannedAt})
	}

	refused := func() *fakeServices {
		return extFake(extPreview(extMixed()), func(context.Context, *learning.ExtractPreview,
			func(distill.ExtractEvent)) (*distill.ExtractExecution, error) {
			return nil, fmt.Errorf("%w: p21 is applied now", distill.ErrExtractStale)
		})
	}

	cursorRow := func() extPreviewOpts { return extPreviewOpts{rows: extRows()[4:]} }

	// openCursor opens the review as the cursor row of an empty
	// selection. The sender of the request sets the flag.
	openCursor := func(h *harness) {
		h.send(openExtractMsg{req: extPreview(cursorRow()).Request, cursor: true})
	}

	internalErr := func() *fakeServices { return extFake(extPreview(extThreeBatches()), extPanicRun) }

	// closedNow is a run whose first batch came back after another
	// writer dismissed its three rows. The end of the run reloads the
	// ledger, so the result shows them as closed now.
	closedNow := func() *fakeServices {
		f := result(extTwoBatches, extAllConflictExec)()
		f.ledger.Dismissed = append(f.ledger.Dismissed, extDismissed(23), extDismissed(22), extDismissed(21))

		return f
	}

	toApplied := func(keys ...string) func(h *harness) {
		return func(h *harness) {
			h.press("3", "f")
			h.press(keys...)
		}
	}

	return []captureScene{
		{name: "90-extract-review", fake: review(extMixed), drive: open(extMixed)},
		{name: "90b-extract-review-live-note", fake: review(extMixed), drive: func(h *harness) {
			open(extMixed)(h)
			detail(h)
		}},
		{name: "90c-extract-review-blocked", fake: review(func() extPreviewOpts {
			o := extMixed()
			o.blockers = []string{`agent cli "claude" not found on PATH`}

			return o
		}), drive: open(extMixed)},
		{name: "90d-extract-review-nothing", fake: review(func() extPreviewOpts {
			o := extMixed()
			o.none = true

			return o
		}), drive: open(extMixed)},
		{name: "90e-extract-plan-not-ready", fake: func() *fakeServices {
			f := extFake(extPreview(extMixed()), nil)
			f.planExtract = func(learning.ExtractRequest) (*learning.ExtractPreview, error) {
				return nil, fmt.Errorf("%w: index database /work/repo/.seamark/index.db is absent: build the index first",
					learning.ErrExtractNotReady)
			}

			return f
		}, drive: open(extMixed)},
		{name: "90f-extract-review-settings-stale", fake: review(extMixed), drive: settingsStale},
		{name: "90g-extract-review-refused", fake: refused, drive: open(extMixed, "y")},
		{name: "90h-extract-review-nothing-selected", fake: review(func() extPreviewOpts {
			return extPreviewOpts{none: true}
		}), drive: func(h *harness) { h.send(openExtractMsg{req: learning.ExtractRequest{}}) }},
		{name: "90i-extract-review-workdir-blocked", fake: review(func() extPreviewOpts {
			o := extMixed()
			o.blockers = []string{"the agent's working directory is unknown: getwd: no such file or directory"}
			o.workDirErr = errors.New("getwd: no such file or directory")

			return o
		}), drive: open(extMixed)},
		{name: "90j-extract-review-cursor-row", fake: review(cursorRow), drive: openCursor},
		{name: "91-extract-review-tab", fake: review(extMixed), drive: open(extMixed, "tab")},
		{name: "92-extract-running", fake: review(extThreeBatches), drive: held(nil)},
		{name: "93-extract-stop-confirm", fake: review(extThreeBatches), drive: held(func(h *harness, _ *extRecord) {
			h.press("x")
		})},
		{name: "93b-extract-stopping", fake: review(extThreeBatches), drive: held(func(h *harness, _ *extRecord) {
			h.press("x", "y")
		})},
		{name: "94-extract-result-stopped", fake: result(extThreeBatches, extStoppedExec),
			drive: open(extThreeBatches, "y")},
		{name: "94b-extract-result-input-changed", fake: result(extThreeBatches, extInputChangedExec),
			drive: open(extThreeBatches, "y")},
		{name: "94c-extract-result-completed", fake: result(extTwoBatches, extCompletedExec),
			drive: open(extTwoBatches, "y")},
		{name: "95-extract-result-conflict", fake: result(extTwoBatches, extConflictExec),
			drive: open(extTwoBatches, "y")},
		{name: "95b-extract-result-applied-link", fake: result(extTwoBatches, extConflictExec),
			drive: resultDetail(extTwoBatches, "y", "down", "down", "down", "down")},
		{name: "95c-extract-result-conflict-closed", fake: closedNow, drive: resultDetail(extTwoBatches, "y")},
		{name: "96-extract-result-failed", fake: result(extThreeBatches, failed), drive: open(extThreeBatches, "y")},
		{name: "96b-extract-result-internal-error", fake: internalErr, drive: open(extThreeBatches, "y")},
		{name: "96c-extract-result-store-error", fake: result(extThreeBatches, extStoreErrorExec),
			drive: open(extThreeBatches, "y")},
		{name: "97-proposals-superseded", fake: superseded, drive: func(h *harness) { h.press("3", "f", "f", "f") }},
		{name: "98-proposals-applied-extract", fake: applied, drive: toApplied()},
		{name: "98b-proposals-applied-extract-review", fake: applied, drive: toApplied("t")},
		{name: "98c-proposals-applied-not-present", fake: appliedAs(pinfile.LinkNotPresent), drive: toApplied()},
		{name: "99-extract-review-ascii", fake: review(extMixed), ascii: true, drive: func(h *harness) {
			open(extMixed)(h)
			detail(h)
		}},
		{name: "99b-extract-result-ascii", fake: result(extTwoBatches, extConflictExec), ascii: true,
			drive: open(extTwoBatches, "y")},
		// The cursor moves from p6 to p23, a pending row whose regions
		// changed, so the detail shows the region change in ASCII.
		{name: "99c-extract-result-ascii-detail", fake: result(extTwoBatches, extConflictExec), ascii: true,
			drive: resultDetail(extTwoBatches, "y", "up", "up", "up")},
	}
}

// TestCaptureExtractScenesAreNamed keeps the scene list in its range:
// numbers 90 to 99 with letter variants, and no name twice. It writes
// nothing, so it runs without the capture directory.
func TestCaptureExtractScenesAreNamed(t *testing.T) {
	seen := map[string]bool{}

	for _, sc := range extCaptureScenes() {
		require.False(t, seen[sc.name], "scene %s is listed twice", sc.name)
		seen[sc.name] = true

		n, err := strconv.Atoi(sc.name[:2])
		require.NoError(t, err, sc.name)
		assert.GreaterOrEqual(t, n, 90, sc.name)
		assert.LessOrEqual(t, n, 99, sc.name)
		assert.True(t, strings.Contains(sc.name, "extract") || strings.Contains(sc.name, "proposals"), sc.name)
	}
}
