package report

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
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
	lift     float64
	outside  bool // no set file lives in the companion's directory
}

// maxCompanions caps the closing list; the strongest partners come first.
const maxCompanions = 6

// CompanionsTitle heads the closing list in change_set and in check. The
// trace and the skills read the label, so it is one string.
const CompanionsTitle = "history suggests also reviewing"

// companionSet is the set of files a companion is measured against: the
// planned files or the diff's files, with their directories.
type companionSet struct {
	files map[string]bool
	dirs  map[string]bool
}

func newCompanionSet(files []string) companionSet {
	set := companionSet{files: map[string]bool{}, dirs: map[string]bool{}}

	for _, f := range files {
		set.files[f] = true
		set.dirs[path.Dir(f)] = true
	}

	return set
}

// note records partner p of set file `file` unless p is in the set,
// keeping the strongest evidence seen for it across the set.
func (s companionSet) note(companions map[string]companion, file string, p store.CoChangePartner) {
	if s.files[p.File] {
		return
	}

	c, seen := companions[p.File]
	if seen && (p.Together < c.together || (p.Together == c.together && p.Lift <= c.lift)) {
		return
	}

	companions[p.File] = companion{
		file: p.File, with: file, together: p.Together, lift: p.Lift,
		outside: !s.dirs[path.Dir(p.File)],
	}
}

// partnerLimit is how many partners to fetch per set file: the closing
// list's cap plus one per set file, because the set's own files are
// filtered out afterwards and a large diff would otherwise hide every
// unplanned partner behind its own files.
func (s companionSet) partnerLimit() int {
	return maxCompanions + len(s.files)
}

