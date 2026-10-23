package lessoncheck

import (
	"errors"

	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds no-added-pattern/1 (contract K3). The checker matches
// the compiled pattern against each line that a change in scope adds.
// The line rules are in lines.go.

// patternMatch is one added line that matches the pattern.
type patternMatch struct {
	change Change
	line   addedLine
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
// because no cut evaluation reads as pass (contract K3, Bounds).
func evaluatePattern(check Check, snap *Snapshot, scope []string) Result {
	var (
		res     Result
		matches []patternMatch
		causes  []reasonHit
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

		added, cause := addedLines(ch, scope, snap.Boundary, lim)
		if cause.reason != "" {
			res.UnknownPaths = append(res.UnknownPaths,
				Evidence{Path: ch.Path, OldPath: ch.OldPath, Change: ch.Kind, Reason: cause.reason, limit: cause.limit})
			causes = append(causes, cause)

			continue
		}

		res.Coverage.PathsEvaluated += n
		res.Coverage.AddedLines += len(added)

		for _, line := range added {
			if check.Pattern.MatchString(line.text) {
				matches = append(matches, patternMatch{change: ch, line: line})
			}
		}
	}

	if !snap.PathsComplete {
		causes = append(causes, incompletePaths(snap))
	}

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

// addedLines returns the lines that one touching change adds to the
// scope (the K3 table). It returns a cause instead when the content that
// the change needs is not text or not available.
//
// A modify or an in-scope move whose content and type are unchanged adds
// no line and needs no content. A mode change alone adds no line, and a
// producer does not read the content of an identical move.
func addedLines(ch Change, scope []string, boundary Boundary, lim Limits) ([]addedLine, reasonHit) {
	sourceIn := ch.OldPath != "" && inScope(ch.OldPath, scope)
	destIn := inScope(ch.Path, scope)

	switch {
	case ch.Kind == ChangeDelete:
		return nil, reasonHit{}
	case ch.Kind == ChangeAdd, ch.Kind == ChangeMove && !sourceIn:
		// A move into the scope from outside adds its whole content.
		after, cause := sideLines(ch.After, false, boundary, lim)
		if cause.reason != "" {
			return nil, cause
		}

		return everyLine(after), reasonHit{}
	case ch.Kind == ChangeMove && !destIn:
		// A move out of the scope is a deletion from the scope.
		return nil, reasonHit{}
	case !ch.ContentChanged && !ch.TypeChanged:
		return nil, reasonHit{}
	}

	// A modify, or a move inside the scope with new content or type,
	// adds the diff of the before and the after content.
	before, cause := sideLines(ch.Before, true, boundary, lim)
	if cause.reason != "" {
		return nil, cause
	}

	after, cause := sideLines(ch.After, false, boundary, lim)
	if cause.reason != "" {
		return nil, cause
	}

	if len(before) > lim.MaxDiffLines || len(after) > lim.MaxDiffLines {
		// The diff is quadratic in the worst case, so a long side stops
		// the evaluation of this path.
		return nil, reasonHit{reason: ReasonResourceLimit, limit: LimitMaxDiffLines}
	}

	return diffLines(before, after), reasonHit{}
}

// sideLines returns the lines of one side, or the cause that keeps them
// unknown. before tells whether the side is the before state, because
// a missing before state is a missing baseline (§5 row 11).
func sideLines(side Side, before bool, boundary Boundary, lim Limits) ([]string, reasonHit) {
	switch side.Type {
	case ObjectFile, ObjectSymlink:
	case ObjectGitlink, ObjectOther:
		return nil, reasonHit{reason: ReasonUnsupportedContent}
	default:
		return nil, unavailable(before, boundary)
	}

	switch side.Content {
	case ContentAvailable:
		// The producer classified the content. The checker repeats the
		// text test, because a pass must never rest on a wrong label.
		if textState(side.text) != ContentAvailable {
			return nil, reasonHit{reason: ReasonUnsupportedContent}
		}

		// A link has its target as content, and the target is one line.
		if side.Type == ObjectSymlink {
			return []string{string(side.text)}, reasonHit{}
		}

		return splitLines(side.text), reasonHit{}
	case ContentBinary, ContentInvalidUTF8:
		return nil, reasonHit{reason: ReasonUnsupportedContent}
	case ContentTooLarge:
		// A side above max-file-bytes is too large on its own. A smaller
		// side is too large because the snapshot reached its total.
		if side.Size > lim.MaxFileBytes {
			return nil, reasonHit{reason: ReasonResourceLimit, limit: LimitMaxFileBytes}
		}

		return nil, reasonHit{reason: ReasonResourceLimit, limit: LimitMaxSnapshotBytes}
	default:
		return nil, unavailable(before, boundary)
	}
}

// unavailable is the cause for content that a check needs and that the
// producer did not read. A before state that is missing at the
// working-tree or actual-edit boundary is a missing baseline. Every
// other missing content is content-unavailable (§5 row 11).
func unavailable(before bool, boundary Boundary) reasonHit {
	if before && boundary != BoundaryProposedEdit {
		return reasonHit{reason: ReasonMissingBaseline}
	}

	return reasonHit{reason: ReasonContentUnavailable}
}

// patternEvidence turns the first MaxEvidencePerResult matches into
// evidence and counts the rest. Only the kept entries get an excerpt.
func patternEvidence(matches []patternMatch, lim Limits) (kept []Evidence, omitted int) {
	all := make([]Evidence, 0, len(matches))

	for _, m := range matches {
		all = append(all, Evidence{Path: m.change.Path, OldPath: m.change.OldPath, Change: m.change.Kind, Line: m.line.number})
	}

	kept, omitted = boundEntries(all, lim.MaxEvidencePerResult)

	for i := range kept {
		kept[i].Excerpt = excerpt(matches[i].line.text, lim.MaxExcerptRunes)
	}

	return kept, omitted
}

// excerpt prepares one matching line for display. Redaction runs first,
// on the raw line, so a secret is gone before the cut. Sanitize removes
// control characters, and the cut bounds the length (contracts §1.6).
// The producer also uses it for the first git status stderr line, the
// text of the git-stderr notice, because that line is display text too
// (ruling D2).
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
