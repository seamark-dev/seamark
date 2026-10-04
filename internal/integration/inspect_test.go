package integration_test

// The inspection tests run as an external package: the fixture matrix
// imports integration, and an in-package test importing the matrix
// would be an import cycle. Everything here goes through the exported
// API, which is what doctor, status, and init use.

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/skills"
)

// inspectFixture writes one fixture and returns the inspection of its
// client, checked against the fixture's typed expectations.
func inspectFixture(t *testing.T, f inspecttest.Fixture) (string, integration.Inspection) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, f.Write(root))

	inspections := inspecttest.Registry().Inspect(root)
	i := slices.IndexFunc(inspections, func(insp integration.Inspection) bool { return insp.ClientID == f.Client })
	require.GreaterOrEqual(t, i, 0, "%s: client %s is inspected", f.Name, f.Client)

	return root, inspections[i]
}

func TestInspectMatrix(t *testing.T) {
	for _, f := range inspecttest.Fixtures() {
		t.Run(f.Name, func(t *testing.T) {
			root, insp := inspectFixture(t, f)

			client, ok := inspecttest.Registry().Lookup(f.Client)
			require.True(t, ok)
			assert.Equal(t, client.Name, insp.Name)
			assert.Equal(t, client.Declared(), insp.Declared)

			// One entry per configured capability, in presentation order,
			// each valid and agreeing with the descriptor on support.
			var order []integration.Capability

			for _, entry := range insp.Capabilities {
				order = append(order, entry.Capability)
				require.NoError(t, entry.Validate(), "%s", entry.Capability)
				assert.Equal(t, client.Supports(entry.Capability), entry.Supported, "%s", entry.Capability)
			}

			assert.Equal(t, integration.ConfiguredCapabilities, order)

			for capability, want := range f.States {
				entry, ok := insp.Entry(capability)
				require.True(t, ok, "%s", capability)
				assert.Equal(t, want, entry.State, "%s: %s", capability, entry.Detail)
			}

			assert.Equal(t, f.GateMode, insp.GateMode)
			assert.Equal(t, f.PossibleGateMode, insp.PossibleGateMode)
			assert.Equal(t, f.ManagedGateMode, insp.ManagedGateMode)

			var reasons []string
			for _, finding := range insp.Findings {
				reasons = append(reasons, finding.Reason)
			}

			for _, want := range f.Findings {
				assert.True(t, slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, want) }),
					"finding %q among %q", want, reasons)
			}

			assert.Empty(t, integration.RepeatedPathReasons(insp.Findings), "a finding names its path once")

			rendered := inspecttest.Rendered(insp)
			for _, word := range f.Words {
				assert.Contains(t, rendered, word)
			}

			data, err := json.Marshal(insp)
			require.NoError(t, err)

			for _, secret := range f.Absent {
				assert.NotContains(t, rendered, secret)
				assert.NotContains(t, string(data), secret)
			}

			assert.Equal(t, client.SetupOps, insp.Setup, "narration reads what setup installs from the typed view")

			if f.Skills != "" {
				assert.Contains(t, skills.Details(inspecttest.Registry().InspectSkills(root)), f.Skills)
			}
		})
	}
}

func TestEveryFindingNamesItsPathOnceAcrossTheMatrix(t *testing.T) {
	// Every consumer prints "<Path>: <Reason>". The rule holds for the
	// inspection of every client and for the full setup plan of every
	// built-in client, in every state of the matrix. A state that stops
	// the plan has no plan findings to check, so the test counts the
	// plans it checks.
	reg := inspecttest.Registry()
	planned := map[string]int{}

	for _, f := range inspecttest.Fixtures() {
		root := t.TempDir()
		require.NoError(t, f.Write(root))

		for _, insp := range reg.Inspect(root) {
			assert.Empty(t, integration.RepeatedPathReasons(insp.Findings), "%s: %s inspection", f.Name, insp.ClientID)
		}

		for _, id := range integration.Builtin().IDs() {
			setups, err := integration.ExplicitSetups(reg, []string{id}, true, true, "")
			require.NoError(t, err)

			plan, err := integration.PlanSetup(reg, integration.SetupRequest{Root: root, Binary: inspecttest.Binary, Clients: setups})
			if err == nil {
				planned[id]++
				assert.Empty(t, integration.RepeatedPathReasons(plan.Findings), "%s: %s plan", f.Name, id)
			}
		}
	}

	for _, id := range integration.Builtin().IDs() {
		assert.Positive(t, planned[id], "the matrix checks at least one plan of %s", id)
	}
}

