package lessonreport

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// EmissionSentence is the fixed limit of every delivery count. The text
// report and the console print it, so neither surface can claim more than
// the audit log states.
const EmissionSentence = "These counts show what Seamark emitted or left out. They do not show what a model saw, or that a pin came before an action."

// HeldBackCauseUnknown is the split key of a held-back record whose cause
// is empty or outside the closed list. The key lives only in the split,
// so the closed list of the audit log keeps its values.
const HeldBackCauseUnknown = "unknown"

// The values of SelectionEvidence.State. Only two states exist, because a
// reader needs to know one thing: whether a zero count is a real zero.
const (
	EvidenceRead    = "read"
	EvidenceUnknown = "unknown"
)

// The values of BudgetLimit.Source.
const (
	BudgetFile    = "file"
	BudgetDefault = "default"
	BudgetFixed   = "fixed"
	BudgetUnread  = "unread"
)

// The units of the limits. A limit prints with its unit, because the three
// limits count different things.
const (
	unitPinsPerInjection    = "pins per injection"
	unitLessonsPerInjection = "lessons per injection"
	unitLessonsPerAnswer    = "lessons per answer"
)

// budgetHeading opens the budget lines. The budgets come from this read of
// lessons.yaml and a record stores none, so the heading warns about old counts.
const budgetHeading = "Selection budgets (current lessons.yaml; a past count can come from another budget):"

// selectionReading builds the selection part of the report from the two
// sources that it depends on. It needs no capture, so it reads the audit
// state and the config only.
func selectionReading(in Inputs, audit SourceRead) *SelectionReading {
	state := EvidenceUnknown
	if audit.State == StateRead {
		state = EvidenceRead
	}

	return &SelectionReading{
		Budgets:  selectionBudgets(in.Config, configRead(in).State),
		Evidence: SelectionEvidence{State: state},
	}
}

// selectionBudgets resolves the three limits. An invalid config gives no
// requested value, so its limits are unread. Selection then uses the
// defaults, so the effective values are still true. An absent config
// asks for nothing, so its limits read as defaults.
func selectionBudgets(cfg *reviews.Config, configState string) SelectionBudgets {
	unread := configState == ConfigInvalid
	if cfg == nil || unread {
		cfg = reviews.DefaultConfig()
	}

	hook := []string{surfaceHook, surfaceExplore}

	pinCap := BudgetLimit{
		Key: "pin_budget", Unit: unitPinsPerInjection, Surfaces: hook,
		Effective: min(cfg.HookPinBudget(), hookLessonTotal),
	}
	pinCap.Source, pinCap.Requested = requestedBudget(cfg.PinBudget, cfg.HookPinBudget(), unread)

	change := BudgetLimit{
		Key: "change_budget", Unit: unitLessonsPerAnswer,
		Surfaces:  []string{surfaceChangeSet, surfacePrepare, surfaceCheck},
		Effective: cfg.ChangeSetBudget(),
	}
	change.Source, change.Requested = requestedBudget(cfg.ChangeBudget, cfg.ChangeSetBudget(), unread)

	return SelectionBudgets{
		PinCap: pinCap,
		HookTotal: BudgetLimit{
			Source: BudgetFixed, Effective: hookLessonTotal, Unit: unitLessonsPerInjection,
			Surfaces: slices.Clone(hook),
		},
		ChangeBudget: change,
	}
}

// requestedBudget returns the source and the requested value of one limit.
// The config reads only a positive stored value, so any other value asks
// for the default.
func requestedBudget(stored, resolved int, unread bool) (source string, requested int) {
	switch {
	case unread:
		return BudgetUnread, 0
	case stored > 0:
		return BudgetFile, stored
	default:
		return BudgetDefault, resolved
	}
}

