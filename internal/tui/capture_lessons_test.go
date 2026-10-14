package tui

// muteCaptureScenes lists the lesson mutes scenes B0 to B2 of contracts
// §7 over muteFile. The list rows show a no-op entry and a duplicate.
// The overlap of two entries shows in the record form and in the review,
// because a row names only its own entry.
func muteCaptureScenes() []phase4Scene {
	// record opens the mute editor and types a record that overlaps
	// three entries: rule F541 under ./api/.
	record := func(h *harness) {
		h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
		h.press("enter", "a")
		h.typeText("F541")
		h.press("tab")
		h.typeText("./api/")
	}

	return []phase4Scene{
		// The cursor goes to entry 5. So at 60x20 the window passes the
		// no-op rows 2 and 3 and the duplicate row 4 with its note.
		{name: "B0-mute-list", want: "duplicate of line", open: phase4OnSettings(muteFile, nil, func(h *harness) {
			h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
			h.press("enter", "down", "down", "down", "down")
		})},
		{name: "B1-mute-record-form", want: "Region", open: phase4OnSettings(muteFile, nil, record)},
		{name: "B2-mute-review", want: "Save .seamark/lessons.yaml", open: phase4OnSettings(muteFile, nil,
			func(h *harness) {
				record(h)
				h.press("enter", "s")
			})},
	}
}

// deliveryCaptureScenes lists the lesson delivery scenes C0 to C3 of
// contracts §7. Each one opens Settings › Lesson delivery with the
// cursor on threshold.
func deliveryCaptureScenes() []phase4Scene {
	delivery := func(h *harness) {
		h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
	}

	return []phase4Scene{
		// threshold 1.5 reads as 1 and pin_budget 100 as the hook cap 8,
		// so the detail shows a stored value next to another effective one.
		{name: "C0-delivery-values", want: "Effective", open: phase4OnSettings("threshold: 1.5\npin_budget: 100\n", nil,
			func(h *harness) {
				delivery(h)
				linksShowDetail(h)
				captureScrollTo(h, "pgdown", "Effective")
			})},
		// One more than the largest value is refused, and the text stays.
		{name: "C1-delivery-number-overflow", want: "must be a whole number", open: phase4OnSettings("pin_budget: 4\n", nil,
			func(h *harness) {
				delivery(h)
				h.press("down", "enter", "down", "space", "backspace")
				h.typeText("2147483648")
				h.press("enter")
			})},
		// The help of hook_delivery quotes the inspections of the clients
		// below the engine rule. A choice also names REPETITION, so the
		// anchor is a word of the client facts.
		{name: "C2-delivery-mode-help", want: "reset hook", open: phase4OnSettings("", func(svc *settingsSvc) {
			svc.integrations = settingsResetInspections()
		}, func(h *harness) {
			delivery(h)
			h.press("down", "down", "down")
			linksShowDetail(h)
			captureScrollTo(h, "pgdown", "verified")
		})},
		// d resets threshold, and hook_delivery changes to
		// once-per-context, so one review writes two keys. At 60x20 the
		// rows of CHANGE start below the window, so the scene scrolls to
		// the second row.
		{name: "C3-delivery-review", want: "once-per-context", open: phase4OnSettings(
			"# delivery\nthreshold: 4 # team value\n", nil, func(h *harness) {
				delivery(h)
				h.press("d", "down", "down", "down", "enter", "down", "down", "space", "enter", "s")
				captureScrollTo(h, "down", "once-per-context")
			})},
	}
}
