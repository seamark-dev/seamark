package maintain

// The authoring of pins. A person creates a pin, edits an unlinked pin,
// edits the note, mode, or check of a linked pin, or removes an
// unlinked pin in the console. PlanAuthor computes the candidate file and every
// reason that blocks it. CommitAuthor plans again from fresh
// observations and writes the one file only when nothing changed.
//
// Authoring writes no ledger row. A new pin has no proposal, a removal
// supersedes nothing, and a note edit keeps the recorded note of its
// proposal. So the commit has no second write to compensate, and it
// keeps no Recovery. Its outcome is the outcome of one file write.
// PlanScope reuses this planner for a manual scope edit, and Commit
// writes that plan, because such an edit also changes the ledger.
//
// The plan reuses the rules of the other editors, so no rule exists
// twice. pinfile.Classify gives the relationship, and pinfile.KeyOwners
// gives the owners of a key. The pinfile planners compute the candidate.
// readGate reads distill.write, and readLedger reads the rows.
// integration.Landed reads the outcome of a failed write. The checks of
// typed text are reviews.CheckAuthoredText and reviews.CheckPinRegion.
// An edit can also set the mode and the check of a pin. checkImpact
// gives the impact block, and the strict check tier of lessoncheck
// refuses an invalid candidate before the save.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// AuthorOp is an authoring operation. The zero value is not an
// operation, so an unset request never changes a pin.
type AuthorOp int

// The authoring operations.
const (
	// AuthorCreate adds a new pin after the last entry of the list, so
	// the earlier pins keep their priority.
	AuthorCreate AuthorOp = iota + 1
	// AuthorEdit changes the fields of one entry in place.
	AuthorEdit
	// AuthorRemove deletes one unlinked entry and its provenance run.
	AuthorRemove
)

// String returns the lower-case name of the operation.
func (o AuthorOp) String() string {
	switch o {
	case AuthorCreate:
		return "create"
	case AuthorEdit:
		return "edit"
	case AuthorRemove:
		return "remove"
	default:
		return "AuthorOp(" + strconv.Itoa(int(o)) + ")"
	}
}

// ScopeChoice is the scope that a person chose for a pin. The zero value
// is no choice, because a blank form must never read as repository-wide.
type ScopeChoice int

// The scope choices.
const (
	// ScopeUnset means that the form holds no choice yet.
	ScopeUnset ScopeChoice = iota
	// ScopeRepoWide delivers the pin for every file.
	ScopeRepoWide
	// ScopeRegions delivers the pin for the regions of the request.
	ScopeRegions
)

// AuthorRequest is one authoring draft. A create uses every field. An
// edit sets only the fields that the person changed, and the planner
// compares each set value with the entry byte for byte. A removal uses
// only Ref.
type AuthorRequest struct {
	Op AuthorOp
	// Ref binds an edit or a removal to the entry that the person saw.
	Ref pinfile.EntryRef
	// Rule and Note are the typed texts.
	Rule, Note string
	// Scope and Regions are the typed scope. Regions holds the rows as
	// the person typed them; the check normalizes them.
	Scope   ScopeChoice
	Regions []string
	// Mode and Check are the lesson-check keys of an edit. Mode advisory
	// removes the mode key, because an absent mode reads as advisory. An
	// empty Check removes the check key. A new pin is advisory, so a
	// create with a mode or a check gets an issue.
	Mode  lessoncheck.Mode
	Check string
	// RuleSet, NoteSet, ScopeSet, ModeSet, and CheckSet mark the fields
	// of an edit that the person changed. A create ignores them.
	RuleSet, NoteSet, ScopeSet, ModeSet, CheckSet bool
}

// AuthorField names a field of the authoring form.
type AuthorField int

// The form fields.
const (
	FieldRule AuthorField = iota + 1
	FieldNote
	FieldScope
	FieldRegions
	FieldMode
	FieldCheck
)

// String returns the lower-case name of the field.
func (f AuthorField) String() string {
	switch f {
	case FieldRule:
		return "rule"
	case FieldNote:
		return "note"
	case FieldScope:
		return "scope"
	case FieldRegions:
		return "regions"
	case FieldMode:
		return "mode"
	case FieldCheck:
		return "check"
	default:
		return "AuthorField(" + strconv.Itoa(int(f)) + ")"
	}
}

// FieldIssue is one verdict of CheckDraft about one field. The text can
// quote typed text, so a caller sanitizes it before display.
type FieldIssue struct {
	Field AuthorField
	// Index is the 0-based row of a region issue. It is -1 for an issue
	// of a whole field.
	Index int
	Text  string
	// Info marks a line that explains a normalization of a region, such
	// as a removed trailing "/". It never blocks the draft.
	Info bool
}

// BlockKind is the cause of an authoring blocker. A screen chooses its
// route key by the kind.
type BlockKind int

// The blocker kinds, in the order of a plan.
const (
	// BlockFile means that the pin file cannot be planned or replaced.
	// Causes are its structure, CRLF line breaks, a link, its mode, a
	// hard link, and a refusal of the planner.
	BlockFile BlockKind = iota + 1
	// BlockEntry means that the reviewed entry changed or is gone.
	BlockEntry
	// BlockRelation means that the link to the ledger forbids the
	// request: a linked pin, a pending match, or an ambiguous link.
	BlockRelation
	// BlockOwner means that another pin or proposal already holds the
	// new key.
	BlockOwner
	// BlockLedger means that no readable ledger gives the relationship.
	BlockLedger
	// BlockForeignRoot means that the index records another root.
	BlockForeignRoot
	// BlockGate means that distill.write is off or cannot be read.
	BlockGate
	// BlockCheck means that the candidate fails the strict check tier,
	// for example a mode enforce without a check. The kind comes last, so
	// the earlier kinds keep their values. A plan lists it before the
	// ledger, root, and gate blockers.
	BlockCheck
)

// AuthorBlocker is one reason that a plan cannot be confirmed. Route
// names the way forward, or is empty when Text names it already. The
// text can quote file, ledger, or path data, so a caller sanitizes it.
type AuthorBlocker struct {
	Kind  BlockKind
	Text  string
	Route string
}

// String returns the reason with its route, as one line.
func (b AuthorBlocker) String() string {
	if b.Route == "" {
		return b.Text
	}

	return b.Text + "; " + b.Route
}

