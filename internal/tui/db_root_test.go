package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
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

// dbRootFlat joins the words of text on one line, because a frame wraps.
func dbRootFlat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// dbRootSchema stamps the database at path with another schema version.
func dbRootSchema(t *testing.T, path string, version int) {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaSchemaVersion, strconv.Itoa(version)))
	require.NoError(t, st.Close())
}

// dbRootOffersBuild reports whether the Overview footer offers b.
func dbRootOffersBuild(h *harness) bool {
	return slices.ContainsFunc(ovScreen(h).keys(), func(b key.Binding) bool {
		return b.Enabled() && slices.Contains(b.Keys(), "b")
	})
}

// dbRootRefusal is the refusal of a build that would move root.
func dbRootRefusal(root string) string {
	return "The index records another repository root, so a build here would move it; open the console in " + root + "."
}

func TestOverviewRefusesABuildThatMovesAnotherRoot(t *testing.T) {
	origin, other := dbRootRepo(t, "origin"), dbRootRepo(t, "other")

	// A new commit makes the index stale, so the list holds a build item.
	require.NoError(t, os.WriteFile(filepath.Join(origin, "api", "more.go"), []byte("package api\n"), 0o644))
	dbRootGit(t, origin, "add", "-A")
	dbRootGit(t, origin, "commit", "-qm", "second")

	h, _ := dbRootConsole(t, other, store.DefaultPath(origin))
	h.press("1")

	o := ovScreen(h)

	assert.False(t, dbRootOffersBuild(h), "the footer offers no key that refuses")

	h.press("b")
	assert.Equal(t, dbRootRefusal(origin), h.m.toast.text)
	assert.False(t, o.capturing(), "no confirmation opens")

	h.send(navigateMsg{dest: destOverview, intent: ovIntentBuild})
	assert.False(t, o.capturing(), "a route of another screen ends at the same refusal")

	// The stale item of this workspace offers a build by default. Here
	// the list, the detail and the shell command agree with the key.
	it := ovFind(t, o, "index:freshness")
	assert.Equal(t, ovActNone, it.act.kind)
	assert.Empty(t, o.rowHint(it))

	detail := dbRootFlat(ovDetail(o, it, 400))
	assert.Contains(t, detail, dbRootRefusal(origin))
	assert.NotContains(t, detail, "seamark index")

	assert.Equal(t, ovAction{}, checkAction(o.st, doctor.Check{Name: "index", State: doctor.StateFail}),
		"the Doctor tab offers no build either")

	assert.Equal(t, origin, dbRootRecorded(t, store.DefaultPath(origin)))
}

func TestOverviewRefusesTheBuildOfAnOlderDatabaseOfAnotherRoot(t *testing.T) {
	origin, other := dbRootRepo(t, "origin"), dbRootRepo(t, "other")
	dbRootSchema(t, store.DefaultPath(origin), store.SupportedSchema()-1)

	h, _ := dbRootConsole(t, other, store.DefaultPath(origin))
	h.press("1")

	o := ovScreen(h)

	h.press("b")
	assert.Equal(t, dbRootRefusal(origin), h.m.toast.text)

	it := ovFind(t, o, "index:state")
	assert.Equal(t, ovActNone, it.act.kind)
	assert.Contains(t, dbRootFlat(ovDetail(o, it, 400)), dbRootRefusal(origin))

	_, lines := o.probeCard(o.st.index.value, 60)
	card := strings.Join(lines, " ")
	assert.Contains(t, card, "no build in this workspace")
	assert.NotContains(t, card, "a build migrates it")
}

func TestOverviewKeepsTheFirstBuildOfANewDatabase(t *testing.T) {
	other := dbRootRepo(t, "other")
	db := filepath.Join(dbRootDir(t), "new.db")

	h, _ := dbRootConsole(t, other, db)
	h.press("1")

	assert.True(t, dbRootOffersBuild(h))

	h.press("b")
	require.True(t, ovScreen(h).capturing())

	h.press("enter")
	assert.Equal(t, other, dbRootRecorded(t, db))
}

// TestOverviewOffersNoSecondTryAfterARefusedBuild covers a read that
// came before the refusal: the first build reaches the engine.
func TestOverviewOffersNoSecondTryAfterARefusedBuild(t *testing.T) {
	f := ovPopulated()
	refused := errors.New("the index records another repository root")

	f.buildIndex = func(bool, func(services.IndexEvent)) (*index.Summary, error) {
		f.index.Database.BuildBlock = refused

		return nil, refused
	}

	h := newHarness(t, f, 120, 40)
	h.press("b", "enter")

	frame := h.view()
	require.Contains(t, frame, "Index build failed")
	assert.NotContains(t, frame, "build again")
	assert.False(t, dbRootOffersBuild(h))
}

