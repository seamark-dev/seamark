package lessoncheck

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds the text form of a verification result. The CLI and
// the MCP server print the same text, so one renderer exists. The text
// layout is not a contract (spec L213); the JSON form is.

// shortHex is the length of an object id or a version in the text.
const shortHex = 12

// RenderText writes the text report of a verification result: the
// state, the snapshot, the configuration, one block per result, the
// listed pins, and the rerun command. Every path and every text that
// comes from the repository passes render.Sanitize, because the report
// goes to a terminal.
func RenderText(w io.Writer, v *Verification) {
	s := v.Summary

	fmt.Fprintf(w, "lesson checks: %s\n", s.State)
	fmt.Fprintf(w, "  pins evaluated: %d (pass %d, violation %d, not-applicable %d, unknown %d)\n",
		s.PinsEvaluated, s.Pass, s.Violation, s.NotApplicable, s.Unknown)
	fmt.Fprintf(w, "  snapshot: %s against %s, %d %s, %s\n",
		v.Boundary, baselineText(v.Baseline), s.Changes, plural(s.Changes, "change", "changes"), v.Freshness)
	if v.Freshness == FreshnessStale {
		fmt.Fprintln(w, "  stale: the inputs changed after the read, or a second read did not finish; only a violation stays")
	}

	fmt.Fprintf(w, "  selection: %s\n", render.Sanitize(v.Selection.Description))

	if len(v.Selection.Paths) > 0 {
		fmt.Fprintf(w, "  paths: %s\n", sanitizeJoin(v.Selection.Paths, ", "))
	}

	fmt.Fprintf(w, "  not covered: %s\n", sanitizeJoin(v.Selection.Excludes, "; "))

	if !v.PathsComplete {
		fmt.Fprintf(w, "  path set: incomplete%s\n", unreadableText(v.UnreadableDirectories, v.UnreadableDirectoriesOmitted))
	}

	for _, hit := range v.Limits {
		fmt.Fprintf(w, "  limit reached: %s %d (%d %s left undecided)\n",
			hit.Name, hit.Value, hit.Paths, plural(hit.Paths, "path", "paths"))
	}

	if len(v.Limitations) > 0 {
		fmt.Fprintf(w, "  limitations: %s\n", strings.Join(v.Limitations, ", "))
	}

	if v.statusStderr != "" {
		// The line is already redacted, sanitized, and bounded.
		fmt.Fprintf(w, "  git stderr: %s\n", v.statusStderr)
	}

	renderConfig(w, v.Config)

	if len(v.Results) > 0 {
		fmt.Fprintln(w, "results:")

		for _, r := range v.Results {
			renderResult(w, r, v.firstRun)
		}
	}

	if len(v.Listings) > 0 {
		fmt.Fprintln(w, "not evaluated:")

		for _, l := range v.Listings {
			renderListing(w, l)
		}
	}

	if v.Rerun != "" {
		fmt.Fprintf(w, "rerun: %s\n", render.Sanitize(v.Rerun))
	}
}

// unreadableText returns the census evidence of an incomplete path set:
// the first unreadable directory and the count of the others. omitted
// counts the directories that the output cap left out of dirs, so the
// count covers every directory. It returns "" when the census read every
// directory. The first directory is enough for the reader to act, and a
// count keeps the line short.
func unreadableText(dirs []string, omitted int) string {
	switch more := len(dirs) - 1 + omitted; {
	case len(dirs) == 0:
		return ""
	case more == 0:
		return fmt.Sprintf(" (unreadable directory: %s)", render.Sanitize(dirs[0]))
	default:
		return fmt.Sprintf(" (unreadable directory: %s, and %d more)", render.Sanitize(dirs[0]), more)
	}
}

// baselineText names the before state of a snapshot.
func baselineText(b Baseline) string {
	switch b.Kind {
	case BaselineHead:
		return "HEAD " + shortID(b.Commit)
	case BaselineEmptyTree:
		return "the empty tree"
	default:
		return string(b.Kind)
	}
}

// renderConfig writes the configuration state. A valid file without
// checks writes one short line, so the common case stays short.
func renderConfig(w io.Writer, c ConfigReport) {
	if c.Valid {
		fmt.Fprintf(w, "config: valid (lesson_checks_version %d)\n", c.LessonChecksVersion)

		return
	}

	fmt.Fprintln(w, "config: invalid; no check ran")

	for _, e := range c.Errors {
		fmt.Fprintf(w, "  - %s\n", render.Sanitize(e.Error()))
	}
}

