package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/scaffold"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ErrBusy reports a second mutation while one runs. The console runs
// one mutation at a time and never queues a duplicate submission.
var ErrBusy = errors.New("another operation is running")

// ErrNoLedger reports a ledger read without a readable index database.
var ErrNoLedger = errors.New("no readable index database: proposals live in the index")

// All is the console's view of the shared engines. Every method is
// synchronous; the shell calls them from background commands, never
// from Update or View. Read methods never write.
//
// All embeds one interface per area, and each area matches the screens
// that use it. A worker that needs one area takes only that area, so
// its signature shows which engine calls it can make. The split stops
// at the workers. The shared state keeps the whole All, because the
// shell loads sources from several areas. Every screen holds that
// state, so the compiler does not limit a screen to its own area. A
// screen narrows the value itself before it starts a worker, for
// example var svc services.Learning = d.st.svc.
type All interface {
	Reader
	Setup
	Decisions
	Learning
	Maintenance
	Settings
}

// Reader is the shared read side of the workspace: the paths, the
// diagnostics, the index, the integrations, the ledger and the delivery
// statistics. The shell and most screens use it. Its methods never
// write, so a helper that takes only a Reader cannot change the
// workspace.
type Reader interface {
	// Root returns the effective repository root.
	Root() string
	// DBPath returns the index database path.
	DBPath() string
	// Version returns the seamark version.
	Version() string

	// Fresh reports a workspace without any Seamark state: no index
	// database, no .seamark directory, and no recognized integration
	// artifact.
	Fresh() (bool, error)
	// Doctor runs the installation checks of `seamark doctor`.
	Doctor() *doctor.Report
	// Index probes the index and gathers semantic health when ready.
	Index() IndexView
	// Integrations inspects every registered client.
	Integrations() []integration.Inspection
	// Clients returns the registered client descriptors.
	Clients() []integration.Client
	// AgentsOnPath returns the IDs of the registered clients whose agent
	// command is on PATH. It never runs an agent.
	AgentsOnPath() []string
	// Ledger reads the proposal decision ledger.
	Ledger() (*proposals.Ledger, error)
	// Stats reads the lesson delivery statistics.
	Stats() lessonstats.Snapshot
}

// Setup covers the integration setup and the local index build. The
// first-run flow, the setup wizard and the Overview build use it. These
// flows install Seamark in the workspace, so they share one area.
type Setup interface {
	// PlanSetup plans an integration change. It writes nothing.
	PlanSetup(setups []integration.ClientSetup) (*SetupPreview, error)
	// ApplySetup applies exactly the reviewed plan.
	ApplySetup(p *SetupPreview) (integration.SetupResult, error)
	// BuildIndex runs a local index build. It cannot be cancelled.
	BuildIndex(force bool, progress func(IndexEvent)) (*index.Summary, error)
}

// Decisions covers the proposal decisions: a preview, then a commit of
// the reviewed preview. Lessons › Proposals uses it.
type Decisions interface {
	// PreviewDecision describes a proposal decision. It writes nothing.
	PreviewDecision(action proposals.Action, ids []int64) (*proposals.Preview, error)
	// CommitDecision revalidates and applies a reviewed decision.
	CommitDecision(p *proposals.Preview) (proposals.Result, error)
}

// Learning covers the learning workflow: the evidence snapshot, the
// evidence refresh, the distillation, the stale-pending cleanup and the
// trigger extraction. Lessons › Evidence and Lessons › Run use it. Its
// runs call GitHub or the agent, so each run takes a context that
// stops it.
type Learning interface {
	// Learning reads the stored evidence, the review target, and the
	// review window. It runs local git only; it never fetches or writes.
	Learning() learning.Snapshot

	// PlanEvidence previews an evidence refresh in mode. It fetches
	// nothing and writes nothing.
	PlanEvidence(mode index.SourceMode) (*learning.Plan, error)
	// RunEvidence runs exactly the reviewed plan. A stop through ctx
	// ends the GitHub fetch; progress runs on the worker goroutine.
	RunEvidence(ctx context.Context, p *learning.Plan, progress func(index.RefreshEvent)) (learning.Outcome, error)
	// PlanDistill previews a distillation. It never runs the agent and
	// never writes.
	PlanDistill(req learning.DistillRequest) (*learning.DistillPreview, error)
	// RunDistill runs exactly the reviewed preview. A stop through ctx
	// starts no new group; progress runs on the worker goroutine.
	RunDistill(ctx context.Context, p *learning.DistillPreview, progress func(distill.Event)) (*distill.Execution, error)
	// CleanupPending removes the stale pending proposals of a
	// cleanup-only preview. It runs no agent.
	CleanupPending(p *learning.DistillPreview) (int, error)
	// PlanExtract previews a trigger extraction of the selected
	// proposals. It never runs the agent and never writes.
	PlanExtract(req learning.ExtractRequest) (*learning.ExtractPreview, error)
	// RunExtract runs exactly the reviewed preview. A stop through ctx
	// starts no new batch; progress runs on the worker goroutine.
	RunExtract(ctx context.Context, p *learning.ExtractPreview,
		progress func(distill.ExtractEvent)) (*distill.ExtractExecution, error)
}

