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

// FieldsAgree reports whether distill.RenderPins writes the pin that
// reviews.ProposalKey names for a row. RenderPins writes the region set
// of model.Proposal.RegionSet, and the key reads the stored fields.
// Both give one key, except for a row whose fields disagree:
//
//   - Regions holds "" or "*" next to a real region in Region or
//     Regions. RegionSet then gives repository-wide, and the key keeps
//     the real regions.
//   - Regions is not empty, and Region names a region that Regions does
//     not hold. RegionSet ignores Region, and the key adds it.
//
// No code path writes such a row, but an old or hand-edited ledger can
// hold one. No link and no absence can then be proven, so such a row
// reads LinkAmbiguousFields (Classify). Extraction and the record of an
// existing pin (proposals.PreviewReconcile) call this predicate too, so
// the rule exists only here.
func FieldsAgree(p model.Proposal) bool {
	return RenderedKey(p) == reviews.ProposalKey(p)
}

// RenderedKey returns the key of the pin that distill.RenderPins writes
// for the row p: its rule and its region set (model.Proposal.RegionSet).
// The raw Region and Regions fields can disagree with that set, so the
// readers of the file link a row by reviews.ProposalKey instead. Both
// keys are equal when FieldsAgree is true. The apply of internal/proposals
// uses this key too, so the rule exists only here.
func RenderedKey(p model.Proposal) reviews.PinKey {
	return reviews.NewPinKey(p.Rule, "", p.RegionSet())
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
	// key, and the region fields of every such pending row agree. The
	// proposal decision route records it as applied.
	LinkPendingMatch
	// LinkAmbiguousFile means that more than one live entry has the key.
	LinkAmbiguousFile
	// LinkAmbiguousLedger means that more than one applied row has the key.
	LinkAmbiguousLedger
	// LinkAmbiguousFields means that the one matching applied row, or a
	// matching pending row, has stored region fields that disagree with
	// each other (FieldsAgree). Such a row reads it too, also when no
	// entry has its key, because the pin that its apply wrote can be
	// live under another key.
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
	// nil when the file could not be read; NotPresentErr then says so. A
	// row whose region fields disagree is listed with the link
	// LinkAmbiguousFields, so every applied row is in Entries or here.
	NotPresent    []model.Proposal
	NotPresentErr error
	// ByProposal holds the link of each applied and pending row by ID.
	// Superseded rows are history and have no entry here.
	ByProposal map[int64]Link
}

// Classify links the entries of d to the proposal rows. It is the only
// place of the link rules. A row of any status matches the entry whose
// key is its reviews.ProposalKey. The CLI, health, and outcome readers
// use the same key, so every surface links a row to the same pin. A row
// whose region fields disagree reads LinkAmbiguousFields wherever it is
// listed, so no surface claims a link or an absence for it.
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
			Applied:    matching(applied, e.Key),
			Pending:    matching(pending, e.Key),
			Superseded: matching(superseded, e.Key),
		}

		el.Link = entryLink(count[e.Key], el)
		el.NoteChanged = el.Link == LinkLinked && el.Applied[0].Note != e.Note

		if unreadable {
			el.Link, el.NoteChanged = LinkUnknown, false
		}

		links.Entries[i] = el
	}

	for _, p := range applied {
		i := slices.IndexFunc(links.Entries, func(el EntryLink) bool { return holds(el.Applied, p.ID) })

		switch {
		case unreadable:
			links.ByProposal[p.ID] = LinkUnknown
		case i < 0:
			links.NotPresent = append(links.NotPresent, p)
			links.ByProposal[p.ID] = rowLink(p, LinkNotPresent)
		default:
			links.ByProposal[p.ID] = rowLink(p, links.Entries[i].Link)
		}
	}

	for _, p := range pending {
		link := LinkNotPresent
		if slices.ContainsFunc(links.Entries, func(el EntryLink) bool { return holds(el.Pending, p.ID) }) {
			link = LinkPendingMatch
		}

		if unreadable {
			link = LinkUnknown
		}

		links.ByProposal[p.ID] = rowLink(p, link)
	}

	if unreadable {
		links.NotPresentErr = ErrUnreadable
	}

	return links
}

// matching returns the rows that link to the pin with key.
func matching(rows []model.Proposal, key reviews.PinKey) []model.Proposal {
	var out []model.Proposal

	for _, p := range rows {
		if reviews.ProposalKey(p) == key {
			out = append(out, p)
		}
	}

	return out
}

// entryLink applies the link rules to one entry. dups is the number of
// live entries with the key of the entry. A pending row with
// disagreeing region fields makes the entry ambiguous, because the
// record of an existing pin refuses such a row (FieldsAgree).
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
	case slices.ContainsFunc(el.Pending, fieldsDisagree):
		return LinkAmbiguousFields
	case len(el.Pending) > 0:
		return LinkPendingMatch
	}

	return LinkNoProposal
}

// rowLink returns the link of one row from the link that its entry, or
// its absence, gives. A row with disagreeing region fields is ambiguous
// wherever it is listed. Its apply writes a pin with another key, so a
// missing entry with its own key does not prove an absence.
func rowLink(p model.Proposal, link Link) Link {
	if link != LinkUnknown && !FieldsAgree(p) {
		return LinkAmbiguousFields
	}

	return link
}

// fieldsDisagree negates FieldsAgree, for slices.ContainsFunc.
func fieldsDisagree(p model.Proposal) bool {
	return !FieldsAgree(p)
}

// holds reports whether rows has a row with the ID.
func holds(rows []model.Proposal, id int64) bool {
	return slices.ContainsFunc(rows, func(p model.Proposal) bool { return p.ID == id })
}
