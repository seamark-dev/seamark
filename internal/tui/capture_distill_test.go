package tui

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// TestCaptureDistillStates writes the distillation scenes (41 to 49) at
// every capture size into SEAMARK_TUI_CAPTURE_DIR, for the maintainer
// visual review. It also checks that every frame fits its terminal.
func TestCaptureDistillStates(t *testing.T) {
	dir := captureDir()

	restore := distGetwd
	distGetwd = func() (string, error) { return "/work/repo", nil }

	t.Cleanup(func() { distGetwd = restore })

	snap := fakeSettingsSnapshot(t, "agent:\n  cli: claude\n")

	for _, sc := range distCaptureScenes() {
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

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}

// distCaptureScenes lists the distillation scenes. They cover the scope
// form, the plan review and its failures, the run and the stop. They
// also cover every result kind, an earlier record, and the proposals of
// one run.
func distCaptureScenes() []captureScene {
	full := distPreviewOpts{groups: 6, cleanup: true, skipped: 23}
	preview := func() *learning.DistillPreview { return distPreview(full) }

	fake := func(p func() *learning.DistillPreview) func() *fakeServices {
		return func() *fakeServices { return distFake(p(), nil) }
	}

	open := func(keys ...string) func(h *harness) {
		return func(h *harness) {
			h.send(navigateMsg{dest: destLessons, intent: "distill"})
			h.press(keys...)
		}
	}

	held := func(then func(h *harness, rec *distRecord)) func(h *harness) {
		return func(h *harness) {
			h.send(navigateMsg{dest: destLessons, intent: "distill"})
			rec := distMidRun(h.t, h)

			if then != nil {
				then(h, rec)
			}
		}
	}

	result := func(x func(p *learning.DistillPreview) *distill.Execution, err error) func() *fakeServices {
		return func() *fakeServices {
			p := preview()

			return distFake(p, func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
				return x(p), err
			})
		}
	}

	return []captureScene{
		{name: "41-distill-scope", fake: fake(preview), drive: open()},
		{name: "41b-distill-scope-custom", fake: fake(preview), drive: open("down", "down", "space")},
		{name: "41c-distill-scope-outside", fake: fake(preview), drive: open("/", ".", ".", "/", "t", "m", "p", "enter")},
		{name: "42-distill-review", fake: fake(preview), drive: open("enter")},
		{name: "42d-distill-review-over-limit", fake: fake(distOverLimit), drive: open(distOverLimitKeys...)},
		{name: "42b-distill-review-blocked", fake: fake(func() *learning.DistillPreview {
			o := full
			o.blockers = []string{`agent cli "claude" not found on PATH`}

			return distPreview(o)
		}), drive: open("enter")},
		{name: "43-distill-plan-config-error", fake: func() *fakeServices {
			f := distFake(preview(), nil)
			f.planDistill = func(learning.DistillRequest) (*learning.DistillPreview, error) {
				return nil, &learning.DistillConfigError{Path: ".seamark/lessons.yaml",
					Err: errors.New("yaml: line 4: mapping values are not allowed in this context")}
			}

			return f
		}, drive: open("enter")},
		{name: "44-distill-nothing-new", fake: fake(func() *learning.DistillPreview {
			return distPreview(distPreviewOpts{skipped: 14})
		}), drive: open("enter")},
		{name: "44b-distill-cleanup-only", fake: fake(func() *learning.DistillPreview {
			return distPreview(distPreviewOpts{skipped: 14, cleanup: true})
		}), drive: open("enter")},
		{name: "44c-distill-cleanup-confirm", fake: fake(func() *learning.DistillPreview {
			return distPreview(distPreviewOpts{skipped: 14, cleanup: true})
		}), drive: open("enter", "c")},
		{name: "45-distill-running", fake: fake(preview), drive: held(nil)},
		{name: "46-distill-stop-confirm", fake: fake(preview), drive: held(func(h *harness, _ *distRecord) {
			h.press("x")
		})},
		{name: "46b-distill-stopping", fake: fake(preview), drive: held(func(h *harness, _ *distRecord) {
			h.press("x", "y")
		})},
		{name: "47-distill-result-partial", fake: result(func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				distDone(&gs[0], distProposals("check-errors", 21, 22)...)
				distDone(&gs[1])
				gs[1].Duplicates = []distill.Duplicate{{Rule: "close-handles", Of: "close-db-handles"}}
				distDone(&gs[2], distProposals("keep-index-fresh", 23)...)
				distFail(&gs[3], distill.FailInvalidReply, errors.New("parse reply: invalid character 'S'"))
				distDone(&gs[4], distProposals("pin-go-version", 24, 25)...)
				distFail(&gs[5], distill.FailTimeout, context.DeadlineExceeded)
				x.Removed = 2
			})
		}, nil), drive: open("enter", "y")},
		{name: "47b-distill-result-stopped", fake: result(distPartialExec, nil), drive: open("enter", "y")},
		{name: "48-distill-result-failed", fake: result(func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				for i := range gs {
					distFail(&gs[i], distill.FailAgent, errors.New(`exec: "claude": executable file not found in $PATH`))
				}

				x.Removed = 2
			})
		}, nil), drive: open("enter", "y")},
		{name: "48b-distill-result-completed", fake: result(func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				for i := range gs {
					distDone(&gs[i], distProposals("rule", int64(21+2*i), int64(22+2*i))...)
				}

				x.Removed = 2
			})
		}, nil), drive: open("enter", "y")},
		// A new draft keeps the finished run as an earlier record, and [
		// shows it read-only.
		{name: "48c-distill-earlier-record", fake: result(func(p *learning.DistillPreview) *distill.Execution {
			return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
				for i := range gs {
					distDone(&gs[i], distProposals("rule", int64(21+2*i), int64(22+2*i))...)
				}

				x.Removed = 2
			})
		}, nil), drive: func(h *harness) {
			open("enter", "y")(h)
			open("[")(h)
		}},
		{name: "49-proposals-run-filter", fake: func() *fakeServices {
			p := preview()
			f := distFake(p, func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
				return distExec(p, func(gs []distill.GroupOutcome, _ *distill.Execution) {
					distDone(&gs[0], domain.Proposal{ID: 2, Rule: "close-db-handles"}, domain.Proposal{ID: 3, Rule: "keep-changelog-current"})
				}), nil
			})

			return f
		}, drive: open("enter", "y", "enter")},
		{name: "42c-distill-review-ascii", fake: fake(preview), ascii: true, drive: open("enter")},
		{name: "45b-distill-running-ascii", fake: fake(preview), ascii: true, drive: held(nil)},
	}
}
