package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandsOf flattens the command strings under one event.
func commandsOf(settings map[string]any, event string) []string {
	hookMap, _ := settings["hooks"].(map[string]any)
	entries, _ := hookMap[event].([]any)

	var out []string

	ForEachCommand(entries, func(_ string, _ map[string]any, cmd string) { out = append(out, cmd) })

	return out
}

func TestMergeInstallsAnySpecListOnce(t *testing.T) {
	// The merge is not tied to Claude Code's hooks: a client adapter hands
	// it its own specs for the same document shape.
	specs := []Spec{
		{Event: "PreToolUse", Matcher: "apply_patch", Marker: "lessons --hook --client codex", Status: "s", Timeout: 10},
		{Event: "PostCompact", Marker: "lessons --hook-reset --client codex", Status: "s", Timeout: 10},
	}
	settings := map[string]any{}

	changed, err := Merge(settings, "/bin/seamark", specs)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{"/bin/seamark lessons --hook --client codex"}, commandsOf(settings, "PreToolUse"))
	assert.Equal(t, []string{"/bin/seamark lessons --hook-reset --client codex"}, commandsOf(settings, "PostCompact"))

	changed, err = Merge(settings, "/bin/seamark", specs)
	require.NoError(t, err)
	assert.False(t, changed, "a second merge changes nothing")

	// A moved binary rewrites the owned command in place.
	changed, err = Merge(settings, "/opt/my tools/seamark", specs)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{"'/opt/my tools/seamark' lessons --hook --client codex"}, commandsOf(settings, "PreToolUse"))
}

func TestMergeWithNoSpecsChangesNothing(t *testing.T) {
	settings := map[string]any{"model": "opus"}

	changed, err := Merge(settings, "/bin/seamark", nil)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, "opus", settings["model"])
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, "/usr/local/bin/seamark", ShellQuote("/usr/local/bin/seamark"))
	assert.Equal(t, "'/Apps/My Tools/seamark'", ShellQuote("/Apps/My Tools/seamark"))
	assert.Equal(t, `'/it'\''s/seamark'`, ShellQuote("/it's/seamark"))
}

func TestParseSettingsKeepsAnEmptyFileMalformed(t *testing.T) {
	_, err := ParseSettings(nil)
	require.Error(t, err, "only a missing file is an empty map; the caller decides that")

	settings, err := ParseSettings([]byte(`{"model":"opus"}`))
	require.NoError(t, err)
	assert.Equal(t, "opus", settings["model"])
}

// hookDoc builds a hook document with one PreToolUse entry per matcher.
func hookDoc(hookType, cmd string, matchers ...string) map[string]any {
	var entries []any

	for _, matcher := range matchers {
		e := map[string]any{"hooks": []any{map[string]any{"type": hookType, "command": cmd}}}
		if matcher != "" {
			e["matcher"] = matcher
		}

		entries = append(entries, e)
	}

	return map[string]any{"hooks": map[string]any{"PreToolUse": entries}}
}

func TestCoveredCountsOnlyAHookTheClientRuns(t *testing.T) {
	gate := ClaudeSpecs(ModeWarn)[0]

	for name, tc := range map[string]struct {
		settings map[string]any
		want     []string
	}{
		"bash matcher":         {hookDoc("command", "/bin/seamark gate --hook", "Bash"), []string{"Bash"}},
		"bash among others":    {hookDoc("command", "/bin/seamark gate --enforce --hook", "Bash|Edit"), []string{"Bash"}},
		"every tool":           {hookDoc("command", "/bin/seamark gate --hook", "*"), []string{"Bash"}},
		"no matcher":           {hookDoc("command", "/bin/seamark gate --hook", ""), []string{"Bash"}},
		"a pattern":            {hookDoc("command", "/bin/seamark gate --hook", "Ba.*"), []string{"Bash"}},
		"another tool":         {hookDoc("command", "/bin/seamark gate --hook", "Edit"), nil},
		"a longer exact name":  {hookDoc("command", "/bin/seamark gate --hook", "BashOutput"), nil},
		"a broken pattern":     {hookDoc("command", "/bin/seamark gate --hook", "Bash("), nil},
		"another hook type":    {hookDoc("prompt", "/bin/seamark gate --hook", "Bash"), nil},
		"another tool's hook":  {hookDoc("command", "/bin/company-security gate --hook", "Bash"), nil},
		"no hooks in the file": {map[string]any{}, nil},
	} {
		tools, cmd := Covered(tc.settings, gate, ClaudeMatcher)
		assert.Equal(t, tc.want, tools, name)

		if len(tools) > 0 {
			assert.Contains(t, cmd, "seamark gate", "%s: the running command is returned for the finding", name)
		} else {
			assert.Empty(t, cmd, name)
		}
	}

	// The warn spec lists the enforce marker as legacy, so a handler
	// installed in the other gate mode still counts as the same handler.
	tools, cmd := Covered(hookDoc("command", "/bin/seamark gate --enforce --hook", "Bash"), gate, ClaudeMatcher)
	assert.Equal(t, []string{"Bash"}, tools)
	assert.Equal(t, "/bin/seamark gate --enforce --hook", cmd)
}

