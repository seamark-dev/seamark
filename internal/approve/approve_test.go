package approve

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCodex writes .codex/config.toml under root.
func writeCodex(t *testing.T, root, body string) string {
	t.Helper()

	path := filepath.Join(root, ".codex", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	return path
}

// approvedIn re-parses the file and returns the tools approved under
// the named server, so tests judge the written TOML by its meaning.
func approvedIn(t *testing.T, path, server string) []string {
	t.Helper()

	cfg := map[string]any{}
	_, err := toml.DecodeFile(path, &cfg)
	require.NoError(t, err, "the written file must parse")

	servers, _ := cfg["mcp_servers"].(map[string]any)
	entry, _ := servers[server].(map[string]any)
	approved, _, _ := classifyTools(entry)

	return approved
}

func TestClaudeRulesAndAllowSet(t *testing.T) {
	rules, err := ClaudeRules()
	require.NoError(t, err)
	assert.Len(t, rules, 8)
	assert.Equal(t, "mcp__seamark__orient", rules[0])
	assert.Contains(t, rules, "Skill(seamark-review-change)")
	assert.NotContains(t, rules, "Skill(seamark-*)")

	set := AllowSet(map[string]any{"permissions": map[string]any{"allow": []any{"Bash(ls *)", 42, "mcp__seamark__why"}}})
	assert.True(t, set["mcp__seamark__why"])
	assert.False(t, set["mcp__seamark__orient"])
	assert.Empty(t, AllowSet(map[string]any{"permissions": "nope"}))
}

func TestPlanCodexCreatesRegistrationAndApprovals(t *testing.T) {
	root := t.TempDir()

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.False(t, p.Exists)
	assert.True(t, p.Register)
	assert.Equal(t, CodexServer, p.Server)
	assert.Equal(t, Tools, p.Missing)
	assert.Empty(t, p.Conflicts)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "wrote   .codex/config.toml (registered seamark mcp; approved 5 tools: orient, why, change_set, check, expand)")

	path := filepath.Join(root, ".codex", "config.toml")
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")
	assert.NotContains(t, string(data), "default_tools_approval_mode", "never a server-wide default")

	// Idempotent: nothing to add, and the narration says so.
	p, err = PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, Tools, p.Approved)
	assert.Empty(t, p.Missing)

	w.Reset()
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml (seamark registered as \"seamark\"; 5/5 tools approved)")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(after))
}

func TestPlanCodexReusesRegistrationAndPreservesEveryByte(t *testing.T) {
	root := t.TempDir()
	original := "# my codex config\nmodel = \"o3\" # keep me\n\n[mcp_servers.my-seamark]\ncommand = \"/opt/bin/seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.my-seamark.tools.orient]\napproval_mode = \"approve\"\n\n[mcp_servers.other]\ncommand = \"npx\"\nargs = [\"-y\", \"thing\"]\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, "my-seamark", p.Server, "an existing registration is reused whatever its name")
	assert.False(t, p.Register)
	assert.Equal(t, []string{"orient"}, p.Approved)
	assert.Equal(t, []string{"why", "change_set", "check", "expand"}, p.Missing)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "updated .codex/config.toml (approved 4 tools: why, change_set, check, expand)")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), original), "existing bytes, comments included, stay in place")
	assert.Equal(t, Tools, approvedIn(t, path, "my-seamark"))
	assert.NotContains(t, string(data), "[mcp_servers.seamark]", "no second registration")
}

func TestPlanCodexReportsExplicitSettingsAndLeavesThemAlone(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\ndisabled_tools = [\"check\"]\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"prompt\"\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"orient", "change_set", "expand"}, p.Missing)
	assert.Equal(t, []string{"disabled_tools lists check", "tools.why.approval_mode = \"prompt\""}, sortedCopy(p.Conflicts))

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "approved 3 tools: orient, change_set, expand; kept explicit settings:")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), original))
	assert.Contains(t, string(data), "approval_mode = \"prompt\"", "the explicit prompt setting survives")
	assert.Equal(t, []string{"orient", "change_set", "expand"}, approvedIn(t, path, CodexServer))
}

func TestPlanCodexLeavesAForeignServerNamedSeamarkAlone(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\ncommand = \"something-else\"\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Empty(t, p.Server)
	assert.Equal(t, []string{"mcp_servers.seamark runs another command (\"something-else\")"}, p.Conflicts)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml (seamark not registered; kept explicit settings: mcp_servers.seamark runs another command (\"something-else\"))")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestPlanCodexReportsInlineTablesInsteadOfBreakingThem(t *testing.T) {
	root := t.TempDir()
	original := "mcp_servers.seamark = { command = \"seamark\", args = [\"mcp\"], tools = { why = { approval_mode = \"approve\" } } }\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, CodexServer, p.Server)
	assert.Equal(t, []string{"why"}, p.Approved)
	assert.Empty(t, p.Missing, "appending would produce invalid TOML, so nothing is planned")
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "cannot be extended by appending")

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// An inline entry for a tool about to be added is closed too.
	root2 := t.TempDir()
	writeCodex(t, root2, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools]\norient = {}\nwhy = { approval_mode = \"approve\" }\n")

	p, err = PlanCodex(root2)
	require.NoError(t, err)
	assert.Equal(t, []string{"why"}, p.Approved)
	assert.Empty(t, p.Missing)
	require.Len(t, p.Conflicts, 1)

	// Dotted keys, by contrast, leave the table open for sub-tables.
	root3 := t.TempDir()
	path3 := writeCodex(t, root3, "[mcp_servers]\nseamark.command = \"seamark\"\nseamark.args = [\"mcp\"]\n")

	p, err = PlanCodex(root3)
	require.NoError(t, err)
	assert.Equal(t, Tools, p.Missing)
	assert.Empty(t, p.Conflicts)
	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root3, p, false))
	assert.Equal(t, Tools, approvedIn(t, path3, CodexServer))
}

