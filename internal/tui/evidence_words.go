package tui

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the words of the evidence workflow: source names,
// freshness badges, the review window, the repository, gh, and the
// outcome words. The Evidence view, the refresh preview and the run
// record share them, so one state has one word everywhere (design map
// §9). Text from the repository, git, gh or a reader is untrusted, so
// these helpers pass it through ui.OneLine or ui.Diag.

// Source names as the evidence screens show them.
const (
	evNameFixes   = "Local fixes"
	evNameReviews = "GitHub reviews"
)

// evSettingsIntent opens the evidence group of the Settings
// destination. The Settings screen parses "group:<name>" with the
// group name of the settings package.
const evSettingsIntent = "group:evidence"

// evModeTitle names the sources of a refresh mode in prose.
func evModeTitle(mode index.SourceMode) string {
	if mode == index.SourcesReviewsAndLocal {
		return "reviews + local fixes"
	}

	return "local fixes"
}

// evModeAction names the action row and the preview of a refresh mode.
func evModeAction(mode index.SourceMode) string {
	return "Refresh " + evModeTitle(mode)
}

// evFindings renders a finding count, or an unknown count when the
// store read failed. An unknown count never renders as zero.
func evFindings(n int, known bool) string {
	if !known {
		return "findings unknown"
	}

	return propPlural(n, "finding", "findings")
}

// evFresh is the freshness badge of one source: a tone and a word.
type evFresh struct {
	tone ui.Tone
	text string
}

// evFreshness returns the freshness of one source from its stored
// stamp and count. A stamp proves a refresh. No stamp and no finding
// means that no refresh is on record. No stamp with findings means an
// unknown time: an older seamark stored them before it recorded refresh
// times. Unknown counts make the time unknown too.
func evFreshness(stamp int64, count int, known bool) evFresh {
	switch {
	case stamp > 0:
		return evFresh{tone: ui.ToneSuccess, text: "refreshed " + ageSince(stamp)}
	case known && count == 0:
		return evFresh{tone: ui.ToneNeutral, text: "never refreshed"}
	default:
		return evFresh{tone: ui.ToneUnknown, text: "last refresh unknown"}
	}
}

// badge renders the freshness with its glyph. The never state uses the
// faint info glyph, as design map §9 asks.
func (f evFresh) badge(t *ui.Theme) string {
	if f.tone == ui.ToneNeutral {
		return t.Faint.Render(t.Glyphs.Info + " " + f.text)
	}

	return t.Badge(f.tone, f.text)
}

// evLastRefresh explains the last refresh time of one source in one
// sentence, for the detail pane.
func evLastRefresh(stamp int64, count int, known bool) string {
	switch {
	case stamp > 0:
		return ageSince(stamp) + " (" + time.Unix(stamp, 0).Format("2006-01-02 15:04") + ")"
	case !known:
		return "Unknown: the stored evidence could not be read."
	case count == 0:
		return "Never: no refresh is on record in this index."
	default:
		return "Unknown: findings exist, but no refresh time is recorded. An older seamark stored them " +
			"before it recorded refresh times."
	}
}

// evWindow renders the effective review window and where it comes
// from, for example "730 days (default)". ok is false when config.yaml
// could not be read, because no window is known then.
func evWindow(w learning.Window) (text string, ok bool) {
	if w.ConfigErr != nil {
		return "unknown", false
	}

	source := " (default)"
	if w.Stored != nil {
		source = " (config.yaml)"
	}

	if w.Effective < 0 {
		return "unlimited" + source, true
	}

	return propPlural(w.Effective, "day", "days") + source, true
}

// evWindowShort renders the window without its source, for a summary
// line.
func evWindowShort(w learning.Window) string {
	switch {
	case w.ConfigErr != nil:
		return "unknown"
	case w.Effective < 0:
		return "unlimited"
	default:
		return propPlural(w.Effective, "day", "days")
	}
}

// evFloorNote explains the newest-comment floor, so the window never
// reads as a strict cutoff.
func evFloorNote(w learning.Window) string {
	if w.Floor <= 0 {
		return ""
	}

	return "the newest " + strconv.Itoa(w.Floor) + " comments always stay"
}

// evRepo renders the GitHub target of the review fetch. It shows the
// slug, never the remote URL, because a URL can carry credentials. ok
// is false when no GitHub repository resolved.
func evRepo(repo reviews.Repository, state reviews.RepositoryState) (text string, ok bool) {
	switch state {
	case reviews.RepositoryGitHub:
		return ui.OneLine(repo.Slug()) + " " + "(remote origin)", true
	case reviews.RepositoryNoOrigin:
		return "unavailable: no origin remote", false
	case reviews.RepositoryNotGitHub:
		return "unavailable: origin is not a github.com repository", false
	default:
		return "not resolved", false
	}
}

// evRepoShort is the summary word of the repository state.
func evRepoShort(repo reviews.Repository, state reviews.RepositoryState) string {
	switch state {
	case reviews.RepositoryGitHub:
		return ui.OneLine(repo.Slug())
	case reviews.RepositoryNoOrigin:
		return "no origin remote"
	case reviews.RepositoryNotGitHub:
		return "origin not on GitHub"
	default:
		return "repository not resolved"
	}
}

