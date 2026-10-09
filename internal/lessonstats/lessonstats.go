// Package lessonstats gathers lesson delivery statistics as typed data
// for `seamark lessons --stats` and the console. It only reads: it never
// appends a firing record, because viewing a lesson is not delivery.
package lessonstats

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// ErrNoIndex reports that a section needs the index and none is
// readable. The firing totals do not need the index, so they still
// render.
var ErrNoIndex = errors.New("no readable index")

// Snapshot is one read of the delivery statistics. Each source keeps
// its own error, so a failed source never renders as zero.
type Snapshot struct {
	// LogPath is the firing log path.
	LogPath string
	// LogExists is false when no firing log exists yet.
	LogExists bool
	// Records is the number of parsed firing records.
	Records int
	// Skipped is the number of log lines that the reader could not
	// parse. A log of only corrupt lines is then not shown as empty.
	Skipped int
	// LogErr holds the log read failure; Summary is then empty.
	LogErr error
	// Summary is the existing delivery aggregation. Ranked has a stable
	// order: ties break on region, then symptom.
	Summary reviews.Summary
	// Eligible is true when NeverFired covers the mined lessons. It is
	// false without an index or with an unreadable lessons config.
	Eligible bool
	// EligibleErr explains why NeverFired is incomplete.
	EligibleErr error
	// Applied holds the applied proposals, in ascending ID order.
	Applied []model.Proposal
	// Readings holds the derived recurrence readings by proposal ID.
	Readings map[int64]outcome.Reading
	// OutcomeErr holds the reading failure.
	OutcomeErr error

	// Lessons counts the active lessons by source. It is nil when
	// LessonsErr is set, and in a snapshot that no Gather filled.
	Lessons *LessonCounts
	// LessonsErr explains why Lessons is nil: no index, an unreadable
	// lessons config, or a failed read of the mined lessons.
	LessonsErr error

	// Days holds the delivered records of each local day, oldest first.
	// It covers DeliveryDays days and ends on the day of the read. It is
	// nil when the log was not read.
	Days []DayCount
	// Undated counts the delivered records whose time does not parse.
	// Days cannot place them, so the view says that they are missing.
	Undated int

	// A section error can hold the failure of one of several sources.
	// Err must name the first failed source, so these fields keep the
	// source failures that a section error can hide.
	minedErr   error
	appliedErr error
	configErr  error
}

// Err returns the first failed source, or nil when every read worked.
// The order is the mined lessons, the applied proposals, the lessons
// config, the firing log, and the outcome measurement. `seamark lessons
// --stats` read its sources in this order before it used Gather, so its
// error text stays the same. Without an index, Err never returns nil,
// because the outcomes need the proposal ledger.
func (s Snapshot) Err() error {
	for _, err := range []error{s.minedErr, s.appliedErr, s.configErr, s.LogErr, s.OutcomeErr} {
		if err != nil {
			return err
		}
	}

	return nil
}

// Gather reads the statistics. st may be nil when no index exists; the
// firing totals still render and the eligibility sections say why they
// are incomplete.
func Gather(st *store.Store, root string) Snapshot {
	return gatherAt(st, root, time.Now())
}

