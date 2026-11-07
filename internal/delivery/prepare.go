package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// This file holds preparation (contract L1): the scoped lesson guidance
// that an agent asks for before it constructs a change. MCP change_set
// and the CLI lessons --prepare get one result from Prepare, so the two
// surfaces cannot disagree. Preparation has no receiver identity, so it
// never reads or writes a delivery state: an edit that follows still
// gets its own reminder (AC-26, A13).

// PreparationSchema is the schema of the preparation document.
const PreparationSchema = "lesson-preparation/1"

// PreparationNotice is the fixed last sentence of every preparation. The
// contract freezes it, because guidance text and tests quote it.
const PreparationNotice = "Preparation is advisory: it authorizes no write, it does not show that a model read it, " +
	"and every covered edit still meets its active checks. Plan with it, then construct the change in a later step, " +
	"not in the same generated program."

// Surface is the delivery surface of a preparation and of its firing
// record. The list is closed.
type Surface string

// The surfaces. Prepare accepts only change_set and prepare. Explore is
// the surface of exploration advice, which never returns a preparation.
const (
	SurfaceChangeSet Surface = "change_set"
	SurfacePrepare   Surface = "prepare"
	SurfaceExplore   Surface = "explore"
)

// The mined statuses of a preparation. The caller opens the index, so
// only the caller knows why a store holds no mined lessons.
const (
	// MinedOK means st is the index of the workspace.
	MinedOK = "ok"
	// MinedNoIndex means no index exists, and st is an in-memory store.
	MinedNoIndex = "no-index"
	// MinedUnavailable means the index did not open, and st is an
	// in-memory store.
	MinedUnavailable = "unavailable"
)

// The checks statuses of a preparation. The list is closed.
const (
	checksNone    = "none"
	checksValid   = "valid"
	checksInvalid = "invalid"
)

// The states of a requested path. The list is closed.
const (
	stateFile      = "file"
	stateDirectory = "directory"
	stateAbsent    = "absent"
	stateOther     = "other"
)

// The pin modes of a preparation. modeInvalid names a raw mode that
// lessons.yaml does not define, so the reader sees the typing error.
const (
	modeAdvisory = "advisory"
	modeCheck    = "check"
	modeEnforce  = "enforce"
	modeInvalid  = "invalid"
)

// The omission kinds of a preparation. The list is closed.
const (
	omittedPin    = "pin"
	omittedLesson = "lesson"
)

// EmissionReturned is the emission of a preparation record: the lessons
// went back to the caller, and no agent context got them. The MCP
// change_set runner writes it on the fallback record of a failed
// preparation too, so it is exported.
const EmissionReturned = "returned"

// receiverNoIdentity is the receiver reason of MCP and the CLI. Neither
// surface gets a session or an agent ID, so the receiver is unknown.
const receiverNoIdentity = "no-receiver-identity"

// prepScopeLabel is the first compose part of the scope digest
// (registry §2.2). A change of the parts or of their order is a new label.
const prepScopeLabel = "prep-scope/1"

// maxRequestedRunes bounds each requested path in the document, because
// a raw input can be of any length.
const maxRequestedRunes = 200

// preparationEventIDBytes is the size of a random event ID: 16 bytes
// give the 32 hex characters of the firing record key event_id.
const preparationEventIDBytes = 16

