package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the pin authoring services. They run the
// production engine over real temporary workspaces. internal/maintain
// tests the authoring engine itself. These tests prove the adapter: the
// effective root, the foreign-root rule, the read-only store, the lock.

// authorCreate is the request of a new repository-wide pin.
func authorCreate() maintain.AuthorRequest {
	return maintain.AuthorRequest{
		Op: maintain.AuthorCreate, Rule: "check-errors", Note: "Check every returned error.",
		Scope: maintain.ScopeRepoWide, RuleSet: true, NoteSet: true, ScopeSet: true,
	}
}

// recordRoot makes the index of ws record root as its repository root,
// as the index of a copied checkout does.
func recordRoot(t *testing.T, ws workspace.Workspace, root string) {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))
	require.NoError(t, st.Close())
}

// foreignCases returns two engines whose effective root is not their
// workspace root: a copied checkout, and a --db of another repository.
// Each case names the root that the index records. P4-16 has no --db
// exception, so both cases must behave the same.
func foreignCases(t *testing.T) map[string]struct {
	e     *engine
	other string
} {
	t.Helper()

	copied, _ := maintWorkspace(t, true)
	other := t.TempDir()
	recordRoot(t, copied, other)

	origin, _ := maintWorkspace(t, true)
	dbOf, err := workspace.Resolve(t.TempDir(), origin.DBPath)
	require.NoError(t, err)

	return map[string]struct {
		e     *engine
		other string
	}{
		"a copied checkout":            {e: engNew(copied, Deps{}), other: other},
		"a --db of another repository": {e: engNew(dbOf, Deps{}), other: origin.Root},
	}
}

func TestEnginePlanAuthorNamesAnotherRootAsABlocker(t *testing.T) {
	for name, tc := range foreignCases(t) {
		t.Run(name, func(t *testing.T) {
			before := servicestest.Tree(t, tc.other)

			plan, err := tc.e.PlanAuthor(authorCreate())
			require.NoError(t, err, "a blocker is data in the plan, not an error")

			assert.Equal(t, tc.other, plan.Root, "the plan reads the file that Pins lists")
			assert.False(t, plan.Confirmable)
			require.NotNil(t, plan.After, "the candidate is still computed (P4-16)")
			assert.Contains(t, string(plan.After), "check-errors")

			var foreign *maintain.AuthorBlocker

			for i := range plan.Blockers {
				if plan.Blockers[i].Kind == maintain.BlockForeignRoot {
					foreign = &plan.Blockers[i]
				}
			}

			require.NotNil(t, foreign, "%v", plan.Blockers)
			assert.Contains(t, foreign.Text, ErrForeignRoot.Error())
			assert.Contains(t, foreign.Text, tc.other)
			assert.Contains(t, foreign.Text, tc.e.ws.Root)
			assert.Equal(t, before, servicestest.Tree(t, tc.other), "a plan writes nothing")
		})
	}

	// The own root is no blocker: the same request is confirmable.
	ws, _ := maintWorkspace(t, true)

	plan, err := engNew(ws, Deps{}).PlanAuthor(authorCreate())
	require.NoError(t, err)
	assert.True(t, plan.Confirmable, "%v", plan.Blockers)
}

func TestEngineCommitAuthorRefusesAnotherRoot(t *testing.T) {
	t.Run("a root that turns foreign after the review", func(t *testing.T) {
		ws, _ := maintWorkspace(t, true)
		e := engNew(ws, Deps{})

		plan, err := e.PlanAuthor(authorCreate())
		require.NoError(t, err)
		require.True(t, plan.Confirmable, "%v", plan.Blockers)

		other := t.TempDir()
		recordRoot(t, ws, other)

		before := servicestest.Tree(t, ws.Root)

		res, err := e.CommitAuthor(plan)
		require.ErrorIs(t, err, ErrForeignRoot)
		assert.Equal(t, maintain.NotWritten, res.Outcome)
		require.ErrorIs(t, res.Err, ErrForeignRoot)
		assert.Equal(t, before, servicestest.Tree(t, ws.Root), "the refusal writes nothing")
		assert.NoFileExists(t, filepath.Join(other, filepath.FromSlash(pinfile.File)), "nothing is created")
		engUnlocked(t, e)
	})

	for name, tc := range foreignCases(t) {
		t.Run(name, func(t *testing.T) {
			plan, err := tc.e.PlanAuthor(authorCreate())
			require.NoError(t, err)

			own := servicestest.Tree(t, tc.e.ws.Root)
			other := servicestest.Tree(t, tc.other)

			// The foreign root is the first refusal, before the plan is
			// found not confirmable.
			res, err := tc.e.CommitAuthor(plan)
			require.ErrorIs(t, err, ErrForeignRoot)
			assert.Equal(t, maintain.NotWritten, res.Outcome)
			assert.Equal(t, own, servicestest.Tree(t, tc.e.ws.Root))
			assert.Equal(t, other, servicestest.Tree(t, tc.other))
			engUnlocked(t, tc.e)
		})
	}
}

