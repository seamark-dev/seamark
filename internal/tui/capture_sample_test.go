package tui

import (
	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/tui/services"
)

// sampleCaptureScenes lists the selection sample scenes D0 to D3 of
// contracts §7. A scripted services.Sample stands in for the engine, as
// in sample_test.go. D0 to D2 open the sample of a new pin review; D3
// opens the sample of a delivery review that changes only hook_delivery.
func sampleCaptureScenes() []phase4Scene {
	// pinSampled returns services whose sample answers with script. A
	// ready index goes with a script that reads evidence, so the header
	// and the sample agree.
	pinSampled := func(script func(services.SampleRequest) services.SampleView, ready bool) func() *fakeServices {
		return func() *fakeServices {
			f := authorFake(nil)
			f.sample = script

			if ready {
				f.index = ovReadyIndex(f.root, f.dbPath)
			}

			return f
		}
	}

	// open drafts a new pin, opens its review and the sample of the
	// review, and adds paths.
	open := func(paths ...string) func(h *harness) {
		return func(h *harness) {
			authorFill(h)
			h.press("s", "s")

			for _, p := range paths {
				sampleAddPath(h, p)
			}
		}
	}

	// lists opens the sample with a.go, presses keys, and scrolls to
	// the end, where the lists and the count of the surface stand.
	lists := func(count string, keys ...string) func(h *harness) {
		return func(h *harness) {
			open("a.go")(h)
			h.press(keys...)
			captureScrollTo(h, "end", count)
		}
	}

	markers := pinSampled(sampleScripted(sampleMarkerView()), true)

	// A review that changes only hook_delivery selects the same lessons
	// on both sides: the sample does not simulate repetition.
	modeOnly := sampleMarkerView()
	modeOnly.Candidate, modeOnly.CandidateThreshold = modeOnly.Current, modeOnly.CurrentThreshold

	rejected := services.SampleView{
		Unavailable: sampleUnavailableTexts["no usable"],
		Rejected:    []delivery.RejectedPath{{Path: "../outside.go", Reason: delivery.RejectOutside}},
	}

	return []phase4Scene{
		{name: "D0-sample-hook", want: "hook selection", open: phase4OnPins(markers, lists("pins held back"))},
		{name: "D1-sample-change-set", want: "change-set selection",
			open: phase4OnPins(markers, lists("lessons trimmed", "space"))},
		{name: "D2-sample-no-paths", want: "Sample unavailable",
			open: phase4OnPins(pinSampled(sampleNoIndex, false), open())},
		{name: "D2b-sample-no-index", want: "Sample unavailable", open: phase4OnPins(pinSampled(sampleNoIndex, false),
			func(h *harness) {
				open("a.go")(h)
				captureScrollTo(h, "end", "model that.")
			})},
		{name: "D2c-sample-rejected-path", want: "rejected", open: phase4OnPins(pinSampled(sampleScripted(rejected), true),
			func(h *harness) {
				open("../outside.go")(h)
				captureScrollTo(h, "end", "rejected")
			})},
		{name: "D3-sample-mode-only", want: "hook_delivery changes repetition", open: phase4OnSettings("threshold: 4\n",
			func(svc *settingsSvc) {
				svc.sample = sampleScripted(modeOnly)
				svc.index = ovReadyIndex(svc.Root(), svc.DBPath())
			}, func(h *harness) {
				h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
				h.press("down", "down", "down", "enter", "down", "down", "space", "enter", "s", "s")
				sampleAddPath(h, "a.go")
			})},
	}
}
