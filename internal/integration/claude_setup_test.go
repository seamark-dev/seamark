package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	assert.Empty(t, repeatedPathReasons(plan.Findings), "a finding names its path once")

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

// The owned gate commands of the test binary, in both modes.
var (
	enforceGateCommand = testBinary + " " + hooks.GateMarker(hooks.ModeEnforce)
	warnGateCommand    = testBinary + " " + hooks.GateMarker(hooks.ModeWarn)
)

// hookEntry returns one PreToolUse entry with a command under matcher.
func hookEntry(matcher, command string) string {
	return `{"matcher":"` + matcher + `","hooks":[{"type":"command","command":"` + command + `"}]}`
}

// bashHook returns a hook document with one command under the Bash
// matcher. Claude Code and Codex share the shape.
func bashHook(command string) string {
	return `{"hooks":{"PreToolUse":[` + hookEntry("Bash", command) + `]}}`
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

// enforceLayouts are settings files whose owned gate hook enforces and
// fires for Bash under Claude Code's matcher rule. A substring test for
// "Bash" misses the star, empty, omitted, and expression matchers. A
// rule where the last command wins reads the layout with two gates as
// warn.
func enforceLayouts() map[string]string {
	enforce, warn := enforceGateCommand, warnGateCommand

	document := func(entries ...string) string {
		return `{"hooks":{"PreToolUse":[` + strings.Join(entries, ",") + `]}}`
	}

	return map[string]string{
		"star matcher":             document(hookEntry("*", enforce)),
		"empty matcher":            document(hookEntry("", enforce)),
		"omitted matcher":          document(`{"hooks":[{"type":"command","command":"` + enforce + `"}]}`),
		"regular expression":       document(hookEntry("Ba.*", enforce)),
		"anchored expression":      document(hookEntry("^Bash$", enforce)),
		"name list":                document(hookEntry("Bash|Edit", enforce)),
		"enforce first, warn last": document(hookEntry("Bash", enforce), hookEntry("Bash", warn)),
	}
}

func TestClaudeReRunKeepsEnforceUnderEveryFiringMatcher(t *testing.T) {
	// The plan reads the installed mode by the rule that inspection uses.
	// A plain re-run must never remove --enforce. Only an explicit warn
	// removes it, and the narrator then says so.
	enforce, warn := enforceGateCommand, warnGateCommand

	for name, body := range enforceLayouts() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, approve.ClaudeSettings, body)

			// The three readers of the managed mode give one answer.
			assert.Equal(t, hooks.ModeEnforce, claudeSetup{}.Inspect(root).ManagedGateMode)
			assert.Equal(t, hooks.ModeEnforce, claudeSetup{}.ManagedGateMode(root))

			plain := planClaude(t, root, ClientSetup{Hooks: true})
			commands := commandsIn(writeFor(t, plain, approve.ClaudeSettings), "PreToolUse")
			assert.Contains(t, commands, enforce)
			assert.NotContains(t, commands, warn, "a plain re-run keeps --enforce")
			assert.NotContains(t, narrated(t, plain, approve.ClaudeSettings, OpApplied), "--enforce from the gate hook")

			// An explicit warn still removes the flag, and the narrator says so.
			downgrade := planClaude(t, root, ClientSetup{Hooks: true, GateMode: hooks.ModeWarn})
			commands = commandsIn(writeFor(t, downgrade, approve.ClaudeSettings), "PreToolUse")
			assert.Contains(t, commands, warn)
			assert.NotContains(t, commands, enforce)
			assert.Contains(t, narrated(t, downgrade, approve.ClaudeSettings, OpApplied),
				"  note    removed --enforce from the gate hook")
		})
	}
}

func TestClaudeReRunReportsAnEnforceFlagThatTheMergeRemoves(t *testing.T) {
	// These owned gate commands never fire for Bash, so the installed mode
	// is warn or none, and a plain re-run writes warn. Merge rewrites every
	// owned command, also one under a matcher that never fires. The user
	// wrote that --enforce, so the narrator must report its removal.
	enforce, warn := enforceGateCommand, warnGateCommand

	for name, entries := range map[string]string{
		"a permission rule":         hookEntry("Bash(git:*)", enforce),
		"another tool name":         hookEntry("BashOutput", enforce),
		"an edit matcher":           hookEntry("Edit", enforce),
		"beside a firing warn hook": hookEntry("BashOutput", enforce) + "," + hookEntry("Bash", warn),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, approve.ClaudeSettings, `{"hooks":{"PreToolUse":[`+entries+`]}}`)
			assert.NotEqual(t, hooks.ModeEnforce, claudeSetup{}.ManagedGateMode(root), "no enforcing gate fires")

			plan := planClaude(t, root, ClientSetup{Hooks: true})
			assert.NotContains(t, commandsIn(writeFor(t, plan, approve.ClaudeSettings), "PreToolUse"), enforce)
			assert.Contains(t, narrated(t, plan, approve.ClaudeSettings, OpApplied),
				"  note    removed --enforce from the gate hook")
		})
	}
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
	assert.True(t, hooks.ManagedRuns(settings, hooks.ClaudeSpecs(hooks.ModeWarn)[1], testBinary, hooks.ClaudeMatcher),
		"the managed lessons hook fires for the edit tools")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], claudeLocalSettings+": also runs `/home/me/bin/seamark gate --enforce --hook` for Bash",
		"the finding quotes the command the local file really runs")
	assert.Contains(t, warnings[0], "so the hook runs twice")
	assert.Contains(t, warnings[0], "runs in enforce mode, and both apply",
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
	assert.Contains(t, warnings[0], "so the hook runs twice")
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
	assert.Contains(t, warnings[0], "for Edit; the managed lessons hook runs too, so the hook runs twice")
	assert.NotContains(t, warnings[0], "and both apply")
}

