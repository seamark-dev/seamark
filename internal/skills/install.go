package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Install modes for Targets. Auto is what a bare --skills means: Claude
// Code always, Codex only when the repository already has an .agents/
// directory, which is the place Codex and other Agent Skills clients
// read. .codex/ is not consulted.
const (
	ModeAuto   = "auto"
	ModeClaude = "claude"
	ModeCodex  = "codex"
	ModeAll    = "all"
)

// Modes lists the accepted install modes for help and error text.
var Modes = []string{ModeAuto, ModeClaude, ModeCodex, ModeAll}

// Client skill directories, repository-relative with forward slashes.
const (
	ClaudeDir = ".claude/skills"
	AgentsDir = ".agents/skills"
)

// Target is one client skill directory an install addresses.
type Target struct {
	// Client names the client in narration: "claude" or "codex".
	Client string
	// Dir is the repository-relative skill directory, slash-separated.
	Dir string
}

var (
	claudeTarget = Target{Client: ModeClaude, Dir: ClaudeDir}
	codexTarget  = Target{Client: ModeCodex, Dir: AgentsDir}
)

// Targets returns the client directories one install mode addresses.
func Targets(root, mode string) ([]Target, error) {
	switch mode {
	case ModeClaude:
		return []Target{claudeTarget}, nil
	case ModeCodex:
		return []Target{codexTarget}, nil
	case ModeAll:
		return []Target{claudeTarget, codexTarget}, nil
	case ModeAuto:
		targets := []Target{claudeTarget}

		if info, err := os.Stat(filepath.Join(root, ".agents")); err == nil && info.IsDir() {
			targets = append(targets, codexTarget)
		}

		return targets, nil
	default:
		return nil, fmt.Errorf("skills: unknown install mode %q (accepted: %s)",
			mode, strings.Join(Modes, ", "))
	}
}

// State classifies one skill directory on disk.
type State int

// The four states Plan reports. Only Absent and Stale lead to a write.
// Foreign is never touched: the directory is the user's, not seamark's.
const (
	Absent  State = iota // nothing at the path
	Current              // managed and byte-equal to the shipped files
	Stale                // managed, but a shipped file differs or is missing
	Foreign              // present without seamark's marker
)

// String names a state for narration and test output.
func (s State) String() string {
	switch s {
	case Absent:
		return "absent"
	case Current:
		return "current"
	case Stale:
		return "stale"
	case Foreign:
		return "foreign"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Entry is one skill directory in an install plan.
type Entry struct {
	Name string
	// Rel is the repository-relative directory, slash-separated.
	Rel   string
	State State
	// Reason explains a Foreign classification; empty otherwise.
	Reason string
}

// Plan inspects every target skill directory before any write, so a
// directory that cannot be read aborts the install while the tree is
// still untouched. The same rule protects init's settings merge.
func Plan(root string, targets []Target) ([]Entry, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}

	var entries []Entry

	for _, t := range targets {
		for _, name := range names {
			e := Entry{Name: name, Rel: path.Join(t.Dir, name)}

			e.State, e.Reason, err = classify(root, e)
			if err != nil {
				return nil, err
			}

			entries = append(entries, e)
		}
	}

	return entries, nil
}

// classify decides the state of one planned directory. A missing path
// is Absent; a path seamark does not own is Foreign with the reason; an
// owned copy is Current or Stale by byte comparison with every shipped
// file. Every file is compared before Stale is returned, so a read
// error surfaces here, before init writes anything, rather than in the
// middle of the refresh. Read errors other than "not found" are
// returned: masking them as Stale would turn a permission problem into
// a failed write later.
func classify(root string, e Entry) (State, string, error) {
	if link, err := SymlinkIn(root, e.Rel); err != nil {
		return Absent, "", err
	} else if link != "" {
		return Foreign, "symlink at " + link, nil
	}

	dir := filepath.Join(root, filepath.FromSlash(e.Rel))

	info, err := os.Stat(dir)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return Absent, "", nil
	case err != nil:
		return Absent, "", fmt.Errorf("%s: %w", e.Rel, err)
	case !info.IsDir():
		return Foreign, "not a directory", nil
	}

	shipped, err := Files(e.Name)
	if err != nil {
		return Absent, "", err
	}

	// Sorted, so a failure names the same path on repeat.
	rels := slices.Sorted(maps.Keys(shipped))

	// No shipped path may be, or pass through, a link. seamark never
	// writes links, so one means the directory was altered by hand or by
	// a commit, and a refresh would write through it to wherever it
	// points. Checked before SKILL.md is read, so a linked SKILL.md
	// cannot borrow the marker from a file elsewhere.
	for _, rel := range rels {
		link, err := SymlinkIn(root, path.Join(e.Rel, rel))
		if err != nil {
			return Absent, "", err
		}

		if link != "" {
			return Foreign, "symlink at " + strings.TrimPrefix(link, e.Rel+"/"), nil
		}
	}

	skillMD, err := os.ReadFile(filepath.Join(dir, SkillFile))

	switch {
	case errors.Is(err, os.ErrNotExist):
		return Foreign, "no " + SkillFile, nil
	case err != nil:
		return Absent, "", fmt.Errorf("%s: %w", e.Rel, err)
	}

	fm, _, err := ParseFrontmatter(skillMD)
	if err != nil {
		return Foreign, "unreadable frontmatter", nil
	}

	if !IsManaged(fm) {
		return Foreign, "no seamark marker", nil
	}

	stale := false

	for _, rel := range rels {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))

		switch {
		case errors.Is(err, os.ErrNotExist):
			stale = true
		case err != nil:
			return Absent, "", fmt.Errorf("%s: %w", path.Join(e.Rel, rel), err)
		case !bytes.Equal(got, shipped[rel]):
			stale = true
		}
	}

	if stale {
		return Stale, "", nil
	}

	return Current, "", nil
}

