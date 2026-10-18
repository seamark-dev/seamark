package services

import (
	"bytes"
	"strconv"
	"time"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The selection sample (R-10, contracts §3.5). A sample runs the lesson
// selection of one surface for explicit paths, twice: under the saved
// configuration and under the candidate of one review. It reads the
// evidence once and folds it with the production folds of
// internal/report, so a sample and the surface cannot disagree. It never
// emits advice, records a firing, or leases or resets delivery state. It
// never opens the index for a write, and it never builds the index.

// SampleSurface names the surface whose selection a sample runs.
type SampleSurface int

const (
	// SampleHook is the selection of one edit-hook injection: the hook
	// names its paths with delivery.NormalizePaths and selects with the
	// hook budget.
	SampleHook SampleSurface = iota
	// SampleChangeSet is the selection of one change_set answer or one
	// check advisory: change_set names its paths with
	// report.ChangeSetNames and selects with the change budget.
	SampleChangeSet
)

// String names the surface for a label.
func (s SampleSurface) String() string {
	switch s {
	case SampleHook:
		return "hook selection"
	case SampleChangeSet:
		return "change-set selection"
	default:
		// A value outside the enumeration is a programming error. The
		// number stays visible, so a label shows the bad value.
		return "SampleSurface(" + strconv.Itoa(int(s)) + ")"
	}
}

// SampleRequest is one sample: a surface, the explicit paths, and the
// two lessons.yaml texts of one review. Before is the file text that
// the review starts from, and After is its candidate: AuthorPlan.Before
// and After, or settings.Plan.Bytes. No other text reaches a sample, so
// a sample never combines the unsaved drafts of two reviews.
type SampleRequest struct {
	Surface SampleSurface
	// Paths are the paths to sample, in the order of the user. They are
	// user text.
	Paths []string
	// Before and After are lessons.yaml texts. Before also decides Stale.
	Before, After []byte
}

// SampleView is the result of one sample. Either Unavailable is set, or
// Current and Candidate both hold a computed selection. A view never
// shows an unavailable sample as two empty selections. Sample states the
// pairing rule of the two selections.
//
// Paths, Rejected, Unindexed, NoFile, and Unavailable hold user, file,
// or index text: a screen sanitizes them before display.
type SampleView struct {
	Surface SampleSurface
	// Paths are the files that the selection uses, named by the rule of
	// the surface. The hook gives its normalized files. change_set gives
	// one name per requested path, repeats kept. Paths is nil when the
	// sample is unavailable before the naming.
	Paths []string
	// Rejected are the paths that the hook rejects, as NormalizePaths
	// keeps them: at most its first few. change_set rejects no path.
	Rejected []delivery.RejectedPath
	// Unindexed are the entries of Paths that the index does not know,
	// by the rule of report.ChangeSetNames, for both surfaces. They are
	// a label only: a new file still matches by path.
	Unindexed []string
	// NoFile are the entries of Paths that name no existing file or
	// directory, a future path for example. A relative entry resolves
	// against the effective root. They are a label only: a lesson can
	// match a path that does not exist yet.
	NoFile []string
	// Current is the selection under Before, Candidate under After.
	Current, Candidate report.Selection
	// CurrentThreshold and CandidateThreshold are the thresholds of the
	// two configurations, as the lessons reader decodes them. An
	// exclusion says only that a lesson is below the threshold, so a
	// view needs the number to name it. Both are zero until both texts
	// parse.
	CurrentThreshold, CandidateThreshold int
	// Unavailable states why the sample computes no selection, as one
	// text that starts with "Sample unavailable: ". It is empty when the
	// sample computes both selections.
	Unavailable string
	// IndexedAt is the time of the last index run; zero when unknown.
	// ReadAt is the start time of the evidence read; zero when no read
	// succeeded.
	IndexedAt, ReadAt time.Time
	// Stale is true when lessons.yaml under the effective root no longer
	// holds Before. The saved side of the sample is then not the disk.
	Stale bool
}

// Texts of an unavailable sample. They state the reason in words, so a
// screen never shows an unavailable sample as zero lessons (F10-labels).
const (
	sampleUnavailable = "Sample unavailable: "
	// sampleNoIndex is the text of an absent index. The hook then reads
	// pins through an empty in-memory index, and a sample does not model
	// that fallback.
	sampleNoIndex = "no readable index. Without an index the hook still delivers pins only; " +
		"this sample does not model that."
	sampleNoPaths      = "no paths. Add one or more paths; no paths never means the whole repository."
	sampleNoUsablePath = "no usable path: the hook rejects every path."
)

// Sample runs the selection of one surface for the paths of req under
// the configurations Before and After. It needs a ready index of this
// workspace. Every other state of the index, a foreign root, no paths,
// or a text that the lessons reader rejects gives Unavailable, never an
// empty selection. It writes nothing and takes no lock.
//
// The sample reads the evidence once with report.ReadEvidence for both
// configurations, then folds it twice with the selection of the surface.
// The production hook and change_set use the same folds with the same
// budgets, so Current equals the production selection for the paths.
// Unlike the production surfaces, a sample never falls back: no
// in-memory index for an absent index, and no default configuration for
// a rejected text.
//
// Pairing rule for a view (hand-off to B4): change_set keeps one pin per
// identity, the pin of ordinal 0. A later pin with the same identity is
// in no list and in no count of report.Selection. So a view must never
// label such a pin "removed" or "excluded". A view pairs pins by
// reviews.NewPinKey and ordinal, and mined lessons by ClusterKey.
func (e *engine) Sample(req SampleRequest) SampleView {
	view := SampleView{Surface: req.Surface}

	st, probe, err := e.openRead()
	if err == nil {
		defer func() { _ = st.Close() }()
	}

	view.IndexedAt = indexedAt(probe)
	view.Stale = lessonsChanged(probe.Root, req.Before)

	if reason := e.sampleBlock(req, probe, err); reason != "" {
		view.Unavailable = sampleUnavailable + reason

		return view
	}

	cur, err := reviews.ParseConfig(req.Before)
	if err != nil {
		view.Unavailable = sampleUnavailable + "the saved configuration cannot be read: " + err.Error()

		return view
	}

	cand, err := reviews.ParseConfig(req.After)
	if err != nil {
		view.Unavailable = sampleUnavailable + "the configuration of this review cannot be read: " + err.Error()

		return view
	}

	view.CurrentThreshold, view.CandidateThreshold = cur.Threshold, cand.Threshold
	view.Paths, view.Rejected, view.Unindexed = e.sampleNames(st, probe.Root, req)
	view.NoFile = integration.Missing(probe.Root, view.Paths)

	if len(view.Paths) == 0 {
		view.Unavailable = sampleUnavailable + sampleNoUsablePath

		return view
	}

	readAt := e.deps.Now()

	// One read serves both configurations. ReadEvidence reads the cluster
	// citations of the pins that are live in either one, so both folds
	// see the same observation of the index.
	ev, err := report.ReadEvidence(st, view.Paths, cur, cand)
	if err != nil {
		view.Unavailable = sampleUnavailable + "the evidence read failed: " + err.Error()

		return view
	}

	view.ReadAt = readAt

	if err := sampleSelect(ev, req.Surface, cur, cand, &view); err != nil {
		view.Unavailable = sampleUnavailable + "the selection failed: " + err.Error()
	}

	return view
}

// sampleBlock returns why a sample cannot run before the configurations
// are read, or "". The order puts the reason that a new path cannot fix
// first: the surface, the index, and the root. readErr is the error of
// openRead.
func (e *engine) sampleBlock(req SampleRequest, probe workspace.IndexProbe, readErr error) string {
	switch {
	case req.Surface != SampleHook && req.Surface != SampleChangeSet:
		return "unknown surface " + req.Surface.String() + "."
	case readErr != nil || probe.State != workspace.IndexReady:
		return sampleIndexReason(probe.State)
	}

	if err := e.foreignRoot(probe); err != nil {
		return err.Error() + "."
	}

	if len(req.Paths) == 0 {
		return sampleNoPaths
	}

	return ""
}

// sampleIndexReason names an index state that a sample cannot read. Only
// a ready index holds the mined evidence and opens read-only. A newer or
// an older schema refuses a read-only open. A database that was never
// indexed holds no mined lessons.
func sampleIndexReason(state workspace.IndexState) string {
	switch state {
	case workspace.IndexAbsent:
		return sampleNoIndex
	case workspace.IndexNewer:
		return "the index is newer: a newer seamark wrote its schema, and this seamark cannot read it."
	case workspace.IndexOlder:
		return "the index is older: its schema needs the migration of an index build, and a sample never builds the index."
	case workspace.IndexNotIndexed:
		return "the index is not indexed: it holds decisions but no mined evidence."
	default:
		return "the index is " + state.String() + "."
	}
}

// sampleNames names the paths of req by the rule of its surface. The
// hook resolves its paths against the effective root, as the production
// hook does with the recorded root. change_set names its paths against
// the workspace root, as the MCP server does. The two roots name one
// directory here, because sampleBlock refuses a foreign root.
// Unindexed comes from the change_set rule for both surfaces, and it
// never changes a name.
func (e *engine) sampleNames(st *store.Store, root string, req SampleRequest,
) (files []string, rejected []delivery.RejectedPath, unindexed []string) {
	if req.Surface == SampleChangeSet {
		names, unknown := report.ChangeSetNames(st, e.ws.Root, req.Paths)

		return names, nil, unknown
	}

	normalized := delivery.NormalizePaths(root, root, req.Paths)
	_, unindexed = report.ChangeSetNames(st, e.ws.Root, normalized.Files)

	return normalized.Files, normalized.Rejected, unindexed
}

// sampleSelect folds the evidence with the selection of surface under
// both configurations, and stores the results in view. Both configs are
// the pointers that ReadEvidence read for, so neither fold can miss a
// citation.
func sampleSelect(ev *report.Evidence, surface SampleSurface, cur, cand *reviews.Config, view *SampleView) error {
	selection := (*report.Evidence).HookSelection
	if surface == SampleChangeSet {
		selection = (*report.Evidence).ChangeSetSelection
	}

	var err error

	if view.Current, err = selection(ev, cur); err != nil {
		return err
	}

	view.Candidate, err = selection(ev, cand)

	return err
}

// lessonsChanged reports whether lessons.yaml under root no longer holds
// the bytes before. It reads through integration.ReadInput, so it
// follows a link as the editors do. An absent file reads as no bytes,
// so it matches the empty Before of a plan that creates the file
// (P4-9). lessonsChanged does not use integration.Landed, because
// Landed reads an absent file as other bytes. A read error counts as
// changed, because then nothing proves that the file holds before.
func lessonsChanged(root string, before []byte) bool {
	_, data, err := integration.ReadInput(root, pinfile.File)

	return err != nil || !bytes.Equal(data, before)
}

// indexedAt returns the time of the last index run of probe, or the zero
// time when the probe does not know it.
func indexedAt(probe workspace.IndexProbe) time.Time {
	if probe.IndexedAt <= 0 {
		return time.Time{}
	}

	return time.Unix(probe.IndexedAt, 0)
}
