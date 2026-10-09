package learning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// Trigger extraction in the console is two confirmed steps, as
// distillation is. PlanExtract builds a read-only preview of the
// explicitly selected proposals, and RunExtract sends exactly that
// preview. The CLI calls distill.ExtractTriggers, which runs the same
// plan and execute steps with the store checks and the guarded save
// turned off (D-1). Both paths share the question rule, the batches, the
// prompt builder, and the executor.
//
// The console excludes more rows than the CLI. A row is asked only when
// the pin file and the ledger agree about it: an applied row needs
// exactly one linked pin, and a pending row must not already be in the
// file. Both link rules come from pinfile.Classify, so the Pins view and
// the extraction preview agree. A third rule excludes a row whose two
// stored region fields disagree, in any status. Classify applies that
// rule to applied rows only, so the exclusion also calls its predicate,
// pinfile.FieldsAgree, for pending rows.
//
// A lessons.yaml that does not read or parse refuses the whole preview
// with an *ExtractConfigError (contract §2.3b, strict configuration).
// The "pin file unreadable" exclusion of D-5 then never runs; it stays
// as a second guard, because an error is never an absence.

var (
	// ErrExtractNotReady reports an index that is absent, not indexed,
	// or incompatible. The console links to the explicit build or
	// repair action; an extraction never builds the index.
	ErrExtractNotReady = errors.New("the index is not ready for trigger extraction")
	// ErrExtractConfig marks a configuration reader error. A paid run
	// must not use a default that the user did not review.
	// ExtractConfigError names the file.
	ErrExtractConfig = errors.New("the trigger extraction configuration is invalid")
	// ErrExtractBlocked reports a run of a preview with blockers. A
	// blocked preview discloses; it never authorizes an agent call.
	ErrExtractBlocked = errors.New("the trigger extraction preview is blocked")
	// ErrNothingToExtract reports a run of a preview without an eligible
	// proposal. No agent starts.
	ErrNothingToExtract = errors.New("the preview has no eligible proposal")
)

// pinCarried is the exclusion reason of a pending row whose pin is
// already in the file. The ledger must record the apply first, because
// an answer would otherwise change the scope of a pending row only.
const pinCarried = "a pin already carries it; record it as applied first (Proposals › c)"

// extGetwd reads the console's working directory. Tests replace it to
// simulate a failure or a changed directory.
var extGetwd = os.Getwd

// ExtractConfigError is a configuration reader error that blocks an
// extraction preview. Path lets the console link to the file or to the
// matching Settings group.
type ExtractConfigError struct {
	// Path is repository-relative: .seamark/config.yaml or
	// .seamark/lessons.yaml.
	Path string
	// Err is the reader error, with its original text.
	Err error
}

// Error names the file and the reader error.
func (e *ExtractConfigError) Error() string {
	return e.Path + ": " + e.Err.Error()
}

// Unwrap matches both ErrExtractConfig and the reader error, for example
// reviews.ErrLessonsConfig.
func (e *ExtractConfigError) Unwrap() []error {
	return []error{ErrExtractConfig, e.Err}
}

// ExtractRequest is the selection the user made for one extraction.
// There is no "all": an empty selection plans no agent call.
type ExtractRequest struct {
	// IDs are the explicitly selected proposals.
	IDs []int64
	// Skipped are selected Pins entries without exactly one linked
	// proposal, with the reason. The preview lists them as ineligible.
	// An ID here that IDs also holds is not asked, because the selecting
	// surface already found it ineligible.
	Skipped []distill.ExtractSkip
}

// ExtractPreview is the read-only preview of one extraction. It is what
// the user confirms, and RunExtract sends only this preview.
type ExtractPreview struct {
	// Request is the selection as the user made it, so "prepare remaining"
	// can plan again with the same choices.
	Request ExtractRequest
	// Plan holds the frozen batches, their exact prompts, and the
	// selected proposals that are not asked.
	Plan *distill.ExtractPlan
	// Invocation is the resolved agent command.
	Invocation agent.CommandSpec
	// WorkDir is where the agent process runs: Invocation.Dir, or the
	// console's working directory when Dir is empty. It can differ from
	// RepoRoot (seamark -C), so a surface discloses it.
	WorkDir string
	// Config and Lessons observe config.yaml and lessons.yaml. A run
	// refuses the preview when either file changed.
	Config  integration.FileGuard
	Lessons integration.FileGuard
	// RepoRoot is the root that the index records; DBPath the database.
	RepoRoot string
	DBPath   string
	// LiveNotes holds the live pin note of each planned applied row whose
	// linked pin has another note than the recorded proposal. The prompt
	// sends the recorded note, so a surface shows both. The values are
	// file text: a surface sanitizes them. Nil means no note differs.
	LiveNotes map[int64]string
	// Blockers are safe display lines that stop a run, for example a
	// missing agent executable. The preview still discloses the plan.
	Blockers []string
	// WorkDirErr is the error that kept PlanExtract from reading the
	// console's working directory, or nil. Blockers then also holds its
	// display line. A surface uses IsWorkDirBlocker to tell that line
	// from an agent blocker, because a setting cannot fix it.
	WorkDirErr error
	// PlannedAt is when the plan was made.
	PlannedAt time.Time
}

