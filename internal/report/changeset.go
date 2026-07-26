package report

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/store"
)

// ChangeSet reports what a planned edit to the given files drags along:
// the files history says change together with them, the externally
// visible symbols whose callers will feel the edit, and the effect tags
// the change can ultimately reach. This is the pre-edit question — "what
// am I about to forget?" — answered from evidence, not vibes.
func ChangeSet(w io.Writer, st *store.Store, root string, files []string) error {
	inSet := map[string]bool{}
	resolved := make([]string, 0, len(files))

	for _, f := range files {
		name, ok := asIndexedFile(st, root, f)
		if !ok {
			// Normalize anyway so the companion aggregation can still
			// exclude it; report it so a typo never reads as "no data".
			name = strings.TrimPrefix(filepath.ToSlash(f), "./")
			fmt.Fprintf(w, "%s: not in the index (new file, or run `seamark index`)\n\n", name)
		}

		inSet[name] = true

		if ok {
			resolved = append(resolved, name)
		}
	}

	// companions accumulates partner files across the whole set, keyed by
	// strongest evidence, so the closing suggestion is deduplicated.
	companions := map[string]companion{}

	for _, file := range resolved {
		fmt.Fprintf(w, "%s\n", file)

		partners, err := st.CoChangePartners(file, 1.0, 6)
		if err != nil {
			return err
		}

		for _, p := range partners {
			fmt.Fprintf(w, "  usually changes with  %-46s %2d/%d commits, lift %.1f\n",
				p.File, p.Together, p.Total, p.Lift)

			if !inSet[p.File] {
				if c, ok := companions[p.File]; !ok || p.Together > c.together {
					companions[p.File] = companion{p.Together, p.Lift}
				}
			}
		}

		if err := exposureLines(w, st, file); err != nil {
			return err
		}

		fmt.Fprintln(w)
	}

	suggestCompanions(w, companions)

	return nil
}

// exposureLines prints who depends on the file's symbols and what the
// file can reach — the structural half of the blast radius.
func exposureLines(w io.Writer, st *store.Store, file string) error {
	syms, err := st.SymbolsInFile(file)
	if err != nil {
		return err
	}

	// Always state what the file defines: when the caller/effect lines
	// below are absent (type-only files have references, not call
	// edges), silence must read as "indexed, nothing to report" — an
	// agent that suspects missing data burns probe calls re-asking.
	if len(syms) > 0 {
		fmt.Fprintf(w, "  defines                %d symbols\n", len(syms))
	}

	callerCounts, err := st.CallerCounts(file)
	if err != nil {
		return err
	}

	type exposed struct {
		fqn string
		n   int
	}

	var ranked []exposed
	tagSet := map[string]bool{}

	for i := range syms {
		if n := callerCounts[syms[i].ID]; n > 0 {
			ranked = append(ranked, exposed{syms[i].FQN, n})
		}

		effs, err := st.EffectsForSymbol(syms[i].ID)
		if err != nil {
			return err
		}

		for _, e := range effs {
			tagSet[e.Tag] = true
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}

		return ranked[i].fqn < ranked[j].fqn
	})

	if len(ranked) > 0 {
		if len(ranked) > 4 {
			ranked = ranked[:4]
		}

		parts := make([]string, 0, len(ranked))
		for _, e := range ranked {
			parts = append(parts, fmt.Sprintf("%s (%d)", e.fqn, e.n))
		}

		fmt.Fprintf(w, "  called from outside    %s\n", strings.Join(parts, " · "))
	}

	if len(tagSet) > 0 {
		tags := make([]string, 0, len(tagSet))
		for t := range tagSet {
			tags = append(tags, t)
		}

		sort.Strings(tags)
		fmt.Fprintf(w, "  effects reachable      %s\n", strings.Join(tags, " · "))
	}

	return nil
}

// companion carries the strongest co-change evidence seen for a partner
// file outside the change set.
type companion struct {
	together int
	lift     float64
}

func suggestCompanions(w io.Writer, companions map[string]companion) {
	if len(companions) == 0 {
		return
	}

	type ranked struct {
		file     string
		together int
		lift     float64
	}

	list := make([]ranked, 0, len(companions))
	for f, c := range companions {
		list = append(list, ranked{f, c.together, c.lift})
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].together != list[j].together {
			return list[i].together > list[j].together
		}

		return list[i].file < list[j].file
	})

	if len(list) > 6 {
		list = list[:6]
	}

	fmt.Fprintf(w, "history suggests also reviewing\n")

	for _, r := range list {
		fmt.Fprintf(w, "  %-50s %d shared commits, lift %.1f\n", r.file, r.together, r.lift)
	}
}
