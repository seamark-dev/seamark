package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seamarkHookEntries returns, per event, the hook entries of a parsed
// hook document whose command holds marker.
func seamarkHookEntries(t *testing.T, document []byte, marker string) map[string][]any {
	t.Helper()

	var parsed struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(document, &parsed))

	out := map[string][]any{}

	for event, entries := range parsed.Hooks {
		for _, entry := range entries {
			raw, err := json.Marshal(entry)
			require.NoError(t, err)

			if strings.Contains(string(raw), marker) {
				out[event] = append(out[event], entry)
			}
		}
	}

	return out
}

func TestCodexHooksMatchTheRecordedFixture(t *testing.T) {
	// The fixture is the shape recorded from the Codex hooks reference.
	// The generated lesson entry must equal it. The gate entry of the
	// fixture arrives with the command gate slice. Neither holds a
	// context-reset hook: a Codex reset clears nothing today.
	plan := planCodex(t, t.TempDir(), ClientSetup{Hooks: true})

	require.Len(t, plan.Writes, 1)
	assert.Equal(t, ".codex/hooks.json", plan.Writes[0].Path)
	assert.Equal(t, "lessons hook", plan.Writes[0].Detail)
	assert.NotContains(t, string(plan.Writes[0].After), "PostCompact")
	assert.NotContains(t, string(plan.Writes[0].After), "--hook-reset")

	generated := strings.ReplaceAll(string(plan.Writes[0].After), testBinary, "/usr/local/bin/seamark")
	fixture := codexFixture(t, "hooks.seamark.json")

	assert.Equal(t, seamarkHookEntries(t, fixture, " lessons --hook"),
		seamarkHookEntries(t, []byte(generated), " lessons --hook"))

	assert.Empty(t, seamarkHookEntries(t, []byte(generated), " gate "), "no gate hook in this slice")
	assert.Empty(t, plan.GateHooks)

	// Trust and the delivery limit are stated, never granted or hidden.
	infos := strings.Join(findingReasons(plan, FindingInfo), "\n")
	assert.Contains(t, infos, "setup never grants trust")
	assert.Contains(t, infos, "`hook_delivery: once-per-context` does not apply")
	assert.Empty(t, findingReasons(plan, FindingWarning))
}

func TestCodexHooksKeepForeignHooksAndConverge(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".codex/hooks.json", `{
  "description": "team hooks",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/opt/audit.sh", "timeout": 5}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "/opt/notify.sh"}]}]
  }
}
`)

	reg := Builtin()
	req := SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: CodexID, Hooks: true}}}

	_, err := ApplySetup(mustPlan(t, reg, req), ApplyOptions{})
	require.NoError(t, err)

	after := readRel(t, root, ".codex/hooks.json")
	assert.Contains(t, after, `"description": "team hooks"`)
	assert.Contains(t, after, "/opt/audit.sh")
	assert.Contains(t, after, "/opt/notify.sh")
	assert.Contains(t, after, testBinary+" lessons --hook --client codex")

	// A second run changes nothing.
	second := mustPlan(t, reg, req)
	assert.Empty(t, second.Writes)
	require.Len(t, second.Kept, 1)
	assert.Equal(t, "seamark hooks already wired", second.Kept[0].Detail)

	// A moved binary updates the owned commands in place: no second copy.
	moved := req
	moved.Binary = "/opt/new home/seamark"

	_, err = ApplySetup(mustPlan(t, reg, moved), ApplyOptions{})
	require.NoError(t, err)

	after = readRel(t, root, ".codex/hooks.json")
	assert.Equal(t, 1, strings.Count(after, "lessons --hook --client codex"))
	assert.Contains(t, after, `'/opt/new home/seamark' lessons --hook --client codex`)
	assert.NotContains(t, after, testBinary)
	assert.Contains(t, after, "/opt/audit.sh")
}

func TestCodexHooksKeepForeignValuesExactly(t *testing.T) {
	// The file is the user's too. A number keeps its digits, and a shell
	// operator keeps its characters: a user reviews this text in Codex.
	root := t.TempDir()
	writeRel(t, root, ".codex/hooks.json", `{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "make lint && echo <ok> > /tmp/log", "timeout": 1.0, "budget": 12345678901234567890}]}]
  }
}
`)

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	require.Len(t, plan.Writes, 1)

	after := string(plan.Writes[0].After)
	assert.Contains(t, after, `"command": "make lint && echo <ok> > /tmp/log"`)
	assert.Contains(t, after, `"timeout": 1.0`)
	assert.Contains(t, after, `"budget": 12345678901234567890`)
	assert.True(t, strings.HasSuffix(after, "}\n"), "one final newline")
}

