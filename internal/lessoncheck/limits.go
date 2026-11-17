package lessoncheck

import "time"

// Limits holds the named limits of the lesson checks. Each field comment
// gives the limit name that a result or a snapshot reports.
//
// Only an evaluation limit gives unknown/resource-limit when a check
// exceeds it, because a cut evaluation must never read as pass.
// PendingMaxAge is a lifecycle limit: a baseline that the sweep discards
// gives missing-baseline. MaxEvidencePerResult, MaxExcerptRunes, and
// MaxRuleRunes cut display text only and never change an outcome.
//
// MaxSnapshotPaths bounds the status records. MaxCensusDirectories
// bounds the directory census of the working-tree snapshot. The census
// has its own bound, because a clean tree has many more directories
// than a change has records.
//
// Tests inject smaller values. DefaultLimits gives the default values.
// The defaults are assumptions that the maintainer has not confirmed yet.
// They stay provisional until measurements of the checkers confirm them.
type Limits struct {
	MaxSnapshotPaths     int           // max-snapshot-paths
	MaxCensusDirectories int           // max-census-directories
	MaxFileBytes         int64         // max-file-bytes
	MaxSnapshotBytes     int64         // max-snapshot-bytes
	MaxDiffLines         int           // max-diff-lines
	MaxGitOutputBytes    int64         // max-git-output-bytes
	VerifyTimeout        time.Duration // verify-timeout
	GuardBudget          time.Duration // guard-budget
	PostEditBudget       time.Duration // post-edit-budget
	MaxBaselineBytes     int64         // max-baseline-bytes
	PendingMaxAge        time.Duration // pending-max-age
	MaxEvidencePerResult int           // max-evidence-per-result
	MaxExcerptRunes      int           // max-excerpt-runes
	MaxRuleRunes         int           // max-rule-runes
}

// The names of the evaluation limits. Result.Limit and LimitHit.Name
// carry them, so a reader sees which limit made a result unknown.
const (
	LimitMaxSnapshotPaths     = "max-snapshot-paths"
	LimitMaxCensusDirectories = "max-census-directories"
	LimitMaxFileBytes         = "max-file-bytes"
	LimitMaxSnapshotBytes     = "max-snapshot-bytes"
	LimitMaxDiffLines         = "max-diff-lines"
	LimitMaxGitOutputBytes    = "max-git-output-bytes"
	LimitVerifyTimeout        = "verify-timeout"
	LimitGuardBudget          = "guard-budget"
	LimitPostEditBudget       = "post-edit-budget"
	LimitMaxBaselineBytes     = "max-baseline-bytes"
)

// mebibyte is the byte count of one MiB. The defaults give the byte
// limits in MiB.
const mebibyte = 1 << 20

// DefaultLimits returns the default limits.
func DefaultLimits() Limits {
	return Limits{
		MaxSnapshotPaths:     5000,
		MaxCensusDirectories: 50000,
		MaxFileBytes:         4 * mebibyte,
		MaxSnapshotBytes:     64 * mebibyte,
		MaxDiffLines:         20000,
		MaxGitOutputBytes:    64 * mebibyte,
		VerifyTimeout:        30 * time.Second,
		GuardBudget:          5 * time.Second,
		PostEditBudget:       10 * time.Second,
		MaxBaselineBytes:     16 * mebibyte,
		PendingMaxAge:        time.Hour,
		MaxEvidencePerResult: 10,
		MaxExcerptRunes:      160,
		MaxRuleRunes:         80,
	}
}

// orDefault returns DefaultLimits for a zero Limits and l otherwise.
// Each entry point calls it, because a zero bound cuts every evaluation.
func (l Limits) orDefault() Limits {
	if l == (Limits{}) {
		return DefaultLimits()
	}

	return l
}

// MaxPatternBytes bounds a check pattern at validation (limit name
// max-pattern-bytes). It is a variable only so that tests can lower it.
// No production code assigns it.
//
// It is not a Limits field. A too-long pattern is a configuration error
// (check-pattern-too-long), and every validator must agree on it: the
// pin writers, the guard, and setup. A Limits value can differ per
// caller, so it cannot hold this bound.
var MaxPatternBytes = 1024