func TestPlanCodexRejectsMalformedTOMLAndSymlinkedPaths(t *testing.T) {
	root := t.TempDir()
	writeCodex(t, root, "[mcp_servers.seamark\ncommand = \"seamark\"\n")

	_, err := PlanCodex(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".codex/config.toml")

	root2 := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root2, ".codex")))

	_, err = PlanCodex(root2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink at .codex")

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries)

	// mcp_servers of the wrong type is the user's data: an error, not an overwrite.
	root3 := t.TempDir()
	writeCodex(t, root3, "mcp_servers = 3\n")

	_, err = PlanCodex(root3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a table")
}

func TestPlanCodexQuotesNonBareServerNames(t *testing.T) {
	root := t.TempDir()
	path := writeCodex(t, root, "[mcp_servers.\"my seamark\"]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, "my seamark", p.Server)
	assert.Equal(t, Tools, p.Missing)

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))
	assert.Equal(t, Tools, approvedIn(t, path, "my seamark"))
}

func TestApplyCodexPreviewWritesNothing(t *testing.T) {
	root := t.TempDir()

	p, err := PlanCodex(root)
	require.NoError(t, err)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, true))
	assert.Contains(t, w.String(), "would write .codex/config.toml (registered seamark mcp; approved 5 tools")
	assert.NoDirExists(t, filepath.Join(root, ".codex"))

	// A trailing newline is added before the block when the file lacks one.
	root2 := t.TempDir()
	path := writeCodex(t, root2, "model = \"o3\"")

	p, err = PlanCodex(root2)
	require.NoError(t, err)
	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root2, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "model = \"o3\"\n\n# seamark:"))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))
}

func TestInspectAndSummaryApprovals(t *testing.T) {
	root := t.TempDir()

	states := Inspect(root)
	require.Len(t, states, 2)
	assert.Equal(t, StateNotConfigured, states[0].State())
	assert.Equal(t, StateNotConfigured, states[1].State())
	assert.Equal(t, "claude not configured · codex not registered", Summary(states))

	// Partial Claude rules name the corrective command once.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["mcp__seamark__orient","mcp__seamark__why","Skill(seamark-plan-change)"]}}`), 0o644))

	states = Inspect(root)
	assert.Equal(t, StatePartial, states[0].State())
	assert.Equal(t, "claude 3/8 rules · codex not registered (re-run seamark init --approve-tools)", Summary(states))

	// Complete on both sides.
	rules, err := ClaudeRules()
	require.NoError(t, err)

	quoted := make([]string, 0, len(rules))
	for _, r := range rules {
		quoted = append(quoted, `"`+r+`"`)
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":[`+strings.Join(quoted, ",")+`]}}`), 0o644))

	p, err := PlanCodex(root)
	require.NoError(t, err)
	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	states = Inspect(root)
	assert.Equal(t, StateCurrent, states[0].State())
	assert.Equal(t, StateCurrent, states[1].State())
	assert.Equal(t, "claude 8/8 rules · codex registered as \"seamark\", 5/5 tools approved", Summary(states))

	// A conflict and an unreadable file are described, never fatal.
	writeCodex(t, root, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"prompt\"\n")
	states = Inspect(root)
	assert.Equal(t, StateConflicting, states[1].State())
	assert.Contains(t, states[1].Describe(), "1 explicit setting(s) kept: tools.why.approval_mode = \"prompt\"")

	writeCodex(t, root, "not toml [\n")
	states = Inspect(root)
	assert.Equal(t, StateUnreadable, states[1].State())
	assert.Contains(t, Summary(states), "codex unreadable")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{not json"), 0o644))
	states = Inspect(root)
	assert.Equal(t, StateUnreadable, states[0].State())
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	return out
}

func TestPlanCodexRejectsInvalidFieldTypesAndValues(t *testing.T) {
	base := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"

	for label, extra := range map[string]string{
		"enabled must be a boolean":                  "enabled = \"false\"\n",
		"tools must be a table":                      "tools = 3\n",
		"tools.why must be a table":                  "[mcp_servers.seamark.tools]\nwhy = 4\n",
		"tools.why.approval_mode must be one of":     "[mcp_servers.seamark.tools.why]\napproval_mode = \"banana\"\n",
		"default_tools_approval_mode must be one of": "default_tools_approval_mode = 7\n",
		"disabled_tools must be an array of strings": "disabled_tools = \"check\"\n",
	} {
		root := t.TempDir()
		writeCodex(t, root, base+extra)

		_, err := PlanCodex(root)
		require.Error(t, err, label)
		assert.Contains(t, err.Error(), strings.Fields(label)[0], label)

		// Codex would refuse the file, so doctor calls it unreadable.
		assert.Equal(t, StateUnreadable, Inspect(root)[1].State(), label)
	}
}

func TestPlanCodexKeepsADisabledServer(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nenabled = false\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, p.Registered)
	assert.Empty(t, p.Missing, "approving tools on a disabled server would mislead")
	assert.Equal(t, []string{"enabled = false"}, p.Conflicts)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml (seamark registered as \"seamark\"; 0/5 tools approved; kept explicit settings: enabled = false)")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
	assert.Equal(t, StateConflicting, Inspect(root)[1].State())
}

func TestPlanCodexPreservesAServerWideApprovalDefault(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\ndefault_tools_approval_mode = \"prompt\"\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"approve\"\n"
	path := writeCodex(t, root, original)

	// Per-tool overrides would win over the default, so none is written.
	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"why"}, p.Approved)
	assert.Empty(t, p.Missing)
	assert.Equal(t, []string{"default_tools_approval_mode = \"prompt\" governs orient, change_set, check, expand"}, p.Conflicts)

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// An approve default already covers the tools: nothing to add, current.
	root2 := t.TempDir()
	writeCodex(t, root2, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\ndefault_tools_approval_mode = \"approve\"\n")

	p, err = PlanCodex(root2)
	require.NoError(t, err)
	assert.Equal(t, Tools, p.Approved)
	assert.Empty(t, p.Missing)
	assert.Empty(t, p.Conflicts)
	assert.Equal(t, StateCurrent, Inspect(root2)[1].State())
}

