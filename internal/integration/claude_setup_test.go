package integration

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

// planClaude runs the Claude Code adapter alone.
func planClaude(t *testing.T, root string, req ClientSetup) ClientPlan {
	t.Helper()

	req.ClientID = ClaudeID

	plan, err := claudeSetup{}.Plan(root, testBinary, req)
	require.NoError(t, err)

	return plan
}

// writeFor returns the planned bytes for one document, parsed as JSON.
func writeFor(t *testing.T, plan ClientPlan, rel string) map[string]any {
	t.Helper()

	for _, w := range plan.Writes {
		if w.Path == rel {
			var doc map[string]any

			require.NoError(t, json.Unmarshal(w.After, &doc))

			return doc
		}
	}

	require.Failf(t, "no write", "the plan does not write %s", rel)

	return nil
}

func findingReasons(plan ClientPlan, level FindingLevel) []string {
	var out []string

	for _, f := range plan.Findings {
		if f.Level == level {
			out = append(out, f.Path+": "+f.Reason)
		}
	}

	return out
}

// commandsIn flattens the hook commands under one event.
func commandsIn(settings map[string]any, event string) []string {
	hookMap, _ := settings["hooks"].(map[string]any)
	entries, _ := hookMap[event].([]any)

	var out []string

	hooks.ForEachCommand(entries, func(_ string, _ map[string]any, cmd string) { out = append(out, cmd) })

	return out
}

func guardPaths(plan ClientPlan) []string {
	var out []string

	for _, g := range plan.Reads {
		out = append(out, g.Path)
	}

	return out
}

func TestClaudeHooksAndGrantsShareOneWrite(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", `{
  "model": "opus",
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "my-own-linter"}]}]},
  "permissions": {"allow": ["Bash(ls *)"], "deny": ["mcp__seamark__expand"]}
}`)

	plan := planClaude(t, root, ClientSetup{Hooks: true, ApproveTools: true})

	require.Len(t, plan.Writes, 1, "one document, one write")
	assert.Equal(t, "gate + lessons + context reset hooks; 7 allow rules", plan.Writes[0].Detail)

	settings := writeFor(t, plan, approve.ClaudeSettings)

	// Foreign content survives.
	assert.Equal(t, "opus", settings["model"])

	var stop []string

	hooks.ForEachCommand(settings["hooks"].(map[string]any)["Stop"].([]any),
		func(_ string, _ map[string]any, cmd string) { stop = append(stop, cmd) })
	assert.Equal(t, []string{"my-own-linter"}, stop, "the user's Stop hook stays")

	// The three hooks are present, in warn mode on a first install.
	for _, spec := range hooks.ClaudeSpecs(hooks.ModeWarn) {
		assert.True(t, hooks.Installed(settings, spec), spec.Marker)
	}

	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(settings))

	// The explicit deny stays, its rule is not added, and the plan says so.
	allow := approve.AllowSet(settings)
	assert.True(t, allow["Bash(ls *)"])
	assert.True(t, allow["mcp__seamark__orient"])
	assert.False(t, allow["mcp__seamark__expand"], "an allow rule cannot override a deny entry")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "permissions.deny lists mcp__seamark__expand")

	// .mcp.json was read for the server name only: guarded, not written.
	// The absent local file is guarded too, so its creation is noticed.
	assert.Equal(t, []string{approve.ClaudeSettings, claudeLocalSettings, approve.MCPConfig}, guardPaths(plan))
	assert.Empty(t, plan.Kept)
}

func TestClaudeGrantsUseTheRegisteredServerName(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".mcp.json", `{"mcpServers":{"sm":{"command":"/opt/seamark","args":["mcp"]}}}`)

	plan := planClaude(t, root, ClientSetup{ApproveTools: true})

	allow := approve.AllowSet(writeFor(t, plan, approve.ClaudeSettings))
	assert.True(t, allow["mcp__sm__orient"], "the rules are spelled with the name .mcp.json registers")
	assert.False(t, allow["mcp__seamark__orient"])

	// A read-only dependency still makes the plan stale when it changes.
	full := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, ApproveTools: true},
	}})
	writeRel(t, root, ".mcp.json", `{"mcpServers":{}}`)

	_, err := ApplySetup(full, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
}

func TestClaudeRegistrationWithoutGrants(t *testing.T) {
	root := t.TempDir()

	plan := planClaude(t, root, ClientSetup{RegisterMCP: true})

	require.Len(t, plan.Writes, 1)
	assert.Equal(t, approve.MCPConfig, plan.Writes[0].Path)
	assert.Equal(t, []string{approve.MCPConfig}, guardPaths(plan), "settings.json is not read without a settings intent")

	doc := writeFor(t, plan, approve.MCPConfig)
	assert.NotNil(t, doc["mcpServers"].(map[string]any)["seamark"])
}

func TestClaudeRegistrationConflictIsReportedAndKept(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".mcp.json", `{"mcpServers":{"seamark":{"command":"other-tool"}}}`)

	plan := planClaude(t, root, ClientSetup{RegisterMCP: true, ApproveTools: true})

	for _, w := range plan.Writes {
		assert.NotEqual(t, approve.MCPConfig, w.Path, "a foreign registration is never replaced")
	}

	assert.Contains(t, plan.Kept, FileKeep{Path: approve.MCPConfig, Detail: "seamark not registered"})

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], `runs another command ("other-tool")`)

	// The grants fall back to the conventional name.
	assert.True(t, approve.AllowSet(writeFor(t, plan, approve.ClaudeSettings))["mcp__seamark__why"])
}

func TestClaudeGateModeIsNeverChangedImplicitly(t *testing.T) {
	root := t.TempDir()

	enforced, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, GateMode: hooks.ModeEnforce},
	}}), ApplyOptions{})
	require.NoError(t, err)
	require.Len(t, enforced.Ops, 1)

	// No mode in the request keeps the installed enforcement.
	kept := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, kept.Writes)
	assert.Equal(t, []FileKeep{{Path: approve.ClaudeSettings, Detail: "nothing to add"}}, kept.Kept)

	// An explicit warn rewrites the hook in place and says what it removes.
	warn := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeWarn})
	settings := writeFor(t, warn, approve.ClaudeSettings)
	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(settings))

	pre := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	assert.Len(t, pre, 2, "the gate hook is rewritten, not duplicated")

	warnings := findingReasons(warn, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "removes --enforce")
}

func TestClaudeSkipsAHookTheLocalSettingsAlreadyRun(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --enforce --hook"}]}
]}}`)

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	settings := writeFor(t, plan, approve.ClaudeSettings)

	assert.Empty(t, hooks.InstalledGateMode(settings), "no second gate handler beside the local one")
	assert.True(t, hooks.LessonsHookInstalled(settings), "the other hooks are still installed")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], claudeLocalSettings+": already runs `/home/me/bin/seamark gate --enforce --hook`",
		"the finding quotes the command the local file really runs")

	assert.Contains(t, guardPaths(plan), claudeLocalSettings, "the duplicate source is a guarded input")

	for _, w := range plan.Writes {
		assert.NotEqual(t, claudeLocalSettings, w.Path, "the user's local file is never written")
	}
}