// SymlinkIn walks rel down from root one component at a time and
// returns the first component that is a symbolic link, or "" when none
// is. The walk stops at the first missing component, because nothing
// below it exists yet. Every path seamark reads or writes under a client
// directory passes this check, so a link committed in a cloned
// repository can never redirect a refresh outside the tree.
func SymlinkIn(root, rel string) (string, error) {
	parts := strings.Split(rel, "/")

	for i := range parts {
		prefix := path.Join(parts[:i+1]...)

		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prefix)))

		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", nil
		case err != nil:
			return "", fmt.Errorf("%s: %w", prefix, err)
		case info.Mode()&os.ModeSymlink != 0:
			return prefix, nil
		}
	}

	return "", nil
}

// Apply executes a plan with one narrated line per skill directory, in
// init's vocabulary: wrote, updated, kept, and the would- forms under
// preview. An absent directory gets every shipped file; a stale managed
// copy gets the shipped files rewritten and keeps any extra file the
// user added; current and foreign directories are left alone. A write
// failure returns with the path in the error, and a re-run completes
// the set because the plan is recomputed from disk.
func Apply(w io.Writer, root string, entries []Entry, printOnly bool) error {
	for _, e := range entries {
		switch e.State {
		case Current:
			fmt.Fprintf(w, "  kept    %s (current)\n", e.Rel)
		case Foreign:
			fmt.Fprintf(w, "  kept    %s (not managed by seamark: %s)\n", e.Rel, e.Reason)
		case Absent, Stale:
			if !printOnly {
				if err := writeSkill(root, e); err != nil {
					return err
				}
			}

			if e.State == Absent {
				fmt.Fprintf(w, "  %s  %s\n", previewVerb("wrote", "would write", printOnly), e.Rel)
			} else {
				fmt.Fprintf(w, "  %s %s (refreshed managed copy)\n", previewVerb("updated", "would update", printOnly), e.Rel)
			}
		}
	}

	return nil
}

// Install plans and applies in one call for callers that need no gap
// between the two, such as tests and doctor fixtures.
func Install(w io.Writer, root string, targets []Target, printOnly bool) error {
	entries, err := Plan(root, targets)
	if err != nil {
		return err
	}

	return Apply(w, root, entries, printOnly)
}