// PrepareRequest is one preparation call.
type PrepareRequest struct {
	// Root is the canonical workspace root. Path normalization, the
	// guidance status, and the firing log use it.
	Root string
	// CWD is the directory that a relative path resolves against. MCP
	// passes Root, and the CLI passes the process directory.
	CWD string
	// Paths are the paths as the caller gives them, in request order.
	Paths []string
	// Surface is SurfaceChangeSet or SurfacePrepare.
	Surface Surface
	// Config is the lenient configuration (reviews.LoadConfig). Nil with
	// ConfigErr set means an invalid file. Nil without ConfigErr makes
	// Prepare call reviews.LoadConfig itself.
	Config    *reviews.Config
	ConfigErr error
	// MinedStatus says what st is: MinedOK (or empty), MinedNoIndex, or
	// MinedUnavailable. The contract request has no such field. The
	// caller opens the index, so only the caller can say why st holds
	// no mined lessons.
	MinedStatus string
	// Now gives the preparation time. Nil means time.Now.
	Now func() time.Time
	// EventID gives the event_id of the firing record. Nil means 16
	// random bytes as hex. Tests set it to get fixed records.
	EventID func() string
	// SeamarkVersion fills Preparation.SeamarkVersion.
	SeamarkVersion string
	// Capability is the lesson-capability/1 report of the workspace, or
	// nil. Prepare copies the badges of each pin into
	// CheckSummary.Enforcement (contract L1, milestone P4). The caller
	// builds it, because the report joins the client inspections, and
	// this package reads no client hook document.
	Capability *lessoncheck.CapabilityReport
}

// Preparation is the lesson-preparation/1 document.
type Preparation struct {
	Schema         string           `json:"schema"`
	SeamarkVersion string           `json:"seamark_version"`
	PreparedAt     string           `json:"prepared_at"`
	Surface        Surface          `json:"surface"`
	Receiver       Receiver         `json:"receiver"`
	Request        PrepareScope     `json:"request"`
	Guidance       Guidance         `json:"guidance"`
	Pins           []PreparedPin    `json:"pins"`
	Lessons        []PreparedLesson `json:"lessons"`
	Checks         []CheckSummary   `json:"checks"`
	Omitted        []Omission       `json:"omitted"`
	Refresh        []string         `json:"refresh"`
	Notice         string           `json:"notice"`
	EventID        string           `json:"event_id,omitempty"`

	// pending is the firing record that Record appends. It is not part
	// of the document, and it is nil after Record or without a selection.
	pending *reviews.AuditEvent
}

// Receiver is the context that gets the preparation. MCP and the CLI
// never know it, so Prepare always writes the lane unknown.
type Receiver struct {
	Lane          string `json:"lane"`
	Reason        string `json:"reason,omitempty"`
	Client        string `json:"client,omitempty"`
	ContextSHA256 string `json:"context_sha256,omitempty"`
	TurnSHA256    string `json:"turn_sha256,omitempty"`
}

// PrepareScope is the requested path scope. The name is not Request,
// because Request is the edit event of Deliver.
type PrepareScope struct {
	Requested     []string        `json:"requested"`
	Paths         []ScopePath     `json:"paths"`
	Rejected      []RejectedInput `json:"rejected,omitempty"`
	RejectedCount int             `json:"rejected_count"`
	ScopeSHA256   string          `json:"scope_sha256"`
}

// ScopePath is one resolved path of the scope.
type ScopePath struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Pins   int    `json:"pins"`
	Checks int    `json:"checks"`
	Read   *bool  `json:"read,omitempty"`
}

// RejectedInput is one input path that gives no workspace path.
type RejectedInput struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Guidance is the state of the guidance that the selection used.
type Guidance struct {
	Status         string `json:"status"`
	GuidanceSHA256 string `json:"guidance_sha256,omitempty"`
	Budget         int    `json:"budget"`
	MinedStatus    string `json:"mined_status"`
	ChecksStatus   string `json:"checks_status"`
	ChecksError    string `json:"checks_error,omitempty"`
}

// PreparedPin is one selected pin with its exact version.
type PreparedPin struct {
	PinRef      string   `json:"pin_ref"`
	PinIdentity string   `json:"pin_identity"`
	PinVersion  string   `json:"pin_version"`
	Entry       int      `json:"entry"`
	Rule        string   `json:"rule"`
	Note        string   `json:"note"`
	Scope       []string `json:"scope"`
	Mode        string   `json:"mode"`
	Paths       []string `json:"paths"`
}

