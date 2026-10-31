package lessonreport

import (
	"fmt"
	"strings"
)

// Compact returns the fixed lesson-delivery line of one version,
// separate from any outcome reading. The line ends with the expansion
// command. Surfaces print the complete line, and its length depends on
// the counts. An unavailable archive or an unknown version gives no
// line, so existing outcome text stays unchanged. Config entries take
// precedence over historical versions.
func Compact(r *Report, pinVersion string) (string, bool) {
	if r == nil || r.Capture.State != CaptureAvailable || pinVersion == "" {
		return "", false
	}

	for _, pins := range [][]PinReading{r.Pins, r.Historical} {
		for _, p := range pins {
			if p.PinVersion == pinVersion {
				return compactPin(r, p), true
			}
		}
	}

	return "", false
}

// compactPin builds the line of one reading. The command prefers the
// pin-ref of a current entry with the same identity, so every version of
// one identity prints the same command.
func compactPin(r *Report, p PinReading) string {
	ref := p.PinRef
	for _, current := range r.Pins {
		if current.PinIdentity == p.PinIdentity && current.PinRef != "" {
			ref = current.PinRef
			break
		}
	}

	command := expandCommand(shownPinRef(ref, p.PinIdentity))

	if p.Occurrences.Occurrences == 0 && p.Delivery.Sessions == 0 {
		return fmt.Sprintf("lesson delivery: no assessed correction linked; %d delivered session(s) — %s",
			p.Delivery.Sessions, command)
	}

	classes := []string{}

	for _, class := range []PairClass{
		PairClass(ClassAtAction), PairClass(ClassEarlierInContext), PairClass(ClassOtherLane),
		PairClass(ClassRegionNotMatched), PairClass(ClassNotEmitted), PairClass(ClassUnknown), PairMixed,
	} {
		if n := p.Occurrences.ByClass[class]; n > 0 {
			classes = append(classes, fmt.Sprintf("%s %d", class, n))
		}
	}

	timings := []string{}

	for _, timing := range []PairTiming{
		PairTiming(TimingBeforeConstruction), PairTiming(TimingSameCallOnly), PairTiming(TimingUnknown), PairTimingMixed,
	} {
		if n := p.Timing[timing]; n > 0 {
			timings = append(timings, fmt.Sprintf("%s %d", timing, n))
		}
	}

	line := fmt.Sprintf("lesson delivery: %d assessed correction occurrence(s)", p.Occurrences.Occurrences)
	if len(classes) > 0 {
		line += " (" + strings.Join(classes, ", ") + ")"
	}

	if len(timings) > 0 {
		line += "; timing " + strings.Join(timings, ", ")
	}

	return line + fmt.Sprintf(
		"; %d delivered session(s): %d %s, %d incomplete — %s",
		p.Delivery.Sessions, p.Observation.NoLinkedCorrection, NoLinkedCorrectionText, p.Observation.Incomplete, command,
	)
}
