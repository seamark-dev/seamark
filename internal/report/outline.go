package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// whyOutlineLimit is the number of symbols that the file outline of why
// shows. A larger file gets a count and one route to the next symbol, so
// the report stays short for a reader that pays per line.
const whyOutlineLimit = 20

// outlineSigCap is the largest number of runes in one outline signature.
// A Go var or const keeps the first line of its spec at any length, so the
// outline sets its own bound. 100 runes keep a typical Go method header.
const outlineSigCap = 100

// printOutline writes the defines section of why <file>. Each entry keeps
// the full FQN and an inclusive file:start-end ref, because two symbols
// can share a short name or even a full FQN (two init functions). The
// caller lines follow the signature of each entry.
func printOutline(w io.Writer, st *store.Store, root, file string, syms []model.Symbol) error {
	if len(syms) == 0 {
		// Silence reads as "the file defines nothing". The index only
		// knows the file through history, so the line says that.
		fmt.Fprintf(w, "\ndefines (0)  no indexed symbols\n")

		return nil
	}

	ordered := sourceOrder(syms)

	callers, err := loadOutlineCallers(st, file, ordered[:min(len(ordered), whyOutlineLimit)])
	if err != nil {
		return err
	}

	if len(ordered) > whyOutlineLimit {
		fmt.Fprintf(w, "\ndefines (%d; showing %d)\n", len(ordered), whyOutlineLimit)
	} else {
		fmt.Fprintf(w, "\ndefines (%d)\n", len(ordered))
	}

	path := scrubText(file)

	for i, s := range ordered {
		if i == whyOutlineLimit {
			break
		}

		fmt.Fprintf(w, "  %-8s %-44s %s\n", s.Kind, scrubText(s.FQN), spanRef(path, s.Span))

		// The second line sits under the FQN column. An empty signature
		// prints no line, so an empty text never stands for a signature.
		if sig := render.Truncate(scrubSource(s.Sig), outlineSigCap); sig != "" {
			fmt.Fprintf(w, "           %s\n", sig)
		}

		if err := callers.printEntry(w, st, root, s); err != nil {
			return err
		}
	}

	if len(ordered) > whyOutlineLimit {
		printOmitted(w, root, file, ordered[whyOutlineLimit], len(ordered)-whyOutlineLimit)
	}

	callers.printClosing(w)

	return nil
}

// printOmitted writes the line for the symbols that the outline leaves
// out. It names only the first of them, because one ref cannot hold the
// others. The expand route prints only when expand can run it.
func printOmitted(w io.Writer, root, file string, next model.Symbol, more int) {
	fqn := scrubText(next.FQN)

	if ref, ok := sourceRoute(root, file, next.Span); ok {
		// The CLI has no expand command, so the route names MCP, as the
		// raw-findings pointer of why and the orient footer do.
		fmt.Fprintf(w, "  … %d more not shown; source of the next one (%s): expand %s (MCP)\n", more, fqn, ref)

		return
	}

	fmt.Fprintf(w, "  … %d more not shown; the next one is %s at %s (no expand route: this ref does not resolve)\n",
		more, fqn, spanRef(scrubText(file), next.Span))
}

// sourceOrder returns a sorted copy of syms. The store orders by start
// line only, and two names of one Go spec share one span. So the sort
// adds the start column and the ID, and the order is stable across runs.
func sourceOrder(syms []model.Symbol) []model.Symbol {
	ordered := append([]model.Symbol(nil), syms...)

	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]

		if a.Span.StartLine != b.Span.StartLine {
			return a.Span.StartLine < b.Span.StartLine
		}

		if a.Span.StartCol != b.Span.StartCol {
			return a.Span.StartCol < b.Span.StartCol
		}

		return a.ID < b.ID
	})

	return ordered
}

// inclusiveLines converts a half-open span to the inclusive line range
// that a file:start-end ref names. A span that ends at column 0 stops
// before its end line, so that line is not part of the symbol. The end
// never goes below the start, because a ref with end < start does not parse.
func inclusiveLines(sp model.Span) (start, end uint32) {
	start, end = sp.StartLine, sp.EndLine

	if sp.EndCol == 0 && end > start {
		end--
	}

	if end < start {
		end = start
	}

	return start, end
}

// spanRef formats the inclusive ref of a span in path. It always holds
// both line numbers, so a one-line symbol reads the same as a long one.
func spanRef(path string, sp model.Span) string {
	start, end := inclusiveLines(sp)

	return fmt.Sprintf("%s:%d-%d", path, start, end)
}

// sourceRoute returns the ref of span in file when expand can show it.
// It runs the functions that expand runs, parseSpanRef and a writeRegion
// that discards its output, so the check and the route cannot disagree.
// A path that the scrub changes is refused, because its printed ref names
// a different file.
func sourceRoute(root, file string, sp model.Span) (string, bool) {
	if scrubText(file) != file {
		return "", false
	}

	start, end := inclusiveLines(sp)
	ref := spanRef(file, sp)

	gotFile, gotStart, gotEnd, ok := parseSpanRef(ref)
	if !ok || gotFile != file || gotStart != start || gotEnd != end {
		return "", false
	}

	if err := writeRegion(io.Discard, root, file, start, end); err != nil {
		return "", false
	}

	return ref, true
}

// scrubText prepares one source-derived name for the terminal. Redaction
// runs first, as in the other report lines, so it sees the text before
// Sanitize removes any rune.
func scrubText(s string) string {
	return render.Sanitize(redact.Secrets(s))
}

// scrubSource prepares a signature for one report line. Redaction sees the
// whole text, so a later cut cannot leave a partial secret that no pattern
// matches. Sanitize keeps tabs, so the whitespace fold comes after it.
func scrubSource(s string) string {
	return strings.Join(strings.Fields(scrubText(s)), " ")
}