func TestManagedGateModeAgreesWithTheInspectionAcrossTheMatrix(t *testing.T) {
	// init reads the gate mode through the narrow method, and doctor and
	// status read the inspection. The two must give one answer for every
	// client in every state of the matrix.
	reg := inspecttest.Registry()

	for _, f := range inspecttest.Fixtures() {
		root := t.TempDir()
		require.NoError(t, f.Write(root))

		for _, insp := range reg.Inspect(root) {
			client, ok := reg.Lookup(insp.ClientID)
			require.True(t, ok)

			if client.Setup != nil {
				assert.Equal(t, insp.ManagedGateMode, client.Setup.ManagedGateMode(root), "%s: %s", f.Name, insp.ClientID)
			}
		}
	}
}

func TestInspectWritesNothing(t *testing.T) {
	// Inspection is offline and read-only: the tree after it is the
	// tree before it, whatever state the fixture left.
	for _, f := range inspecttest.Fixtures() {
		root := t.TempDir()
		require.NoError(t, f.Write(root))

		before := listTree(t, root)
		inspecttest.Registry().Inspect(root)
		assert.Equal(t, before, listTree(t, root), f.Name)
	}
}

// listTree lists every file with its size, for a before/after check.
func listTree(t *testing.T, root string) map[string]int64 {
	t.Helper()

	out := map[string]int64{}

	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		out[path] = info.Size()

		return nil
	}))

	return out
}

func TestInspectSkillsLabelsEveryConsumerOfASharedDirectory(t *testing.T) {
	root := t.TempDir()

	// The shared fixture: the per-client entry names the directory and
	// the other consumer, and an unsupported capability says so.
	_, shared := inspectFixture(t, inspecttest.Named("shared skills"))
	assert.Contains(t, inspecttest.Rendered(shared), ".agents/skills 3/3 current (shared with codex)")
	assert.Contains(t, inspecttest.Rendered(shared), "not supported")

	states := inspecttest.Registry().InspectSkills(root)
	require.Len(t, states, 2, "two destinations, three clients")
	assert.Equal(t, "claude", states[0].Client)
	assert.Equal(t, ".claude/skills", states[0].Dir)
	assert.Equal(t, "codex+shared", states[1].Client, "a shared directory names every client that reads it")
	assert.Equal(t, ".agents/skills", states[1].Dir)

	// The built-in registry labels the directories the way status and
	// doctor always did.
	builtin := integration.Builtin().InspectSkills(root)
	require.Len(t, builtin, 2)
	assert.Equal(t, "claude", builtin[0].Client)
	assert.Equal(t, "codex", builtin[1].Client)

	// Each consumer's own entry names the directory and the sharers, so
	// the two clients never report a state the other contradicts.
	for _, insp := range inspecttest.Registry().Inspect(root) {
		entry, ok := insp.Entry(integration.CapabilitySkills)
		require.True(t, ok)

		switch insp.ClientID {
		case integration.ClaudeID:
			assert.Equal(t, ".claude/skills not installed", entry.Detail)
		case integration.CodexID:
			assert.Equal(t, ".agents/skills not installed (shared with shared)", entry.Detail)
		case inspecttest.SharedID:
			assert.Equal(t, ".agents/skills not installed (shared with codex)", entry.Detail)
		}

		assert.Contains(t, entry.Action, "seamark init --skills")
	}
}

func TestInspectionJSONUsesTheEnumNames(t *testing.T) {
	_, insp := inspectFixture(t, inspecttest.Named("pending trust"))

	data, err := json.Marshal(insp)
	require.NoError(t, err)

	text := string(data)
	assert.Contains(t, text, `"client":"codex"`)
	assert.Contains(t, text, `"state":"current"`)
	assert.Contains(t, text, `"trust":"unknown"`)
	assert.Contains(t, text, `"level":"verified"`)
	assert.Contains(t, text, `"level":"pending"`)
	assert.Contains(t, text, `"level":"info"`)
	assert.Contains(t, text, `"gate_mode":"warn"`)
	assert.NotContains(t, text, `"state":1`, "an enum is written by name, never by number")

	var back integration.Inspection
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, insp, back, "the view survives the JSON round trip")

	// A name from a newer binary fails to decode instead of reading as
	// the zero value.
	var state integration.CapabilityState
	require.Error(t, json.Unmarshal([]byte(`"vanished"`), &state))

	var level integration.Verification
	require.Error(t, json.Unmarshal([]byte(`"7"`), &level))
}