func TestClaudeReadsTheLocalMatcherByClaudeCodesRules(t *testing.T) {
	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1]

	for matcher, twice := range map[string]string{
		// A comma list names exact tools.
		"Edit, Write": "for Edit, Write; the managed lessons hook runs too",
		// An unanchored expression: Edit$ fires for MultiEdit too.
		"Edit$":                "for Edit, MultiEdit; the managed lessons hook runs too",
		"Edit|Write|MultiEdit": "for Edit, Write, MultiEdit; the managed lessons hook runs too",
		"*":                    "for Edit, Write, MultiEdit; the managed lessons hook runs too",
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

func TestClaudeSetupNamesALocalFileThatItCannotReadOnce(t *testing.T) {
	// The read error starts with the path, and the consumer prints the
	// path before the reason already.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(claudeLocalSettings)), 0o755))

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	assert.Equal(t, []string{claudeLocalSettings + ": not checked for duplicate seamark hooks: not a regular file"},
		findingReasons(plan, FindingWarning))
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
			assert.Equal(t, VerificationUnverified, entry.Verification.Level, "%s: no recorded native check", entry.Capability)

			if entry.Capability == CapabilitySkills {
				// Evidence only: the registry fills the directory state.
				continue
			}

			out[entry.Capability] = entry.State
		}

		return out
	}

	// The adapter reports its native documents: the registration, the
	// grants, and the three hooks. The registry adds the skills entry.
	absent := map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateAbsent, CapabilityToolGrants: StateAbsent,
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
	}

	root := t.TempDir()
	assert.Equal(t, absent, state(root))

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: ClaudeID, RegisterMCP: true},
	}}), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[Capability]CapabilityState{
		CapabilityMCPRegistration: StateCurrent, CapabilityToolGrants: StatePartial,
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
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
		CapabilityEdits: StateAbsent, CapabilityCommands: StateAbsent, CapabilityResets: StateAbsent,
	}, state(broken), "a broken .mcp.json says nothing about the hooks")
}

func TestClaudeKeepsAWrappedHookAndReportsIt(t *testing.T) {
	// The wrapper ends like seamark's own command. Setup used to take it
	// for its own and rewrote it to the bare command, without a word.
	root := t.TempDir()
	wrapped := "test -x /usr/local/bin/seamark && /usr/local/bin/seamark gate --enforce --hook"
	writeRel(t, root, ".claude/settings.json", `{"hooks": {"PreToolUse": [
  {"matcher": "Bash", "hooks": [{"type": "command", "command": "`+wrapped+`"}]}
]}}`)

	plan, err := claudeSetup{}.Plan(root, testBinary, ClientSetup{ClientID: ClaudeID, Hooks: true})
	require.NoError(t, err)

	require.Len(t, plan.Writes, 1)

	// The settings encoder writes "&" as an escape. The value is the same.
	after := strings.ReplaceAll(string(plan.Writes[0].After), `\u0026`, "&")
	assert.Contains(t, after, wrapped, "the wrapper stays as the user wrote it")
	assert.Contains(t, after, testBinary+" gate --hook", "the shared file still gets every managed hook")

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], wrapped)
	assert.Contains(t, warnings[0], "the managed gate hook runs too")
	assert.Contains(t, warnings[0], "runs twice")
	assert.Contains(t, warnings[0], "runs in enforce mode, and both apply")

	// The wrapped gate enforces, whatever mode the managed hook has. The
	// gate line of the run reads the modes from here.
	assert.ElementsMatch(t, []GateHook{
		{Path: ".claude/settings.json", Mode: "warn", Managed: true},
		{Path: ".claude/settings.json", Mode: "enforce"},
	}, plan.GateHooks)
}