// TestEngineCommitAuthorRefusesAHardLinkOrCRLF drives the file refusals
// of P4-17 and P4-6 through CommitAuthor. A hard link changes no guarded
// byte, so the new blocker itself makes the review stale. CRLF changes
// the bytes, so the review is stale, and the next review names the CRLF
// block and cannot be confirmed.
func TestEngineCommitAuthorRefusesAHardLinkOrCRLF(t *testing.T) {
	t.Run("a hard link after the review", func(t *testing.T) {
		ws, _ := maintWorkspace(t, true)
		e := engNew(ws, Deps{})

		plan, err := e.PlanAuthor(authorCreate())
		require.NoError(t, err)
		require.True(t, plan.Confirmable, "%v", plan.Blockers)

		hardLink(t, ws.Root)

		res, err := e.CommitAuthor(plan)

		var stale *maintain.StaleError
		require.ErrorAs(t, err, &stale)
		assert.Contains(t, stale.Differences, integration.HardLinkReason+"; edit .seamark/lessons.yaml by hand")
		assert.Equal(t, maintain.NotWritten, res.Outcome)
		assert.Equal(t, err, res.Err)
		assert.Equal(t, maintPins, maintFile(t, ws), "the refusal keeps the file and its link")
		engUnlocked(t, e)
	})

	t.Run("CRLF line breaks after the review", func(t *testing.T) {
		ws, _ := maintWorkspace(t, true)
		e := engNew(ws, Deps{})

		plan, err := e.PlanAuthor(authorCreate())
		require.NoError(t, err)
		require.True(t, plan.Confirmable, "%v", plan.Blockers)

		crlf := strings.ReplaceAll(maintPins, "\n", "\r\n")
		servicestest.Write(t, ws, pinfile.File, crlf)

		res, err := e.CommitAuthor(plan)
		require.ErrorIs(t, err, maintain.ErrStale, "the reviewed bytes changed")
		assert.Equal(t, maintain.NotWritten, res.Outcome)
		engUnlocked(t, e)

		again, err := e.PlanAuthor(authorCreate())
		require.NoError(t, err)
		assert.False(t, again.Confirmable)
		require.Len(t, again.Blockers, 1)
		assert.Contains(t, again.Blockers[0].String(), "the file uses CRLF line breaks; edit .seamark/lessons.yaml by hand")

		res, err = e.CommitAuthor(again)
		require.ErrorIs(t, err, maintain.ErrNotConfirmable)
		assert.Equal(t, maintain.NotWritten, res.Outcome)
		assert.Equal(t, crlf, maintFile(t, ws), "no refusal writes")
		engUnlocked(t, e)
	})
}

func TestEnginePlanAuthorWithoutAnIndexNamesTheLedger(t *testing.T) {
	root := t.TempDir()
	servicestest.Git(t, root, "init", "-b", "main")

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)
	servicestest.Write(t, ws, pinfile.File, maintPins)

	e := engNew(ws, Deps{})
	before := servicestest.Tree(t, ws.Root)

	plan, err := e.PlanAuthor(authorCreate())
	require.NoError(t, err)

	assert.Equal(t, ws.Root, plan.Root, "without an index the workspace root applies")
	assert.False(t, plan.LinksKnown)
	assert.False(t, plan.Confirmable)
	require.NotNil(t, plan.After, "the preview still works (P4-8)")

	kinds := make([]maintain.BlockKind, 0, len(plan.Blockers))
	for _, b := range plan.Blockers {
		kinds = append(kinds, b.Kind)
	}

	assert.Contains(t, kinds, maintain.BlockLedger)
	assert.NotContains(t, kinds, maintain.BlockForeignRoot, "no index records another root")

	res, err := e.CommitAuthor(plan)
	require.Error(t, err)
	assert.Equal(t, maintain.NotWritten, res.Outcome)
	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a plan and a refused commit write nothing")
	assert.NoFileExists(t, ws.DBPath, "the commit creates no database")
	engUnlocked(t, e)
}

