package maintain

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// Inspection is one snapshot of the installed pins and their relation to
// the ledger. Every field is a read; nothing in it selects an action.
type Inspection struct {
	// Doc is the read of the pin file; nil when DocErr is set.
	Doc *pinfile.Document
	// DocErr is a read failure of the file itself: a path that is not a
	// regular file, or an I/O error. The listing is then empty.
	DocErr error
	// Links is the classification of the entries against the ledger. It
	// is nil when the ledger cannot be read.
	Links *pinfile.Links
	// LinkErr says why no link is known: pinfile.ErrUnreadable for a pin
	// file that cannot be read, or an error that wraps ErrNoLedger. It is
	// nil when every link is known.
	LinkErr error
	// Pins holds one item per Doc.Entries item, in file order.
	Pins []Pin
	// Superseded is the read-only history of pruned proposals, in
	// ascending ID order.
	Superseded []model.Proposal
	// Clusters groups the pins whose wording restates each other, by
	// index in Pins. Only present, linked pins take part.
	Clusters [][]int
	// LedgerErr is the read failure of the ledger; ReadingsErr joins the
	// read failures of the readings.
	LedgerErr, ReadingsErr error
	// Index is the index probe that the readings used.
	Index workspace.IndexProbe
	// Database holds the answers of the console service about the index
	// database. Inspect leaves it zero, and the service fills it, because
	// the answers compare the database with the workspace.
	Database workspace.DatabaseFacts
	// At is the time that the readings used.
	At time.Time
	// Checks is the lesson-check reading of the pin file. It is zero when
	// the file gives no listing.
	Checks ChecksReading
}

// Pin is one live entry with its link and readings.
type Pin struct {
	Entry    pinfile.Entry
	Link     pinfile.EntryLink
	Readings Readings
	// Check is the lesson-check reading of the entry.
	Check PinCheck
}

// ChecksReading is the lesson-check reading of the pin file. Inspect
// decodes the bytes that it listed, so the console shows the mode and
// the predicate of each pin without a second read of the file. The
// definitions and the error texts hold file data, so a caller sanitizes
// them before display.
type ChecksReading struct {
	// Uses is true when the file uses a lesson-check key: a checks map,
	// a check or enforce mode, or a check reference. Doctor and status
	// print their lesson-check lines only then, and the console follows
	// the same rule.
	Uses bool
	// SHA256 is the hex digest of the decoded bytes. The capability
	// report carries the digest of its own read, so a caller can tell a
	// report of other bytes.
	SHA256 string
	// Defs holds the check definitions by ID, as the file holds them.
	Defs map[string]reviews.CheckDef
	// Errors are the semantic errors of the file (tier 2).
	Errors []lessoncheck.ConfigError
}

