package services

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the installed lesson services run the production
// engine over real temporary workspaces, as the learning engine tests
// do. Only the agent invoker is a fake.

// maintPins is a pin file with one pin that the ledger links and one pin
// without a proposal. The second pin must stay through every prune.
const maintPins = "pin:\n" +
	"  - rule: close-db-handles\n    region: api\n    note: Close the handle on every error path.\n" +
	"  - rule: keep-docs-current\n    note: Keep the docs current.\n"

// maintWorkspace builds an indexed workspace with the pin file maintPins,
// the applied proposal of its first pin, and one superseded proposal.
// write sets distill.write. It returns the linked proposal.
func maintWorkspace(t *testing.T, write bool) (ws workspace.Workspace, linked domain.Proposal) {
	t.Helper()

	ws, _ = servicestest.Workspace(t, "", "api")

	cfg := filepath.Join(ws.Root, filepath.FromSlash(settings.File))
	data, err := os.ReadFile(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, append(data, fmt.Sprintf("distill:\n  write: %t\n", write)...), 0o644))

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	linked = domain.Proposal{
		Signature: "sig-close", Rule: "close-db-handles", Region: "api", Note: "Close the handle on every error path.",
		Members: []int64{21, 22}, Status: domain.ProposalApplied,
	}
	require.NoError(t, storetest.InsertProposal(st, &linked))

	old := domain.Proposal{
		Signature: "sig-old", Rule: "retired-rule", Note: "An old rule.", Members: []int64{99},
		Status: domain.ProposalSuperseded,
	}
	require.NoError(t, storetest.InsertProposal(st, &old))
	require.NoError(t, st.Close())

	servicestest.Write(t, ws, pinfile.File, maintPins)

	return ws, linked
}

// maintPrune is the prune request of the first pin of e, the linked one.
func maintPrune(t *testing.T, e *engine) maintain.Request {
	t.Helper()

	in := e.Pins()
	require.NotEmpty(t, in.Pins)

	return maintain.Request{Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{in.Pins[0].Entry.Ref()}, Cursor: true}
}

// maintFile reads the pin file of ws.
func maintFile(t *testing.T, ws workspace.Workspace) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(pinfile.File)))
	require.NoError(t, err)

	return string(data)
}

func TestEnginePinsReadWithoutAnIndex(t *testing.T) {
	root := t.TempDir()
	servicestest.Git(t, root, "init", "-b", "main")

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)
	servicestest.Write(t, ws, pinfile.File, maintPins)

	before := servicestest.Tree(t, ws.Root)
	in := engNew(ws, Deps{}).Pins()

	require.NotNil(t, in)
	require.NoError(t, in.DocErr)
	require.Len(t, in.Pins, 2, "the pin file alone lists the installed pins")
	assert.Equal(t, "close-db-handles", in.Pins[0].Entry.Rule)
	assert.Equal(t, "keep-docs-current", in.Pins[1].Entry.Rule)

	assert.Nil(t, in.Links, "no ledger means no known link")
	require.ErrorIs(t, in.LinkErr, maintain.ErrNoLedger)
	assert.Equal(t, workspace.IndexAbsent, in.Index.State)
	assert.Equal(t, servicestest.Now, in.At, "the readings use the engine clock")

	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "the read writes nothing")
	assert.NoFileExists(t, ws.DBPath, "the read creates no database")
}

func TestEnginePinsLinkTheLedger(t *testing.T) {
	ws, linked := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	before := servicestest.Tree(t, ws.Root)
	in := e.Pins()

	require.NotNil(t, in.Links)
	require.NoError(t, in.LinkErr)
	require.Len(t, in.Pins, 2)
	assert.Equal(t, pinfile.LinkLinked, in.Pins[0].Link.Link)
	require.Len(t, in.Pins[0].Link.Applied, 1)
	assert.Equal(t, linked.ID, in.Pins[0].Link.Applied[0].ID)
	assert.Equal(t, pinfile.LinkNoProposal, in.Pins[1].Link.Link)

	require.Len(t, in.Superseded, 1, "the superseded row is history")
	assert.Equal(t, "retired-rule", in.Superseded[0].Rule)

	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "the read writes nothing")
	engUnlocked(t, e)
}

func TestEngineCommitsAReviewedPrune(t *testing.T) {
	ws, linked := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	before := servicestest.Tree(t, ws.Root)

	plan, err := e.PlanMaintenance(maintPrune(t, e))
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blocked)
	require.NotNil(t, plan.Change)
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a plan writes nothing")
	engUnlocked(t, e)

	res, err := e.CommitMaintenance(plan)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.True(t, res.LedgerDone)
	assert.Equal(t, []int64{linked.ID}, res.Moved)
	engUnlocked(t, e)

	file := maintFile(t, ws)
	assert.NotContains(t, file, "close-db-handles", "the reviewed pin left the file")
	assert.Contains(t, file, "keep-docs-current", "the other pin stays")

	ledger, err := e.Ledger()
	require.NoError(t, err)
	require.Len(t, ledger.Superseded, 2)
	assert.Equal(t, linked.ID, ledger.Superseded[0].ID, "the linked row moved to superseded")
	assert.Empty(t, ledger.Applied)

	// The same reviewed plan never runs twice: the file changed.
	again := servicestest.Tree(t, ws.Root)

	_, err = e.CommitMaintenance(plan)
	require.ErrorIs(t, err, maintain.ErrStale)
	assert.Equal(t, again, servicestest.Tree(t, ws.Root), "a stale plan writes nothing")
	engUnlocked(t, e)
}