// collectCompanions gathers the partners of every indexed file in set that
// the set itself leaves out.
func collectCompanions(st *store.Store, set companionSet, indexed []string) (map[string]companion, error) {
	companions := map[string]companion{}

	for _, file := range indexed {
		partners, err := st.CoChangePartners(file, 1.0, set.partnerLimit())
		if err != nil {
			return nil, err
		}

		for _, p := range partners {
			set.note(companions, file, p)
		}
	}

	return companions, nil
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

// companionReasonBudget bounds the git work behind one closing list. The
// list prints on every change_set and check call, so the whole list shares
// one deadline instead of paying a full timeout per partner; a slow history
// then costs one budget, not six.
const companionReasonBudget = 5 * time.Second

// printCompanions writes the closing list: one line of numbers per partner
// and, when history has them, one line of reasons. A bare file name was
// the weakest line on the screen in the first workflow cohort; the reason
// is what makes a partner a question the agent answers. Both reasons are
// quoted facts from git, never a claim about the code.
func printCompanions(w io.Writer, st *store.Store, root, title string, companions map[string]companion) {
	list := rankCompanions(companions)
	if len(list) == 0 {
		return
	}

	fmt.Fprintln(w, title)

	funcs := partnerFunctions(root, list)

	for i, c := range list {
		fmt.Fprintf(w, "  %-50s %d shared commits with %s, lift %.1f\n", c.file, c.together, c.with, c.lift)

		if reason := companionReason(st, c, funcs[i]); reason != "" {
			fmt.Fprintf(w, "    %s\n", reason)
		}
	}
}

// partnerFunctions names, for each listed partner, the functions the shared
// commits touched in it; the result is indexed like list. Every git call in
// the list runs concurrently under one deadline: the set files are scanned
// once each, then every partner is diffed over its set file's commits only.
// Without a repository root there is no git to ask, so every entry is nil.
func partnerFunctions(root string, list []companion) [][]string {
	funcs := make([][]string, len(list))
	if root == "" {
		return funcs
	}

	ctx, cancel := context.WithTimeout(context.Background(), companionReasonBudget)
	defer cancel()

	// One scan per distinct set file: several partners usually anchor on
	// the same planned file, and its commit list is the expensive part.
	// The anchors are listed before the scans start, so no goroutine
	// writes the map while another ranges over it.
	shared := map[string]map[string]bool{}
	for _, c := range list {
		shared[c.with] = nil
	}

	anchors := slices.Sorted(maps.Keys(shared))

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, with := range anchors {
		wg.Add(1)

		go func(with string) {
			defer wg.Done()

			commits := history.FileCommits(ctx, root, with)

			mu.Lock()
			shared[with] = commits
			mu.Unlock()
		}(with)
	}

	wg.Wait()

	for i, c := range list {
		wg.Add(1)

		go func(i int, c companion) {
			defer wg.Done()

			funcs[i] = history.PartnerFunctions(ctx, root, c.file, shared[c.with], 3)
		}(i, c)
	}

	wg.Wait()

	return funcs
}

// companionReason joins what the shared commits touched in the partner and
// the latest correction recorded on it. The first comes from git and is
// computed for the whole list at once; the second needs only the index, so
// an index without a repository still gives one.
func companionReason(st *store.Store, c companion, funcs []string) string {
	var parts []string

	if len(funcs) > 0 {
		parts = append(parts, "mostly "+render.Sanitize(strings.Join(funcs, ", ")))
	}

	if fix := latestFix(st, c.file); fix != nil {
		parts = append(parts, fmt.Sprintf("last fix here: %s (%s)", render.Sanitize(fix.Title), shortRef(fix.Ref)))
	}

	return strings.Join(parts, " · ")
}

// latestFix returns the most recent correction recorded on file, or nil.
// A fix on a companion right after a change to its partner is the trace a
// forgotten companion leaves in history, which is why it is quoted here. A
// commit the fix miner classifies wins over a newer revert: a fix subject
// says what the rule is, a revert subject only names what was withdrawn.
func latestFix(st *store.Store, file string) *model.Decision {
	decisions, err := st.DecisionsForFile(file, 20)
	if err != nil {
		return nil
	}

	var revert *model.Decision

	for i := range decisions {
		d := &decisions[i]

		// The classifier reports a revert subject as a correction too, so
		// the kind and the classification are read together.
		switch source := fixes.Classify(d.Title, d.Body); {
		case d.Kind == model.DecisionRevert || source == model.SourceRevert:
			if revert == nil {
				revert = d
			}
		case source != "":
			return d
		}
	}

	return revert
}

// isFix reports whether a decision is a correction: a revert, or a commit
// the fix miner classifies as a fix from its title or body. One rule
// serves the fix-density line, the heat colours, and the companion reason.
func isFix(d model.Decision) bool {
	return d.Kind == model.DecisionRevert || fixes.Classify(d.Title, d.Body) != ""
}

// shortRef abbreviates a commit hash the way git log does; other refs
// (a PR number, an ADR path, however long) stay whole.
func shortRef(ref string) string {
	if len(ref) >= 40 && isHex(ref) {
		return ref[:7]
	}

	return ref
}

func isHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}

	return value != ""
}

// CheckCompanions prints, after a gate verdict, the files history says
// usually change with the diff's files but that the diff leaves untouched.
// A forgotten companion is the omission history can see and a policy over
// effects cannot. The list is advisory and never part of the verdict; files
// comes from gate.ChangedPaths, like the lessons advisory, so the section
// can never disagree with the verdict about what changed. It degrades to
// silence on any error.
func CheckCompanions(w io.Writer, st *store.Store, root string, files []string) {
	if len(files) == 0 {
		return
	}

	names := make([]string, 0, len(files))
	indexed := make([]string, 0, len(files))

	for _, f := range files {
		name, ok := asIndexedFile(st, root, f)
		if !ok {
			name = strings.TrimPrefix(path.Clean(strings.ReplaceAll(f, "\\", "/")), "./")
		}

		names = append(names, name)

		if ok {
			indexed = append(indexed, name)
		}
	}

	companions, err := collectCompanions(st, newCompanionSet(names), indexed)
	if err != nil || len(companions) == 0 {
		return
	}

	fmt.Fprintln(w)
	printCompanions(w, st, root, CompanionsTitle+"  (usually changes with the diff's files, absent from this diff)", companions)
}