func TestPlanCodexDecodesQuotedAndEscapedKeys(t *testing.T) {
	// "sea\u006dark" is seamark spelled with an escape: Codex resolves it,
	// so the closed inline tools table must be recognized under it.
	root := t.TempDir()
	original := "[mcp_servers.\"sea\\u006dark\"]\ncommand = \"seamark\"\nargs = [\"mcp\"]\ntools = {}\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Equal(t, "seamark", p.Server)
	assert.True(t, p.Registered)
	assert.Empty(t, p.Missing)
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "inline tables")

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// A literal-quoted header and an inline entry for a missing tool.
	root2 := t.TempDir()
	writeCodex(t, root2, "[mcp_servers.'seamark']\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.'seamark'.tools]\norient = {}\n")

	p, err = PlanCodex(root2)
	require.NoError(t, err)
	assert.Empty(t, p.Missing)
	require.Len(t, p.Conflicts, 1)
}

func TestInspectCodexNeverInventsARegistration(t *testing.T) {
	root := t.TempDir()
	original := "mcp_servers = { foo = { command = \"npx\" } }\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.False(t, p.Registered)
	assert.False(t, p.Register, "the inline mcp_servers table cannot take a new server")

	c := Inspect(root)[1]
	assert.Empty(t, c.Registered)
	assert.True(t, strings.HasPrefix(c.Describe(), "codex not registered"), c.Describe())
	assert.Equal(t, StateConflicting, c.State())

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml (seamark not registered; kept explicit settings:")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestInspectSanitizesDecoderText(t *testing.T) {
	root := t.TempDir()
	writeCodex(t, root, "[mcp_servers.\x1b[31mseamark\n")

	c := Inspect(root)[1]
	assert.Equal(t, StateUnreadable, c.State())
	assert.NotContains(t, c.Err, "\x1b", "repository bytes reach terminals through this text")
	assert.NotContains(t, Summary(Inspect(root)), "\x1b")
}

func TestPlanCodexValidatesEveryServerBeforeDetection(t *testing.T) {
	// args = "mcp" is not a registration by the array rule, and it is not
	// a foreign server either: Codex refuses the file, so init must stop.
	root := t.TempDir()
	writeCodex(t, root, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = \"mcp\"\n")

	_, err := PlanCodex(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mcp_servers.seamark.args must be an array of strings")

	// A wrong-typed field on another server refuses the file just the same.
	root2 := t.TempDir()
	writeCodex(t, root2, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.other]\ncommand = 7\n")

	_, err = PlanCodex(root2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mcp_servers.other.command must be a string")

	root3 := t.TempDir()
	writeCodex(t, root3, "[mcp_servers]\nother = 1\n")

	_, err = PlanCodex(root3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mcp_servers.other must be a table")
}

func TestInlineDetectionSurvivesMultilineStringsAndNestedArrays(t *testing.T) {
	// A multiline value containing a header-shaped line, then a closed
	// inline tools table under the same server.
	root := t.TempDir()
	original := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = \"\"\"\n[other]\n\"\"\"\ntools = {}\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.Empty(t, p.Missing, "the header-shaped line inside the string must not reset the table context")
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "inline tables")

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// The literal form, a single-line string with brackets, and a nested
	// array whose element line starts with a bracket.
	for label, body := range map[string]string{
		"literal multiline":  "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = '''\n[other]\n'''\ntools = {}\n",
		"single-line string": "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = \"[other] # not a comment\"\ntools = {}\n",
		"nested array":       "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nmatrix = [\n  [1, 2],\n]\ntools = {}\n",
	} {
		r := t.TempDir()
		writeCodex(t, r, body)

		p, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.Empty(t, p.Missing, label)
		require.Len(t, p.Conflicts, 1, label)
	}

	// A closed multiline string on its own does not swallow what follows.
	r := t.TempDir()
	path = writeCodex(t, r, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = \"\"\"done\"\"\"\n")

	p, err = PlanCodex(r)
	require.NoError(t, err)
	assert.Equal(t, Tools, p.Missing)
	require.NoError(t, ApplyCodex(&bytes.Buffer{}, r, p, false))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))
}

func TestInlineDetectionIgnoresHeaderShapedArrayElements(t *testing.T) {
	// A nested array element that decodes as a header on its own must
	// not become the table context: the inline server table after it is
	// top-level, and appending [mcp_servers.seamark.tools.*] headers to
	// extend an inline table is TOML Codex refuses.
	root := t.TempDir()
	original := "x = [\n[ \"b\" ]\n]\nmcp_servers.seamark = { command = \"seamark\", args = [\"mcp\"] }\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, p.Registered)
	assert.Empty(t, p.Missing)
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "inline tables")

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// A header after the array still counts, so depth tracking does not
	// hide a real table.
	r := t.TempDir()
	writeCodex(t, r, "x = [\n[ \"b\" ],\n]\n[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\ntools = {}\n")

	p, err = PlanCodex(r)
	require.NoError(t, err)
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "inline tables")
}