// Maintenance covers the installed pins of .seamark/lessons.yaml: the
// pin read, the retarget and prune maintenance, and the recovery of a
// partial operation. Lessons › Pins and its maintenance modal use it.
type Maintenance interface {
	// Pins reads the installed pins of .seamark/lessons.yaml and relates
	// them to the ledger. It needs no index and never fails: a file or
	// ledger problem is data in the inspection. It writes nothing.
	Pins() *maintain.Inspection
	// PlanMaintenance plans a retarget or a prune of the selected pins.
	// It writes nothing.
	PlanMaintenance(req maintain.Request) (*maintain.Plan, error)
	// CommitMaintenance revalidates and commits exactly the reviewed plan.
	CommitMaintenance(p *maintain.Plan) (maintain.Result, error)
	// PlanRecovery plans the ledger-only completion of a partial
	// maintenance operation of this session. It writes nothing.
	PlanRecovery(r *maintain.Recovery) (*maintain.Plan, error)
	// CommitRecovery revalidates the recovery proof and completes the
	// ledger update. It never writes the pin file.
	CommitRecovery(p *maintain.Plan) (maintain.Result, error)
}

// Settings covers the focused settings of the configuration file: the
// read, a guarded plan and the save. The Settings screen uses it.
type Settings interface {
	// Settings reads the focused settings of the configuration file.
	// The error reports invalid arguments only; a file problem is data
	// in the snapshot.
	Settings() (*settings.Snapshot, error)
	// PlanSettings plans a guarded patch of one settings group. It
	// writes nothing.
	PlanSettings(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error)
	// SaveSettings writes exactly the reviewed plan, or reports it stale.
	SaveSettings(p *settings.Plan) (settings.Result, error)
}

// IndexView is the index section of the Overview: the probe, and the
// semantic health when the index is ready.
type IndexView struct {
	Probe workspace.IndexProbe
	// Status is nil unless Probe.State is IndexReady and Gather worked.
	Status *status.Status
	// StatusErr holds the semantic health read failure.
	StatusErr error
	// Policy summarizes the gate policy. It does not need the index, so
	// it is filled in every index state.
	Policy PolicySummary
}

// PolicySummary is the typed rule count of the gate policy, for the
// Guard card. Err holds the policy load failure.
type PolicySummary struct {
	Mode     string
	Deny     int
	Approval int
	Err      error
}

// summarizePolicy loads the gate policy of root. It reads only.
func summarizePolicy(root string) PolicySummary {
	p, err := gate.LoadPolicy(root)
	if err != nil {
		return PolicySummary{Err: err}
	}

	return PolicySummary{Mode: p.Mode, Deny: len(p.Deny), Approval: len(p.RequireApproval)}
}

// SetupPreview is a reviewed integration plan: the shared setup plan
// plus a readable diff of every planned write.
type SetupPreview struct {
	Setups   []integration.ClientSetup
	Plan     *integration.SetupPlan
	Writes   []integration.WritePreview
	GateMode string
	// PlannedAt is the plan time; the result screen reports it.
	PlannedAt time.Time
}

// IndexEvent is one progress event of an index build. Phase is one of
// scan, parse, history, write; Total 0 means the phase has no count.
type IndexEvent struct {
	Phase string
	Done  int
	Total int
	// Log is a warning or note line; Phase is then empty.
	Log string
}

// engine is the production All implementation over one workspace.
type engine struct {
	ws      workspace.Workspace
	version string
	binary  string
	reg     *integration.Registry
	deps    Deps

	// mu serializes mutations. The shell already refuses a second
	// mutation; this lock keeps the engine safe on its own. A mutation
	// takes it with TryLock, so a second one fails with ErrBusy and
	// never waits behind a long run.
	mu sync.Mutex
}

