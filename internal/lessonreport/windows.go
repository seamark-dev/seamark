package lessonreport

import "strconv"

// This file reads the observation window of each delivered session. A
// window can read linked or no-linked-correction only from a captured
// session, and this build reads no capture archive. So every delivered
// session reads incomplete, with the reason session-not-captured and an
// unknown timing. The count still shows how many delivered sessions have
// no reading.

// NoLinkedCorrectionText is the fixed phrase for the qualified negative
// observation. A surface that prints the partition uses it.
const NoLinkedCorrectionText = "no linked correction observed (qualified)"

// The partitions of a window.
const (
	PartitionLinked             = "linked"
	PartitionNoLinkedCorrection = "no-linked-correction"
	PartitionIncomplete         = "incomplete"
)

// The reason codes of an incomplete window. A session that the report
// cannot read is not captured. The code unassembled-evidence names a
// captured session whose evidence is not assembled. No reading of this
// build carries it, but the value stays in the closed list of delivery/1.
const (
	CodeSessionNotCaptured  Code = "session-not-captured"
	CodeUnassembledEvidence Code = "unassembled-evidence"
)

// windowResult is the observation of one pin version and its sessions.
type windowResult struct {
	observation Observation
	sessions    []SessionWindow
}

// windowBuilder reads the windows of every pin version from the audit
// index. It caches one result per pin version.
type windowBuilder struct {
	idx     *index
	results map[versionKey]*windowResult
}

func newWindowBuilder(idx *index) *windowBuilder {
	return &windowBuilder{idx: idx, results: map[versionKey]*windowResult{}}
}

// of returns the window reading of a pin version.
func (w *windowBuilder) of(key versionKey) *windowResult {
	if cached, ok := w.results[key]; ok {
		return cached
	}

	out := &windowResult{
		observation: Observation{
			Unit:               ObservationUnit,
			IncompleteByReason: map[Code]int{},
			ByTiming:           map[Timing]Partition{},
			Endpoint:           ObservationEndpoint,
		},
		sessions: []SessionWindow{},
	}

	for _, o := range w.openings(key.version) {
		s := w.session(o, key)
		out.sessions = append(out.sessions, s)
		out.observation.add(s)
	}

	w.results[key] = out

	return out
}

// add counts one session window in the partition and in its timing row.
func (o *Observation) add(s SessionWindow) {
	p := o.ByTiming[s.Timing]

	switch s.Partition {
	case PartitionLinked:
		o.Linked++
		p.Linked++
	case PartitionNoLinkedCorrection:
		o.NoLinkedCorrection++
		p.NoLinkedCorrection++
	default:
		o.Incomplete++
		p.Incomplete++

		for _, r := range s.Reasons {
			o.IncompleteByReason[r]++
		}
	}

	o.ByTiming[s.Timing] = p
}

// opening is the first injection of a pin version in a delivered session.
type opening struct {
	digest string // session digest of the audit
	pos    int    // line position of the opening injection
}

// openings lists the delivered sessions of a version in the file order of
// their opening injections. A session needs an injection of the version
// from the hook or the exploration surface. A call that the guard denied
// opens no window: the attempt never ran.
func (w *windowBuilder) openings(version string) []opening {
	var out []opening

	seen := map[string]bool{}

	for _, i := range w.idx.byVersion[version] {
		f := w.idx.lines[i].Firing
		if seen[f.SessionSHA] || !w.idx.opensSession(f, version) {
			continue
		}

		seen[f.SessionSHA] = true

		out = append(out, opening{digest: f.SessionSHA, pos: i})
	}

	return out
}

// session reads the window of one delivered session. No session is
// captured in this build, so the window is incomplete and its timing is
// unknown.
func (w *windowBuilder) session(o opening, key versionKey) SessionWindow {
	open := w.idx.lines[o.pos].Firing
	lane := open.Lane

	if open.ContextSHA == "" || lane == "" {
		lane = laneUnknown
	}

	return SessionWindow{
		SessionRef: shortRef(o.digest), PinVersion: key.version, Lane: lane,
		Partition: PartitionIncomplete, Timing: TimingUnknown, Reasons: []Code{CodeSessionNotCaptured},
		StartRecord: auditFileName + ":" + strconv.Itoa(w.idx.lines[o.pos].Line),
	}
}
