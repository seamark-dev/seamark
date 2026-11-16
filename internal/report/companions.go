package report

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seamark-dev/seamark/internal/fixes"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// companion is one file history says usually changes with a set of files
// the agent named, with the strongest evidence for it. The set is a plan
// (change_set) or a diff (check); a companion is never in the set.
type companion struct {
	file     string
	with     string // the set file that shares the most commits with it
	together int
	total    int // the commits co-change counts in the mined window
	lift     float64
	outside  bool // no set file lives in the companion's directory
}

// maxCompanions caps the closing list; the strongest partners come first.
const maxCompanions = 6

// CompanionsTitle heads the closing list in change_set and in check. The
// trace and the skills read the label, so it is one string.
const CompanionsTitle = "history suggests also reviewing"

// checkCompanionsTitle is the check variant: the diff, not a plan, is the
// set the companions are measured against.
const checkCompanionsTitle = CompanionsTitle + "  (usually changes with the diff's files, absent from this diff)"

// companionSet is the set of files a companion is measured against (the
// planned files or the diff's files, with their directories) and the
// partners noted so far, keyed by file with the strongest evidence seen.
// inSet holds the set files that are a partner of another set file.
type companionSet struct {
	files map[string]bool
	dirs  map[string]bool
	found map[string]companion
	inSet map[string]bool
}

func newCompanionSet(files []string) *companionSet {
	set := &companionSet{
		files: map[string]bool{}, dirs: map[string]bool{},
		found: map[string]companion{}, inSet: map[string]bool{},
	}

	for _, f := range files {
		set.files[f] = true
		set.dirs[path.Dir(f)] = true
	}

	return set
}

// note records partner p of set file `file` unless p is in the set,
// keeping the strongest evidence seen for it across the set.
func (s *companionSet) note(file string, p store.CoChangePartner) {
	if s.files[p.File] {
		s.inSet[p.File] = true

		return
	}

	c, seen := s.found[p.File]
	if seen && (p.Together < c.together || (p.Together == c.together && p.Lift <= c.lift)) {
		return
	}

	s.found[p.File] = companion{
		file: p.File, with: file, together: p.Together, total: p.Total, lift: p.Lift,
		outside: !s.dirs[path.Dir(p.File)],
	}
}

// partnerLimit is how many partners to fetch per set file: the closing
// list's cap plus one per set file, because the set's own files are
// filtered out afterwards and a large diff would otherwise hide every
// unplanned partner behind its own files.
func (s *companionSet) partnerLimit() int {
	return maxCompanions + len(s.files)
}

// collect notes the partners of every indexed set file.
func (s *companionSet) collect(st *store.Store, indexed []string) error {
	for _, file := range indexed {
		partners, err := st.CoChangePartners(file, 1.0, s.partnerLimit())
		if err != nil {
			return err
		}

		for _, p := range partners {
			s.note(file, p)
		}
	}

	return nil
}

// rankCompanions orders partners by shared commits, then lift, then the
// partners outside the set's directories, then name. The directory rule
// breaks ties only: when the numbers say nothing, the file in another
// module is the one a plan forgets.
func rankCompanions(companions map[string]companion) []companion {
	list := make([]companion, 0, len(companions))
	for _, c := range companions {
		list = append(list, c)
	}

	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]

		switch {
		case a.together != b.together:
			return a.together > b.together
		case a.lift != b.lift:
			return a.lift > b.lift
		case a.outside != b.outside:
			return a.outside
		default:
			return a.file < b.file
		}
	})

	if len(list) > maxCompanions {
		list = list[:maxCompanions]
	}

	return list
}

// historyBudget bounds the git work behind one list of partners: the why
// report's partner list and the closing companions list. Both print on
// every call, so the whole list shares one deadline instead of paying a
// full timeout per partner; a slow history then costs one budget, not six.
const historyBudget = 5 * time.Second

