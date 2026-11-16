package gate

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
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

// EvalDiff computes the blast radius of a unified diff and evaluates
// policy over it. Changed lines map to symbols. Each symbol already
// carries the effect tags that it reaches through its callees. The union
// of these tags is what the changed code reaches through its own calls.
// It leaves out the code that reads a changed value, because the index
// records no edge from a reader to a value.
//
// The gate decides the unknown-reach note per change group. A file gets
// the note when one of its change groups has no recorded line in an
// indexed symbol. So one edit inside a symbol cannot hide a deleted
// function or an import edit in the same file.
//
// A second note names the changed value and type symbols that carry no
// effects, because a clean union says nothing about their readers.
// valueSymbols decides which symbols the note names.
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

	// values holds the FQN of each value or type symbol for the value
	// note, by symbol ID, so a symbol counts once.
	values := map[int64]string{}

	var unindexed []string

	for _, f := range parsed.paths {
		files = append(files, f)
		changed := parsed.lines[f]

		syms, err := st.SymbolsInFile(f)
		if err != nil {
			return nil, err
		}

		// effectful tells for each touched symbol whether it carries
		// effects. The value note leaves out a symbol with effects, because
		// diff.effects already shows them.
		effectful := map[int64]bool{}

		for i := range syms {
			sym := &syms[i]

			// A symbol is touched when a changed line lies in its span.
			if !slices.ContainsFunc(changed, func(n uint32) bool { return inSpan(sym, n) }) {
				continue
			}

			effs, err := st.EffectsForSymbol(sym.ID)
			if err != nil {
				return nil, err
			}

			for _, e := range effs {
				tagSet[e.Tag] = true
			}

			effectful[sym.ID] = len(effs) > 0
		}

		for _, g := range parsed.groups[f] {
			for _, sym := range valueSymbols(g, syms, effectful) {
				values[sym.ID] = sym.FQN
			}
		}

		// A file with no change group, or with a group that counts for no
		// symbol, has a change that the index cannot attribute. Examples
		// are an unparsed or deleted file, a deleted function, and an
		// import edit, which can change resolution and reach. The reach of
		// such a change is unknown, and missing evidence never reads as safe.
		groups := parsed.groups[f]
		if len(groups) == 0 || slices.ContainsFunc(groups, func(g changeGroup) bool { return !attributed(g.lines, syms) }) {
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
			"files":                 files,
			"effects":               tags,
			"unindexed_files":       len(unindexed),
			"unknown_reach_symbols": len(values),
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

	if len(values) > 0 {
		d.Notes = append(d.Notes, valueNote(slices.Collect(maps.Values(values))))
	}

	return d, nil
}

// maxNamedValues limits the symbols that the value note names. A change
// to a generated file can touch hundreds of types, and a long note hides
// the verdict.
const maxNamedValues = 10

// valueNote says that seamark does not know who reads the changed value
// and type symbols fqns. It names at most maxNamedValues of them in
// sorted order and counts the rest.
func valueNote(fqns []string) string {
	sort.Strings(fqns)

	named := strings.Join(fqns[:min(len(fqns), maxNamedValues)], ", ")
	if extra := len(fqns) - maxNamedValues; extra > 0 {
		named += fmt.Sprintf(", and %d more", extra)
	}

	if len(fqns) == 1 {
		return "1 changed value or type has no effects of its own (" + named +
			") — seamark does not track readers of a value, so its effect reach is unknown, not clean"
	}

	return fmt.Sprintf("%d changed values or types have no effects of their own (%s) — "+
		"seamark does not track readers of a value, so their effect reach is unknown, not clean", len(fqns), named)
}

// valueSymbols returns the symbols of syms that the value note names for
// one change group. A symbol is named when it holds a changed place of
// the group, is a value or a type, and carries no effects (effectful is
// false). Its readers can reach effects, but no edge records a reader of
// a value.
//
// A changed place is an added line or a cut. The line above a cut counts
// for the effects of the group, but it names no symbol by itself. That
// line often belongs to an unchanged neighbour, for example the const
// above an edited const.
//
// A place in a function or a method names nothing, because the effects of
// that code show. A class is not named when a recorded line of the same
// group lies in one of its methods, because the group then reports the
// effects of that method. Examples are an added method with an added
// blank line before it, and a comment edit directly below a method.
func valueSymbols(g changeGroup, syms []model.Symbol, effectful map[int64]bool) []*model.Symbol {
	edited := holders(syms, func(s *model.Symbol) bool {
		return isCode(s.Kind) && slices.ContainsFunc(g.lines, func(n uint32) bool { return inSpan(s, n) })
	})

	places := make([][]*model.Symbol, 0, len(g.added)+len(g.cuts))

	for _, n := range g.added {
		places = append(places, holders(syms, func(s *model.Symbol) bool { return inSpan(s, n) }))
	}

	for _, c := range g.cuts {
		places = append(places, holders(syms, func(s *model.Symbol) bool { return holdsCut(s, c) }))
	}

	var out []*model.Symbol

	for _, held := range places {
		if slices.ContainsFunc(held, func(s *model.Symbol) bool { return isCode(s.Kind) }) {
			continue
		}

		for _, s := range held {
			if effectful[s.ID] || slices.ContainsFunc(edited, func(m *model.Symbol) bool { return encloses(s, m) }) {
				continue
			}

			out = append(out, s)
		}
	}

	return out
}

// holders returns the symbols of syms for which holds is true.
func holders(syms []model.Symbol, holds func(s *model.Symbol) bool) []*model.Symbol {
	var out []*model.Symbol

	for i := range syms {
		if holds(&syms[i]) {
			out = append(out, &syms[i])
		}
	}

	return out
}

// holdsCut reports whether the removed lines at c lay inside sym. The new
// side has no line for them, so their neighbours decide. Lines on both
// sides of the cut lie in sym when sym has lines before and after the
// cut, for example a struct or a map literal with a closing line.
//
// A Python class has no closing line, so its span ends at its last
// statement. A removed last attribute then has a line below that lies
// outside the class. The cut still lies inside sym when it is indented
// deeper than the start of sym and the line with text above it lies in
// sym. The indent test keeps a removed Go const of a block from naming the
// const above it, which starts at the same column.
func holdsCut(sym *model.Symbol, c cutSite) bool {
	if c.above > 0 && inSpan(sym, c.above) && inSpan(sym, c.below) {
		return true
	}

	return c.indent > sym.Span.StartCol && inSpan(sym, c.textAbove)
}

// isCode reports whether a symbol of kind k is a function or a method:
// code whose own calls give its effects. Every other kind in a file is
// a value or a type, whose readers the index does not record.
func isCode(k model.SymbolKind) bool {
	return k == model.KindFunction || k == model.KindMethod
}

// encloses reports whether the span of outer holds the span of inner.
func encloses(outer, inner *model.Symbol) bool {
	return outer.Span.StartLine <= inner.Span.StartLine && inner.Span.EndLine <= outer.Span.EndLine
}

// attributed reports whether a change group has a recorded line in the
// span of one of syms. A group with no line is not attributed, because
// an empty set matches no symbol.
func attributed(group []uint32, syms []model.Symbol) bool {
	for i := range syms {
		if slices.ContainsFunc(group, func(n uint32) bool { return inSpan(&syms[i], n) }) {
			return true
		}
	}

	return false
}

// inSpan reports whether new-side line n lies in the span of sym.
func inSpan(sym *model.Symbol, n uint32) bool {
	return sym.Span.StartLine <= n && n <= sym.Span.EndLine
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