func TestInlineDetectionConsumesTheWholeMultilineTerminator(t *testing.T) {
	// TOML lets one or two content quotes sit right before the closing
	// delimiter, so """foo"""" is one string. A scan that stops after
	// three quotes reads the fourth as a new string, misses the array's
	// closing bracket, and then never sees the inline server table on
	// the next line. Appending per-tool tables to that table is TOML
	// Codex refuses, so the plan must report the conflict and write
	// nothing.
	root := t.TempDir()
	original := "[mcp_servers]\nother = { command = \"echo\", args = [\"\"\"foo\"\"\"\"] }\nseamark = { command = \"seamark\", args = [\"mcp\"] }\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, p.Registered)
	assert.Empty(t, p.Missing, "appending would produce invalid TOML, so nothing is planned")
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "inline tables")

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))

	// Every legal terminator, in both string styles, on one line and
	// across lines, with the bracket on the same or the next line, in a
	// nested array, and next to another value. A closed inline table
	// after the value must be found in every case.
	inline := "seamark = { command = \"seamark\", args = [\"mcp\"] }\n"

	for label, body := range map[string]string{
		"basic three":           "[mcp_servers]\nother = { args = [\"\"\"foo\"\"\"] }\n" + inline,
		"basic four":            "[mcp_servers]\nother = { args = [\"\"\"foo\"\"\"\"] }\n" + inline,
		"basic five":            "[mcp_servers]\nother = { args = [\"\"\"foo\"\"\"\"\"] }\n" + inline,
		"literal three":         "[mcp_servers]\nother = { args = ['''foo'''] }\n" + inline,
		"literal four":          "[mcp_servers]\nother = { args = ['''foo''''] }\n" + inline,
		"literal five":          "[mcp_servers]\nother = { args = ['''foo'''''] }\n" + inline,
		"basic empty":           "[mcp_servers]\nother = { args = [\"\"\"\"\"\"] }\n" + inline,
		"literal empty":         "[mcp_servers]\nother = { args = [''''''] }\n" + inline,
		"escaped quote":         "[mcp_servers]\nother = { args = [\"\"\"foo\\\"\"\"\"\"] }\n" + inline,
		"literal backslash":     "[mcp_servers]\nother = { args = ['''foo\\''''] }\n" + inline,
		"adjacent value":        "[mcp_servers]\nother = { args = [\"\"\"foo\"\"\"\"\", \"bar\"] }\n" + inline,
		"nested array":          "[mcp_servers]\nother = { matrix = [[\"\"\"foo\"\"\"\"], [\"bar\"]] }\n" + inline,
		"cross-line same line":  "[other]\nargs = [\"\"\"\nfoo\"\"\"\"]\n[mcp_servers]\n" + inline,
		"cross-line next line":  "[other]\nargs = [\"\"\"\nfoo\"\"\"\"\n]\n[mcp_servers]\n" + inline,
		"literal cross-line":    "[other]\nargs = ['''\nfoo''''\n]\n[mcp_servers]\n" + inline,
		"header-shaped content": "[other]\nargs = [\"\"\"\n[mcp_servers.seamark]\n\"\"\"\"]\n[mcp_servers]\n" + inline,
		"crlf":                  "[mcp_servers]\r\nother = { args = [\"\"\"foo\"\"\"\"] }\r\n" + strings.TrimSuffix(inline, "\n") + "\r\n",
		"no final newline":      "[other]\nargs = [\"\"\"\nfoo\"\"\"\"]\n[mcp_servers]\n" + strings.TrimSuffix(inline, "\n"),
	} {
		r := t.TempDir()
		path := writeCodex(t, r, body)

		p, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.True(t, p.Registered, label)
		assert.Empty(t, p.Missing, label)
		require.Len(t, p.Conflicts, 1, label)
		assert.Contains(t, p.Conflicts[0], "inline tables", label)

		require.NoError(t, ApplyCodex(&bytes.Buffer{}, r, p, false), label)

		data, err := os.ReadFile(path)
		require.NoError(t, err, label)
		assert.Equal(t, body, string(data), label)
	}

	// One or two quotes inside the content do not close the string, so
	// the header-shaped line after them stays content and the inline
	// tools table under the real header is still found.
	for label, body := range map[string]string{
		"one quote":  "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = \"\"\"a\" ]\n[other]\n\"\"\"\ntools = {}\n",
		"two quotes": "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = \"\"\"a\"\" ]\n[other]\n\"\"\"\ntools = {}\n",
	} {
		r := t.TempDir()
		writeCodex(t, r, body)

		p, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.Empty(t, p.Missing, label)
		require.Len(t, p.Conflicts, 1, label)
	}
}

