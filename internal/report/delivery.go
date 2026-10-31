package report

import (
	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// CaptureDelivery maps applied rows to the unique current pin version
// read by Gather. Historical versions and ambiguous identities cannot
// stand in for a configured pin. This mapping does no source reads and
// leaves both the outcome and capture units unchanged.
func CaptureDelivery(applied []model.Proposal, r *lessonreport.Report) map[int64]string {
	if r == nil || r.Capture.State != lessonreport.CaptureAvailable {
		return nil
	}

	current := make(map[string]string, len(r.Pins))
	for _, pin := range r.Pins {
		if _, exists := current[pin.PinIdentity]; exists {
			current[pin.PinIdentity] = ""
		} else {
			current[pin.PinIdentity] = pin.PinVersion
		}
	}

	lines := make(map[int64]string)
	for _, p := range applied {
		if p.Status != model.ProposalApplied {
			continue
		}

		identity := reviews.PinIdentityDigest(reviews.ProposalKey(p))
		if line, ok := lessonreport.Compact(r, current[identity]); ok {
			lines[p.ID] = line
		}
	}

	return lines
}
