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

func TestFiresFollowsTheSpecToolsAndTheClientRule(t *testing.T) {
	gate := CodexSpecs(ModeWarn)[0]
	assert.True(t, Fires(gate, "Bash", CodexMatcher))
	assert.True(t, Fires(gate, "", CodexMatcher), "no matcher fires for every tool")
	assert.False(t, Fires(gate, "apply_patch", CodexMatcher), "a matcher of another tool never fires for Bash")

	lessons := ClaudeSpecs(ModeWarn)[1]
	assert.True(t, Fires(lessons, "Write", ClaudeMatcher), "one of the spec's tools is enough")
	assert.False(t, Fires(lessons, "Bash", ClaudeMatcher))

	reset := ClaudeSpecs(ModeWarn)[2]
	assert.True(t, Fires(reset, "anything", ClaudeMatcher), "an event without a matcher fires for every entry")
}

func TestEffectiveGateModeReadsTheCodexMarkers(t *testing.T) {
	codex := CodexSpecs(ModeWarn)[0]

	for name, tc := range map[string]struct {
		settings map[string]any
		want     string
	}{
		"warn":                   {hookDoc("command", "/bin/seamark gate --hook --client codex", "Bash"), ModeWarn},
		"enforce":                {hookDoc("command", "/bin/seamark gate --enforce --hook --client codex", "Bash"), ModeEnforce},
		"enforce under a regexp": {hookDoc("command", "/bin/seamark gate --enforce --hook --client codex", "^Bash$"), ModeEnforce},
		"a Claude Code gate":     {hookDoc("command", "/bin/seamark gate --enforce --hook", "Bash"), ""},
		"never fires":            {hookDoc("command", "/bin/seamark gate --enforce --hook --client codex", "apply_patch"), ""},
	} {
		assert.Equal(t, tc.want, EffectiveGateMode(tc.settings, codex, CodexMatcher), name)
		assert.Equal(t, tc.want, EffectiveGateMode(tc.settings, CodexSpecs(ModeEnforce)[0], CodexMatcher), name)
	}

	// The Claude Code reader stays Claude-specific: a Codex gate in a
	// Claude Code file is not a hook that Claude Code setup manages.
	assert.Empty(t, InstalledGateMode(hookDoc("command", "/bin/seamark gate --enforce --hook --client codex", "Bash")))
}

