package status

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// pinCounts are the pin counts of lessons.yaml by normalized mode. The
// pins line prints them. The capability report lists only the pins with
// a check, so the counts of the advisory pins come from the file.
type pinCounts struct {
	enforce, check, advisory int
}

// gatherLessonChecks fills the lesson-check report of the status
// (contract K10). It reads lessons.yaml once and joins it with the
// inspection of the clients. The report is set only when a lesson-check
// or exploration hook is installed for some client, or lessons.yaml uses
// a lesson-check key. So the text and the JSON of a repository without
// them keep their bytes.
func gatherLessonChecks(s *Status, root string, self integration.BinaryIdentity) {
	cfg, errs := lessoncheck.LoadForCapability(root)

	if !slices.ContainsFunc(s.Clients, integration.Inspection.LessonHooksInstalled) &&
		!lessoncheck.UsesLessonChecks(cfg, errs) {
		return
	}

	report := integration.LessonCapabilityOf(s.Clients, cfg, errs, self)
	s.LessonChecks = &report

	enforce, check, advisory := lessoncheck.PinModeCounts(cfg)
	s.lessonPins = pinCounts{enforce: enforce, check: check, advisory: advisory}
}

// printLessonChecks renders the lesson checks block after the clients
// block: one line per client, then the pins line (contract K10). A
// status without the report prints nothing.
func printLessonChecks(w io.Writer, s *Status) {
	r := s.LessonChecks
	if r == nil {
		return
	}

	label := "lesson checks"

	for _, c := range r.Clients {
		fmt.Fprintf(w, "%-14s %-7s %s\n", label, c.Client, lessonClientText(c))

		label = ""
	}

	fmt.Fprintf(w, "%-14s %-7s %s\n", label, "pins", lessonPinsText(*r, s.lessonPins))
}

// lessonClientText summarizes one client: the installed mode, the state
// of the pre-write and post-edit paths, and the exploration state. The
// paths of a client without a lesson-check hook read as the mode off
// alone. A client without the structured Read path reads exploration
// inactive (§5 row 42). Its report path keeps the state unsupported.
func lessonClientText(c lessoncheck.ClientCapability) string {
	parts := []string{string(c.Installed)}

	if !onlyNotInstalled(c) {
		parts = append(parts, c.StageText(lessoncheck.StagePreWrite), c.StageText(lessoncheck.StagePostEdit))
	}

	if c.HasStage(lessoncheck.StageExploration) {
		parts = append(parts, c.StageText(lessoncheck.StageExploration))
	} else {
		parts = append(parts, "exploration inactive (no structured Read path)")
	}

	return strings.Join(parts, " · ")
}

// onlyNotInstalled reports whether every pre-write and post-edit path of
// the client lacks its hook. The mode off then says all.
func onlyNotInstalled(c lessoncheck.ClientCapability) bool {
	for _, p := range c.Paths {
		if p.HookStage != lessoncheck.StagePreWrite && p.HookStage != lessoncheck.StagePostEdit {
			continue
		}

		if !slices.Contains(p.Reasons, lessoncheck.CapNotInstalled) {
			return false
		}
	}

	return true
}

// lessonPinsText is the pins line: the pin counts by mode, the clients
// on which a pin of the mode is requested but inactive, and the
// configuration state.
func lessonPinsText(r lessoncheck.CapabilityReport, counts pinCounts) string {
	inactive := func(mode lessoncheck.Mode) string {
		clients := r.InactiveClients(mode)
		if len(clients) == 0 {
			return ""
		}

		return " (requested-inactive on " + strings.Join(clients, ", ") + ")"
	}

	return fmt.Sprintf("%d enforce%s, %d check%s, %d advisory · %s",
		counts.enforce, inactive(lessoncheck.ModeEnforce), counts.check, inactive(lessoncheck.ModeCheck),
		counts.advisory, r.ConfigText())
}