// TestPinsNameNoBuildThatTheKeyRefuses covers the detail and the history
// filters while the index is not ready. A route may name a build only
// where b runs one.
func TestPinsNameNoBuildThatTheKeyRefuses(t *testing.T) {
	newer := func(string) string { return "A newer seamark wrote this index. This binary never rebuilds it." }

	const refusedMark, newerMark = "open the console in", "This binary never rebuilds it."

	cases := []struct {
		name    string
		version func() int
		foreign bool
		want    func(root string) string
		// mark is a part of want that a wrapped frame keeps on one line.
		mark string
	}{
		{"an older database of another repository", func() int { return store.SupportedSchema() - 1 }, true, dbRootRefusal, refusedMark},
		{"a newer database of another repository", func() int { return store.SupportedSchema() + 1 }, true, newer, newerMark},
		{"a newer default database", func() int { return store.SupportedSchema() + 1 }, false, newer, newerMark},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin := dbRootRepo(t, "origin")
			dbRootSchema(t, store.DefaultPath(origin), c.version())

			dir, db := origin, ""
			if c.foreign {
				dir, db = dbRootRepo(t, "other"), store.DefaultPath(origin)
			}

			h, _ := dbRootConsole(t, dir, db)
			pinsCursorTo(h, 1)

			detail := pinsDetail(t, h)
			assert.Contains(t, detail, c.want(origin))
			assert.NotContains(t, detail, "Build the index")
			assert.NotContains(t, detail, "seamark index")

			// The next filters read the ledger, so they name a route too.
			for range 2 {
				h.press("f")

				frame := dbRootFlat(h.view())
				assert.Contains(t, frame, c.mark)
				assert.NotContains(t, frame, "to build the index")
				assert.NotContains(t, frame, "seamark index")
			}
		})
	}
}

// TestPinsKeepTheBuildRouteWhereTheKeyBuilds holds the other side of the
// rule: a first build and a migration of the own database still work.
func TestPinsKeepTheBuildRouteWhereTheKeyBuilds(t *testing.T) {
	t.Run("a new database under --db", func(t *testing.T) {
		h, _ := dbRootConsole(t, dbRootRepo(t, "other"), filepath.Join(dbRootDir(t), "new.db"))
		pinsCursorTo(h, 1)

		assert.Contains(t, pinsDetail(t, h), "Build the index from Overview (press 1, then b)")
	})

	t.Run("an older default database", func(t *testing.T) {
		origin := dbRootRepo(t, "origin")
		dbRootSchema(t, store.DefaultPath(origin), store.SupportedSchema()-1)

		h, _ := dbRootConsole(t, origin, "")
		pinsCursorTo(h, 1)

		assert.Contains(t, pinsDetail(t, h), "Build the index from Overview (press 1, then b)")
	})
}

// dbRootReview opens the review of a note edit of the free pin.
func dbRootReview(t *testing.T, h *harness) *pinReview {
	t.Helper()

	pinsCursorTo(h, 3)
	h.press("i")
	require.NotNil(t, authorOf(h).form)

	h.press("down", "enter")
	h.typeText(" More.")
	h.press("esc", "s")

	review := authorOf(h).review
	require.NotNil(t, review)
	require.NotNil(t, review.preview)

	return review
}

// dbRootLedgerLine returns the line of the ledger blocker of review.
func dbRootLedgerLine(t *testing.T, review *pinReview) string {
	t.Helper()

	for _, b := range review.preview.Blockers {
		if b.Kind == maintain.BlockLedger {
			return pinBlockerLine(review.st, b)
		}
	}

	require.Fail(t, "the review names no ledger blocker")

	return ""
}

func TestPinReviewOpensNoBuildThatTheKeyRefuses(t *testing.T) {
	origin, other := dbRootRepo(t, "origin"), dbRootRepo(t, "other")
	dbRootSchema(t, store.DefaultPath(origin), store.SupportedSchema()-1)

	h, _ := dbRootConsole(t, other, store.DefaultPath(origin))
	review := dbRootReview(t, h)

	line := dbRootLedgerLine(t, review)
	assert.Contains(t, line, "open the console in "+origin)
	assert.NotContains(t, line, "build the index in Overview")

	assert.Negative(t, review.routed(), "o opens no build")
}

func TestPinReviewKeepsTheBuildOfANewDatabase(t *testing.T) {
	h, _ := dbRootConsole(t, dbRootRepo(t, "other"), filepath.Join(dbRootDir(t), "new.db"))
	review := dbRootReview(t, h)

	assert.Contains(t, dbRootLedgerLine(t, review), "build the index in Overview")
	require.GreaterOrEqual(t, review.routed(), 0)

	dest, intent, _, ok := review.route()
	assert.True(t, ok)
	assert.Equal(t, destOverview, dest)
	assert.Equal(t, ovIntentBuild, intent)
}

