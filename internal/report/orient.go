package report

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// The orient footer names the next steps. Two footers exist because the
// CLI has no change_set or expand command, and a printed route must run
// where it is printed. The CLI footer offers `seamark` commands and
// labels each MCP tool with "(MCP)", as the why footer does. Both name
// the same steps in the same order.
const (
	orientFooterMCP = "next: `why <symbol|file>` for any of the above; `change_set` before editing; " +
		"`expand` for source; `expand lessons:<dir>` for an area's raw review findings"

	orientFooterCLI = "next: `seamark why <symbol|file>` for any of the above; `change_set` (MCP) before editing; " +
		"`expand` (MCP) for source; `expand lessons:<dir>` (MCP) or `seamark lessons --region <dir>` " +
		"for an area's raw review findings"
)

// Orient writes the repo overview with the MCP footer, which names MCP
// tools. The MCP orient tool and the seamark://orient resource call it.
// The CLI calls OrientCLI instead.
func Orient(w io.Writer, st *store.Store, root string) error {
	return orient(w, st, root, orientFooterMCP, time.Now(), fingerprintOf(st), index.OtherVersion)
}

// OrientNamed is Orient with the title of named, the root that the index
// records. The MCP server can answer from the index of another
// repository, and the title then names that repository, as OrientCLI
// does. The rest reads root, the workspace of the server.
func OrientNamed(w io.Writer, st *store.Store, root, named string) error {
	return orientNamed(w, st, root, named, orientFooterMCP, time.Now(), fingerprintOf(st), index.OtherVersion)
}

// OrientCLI writes the same repo overview with the CLI footer, which
// offers only routes that run in the CLI.
func OrientCLI(w io.Writer, st *store.Store, root string) error {
	return orient(w, st, root, orientFooterCLI, time.Now(), fingerprintOf(st), index.OtherVersion)
}

// fingerprintOf returns the workspace fingerprint for the database of st,
// so the freshness compares what the index run stored.
func fingerprintOf(st *store.Store) func(string) string {
	return func(root string) string { return index.WorkspaceState(root, st.Path()) }
}

// orient writes the repo overview an agent needs before its first edit:
// scale, module layout, the load-bearing API, the files whose changes
// travel in groups, and the recent decision trail. One screen, not a
// tour — every extra line here is paid on every onboarding.
//
// now is the time that the index age uses, fingerprint gives the current
// workspace fingerprint for the freshness, and otherVersion reports an
// index of another seamark version. The callers pass all three, so a test
// fixes the clock, the git state, and the version without a shared
// variable.
func orient(w io.Writer, st *store.Store, root, footer string,
	now time.Time, fingerprint func(string) string, otherVersion func(*store.Store) (bool, error),
) error {
	return orientNamed(w, st, root, root, footer, now, fingerprint, otherVersion)
}

