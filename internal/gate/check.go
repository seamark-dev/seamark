package gate

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/store"
)

// ErrBlocked marks decisions that must stop the caller (enforce mode);
// the CLI maps it to a distinct exit code for hooks and CI.
var ErrBlocked = errors.New("blocked by policy")

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// EvalDiff computes a unified diff's blast radius and evaluates policy
// over it: changed lines map to symbols, and symbols already carry the
// transitively-propagated effect tags — their union is what this change
// can ultimately reach (RFC-001 §5.5, pre-edit enforcement).
func EvalDiff(p *Policy, st *store.Store, diffText string) (*Decision, error) {
	touched := map[string][][2]uint32{} // file -> [start, end] line ranges

	file := ""
	oldFile := ""

	// register marks a path as touched even before (or without) any
	// hunk: binary, mode-only, rename-only, and empty-file changes have
	// no `@@` hunks, yet all alter behaviour-relevant state and must
	// enter diff.files and the uncertainty accounting.
	register := func(p string) {
		if p == "" {
			return
		}

		if _, ok := touched[p]; !ok {
			touched[p] = nil
		}
	}

	for line := range strings.SplitSeq(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, oldFile = "", "" // new record

			if p := gitHeaderPath(line); p != "" {
				file = p
				register(p)
			}
		case strings.HasPrefix(line, "rename to "):
			// More reliable than the `diff --git` split for spaced
			// paths; replaces the record's registration when they differ.
			if p := diffHeaderPath(strings.TrimPrefix(line, "rename to "), ""); p != "" {
				file = p
				register(p)
			}
		case strings.HasPrefix(line, "--- "):
			oldFile = diffHeaderPath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			file = diffHeaderPath(line[4:], "b/")

			// A whole-file deletion has `+++ /dev/null`: the OLD path is
			// what vanished, and it must enter diff.files — deleting an
			// effectful file must never look like an empty diff.
			if file == "" && oldFile != "" {
				file = oldFile
			}

			register(file)
		default:
			m := hunkRe.FindStringSubmatch(line)
			if m == nil || file == "" {
				continue
			}

			start, _ := strconv.Atoi(m[1])
			count := 1

			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}

			if count < 1 {
				count = 1 // pure deletions still touch the surrounding area
			}

			touched[file] = append(touched[file],
				[2]uint32{uint32(start), uint32(start + count - 1)})
		}
	}

	files := make([]string, 0, len(touched))
	tagSet := map[string]bool{}

	var unindexed []string

	for f, ranges := range touched {
		files = append(files, f)

		syms, err := st.SymbolsInFile(f)
		if err != nil {
			return nil, err
		}

		covered := false

		for i := range syms {
			sym := &syms[i]

			overlaps := false

			for _, r := range ranges {
				if sym.Span.StartLine <= r[1] && sym.Span.EndLine >= r[0] {
					overlaps = true
					break
				}
			}

			if !overlaps {
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
// first-appearance order — the same header parsing EvalDiff trusts, so
// an advisory surface (lessons on `check`) can never disagree with the
// verdict about which files changed.
func ChangedPaths(diffText string) []string {
	seen := map[string]bool{}

	var out []string

	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	oldFile := ""

	for line := range strings.SplitSeq(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			oldFile = ""
			add(gitHeaderPath(line))
		case strings.HasPrefix(line, "rename to "):
			add(diffHeaderPath(strings.TrimPrefix(line, "rename to "), ""))
		case strings.HasPrefix(line, "--- "):
			oldFile = diffHeaderPath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			if p := diffHeaderPath(line[4:], "b/"); p != "" {
				add(p)
			} else {
				add(oldFile) // whole-file deletion: the old path vanished
			}
		}
	}

	return out
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