func TestClaudeStillUpdatesAnOwnedHookTheLocalFileDuplicates(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --hook"}]}
]}}`)
	writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/old/path/seamark gate --hook"}]}
]}}`)

	plan := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeEnforce})
	settings := writeFor(t, plan, approve.ClaudeSettings)

	// Only an add is skipped. The entry setup owns still takes the new
	// binary path and the requested mode.
	assert.Equal(t, hooks.ModeEnforce, hooks.InstalledGateMode(settings), "the enforce request is not dropped")
	assert.Contains(t, commandsIn(settings, "PreToolUse"), testBinary+" gate --enforce --hook")
	assert.NotContains(t, commandsIn(settings, "PreToolUse"), "/old/path/seamark gate --hook")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "runs `/home/me/bin/seamark gate --hook`")
	assert.Contains(t, warnings[0], "so it runs twice")
}

func TestClaudeInstallsTheHookForTheToolsTheLocalFileDoesNotCover(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Edit","hooks":[{"type":"command","command":"/home/me/bin/seamark lessons --hook"}]}
]}}`)

	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	settings := writeFor(t, plan, approve.ClaudeSettings)

	// A local hook for Edit alone must not leave Write without lessons.
	covered, _ := hooks.Covered(settings, lessons, hooks.ClaudeMatcher)
	assert.Equal(t, []string{"Write", "MultiEdit"}, covered, "only the uncovered tools are added, so Edit does not run twice")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "for Edit; setup adds the hook to .claude/settings.json for Write, MultiEdit only")

	// The result is complete and stable: a second run adds nothing and
	// has nothing to report.
	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true},
	}}), ApplyOptions{})
	require.NoError(t, err)

	again := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, again.Writes)
	assert.Empty(t, findingReasons(again, FindingWarning))
}

func TestClaudeReadsTheLocalMatcherByClaudeCodesRules(t *testing.T) {
	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]

	for matcher, tc := range map[string]struct {
		added []string
		note  string
	}{
		// A comma list names exact tools.
		"Edit, Write": {[]string{"MultiEdit"}, "for Edit, Write; setup adds the hook to .claude/settings.json for MultiEdit only"},
		// An unanchored expression: Edit$ fires for MultiEdit too.
		"Edit$": {[]string{"Write"}, "for Edit, MultiEdit; setup adds the hook to .claude/settings.json for Write only"},
		// Every tool is covered: nothing is added.
		"Edit|Write|MultiEdit": {nil, "setup does not add a second copy"},
		"Write|Edit$":          {nil, "setup does not add a second copy"},
	} {
		t.Run(matcher, func(t *testing.T) {
			root := t.TempDir()

			local, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
				"matcher": matcher,
				"hooks":   []any{map[string]any{"type": "command", "command": "/home/me/bin/seamark lessons --hook"}},
			}}}})
			require.NoError(t, err)
			writeRel(t, root, claudeLocalSettings, string(local))

			plan := planClaude(t, root, ClientSetup{Hooks: true})

			covered, _ := hooks.Covered(writeFor(t, plan, approve.ClaudeSettings), lessons, hooks.ClaudeMatcher)
			assert.Equal(t, tc.added, covered, "no tool runs the handler twice, and no tool is left without one")

			warnings := findingReasons(plan, FindingWarning)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], tc.note)
		})
	}
}

func TestClaudeReportsCoverageItCannotComplete(t *testing.T) {
	// The shared hook has a narrow matcher, by hand or from an earlier
	// run beside a local hook that is now gone. Setup never rewrites an
	// existing matcher, so it must say which tools have no handler.
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[
  {"matcher":"Write|MultiEdit","hooks":[{"type":"command","command":"/usr/local/bin/seamark lessons --hook"}]}
]}}`)

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], ".claude/settings.json: no hook runs `seamark lessons --hook` for Edit")

	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]
	covered, _ := hooks.Covered(writeFor(t, plan, approve.ClaudeSettings), lessons, hooks.ClaudeMatcher)
	assert.Equal(t, []string{"Write", "MultiEdit"}, covered, "the existing matcher is left as it is")
}