// IsWorkDirBlocker reports whether line is the blocker of WorkDirErr.
// The agent runs in the console's working directory, so only a restart
// from a readable directory removes this blocker. The check compares
// the line that PlanExtract built, so a surface never parses the text.
func (p *ExtractPreview) IsWorkDirBlocker(line string) bool {
	return p.WorkDirErr != nil && line == workDirBlocker(p.WorkDirErr)
}

// workDirBlocker returns the safe display line of a working-directory
// error. PlanExtract and IsWorkDirBlocker share it, so both build the
// same line.
func workDirBlocker(err error) string {
	return distSafe(fmt.Sprintf("the agent's working directory is unknown: %v", err))
}

// NoOp reports a preview without an eligible proposal. The console shows
// the ineligible selection and offers no start.
func (p *ExtractPreview) NoOp() bool {
	return p.Plan == nil || len(p.Plan.Batches) == 0
}

// Ineligible returns every selected item that the run does not ask: the
// request's skipped Pins entries first, in request order, then the
// plan's ineligible proposals, in ascending ID order. It returns a copy.
func (p *ExtractPreview) Ineligible() []distill.ExtractSkip {
	out := slices.Clone(p.Request.Skipped)

	if p.Plan != nil {
		out = append(out, p.Plan.Ineligible...)
	}

	return out
}

// PlanExtract previews one extraction of the selected proposals. It reads
// the index through a read-only store, never runs the agent, and never
// stamps a row. It checks only that the agent executable is on PATH. A
// malformed config.yaml or lessons.yaml returns an *ExtractConfigError
// that names the file, and no preview: a paid run must not use a pin
// file that the user cannot see. It does not read distill.write, because
// extraction never writes a pin.
func PlanExtract(ws workspace.Workspace, reg *integration.Registry, req ExtractRequest) (*ExtractPreview, error) {
	probe := workspace.Probe(ws)
	if probe.State != workspace.IndexReady {
		return nil, distNotReady(ErrExtractNotReady, probe)
	}

	root := probe.Root

	if reg == nil {
		reg = integration.Builtin()
	}

	in, err := distReadInputs(reg, root, func(path string, err error) error {
		return &ExtractConfigError{Path: path, Err: err}
	})
	if err != nil {
		return nil, err
	}

	b := in.bind(distill.ErrExtractStale, root, ws.DBPath)

	st, now, err := workspace.OpenReadOnly(ws)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrExtractNotReady, err)
	}
	defer func() { _ = st.Close() }()

	if now.State != workspace.IndexReady || now.Root != root {
		return nil, fmt.Errorf("%w: the index changed while planning; prepare again", distill.ErrExtractStale)
	}

	view, err := observePins(st, root, b.lessons)
	if err != nil {
		return nil, err
	}

	// The plan reads the selected rows again. A row that changed between
	// the two reads was classified in another state, so the preview
	// would describe neither state.
	var changed []string

	plan, err := distill.NewExtractPlan(st, distill.ExtractPlanOptions{
		IDs:  withoutSkipped(req.IDs, req.Skipped),
		Root: root,
		Spec: b.invocation,
		Exclude: func(p model.Proposal) (string, bool) {
			if !view.current(p) {
				changed = append(changed, fmt.Sprintf("p%d", p.ID))

				return "changed while planning", true
			}

			return view.exclusion(p)
		},
	})
	if err != nil {
		return nil, err
	}

	if len(changed) > 0 {
		return nil, fmt.Errorf("%w: the ledger changed while planning (%s); prepare again",
			distill.ErrExtractStale, strings.Join(changed, ", "))
	}

	// The bound files must be the files the plan read. A write during
	// planning would leave the preview describing neither version.
	if err := distCheckFiles(b); err != nil {
		return nil, err
	}

	p := &ExtractPreview{
		Request:    ExtractRequest{IDs: slices.Clone(req.IDs), Skipped: slices.Clone(req.Skipped)},
		Plan:       plan,
		Invocation: b.invocation,
		WorkDir:    b.invocation.Dir,
		Config:     b.config,
		Lessons:    b.lessons,
		RepoRoot:   root,
		DBPath:     ws.DBPath,
		LiveNotes:  view.liveNotes(plan.IDs()),
		PlannedAt:  plan.PlannedAt,
	}

	// An empty Dir runs the agent in the console's directory. The
	// preview must name that directory, or the run is not disclosed.
	if p.WorkDir == "" {
		if p.WorkDir, err = extGetwd(); err != nil {
			p.WorkDirErr = err
			p.Blockers = append(p.Blockers, workDirBlocker(err))
		}
	}

	// Only PATH is checked: running a configured command, even with
	// --version, could start a login flow or spend money.
	if _, err := exec.LookPath(b.invocation.Argv[0]); err != nil {
		p.Blockers = append(p.Blockers,
			distSafe(fmt.Sprintf("agent cli %q not found on PATH", b.invocation.Argv[0])))
	}

	return p, nil
}