func TestEngineCommitsAReviewedPinCreate(t *testing.T) {
	ws, _ := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	ledger, err := e.Ledger()
	require.NoError(t, err)

	plan, err := e.PlanAuthor(authorCreate())
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blockers)

	res, err := e.CommitAuthor(plan)
	require.NoError(t, err)
	assert.Equal(t, maintain.Written, res.Outcome)
	require.NotNil(t, res.NewRef)
	assert.Equal(t, string(plan.After), maintFile(t, ws), "the reviewed bytes are the file")
	engUnlocked(t, e)

	again, err := e.Ledger()
	require.NoError(t, err)
	assert.Equal(t, ledger, again, "an authoring commit never writes the ledger")

	// The same reviewed plan never runs twice: the file changed.
	_, err = e.CommitAuthor(plan)
	require.ErrorIs(t, err, maintain.ErrStale)
	engUnlocked(t, e)
}

func TestEngineLinkedNoteEditKeepsHistory(t *testing.T) {
	ws, linked := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	// Delivery history that a note edit must not touch.
	audit := reviews.AuditLogPath(ws.Root)
	state := filepath.Join(ws.Root, ".seamark", "lessons-hook-state.json")
	servicestest.Write(t, ws, ".seamark/lessons-audit.jsonl", "{\"rule\":\"close-db-handles\"}\n")
	servicestest.Write(t, ws, ".seamark/lessons-hook-state.json", "{\"version\":1}\n")

	auditBefore, err := os.ReadFile(audit)
	require.NoError(t, err)
	stateBefore, err := os.ReadFile(state)
	require.NoError(t, err)

	ledger, err := e.Ledger()
	require.NoError(t, err)

	in := e.Pins()
	require.Equal(t, pinfile.LinkLinked, in.Pins[0].Link.Link)

	const newNote = "Close every handle, also on the error path."

	plan, err := e.PlanAuthor(maintain.AuthorRequest{
		Op: maintain.AuthorEdit, Ref: in.Pins[0].Entry.Ref(), Note: newNote, NoteSet: true,
	})
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blockers)
	assert.Equal(t, pinfile.LinkLinked, plan.Link)

	res, err := e.CommitAuthor(plan)
	require.NoError(t, err)
	assert.Equal(t, maintain.Written, res.Outcome)
	assert.Contains(t, maintFile(t, ws), newNote)
	engUnlocked(t, e)

	auditAfter, err := os.ReadFile(audit)
	require.NoError(t, err)
	stateAfter, err := os.ReadFile(state)
	require.NoError(t, err)
	assert.Equal(t, auditBefore, auditAfter, "the audit log stays byte for byte")
	assert.Equal(t, stateBefore, stateAfter, "the hook state stays byte for byte")

	again, err := e.Ledger()
	require.NoError(t, err)
	assert.Equal(t, ledger, again, "the proposal row keeps its recorded rule, note, and evidence")

	// Pins shows the difference as an edit of the linked pin.
	in = e.Pins()
	assert.Equal(t, pinfile.LinkLinked, in.Pins[0].Link.Link)
	assert.True(t, in.Pins[0].Link.NoteChanged)

	// Trigger extraction still asks about the recorded note, and the
	// preview names the live note beside it.
	preview, err := e.PlanExtract(learning.ExtractRequest{IDs: []int64{linked.ID}})
	require.NoError(t, err)
	require.NotEmpty(t, preview.Plan.Batches)
	assert.Contains(t, preview.Plan.Batches[0].Prompt, linked.Note)
	assert.NotContains(t, preview.Plan.Batches[0].Prompt, newNote)
	assert.Equal(t, newNote, preview.LiveNotes[linked.ID])
}
