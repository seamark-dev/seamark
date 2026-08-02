package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// Orient writes the repo overview an agent needs before its first edit:
// scale, module layout, the load-bearing API, the files whose changes
// travel in groups, and the recent decision trail. One screen, not a
// tour — every extra line here is paid on every onboarding.
func Orient(w io.Writer, st *store.Store, root string) error {
	stats, err := st.Stats()
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "%s — orientation\n", path.Base(root))
	fmt.Fprintf(w, "  index  %d symbols · %d call/def edges · %d co-change pairs · %d decisions · %d effect-tagged symbols\n",
		stats.Symbols, stats.Edges, stats.CoChanges, stats.Decisions, stats.Tagged)

	if stats.Lessons > 0 {
		fmt.Fprintf(w, "         %d review lessons (run `seamark index --reviews` to refresh)\n", stats.Lessons)
	}

	// A compact coverage warning when files failed to parse: everything
	// below describes only the code the index can see, and pretending
	// otherwise would make incomplete answers look authoritative
	// (RFC-001 §5.2). Details live in `seamark status`.
	if summary, err := st.GetMeta("index_summary"); err == nil && summary != "" {
		var cov struct {
			ParseErrors int `json:"parse_errors"`
		}

		if json.Unmarshal([]byte(summary), &cov) == nil && cov.ParseErrors > 0 {
			noun := "files"
			if cov.ParseErrors == 1 {
				noun = "file"
			}

			fmt.Fprintf(w, "  WARN   %d %s failed to parse and are invisible below — `seamark status` has details\n",
				cov.ParseErrors, noun)
		}
	}

	if err := moduleSection(w, st); err != nil {
		return err
	}

	// Overfetch and drop test code: a heavily-reused test helper is not
	// the load-bearing surface a newcomer should read first.
	called, err := st.TopCalled(32)
	if err != nil {
		return err
	}

	production := called[:0]

	for _, c := range called {
		if !model.IsTestPath(c.File) {
			production = append(production, c)
		}
	}

	if len(production) > 8 {
		production = production[:8]
	}

	if len(production) > 0 {
		fmt.Fprintf(w, "\nmost-called (the load-bearing surface)\n")
		for _, c := range production {
			fmt.Fprintf(w, "  %-44s %-30s %d callers\n", c.FQN, location(c.Symbol), c.Callers)
		}
	}

	hot, err := st.HotFiles(8)
	if err != nil {
		return err
	}

	if len(hot) > 0 {
		fmt.Fprintf(w, "\nchange hubs (files whose changes rarely travel alone)\n")
		for _, h := range hot {
			fmt.Fprintf(w, "  %-50s %d coupled files, max lift %.1f\n", h.File, h.Partners, h.MaxLift)
		}
	}

	decisions, err := st.RecentDecisions(8)
	if err != nil {
		return err
	}

	if len(decisions) > 0 {
		fmt.Fprintf(w, "\nrecent decisions\n")
		printDecisions(w, decisions)
	}

	cfg := loadLessonConfig(w, root)

	mined, err := st.TopLessons(1, 200)
	if err != nil {
		return err
	}

	lessons := cfg.Surface(mined, "") // repo-wide scope: every pin applies
	if len(lessons) > 6 {
		lessons = lessons[:6]
	}

	if len(lessons) > 0 {
		fmt.Fprintf(w, "\nreviewers keep flagging  (recurring feedback — avoid the repeat)\n")
		printLessons(w, lessons)
	}

	fmt.Fprintf(w, "\nnext: `why <symbol|file>` for any of the above; `change_set` before editing; "+
		"`expand` for source; `expand lessons:<dir>` for an area's raw review findings\n")

	return nil
}

// moduleSection aggregates per-file symbol counts into top-level modules
// (two path segments deep, so internal/store and api both read well).
func moduleSection(w io.Writer, st *store.Store) error {
	counts, err := st.FileSymbolCounts()
	if err != nil {
		return err
	}

	modules := map[string]int{}

	for file, n := range counts {
		parts := strings.SplitN(file, "/", 3)

		switch len(parts) {
		case 1:
			modules["(root)"] += n
		case 2:
			modules[parts[0]] += n
		default:
			modules[parts[0]+"/"+parts[1]] += n
		}
	}

	type mod struct {
		name string
		n    int
	}

	ranked := make([]mod, 0, len(modules))
	for name, n := range modules {
		ranked = append(ranked, mod{name, n})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}

		return ranked[i].name < ranked[j].name
	})

	if len(ranked) == 0 {
		return nil
	}

	if len(ranked) > 12 {
		ranked = ranked[:12]
	}

	fmt.Fprintf(w, "\nmodules (by symbol count)\n")

	for _, m := range ranked {
		fmt.Fprintf(w, "  %-40s %d\n", m.name, m.n)
	}

	return nil
}