func TestCodexSpecsInstallTheGateAndLessonHooks(t *testing.T) {
	settings := map[string]any{}

	changed, err := Merge(settings, "/usr/local/bin/seamark", CodexSpecs(ModeWarn))
	require.NoError(t, err)
	assert.True(t, changed)

	hookMap := settings["hooks"].(map[string]any)
	require.Len(t, hookMap["PreToolUse"], 2, "the gate hook and the lessons hook")
	assert.NotContains(t, hookMap, "PostCompact",
		"a Codex reset clears nothing today, and setup installs no hook without an effect")

	gate := hookMap["PreToolUse"].([]any)[0].(map[string]any)
	assert.Equal(t, "Bash", gate["matcher"], "Codex names its shell tool Bash")

	handler := gate["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "/usr/local/bin/seamark gate --hook --client codex", handler["command"])
	assert.Equal(t, 15, handler["timeout"], "seconds, as Codex reads the field")

	lessons := hookMap["PreToolUse"].([]any)[1].(map[string]any)
	assert.Equal(t, "apply_patch", lessons["matcher"], "Codex reports every file edit as apply_patch")

	handler = lessons["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "/usr/local/bin/seamark lessons --hook --client codex", handler["command"])
	assert.Equal(t, 10, handler["timeout"])

	// A second merge finds its own hooks.
	changed, err = Merge(settings, "/usr/local/bin/seamark", CodexSpecs(ModeWarn))
	require.NoError(t, err)
	assert.False(t, changed)

	// A mode switch rewrites the gate hook in place: the opposite marker
	// is legacy, so no second gate entry appears.
	changed, err = Merge(settings, "/usr/local/bin/seamark", CodexSpecs(ModeEnforce))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{
		"/usr/local/bin/seamark gate --enforce --hook --client codex",
		"/usr/local/bin/seamark lessons --hook --client codex",
	}, commandsOf(settings, "PreToolUse"))

	assert.Equal(t, "gate", CodexSpecs(ModeEnforce)[0].Name, "the gate spec comes first, as in ClaudeSpecs")
	assert.Equal(t, "gate", ClaudeSpecs(ModeEnforce)[0].Name)
}

func TestCodexMatcherFollowsTheDocumentedRules(t *testing.T) {
	for name, tc := range map[string]struct {
		matcher, tool string
		want          bool
	}{
		"exact name":          {"Bash", "Bash", true},
		"another name":        {"Bash", "apply_patch", false},
		"empty matches all":   {"", "Bash", true},
		"star matches all":    {"*", "Bash", true},
		"alternation":         {"Bash|apply_patch", "Bash", true},
		"anchored":            {"^apply_patch$", "apply_patch", true},
		"unanchored search":   {"ash", "Bash", true},
		"a broken expression": {"(", "Bash", false},
	} {
		assert.Equal(t, tc.want, CodexMatcher(tc.matcher, tc.tool), name)
	}
}

func TestClaudeAndCodexMarkersNeverClaimEachOther(t *testing.T) {
	// Both clients can share one repository, and a document can hold the
	// commands of both. A marker matches as a suffix, so the shorter
	// Claude marker must not own the longer Codex command.
	// The Codex reset command exists although setup does not install it.
	claude := append(ClaudeSpecs(ModeWarn), ClaudeSpecs(ModeEnforce)[0])
	codex := append(CodexSpecs(ModeWarn), CodexSpecs(ModeEnforce)[0],
		Spec{Event: "PostCompact", Marker: CodexLessonsResetMarker})

	for _, c := range claude {
		for _, x := range codex {
			assert.False(t, OwnedBySeamark(x.Command("/bin/seamark"), c.Markers()),
				"%q must not own %q", c.Marker, x.Marker)
			assert.False(t, OwnedBySeamark(c.Command("/bin/seamark"), x.Markers()),
				"%q must not own %q", x.Marker, c.Marker)
		}
	}

	// One document with both sets converges for both.
	settings := map[string]any{}
	claude, codex = ClaudeSpecs(ModeWarn), CodexSpecs(ModeWarn)

	_, err := Merge(settings, "/bin/seamark", claude)
	require.NoError(t, err)
	_, err = Merge(settings, "/bin/seamark", codex)
	require.NoError(t, err)

	for _, specs := range [][]Spec{claude, codex} {
		changed, err := Merge(settings, "/bin/seamark", specs)
		require.NoError(t, err)
		assert.False(t, changed)
	}

	assert.Len(t, settings["hooks"].(map[string]any)["PreToolUse"], 4, "two gate hooks and two lessons hooks")
}

func TestWrappedFindsASeamarkHookBehindAnotherCommand(t *testing.T) {
	lessons := CodexSpecs(ModeWarn)[1]

	settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "apply_patch", "hooks": []any{
			map[string]any{"type": "command", "command": "/bin/seamark lessons --hook --client codex"},
			map[string]any{"type": "command", "command": "/opt/wrap.sh seamark lessons --hook --client codex"},
			map[string]any{"type": "command", "command": "sh -c 'seamark lessons --hook --client codex >> /tmp/log'"},
			map[string]any{"type": "command", "command": "/opt/seamark2 lessons --hook --client codex"},
			map[string]any{"type": "command", "command": "/opt/audit.sh"},
		}},
	}}}

	assert.Equal(t, []WrappedCommand{
		// An unknown program with the seamark command as its arguments.
		{Command: "/opt/wrap.sh seamark lessons --hook --client codex", Matcher: "apply_patch", Type: "command"},
		// A shell script that executes the seamark command.
		{Command: "sh -c 'seamark lessons --hook --client codex >> /tmp/log'", Certain: true, Matcher: "apply_patch", Type: "command"},
	}, Wrapped(settings, lessons),
		"the owned hook, the lookalike binary, and the unrelated hook run no seamark hook through a wrapper")

	assert.Empty(t, Wrapped(map[string]any{}, lessons))
	assert.Empty(t, Wrapped(settings, Spec{Event: "PostCompact", Marker: CodexLessonsResetMarker}),
		"another event holds no wrapper")
}

