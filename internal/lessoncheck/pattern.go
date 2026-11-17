package lessoncheck

import (
	"errors"
	"regexp"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds no-added-pattern/1. The checker matches the compiled
// pattern against each line that a change in scope adds. The line rules
// are in lines.go.

// patternMatch is one added line that matches the pattern: its evidence
// entry without the excerpt, and the line text. It does not hold the
// whole Change, because a pattern can match many lines and only the
// first MaxEvidencePerResult entries stay.
type patternMatch struct {
	evidence Evidence
	text     string
}

// evaluatePattern applies no-added-pattern/1 in its numbered order:
//
//  1. Each change that touches the scope, sorted by path and old path,
//     gives its added lines, or an unknown path when the content that it
//     needs is not text or not available.
//  2. Any match is a violation. The unknown paths stay in the result.
//  3. Else any unknown path gives unknown, with the reason that
//     reasonPrecedence picks.
//  4. Else any touching change gives pass.
//  5. Else the outcome is not-applicable.
//
// A path set that is not complete adds its cause to the causes of step
// 3. So it gives unknown before step 4: a pass needs the full path set,
// because no cut evaluation reads as pass.
func evaluatePattern(b budget, check Check, snap *Snapshot, scope []string) Result {
	var (
		res     Result
		matches []patternMatch
	)

	// Validate compiles the pattern of every valid definition. A
	// hand-built Check without one cannot run, so it fails closed.
	if check.Pattern == nil {
		return Result{Outcome: OutcomeUnknown, Reason: ReasonConfigurationError}
	}

	lim := snap.limits()
	changes := touching(snap, scope)

	for _, ch := range changes {
		n := endpointsInScope(ch, scope)
		res.Coverage.PathsInScope += n

		added, cause := addedLines(b, ch, scope, snap.Boundary, lim)
		if cause.reason != "" {
			res.UnknownPaths = append(res.UnknownPaths, unknownPath(ch, cause))

			continue
		}

		res.Coverage.PathsEvaluated += n
		res.Coverage.AddedLines += len(added)

		found, cause := matchAdded(b, check.Pattern, ch, added)
		matches = append(matches, found...)

		if cause.reason != "" {
			res.UnknownPaths = append(res.UnknownPaths, unknownPath(ch, cause))
		}
	}

	causes := unknownCauses(res.UnknownPaths, snap)

	switch {
	case len(matches) > 0:
		res.Outcome = OutcomeViolation
		res.Evidence, res.EvidenceOmitted = patternEvidence(matches, lim)
	case len(causes) > 0:
		res.Outcome = OutcomeUnknown
		res.Reason, res.Limit = pickReason(causes)
	case len(changes) > 0:
		res.Outcome = OutcomePass
	default:
		res.Outcome = OutcomeNotApplicable
	}

	return res
}

// matchBudgetStride is the number of lines between two checks of the
// budget. The check costs more than one match of a short line.
const matchBudgetStride = 4096

// matchAdded returns the added lines of one change that match the
// pattern. It returns the cause of the end of the budget when the budget
// ends before the last line. The matches found until then stay, because
// an observed violation stays observed.
func matchAdded(b budget, pattern *regexp.Regexp, ch Change, added []addedLine) ([]patternMatch, reasonHit) {
	var matches []patternMatch

	base := changeEvidence(ch)

	for i, line := range added {
		if i%matchBudgetStride == 0 {
			if cause, ended := b.ended(); ended {
				return matches, cause
			}
		}

		if pattern.MatchString(line.text) {
			e := base
			e.Line = line.number
			matches = append(matches, patternMatch{evidence: e, text: line.text})
		}
	}

	return matches, reasonHit{}
}

// addedLines returns the lines that one touching change adds to the
// scope. It returns a cause instead when the content that the change
// needs is not text or not available.
//
// A modify or an in-scope move whose content and type are unchanged adds
// no line and needs no content. A mode change alone adds no line, and a
// producer does not read the content of an identical move.
func addedLines(b budget, ch Change, scope []string, boundary Boundary, lim Limits) ([]addedLine, reasonHit) {
	// A path that starts after the budget ended is unknown, so a long
	// list of paths never extends the evaluation.
	if cause, ended := b.ended(); ended {
		return nil, cause
	}

	sourceIn := ch.OldPath != "" && inScope(ch.OldPath, scope)
	destIn := inScope(ch.Path, scope)

	switch {
	case ch.Kind == ChangeDelete:
		return nil, reasonHit{}
	case ch.Kind == ChangeAdd, ch.Kind == ChangeMove && !sourceIn:
		// A move into the scope from outside adds its whole content.
		if cause := afterCause(ch, boundary, lim); cause.reason != "" {
			return nil, cause
		}

		return everyLine(sideLines(ch.After)), reasonHit{}
	case ch.Kind == ChangeMove && !destIn:
		// A move out of the scope is a deletion from the scope.
		return nil, reasonHit{}
	case !ch.ContentChanged && !ch.TypeChanged:
		return nil, reasonHit{}
	}

	// A modify, or a move inside the scope with new content or type,
	// adds the diff of the before and the after content.
	if cause := sideCause(ch.Before, true, boundary, lim); cause.reason != "" {
		return nil, cause
	}

	if cause := afterCause(ch, boundary, lim); cause.reason != "" {
		return nil, cause
	}

	// The diff is quadratic in the worst case, so a long side stops the
	// evaluation of this path. The count comes before the split, so a
	// side above the limit is never split.
	if sideLineCount(ch.Before) > lim.MaxDiffLines || sideLineCount(ch.After) > lim.MaxDiffLines {
		return nil, reasonHit{reason: ReasonResourceLimit, limit: LimitMaxDiffLines}
	}

	return diffLinesWithin(b, sideLines(ch.Before), sideLines(ch.After))
}

// afterCause returns the cause that keeps the after side of a change
// unknown, or no cause when sideLines can read it. A projected change
// whose after content the projection cannot compute gives
// unprojectable-edit. addedLines checks the before side of a modify
// first, so its own cause, such as binary content, wins.
func afterCause(ch Change, boundary Boundary, lim Limits) reasonHit {
	if ch.unprojectable != "" {
		return reasonHit{reason: ReasonUnprojectableEdit}
	}

	return sideCause(ch.After, false, boundary, lim)
}

// sideCause returns the cause that keeps the lines of one side unknown,
// or no cause when sideLines can read them. before tells whether the
// side is the before state, because a missing before state is a missing
// baseline.
func sideCause(side Side, before bool, boundary Boundary, lim Limits) reasonHit {
	switch side.Type {
	case ObjectFile, ObjectSymlink:
	case ObjectGitlink, ObjectOther:
		return reasonHit{reason: ReasonUnsupportedContent}
	default:
		return unavailable(before, boundary)
	}

	switch side.Content {
	case ContentAvailable:
		// The producer classified the content. The checker repeats the
		// text test, because a pass must never rest on a wrong label.
		if textState(side.text) != ContentAvailable {
			return reasonHit{reason: ReasonUnsupportedContent}
		}

		return reasonHit{}
	case ContentBinary, ContentInvalidUTF8:
		return reasonHit{reason: ReasonUnsupportedContent}
	case ContentTooLarge:
		// A side that names its own limit keeps it. A pending record
		// drops content above max-baseline-bytes, and the size of the
		// side does not show that limit.
		if side.limit != "" {
			return reasonHit{reason: ReasonResourceLimit, limit: side.limit}
		}

		// A side above max-file-bytes is too large on its own. A smaller
		// side is too large because the snapshot reached its total.
		if side.Size > lim.MaxFileBytes {
			return reasonHit{reason: ReasonResourceLimit, limit: LimitMaxFileBytes}
		}

		return reasonHit{reason: ReasonResourceLimit, limit: LimitMaxSnapshotBytes}
	default:
		return unavailable(before, boundary)
	}
}

// sideLines returns the lines of a side that sideCause accepts. A link
// has its target as content, and the target is one line.
func sideLines(side Side) []string {
	if side.Type == ObjectSymlink {
		return []string{string(side.text)}
	}

	return splitLines(side.text)
}

// sideLineCount returns the line count of sideLines without the split.
// The max-diff-lines test uses it, so a long side costs no split.
func sideLineCount(side Side) int {
	if side.Type == ObjectSymlink {
		return 1
	}

	return lineCount(side.text)
}

// unavailable is the cause for content that a check needs and that the
// producer did not read. A before state that is missing at the
// working-tree or actual-edit boundary is a missing baseline. Every
// other missing content is content-unavailable.
func unavailable(before bool, boundary Boundary) reasonHit {
	if before && boundary != BoundaryProposedEdit {
		return reasonHit{reason: ReasonMissingBaseline}
	}

	return reasonHit{reason: ReasonContentUnavailable}
}

// patternEvidence turns the first MaxEvidencePerResult matches into
// evidence and counts the rest. Only the kept entries get an excerpt,
// so the cut matches cost no redaction.
func patternEvidence(matches []patternMatch, lim Limits) (kept []Evidence, omitted int) {
	matches, omitted = boundEntries(matches, lim.MaxEvidencePerResult)
	kept = make([]Evidence, 0, len(matches))

	for _, m := range matches {
		e := m.evidence
		e.Excerpt = excerpt(m.text, lim.MaxExcerptRunes)
		kept = append(kept, e)
	}

	return kept, omitted
}

// excerpt prepares one matching line for display. Redaction runs first,
// on the raw line, so a secret is gone before the cut. Sanitize removes
// control characters, and the cut bounds the length, because an excerpt
// goes to a terminal or an agent. The producer also uses it for the
// first git status stderr line, the text of the git-stderr notice,
// because that line is display text too.
func excerpt(line string, maxRunes int) string {
	return render.Truncate(render.Sanitize(redact.Secrets(line)), maxRunes)
}

// errNoPattern refuses to describe a no-added-pattern/1 definition
// without a pattern, because such a definition matches nothing useful
// and Validate refuses it.
var errNoPattern = errors.New("lessoncheck: no-added-pattern/1 requires a non-empty pattern")

// describePattern states the predicate of no-added-pattern/1.
func describePattern(def reviews.CheckDef) (string, error) {
	if def.Pattern == "" {
		return "", errNoPattern
	}

	return "violation if a line that a change adds in scope matches the Go regular expression '" +
		render.Sanitize(def.Pattern) + "'; a new file adds every line; binary or unreadable content gives unknown", nil
}
