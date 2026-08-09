// Package hooks knows how seamark's Claude Code hooks are
// spelled in .claude/settings.json: the marker strings, the ownership
// rule, and gate-mode detection live here once — init writes hooks,
// status reads them, and two copies of the matching logic would drift.
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Gate hook modes. Warn installs a hook that follows .seamark/policy.yaml
// and never blocks by itself; enforce bakes --enforce into the hook, so
// blocking verdicts exit 2 and the gate's own failures fail closed.
const (
	ModeWarn    = "warn"
	ModeEnforce = "enforce"
)

// GateMarker returns the gate hook's argument tail for a mode. Warn omits
// --enforce so .seamark/policy.yaml stays the single source of truth for
// blocking; enforce bakes the flag in, which also makes the gate's own
// failures block (fail closed).
func GateMarker(mode string) string {
	if mode == ModeEnforce {
		return "gate --enforce --hook"
	}

	return "gate --hook"
}

// ForEachCommand visits every command hook under one hook-event array,
// passing each entry's matcher alongside the hook.
func ForEachCommand(pre []any, fn func(matcher string, h map[string]any, cmd string)) {
	for _, e := range pre {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}

		matcher, _ := entry["matcher"].(string)

		hs, ok := entry["hooks"].([]any)
		if !ok {
			continue
		}

		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}

			if cmd, ok := hm["command"].(string); ok {
				fn(matcher, hm, cmd)
			}
		}
	}
}

// OwnedBySeamark reports whether cmd is one of seamark's own hook
// commands: a marker suffix AND a binary whose basename is exactly
// seamark (allowing the Windows .exe suffix). The binary check keeps the
// marker from claiming someone else's hook — `company-security gate
// --hook` (or a lookalike such as `seamark2`) must never be treated as
// ours.
func OwnedBySeamark(cmd string, markers []string) bool {
	for _, marker := range markers {
		rest, ok := strings.CutSuffix(cmd, " "+marker)
		if !ok {
			continue
		}

		base := filepath.Base(strings.Trim(rest, "'"))

		return strings.TrimSuffix(base, ".exe") == "seamark"
	}

	return false
}

// InstalledGateMode reports the mode of an OPERATIONAL gate hook in a
// parsed settings map: enforce, warn, or "" when none is present. A gate
// command only counts when wired the way Claude Code will actually run
// it — a "command"-typed hook whose matcher covers Bash; the same
// command under another matcher never fires on shell commands and must
// not report as installed.
func InstalledGateMode(settings map[string]any) string {
	hooks, _ := settings["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)

	mode := ""

	ForEachCommand(pre, func(matcher string, h map[string]any, cmd string) {
		if !strings.Contains(matcher, "Bash") {
			return
		}

		if t, _ := h["type"].(string); t != "command" {
			return
		}

		switch {
		case OwnedBySeamark(cmd, []string{GateMarker(ModeEnforce)}):
			mode = ModeEnforce
		case OwnedBySeamark(cmd, []string{GateMarker(ModeWarn)}):
			mode = ModeWarn
		}
	})

	return mode
}

// LessonsMarker is the edit-lessons hook's argument tail.
const LessonsMarker = "lessons --hook"

// LessonsResetMarker is the PostCompact hook that starts a new lesson
// delivery generation for the provider session.
const LessonsResetMarker = "lessons --hook-reset"

// LessonsHookInstalled reports whether seamark's edit-lessons hook is
// operational in a parsed settings map: a "command"-typed hook under a
// matcher covering Edit tools.
func LessonsHookInstalled(settings map[string]any) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	pre, _ := hooks["PreToolUse"].([]any)

	found := false

	ForEachCommand(pre, func(matcher string, h map[string]any, cmd string) {
		if !strings.Contains(matcher, "Edit") {
			return
		}

		if t, _ := h["type"].(string); t != "command" {
			return
		}

		if OwnedBySeamark(cmd, []string{LessonsMarker}) {
			found = true
		}
	})

	return found
}

// ReadSettings loads <root>/.claude/settings.json; a missing file is an
// empty map, an unparseable one an error.
func ReadSettings(root string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}

	if err != nil {
		return nil, err
	}

	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf(".claude/settings.json: %w", err)
	}

	return settings, nil
}

// InstalledGateModeAt reads <root>/.claude/settings.json and reports the
// installed gate-hook mode; "" with a nil error when the file is absent
// or simply carries no seamark gate hook. An unreadable or unparseable
// file is an error — "your hook configuration cannot be read" and "no
// hook installed" are different findings and must not be conflated.
func InstalledGateModeAt(root string) (string, error) {
	settings, err := ReadSettings(root)
	if err != nil {
		return "", err
	}

	return InstalledGateMode(settings), nil
}
