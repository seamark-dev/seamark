// Package indexhealth reads the index health facts that both `seamark
// status` and orient show: the file coverage of the last index run, the
// index time, the freshness against the workspace, and the call-edge
// origins. One package owns the rules, so the two surfaces cannot read
// the same database in two ways. It holds no integration state, so
// orient can use it without the full status gatherer.
package indexhealth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// Coverage is the persisted file summary of the last index run.
type Coverage struct {
	// Known is false when no readable summary exists. That includes an
	// index from before coverage was persisted. The renderer must then
	// say "unknown" and never imply full coverage.
	Known        bool `json:"known"`
	FilesSeen    int  `json:"files_seen,omitempty"`
	FilesParsed  int  `json:"files_parsed,omitempty"`
	FilesSkipped int  `json:"files_skipped,omitempty"`
	// FilesDeleted counts the tracked paths that git status shows as
	// deleted and not staged. It is not a parse error. An index from
	// before this field reads 0.
	FilesDeleted int `json:"files_deleted"`
	ParseErrors  int `json:"parse_errors"`
	// FilesUnsupported counts the seen files that no language extractor
	// handles, such as Markdown or YAML. The indexer counts every other
	// seen file as parsed, skipped, or a parse error, so ParseCoverage
	// derives it and the summary stores no count of its own.
	FilesUnsupported int `json:"files_unsupported"`
}

// Freshness states: whether the index still matches the workspace.
const (
	FreshCurrent = "current"
	FreshStale   = "stale"
	// FreshUnknown means no fingerprint exists to compare — a non-git
	// workspace has no cheap change signal. Unknown is not current.
	FreshUnknown = "unknown"
)

// Stale reasons: why a stale index is stale. The rebuild fixes both, so
// another version wins when both hold.
const (
	// StaleWorkspaceChanged means that the workspace fingerprint does not
	// match the one that the index records.
	StaleWorkspaceChanged = "workspace-changed"
	// StaleOtherVersion means that another seamark version built the
	// index, so it can lack facts that this version records.
	StaleOtherVersion = "other-version"
)

// Health holds the facts that Read gets from one index database.
type Health struct {
	// IndexedAt is the Unix time of the last full index run. A value of
	// 0 or less means that no readable time exists.
	IndexedAt int64
	Freshness string
	// StaleReason says why a stale index is stale; it is empty for any
	// other freshness.
	StaleReason string
	Coverage    Coverage
	// EdgeOrigins counts the CALLS edges per resolution origin. It is
	// nil when the index has no call edge.
	EdgeOrigins map[string]int
}

// Read gets the health facts from st. fingerprint returns the current
// workspace fingerprint of root, and otherVersion reports an index that
// another seamark version built. Production passes index.WorkspaceState
// and index.OtherVersion; the caller injects them, so this package does
// not import the indexer. An index of another version is stale whatever
// its fingerprint, because only a rebuild adds the facts it lacks. Read
// calls fingerprint only for an index of this version that records a
// fingerprint, because the call runs git. Every store error comes back,
// because a failed read is not the same as an absent fact.
func Read(st *store.Store, root string, fingerprint func(string) string,
	otherVersion func(*store.Store) (bool, error),
) (Health, error) {
	h := Health{Freshness: FreshUnknown}

	indexedAt, err := st.GetMeta(store.MetaIndexedAt)
	if err != nil {
		return Health{}, err
	}

	summary, err := st.GetMeta(store.MetaIndexSummary)
	if err != nil {
		return Health{}, err
	}

	recorded, err := st.GetMeta(store.MetaIndexedState)
	if err != nil {
		return Health{}, err
	}

	h.IndexedAt = ParseUnix(indexedAt)
	h.Coverage = ParseCoverage(summary)

	other, err := otherVersion(st)
	if err != nil {
		return Health{}, err
	}

	switch {
	case other:
		h.Freshness, h.StaleReason = FreshStale, StaleOtherVersion
	case recorded != "":
		if h.Freshness = Freshness(recorded, fingerprint(root)); h.Freshness == FreshStale {
			h.StaleReason = StaleWorkspaceChanged
		}
	}

	if h.EdgeOrigins, err = st.EdgeOriginCounts(); err != nil {
		return Health{}, err
	}

	return h, nil
}

// Freshness compares the fingerprint that the index records with the
// current one. No recorded fingerprint gives unknown. A recorded one with
// no current fingerprint gives stale, because the index can no longer
// prove that it matches the workspace.
func Freshness(recorded, current string) string {
	// The empty case comes first, so two empty fingerprints never match.
	switch recorded {
	case "":
		return FreshUnknown
	case current:
		return FreshCurrent
	default:
		return FreshStale
	}
}

// ParseCoverage reads the persisted index summary. It returns a known
// Coverage only for a JSON object with integer counts that the indexer
// can write: the seen, parsed, and parse-error counts present, no count
// below zero, and parsed plus skipped plus parse errors not above seen.
// Any other text gives the zero Coverage. A summary that does not pass
// these checks must not print as full coverage.
func ParseCoverage(summary string) Coverage {
	var fields map[string]json.RawMessage

	if json.Unmarshal([]byte(summary), &fields) != nil || fields == nil {
		return Coverage{}
	}

	// count reads one integer field. A required field must be present.
	// A string, a float, a null, or a negative value fails.
	ok := true

	count := func(key string, required bool) int {
		raw, present := fields[key]
		if !present {
			ok = ok && !required

			return 0
		}

		n, err := strconv.Atoi(string(bytes.TrimSpace(raw)))
		if err != nil || n < 0 {
			ok = false

			return 0
		}

		return n
	}

	c := Coverage{
		Known:        true,
		FilesSeen:    count("files_seen", true),
		FilesParsed:  count("files_parsed", true),
		FilesSkipped: count("files_skipped", false),
		FilesDeleted: count("files_deleted", false),
		ParseErrors:  count("parse_errors", true),
	}

	// The parsed, skipped, and failed files are supported files. Seen
	// files also include unsupported files, so the sum cannot be above
	// seen in a summary that the indexer wrote.
	if !ok || !withinSeen(c) {
		return Coverage{}
	}

	// withinSeen has checked that no step of this subtraction goes below
	// zero.
	c.FilesUnsupported = c.FilesSeen - c.FilesParsed - c.FilesSkipped - c.ParseErrors

	return c
}