// PreparedLesson is one selected mined lesson. A mined lesson has no
// version: its cluster key names it.
type PreparedLesson struct {
	ClusterKey  string `json:"cluster_key"`
	Region      string `json:"region"`
	Symptom     string `json:"symptom"`
	Occurrences int    `json:"occurrences"`
}

// CheckSummary is one pin with a check that applies to a requested path
// by the pin region rule. The list is not budgeted: an edit meets every
// active check.
type CheckSummary struct {
	PinRef      string                  `json:"pin_ref"`
	PinVersion  string                  `json:"pin_version"`
	Mode        string                  `json:"mode"`
	Runs        bool                    `json:"runs"`
	Check       lessoncheck.CheckRef    `json:"check"`
	Predicate   string                  `json:"predicate"`
	Paths       []string                `json:"paths"`
	Enforcement []lessoncheck.PinClient `json:"enforcement,omitempty"`
}

// Omission is one pin or mined lesson that the change budget or the
// restatement collapse left out, with the command that shows it.
type Omission struct {
	Kind       string `json:"kind"`
	PinRef     string `json:"pin_ref,omitempty"`
	PinVersion string `json:"pin_version,omitempty"`
	ClusterKey string `json:"cluster_key,omitempty"`
	Rule       string `json:"rule"`
	Reason     string `json:"reason"`
	Inspect    string `json:"inspect"`
}

// Prepare selects the lessons of the requested paths with the change
// budget of change_set and returns the preparation. st must be open: an
// index read-only, or an in-memory store when no index is usable.
//
// Prepare makes no model call, reads no snapshot, runs no checker and
// no git command, and takes no delivery lease. Every call returns the
// current selection, so a caller detects a needed refresh by a new
// path or by a new guidance digest. A path set without one workspace
// path gives a preparation without a selection, not an error.
//
// A selection with at least one lesson has one firing record of
// version 2 (contract L3). Prepare does not append it: the caller calls
// Record after its answer is ready. So an answer that fails after
// Prepare leaves no returned record (P3 review, ruling A).
func Prepare(ctx context.Context, st *store.Store, req PrepareRequest) (*Preparation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if st == nil {
		return nil, errors.New("delivery: preparation needs an open store")
	}

	if req.Surface != SurfaceChangeSet && req.Surface != SurfacePrepare {
		return nil, fmt.Errorf("delivery: %q is not a preparation surface", req.Surface)
	}

	mined := req.MinedStatus
	switch mined {
	case "":
		mined = MinedOK
	case MinedOK, MinedNoIndex, MinedUnavailable:
	default:
		return nil, fmt.Errorf("delivery: unknown mined status %q", mined)
	}

	cfg, loadErr := preparationConfig(req)

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}

	at := now()
	normalized := NormalizePaths(req.Root, req.CWD, req.Paths)
	files := normalized.Files

	digest, status := report.GuidanceOf(req.Root, cfg, loadErr)
	checksStatus, checksError := checksStatusOf(cfg, loadErr)

	p := &Preparation{
		Schema:         PreparationSchema,
		SeamarkVersion: req.SeamarkVersion,
		PreparedAt:     at.UTC().Format(time.RFC3339),
		Surface:        req.Surface,
		Receiver:       Receiver{Lane: laneUnknown, Reason: receiverNoIdentity},
		Request:        scopeOf(req.Root, req.Paths, normalized),
		Guidance: Guidance{
			Status: status, GuidanceSHA256: digest, Budget: cfg.ChangeSetBudget(),
			MinedStatus: mined, ChecksStatus: checksStatus, ChecksError: checksError,
		},
		Pins:    []PreparedPin{},
		Lessons: []PreparedLesson{},
		Checks:  []CheckSummary{},
		Omitted: []Omission{},
		Refresh: []string{"paths-added", "guidance-changed", "receiver-changed"},
		Notice:  PreparationNotice,
	}

	if len(files) == 0 {
		// Nothing is in the workspace, so nothing is selected or recorded.
		return p, nil
	}

	ev, err := report.ReadEvidence(st, files, cfg)
	if err != nil {
		return nil, fmt.Errorf("select lessons: %w", err)
	}

	sel, err := ev.ChangeSetSelection(cfg)
	if err != nil {
		return nil, fmt.Errorf("select lessons: %w", err)
	}

	root := inspectRoot(req.Root)
	refs := cfg.ShortPinRefs()
	pinPaths, pinCounts := pinScopes(cfg, files)
	checks, checkCounts := checkSummaries(cfg, refs, pinPaths)
	withEnforcement(checks, req.Capability)

	for i, f := range files {
		p.Request.Paths[i].Pins = pinCounts[f]
		p.Request.Paths[i].Checks = checkCounts[f]
	}

	for _, sp := range sel.Pins {
		p.Pins = append(p.Pins, preparedPin(cfg, refs, sp, pinPaths))
	}

	for _, l := range sel.Mined {
		p.Lessons = append(p.Lessons, PreparedLesson{
			ClusterKey: l.ClusterKey, Region: l.Region,
			Symptom: render.Sanitize(l.Symptom), Occurrences: l.Occurrences,
		})
	}

	p.Checks = checks
	p.Omitted = omissions(cfg, refs, sel, root, files, pinPaths)

	// A cancelled call returns no preparation, so it records none either.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(sel.Lessons) > 0 {
		p.pending = preparationRecord(req, at, files, normalized.RejectedCount, cfg, sel, digest, status,
			p.Request.ScopeSHA256)
	}

	return p, nil
}