// RunExtract executes a confirmed preview. Before any work it checks the
// workspace identity, both bound files, the resolved invocation, the
// working directory, every planned row with its living evidence, and the
// pin links of the planned rows. A changed input returns an error that
// wraps distill.ErrExtractStale (and so distill.ErrStale) and starts
// nothing. It then calls distill.ExecuteExtract, which repeats the file,
// identity, and link checks before each batch through its Check option.
//
// A nil inv builds the invoker from the preview's invocation; tests pass
// a fake. That invoker starts the agent in its own process group, so a
// stop also ends a wrapper script and its model call. The execution is
// nil when the run is refused before it starts; otherwise it is the
// typed result, also after a stop.
func RunExtract(ctx context.Context, ws workspace.Workspace, reg *integration.Registry, p *ExtractPreview,
	inv agent.Invoker, onEvent func(distill.ExtractEvent),
) (*distill.ExtractExecution, error) {
	if p == nil || p.Plan == nil {
		return nil, errors.New("extract: no preview to run")
	}

	if len(p.Blockers) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrExtractBlocked, strings.Join(p.Blockers, "; "))
	}

	if p.NoOp() {
		return nil, ErrNothingToExtract
	}

	b := p.binding()

	if err := p.checkInputs(ws); err != nil {
		return nil, err
	}

	if reg == nil {
		reg = integration.Builtin()
	}

	if err := distCheckInvocation(reg, b); err != nil {
		return nil, err
	}

	if inv == nil {
		command, err := agent.NewGroupCommand(p.Invocation)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrExtractBlocked, err)
		}

		inv = command
	}

	st, err := store.Open(ws.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	if err := p.Plan.Revalidate(st); err != nil {
		return nil, err
	}

	if err := p.checkLinks(st, p.Plan.Batches); err != nil {
		return nil, err
	}

	// next is the first batch whose call has not started. Before each
	// batch the link check reads only that batch, as the row checks of
	// the plan do. A row that an earlier batch answered is done, and its
	// stored pending regions can change its own link without a conflict.
	// Check and OnEvent run on the executing goroutine, so no lock is
	// needed.
	next := 0

	return distill.ExecuteExtract(ctx, st, inv, p.Plan, distill.ExtractExecuteOptions{
		Boundary: true,
		Check: func() error {
			if err := p.checkInputs(ws); err != nil {
				return err
			}

			return p.checkLinks(st, p.Plan.Batches[next:min(next+1, len(p.Plan.Batches))])
		},
		OnEvent: func(ev distill.ExtractEvent) {
			if ev.Kind == distill.ExtractBatchStarted {
				next = ev.Index + 1
			}

			if onEvent != nil {
				onEvent(ev)
			}
		},
	})
}

// binding returns what the extraction preview binds.
func (p *ExtractPreview) binding() inputBinding {
	return inputBinding{
		stale:      distill.ErrExtractStale,
		repoRoot:   p.RepoRoot,
		dbPath:     p.DBPath,
		invocation: p.Invocation,
		config:     p.Config,
		lessons:    p.Lessons,
	}
}

// checkInputs checks the workspace identity, both bound files, and the
// working directory of the agent. These checks are cheap, so a run
// repeats them before each batch.
func (p *ExtractPreview) checkInputs(ws workspace.Workspace) error {
	if err := distCheckInputs(ws, p.binding()); err != nil {
		return err
	}

	// A set Dir is part of the invocation, which its own check binds.
	if p.Invocation.Dir != "" {
		return nil
	}

	cwd, err := extGetwd()
	if err != nil {
		return fmt.Errorf("%w: the working directory of the agent: %w", distill.ErrExtractStale, err)
	}

	if cwd != p.WorkDir {
		return fmt.Errorf("%w: the working directory of the agent changed since the preview", distill.ErrExtractStale)
	}

	return nil
}

