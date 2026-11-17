package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// keepOneAction is the action of the warning about two managed Codex
// handlers of one hook.
const keepOneAction = "keep one of those handlers in .codex/hooks.json and remove the others"

func TestCodexInspectionNamesTwoManagedGateHandlers(t *testing.T) {
	// A timeout that the user set keeps the handlers apart in the file, but
	// Codex still runs both of them.
	timed := `{"matcher":"Bash","hooks":[{"type":"command","command":"` + codexWarnGate + `","timeout":20}]}`

	cases := map[string]struct {
		entries []string
		reason  string
	}{
		"two equal handlers": {
			[]string{hookEntry("Bash", codexWarnGate), hookEntry("Bash", codexWarnGate)},
			"has 2 handlers of the managed gate hook that run for Bash; Codex runs each of them, so the hook runs twice",
		},
		"a handler with its own timeout": {
			[]string{hookEntry("Bash", codexWarnGate), timed},
			"has 2 handlers of the managed gate hook that run for Bash; Codex runs each of them, so the hook runs twice",
		},
		"three handlers": {
			[]string{hookEntry("Bash", codexWarnGate), hookEntry("Bash", codexWarnGate), hookEntry("Bash", codexWarnGate)},
			"has 3 handlers of the managed gate hook that run for Bash; Codex runs each of them, so the hook runs 3 times",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			codexHooksWith(t, root, tc.entries...)

			insp := codexSetup{}.Inspect(root)

			found := warningsWith(insp.Findings, keepOneAction)
			require.Len(t, found, 1)
			assert.Equal(t, codexHooksFile, found[0].Path)
			assert.Equal(t, tc.reason, found[0].Reason)
			assert.Equal(t, StateCurrent, entryOf(t, insp, CapabilityCommands).State)
			assert.Equal(t, "gate (warn) + lessons hooks installed", insp.DescribeHooks())
		})
	}
}

func TestCodexSetupNamesTwoManagedGateHandlersThatStay(t *testing.T) {
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", codexWarnGate))

	plan := planCodex(t, root, ClientSetup{Hooks: true})

	planned := warningsWith(plan.Findings, keepOneAction)
	require.Len(t, planned, 1)
	assert.Contains(t, planned[0].Reason, "so the hook runs twice")

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: CodexID, Hooks: true},
	}}), ApplyOptions{})
	require.NoError(t, err)

	// Setup keeps every handler in the Codex form, so both stay.
	assert.Equal(t, 2, strings.Count(readRel(t, root, codexHooksFile), codexWarnGate))
	assert.Equal(t, planned, warningsWith(codexSetup{}.Inspect(root).Findings, keepOneAction))
}

func TestCodexSetupNamesADuplicateThatOnlyTheMergeStarts(t *testing.T) {
	// The second handler names a binary that is missing, so it runs only
	// after the merge rewrites it to the running binary.
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate),
		hookEntry("Bash", absentBinary+" "+hooks.CodexGateMarker(hooks.ModeWarn)))

	assert.Empty(t, warningsWith(codexSetup{}.Inspect(root).Findings, keepOneAction), "a handler that cannot start runs nothing")

	plan := planCodex(t, root, ClientSetup{Hooks: true})

	planned := warningsWith(plan.Findings, keepOneAction)
	require.Len(t, planned, 1)

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: CodexID, Hooks: true},
	}}), ApplyOptions{})
	require.NoError(t, err)

	assert.Equal(t, planned, warningsWith(codexSetup{}.Inspect(root).Findings, keepOneAction))
}

func TestCodexDuplicateWarningCountsOnlyTheCodexForm(t *testing.T) {
	// A running handler in the Claude Code form has its own warning, so
	// the duplicate warning does not name it a second time.
	root := t.TempDir()
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("Bash", claudeFormWarn))

	insp := codexSetup{}.Inspect(root)

	assert.Empty(t, warningsWith(insp.Findings, keepOneAction))
	assert.Len(t, findingsAt(insp.Findings, codexHooksFile, "in the Claude Code form"), 1)
}

func TestCodexNamesTwoManagedLessonsHandlers(t *testing.T) {
	root := t.TempDir()
	lessons := testBinary + " " + hooks.CodexLessonsMarker
	codexHooksWith(t, root, hookEntry("Bash", codexWarnGate), hookEntry("apply_patch", lessons))

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	insp := codexSetup{}.Inspect(root)

	for _, findings := range [][]Finding{plan.Findings, insp.Findings} {
		found := warningsWith(findings, keepOneAction)
		require.Len(t, found, 1)
		assert.Equal(t, "has 2 handlers of the managed lessons hook that run for apply_patch; "+
			"Codex runs each of them, so the hook runs twice", found[0].Reason)
	}

	assert.Equal(t, StateCurrent, entryOf(t, insp, CapabilityEdits).State)
}
