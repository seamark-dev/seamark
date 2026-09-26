// Package hooks knows how seamark's Claude Code hooks are
// spelled in .claude/settings.json: the marker strings, the ownership
// rule, and gate-mode detection live here once — init writes hooks,
// status reads them, and two copies of the matching logic would drift.
// The merge that installs the hooks lives here too (merge.go), so init
// and the client setup adapters share one implementation.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// commands: exactly one shell word that names the seamark binary, then
// a marker, and nothing else. Setup rewrites a command it owns, so the
// rule must be strict in both directions.
//
// The binary check keeps the marker from claiming someone else's hook:
// `company-security gate --hook` (or a lookalike such as `seamark2`)
// is not ours. The one-word check keeps setup from destroying a wrapper:
// `/opt/wrapper /usr/local/bin/seamark gate --hook` ends like our command,
// and a rewrite to the bare command removes the wrapper without a word.
func OwnedBySeamark(cmd string, markers []string) bool {
	for _, marker := range markers {
		rest, ok := strings.CutSuffix(cmd, " "+marker)
		if !ok {
			continue
		}

		binary, ok := shellWord(rest)

		return ok && IsSeamarkBinary(binary)
	}

	return false
}

// shellWord returns the value of s when s is exactly one shell word in
// a form that ShellQuote writes: a plain word without a character that a
// shell interprets, or one single-quoted string. Anything else, such as
// two words or a double-quoted string, is not one word that setup knows.
func shellWord(s string) (string, bool) {
	if s == "" {
		return "", false
	}

	if !strings.ContainsAny(s, shellSpecial) {
		return s, true
	}

	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}

	// Inside the quotes, ShellQuote writes a quote character as '\''.
	// Any other quote character ends the string early: more than one word.
	inner := strings.ReplaceAll(s[1:len(s)-1], `'\''`, "\x00")
	if strings.ContainsRune(inner, '\'') {
		return "", false
	}

	return strings.ReplaceAll(inner, "\x00", "'"), true
}

// IsSeamarkBinary reports whether a command path names the seamark
// binary: exact basename "seamark", tolerating the Windows suffix. The
// hook owner, the Claude Code registration, and the Codex registration
// all use this one rule, so they agree on what is seamark's.
func IsSeamarkBinary(command string) bool {
	return strings.TrimSuffix(filepath.Base(command), ".exe") == "seamark"
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

// The Codex hook commands name their client. A hook command without
// --client keeps Claude Code semantics, because every installed Claude
// hook runs exactly that command. Each marker matches as a suffix, so a
// Claude marker never claims a Codex command and the reverse.
const (
	CodexLessonsMarker      = "lessons --hook --client codex"
	CodexLessonsResetMarker = "lessons --hook-reset --client codex"
)

// codexClientSelector is the argument that names Codex in a hook command.
const codexClientSelector = " --client codex"

// CodexGateMarker returns the Codex gate hook's argument tail for a
// mode. It is the Claude Code marker plus the client selector, so the
// mode rule of GateMarker holds for every client: a gate marker starts
// with GateMarker(mode), and the selector follows.
func CodexGateMarker(mode string) string {
	return GateMarker(mode) + codexClientSelector
}

// gateMarkerMode returns the mode that a gate marker bakes in. Every
// gate marker starts with GateMarker(mode); a client selector can
// follow. A marker of another hook gives "".
func gateMarkerMode(marker string) string {
	for _, mode := range []string{ModeEnforce, ModeWarn} {
		prefix := GateMarker(mode)
		if marker == prefix || strings.HasPrefix(marker, prefix+" ") {
			return mode
		}
	}

	return ""
}

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

	return ParseSettings(data)
}

// ParseSettings decodes the bytes of .claude/settings.json. The setup
// planner reads the file once under a guard and parses those same
// bytes, so the plan and its guard always describe one file state. An
// empty file is malformed JSON, as it always was; only a missing file
// is an empty map, and the caller decides that.
func ParseSettings(data []byte) (map[string]any, error) {
	settings, err := ParseDocument(data)
	if err != nil {
		return nil, fmt.Errorf(".claude/settings.json: %w", err)
	}

	return settings, nil
}

// ParseDocument decodes one JSON hook document without naming a file
// in the error, so a caller that reads another file, such as
// .claude/settings.local.json, can name the right one. A top-level
// null is an empty document: it holds nothing, and a nil map would
// panic on the first merge.
func ParseDocument(data []byte) (map[string]any, error) {
	settings := map[string]any{}

	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	if settings == nil {
		settings = map[string]any{}
	}

	return settings, nil
}

// ParseDocumentExact is ParseDocument for a document that setup writes
// back. It keeps every number as written: the default decoder reads a
// number as a float64, which rounds an integer above 2^53 and turns 1.0
// into 1. FormatDocumentExact is the matching encoder.
//
// The Claude Code settings keep ParseDocument and their own encoder,
// because a file that init wrote before must stay byte-stable.
func ParseDocumentExact(data []byte) (map[string]any, error) {
	document := map[string]any{}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}

	// One document per file. A second value is a broken file.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after the JSON document")
	}

	if document == nil {
		document = map[string]any{}
	}

	return document, nil
}

// FormatDocumentExact encodes a hook document with two-space indent and
// a final newline. It does not escape &, <, and >: a hook command such
// as "a && b" is the user's text, and "a \u0026\u0026 b" is hard to read
// and to review.
func FormatDocumentExact(document map[string]any) ([]byte, error) {
	var out bytes.Buffer

	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(document); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
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
