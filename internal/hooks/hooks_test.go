package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOwnedBySeamark(t *testing.T) {
	markers := []string{"gate --hook", "gate --enforce --hook"}

	for cmd, want := range map[string]bool{
		"/bin/seamark gate --hook":                        true,
		"/bin/seamark gate --enforce --hook":              true,
		"'/Apps/My Tools/seamark' gate --hook":            true,
		"/c/tools/seamark.exe gate --hook":                true,
		"'/c/my tools/seamark.exe' gate --enforce --hook": true,
		"/usr/bin/company-security gate --hook":           false,
		"/bin/seamarketing-tool gate --hook":              false,
		"/bin/seamark2 gate --enforce --hook":             false,
		"/bin/seamark2.exe gate --hook":                   false,
		"/bin/seamark lessons --hook":                     false, // not a gate marker
		"my-tool --lessons --hook-dir=/x":                 false,
	} {
		assert.Equal(t, want, OwnedBySeamark(cmd, markers), "cmd: %s", cmd)
	}
}

func TestInstalledGateModeFollowsClaudeCodeMatchers(t *testing.T) {
	assert.Empty(t, InstalledGateMode(map[string]any{}), "no settings, no hook")

	doc := func(entries ...map[string]any) map[string]any {
		pre := make([]any, 0, len(entries))
		for _, e := range entries {
			pre = append(pre, e)
		}

		return map[string]any{"hooks": map[string]any{"PreToolUse": pre}}
	}

	entry := func(matcher, hookType, cmd string) map[string]any {
		return map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": hookType, "command": cmd}}}
	}

	const (
		enforce = "/bin/seamark gate --enforce --hook"
		warn    = "/bin/seamark gate --hook"
	)

	for name, tc := range map[string]struct {
		settings map[string]any
		want     string
	}{
		"a foreign hook is not ours": {doc(entry("Bash", "command", "/usr/bin/company-security gate --enforce --hook")), ""},
		"warn":                       {doc(entry("Bash", "command", warn)), ModeWarn},
		"enforce":                    {doc(entry("Bash", "command", enforce)), ModeEnforce},
		// A gate command that Claude Code never runs for Bash is not an
		// operational gate hook.
		"an Edit matcher":       {doc(entry("Edit", "command", enforce)), ""},
		"another hook type":     {doc(entry("Bash", "notify", enforce)), ""},
		"a longer exact name":   {doc(entry("Bashful", "command", enforce)), ""},
		"a star matcher":        {doc(entry("*", "command", enforce)), ModeEnforce},
		"an empty matcher":      {doc(entry("", "command", enforce)), ModeEnforce},
		"a regular expression":  {doc(entry("Ba.*", "command", enforce)), ModeEnforce},
		"an anchored pattern":   {doc(entry("^Bash$", "command", enforce)), ModeEnforce},
		"a name list":           {doc(entry("Bash|Edit", "command", enforce)), ModeEnforce},
		"enforce wins, first":   {doc(entry("Bash", "command", enforce), entry("Bash", "command", warn)), ModeEnforce},
		"enforce wins, last":    {doc(entry("Bash", "command", warn), entry("Bash", "command", enforce)), ModeEnforce},
		"a non-firing enforcer": {doc(entry("Edit", "command", enforce), entry("Bash", "command", warn)), ModeWarn},
	} {
		assert.Equal(t, tc.want, InstalledGateMode(tc.settings), name)
	}

	omitted := map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": enforce}}},
	}}}
	assert.Equal(t, ModeEnforce, InstalledGateMode(omitted), "an omitted matcher fires for every tool")
}