func TestDescribeFallsBackToTheCapabilityVocabulary(t *testing.T) {
	cases := map[integration.CapabilityInspection]string{
		{Capability: integration.CapabilityMCPRegistration, Supported: true}:                                           "not registered",
		{Capability: integration.CapabilityToolGrants, Supported: true}:                                                "not configured",
		{Capability: integration.CapabilityEdits, Supported: true}:                                                     "not installed",
		{Capability: integration.CapabilityCommands}:                                                                   "not supported",
		{Capability: integration.CapabilityEdits, Supported: true, State: integration.StatePartial}:                    "partial",
		{Capability: integration.CapabilityEdits, Supported: true, Detail: "lessons hook installed"}:                   "lessons hook installed",
		{Capability: integration.CapabilityEdits, Supported: true, State: integration.StateUnreadable, Detail: "boom"}: "boom",
	}

	for entry, want := range cases {
		assert.Equal(t, want, entry.Describe(), "%+v", entry)
	}

	assert.Empty(t, integration.VerificationEvidence{}.Describe(), "unknown evidence claims nothing")
	assert.Equal(t, "not natively verified",
		integration.VerificationEvidence{Level: integration.VerificationUnverified}.Describe())
	assert.Equal(t, "native check pending (PreToolUse Bash)",
		integration.VerificationEvidence{Level: integration.VerificationPending, Surface: "PreToolUse Bash"}.Describe())
}

func TestDescribeHooksNamesWhatIsMissingForTheClientOnly(t *testing.T) {
	// The shared client declares edits and no commands: a gate hook is
	// not missing for it.
	_, shared := inspectFixture(t, inspecttest.Named("unknown verification"))
	assert.Equal(t, "lessons hook installed", shared.DescribeHooks())

	// Claude Code declares both: a lessons hook alone misses its gate
	// hook, and a gate hook alone misses its lessons hook.
	root := t.TempDir()
	writeSettings := func(matcher, marker string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
			[]byte(`{"hooks":{"PreToolUse":[{"matcher":"`+matcher+`","hooks":[`+
				`{"type":"command","command":"`+inspecttest.Binary+` `+marker+`"}]}]}}`), 0o644))
	}

	claude := func() integration.Inspection {
		return integration.Builtin().Inspect(root)[0]
	}

	writeSettings("Edit|Write|MultiEdit", "lessons --hook")
	assert.Equal(t, "lessons hook installed, gate hook missing; context reset hook missing", claude().DescribeHooks())

	writeSettings("Bash", "gate --enforce --hook")
	insp := claude()
	assert.Equal(t, "gate hook installed (enforce), lessons hook missing", insp.DescribeHooks())
	assert.Equal(t, "enforce", insp.GateMode)

	// A lessons hook whose matcher misses a tool is partial and says so.
	writeSettings("Edit", "lessons --hook")
	insp = claude()
	edits, _ := insp.Entry(integration.CapabilityEdits)
	assert.Equal(t, integration.StatePartial, edits.State)
	assert.Contains(t, insp.DescribeHooks(), "lessons hook runs for Edit only, not for Write, MultiEdit")
	assert.Contains(t, edits.Action, `set the hook's matcher to "Edit|Write|MultiEdit"`)
}

func TestHookEvidenceCoversBothClaudeFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))

	claude := func() integration.Inspection { return integration.Builtin().Inspect(root)[0] }
	shared := filepath.Join(root, ".claude", "settings.json")
	local := filepath.Join(root, ".claude", "settings.local.json")

	// A wrapper the shell certainly runs is a running gate that setup
	// does not manage; its enforce marker sets the mode, and the fact
	// is a typed finding, not a phrase to parse.
	wrapped := "test -x /usr/local/bin/seamark && /usr/local/bin/seamark gate --enforce --hook"
	require.NoError(t, os.WriteFile(shared,
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+wrapped+`"}]}]}}`), 0o644))

	insp := claude()
	commands, _ := insp.Entry(integration.CapabilityCommands)
	assert.Equal(t, integration.StateCurrent, commands.State)
	assert.Equal(t, "gate hook (enforce) runs from .claude/settings.json (not managed by setup)", commands.Detail)
	assert.Equal(t, "enforce", insp.GateMode)
	assert.Empty(t, insp.ManagedGateMode, "setup owns no gate hook here")
	assert.Equal(t, "gate hook installed (enforce), lessons hook missing", insp.DescribeHooks())
	require.Len(t, insp.Findings, 1)
	assert.Equal(t, integration.FindingInfo, insp.Findings[0].Level)
	assert.Contains(t, insp.Findings[0].Reason, "setup does not manage that definition")

	// The same wrapper in the personal file alone: still a running,
	// enforcing gate, named by its file.
	require.NoError(t, os.WriteFile(shared, []byte(`{}`), 0o644))
	require.NoError(t, os.WriteFile(local,
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+wrapped+`"}]}]}}`), 0o644))

	insp = claude()
	commands, _ = insp.Entry(integration.CapabilityCommands)
	assert.Equal(t, "gate hook (enforce) runs from .claude/settings.local.json (not managed by setup)", commands.Detail)
	assert.Equal(t, "enforce", insp.GateMode)

	// A personal lessons hook that matches Edit only covers one tool: the
	// hook is partial, not current, whatever file runs it.
	require.NoError(t, os.WriteFile(local,
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"/usr/local/bin/seamark lessons --hook"}]}]}}`), 0o644))

	insp = claude()
	edits, _ := insp.Entry(integration.CapabilityEdits)
	assert.Equal(t, integration.StatePartial, edits.State)
	assert.Equal(t, "lessons hook runs for Edit only, not for Write, MultiEdit", edits.Detail)

	// The managed hook beside a personal copy for some tools: the hook is
	// current, and the overlap is a warning that names the tools.
	require.NoError(t, inspecttest.Named("current").Write(root))

	insp = claude()
	edits, _ = insp.Entry(integration.CapabilityEdits)
	assert.Equal(t, integration.StateCurrent, edits.State)
	assert.Equal(t, "warn", insp.GateMode)
	assert.Equal(t, "warn", insp.ManagedGateMode)

	var twice []string
	for _, f := range insp.Findings {
		if f.Level == integration.FindingWarning {
			twice = append(twice, f.Reason)
		}
	}

	require.Len(t, twice, 1)
	assert.Contains(t, twice[0], "also runs `/usr/local/bin/seamark lessons --hook` for Edit")
	assert.Contains(t, twice[0], "the managed lessons hook runs too, so the hook runs twice")
}

func TestGateModeCombinesEveryCodexSource(t *testing.T) {
	// The managed warn hook and an enforcing inline hook both run: the
	// effective mode is enforce, the managed mode stays warn for setup.
	_, insp := inspectFixture(t, inspecttest.Named("inline enforce"))
	assert.Equal(t, "enforce", insp.GateMode)
	assert.Equal(t, "warn", insp.ManagedGateMode)

	commands, _ := insp.Entry(integration.CapabilityCommands)
	assert.Equal(t, "gate hook (enforce) installed", commands.Detail)
}

func TestInspectionRedactsCredentialsInHookCommands(t *testing.T) {
	_, insp := inspectFixture(t, inspecttest.Named("redacted wrapper"))

	data, err := json.Marshal(insp)
	require.NoError(t, err)
	assert.NotContains(t, string(data), inspecttest.Secret, "a credential in a wrapper never reaches JSON")
	assert.Contains(t, string(data), "[REDACTED]")

	commands, _ := insp.Entry(integration.CapabilityCommands)
	assert.NotContains(t, commands.Detail, inspecttest.Secret)
	assert.NotContains(t, insp.DescribeHooks(), inspecttest.Secret)

	for _, f := range insp.Findings {
		assert.NotContains(t, f.Reason, inspecttest.Secret)
	}
}

func TestDescribeVerificationGroupsTheInstalledSurfaces(t *testing.T) {
	_, codex := inspectFixture(t, inspecttest.Named("pending trust"))
	assert.Equal(t, "edits: natively verified on codex-cli 0.154.0 (PreToolUse apply_patch); "+
		"commands: native check pending (PreToolUse Bash)", codex.DescribeVerification(),
		"absent entries have nothing to have verified")

	_, claude := inspectFixture(t, inspecttest.Named("current"))
	assert.Equal(t, "skills, mcp-registration, tool-grants, edits, commands, resets: not natively verified",
		claude.DescribeVerification(), "an installed hook is not a checked one")

	_, shared := inspectFixture(t, inspecttest.Named("unknown verification"))
	assert.Empty(t, shared.DescribeVerification(), "unknown evidence claims nothing")

	_, absent := inspectFixture(t, inspecttest.Named("absent"))
	assert.Empty(t, absent.DescribeVerification())
}

func TestDescribeSupportFollowsTheDescriptor(t *testing.T) {
	reg := inspecttest.Registry()

	claude, _ := reg.Lookup(integration.ClaudeID)
	assert.Equal(t, "skills, hooks, command gate, MCP registration, tool grants, inference", claude.DescribeSupport())

	codex, _ := reg.Lookup(integration.CodexID)
	assert.Equal(t, claude.DescribeSupport(), codex.DescribeSupport())

	shared, _ := reg.Lookup(inspecttest.SharedID)
	assert.Equal(t, "skills", shared.DescribeSupport(), "declared setup operations only")

	assert.Equal(t, "nothing yet", integration.Client{}.DescribeSupport())
}

func TestMissingInvokerResolvesWithoutTheBinary(t *testing.T) {
	f := inspecttest.Named("missing invoker")
	root, _ := inspectFixture(t, f)

	cfg, err := agent.LoadConfig(root)
	require.NoError(t, err)

	spec, err := inspecttest.Registry().ResolveInvocation(cfg, root)
	require.NoError(t, err, "resolution is pure: no PATH lookup")
	assert.Equal(t, f.Invoker, spec.Name)
	assert.Equal(t, []string{"no-such-agent-binary-xyz"}, spec.Argv)
}

func TestEnforceWinsInsideOneWrapper(t *testing.T) {
	// A wrapper that runs the warn command and then the enforce command
	// blocks: the second run exits 2. The mode must not depend on which
	// marker the reader checks first.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	wrapped := "/usr/local/bin/seamark gate --hook; /usr/local/bin/seamark gate --enforce --hook"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"`+wrapped+`"}]}]}}`), 0o644))

	insp := integration.Builtin().Inspect(root)[0]
	assert.Equal(t, "enforce", insp.GateMode)

	commands, _ := insp.Entry(integration.CapabilityCommands)
	assert.Contains(t, commands.Detail, "gate hook (enforce)")
}

