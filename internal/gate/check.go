package gate

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/store"
)

// ErrBlocked marks decisions that must stop the caller (enforce mode);
// the CLI maps it to a distinct exit code for hooks and CI.
var ErrBlocked = errors.New("blocked by policy")

// The diff sources name where the diff text of EvalDiff comes from. The
// texts for a diff with no changed file name the source, so the CLI and
// MCP give the same words for the same case.
const (
	// SourceStdin is a diff that the CLI reads from a stdin pipe.
	SourceStdin = "stdin"
	// SourceDiffArgument is the diff argument of the MCP check tool.
	SourceDiffArgument = "the diff argument"
	// SourceGitDiffHead is the output of `git diff HEAD`, which both
	// callers use when they get no diff.
	SourceGitDiffHead = "git diff HEAD"
)

// noFileHeader says why input with bytes in it gives no changed file.
const noFileHeader = "names no changed file (no diff --git, ---, +++ or rename header with a path)"

// EvalDiff computes a unified diff's blast radius and evaluates policy
// over it: changed lines map to symbols, and symbols already carry the
// transitively-propagated effect tags — their union is what this change
// can ultimately reach (RFC-001 §5.5, pre-edit enforcement).
//
// source names where diffText comes from, one of the Source constants.
// A diff with no changed file never reads as a clean allow. Empty text
// gets a note. Text with bytes but no file gets a note, and under
// enforcement it blocks, because the gate cannot read it as a diff.
func EvalDiff(p *Policy, st *store.Store, diffText, source string) (*Decision, error) {
	parsed := parseDiff(diffText)

	// Input with bytes in it but no file is not a diff that check can
	// read. Under enforcement a failure of the gate must not fail open,
	// so the gate blocks before any rule runs.
	unreadable := len(parsed.paths) == 0 && diffText != ""
	if unreadable && p.Mode == "enforce" {
		return nil, FailClosed(fmt.Errorf("%s %s; enforce mode blocks input that it cannot read as a diff",
			source, noFileHeader), true)
	}

	files := make([]string, 0, len(parsed.paths))
	tagSet := map[string]bool{}

	var unindexed []string

	for _, f := range parsed.paths {
		files = append(files, f)
		changed := parsed.lines[f]

		syms, err := st.SymbolsInFile(f)
		if err != nil {
			return nil, err
		}

		covered := false

		for i := range syms {
			sym := &syms[i]

			// A symbol is touched when a changed line lies in its span.
			touches := slices.ContainsFunc(changed, func(n uint32) bool {
				return sym.Span.StartLine <= n && n <= sym.Span.EndLine
			})
			if !touches {
				continue
			}

			covered = true

			effs, err := st.EffectsForSymbol(sym.ID)
			if err != nil {
				return nil, err
			}

			for _, e := range effs {
				tagSet[e.Tag] = true
			}
		}

		// No changed line landed inside any indexed symbol — an unparsed
		// file, a deleted one, or edits between symbols (imports!) that
		// can still change resolution and reach. Either way the index
		// cannot attribute this change: its effect reach is UNKNOWN, and
		// absence of evidence must never quietly read as evidence of
		// safety (RFC-001 §5.2).
		if !covered {
			unindexed = append(unindexed, f)
		}
	}

	// The diff gives the files in its own order. A rule that indexes
	// diff.files must see the same list for the same set of files.
	sort.Strings(files)

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}

	activation := map[string]any{
		"effect":  []string{},
		"command": zeroCommand(),
		"env":     p.DetectEnv(os.Environ()),
		"diff": map[string]any{
			"files":           files,
			"effects":         tags,
			"unindexed_files": len(unindexed),
		},
	}

	d, err := p.evaluate(tags, activation)
	if err != nil {
		return nil, err
	}

	// The texts quote no input byte, so they need no sanitizing.
	switch {
	case unreadable:
		d.Notes = append(d.Notes, fmt.Sprintf("%s %s; this verdict covers no change", source, noFileHeader))
	case diffText == "" && source == SourceGitDiffHead:
		// A clean tree and a tree with only untracked files both give
		// empty output. The note says so, because untracked files can
		// hold the change.
		d.Notes = append(d.Notes, "no changed files in git diff HEAD; untracked files are not part of it")
	case diffText == "":
		d.Notes = append(d.Notes, "no changed files in "+source)
	}

	if len(unindexed) > 0 {
		sort.Strings(unindexed)
		d.Notes = append(d.Notes, fmt.Sprintf(
			"%d of %d changed files have changes outside any indexed symbol (%s) — their effect reach is unknown, not clean",
			len(unindexed), len(files), strings.Join(unindexed, ", ")))
	}

	return d, nil
}

// gitHeaderPath extracts the new-side path from a `diff --git a/X b/Y`
// record line. Quoted forms unwrap; unquoted forms split on the LAST
// " b/", which is only ambiguous for paths containing " b/" themselves —
// and any later rename/---/+++ header then corrects the record. This
// line is the only path source for hunkless records (binary, mode-only,
// empty-file changes).
func gitHeaderPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")

	if i := strings.LastIndex(rest, ` "b/`); i >= 0 {
		if unquoted, err := strconv.Unquote(rest[i+1:]); err == nil {
			return strings.TrimPrefix(unquoted, "b/")
		}
	}

	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}

	return ""
}

// ChangedPaths lists the repo-relative files a unified diff touches, in
// first-appearance order. It uses the parser of EvalDiff, so an advisory
// surface (lessons on `check`) can never disagree with the verdict about
// which files changed.
func ChangedPaths(diffText string) []string {
	return parseDiff(diffText).paths
}

// diffHeaderPath extracts the repo-relative path from a "---"/"+++"
// header value (prefix "a/" or "b/" respectively). Git appends a TAB to
// paths containing spaces and C-quotes paths with special characters —
// both must unwrap or the file is silently skipped and its blast radius
// missed.
func diffHeaderPath(v, prefix string) string {
	v = strings.TrimSuffix(v, "\t")

	if strings.HasPrefix(v, `"`) {
		if unquoted, err := strconv.Unquote(v); err == nil {
			v = unquoted
		}
	}

	if v == "/dev/null" {
		return ""
	}

	return strings.TrimPrefix(v, prefix)
}

// zeroCommand carries every key command rules may access: CEL map access
// to a missing key is an evaluation error, not false.
func zeroCommand() map[string]any {
	return map[string]any{
		"name":          "",
		"argv":          []string{},
		"raw":           "",
		"is_push":       false,
		"is_force_push": false,
		"target_branch": "",
		"dynamic":       false,
	}
}