// checkLinks classifies the planned rows of batches against the current
// pin file and ledger. A row that the pin file now excludes, for example
// because another applied row made its pin ambiguous, stops the run. A
// row that another writer changed is left to the row checks of the plan,
// which name the change more precisely.
func (p *ExtractPreview) checkLinks(st *store.Store, batches []distill.ExtractBatch) error {
	view, err := observePins(st, p.RepoRoot, p.Lessons)
	if err != nil {
		return err
	}

	var reasons []string

	for _, batch := range batches {
		for _, row := range batch.Proposals {
			if !view.current(row) {
				continue
			}

			if reason, excluded := view.exclusion(row); excluded {
				reasons = append(reasons, fmt.Sprintf("p%d is excluded now: %s", row.ID, reason))
			}
		}
	}

	if len(reasons) > 0 {
		return fmt.Errorf("%w: %s", distill.ErrExtractStale, strings.Join(reasons, "; "))
	}

	return nil
}

// pinView is one observation of the pin file and of the pending and
// applied rows. The extraction exclusions read it.
type pinView struct {
	doc   *pinfile.Document
	links *pinfile.Links
	// rows holds the pending and applied rows by ID, as Classify saw them.
	rows map[int64]model.Proposal
}

// observePins reads the pin file under root and the pending and applied
// rows, then classifies them. The file must match the guard lessons,
// because the preview binds that observation. A read error of the file is
// a configuration error, never an empty pin list.
func observePins(st *store.Store, root string, lessons integration.FileGuard) (*pinView, error) {
	doc, err := pinfile.Read(root)
	if err != nil {
		return nil, &ExtractConfigError{Path: pinfile.File, Err: err}
	}

	if doc.Guard != lessons {
		return nil, fmt.Errorf("%w: %s changed since the preview", distill.ErrExtractStale, pinfile.File)
	}

	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return nil, err
	}

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return nil, err
	}

	v := &pinView{
		doc:   doc,
		links: pinfile.Classify(doc, applied, pending, nil),
		rows:  make(map[int64]model.Proposal, len(applied)+len(pending)),
	}

	for _, row := range slices.Concat(applied, pending) {
		v.rows[row.ID] = row
	}

	return v, nil
}

// current reports whether the view holds row p with the same status,
// identity, and regions. Only then does the view's link describe p.
func (v *pinView) current(p model.Proposal) bool {
	now, ok := v.rows[p.ID]

	return ok && store.NewRowGuard(p).Matches(now)
}

// exclusion returns why the pin file excludes row p from a trigger
// question, and true; or "" and false. The caller checks current first.
// An applied row needs exactly one linked pin, and a pending row must
// not be in the file already. A row whose two stored region fields
// disagree has no provable pin key, so it is excluded in both states.
func (v *pinView) exclusion(p model.Proposal) (string, bool) {
	if !pinfile.FieldsAgree(p) {
		return pinfile.LinkAmbiguousFields.Reason(), true
	}

	link := v.links.ByProposal[p.ID]

	switch {
	case p.Status == model.ProposalApplied && link != pinfile.LinkLinked:
		return link.Reason(), true
	case p.Status == model.ProposalProposed && link == pinfile.LinkPendingMatch:
		return pinCarried, true
	case p.Status == model.ProposalProposed && link == pinfile.LinkUnknown:
		// An unreadable file is not an absence: no row is known to be
		// outside the file.
		return link.Reason(), true
	}

	return "", false
}

// liveNotes returns the live note of each planned row whose linked pin
// has another note than the row records. It returns nil when no planned
// row differs.
func (v *pinView) liveNotes(planned []int64) map[int64]string {
	var notes map[int64]string

	for i, el := range v.links.Entries {
		if el.Link != pinfile.LinkLinked || !el.NoteChanged || !slices.Contains(planned, el.Applied[0].ID) {
			continue
		}

		if notes == nil {
			notes = map[int64]string{}
		}

		notes[el.Applied[0].ID] = v.doc.Entries[i].Note
	}

	return notes
}

// withoutSkipped returns ids without the IDs of skipped items. ID 0
// marks a Pins entry without a proposal and matches no ID.
func withoutSkipped(ids []int64, skipped []distill.ExtractSkip) []int64 {
	skip := map[int64]bool{}

	for _, s := range skipped {
		if s.ID != 0 {
			skip[s.ID] = true
		}
	}

	var out []int64

	for _, id := range ids {
		if !skip[id] {
			out = append(out, id)
		}
	}

	return out
}
