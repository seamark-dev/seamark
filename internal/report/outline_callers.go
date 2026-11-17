package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// outlineCallerSample is the largest number of caller definitions that
// one outline entry shows. Three often name a second caller file and
// keep the entry short.
const outlineCallerSample = 3

// outlineCallerBudget is the largest number of caller definitions in one
// outline. The entries spend it in source order, so the output does not
// grow past 20 entries of 3 each and the cut is predictable.
const outlineCallerBudget = 30

// outlineCallers holds the caller data of the shown outline entries and
// the state that the closing lines need.
type outlineCallers struct {
	stats  map[int64]store.CallerStat
	sample map[int64][]store.CallEdge

	// left is the part of the outline budget that is not spent.
	left int

	// unsampled counts the entries with callers that got no sample line,
	// because the budget was spent.
	unsampled int

	// noneIndexed is true when an entry printed "none indexed".
	noneIndexed bool
}

// loadOutlineCallers reads the caller counts of the file and one sample
// for the shown entries. It runs two queries in total, so the work does
// not grow with the number of entries or with the fan-in of one symbol.
func loadOutlineCallers(st *store.Store, file string, shown []model.Symbol) (*outlineCallers, error) {
	stats, err := st.CallerStats(file)
	if err != nil {
		return nil, err
	}

	var ids []int64

	for _, s := range shown {
		if stats[s.ID].Total > 0 {
			ids = append(ids, s.ID)
		}
	}

	sample, err := st.CallerSample(file, ids, outlineCallerSample)
	if err != nil {
		return nil, err
	}

	return &outlineCallers{stats: stats, sample: sample, left: outlineCallerBudget}, nil
}

// printEntry writes the caller lines of one entry: the count line, then
// the caller definitions while the budget lasts.
func (o *outlineCallers) printEntry(w io.Writer, st *store.Store, root string, sym model.Symbol) error {
	stat := o.stats[sym.ID]

	if stat.Total == 0 {
		// Only functions and methods get CALLS edges in, as in why
		// <symbol>. A missing line under a function would read as "not
		// called", so the line says that the index has no edge.
		if sym.Kind == model.KindFunction || sym.Kind == model.KindMethod {
			fmt.Fprintf(w, "           callers  none indexed\n")

			o.noneIndexed = true
		}

		return nil
	}

	line := fmt.Sprintf("           callers  %d · %d in other non-test files · %d in test files",
		stat.Total, stat.OtherFiles, stat.Tests)

	if stat.NameMatch > 0 {
		line += fmt.Sprintf(" · %d resolved by name match only", stat.NameMatch)
	}

	fmt.Fprintf(w, "%s\n", line)

	if o.left == 0 {
		o.unsampled++

		return nil
	}

	edges := o.sample[sym.ID]

	// Test callers bury the production callers, so they show only when
	// no other caller exists. The caller list of why <symbol> does the same.
	if stat.Total > stat.Tests {
		edges = withoutTests(edges)
	}

	edges = edges[:min(len(edges), outlineCallerSample, o.left)]
	o.left -= len(edges)

	parts := make([]string, 0, len(edges)+1)

	for _, c := range edges {
		// The location is the start of the caller's definition, not the
		// call site. So the line label says "caller definitions".
		part := scrubText(c.FQN) + " " + scrubText(location(c.Symbol))
		if c.Origin == model.OriginUniqueName {
			part += " (name match)"
		}

		parts = append(parts, part)
	}

	if more := stat.Total - len(edges); more > 0 {
		route, err := whyRoute(st, root, sym)
		if err != nil {
			return err
		}

		if route != "" {
			parts = append(parts, fmt.Sprintf("+%d more: %s", more, route))
		} else {
			parts = append(parts, fmt.Sprintf("+%d more (no why route: this name does not resolve to this symbol)", more))
		}
	}

	fmt.Fprintf(w, "           caller definitions  %s\n", strings.Join(parts, " · "))

	return nil
}

// printClosing writes the lines that follow the entries: the entries that
// the budget left without caller definitions, and the note on "none
// indexed". Each line prints once per outline, so the entries stay short.
func (o *outlineCallers) printClosing(w io.Writer) {
	if o.unsampled > 0 {
		fmt.Fprintf(w, "  … caller definitions not shown for %d symbols (outline budget: %d); their count lines still show\n",
			o.unsampled, outlineCallerBudget)
	}

	if o.noneIndexed {
		fmt.Fprintf(w, "  none indexed: the index skips some calls, so no edge does not prove that nothing calls the symbol;"+
			" why on the symbol lists the skip rules\n")
	}
}

// withoutTests returns the edges whose caller is not in a test file.
func withoutTests(edges []store.CallEdge) []store.CallEdge {
	out := make([]store.CallEdge, 0, len(edges))

	for _, c := range edges {
		if !model.IsTestPath(c.File) {
			out = append(out, c)
		}
	}

	return out
}

// whyRoute returns the why route that reaches sym, or "" when no route
// reaches it. It runs the steps of Why: a file match comes first, then
// the exact match. The route prints only when that exact match is this
// one symbol, because Why reports the first match, and a second match can
// stand first.
func whyRoute(st *store.Store, root string, sym model.Symbol) (string, error) {
	fqn := sym.FQN

	// An empty name is no query, and the MCP tool refuses it. The report
	// prints the scrubbed name, so a name that the scrub changes prints
	// as a name that the index does not hold.
	if fqn == "" || scrubText(fqn) != fqn {
		return "", nil
	}

	if _, ok := asIndexedFile(st, root, fqn); ok {
		return "", nil
	}

	exact, err := st.ExactSymbols(fqn, 2)
	if err != nil {
		return "", err
	}

	if len(exact) != 1 || exact[0].ID != sym.ID {
		return "", nil
	}

	// A shell reads some runes as syntax, for example "(", "$", or a
	// space, so the pasted name fails or names another symbol. The MCP
	// query takes the name as it is. So each surface gets its own route.
	if word := render.ShellWord(fqn); word != fqn {
		return "why " + fqn + " (MCP) or `seamark why " + word + "`", nil
	}

	return "why " + fqn, nil
}
