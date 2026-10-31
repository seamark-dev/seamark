package lessoncheck

import "github.com/seamark-dev/seamark/internal/reviews"

// This file holds forbid-path-change/1. The checker reads path and
// change metadata only, so it supports binary content and never needs a
// file read.

// evaluateForbid applies forbid-path-change/1 in its numbered order:
//
//  1. Any observed change that touches the scope is a violation.
//  2. Else an unobserved change in scope or a path set that is not
//     complete gives unknown, with the reason that reasonPrecedence
//     picks.
//  3. Else the outcome is not-applicable.
//
// The checker has no pass outcome: a pin that forbids changes holds when
// no change touches its scope. An unchanged path named by --file is not
// a change, so it gives not-applicable.
//
// At the working-tree boundary, only an observed difference counts as a
// change, so a violation never names a change that did not happen. A
// --file path below a symbolic link is a change that the producer did
// not observe (Change.unobserved). So it is an unknown path with reason
// content-unavailable and never evidence. A path whose Lstat failed for
// a reason other than absence is unobserved too. A change that git
// reports through a link is observed, so it stays evidence.
func evaluateForbid(_ budget, _ Check, snap *Snapshot, scope []string) Result {
	var (
		res      Result
		evidence []Evidence
	)

	for _, ch := range touching(snap, scope) {
		n := endpointsInScope(ch, scope)
		res.Coverage.PathsInScope += n

		if ch.unobserved {
			res.UnknownPaths = append(res.UnknownPaths, unknownPath(ch, reasonHit{reason: ReasonContentUnavailable}))

			continue
		}

		// Every end in scope is decided: the change itself is the finding.
		res.Coverage.PathsEvaluated += n

		evidence = append(evidence, changeEvidence(ch))
	}

	causes := unknownCauses(res.UnknownPaths, snap)

	switch {
	case len(evidence) > 0:
		// The unknown paths stay in the result beside the evidence.
		res.Outcome = OutcomeViolation
		res.Evidence, res.EvidenceOmitted = boundEntries(evidence, snap.limits().MaxEvidencePerResult)
	case len(causes) > 0:
		res.Outcome = OutcomeUnknown
		res.Reason, res.Limit = pickReason(causes)
	default:
		res.Outcome = OutcomeNotApplicable
	}

	return res
}

// describeForbid states the predicate of forbid-path-change/1.
func describeForbid(reviews.CheckDef) (string, error) {
	return "violation if any change touches a file in scope: an add, a content change, " +
		"a deletion, a file mode or type change, or either end of a move; content is not read", nil
}
