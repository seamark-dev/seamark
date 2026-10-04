package pinfile

// The link rules between the pins of the file and the proposal rows.
// Every console surface uses Classify, so the Pins view, maintenance, and
// trigger extraction agree on which pin a proposal row describes.

import (
	"errors"
	"slices"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// ErrUnreadable reports that the pin file could not be read, so no row
// is known to be absent. An error is not an absence: a read failure must
// never make an applied row look removed.
var ErrUnreadable = errors.New("unknown: .seamark/lessons.yaml could not be read")

// ProposalKey returns the canonical key of the pin that a proposal row
// describes. It uses the region set that distill.RenderPins writes, so
// an applied row and the pin that its apply wrote share one key.
func ProposalKey(p model.Proposal) reviews.PinKey {
	return reviews.NewPinKey(p.Rule, "", p.RegionSet())
}

// storedKey returns the key from the raw stored region fields. The older
// readers (health, outcome, the CLI) use it.
func storedKey(p model.Proposal) reviews.PinKey {
	return reviews.NewPinKey(p.Rule, p.Region, p.Regions)
}

// FieldsAgree reports whether both key forms of a row are equal. They
// differ for a row whose Regions holds "*" beside real regions, or whose
// Region is not in Regions. No link can then be proven. Extraction calls
// it for pending rows too, so the rule exists only here.
func FieldsAgree(p model.Proposal) bool {
	return storedKey(p) == ProposalKey(p)
}

// Link is the relationship between a pin and the proposal rows. The zero
// value is LinkUnknown, so an unset link never reads as linked.
type Link int

// The relationships. Only LinkLinked allows an automated action.
const (
	// LinkUnknown means that the pin file could not be read.
	LinkUnknown Link = iota
	// LinkLinked means one live entry and one applied row with agreeing
	// region fields share the key.
	LinkLinked
	// LinkNoProposal means that no applied or pending row has the key. It
	// does not prove that a person wrote the pin.
	LinkNoProposal
	// LinkPendingMatch means that no applied row but a pending row has the
	// key. The proposal decision route records it as applied.
	LinkPendingMatch
	// LinkAmbiguousFile means that more than one live entry has the key.
	LinkAmbiguousFile
	// LinkAmbiguousLedger means that more than one applied row has the key.
	LinkAmbiguousLedger
	// LinkAmbiguousFields means that the one matching applied row has
	// stored region fields that disagree with each other.
	LinkAmbiguousFields
	// LinkNotPresent means that no live entry has the key of the row. For
	// a pending row this is the normal state.
	LinkNotPresent
)

// Reason returns a short plain explanation of the link for display.
func (l Link) Reason() string {
	switch l {
	case LinkLinked:
		return "linked to one applied proposal"
	case LinkNoProposal:
		return "no linked proposal"
	case LinkPendingMatch:
		return "matches a pending proposal; record it as applied first"
	case LinkAmbiguousFile:
		return "ambiguous: duplicate pins"
	case LinkAmbiguousLedger:
		return "ambiguous: several applied proposals"
	case LinkAmbiguousFields:
		return "ambiguous: stored region fields disagree"
	case LinkNotPresent:
		return "pin not present"
	default:
		return "pin file unreadable"
	}
}

// EntryLink is the classification of one live entry.
type EntryLink struct {
	Link Link
	// Applied, Pending, and Superseded list every row that matches the
	// entry, so an ambiguous entry can show all of them.
	Applied, Pending, Superseded []model.Proposal
	// NoteChanged is true for a linked entry whose live note differs from
	// the note that the proposal recorded.
	NoteChanged bool
}

// Links is the classification of one document against the ledger.
type Links struct {
	// Entries holds one link per Document.Entries item, in the same order.
	Entries []EntryLink
	// NotPresent lists the applied rows that no live entry carries. It is
	// nil when the file could not be read; NotPresentErr then says so.
	NotPresent    []model.Proposal
	NotPresentErr error
	// ByProposal holds the link of each applied and pending row by ID.
	// Superseded rows are history and have no entry here.
	ByProposal map[int64]Link
}

// Classify links the entries of d to the proposal rows. It is the only
// place of the link rules. An applied row matches an entry by its
// ProposalKey, or by its stored key when its region fields disagree, so
// such a row always shows as ambiguous. A superseded row matches the
// same way, so its history shows on each entry that a reader of either
// key form links. A pending row matches by its ProposalKey only.
//
// A document that Unreadable reports makes every link unknown. The
// entries that still decode keep their matches for display.
func Classify(d *Document, applied, pending, superseded []model.Proposal) *Links {
	links := &Links{ByProposal: map[int64]Link{}}

	var entries []Entry
	if d != nil {
		entries = d.Entries
	}

	unreadable := d.Unreadable()

	count := map[reviews.PinKey]int{}
	for _, e := range entries {
		count[e.Key]++
	}

	links.Entries = make([]EntryLink, len(entries))

	for i, e := range entries {
		el := EntryLink{
			Applied:    matching(applied, e.Key, true),
			Pending:    matching(pending, e.Key, false),
			Superseded: matching(superseded, e.Key, true),
		}

		el.Link = entryLink(count[e.Key], el)
		el.NoteChanged = el.Link == LinkLinked && el.Applied[0].Note != e.Note

		if unreadable {
			el.Link, el.NoteChanged = LinkUnknown, false
		}

		links.Entries[i] = el
	}

	for _, p := range applied {
		link := appliedLink(p, links.Entries)

		if unreadable {
			link = LinkUnknown
		} else if link == LinkNotPresent {
			links.NotPresent = append(links.NotPresent, p)
		}

		links.ByProposal[p.ID] = link
	}

	for _, p := range pending {
		link := LinkNotPresent

		switch {
		case unreadable:
			link = LinkUnknown
		case slices.ContainsFunc(links.Entries, func(el EntryLink) bool { return holds(el.Pending, p.ID) }):
			link = LinkPendingMatch
		}

		links.ByProposal[p.ID] = link
	}

	if unreadable {
		links.NotPresentErr = ErrUnreadable
	}

	return links
}

// matching returns the rows that describe the pin with key. With either
// set, a row with disagreeing region fields also matches by its stored
// key, so the ambiguity shows on the entry that an older reader links.
func matching(rows []model.Proposal, key reviews.PinKey, either bool) []model.Proposal {
	var out []model.Proposal

	for _, p := range rows {
		if ProposalKey(p) == key || (either && storedKey(p) == key) {
			out = append(out, p)
		}
	}

	return out
}

// entryLink applies the link rules to one entry. dups is the number of
// live entries with the key of the entry.
func entryLink(dups int, el EntryLink) Link {
	switch {
	case dups > 1:
		return LinkAmbiguousFile
	case len(el.Applied) > 1:
		return LinkAmbiguousLedger
	case len(el.Applied) == 1 && !FieldsAgree(el.Applied[0]):
		return LinkAmbiguousFields
	case len(el.Applied) == 1:
		return LinkLinked
	case len(el.Pending) > 0:
		return LinkPendingMatch
	}

	return LinkNoProposal
}

// appliedLink returns the link of one applied row. A row that no entry
// lists is not present. A row with disagreeing region fields is
// ambiguous wherever it matches; any other row takes the link of its
// entry.
func appliedLink(p model.Proposal, entries []EntryLink) Link {
	i := slices.IndexFunc(entries, func(el EntryLink) bool { return holds(el.Applied, p.ID) })

	switch {
	case i < 0:
		return LinkNotPresent
	case !FieldsAgree(p):
		return LinkAmbiguousFields
	}

	return entries[i].Link
}

// holds reports whether rows has a row with the ID.
func holds(rows []model.Proposal, id int64) bool {
	return slices.ContainsFunc(rows, func(p model.Proposal) bool { return p.ID == id })
}
