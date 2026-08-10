package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
)

// Gate hook modes — shared with the status surfaces via internal/hooks,
// which owns the marker spellings and the ownership rule.
const (
	gateModeWarn    = hooks.ModeWarn
	gateModeEnforce = hooks.ModeEnforce
)

func newInitCmd(opts *options) *cobra.Command {
	var (
		printOnly bool
		gateMode  string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up seamark in this repository (config scaffolds + agent hooks)",
		Long: `Prepares a repository to use seamark:

  - scaffolds .seamark/policy.yaml, .seamark/lessons.yaml and
    .seamark/config.yaml (starters, never overwriting an existing file)
  - adds the .gitignore carve-outs that keep the index local but the
    policy-as-code files in review
  - wires Claude Code hooks into .claude/settings.json: the command gate
    on Bash, the review-lessons reminder on edits, and a PostCompact
    delivery reset — merged into any existing hooks, and safe to re-run

A first init never blocks anything: it installs the gate hook in warn
mode, which reports verdicts and always lets the command through.
Enforcement is an explicit opt-in:

  seamark init --gate-mode enforce

That bakes --enforce into the hook: deny/require_approval verdicts exit 2
(blocking the agent's command) and the gate's own failures fail closed.
A fresh init also scaffolds .seamark/policy.yaml with the matching mode;
an existing policy file is never modified.

Re-running init without --gate-mode keeps whatever mode is installed —
enforcement is never added or removed implicitly. Every run ends with a
"gate" line stating the effective behaviour, derived from the installed
hook and the policy file actually on disk.

Use --print to preview every change without writing anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if gateMode != "" && gateMode != gateModeWarn && gateMode != gateModeEnforce {
				return fmt.Errorf("init: --gate-mode must be %s or %s, got %q",
					gateModeWarn, gateModeEnforce, gateMode)
			}

			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return err
			}

			bin := seamarkPath()

			return runInit(cmd.OutOrStdout(), root, bin, gateMode, printOnly)
		},
	}

	cmd.Flags().BoolVar(&printOnly, "print", false, "preview changes without writing")
	cmd.Flags().StringVar(&gateMode, "gate-mode", "",
		"gate hook mode: warn (report, never block) or enforce (blocking verdicts exit 2); "+
			"omitted keeps the installed mode (warn on first init)")

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

func runInit(w io.Writer, root, bin, gateMode string, printOnly bool) error {
	// 0. Load, validate and merge .claude/settings.json BEFORE touching
	// anything: a malformed or wrong-shaped file must abort init before
	// the scaffold writes, never halfway through them.
	settingsPath := filepath.Join(root, ".claude", "settings.json")

	settings, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}

	gateMode = resolveGateMode(settings, gateMode)

	// Remember the pre-merge gate mode: silently dropping enforcement on
	// a re-init would be as bad as silently installing it, so a mode
	// change on an existing hook is reported loudly by writeHooks.
	previous := installedGateMode(settings)

	hooksChanged, err := mergeHooks(settings, bin, gateMode)
	if err != nil {
		return fmt.Errorf("%s: %w", settingsPath, err)
	}

	verb := "wrote"
	if printOnly {
		verb = "would write"
	}

	// 1. Config scaffolds — never clobber an existing file.
	for _, f := range []struct {
		rel, body string
	}{
		{".seamark/policy.yaml", starterPolicyFor(gateMode)},
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

	// 3. Claude Code hooks — validated and merged above, write-only here.
	if err := writeHooks(w, settingsPath, settings, hooksChanged, bin, gateMode, previous, printOnly); err != nil {
		return err
	}

	// 4. Report the EFFECTIVE blocking behaviour, derived from the hook
	// AND the policy file actually on disk — not from the flag alone: a
	// kept policy.yaml can carry a different mode than the hook, and
	// claiming "nothing blocks" while a kept `mode: enforce` still blocks
	// would repeat the exact trust bug this command exists to prevent.
	policyMode, policyErr := policyFileMode(root, gateMode)

	switch {
	case policyErr != nil && gateMode == gateModeEnforce:
		fmt.Fprintf(w, "  gate    enforce — but .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook fails closed: EVERY hooked command blocks until the policy is fixed\n", policyErr)
	case policyErr != nil:
		fmt.Fprintf(w, "  gate    warn — .seamark/policy.yaml failed to load (%v);\n"+
			"          the hook reports the error and fails open\n", policyErr)
	case gateMode == gateModeEnforce:
		fmt.Fprintf(w, "  gate    enforce — deny/require_approval verdicts exit 2 and block; gate failures fail closed\n")

		if policyMode != gateModeEnforce {
			fmt.Fprintf(w, "          note: the kept .seamark/policy.yaml says mode: %s — it still governs plain\n"+
				"          `seamark gate` and `seamark check` runs; only the Claude hook enforces\n", policyMode)
		}
	case policyMode == gateModeEnforce:
		fmt.Fprintf(w, "  gate    enforce — the kept .seamark/policy.yaml sets mode: enforce and the hook\n"+
			"          follows it: deny/require_approval verdicts exit 2 and block; edit policy.yaml\n"+
			"          to stop blocking\n")
	default:
		fmt.Fprintf(w, "  gate    warn — verdicts are reported, nothing blocks (opt in: --gate-mode enforce)\n")
	}

	fmt.Fprintf(w, "\nnext: `seamark index` to build the graph, "+
		"`seamark index --reviews` to mine review lessons\n")

	if printOnly {
		fmt.Fprintf(w, "(nothing was written — --print)\n")
	}

	return nil
}

// loadSettings reads and parses a .claude/settings.json. An absent file
// is an empty map; an unparseable one is a loud error with remediation —
// raised before init writes anything, so a broken file cannot leave the
// repository half-initialized.
func loadSettings(path string) (map[string]any, error) {
	settings := map[string]any{}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, fmt.Errorf("%s: %w (fix or move it, then re-run)", path, err)
		}
	case !os.IsNotExist(err):
		return nil, err
	}

	return settings, nil
}

// resolveGateMode turns the --gate-mode flag into the mode to install.
// An empty flag keeps what a previous init installed (warn on the first
// run): enforcement must never be added — or removed — implicitly.
func resolveGateMode(settings map[string]any, requested string) string {
	if requested != "" {
		return requested
	}

	if mode := installedGateMode(settings); mode != "" {
		return mode
	}

	return gateModeWarn
}

// policyFileMode reads the mode of .seamark/policy.yaml through the real
// loader. An absent file resolves to fallback: the scaffold that init is
// writing (or would write, under --print) carries exactly that mode.
func policyFileMode(root, fallback string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, ".seamark", "policy.yaml")); os.IsNotExist(err) {
		return fallback, nil
	}

	policy, err := gate.LoadPolicy(root)
	if err != nil {
		return "", err
	}

	return policy.Mode, nil
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

// hookSpec is one Claude Code hook seamark installs.
type hookSpec struct {
	event   string
	matcher string
	// marker is the command's argument tail (everything after the binary
	// path). It is matched as a suffix — with the joining space — to
	// recognize an existing seamark hook across path changes, without
	// clobbering an unrelated command that merely contains the same text.
	marker string
	// legacy lists older marker spellings of the same hook: recognized
	// like marker but rewritten to it, so re-running init migrates an
	// existing install instead of adding a duplicate hook beside it.
	legacy  []string
	status  string
	timeout int
}

// gateMarker is the shared marker rule (see internal/hooks).
func gateMarker(gateMode string) string { return hooks.GateMarker(gateMode) }

// hookSpecs returns the hooks init installs for a gate mode. The opposite
// mode's marker is listed as legacy, so switching modes rewrites the
// existing gate hook in place.
func hookSpecs(gateMode string) []hookSpec {
	other := gateModeEnforce
	if gateMode == gateModeEnforce {
		other = gateModeWarn
	}

	return []hookSpec{
		{"PreToolUse", "Bash", gateMarker(gateMode), []string{gateMarker(other)},
			"seamark gate: classifying command", 15},
		{"PreToolUse", "Edit|Write|MultiEdit", hooks.LessonsMarker, nil,
			"seamark: checking review lessons", 10},
		{"PostCompact", "", hooks.LessonsResetMarker, nil,
			"seamark: resetting lesson delivery", 10},
	}
}

// writeHooks persists the already-merged settings and reports what
// happened. All parsing and validation runs earlier in runInit, before
// any file is written — this function only serializes and narrates.
func writeHooks(w io.Writer, path string, settings map[string]any, changed bool,
	bin, gateMode, previous string, printOnly bool) error {
	if !changed {
		fmt.Fprintf(w, "  kept    .claude/settings.json (seamark hooks already wired)\n")
		printHookCommands(w, bin, gateMode)

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

	fmt.Fprintf(w, "  %s .claude/settings.json (gate + lessons + context reset hooks)\n", verb)
	printHookCommands(w, bin, gateMode)

	// The note states only what changed — the hook flag; whether anything
	// still blocks is the effective-mode line's job (a kept enforce
	// policy blocks regardless of the flag).
	if previous == gateModeEnforce && gateMode == gateModeWarn {
		removed := "removed"
		if printOnly {
			removed = "would remove"
		}

		fmt.Fprintf(w, "  note    %s --enforce from the gate hook: the hook follows .seamark/policy.yaml\n"+
			"          instead — re-run with --gate-mode enforce to restore the baked-in flag\n", removed)
	}

	return nil
}

// printHookCommands lists the exact hook command lines: what runs on
// which tool must never require opening settings.json to find out.
func printHookCommands(w io.Writer, bin, gateMode string) {
	for _, spec := range hookSpecs(gateMode) {
		where := spec.event
		if spec.matcher != "" {
			where += " " + spec.matcher
		}

		fmt.Fprintf(w, "          %-30s %s %s\n", where, shellQuote(bin), spec.marker)
	}
}

// installedGateMode is the shared detection rule (see internal/hooks).
func installedGateMode(settings map[string]any) string {
	return hooks.InstalledGateMode(settings)
}

// mergeHooks adds seamark's hooks into an existing settings
// map, preserving every other hook and updating (never duplicating) a
// seamark hook already present. Returns whether anything changed. It
// errors rather than silently overwriting a present-but-wrong-typed
// hooks/PreToolUse field — the same loud handling loadSettings gives
// malformed JSON.
func mergeHooks(settings map[string]any, bin, gateMode string) (changed bool, err error) {
	hookMap, err := childMap(settings, "hooks")
	if err != nil {
		return false, err
	}

	for _, spec := range hookSpecs(gateMode) {
		eventHooks, err := childSlice(hookMap, spec.event)
		if err != nil {
			return false, err
		}

		want := shellQuote(bin) + " " + spec.marker

		found, updated := applyExisting(eventHooks, append([]string{spec.marker}, spec.legacy...), want)
		if found {
			changed = changed || updated
			continue
		}

		entry := map[string]any{
			"hooks": []any{map[string]any{
				"type":          "command",
				"command":       want,
				"timeout":       spec.timeout,
				"statusMessage": spec.status,
			}},
		}
		if spec.matcher != "" {
			entry["matcher"] = spec.matcher
		}

		eventHooks = append(eventHooks, entry)
		hookMap[spec.event] = eventHooks
		changed = true
	}

	return changed, nil
}

// applyExisting rewrites seamark's own hook command to want if present,
// recognized by hooks.OwnedBySeamark, so an unrelated command that
// merely contains or ends with the marker text is left alone. Reports
// whether such a hook existed and whether it changed.
func applyExisting(pre []any, markers []string, want string) (found, updated bool) {
	forEachCommand(pre, func(h map[string]any, cmd string) {
		if !hooks.OwnedBySeamark(cmd, markers) {
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

// forEachCommand is the shared traversal (see internal/hooks); init's
// callers do not need the matcher.
func forEachCommand(pre []any, fn func(h map[string]any, cmd string)) {
	hooks.ForEachCommand(pre, func(_ string, h map[string]any, cmd string) { fn(h, cmd) })
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
