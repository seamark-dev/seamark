package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// These tests drive the console over real repositories, real databases
// and the real engine. The rules compare files and directories, and a
// fake has neither.

// dbRootPins holds one linked, one pending and one free pin.
const dbRootPins = "pin:\n" +
	"  - rule: linked-rule\n    region: api\n    note: Linked note here.\n" +
	"  - rule: pending-rule\n    region: api\n    note: Pending note here.\n" +
	"  - rule: free-rule\n    region: api\n    note: Free note here.\n"

// dbRootDir returns a new directory by its resolved path, because the
// index records the resolved root.
func dbRootDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

func dbRootGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// dbRootRepo builds a clean indexed repository with the pins of
// dbRootPins and their ledger rows.
func dbRootRepo(t *testing.T, name string) string {
	t.Helper()

	root := filepath.Join(dbRootDir(t), name)
	require.NoError(t, os.MkdirAll(filepath.Join(root, store.DefaultDir), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api"), 0o755))

	files := map[string]string{
		filepath.Join("api", "main.go"):           "package api\n",
		".gitignore":                              ".seamark/\n",
		filepath.Join(".seamark", "lessons.yaml"): dbRootPins,
		filepath.Join(".seamark", "config.yaml"):  "distill:\n  write: true\n",
	}

	for path, text := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(text), 0o644))
	}

	dbRootGit(t, root, "init", "-q", "-b", "main")
	dbRootGit(t, root, "add", "-A")
	dbRootGit(t, root, "commit", "-qm", "initial")

	_, err := index.Run(index.Options{Root: root})
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	require.NoError(t, storetest.InsertProposal(st, &domain.Proposal{
		Signature: "sig-l", Rule: "linked-rule", Region: "api", Note: "Linked note here.",
		Members: []int64{1}, Status: domain.ProposalApplied,
	}))
	require.NoError(t, storetest.InsertProposal(st, &domain.Proposal{
		Signature: "sig-p", Rule: "pending-rule", Region: "api", Note: "Pending note here.",
		Members: []int64{2}, Status: domain.ProposalProposed,
	}))

	return root
}

// dbRootCopy returns a full copy of root with its index. The copy still
// records root, as a copied checkout does before its first build.
func dbRootCopy(t *testing.T, root string) string {
	t.Helper()

	dst := filepath.Join(dbRootDir(t), "copy")
	require.NoError(t, os.CopyFS(dst, os.DirFS(root)))

	return dst
}

// dbRootLink returns a new link to dir.
func dbRootLink(t *testing.T, dir string) string {
	t.Helper()

	link := filepath.Join(dbRootDir(t), "link")
	require.NoError(t, os.Symlink(dir, link))

	return link
}

// dbRootConsole opens Lessons › Pins over the real engine.
func dbRootConsole(t *testing.T, dir, db string) (*harness, services.All) {
	t.Helper()

	ws, err := workspace.Resolve(dir, db)
	require.NoError(t, err)

	svc := services.New(ws, "test", "/usr/local/bin/seamark")

	h := newHarness(t, svc, 150, 50)
	h.press("3", "m")

	return h, svc
}

// dbRootRecorded reads the root that the database at path records.
func dbRootRecorded(t *testing.T, path string) string {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	root, err := st.GetMeta(store.MetaRepoRoot)
	require.NoError(t, err)

	return root
}

// dbRootPrune plans a prune of the first pin through the real engine.
func dbRootPrune(t *testing.T, svc services.All) *maintain.Plan {
	t.Helper()

	plan, err := svc.PlanMaintenance(maintain.Request{
		Action: maintain.ActionPrune, Refs: []pinfile.EntryRef{svc.Pins().Pins[0].Entry.Ref()}, Cursor: true,
	})
	require.NoError(t, err)
	require.True(t, plan.Confirmable, "%v", plan.Blocked)

	return plan
}

// assertCopyRoutes holds the state of a copied checkout: the keys are
// off, the route is the build, and the build of that route works.
func assertCopyRoutes(t *testing.T, h *harness, svc services.All, origin, copied string) {
	t.Helper()

	pinsCursorTo(h, 1)

	p := pinsOf(h)
	assert.Equal(t, origin, pinsForeignRoot(p.st))
	assert.Empty(t, pinsDBRoot(p.st))

	detail := pinsDetail(t, h)
	assert.Contains(t, detail, "Build the index of this checkout")
	assert.NotContains(t, detail, "open the console")

	before, err := os.ReadFile(filepath.Join(origin, pinfile.File))
	require.NoError(t, err)

	_, err = svc.CommitMaintenance(dbRootPrune(t, svc))
	require.ErrorIs(t, err, services.ErrForeignRoot)

	after, err := os.ReadFile(filepath.Join(origin, pinfile.File))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the pin file of the origin stays")

	h.press("1", "b", "enter")
	h.press("enter", "3", "m", "r")

	assert.Equal(t, copied, dbRootRecorded(t, store.DefaultPath(copied)))
	assert.Empty(t, pinsForeignRoot(pinsOf(h).st), "the build of the route ends the state")
}

func TestCopyThroughALinkedDBKeepsTheRoutesOfACopy(t *testing.T) {
	origin := dbRootRepo(t, "origin")
	copied := dbRootCopy(t, origin)

	h, svc := dbRootConsole(t, copied, store.DefaultPath(dbRootLink(t, copied)))

	assertCopyRoutes(t, h, svc, origin, copied)
}

func TestCopyThroughARelativeDBKeepsTheRoutesOfACopy(t *testing.T) {
	origin := dbRootRepo(t, "origin")
	copied := dbRootCopy(t, origin)

	t.Chdir(dbRootLink(t, copied))

	h, svc := dbRootConsole(t, ".", filepath.Join(store.DefaultDir, store.DefaultDBName))
	require.Equal(t, copied, svc.Root())

	assertCopyRoutes(t, h, svc, origin, copied)
}

// TestMovedRepositoryWithALinkIsNotForeign covers a repository that moved
// while a link keeps the old path alive. The index records the old
// spelling of this directory, and the engine accepts it. The build does
// nothing there, so a foreign state in the console has no way out.
func TestMovedRepositoryWithALinkIsNotForeign(t *testing.T) {
	parent := dbRootDir(t)
	oldHome, newHome := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	require.NoError(t, os.MkdirAll(oldHome, 0o755))
	require.NoError(t, os.Rename(dbRootRepo(t, "x"), filepath.Join(oldHome, "x")))

	st, err := store.Open(store.DefaultPath(filepath.Join(oldHome, "x")))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, filepath.Join(oldHome, "x")))
	require.NoError(t, st.Close())

	require.NoError(t, os.Rename(oldHome, newHome))
	require.NoError(t, os.Symlink(newHome, oldHome))

	h, svc := dbRootConsole(t, filepath.Join(newHome, "x"), "")
	require.Equal(t, filepath.Join(oldHome, "x"), svc.Pins().Index.Root, "the index keeps the old spelling")

	p := pinsOf(h)
	assert.Empty(t, authorForeignRoot(p.st))
	assert.Empty(t, pinsForeignRoot(p.st))

	pinsCursorTo(h, 1)
	assert.Subset(t, pinsKeys(h), []string{"-"}, "the prune stays on, as the engine allows it")

	frame := strings.Join(strings.Fields(h.view()), " ")
	assert.NotContains(t, frame, "another repository")
	assert.NotContains(t, pinsDetail(t, h), "Build the index")
}