func TestCodexHooksStopOnADocumentTheyCannotOwn(t *testing.T) {
	for name, body := range map[string]string{
		"invalid JSON":      "{ broken",
		"two JSON values":   "{} {}",
		"hooks is a list":   `{"hooks": []}`,
		"event is a string": `{"hooks": {"PreToolUse": "x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, ".codex/hooks.json", body)

			_, err := codexSetup{}.Plan(root, testBinary, ClientSetup{ClientID: CodexID, Hooks: true})
			require.Error(t, err, "setup never overwrites the user's data to install a hook")
			assert.Contains(t, err.Error(), ".codex/hooks.json")
			assert.Equal(t, body, readRel(t, root, ".codex/hooks.json"))
		})
	}

	// A link is refused: setup writes no document through a link.
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(root, ".codex", "hooks.json")))

	_, err := codexSetup{}.Plan(root, testBinary, ClientSetup{ClientID: CodexID, Hooks: true})
	require.Error(t, err)

	// An empty binary path is a broken request.
	_, err = codexSetup{}.Plan(t.TempDir(), "", ClientSetup{ClientID: CodexID, Hooks: true})
	require.Error(t, err)
}

func TestCodexHooksInstallNoSecondHandler(t *testing.T) {
	// Codex runs every matching handler of every source, and Codex
	// delivery never suppresses a repeat. A second handler doubles the
	// advice, the log records, and the budget.
	inline := `model = "x"

[[hooks.PreToolUse]]
matcher = "apply_patch"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "/usr/local/bin/seamark lessons --hook --client codex"
`
	hooksWith := func(command string) string {
		return `{"hooks": {"PreToolUse": [{"matcher": "apply_patch", "hooks": [{"type": "command", "command": "` + command + `"}]}]}}`
	}

	cases := []struct {
		name      string
		hooksJSON string
		config    string
		source    string
	}{
		{"an exec prefix in hooks.json", hooksWith("timeout 5 /usr/local/bin/seamark lessons --hook --client codex"), "",
			".codex/hooks.json already runs the seamark hook"},
		{"a shell condition in hooks.json",
			hooksWith("test -x /usr/local/bin/seamark && /usr/local/bin/seamark lessons --hook --client codex"), "",
			".codex/hooks.json already runs the seamark hook"},
		{"an inline hook in config.toml", "", inline, ".codex/config.toml [hooks] already runs the seamark hook"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()

			if tc.hooksJSON != "" {
				writeRel(t, root, ".codex/hooks.json", tc.hooksJSON)
			}

			if tc.config != "" {
				writeRel(t, root, ".codex/config.toml", tc.config)
			}

			before := treeFiles(t, root)

			plan := planCodex(t, root, ClientSetup{Hooks: true})

			assert.Empty(t, plan.Writes, "no second handler, so nothing to write")
			require.Len(t, plan.Kept, 1)
			assert.Contains(t, plan.Kept[0].Detail, "no hook installed")

			warnings := findingReasons(plan, FindingWarning)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], tc.source)
			assert.Contains(t, warnings[0], "installed no second handler")

			// The run through the coordinator leaves every file as it was.
			_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary,
				Clients: []ClientSetup{{ClientID: CodexID, Hooks: true}}}), ApplyOptions{})
			require.NoError(t, err)
			assert.Equal(t, before, treeFiles(t, root))

			if tc.hooksJSON != "" {
				assert.Equal(t, tc.hooksJSON, readRel(t, root, ".codex/hooks.json"), "the wrapper stays, byte for byte")
			}
		})
	}
}

func TestCodexHooksInstallWhenTheHookTextIsOnlyPrinted(t *testing.T) {
	// The reported defect: a diagnostic hook prints the full seamark
	// command. Nothing executes that text, so the real hook is missing
	// and setup must install it. A false "already runs" leaves the user
	// without any lesson hook.
	printed := "echo '/usr/local/bin/seamark lessons --hook --client codex'"

	root := t.TempDir()
	writeRel(t, root, ".codex/hooks.json",
		`{"hooks": {"PreToolUse": [{"matcher": "apply_patch", "hooks": [{"type": "command", "command": "`+printed+`"}]}]}}`)
	writeRel(t, root, ".codex/config.toml",
		"[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"echo \\\"/usr/local/bin/seamark lessons --hook --client codex\\\"\"\n")

	plan := planCodex(t, root, ClientSetup{Hooks: true})

	require.Len(t, plan.Writes, 1, "the real hook is not installed yet")
	after := string(plan.Writes[0].After)
	assert.Contains(t, after, testBinary+" lessons --hook --client codex")
	assert.Contains(t, after, printed, "the diagnostic hook stays")
	assert.Empty(t, findingReasons(plan, FindingWarning), "printed text is no second handler")
}

func TestCodexHooksInstallBesideACommandThatOnlyCanRunTheHook(t *testing.T) {
	// An unknown program gets the seamark command as its arguments. A
	// wrapper runs them and echo prints them, and the words do not say
	// which. Setup installs the managed hook, because a missing hook costs
	// more than a repeated reminder, and it names the command.
	for _, command := range []string{
		"/opt/wrapper /usr/local/bin/seamark lessons --hook --client codex",
		"echo /usr/local/bin/seamark lessons --hook --client codex",
	} {
		root := t.TempDir()
		writeRel(t, root, ".codex/hooks.json",
			`{"hooks": {"PreToolUse": [{"matcher": "apply_patch", "hooks": [{"type": "command", "command": "`+command+`"}]}]}}`)

		plan := planCodex(t, root, ClientSetup{Hooks: true})

		require.Len(t, plan.Writes, 1, command)
		after := string(plan.Writes[0].After)
		assert.Contains(t, after, command, "the command stays as the user wrote it")
		assert.Contains(t, after, testBinary+" lessons --hook --client codex")

		warnings := findingReasons(plan, FindingWarning)
		require.Len(t, warnings, 1, command)
		assert.Contains(t, warnings[0], "can run the seamark hook")
		assert.Contains(t, warnings[0], "setup cannot tell")
	}
}

func TestCodexHooksKeepAManagedHandlerAndReportTheDuplicate(t *testing.T) {
	// The managed handler exists already, and an inline copy appears
	// later. Setup keeps its handler current and says that both run.
	root := t.TempDir()

	reg := Builtin()
	req := SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: CodexID, Hooks: true}}}

	_, err := ApplySetup(mustPlan(t, reg, req), ApplyOptions{})
	require.NoError(t, err)

	writeRel(t, root, ".codex/config.toml",
		"[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"seamark lessons --hook --client codex\"\n")

	moved := req
	moved.Binary = "/opt/moved/seamark"

	plan := mustPlan(t, reg, moved)
	require.Len(t, plan.Writes, 1, "the managed handler follows the binary")
	assert.Contains(t, string(plan.Writes[0].After), "/opt/moved/seamark lessons --hook --client codex")

	var warnings []string

	for _, f := range plan.Findings {
		if f.Level == FindingWarning {
			warnings = append(warnings, f.Reason)
		}
	}

	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "the hook runs twice")
}

func TestCodexHooksReadTheInlineSourceWithoutOwningIt(t *testing.T) {
	root := t.TempDir()

	// Inline hooks that are not seamark's are information only.
	writeRel(t, root, ".codex/config.toml", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/opt/notify.sh\"\n")

	plan := planCodex(t, root, ClientSetup{Hooks: true})
	require.Len(t, plan.Writes, 1)
	assert.Empty(t, findingReasons(plan, FindingWarning))
	assert.Contains(t, strings.Join(findingReasons(plan, FindingInfo), "\n"), "inline [hooks] are present")

	// config.toml is an input of this plan: read, guarded, never written.
	var guarded []string
	for _, guard := range plan.Reads {
		guarded = append(guarded, guard.Path)
	}

	assert.ElementsMatch(t, []string{".codex/hooks.json", ".codex/config.toml"}, guarded)

	// A lookalike binary and a text that only quotes the command run no
	// seamark hook, so the managed hook is installed.
	writeRel(t, root, ".codex/hooks.json", `{"hooks": {"PreToolUse": [{"matcher": "apply_patch", "hooks": [
  {"type": "command", "command": "/opt/seamark2 lessons --hook --client codex"},
  {"type": "command", "command": "echo 'lessons --hook --client codex'"}
]}]}}`)

	plan = planCodex(t, root, ClientSetup{Hooks: true})
	require.Len(t, plan.Writes, 1)
	assert.Empty(t, findingReasons(plan, FindingWarning))
	assert.Contains(t, string(plan.Writes[0].After), testBinary+" lessons --hook --client codex")
	assert.Contains(t, string(plan.Writes[0].After), "/opt/seamark2")

	// A config.toml that cannot be parsed does not stop a hooks-only run.
	writeRel(t, root, ".codex/config.toml", "model = [broken")

	plan = planCodex(t, root, ClientSetup{Hooks: true})
	require.Len(t, plan.Writes, 1)
	assert.Contains(t, strings.Join(findingReasons(plan, FindingWarning), "\n"), "not checked for inline seamark hooks")
}

func TestCodexHooksAndRegistrationShareOneReadOfTheConfig(t *testing.T) {
	// Both parts of the plan need config.toml. Two reads give two guards,
	// and the coordinator takes two different guards for a changed file.
	root := t.TempDir()
	writeRel(t, root, ".codex/config.toml", "model = \"x\"\n")

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary,
		Clients: []ClientSetup{{ClientID: CodexID, Hooks: true, RegisterMCP: true, ApproveTools: true}}})

	var paths []string
	for _, write := range plan.Writes {
		paths = append(paths, write.Path)
	}

	assert.Equal(t, []string{".codex/config.toml", ".codex/hooks.json"}, paths)

	result, err := ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)

	for _, op := range result.Ops {
		assert.Equal(t, OpApplied, op.Status, op.Path)
	}
}