func TestOwnershipNeedsOneStandaloneSeamarkWord(t *testing.T) {
	markers := []string{CodexLessonsMarker}
	tail := " " + CodexLessonsMarker

	for _, cmd := range []string{
		"seamark" + tail,
		"/usr/local/bin/seamark" + tail,
		"/usr/local/bin/seamark.exe" + tail,
		"'/opt/my tools/seamark'" + tail,
		`'/opt/it'\''s here/seamark'` + tail,
		"'/usr/local/bin/seamark'" + tail, // quoted without need: still one word
	} {
		assert.True(t, OwnedBySeamark(cmd, markers), cmd)
		assert.Equal(t, HookRuns, SeamarkHookUse(cmd, markers), "an owned command runs the hook: %s", cmd)
	}

	// Setup rewrites what it owns. Each of these ends like our command,
	// and a rewrite removes the part that the user put in front.
	for _, cmd := range []string{
		"/opt/wrapper /usr/local/bin/seamark" + tail,
		"test -x /usr/local/bin/seamark && /usr/local/bin/seamark" + tail,
		"env SEAMARK_DEBUG=1 /usr/local/bin/seamark" + tail,
		"cd /repo; /usr/local/bin/seamark" + tail,
		"timeout 5 seamark" + tail,
		`"/opt/my tools/seamark"` + tail,
		"'/opt/a' '/usr/local/bin/seamark'" + tail,
		"$(which seamark)" + tail,
	} {
		assert.False(t, OwnedBySeamark(cmd, markers), "not one standalone word: %s", cmd)
	}

	// Another binary is never ours, in any form.
	for _, cmd := range []string{"/opt/seamark2" + tail, "company-security" + tail, tail, CodexLessonsMarker} {
		assert.False(t, OwnedBySeamark(cmd, markers), cmd)
		assert.Equal(t, HookNotRun, SeamarkHookUse(cmd, markers), cmd)
	}
}

func TestMergeKeepsAWrappedCommandAsItIs(t *testing.T) {
	// The reported defect: the wrapper ends like our command, and the
	// merge rewrote it to the bare command.
	wrapped := "/opt/wrapper /usr/local/bin/seamark " + CodexLessonsMarker
	settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "apply_patch", "hooks": []any{map[string]any{"type": "command", "command": wrapped}}},
	}}}

	_, err := Merge(settings, "/usr/local/bin/seamark", CodexSpecs(ModeWarn))
	require.NoError(t, err)

	assert.Contains(t, commandsOf(settings, "PreToolUse"), wrapped, "the wrapper survives the merge")
	assert.Equal(t, []WrappedCommand{{Command: wrapped, Matcher: "apply_patch", Type: "command"}},
		Wrapped(settings, CodexSpecs(ModeWarn)[1]))

	// The same holds for the Claude Code gate hook.
	gate := "test -x /usr/local/bin/seamark && /usr/local/bin/seamark gate --enforce --hook"
	claude := map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": gate}}},
	}}}

	assert.Empty(t, InstalledGateMode(claude), "a wrapped gate is not a gate that setup manages")

	_, err = Merge(claude, "/usr/local/bin/seamark", ClaudeSpecs(ModeWarn))
	require.NoError(t, err)
	assert.Contains(t, commandsOf(claude, "PreToolUse"), gate)
	assert.Equal(t, ModeWarn, InstalledGateMode(claude), "the managed hook is the one that was added")
}