// evGH renders where gh is. A path on PATH is not a login, so the
// words never claim access.
func evGH(path string) string {
	if path == "" {
		return "gh not found on PATH"
	}

	return "gh found on PATH (" + ui.OneLine(path) + ")"
}

// evFixRows returns the fix-mined sources that hold findings, largest
// first, as label and count pairs. The source names come from the
// store, so ui.OneLine sanitizes them.
func evFixRows(by map[string]int) [][2]string {
	type row struct {
		name string
		n    int
	}

	var rows []row

	for _, src := range domain.FixMinedSources() {
		if n := by[src]; n > 0 {
			rows = append(rows, row{name: ui.OneLine(src), n: n})
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].n > rows[j].n })

	out := make([][2]string, len(rows))
	for i, r := range rows {
		out[i] = [2]string{r.name, strconv.Itoa(r.n)}
	}

	return out
}

// evTotal sums the stored findings of every source.
func evTotal(by map[string]int) int {
	total := 0
	for _, n := range by {
		total += n
	}

	return total
}

// evCauseWord names a typed source cause in a few words, for "failed:
// <cause>" and "unavailable: <cause>". The console never parses the
// reason text for this.
func evCauseWord(c index.SourceCause) string {
	switch c {
	case index.CauseNoHistory:
		return "no git history"
	case index.CauseMineFailed:
		return "git mine"
	case index.CauseNoRepository:
		return "no GitHub origin"
	case index.CauseGHMissing:
		return "gh not on PATH"
	case index.CauseAuth:
		return "gh login"
	case index.CauseRateLimited:
		return "rate limit"
	case index.CauseFetchFailed:
		return "fetch error"
	case index.CauseUnreadable:
		return "unreadable reply"
	case index.CauseStore:
		return "database"
	case index.CauseRefused:
		return "refused"
	default:
		return ""
	}
}

// evState is the display of one source outcome: its tone, its glyph
// and its word. Failed and the attention states share the glyph "!", so
// the word always differs.
type evState struct {
	tone ui.Tone
	word string
}

// evOutcomeState maps a typed source outcome to its display.
func evOutcomeState(o index.SourceOutcome) evState {
	cause := evCauseWord(o.Cause)

	switch o.State {
	case index.SourceRefreshed:
		return evState{tone: ui.ToneSuccess, word: "refreshed"}
	case index.SourceEmpty:
		return evState{tone: ui.ToneSuccess, word: "empty"}
	case index.SourceUnavailable:
		return evState{tone: ui.ToneAttention, word: joinCause("unavailable", cause)}
	case index.SourceFailed:
		return evState{tone: ui.ToneFailure, word: joinCause("failed", cause)}
	case index.SourceStopped:
		return evState{tone: ui.ToneAttention, word: "stopped"}
	default:
		if o.Cause == index.CauseRefused {
			return evState{tone: ui.ToneAttention, word: "not attempted: refused"}
		}

		return evState{tone: ui.ToneAttention, word: "not attempted"}
	}
}

// joinCause joins a state word and its cause word with a colon.
func joinCause(word, cause string) string {
	if cause == "" {
		return word
	}

	return word + ": " + cause
}

// evSucceeded reports a source that replaced its stored corpus.
func evSucceeded(o index.SourceOutcome) bool {
	return o.State == index.SourceRefreshed || o.State == index.SourceEmpty
}

// evBytes renders a byte count of a fetch in binary units. No total
// exists, so it is activity and never a progress fraction.
func evBytes(n int) string {
	const kib = 1024

	switch {
	case n < kib:
		return strconv.Itoa(n) + " B"
	case n < kib*kib:
		return strconv.Itoa(n/kib) + " KiB"
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(kib*kib))
	}
}

// evSameObservation reports whether a new evidence snapshot still shows
// what a plan saw. The backend rejects a stale plan anyway; the view
// closes the preview early, so the user never confirms old facts. A
// blocked plan resolves no repository and no gh, so only its index and
// window facts count.
func evSameObservation(plan learning.Snapshot, now learning.Snapshot) bool {
	if plan.Probe.State != now.Probe.State || plan.Probe.Root != now.Probe.Root ||
		plan.Probe.Schema != now.Probe.Schema {
		return false
	}

	if (plan.Window.ConfigErr == nil) != (now.Window.ConfigErr == nil) ||
		plan.Window.Effective != now.Window.Effective || !sameStored(plan.Window.Stored, now.Window.Stored) {
		return false
	}

	// A failed count read tells nothing about a change, so a transient
	// read error keeps the preview.
	if plan.CountsErr != nil || now.CountsErr != nil {
		return true
	}

	if plan.FixesMinedAt != now.FixesMinedAt || plan.ReviewsMinedAt != now.ReviewsMinedAt ||
		plan.FixFindings != now.FixFindings || plan.ReviewFindings != now.ReviewFindings ||
		plan.Lessons != now.Lessons {
		return false
	}

	if plan.RepoState == reviews.RepositoryUnresolved {
		return true
	}

	return plan.Repository == now.Repository && plan.RepoState == now.RepoState && plan.GHPath == now.GHPath
}

// sameStored compares two optional window values.
func sameStored(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return *a == *b
}
