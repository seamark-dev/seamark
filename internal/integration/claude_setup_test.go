package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
)

// planClaude runs the Claude Code adapter alone, with the intent of an
// explicit selection: the other hook sources are checked.
func planClaude(t *testing.T, root string, req ClientSetup) ClientPlan {
	t.Helper()

	req.ClientID, req.CheckHookSources = ClaudeID, true

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

// narrated runs the narrator of one planned document and returns its
// lines, the way init prints them after the write.
func narrated(t *testing.T, plan ClientPlan, rel string, status OpStatus) string {
	t.Helper()

	var out bytes.Buffer

	for _, w := range plan.Writes {
		if w.Path == rel {
			require.NotNil(t, w.Narrate, "%s has no narrator", rel)
			w.Narrate(&out, status)

			return out.String()
		}
	}

	for _, k := range plan.Kept {
		if k.Path == rel {
			require.NotNil(t, k.Narrate, "%s has no narrator", rel)
			k.Narrate(&out, OpKept)

			return out.String()
		}
	}

	require.Failf(t, "not planned", "the plan neither writes nor keeps %s", rel)

	return ""
}

// keptDetails maps each kept document to its detail.
func keptDetails(plan ClientPlan) map[string]string {
	out := map[string]string{}

	for _, k := range plan.Kept {
		out[k.Path] = k.Detail
	}

	return out
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
		covered, _ := hooks.Covered(settings, spec, hooks.ClaudeMatcher)
		assert.Equal(t, spec.Tools(), covered, spec.Marker)
	}

	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(settings))

	// The explicit deny stays, its rule is not added, and the plan says so.
	allow := approve.AllowSet(settings)
	assert.True(t, allow["Bash(ls *)"])
	assert.True(t, allow["mcp__seamark__orient"])
	assert.False(t, allow["mcp__seamark__expand"], "an allow rule cannot override a deny entry")

	// The kept entry is named on the allow-rule line, in init's words,
	// so it needs no separate finding.
	assert.Empty(t, findingReasons(plan, FindingWarning))

	lines := narrated(t, plan, approve.ClaudeSettings, OpApplied)
	assert.Contains(t, lines, "  updated .claude/settings.json (gate + lessons + context reset hooks)\n")
	assert.Contains(t, lines, "          PreToolUse Bash                "+testBinary+" gate --hook\n")
	assert.Contains(t, lines, "  approved 7 Claude Code allow rules in .claude/settings.json "+
		"(seamark MCP tools + skills; kept explicit settings: permissions.deny lists mcp__seamark__expand)\n")
	assert.Contains(t, lines, "          mcp__seamark__orient\n")

	preview := narrated(t, plan, approve.ClaudeSettings, OpPlanned)
	assert.Contains(t, preview, "  would update .claude/settings.json")
	assert.Contains(t, preview, "  would approve 7 Claude Code allow rules")

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
	assert.Equal(t, map[string]string{approve.ClaudeSettings: "nothing to add"}, keptDetails(kept))
	assert.Contains(t, narrated(t, kept, approve.ClaudeSettings, OpKept),
		"  kept    .claude/settings.json (seamark hooks already wired)\n")
	assert.Contains(t, narrated(t, kept, approve.ClaudeSettings, OpKept), testBinary+" gate --enforce --hook",
		"a kept file still lists the exact hook commands")

	// An explicit warn rewrites the hook in place and says what it removes.
	warn := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeWarn})
	settings := writeFor(t, warn, approve.ClaudeSettings)
	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(settings))

	pre := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	assert.Len(t, pre, 2, "the gate hook is rewritten, not duplicated")

	// The narrator reports the removed flag, in init's words.
	assert.Empty(t, findingReasons(warn, FindingWarning))
	assert.Contains(t, narrated(t, warn, approve.ClaudeSettings, OpApplied),
		"  note    removed --enforce from the gate hook: the hook follows .seamark/policy.yaml\n")
	assert.Contains(t, narrated(t, warn, approve.ClaudeSettings, OpPlanned), "  note    would remove --enforce")
}

