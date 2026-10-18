package tui

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// captureEvidenceScenes are the Evidence states for the visual review
// (AC-12). They show the sources, the blocked view and the previews.
// They also show a run in progress, its stop, and its results. The Evidence scenes use the
// numbers 31 to 40 (decision D-16); a letter adds a variant.
func captureEvidenceScenes() []captureScene {
	populated := func() *fakeServices {
		f := evFake()
		ready := ovPopulated()
		f.index, f.doctor, f.integrations = ready.index, ready.doctor, ready.integrations

		return f
	}

	open := func(h *harness) *lessons {
		h.press("3", "e")

		return h.m.screens[destLessons].(*lessons)
	}

	started := func(h *harness) *evRun {
		les := open(h)
		evStarted(h.t, h, les, index.SourcesReviewsAndLocal)
		evEvent(h, index.PhaseMineFixes, index.SourceFixes, nil)
		evEvent(h, index.PhasePersist, index.SourceFixes, nil)
		evEvent(h, index.PhaseSourceDone, index.SourceFixes, func(e *index.RefreshEvent) {
			e.State = index.SourceRefreshed
		})
		evEvent(h, index.PhaseFetchReviews, index.SourceReviews, nil)
		evEvent(h, index.PhaseFetchBytes, index.SourceReviews, func(e *index.RefreshEvent) { e.Bytes = 3 << 19 })

		return les.evid.run
	}

	return []captureScene{
		{name: "31-evidence", fake: populated, drive: func(h *harness) { open(h) }},
		{name: "31b-evidence-reviews-detail", fake: populated, drive: func(h *harness) {
			open(h)
			h.press("down", "tab")
		}},
		{name: "32-evidence-no-index", fake: func() *fakeServices {
			f := evFake()
			f.learning = learning.Snapshot{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}
			f.index = services.IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}

			return f
		}, drive: func(h *harness) { open(h) }},
		{name: "32b-evidence-config-error", fake: func() *fakeServices {
			f := populated()
			f.learning.Window = learning.Window{Floor: 200, ConfigErr: errors.New("yaml: line 4: mapping values are not allowed in this context")}

			return f
		}, drive: func(h *harness) {
			open(h)
			h.press("down", "down")
		}},
		{name: "33-evidence-preview-local", fake: populated, drive: func(h *harness) {
			evPress(h, open(h), evRefreshLocal)
		}},
		{name: "33b-evidence-preview-reviews", fake: populated, drive: func(h *harness) {
			evPress(h, open(h), evRefreshReviews)
		}},
		{name: "34-evidence-preview-blocked", fake: func() *fakeServices {
			f := populated()
			f.planEvidence = func(mode index.SourceMode) (*learning.Plan, error) {
				p := evPlanOf(f.learning, mode)
				p.Blockers = []learning.Blocker{{
					Kind: learning.BlockConfig, Reason: "yaml: line 4: mapping values are not allowed in this context",
					Path: "/work/repo/.seamark/config.yaml",
				}}

				return p, nil
			}

			return f
		}, drive: func(h *harness) { evPress(h, open(h), evRefreshReviews) }},
		{name: "35-evidence-run-active", fake: populated, drive: func(h *harness) { started(h) }},
		{name: "35b-evidence-run-stop-confirm", fake: populated, drive: func(h *harness) {
			started(h)
			h.press("x")
		}},
		{name: "36-evidence-run-stopping", fake: populated, drive: func(h *harness) {
			started(h)
			h.press("x", "y")
		}},
		{name: "37-evidence-result-partial", fake: populated, drive: func(h *harness) {
			started(h)
			h.send(evDoneMsg{id: 1, out: learning.Outcome{
				Fixes:   evOutcomeStored(24),
				Reviews: evOutcome(index.SourceFailed, index.CauseAuth, evBadToken),
				After:   evSnap(),
			}})
			h.m.toast = toastMsg{}
		}},
		// The engine groups the fetched comments before the review write.
		// The done fetch step keeps its counts. One stored fix finding
		// shows the singular sentence.
		{name: "37b-evidence-result-refreshed", fake: populated, drive: func(h *harness) {
			started(h)
			evEvent(h, index.PhaseFetched, index.SourceReviews, func(e *index.RefreshEvent) {
				e.Comments, e.Kept = 287, 250
			})
			evEvent(h, index.PhaseCluster, index.SourceReviews, nil)
			evEvent(h, index.PhasePersist, index.SourceReviews, nil)
			evEvent(h, index.PhaseSourceDone, index.SourceReviews, func(e *index.RefreshEvent) {
				e.State = index.SourceRefreshed
			})

			after := evSnap()
			after.FixFindings, after.ReviewFindings, after.Lessons = 1, 120, 13
			h.send(evDoneMsg{id: 1, out: learning.Outcome{
				Fixes:    evOutcomeStored(1),
				Reviews:  index.SourceOutcome{State: index.SourceRefreshed, Stored: 120, Lessons: 13},
				Comments: 287, Kept: 250, After: after,
			}})
			h.m.toast = toastMsg{}
		}},
		// GitHub refuses the fetch for a rate limit. The stored reviews
		// stay, and the guidance names the wait, not a login.
		{name: "37c-evidence-result-rate-limited", fake: populated, drive: func(h *harness) {
			started(h)
			evEvent(h, index.PhaseSourceDone, index.SourceReviews, func(e *index.RefreshEvent) {
				e.State = index.SourceFailed
			})
			h.send(evDoneMsg{id: 1, out: learning.Outcome{
				Fixes:   evOutcomeStored(24),
				Reviews: evOutcome(index.SourceFailed, index.CauseRateLimited, evRateLimited),
				After:   evSnap(),
			}})
			h.m.toast = toastMsg{}
		}},
		// The start check refuses a stale plan before any work, so no
		// progress event comes before this result.
		{name: "38-evidence-result-conflict", fake: populated, drive: func(h *harness) {
			evStarted(h.t, h, open(h), index.SourcesReviewsAndLocal)

			stale := fmt.Errorf("%w: HEAD moved", learning.ErrConflict)
			refused := evOutcome(index.SourceNotAttempted, index.CauseRefused, "HEAD moved")
			h.send(evDoneMsg{id: 1, out: learning.Outcome{
				Fixes: refused, Reviews: refused, Conflict: stale, After: evSnap(),
			}, err: stale})
			h.m.toast = toastMsg{}
		}},
		// The check before the review write refuses it after the fix
		// source saved and the fetch ran.
		{name: "38b-evidence-result-conflict-mid-run", fake: populated, drive: func(h *harness) {
			started(h)
			evEvent(h, index.PhaseFetched, index.SourceReviews, func(e *index.RefreshEvent) {
				e.Comments, e.Kept = 287, 250
			})
			evEvent(h, index.PhaseSourceDone, index.SourceReviews, func(e *index.RefreshEvent) {
				e.State = index.SourceFailed
			})

			stale := fmt.Errorf("%w: the index database file was replaced", learning.ErrConflict)
			h.send(evDoneMsg{id: 1, out: learning.Outcome{
				Fixes:    evOutcomeStored(24),
				Reviews:  evOutcome(index.SourceFailed, index.CauseRefused, stale.Error()),
				Comments: 287, Kept: 250, Conflict: stale, After: evSnap(),
			}, err: stale})
			h.m.toast = toastMsg{}
		}},
		{name: "39-evidence-ascii", fake: populated, ascii: true, drive: func(h *harness) { open(h) }},
		{name: "40-evidence-notice-overview", fake: populated, drive: func(h *harness) {
			started(h)
			h.press("1")
		}},
	}
}

// evOutcomeStored is a refreshed source outcome with n stored findings.
func evOutcomeStored(n int) index.SourceOutcome {
	return index.SourceOutcome{State: index.SourceRefreshed, Stored: n}
}

// TestCaptureEvidence writes the Evidence scenes at every capture size
// into SEAMARK_TUI_CAPTURE_DIR, like TestCaptureRepresentativeStates.
// It also checks that every frame fits.
func TestCaptureEvidence(t *testing.T) {
	dir := captureDir()

	for _, sc := range captureEvidenceScenes() {
		for _, size := range captureSizes {
			w, h := size[0], size[1]

			hs := &harness{t: t, m: newModel(sc.fake(), ui.NewTheme(sc.ascii))}
			hs.run(hs.m.Init())
			hs.send(teaSize(w, h))

			if sc.drive != nil {
				sc.drive(hs)
			}

			frame := hs.view()
			assertFrame(t, frame, w, h)

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}
