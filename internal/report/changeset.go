package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// ChangeSetOptions are the options of ChangeSetWith.
type ChangeSetOptions struct {
	// Record appends the legacy firing record of the change_set surface
	// for the printed lessons. MCP change_set sets it to false, because
	// the record of its preparation is the one record of the call.
	Record bool
}

// ChangeSet reports what a planned edit to the given files drags along:
// the files history says change together with them, the symbols that
// other non-test files call, with the count of those callers, and the
// effect tags the change can ultimately reach. This is the pre-edit
// question — "what am I about to forget?" — answered from evidence.
//
// ChangeSet records the printed lessons with the legacy writer. No
// production caller uses it since MCP change_set moved to
// ChangeSetWith. The tests of the text and of the legacy record keep
// it.
func ChangeSet(w io.Writer, st *store.Store, root string, files []string) error {
	_, err := ChangeSetWith(w, st, root, files, ChangeSetOptions{Record: true})

	return err
}

// CheckChangeSetFiles rejects the first files entry that names no path:
// an empty entry, an entry of only white space, or an entry with a NUL
// byte. The lesson preparation rejects the empty and NUL entries as
// malformed. This one check runs before both parts, so the text and the
// preparation never disagree about an entry. The error names the
// 1-based position and no input bytes, so it needs no sanitizing.
//
// The report package cannot import delivery, because delivery imports
// report. A test in package mcp keeps the two rules in step.
func CheckChangeSetFiles(files []string) error {
	for i, f := range files {
		var problem string

		switch {
		case f == "":
			problem = "is empty"
		case strings.ContainsRune(f, 0):
			problem = "holds a NUL byte"
		case strings.TrimSpace(f) == "":
			problem = "holds only white space"
		default:
			continue
		}

		return fmt.Errorf("change_set needs a path in every files entry: entry %d %s", i+1, problem)
	}

	return nil
}

// ChangeSetWith writes the text of ChangeSet and returns the lessons of
// its lesson block. The text does not depend on opts. MCP change_set
// needs the lessons for the fallback record of a failed preparation.
func ChangeSetWith(w io.Writer, st *store.Store, root string, files []string,
	opts ChangeSetOptions,
) (selected []model.Lesson, err error) {
	// The check comes before the first store read. Package nodes have an
	// empty file, so an empty name matches them, and an empty lesson
	// scope matches the whole repository.
	if err := CheckChangeSetFiles(files); err != nil {
		return nil, err
	}

	names, unindexed := ChangeSetNames(st, root, files)
	notIndexed := make(map[string]bool, len(unindexed))

	for _, name := range unindexed {
		// Report it, so a typo never reads as "no data". The name is
		// tool input, so it is sanitized like every path on this surface.
		fmt.Fprintf(w, "%s: not in the index (new file, or run `seamark index`)\n\n", render.Sanitize(name))

		notIndexed[name] = true
	}

	// Co-change and exposure need the index, so indexed stays the gate
	// for those. asIndexedFile answers from the resolved name alone, so
	// a name is unindexed for every spelling in the request or for none.
	indexed := make([]string, 0, len(names))

	for _, name := range names {
		if !notIndexed[name] {
			indexed = append(indexed, name)
		}
	}

	set := newCompanionSet(names)

	// More partners are fetched than printed: the per-file lines stay
	// six, while the closing list must see past the planned files. The
	// closing list needs the partners of every file, so they are read
	// before the first file block.
	partners := make([][]store.CoChangePartner, len(indexed))

	for i, file := range indexed {
		var err error

		if partners[i], err = st.CoChangePartners(file, 1.0, set.partnerLimit()); err != nil {
			return nil, err
		}

		for _, p := range partners[i] {
			set.note(file, p)
		}
	}

	// A partner prints once. The closing list holds the partners that the
	// plan leaves out, with their counts and reasons, so a file block
	// keeps only the partners that the closing list does not show: the
	// planned files and the partners past its cap.
	closing := rankCompanions(set.found)
	listed := make(map[string]bool, len(closing))

	for _, c := range closing {
		listed[c.file] = true
	}

	for i, file := range indexed {
		fmt.Fprintf(w, "%s\n", file)

		for _, p := range partners[i][:min(len(partners[i]), maxCompanions)] {
			if listed[p.File] {
				continue
			}

			// Partner names come from git history, where control
			// characters are legal, so they are sanitized before the terminal.
			fmt.Fprintf(w, "  usually changes with  %-46s %2d/%d commits, lift %.1f\n",
				render.Sanitize(p.File), p.Together, p.Total, p.Lift)
		}

		if err := exposureLines(w, st, file); err != nil {
			return nil, err
		}

		fmt.Fprintln(w)
	}

	printCompanions(w, st, root, CompanionsTitle, closing, true)

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
	if err != nil || len(lessons) == 0 {
		return nil, nil
	}

	PrintLessonBlock(w,
		"lessons for this change  (regions map to the files above; avoid repeating these)",
		lessons, trimmed)

	if opts.Record {
		_ = reviews.RecordFiringSurface(root, "change_set", names, "", lessons)
	}

	return lessons, nil
}

// ChangeSetNames names the files of a change_set request the way the
// index names them (asIndexedFile). names holds one name per file, in
// request order. unindexed holds the names that the index does not know,
// in the same order. A selection sample of the change_set surface names
// its paths here, so it selects for the same files as ChangeSet.
func ChangeSetNames(st *store.Store, root string, files []string) (names, unindexed []string) {
	names = make([]string, 0, len(files))

	for _, f := range files {
		name, ok := asIndexedFile(st, root, f)

		// Lessons and the companion set need only a path: a brand-new file
		// in a pinned region deserves its guidance MOST, and a new file the
		// agent listed is never suggested back to it. So names keeps an
		// unindexed file too.
		names = append(names, name)

		if !ok {
			unindexed = append(unindexed, name)
		}
	}

	return names, unindexed
}

// exposureLines prints who depends on the file's symbols and what the
// file can reach — the structural half of the blast radius. The caller
// line counts only callers in other non-test files. An edit inside the
// file reaches its own callers too. Test callers bury the production
// callers, and the why caller list folds them for the same reason.
func exposureLines(w io.Writer, st *store.Store, file string) error {
	syms, err := st.SymbolsInFile(file)
	if err != nil {
		return err
	}

	// Always state what the file defines. When the caller and effect
	// lines below are absent, a type-only file has references, not call
	// edges, so silence reads as "indexed, nothing to report". A file
	// with no indexed symbol has no such evidence at all. Missing
	// evidence is not "nothing to report", so the line says that the
	// reach is unknown. The text is fixed, so it needs no scrub.
	if len(syms) == 0 {
		fmt.Fprintf(w, "  defines                no indexed symbols — its callers and effect reach are unknown, not none\n")

		return nil
	}

	fmt.Fprintf(w, "  defines                %d symbols\n", len(syms))

	type exposed struct {
		fqn string
		n   int
	}

	// One query counts the callers of every symbol in the file. why
	// <file> reads the same counts, so both reports use one rule.
	stats, err := st.CallerStats(file)
	if err != nil {
		return err
	}

	var ranked []exposed
	tagSet := map[string]bool{}

	for i := range syms {
		if n := stats[syms[i].ID].OtherFiles; n > 0 {
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

		// The suffix names the count unit, so a reader does not take n
		// for all callers or for caller files.
		fmt.Fprintf(w, "  called from outside    %s — callers in other non-test files\n", strings.Join(parts, " · "))
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
