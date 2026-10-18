package reviews

import (
	"slices"

	"github.com/seamark-dev/seamark/internal/wording"
)

// CollapseRestatedPins drops the surfaced pins that restate an earlier
// pin. The caller's order sets "earlier", so the higher-ranked pin
// stays. The check runs against the kept pins only, so it is not
// transitive: a pin goes only when it restates a pin that stays.
//
// dropped lists the removed pins in input order. A selection sample
// shows them as restatements, and a count alone cannot name them.
// CollapseRestated gives the count for the "+N more" line.
//
// Bare linter-code pins collapse only on equal codes. wording.Topic owns
// that rule, so every deduplication surface treats codes the same way.
func CollapseRestatedPins(pins []SurfacedPin) (kept, dropped []SurfacedPin) {
	if len(pins) < 2 {
		return pins, nil
	}

	topics := make([]wording.Topic, 0, len(pins))
	// Fresh storage, not pins[:0]: an in-place filter would overwrite
	// the contents of the caller's slice.
	kept = make([]SurfacedPin, 0, len(pins))

	for _, sp := range pins {
		t := wording.New(sp.Pin.Rule, sp.Pin.Note)

		if slices.ContainsFunc(topics, t.Restates) {
			dropped = append(dropped, sp)

			continue
		}

		topics = append(topics, t)
		kept = append(kept, sp)
	}

	return kept, dropped
}