// IDs returns the check IDs that the file defines, in sorted order.
func (c ChecksReading) IDs() []string {
	ids := make([]string, 0, len(c.Defs))
	for id := range c.Defs {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids
}

// PinCheck is the lesson-check reading of one entry. It holds what the
// pin requests and the predicate of its check. What the installed hooks
// make of that request is the capability report, which the console
// joins by Ref.
type PinCheck struct {
	// Known is false when the entry does not pair with the decoded pin
	// list. A caller then shows no lesson-check fact of the entry.
	Known bool
	// Ref and Version are the pin-ref and the pin version.
	Ref, Version string
	// Mode is the normalized mode. An unknown mode reads advisory, and
	// Errors then holds its error.
	Mode lessoncheck.Mode
	// Check is the bound check, or nil without one. Its Kind and Version
	// are empty when the ID names no definition.
	Check *lessoncheck.CheckRef
	// Predicate is the lessoncheck.Describe text of the check, or "" when
	// no checker can describe it.
	Predicate string
	// Scope is the checker scope (AllRegions, sorted). Empty means
	// repository-wide.
	Scope []string
	// Listing is the listing of an advisory pin: not-checkable without a
	// check, advisory-with-check with one. It is "" for a check or an
	// enforce pin.
	Listing lessoncheck.Listing
	// Errors are the semantic errors of this entry.
	Errors []lessoncheck.ConfigError
}

// Readings are the derived readings of one pin. Each one says whether it
// is known, and an unknown one carries the reason. Missing input is not a
// negative result. A reading is advice: it never selects or runs an
// action. A reason can quote a store error, so a caller sanitizes it
// before display. Confidence, Scope, Trigger and Outcome read the linked
// proposal, so a pin without exactly one linked proposal has only the
// Exposure reading.
type Readings struct {
	Confidence ConfidenceReading
	Scope      ScopeReading
	Trigger    TriggerReading
	Outcome    OutcomeReading
	Exposure   ExposureReading
	// Similar holds the indexes in Inspection.Pins of the other pins of
	// the same redundancy cluster. Similar wording does not prove the
	// same coverage, so a similar pin is no reason to prune. A prune
	// plan decides its items first and then only lists the similar pins
	// that stay (PlanItem.Kept).
	Similar []int
}

// ConfidenceReading is the evidence tier of the linked proposal, as the
// proposal health computes it.
type ConfidenceReading struct {
	Known  bool
	Reason string
	Tier   confidence.Tier
	Facts  confidence.Facts
	// Living holds the cited findings that are still in the index. They
	// are metadata: the finding bodies are not read.
	//
	// A cited finding leaves the index when it ages out of the mining
	// window, when a later commit reverts its fix, or when a re-mine no
	// longer finds it. The proposal still cites it, but it is not living.
	// The members of the proposal keep its ID, so a caller can name each
	// cited finding that Living does not hold.
	Living []model.Finding
}

// ScopeReading is the scope that today's evidence computes for the
// linked proposal (distill.RecommendScope). It needs an indexed
// database, because the shared region engine reads the history. Without
// a living cited finding the advice is distill.ScopeNoEvidence, because
// missing evidence must never widen a pin.
type ScopeReading struct {
	Known  bool
	Reason string
	Advice distill.ScopeAdvice
}

// TriggerReading is the trigger question state of the linked proposal.
// The stored trigger paths passed the path checks of the extraction
// before the store saved them, so the reading shows them as facts.
type TriggerReading struct {
	Known  bool
	Reason string
	// Open is true when an extraction can ask the question.
	Open  bool
	State distill.TriggerReason
	// Blocked holds the confirmed triggers that the region set does not
	// deliver. They come from the scope reading, so they are empty while
	// that reading is unknown.
	Blocked []distill.TriggerFact
}

// OutcomeReading is the passive outcome reading of the linked proposal.
// It is a derived recurrence check. It counts the mistake events before
// and after the first delivery, so it shows a correlation and never
// proves a cause. No plan reads it, so no verdict selects a prune: a
// working verdict validates the pin (outcome.VerdictWorking).
type OutcomeReading struct {
	Known   bool
	Reason  string
	Reading outcome.Reading
}

// ExposureReading is the delivery history of the live pin identity. It
// exists for every pin, linked or not, because the firing log keys on
// the pin text.
//
// The identity covers the rule, the note, and the regions
// (reviews.PinIdentity). A retarget changes the regions, so it starts a
// new history. The firings of the old regions stay with the old
// identity. Exposure holds the delivery count and the first delivery.
// A known reading with Fired false is a fact, not an unknown: the log
// was read and holds no delivery of this identity.
type ExposureReading struct {
	Known    bool
	Reason   string
	Exposure reviews.Exposure
	// Fired is true when the firing log holds a delivery of the identity.
	Fired bool
}

// Inspect reads the pin file under root and relates it to the ledger in
// st. It computes the readings once, with the clock value now. st can be
// nil, and a ledger that cannot be read leaves the pins listed with
// unknown links. Inspect writes nothing: no file, no ledger row, and no
// firing record.
func Inspect(root string, st *store.Store, probe workspace.IndexProbe, now time.Time) *Inspection {
	in := &Inspection{Index: probe, At: now}

	in.Doc, in.DocErr = pinfile.Read(root)

	var entries []pinfile.Entry
	if in.Doc != nil {
		entries = in.Doc.Entries
	}

	in.Pins = make([]Pin, len(entries))
	for i, e := range entries {
		in.Pins[i].Entry = e
	}

	in.readChecks()

	rows, err := readLedger(st)
	if err != nil {
		in.LedgerErr = err
	} else {
		in.Superseded = rows.superseded
		in.Links = pinfile.Classify(in.Doc, rows.applied, rows.pending, rows.superseded)

		for i := range in.Pins {
			in.Pins[i].Link = in.Links.Entries[i]
		}
	}

	// A file that cannot be read is the stronger reason: without it no
	// pin is known to be present or absent, whatever the ledger says.
	switch {
	case in.Doc.Unreadable():
		in.LinkErr = pinfile.ErrUnreadable
	case in.LedgerErr != nil:
		in.LinkErr = in.LedgerErr
	}

	var errs []error

	// One read of the firing log serves the exposure and the outcome
	// readings. Agent hooks append to the log while the console is open,
	// so two reads could give one pin two delivery histories (D-23).
	audit := firingLog{}
	if audit.firings, audit.err = reviews.ReadFirings(root); audit.err != nil {
		errs = append(errs, fmt.Errorf("read the firing log: %w", audit.err))
	}

	in.readExposure(audit)

	if in.LinkErr == nil {
		in.Clusters = clusters(in.Pins)
		errs = append(errs, in.readLinked(root, st, now, audit)...)
	} else {
		for i := range in.Pins {
			in.Pins[i].Readings.unknown(in.LinkErr.Error())
		}
	}

	in.ReadingsErr = errors.Join(errs...)

	return in
}

// readChecks decodes the listed bytes with the strict check tier and
// fills the lesson-check readings. A file without a reliable listing
// gives no reading, because no entry is then known to pair with a pin.
//
// An entry pairs with the pin at the same position: the listing skips
// null items, as the decoder does (checkImpact). The key must match too,
// so a listing that differs from the decode never shows the facts of
// another pin.
func (in *Inspection) readChecks() {
	if in.Doc.Unreadable() {
		return
	}

	cfg, errs, err := lessoncheck.Parse(in.Doc.Bytes)
	if err != nil {
		return
	}

	in.Checks = ChecksReading{
		Uses: lessoncheck.UsesLessonChecks(cfg, errs), SHA256: cfg.SHA256, Defs: cfg.Raw.Checks, Errors: errs,
	}

	for i := range in.Pins {
		if i < len(cfg.Pins) && cfg.Pins[i].Key == in.Pins[i].Entry.Key {
			in.Pins[i].Check = pinCheckOf(cfg, cfg.Pins[i], errs)
		}
	}
}

// pinCheckOf builds the reading of one decoded pin. The check reference
// copies the definition version (reviews.CheckVersion), so it names the
// same version as the verification and the capability report.
func pinCheckOf(cfg *lessoncheck.Config, pin lessoncheck.Pin, errs []lessoncheck.ConfigError) PinCheck {
	c := PinCheck{Known: true, Ref: pin.Ref, Version: pin.Version, Mode: pin.Mode, Scope: pin.Scope}

	subject := "pin " + strconv.Itoa(pin.Entry)
	for _, e := range errs {
		if e.Subject == subject {
			c.Errors = append(c.Errors, e)
		}
	}

	if pin.CheckID != "" {
		ref := lessoncheck.CheckRef{ID: pin.CheckID}

		if def, ok := cfg.Raw.Checks[pin.CheckID]; ok {
			ref.Kind, ref.Version = lessoncheck.Kind(def.Kind), reviews.CheckVersion(def)

			if text, err := lessoncheck.Describe(def); err == nil {
				c.Predicate = text
			}
		}

		c.Check = &ref
	}

	switch {
	case pin.Mode != lessoncheck.ModeAdvisory:
	case pin.CheckID == "":
		c.Listing = lessoncheck.ListingNotCheckable
	default:
		c.Listing = lessoncheck.ListingAdvisoryWithCheck
	}

	return c
}

// unknown marks every proposal reading of r unknown for reason. The
// exposure reading stays, because it needs no proposal.
func (r *Readings) unknown(reason string) {
	r.Confidence = ConfidenceReading{Reason: reason}
	r.Scope = ScopeReading{Reason: reason}
	r.Trigger = TriggerReading{Reason: reason}
	r.Outcome = OutcomeReading{Reason: reason}
}

// reasonNoFiringLog is the reason of the exposure and outcome readings
// when the firing log cannot be read.
const reasonNoFiringLog = "unknown: the firing log could not be read"

// firingLog is the one read of the firing log in a snapshot: the
// firings, or the read failure.
type firingLog struct {
	firings []reviews.Firing
	err     error
}

// readExposure looks up every live pin identity in the firing log. The
// log is read once by the caller and never appended to.
func (in *Inspection) readExposure(audit firingLog) {
	if audit.err != nil {
		for i := range in.Pins {
			in.Pins[i].Readings.Exposure = ExposureReading{Reason: reasonNoFiringLog}
		}

		return
	}

	exposure := reviews.FirstFirings(audit.firings)

	for i := range in.Pins {
		exp, fired := exposure[reviews.PinIdentity(pinRule(in.Pins[i].Entry))]
		in.Pins[i].Readings.Exposure = ExposureReading{Known: true, Exposure: exp, Fired: fired}
	}
}

// readLinked computes the proposal readings of the linked pins. Every
// other pin reads its link reason, because no single proposal describes
// it.
func (in *Inspection) readLinked(root string, st *store.Store, now time.Time, audit firingLog) []error {
	var (
		linked []int
		rows   []model.Proposal
	)

	for i := range in.Pins {
		if in.Pins[i].Link.Link != pinfile.LinkLinked {
			in.Pins[i].Readings.unknown(in.Pins[i].Link.Link.Reason())

			continue
		}

		linked = append(linked, i)
		rows = append(rows, in.Pins[i].Link.Applied[0])
	}

	if len(linked) == 0 {
		return nil
	}

	var errs []error

	meta, err := st.FindingsMetaByIDs(members(rows))
	if err != nil {
		for _, i := range linked {
			in.Pins[i].Readings.unknown("unknown: the cited findings could not be read")
		}

		return []error{fmt.Errorf("read the cited findings: %w", err)}
	}

	for k, i := range linked {
		if err := in.readOne(&in.Pins[i].Readings, root, st, rows[k], meta, now); err != nil {
			errs = append(errs, err)
		}
	}

	return append(errs, in.readOutcomes(st, linked, rows, audit)...)
}

// readOne computes the confidence, trigger, and scope readings of one
// linked row.
func (in *Inspection) readOne(r *Readings, root string, st *store.Store, row model.Proposal,
	meta map[int64]model.Finding, now time.Time,
) error {
	living := livingOf(row, meta)
	tier, facts := confidence.Assess(row, meta, root, now)
	r.Confidence = ConfidenceReading{Known: true, Tier: tier, Facts: facts, Living: living}

	open, state := distill.TriggerQuestionOpen(row)
	r.Trigger = TriggerReading{Known: true, Open: open, State: state}

	if in.Index.State != workspace.IndexReady {
		r.Scope = ScopeReading{Reason: noIndexReason(in.Index)}

		return nil
	}

	advice, err := distill.RecommendScope(st, root, row, living)
	if err != nil {
		r.Scope = ScopeReading{Reason: "unknown: the scope could not be computed"}

		return fmt.Errorf("compute the scope of p%d: %w", row.ID, err)
	}

	r.Scope = ScopeReading{Known: true, Advice: advice}

	for _, f := range advice.Facts {
		if f.BlockedLine(row.Status) != "" {
			r.Trigger.Blocked = append(r.Trigger.Blocked, f)
		}
	}

	return nil
}

// readOutcomes runs the outcome engine once over the linked rows, with
// the firing log of the snapshot. Each linked row has exactly one live
// entry with its key, so the engine reads the right pin.
func (in *Inspection) readOutcomes(st *store.Store, linked []int, rows []model.Proposal, audit firingLog) []error {
	unknown := func(reason string) {
		for _, i := range linked {
			in.Pins[i].Readings.Outcome = OutcomeReading{Reason: reason}
		}
	}

	// Inspect already joined the read failure of the log once.
	if audit.err != nil {
		unknown(reasonNoFiringLog)

		return nil
	}

	// Config is set here: pinfile leaves it nil only with Doc.ReaderErr,
	// and the linked readings run only when that error is nil. The check
	// keeps a change of that rule from giving a zero outcome.
	cfg := in.Doc.Config
	if cfg == nil {
		unknown(pinfile.ErrUnreadable.Error())

		return []error{in.Doc.ReaderErr}
	}

	readings, err := outcome.Gather(st, cfg, rows, audit.firings)
	if err != nil {
		unknown("unknown: the outcome could not be measured")

		return []error{fmt.Errorf("measure the outcomes: %w", err)}
	}

	for k, i := range linked {
		if reading, ok := readings[rows[k].ID]; ok {
			in.Pins[i].Readings.Outcome = OutcomeReading{Known: true, Reading: reading}
		} else {
			in.Pins[i].Readings.Outcome = OutcomeReading{Reason: "no reading: the proposal cites no finding"}
		}
	}

	return nil
}

// noIndexReason explains why no scope advice exists without a ready
// index: the advice reads the co-change history of an index run.
func noIndexReason(probe workspace.IndexProbe) string {
	return fmt.Sprintf("unknown: scope advice needs an indexed database (the index is %s); "+
		"build it with `seamark index`", probe.State)
}

// clusters groups the present, linked pins whose wording restates each
// other. Each pin enters with its live rule, note, and regions and with
// the evidence identity of its proposal, so a missing pin or an unlinked
// pin never counts as a retained alternative.
func clusters(pins []Pin) [][]int {
	var (
		ps  []model.Proposal
		idx = map[int64]int{}
	)

	for i, p := range pins {
		if p.Link.Link != pinfile.LinkLinked {
			continue
		}

		row := p.Link.Applied[0]
		idx[row.ID] = i
		ps = append(ps, model.Proposal{
			ID: row.ID, Signature: row.Signature, Members: row.Members,
			Rule: p.Entry.Rule, Note: p.Entry.Note, Regions: pinRule(p.Entry).AllRegions(),
		})
	}

	var out [][]int

	for _, group := range distill.Clusters(ps) {
		ids := make([]int, len(group))
		for k, p := range group {
			ids[k] = idx[p.ID]
		}

		out = append(out, ids)
	}

	for _, group := range out {
		for _, i := range group {
			for _, j := range group {
				if j != i {
					pins[i].Readings.Similar = append(pins[i].Readings.Similar, j)
				}
			}
		}
	}

	return out
}
