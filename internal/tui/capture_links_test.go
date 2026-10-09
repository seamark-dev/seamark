package tui

import (
	"strconv"
	"testing"
	"time"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// setSettingsSnapshot sets the settings snapshot of a console to a real
// inspection of content. The Overview reads the shared state, so the
// scene needs no reload and shows no refresh toast.
func setSettingsSnapshot(h *harness, content string) {
	h.m.st.settings.set(fakeSettingsSnapshot(h.t, content), nil, time.Now())
}

// linksEvidenceFake is a populated workspace whose ready index holds the given
// findings. An empty ledger goes with findings, so the Overview
// suggests to prepare a distillation.
func linksEvidenceFake(fixes, reviews int) func() *fakeServices {
	return func() *fakeServices {
		f := ovPopulated()
		f.learning = ovlEvidence(fixes, reviews)

		if fixes+reviews > 0 {
			f.ledger = &proposals.Ledger{}

			return f
		}

		// A store without findings has no lessons and no review mine
		// either, so the LEARN card agrees with the item.
		s := *f.index.Status
		s.Lessons, s.ReviewsMinedAt = 0, 0
		f.index.Status = &s

		return f
	}
}

// linksDoctorFake is a populated workspace with the linked doctor checks.
func linksDoctorFake() *fakeServices {
	f := ovPopulated()
	f.doctor = doclReport()

	return f
}

// linksShowDetail shows the detail of the selected check or item: enter in
// the narrow layout, tab in the wide one. In the wide layout enter
// would open the workflow instead.
func linksShowDetail(h *harness) {
	if h.m.st.width < wideWidth {
		h.press("enter")

		return
	}

	h.press("tab")
}

// linksCaptureScenes are the Phase 2 link states for the visual review: the
// learning items of the Overview and the linked Doctor checks. Their
// numbers start at 61, so they never collide with the other scenes.
func linksCaptureScenes() []captureScene {
	return []captureScene{
		{name: "61-overview-collect-evidence", fake: linksEvidenceFake(0, 0), drive: func(h *harness) {
			ovSelect(h.t, h, "learn:evidence")
		}},
		{name: "62-overview-prepare-distill", fake: linksEvidenceFake(8, 4), drive: func(h *harness) {
			ovSelect(h.t, h, "learn:distill")
		}},
		{name: "63-overview-inference-unresolved", fake: ovPopulated, drive: func(h *harness) {
			setSettingsSnapshot(h, "agent:\n  cli: nosuch\n")
			ovSelect(h.t, h, "learn:agent")
		}},
		{name: "64-overview-pin-write-off", fake: ovPopulated, drive: func(h *harness) {
			setSettingsSnapshot(h, "")
			ovSelect(h.t, h, "learn:pinwrite")
		}},
		{name: "65-overview-run-row", fake: ovPopulated, drive: func(h *harness) {
			captureRun(h)
			ovSelect(h.t, h, "learn:run")
		}},
		{name: "66-overview-settings-malformed", fake: ovPopulated, drive: func(h *harness) {
			setSettingsSnapshot(h, "agent: [\n")
			ovSelect(h.t, h, "learn:config")
			linksShowDetail(h)
		}},
		{name: "67-doctor-agent-link", fake: linksDoctorFake, drive: func(h *harness) {
			h.press("4")
		}},
		{name: "68-doctor-gh-link-detail", fake: linksDoctorFake, drive: func(h *harness) {
			h.press("4", "down")
			linksShowDetail(h)
		}},
		{name: "69-overview-links-ascii", fake: linksEvidenceFake(0, 0), ascii: true, drive: func(h *harness) {
			setSettingsSnapshot(h, "agent:\n  cli: nosuch\n")
			captureRun(h)
			ovSelect(h.t, h, "learn:run")
		}},
	}
}

// TestCaptureLinks writes the frames of the link scenes at every
// capture size into SEAMARK_TUI_CAPTURE_DIR, like
// TestCaptureRepresentativeStates. It also checks that every frame fits
// its terminal.
func TestCaptureLinks(t *testing.T) {
	dir := captureDir()

	for _, sc := range linksCaptureScenes() {
		for _, size := range captureSizes {
			w, hgt := size[0], size[1]

			h := &harness{t: t, m: newModel(sc.fake(), newTheme(sc.ascii))}
			h.run(h.m.Init())
			h.send(teaSize(w, hgt))

			if sc.drive != nil {
				sc.drive(h)
			}

			frame := h.view()
			assertFrame(t, frame, w, hgt)

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(hgt) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}
