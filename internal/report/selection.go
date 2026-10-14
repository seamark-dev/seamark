package report

import (
	"errors"
	"slices"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// ErrForeignConfig marks a selection request for a config that
// ReadEvidence did not read for. The evidence holds the cluster
// citations of the pins live in its own configs only, so a selection
// with another config can miss a covered cluster.
var ErrForeignConfig = errors.New("report: the lesson evidence was not read for this config")

// Selection is the typed result of one fold. Lessons and HeldBack are
// exactly what the production surface delivers and prints. The other
// fields state where each pin and mined lesson of the evidence went,
// with one exception. change_set keeps only the first pin of each
// identity, so a later pin with that identity is in no list and in no
// count. A selection sample explains a result from these fields, never
// from report text. So a sample must not show that pin as removed or
// excluded.
type Selection struct {
	// Lessons is the selected list in delivery order: Pins, then Mined.
	Lessons []model.Lesson
	// HeldBack is the count that the surface prints. For the edit hook
	// it counts pins only: Restated, OverPinCap, and OverTotal. For
	// change_set and check it counts Restated, OverTotal, and
	// MinedOverTotal. So the count is not "over budget" alone.
	HeldBack int
	// Pins are the selected pins, in the order of Lessons. The hook list
	// can hold one pin identity twice, because the single-file selector
	// keeps a pin that lessons.yaml repeats. The change_set list holds
	// each identity once. So a pairing of two lists needs the identity
	// and its ordinal.
	//
	// The Rank of a pin is its confidence tier only when the index holds
	// an applied proposal row. Without such a row, no annotator runs, and
	// every pin has Rank 0, the value of confidence.TierWeak. So a view
	// must not derive a tier from Rank.
	Pins []reviews.SurfacedPin
	// Mined are the selected mined lessons, in the order of Lessons.
	Mined []model.Lesson
	// Restated holds the pins that the restatement collapse drops. On
	// change_set, a repeat of a pin identity is never in this list. The
	// fold keeps one pin per identity before the collapse.
	Restated []reviews.SurfacedPin
	// OverPinCap holds the pins above the pin cap of the edit hook.
	// change_set has no pin cap, so there it is empty.
	OverPinCap []reviews.SurfacedPin
	// OverTotal holds the pins above the total cap.
	OverTotal []reviews.SurfacedPin
	// MinedOverTotal holds the mined lessons above the total cap.
	MinedOverTotal []model.Lesson
	// Excluded explains each mined cluster that the config keeps out,
	// one entry per cluster, in first-file order. A cluster that an
	// earlier file selects is not an exclusion. The evidence reads at
	// most 100 mined lessons per file, so a lesson past that read is in
	// no list.
	Excluded []Exclusion
}

// Exclusion explains why the config keeps a mined lesson out of a
// selection. It names every reason that applies, and more than one
// reason can apply. The reasons come from the predicates that the
// surfaces apply, so a sample and the surfaces cannot disagree.
type Exclusion struct {
	Lesson model.Lesson
	// Covered is true when an applied pin, live in the config, cites
	// every finding of the cluster.
	Covered bool
	// BelowThreshold is true when the lesson recurs fewer times than
	// the threshold of the config.
	BelowThreshold bool
	// Mutes holds a copy of each entry of Config.Mute that hides the
	// lesson, in the order of Config.Mute.
	Mutes []reviews.MuteRule
}

// HookSelection selects the lessons of one edit-hook injection under
// cfg and its hook budget. Lessons and HeldBack equal the result of
// LessonsForFilesBudget with HookBudget(cfg). cfg must be one of the
// configs of ReadEvidence, else the error is ErrForeignConfig.
func (ev *Evidence) HookSelection(cfg *reviews.Config) (Selection, error) {
	if !ev.readFor(cfg) {
		return Selection{}, ErrForeignConfig
	}

	return ev.hookFold(cfg, HookBudget(cfg)), nil
}

// ChangeSetSelection selects the lessons of one change_set answer or
// one check advisory under cfg and its change budget. Lessons and
// HeldBack equal the result of LessonsForFiles with
// cfg.ChangeSetBudget(). cfg must be one of the configs of
// ReadEvidence, else the error is ErrForeignConfig.
func (ev *Evidence) ChangeSetSelection(cfg *reviews.Config) (Selection, error) {
	if !ev.readFor(cfg) {
		return Selection{}, ErrForeignConfig
	}

	return ev.changeSetFold(cfg, cfg.ChangeSetBudget()), nil
}

// readFor reports whether ReadEvidence read the citations for cfg. It
// compares pointers, because the caller keeps the configs that it
// passes to ReadEvidence. A value comparison of two configs is not
// necessary for that.
func (ev *Evidence) readFor(cfg *reviews.Config) bool {
	return slices.Contains(ev.cfgs, cfg)
}

// cutAt splits items at limit into the kept items and the cut items. A
// limit of zero or less means no cap.
func cutAt[T any](items []T, limit int) (kept, cut []T) {
	if limit <= 0 {
		return items, nil
	}

	return splitAt(items, limit)
}

// splitAt splits items after the first n items. The second part is nil
// when nothing is left, so an empty list of a Selection stays nil.
func splitAt[T any](items []T, n int) (head, tail []T) {
	n = max(n, 0)

	if n >= len(items) {
		return items, nil
	}

	// The head gets no spare capacity. Both parts share one array, so an
	// append to the head must not overwrite the tail.
	return items[:n:n], items[n:]
}
