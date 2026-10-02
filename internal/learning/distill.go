package learning

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// Distillation in the console is two confirmed steps: PlanDistill builds
// a read-only preview, and RunDistill executes exactly that preview. A
// stale-pending cleanup without eligible groups is a third, separately
// confirmed action: CleanupPending. The CLI keeps its own entry,
// distill.Run, which plans with the same step and selects the same groups.

// The repository-relative files that a distillation preview binds.
const (
	distConfigFile  = ".seamark/config.yaml"
	distLessonsFile = ".seamark/lessons.yaml"
)

var (
	// ErrDistillNotReady reports an index that is absent, not indexed,
	// or incompatible. The console links to the explicit build or repair
	// action; a distillation never builds the index as a side effect.
	ErrDistillNotReady = errors.New("the index is not ready for distillation")
	// ErrDistillConfig marks a configuration reader error. The console
	// is stricter than the CLI here: it must not spend against a default
	// that the user did not review. DistillConfigError names the file.
	ErrDistillConfig = errors.New("the distillation configuration is invalid")
	// ErrDistillBlocked reports a run of a preview with blockers. A
	// blocked preview discloses; it never authorizes an agent call.
	ErrDistillBlocked = errors.New("the distillation preview is blocked")
	// ErrNothingToDistill reports a run of a preview without an eligible
	// group. No agent starts. A needed cleanup has its own action.
	ErrNothingToDistill = errors.New("the preview has no eligible evidence group")
	// ErrNotCleanupOnly reports a cleanup request for a preview that has
	// eligible groups or no cleanup. A run performs such a cleanup itself.
	ErrNotCleanupOnly = errors.New("the preview is not a cleanup-only plan")
)

// DistillConfigError is a configuration reader error that blocks a
// distillation preview. Path lets the console link to the file or to
// the matching Settings group.
type DistillConfigError struct {
	// Path is repository-relative: .seamark/config.yaml or
	// .seamark/lessons.yaml.
	Path string
	// Err is the reader error, with its original text.
	Err error
}

// Error names the file and the reader error.
func (e *DistillConfigError) Error() string {
	return e.Path + ": " + e.Err.Error()
}

// Unwrap matches both ErrDistillConfig and the reader error, for
// example reviews.ErrLessonsConfig.
func (e *DistillConfigError) Unwrap() []error {
	return []error{ErrDistillConfig, e.Err}
}

// DistillRequest is the scope the user chose for one distillation.
type DistillRequest struct {
	// RegionInput is the region as typed. NormalizeRegion reads it
	// relative to the workspace root; "" means the whole repository.
	RegionInput string
	// Limit caps new groups. 0 means distill.DefaultGroupLimit, not
	// "all": all groups need the explicit AllGroups choice, because a
	// run without a bound must be a deliberate choice.
	Limit int
	// AllGroups selects every eligible group. Limit must then be 0.
	AllGroups bool
}

// DistillPreview is the read-only preview of one distillation. It is
// what the user confirms, and RunDistill executes only this preview.
type DistillPreview struct {
	// Request is the scope as the user chose it, so "prepare remaining
	// work" can plan again with the same choices.
	Request DistillRequest
	// Plan is the frozen group and evidence selection.
	Plan *distill.Plan
	// Region is the normalized repository-relative region; "" means
	// the whole repository.
	Region string
	// Invocation is the resolved agent command. Dir is empty for the
	// default Claude preset and for a custom agent.argv; only Codex sets
	// it to RepoRoot. An empty Dir runs the process in the console's
	// working directory, which can differ from RepoRoot (seamark -C).
	// A surface that discloses the directory must show os.Getwd() then.
	Invocation agent.CommandSpec
	// Config and Lessons observe config.yaml and lessons.yaml. A run
	// refuses the preview when either file changed.
	Config  integration.FileGuard
	Lessons integration.FileGuard
	// RepoRoot is the root that the index records; DBPath the database.
	RepoRoot string
	DBPath   string
	// PlannedAt is when the plan was made.
	PlannedAt time.Time
	// Blockers are safe display lines that stop a run, for example a
	// missing agent executable. The preview still discloses the plan.
	Blockers []string
}

// NoOp reports a preview with neither eligible groups nor a cleanup.
// The console shows "Nothing new to distill" and starts nothing.
func (p *DistillPreview) NoOp() bool {
	return p.Plan != nil && len(p.Plan.Groups) == 0 && len(p.Plan.Cleanup) == 0
}

// CleanupOnly reports a preview whose only work is the stale-pending
// cleanup. CleanupPending performs it after its own confirmation.
func (p *DistillPreview) CleanupOnly() bool {
	return p.Plan != nil && len(p.Plan.Groups) == 0 && len(p.Plan.Cleanup) > 0
}