// The engine is the single production implementation of every area.
// One assertion per area names the area in the build error when a
// method signature drifts.
var (
	_ All         = (*engine)(nil)
	_ Reader      = (*engine)(nil)
	_ Setup       = (*engine)(nil)
	_ Decisions   = (*engine)(nil)
	_ Learning    = (*engine)(nil)
	_ Maintenance = (*engine)(nil)
	_ Settings    = (*engine)(nil)
)

// Deps are the three external boundaries of the engine: the GitHub
// fetch, the agent invoker and the clock. Tests replace them, so no
// engine test contacts GitHub or runs a paid agent, and every time is
// fixed. A zero field selects the real boundary: gh, the command of the
// reviewed preview, and time.Now. Deps holds nothing else, because
// every other input of the engine is a file or the store.
type Deps struct {
	// Fetch reads the GitHub review comments; nil runs gh.
	Fetch reviews.FetchFunc
	// Invoke sends the distillation and the trigger extraction prompts;
	// nil runs the command of the reviewed preview.
	Invoke agent.Invoker
	// Now reads the clock; nil reads the system clock.
	Now func() time.Time
}

// New binds the shared engines to one resolved workspace.
// binary is the seamark path that hook commands use.
func New(ws workspace.Workspace, version, binary string) All {
	return NewWithDeps(ws, version, binary, Deps{})
}

// NewWithDeps binds the shared engines to one resolved workspace with
// the boundaries of deps. The console tests that drive a screen over the
// real engine use it, so they replace only the GitHub fetch, the agent
// and the clock.
func NewWithDeps(ws workspace.Workspace, version, binary string, deps Deps) All {
	return newEngine(ws, version, binary, deps)
}

// newEngine builds the engine with explicit boundaries. New passes the
// zero Deps, which selects the production boundaries; tests pass fakes.
func newEngine(ws workspace.Workspace, version, binary string, deps Deps) *engine {
	if deps.Now == nil {
		deps.Now = time.Now
	}

	return &engine{ws: ws, version: version, binary: binary, reg: integration.Builtin(), deps: deps}
}

func (e *engine) Root() string { return e.ws.Root }

func (e *engine) DBPath() string { return e.ws.DBPath }

func (e *engine) Version() string { return e.version }

// seamarkDir is the directory of Seamark configuration and state. Any
// entry marks earlier Seamark use, for example effects.yaml, a delivery
// log, or an empty directory from a failed run. Such partial state leads
// to the diagnostics, not to the clean-install welcome.
const seamarkDir = ".seamark"

// Fresh reports true only when the workspace has no index database, no
// .seamark directory, and no recognized integration artifact.
func (e *engine) Fresh() (bool, error) {
	if workspace.Probe(e.ws).State != workspace.IndexAbsent {
		return false, nil
	}

	// Lstat also sees a dangling link at the database path. The probe
	// follows links, so it reports such a path as absent.
	for _, p := range []string{e.ws.DBPath, filepath.Join(e.ws.Root, seamarkDir)} {
		_, err := os.Lstat(p)

		switch {
		case err == nil:
			return false, nil
		case !errors.Is(err, os.ErrNotExist):
			return false, err
		}
	}

	// A generic .claude/ or .agents/ directory is not Seamark setup;
	// only a classified capability state is.
	for _, insp := range e.reg.Inspect(e.ws.Root) {
		for _, c := range insp.Capabilities {
			if c.State != integration.StateAbsent {
				return false, nil
			}
		}
	}

	return true, nil
}

func (e *engine) Doctor() *doctor.Report {
	return doctor.Run(e.ws.Root, e.ws.DBPath, e.version)
}

func (e *engine) Index() IndexView {
	policy := summarizePolicy(e.ws.Root)

	probe := workspace.Probe(e.ws)
	if probe.State != workspace.IndexReady {
		return IndexView{Probe: probe, Policy: policy}
	}

	st, probe, err := workspace.OpenReadOnly(e.ws)
	if err != nil {
		return IndexView{Probe: probe, StatusErr: err, Policy: policy}
	}
	defer func() { _ = st.Close() }()

	s, err := status.Gather(st, probe.Root)

	return IndexView{Probe: probe, Status: s, StatusErr: err, Policy: policy}
}

func (e *engine) AgentsOnPath() []string {
	var found []string

	for _, c := range e.reg.Clients() {
		if c.Invocation == nil {
			continue
		}

		spec, err := c.Invocation(e.ws.Root)
		if err != nil || len(spec.Argv) == 0 {
			continue
		}

		// LookPath only searches PATH; it never runs the agent.
		if _, err := exec.LookPath(spec.Argv[0]); err == nil {
			found = append(found, c.ID)
		}
	}

	return found
}

