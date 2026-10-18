package report

import (
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Evidence is one read of the index for the lesson selection of a set
// of files. A fold applies a config and a budget rule to it without a
// store read, so two configs can select from one observation. The edit
// hook, change_set, and check select through it, so a sample that uses
// it selects as they do.
//
// Evidence is not safe for concurrent use: a fold fills the confidence
// memo.
type Evidence struct {
	// files are the files of the selection, in the order of the request.
	// A tie in the pin order keeps this order.
	files []string
	// cfgs are the configs that ReadEvidence read the citations for.
	// HookSelection and ChangeSetSelection refuse another config, because
	// its live pins can have no citations here.
	cfgs []*reviews.Config
	// applied holds the applied proposals, newest ID first.
	applied []model.Proposal
	// citations holds the cluster citation counts of each applied
	// proposal, by proposal ID. ReadEvidence reads them only for a row
	// with members whose pin is live in one of its configs.
	citations map[int64]map[string]store.ClusterCited
	// assessor gives the confidence verdict of a pin. It is nil when no
	// applied proposal exists. Then the fold has no annotator and every
	// pin ranks 0, as in the single-file selector.
	assessor *pinAssessor
	// mined holds the mined lessons of each file at threshold 1, in the
	// order of files. The fold applies the threshold and the mutes.
	mined [][]model.Lesson
}

// errNoConfig marks a call of ReadEvidence without a usable config. The
// citation read depends on the configs, so a read without one cannot
// serve a fold.
var errNoConfig = errors.New("report: lesson evidence needs at least one non-nil config")

// ReadEvidence reads the lesson evidence of files once: the applied
// proposals, the cluster citations, the finding metadata, the recorded
// repository root, one time stamp, and the mined lessons of each file.
// It writes nothing.
//
// A fold must use one of cfgs. The citations are read only for the
// applied proposals whose pin is live in one of cfgs, because the hook
// reads them on every edit. A fold with another config can miss a
// covered cluster, so HookSelection and ChangeSetSelection refuse it
// with ErrForeignConfig.
func ReadEvidence(st *store.Store, files []string, cfgs ...*reviews.Config) (*Evidence, error) {
	if len(cfgs) == 0 || slices.Contains(cfgs, nil) {
		return nil, errNoConfig
	}

	// One time stamp for every confidence verdict: the ages of one
	// evidence read must agree, whatever fold asks first.
	now := time.Now()

	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return nil, err
	}

	citations, err := readCitations(st, applied, cfgs...)
	if err != nil {
		return nil, err
	}

	assessor, err := newPinAssessor(st, applied, now)
	if err != nil {
		return nil, err
	}

	mined := make([][]model.Lesson, len(files))

	for i, f := range files {
		// Threshold 1 reads every lesson of the region. The fold applies
		// the threshold of its config, so two configs can differ there.
		mined[i], err = st.LessonsForFile(f, 1, 100)
		if err != nil {
			return nil, err
		}
	}

	return &Evidence{
		files:     slices.Clone(files),
		cfgs:      slices.Clone(cfgs),
		applied:   applied,
		citations: citations,
		assessor:  assessor,
		mined:     mined,
	}, nil
}

// readCitations reads the cluster citation counts of each applied
// proposal with members whose pin is live in one of cfgs. A row with no
// live pin cannot cover a cluster, so its counts are not read.
func readCitations(st *store.Store, applied []model.Proposal, cfgs ...*reviews.Config,
) (map[int64]map[string]store.ClusterCited, error) {
	citations := map[int64]map[string]store.ClusterCited{}

	for _, p := range applied {
		live := slices.ContainsFunc(cfgs, func(cfg *reviews.Config) bool { return pinLive(cfg, p) })
		if !live || len(p.Members) == 0 {
			continue
		}

		counts, err := st.ClusterCitation(p.Members)
		if err != nil {
			return nil, err
		}

		citations[p.ID] = counts
	}

	return citations, nil
}