// PlanDistill previews one distillation. It reads the index through a
// read-only store and never runs the agent, prunes, or marks. It checks
// only that the agent executable is on PATH. A malformed config.yaml or
// lessons.yaml returns a *DistillConfigError; the CLI would warn and
// fall back to defaults instead.
func PlanDistill(ws workspace.Workspace, reg *integration.Registry, req DistillRequest) (*DistillPreview, error) {
	limit, err := distLimit(req)
	if err != nil {
		return nil, err
	}

	probe := workspace.Probe(ws)
	if probe.State != workspace.IndexReady {
		return nil, distNotReady(probe)
	}

	root := probe.Root

	region, err := distill.NormalizeRegion(root, req.RegionInput)
	if err != nil {
		return nil, err
	}

	if reg == nil {
		reg = integration.Builtin()
	}

	in, err := distReadInputs(reg, root)
	if err != nil {
		return nil, err
	}

	st, now, err := workspace.OpenReadOnly(ws)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDistillNotReady, err)
	}
	defer func() { _ = st.Close() }()

	if now.State != workspace.IndexReady || now.Root != root {
		return nil, fmt.Errorf("%w: the index changed while planning; prepare again", distill.ErrStale)
	}

	plan, err := distill.NewPlan(st, distill.NewLexicalGrouper(), distill.PlanOptions{
		Region: region,
		Limit:  limit,
		Pins:   distill.PinsFromConfig(in.lessons),
		Spec:   in.spec,
		Root:   root,
	})
	if err != nil {
		return nil, err
	}

	// The bound files must be the files the plan read. A write during
	// planning would leave the preview describing neither version.
	if err := distCheckFiles(root, in.configGuard, in.lessonsGuard); err != nil {
		return nil, err
	}

	p := &DistillPreview{
		Request:    req,
		Plan:       plan,
		Region:     region,
		Invocation: in.spec,
		Config:     in.configGuard,
		Lessons:    in.lessonsGuard,
		RepoRoot:   root,
		DBPath:     ws.DBPath,
		PlannedAt:  plan.PlannedAt,
	}

	// Only PATH is checked: running a configured command, even with
	// --version, could start a login flow or spend money.
	if _, err := exec.LookPath(in.spec.Argv[0]); err != nil {
		p.Blockers = append(p.Blockers, distSafe(fmt.Sprintf("agent cli %q not found on PATH", in.spec.Argv[0])))
	}

	return p, nil
}

// RunDistill executes a confirmed preview. Before any work it checks
// the workspace identity, both bound files, the resolved invocation,
// and the store snapshot; a changed input returns an error that wraps
// distill.ErrStale and starts nothing. It then calls distill.Execute
// with the store boundary checks on, and repeats the file and identity
// checks before each group.
//
// A nil inv builds the invoker from the preview's invocation; tests
// pass a fake. That invoker starts the agent in its own process group,
// so a stop also ends a wrapper script and its model call. The console
// is in raw mode, so no terminal interrupt reaches the agent. The
// execution is nil when the run is refused before it starts; otherwise
// it is the typed result, also after a stop.
func RunDistill(ctx context.Context, ws workspace.Workspace, reg *integration.Registry, p *DistillPreview,
	inv agent.Invoker, progress func(distill.Event),
) (*distill.Execution, error) {
	if p == nil || p.Plan == nil {
		return nil, errors.New("distill: no preview to run")
	}

	if len(p.Blockers) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrDistillBlocked, strings.Join(p.Blockers, "; "))
	}

	if len(p.Plan.Groups) == 0 {
		return nil, ErrNothingToDistill
	}

	if err := distCheckInputs(ws, p); err != nil {
		return nil, err
	}

	if reg == nil {
		reg = integration.Builtin()
	}

	if err := distCheckInvocation(reg, p); err != nil {
		return nil, err
	}

	if inv == nil {
		command, err := agent.NewGroupCommand(p.Invocation)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDistillBlocked, err)
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

	return distill.Execute(ctx, st, inv, p.Plan, distill.ExecuteOptions{
		Progress:   progress,
		Revalidate: func() error { return distCheckInputs(ws, p) },
		Boundary:   true,
	})
}