// AuthorPlan is a reviewed authoring operation. It is complete even when
// it cannot be confirmed, so a review can show the exact change and
// every blocker. Some fields hold file, ledger, or typed data: Before,
// After, Diff, Old, New, the owners, the rows, and Missing. A caller
// sanitizes them before display.
type AuthorPlan struct {
	Request AuthorRequest
	// Root is the root of the pin file. Guard is the observation of the
	// file that the plan read.
	Root  string
	Guard integration.FileGuard
	// Before is the file content; After is the verified candidate, or
	// nil when no candidate exists.
	Before, After []byte
	// Diff is the redacted display diff; Added and Removed count lines.
	Diff           string
	Added, Removed int
	// Old is the entry as the plan read it. New is the entry that After
	// holds, or the requested pin when no candidate exists. Old is zero
	// for a create, and New is zero for a removal.
	Old, New reviews.PinRule
	// Link is the relationship of the entry. It is meaningful only when
	// LinksKnown is true, and it is zero for a create.
	Link       pinfile.Link
	LinksKnown bool
	// EntryApplied and EntryPending are the IDs of the rows that match
	// the entry, in ascending order.
	EntryApplied, EntryPending []int64
	// Proposal and ProposalGuard are the linked row of a linked entry.
	Proposal      *model.Proposal
	ProposalGuard *store.RowGuard
	// OldOwners hold the key of the entry; NewOwners hold the new key.
	// The edited entry is never its own owner.
	OldOwners, NewOwners pinfile.Owners
	// KeyChanged and IdentityChanged compare the old and the new pin of
	// an edit: the link key, and the firing identity of the deliveries.
	KeyChanged, IdentityChanged bool
	// Write is distill.write at plan time; GateErr is its read failure.
	Write   bool
	GateErr error
	// NoOp is true for an edit whose fields all decode to the values of
	// the entry. Such a draft offers no write.
	NoOp bool
	// Issues are the verdicts of CheckDraft.
	Issues []FieldIssue
	// Blockers list every reason that the plan cannot be confirmed.
	Blockers []AuthorBlocker
	// Notes are the plain statements of the review.
	Notes []string
	// History holds the superseded and dismissed rows of the key. They
	// are history, never the source of the pin.
	History []model.Proposal
	// Missing lists the typed regions that name no file or directory
	// under Root yet. It is information, never a blocker.
	Missing []string
	// NewRef binds the new or edited entry in After; nil otherwise.
	NewRef *pinfile.EntryRef
	// Impact is the checker impact of an edit, shown before the save. It
	// holds the block of the edited pin when the pin is check or enforce
	// before or after the edit. It is empty otherwise, also for a create,
	// because a new pin is advisory.
	Impact []lessoncheck.Impact
	// Confirmable is true when nothing blocks the write.
	Confirmable bool
}

// WriteOutcome says what the pin file holds after an authoring commit
// (P4-18). The zero value is NotWritten, so a refused commit reads as
// "nothing written".
type WriteOutcome int

// The commit outcomes.
const (
	// NotWritten means that the file does not hold the planned bytes.
	NotWritten WriteOutcome = iota
	// Written means that the file holds the planned bytes.
	Written
	// Unknown means that the write failed and the file cannot be read
	// again. The caller must not report "written" or "not written".
	Unknown
)

// String returns the name of the outcome.
func (o WriteOutcome) String() string {
	switch o {
	case NotWritten:
		return "not written"
	case Written:
		return "written"
	case Unknown:
		return "unknown"
	default:
		return "WriteOutcome(" + strconv.Itoa(int(o)) + ")"
	}
}

// AuthorResult is the outcome of an authoring commit.
type AuthorResult struct {
	Outcome WriteOutcome
	// Err is the refusal or the write failure. It can hold file paths,
	// so a caller sanitizes it before display.
	Err error
	// NewRef binds the new or edited entry when the outcome is Written.
	NewRef *pinfile.EntryRef
}

// StaleError is the ErrStale of an authoring commit. It names each
// observation that changed since the review, in plain words, so the
// review can show them before the person reviews again. A difference can
// quote a blocker with file, ledger, or path text, so a caller sanitizes
// it before display.
type StaleError struct {
	Differences []string
}

// Error returns ErrStale with the differences.
func (e *StaleError) Error() string {
	return ErrStale.Error() + ": " + strings.Join(e.Differences, "; ")
}

// Unwrap returns ErrStale, so errors.Is matches it.
func (e *StaleError) Unwrap() error {
	return ErrStale
}

// AuthorWriter is the pin file boundary of an authoring commit. The
// default writes through pinfile.PrepareAppend for a create, else
// pinfile.Prepare, and then Apply. Tests replace it to inject a failure.
type AuthorWriter interface {
	// Write writes after over the file that expect observed. create
	// allows an absent file. An error that wraps ErrStale,
	// pinfile.ErrStale, or pinfile.ErrNotWritable means that nothing was
	// written. Every other error is an error of the write step.
	Write(reg *integration.Registry, root string, expect integration.FileGuard, after []byte, create bool) error
}

// The fixed texts of the issues.
const (
	issueScope       = "choose repository-wide or these regions"
	issueNoRegion    = "add at least one region, or choose repository-wide"
	issueRepoRegions = "repository-wide takes no regions; remove them or choose these regions"
	issueNoNote      = "a new pin needs a note"
	issueBlankNote   = "the note holds only white space; clear it or write guidance"
	issueBareRule    = "a pin without a note needs a rule"
	issueMode        = "choose advisory, check, or enforce"
	issueCreateCheck = "a new pin starts advisory; set its mode and check in an edit after the save"
)

// The fixed texts and routes of the blockers. A blocker can also quote a
// pinfile, store, or engine error. routeByHand and reasonLedgerFixes are
// in plan.go, because the maintenance plan uses them too.
const (
	routeIndex    = "build the index in Overview, then review again"
	routeOwnIndex = "build the index of this workspace in Overview, then review again"
	// routeLinkedPin names the rule only, because the scope preview now
	// owns the regions of a linked pin. It names no maintenance key:
	// MAINTENANCE in Pins gives the route that fits the state of the pin.
	routeLinkedPin  = "press r in Pins, then read the route for its rule under MAINTENANCE"
	routePrune      = "use - Prune, which also marks the proposal superseded"
	routeRecord     = "Proposals › c records it as applied"
	routeOwner      = "choose another rule or scope, or decide the proposal in Proposals"
	routeOwnerScope = "choose other regions, or decide the proposal in Proposals"
	routeScopeLink  = "press esc, then i, and review the draft again"
	// routeScopeFile replaces the hand route of a scope plan. A region
	// change by hand unlinks the pin, so the route tells the person to
	// fix the file and to keep the regions out of that fix.
	routeScopeFile = "fix what blocks .seamark/lessons.yaml and leave the regions of this pin as they are, " +
		"then press i to open the draft again; a region change by hand unlinks the pin"
	routeRefresh   = "review the entry in Pins again"
	textFileBlock  = "the pin file cannot be changed: "
	textEntryGone  = "the pin changed since the draft started"
	textLinkedNote = "linked to proposal #%d; only its note, mode, check, and regions can change here"
	// textLinkedScope has no route, because the text names the way.
	textLinkedScope = "linked to proposal #%d; a change of its regions needs the scope preview"
	textLinkedOnly  = "linked to proposal #%d; Remove is only for unlinked pins"
	textOwnedBy     = "the rule and scope are already held by "
	textGateUnread  = "distill.write cannot be read"
	routeGateUnread = "fix .seamark/config.yaml first"
	textGateOff     = "distill.write is off: this preview changes nothing"
	routeGateOff    = "turn on Settings › Pin writing and preview again"
	routeCheckDraft = "choose a check that checks defines, or the advisory mode"
)