// printCompanions writes the closing list, ranked by rankCompanions: one
// line of numbers per partner and, when history has them, one line of
// reasons. A bare file name was the weakest line on the screen in the
// first workflow cohort; the reason is what makes a partner a question
// the agent answers. Both reasons are quoted facts from git, never a
// claim about the code.
//
// With window, the numbers take the N/M form of the change_set file
// lines, because change_set prints a listed partner only here. check
// prints no file lines, and its line keeps the shared-commit form.
func printCompanions(w io.Writer, st *store.Store, root, title string, list []companion, window bool) {
	if len(list) == 0 {
		return
	}

	fmt.Fprintln(w, title)

	funcs := partnerFunctions(root, list)

	for i, c := range list {
		// File names come from git history, where control characters are
		// legal, so they are sanitized like commit titles before the terminal.
		counts := fmt.Sprintf("%d shared commits", c.together)
		if window {
			counts = fmt.Sprintf("%d/%d commits", c.together, c.total)
		}

		fmt.Fprintf(w, "  %-50s %s with %s, lift %.1f\n",
			render.Sanitize(c.file), counts, render.Sanitize(c.with), c.lift)

		if reason := companionReason(st, c, funcs[i]); reason != "" {
			fmt.Fprintf(w, "    %s\n", reason)
		}
	}
}

// partnerFunctions names, for each listed partner, the functions the shared
// commits touched in it; the result is indexed like list. Every git call
// runs concurrently under one deadline. Several partners usually anchor on
// the same set file, and its commit list is the expensive part, so each
// set file is scanned once and its partners are diffed over those commits.
// Without a repository root there is no git to ask, so every entry is nil.
func partnerFunctions(root string, list []companion) [][]string {
	funcs := make([][]string, len(list))
	if root == "" {
		return funcs
	}

	ctx, cancel := context.WithTimeout(context.Background(), historyBudget)
	defer cancel()

	byAnchor := map[string][]int{}
	for i, c := range list {
		byAnchor[c.with] = append(byAnchor[c.with], i)
	}

	var wg sync.WaitGroup

	for with, partners := range byAnchor {
		wg.Add(1)

		go func() {
			defer wg.Done()

			commits := history.FileCommits(ctx, root, with)

			var diffs sync.WaitGroup

			for _, i := range partners {
				diffs.Add(1)

				go func() {
					defer diffs.Done()

					// Every name comes back, because liveFunctions drops the
					// removed ones before the cut to reasonFunctions.
					funcs[i] = history.PartnerFunctions(ctx, root, list[i].file, commits, 0)
				}()
			}

			diffs.Wait()
		}()
	}

	wg.Wait()

	return funcs
}

// reasonFunctions is how many function names a "mostly" reason shows.
const reasonFunctions = 3

// liveFunctions keeps the names in funcs that file still defines, in
// order, and cuts the result to reasonFunctions. Hunk headers describe the
// file as it was in each commit, so a renamed or removed function can
// appear there. An agent cannot open such a name in the current file.
// The match is exact, because a symbol's Name is the bare identifier that
// the hunk-header reader also gives.
//
// A file with no indexed symbols keeps every name. The index does not
// parse every language, so an empty list says nothing about the names. A
// store error keeps every name too, because the reason is only a hint.
// The store reads run here, after the git goroutines finish.
func liveFunctions(st *store.Store, file string, funcs []string) []string {
	if len(funcs) == 0 {
		return nil
	}

	syms, err := st.SymbolsInFile(file)
	if err != nil || len(syms) == 0 {
		return funcs[:min(len(funcs), reasonFunctions)]
	}

	defined := make(map[string]bool, len(syms))
	for _, s := range syms {
		defined[s.Name] = true
	}

	live := make([]string, 0, reasonFunctions)

	for _, name := range funcs {
		if !defined[name] {
			continue
		}

		live = append(live, name)

		if len(live) == reasonFunctions {
			break
		}
	}

	return live
}