func TestCoverageIsPerTool(t *testing.T) {
	lessons := ClaudeSpecs(ModeWarn)[1]
	require.Equal(t, []string{"Edit", "Write", "MultiEdit"}, lessons.Tools())

	// "Edit" is a whole-name pattern: it does not fire for MultiEdit.
	tools, _ := Covered(hookDoc("command", "/bin/seamark lessons --hook", "Edit"), lessons, ClaudeMatcher)
	assert.Equal(t, []string{"Edit"}, tools)

	// Several entries add up, in the spec's tool order per entry.
	tools, _ = Covered(hookDoc("command", "/bin/seamark lessons --hook", "MultiEdit", "Edit|Write"), lessons, ClaudeMatcher)
	assert.ElementsMatch(t, []string{"Edit", "Write", "MultiEdit"}, tools)

	// An event without a matcher has one unnamed tool: the event itself.
	reset := ClaudeSpecs(ModeWarn)[2]
	assert.Equal(t, []string{""}, reset.Tools())

	doc := map[string]any{"hooks": map[string]any{"PostCompact": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/bin/seamark lessons --hook-reset"}}},
	}}}
	tools, _ = Covered(doc, reset, ClaudeMatcher)
	assert.Equal(t, []string{""}, tools)
}

func TestParseDocumentReadsNullAsAnEmptyDocument(t *testing.T) {
	settings, err := ParseDocument([]byte("null"))
	require.NoError(t, err)

	changed, err := Merge(settings, "/bin/seamark", ClaudeSpecs(ModeWarn))
	require.NoError(t, err, "a nil map would panic here")
	assert.True(t, changed)

	_, err = ParseDocument([]byte("{ broken"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "settings.json", "the caller names the file")
}

func TestClaudeMatcherFollowsTheDocumentedRules(t *testing.T) {
	tools := []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit"}

	for matcher, want := range map[string][]string{
		// Match all.
		"":  tools,
		"*": tools,
		// Exact names: "|" or "," separates, spaces around a name do not count.
		"Edit":            {"Edit"},
		"Edit|Write":      {"Edit", "Write"},
		"Edit, Write":     {"Edit", "Write"},
		" Edit ,Write | ": {"Edit", "Write"},
		"edit":            nil,
		"code-reviewer":   nil,
		// Any other character: an unanchored regular expression.
		"Edit$":       {"Edit", "MultiEdit", "NotebookEdit"},
		"^Edit$":      {"Edit"},
		"Edit.*":      {"Edit", "MultiEdit", "NotebookEdit"},
		"^(Multi)?E":  {"Edit", "MultiEdit"},
		"mcp__.*":     nil,
		"(?=Edit)Ed":  nil, // JavaScript only; Go cannot compile it, so it covers nothing
		"Edit(":       nil,
		"Write|Ba.*$": {"Bash", "Write"},
	} {
		var got []string

		for _, tool := range tools {
			if ClaudeMatcher(matcher, tool) {
				got = append(got, tool)
			}
		}

		assert.Equal(t, want, got, "matcher %q", matcher)
	}
}

func TestEffectiveGateModeLetsEnforceWin(t *testing.T) {
	gate := ClaudeSpecs(ModeWarn)[0]

	both := hookDoc("command", "/bin/seamark gate --hook", "Bash")
	entries := both["hooks"].(map[string]any)["PreToolUse"].([]any)
	both["hooks"].(map[string]any)["PreToolUse"] = append(entries,
		hookDoc("command", "/opt/seamark gate --enforce --hook", "Bash, Edit")["hooks"].(map[string]any)["PreToolUse"].([]any)...)

	for name, tc := range map[string]struct {
		settings map[string]any
		want     string
	}{
		"warn":                     {hookDoc("command", "/bin/seamark gate --hook", "Bash"), ModeWarn},
		"enforce":                  {hookDoc("command", "/bin/seamark gate --enforce --hook", "*"), ModeEnforce},
		"one of two enforces":      {both, ModeEnforce},
		"enforce that never fires": {hookDoc("command", "/bin/seamark gate --enforce --hook", "Edit"), ""},
		"enforce of another type":  {hookDoc("prompt", "/bin/seamark gate --enforce --hook", "Bash"), ""},
		"another tool's gate":      {hookDoc("command", "/bin/company-security gate --enforce --hook", "Bash"), ""},
		"no document":              {nil, ""},
	} {
		// The spec of either mode finds the hook of both modes.
		assert.Equal(t, tc.want, EffectiveGateMode(tc.settings, gate, ClaudeMatcher), name)
		assert.Equal(t, tc.want, EffectiveGateMode(tc.settings, ClaudeSpecs(ModeEnforce)[0], ClaudeMatcher), name)
	}
}