// The review texts of an authoring plan (contracts §5). B2 shows them as
// they are.
const (
	textIdentityChanged = "Earlier deliveries stay recorded under the earlier wording and scope. " +
		"Nothing is reset or moved. The new wording is a new delivery identity: a context that " +
		"already saw the old wording can receive it again, also in once-per-context mode."
	textIdentityLinked = "Proposal #%d measures its outcome by the live wording, so its Outcome reads " +
		"'untested' until the new wording is delivered."
	textIdentityKept = "The delivery identity does not change. Stats can still list the old and new " +
		"spelling as separate rows."
	// textLinkedEdited holds the sentences that a note edit and a scoped plan
	// share, so the two texts cannot differ on the 'edited' mark.
	textLinkedEdited = "Proposal #%d keeps its recorded rule, note, and evidence. Pins shows the " +
		"difference as 'edited'. Trigger extraction still asks about the recorded note."
	textLinkedKeeps = textLinkedEdited + " Saving does not extract triggers or retarget."
	// The two texts below replace textIdentityKept and textLinkedKeeps for
	// an edit of the mode or the check only. Such an edit changes no
	// spelling and no note, so the texts of a wording edit would claim
	// effects that it does not have.
	textChecksKept = "The delivery identity does not change. This edit changes only the %s of the pin; its " +
		"rule, note, and scope stay as they are."
	textLinkedChecks = "Proposal #%d keeps its recorded rule, note, and evidence. This edit leaves the note as " +
		"it is, and Pins marks a pin 'edited' only for a note difference. Saving does not extract triggers " +
		"or retarget."
	// textVersionChanged follows a text that keeps the delivery identity, when
	// the pin version still changes. A mode, a check, or a note case edit
	// keeps the identity, and the once-per-context state follows the version.
	textVersionChanged = "The pin gets a new version, so a context that already saw it can receive it " +
		"again, also in once-per-context mode."
	textLedgerEffect = "The proposal ledger does not change."
	// The three texts below serve a scope plan, which writes the ledger. A
	// new order keeps the key, so the spelling words of textIdentityKept
	// would state something false. A scope plan changes the regions, so
	// the last words of textLinkedKeeps would state something false too.
	textScopeLedger = "The proposal ledger records the new regions and that a person chose them. " +
		"Its rule, note, and evidence stay."
	textScopeOrder = "The delivery identity does not change, because the region set stays. Only its order changes."
	textScopeKeeps = textLinkedEdited + " Saving does not extract triggers."
	noteNoOp       = "the draft changes nothing"
	noteCreateLast = "the new pin goes after the last pin, so the earlier pins keep their order"
)

// The differences of a stale authoring commit.
const (
	diffExistence = "file existence"
	diffBytes     = "file bytes"
	diffMode      = "file mode"
	diffLink      = "symbolic link"
	diffAccess    = "write access"
	diffFile      = "file observation"
	diffContent   = "planned file content"
	diffRoot      = "workspace root"
	diffGate      = "distill.write"
	diffLedger    = "the proposal ledger cannot be read"
	diffRows      = "the proposal rows that match the pin"
	diffConfirm   = "the plan can no longer be confirmed"
)

// errNotStarted marks an error of the default writer before the engine
// write: the file is then unchanged, so the commit reads nothing back.
var errNotStarted = errors.New("the write did not start")

// gateBlocker returns the blocker of the write gate, or nil when the gate
// is on. The maintenance plan states the same sentence (finish), so one
// cause has one text.
func gateBlocker(write bool, gateErr error) *AuthorBlocker {
	switch {
	case gateErr != nil:
		return &AuthorBlocker{Kind: BlockGate, Text: textGateUnread, Route: routeGateUnread}
	case !write:
		return &AuthorBlocker{Kind: BlockGate, Text: textGateOff, Route: routeGateOff}
	}

	return nil
}

// CheckDraft checks the typed fields of an authoring draft. The form
// shows the verdicts live, and PlanAuthor fills AuthorPlan.Issues with
// the same verdicts. base is the decoded entry of an edit, or nil.
//
// A create needs a rule, a nonblank note, and a scope choice. An edit
// checks a rule or a note only when it sets it and the value differs
// from base. So a legacy rule or note that the person did not edit never
// blocks the draft. An edit can clear the note while the rule stays
// nonblank. A removal has no field to check.
//
// A set scope replaces the whole region list, so the check covers every
// region row. A scope equal to base is checked too. The entry stores its
// scope in two keys, region and regions, so a typed list has no one
// stored text to compare with. A legacy region that CheckPinRegion
// refuses thus blocks every scope edit of its entry.
//
// A set mode must be one of the three mode words. A create with a mode
// other than advisory or with a check gets an issue, because a new pin
// is advisory and the create never drops a field without a word. The
// strict check tier on the candidate decides the rest, for example a
// check that checks does not define (PlanAuthor, BlockCheck).
func CheckDraft(req AuthorRequest, base *reviews.PinRule) []FieldIssue {
	issues, _ := checkDraft(req, base)

	return issues
}

// checkDraft returns the verdicts of CheckDraft and the normalized
// region set of the draft. The set is empty and not nil for a
// repository-wide choice, and nil when the draft sets no scope.
func checkDraft(req AuthorRequest, base *reviews.PinRule) (issues []FieldIssue, set []string) {
	if req.Op != AuthorCreate && req.Op != AuthorEdit {
		return nil, nil
	}

	var c draftCheck

	create := req.Op == AuthorCreate
	changed := func(set bool, value string, old func(reviews.PinRule) string) bool {
		return create || (set && (base == nil || value != old(*base)))
	}

	if changed(req.RuleSet, req.Rule, func(p reviews.PinRule) string { return p.Rule }) {
		c.text(FieldRule, reviews.TextPinRule, req.Rule)
	}

	if changed(req.NoteSet, req.Note, func(p reviews.PinRule) string { return p.Note }) {
		c.note(req, base)
	}

	if create || req.ScopeSet {
		set = c.scope(req)
	}

	c.checkKeys(req)

	return c.issues, set
}

// draftCheck collects the verdicts of one draft.
type draftCheck struct {
	issues []FieldIssue
}

// add records one verdict.
func (c *draftCheck) add(field AuthorField, index int, text string, info bool) {
	c.issues = append(c.issues, FieldIssue{Field: field, Index: index, Text: text, Info: info})
}

// text checks one typed text with the shared text check.
func (c *draftCheck) text(field AuthorField, kind reviews.TextKind, s string) {
	if err := reviews.CheckAuthoredText(kind, s); err != nil {
		c.add(field, -1, err.Error(), false)
	}
}

// note checks a note that the draft sets. A create needs guidance. An
// edit can clear the note, but a pin needs a rule or a note to say
// anything. A note of only white space is neither: it changes the
// delivery identity and shows no guidance.
func (c *draftCheck) note(req AuthorRequest, base *reviews.PinRule) {
	blank := strings.TrimSpace(req.Note) == ""

	switch {
	case req.Op == AuthorCreate && blank:
		c.add(FieldNote, -1, issueNoNote, false)
	case req.Note == "":
		rule, known := req.Rule, req.RuleSet
		if !known && base != nil {
			rule, known = base.Rule, true
		}

		if known && strings.TrimSpace(rule) == "" {
			c.add(FieldNote, -1, issueBareRule, false)
		}
	case blank:
		c.add(FieldNote, -1, issueBlankNote, false)
	default:
		c.text(FieldNote, reviews.TextPinNote, req.Note)
	}
}