// CleanupPending deletes the stale pending proposals of a cleanup-only
// preview: no eligible group and a non-empty cleanup. It deletes only
// the planned IDs that are still pending, and only while the store
// snapshot and the workspace identity are unchanged. The configuration
// files do not decide the cleanup set, so a change to them does not
// block it. It returns the number of deleted rows.
func CleanupPending(ws workspace.Workspace, p *DistillPreview) (int, error) {
	if p == nil || p.Plan == nil {
		return 0, errors.New("distill: no preview to clean up")
	}

	if !p.CleanupOnly() {
		return 0, ErrNotCleanupOnly
	}

	if err := distCheckWorkspace(ws, p); err != nil {
		return 0, err
	}

	st, err := store.Open(ws.DBPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()

	x, err := distill.Execute(context.Background(), st, nil, p.Plan, distill.ExecuteOptions{Boundary: true})
	if err != nil {
		return 0, err
	}

	if x.Stop != distill.StopNone {
		return 0, x.StopErr
	}

	return x.Removed, nil
}

// distInputs are the configuration inputs of one preview.
type distInputs struct {
	spec         agent.CommandSpec
	lessons      *reviews.Config
	configGuard  integration.FileGuard
	lessonsGuard integration.FileGuard
}

// distReadInputs observes both files, then reads them with the shared
// readers. Every reader error blocks the preview: a paid run must not
// fall back to defaults that the user did not review.
func distReadInputs(reg *integration.Registry, root string) (distInputs, error) {
	var in distInputs

	var err error

	if in.configGuard, _, err = integration.ReadInput(root, distConfigFile); err != nil {
		return in, &DistillConfigError{Path: distConfigFile, Err: err}
	}

	if in.lessonsGuard, _, err = integration.ReadInput(root, distLessonsFile); err != nil {
		return in, &DistillConfigError{Path: distLessonsFile, Err: err}
	}

	acfg, err := agent.LoadConfig(root)
	if err != nil {
		return in, &DistillConfigError{Path: distConfigFile, Err: err}
	}

	if in.spec, err = reg.ResolveInvocation(acfg, root); err != nil {
		return in, &DistillConfigError{Path: distConfigFile, Err: err}
	}

	if in.lessons, err = reviews.LoadConfig(root); err != nil {
		return in, &DistillConfigError{Path: distLessonsFile, Err: err}
	}

	return in, nil
}

// distCheckInputs checks the workspace identity and both bound files.
// RunDistill calls it before the start and before each group.
func distCheckInputs(ws workspace.Workspace, p *DistillPreview) error {
	if err := distCheckWorkspace(ws, p); err != nil {
		return err
	}

	return distCheckFiles(p.RepoRoot, p.Config, p.Lessons)
}

// distCheckWorkspace compares the database path, the index state, and
// the recorded root with the preview.
func distCheckWorkspace(ws workspace.Workspace, p *DistillPreview) error {
	if ws.DBPath != p.DBPath {
		return fmt.Errorf("%w: the workspace database changed from %s to %s", distill.ErrStale, p.DBPath, ws.DBPath)
	}

	probe := workspace.Probe(ws)

	if probe.State != workspace.IndexReady {
		return fmt.Errorf("%w: the index is %s now", distill.ErrStale, probe.State)
	}

	if probe.Root != p.RepoRoot {
		return fmt.Errorf("%w: the index now records the root %s", distill.ErrStale, distSafe(probe.Root))
	}

	return nil
}

// distCheckFiles observes both files again and compares them with the
// guards: content, existence, mode, and symbolic links all count.
func distCheckFiles(root string, config, lessons integration.FileGuard) error {
	for _, want := range []integration.FileGuard{config, lessons} {
		now, _, err := integration.ReadInput(root, want.Path)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", distill.ErrStale, want.Path, err)
		}

		if now != want {
			return fmt.Errorf("%w: %s changed since the preview", distill.ErrStale, want.Path)
		}
	}

	return nil
}

// distCheckInvocation resolves the agent again and compares it with the
// previewed command. The run must never switch agents silently.
func distCheckInvocation(reg *integration.Registry, p *DistillPreview) error {
	acfg, err := agent.LoadConfig(p.RepoRoot)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", distill.ErrStale, distConfigFile, err)
	}

	spec, err := reg.ResolveInvocation(acfg, p.RepoRoot)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", distill.ErrStale, distConfigFile, err)
	}

	same := spec.Name == p.Invocation.Name && slices.Equal(spec.Argv, p.Invocation.Argv) &&
		spec.Dir == p.Invocation.Dir && spec.Diagnostic == p.Invocation.Diagnostic
	if !same {
		return fmt.Errorf("%w: the agent command changed since the preview", distill.ErrStale)
	}

	return nil
}

// distLimit turns a request into the engine limit. A negative limit is
// an error, 0 without AllGroups is the CLI default, and AllGroups is the
// engine's 0. The explicit choice keeps an unbounded run deliberate.
func distLimit(req DistillRequest) (int, error) {
	switch {
	case req.AllGroups && req.Limit != 0:
		return 0, fmt.Errorf("%w: choose a group limit or all eligible groups, not both", distill.ErrInvalidLimit)
	case req.AllGroups:
		return 0, nil
	case req.Limit < 0:
		return 0, fmt.Errorf("%w: got %d", distill.ErrInvalidLimit, req.Limit)
	case req.Limit == 0:
		return distill.DefaultGroupLimit, nil
	default:
		return req.Limit, nil
	}
}

// distNotReady describes an index that cannot serve a plan.
func distNotReady(probe workspace.IndexProbe) error {
	if probe.Err != nil {
		return fmt.Errorf("%w: index database %s is %s: %w", ErrDistillNotReady, probe.Path, probe.State, probe.Err)
	}

	return fmt.Errorf("%w: index database %s is %s: build the index first", ErrDistillNotReady, probe.Path, probe.State)
}

// distSafe makes repository-controlled text safe to display: secrets
// redacted and terminal control sequences removed.
func distSafe(s string) string {
	return render.Sanitize(redact.Secrets(s))
}
