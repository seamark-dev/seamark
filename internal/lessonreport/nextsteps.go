package lessonreport

// The next-step codes name existing controls. A suggestion never runs a
// command or changes a pin.
//
// Each step reads a corrected action, an occurrence, or captured
// feedback. This build reads no capture archive, so no reading carries a
// step, and every next_steps list stays empty. The values stay, because
// the delivery/1 format names the closed set.
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
