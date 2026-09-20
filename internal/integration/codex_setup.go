package integration

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/render"
)

// codexSetup plans Codex's native configuration. The MCP registration
// and the per-tool approvals share .codex/config.toml, so one plan
// composes both into one write. The TOML algorithm stays in the approve
// package: it appends and inserts, and it never rewrites a user's line.
// The lifecycle hooks join with their native evidence in a later slice.
type codexSetup struct{}

// Plan composes every requested edit and writes nothing. The binary
// path is not used: the registration names the bare command, because
// the file is committed and shared between machines.
func (codexSetup) Plan(root, _ string, req ClientSetup) (ClientPlan, error) {
	if !req.RegisterMCP && !req.ApproveTools {
		return ClientPlan{}, nil
	}

	guard, data, err := ReadGuarded(root, approve.CodexConfig)
	if err != nil {
		return ClientPlan{}, err
	}

	config, err := approve.PlanCodexData(data, guard.Exists,
		approve.CodexOptions{Register: req.RegisterMCP, Approve: req.ApproveTools})
	if err != nil {
		return ClientPlan{}, err
	}

	plan := ClientPlan{Reads: []FileGuard{guard}}

	// The line init has always printed for this file. It names the
	// explicit settings that were kept, so they need no finding.
	narrate := func(w io.Writer, status OpStatus) { approve.NarrateCodex(w, config, status == OpPlanned) }

	if config.Changed() {
		plan.Writes = append(plan.Writes, FileWrite{
			Path: approve.CodexConfig, After: config.Document(data), Detail: codexWriteDetail(config), Narrate: narrate,
		})

		// Codex reads the project layer only after the user trusts the
		// project. Setup owns the file, never the trust decision.
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingInfo,
			Path:   approve.CodexConfig,
			Reason: "Codex reads this file only in a project the user trusts; setup never grants trust",
			Action: "open the project in Codex and accept its trust prompt",
		})
	} else {
		plan.Kept = append(plan.Kept, FileKeep{Path: approve.CodexConfig, Detail: codexKeptDetail(config), Narrate: narrate})
	}

	// A tool table needs the server table it belongs to. Without the
	// registration intent the approvals have nowhere to attach.
	if req.ApproveTools && !req.RegisterMCP && !config.Registered {
		plan.Findings = append(plan.Findings, Finding{
			Level:  FindingWarning,
			Path:   approve.CodexConfig,
			Reason: "seamark mcp is not registered, so no tool approval was added",
			Action: "register the server, then approve the tools",
		})
	}

	return plan, nil
}

// codexWriteDetail says what the write adds, in init's words.
func codexWriteDetail(config *approve.CodexPlan) string {
	var parts []string

	if config.Register {
		parts = append(parts, "registered seamark mcp")
	}

	if len(config.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("approved %d tools: %s", len(config.Missing), strings.Join(config.Missing, ", ")))
	}

	return strings.Join(parts, "; ")
}

// codexKeptDetail says why the file needs nothing.
func codexKeptDetail(config *approve.CodexPlan) string {
	if !config.Registered {
		return "seamark not registered"
	}

	return fmt.Sprintf("seamark registered as %q; %d/%d tools approved",
		render.Sanitize(config.Server), len(config.Approved), len(approve.Tools))
}

// Inspect reports the registration and the grants, offline. Both are
// pending verification: the native check for the generated file is
// defined and has not run against this version of the setup.
func (codexSetup) Inspect(root string) Inspection {
	evidence := VerificationEvidence{Level: VerificationPending, Surface: "project " + approve.CodexConfig}
	record := approve.InspectCodex(root)

	registration := CapabilityInspection{
		Capability: CapabilityMCPRegistration, Supported: true, Verification: evidence,
	}

	switch {
	case record.Err != "":
		registration.State, registration.Detail = StateUnreadable, render.Sanitize(record.Err)
	case record.Registered != "":
		registration.State = StateCurrent
		registration.Detail = fmt.Sprintf("registered as %q", render.Sanitize(record.Registered))
	case len(record.Conflicts) > 0:
		// Without a registration every conflict is about the registration:
		// the name is taken, or the layout cannot take the table.
		registration.State = StateConflict
		registration.Detail = render.Sanitize(strings.Join(record.Conflicts, "; "))
	default:
		registration.State = StateAbsent
	}

	return Inspection{
		ClientID:     CodexID,
		Capabilities: []CapabilityInspection{registration, inspectGrants(record, evidence)},
	}
}