func previewVerb(verb, preview string, printOnly bool) string {
	if printOnly {
		return preview
	}

	return verb
}

// writeSkill writes every shipped file of one skill in sorted order, so
// a failure always names the same path on repeat. Each path is checked
// for links again right before the write: the plan may be older than
// the tree, and a write must never follow a link out of the repository.
func writeSkill(root string, e Entry) error {
	files, err := Files(e.Name)
	if err != nil {
		return err
	}

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		target := path.Join(e.Rel, rel)

		link, err := SymlinkIn(root, target)
		if err != nil {
			return err
		}

		if link != "" {
			return fmt.Errorf("%s: symlink at %s; seamark writes only real paths inside the repository", target, link)
		}

		dst := filepath.Join(root, filepath.FromSlash(target))

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}

		if err := os.WriteFile(dst, files[rel], 0o644); err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
	}

	return nil
}

// ClientState summarizes one client's skill directory for init, doctor,
// and status. Counts are over the shipped skills; Foreign counts
// directories that carry a shipped skill's name but not seamark's marker.
type ClientState struct {
	Client  string `json:"client"`
	Dir     string `json:"dir"`
	Current int    `json:"current"`
	Stale   int    `json:"stale"`
	Missing int    `json:"missing"`
	Foreign int    `json:"foreign"`
	Err     string `json:"error,omitempty"`
}

// Installed reports whether the client holds any managed skill.
func (c ClientState) Installed() bool {
	return c.Current+c.Stale > 0
}

// Inspect reports both clients regardless of detection, so a stale copy
// in a directory auto would skip stays visible. A read error is recorded
// on the client instead of failing the call: status must never fail
// because one directory is unreadable.
func Inspect(root string) []ClientState {
	var states []ClientState

	for _, t := range []Target{claudeTarget, codexTarget} {
		s := ClientState{Client: t.Client, Dir: t.Dir}

		entries, err := Plan(root, []Target{t})
		if err != nil {
			s.Err = err.Error()
			states = append(states, s)

			continue
		}

		for _, e := range entries {
			switch e.State {
			case Current:
				s.Current++
			case Stale:
				s.Stale++
			case Absent:
				s.Missing++
			case Foreign:
				s.Foreign++
			}
		}

		states = append(states, s)
	}

	return states
}

// NeedsRefresh reports whether a managed copy is stale or missing, the
// two states `seamark init --skills` repairs.
func (c ClientState) NeedsRefresh() bool {
	return c.Installed() && (c.Stale > 0 || c.Missing > 0)
}

// Describe renders one client in a few words, for example
// "claude 3/3 current, 1 stale" or "codex not installed". init, status,
// and doctor all print it, so the three never phrase a state differently.
func (c ClientState) Describe() string {
	switch {
	case c.Err != "":
		return fmt.Sprintf("%s unreadable (%s)", c.Client, c.Err)
	case !c.Installed():
		if c.Foreign > 0 {
			return fmt.Sprintf("%s not installed, %d not managed", c.Client, c.Foreign)
		}

		return c.Client + " not installed"
	}

	total := c.Current + c.Stale + c.Missing + c.Foreign
	p := fmt.Sprintf("%s %d/%d current", c.Client, c.Current, total)

	if c.Stale > 0 {
		p += fmt.Sprintf(", %d stale", c.Stale)
	}

	if c.Missing > 0 {
		p += fmt.Sprintf(", %d missing", c.Missing)
	}

	if c.Foreign > 0 {
		p += fmt.Sprintf(", %d not managed", c.Foreign)
	}

	return p
}

// Summary renders the one-line view init and status print, for example
// "claude 3/3 current · codex not installed". A stale or missing managed
// copy names the corrective command once at the end.
func Summary(states []ClientState) string {
	var (
		parts   []string
		refresh bool
	)

	for _, s := range states {
		parts = append(parts, s.Describe())
		refresh = refresh || s.NeedsRefresh()
	}

	line := strings.Join(parts, " · ")

	if refresh {
		line += " (re-run seamark init --skills)"
	}

	return line
}
