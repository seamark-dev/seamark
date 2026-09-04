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
	assert.Equal(t, []string{"mcp_servers.seamark runs another command"}, p.Conflicts)

	var w bytes.Buffer
	require.NoError(t, ApplyCodex(&w, root, p, false))
	assert.Contains(t, w.String(), "kept    .codex/config.toml (seamark not registered; kept explicit settings: mcp_servers.seamark runs another command)")

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
