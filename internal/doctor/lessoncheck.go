package doctor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// checkLessonChecks reports the lesson-check row "checks". The row
// reads the capability report of the shared inspection, so it agrees
// with status, init, and the console. It prints only when a
// lesson-check or exploration hook is installed for some client, or
// lessons.yaml uses a lesson-check key. So a repository without them
// keeps its doctor bytes.
//
// The state is fail only when a guard is installed and the
// configuration does not validate, because the guard then denies every
// covered edit. A requested pin that no handler covers, a partial
// install, a wrapper that discards the exit status, untrusted hooks,
// another hook binary, or an invalid configuration is a warning.
func checkLessonChecks(r *Report, inspections []integration.Inspection, root string, self integration.BinaryIdentity) {
	cfg, errs := lessoncheck.LoadForCapability(root)

	if !slices.ContainsFunc(inspections, integration.Inspection.LessonHooksInstalled) &&
		!lessoncheck.UsesLessonChecks(cfg, errs) {
		return
	}

	report := integration.LessonCapabilityOf(inspections, cfg, errs, self)
	guard := slices.ContainsFunc(inspections, integration.Inspection.LessonGuardInstalled)

	state := StateOK

	switch {
	case !report.Config.Valid && guard:
		state = StateFail
	case !report.Config.Valid, lessonCheckWarns(report):
		state = StateWarn
	}

	r.add("checks", state, lessonCheckDetail(report, cfg, inspections),
		strings.Join(compact(lessonCheckFixes(report, guard, inspections)), "; "))
}

// lessonCheckDetail is the detail of the row: the installed mode of each
// client, the pin counts by mode, the configuration state, and the limit
// of each client whose verified pre-write paths have one. A client
// without the structured Read path reads exploration inactive.
//
// The installed mode is a closed set without an unknown value. So an
// unreadable hook document gets a doctor-level qualifier, and the row
// never reports it as the mode off (error is not absence).
func lessonCheckDetail(report lessoncheck.CapabilityReport, cfg *lessoncheck.Config,
	inspections []integration.Inspection,
) string {
	var parts, clients, limitNotes, noExploration []string

	for _, c := range report.Clients {
		clients = append(clients, installedText(c, inspections))

		// A verified pre-write path can hold only inside a limit, for
		// example a client that lets the write run after its hook timeout.
		// The row names the limit, because the row otherwise reads as full
		// enforcement.
		if limits := c.Limits(lessoncheck.StagePreWrite); len(limits) > 0 {
			limitNotes = append(limitNotes, c.Client+" pre-write "+lessoncheck.LimitText(limits))
		}

		if !c.HasStage(lessoncheck.StageExploration) {
			noExploration = append(noExploration, "exploration inactive for "+c.Client)
		}
	}

	if len(clients) > 0 {
		parts = append(parts, strings.Join(clients, ", "))
	}

	enforce, check, advisory := lessoncheck.PinModeCounts(cfg)
	parts = append(parts, fmt.Sprintf("%d enforce, %d check, %d advisory pin(s)", enforce, check, advisory), report.ConfigText())

	parts = append(parts, limitNotes...)

	return strings.Join(append(parts, noExploration...), " · ")
}

// installedText names one client and its installed mode, in the words
// that status and the console use.
func installedText(c lessoncheck.ClientCapability, inspections []integration.Inspection) string {
	return c.Client + " " + integration.LessonInstalledText(c, integration.LessonDocumentError(inspections, c.Client))
}

// lessonCheckWarns reports whether the report holds one of the warning
// states of the capability report.
func lessonCheckWarns(report lessoncheck.CapabilityReport) bool {
	for _, p := range report.Pins {
		if slices.ContainsFunc(p.Clients, func(c lessoncheck.PinClient) bool {
			return c.Badge == lessoncheck.BadgeRequestedInactive
		}) {
			return true
		}
	}

	warnReasons := []lessoncheck.CapReason{lessoncheck.CapReportOnlyWrapper, lessoncheck.CapUntrusted, lessoncheck.CapOtherBinary,
		lessoncheck.CapDocumentUnreadable}

	for _, c := range report.Clients {
		if c.Installed == lessoncheck.InstalledPartial {
			return true
		}

		for _, p := range c.Paths {
			if slices.ContainsFunc(p.Reasons, func(r lessoncheck.CapReason) bool { return slices.Contains(warnReasons, r) }) {
				return true
			}
		}
	}

	return false
}

// lessonCheckFixes returns the corrective actions of the row, in a
// fixed order: the configuration first, because an armed guard denies
// every covered edit until it validates.
func lessonCheckFixes(report lessoncheck.CapabilityReport, guard bool, inspections []integration.Inspection) []string {
	var fixes []string

	if !report.Config.Valid {
		fix := "fix .seamark/lessons.yaml; `seamark lessons --verify` lists every error"
		if guard {
			fix += " (until then the installed guard denies every covered edit)"
		}

		fixes = append(fixes, fix)
	}

	for _, c := range report.Clients {
		fixes = append(fixes, clientFixes(report, c, inspections)...)
	}

	return fixes
}

