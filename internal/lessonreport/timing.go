package lessonreport

// This file holds the closed list of the decision timings. This build
// has no source for a decision timing, so every timing that the report
// prints reads unknown.

// The action-level timings.
const (
	TimingBeforeConstruction Timing = "before-construction"
	TimingSameCallOnly       Timing = "same-call-only"
	TimingUnknown            Timing = "unknown"
)

// PairTimingMixed is the timing of an occurrence whose actions disagree.
const PairTimingMixed PairTiming = "mixed"

// auditFileName is the name that a record reference, such as the start
// record of a window, prints before the line number.
const auditFileName = "lessons-audit.jsonl"