// Record appends the one firing record of the preparation (contract L3)
// and sets EventID. The caller calls it after the rest of its answer
// succeeds, so an answer that fails leaves no returned record (P3 review,
// ruling A). A preparation without a selected lesson has no record.
//
// A second call writes nothing, so one call of a surface gives at most
// one record. The append is best effort: a failure leaves EventID empty.
func (p *Preparation) Record() {
	if p == nil || p.pending == nil {
		return
	}

	ev := *p.pending
	p.pending = nil

	if _, err := reviews.RecordEvent(ev); err != nil {
		return
	}

	p.EventID = ev.EventID
}

// preparationConfig returns the configuration of the selection and its
// read error. An invalid file gives the defaults, as in the edit hook:
// the selection then holds no pin, and the guidance status says so.
func preparationConfig(req PrepareRequest) (*reviews.Config, error) {
	cfg, loadErr := req.Config, req.ConfigErr
	if cfg == nil && loadErr == nil {
		cfg, loadErr = reviews.LoadConfig(req.Root)
	}

	if loadErr != nil || cfg == nil {
		return reviews.DefaultConfig(), loadErr
	}

	return cfg, nil
}

// checksStatusOf returns the checks status of cfg and the first error.
//
// lessoncheck.Parse is reviews.ParseConfig followed by Validate. The
// caller already decoded the bytes, so Validate on that config gives
// the errors of Parse on the same bytes without a second read. A second
// read could see other bytes than the selection did.
func checksStatusOf(cfg *reviews.Config, loadErr error) (status, message string) {
	if loadErr != nil {
		// A file that does not decode has no check set. The message drops
		// the prefix, as the decode-error entry of a verification does.
		prefix := reviews.ErrLessonsConfig.Error() + ": "

		return checksInvalid, render.Sanitize(strings.TrimPrefix(loadErr.Error(), prefix))
	}

	if errs := lessoncheck.Validate(cfg); len(errs) > 0 {
		return checksInvalid, render.Sanitize(errs[0].Message)
	}

	// Validate refuses a check reference without a definition, so a
	// valid file without definitions binds no check.
	if len(cfg.Checks) == 0 {
		return checksNone, ""
	}

	return checksValid, ""
}

