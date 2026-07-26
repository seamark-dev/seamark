package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

func newWhyCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "why <symbol|file>",
		Short: "Explain a symbol or file: definition, callers, co-change, decisions",
		Long: `Answers, from the index: where is this defined, who calls it, which
files empirically change together with it, and which commits explain it.
Accepts a symbol name ("Store.Rebuild"), an FQN ("internal/store.Store.Rebuild"),
or a repo-relative file path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, root, err := openIndex(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			return runWhy(cmd.OutOrStdout(), st, root, args[0])
		},
	}
}

// openIndex opens an existing index, refusing to create an empty one.
func openIndex(opts *options) (*store.Store, string, error) {
	root, err := filepath.Abs(opts.workspace)
	if err != nil {
		return nil, "", err
	}

	dbPath := opts.dbPath
	if dbPath == "" {
		// Mirror the indexer: the index lives at the git toplevel.
		if r, err := gitToplevel(root); err == nil {
			root = r
		}

		dbPath = store.DefaultPath(root)
	}

	if _, err := os.Stat(dbPath); err != nil {
		return nil, "", errors.New("no index found; run `seamark index` first")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return nil, "", err
	}

	if r, err := st.GetMeta("repo_root"); err == nil && r != "" {
		root = r
	}

	return st, root, nil
}

func gitToplevel(dir string) (string, error) {
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")

	return strings.TrimSpace(out), err
}

// runWhy resolves query as a file or symbol and prints the report.
func runWhy(w io.Writer, st *store.Store, root, query string) error {
	if file, ok := asIndexedFile(st, root, query); ok {
		return fileReport(w, st, file)
	}

	syms, err := st.FindSymbols(query, 5)
	if err != nil {
		return err
	}

	if len(syms) == 0 {
		return fmt.Errorf("nothing in the index matches %q", query)
	}

	if err := symbolReport(w, st, syms[0]); err != nil {
		return err
	}

	if len(syms) > 1 {
		fmt.Fprintf(w, "\nalso matched\n")
		for _, s := range syms[1:] {
			fmt.Fprintf(w, "  %-40s %s\n", s.FQN, location(s))
		}
	}

	return nil
}

// asIndexedFile reports whether query names a file the index knows, trying
// the query as-given and relative to the workspace root (so paths pasted
// from a subdirectory shell still resolve).
func asIndexedFile(st *store.Store, root, query string) (string, bool) {
	candidates := []string{strings.TrimPrefix(filepath.ToSlash(query), "./")}

	if abs, err := filepath.Abs(query); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			candidates = append(candidates, filepath.ToSlash(rel))
		}
	}

	for _, c := range candidates {
		if syms, err := st.SymbolsInFile(c); err == nil && len(syms) > 0 {
			return c, true
		}
		if decs, err := st.DecisionsForFile(c, 1); err == nil && len(decs) > 0 {
			return c, true
		}
	}

	return "", false
}

func symbolReport(w io.Writer, st *store.Store, sym model.Symbol) error {
	fmt.Fprintf(w, "%s  (%s)\n", sym.FQN, sym.Kind)
	fmt.Fprintf(w, "  defined  %s\n", location(sym))

	if sym.Sig != "" {
		// Signatures come from source text; a raw string literal in a var
		// initializer can carry control bytes, so they get the same wash.
		fmt.Fprintf(w, "  sig      %s\n", render.Sanitize(sym.Sig))
	}

	effects, err := st.EffectsForSymbol(sym.ID)
	if err != nil {
		return err
	}

	if len(effects) > 0 {
		parts := make([]string, 0, len(effects))

		// Tags come from the workspace overlay — untrusted in a cloned repo.
		for _, e := range effects {
			if e.Origin == "direct" {
				parts = append(parts, render.Sanitize(e.Tag)+" [direct]")
			} else {
				parts = append(parts, fmt.Sprintf("%s [depth %d]", render.Sanitize(e.Tag), e.Depth))
			}
		}

		fmt.Fprintf(w, "  effects  %s\n", strings.Join(parts, " · "))
	}

	callers, err := st.Callers(sym.ID)
	if err != nil {
		return err
	}

	callees, err := st.Callees(sym.ID)
	if err != nil {
		return err
	}

	if len(callers) > 0 {
		fmt.Fprintf(w, "\ncallers (%d)%s\n", len(callers), originSummary(callers))
		printCallEdges(w, callers)
	}

	if len(callees) > 0 {
		fmt.Fprintf(w, "\ncalls (%d)%s\n", len(callees), originSummary(callees))
		printCallEdges(w, callees)
	}

	if sym.File != "" {
		if err := historySections(w, st, sym.File); err != nil {
			return err
		}
	}

	return nil
}

func fileReport(w io.Writer, st *store.Store, file string) error {
	fmt.Fprintf(w, "%s\n", file)

	syms, err := st.SymbolsInFile(file)
	if err != nil {
		return err
	}

	if len(syms) > 0 {
		fmt.Fprintf(w, "\ndefines (%d)\n", len(syms))
		for _, s := range limitSyms(syms, 20) {
			fmt.Fprintf(w, "  %-40s %-8s line %d\n", s.FQN, s.Kind, s.Span.StartLine)
		}
	}

	return historySections(w, st, file)
}

// historySections prints the co-change and decision layers for a file —
// the part of the report no structural tool can produce.
func historySections(w io.Writer, st *store.Store, file string) error {
	partners, err := st.CoChangePartners(file, 1.0, 10)
	if err != nil {
		return err
	}

	if len(partners) > 0 {
		fmt.Fprintf(w, "\nusually changed with  (empirical, lift > 1 means beyond chance)\n")
		for _, p := range partners {
			fmt.Fprintf(w, "  %-50s %2d/%d commits   lift %.1f\n",
				p.File, p.Together, p.Total, p.Lift)
		}
	}

	decisions, err := st.DecisionsForFile(file, 10)
	if err != nil {
		return err
	}

	if len(decisions) > 0 {
		fmt.Fprintf(w, "\nrecent decisions\n")
		for _, d := range decisions {
			marker := " "

			if d.Kind == model.DecisionRevert {
				marker = "⚠ revert"
			}

			fmt.Fprintf(w, "  %s  %.8s  %-60s %s %s\n",
				time.Unix(d.TS, 0).Format("2006-01-02"), d.Ref,
				render.Truncate(render.Sanitize(d.Title), 60), render.Sanitize(d.Author), marker)
		}
	}

	return nil
}

// printCallEdges lists neighbors with the derivation of each edge, so a
// low-confidence unique-name guess never masquerades as a resolved call.
func printCallEdges(w io.Writer, edges []store.CallEdge) {
	shown := edges
	if len(shown) > 15 {
		shown = shown[:15]
	}

	for _, c := range shown {
		fmt.Fprintf(w, "  %-40s %-34s [%s]\n", c.FQN, location(c.Symbol), c.Origin)
	}

	if len(edges) > len(shown) {
		fmt.Fprintf(w, "  … %d more\n", len(edges)-len(shown))
	}
}

// originSummary flags edge lists dominated by the name-guess tier.
func originSummary(edges []store.CallEdge) string {
	guesses := 0

	for _, c := range edges {
		if c.Origin == model.OriginUniqueName {
			guesses++
		}
	}

	if guesses == 0 {
		return ""
	}

	return fmt.Sprintf("  — %d resolved by name match only", guesses)
}

func location(s model.Symbol) string {
	if s.File == "" {
		return "(external)"
	}

	return fmt.Sprintf("%s:%d", s.File, s.Span.StartLine)
}

func limitSyms(syms []model.Symbol, n int) []model.Symbol {
	if len(syms) > n {
		return syms[:n]
	}

	return syms
}
