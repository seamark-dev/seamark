package lessonreport

import (
	"time"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The repository-relative paths of the two audit sources. Build has no
// root, and the report prints no absolute path.
const (
	auditPath      = ".seamark/lessons-audit.jsonl"
	checkAuditPath = ".seamark/lesson-check-audit.jsonl"
)

// The states of a source, the archive, and the config.
const (
	StateRead       = "read"
	StateAbsent     = "absent"
	StateUnreadable = "unreadable"
	StateOverLimit  = "over-limit"

	ArchiveOlderSchema = "older-schema"
	ArchiveNewerSchema = "newer-schema"
	ArchiveBusy        = "busy"

	// ArchiveNotReadError is the fixed error text of as_of.archive. The
	// text holds in every checkout, also in one that holds an archive.
	ArchiveNotReadError = "this build does not read a capture archive"

	ConfigOK      = "ok"
	ConfigInvalid = "invalid"

	CaptureAvailable   = "available"
	CaptureUnavailable = "unavailable"
	// CaptureUnavailableMessage is the fixed phrase of an unavailable capture.
	CaptureUnavailableMessage = "capture evidence unavailable"

	// ObservationUnit and ObservationEndpoint label the observation
	// section of every pin reading.
	ObservationUnit     = "delivered sessions"
	ObservationEndpoint = "captured-session-end"
	// UnlinkedUnit labels the unlinked section.
	UnlinkedUnit = "feedback items"
)

// The surface names of the audit log. The hook surface is stored as the
// empty string, and the report names it "hook" (reviews.Summarize does
// too). The names equal delivery.Surface*, which lessonreport must not
// import, and the check surface of report.
const (
	surfaceHook      = "hook"
	surfaceChangeSet = "change_set"
	surfacePrepare   = "prepare"
	surfaceExplore   = reviews.ExploreSurface
	surfaceCheck     = "check"
)

// maxErrorRunes bounds a source error text in the report.
const maxErrorRunes = 200

// Build reads the inputs once and returns the delivery/1 report. It is
// pure: no I/O, no clock (Inputs.Now is the time), no cache, and no
// order that depends on a map. A missing or unreadable source is data in
// the report. The only error is a pin reference that names no single pin
// (*reviews.PinRefError), and only when pinRef is not empty.
//
// Build fills the per-version reading: as_of, capture, units, totals,
// one PinReading per config pin with its delivery counts and its
// observation windows, the coverage counts that the audit alone gives,
// and the versions and session windows of an expansion. The report reads
// no capture archive, so the classes, the occurrences, the timing, the
// historical section, and the unlinked feedback stay empty, and
// as_of.archive is a fixed state. Every delivered session reads
// incomplete, because no session is captured. Check operations and guard
// attempts use separate units. Every next step needs a corrected action
// or captured feedback, so the next steps stay empty. An empty value
// never reads like a result. The selection reading states the current
// budgets and whether the audit read.
func Build(in Inputs, pinRef string) (Report, error) {
	return buildWith(in, pinRef, nil)
}

// buildWith is Build with a replacement for the position lookups of the index.
// A test passes a plain walk over the audit to compare both answers. A nil
// replacement keeps the position lists of the index.
func buildWith(in Inputs, pinRef string, scans func(*index) positionScans) (Report, error) {
	var expandIdentity string

	if pinRef != "" {
		identity, err := ResolveRef(in.Config, pinRef)
		if err != nil {
			return Report{}, err
		}

		expandIdentity = identity
	}

	auditRead := auditSource(in)
	checkRead := checkAuditSource(in)
	idx := newIndex(in, auditRead.State, checkRead.State)

	if scans != nil {
		idx.scans = scans(idx)
	}

	an := analyze(idx)
	checks := readChecks(in, checkRead.State)

	report := Report{
		Schema: Schema,
		AsOf: AsOf{
			GeneratedAt: formatTime(in.Now),
			Audit:       auditRead,
			CheckAudit:  checkRead,
			Archive:     archiveRead(),
			Config:      configRead(in),
		},
		Units:      unitTable(),
		Totals:     idx.totals,
		Pins:       []PinReading{},
		Historical: []PinReading{},
		Unlinked:   Unlinked{Unit: UnlinkedUnit, ByReason: map[Code]int{}, NextSteps: []NextStep{}},
		Coverage:   an.coverage,
		Checks:     checks.reading,
		Selection:  selectionReading(in, auditRead),
	}

	report.Capture = captureState(report.AsOf.Archive)

	current := currentPins(in.Config)
	for _, p := range current {
		report.Pins = append(report.Pins, idx.reading(p, an, checks))
	}

	if expandIdentity != "" {
		report.Expanded = expansion(expandIdentity, current, an)
	}

	return report, nil
}

// pinVersion is one version of one pin identity: one entry of the
// current config.
type pinVersion struct {
	ref      string // Config.ShortPinRef
	entry    int    // 1-based config entry
	rule     string // the rule text as configured
	identity string
	version  string
	mode     string // the configured mode as lessoncheck.NormalizeMode reads it
	scope    []string

	// configured is the config entry of the version. Its note is part of
	// how the v1 hook rendered the pin.
	configured *reviews.PinRule
}

// currentPins returns one pinVersion per pin entry of the config, in
// file order. Duplicate entries each get their own reading.
func currentPins(cfg *reviews.Config) []pinVersion {
	if cfg == nil {
		return nil
	}

	out := make([]pinVersion, 0, len(cfg.Pin))
	refs := cfg.ShortPinRefs()

	for i, p := range cfg.Pin {
		key := reviews.NewPinKey(p.Rule, p.Region, p.Regions)

		// The report prints the mode that the checker uses. An omitted or
		// unknown mode reads advisory, so the report never shows an armed
		// check where the checker sees none.
		mode, _ := lessoncheck.NormalizeMode(p.Mode)

		out = append(out, pinVersion{
			ref:      refs[key],
			entry:    i + 1,
			rule:     p.Rule,
			identity: reviews.PinIdentityDigest(key),
			version:  cfg.PinVersion(p),
			mode:     string(mode),
			scope:    lessoncheck.SortedScope(p.AllRegions()),

			configured: &cfg.Pin[i],
		})
	}

	return out
}

// expansion returns the --pin detail of one identity: its versions in
// config file order, and the session windows in the order of the
// versions. ResolveRef reads the config only, so the identity is always
// a current one. The occurrences stay empty, because they need a capture
// archive.
func expansion(identity string, current []pinVersion, an *analysis) *Expansion {
	exp := &Expansion{Versions: []string{}, Occurrences: []OccurrenceDetail{}}

	seen := map[string]bool{}
	add := func(p pinVersion) {
		if p.identity != identity || seen[p.version] {
			return
		}

		seen[p.version] = true
		exp.Versions = append(exp.Versions, p.version)

		if exp.PinRef == "" {
			exp.PinRef = p.ref
		}
	}

	for _, p := range current {
		add(p)
	}

	exp.PinRef = shownPinRef(exp.PinRef, identity)
	exp.Sessions = an.sessions(identity, exp.Versions)

	return exp
}

// shownPinRef returns the printed name of a pin. A configured identity
// prints its pin-ref. An identity that no config entry holds prints the
// shown prefix of its digest. Every surface uses this one rule, so the
// text selection and the expand commands print the same name.
func shownPinRef(ref, identity string) string {
	if ref != "" {
		return ref
	}

	return identity[:min(reviews.ShownPinRef, len(identity))]
}

// reading builds the PinReading of one version from the audit index and
// the window reading, with the check reading of the same version. The
// literal leaves out the observation, because fill always sets it.
func (idx *index) reading(p pinVersion, an *analysis, checks *checkIndex) PinReading {
	key := versionKey{p.identity, p.version}
	reading := PinReading{
		PinRef:      p.ref,
		PinIdentity: p.identity,
		PinVersion:  p.version,
		Entry:       p.entry,
		Rule:        render.Sanitize(p.rule),
		Scope:       p.scope,
		Mode:        p.mode,
		Delivery:    idx.deliveryCounts(p),
		Occurrences: OccurrenceCounts{
			ByClass:  map[PairClass]int{},
			ByOrigin: map[Origin]int{},
		},
		Timing:      map[PairTiming]int{},
		Facts:       map[Fact]int{},
		Reasons:     map[Code]int{},
		Limitations: []Code{},
		NextSteps:   []NextStep{},
		Checks:      checks.pin(key),
	}

	an.fill(&reading, key)

	return reading
}

// formatTime renders a time as RFC 3339 UTC. The zero time renders empty:
// Build has no clock, and a missing time is not a date.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

// sanitizeError bounds and sanitizes an error text for the report.
func sanitizeError(text string) string {
	return render.Truncate(render.Sanitize(text), maxErrorRunes)
}

// auditSource states how the firing log read ended. A prefix with no
// byte read is absent: ReadAuditPrefix gives the same prefix for a
// missing and an empty file, and neither holds a record.
func auditSource(in Inputs) SourceRead {
	p := in.Audit
	out := SourceRead{
		Path:        auditPath,
		Bytes:       p.Bytes,
		Records:     len(p.Lines),
		Legacy:      p.Legacy,
		Malformed:   p.Malformed,
		PartialTail: p.PartialTail,
		NewerSchema: p.NewerSchema,
	}

	switch {
	case in.AuditError != "":
		out = SourceRead{Path: auditPath, State: StateUnreadable, Error: sanitizeError(in.AuditError)}
	case p.OverLimit:
		out.State = StateOverLimit
		out.Generations = 1
	case p.Path == "" || (p.Bytes == 0 && len(p.Lines) == 0):
		out.State = StateAbsent
	default:
		out.State = StateRead
		out.Generations = 1
	}

	return out
}

// checkAuditSource states how the lesson-check stream read ended.
func checkAuditSource(in Inputs) SourceRead {
	p := in.CheckAudit
	out := SourceRead{
		Path:        checkAuditPath,
		Generations: p.Generations,
		Bytes:       p.Bytes,
		Records:     len(p.Records),
		Malformed:   p.Malformed,
		PartialTail: p.PartialTail,
		NewerSchema: p.NewerSchema,
	}

	switch {
	case in.CheckAuditError != "":
		out = SourceRead{Path: checkAuditPath, State: StateUnreadable, Error: sanitizeError(in.CheckAuditError)}
	case p.OverLimit:
		out.State = StateOverLimit
	case p.Generations == 0:
		out.State = StateAbsent
	default:
		out.State = StateRead
	}

	return out
}

// archiveRead is the fixed archive state of the report. This build has
// no capture archive reader, so the state is always absent. The field
// stays, so the delivery/1 document keeps one field set on every build.
func archiveRead() ArchiveRead {
	return ArchiveRead{State: StateAbsent, Error: ArchiveNotReadError}
}

// configRead states how the config read ended. A state that the caller
// sets takes precedence. An empty state derives from the config. The
// guidance digest and the pin count come from the config when the
// caller left them out.
func configRead(in Inputs) ConfigRead {
	out := in.ConfigRead

	if out.State == "" {
		out.State = ConfigOK
		if in.Config == nil {
			out.State = StateAbsent
		}
	}

	if in.Config != nil {
		if out.GuidanceSHA256 == "" {
			out.GuidanceSHA256 = in.Config.GuidanceDigest()
		}

		if out.Pins == 0 {
			out.Pins = len(in.Config.Pin)
		}
	}

	return out
}

// captureState maps the archive state to the capture state.
func captureState(a ArchiveRead) CaptureState {
	if a.State == StateRead {
		return CaptureState{State: CaptureAvailable}
	}

	reason := a.State
	if reason == StateAbsent {
		reason = "no-archive"
	}

	return CaptureState{State: CaptureUnavailable, Reason: reason, Message: CaptureUnavailableMessage}
}