// gatherAt is Gather with a fixed clock, so tests can place the day
// counts. now sets the last day of Days and its time zone.
func gatherAt(st *store.Store, root string, now time.Time) Snapshot {
	snap := Snapshot{LogPath: reviews.AuditLogPath(root)}

	// A malformed config is the reader's error, never defaults: defaults
	// would judge the firings against rules the agent never received.
	cfg, cfgErr := reviews.LoadConfig(root)
	if cfgErr != nil {
		cfgErr = fmt.Errorf("failed to load lessons config: %w", cfgErr)
	}

	snap.configErr = cfgErr

	// The mined lessons are read once, because the lesson counts and the
	// eligibility sections both need them.
	var (
		mined    []model.Lesson
		minedErr error
	)

	if st != nil {
		mined, minedErr = st.AllLessons(0)
		if minedErr != nil {
			minedErr = fmt.Errorf("failed to fetch mined lessons: %w", minedErr)
		}
	}

	snap.minedErr = minedErr

	// The lesson counts do not need the delivery log, so a log failure
	// does not hide them.
	snap.Lessons, snap.LessonsErr = lessonCounts(st != nil, mined, minedErr, cfg, cfgErr)

	// ReadFirings returns no records for a missing log and for an empty
	// log. The stat keeps "no recorded delivery" apart from zero counts.
	_, statErr := os.Stat(snap.LogPath)
	snap.LogExists = statErr == nil

	firings, skipped, err := reviews.ReadFiringsCounted(root)
	if err != nil {
		// Partial records would render as low counts. The error is the
		// truthful state, so every firing-based section reports it.
		snap.LogErr = fmt.Errorf("failed to read firing log: %w", err)
		snap.EligibleErr = snap.LogErr
		snap.OutcomeErr = snap.LogErr

		if st != nil {
			snap.Applied, snap.appliedErr = appliedProposals(st)
		}

		return snap
	}

	// A hook can create the log between the stat and the read.
	snap.LogExists = snap.LogExists || len(firings) > 0
	snap.Records = len(firings)
	snap.Skipped = skipped
	snap.Days, snap.Undated = DailyCounts(firings, now)

	if st == nil {
		snap.Summary = summarize(firings, nil)
		snap.EligibleErr = fmt.Errorf("%w: eligibility needs the mined lessons", ErrNoIndex)
		snap.OutcomeErr = fmt.Errorf("%w: pin outcomes need the proposal ledger", ErrNoIndex)

		return snap
	}

	snap.Summary, snap.Eligible, snap.EligibleErr = eligibleSummary(mined, minedErr, cfg, cfgErr, firings)
	snap.Applied, snap.appliedErr = appliedProposals(st)

	switch {
	case snap.appliedErr != nil:
		snap.OutcomeErr = snap.appliedErr
	case cfgErr != nil:
		snap.OutcomeErr = cfgErr
	default:
		// The proposal health measures outcomes with the same function.
		// Gather gives it the records that it already read, so the
		// summary and the outcomes come from one read of the log.
		snap.Readings, snap.OutcomeErr = proposals.MeasureOutcomes(st, cfg, NewestFirst(snap.Applied), firings)
	}

	return snap
}

// eligibleSummary aggregates the firings against the lessons that could
// fire. Without that set, it still aggregates the firings and reports
// why NeverFired is incomplete.
func eligibleSummary(mined []model.Lesson, minedErr error, cfg *reviews.Config, cfgErr error,
	firings []reviews.Firing,
) (reviews.Summary, bool, error) {
	if cfgErr != nil {
		return summarize(firings, nil), false, cfgErr
	}

	if minedErr != nil {
		return summarize(firings, nil), false, minedErr
	}

	// The set that COULD fire: what the config surfaces repo-wide.
	return summarize(firings, cfg.Surface(mined, "")), true, nil
}

// appliedProposals returns the applied proposals in ascending ID order.
func appliedProposals(st *store.Store) ([]model.Proposal, error) {
	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch applied proposals: %w", err)
	}

	sort.Slice(applied, func(i, j int) bool { return applied[i].ID < applied[j].ID })

	return applied, nil
}

// NewestFirst returns a copy of the proposals in descending ID order,
// the order of the store. `seamark lessons --stats` printed and measured
// the applied proposals in this order before it used Gather. It still
// prints in this order. Gather also measures in this order. The
// measurement stops at the first failed commit count and names that
// pin, so the command error names the same pin.
func NewestFirst(applied []model.Proposal) []model.Proposal {
	out := slices.Clone(applied)

	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })

	return out
}

// summarize runs the existing aggregation and makes the Ranked order
// total. Summarize breaks ties in map order, so a refresh could reorder
// equal rows and move a selection that follows the row position.
func summarize(firings []reviews.Firing, surfaced []model.Lesson) reviews.Summary {
	s := reviews.Summarize(firings, surfaced)

	sort.SliceStable(s.Ranked, func(i, j int) bool {
		a, b := s.Ranked[i], s.Ranked[j]

		switch {
		case a.Matches != b.Matches:
			return a.Matches > b.Matches
		case a.Count != b.Count:
			return a.Count > b.Count
		case a.LastTS != b.LastTS:
			return a.LastTS > b.LastTS
		case a.Region != b.Region:
			return a.Region < b.Region
		default:
			return a.Symptom < b.Symptom
		}
	})

	return s
}