// coveredBy returns the mined clusters that an applied pin live in cfg
// fully cites. coveredClusters states the rule. A row whose counts were
// not read covers nothing.
func coveredBy(cfg *reviews.Config, applied []model.Proposal,
	citations map[int64]map[string]store.ClusterCited,
) map[string]bool {
	// The rule judges each pin alone, never a union of citations. Two
	// pins that each cite half of a cluster capture nothing: neither pin
	// carries the theme, and a union would hide an open recurrence.
	covered := map[string]bool{}

	for _, p := range applied {
		if !pinLive(cfg, p) || len(p.Members) == 0 {
			continue
		}

		for key, c := range citations[p.ID] {
			if c.Cited == c.Total {
				covered[key] = true
			}
		}
	}

	return covered
}

// pinVerdict is the raw confidence verdict of one applied pin. The
// display note depends on the surface, so the memo keeps the tier and
// the facts, and each fold formats the note.
type pinVerdict struct {
	tier  confidence.Tier
	facts confidence.Facts
}

// note formats the verdict for a surface. An ambient surface (the hook,
// change_set, check) names weak evidence only, to keep the injected
// context small. A deliberate view names every tier with its facts.
func (v pinVerdict) note(ambient bool) string {
	switch {
	case ambient && v.tier == confidence.TierWeak:
		return "weak evidence: " + v.facts.Line()
	case !ambient:
		return v.tier.String() + ": " + v.facts.Line()
	default:
		return ""
	}
}

// pinAssessor gives the confidence verdict of a pin from one read of
// the applied proposals and their findings (RFC-002 §6).
type pinAssessor struct {
	// byKey maps a pin key to its applied proposal. For two rows with one
	// key, the lowest ID wins, because the rows arrive newest first.
	byKey map[reviews.PinKey]model.Proposal
	meta  map[int64]model.Finding
	root  string
	now   time.Time
	// memo keeps one verdict per pin key. Assess stats the cited paths,
	// and one read must not probe the disk again for the same answer.
	memo map[reviews.PinKey]pinVerdict
}

// newPinAssessor reads what the confidence verdicts need. It returns nil
// when no applied proposal exists: then no pin carries a rank or a
// note, and the selection keeps the file order of equal pins.
func newPinAssessor(st *store.Store, applied []model.Proposal, now time.Time) (*pinAssessor, error) {
	byKey := make(map[reviews.PinKey]model.Proposal, len(applied))

	var cited []int64

	for _, p := range applied {
		byKey[reviews.ProposalKey(p)] = p
		cited = append(cited, p.Members...)
	}

	if len(byKey) == 0 {
		return nil, nil
	}

	meta, err := st.FindingsMetaByIDs(cited)
	if err != nil {
		return nil, err
	}

	// A missing root only turns off the dead-path check, so a read
	// error of this value is not a selection error.
	root, _ := st.GetMeta(store.MetaRepoRoot)

	return &pinAssessor{
		byKey: byKey,
		meta:  meta,
		root:  root,
		now:   now,
		memo:  map[reviews.PinKey]pinVerdict{},
	}, nil
}

// verdict returns the confidence verdict of the pin with key. The
// boolean is false for a pin without an applied proposal, for example a
// pin written by hand.
func (a *pinAssessor) verdict(key reviews.PinKey) (pinVerdict, bool) {
	p, applied := a.byKey[key]
	if !applied {
		return pinVerdict{}, false
	}

	if v, done := a.memo[key]; done {
		return v, true
	}

	tier, facts := confidence.Assess(p, a.meta, a.root, a.now)
	v := pinVerdict{tier: tier, facts: facts}
	a.memo[key] = v

	return v, true
}

// annotator returns the PinAnnotator of one surface. A pin without an
// applied proposal has no history to doubt, so it ranks strong and
// carries no note. A nil assessor returns a nil annotator, so every pin
// ranks equally.
func (a *pinAssessor) annotator(ambient bool) reviews.PinAnnotator {
	if a == nil {
		return nil
	}

	return func(pin reviews.PinRule) (int, string) {
		v, ok := a.verdict(reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions))
		if !ok {
			return int(confidence.TierStrong), ""
		}

		return int(v.tier), v.note(ambient)
	}
}