// TestMaintPreviewFailureNamesNoBuildThatTheKeyRefuses covers the plan
// error of an index that the console read as readable.
func TestMaintPreviewFailureNamesNoBuildThatTheKeyRefuses(t *testing.T) {
	fx := maintFixture(t)
	req := maintain.Request{Action: maintain.ActionRetarget, Cursor: true,
		Refs: []pinfile.EntryRef{fx.pins[maintWrap].Entry.Ref()}}

	cases := map[string]struct {
		edit func(f *fakeServices)
		want string
	}{
		"a newer schema": {func(f *fakeServices) { f.pins.Index.State = workspace.IndexNewer }, "never rebuilds it"},
		"another repository": {func(f *fakeServices) {
			f.database = &workspace.DatabaseFacts{
				Kind: store.DatabaseOther, BuildBlock: errors.New("the index records another repository root"),
			}
		}, "The index records another repository root."},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := maintFakeOf(fx, nil, nil)
			f.planMaintenance = func(maintain.Request) (*maintain.Plan, error) {
				return nil, fmt.Errorf("%w (older)", services.ErrNoLedger)
			}

			c.edit(f)

			text := maintContent(t, maintOpen(t, f, req, 120, 40))
			assert.Contains(t, text, c.want)
			assert.NotContains(t, text, "Build it from Overview")
		})
	}
}

func TestSettingsRoutesToTheRecordedRootUnderAnotherDatabase(t *testing.T) {
	origin, other := dbRootRepo(t, "origin"), dbRootRepo(t, "other")

	ws, err := workspace.Resolve(other, store.DefaultPath(origin))
	require.NoError(t, err)

	h := lessonsOpen(t, services.New(ws, "test", "/usr/local/bin/seamark"), "group:delivery", 150, 50)

	s := settingsScr(h)
	block := settingsBlock(s.snap().Editable(settings.GroupDelivery), pinfile.File)
	assert.Equal(t, "Open the console in "+origin+", because a save works there.", block.route)
	assert.False(t, s.foreignRoot(), "the route names no build, so o opens none")

	frame := h.view()
	assert.NotContains(t, dbRootFlat(frame), "Build the index")
	assert.NotContains(t, footerLine(frame), "index build")

	h.press("o")
	assert.Equal(t, destSettings, h.m.active)

	h.press("d")
	assert.Contains(t, h.m.toast.text, "Open the console in "+origin)
}

func TestSettingsKeepsTheBuildRouteOfACopiedCheckout(t *testing.T) {
	copied := dbRootCopy(t, dbRootRepo(t, "origin"))

	ws, err := workspace.Resolve(copied, store.DefaultPath(dbRootLink(t, copied)))
	require.NoError(t, err)

	h := lessonsOpen(t, services.New(ws, "test", "/usr/local/bin/seamark"), "group:delivery", 150, 50)

	s := settingsScr(h)
	assert.Equal(t, settingsForeignRootRoute, settingsBlock(s.snap().Editable(settings.GroupDelivery), pinfile.File).route)
	assert.True(t, s.foreignRoot())
	assert.Contains(t, footerLine(h.view()), "o index build")

	h.press("o")
	assert.Equal(t, destOverview, h.m.active)
	assert.True(t, ovScreen(h).capturing(), "o opens the build of a copied checkout")
}

// TestLinkedDefaultPathRoutesToItsOwner covers a worktree that shares the
// database of another worktree. Retarget and prune stay off, as without a
// link. A build refuses, so every route names the console of the owner.
func TestLinkedDefaultPathRoutesToItsOwner(t *testing.T) {
	origin := dbRootRepo(t, "origin")
	side := dbRootCopy(t, origin)

	require.NoError(t, os.Remove(store.DefaultPath(side)))
	require.NoError(t, os.Symlink(store.DefaultPath(origin), store.DefaultPath(side)))

	h, svc := dbRootConsole(t, side, "")

	p := pinsOf(h)
	assert.Equal(t, origin, pinsForeignRoot(p.st), "the keys stay off")
	assert.Equal(t, origin, pinsDBRoot(p.st), "the route names the owner")

	for pin := 1; pin <= 3; pin++ {
		pinsCursorTo(h, pin)

		detail := pinsDetail(t, h)
		assert.NotContains(t, detail, "Build the index", "pin %d", pin)

		h.press("w")
		assert.Contains(t, strings.ToLower(h.m.toast.text), "open the console in "+strings.ToLower(origin), "pin %d", pin)
		assert.NotContains(t, h.m.toast.text, "press 1", "pin %d", pin)
	}

	_, err := svc.CommitMaintenance(dbRootPrune(t, svc))
	require.ErrorIs(t, err, services.ErrForeignRoot)
	assert.Contains(t, err.Error(), "open the console in "+origin)

	h.press("1")
	h.press("b")
	assert.Equal(t, dbRootRefusal(origin), h.m.toast.text)
	assert.Equal(t, origin, dbRootRecorded(t, store.DefaultPath(origin)))
}