func TestPartialHookActionNamesTheDocumentThatHoldsIt(t *testing.T) {
	// A lessons hook only in the personal file, for Edit only: the
	// action names that file, where the matcher is, not the shared file.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"/usr/local/bin/seamark lessons --hook"}]}]}}`), 0o644))

	edits, _ := integration.Builtin().Inspect(root)[0].Entry(integration.CapabilityEdits)
	assert.Equal(t, integration.StatePartial, edits.State)
	assert.Equal(t, `set the hook's matcher to "Edit|Write|MultiEdit" in .claude/settings.local.json; `+
		"setup does not change the matcher of an existing hook", edits.Action)

	// The managed hook under a narrow matcher: the action names the
	// shared file, as before.
	require.NoError(t, os.Remove(filepath.Join(root, ".claude", "settings.local.json")))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"/usr/local/bin/seamark lessons --hook"}]}]}}`), 0o644))

	edits, _ = integration.Builtin().Inspect(root)[0].Entry(integration.CapabilityEdits)
	assert.Contains(t, edits.Action, "in .claude/settings.json;")
}

func TestCoverageAcrossSourcesIsComplete(t *testing.T) {
	// The shared hook covers Edit, a personal wrapper covers the rest:
	// every tool has a handler, so the hook is current and nothing is
	// missing, in inspection and in setup alike.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"`+inspecttest.Binary+` lessons --hook"}]}]}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Write|MultiEdit","hooks":[{"type":"command","command":"test -x `+
			inspecttest.Binary+` && `+inspecttest.Binary+` lessons --hook"}]}]}}`), 0o644))

	insp := integration.Builtin().Inspect(root)[0]
	edits, _ := insp.Entry(integration.CapabilityEdits)
	assert.Equal(t, integration.StateCurrent, edits.State, edits.Detail)

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
		Root: root, Binary: inspecttest.Binary,
		Clients: []integration.ClientSetup{{ClientID: integration.ClaudeID, Hooks: true, CheckHookSources: true}},
	})
	require.NoError(t, err)

	for _, f := range plan.Findings {
		assert.NotContains(t, f.Reason, "no hook runs", "init must not ask for a matcher that would deliver twice")
	}
}
