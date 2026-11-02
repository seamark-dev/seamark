package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the manual scope edit run the production engine
// over the maintenance workspace.

// reviewRow reads proposal id from the index of ws, in any status.
func reviewRow(t *testing.T, ws workspace.Workspace, id int64) domain.Proposal {
	t.Helper()

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	rows, err := st.ProposalsWithIDs([]int64{id})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// scopeRequest is the edit that sets the regions of the linked pin of e.
func scopeRequest(t *testing.T, e *engine, regions ...string) maintain.ScopeRequest {
	t.Helper()

	pins := e.Pins().Pins
	require.NotEmpty(t, pins)

	return maintain.ScopeRequest{Edit: maintain.AuthorRequest{
		Op: maintain.AuthorEdit, Ref: pins[0].Entry.Ref(), Scope: maintain.ScopeRegions, Regions: regions, ScopeSet: true,
	}}
}

func TestEngineCommitsAReviewedScopeEdit(t *testing.T) {
	ws, linked := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	plan, err := e.PlanScope(scopeRequest(t, e, "api", "web"))
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blocked)
	assert.Equal(t, servicestest.Now.Unix(), plan.Scoping.At, "the engine clock stamps the choice")

	res, err := e.CommitMaintenance(plan)
	require.NoError(t, err)
	assert.True(t, res.FileWritten)
	assert.True(t, res.LedgerDone, res.Note)
	engUnlocked(t, e)

	assert.Contains(t, maintFile(t, ws), "regions: [api, web]")

	row := reviewRow(t, ws, linked.ID)
	assert.Equal(t, []string{"api", "web"}, row.RegionSet())
	assert.Equal(t, servicestest.Now.Unix(), row.ScopeChosen)
	assert.Equal(t, pinfile.LinkLinked, e.Pins().Pins[0].Link.Link, "the link holds")
}

// TestEngineRefusesAScopeCommitUnderAnotherRoot holds the stricter line of
// console pin authoring. These ownership states allow a prune, but a scope
// edit plans for this workspace. So a plan made in the recorded root
// refuses when a console in another workspace commits it, and neither
// store changes.
func TestEngineRefusesAScopeCommitUnderAnotherRoot(t *testing.T) {
	cases := []struct {
		name string
		ws   func(t *testing.T, origin workspace.Workspace) workspace.Workspace
	}{
		{"a database of another repository", func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			t.Helper()

			ws, err := workspace.Resolve(rootDir(t), origin.DBPath)
			require.NoError(t, err)

			return ws
		}},
		{"a linked default path", func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			t.Helper()

			root := rootCopy(t, origin, "copy")
			linked := store.DefaultPath(root)
			require.NoError(t, os.Remove(linked))
			require.NoError(t, os.Symlink(origin.DBPath, linked))

			ws, err := workspace.Resolve(root, linked)
			require.NoError(t, err)

			return ws
		}},
		{"a shared file", func(t *testing.T, origin workspace.Workspace) workspace.Workspace {
			t.Helper()

			db := filepath.Join(rootDir(t), "custom.db")
			require.NoError(t, os.Rename(origin.DBPath, db))

			ws, err := workspace.Resolve(rootDir(t), db)
			require.NoError(t, err)

			return ws
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin, linked := maintWorkspace(t, true)
			e := engNew(origin, Deps{})

			plan, err := e.PlanScope(scopeRequest(t, e, "api", "web"))
			require.NoError(t, err)
			require.True(t, plan.Confirmable, "%v", plan.Blocked)

			ws := c.ws(t, origin)
			other := engNew(ws, Deps{})
			require.NoError(t, other.Pins().Database.WriteBlock, "this state allows a prune")

			file := maintFile(t, origin)

			res, err := other.CommitMaintenance(plan)
			require.ErrorIs(t, err, ErrForeignRoot)
			assert.False(t, res.FileWritten)
			assert.Equal(t, file, maintFile(t, origin), "the pin file stays")
			engUnlocked(t, other)

			row := reviewRow(t, ws, linked.ID)
			assert.Equal(t, "api", row.Region, "the ledger stays")
			assert.Zero(t, row.ScopeChosen)
		})
	}
}
