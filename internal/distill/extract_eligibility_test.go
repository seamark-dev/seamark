package distill

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestTriggerQuestionOpenStates(t *testing.T) {
	cases := []struct {
		name   string
		p      model.Proposal
		open   bool
		reason TriggerReason
	}{
		{"never asked", model.Proposal{}, true, TriggerOpen},
		{"version without a stamp is never asked",
			model.Proposal{TriggerPromptVersion: TriggerPromptVersion}, true, TriggerOpen},
		{"negative before versioning",
			model.Proposal{TriggerChecked: 100}, true, TriggerOpenObsoleteNegative},
		{"negative to an older question",
			model.Proposal{TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion - 1},
			true, TriggerOpenObsoleteNegative},
		{"negative to the current question",
			model.Proposal{TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion},
			false, TriggerAnsweredCurrentNegative},
		{"negative to a newer question",
			model.Proposal{TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion + 1},
			false, TriggerAnsweredCurrentNegative},
		{"positive to an older question",
			model.Proposal{TriggerPaths: []string{"api"}, TriggerChecked: 100, TriggerPromptVersion: 1},
			false, TriggerAnsweredPositive},
		{"positive without a stamp",
			model.Proposal{TriggerPaths: []string{"api"}}, false, TriggerAnsweredPositive},
		{"positive to the current question",
			model.Proposal{TriggerPaths: []string{"api"}, TriggerChecked: 100, TriggerPromptVersion: TriggerPromptVersion},
			false, TriggerAnsweredPositive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, reason := TriggerQuestionOpen(tc.p)

			assert.Equal(t, tc.open, open)
			assert.Equal(t, tc.reason, reason)
			assert.Equal(t, cliTriggerQuestionOpen(tc.p), open, "the predicate is the CLI rule")
		})
	}
}

// cliTriggerQuestionOpen is a verbatim copy of the rule the CLI loop
// applied before it called TriggerQuestionOpen.
func cliTriggerQuestionOpen(p model.Proposal) bool {
	if len(p.TriggerPaths) > 0 ||
		(p.TriggerChecked > 0 && p.TriggerPromptVersion >= TriggerPromptVersion) {
		return false // the CLI loop's "continue"
	}

	return true
}

func TestTriggerQuestionOpenIgnoresStatus(t *testing.T) {
	// The status is the caller's check: the predicate answers the same
	// for every status, so the CLI and the console can add their own.
	for _, status := range []string{model.ProposalProposed, model.ProposalApplied,
		model.ProposalDismissed, model.ProposalSuperseded} {
		open, reason := TriggerQuestionOpen(model.Proposal{Status: status})

		assert.True(t, open, status)
		assert.Equal(t, TriggerOpen, reason, status)
	}
}

func TestTriggerReasonStrings(t *testing.T) {
	assert.Equal(t, "never asked", TriggerOpen.String())
	assert.Equal(t, "negative answer to an older question", TriggerOpenObsoleteNegative.String())
	assert.Equal(t, "already answered with trigger paths", TriggerAnsweredPositive.String())
	assert.Equal(t, "already answered negative under the current question",
		TriggerAnsweredCurrentNegative.String())
	assert.Equal(t, "TriggerReason(9)", TriggerReason(9).String())
}