// clientFixes returns the actions of one client. Setup installs the
// missing handlers and names the current binary, so most fixes rerun
// init. A wrapper or trust is the user's own setting, so the fix says
// what to change instead. Each report-only definition gets the step of
// the enforce refusal, before the install step, because setup refuses
// enforce while such a definition exists.
//
// Two states need care, because an init that does not repair them
// would make doctor repeat its advice. Init cannot rewrite a hook
// document that does not parse, so the fix names the file. Init repairs
// a partial install only when the request names a lesson-check mode.
func clientFixes(report lessoncheck.CapabilityReport, c lessoncheck.ClientCapability,
	inspections []integration.Inspection,
) []string {
	var fixes []string

	if doc := integration.LessonDocumentError(inspections, c.Client); doc != "" {
		file, _, _ := strings.Cut(doc, ": ")

		return append(fixes, "fix or remove "+file+", which does not parse; setup cannot repair it")
	}

	if integration.LessonSourceUnreadable(c) {
		fixes = append(fixes, "fix or remove the unreadable supplemental hook source of "+c.Client)
	}

	fixes = append(fixes, integration.ReportOnlySteps(c.Client, integration.LessonReportOnly(inspections, c.Client))...)

	has := func(reason lessoncheck.CapReason) bool {
		return slices.ContainsFunc(c.Paths, func(p lessoncheck.PathCapability) bool { return slices.Contains(p.Reasons, reason) })
	}

	switch want := wantedMode(report, c); {
	case want != "" && c.Installed == lessoncheck.InstalledOff,
		want == lessoncheck.ModeEnforce && c.Installed == lessoncheck.InstalledCheck:
		fixes = append(fixes, fmt.Sprintf("run `seamark init --client %s --lesson-checks %s` to install the handlers", c.Client, want))
	case c.Installed == lessoncheck.InstalledPartial:
		fixes = append(fixes, partialFix(c.Client, repairMode(inspections, c.Client, want)))
	}

	if has(lessoncheck.CapOtherBinary) {
		fixes = append(fixes, fmt.Sprintf("run `seamark init --client %s` with the current binary, so the hooks name it", c.Client))
	}

	if has(lessoncheck.CapUntrusted) {
		fixes = append(fixes, integration.LessonTrustStep(c.Client))
	}

	return fixes
}

// wantedMode returns the mode that the requested-inactive pins of a
// client need: enforce when an enforce pin is inactive there, check
// when only a check pin is, else "".
func wantedMode(report lessoncheck.CapabilityReport, c lessoncheck.ClientCapability) lessoncheck.Mode {
	for _, mode := range []lessoncheck.Mode{lessoncheck.ModeEnforce, lessoncheck.ModeCheck} {
		if slices.Contains(report.InactiveClients(mode), c.Client) {
			return mode
		}
	}

	return ""
}

// partialFix names the setup command that restores the missing hook of a
// partial install. Without a known mode it offers both, because setup
// installs the missing half only for a named mode.
func partialFix(client string, mode lessoncheck.Mode) string {
	const tail = " to restore the missing lesson-check hook"

	if mode == "" {
		return fmt.Sprintf("run `seamark init --client %[1]s --lesson-checks check` or `seamark init --client %[1]s "+
			"--lesson-checks enforce`%s", client, tail)
	}

	return fmt.Sprintf("run `seamark init --client %s --lesson-checks %s`%s", client, mode, tail)
}

// repairMode returns the mode that restores a partial install: the
// stronger of the recorded mode and the wanted one, so the repair never
// weakens the install and still serves the pins. It returns "" when
// neither is known.
func repairMode(inspections []integration.Inspection, client string, wanted lessoncheck.Mode) lessoncheck.Mode {
	if wanted == lessoncheck.ModeEnforce {
		return wanted
	}

	if recorded := recordedMode(inspections, client); recorded != "" {
		return recorded
	}

	return wanted
}

// recordedMode reads the mode from the pre-write entry that survives in
// the hook sources of a client: a guard records enforce, a baseline
// records check. It returns "" when no pre-write entry survives, because
// the post-edit entry belongs to both modes.
func recordedMode(inspections []integration.Inspection, client string) lessoncheck.Mode {
	mode := lessoncheck.Mode("")

	for _, insp := range inspections {
		if insp.ClientID != client || insp.LessonHooks == nil {
			continue
		}

		for _, e := range insp.LessonHooks.Entries {
			switch {
			case e.Stage != lessoncheck.StagePreWrite:
			case lessoncheck.IsGuardMarker(e.Marker):
				return lessoncheck.ModeEnforce
			case lessoncheck.IsBaselineMarker(e.Marker):
				mode = lessoncheck.ModeCheck
			}
		}
	}

	return mode
}