// scopeOf builds the request scope from the raw inputs and their
// normalization under root. The pin and check counts of each path come
// later, from the selection.
func scopeOf(root string, raw []string, normalized Normalized) PrepareScope {
	scope := PrepareScope{
		Requested:     make([]string, 0, len(raw)),
		Paths:         make([]ScopePath, 0, len(normalized.Files)),
		RejectedCount: normalized.RejectedCount,
		ScopeSHA256:   scopeDigest(normalized.Files),
	}

	for _, p := range raw {
		scope.Requested = append(scope.Requested, displayInput(p))
	}

	for _, f := range normalized.Files {
		scope.Paths = append(scope.Paths, ScopePath{Path: f, State: pathState(root, f)})
	}

	for _, r := range normalized.Rejected {
		scope.Rejected = append(scope.Rejected, RejectedInput{Path: displayInput(r.Path), Reason: string(r.Reason)})
	}

	return scope
}

// displayInput is the document form of a raw input path: sanitized and
// bounded, because the client chooses its bytes and its length.
func displayInput(raw string) string {
	return render.Truncate(render.Sanitize(raw), maxRequestedRunes)
}

// pathState returns the state of a resolved path. Only a missing file is
// absent: another Lstat error does not prove that no file exists.
func pathState(root, rel string) string {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return stateAbsent
	case err != nil:
		return stateOther
	case info.Mode().IsRegular():
		return stateFile
	case info.IsDir():
		return stateDirectory
	default:
		return stateOther
	}
}

// scopeDigest is the scope_sha256 of a path set: the compose digest of
// the label, the path count, and the sorted unique paths.
func scopeDigest(files []string) string {
	sorted := slices.Clone(files)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)

	parts := append([]string{prepScopeLabel, strconv.Itoa(len(sorted))}, sorted...)

	return reviews.ComposeDigest(parts...)
}

// pinScopes maps each pin key to the resolved paths that the pin applies
// to, and counts the pin entries that apply to each path. Both use the
// region rule of the edit hook (SurfacePins), which works in both
// directions, so a directory input gets the pins below it. The checks
// list takes the same paths, so a pin and its check never disagree.
func pinScopes(cfg *reviews.Config, files []string) (paths map[reviews.PinKey][]string, counts map[string]int) {
	paths = map[reviews.PinKey][]string{}
	counts = make(map[string]int, len(files))

	for _, f := range files {
		applicable := cfg.SurfacePins(f, nil)
		counts[f] = len(applicable)
		seen := map[reviews.PinKey]bool{}

		for _, sp := range applicable {
			key := reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)
			if seen[key] {
				continue
			}

			seen[key] = true
			paths[key] = append(paths[key], f)
		}
	}

	return paths, counts
}

// preparedPin is the document form of one selected pin. The entry comes
// from report.EntryOf, so the preparation and the firing record name
// one entry and one version for the pin. refs is cfg.ShortPinRefs. A
// selected pin is an entry of cfg, so refs holds its key.
func preparedPin(cfg *reviews.Config, refs map[reviews.PinKey]string, sp reviews.SurfacedPin,
	pinPaths map[reviews.PinKey][]string,
) PreparedPin {
	key := reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)

	paths := pinPaths[key]
	if paths == nil {
		paths = []string{}
	}

	return PreparedPin{
		PinRef:      refs[key],
		PinIdentity: reviews.PinIdentityDigest(key),
		PinVersion:  cfg.PinVersion(sp.Pin),
		Entry:       report.EntryOf(cfg, sp),
		Rule:        render.Sanitize(sp.Pin.Rule),
		Note:        render.Sanitize(sp.Pin.Note),
		Scope:       lessoncheck.SortedScope(sp.Pin.AllRegions()),
		Mode:        preparedMode(sp.Pin.Mode),
		Paths:       paths,
	}
}

