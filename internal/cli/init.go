package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/index"
)

func newInitCmd(opts *options) *cobra.Command {
	var printOnly bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up seamark in this repository (config scaffolds + agent hooks)",
		Long: `Prepares a repository to use seamark:

  - scaffolds .seamark/policy.yaml, .seamark/lessons.yaml and
    .seamark/config.yaml (starters, never overwriting an existing file)
  - adds the .gitignore carve-outs that keep the index local but the
    policy-as-code files in review
  - wires the Claude Code PreToolUse hooks into .claude/settings.json:
    the command gate on Bash, the review-lessons reminder on edits —
    merged into any existing hooks, and safe to re-run

Use --print to preview every change without writing anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return err
			}

			bin := seamarkPath()

			return runInit(cmd.OutOrStdout(), root, bin, printOnly)
		},
	}

	cmd.Flags().BoolVar(&printOnly, "print", false, "preview changes without writing")

	return cmd
}

// seamarkPath resolves the absolute path of the running binary, so the
// hooks invoke this exact install regardless of the caller's PATH. Falls
// back to the bare name if resolution fails.
func seamarkPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "seamark"
	}

	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}

	return exe
}

func runInit(w io.Writer, root, bin string, printOnly bool) error {
	verb := "wrote"
	if printOnly {
		verb = "would write"
	}

	// 1. Config scaffolds — never clobber an existing file.
	for _, f := range []struct {
		rel, body string
	}{
		{".seamark/policy.yaml", starterPolicy},
		{".seamark/lessons.yaml", starterLessons},
		{".seamark/config.yaml", starterConfig},
	} {
		path := filepath.Join(root, filepath.FromSlash(f.rel))
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(w, "  kept    %s (already present)\n", f.rel)
			continue
		}

		if !printOnly {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}

			if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
				return err
			}
		}

		fmt.Fprintf(w, "  %s  %s\n", verb, f.rel)
	}

	// 2. .gitignore carve-outs.
	if err := ensureGitignore(w, root, printOnly); err != nil {
		return err
	}

	// 3. Claude Code hooks.
	if err := ensureHooks(w, root, bin, printOnly); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nnext: `seamark index` to build the graph, "+
		"`seamark index --reviews` to mine review lessons\n")

	if printOnly {
		fmt.Fprintf(w, "(nothing was written — --print)\n")
	}

	return nil
}

// gitignoreBlock is appended verbatim when no carve-outs are present; a
// .gitignore initialized by an older seamark grows just the lines it is
// missing.
const gitignoreBlock = `
# Seamark: the index and audit log stay local; the policy-as-code
# overlays belong in review.
.seamark/*
!.seamark/policy.yaml
!.seamark/effects.yaml
!.seamark/lessons.yaml
!.seamark/config.yaml
`

// carveoutLines returns the pattern lines of gitignoreBlock (comments and
// blanks dropped) — the unit ensureGitignore checks and repairs, so a new
// overlay file added to the block reaches already-initialized repos too.
func carveoutLines() []string {
	var lines []string

	for _, ln := range strings.Split(gitignoreBlock, "\n") {
		if ln != "" && !strings.HasPrefix(ln, "#") {
			lines = append(lines, ln)
		}
	}

	return lines
}

func ensureGitignore(w io.Writer, root string, printOnly bool) error {
	path := filepath.Join(root, ".gitignore")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	present := map[string]bool{}
	for _, ln := range strings.Split(string(data), "\n") {
		present[strings.TrimSpace(ln)] = true
	}

	lines := carveoutLines()

	var missing []string

	for _, ln := range lines {
		if !present[ln] {
			missing = append(missing, ln)
		}
	}

	if len(missing) == 0 {
		fmt.Fprintf(w, "  kept    .gitignore (seamark carve-outs already present)\n")
		return nil
	}

	// No carve-outs at all gets the commented block; an older block grows
	// just its missing lines. Appending at the end keeps every `!`
	// re-include after `.seamark/*`, the order gitignore precedence needs.
	block := gitignoreBlock
	if len(missing) < len(lines) {
		block = "\n" + strings.Join(missing, "\n") + "\n"
	}

	verb := "updated"
	if printOnly {
		verb = "would update"
	}

	if !printOnly {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()

		if _, err := f.WriteString(block); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "  %s .gitignore (seamark carve-outs)\n", verb)

	return nil
}

// hookSpec is one PreToolUse hook seamark installs.
type hookSpec struct {
	matcher string
	// marker is the command's argument tail (everything after the binary
	// path). It is matched as a suffix — with the joining space — to
	// recognize an existing seamark hook across path changes, without
	// clobbering an unrelated command that merely contains the same text.
	marker  string
	status  string
	timeout int
}

var hookSpecs = []hookSpec{
	{"Bash", "gate --enforce --hook", "seamark gate: classifying command", 15},
	{"Edit|Write|MultiEdit", "lessons --hook", "seamark: checking review lessons", 10},
}

func ensureHooks(w io.Writer, root, bin string, printOnly bool) error {
	path := filepath.Join(root, ".claude", "settings.json")

	settings := map[string]any{}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %w (fix or move it, then re-run)", path, err)
		}
	case !os.IsNotExist(err):
		return err
	}

	changed, err := mergeHooks(settings, bin)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if !changed {
		fmt.Fprintf(w, "  kept    .claude/settings.json (seamark hooks already wired)\n")
		return nil
	}

	verb := "updated"
	if printOnly {
		verb = "would update"
	}

	if !printOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}

		out, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}

		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "  %s .claude/settings.json (PreToolUse hooks: gate + lessons)\n", verb)

	return nil
}

// mergeHooks adds seamark's PreToolUse hooks into an existing settings
// map, preserving every other hook and updating (never duplicating) a
// seamark hook already present. Returns whether anything changed. It
// errors rather than silently overwriting a present-but-wrong-typed
// hooks/PreToolUse field — the same loud handling ensureHooks gives
// malformed JSON.
func mergeHooks(settings map[string]any, bin string) (changed bool, err error) {
	hooks, err := childMap(settings, "hooks")
	if err != nil {
		return false, err
	}

	pre, err := childSlice(hooks, "PreToolUse")
	if err != nil {
		return false, err
	}

	for _, spec := range hookSpecs {
		want := shellQuote(bin) + " " + spec.marker

		found, updated := applyExisting(pre, spec.marker, want)
		if found {
			changed = changed || updated
			continue
		}

		pre = append(pre, map[string]any{
			"matcher": spec.matcher,
			"hooks": []any{map[string]any{
				"type":          "command",
				"command":       want,
				"timeout":       spec.timeout,
				"statusMessage": spec.status,
			}},
		})
		changed = true
	}

	hooks["PreToolUse"] = pre

	return changed, nil
}

// applyExisting rewrites seamark's own hook command to want if present,
// recognized by the marker as a command suffix (with its joining space)
// so an unrelated command that merely contains the marker text is left
// alone. Reports whether such a hook existed and whether it changed.
func applyExisting(pre []any, marker, want string) (found, updated bool) {
	suffix := " " + marker

	forEachCommand(pre, func(h map[string]any, cmd string) {
		if !strings.HasSuffix(cmd, suffix) {
			return
		}

		found = true

		if cmd != want {
			h["command"] = want
			updated = true
		}
	})

	return found, updated
}

// shellQuote single-quotes a path that a shell would otherwise split or
// interpret; a clean path is returned as-is. Claude Code runs hook
// commands through a shell, so a binary path with spaces must be quoted.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\$`(){}[]*?&|;<>#~") {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// forEachCommand visits every command hook under a PreToolUse array.
func forEachCommand(pre []any, fn func(h map[string]any, cmd string)) {
	for _, e := range pre {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}

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
				fn(hm, cmd)
			}
		}
	}
}

// childMap returns m[key] as a map, creating it in place if absent. A
// present-but-wrong-typed value is an error, not a silent overwrite —
// clobbering the user's data to install a hook is exactly what init must
// never do.
func childMap(m map[string]any, key string) (map[string]any, error) {
	switch v := m[key].(type) {
	case map[string]any:
		return v, nil
	case nil:
		child := map[string]any{}
		m[key] = child

		return child, nil
	default:
		return nil, fmt.Errorf("%q is present but not an object; refusing to overwrite it", key)
	}
}

// childSlice returns m[key] as a slice, nil if absent, or an error if
// present with the wrong type (same reasoning as childMap).
func childSlice(m map[string]any, key string) ([]any, error) {
	switch v := m[key].(type) {
	case []any:
		return v, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%q is present but not an array; refusing to overwrite it", key)
	}
}