func TestClaudeReadsTheShellOptionsOfAWrappedGate(t *testing.T) {
	// The reported defect: "--norc" holds the letter c, so the reader took
	// it for -c. The reader saw no gate in the enforcing wrapper. The run
	// then reported only the managed warn gate, and nothing about the
	// second one.
	root := t.TempDir()
	wrapped := "bash --norc -c '/usr/local/bin/seamark gate --enforce --hook'"
	writeRel(t, root, ".claude/settings.json", bashHook(wrapped))

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	assert.ElementsMatch(t, []GateHook{
		{Path: ".claude/settings.json", Mode: "warn", Managed: true},
		{Path: ".claude/settings.json", Mode: "enforce"},
	}, plan.GateHooks)

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "runs twice")
	assert.Contains(t, warnings[0], "runs in enforce mode, and both apply")

	insp := claudeSetup{}.Inspect(root)
	commands, _ := insp.Entry(CapabilityCommands)
	assert.Equal(t, "gate hook (enforce) runs from .claude/settings.json (not managed by setup)", commands.Detail)
	assert.Equal(t, "enforce", insp.GateMode)
}

func TestClaudeGateThatMayRunIsPossibleNotInstalled(t *testing.T) {
	// The reported defect: "echo seamark gate --enforce --hook" made the
	// inspection say "enforce". The reader cannot tell what an unknown
	// program does with the gate command, so the gate is one that may
	// run, with the mode it has when it runs, and nothing is known to
	// block.
	root := t.TempDir()
	printed := "echo /usr/local/bin/seamark gate --enforce --hook"
	writeRel(t, root, ".claude/settings.json", bashHook(printed))

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	assert.ElementsMatch(t, []GateHook{
		{Path: ".claude/settings.json", Mode: "warn", Managed: true},
		{Path: ".claude/settings.json", Mode: "enforce", Uncertain: true},
	}, plan.GateHooks)
	assert.Equal(t, GateSummary{Warn: []string{".claude/settings.json"}, Possible: []string{".claude/settings.json"}},
		SummarizeGateHooks(plan.GateHooks))

	warnings := findingReasons(plan, FindingWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "setup cannot tell")

	insp := claudeSetup{}.Inspect(root)
	commands, _ := insp.Entry(CapabilityCommands)
	assert.Equal(t, StatePartial, commands.State)
	assert.Equal(t, "gate hook (enforce) may run from .claude/settings.json: `"+printed+"`", commands.Detail)
	assert.Empty(t, insp.GateMode, "no definition certainly runs the gate")
	assert.Equal(t, "enforce", insp.PossibleGateMode)
	assert.Equal(t, GateSummary{Possible: []string{ClaudeID}}, SummarizeInspections([]Inspection{insp}))
	assert.Equal(t, "gate hook installed (mode unknown); "+commands.Detail, insp.DescribeHooks())
}

func TestClaudeWrappedGateThatDiscardsItsExitStatusIsReportOnly(t *testing.T) {
	// "|| true" makes the command exit 0, whatever the gate returns, so
	// no verdict blocks. The gate line must not say "enforce", and a
	// finding says what to change.
	root := t.TempDir()
	wrapped := "/usr/local/bin/seamark gate --enforce --hook || true"
	writeRel(t, root, ".claude/settings.json", bashHook(wrapped))

	plan := planClaude(t, root, ClientSetup{Hooks: true})

	assert.ElementsMatch(t, []GateHook{
		{Path: ".claude/settings.json", Mode: "warn", Managed: true},
		{Path: ".claude/settings.json", Mode: GateModeReportOnly},
	}, plan.GateHooks)

	discards := findingsWith(plan.Findings, "discards the exit status")
	require.Len(t, discards, 1)
	assert.Equal(t, FindingWarning, discards[0].Level)
	assert.Equal(t, ".claude/settings.json", discards[0].Path)
	assert.True(t, strings.HasPrefix(discards[0].Reason, "has `"+wrapped+"`"), "the reason never starts with the path")
	assert.NotContains(t, strings.Join(findingReasons(plan, FindingWarning), "\n"), "enforce mode",
		"the duplicate warning names no enforcing mode")

	// Inspection of the wrapper alone reads report-only and repeats the
	// finding.
	insp := claudeSetup{}.Inspect(root)
	commands, _ := insp.Entry(CapabilityCommands)
	assert.Equal(t, "gate hook (report-only) runs from .claude/settings.json (not managed by setup)", commands.Detail)
	assert.Equal(t, GateModeReportOnly, insp.GateMode)
	assert.Len(t, findingsWith(insp.Findings, "discards the exit status"), 1)

	// The personal file gets the same reading.
	root = t.TempDir()
	writeRel(t, root, claudeLocalSettings, bashHook(wrapped))

	plan = planClaude(t, root, ClientSetup{Hooks: true})
	assert.Contains(t, plan.GateHooks, GateHook{Path: claudeLocalSettings, Mode: GateModeReportOnly})

	discards = findingsWith(plan.Findings, "discards the exit status")
	require.Len(t, discards, 1)
	assert.Equal(t, claudeLocalSettings, discards[0].Path)
}