// preparedMode is the mode of a pin in the document. An unknown raw value
// is invalid, so the reader sees the error in lessons.yaml.
func preparedMode(raw string) string {
	switch raw {
	case "", modeAdvisory:
		return modeAdvisory
	case modeCheck, modeEnforce:
		return raw
	default:
		return modeInvalid
	}
}

// checkSummaries lists every pin with a check mode or a check reference
// that applies to a resolved path, in entry order. No budget, collapse,
// or suppression applies, because an edit meets every active check. It
// also counts the executable pins that apply to each path. An advisory
// pin with a check never runs, so it is listed with Runs false.
//
// The paths of a pin come from pinScopes, so a pin and its check name
// one path set. A directory above the scope of a pin thus gets the check
// of that pin too. The checkers still evaluate only the changes inside
// the pin scope. refs is cfg.ShortPinRefs.
func checkSummaries(cfg *reviews.Config, refs map[reviews.PinKey]string, pinPaths map[reviews.PinKey][]string) (
	summaries []CheckSummary, counts map[string]int,
) {
	summaries = []CheckSummary{}
	counts = map[string]int{}

	for _, pin := range cfg.Pin {
		// An unknown mode reads advisory, as in the checkers, so it never
		// arms a check. Runs then states what the checkers do.
		mode, _ := lessoncheck.NormalizeMode(pin.Mode)
		executable := mode.Executable()

		if !executable && pin.Check == "" {
			continue
		}

		key := reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions)

		paths := pinPaths[key]
		if len(paths) == 0 {
			continue
		}

		if executable {
			for _, f := range paths {
				counts[f]++
			}
		}

		ref, predicate := checkRefOf(cfg, pin.Check)

		// The pin row holds the same slice, so the summary gets a copy.
		// A change to one field of the document then never changes the other.
		summaries = append(summaries, CheckSummary{
			PinRef:     refs[key],
			PinVersion: cfg.PinVersion(pin),
			Mode:       string(mode),
			Runs:       executable,
			Check:      ref,
			Predicate:  predicate,
			Paths:      slices.Clone(paths),
		})
	}

	return summaries, counts
}

// withEnforcement copies the K10 badges of each pin into its check
// summary. A pin joins only with the same pin-ref and pin version, so a
// report of another file state never lends its badges. Without a report
// the field stays absent.
func withEnforcement(checks []CheckSummary, capability *lessoncheck.CapabilityReport) {
	if capability == nil {
		return
	}

	for i := range checks {
		pin, ok := capability.Pin(checks[i].PinRef)
		if !ok || pin.PinVersion != checks[i].PinVersion || len(pin.Clients) == 0 {
			continue
		}

		checks[i].Enforcement = slices.Clone(pin.Clients)
	}
}

// checkRefOf names the check of a pin and its predicate. It reads the
// written definition, so an invalid file still names the kind and the
// version of a definition that exists. A reference without a definition
// keeps only its ID, and a definition that no checker runs has no
// predicate.
func checkRefOf(cfg *reviews.Config, id string) (ref lessoncheck.CheckRef, predicate string) {
	ref = lessoncheck.CheckRef{ID: id}

	def, ok := cfg.Checks[id]
	if !ok || id == "" {
		return ref, ""
	}

	ref.Kind = lessoncheck.Kind(def.Kind)
	ref.Version = reviews.CheckVersion(def)

	predicate, err := lessoncheck.Describe(def)
	if err != nil {
		return ref, ""
	}

	return ref, render.Sanitize(predicate)
}