func TestPlanCodexStillWritesAfterAMultilineTerminator(t *testing.T) {
	// The same terminators before a normal table, or a header-only
	// table, must not be mistaken for an open value: approvals are
	// written, the file stays valid, and a second run changes nothing.
	server := "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"

	for label, body := range map[string]string{
		"basic four":        "[other]\nargs = [\"\"\"foo\"\"\"\"]\n" + server,
		"basic five":        "[other]\nargs = [\"\"\"foo\"\"\"\"\"]\n" + server,
		"literal four":      "[other]\nargs = ['''foo'''']\n" + server,
		"literal five":      "[other]\nargs = ['''foo''''']\n" + server,
		"cross-line":        "[other]\nargs = [\"\"\"\nfoo\"\"\"\"\n]\n" + server,
		"header-only after": "[other]\nargs = ['''\nfoo''''\n]\n[mcp_servers.seamark]\n",
		"own key after":     "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nnote = [\"\"\"foo\"\"\"\"]\n",
	} {
		r := t.TempDir()
		path := writeCodex(t, r, body)

		p, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.Empty(t, p.Conflicts, label)
		assert.Equal(t, Tools, p.Missing, label)

		require.NoError(t, ApplyCodex(&bytes.Buffer{}, r, p, false), label)

		data, err := os.ReadFile(path)
		require.NoError(t, err, label)
		assert.True(t, strings.HasPrefix(string(data), body), label)
		assert.Equal(t, Tools, approvedIn(t, path, CodexServer), label)

		again, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.True(t, again.Registered, label)
		assert.Empty(t, again.Missing, label)
		assert.Empty(t, again.Conflicts, label)
		require.NoError(t, ApplyCodex(&bytes.Buffer{}, r, again, false), label)

		same, err := os.ReadFile(path)
		require.NoError(t, err, label)
		assert.Equal(t, string(data), string(same), label)
	}
}

func TestScanLineConsumesMultilineTerminators(t *testing.T) {
	// The scan must leave the string closed and the array closed for
	// every legal terminator, and must keep the string open when fewer
	// than three quotes follow the content.
	open := scanState{multi: '"', depth: 1}
	openLiteral := scanState{multi: '\'', depth: 1}

	for _, tc := range []struct {
		name  string
		start scanState
		line  string
		want  scanState
	}{
		{"basic three", scanState{}, `a = ["""foo"""]`, scanState{}},
		{"basic four", scanState{}, `a = ["""foo""""]`, scanState{}},
		{"basic five", scanState{}, `a = ["""foo"""""]`, scanState{}},
		{"literal three", scanState{}, `a = ['''foo''']`, scanState{}},
		{"literal four", scanState{}, `a = ['''foo'''']`, scanState{}},
		{"literal five", scanState{}, `a = ['''foo''''']`, scanState{}},
		{"basic empty", scanState{}, `a = [""""""]`, scanState{}},
		{"literal empty", scanState{}, `a = ['''''']`, scanState{}},
		{"adjacent value", scanState{}, `a = ["""foo""""", "bar"]`, scanState{}},
		{"escaped quote then close", scanState{}, `a = ["""foo\""""]`, scanState{}},
		{"literal ignores backslash", scanState{}, `a = ['''foo\'''']`, scanState{}},
		{"brackets in content", scanState{}, `a = ["""[x]""""]`, scanState{}},
		{"comment after close", scanState{}, `a = ["""foo"""" # ]`, scanState{depth: 1}},
		{"one quote stays open", scanState{}, `a = ["""foo" ]`, open},
		{"two quotes stay open", scanState{}, `a = ["""foo"" ]`, open},
		{"escaped quote stays open", scanState{}, `a = ["""foo\""" ]`, open},
		{"close on a later line", open, `foo""""]`, scanState{}},
		{"close with five on a later line", open, `foo"""""] # x`, scanState{}},
		{"literal close on a later line", openLiteral, `foo'''']`, scanState{}},
		{"other style never closes", openLiteral, `foo""""]`, openLiteral},
		{"later line still open", open, `foo"" ]`, open},
		{"crlf tail", scanState{}, "a = [\"\"\"foo\"\"\"\"]\r", scanState{}},
	} {
		assert.Equal(t, tc.want, scanLine(tc.line, tc.start), tc.name)
	}
}