func TestClaudeInstallsEveryHookAndReportsTheLocalDuplicate(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --enforce --hook"}]}
]}}`)

	plan := planClaude(t, root, ClientSetup{Hooks: true})
	settings := writeFor(t, plan, approve.ClaudeSettings)

	// The shared file is the one the team commits. It gets every hook,
	// whatever the personal file of the person who ran setup holds.
	assert.Equal(t, hooks.ModeWarn, hooks.InstalledGateMode(settings))
	assert.True(t, hooks.LessonsHookInstalled(settings))

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], claudeLocalSettings+": runs `/home/me/bin/seamark gate --enforce --hook`",
		"the finding quotes the command the local file really runs")
	assert.Contains(t, warnings[0], "so it runs twice for Bash")
	assert.Contains(t, warnings[0], "the local copy runs in another gate mode, and both apply",
		"a local --enforce still blocks under the shared warn hook")

	assert.Contains(t, guardPaths(plan), claudeLocalSettings, "the other hook source is a guarded input")

	// Both gate hooks reach the caller, each with its own mode, so the
	// gate summary can say that the run still blocks.
	assert.Equal(t, []GateHook{
		{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true},
		{Path: claudeLocalSettings, Mode: hooks.ModeEnforce},
	}, plan.GateHooks)

	full := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, CheckHookSources: true},
	}})
	require.Len(t, full.GateHooks, 2)
	assert.Equal(t, ClaudeID, full.GateHooks[1].ClientID, "the coordinator names the client")

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

func TestClaudeReportsTheDuplicateOnEveryRunUntilItIsRemoved(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Edit","hooks":[{"type":"command","command":"/home/me/bin/seamark lessons --hook"}]}
]}}`)

	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	covered, _ := hooks.Covered(writeFor(t, plan, approve.ClaudeSettings), lessons, hooks.ClaudeMatcher)
	assert.Equal(t, lessons.Tools(), covered, "the shared hook keeps its full matcher")

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, CheckHookSources: true},
	}}), ApplyOptions{})
	require.NoError(t, err)

	// The file is complete, so a second run writes nothing. The duplicate
	// is still there, so the run still says so.
	again := planClaude(t, root, ClientSetup{Hooks: true})
	assert.Empty(t, again.Writes)

	warnings := findingReasons(again, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "so it runs twice for Edit")
	assert.NotContains(t, warnings[0], "another gate mode")
}

func TestClaudeReadsTheLocalMatcherByClaudeCodesRules(t *testing.T) {
	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]

	for matcher, twice := range map[string]string{
		// A comma list names exact tools.
		"Edit, Write": "so it runs twice for Edit, Write",
		// An unanchored expression: Edit$ fires for MultiEdit too.
		"Edit$":                "so it runs twice for Edit, MultiEdit",
		"Edit|Write|MultiEdit": "so it runs twice for Edit, Write, MultiEdit",
		"*":                    "so it runs twice for Edit, Write, MultiEdit",
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
			assert.Equal(t, lessons.Tools(), covered, "the shared hook never depends on the local file")

			warnings := findingReasons(plan, FindingWarning)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], twice)
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
		{ClientID: ClaudeID, Hooks: true, CheckHookSources: true},
	}})

	// The planned shared gate hook would now be a second gate handler.
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --hook"}]}
]}}`)

	_, err := ApplySetup(plan, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.NoFileExists(t, filepath.Join(root, ".claude", "settings.json"))
}

func TestClaudeWithoutTheSourceCheckNeverReadsTheLocalFile(t *testing.T) {
	// The intent of an init run without --client. That form never read
	// the local file, and the file is personal: the shared settings that
	// the team commits must not depend on who ran init.
	root := t.TempDir()
	writeRel(t, root, claudeLocalSettings, `{"hooks":{"PreToolUse":[
  {"matcher":"Bash","hooks":[{"type":"command","command":"/home/me/bin/seamark gate --enforce --hook"}]},
  {"matcher":"Edit","hooks":[{"type":"command","command":"/home/me/bin/seamark lessons --hook"}]}
]}}`)

	plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
	require.NoError(t, err)

	settings := writeFor(t, plan, approve.ClaudeSettings)

	for _, spec := range hooks.ClaudeSpecs(hooks.ModeWarn) {
		covered, _ := hooks.Covered(settings, spec, hooks.ClaudeMatcher)
		assert.Equal(t, spec.Tools(), covered, "%s: every hook, for every tool", spec.Marker)
	}

	assert.Empty(t, plan.Findings)
	assert.Equal(t, []string{approve.ClaudeSettings}, guardPaths(plan), "the local file is not an input of this run")
	assert.Equal(t, []GateHook{{Path: approve.ClaudeSettings, Mode: hooks.ModeWarn, Managed: true}}, plan.GateHooks)

	// A broken local file is none of this run's business either.
	writeRel(t, root, claudeLocalSettings, "{ broken")

	_, err = claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
	require.NoError(t, err)
}

func TestClaudeReadsAnInputThroughALinkAndNeverWritesThroughOne(t *testing.T) {
	outside := t.TempDir()
	writeRel(t, outside, "mcp.json", `{"mcpServers":{"sm":{"command":"/opt/seamark","args":["mcp"]}}}`)
	writeRel(t, outside, "empty.json", `{"mcpServers":{}}`)

	// Grants only: .mcp.json names the server and is never written, so a
	// link is read through, as init always did.
	root := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "mcp.json"), filepath.Join(root, ".mcp.json")))

	plan := planClaude(t, root, ClientSetup{ApproveTools: true})
	assert.True(t, approve.AllowSet(writeFor(t, plan, approve.ClaudeSettings))["mcp__sm__orient"])

	// The registration is kept when the linked file already holds it.
	full := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}})
	assert.Empty(t, full.Writes)

	// A registration that would be written through the link is refused.
	linked := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "empty.json"), filepath.Join(linked, ".mcp.json")))

	_, err := PlanSetup(Builtin(), SetupRequest{Root: linked, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}})
	require.ErrorContains(t, err, ".mcp.json: symlink at .mcp.json")
	assert.Equal(t, `{"mcpServers":{}}`, readRel(t, outside, "empty.json"))
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