// omissions lists the pins and the mined lessons that the change budget
// or the restatement collapse left out: Restated, then OverTotal, then
// MinedOverTotal. Each names the command that shows the lessons of the
// first requested path it applies to. refs is cfg.ShortPinRefs.
func omissions(cfg *reviews.Config, refs map[reviews.PinKey]string, sel report.Selection, root string, files []string,
	pinPaths map[reviews.PinKey][]string,
) []Omission {
	out := []Omission{}

	pin := func(sp reviews.SurfacedPin, reason string) Omission {
		key := reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)

		return Omission{
			Kind: omittedPin, PinRef: refs[key], PinVersion: cfg.PinVersion(sp.Pin),
			Rule: render.Sanitize(sp.Pin.Rule), Reason: reason,
			Inspect: inspectCommand(root, firstPathOf(pinPaths[key], files)),
		}
	}

	for _, sp := range sel.Restated {
		out = append(out, pin(sp, reviews.CauseRestated))
	}

	for _, sp := range sel.OverTotal {
		out = append(out, pin(sp, reviews.CauseOverTotal))
	}

	for _, l := range sel.MinedOverTotal {
		out = append(out, Omission{
			Kind: omittedLesson, ClusterKey: l.ClusterKey,
			Rule: render.Sanitize(l.Symptom), Reason: reviews.CauseOverTotal,
			Inspect: inspectCommand(root, firstPathOf(minedPaths(l, files), files)),
		})
	}

	return out
}

// minedPaths returns the resolved paths that a mined lesson applies to.
// The index reads a lesson for a file when its region is the file or a
// directory above it, so the test is the same.
func minedPaths(l model.Lesson, files []string) []string {
	var out []string

	for _, f := range files {
		if reviews.RegionMatches(l.Region, f) {
			out = append(out, f)
		}
	}

	return out
}

// firstPathOf returns the first path of paths, or the first file when
// paths is empty. A selected item always applies to a file, so the
// fallback only keeps the command complete.
func firstPathOf(paths, files []string) string {
	if len(paths) > 0 {
		return paths[0]
	}

	return files[0]
}

// inspectCommand is the command that shows every lesson of one path.
// The path is absolute, because another process runs the command, maybe
// from another directory (contracts §1.3). The lessoncheck rerun command
// quotes its paths with the same function, so the two commands agree.
func inspectCommand(root, rel string) string {
	return "seamark lessons --file " + lessoncheck.QuoteArg(filepath.Join(root, filepath.FromSlash(rel)))
}

// inspectRoot returns root as an absolute path for the inspect command.
// A relative root stays as it is when the working directory is unknown.
func inspectRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}

	return root
}

// preparationRecord builds the one firing record of a preparation call
// (contract L3, surfaces change_set and prepare). Record appends it
// later. It is nil when no event ID is available. The receiver is
// unknown, so the record carries no session, match, context, turn,
// lane, or record_evaluations key.
func preparationRecord(req PrepareRequest, at time.Time, files []string, rejected int, cfg *reviews.Config,
	sel report.Selection, digest, status, scope string,
) *reviews.AuditEvent {
	// RecordEvent makes a random ID for an empty one and does not return
	// it. So the ID comes first, and the preparation can name its record.
	eventID := preparationEventID(req.EventID)
	if eventID == "" {
		return nil
	}

	returned := make([]reviews.PinDelivery, len(sel.Pins))
	for i := range returned {
		returned[i] = reviews.PinReturned
	}

	return &reviews.AuditEvent{
		Root:           req.Root,
		Surface:        string(req.Surface),
		Files:          files,
		Records:        []reviews.EventRecord{{Lessons: sel.Lessons}},
		Pins:           sel.FiringPins(cfg, returned),
		Emission:       EmissionReturned,
		Evaluation:     evaluationComplete,
		GuidanceSHA:    digest,
		GuidanceStatus: status,
		ScopeSHA:       scope,
		PathsComplete:  rejected == 0,
		RejectedPaths:  rejected,
		EventID:        eventID,
		Now:            at,
	}
}

// preparationEventID returns the event ID of a preparation: the value
// of the caller's function, or 16 random bytes as hex. It is "" when no
// random bytes are available.
func preparationEventID(given func() string) string {
	if given != nil {
		return given()
	}

	b := make([]byte, preparationEventIDBytes)
	if _, err := rand.Read(b); err != nil {
		return ""
	}

	return hex.EncodeToString(b)
}