func TestPlanCodexCompletesAHeaderOnlyTable(t *testing.T) {
	// The state "remove these tables to undo" leaves behind: the server
	// header without its keys, and one leftover per-tool table.
	root := t.TempDir()
	path := writeCodex(t, root, "[mcp_servers.seamark]\n\n[mcp_servers.seamark.tools.orient]\napproval_mode = \"approve\"\n")

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.False(t, p.Registered)
	assert.True(t, p.Register, "no command is configured, so nothing is in the way")
	assert.Empty(t, p.Conflicts)
	assert.Equal(t, []string{"orient"}, p.Approved)
	assert.Equal(t, []string{"why", "change_set", "check", "expand"}, p.Missing)

	var out bytes.Buffer
	require.NoError(t, ApplyCodex(&out, root, p, false))
	assert.Contains(t, out.String(), "updated .codex/config.toml (registered seamark mcp; approved 4 tools: why, change_set, check, expand)")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools.orient]\n"), string(data))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))

	again, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, again.Registered)
	assert.Empty(t, again.Missing)

	// A header that ends the file without a newline gets one before the keys.
	r := t.TempDir()
	path = writeCodex(t, r, "[mcp_servers.seamark]")

	p, err = PlanCodex(r)
	require.NoError(t, err)
	require.True(t, p.Register)
	require.NoError(t, ApplyCodex(&bytes.Buffer{}, r, p, false))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))

	// Args that do not run mcp, or a dotted-key table, are reported and kept.
	for label, body := range map[string]string{
		"foreign args": "[mcp_servers.seamark]\nargs = [\"serve\"]\n",
		"dotted key":   "mcp_servers.seamark.enabled = true\n",
	} {
		r := t.TempDir()
		writeCodex(t, r, body)

		p, err := PlanCodex(r)
		require.NoError(t, err, label)
		assert.False(t, p.Register, label)
		assert.Empty(t, p.Missing, label)
		require.Len(t, p.Conflicts, 1, label)
	}
}