func TestEngineNeverCommitsAGateOffPlan(t *testing.T) {
	ws, _ := maintWorkspace(t, false)
	e := engNew(ws, Deps{})

	plan, err := e.PlanMaintenance(maintPrune(t, e))
	require.NoError(t, err)
	assert.False(t, plan.Confirmable)
	assert.False(t, plan.Write)
	require.NotNil(t, plan.Change, "a gate-off plan still shows its change")

	before := servicestest.Tree(t, ws.Root)

	_, err = e.CommitMaintenance(plan)
	require.ErrorIs(t, err, maintain.ErrWriteDisabled)
	assert.Equal(t, before, servicestest.Tree(t, ws.Root))
	engUnlocked(t, e)
}

func TestEngineCompletesARecoveryOfThisSession(t *testing.T) {
	ws, linked := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	plan, err := e.PlanMaintenance(maintPrune(t, e))
	require.NoError(t, err)
	require.True(t, plan.Confirmable)

	// The reviewed bytes land, but the ledger step does not run: the
	// partial state that a failed commit of this session leaves.
	servicestest.Write(t, ws, pinfile.File, string(plan.Change.After))

	r := &maintain.Recovery{
		Action: plan.Action, Request: plan.Request, Items: plan.Items, Written: sha256.Sum256(plan.Change.After),
		Before: plan.Change.Before, After: plan.Change.After, Edits: plan.Change.Edits,
		NewKeys: plan.Change.NewKeys, Supersede: plan.Supersede,
	}

	rp, err := e.PlanRecovery(r)
	require.NoError(t, err)
	require.True(t, rp.Confirmable, "%v", rp.Blocked)
	assert.Nil(t, rp.Change, "a recovery writes no pin file")

	res, err := e.CommitRecovery(rp)
	require.NoError(t, err)
	assert.True(t, res.LedgerDone)
	assert.False(t, res.FileWritten)
	assert.Equal(t, []int64{linked.ID}, res.Moved)
	assert.Equal(t, string(plan.Change.After), maintFile(t, ws), "the recovery keeps the file")
	engUnlocked(t, e)

	// A completed recovery is no longer proven, so it cannot run again.
	_, err = e.CommitRecovery(rp)
	require.ErrorIs(t, err, maintain.ErrStale)
	engUnlocked(t, e)
}

func TestEnginePlansAndRunsAnExtraction(t *testing.T) {
	ws, marker := servicestest.Workspace(t, "", "api")

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	pending := domain.Proposal{
		Signature: "sig-ext", Rule: "close-db-handles", Region: "api", Note: "Close the handle.",
		Members: []int64{21, 22}, Status: domain.ProposalProposed,
	}
	require.NoError(t, storetest.InsertProposal(st, &pending))
	require.NoError(t, st.Close())

	var prompts []string

	agent := &servicestest.Agent{Fn: func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)

		return `{"triggers": []}`, nil
	}}
	e := engNew(ws, Deps{Invoke: agent})

	before := servicestest.Tree(t, ws.Root)

	preview, err := e.PlanExtract(learning.ExtractRequest{IDs: []int64{pending.ID}})
	require.NoError(t, err)
	require.False(t, preview.NoOp())
	assert.Equal(t, []int64{pending.ID}, preview.Plan.IDs())
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a plan writes nothing")
	assert.Zero(t, agent.Count(), "a plan never invokes the agent")
	engUnlocked(t, e)

	x, err := e.RunExtract(context.Background(), preview, nil)
	require.NoError(t, err)
	require.NotNil(t, x)

	assert.Equal(t, 1, agent.Count(), "the fake invoker replaces the configured command")
	require.Len(t, prompts, 1)
	assert.True(t, strings.Contains(prompts[0], fmt.Sprintf("id=%d", pending.ID)), prompts[0])
	assert.Equal(t, []int64{pending.ID}, x.Persisted, "a valid negative answer is stored")
	assert.NoFileExists(t, marker, "the configured command never runs")
	engUnlocked(t, e)
}

func TestEngineRefusesAMaintenanceCommitForAnotherRepositoryRoot(t *testing.T) {
	ws, _ := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	plan, err := e.PlanMaintenance(maintPrune(t, e))
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blocked)

	// A copied checkout keeps the index of the original repository, so
	// the database records another root than the workspace.
	other := t.TempDir()
	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, other))
	require.NoError(t, st.Close())

	before := servicestest.Tree(t, ws.Root)

	res, err := e.CommitMaintenance(plan)
	require.ErrorIs(t, err, errForeignRoot)
	assert.Contains(t, err.Error(), other)
	assert.False(t, res.FileWritten)
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "the refusal writes nothing")
	engUnlocked(t, e)

	_, err = e.CommitRecovery(plan)
	require.ErrorIs(t, err, errForeignRoot, "a recovery completion is refused the same way")
	engUnlocked(t, e)
}

func TestSameDirAcceptsALinkToTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	assert.True(t, sameDir(dir, dir+"/"))
	assert.True(t, sameDir(link, dir), "a link spelling names the same directory")
	assert.False(t, sameDir(dir, t.TempDir()))
	assert.False(t, sameDir(dir, filepath.Join(dir, "missing")))
}
