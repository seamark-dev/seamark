package distill

import (
	"strconv"

	"github.com/seamark-dev/seamark/internal/model"
)

// The trigger question of a proposal is open or answered. The CLI
// backfill and the console extraction plan read the same predicate, so
// a model call is never bought twice for one question. The surfaces add
// their own exclusions on top of it, such as pin presence.

// TriggerReason says why the trigger question of a proposal is open or
// answered under the current TriggerPromptVersion.
type TriggerReason int

const (
	// TriggerOpen means the question was never answered.
	TriggerOpen TriggerReason = iota
	// TriggerOpenObsoleteNegative means the stored answer is a negative
	// answer to an older question. The current question asks again once.
	TriggerOpenObsoleteNegative
	// TriggerAnsweredPositive means validated trigger paths are stored.
	// A positive answer is never bought again, whatever its version.
	TriggerAnsweredPositive
	// TriggerAnsweredCurrentNegative means the current question was
	// answered and named no valid path.
	TriggerAnsweredCurrentNegative
)

// String returns a stable lower-case phrase for the reason. A surface
// can show it as the reason that a proposal is or is not asked.
func (r TriggerReason) String() string {
	switch r {
	case TriggerOpen:
		return "never asked"
	case TriggerOpenObsoleteNegative:
		return "negative answer to an older question"
	case TriggerAnsweredPositive:
		return "already answered with trigger paths"
	case TriggerAnsweredCurrentNegative:
		return "already answered negative under the current question"
	default:
		return "TriggerReason(" + strconv.Itoa(int(r)) + ")"
	}
}

// TriggerQuestionOpen reports whether the trigger question of p is open,
// and why. It reads only the trigger fields of p: the status and the pin
// file are the caller's checks. Stored paths close the question at any
// version. A negative answer closes it only at TriggerPromptVersion or
// later, because a newer question can change a negative answer.
func TriggerQuestionOpen(p model.Proposal) (bool, TriggerReason) {
	switch {
	case len(p.TriggerPaths) > 0:
		return false, TriggerAnsweredPositive
	case p.TriggerChecked > 0 && p.TriggerPromptVersion >= TriggerPromptVersion:
		return false, TriggerAnsweredCurrentNegative
	case p.TriggerChecked > 0:
		return true, TriggerOpenObsoleteNegative
	default:
		return true, TriggerOpen
	}
}
