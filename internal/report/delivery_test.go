package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func captureReading(p model.Proposal, version string) lessonreport.PinReading {
	identity := reviews.PinIdentityDigest(reviews.ProposalKey(p))

	return lessonreport.PinReading{
		PinIdentity: identity, PinVersion: version, PinRef: identity[:12],
		Occurrences: lessonreport.OccurrenceCounts{Occurrences: 7,
			ByClass: map[lessonreport.PairClass]int{lessonreport.PairClass(lessonreport.ClassAtAction): 7}},
		Timing:      map[lessonreport.PairTiming]int{lessonreport.PairTiming(lessonreport.TimingSameCallOnly): 7},
		Delivery:    lessonreport.DeliveryCounts{Sessions: 9},
		Observation: lessonreport.Observation{NoLinkedCorrection: 2, Incomplete: 3},
	}
}

func TestCaptureDeliveryCurrentVersionsOnly(t *testing.T) {
	p := model.Proposal{ID: 1, Rule: "close-handles", Region: "api", Status: model.ProposalApplied}
	pin := captureReading(p, "current-version")
	r := &lessonreport.Report{Capture: lessonreport.CaptureState{State: lessonreport.CaptureAvailable},
		Pins: []lessonreport.PinReading{pin}}
	want, ok := lessonreport.Compact(r, pin.PinVersion)
	require.True(t, ok)
	assert.Equal(t, map[int64]string{p.ID: want.Line}, CaptureDelivery([]model.Proposal{p}, r))

	for _, status := range []string{model.ProposalProposed, model.ProposalDismissed, ""} {
		other := p
		other.Status = status
		assert.Empty(t, CaptureDelivery([]model.Proposal{other}, r), status)
	}

	// The old association remains inspectable, but cannot replace a current
	// config entry or supply its occurrence count after a semantic change.
	r.Historical = r.Pins
	r.Pins = nil
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))
	r.Pins = []lessonreport.PinReading{captureReading(p, "new-version")}
	r.Pins[0].Occurrences.Occurrences = 0
	r.Pins[0].Delivery.Sessions = 0
	lines := CaptureDelivery([]model.Proposal{p}, r)
	assert.Contains(t, lines[p.ID], "no assessed correction linked")
	assert.NotContains(t, lines[p.ID], "7 assessed")

	// Duplicate identities are unresolved even if their versions happen
	// to match; neither entry is safe to choose for a proposal.
	r.Pins = []lessonreport.PinReading{pin, pin}
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))
	r.Pins[1].PinVersion = "different-version"
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))
	r.Pins = []lessonreport.PinReading{pin, pin, pin}
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))
	r.Pins = []lessonreport.PinReading{captureReading(p, "")}
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))

	other := p
	other.ID, other.Rule = 2, "other-pin"
	r.Pins = []lessonreport.PinReading{pin, captureReading(other, "other-version")}
	assert.Len(t, CaptureDelivery([]model.Proposal{p, other}, r), 2)
	r.Capture.State = lessonreport.CaptureUnavailable
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, r))
	assert.Empty(t, CaptureDelivery([]model.Proposal{p}, nil))
}

func TestPrintProposalLedgerDeliveryLine(t *testing.T) {
	pending := []model.Proposal{{ID: 1, Rule: "pending", Status: model.ProposalProposed}}
	applied := []model.Proposal{{ID: 2, Rule: "close-handles", Status: model.ProposalApplied}}
	dismissed := []model.Proposal{{ID: 3, Rule: "dismissed", Status: model.ProposalDismissed}}
	r := &lessonreport.Report{Capture: lessonreport.CaptureState{State: lessonreport.CaptureAvailable},
		Pins: []lessonreport.PinReading{captureReading(applied[0], "current")}}
	delivery := CaptureDelivery(applied, r)[2]
	require.Greater(t, len([]rune(delivery)), 200)
	outcomeLine := (outcome.Reading{Exposed: true, Verdict: outcome.VerdictNotLanding, PostEvents: 2, Firings: 41}).Line()
	health := map[int64]ProposalHealth{2: {Outcome: outcomeLine}}

	render := func() string {
		var b strings.Builder
		PrintProposalLedger(&b, pending, applied, dismissed, nil, health)

		return b.String()
	}
	legacy := render()
	h := health[2]
	h.Delivery = delivery
	health[2] = h
	// Even an incorrectly supplied health map cannot show delivery for
	// a proposal which has not been applied.
	health[1] = ProposalHealth{Delivery: "pending-capture-must-not-print"}
	health[3] = ProposalHealth{Delivery: "dismissed-capture-must-not-print"}
	out := render()
	assert.Contains(t, out, "        "+outcomeLine+"\n        "+delivery+"\n")
	assert.Equal(t, legacy, strings.Replace(out, "        "+delivery+"\n", "", 1))
	assert.NotContains(t, out, "must-not-print")

	health[2] = ProposalHealth{Outcome: outcomeLine}
	assert.Equal(t, legacy, render(), "unavailable capture preserves every legacy byte")

	var a, b strings.Builder
	PrintProposalLedger(&a, nil, applied, nil, nil, nil)
	PrintProposalLedger(&b, nil, applied, nil, nil, map[int64]ProposalHealth{})
	assert.Equal(t, a.String(), b.String())
}

func TestPrintOutcomesKeepsCaptureDeliverySeparate(t *testing.T) {
	applied := []model.Proposal{{ID: 2, Rule: "close-handles", Status: model.ProposalApplied}}
	rd := outcome.Reading{Exposed: true, Verdict: outcome.VerdictNotLanding, PostEvents: 2, Firings: 41}
	readings := map[int64]outcome.Reading{2: rd}
	r := &lessonreport.Report{Capture: lessonreport.CaptureState{State: lessonreport.CaptureAvailable},
		Pins: []lessonreport.PinReading{captureReading(applied[0], "current")}}
	delivery := CaptureDelivery(applied, r)
	var legacy, added, empty strings.Builder
	PrintOutcomes(&legacy, applied, readings, nil)
	PrintOutcomes(&added, applied, readings, delivery)
	PrintOutcomes(&empty, applied, readings, map[int64]string{})
	want := "\npin outcomes — 1 measured: 0 working, 1 not landing, 0 untested\n" +
		fmt.Sprintf("  p%-4d %-34s %s\n", 2, "close-handles", rd.Line())
	assert.Equal(t, want, legacy.String())
	assert.Equal(t, want, empty.String())
	assert.Equal(t, want+"        "+delivery[2]+"\n", added.String())
	assert.Contains(t, added.String(), "recurred 2× since exposure (fired 41×)")
	assert.Contains(t, added.String(), "7 assessed correction occurrence(s)")
	assert.Contains(t, added.String(), "9 delivered session(s): 2 no linked correction observed (qualified), 3 incomplete")
	assert.Equal(t, rd, readings[2], "rendering cannot alter the recurrence reading")
}