// fileUnion is the lesson material for a set of files before any
// budget applies. (*Evidence).union builds it, and hookFold and
// changeSetFold apply their own budget rule to it. So the surfaces
// cannot disagree about the material.
type fileUnion struct {
	// pins holds each pin identity once, ranked across the whole set.
	// union collapses no restatement: each fold has its own collapse
	// rule. changeSetFold reads this list.
	pins []reviews.SurfacedPin
	// everyPin is pins plus the pins that repeat an identity inside
	// lessons.yaml. The single-file selector keeps such a repeat, so
	// hookFold reads this list to select the same lessons.
	everyPin []reviews.SurfacedPin
	// mined holds the mined lessons that clear the surfacing bar, one
	// per cluster, in first-file order.
	mined []model.Lesson
	// excluded explains each mined cluster that the config keeps out,
	// one entry per cluster, in first-file order. Both folds copy it,
	// because no budget rule changes it.
	excluded []Exclusion
}

// pinSlot identifies one lessons.yaml pin across the files of a union.
// A pin identity can occur twice in lessons.yaml, for example with two
// notes. ordinal counts the earlier pins of the same identity in one
// file's list. Equal identities share rank and depth, so the stable
// order of SurfacePins gives one pin the same ordinal for every file.
type pinSlot struct {
	key     reviews.PinKey
	ordinal int
}

// union merges the pins and the mined lessons of the evidence files
// under cfg. ambient selects the note style of the confidence verdicts.
func (ev *Evidence) union(cfg *reviews.Config, ambient bool) fileUnion {
	covered := coveredBy(cfg, ev.applied, ev.citations)
	annotate := ev.assessor.annotator(ambient)

	// Pins: union by slot, keeping each pin's best appearance
	// (highest rank, then deepest match) across the files.
	best := map[pinSlot]reviews.SurfacedPin{}

	var order []pinSlot

	for _, f := range ev.files {
		seen := map[reviews.PinKey]int{}

		for _, sp := range cfg.SurfacePins(f, annotate) {
			key := reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)
			slot := pinSlot{key: key, ordinal: seen[key]}
			seen[key]++

			cur, ok := best[slot]
			if !ok {
				best[slot] = sp
				order = append(order, slot)

				continue
			}

			if sp.Rank > cur.Rank || (sp.Rank == cur.Rank && sp.Depth > cur.Depth) {
				best[slot] = sp
			}
		}
	}

	// Stable: a stable sort of the full list and a filter afterwards
	// give the order that a sort of the filtered list gives.
	sort.SliceStable(order, func(i, j int) bool {
		a, b := best[order[i]], best[order[j]]
		if a.Rank != b.Rank {
			return a.Rank > b.Rank
		}

		return a.Depth > b.Depth
	})

	pins := make([]reviews.SurfacedPin, 0, len(order))
	everyPin := make([]reviews.SurfacedPin, 0, len(order))

	for _, slot := range order {
		everyPin = append(everyPin, best[slot])

		if slot.ordinal == 0 {
			pins = append(pins, best[slot])
		}
	}

	// Mined recurrence: the per-file reads, deduplicated by cluster,
	// filtered exactly as the single-file surface filters.
	seenCluster := map[string]bool{}
	// The excluded clusters have their own set. So an exclusion never
	// stops a later file from selecting its cluster.
	excludedCluster := map[string]bool{}

	var (
		mined    []model.Lesson
		excluded []Exclusion
	)

	for _, ls := range ev.mined {
		for _, l := range ls {
			if seenCluster[l.ClusterKey] {
				continue
			}

			if covered[l.ClusterKey] || !cfg.SurfacesMined(l) {
				if !excludedCluster[l.ClusterKey] {
					excludedCluster[l.ClusterKey] = true
					excluded = append(excluded, exclusionOf(cfg, l, covered[l.ClusterKey]))
				}

				continue
			}

			seenCluster[l.ClusterKey] = true
			mined = append(mined, l)
		}
	}

	return fileUnion{pins: pins, everyPin: everyPin, mined: mined, excluded: excluded}
}

// exclusionOf states every reason that keeps the mined lesson l out of a
// selection under cfg. The reasons come from the predicates of the
// filter in union: the coverage map, BelowThreshold, and MuteMatches. A mute entry is copied by value, so the exclusion names
// the entry and holds no reference into cfg.
func exclusionOf(cfg *reviews.Config, l model.Lesson, covered bool) Exclusion {
	ex := Exclusion{
		Lesson:         l,
		Covered:        covered,
		BelowThreshold: cfg.BelowThreshold(l),
	}

	for _, i := range cfg.MuteMatches(l) {
		ex.Mutes = append(ex.Mutes, cfg.Mute[i])
	}

	return ex
}