func (e *engine) Integrations() []integration.Inspection {
	return e.reg.Inspect(e.ws.Root)
}

func (e *engine) Clients() []integration.Client {
	return e.reg.Clients()
}

func (e *engine) Ledger() (*proposals.Ledger, error) {
	st, probe, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	return proposals.Load(st, probe.Root, e.deps.Now())
}

func (e *engine) Stats() lessonstats.Snapshot {
	st, probe, err := e.openRead()
	if err != nil {
		// Delivery totals need only the firing log; the eligibility
		// sections then explain that the index is missing.
		return lessonstats.Gather(nil, probe.Root)
	}
	defer func() { _ = st.Close() }()

	return lessonstats.Gather(st, probe.Root)
}

// openRead opens the index read-only. The probe keeps the effective
// root even on failure, so callers can still read root-level files.
func (e *engine) openRead() (*store.Store, workspace.IndexProbe, error) {
	probe := e.probe()

	if !probe.State.Readable() {
		return nil, probe, fmt.Errorf("%w (%s)", ErrNoLedger, probe.State)
	}

	return workspace.OpenReadOnly(e.ws)
}

// probe classifies the index and fills in the effective root. The
// root is the root that the index records, else the workspace root.
// The ledger, the learning services and the CLI read the index against
// this root, so every console view reads the same files.
func (e *engine) probe() workspace.IndexProbe {
	probe := workspace.Probe(e.ws)
	if probe.Root == "" {
		probe.Root = e.ws.Root
	}

	return probe
}

func (e *engine) PlanSetup(setups []integration.ClientSetup) (*SetupPreview, error) {
	req, gateMode := scaffold.Request(e.reg, e.ws.Root, e.binary, setups, "")

	plan, err := integration.PlanSetup(e.reg, req)
	if err != nil {
		return nil, err
	}

	return &SetupPreview{
		Setups:    setups,
		Plan:      plan,
		Writes:    integration.PreviewWrites(plan),
		GateMode:  gateMode,
		PlannedAt: e.deps.Now(),
	}, nil
}

func (e *engine) ApplySetup(p *SetupPreview) (integration.SetupResult, error) {
	if !e.mu.TryLock() {
		return integration.SetupResult{}, ErrBusy
	}
	defer e.mu.Unlock()

	// SkillsLog is CLI prose; raw output must never reach the
	// full-screen renderer.
	return integration.ApplySetup(p.Plan, integration.ApplyOptions{SkillsLog: io.Discard})
}

func (e *engine) BuildIndex(force bool, progress func(IndexEvent)) (*index.Summary, error) {
	if !e.mu.TryLock() {
		return nil, ErrBusy
	}
	defer e.mu.Unlock()

	return index.Run(index.Options{
		Root:   e.ws.Root,
		DBPath: e.ws.DBPath,
		Force:  force,
		Logf: func(format string, args ...any) {
			progress(IndexEvent{Log: fmt.Sprintf(format, args...)})
		},
		Progress: func(phase string, done, total int) {
			progress(IndexEvent{Phase: phase, Done: done, Total: total})
		},
	})
}

func (e *engine) PreviewDecision(action proposals.Action, ids []int64) (*proposals.Preview, error) {
	st, probe, err := e.openRead()
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	switch action {
	case proposals.ActionDismiss:
		return proposals.PreviewDismiss(st, ids)
	case proposals.ActionReconcile:
		return proposals.PreviewReconcile(st, probe.Root, ids)
	default:
		return proposals.PreviewApply(st, probe.Root, ids)
	}
}

func (e *engine) CommitDecision(p *proposals.Preview) (proposals.Result, error) {
	if !e.mu.TryLock() {
		return proposals.Result{Action: p.Action}, ErrBusy
	}
	defer e.mu.Unlock()

	probe := workspace.Probe(e.ws)
	if probe.State != workspace.IndexReady && probe.State != workspace.IndexNotIndexed {
		return proposals.Result{Action: p.Action}, fmt.Errorf("%w (%s)", ErrNoLedger, probe.State)
	}

	// A decision is a short write; the store closes right after it, so
	// the console never holds the database between actions.
	st, err := store.Open(e.ws.DBPath)
	if err != nil {
		return proposals.Result{Action: p.Action}, err
	}
	defer func() { _ = st.Close() }()

	return proposals.Commit(st, probe.Root, p)
}
