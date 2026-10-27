package lessonreport

import (
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// CompactSummary is the fixed capture-delivery line, separate from any
// outcome reading. Command is also available to an expansion key.
// Surfaces print the complete line; its length depends on the counts.
type CompactSummary struct {
	PinVersion string
	Line       string
	Command    string
}

// Compact returns one version's capture reading. An unavailable archive
// or an unknown version gives no summary, so existing outcome text stays
// unchanged. Config entries take precedence over historical versions.
func Compact(r *Report, pinVersion string) (CompactSummary, bool) {
	if r == nil || r.Capture.State != CaptureAvailable || pinVersion == "" {
		return CompactSummary{}, false
	}

	for _, pins := range [][]PinReading{r.Pins, r.Historical} {
		for _, p := range pins {
			if p.PinVersion == pinVersion {
				return compactPin(r, p), true
			}
		}
	}

	return CompactSummary{}, false
}

func compactPin(r *Report, p PinReading) CompactSummary {
	ref := p.PinRef
	for _, current := range r.Pins {
		if current.PinIdentity == p.PinIdentity && current.PinRef != "" {
			ref = current.PinRef
			break
		}
	}

	if ref == "" {
		ref = p.PinIdentity[:min(reviews.ShownPinRef, len(p.PinIdentity))]
	}

	command := "seamark lessons --delivery --pin " + ref
	summary := CompactSummary{PinVersion: p.PinVersion, Command: command}

	if p.Occurrences.Occurrences == 0 && p.Delivery.Sessions == 0 {
		summary.Line = fmt.Sprintf("capture delivery: no assessed correction linked; %d delivered session(s) — %s",
			p.Delivery.Sessions, command)

		return summary
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

	summary.Line = fmt.Sprintf("capture delivery: %d assessed correction occurrence(s)", p.Occurrences.Occurrences)
	if len(classes) > 0 {
		summary.Line += " (" + strings.Join(classes, ", ") + ")"
	}

	if len(timings) > 0 {
		summary.Line += "; timing " + strings.Join(timings, ", ")
	}

	summary.Line += fmt.Sprintf(
		"; %d delivered session(s): %d no linked correction observed (qualified), %d incomplete — %s",
		p.Delivery.Sessions, p.Observation.NoLinkedCorrection, p.Observation.Incomplete, command,
	)

	return summary
}
