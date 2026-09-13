package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// ChangeSet reports what a planned edit to the given files drags along:
// the files history says change together with them, the externally
// visible symbols whose callers will feel the edit, and the effect tags
// the change can ultimately reach. This is the pre-edit question — "what
// am I about to forget?" — answered from evidence, not vibes.
func ChangeSet(w io.Writer, st *store.Store, root string, files []string) error {
	names := make([]string, 0, len(files))
	indexed := make([]string, 0, len(files))

	for _, f := range files {
		name, ok := asIndexedFile(st, root, f)
		if !ok {
			// Report it, so a typo never reads as "no data".
			fmt.Fprintf(w, "%s: not in the index (new file, or run `seamark index`)\n\n", name)
		}

		// Lessons and the companion set need only a path: a brand-new file
		// in a pinned region deserves its guidance MOST, and a new file the
		// agent listed is never suggested back to it. Co-change and
		// exposure need the index, so indexed stays the gate for those.
		names = append(names, name)

		if ok {
			indexed = append(indexed, name)
		}
	}

	set := newCompanionSet(names)

	for _, file := range indexed {
		fmt.Fprintf(w, "%s\n", file)

		// More partners are fetched than printed: the per-file lines stay
		// six, while the closing list must see past the planned files.
		partners, err := st.CoChangePartners(file, 1.0, set.partnerLimit())
		if err != nil {
			return err
		}

		for _, p := range partners[:min(len(partners), maxCompanions)] {
			// Partner names come from git history, where control
			// characters are legal, so they are sanitized before the terminal.
			fmt.Fprintf(w, "  usually changes with  %-46s %2d/%d commits, lift %.1f\n",
				render.Sanitize(p.File), p.Together, p.Total, p.Lift)
		}

		for _, p := range partners {
			set.note(file, p)
		}

		if err := exposureLines(w, st, file); err != nil {
			return err
		}

		fmt.Fprintln(w)
	}

	printCompanions(w, st, root, CompanionsTitle, set.found)

	// The repository's memory, at the moment it matters (RFC-002 §8):
	// the pins and recurring lessons governing the files about to
	// change, budgeted like every ambient injection. Degrades to
	// nothing, never fails the report — a broken lessons config must
	// not take the co-change answer down with it.
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	lessons, trimmed, err := LessonsForFiles(st, cfg, names, cfg.ChangeSetBudget())
	if err == nil && len(lessons) > 0 {
		PrintLessonBlock(w,
			"lessons for this change  (regions map to the files above; avoid repeating these)",
			lessons, trimmed)

		_ = reviews.RecordFiringSurface(root, "change_set", names, "", lessons)
	}

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
