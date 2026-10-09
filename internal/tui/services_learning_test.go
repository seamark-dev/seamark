package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests run the production engine over a real indexed
// workspace. Only the two paid or network boundaries are fakes: the
// GitHub fetch and the agent invoker.

// engNow is the clock of the engine tests.
var engNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// engOrigin is a GitHub origin, so a review refresh has a target.
const engOrigin = "git@github.com:o/r.git"

// engPage is one gh page with one inline comment on a.go. It clusters
// into one review lesson.
const engPage = `[{"id":11,"user":{"login":"rev","type":"User"},` +
	`"body":"Close the handle on every error path.","path":"a.go","line":3,` +
	`"html_url":"https://github.com/o/r/pull/1#discussion_r11","created_at":"2026-07-21T10:00:00Z",` +
	`"pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

// engNew builds the engine over ws with the test clock.
func engNew(ws workspace.Workspace, deps engineDeps) *engine {
	if deps.now == nil {
		deps.now = func() time.Time { return engNow }
	}

	return newEngine(ws, "test", "/usr/local/bin/seamark", deps)
}

// engGit runs git in root with a fixed identity. Global config must not
// leak in, because it can rewrite remotes.
func engGit(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// engWrite writes a repository-relative file of ws.
func engWrite(t *testing.T, ws workspace.Workspace, rel, content string) {
	t.Helper()

	path := filepath.Join(ws.Root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// engFindings builds two review findings per directory with words that
// no other finding shares, so the grouper makes one group per
// directory. Directory i gets the IDs 21+2i and 22+2i.
func engFindings(dirs ...string) []domain.Finding {
	var out []domain.Finding

	for i, dir := range dirs {
		for j := range 2 {
			id := int64(21 + 2*i + j)
			out = append(out, domain.Finding{
				ID: id, Path: fmt.Sprintf("%s/f%d.go", dir, id), PR: int(id), Reviewer: "person",
				Body: fmt.Sprintf("Uniq%dalpha uniq%dbravo uniq%dcharlie.", id, id, id),
			})
		}
	}

	return out
}

// engWorkspace builds an indexed git workspace with one fix commit, the
// given origin, and the review findings of engFindings(dirs...). Its
// config.yaml names an agent command that leaves the returned marker
// when it runs, so a test can prove that a read never runs the agent.
func engWorkspace(t *testing.T, origin string, dirs ...string) (ws workspace.Workspace, marker string) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/eng\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package eng\n\n// A returns one.\nfunc A() int { return 1 }\n"), 0o644))

	engGit(t, root, "init", "-b", "main")
	engGit(t, root, "add", "-A")
	engGit(t, root, "commit", "-m", "initial")

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package eng\n\n// A returns one.\nfunc A() int { return 1 }\n\nfunc resetState() {}\n"), 0o644))
	engGit(t, root, "add", "-A")
	engGit(t, root, "commit", "-m", "fix: reset state before reuse (#42)")

	if origin != "" {
		engGit(t, root, "remote", "add", "origin", origin)
	}

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: ws.Root, DBPath: ws.DBPath})
	require.NoError(t, err)

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, engFindings(dirs...)))
	require.NoError(t, st.Close())

	marker = filepath.Join(t.TempDir(), "agent-ran")
	engWrite(t, ws, settings.File, fmt.Sprintf(
		"agent:\n  argv: [\"sh\", \"-c\", \"touch %s; cat >/dev/null; echo '{\\\"patterns\\\":[]}'\"]\n", marker))

	return ws, marker
}

// engTree returns the SHA-256 of every file under root, the git
// directory included. SQLite may create the -wal and -shm side files
// of a read-only open, so the tree leaves them out; the database file
// itself stays in.
func engTree(t *testing.T, root string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		if strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(data)
		tree[path] = hex.EncodeToString(sum[:])

		return nil
	})
	require.NoError(t, err)

	return tree
}

// engUnlocked checks that no mutation holds the engine lock.
func engUnlocked(t *testing.T, e *engine) {
	t.Helper()

	require.True(t, e.mu.TryLock(), "the engine lock is free again")
	e.mu.Unlock()
}

// engFetch is a scripted GitHub fetch that counts its calls.
type engFetch struct {
	mu    sync.Mutex
	calls []string
	raw   string
	err   error
	// during runs inside the fetch, while the refresh holds the lock.
	during func()
}

func (f *engFetch) fetch(_ context.Context, owner, repo string, _ reviews.Options) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, owner+"/"+repo)
	f.mu.Unlock()

	if f.during != nil {
		f.during()
	}

	if f.err != nil {
		return nil, f.err
	}

	return []byte(f.raw), nil
}

func (f *engFetch) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

// engAgent is a scripted invoker that counts its calls. By default it
// answers with one proposal that cites the finding IDs of the prompt.
type engAgent struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, prompt string) (string, error)
}

func (a *engAgent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()

	if a.fn != nil {
		return a.fn(ctx, prompt)
	}

	return engReply(prompt), nil
}

func (a *engAgent) Name() string { return "fake" }

func (a *engAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.calls
}

// engPromptID finds the finding IDs of a prompt.
var engPromptID = regexp.MustCompile(`finding id=(\d+) `)

// engLessons are the distinct replies per group, keyed by the first
// finding ID. Similar words would make the deduplication drop a group.
var engLessons = map[int]string{
	21: `"rule":"close-db-handles","note":"Close the database handle on every error path."`,
	23: `"rule":"verify-webhook-signatures","note":"Verify webhook signatures before parsing payloads."`,
}

// engReply answers a prompt with one proposal that cites every finding
// of the prompt, so the store keeps one pending proposal per group.
func engReply(prompt string) string {
	var ids []string

	for _, m := range engPromptID.FindAllStringSubmatch(prompt, -1) {
		ids = append(ids, m[1])
	}

	first, _ := strconv.Atoi(ids[0])

	return fmt.Sprintf(`{"patterns":[{%s,"finding_ids":[%s],"trigger_paths":[]}]}`,
		engLessons[first], strings.Join(ids, ","))
}

func TestEngineReadsNeverWriteFetchOrInvoke(t *testing.T) {
	ws, marker := engWorkspace(t, engOrigin, "api", "web")
	fetch := &engFetch{raw: engPage}
	agent := &engAgent{}
	e := engNew(ws, engineDeps{fetch: fetch.fetch, invoke: agent})

	before := engTree(t, ws.Root)

	snap := e.Learning()
	require.NoError(t, snap.CountsErr)
	assert.Equal(t, workspace.IndexReady, snap.Probe.State)
	assert.Equal(t, 4, snap.ReviewFindings)
	assert.Equal(t, "o/r", snap.Repository.Slug())

	set, err := e.Settings()
	require.NoError(t, err)
	require.NoError(t, set.Blocked)

	for _, mode := range []index.SourceMode{index.SourcesLocal, index.SourcesReviewsAndLocal} {
		plan, err := e.PlanEvidence(mode)
		require.NoError(t, err)
		assert.True(t, plan.Ready(), "mode %s", mode)
	}

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	assert.Len(t, preview.Plan.Groups, 2)

	plan, err := e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)
	assert.False(t, plan.NoOp)

	assert.Equal(t, before, engTree(t, ws.Root), "reads and plans write nothing")
	assert.Empty(t, fetch.called(), "reads and plans never fetch")
	assert.Zero(t, agent.count(), "reads and plans never invoke the agent")
	assert.NoFileExists(t, marker, "no configured command runs")
	engUnlocked(t, e)
}

func TestEngineRunsTheReviewedEvidencePlan(t *testing.T) {
	t.Run("reviews and local fixes", func(t *testing.T) {
		ws, _ := engWorkspace(t, engOrigin)
		fetch := &engFetch{raw: engPage}
		e := engNew(ws, engineDeps{fetch: fetch.fetch})

		plan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
		require.NoError(t, err)
		require.True(t, plan.Network)

		var events []index.RefreshEvent

		out, err := e.RunEvidence(context.Background(), plan, func(ev index.RefreshEvent) { events = append(events, ev) })
		require.NoError(t, err)

		assert.Equal(t, []string{"o/r"}, fetch.called(), "the fake fetch replaces gh")
		assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
		assert.Equal(t, index.SourceRefreshed, out.Reviews.State)
		assert.Equal(t, 1, out.After.Lessons, "the fetched comment is stored as one lesson")
		assert.NotEmpty(t, events, "the progress sink receives typed events")
		engUnlocked(t, e)
	})

	t.Run("local fixes only", func(t *testing.T) {
		ws, _ := engWorkspace(t, engOrigin)
		fetch := &engFetch{raw: engPage}
		e := engNew(ws, engineDeps{fetch: fetch.fetch})

		plan, err := e.PlanEvidence(index.SourcesLocal)
		require.NoError(t, err)
		assert.False(t, plan.Network)

		out, err := e.RunEvidence(context.Background(), plan, nil)
		require.NoError(t, err)

		assert.Empty(t, fetch.called(), "a local refresh never fetches")
		assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
		assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)
		engUnlocked(t, e)
	})
}

func TestEngineRunEvidenceRefusesWithoutWork(t *testing.T) {
	ws, _ := engWorkspace(t, engOrigin)
	fetch := &engFetch{raw: engPage}
	e := engNew(ws, engineDeps{fetch: fetch.fetch})

	plan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	before := engTree(t, ws.Root)

	// Another mutation holds the lock.
	e.mu.Lock()
	out, err := e.RunEvidence(context.Background(), plan, nil)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
	assert.Equal(t, index.CauseRefused, out.Reviews.Cause)
	require.ErrorIs(t, out.After.CountsErr, ErrBusy, "a refused run reads unknown counts, not zero counts")
	assert.Equal(t, engNow, out.FinishedAt)

	// A plan of another workspace never runs here.
	other, _ := engWorkspace(t, engOrigin)
	foreign, err := engNew(other, engineDeps{}).PlanEvidence(index.SourcesLocal)
	require.NoError(t, err)

	out, err = e.RunEvidence(context.Background(), foreign, nil)
	require.ErrorIs(t, err, errForeignPlan)
	assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
	engUnlocked(t, e)

	_, err = e.RunEvidence(context.Background(), nil, nil)
	require.Error(t, err, "a run needs a plan")
	engUnlocked(t, e)

	assert.Empty(t, fetch.called())
	assert.Equal(t, before, engTree(t, ws.Root), "a refused run writes nothing")
}

func TestEngineRunsTheReviewedDistillation(t *testing.T) {
	ws, marker := engWorkspace(t, "", "api", "web")
	agent := &engAgent{}
	e := engNew(ws, engineDeps{invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.Len(t, preview.Plan.Groups, 2)

	var events []distill.Event

	x, err := e.RunDistill(context.Background(), preview, func(ev distill.Event) { events = append(events, ev) })
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.Equal(t, distill.StopNone, x.Stop)
	assert.Equal(t, 2, agent.count(), "the fake invoker replaces the configured command")
	assert.NoFileExists(t, marker, "the configured command never runs")
	assert.NotEmpty(t, events)

	for _, g := range x.Groups {
		assert.Equal(t, distill.GroupCompleted, g.Status)
		assert.Len(t, g.Proposals, 1)
	}

	ledger, err := e.Ledger()
	require.NoError(t, err)
	assert.Len(t, ledger.Pending, 2, "each group stores one pending proposal")
	engUnlocked(t, e)
}

func TestEngineRunDistillWithoutAFakeRunsThePreviewedCommand(t *testing.T) {
	ws, marker := engWorkspace(t, "", "api")
	e := engNew(ws, engineDeps{})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.Len(t, preview.Plan.Groups, 1)

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.FileExists(t, marker, "a nil invoker runs the command of the preview")
	assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status)
	engUnlocked(t, e)
}

// TestEngineRefusesEveryMutationDuringADistillation proves that the
// distillation holds the engine lock for its whole run. Every other
// mutation fails with ErrBusy and changes nothing, reads still work,
// and the lock is free again after the run.
func TestEngineRefusesEveryMutationDuringADistillation(t *testing.T) {
	ws, _ := engWorkspace(t, engOrigin, "api")
	fetch := &engFetch{raw: engPage}
	agent := &engAgent{}
	e := engNew(ws, engineDeps{fetch: fetch.fetch, invoke: agent})

	// A pin without a proposal gives the maintenance read an entry.
	engWrite(t, ws, pinfile.File, "pin:\n  - rule: keep-docs-current\n    note: Keep the docs current.\n")

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)

	evPlan, err := e.PlanEvidence(index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	set, err := e.Settings()
	require.NoError(t, err)

	setPlan, err := e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)

	before := engTree(t, ws.Root)

	var (
		errs      = map[string]error{}
		nested    *distill.Execution
		nestedExt *distill.ExtractExecution
		readErr   error
		readPlan  *settings.Plan
		readCount int
		pins      *maintain.Inspection
		maintPlan *maintain.Plan
		maintErr  error
		extPlan   *learning.ExtractPreview
		extErr    error
	)

	agent.fn = func(_ context.Context, prompt string) (string, error) {
		nested, errs["RunDistill"] = e.RunDistill(context.Background(), preview, nil)
		_, errs["CleanupPending"] = e.CleanupPending(preview)
		_, errs["RunEvidence"] = e.RunEvidence(context.Background(), evPlan, nil)
		_, errs["SaveSettings"] = e.SaveSettings(setPlan)
		_, errs["BuildIndex"] = e.BuildIndex(false, func(IndexEvent) {})
		_, errs["CommitDecision"] = e.CommitDecision(&proposals.Preview{Action: proposals.ActionDismiss})
		_, errs["CommitMaintenance"] = e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionPrune})
		_, errs["CommitRecovery"] = e.CommitRecovery(&maintain.Plan{Action: maintain.ActionPrune})
		nestedExt, errs["RunExtract"] = e.RunExtract(context.Background(), &learning.ExtractPreview{}, nil)

		// Reads take no lock, so the views stay responsive.
		readPlan, readErr = e.PlanSettings(set, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
		readCount = e.Learning().ReviewFindings
		pins = e.Pins()
		maintPlan, maintErr = e.PlanMaintenance(maintain.Request{
			Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{pins.Pins[0].Entry.Ref()}, Cursor: true,
		})
		extPlan, extErr = e.PlanExtract(learning.ExtractRequest{})

		return engReply(prompt), nil
	}

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	for name, err := range errs {
		require.ErrorIs(t, err, ErrBusy, name)
	}

	assert.Len(t, errs, 9)
	assert.Nil(t, nested, "a refused run returns no execution")
	assert.Nil(t, nestedExt, "a refused extraction returns no execution")
	assert.Equal(t, 1, agent.count(), "the refused runs start no agent call")
	assert.Empty(t, fetch.called(), "the refused refresh never fetches")
	require.NoError(t, readErr)
	assert.NotNil(t, readPlan)
	assert.Equal(t, 2, readCount)

	require.Len(t, pins.Pins, 1, "the Pins read works during the run")
	require.NoError(t, maintErr)
	require.Len(t, maintPlan.Items, 1, "a maintenance plan works during the run")
	assert.Equal(t, maintain.ItemBlocked, maintPlan.Items[0].Status, "a pin without a proposal stays read-only")
	require.NoError(t, extErr)
	assert.True(t, extPlan.NoOp(), "an extraction plan works during the run")

	// The run changed only the database; the refused save changed no file.
	after := engTree(t, ws.Root)
	delete(before, ws.DBPath)
	delete(after, ws.DBPath)
	assert.Equal(t, before, after)

	// The lock is free again, so the reviewed save now succeeds.
	res, err := e.SaveSettings(setPlan)
	require.NoError(t, err)
	assert.True(t, res.Written)
}

func TestEngineMutationsReleaseTheLockOnEveryPath(t *testing.T) {
	ws, _ := engWorkspace(t, "", "api")
	agent := &engAgent{}
	e := engNew(ws, engineDeps{invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)

	_, err = e.RunDistill(context.Background(), nil, nil)
	require.Error(t, err)
	engUnlocked(t, e)

	_, err = e.CleanupPending(preview)
	require.ErrorIs(t, err, learning.ErrNotCleanupOnly)
	engUnlocked(t, e)

	// A changed config.yaml makes the preview stale; the run refuses it.
	engWrite(t, ws, settings.File, "agent:\n  cli: claude\n")

	x, err := e.RunDistill(context.Background(), preview, nil)
	require.ErrorIs(t, err, distill.ErrStale)
	assert.Nil(t, x)
	assert.Zero(t, agent.count())
	engUnlocked(t, e)

	_, err = e.SaveSettings(nil)
	require.Error(t, err)
	engUnlocked(t, e)

	// Each maintenance and extraction mutation refuses a missing or an
	// unusable plan, and frees the lock on that path too.
	_, err = e.CommitMaintenance(nil)
	require.ErrorIs(t, err, errNoPlan)
	engUnlocked(t, e)

	_, err = e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionPrune})
	require.Error(t, err, "a plan that was never confirmable never commits")
	engUnlocked(t, e)

	_, err = e.CommitRecovery(nil)
	require.ErrorIs(t, err, errNoPlan)
	engUnlocked(t, e)

	_, err = e.CommitRecovery(&maintain.Plan{Action: maintain.ActionPrune})
	require.Error(t, err, "a plan without a recovery proof never commits")
	engUnlocked(t, e)

	ext, err := e.RunExtract(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Nil(t, ext)
	engUnlocked(t, e)

	noop, err := e.PlanExtract(learning.ExtractRequest{})
	require.NoError(t, err)

	ext, err = e.RunExtract(context.Background(), noop, nil)
	require.ErrorIs(t, err, learning.ErrNothingToExtract)
	assert.Nil(t, ext)
	assert.Zero(t, agent.count())
	engUnlocked(t, e)

	e.mu.Lock()
	x, err = e.RunDistill(context.Background(), preview, nil)
	_, cleanErr := e.CleanupPending(preview)
	_, maintErr := e.CommitMaintenance(&maintain.Plan{Action: maintain.ActionRetarget})
	_, recoverErr := e.CommitRecovery(&maintain.Plan{Action: maintain.ActionRetarget})
	ext, extErr := e.RunExtract(context.Background(), noop, nil)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	require.ErrorIs(t, cleanErr, ErrBusy)
	require.ErrorIs(t, maintErr, ErrBusy)
	require.ErrorIs(t, recoverErr, ErrBusy)
	require.ErrorIs(t, extErr, ErrBusy)
	assert.Nil(t, x)
	assert.Nil(t, ext)
}

func TestEngineCleansUpACleanupOnlyPreview(t *testing.T) {
	ws, _ := engWorkspace(t, "")

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, storetest.InsertProposal(st, &domain.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: domain.ProposalProposed,
	}))
	require.NoError(t, st.Close())

	agent := &engAgent{}
	e := engNew(ws, engineDeps{invoke: agent})

	preview, err := e.PlanDistill(learning.DistillRequest{})
	require.NoError(t, err)
	require.True(t, preview.CleanupOnly())

	removed, err := e.CleanupPending(preview)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Zero(t, agent.count(), "a cleanup runs no agent")

	ledger, err := e.Ledger()
	require.NoError(t, err)
	assert.Empty(t, ledger.Pending)
	engUnlocked(t, e)
}

func TestEngineClockSeam(t *testing.T) {
	e := engNew(workspace.Workspace{Root: t.TempDir()}, engineDeps{})

	out := e.refusedOutcome(errors.New("refused"))
	assert.Equal(t, engNow, out.FinishedAt)

	prod := newEngine(workspace.Workspace{Root: t.TempDir()}, "test", "", engineDeps{})
	require.NotNil(t, prod.deps.now, "a zero clock reads the system clock")
	assert.WithinDuration(t, time.Now(), prod.deps.now(), time.Minute)
}
