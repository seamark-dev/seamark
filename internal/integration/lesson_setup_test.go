package integration

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// lessonPatternYAML is a valid lessons.yaml with an enforce pin that has
// a no-added-pattern/1 check. The Codex guard cannot project an update
// with hunks in its scope (P0 ruling A8).
const lessonPatternYAML = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: enforce
    check: no-todo
`

// applyLessonSetup plans and applies one setup run and returns the
// narration of every operation and the findings of the plan.
func applyLessonSetup(t *testing.T, root string, setups ...ClientSetup) (string, []Finding) {
	t.Helper()

	return applyLessonSetupWithBinary(t, root, testBinary, setups...)
}

func applyLessonSetupWithBinary(t *testing.T, root, binary string, setups ...ClientSetup) (string, []Finding) {
	t.Helper()

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: binary, Clients: setups})

	var out bytes.Buffer

	_, err := ApplySetup(plan, ApplyOptions{Observe: func(op OpResult) {
		if op.Narrate != nil {
			op.Narrate(&out, op.Status)
		}
	}})
	require.NoError(t, err)

	return out.String(), plan.Findings
}

func TestSetupLessonChecksLegacyEnforceReadsLocalBlocker(t *testing.T) {
	root := t.TempDir()
	local := `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[` +
		`{"type":"command","command":"/opt/bin/seamark lessons --guard --hook || true"}]}]}}`
	writeRel(t, root, claudeLocalSettings, local)
	legacy, err := LegacySetups(root, "", false, "")
	require.NoError(t, err)
	before := treeFiles(t, root)
	plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary,
		Clients: WithLessonChecks(legacy, hooks.LessonChecksEnforce, "")})
	require.ErrorContains(t, err, "report-only-wrapper")
	assert.Nil(t, plan)
	assert.Equal(t, before, treeFiles(t, root), "refusal precedes every write")

	plan = mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: legacy})
	for _, read := range plan.Reads {
		assert.NotEqual(t, claudeLocalSettings, read.Path, "plain legacy init keeps its no-local-read behavior")
	}
	_, err = ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, local, readRel(t, root, claudeLocalSettings))
}

func TestSetupLessonChecksSummaryUsesPreservedPaths(t *testing.T) {
	root := t.TempDir()
	binary := fakeSeamark(t, t.TempDir(), "self")
	installLessonHooks(t, root, ClaudeID, binary, hooks.LessonChecksEnforce, hooks.LessonExplorationOn)
	document := parseRel(t, root, approve.ClaudeSettings)
	hookMap := document["hooks"].(map[string]any)
	for _, value := range hookMap {
		entries, _ := value.([]any)
		for _, entry := range entries {
			hooks.ForEachCommand([]any{entry}, func(_ string, _ map[string]any, command string) {
				if hooks.OwnedBySeamark(command, []string{hooks.LessonGuardMarker, hooks.LessonVerifyMarker}) {
					entry.(map[string]any)["matcher"] = "Edit"
				}
			})
		}
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	writeRel(t, root, approve.ClaudeSettings, string(data))

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: binary, Clients: []ClientSetup{{
		ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce,
	}}})
	var out bytes.Buffer
	_, err = ApplySetup(plan, ApplyOptions{Observe: func(op OpResult) {
		if op.Narrate != nil {
			op.Narrate(&out, op.Status)
		}
	}})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "pre-write Write inactive (tool-uncovered)")
	assert.Contains(t, out.String(), "post-edit MultiEdit inactive (tool-uncovered)")
	assert.Contains(t, out.String(), "PreToolUse Edit                "+binary+" "+hooks.LessonGuardMarker)
	assert.NotContains(t, out.String(), "PreToolUse Edit|Write|MultiEdit "+binary+" "+hooks.LessonGuardMarker)
	assert.Equal(t, document, parseRel(t, root, approve.ClaudeSettings), "setup preserves user matchers")

	report := LessonCapability([]Inspection{inspectionOf(t, root, ClaudeID)}, root, BinaryIdentity{Path: binary})
	client, ok := report.Client(ClaudeID)
	require.True(t, ok)
	for _, path := range client.Paths {
		if path.State == lessoncheck.PathActive {
			continue
		}
		assert.Contains(t, out.String(), (lessonPath{stage: path.HookStage, tool: path.Tool,
			state: path.State, reasons: path.Reasons}).String())
	}
}

func TestSetupLessonChecksCodexRefusesSecondHandler(t *testing.T) {
	for _, marker := range []string{hooks.CodexLessonBaselineMarker, hooks.CodexLessonGuardMarker, hooks.CodexLessonVerifyMarker} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			event := "PreToolUse"
			if marker == hooks.CodexLessonVerifyMarker {
				event = "PostToolUse"
			}
			inline := "[[hooks." + event + "]]\nmatcher = \"apply_patch\"\n[[hooks." + event + ".hooks]]\ntype = \"command\"\n" +
				"command = \"/opt/bin/seamark " + marker + "\"\n"
			writeRel(t, root, ".codex/config.toml", inline)
			before := treeFiles(t, root)
			plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
				ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck,
			}}})
			require.ErrorContains(t, err, "lesson checks: codex")
			assert.ErrorContains(t, err, ".codex/config.toml [hooks]")
			assert.ErrorContains(t, err, "second handler")
			assert.Nil(t, plan)
			assert.Equal(t, before, treeFiles(t, root))
		})
	}

	t.Run("a certain wrapper in hooks.json", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, codexHooksFile, `{"hooks":{"PostToolUse":[{"matcher":"apply_patch","hooks":[`+
			`{"type":"command","command":"timeout 5 /opt/bin/seamark lessons --verify --hook --client codex"}]}]}}`)
		before := treeFiles(t, root)
		_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
			ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck,
		}}})
		require.ErrorContains(t, err, "second handler")
		assert.ErrorContains(t, err, codexHooksFile)
		assert.Equal(t, before, treeFiles(t, root))
	})

	t.Run("an uncertain wrapper warns and installs", func(t *testing.T) {
		root := t.TempDir()
		command := "/opt/wrapper /opt/bin/seamark " + hooks.CodexLessonVerifyMarker
		writeRel(t, root, codexHooksFile, `{"hooks":{"PostToolUse":[{"matcher":"apply_patch","hooks":[`+
			`{"type":"command","command":"`+command+`"}]}]}}`)
		_, findings := applyLessonSetup(t, root, ClientSetup{
			ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck,
		})
		assert.Contains(t, readRel(t, root, codexHooksFile), command)
		assert.Contains(t, readRel(t, root, codexHooksFile), testBinary+" "+hooks.CodexLessonVerifyMarker)
		assert.True(t, slices.ContainsFunc(findings, func(f Finding) bool {
			return f.Level == FindingWarning && strings.Contains(f.Reason, "can run the seamark hook")
		}))
	})
}

func TestSetupLessonChecksSupplementalUnknownInstallsWithReason(t *testing.T) {
	for _, clientID := range []string{ClaudeID, CodexID} {
		t.Run(clientID, func(t *testing.T) {
			root := t.TempDir()
			binary := fakeSeamark(t, t.TempDir(), "self")
			rel := claudeLocalSettings
			if clientID == CodexID {
				rel = approve.CodexConfig
			}
			writeRel(t, root, rel, "BROKEN [[")
			out, _ := applyLessonSetupWithBinary(t, root, binary, ClientSetup{
				ClientID: clientID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce,
			})
			assert.Contains(t, out, "document-unreadable")
			assert.Contains(t, out, " inactive ")
			assert.Equal(t, "BROKEN [[", readRel(t, root, rel))
			report := LessonCapability([]Inspection{inspectionOf(t, root, clientID)}, root, BinaryIdentity{Path: binary})
			client, ok := report.Client(clientID)
			require.True(t, ok)
			assert.Equal(t, lessoncheck.InstalledEnforce, client.Installed, "unknown source does not refuse installation")
		})
	}
}

func TestSetupLessonChecksSummaryNamesMissingBinary(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "missing", "seamark")
	out, _ := applyLessonSetupWithBinary(t, root, binary, ClientSetup{
		ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksCheck,
	})
	assert.Contains(t, out, "pre-write Edit inactive (binary-missing, other-binary)")
	assert.Contains(t, out, "post-edit Write inactive (binary-missing, other-binary)")
}

func TestSetupLessonChecksSummaryKeepsContextsDistinct(t *testing.T) {
	// Synthetic evidence exercises narration only. It establishes no native support.
	saved := guardRecorded
	t.Cleanup(func() { guardRecorded = saved })
	guardRecorded = map[guardPath]recordedEvidence{
		{client: ClaudeID, tool: "Edit", context: lessoncheck.ContextRoot}: {
			clientVersion: "synthetic-fixture", surface: "fixture root Edit",
		},
	}
	root := t.TempDir()
	out, _ := applyLessonSetupWithBinary(t, root, fakeSeamark(t, t.TempDir(), "self"), ClientSetup{
		ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce,
	})
	assert.Contains(t, out, "pre-write Edit child active-unverified (native-deny-unverified, native-timeout-unverified)")
	assert.NotContains(t, out, "pre-write Edit root active-unverified")
}

// hookEntries returns the JSON of every entry of a hook document whose
// commands no lesson-check or exploration marker owns, by event. A
// lesson-check transition must leave these bytes as they are.
func hookEntries(t *testing.T, root, rel string) map[string][]string {
	t.Helper()

	markers := append(hooks.LessonCheckMarkers(hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksEnforce)),
		hooks.LessonCheckMarkers(hooks.CodexLessonCheckSpecs(hooks.LessonChecksEnforce))...)
	markers = append(markers, hooks.LessonExploreMarker)

	hookMap, _ := parseRel(t, root, rel)["hooks"].(map[string]any)
	out := map[string][]string{}

	for event, value := range hookMap {
		entries, _ := value.([]any)

		for _, entry := range entries {
			owned := false

			hooks.ForEachCommand([]any{entry}, func(_ string, _ map[string]any, cmd string) {
				owned = owned || hooks.OwnedBySeamark(cmd, markers)
			})

			if owned {
				continue
			}

			data, err := json.Marshal(entry)
			require.NoError(t, err)

			out[event] = append(out[event], string(data))
		}
	}

	return out
}

func TestValidateClientLessonCheckOperations(t *testing.T) {
	base := func() Client {
		return Client{
			ID: "fake", Name: "Fake", Setup: claudeSetup{},
			SetupOps: SetupSupport{Hooks: true}, Guards: claudeGuards{}, Completions: claudeCompletions{},
			Explores: claudeExplores{},
		}
	}

	for _, tc := range []struct {
		name   string
		change func(*Client)
		want   string
	}{
		{name: "every operation with its decoders", change: func(c *Client) {
			c.SetupOps.LessonVerify, c.SetupOps.LessonGuard, c.SetupOps.LessonExploration = true, true, true
		}},
		{name: "verification without hook installation", want: "lesson-check hooks are declared without hook installation",
			change: func(c *Client) { c.SetupOps = SetupSupport{LessonVerify: true} }},
		{name: "exploration without hook installation", want: "lesson-check hooks are declared without hook installation",
			change: func(c *Client) { c.SetupOps = SetupSupport{LessonExploration: true} }},
		{name: "verification without a guard decoder", want: "lesson verification is declared without guard and completion decoders",
			change: func(c *Client) { c.SetupOps.LessonVerify, c.Guards = true, nil }},
		{name: "verification without a completion decoder", want: "lesson verification is declared without guard and completion decoders",
			change: func(c *Client) { c.SetupOps.LessonVerify, c.Completions = true, nil }},
		{name: "a guard without verification", want: "a lesson guard is declared without lesson verification",
			change: func(c *Client) { c.SetupOps.LessonGuard = true }},
		{name: "exploration without an explore decoder", want: "lesson exploration is declared without an explore decoder",
			change: func(c *Client) { c.SetupOps.LessonExploration, c.Explores = true, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.change(&c)

			_, err := NewRegistry(c)
			if tc.want == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Equal(t, `client "fake": `+tc.want, err.Error())
		})
	}

	// The built-in clients declare what their decoders cover: Codex has
	// no structured Read path.
	claude, _ := Builtin().Lookup(ClaudeID)
	codex, _ := Builtin().Lookup(CodexID)

	assert.True(t, claude.SetupOps.LessonVerify && claude.SetupOps.LessonGuard && claude.SetupOps.LessonExploration)
	assert.True(t, codex.SetupOps.LessonVerify && codex.SetupOps.LessonGuard)
	assert.False(t, codex.SetupOps.LessonExploration)

	// The setup intents take closed values only.
	for _, intent := range []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, LessonChecks: "strict"},
		{ClientID: ClaudeID, Hooks: true, LessonExploration: "yes"},
	} {
		_, err := PlanSetup(Builtin(), SetupRequest{Root: t.TempDir(), Binary: testBinary, Clients: []ClientSetup{intent}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `setup: client "claude": lesson`)
	}
}

// A run without a lesson flag keeps the installed mode. A second handler
// still blocks the plan. The message must not tell the user to let setup
// manage a handler that setup already manages.
func TestSetupLessonChecksCodexKeptModeRefusalNamesTheDuplicate(t *testing.T) {
	root := t.TempDir()
	applyLessonSetup(t, root, ClientSetup{ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck})
	writeRel(t, root, ".codex/config.toml", "[[hooks.PostToolUse]]\nmatcher = \"apply_patch\"\n"+
		"[[hooks.PostToolUse.hooks]]\ntype = \"command\"\n"+
		"command = \"/opt/bin/seamark lessons --verify --hook --client codex\"\n")
	before := treeFiles(t, root)

	kept, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
		ClientID: CodexID, Hooks: true,
	}}})
	require.ErrorContains(t, err, "lesson checks: codex PostToolUse")
	assert.ErrorContains(t, err, ".codex/config.toml [hooks]")
	assert.ErrorContains(t, err, "setup already manages this handler, so remove that definition")
	assert.NotContains(t, err.Error(), "to let setup manage lesson checks")
	assert.Nil(t, kept)
	assert.Equal(t, before, treeFiles(t, root), "the refusal writes nothing")

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
		ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck,
	}}})
	require.ErrorContains(t, err, "remove that definition to let setup manage lesson checks", "an explicit request keeps its wording")
	assert.NotContains(t, err.Error(), "already manages")

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{
		ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksOff,
	}}})
	assert.NoError(t, err, "off removes the managed handler and leaves no duplicate")
}

func TestSetupLessonChecksEnforceRefusesBeforeWrite(t *testing.T) {
	enforce := func(clientID string) ClientSetup {
		return ClientSetup{ClientID: clientID, Hooks: true, CheckHookSources: true, LessonChecks: hooks.LessonChecksEnforce}
	}

	refuse := func(t *testing.T, root string, setup ClientSetup) error {
		t.Helper()

		before := treeFiles(t, root)

		plan, err := PlanSetup(Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{setup}})
		require.Error(t, err)
		assert.Nil(t, plan, "no plan exists, so nothing can be written")
		assert.Equal(t, before, treeFiles(t, root))

		return err
	}

	t.Run("trust pending", func(t *testing.T) {
		// No built-in adapter reads a trust record (contract K10:
		// "reserved"), so the shared decision gets the state directly.
		err := enforceRefusal(CodexID, lessonSourceState{trust: TrustPending})
		require.Error(t, err)
		assert.Equal(t, "lesson checks: enforce for codex would stay inactive (untrusted); resolve it, then rerun", err.Error())

		err = enforceRefusal(ClaudeID, lessonSourceState{trust: TrustPending, hooksDisabled: true, reportOnly: []string{"x"}})
		require.Error(t, err)
		assert.Equal(t, "lesson checks: enforce for claude would stay inactive "+
			"(hooks-disabled, report-only-wrapper, untrusted); resolve it, then rerun", err.Error(), "sorted reasons")

		assert.NoError(t, enforceRefusal(CodexID, lessonSourceState{}), "unknown trust is no refusal")
	})

	t.Run("hooks disabled", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[features]\nhooks = false\n")

		err := refuse(t, root, enforce(CodexID))
		assert.Equal(t, "lesson checks: enforce for codex would stay inactive (hooks-disabled); resolve it, then rerun", err.Error())
	})

	t.Run("a report-only wrapper", func(t *testing.T) {
		wrapped := `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[` +
			`{"type":"command","command":"/opt/bin/seamark lessons --guard --hook || true"}]}]}}`

		for _, rel := range []string{approve.ClaudeSettings, claudeLocalSettings} {
			root := t.TempDir()
			writeRel(t, root, rel, wrapped)

			err := refuse(t, root, enforce(ClaudeID))
			assert.Equal(t, "lesson checks: enforce for claude would stay inactive (report-only-wrapper); resolve it, then rerun",
				err.Error(), rel)
		}

		// The inline hooks of the Codex configuration are a source too.
		root := t.TempDir()
		writeRel(t, root, approve.CodexConfig, "[[hooks.PreToolUse]]\nmatcher = \"apply_patch\"\n"+
			"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"/opt/bin/seamark lessons --guard --hook --client codex & \"\n")

		err := refuse(t, root, enforce(CodexID))
		assert.Contains(t, err.Error(), "(report-only-wrapper)")

		// A wrapper whose exit status passes is no report-only wrapper.
		root = t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, `{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[`+
			`{"type":"command","command":"cd /repo && /opt/bin/seamark lessons --guard --hook"}]}]}}`)
		_, _ = applyLessonSetup(t, root, enforce(ClaudeID))
	})

	t.Run("a configuration that does not validate", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".seamark/lessons.yaml", "lesson_checks_version: 1\npin:\n  - rule: r\n    region: src\n    mode: enforce\n")

		err := refuse(t, root, enforce(ClaudeID))
		assert.True(t, strings.HasPrefix(err.Error(), "lesson checks: .seamark/lessons.yaml does not validate: pin 1"), err.Error())
		assert.True(t, strings.HasSuffix(err.Error(), "; fix it before enabling enforce"), err.Error())

		root = t.TempDir()
		writeRel(t, root, ".seamark/lessons.yaml", "pin: [")

		err = refuse(t, root, enforce(CodexID))
		assert.Contains(t, err.Error(), "lesson checks: .seamark/lessons.yaml does not validate: lessons config: ")
	})

	t.Run("a client without a pre-write guard", func(t *testing.T) {
		codex, _ := Builtin().Lookup(CodexID)
		codex.SetupOps.LessonGuard = false

		reg, err := NewRegistry(codex)
		require.NoError(t, err)

		root := t.TempDir()
		_, err = PlanSetup(reg, SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{enforce(CodexID)}})
		require.Error(t, err)
		assert.Equal(t, "lesson checks: enforce is not supported for codex (no pre-write guard for apply_patch in root and child); "+
			"choose --lesson-checks check for check-only behavior or omit the client", err.Error())
		assert.Empty(t, treeFiles(t, root))

		// Check-only behavior stays available for that client.
		_, err = PlanSetup(reg, SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
			{ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck},
		}})
		require.NoError(t, err)
	})

	t.Run("an inspection that cannot tell installs", func(t *testing.T) {
		// Seamark cannot read the Codex trust record, so the state is
		// unknown: setup installs, and the line names the reason (P0
		// ruling M4). The enforce pattern pin limits the apply_patch
		// path too (contracts §5 row 40).
		root := t.TempDir()
		binary := fakeSeamark(t, t.TempDir(), "self")
		writeRel(t, root, ".seamark/lessons.yaml", lessonPatternYAML)

		out, findings := applyLessonSetupWithBinary(t, root, binary, enforce(CodexID))
		assert.Contains(t, readRel(t, root, codexHooksFile), binary+" "+hooks.CodexLessonGuardMarker)
		assert.Contains(t, out, "  lesson checks  codex enforce — pre-write apply_patch active-unverified "+
			"(content-unprojectable, native-deny-unverified, native-timeout-unverified, trust-unknown); "+
			"post-edit apply_patch active-unverified (trust-unknown)\n")
		assert.Contains(t, findings, codexTrustFinding, "setup never grants trust")

		// Claude Code has no trust review, and no pattern pin limits it.
		root = t.TempDir()
		out, _ = applyLessonSetupWithBinary(t, root, binary, enforce(ClaudeID))
		assert.Contains(t, out, "  lesson checks  claude enforce — "+
			"pre-write Edit active-unverified (native-deny-unverified, native-timeout-unverified); "+
			"pre-write Write active-unverified (native-deny-unverified, native-timeout-unverified); "+
			"pre-write MultiEdit active-unverified (native-deny-unverified, native-timeout-unverified); "+
			"post-edit Edit active-unverified; post-edit Write active-unverified; post-edit MultiEdit active-unverified\n")
		assert.NotContains(t, out, "trust-unknown")
	})
}

func TestSetupLessonChecksPreservesGateAndOtherHooks(t *testing.T) {
	for _, tc := range []struct {
		clientID, document string
		specs              func(string) []hooks.Spec
	}{
		{clientID: ClaudeID, document: approve.ClaudeSettings, specs: hooks.ClaudeLessonCheckSpecs},
		{clientID: CodexID, document: codexHooksFile, specs: hooks.CodexLessonCheckSpecs},
	} {
		t.Run(tc.clientID, func(t *testing.T) {
			root := t.TempDir()
			all := tc.specs(hooks.LessonChecksEnforce)

			_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
				Root: root, Binary: testBinary,
				Clients: []ClientSetup{{ClientID: tc.clientID, Hooks: true, GateMode: hooks.ModeEnforce}},
			}), ApplyOptions{})
			require.NoError(t, err)

			reg := Builtin()
			client, _ := reg.Lookup(tc.clientID)
			entries := hookEntries(t, root, tc.document)
			inspection := client.Setup.Inspect(root)
			untouched := readRel(t, root, tc.document)

			// A run without the flags installs nothing new (L170).
			out, _ := applyLessonSetup(t, root, ClientSetup{ClientID: tc.clientID, Hooks: true})
			assert.Equal(t, untouched, readRel(t, root, tc.document))
			assert.NotContains(t, out, "lesson checks")
			assert.NotContains(t, out, "lesson-check")

			for _, step := range []struct{ request, want string }{
				{hooks.LessonChecksCheck, hooks.LessonChecksCheck},
				{"", hooks.LessonChecksCheck},
				{hooks.LessonChecksEnforce, hooks.LessonChecksEnforce},
				{"", hooks.LessonChecksEnforce},
				{hooks.LessonChecksCheck, hooks.LessonChecksCheck},
				{hooks.LessonChecksOff, hooks.LessonChecksOff},
				{"", hooks.LessonChecksOff},
			} {
				out, _ := applyLessonSetup(t, root, ClientSetup{ClientID: tc.clientID, Hooks: true, LessonChecks: step.request})

				document := parseRel(t, root, tc.document)
				assert.Equal(t, step.want, hooks.InstalledLessonChecks(document, all), "after %q", step.request)
				assert.Equal(t, entries, hookEntries(t, root, tc.document), "after %q: the other entries keep their bytes", step.request)
				assert.Equal(t, hooks.ModeEnforce, client.Setup.ManagedGateMode(root), "after %q: the gate mode stays", step.request)

				// The lesson-check entries are invisible to the gate, lessons,
				// and reset inspection (contract K10).
				now := client.Setup.Inspect(root)
				assert.Equal(t, inspection.Capabilities, now.Capabilities, "after %q", step.request)
				assert.Equal(t, inspection.Findings, now.Findings, "after %q", step.request)
				assert.Equal(t, inspection.GateMode, now.GateMode, "after %q", step.request)

				if step.request == "" {
					assert.NotContains(t, out, "  lesson checks  ", "the line needs a flag")
				} else {
					assert.Contains(t, out, "  lesson checks  "+tc.clientID+" "+step.want+" — ")
				}
			}

			// Off is the rollback: the document is the one before the first
			// lesson-check request.
			assert.Equal(t, untouched, readRel(t, root, tc.document))
		})
	}

	t.Run("a mode switch rewrites the pre-write entry in place", func(t *testing.T) {
		root := t.TempDir()
		applyHooks(t, root, ClaudeID)
		applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksCheck})

		pre := func() []string {
			hookMap := parseRel(t, root, approve.ClaudeSettings)["hooks"].(map[string]any)

			var commands []string

			hooks.ForEachCommand(hookMap["PreToolUse"].([]any), func(_ string, _ map[string]any, cmd string) {
				commands = append(commands, cmd)
			})

			return commands
		}

		assert.Equal(t, []string{
			testBinary + " gate --hook", testBinary + " lessons --hook", testBinary + " " + hooks.LessonBaselineMarker,
		}, pre())

		out, _ := applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})
		assert.Equal(t, []string{
			testBinary + " gate --hook", testBinary + " lessons --hook", testBinary + " " + hooks.LessonGuardMarker,
		}, pre())
		assert.Contains(t, out, "  updated .claude/settings.json (lesson-check hooks)\n",
			"the line names the lesson-check change, not the gate hooks")
		assert.Contains(t, out, "          PreToolUse Edit|Write|MultiEdit "+testBinary+" "+hooks.LessonGuardMarker+"\n")
	})

	t.Run("a stray entry of the other mode goes", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, approve.ClaudeSettings, `{"hooks":{"PostCompact":[{"hooks":[`+
			`{"type":"command","command":"`+testBinary+` lessons --baseline --hook"}]}]}}`)

		applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce})
		assert.NotContains(t, readRel(t, root, approve.ClaudeSettings), hooks.LessonBaselineMarker)
	})

	t.Run("a configuration error limits a check install", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, ".seamark/lessons.yaml", "lesson_checks_version: 2\n")

		out, _ := applyLessonSetupWithBinary(t, root, fakeSeamark(t, t.TempDir(), "self"),
			ClientSetup{ClientID: CodexID, Hooks: true, LessonChecks: hooks.LessonChecksCheck})
		assert.Contains(t, out, "  lesson checks  codex check — pre-write apply_patch inactive (configuration-error, trust-unknown); "+
			"post-edit apply_patch inactive (configuration-error, trust-unknown)\n")
	})

	t.Run("a partial install keeps its pre-write mode", func(t *testing.T) {
		root := t.TempDir()
		pre := hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksEnforce)[:1]
		document := map[string]any{}
		_, err := hooks.Merge(document, testBinary, pre)
		require.NoError(t, err)

		data, err := json.Marshal(document)
		require.NoError(t, err)
		writeRel(t, root, approve.ClaudeSettings, string(data))

		out, _ := applyLessonSetup(t, root, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonExploration: hooks.LessonExplorationOff})
		assert.Equal(t, hooks.LessonChecksEnforce,
			hooks.InstalledLessonChecks(parseRel(t, root, approve.ClaudeSettings), hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksEnforce)),
			"a plain run restores the missing post-edit hooks")
		assert.Contains(t, out, "  lesson checks  claude enforce — ")
	})
}

func TestSetupLessonExplorationInstallsClaudeRead(t *testing.T) {
	root := t.TempDir()
	binary := fakeSeamark(t, t.TempDir(), "self")
	applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: ClaudeID, Hooks: true})
	before := readRel(t, root, approve.ClaudeSettings)

	out, _ := applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonExploration: hooks.LessonExplorationOn})

	hookMap := parseRel(t, root, approve.ClaudeSettings)["hooks"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"matcher": "Read", "hooks": []any{map[string]any{
		"type": "command", "command": binary + " " + hooks.LessonExploreMarker, "timeout": float64(10),
	}}}}, hookMap["PostToolUse"], "one Read entry, no status message")
	assert.NotContains(t, hookMap, "PostToolUseFailure", "a failed read stays uncovered (contracts §5 row 89)")
	assert.Equal(t, hooks.LessonChecksOff,
		hooks.InstalledLessonChecks(parseRel(t, root, approve.ClaudeSettings), hooks.ClaudeLessonCheckSpecs(hooks.LessonChecksEnforce)),
		"exploration installs no lesson-check hook")
	assert.Contains(t, out, "  updated .claude/settings.json (exploration hook)\n")
	assert.Contains(t, out, "          PostToolUse Read               "+binary+" "+hooks.LessonExploreMarker+"\n")
	assert.Contains(t, out, "  lesson checks  claude off — no pre-write or post-edit hook installed; exploration Read active-unverified\n")

	// A run without the flag keeps it; off removes it and nothing else.
	installed := readRel(t, root, approve.ClaudeSettings)
	applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: ClaudeID, Hooks: true})
	assert.Equal(t, installed, readRel(t, root, approve.ClaudeSettings))

	out, _ = applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: ClaudeID, Hooks: true, LessonExploration: hooks.LessonExplorationOff})
	assert.Equal(t, before, readRel(t, root, approve.ClaudeSettings))
	assert.Contains(t, out, "  updated .claude/settings.json (removed the exploration hook)\n")

	// Codex has no structured Read path: the run does not fail, installs
	// nothing for it, and names it (contracts §5 row 42).
	root = t.TempDir()
	applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: CodexID, Hooks: true})
	codexBefore := readRel(t, root, codexHooksFile)

	_, findings := applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: CodexID, Hooks: true, LessonExploration: hooks.LessonExplorationOn})
	assert.Equal(t, codexBefore, readRel(t, root, codexHooksFile))
	assert.Contains(t, findings, Finding{
		Level: FindingWarning,
		Reason: "lesson exploration: nothing was installed for codex (no structured Read path); " +
			"preparation (seamark lessons --prepare) covers it",
	})

	_, findings = applyLessonSetupWithBinary(t, root, binary, ClientSetup{ClientID: CodexID, Hooks: true, LessonExploration: hooks.LessonExplorationOff})
	assert.Empty(t, findingsWith(findings, "lesson exploration"), "off has nothing to say for such a client")
}