func TestSeamarkHookUseFollowsExecutionAndQuoting(t *testing.T) {
	codex := []string{CodexLessonsMarker}
	full := "/usr/local/bin/seamark " + CodexLessonsMarker

	cases := []struct {
		name string
		cmd  string
		want HookUse
	}{
		// The reported defect: the full command as printed text. One quoted
		// word is one argument of echo, and nothing executes it.
		{"echo with the command in single quotes", "echo '" + full + "'", HookNotRun},
		{"echo with the command in double quotes", `echo "` + full + `"`, HookNotRun},
		{"printf with the command as its format", "printf '%s\\n' '" + full + "'", HookNotRun},
		{"a quoted command inside a longer text", `logger "would run: ` + full + ` (disabled)"`, HookNotRun},
		{"a comment-like note in an argument", "true '" + full + "'", HookNotRun},
		{"the marker text without a binary", `echo "` + CodexLessonsMarker + `"`, HookNotRun},
		{"a lookalike binary", "/opt/seamark2 " + CodexLessonsMarker, HookNotRun},
		{"another seamark hook", "/usr/local/bin/seamark lessons --hook", HookNotRun},
		{"the reset hook", "/usr/local/bin/seamark lessons --hook-reset --client codex", HookNotRun},
		{"more arguments than the hook has", full + " --verbose", HookNotRun},
		{"nothing", "", HookNotRun},

		// The shell executes the seamark command.
		{"the bare command", full, HookRuns},
		{"a quoted binary path", "'/opt/my tools/seamark' " + CodexLessonsMarker, HookRuns},
		{"a double-quoted binary path", `"/opt/my tools/seamark" ` + CodexLessonsMarker, HookRuns},
		{"a shell condition", "test -x /usr/local/bin/seamark && " + full, HookRuns},
		{"a command list", "cd /repo; " + full, HookRuns},
		{"a fallback", full + " || true", HookRuns},
		{"redirects", full + " >> /tmp/log 2>&1", HookRuns},
		{"a redirect with an attached target", full + " 2>/dev/null", HookRuns},
		{"an environment assignment", "SEAMARK_DEBUG=1 " + full, HookRuns},
		{"env", "env SEAMARK_DEBUG=1 " + full, HookRuns},
		{"timeout", "timeout 5 " + full, HookRuns},
		{"a subshell", "(cd /repo && " + full + ")", HookRuns},
		{"a pipeline", full + " | tee /tmp/log", HookRuns},
		{"sh -c with the command as the script", "sh -c '" + full + "'", HookRuns},
		{"bash -lc with a redirect inside", `bash -lc "` + full + ` >> /tmp/log"`, HookRuns},
		{"escaped spaces in the binary path", `/opt/my\ tools/seamark ` + CodexLessonsMarker, HookRuns},

		// Another program gets the seamark command as its arguments. A
		// wrapper runs them, and echo prints them: the words do not say.
		{"an unknown wrapper", "/opt/wrapper " + full, HookMayRun},
		{"echo without quotes", "echo " + full, HookMayRun},
		{"a substitution the reader does not follow", "$(which seamark) " + CodexLessonsMarker + "; " + full, HookMayRun},
		{"an unclosed quote around real words", "/opt/wrapper " + full + " '", HookMayRun},

		// An echo inside a script is still an echo.
		{"sh -c that only prints", `sh -c "echo '` + full + `'"`, HookNotRun},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SeamarkHookUse(tc.cmd, codex), tc.cmd)
		})
	}

	// One marker is a prefix of another. The Claude Code marker must not
	// match inside the Codex command or the reset command.
	claude := []string{LessonsMarker}
	assert.Equal(t, HookNotRun, SeamarkHookUse(full, claude))
	assert.Equal(t, HookNotRun, SeamarkHookUse("/bin/seamark lessons --hook-reset", claude))
	assert.Equal(t, HookRuns, SeamarkHookUse("sh -c '/bin/seamark lessons --hook'", claude))
}