// companionReason joins what the shared commits touched in the partner and
// the latest correction recorded on it. The first comes from git and is
// computed for the whole list at once; the second needs only the index, so
// an index without a repository still gives one.
func companionReason(st *store.Store, c companion, funcs []string) string {
	var parts []string

	if funcs = liveFunctions(st, c.file, funcs); len(funcs) > 0 {
		parts = append(parts, "mostly "+render.Sanitize(strings.Join(funcs, ", ")))
	}

	if fix := latestFix(st, c.file); fix != nil {
		parts = append(parts, fmt.Sprintf("last fix here: %s (%s)", render.Sanitize(fix.Title), shortRef(fix.Ref)))
	}

	return strings.Join(parts, " · ")
}

// latestFixWindow is how many recent decisions latestFix reads: the same
// window the why report lists decisions from, so a fix old enough to drop
// off that list stops being quoted as a reason here too.
const latestFixWindow = 20

// latestFix returns the most recent correction recorded on file, or nil.
// A fix on a companion right after a change to its partner is the trace a
// forgotten companion leaves in history, which is why it is quoted here. A
// commit the fix miner classifies wins over a newer revert: a fix subject
// says what the rule is, a revert subject only names what was withdrawn.
func latestFix(st *store.Store, file string) *model.Decision {
	decisions, err := st.DecisionsForFile(file, latestFixWindow)
	if err != nil {
		return nil
	}

	var revert *model.Decision

	for i := range decisions {
		d := &decisions[i]

		// The classifier reports a revert subject as a correction too, so
		// the kind and the classification are read together.
		source := fixes.Classify(d.Title, d.Body)

		if d.Kind == model.DecisionRevert || source == model.SourceRevert {
			if revert == nil {
				revert = d
			}

			continue
		}

		if source != "" {
			return d
		}
	}

	return revert
}

// shortRef abbreviates a commit hash the way git log does; other refs
// (a PR number, an ADR path, however long) stay whole.
func shortRef(ref string) string {
	if len(ref) >= 40 && strings.Trim(ref, "0123456789abcdefABCDEF") == "" {
		return ref[:7]
	}

	return ref
}

// CheckCompanions prints, after a gate verdict, the files history says
// usually change with the diff's files but that the diff leaves untouched.
// A forgotten companion is the omission history can see and a policy over
// effects cannot. The list is advisory and never part of the verdict; files
// comes from gate.ChangedPaths, like the lessons advisory, so the section
// can never disagree with the verdict about what changed.
//
// When no companion is listed, one status line says why. Silence would
// read the same for a diff that holds every partner, for files without
// stored partners, and for a failed read. An empty file list stays silent.
func CheckCompanions(w io.Writer, st *store.Store, root string, files []string) {
	if len(files) == 0 {
		return
	}

	names := make([]string, 0, len(files))
	indexed := make([]string, 0, len(files))

	for _, f := range files {
		name, ok := asIndexedFile(st, root, f)
		names = append(names, name)

		if ok {
			indexed = append(indexed, name)
		}
	}

	set := newCompanionSet(names)
	err := set.collect(st, indexed)

	fmt.Fprintln(w)

	if err != nil || len(set.found) == 0 {
		fmt.Fprintln(w, companionStatus(err, len(set.inSet)))

		return
	}

	printCompanions(w, st, root, checkCompanionsTitle, rankCompanions(set.found), false)
}

// companionStatus is the line check prints when it lists no companion.
// inDiff counts the diff files that are a partner of another diff file.
// The text is fixed apart from that count, so it carries no repository
// text and needs no sanitizing. It never starts with the list title,
// because a reader of the list would take its next line for a path.
func companionStatus(err error, inDiff int) string {
	switch {
	case err != nil:
		// The error text can carry paths, and the line only says that
		// the list is missing.
		return "companions  not shown (the co-change read failed)"
	case inDiff > 0:
		return fmt.Sprintf("companions  none outside this diff (co-change partners in the diff: %d)", inDiff)
	default:
		// Every diff file anchors its live partners. That includes a
		// deleted path and the old path of a rename record, because the
		// diff parser lists both. A deleted or renamed-away file is never
		// a partner, so it never counts here. The line prints only when
		// no diff file has a live partner, so it states what the index
		// offers, not what history shows.
		return "companions  none (the index holds no co-change partner for the diff's files)"
	}
}