// scope checks the scope choice and each region, and returns the
// normalized set. A repeat is found by the region form of the pin key.
// The key reads "./api", "api/", "api//", and "api" as one region, so
// the file must not hold two of them.
func (c *draftCheck) scope(req AuthorRequest) []string {
	switch req.Scope {
	case ScopeRepoWide:
		if len(req.Regions) > 0 {
			c.add(FieldRegions, -1, issueRepoRegions, false)
		}

		return []string{}
	case ScopeRegions:
	default:
		c.add(FieldScope, -1, issueScope, false)

		return nil
	}

	if len(req.Regions) == 0 {
		c.add(FieldRegions, -1, issueNoRegion, false)
	}

	set := []string{}
	first := map[string]int{}

	for i, raw := range req.Regions {
		in, err := reviews.CheckPinRegion(raw)
		if err != nil {
			c.add(FieldRegions, i, err.Error(), false)

			continue
		}

		for _, n := range in.Notes {
			c.add(FieldRegions, i, n, true)
		}

		// CheckPinRegion removes one trailing "/", and the key removes
		// one more. So the key, not the checked value, decides a repeat.
		key := reviews.NewPinKey("", in.Value, nil).Region
		if j, ok := first[key]; ok {
			c.add(FieldRegions, i, fmt.Sprintf("region %d repeats region %d", i+1, j+1), false)

			continue
		}

		first[key] = i
		set = append(set, in.Value)
	}

	return set
}

// checkKeys checks the lesson-check keys of a draft (CheckDraft).
func (c *draftCheck) checkKeys(req AuthorRequest) {
	switch {
	case req.Op == AuthorCreate:
		if req.Mode != "" && req.Mode != lessoncheck.ModeAdvisory {
			c.add(FieldMode, -1, issueCreateCheck, false)
		}

		if req.Check != "" {
			c.add(FieldCheck, -1, issueCreateCheck, false)
		}
	case req.ModeSet:
		if _, err := ParseMode(string(req.Mode)); err != nil {
			c.add(FieldMode, -1, issueMode, false)
		}
	}
}

// Blocking reports whether an issue blocks the draft: an issue blocks
// unless it is Info. The form and the planner share this rule.
func Blocking(issues []FieldIssue) bool {
	return slices.ContainsFunc(issues, func(i FieldIssue) bool { return !i.Info })
}

// PlanAuthor computes the authoring operation of req from the current
// pin file, write gate, and ledger. It writes nothing. It returns an
// error only for an invalid request; every blocker and every issue goes
// into the plan.
//
// A missing ledger, an unreadable listing, or a foreign root gives a
// blocker with its route (P4-8, P4-16). The plan then still holds the
// candidate, the diff, and the identity. A draft with a blocking issue
// gets no candidate, because the issue names the fix.
func PlanAuthor(d Deps, req AuthorRequest) (*AuthorPlan, error) {
	a, err := planAuthoring(d, req, false)
	if err != nil {
		return nil, err
	}

	return a.plan, nil
}

// planAuthoring runs the authoring planner. scoped is true for PlanScope:
// a region change of a linked pin then passes, because Commit writes the
// ledger row with it. PlanAuthor passes false, so CommitAuthor can never
// write a region change that leaves the row behind.
func planAuthoring(d Deps, req AuthorRequest, scoped bool) (*authoring, error) {
	switch {
	case req.Op < AuthorCreate || req.Op > AuthorRemove:
		return nil, fmt.Errorf("maintain: unknown authoring operation %s", req.Op)
	case req.Op != AuthorCreate && req.Ref.Key == (reviews.PinKey{}):
		return nil, ErrNothingSelected
	case d.Root == "":
		return nil, errors.New("maintain: workspace root is empty")
	}

	a := &authoring{deps: d, req: req, scoped: scoped, plan: &AuthorPlan{Request: req, Root: d.Root}}
	a.plan.Write, a.plan.GateErr = readGate(d.Root)

	a.read()
	a.readLedger()
	a.bind()
	a.draft()
	a.relation()
	a.owner()

	if err := a.candidate(); err != nil {
		return nil, err
	}

	a.finish()

	return a, nil
}

// authoring holds the state of one PlanAuthor or PlanScope call.
type authoring struct {
	deps Deps
	req  AuthorRequest
	plan *AuthorPlan
	// scoped is true for a manual scope edit (planAuthoring).
	scoped bool

	doc *pinfile.Document
	// structural is true when the editing gate of pinfile refuses the
	// file. Every planner then refuses with the same reason, so the plan
	// shows the file blocker once and runs no planner.
	structural bool

	rows      ledgerRows
	dismissed []model.Proposal
	ledgerErr error

	// entry is the bound entry of an edit or a removal, or nil.
	entry *pinfile.Entry
	// link is the classification of entry while the links are known.
	link pinfile.EntryLink
	// change is the candidate of an edit, which a scope plan commits.
	change *pinfile.Change
	// set is the normalized region set of the draft (checkDraft).
	set []string
	// drafted is true when New, the keys, and the owners are computed.
	drafted bool
	newKey  reviews.PinKey
}

// block records one blocker.
func (a *authoring) block(kind BlockKind, text, route string) {
	a.plan.Blockers = append(a.plan.Blockers, AuthorBlocker{Kind: kind, Text: text, Route: route})
}

// blocked reports whether a blocker of one of the kinds exists.
func (a *authoring) blocked(kinds ...BlockKind) bool {
	return slices.ContainsFunc(a.plan.Blockers, func(b AuthorBlocker) bool { return slices.Contains(kinds, b.Kind) })
}

// read reads the pin file and records what blocks its replacement. A
// read failure is a blocker, not an error, so the review can say why.
func (a *authoring) read() {
	p := a.plan

	doc, err := pinfile.Read(a.deps.Root)
	if err != nil {
		a.block(BlockFile, textFileBlock+err.Error(), routeByHand)

		return
	}

	a.doc, p.Guard, p.Before = doc, doc.Guard, doc.Bytes

	// Read fills FileBlock with the structural gate or the refusal of the
	// engine. Only the gate stops the planners, and CRLF line breaks are
	// part of it (P4-6). Parse returns the gate alone, so the plan asks
	// Parse and does not restate the choice of Read.
	if doc.FileBlock != "" {
		a.block(BlockFile, textFileBlock+doc.FileBlock, routeByHand)
		a.structural = pinfile.Parse(doc.Bytes).FileBlock != ""
	}

	// The save replaces the file by rename, so every other name of a
	// hard-linked file keeps the old bytes (P4-17).
	linked, err := integration.HardLinked(a.deps.Root, pinfile.File)

	switch {
	case err != nil:
		a.block(BlockFile, err.Error(), routeByHand)
	case linked:
		a.block(BlockFile, integration.HardLinkReason, routeByHand)
	}
}

