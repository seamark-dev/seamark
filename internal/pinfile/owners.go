package pinfile

// The owners of a pin key, for the authoring of pins. A pin that a
// person creates, renames, or rescopes must not take a key that another
// live entry or a current proposal already holds. The editor never
// merges two pins, and it never adopts a proposal by its label. Only the
// authoring planner calls these helpers. Plan, collide, the maintenance
// planner, and the Pins screen keep their own key rule, so a retarget
// keeps its Phase 3 behavior.

import (
	"slices"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Owners lists what already holds one pin key.
type Owners struct {
	// Entries are the live entries with the key, in file order.
	Entries []Entry
	// Applied and Pending are the proposal rows that own the key, in the
	// order of the input rows.
	Applied, Pending []model.Proposal
}

// Any reports whether an entry or a proposal row owns the key.
func (o Owners) Any() bool {
	return len(o.Entries) > 0 || len(o.Applied) > 0 || len(o.Pending) > 0
}

// KeyOwners returns the owners of key in d and in the rows. An entry
// owns the key when its key is the key. self names the edited entry,
// which is never its own owner. A row owns the key by rowKeys: its link
// key, and the key of the pin that its apply writes. An applied row owns
// its keys also when its pin is absent from the file, because the
// ledger still records the pin.
//
// KeyOwners trusts d as it is. The caller decides what an unreadable
// file or ledger blocks, because a missing listing is not an absence.
func KeyOwners(d *Document, applied, pending []model.Proposal, key reviews.PinKey, self *EntryRef) Owners {
	var o Owners

	if d != nil {
		for _, e := range d.Entries {
			if e.Key == key && (self == nil || e.Ref() != *self) {
				o.Entries = append(o.Entries, e)
			}
		}
	}

	o.Applied = owning(applied, key)
	o.Pending = owning(pending, key)

	return o
}

// AuthorKeys returns the keys that the applied rows own by the rule of
// KeyOwners, except the rows whose ID is in exclude. An authoring edit
// passes them to Plan as otherApplied. exclude holds the rows linked to
// the edited entry. A note edit of a linked pin then does not collide
// with its own proposal.
func AuthorKeys(applied []model.Proposal, exclude []int64) []reviews.PinKey {
	var out []reviews.PinKey

	for _, p := range applied {
		if !slices.Contains(exclude, p.ID) {
			out = append(out, rowKeys(p)...)
		}
	}

	return out
}

// owning returns the rows of rows that own key.
func owning(rows []model.Proposal, key reviews.PinKey) []model.Proposal {
	var out []model.Proposal

	for _, p := range rows {
		if slices.Contains(rowKeys(p), key) {
			out = append(out, p)
		}
	}

	return out
}

// rowKeys returns the keys that one proposal row owns: its link key
// (reviews.ProposalKey) and the key of the pin that its apply writes
// (RenderedKey). The two differ only for a row whose region fields
// disagree (FieldsAgree). Such a row owns both, because its pin can be
// live under either key.
func rowKeys(p model.Proposal) []reviews.PinKey {
	link, rendered := reviews.ProposalKey(p), RenderedKey(p)
	if link == rendered {
		return []reviews.PinKey{link}
	}

	return []reviews.PinKey{link, rendered}
}
