package lessoncheck

import (
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the checker impact of a pin edit. A writer that
// changes the scope, the note, or the check binding of an executable pin
// shows the impact before the explicit apply. A person then reviews what
// the checkers do after the edit, not only the new text.

// ActiveRequested is the active state of a pin that is executable after
// the edit. The text is fixed, because ImpactOf does not read the hook
// installation state. A caller with the capability report shows
// CapabilityReport.ActiveText instead.
const ActiveRequested = "requested; inactive: not-installed"

// ActiveAdvisory is the active state of a pin that is advisory after the
// edit. It occurs only when an edit makes an executable pin advisory
// (lessons --set-mode).
const ActiveAdvisory = "not requested; mode advisory runs no check"

// Impact is the checker impact of one edited pin. ImpactOf fills it, and
// ImpactLines renders it, so every surface shows the same block.
type Impact struct {
	// Entry is the 1-based position of the pin in the pin list.
	Entry int
	// PinRef and Rule name the pin as the person knows it: the pin-ref and
	// the rule before the edit. Rule is sanitized and bounded.
	PinRef string
	Rule   string
	// Check is the bound check after the edit, else the check before it.
	// Kind and Version stay "" for an ID that names no definition.
	Check CheckRef
	// Mode is the normalized mode after the edit.
	Mode Mode
	// ScopeBefore and ScopeAfter are the checker scopes (AllRegions,
	// sorted). Each is non-nil, and empty means repository-wide.
	ScopeBefore, ScopeAfter []string
	// Predicate is the predicate text of Check (Describe), or the reason
	// that no checker can run it. PredicateChanged is false when the
	// checker scope, the check definition, and the executable state stay,
	// for example after a note-only change.
	Predicate        string
	PredicateChanged bool
	// VersionBefore and VersionAfter are the pin versions. The regions and
	// the note are parts of the version.
	VersionBefore, VersionAfter string
	// Active is the active state after the edit. ImpactOf sets
	// ActiveRequested or ActiveAdvisory. A caller that reads the
	// capability report replaces it with CapabilityReport.ActiveText.
	Active string
}

// ImpactOf compares the pins at entries in two decodes of lessons.yaml:
// the file before an edit and the candidate after it. entries holds
// 1-based positions in before.Pin, and the edit keeps each position in
// after.Pin. It returns one Impact per pin that is check or enforce before
// or after the edit, in the order of entries. An advisory pin gets none.
//
// The configs come from the lenient decode (reviews.ParseConfig), not
// from Validate. So a writer can show the impact before its strict tier
// refuses a candidate. The pin versions also stay deterministic for an
// invalid reference. ImpactOf returns an error for an entry outside
// either pin list, because a missing pin must never read as an advisory
// one.
func ImpactOf(before, after *reviews.Config, entries []int) ([]Impact, error) {
	var out []Impact

	for _, n := range entries {
		if n < 1 || n > len(before.Pin) || n > len(after.Pin) {
			return nil, fmt.Errorf("lessoncheck: pin entry %d is not in both pin lists (%d before, %d after)",
				n, len(before.Pin), len(after.Pin))
		}

		old, next := before.Pin[n-1], after.Pin[n-1]
		oldMode, _ := NormalizeMode(old.Mode)
		nextMode, _ := NormalizeMode(next.Mode)

		if !isExecutable(oldMode) && !isExecutable(nextMode) {
			continue
		}

		// The check after the edit decides the predicate. An edit that
		// keeps no check still names the check that it unbinds.
		checkCfg, checkID := after, next.Check
		if checkID == "" {
			checkCfg, checkID = before, old.Check
		}

		check := rawCheckRef(checkCfg, checkID)
		scopeBefore, scopeAfter := scopeOf(old), scopeOf(next)

		out = append(out, Impact{
			Entry:       n,
			PinRef:      before.ShortPinRef(reviews.NewPinKey(old.Rule, old.Region, old.Regions)),
			Rule:        ruleText(old.Rule, DefaultLimits().MaxRuleRunes),
			Check:       check,
			Mode:        nextMode,
			ScopeBefore: scopeBefore,
			ScopeAfter:  scopeAfter,
			Predicate:   predicateText(checkCfg, checkID),
			PredicateChanged: !slices.Equal(scopeBefore, scopeAfter) ||
				checkPart(before, old.Check) != checkPart(after, next.Check) ||
				isExecutable(oldMode) != isExecutable(nextMode),
			VersionBefore: before.PinVersion(old),
			VersionAfter:  after.PinVersion(next),
			Active:        activeText(nextMode),
		})
	}

	return out, nil
}

// checkPart identifies the check definition of a pin: its version, or a
// marked ID for an ID that names no definition. Two IDs with one
// definition give one part, so a renamed check keeps the predicate.
func checkPart(cfg *reviews.Config, id string) string {
	if id == "" {
		return ""
	}

	if def, ok := cfg.Checks[id]; ok {
		return reviews.CheckVersion(def)
	}

	return "unresolved:" + id
}

// predicateText returns the predicate of check id in cfg, or the reason
// that no checker can run it. The reason keeps the block complete for a
// candidate that the strict tier refuses later.
func predicateText(cfg *reviews.Config, id string) string {
	if id == "" {
		return "not available: the pin binds no check"
	}

	def, ok := cfg.Checks[id]
	if !ok {
		return fmt.Sprintf("not available: check %q is not defined under checks", render.Sanitize(id))
	}

	text, err := Describe(def)
	if err != nil {
		return "not available: " + render.Sanitize(err.Error())
	}

	return text
}

// activeText returns the active state of a pin with mode after the edit.
func activeText(mode Mode) string {
	if isExecutable(mode) {
		return ActiveRequested
	}

	return ActiveAdvisory
}

// ImpactLines renders the impact block of each impact, in order. The first
// line of a block names the pin, and the other lines start with two
// spaces. A nil or empty slice gives no lines, so a surface without an
// executable pin keeps its bytes. Every text from the file passes
// render.Sanitize.
func ImpactLines(impacts []Impact) []string {
	var lines []string

	for _, im := range impacts {
		lines = append(lines,
			fmt.Sprintf("pin %s %s: check %s, mode %s", im.PinRef, im.Rule, impactCheckText(im.Check), im.Mode),
			"  "+impactScopeText(im.ScopeBefore, im.ScopeAfter),
			"  "+impactPredicateText(im),
			fmt.Sprintf("  version %s -> %s", shortID(im.VersionBefore), shortID(im.VersionAfter)),
			"  active: "+im.Active,
		)
	}

	return lines
}

// impactCheckText names a check: "<id> (<kind>, <12 hex>)". An edit
// without a check gives "none". An ID that names no definition gives
// "<id> (not defined)". rawCheckRef sets the version of every written
// definition, so an empty version means that no definition exists.
func impactCheckText(c CheckRef) string {
	switch {
	case c.ID == "":
		return "none"
	case c.Version == "":
		return render.Sanitize(c.ID) + " (not defined)"
	}

	kind := render.Sanitize(string(c.Kind))
	if kind == "" {
		kind = "no kind"
	}

	return fmt.Sprintf("%s (%s, %s)", render.Sanitize(c.ID), kind, shortID(c.Version))
}

// impactScopeText returns "scope <old> -> <new>", or "scope unchanged".
func impactScopeText(before, after []string) string {
	if slices.Equal(before, after) {
		return "scope unchanged"
	}

	return "scope " + scopeText(before) + " -> " + scopeText(after)
}

// impactPredicateText returns "predicate: <text>", or "predicate
// unchanged" when the checkers evaluate the pin as before.
func impactPredicateText(im Impact) string {
	if !im.PredicateChanged {
		return "predicate unchanged"
	}

	return "predicate: " + im.Predicate
}