// readLedger reads the rows that the relationship and the history need.
// A missing or failed read is unknown, never an empty ledger.
func (a *authoring) readLedger() {
	a.rows, a.ledgerErr = readLedger(a.deps.Reader)
	if a.ledgerErr == nil {
		a.dismissed, a.ledgerErr = readStatus(a.deps.Reader, model.ProposalDismissed)
	}

	a.plan.LinksKnown = a.ledgerErr == nil && a.doc != nil && !a.doc.Unreadable()
}

// bind finds the reviewed entry of an edit or a removal, and reads its
// relationship and the owners of its key.
func (a *authoring) bind() {
	p, ref := a.plan, a.req.Ref

	if a.req.Op == AuthorCreate || a.doc == nil {
		return
	}

	k := slices.IndexFunc(a.doc.Entries, func(e pinfile.Entry) bool { return e.Index == ref.Index && e.Ref() == ref })
	if k < 0 {
		a.block(BlockEntry, textEntryGone, routeRefresh)

		return
	}

	e := a.doc.Entries[k]
	a.entry = &e
	p.Old = pinRule(e)
	p.OldOwners = pinfile.KeyOwners(a.doc, a.rows.applied, a.rows.pending, e.Key, &ref)

	if !p.LinksKnown {
		return
	}

	el := pinfile.Classify(a.doc, a.rows.applied, a.rows.pending, a.rows.superseded).Entries[k]
	a.link = el
	p.Link = el.Link
	p.EntryApplied, p.EntryPending = proposalIDs(el.Applied), proposalIDs(el.Pending)

	if el.Link == pinfile.LinkLinked {
		row := el.Applied[0]
		guard := store.NewRowGuard(row)
		p.Proposal, p.ProposalGuard = &row, &guard
	}
}

// draft checks the typed fields and computes the requested pin, its key,
// its owners, and the identity consequence. A draft with a blocking
// issue stops here, because its pin is not what the person means yet.
func (a *authoring) draft() {
	p := a.plan

	var base *reviews.PinRule
	if a.entry != nil {
		base = &p.Old
	}

	p.Issues, a.set = checkDraft(a.req, base)

	switch {
	case Blocking(p.Issues):
		return
	case a.req.Op == AuthorCreate:
		p.New = reviews.PinForRegions(a.req.Rule, a.req.Note, a.set)
	case a.req.Op == AuthorEdit && a.entry != nil:
		p.New = edited(p.Old, a.req, a.set)
	case a.req.Op == AuthorRemove && a.entry != nil:
		// A removal shows the history of the key that it removes.
		p.History = a.history(a.entry.Key)

		return
	default:
		return
	}

	a.drafted = true
	a.newKey = reviews.NewPinKey(p.New.Rule, p.New.Region, p.New.Regions)

	var self *pinfile.EntryRef
	if a.entry != nil {
		self = &a.req.Ref
		p.KeyChanged = a.entry.Key != a.newKey
		p.IdentityChanged = reviews.PinIdentity(p.Old) != reviews.PinIdentity(p.New)
	}

	p.NewOwners = pinfile.KeyOwners(a.doc, a.rows.applied, a.rows.pending, a.newKey, self)
	p.History = a.history(a.newKey)
	p.Missing = integration.Missing(a.deps.Root, a.set)
}

// edited returns the pin that an edit asks for: the set fields of req
// over old. A new scope takes the layout of reviews.PinForRegions, as
// the retarget splices write it. An entry with only a regions key keeps
// that key instead (A1-R6), so bindNew replaces this pin with the entry
// of the candidate.
func edited(old reviews.PinRule, req AuthorRequest, set []string) reviews.PinRule {
	pin := old

	if req.RuleSet {
		pin.Rule = req.Rule
	}

	if req.NoteSet {
		pin.Note = req.Note
	}

	if req.ScopeSet {
		pin = reviews.PinForRegions(pin.Rule, pin.Note, set)
		pin.Mode, pin.Check = old.Mode, old.Check
	}

	if req.ModeSet {
		pin.Mode = modeKey(req.Mode)
	}

	if req.CheckSet {
		pin.Check = req.Check
	}

	return pin
}

// history returns the superseded and dismissed rows that own key, in
// ascending ID order. It asks KeyOwners, so history and ownership follow
// one key rule. KeyOwners checks its two row lists by the same rule
// (rowKeys). So history gives the superseded rows as the first list and
// the dismissed rows as the second list, and joins the two results.
func (a *authoring) history(key reviews.PinKey) []model.Proposal {
	o := pinfile.KeyOwners(nil, a.rows.superseded, a.dismissed, key, nil)

	return slices.SortedFunc(slices.Values(slices.Concat(o.Applied, o.Pending)), byID)
}

// relation applies the relationship matrix to an edit or a removal. Only
// an entry without a proposal permits every field. A linked entry
// permits note, mode, and check edits. A region change needs a scoped
// plan, because the row must change with the entry. Prune owns its
// removal, so that edit uses its own plan. A scoped plan also needs a
// link: an entry that lost its row has no ledger change to write.
func (a *authoring) relation() {
	p := a.plan

	if a.entry == nil || !p.LinksKnown {
		return
	}

	switch p.Link {
	case pinfile.LinkNoProposal:
		// A row with disagreeing region fields can own the key of an
		// entry that reads as unlinked (pinfile.KeyOwners).
		switch {
		case p.OldOwners.Any():
			a.ambiguousOwners(p.OldOwners)
		case a.scoped:
			a.block(BlockRelation, p.Link.Reason(), routeScopeLink)
		}
	case pinfile.LinkLinked:
		switch {
		case a.req.Op == AuthorRemove:
			a.block(BlockRelation, fmt.Sprintf(textLinkedOnly, p.Proposal.ID), routePrune)
		case a.req.RuleSet && a.req.Rule != p.Old.Rule:
			a.block(BlockRelation, fmt.Sprintf(textLinkedNote, p.Proposal.ID), routeLinkedPin)
		}

		if !a.scoped && ChangesRegions(p.Old, a.req) {
			a.block(BlockRelation, fmt.Sprintf(textLinkedScope, p.Proposal.ID), "")
		}

		// The apply of another applied row with disagreeing region fields
		// can have written this key too. The planner reports that row as
		// a collision, but the cause is the ledger.
		others := slices.DeleteFunc(slices.Clone(p.OldOwners.Applied), func(r model.Proposal) bool {
			return r.ID == p.Proposal.ID
		})
		if len(others) > 0 {
			a.ambiguousOwners(pinfile.Owners{Applied: others})
		}
	case pinfile.LinkPendingMatch:
		a.block(BlockRelation, p.Link.Reason(), routeRecord)
	default:
		a.block(BlockRelation, p.Link.Reason(), reasonLedgerFixes)
	}
}

// ambiguousOwners blocks an entry whose key the rows of o also own. Such
// a row has disagreeing region fields, and its apply can have written the
// entry. The link is then not certain, and only a fix of the file or of
// the ledger resolves it.
func (a *authoring) ambiguousOwners(o pinfile.Owners) {
	labels := strings.Join(ownerLabels(o), ", ")
	a.block(BlockRelation, pinfile.LinkAmbiguousFields.Reason()+" ("+labels+")", reasonLedgerFixes)
}

