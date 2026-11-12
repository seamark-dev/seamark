package status

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/integration"
)

func TestPrintGateNamesAGateHookThatNeverRuns(t *testing.T) {
	// A present gate hook that the client never runs is not "not
	// installed". A setup run does not repair a matcher, so the gate line
	// offers none. The clients line and the warning of that client already
	// name the cause and the repair, so the gate section does not repeat
	// them.
	idle := integration.Inspection{ClientID: "claude", Capabilities: []integration.CapabilityInspection{{
		Capability: integration.CapabilityCommands, Supported: true, State: integration.StatePartial, Inert: true,
		Detail: `gate hook never runs from .claude/settings.json: its matcher "Shell" does not fire for Bash`,
		Action: `set the matcher of that hook to "Bash" in .claude/settings.json`,
	}}}
	enforcing := integration.Inspection{ClientID: "codex", GateMode: "enforce", Capabilities: []integration.CapabilityInspection{{
		Capability: integration.CapabilityCommands, Supported: true, State: integration.StateCurrent,
	}}}

	// A Codex definition that may run a gate and a third hook document
	// that cannot be read each add a line. The inert hook adds none.
	enforcingMaybe := enforcing
	enforcingMaybe.PossibleGateMode = "warn"
	unreadable := integration.Inspection{ClientID: "shared", HookDocumentError: ".shared/hooks.json: boom",
		Capabilities: []integration.CapabilityInspection{{
			Capability: integration.CapabilityCommands, Supported: true, State: integration.StateUnreadable,
			Detail: "unreadable (.shared/hooks.json: boom)",
		}}}

	cases := []struct {
		name    string
		status  Status
		want    string
		without string
	}{
		{
			name:    "inert alone",
			status:  Status{GatePolicyMode: "warn", Clients: []integration.Inspection{idle}},
			want:    "gate           policy mode warn; no gate hook runs for shell commands\n",
			without: "`seamark init`, or",
		},
		{
			name:   "inert beside an enforcing gate",
			status: Status{GatePolicyMode: "warn", Clients: []integration.Inspection{idle, enforcing}},
			want:   "gate           enforce (codex hook carries --enforce; blocking verdicts exit 2)\n",
		},
		{
			name:   "inert under a broken policy",
			status: Status{GatePolicyError: "boom", Clients: []integration.Inspection{idle}},
			want:   "gate           POLICY BROKEN (boom); no operational gate hook\n",
		},
		{
			name:   "inert beside the unreadable and possible lines",
			status: Status{GatePolicyMode: "warn", Clients: []integration.Inspection{idle, enforcingMaybe, unreadable}},
			want: "gate           enforce (codex hook carries --enforce; blocking verdicts exit 2)\n" +
				"               hook configuration UNREADABLE for shared: unreadable (.shared/hooks.json: boom)\n" +
				"               a definition of codex may also run a gate hook; seamark cannot tell what it does\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printGate(&b, &tc.status)

			assert.Equal(t, tc.want, b.String())
			assert.NotContains(t, b.String(), "its matcher", "the clients line names the cause")
			assert.NotContains(t, b.String(), "set the matcher", "the warning of the client names the repair")

			if tc.without != "" {
				assert.NotContains(t, b.String(), tc.without)
			}
		})
	}
}