// withinSeen reports whether parsed plus skipped plus parse errors is not
// above seen. It subtracts each count from seen and does not add the
// counts. A sum near the int limit wraps to a negative value, which is
// never above seen. The caller has checked that no count is below zero.
func withinSeen(c Coverage) bool {
	left := c.FilesSeen

	for _, n := range []int{c.FilesParsed, c.FilesSkipped, c.ParseErrors} {
		if n > left {
			return false
		}

		left -= n
	}

	return true
}

// ParseUnix reads a stored Unix time. Text that is not an integer gives
// 0, which every reader treats as no time.
func ParseUnix(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}

	return n
}

// Age renders the time from ts to now as a coarse age: minutes under one
// hour, hours under 48 hours, else days. A time of 0 or less, or a time
// after now, gives "unknown". The caller passes now, so a test can fix it.
func Age(ts int64, now time.Time) string {
	if ts <= 0 {
		return "unknown"
	}

	// The check compares seconds before time.Unix runs. time.Unix
	// overflows for a time far in the future, and the overflow gives a
	// time in the past, so the age prints the duration limit in days.
	if ts > now.Unix() {
		return "unknown"
	}

	d := now.Sub(time.Unix(ts, 0))

	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// maxOriginName is the most runes that one origin name prints with. An
// origin name comes from the database, so its length has no other bound.
const maxOriginName = 32

// OriginSummary renders the call-edge origins as shares of all CALLS
// edges, largest share first, with names in order for equal counts. Each
// share has one decimal, and the shares sum to 100.0 (see shareTenths).
// With limit above 0, the entries after limit fold into one entry, so a
// database with many origins gives a bounded line. A limit of 0 prints
// every origin. Zero call edges give "none".
func OriginSummary(origins map[string]int, limit int) string {
	calls := 0
	for _, n := range origins {
		calls += n
	}

	if calls == 0 {
		return "none"
	}

	keys := make([]string, 0, len(origins))
	for k := range origins {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if origins[keys[i]] != origins[keys[j]] {
			return origins[keys[i]] > origins[keys[j]]
		}

		return keys[i] < keys[j] // deterministic order for equal counts
	})

	counts := make([]int, len(keys))
	for i, k := range keys {
		counts[i] = origins[k]
	}

	tenths := shareTenths(counts, calls)

	shown := len(keys)
	if limit > 0 && shown > limit {
		shown = limit
	}

	parts := make([]string, 0, shown+1)
	for i, k := range keys[:shown] {
		parts = append(parts, fmt.Sprintf("%d.%d%% %s", tenths[i]/10, tenths[i]%10, originName(k)))
	}

	// The folded entry takes the sum of the folded shares, so the line
	// still sums to 100.0.
	if rest := len(keys) - shown; rest > 0 {
		folded := 0
		for _, n := range tenths[shown:] {
			folded += n
		}

		parts = append(parts, fmt.Sprintf("%d.%d%% in %d more origins", folded/10, folded%10, rest))
	}

	return strings.Join(parts, " · ") + fmt.Sprintf(" (%d calls)", calls)
}

// originName renders one origin name for a terminal. The indexer writes
// only the known names, so they print as they are. The empty name is the
// schema default and is not a known origin, so it prints "(unknown)".
// Any other name comes from a database that seamark did not write. It is
// scrubbed of control sequences and secret-shaped values, and cut to
// maxOriginName runes.
func originName(name string) string {
	switch name {
	case model.OriginQualified, model.OriginSamePackage, model.OriginSameClass,
		model.OriginUniqueName, model.OriginParse:
		return name
	case "":
		return "(unknown)"
	}

	clean := []rune(render.Sanitize(redact.Secrets(name)))
	if len(clean) > maxOriginName {
		clean = append(clean[:maxOriginName-1], '…')
	}

	return string(clean)
}

// shareTenths turns counts into shares in tenths of a percent that sum to
// exactly 1000. Truncation alone loses up to one tenth per origin, so the
// printed line would not sum to 100.0. The largest-remainder method gives
// the lost tenths to the counts with the largest remainders. Equal
// remainders go in slice order, which is the display order, so a share
// never rises along the line. Integer math keeps the result exact on
// every platform. The caller passes counts in display order and their
// sum as total, which must be above zero.
func shareTenths(counts []int, total int) []int {
	tenths := make([]int, len(counts))
	order := make([]int, len(counts))
	given := 0

	for i, n := range counts {
		tenths[i] = n * 1000 / total
		given += tenths[i]
		order[i] = i
	}

	remainder := func(i int) int { return counts[i] * 1000 % total }

	sort.SliceStable(order, func(a, b int) bool {
		return remainder(order[a]) > remainder(order[b])
	})

	for _, i := range order[:1000-given] {
		tenths[i]++
	}

	return tenths
}