// owner blocks a create, a rename, or a rescope whose new key another
// pin or proposal holds. The editor never merges two pins and never
// adopts a proposal by its label. A note edit keeps its key, so its own
// linked row is no new owner.
func (a *authoring) owner() {
	p := a.plan

	if !a.drafted || (a.req.Op != AuthorCreate && !p.KeyChanged) || !p.NewOwners.Any() {
		return
	}

	route := routeOwner
	if a.scoped {
		route = routeOwnerScope
	}

	a.block(BlockOwner, textOwnedBy+strings.Join(ownerLabels(p.NewOwners), ", "), route)
}

// candidate computes and verifies the candidate file with the pinfile
// planners. A refusal becomes a blocker. A collision that an owner or a
// relationship blocker already explains is not repeated.
func (a *authoring) candidate() error {
	p := a.plan

	if a.doc == nil || a.structural || Blocking(p.Issues) || (a.req.Op != AuthorCreate && a.entry == nil) {
		return nil
	}

	if a.req.Op == AuthorCreate {
		if a.create() {
			return a.strict(nil)
		}

		return nil
	}

	e := pinfile.Edit{Ref: a.req.Ref, Delete: true, Provenance: a.entry.ProvenanceIDs}
	if a.req.Op == AuthorEdit {
		e = pinfile.Edit{Ref: a.req.Ref, Rewrite: a.rewrite()}
	}

	// A linked note edit must not collide with its own row, so the rows
	// of the entry stay out of the applied keys.
	change, err := pinfile.Plan(a.doc, []pinfile.Edit{e}, pinfile.AuthorKeys(a.rows.applied, p.EntryApplied))

	refused := pinfile.EditErrors(err)
	if err != nil && len(refused) == 0 {
		return fmt.Errorf("maintain: plan the pin file: %w", err)
	}

	for _, ee := range refused {
		switch {
		case errors.Is(ee, pinfile.ErrNoOp):
			p.NoOp = true
		case errors.Is(ee, pinfile.ErrCollision) && a.blocked(BlockOwner, BlockRelation):
		default:
			a.block(BlockFile, ee.Error(), routeByHand)
		}
	}

	if change == nil {
		return nil
	}

	a.change = change
	p.After, p.Diff, p.Added, p.Removed = change.After, change.Diff, change.Added, change.Removed

	// An edit keeps the position of its entry; a removal leaves no entry.
	if a.req.Op == AuthorEdit {
		d := pinfile.Parse(change.After)
		if k := slices.IndexFunc(d.Entries, func(x pinfile.Entry) bool { return x.Index == a.req.Ref.Index }); k >= 0 {
			a.bindNew(d.Entries[k])
		}
	}

	return a.strict(change)
}

// strict adds the checker impact of an edit and refuses a candidate
// that fails the strict check tier. pinfile.Prepare runs the same tier
// at the write. A refusal there comes after the review, so the plan
// names it before the save instead. change is nil for a create, which
// writes an advisory pin and so has no impact.
//
// The impact reads the lenient decodes, so the review shows it also when
// the strict tier refuses the candidate. The route depends on the cause:
// an error that the file already holds needs a manual fix, and an error
// that only the draft makes needs another mode or check.
func (a *authoring) strict(change *pinfile.Change) error {
	p := a.plan

	if change != nil {
		impact, err := checkImpact(a.doc, change)
		if err != nil {
			return err
		}

		p.Impact = impact
	}

	cfg, err := reviews.ParseConfig(p.After)
	if err != nil {
		a.block(BlockFile, textFileBlock+"the candidate does not load: "+err.Error(), routeByHand)

		return nil
	}

	reason := pinfile.CandidateRefusal(cfg)
	if reason == "" {
		return nil
	}

	route := routeCheckDraft
	if before, err := reviews.ParseConfig(p.Before); err != nil || len(lessoncheck.Validate(before)) > 0 {
		route = routeByHand
	}

	a.block(BlockCheck, reason, route)

	return nil
}

// create renders the new pin and appends it after the last entry. It
// reports whether a candidate exists.
func (a *authoring) create() bool {
	p := a.plan

	block, err := pinfile.RenderPin(p.New)
	if err != nil {
		a.block(BlockFile, err.Error(), routeByHand)

		return false
	}

	after, err := pinfile.PlanAppend(a.doc, block, pinfile.Placement{Tail: true})
	if err != nil {
		// A refusal of the file layout names its manual route itself.
		route := routeByHand
		if errors.Is(err, pinfile.ErrUnsupported) {
			route = ""
		}

		a.block(BlockFile, err.Error(), route)

		return false
	}

	p.After = after
	p.Diff, p.Added, p.Removed = integration.UnifiedDiff(pinfile.File, p.Before, after)

	// The tail append puts the new entry last.
	d := pinfile.Parse(after)
	if n := len(d.Entries); n > 0 && d.Entries[n-1].Key == a.newKey {
		a.bindNew(d.Entries[n-1])
	}

	return true
}

// bindNew binds the new or edited entry e of the candidate, and takes New
// from it. So the review shows the pin that the file will hold, also its
// layout: an entry with only a regions key keeps that key (A1-R6).
func (a *authoring) bindNew(e pinfile.Entry) {
	p := a.plan
	ref := e.Ref()
	p.NewRef, p.New = &ref, pinRule(e)

	if a.entry != nil {
		p.IdentityChanged = reviews.PinIdentity(p.Old) != reviews.PinIdentity(p.New)
	}
}

// rewrite returns the Rewrite of an edit: only the fields that the draft
// sets. A repository-wide scope is an empty, not a nil, region set.
func (a *authoring) rewrite() *pinfile.Rewrite {
	rw := &pinfile.Rewrite{}

	if a.req.RuleSet {
		rule := a.req.Rule
		rw.Rule = &rule
	}

	if a.req.NoteSet {
		note := a.req.Note
		rw.Note = &note
	}

	if a.req.ScopeSet {
		set := slices.Clone(a.set)
		if set == nil {
			set = []string{}
		}

		rw.Regions = &set
	}

	// The lesson-check keys follow the splice rule of lessons --set-mode,
	// so the form and the command write the same bytes.
	if a.req.ModeSet {
		mode := modeKey(a.req.Mode)
		rw.Mode = &mode
	}

	if a.req.CheckSet {
		check := a.req.Check
		rw.Check = &check
	}

	return rw
}

// finish adds the blockers of the ledger, the root, and the gate, writes
// the notes, and decides whether the plan can be confirmed. The gate
// comes last, because a separate settings save fixes it.
func (a *authoring) finish() {
	p := a.plan

	if a.ledgerErr != nil {
		a.block(BlockLedger, a.ledgerErr.Error(), routeIndex)
	}

	if a.deps.ForeignRoot != nil {
		a.block(BlockForeignRoot, a.deps.ForeignRoot.Error(), routeOwnIndex)
	}

	if b := gateBlocker(p.Write, p.GateErr); b != nil {
		p.Blockers = append(p.Blockers, *b)
	}

	p.Notes = a.notes()
	p.Confirmable = len(p.Blockers) == 0 && !Blocking(p.Issues) && p.After != nil && !p.NoOp && p.LinksKnown
}

