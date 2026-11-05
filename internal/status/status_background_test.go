package status

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
)

func TestPrintGateNamesAHookInTheBackground(t *testing.T) {
	// The owned gate runs in the background after a hand edit. Codex does
	// not wait for its verdict, so nothing blocks, and the line names
	// that cause.
	st, root := seededStore(t)

	require.NoError(t, inspecttest.Named("pending trust").Write(root))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"`+inspecttest.Binary+` gate --enforce --hook --client codex","async":true}]}]}}`), 0o644))

	s, err := gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "gate           report-only (codex hook runs in the background); nothing blocks")
	assert.NotContains(t, b.String(), "discards its exit status")

	inspection := func(id, mode string, background bool) integration.Inspection {
		return integration.Inspection{ClientID: id, GateMode: mode, GateBackground: background,
			Capabilities: []integration.CapabilityInspection{
				{Capability: integration.CapabilityCommands, Supported: true, State: integration.StateCurrent},
			}}
	}

	// Beside a warn hook, and under a broken policy, the line names the
	// cause of each report-only hook.
	b.Reset()
	Print(&b, &Status{GatePolicyMode: "enforce", Clients: []integration.Inspection{
		inspection("claude", "warn", false), inspection("codex", integration.GateModeReportOnly, true),
	}})
	assert.Contains(t, b.String(), "               the codex hook runs in the background, so it never blocks")

	b.Reset()
	Print(&b, &Status{GatePolicyError: "boom", Clients: []integration.Inspection{
		inspection("claude", integration.GateModeReportOnly, false), inspection("codex", integration.GateModeReportOnly, true),
	}})
	assert.Contains(t, b.String(), "the report-only hook discards its exit status or runs in the background (claude, codex)")
}