func TestPlanCodexNamesTheSameInvalidToolEveryRun(t *testing.T) {
	root := t.TempDir()
	writeCodex(t, root, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"maybe\"\n\n[mcp_servers.seamark.tools.check]\napproval_mode = \"maybe\"\n")

	for i := 0; i < 20; i++ {
		_, err := PlanCodex(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mcp_servers.seamark.tools.check.approval_mode", "the first tool in name order")
	}
}

func TestPlanClaudeReadsDenyAskAndServerWideRules(t *testing.T) {
	settings := map[string]any{"permissions": map[string]any{
		"allow": []any{"mcp__seamark__orient", "mcp__seamark__check", "Skill(seamark-plan-change)"},
		"deny":  []any{"mcp__seamark__check"},
		"ask":   []any{"Skill(seamark-review-change)"},
	}}

	p, err := PlanClaude(settings, ClaudeServer)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__seamark__orient", "Skill(seamark-plan-change)"}, p.Approved)
	assert.Equal(t, []string{"mcp__seamark__why", "mcp__seamark__change_set", "mcp__seamark__expand", "Skill(seamark-understand-repo)"}, p.Missing)
	assert.Equal(t, []string{"permissions.deny lists mcp__seamark__check", "permissions.ask lists Skill(seamark-review-change)"}, p.Conflicts,
		"deny wins over allow in Claude Code, so an allowed-and-denied tool is a conflict, never approved")

	// The server-wide rule approves every tool of the server, once.
	wide, err := PlanClaude(map[string]any{"permissions": map[string]any{"allow": []any{"mcp__seamark"}}}, ClaudeServer)
	require.NoError(t, err)
	assert.Len(t, wide.Approved, 5)
	assert.Len(t, wide.Missing, 3, "the skill rules are not covered by a server rule")
	assert.Empty(t, wide.Conflicts)

	denied, err := PlanClaude(map[string]any{"permissions": map[string]any{"deny": []any{"mcp__seamark"}}}, ClaudeServer)
	require.NoError(t, err)
	assert.Empty(t, denied.Approved)
	assert.Equal(t, []string{"permissions.deny lists mcp__seamark"}, denied.Conflicts)
	assert.Len(t, denied.Missing, 3)

	// Another server name spells other rules; the seamark-named rule then
	// covers nothing.
	other, err := PlanClaude(map[string]any{"permissions": map[string]any{"allow": []any{"mcp__seamark__orient", "mcp__sm__orient"}}}, "sm")
	require.NoError(t, err)
	assert.Equal(t, []string{"mcp__sm__orient"}, other.Approved)
	assert.Equal(t, "mcp__sm__why", other.Missing[0])
}

func TestClaudeRegistrationDerivesTheServerName(t *testing.T) {
	root := t.TempDir()

	reg, err := ClaudeRegistration(root)
	require.NoError(t, err)
	assert.False(t, reg.Exists)
	assert.Equal(t, ClaudeServer, reg.ServerName(), "no file: the conventional name")

	writeMCP := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(body), 0o644))
	}

	writeMCP(`{"mcpServers":{"sm":{"command":"/usr/local/bin/seamark","args":["mcp"]},"other":{"command":"npx"}}}`)
	reg, err = ClaudeRegistration(root)
	require.NoError(t, err)
	assert.True(t, reg.Exists)
	assert.Equal(t, "sm", reg.Server)

	writeMCP(`{"mcpServers":{"zz":{"command":"seamark.exe"},"seamark":{"command":"seamark"}}}`)
	reg, err = ClaudeRegistration(root)
	require.NoError(t, err)
	assert.Equal(t, ClaudeServer, reg.Server, "the conventional name wins when several match")

	writeMCP(`{"mcpServers":{"other":{"command":"npx"}}}`)
	reg, err = ClaudeRegistration(root)
	require.NoError(t, err)
	assert.Equal(t, "", reg.Server)
	assert.Equal(t, ClaudeServer, reg.ServerName())

	writeMCP(`{not json`)
	_, err = ClaudeRegistration(root)
	require.Error(t, err)

	// Inspection follows the registered name: rules spelled for the
	// conventional name approve nothing under "sm", and the description
	// says which server the count is for.
	writeMCP(`{"mcpServers":{"sm":{"command":"seamark","args":["mcp"]}}}`)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["mcp__seamark__orient","mcp__sm"]}}`), 0o644))

	c := inspectClaude(root)
	assert.Equal(t, "sm", c.Registered)
	assert.Equal(t, 5, c.Approved)
	assert.Equal(t, StatePartial, c.State())
	assert.Equal(t, `claude 5/8 rules for server "sm"`, c.Describe())
}

func TestStateUsesOneRuleForBothClients(t *testing.T) {
	claude := ClientApproval{Client: ClientClaude, Total: 8}
	codex := ClientApproval{Client: ClientCodex, Registered: "seamark", Total: 5}
	registeredClaude := ClientApproval{Client: ClientClaude, Registered: "seamark", Total: 8}
	unregisteredCodex := ClientApproval{Client: ClientCodex, Approved: 5, Total: 5}

	assert.Equal(t, StateNotConfigured, claude.State())
	assert.Equal(t, StatePartial, codex.State(), "a registration approves nothing, so every call prompts and the re-run hint is due")
	assert.Equal(t, StatePartial, registeredClaude.State(), "the same rule for a .mcp.json registration without rules")
	assert.Equal(t, StatePartial, unregisteredCodex.State(), "approvals without a server Codex can launch; the re-run registers it")
	assert.Equal(t, `codex registered as "seamark", 0/5 tools approved`, codex.Describe())
	assert.Equal(t, `codex not registered, 5/5 tools approved`, unregisteredCodex.Describe())
	assert.Equal(t, "claude not configured", claude.Describe())
	assert.Equal(t, "claude 0/8 rules", registeredClaude.Describe(), "the words must agree with the partial state")

	renamed := ClientApproval{Client: ClientClaude, Registered: "sm", Total: 8}
	assert.Equal(t, StatePartial, renamed.State())
	assert.Equal(t, `claude 0/8 rules for server "sm"`, renamed.Describe())

	denied := ClientApproval{Client: ClientClaude, Total: 8, Conflicts: []string{"permissions.deny lists mcp__seamark__check"}}
	assert.Equal(t, StateConflicting, denied.State())
	assert.Equal(t, "claude not configured, 1 explicit setting(s) kept: permissions.deny lists mcp__seamark__check", denied.Describe())
}

func TestInspectClaudeReportsABrokenMCPConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{not json"), 0o644))

	rules, err := ClaudeRules()
	require.NoError(t, err)

	quoted := make([]string, 0, len(rules))
	for _, r := range rules {
		quoted = append(quoted, `"`+r+`"`)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":[`+strings.Join(quoted, ",")+`]}}`), 0o644))

	// The server name in .mcp.json spells every rule, so a file that
	// cannot be read makes the count unknowable; status has no other
	// check that reads it.
	c := inspectClaude(root)
	assert.Equal(t, StateUnreadable, c.State())
	assert.Contains(t, c.Err, ".mcp.json")
	assert.Equal(t, 0, c.Approved, "8 rules under a guessed name must not read as current")
}

func TestPlanCodexInsertsOnlyTheMissingRegistrationKeys(t *testing.T) {
	root := t.TempDir()
	path := writeCodex(t, root, "[mcp_servers.seamark]\nargs = [\"mcp\"]\n")

	p, err := PlanCodex(root)
	require.NoError(t, err)
	require.True(t, p.Register, "%v", p.Conflicts)
	assert.Empty(t, p.Conflicts)

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "args ="), "the key the table already holds is not written twice")
	assert.True(t, strings.HasPrefix(string(data), "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"), string(data))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))
}

func TestApplyCodexRefusesAFileEditedSinceThePlan(t *testing.T) {
	root := t.TempDir()
	path := writeCodex(t, root, "[mcp_servers.seamark]\n")

	p, err := PlanCodex(root)
	require.NoError(t, err)
	require.True(t, p.Register)

	// Shorter than the planned offset: the splice would read past the
	// end. Longer above the header: the keys would land in another table.
	for _, edited := range []string{"", "[other]\nx = 1\n\n[mcp_servers.seamark]\n"} {
		require.NoError(t, os.WriteFile(path, []byte(edited), 0o644))

		err := ApplyCodex(&bytes.Buffer{}, root, p, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed since it was planned")

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, edited, string(data), "a refused write leaves the file alone")
	}
}