// notes returns the review texts of the plan (contracts §5).
func (a *authoring) notes() []string {
	p := a.plan

	if p.NoOp {
		return []string{noteNoOp}
	}

	var notes []string

	linked := p.Link == pinfile.LinkLinked && p.Proposal != nil
	checks, only := a.checksEdit()

	switch {
	case a.req.Op == AuthorCreate:
		notes = append(notes, noteCreateLast)
	case a.scoped && a.drafted && !p.IdentityChanged:
		notes = append(notes, textScopeOrder)
	case a.req.Op == AuthorEdit && a.drafted && only && !p.IdentityChanged:
		notes = append(notes, fmt.Sprintf(textChecksKept, checks))
	case a.req.Op == AuthorEdit && a.drafted && p.IdentityChanged:
		notes = append(notes, textIdentityChanged)
		if linked {
			notes = append(notes, fmt.Sprintf(textIdentityLinked, p.Proposal.ID))
		}
	case a.req.Op == AuthorEdit && a.drafted:
		notes = append(notes, textIdentityKept)
	}

	if a.req.Op == AuthorEdit && !p.IdentityChanged && a.versionChanged() {
		notes = append(notes, textVersionChanged)
	}

	// Pins marks 'edited' only for a note difference. So a scoped plan
	// takes the text about that mark only when the note really changes. A
	// draft with a blocking issue has no New, so it compares nothing.
	switch {
	case a.scoped && linked && a.drafted && p.New.Note != p.Old.Note:
		notes = append(notes, fmt.Sprintf(textScopeKeeps, p.Proposal.ID))
	case a.scoped:
	case a.req.Op == AuthorEdit && linked && only:
		notes = append(notes, fmt.Sprintf(textLinkedChecks, p.Proposal.ID))
	case a.req.Op == AuthorEdit && linked:
		notes = append(notes, fmt.Sprintf(textLinkedKeeps, p.Proposal.ID))
	}

	if a.scoped {
		notes = append(notes, textScopeLedger)
	} else {
		notes = append(notes, textLedgerEffect)
	}

	if p.After != nil {
		notes = append(notes, noteRedacted)
	}

	return notes
}

// versionChanged reports whether the candidate gives the pin a new pin
// version. Each side reads its own check definitions, so a renamed check
// with the same definition is no change. A plan without a candidate
// reports false, because it writes nothing.
func (a *authoring) versionChanged() bool {
	p := a.plan

	if !a.drafted || p.After == nil {
		return false
	}

	before, err := reviews.ParseConfig(p.Before)
	if err != nil {
		return false
	}

	after, err := reviews.ParseConfig(p.After)
	if err != nil {
		return false
	}

	return before.PinVersion(p.Old) != after.PinVersion(p.New)
}

// checksEdit reports which lesson-check keys the edit changes: "mode",
// "check", or "mode and check". only is true when the edit changes no
// other field of the pin. The test compares the old and the new pin, not
// the set flags, because a draft can set a field to its current value.
func (a *authoring) checksEdit() (keys string, only bool) {
	p := a.plan
	old, next := p.Old, p.New

	modeChanged, checkChanged := old.Mode != next.Mode, old.Check != next.Check
	if !a.drafted || (!modeChanged && !checkChanged) {
		return "", false
	}

	same := old.Rule == next.Rule && old.Note == next.Note && slices.Equal(old.AllRegions(), next.AllRegions())

	switch {
	case modeChanged && checkChanged:
		keys = "mode and check"
	case modeChanged:
		keys = "mode"
	default:
		keys = "check"
	}

	return keys, same
}

// proposalIDs returns the IDs of rows, which come in ascending order.
func proposalIDs(rows []model.Proposal) []int64 {
	var ids []int64

	for _, r := range rows {
		ids = append(ids, r.ID)
	}

	return ids
}

// owner is one holder of a key: an entry by its binding, or a row by its
// ID and status.
type owner struct {
	ref    pinfile.EntryRef
	id     int64
	status string
	label  string
}

// owners lists the holders of o in a fixed order: entries, applied rows,
// pending rows.
func owners(o pinfile.Owners) []owner {
	var out []owner

	for _, e := range o.Entries {
		out = append(out, owner{ref: e.Ref(), label: fmt.Sprintf("the pin at line %d", e.Line)})
	}

	for _, r := range o.Applied {
		out = append(out, owner{id: r.ID, status: r.Status, label: fmt.Sprintf("applied #%d", r.ID)})
	}

	for _, r := range o.Pending {
		out = append(out, owner{id: r.ID, status: r.Status, label: fmt.Sprintf("pending #%d", r.ID)})
	}

	return out
}

// ownerLabels returns the plain names of the holders of o.
func ownerLabels(o pinfile.Owners) []string {
	var out []string

	for _, w := range owners(o) {
		out = append(out, w.label)
	}

	return out
}

// CommitAuthor writes a confirmed authoring plan. It plans again from
// the current file, gate, and ledger. It refuses with a StaleError
// unless the new plan is identical in every observation that the person
// reviewed. It then writes the one file through Deps.Author. It never
// writes the ledger and never builds a Recovery.
//
// A non-nil error means that CommitAuthor wrote nothing: the outcome is
// then NotWritten, and CommitAuthor does not read the file back (P4-18).
// After the write starts, the error is nil and the result holds the
// outcome. After a failed write, CommitAuthor reads the file again and
// does not assume the outcome. integration.Landed decides between
// Written, NotWritten, and Unknown. CommitAuthor never retries a write.
func CommitAuthor(d Deps, p *AuthorPlan) (AuthorResult, error) {
	if p == nil {
		return AuthorResult{}, errors.New("maintain: no plan")
	}

	refuse := func(err error) (AuthorResult, error) {
		return AuthorResult{Outcome: NotWritten, Err: err}, err
	}

	if d.ForeignRoot != nil {
		return refuse(d.ForeignRoot)
	}

	if err := confirmRefusal(p.Confirmable, p.Write, p.GateErr); err != nil {
		return refuse(err)
	}

	q, err := PlanAuthor(d, p.Request)
	if err != nil {
		return refuse(err)
	}

	if diffs := authorDifferences(p, q); len(diffs) > 0 {
		return refuse(&StaleError{Differences: diffs})
	}

	res, err := writeAuthored(d, q.Guard, q.After, q.Request.Op == AuthorCreate)
	if err != nil {
		return refuse(err)
	}

	if res.Outcome == Written {
		res.NewRef = q.NewRef
	}

	return res, nil
}