func TestALocalFileCreatedAfterThePlanMakesItStale(t *testing.T) {
	root := t.TempDir()

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true},
	}})

	// The planned shared gate hook would now be a second gate handler.
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --hook"}]}
]}}`)

	_, err := ApplySetup(plan, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
}

func TestClaudeIgnoresALocalEntryThatNeverFires(t *testing.T) {
	// A gate command under an Edit matcher, or with another hook type,
	// never runs on a shell command. It is not a duplicate.
	for name, local := range map[string]string{
		"wrong matcher": `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]}]}}`,
		"wrong type":    `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"prompt","command":"/bin/seamark gate --hook"}]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, claudeLocalSettings, local)

			plan := planClaude(t, root, ClientSetup{Hooks: true})

			assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(writeFor(t, plan, approve.ClaudeSettings)))
			assert.Empty(t, findingReasons(plan, FindingWarning))
		})
	}
}

func TestClaudeSetupContinuesPastABrokenLocalFile(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, "{ broken")

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(writeFor(t, plan, approve.ClaudeSettings)))

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], claudeLocalSettings+": not checked for duplicate seamark hooks")
	assert.NotContains(t, warnings[0], approve.ClaudeSettings+":", "the finding must not blame the shared file")
}

func TestClaudePlanRejectsWrongTypedFields(t *testing.T) {
	for body, intent := range map[string]ClientSetup{
		`{"hooks": []}`:                        {Hooks: true},
		`{"hooks": {"PreToolUse": "nope"}}`:    {Hooks: true},
		`{"permissions": {"allow": "nope"}}`:   {ApproveTools: true},
		`{"permissions": ["not", "a", "map"]}`: {ApproveTools: true},
	} {
		root := t.TempDir()
		writeRel(t, root, ".claude/settings.json", body)

		intent.ClientID = ClaudeID

		_, err := claudeSetup{}.Plan(root, testBinary, intent)
		require.ErrorContains(t, err, "refusing to overwrite", body)
	}

	_, err := claudeSetup{}.Plan(t.TempDir(), "", ClientSetup{ClientID: ClaudeID, Hooks: true})
	require.ErrorContains(t, err, "binary path is empty")
}

func TestClaudeInspection(t *testing.T) {
	state := func(root string) map[Capability]CapabilityState {
		out := map[Capability]CapabilityState{}

		for _, entry := range (claudeSetup{}).Inspect(root).Capabilities {
			require.NoError(t, entry.Validate())
			assert.Equal(t, TrustUnknown, entry.Trust, "setup never reports trust it cannot read")
			assert.NotEqual(t, VerificationVerified, entry.Verification.Level)

			out[entry.Capability] = entry.State
		}

		return out
	}

	root := t.TempDir()
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateAbsent, CapabilityToolGrants: StateAbsent,
	}, state(root))

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}}), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateCurrent, CapabilityToolGrants: StatePartial,
	}, state(root), "a registration alone approves nothing")

	_, err = ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, ApproveTools: true},
	}}), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, StateCurrent, state(root)[CapabilityToolGrants])

	conflict := t.TempDir()
	writeRel(t, conflict, ".mcp.json", `{"mcpServers":{"seamark":{"command":"other-tool"}}}`)
	assert.Equal(t, StateConflict, state(conflict)[CapabilityMCPRegistration])

	broken := t.TempDir()
	writeRel(t, broken, ".mcp.json", "{")
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateUnreadable, CapabilityToolGrants: StateUnreadable,
	}, state(broken))
}