// orientNamed is orient with the title of named.
func orientNamed(w io.Writer, st *store.Store, root, named, footer string,
	now time.Time, fingerprint func(string) string, otherVersion func(*store.Store) (bool, error),
) error {
	stats, err := st.Stats()
	if err != nil {
		return err
	}

	// named can come from the database, so the title sanitizes it.
	fmt.Fprintf(w, "%s — orientation\n", render.Sanitize(path.Base(named)))
	fmt.Fprintf(w, "  index  %d symbols · %d call/def edges · %d co-change pairs · %d decisions · %d effect-tagged symbols\n",
		stats.Symbols, stats.Edges, stats.CoChanges, stats.Decisions, stats.Tagged)

	if stats.Lessons > 0 {
		fmt.Fprintf(w, "         %d review lessons (run `seamark index --reviews` to refresh)\n", stats.Lessons)
	}

	health, err := indexhealth.Read(st, root, fingerprint, otherVersion)
	if err != nil {
		return err
	}

	for _, line := range healthLines(health, now) {
		fmt.Fprintf(w, "%s\n", line)
	}

	// The warning names the files that failed to parse. Every section
	// below describes only the code that the index can see, so an agent
	// must know that the answers are incomplete. Unknown coverage gets
	// no warning, because the health header already names it.
	if cov := health.Coverage; cov.Known && cov.ParseErrors > 0 {
		noun := "files"
		if cov.ParseErrors == 1 {
			noun = "file"
		}

		fmt.Fprintf(w, "  WARN   %d %s failed to parse and are invisible below — `seamark status` has details\n",
			cov.ParseErrors, noun)
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

	mined, err := st.TopLessons(1, orientMinedRead)
	if err != nil {
		return err
	}

	// Every pin applies to the repository-wide scope, and the pins come
	// first. The pin count uses the rule of the surfaced mined count, so
	// a reviewer login "pinned" does not count as a pin.
	all := cfg.Surface(mined, "")
	pins := len(all) - surfacedMined(cfg, mined)
	lessons := all[:min(len(all), orientLessonLimit)]
	shownPins := min(pins, len(lessons))

	if len(lessons) > 0 {
		fmt.Fprintf(w, "\n%s\n", lessonHeader(shownPins, len(lessons)-shownPins))
		printLessons(w, lessons)

		hiddenPins := pins - shownPins
		hiddenMined := len(all) - len(lessons) - hiddenPins

		if line := orientMoreLine(hiddenPins, hiddenMined, len(mined) == orientMinedRead); line != "" {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	fmt.Fprintf(w, "\n%s\n", footer)

	return nil
}

// healthLines renders the three lines of the index health header: the
// index time and the freshness, the file coverage, and the call-edge
// origins. The facts and their rules come from indexhealth, which status
// also uses. The header offers no `seamark index` route. That command
// fails under the database of another repository, and the refresh notes
// already name the next step.
func healthLines(h indexhealth.Health, now time.Time) [3]string {
	const label, indent = "  health ", "         "

	indexed := "indexed unknown (no completed index run)"

	if h.IndexedAt > 0 {
		// UTC keeps the text the same in every local time zone.
		stamp := time.Unix(h.IndexedAt, 0).UTC().Format("2006-01-02 15:04") + " UTC"

		if age := indexhealth.Age(h.IndexedAt, now); age == "unknown" {
			indexed = "indexed " + stamp + " (age unknown)"
		} else {
			indexed = "indexed " + stamp + " (" + age + " ago)"
		}
	}

	var fresh string

	switch h.Freshness {
	case indexhealth.FreshCurrent:
		fresh = "freshness current"
	case indexhealth.FreshStale:
		fresh = "freshness stale (the workspace changed since this index)"
		if h.StaleReason == indexhealth.StaleOtherVersion {
			fresh = "freshness stale (this index was built by another seamark version)"
		}
	default:
		fresh = "freshness unknown (no git fingerprint)"
	}

	// The line names no parse errors, because the warning below names
	// them.
	coverage := "coverage unknown (no readable coverage summary) — `seamark status` has details"

	if c := h.Coverage; c.Known {
		coverage = fmt.Sprintf("parsed %d of %d seen files", c.FilesParsed, c.FilesSeen)

		if c.FilesSkipped > 0 {
			coverage += fmt.Sprintf(" · %d skipped by config", c.FilesSkipped)
		}

		// A Markdown or YAML file is seen and not parsed. The count says
		// why the parsed count is lower, so the gap does not read as lost
		// code.
		if c.FilesUnsupported > 0 {
			coverage += fmt.Sprintf(" · %d with no language extractor", c.FilesUnsupported)
		}

		switch {
		case c.FilesDeleted == 1:
			coverage += " · 1 tracked file deleted, not staged"
		case c.FilesDeleted > 1:
			coverage += fmt.Sprintf(" · %d tracked files deleted, not staged", c.FilesDeleted)
		}
	}

	// Zero call edges is a valid index, for example one with no supported
	// code. It prints a count, never a percent.
	origins := "none indexed (0 calls)"

	for _, n := range h.EdgeOrigins {
		if n > 0 {
			origins = indexhealth.OriginSummary(h.EdgeOrigins, orientOriginLimit)

			break
		}
	}

	return [3]string{
		label + indexed + " · " + fresh,
		indent + coverage,
		indent + "call edges by origin: " + origins,
	}
}

// orientOriginLimit is the most origins that the header names. The rest
// fold into one entry, so a database with many origins keeps one short
// line.
const orientOriginLimit = 5

// orientLessonLimit is the number of lessons that orient shows. A count
// line names the rest, so the cap hides no lesson silently.
const orientLessonLimit = 6

// orientMinedRead is the most mined lessons that orient reads. A read
// that returns this many can leave more out, so the count says "or more".
const orientMinedRead = 200

// orientMoreLine names the lessons that orient leaves out, per kind, and
// where each kind is listed. No command lists every pin with its note,
// so the pin route is the file. The line quotes no repository text, so
// it needs no scrub. It returns "" when orient leaves nothing out.
func orientMoreLine(pins, mined int, minedCapped bool) string {
	// A full read can leave more mined lessons out, so the counts that
	// hold them say "or more".
	more, minedMore := " more", ""
	minedNoun := plural(mined, "mined lesson", "mined lessons")

	if minedCapped {
		more, minedMore, minedNoun = " or more", " or more", "mined lessons"
	}

	pinNoun := plural(pins, "pin", "pins")

	switch {
	case pins > 0 && mined > 0:
		return fmt.Sprintf("+%d%s: %d %s (.seamark/lessons.yaml lists every pin), %d%s %s (`seamark lessons --list`)",
			pins+mined, more, pins, pinNoun, mined, minedMore, minedNoun)
	case pins > 0:
		return fmt.Sprintf("+%d more %s: .seamark/lessons.yaml lists every pin", pins, pinNoun)
	case mined > 0:
		return fmt.Sprintf("+%d%s %s: `seamark lessons --list` shows every mined lesson", mined, more, minedNoun)
	default:
		return ""
	}
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