// SelectionLine returns the selection text of one pin version, one line per
// part. The console prints it and adds no word and no count, so the CLI and
// the console cannot differ. The budget lines read lessons.yaml, so they stay
// when the audit does not read. The count lines then stay out, because
// each one would print a zero that no record states. The bool is false
// when the report holds no reading of that version.
func SelectionLine(r *Report, pinVersion string) (string, bool) {
	if r == nil || r.Selection == nil {
		return "", false
	}

	reading, ok := readingOf(r, pinVersion)
	if !ok {
		return "", false
	}

	lines := append(budgetLines(r.Selection.Budgets), evidenceLine(*r))

	if r.Selection.Evidence.State == EvidenceRead {
		lines = append(lines, deliveryCountLine(reading.Delivery))

		if split := heldBackCauseLine(reading.Delivery); split != "" {
			lines = append(lines, split)
		}
	}

	for i, line := range lines {
		lines[i] = render.Sanitize(line)
	}

	return strings.Join(lines, "\n"), true
}

// readingOf finds the reading of a pin version. Duplicate entries of one
// version hold the same counts, so the first reading stands for all of them.
func readingOf(r *Report, pinVersion string) (PinReading, bool) {
	for _, group := range [][]PinReading{r.Pins, r.Historical} {
		for _, reading := range group {
			if reading.PinVersion == pinVersion {
				return reading, true
			}
		}
	}

	return PinReading{}, false
}

// budgetLines returns the heading and the three limit lines.
func budgetLines(b SelectionBudgets) []string {
	return []string{
		budgetHeading,
		pinCapLine(b.PinCap),
		fmt.Sprintf("total cap: fixed %d %s", b.HookTotal.Effective, b.HookTotal.Unit),
		limitLine("change budget", requestedText(b.ChangeBudget), b.ChangeBudget),
	}
}

// pinCapLine prints the pin cap. It names the fixed total when the requested
// value is above it, because the two numbers then differ.
func pinCapLine(l BudgetLimit) string {
	requested := requestedText(l)
	if l.Source == BudgetDefault {
		requested += " (default)"
	}

	line := limitLine("pin cap", requested, l)
	if l.Source == BudgetFile && l.Requested > l.Effective {
		line += "; pin_budget cannot raise the pin cap above the fixed total"
	}

	return line
}

// limitLine prints one limit with its unit.
func limitLine(name, requested string, l BudgetLimit) string {
	return fmt.Sprintf("%s: requested %s, effective %d %s", name, requested, l.Effective, l.Unit)
}

// requestedText prints a requested value. An unread limit has no number, and
// a zero would read as a request.
func requestedText(l BudgetLimit) string {
	if l.Source == BudgetUnread {
		return "unknown"
	}

	return strconv.Itoa(l.Requested)
}

// evidenceLine prints the evidence reading. An unknown reading names the
// audit state, so the reader sees why no count follows.
func evidenceLine(r Report) string {
	if r.Selection.Evidence.State == EvidenceRead {
		return "evidence read"
	}

	return unknownAuditText(r.AsOf.Audit.State)
}

// deliveryGap returns the text that replaces each delivery count of the
// report, or "" when the counts stand. An unfilled report has no evidence
// state to name, and only Build fills a report that the program prints.
func deliveryGap(r Report) string {
	if r.Selection == nil || r.Selection.Evidence.State == EvidenceRead {
		return ""
	}

	return unknownAuditText(r.AsOf.Audit.State)
}

// unknownAuditText is the text that stands in place of a count when the
// audit does not read.
func unknownAuditText(auditState string) string {
	return "unknown (audit " + auditState + ")"
}

// deliveryCountLine prints the record counts of one pin version. The report
// and SelectionLine share it, so the two strings cannot differ.
func deliveryCountLine(d DeliveryCounts) string {
	return fmt.Sprintf("delivery records: %s; injected %d; suppressed-repeat %d; held-back %d; emit-failed %d; returned %d",
		textCounts(d.Records), d.Injected, d.SuppressedRepeat, d.HeldBack, d.EmitFailed, d.Returned)
}

// heldBackCauseLine prints the split of the held-back records, sorted by
// surface and cause so that one reading prints one string. It is empty when
// the split has no entry.
func heldBackCauseLine(d DeliveryCounts) string {
	var parts []string

	for _, surface := range slices.Sorted(maps.Keys(d.HeldBackCauses)) {
		causes := d.HeldBackCauses[surface]
		for _, cause := range slices.Sorted(maps.Keys(causes)) {
			parts = append(parts, fmt.Sprintf("%s/%s %d", surface, cause, causes[cause]))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return "held back by cause (records): " + strings.Join(parts, ", ")
}
