package integration

import (
	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/render"
)

// inspectGrants converts an approval record into the tool-grants
// inspection. Claude Code and Codex share the record, so they share
// this conversion, and both clients classify a grant state the same way.
func inspectGrants(record approve.ClientApproval, evidence VerificationEvidence) CapabilityInspection {
	entry := CapabilityInspection{
		Capability:   CapabilityToolGrants,
		Supported:    true,
		Verification: evidence,
		Detail:       render.Sanitize(record.Describe()),
	}

	switch record.State() {
	case approve.StateUnreadable:
		entry.State = StateUnreadable
	case approve.StateConflicting:
		entry.State = StateConflict
	case approve.StatePartial:
		entry.State = StatePartial
	case approve.StateCurrent:
		entry.State = StateCurrent
	default:
		entry.State = StateAbsent
	}

	return entry
}