// renderResult writes one result: the outcome, the pin, the check, the
// scope, the findings, and the undecided paths.
func renderResult(w io.Writer, r Result, firstRun map[int]Outcome) {
	fmt.Fprintf(w, "  %s  pin %s %s (entry %d, mode %s)\n", outcomeText(r), r.PinRef, r.Rule, r.Entry, r.Mode)
	fmt.Fprintf(w, "    check: %s\n", checkText(r.Check))
	fmt.Fprintf(w, "    scope: %s\n", scopeText(r.Scope))

	if first, ok := firstRunText(r, firstRun); ok {
		fmt.Fprintf(w, "    first run: %s (not confirmed at publication)\n", first)
	}

	for _, e := range r.Evidence {
		fmt.Fprintf(w, "    %s\n", evidenceText(e))
	}

	if r.EvidenceOmitted > 0 {
		fmt.Fprintf(w, "    and %d more\n", r.EvidenceOmitted)
	}

	for _, e := range r.UnknownPaths {
		fmt.Fprintf(w, "    undecided: %s (%s)\n", evidenceText(e), e.Reason)
	}
}

// renderListing writes one pin that the verification did not evaluate.
// An advisory pin with a check names the command that arms the check,
// because the check never runs in advisory mode (§5 row 20).
func renderListing(w io.Writer, l PinListing) {
	fmt.Fprintf(w, "  %s  pin %s %s (entry %d)\n", l.Listing, l.PinRef, l.Rule, l.Entry)

	if l.Check != nil {
		fmt.Fprintf(w, "    check: %s\n", checkText(*l.Check))
		fmt.Fprintf(w, "    to run it: seamark lessons --set-mode check --pin %s\n", l.PinRef)
	}
}

// firstRunText returns the outcome that a stale result had at the read,
// when the result keeps it. A first-run unknown names its reason and its
// limit, as outcomeText does (fix F-C). A first-run pass or
// not-applicable comes from the text-only map of Verify (contract K5).
func firstRunText(r Result, firstRun map[int]Outcome) (string, bool) {
	if r.FirstRun != nil {
		return outcomeText(Result{Outcome: OutcomeUnknown, Reason: r.FirstRun.Reason, Limit: r.FirstRun.Limit}), true
	}

	first, ok := firstRun[r.Entry]

	return string(first), ok
}

// outcomeText returns the outcome, with the reason and the limit of an
// unknown outcome: "unknown/resource-limit (max-file-bytes)".
func outcomeText(r Result) string {
	if r.Outcome != OutcomeUnknown {
		return string(r.Outcome)
	}

	text := string(r.Outcome) + "/" + string(r.Reason)
	if r.Limit != "" {
		text += " (" + r.Limit + ")"
	}

	return text
}

// checkText names a check: its ID, its kind, and the short version.
// A configuration error can leave the kind and the version empty.
func checkText(c CheckRef) string {
	text := render.Sanitize(c.ID)
	if c.Kind != "" {
		text += " " + render.Sanitize(string(c.Kind))
	}

	if c.Version != "" {
		text += " " + shortID(c.Version)
	}

	return text
}

// scopeText returns the scope of a pin, or "repository-wide" for an
// empty scope.
func scopeText(scope []string) string {
	if len(scope) == 0 {
		return "repository-wide"
	}

	return sanitizeJoin(scope, ", ")
}

// evidenceText returns one finding: the change, the path, and for a
// line match the line and the excerpt. The excerpt is already redacted,
// sanitized, and bounded (contract K3).
func evidenceText(e Evidence) string {
	path := render.Sanitize(e.Path)
	if e.OldPath != "" {
		path = render.Sanitize(e.OldPath) + " -> " + path
	}

	if e.Line > 0 {
		return fmt.Sprintf("%s %s:%d: %s", e.Change, path, e.Line, e.Excerpt)
	}

	return fmt.Sprintf("%s %s", e.Change, path)
}

// shortID returns the first shortHex characters of an id.
func shortID(id string) string {
	if len(id) > shortHex {
		return id[:shortHex]
	}

	return id
}

// sanitizeJoin sanitizes each value and joins them.
func sanitizeJoin(values []string, sep string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = render.Sanitize(v)
	}

	return strings.Join(out, sep)
}

// plural returns one when n is 1, else many.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}