// writeAuthored writes after over the file that guard observed, through
// Deps.Author. create allows an absent file. A non-nil error means that
// nothing was written before the engine write: the refusal or the stale
// observation. After the write starts, the error is nil and the result
// holds the outcome. A failed write reads the file again, and
// integration.Landed decides the outcome. The pin authoring and the
// check-definition authoring share this rule.
func writeAuthored(d Deps, guard integration.FileGuard, after []byte, create bool) (AuthorResult, error) {
	err := d.author().Write(d.Reg, d.Root, guard, after, create)
	if err == nil {
		return AuthorResult{Outcome: Written}, nil
	}

	if beforeWrite(err) {
		if !errors.Is(err, ErrStale) && errors.Is(err, pinfile.ErrStale) {
			err = &StaleError{Differences: []string{diffBytes}}
		}

		return AuthorResult{Outcome: NotWritten, Err: err}, err
	}

	holds, rerr := fileHolds(d.Root, sha256.Sum256(after))

	switch {
	case rerr != nil:
		return AuthorResult{Outcome: Unknown, Err: readBackFailed(err, rerr)}, nil
	case holds:
		return AuthorResult{Outcome: Written, Err: err}, nil
	}

	return AuthorResult{Outcome: NotWritten, Err: err}, nil
}

// beforeWrite reports whether an error of the writer came before the
// engine wrote anything. Such an error leaves the file as it was.
func beforeWrite(err error) bool {
	return errors.Is(err, errNotStarted) || errors.Is(err, ErrStale) ||
		errors.Is(err, pinfile.ErrStale) || errors.Is(err, pinfile.ErrNotWritable)
}

// authorDifferences names what changed between a reviewed plan p and the
// plan q from the current observations. An empty result means that q
// may be written. integration.SameGuard decides about the file, so the
// mode counts (P4-7).
func authorDifferences(p, q *AuthorPlan) []string {
	var out []string

	add := func(changed bool, reason string) {
		if changed && !slices.Contains(out, reason) {
			out = append(out, reason)
		}
	}

	if !integration.SameGuard(p.Guard, q.Guard) {
		for _, r := range guardChanges(p.Guard, q.Guard) {
			add(true, r)
		}
	}

	add(integration.SameGuard(p.Guard, q.Guard) && !bytes.Equal(p.After, q.After), diffContent)
	add(p.Root != q.Root, diffRoot)
	add(p.Write != q.Write || (p.GateErr == nil) != (q.GateErr == nil), diffGate)
	add(p.LinksKnown && !q.LinksKnown, diffLedger)

	// said counts the lines before the lines about the rows.
	said := len(out)

	rel, named := relationChange(p, q)
	add(rel != "", rel)

	if p.Proposal != nil && q.Proposal != nil &&
		(p.Proposal.ID != q.Proposal.ID || p.ProposalGuard == nil || !p.ProposalGuard.Matches(*q.Proposal)) {
		add(true, fmt.Sprintf("proposal #%d changed", p.Proposal.ID))
	}

	// Without a known ledger, q lists no rows, and diffLedger names the
	// cause. A missing row is then no change of an owner.
	if q.LinksKnown {
		for _, r := range slices.Concat(ownerChanges(p.OldOwners, q.OldOwners, named), ownerChanges(p.NewOwners, q.NewOwners, named)) {
			add(true, r)
		}

		// A row can start to match the entry and still own the same key,
		// for example when its region fields come to agree. No line above
		// names that change.
		rows := !slices.Equal(p.EntryApplied, q.EntryApplied) || !slices.Equal(p.EntryPending, q.EntryPending)
		add(rows && len(out) == said, diffRows)
	}

	// A new blocker without a changed observation, such as a new hard
	// link, still makes the review stale.
	if len(out) == 0 && !q.Confirmable {
		for _, b := range q.Blockers {
			add(true, b.String())
		}

		add(q.NoOp, noteNoOp)
		add(len(out) == 0, diffConfirm)
	}

	return out
}

// relationChange names a new link of the entry, or returns "". It also
// returns the rows that the text names, so the owner lines do not repeat
// them. A known ledger gives LinkUnknown only to a plan without an entry:
// a create, or an entry that is gone, which the file change names.
func relationChange(p, q *AuthorPlan) (text string, named []int64) {
	if !q.LinksKnown || q.Link == pinfile.LinkUnknown {
		return "", nil
	}

	switch {
	case q.Link == pinfile.LinkLinked && q.Proposal != nil && (p.Proposal == nil || p.Proposal.ID != q.Proposal.ID):
		// The line "proposal #N changed" names the earlier linked row.
		named = []int64{q.Proposal.ID}
		if p.Proposal != nil {
			named = append(named, p.Proposal.ID)
		}

		return fmt.Sprintf("now linked to proposal #%d", q.Proposal.ID), named
	case p.Link != q.Link:
		return "relationship: " + q.Link.Reason(), nil
	}

	return "", nil
}

// guardChanges names the parts of a file observation that differ. A
// change of existence implies the other parts, so it stands alone.
func guardChanges(a, b integration.FileGuard) []string {
	if a.Exists != b.Exists {
		return []string{diffExistence}
	}

	var out []string

	for _, c := range []struct {
		changed bool
		name    string
	}{
		{a.SHA256 != b.SHA256, diffBytes},
		{a.Mode != b.Mode, diffMode},
		{a.Linked != b.Linked, diffLink},
		{a.Denied != b.Denied, diffAccess},
	} {
		if c.changed {
			out = append(out, c.name)
		}
	}

	if len(out) == 0 {
		out = append(out, diffFile)
	}

	return out
}

// ownerChanges names the holders of a key that came or went: entries by
// their binding, rows by ID and status. It leaves out the rows in skip.
func ownerChanges(before, after pinfile.Owners, skip []int64) []string {
	key := func(w owner) owner { return owner{ref: w.ref, id: w.id, status: w.status} }
	has := func(list []owner, w owner) bool {
		return slices.ContainsFunc(list, func(x owner) bool { return key(x) == key(w) })
	}
	named := func(w owner) bool { return w.id != 0 && slices.Contains(skip, w.id) }

	old, now := owners(before), owners(after)

	var out []string

	for _, w := range now {
		if !has(old, w) && !named(w) {
			out = append(out, "new owner: "+w.label)
		}
	}

	for _, w := range old {
		if !has(now, w) && !named(w) {
			out = append(out, "owner gone: "+w.label)
		}
	}

	return out
}

// authorWriter is the production AuthorWriter: the guarded atomic write
// of the pin file through integration Route A.
type authorWriter struct{}

// Write prepares the guarded write and applies it. pinfile.Prepare
// compares only the existence and the content with expect. It binds its
// own fresh observation, and the engine checks that observation in full
// before the rename. So Write compares expect with the observation of
// Prepare. A new mode or a new link in between is a change that the
// person did not review (P4-7).
func (authorWriter) Write(reg *integration.Registry, root string, expect integration.FileGuard, after []byte, create bool) error {
	prepare := pinfile.Prepare
	if create {
		prepare = pinfile.PrepareAppend
	}

	w, err := prepare(reg, root, expect, after)
	if err != nil {
		return fmt.Errorf("%w: %w", errNotStarted, err)
	}

	if !integration.SameGuard(expect, w.Guard) {
		return &StaleError{Differences: guardChanges(expect, w.Guard)}
	}

	return w.Apply()
}
