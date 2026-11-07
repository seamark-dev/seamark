package lessonreport

// The next-step codes name existing controls. A suggestion never runs a
// command or changes a pin.
//
// The next steps are a fixed field: this build has no source for a
// step, so no reading carries one, and every next_steps list stays
// empty. The values stay, because the delivery/1 format names the closed
// set.
const (
	NextInspectEarlierDelivery    NextCode = "inspect-earlier-delivery"
	NextPrepareBeforeConstruction NextCode = "prepare-before-construction"
	NextReviewHookDeliveryAlways  NextCode = "review-hook-delivery-always"
	NextInspectLaneCoverage       NextCode = "inspect-lane-coverage"
	NextReviewRetarget            NextCode = "review-retarget"
	NextReviewPinRegions          NextCode = "review-pin-regions"
	NextInspectBudget             NextCode = "inspect-budget"
	NextInspectEmissionFailure    NextCode = "inspect-emission-failure"
	NextAssembleEvidence          NextCode = "assemble-evidence"
	NextAssessEvidence            NextCode = "assess-evidence"
	NextInspectEvidence           NextCode = "inspect-evidence"
	NextReviewInterpretation      NextCode = "review-interpretation"
	NextReviewCheckMode           NextCode = "review-check-mode"
)

// hookLessonTotal is the fixed lesson total of one hook injection. It repeats
// report.HookLessonTotal, so this package needs no import of report and its
// store dependencies. TestSelectionBudgetsFollowTheHookBudget keeps the two
// values equal.
const hookLessonTotal = 8

// expandCommand returns the command that prints the expansion of one pin.
// The text overview prints it under each pin reading.
func expandCommand(ref string) string {
	return "seamark lessons --delivery --pin " + ref
}