func TestPlanCodexKeepsAParkedHeaderOnlyTable(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\nenabled = false\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.False(t, p.Register, "enabled = false is the user's choice; registering would switch the server back on")
	assert.Empty(t, p.Missing)
	assert.Equal(t, []string{"enabled = false"}, p.Conflicts)

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestPlanCodexAppendsTheHeaderWhenOnlySubTablesRemain(t *testing.T) {
	root := t.TempDir()
	path := writeCodex(t, root, "[mcp_servers.seamark.tools.orient]\napproval_mode = \"approve\"\n")

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, p.Register, "%v", p.Conflicts)
	assert.Empty(t, p.Conflicts)
	assert.Equal(t, []string{"orient"}, p.Approved)
	assert.Len(t, p.Missing, 4)

	// Not registered yet, but the approvals are real: inspection says
	// so instead of calling the file current.
	c := inspectCodex(root)
	assert.Equal(t, "", c.Registered)
	assert.Equal(t, 1, c.Approved)
	assert.Equal(t, StatePartial, c.State())
	assert.Equal(t, "codex not registered, 1/5 tools approved", c.Describe())

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))
	assert.Equal(t, Tools, approvedIn(t, path, CodexServer))

	again, err := PlanCodex(root)
	require.NoError(t, err)
	assert.True(t, again.Registered)
	assert.Empty(t, again.Missing)
}

func TestClaudePlanCountsConflictingRules(t *testing.T) {
	p, err := PlanClaude(map[string]any{"permissions": map[string]any{
		"allow": []any{"Skill(seamark-understand-repo)", "Skill(seamark-plan-change)", "Skill(seamark-review-change)"},
		"deny":  []any{"mcp__seamark"},
	}}, ClaudeServer)
	require.NoError(t, err)
	assert.Len(t, p.Conflicts, 1, "one entry")
	assert.Equal(t, 5, p.Conflicting(), "five rules")
	assert.Empty(t, p.Missing)
}

func TestPlanCodexKeepsAnHTTPTransportRegistration(t *testing.T) {
	root := t.TempDir()
	original := "[mcp_servers.seamark]\nurl = \"https://example.com/mcp\"\n"
	path := writeCodex(t, root, original)

	p, err := PlanCodex(root)
	require.NoError(t, err)
	assert.False(t, p.Register, "a url server has no command by design; adding one makes Codex refuse the file")
	assert.Empty(t, p.Missing)
	assert.Equal(t, []string{`mcp_servers.seamark uses an HTTP transport (url = https://example.com/mcp)`}, p.Conflicts)

	require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestPlanClaudeMatchesWildcardEntries(t *testing.T) {
	// A wildcard deny blocks every tool however allow reads; counting the
	// server-wide allow first would report 8/8 while every call is denied.
	p, err := PlanClaude(map[string]any{"permissions": map[string]any{
		"allow": []any{"mcp__seamark", "Skill(seamark-understand-repo)", "Skill(seamark-plan-change)", "Skill(seamark-review-change)"},
		"deny":  []any{"mcp__seamark__*"},
	}}, ClaudeServer)
	require.NoError(t, err)
	assert.Len(t, p.Approved, 3, "the skills only")
	assert.Empty(t, p.Missing)
	assert.Equal(t, []string{"permissions.deny lists mcp__seamark__*"}, p.Conflicts)
	assert.Equal(t, 5, p.Conflicting())

	allowed, err := PlanClaude(map[string]any{"permissions": map[string]any{
		"allow": []any{"mcp__seamark__*"},
		"ask":   []any{"Skill(seamark-*)"},
	}}, ClaudeServer)
	require.NoError(t, err)
	assert.Len(t, allowed.Approved, 5, "a tool prefix anchored under the server is the one allow wildcard Claude Code honours")
	assert.Empty(t, allowed.Missing)
	assert.Equal(t, []string{"permissions.ask lists Skill(seamark-*)"}, allowed.Conflicts)

	// Claude Code ignores an unanchored allow pattern, so counting it
	// would leave every call prompting and skip the exact rules; the
	// same patterns in deny still count, because a missed restriction
	// hides a prompt.
	for _, pattern := range []string{"*", "mcp__*", "mcp__seamark*", "Skill(seamark-*)", "mcp__seamark__*ch*"} {
		loose, err := PlanClaude(map[string]any{"permissions": map[string]any{"allow": []any{pattern}}}, ClaudeServer)
		require.NoError(t, err, pattern)
		assert.Empty(t, loose.Approved, pattern)
		assert.Len(t, loose.Missing, 8, pattern)
	}

	denied, err := PlanClaude(map[string]any{"permissions": map[string]any{"deny": []any{"mcp__*"}}}, ClaudeServer)
	require.NoError(t, err)
	assert.Equal(t, 5, denied.Conflicting())

	// A wildcard for another server covers nothing here.
	other, err := PlanClaude(map[string]any{"permissions": map[string]any{"deny": []any{"mcp__other__*"}}}, ClaudeServer)
	require.NoError(t, err)
	assert.Empty(t, other.Conflicts)
	assert.Len(t, other.Missing, 8)
}
