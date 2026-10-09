package learning

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// evGitIn runs git in root with a hermetic identity and config. Global
// config must not leak in, because it can rewrite remotes.
func evGitIn(t *testing.T, root string, args ...string) {
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

// evWorkspace builds an indexed git workspace with one fix commit and a
// seeded evidence corpus: one review lesson with one review finding, and
// one fix finding. Both stamps read 1000. origin is the origin URL; ""
// means no remote.
func evWorkspace(t *testing.T, origin string) workspace.Workspace {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/ev\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package ev\n\n// A returns one.\nfunc A() int { return 1 }\n"), 0o644))

	evGitIn(t, root, "init", "-b", "main")
	evGitIn(t, root, "add", "-A")
	evGitIn(t, root, "commit", "-m", "initial")

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package ev\n\n// A returns one.\nfunc A() int { return 1 }\n\nfunc resetState() {}\n"), 0o644))
	evGitIn(t, root, "add", "-A")
	evGitIn(t, root, "commit", "-m", "fix: reset state before reuse (#42)")

	if origin != "" {
		evGitIn(t, root, "remote", "add", "origin", origin)
	}

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: ws.Root, DBPath: ws.DBPath})
	require.NoError(t, err)

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	require.NoError(t, st.ReplaceLessons([]model.Lesson{{
		ClusterKey: "a.go\x00seeded", Region: "a.go", Reviewer: "person", Symptom: "seeded", Occurrences: 1,
	}}, []model.Finding{{
		ID: 900, LessonKey: "a.go\x00seeded", Path: "a.go", Body: "seeded review", Source: model.SourceReview,
	}}))
	require.NoError(t, st.ReplaceFixFindings([]model.Finding{{
		ID: 901, Path: "a.go", Body: "fix: seeded", Source: model.SourceFixSubject,
	}}))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, "1000"))
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, "1000"))

	return ws
}

// evFileDigest returns the SHA-256 of a file, or "absent".
func evFileDigest(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}

	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// evMarkerTools puts gh and git scripts that record each call first on
// PATH. With keepPath false, PATH holds only them, so no real git runs.
func evMarkerTools(t *testing.T, keepPath bool, tools ...string) (marker string) {
	t.Helper()

	bin := t.TempDir()
	marker = filepath.Join(bin, "calls")

	for _, tool := range tools {
		script := "#!/bin/sh\necho " + tool + " \"$@\" >> '" + marker + "'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755))
	}

	path := bin
	if keepPath {
		path += string(os.PathListSeparator) + os.Getenv("PATH")
	}

	t.Setenv("PATH", path)

	return marker
}

// evFetch returns a FetchFunc that answers with raw and err.
func evFetch(raw string, err error) reviews.FetchFunc {
	return func(context.Context, string, string, reviews.Options) ([]byte, error) {
		if err != nil {
			return nil, err
		}

		return []byte(raw), nil
	}
}

// evNoFetch returns a FetchFunc that fails the test when it runs.
func evNoFetch(t *testing.T) reviews.FetchFunc {
	return func(context.Context, string, string, reviews.Options) ([]byte, error) {
		t.Error("the fetch must not run")

		return nil, errors.New("unexpected fetch")
	}
}

// evPage is one gh page with an inline comment that clusters into one
// lesson.
const evPage = `[{"id":11,"user":{"login":"rev","type":"User"},` +
	`"body":"Close the handle on every error path.","path":"a.go","line":3,` +
	`"html_url":"https://github.com/o/r/pull/1#discussion_r11","created_at":"2026-07-21T10:00:00Z",` +
	`"pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

// evAssertReviewsKept checks that the seeded review corpus is intact.
func evAssertReviewsKept(t *testing.T, snap Snapshot) {
	t.Helper()

	require.NoError(t, snap.CountsErr)
	assert.Equal(t, 1, snap.Lessons, "the stored lessons stay")
	assert.Equal(t, 1, snap.ReviewFindings, "the stored review findings stay")
	assert.Equal(t, int64(1000), snap.ReviewsMinedAt, "the review stamp stays")
}

func TestInspectReadsStoredEvidenceWithoutWriting(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")
	before := evFileDigest(t, ws.DBPath)

	snap := Inspect(ws)

	require.NoError(t, snap.CountsErr)
	assert.Equal(t, workspace.IndexReady, snap.Probe.State)
	assert.Equal(t, 1, snap.Lessons)
	assert.Equal(t, 1, snap.ReviewFindings)
	assert.Equal(t, 1, snap.FixFindings)
	assert.Equal(t, 1, snap.Findings[model.SourceFixSubject])
	assert.Equal(t, int64(1000), snap.FixesMinedAt)
	assert.Equal(t, int64(1000), snap.ReviewsMinedAt)
	assert.Equal(t, "o/r", snap.Repository.Slug())
	assert.Equal(t, reviews.RepositoryGitHub, snap.RepoState)
	assert.Nil(t, snap.Window.Stored)
	assert.Equal(t, reviews.DefaultReviewWindowDays, snap.Window.Effective)
	assert.Equal(t, reviews.CountFloor, snap.Window.Floor)
	assert.False(t, snap.ObservedAt.IsZero())

	assert.Equal(t, before, evFileDigest(t, ws.DBPath), "an inspection never writes")
}

func TestInspectCountsDistilledGroupsWithoutProposals(t *testing.T) {
	ws := evWorkspace(t, "")

	snap := Inspect(ws)
	require.NoError(t, snap.CountsErr)
	assert.Zero(t, snap.DistilledGroups)

	// Both groups ended without a proposal. The ledger stays empty, and
	// only the records tell that a distillation ran.
	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, storetest.MarkDistilled(st, "sig-a", "a.go", 100))
	_, err = st.SaveDistilledGroup("sig-b", "", 200, nil)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	before := evFileDigest(t, ws.DBPath)
	snap = Inspect(ws)

	require.NoError(t, snap.CountsErr)
	assert.Equal(t, 2, snap.DistilledGroups)
	assert.Equal(t, before, evFileDigest(t, ws.DBPath), "the count uses the read-only store")

	// An evidence refresh keeps the record of distilled groups.
	p, err := PlanRefresh(ws, index.SourcesLocal)
	require.NoError(t, err)

	out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, out.After.DistilledGroups)
}

func TestInspectLeavesAnAbsentDatabaseAbsent(t *testing.T) {
	ws, err := workspace.Resolve(t.TempDir(), "")
	require.NoError(t, err)

	snap := Inspect(ws)

	assert.Equal(t, workspace.IndexAbsent, snap.Probe.State)
	require.Error(t, snap.CountsErr, "unknown counts are not zero")
	assert.Zero(t, snap.FixesMinedAt, "no stamp is invented")
	assert.Zero(t, snap.ReviewsMinedAt)
	assert.NoFileExists(t, ws.DBPath)
	assert.NoDirExists(t, filepath.Dir(ws.DBPath), "an inspection creates no .seamark directory")
}

// evInt returns a pointer to n.
func evInt(n int) *int {
	return &n
}

func TestInspectReadsTheWindowSetting(t *testing.T) {
	cases := []struct {
		yaml      string
		stored    *int
		effective int
	}{
		{yaml: "reviews:\n  window_days: 30\n", stored: evInt(30), effective: 30},
		{yaml: "reviews:\n  window_days: 0\n", stored: evInt(0), effective: -1},
		{yaml: "reviews:\n  window_days: -5\n", stored: evInt(-5), effective: -1},
		{yaml: "index:\n  generated: true\n", effective: reviews.DefaultReviewWindowDays},
	}

	ws := evWorkspace(t, "")

	for _, c := range cases {
		require.NoError(t, os.WriteFile(evConfigPath(ws.Root), []byte(c.yaml), 0o644))

		w := Inspect(ws).Window

		require.NoError(t, w.ConfigErr)
		assert.Equal(t, c.stored, w.Stored, c.yaml)
		assert.Equal(t, c.effective, w.Effective, c.yaml)
	}
}

func TestPlanRefreshDisclosesWithoutWork(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")
	marker := evMarkerTools(t, true, "gh")

	dbBefore := evFileDigest(t, ws.DBPath)
	configBefore := evFileDigest(t, evConfigPath(ws.Root))

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	assert.True(t, p.Ready(), "%v", p.Blockers)
	assert.Equal(t, index.SourcesReviewsAndLocal, p.Mode)
	assert.Equal(t, "o/r", p.Repository.Slug())
	assert.Equal(t, reviews.RepositoryGitHub, p.RepoState)
	assert.Equal(t, filepath.Join(filepath.Dir(marker), "gh"), p.GHPath, "gh is found, not run")
	assert.True(t, p.Network)
	assert.Len(t, p.Head, 40, "HEAD is a full commit id")
	assert.Equal(t, 365, p.FixWindowDays)
	assert.Equal(t, reviews.DefaultReviewWindowDays, p.Window.Effective)
	assert.Equal(t, 1, p.Current.Lessons)
	assert.Equal(t, 1, p.Current.FixFindings)
	assert.False(t, p.PlannedAt.IsZero())

	var replaced []string

	for _, e := range p.Affects {
		if e.Kind == EffectReplace {
			replaced = append(replaced, e.Source)
		}
	}

	assert.Equal(t, []string{index.SourceFixes, index.SourceReviews}, replaced)

	assert.NoFileExists(t, marker, "planning runs no gh")
	assert.Equal(t, dbBefore, evFileDigest(t, ws.DBPath), "planning writes no database")
	assert.Equal(t, configBefore, evFileDigest(t, evConfigPath(ws.Root)), "planning writes no config")
}

func TestPlanRefreshLocalModeKeepsReviews(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesLocal)
	require.NoError(t, err)

	assert.True(t, p.Ready())
	assert.False(t, p.Network, "local mode never contacts GitHub")

	kinds := map[string]EffectKind{}

	for _, e := range p.Affects {
		if e.Source != "" && e.Kind != EffectLater {
			kinds[e.Source] = e.Kind
		}
	}

	assert.Equal(t, map[string]EffectKind{
		index.SourceFixes: EffectReplace, index.SourceReviews: EffectKeep,
	}, kinds)
}

// evEffectKinds maps each source of the plan effects to its kind, and
// counts the LATER effects apart.
func evEffectKinds(p *Plan) (kinds map[string]EffectKind, later int) {
	kinds = map[string]EffectKind{}

	for _, e := range p.Affects {
		switch {
		case e.Kind == EffectLater:
			later++
		case e.Source != "":
			kinds[e.Source] = e.Kind
		}
	}

	return kinds, later
}

func TestPlanRefreshWithoutACommitKeepsTheFixFindings(t *testing.T) {
	// An orphan branch leaves HEAD without a commit, and the index and
	// its stored fix finding stay.
	ws := evWorkspace(t, "git@github.com:o/r.git")
	evGitIn(t, ws.Root, "checkout", "--orphan", "fresh")

	t.Run("local", func(t *testing.T) {
		p, err := PlanRefresh(ws, index.SourcesLocal)
		require.NoError(t, err)

		require.True(t, p.Ready(), "%v", p.Blockers)
		assert.Empty(t, p.Head, "HEAD names no commit")

		kinds, later := evEffectKinds(p)
		assert.Equal(t, map[string]EffectKind{
			index.SourceFixes: EffectKeep, index.SourceReviews: EffectKeep,
		}, kinds, "the plan promises no replacement that the run does not do")
		assert.Zero(t, later, "a refresh that changes no evidence has no later effect")

		out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
		require.NoError(t, err)

		assert.Equal(t, index.SourceUnavailable, out.Fixes.State)
		assert.Equal(t, index.CauseNoHistory, out.Fixes.Cause)
		assert.Equal(t, 1, out.After.FixFindings, "the run keeps the stored fix finding")
		assert.Equal(t, int64(1000), out.After.FixesMinedAt)
	})

	t.Run("reviews and local", func(t *testing.T) {
		p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
		require.NoError(t, err)

		kinds, later := evEffectKinds(p)
		assert.Equal(t, map[string]EffectKind{
			index.SourceFixes: EffectKeep, index.SourceReviews: EffectReplace,
		}, kinds)
		assert.Positive(t, later, "the review corpus can still change")
	})

	// Integration check fix: without a GitHub origin the fetch cannot run
	// either, so the plan promised a later change that no run makes.
	t.Run("reviews and local without an origin", func(t *testing.T) {
		bare := evWorkspace(t, "")
		evGitIn(t, bare.Root, "checkout", "--orphan", "fresh")

		p, err := PlanRefresh(bare, index.SourcesReviewsAndLocal)
		require.NoError(t, err)

		require.True(t, p.Ready(), "%v", p.Blockers)
		assert.Empty(t, p.Head)
		assert.False(t, p.Network)

		kinds, later := evEffectKinds(p)
		assert.Equal(t, EffectKeep, kinds[index.SourceFixes])
		assert.Zero(t, later, "a refresh that changes no evidence has no later effect")

		out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
		require.NoError(t, err)

		assert.Equal(t, index.CauseNoHistory, out.Fixes.Cause)
		assert.Equal(t, index.CauseNoRepository, out.Reviews.Cause)
		assert.Equal(t, 1, out.After.FixFindings, "the run keeps the stored fix finding")
		evAssertReviewsKept(t, out.After)
	})
}

func TestPlanRefreshWithoutOriginIsNoBlocker(t *testing.T) {
	ws := evWorkspace(t, "")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	assert.True(t, p.Ready(), "the local source still runs")
	assert.Equal(t, reviews.RepositoryNoOrigin, p.RepoState)
	assert.False(t, p.Network)

	// The fix mine still changes evidence, but no fetch mines review
	// lessons, so only the evidence-group effect is later.
	var later []string

	for _, e := range p.Affects {
		if e.Kind == EffectLater {
			later = append(later, e.Text)
		}
	}

	require.Len(t, later, 1)
	assert.Contains(t, later[0], "the next distillation sees the changed evidence groups")

	out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
	require.NoError(t, err)

	assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
	assert.Equal(t, index.SourceUnavailable, out.Reviews.State)
	assert.Equal(t, index.CauseNoRepository, out.Reviews.Cause)
	evAssertReviewsKept(t, out.After)
}

func TestPlanRefreshRejectsAnUnknownMode(t *testing.T) {
	_, err := PlanRefresh(evWorkspace(t, ""), index.SourceMode(9))
	require.ErrorContains(t, err, "unknown source mode")
}

// evStampSchema overwrites the schema stamp of the database at path in
// place. The file identity stays, so only the probe sees the change.
func evStampSchema(t *testing.T, path string, version int) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	_, err = db.Exec(`UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(version), store.MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestPlanRefreshBlocksOnAnIndexThatIsNotReady(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, ws workspace.Workspace)
		state workspace.IndexState
	}{
		{name: "absent", setup: func(t *testing.T, ws workspace.Workspace) {
			require.NoError(t, os.Remove(ws.DBPath))
		}, state: workspace.IndexAbsent},
		{name: "older", setup: func(t *testing.T, ws workspace.Workspace) {
			evStampSchema(t, ws.DBPath, store.SupportedSchema()-1)
		}, state: workspace.IndexOlder},
		{name: "newer", setup: func(t *testing.T, ws workspace.Workspace) {
			evStampSchema(t, ws.DBPath, store.SupportedSchema()+1)
		}, state: workspace.IndexNewer},
		{name: "not indexed", setup: func(t *testing.T, ws workspace.Workspace) {
			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, st.SetMeta(store.MetaIndexedAt, ""))
			require.NoError(t, st.Close())
		}, state: workspace.IndexNotIndexed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")
			c.setup(t, ws)

			before := evFileDigest(t, ws.DBPath)
			marker := evMarkerTools(t, false, "gh", "git")

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			assert.False(t, p.Ready())
			assert.Equal(t, c.state, p.Probe.State)
			assert.Equal(t, reviews.RepositoryUnresolved, p.RepoState, "a blocked plan resolves no origin")
			require.Len(t, p.Blockers, 1)
			assert.Equal(t, BlockIndex, p.Blockers[0].Kind)
			assert.Equal(t, ws.DBPath, p.Blockers[0].Path)
			assert.NotEmpty(t, p.Blockers[0].Reason)

			out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
			require.ErrorIs(t, err, ErrRefreshBlocked)
			assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
			assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)

			// After is a fresh read, never a zero value that reads as
			// known zero counts.
			assert.Equal(t, c.state, out.After.Probe.State)
			assert.False(t, out.After.ObservedAt.IsZero())
			assert.Equal(t, reviews.RepositoryUnresolved, out.After.RepoState)

			if c.state.Readable() {
				require.NoError(t, out.After.CountsErr)
				assert.Equal(t, 1, out.After.Lessons, "a readable database shows its real counts")
			} else {
				require.Error(t, out.After.CountsErr, "unknown counts are not zero")
			}

			assert.NoFileExists(t, marker, "a blocked plan and its run start no git and no gh")
			assert.Equal(t, before, evFileDigest(t, ws.DBPath), "the database is never migrated or created")
		})
	}
}

func TestPlanRefreshBlocksOnAMalformedConfig(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	// The parse error quotes the value. The reason must drop the escape
	// sequence and redact the secret-shaped text.
	require.NoError(t, os.WriteFile(evConfigPath(ws.Root),
		[]byte("reviews:\n  window_days: \"\x1b[31mTOKEN=abc123\"\n"), 0o644))

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	require.Len(t, p.Blockers, 1)
	assert.Equal(t, BlockConfig, p.Blockers[0].Kind)
	assert.Equal(t, evConfigPath(ws.Root), p.Blockers[0].Path)
	require.Error(t, p.Window.ConfigErr)
	assert.Zero(t, p.Window.Effective, "no window is known, so none is shown as effective")

	reason := p.Blockers[0].Reason
	assert.Contains(t, reason, "index config")
	assert.NotContains(t, reason, "\x1b")
	assert.NotContains(t, reason, "abc123")

	out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
	require.ErrorIs(t, err, ErrRefreshBlocked)

	// The corpus stays, so After shows it and not a zero snapshot.
	require.NoError(t, out.After.CountsErr)
	assert.Equal(t, workspace.IndexReady, out.After.Probe.State)
	assert.Equal(t, 1, out.After.Lessons)
	assert.Equal(t, 1, out.After.FixFindings)
	assert.Equal(t, int64(1000), out.After.ReviewsMinedAt)
	require.Error(t, out.After.Window.ConfigErr)
}

// evExitError returns a real *exec.ExitError with the given code, as gh
// leaves it in the error chain.
func evExitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)

	return err
}

func TestRunRefreshSourceOutcomes(t *testing.T) {
	missing := fmt.Errorf("gh api: %w (is the GitHub CLI installed?)",
		&exec.Error{Name: "gh", Err: exec.ErrNotFound})
	auth := fmt.Errorf("gh api: To get started with GitHub CLI: %w", evExitError(t, 4))

	cases := []struct {
		name    string
		fetch   reviews.FetchFunc
		state   index.SourceState
		cause   index.SourceCause
		lessons int
		kept    bool
	}{
		{name: "refreshed", fetch: evFetch(evPage, nil), state: index.SourceRefreshed, lessons: 1},
		{name: "successful empty", fetch: evFetch("[]", nil), state: index.SourceEmpty},
		{name: "gh missing", fetch: evFetch("", missing), state: index.SourceUnavailable,
			cause: index.CauseGHMissing, kept: true},
		{name: "auth", fetch: evFetch("", auth), state: index.SourceFailed, cause: index.CauseAuth, kept: true},
		{name: "fetch failed", fetch: evFetch("", errors.New("gh api: HTTP 502")),
			state: index.SourceFailed, cause: index.CauseFetchFailed, kept: true},
		{name: "unreadable", fetch: evFetch("<html>", nil), state: index.SourceFailed,
			cause: index.CauseUnreadable, kept: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			out, err := RunRefresh(context.Background(), p, c.fetch, nil)
			require.NoError(t, err)

			// The local success stays visible whatever the remote does.
			assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
			assert.Equal(t, index.CauseNone, out.Fixes.Cause)
			assert.Equal(t, 1, out.After.FixFindings, "the fixture history holds one fix")
			assert.Greater(t, out.After.FixesMinedAt, int64(1000))

			// The typed cause picks the guidance, so no caller parses Reason.
			assert.Equal(t, c.state, out.Reviews.State)
			assert.Equal(t, c.cause, out.Reviews.Cause)
			assert.NoError(t, out.Conflict)
			assert.False(t, out.FinishedAt.IsZero())

			if c.kept {
				evAssertReviewsKept(t, out.After)
				assert.NotEmpty(t, out.Reviews.Reason)

				return
			}

			// A successful fetch replaces the corpus, an empty one too.
			assert.Equal(t, c.lessons, out.After.Lessons)
			assert.Greater(t, out.After.ReviewsMinedAt, int64(1000))
		})
	}
}

func TestRunRefreshNamesALoginOrARateLimitFromGH(t *testing.T) {
	// The stderr lines are the ones that gh 2.20.2 writes. gh api exits
	// with code 1 for each HTTP error, so only the status names the cause.
	cases := []struct {
		name   string
		stderr string
		cause  index.SourceCause
	}{
		{name: "bad or expired token", stderr: "gh: Bad credentials (HTTP 401)", cause: index.CauseAuth},
		{name: "rate limit", stderr: "gh: API rate limit exceeded for user ID 1. (HTTP 403)",
			cause: index.CauseRateLimited},
		{name: "server error", stderr: "gh: HTTP 502", cause: index.CauseFetchFailed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")

			bin := t.TempDir()
			script := "#!/bin/sh\necho '" + c.stderr + "' >&2\nexit 1\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			out, err := RunRefresh(context.Background(), p, nil, nil)
			require.NoError(t, err)

			assert.Equal(t, index.SourceFailed, out.Reviews.State)
			assert.Equal(t, c.cause, out.Reviews.Cause)
			assert.Contains(t, out.Reviews.Reason, c.stderr, "the reason keeps the gh line")
			evAssertReviewsKept(t, out.After)
		})
	}
}

// evFailWrites installs a trigger that makes every write to table fail.
// It stands in for a store error at the commit boundary and keeps the
// database file, so the database check still passes.
func evFailWrites(t *testing.T, dbPath, table string) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dbPath)
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	for _, op := range []string{"INSERT", "DELETE"} {
		_, err := db.Exec("CREATE TRIGGER fail_" + strings.ToLower(op) + " BEFORE " + op + " ON " + table +
			" BEGIN SELECT RAISE(ABORT, 'disk says no'); END")
		require.NoError(t, err)
	}
}

func TestRunRefreshReportsAReviewWriteFailure(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	// The fetch succeeds, and then the store refuses the write.
	out, err := RunRefresh(context.Background(), p,
		func(context.Context, string, string, reviews.Options) ([]byte, error) {
			evFailWrites(t, ws.DBPath, "lesson")

			return []byte(evPage), nil
		}, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrConflict)
	assert.NoError(t, out.Conflict)

	assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
	assert.Equal(t, index.SourceFailed, out.Reviews.State)
	assert.Equal(t, index.CauseStore, out.Reviews.Cause, "a write failure is not a fetch failure")
	assert.Contains(t, out.Reviews.Reason, "disk says no")
	assert.Equal(t, 1, out.Comments, "the fetch counts stay visible")
	evAssertReviewsKept(t, out.After)
}

func TestRunRefreshLocalModeNeverFetches(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesLocal)
	require.NoError(t, err)

	out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
	require.NoError(t, err)

	assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
	assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)
	evAssertReviewsKept(t, out.After)
}

func TestRunRefreshUsesThePlannedWindow(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")
	require.NoError(t, os.WriteFile(evConfigPath(ws.Root), []byte("reviews:\n  window_days: 0\n"), 0o644))

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	var seen int

	_, err = RunRefresh(context.Background(), p,
		func(_ context.Context, _, _ string, opts reviews.Options) ([]byte, error) {
			seen = opts.WindowDays

			return []byte("[]"), nil
		}, nil)
	require.NoError(t, err)

	assert.Equal(t, -1, seen, "window_days: 0 runs unlimited, as the plan showed")
}

func TestRunRefreshConflictsWhenTheBoundStateChanges(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, ws workspace.Workspace)
		reason string
	}{
		{name: "origin", change: func(t *testing.T, ws workspace.Workspace) {
			evGitIn(t, ws.Root, "remote", "set-url", "origin", "git@github.com:o/other.git")
		}, reason: "the origin remote changed"},
		{name: "head", change: func(t *testing.T, ws workspace.Workspace) {
			evGitIn(t, ws.Root, "commit", "--allow-empty", "-m", "later")
		}, reason: "HEAD moved"},
		{name: "config", change: func(t *testing.T, ws workspace.Workspace) {
			require.NoError(t, os.WriteFile(evConfigPath(ws.Root), []byte("reviews:\n  window_days: 7\n"), 0o644))
		}, reason: "config.yaml changed"},
		{name: "another writer", change: func(t *testing.T, ws workspace.Workspace) {
			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, "2000"))
			require.NoError(t, st.Close())
		}, reason: "another refresh changed the stored evidence"},
		{name: "database replaced", change: func(t *testing.T, ws workspace.Workspace) {
			evReplaceFile(t, ws.DBPath)
		}, reason: "the index database file was replaced"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			c.change(t, ws)
			before := Inspect(ws)

			out, err := RunRefresh(context.Background(), p, evNoFetch(t), nil)
			require.ErrorIs(t, err, ErrConflict)
			require.ErrorIs(t, out.Conflict, ErrConflict)
			assert.Contains(t, err.Error(), c.reason)

			assert.Equal(t, index.SourceNotAttempted, out.Fixes.State)
			assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)
			assert.Equal(t, index.CauseRefused, out.Fixes.Cause)
			assert.Equal(t, index.CauseRefused, out.Reviews.Cause)
			assert.Contains(t, out.Reviews.Reason, c.reason)

			// Nothing ran: the stored evidence is what the change left.
			assert.Equal(t, before.FixesMinedAt, out.After.FixesMinedAt)
			assert.Equal(t, before.ReviewsMinedAt, out.After.ReviewsMinedAt)
			assert.Equal(t, before.Findings, out.After.Findings)
		})
	}
}

// evReplaceFile replaces path with a copy of itself, so the bytes stay
// and the file identity changes, as an atomic restore would do.
func evReplaceFile(t *testing.T, path string) {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	tmp := path + ".restore"
	require.NoError(t, os.WriteFile(tmp, data, 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

func TestRunRefreshChecksTheDatabaseAtTheReviewWrite(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	// The fetch runs between the two writes. A database swapped during
	// it must stop the review write.
	out, err := RunRefresh(context.Background(), p,
		func(context.Context, string, string, reviews.Options) ([]byte, error) {
			evReplaceFile(t, ws.DBPath)

			return []byte(evPage), nil
		}, nil)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, out.Conflict, ErrConflict)

	assert.Equal(t, index.SourceRefreshed, out.Fixes.State, "the local write happened before the swap")
	assert.Equal(t, index.SourceFailed, out.Reviews.State)
	assert.Equal(t, index.CauseRefused, out.Reviews.Cause)
	assert.Contains(t, out.Reviews.Reason, "replaced")
	evAssertReviewsKept(t, out.After)
}

// evRemoveDatabase deletes the database at path with its SQLite side
// files, as a user who deletes .seamark/index.db does.
func evRemoveDatabase(t *testing.T, path string) {
	t.Helper()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		err := os.Remove(path + suffix)
		if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
	}
}

// TestRunRefreshNeverCreatesOrMigratesAChangedDatabase changes the
// database while a source works. The check before the store open must
// refuse the source, and the database must stay as the change left it:
// not created again and not migrated.
func TestRunRefreshNeverCreatesOrMigratesAChangedDatabase(t *testing.T) {
	older := store.SupportedSchema() - 1

	changes := []struct {
		name   string
		change func(t *testing.T, ws workspace.Workspace)
		reason string
		check  func(t *testing.T, ws workspace.Workspace)
	}{
		{name: "removed", change: func(t *testing.T, ws workspace.Workspace) {
			evRemoveDatabase(t, ws.DBPath)
		}, reason: "the index database file was removed", check: func(t *testing.T, ws workspace.Workspace) {
			assert.NoFileExists(t, ws.DBPath, "the refresh must not create the database again")
		}},
		{name: "downgraded", change: func(t *testing.T, ws workspace.Workspace) {
			evStampSchema(t, ws.DBPath, older)
		}, reason: "the index database is now older", check: func(t *testing.T, ws workspace.Workspace) {
			version, err := store.ProbeVersion(ws.DBPath)
			require.NoError(t, err)
			assert.Equal(t, older, version, "the refresh must not migrate the database")
		}},
	}

	for _, c := range changes {
		// The fix mine cannot be entered, so the change runs at its start
		// event. The source then opens the store after the change.
		t.Run(c.name+" during the fix mine", func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			out, err := RunRefresh(context.Background(), p, evNoFetch(t), func(e index.RefreshEvent) {
				if e.Phase == index.PhaseMineFixes {
					c.change(t, ws)
				}
			})
			require.ErrorIs(t, err, ErrConflict)
			require.ErrorIs(t, out.Conflict, ErrConflict)

			assert.Equal(t, index.SourceFailed, out.Fixes.State)
			assert.Equal(t, index.CauseRefused, out.Fixes.Cause)
			assert.Contains(t, out.Fixes.Reason, c.reason)
			assert.Equal(t, index.SourceNotAttempted, out.Reviews.State)
			c.check(t, ws)
		})

		t.Run(c.name+" during the fetch", func(t *testing.T) {
			ws := evWorkspace(t, "git@github.com:o/r.git")

			p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
			require.NoError(t, err)

			out, err := RunRefresh(context.Background(), p,
				func(context.Context, string, string, reviews.Options) ([]byte, error) {
					c.change(t, ws)

					return []byte(evPage), nil
				}, nil)
			require.ErrorIs(t, err, ErrConflict)
			require.ErrorIs(t, out.Conflict, ErrConflict)

			assert.Equal(t, index.SourceRefreshed, out.Fixes.State, "the local write happened before the change")
			assert.Equal(t, index.SourceFailed, out.Reviews.State)
			assert.Equal(t, index.CauseRefused, out.Reviews.Cause)
			assert.Contains(t, out.Reviews.Reason, c.reason)
			c.check(t, ws)
		})
	}
}

func TestRunRefreshStopDuringTheFetch(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	// The fake blocks like a slow gh until the stop arrives.
	fetch := func(ctx context.Context, _, _ string, _ reviews.Options) ([]byte, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	}

	go func() {
		<-started
		cancel()
	}()

	begin := time.Now()
	out, err := RunRefresh(ctx, p, fetch, nil)
	require.NoError(t, err)

	assert.Less(t, time.Since(begin), 10*time.Second, "the stop returns without waiting for the fetch")
	assert.Equal(t, index.SourceRefreshed, out.Fixes.State)
	assert.Equal(t, index.SourceStopped, out.Reviews.State)
	assert.Equal(t, index.CauseStopped, out.Reviews.Cause)
	evAssertReviewsKept(t, out.After)
}

func TestRunRefreshDiscardsDataThatArrivesAfterAStop(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	out, err := RunRefresh(ctx, p, func(context.Context, string, string, reviews.Options) ([]byte, error) {
		cancel()

		return []byte(evPage), nil
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, index.SourceStopped, out.Reviews.State)
	evAssertReviewsKept(t, out.After)
}

func TestRunRefreshStoppedBeforeTheStartDoesNothing(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := RunRefresh(ctx, p, evNoFetch(t), nil)
	require.NoError(t, err)

	assert.Equal(t, index.SourceStopped, out.Fixes.State)
	assert.Equal(t, index.SourceStopped, out.Reviews.State)
	assert.Equal(t, index.CauseStopped, out.Fixes.Cause)
	assert.Equal(t, index.CauseStopped, out.Reviews.Cause)
	assert.Equal(t, int64(1000), out.After.FixesMinedAt, "no fix mine ran")
	evAssertReviewsKept(t, out.After)
}

func TestRunRefreshReportsTypedProgress(t *testing.T) {
	ws := evWorkspace(t, "git@github.com:o/r.git")

	p, err := PlanRefresh(ws, index.SourcesReviewsAndLocal)
	require.NoError(t, err)

	var done []string

	out, err := RunRefresh(context.Background(), p, evFetch(evPage, nil), func(e index.RefreshEvent) {
		if e.Phase == index.PhaseSourceDone {
			done = append(done, e.Source+"="+string(e.State))
		}
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"fixes=refreshed", "reviews=refreshed"}, done)
	assert.Equal(t, 1, out.Comments)
	assert.Equal(t, 1, out.Kept)
}

func TestRunRefreshRejectsANilPlan(t *testing.T) {
	out, err := RunRefresh(context.Background(), nil, nil, nil)
	require.ErrorContains(t, err, "no plan")

	require.ErrorContains(t, out.After.CountsErr, "no plan", "no workspace was read, so the counts are unknown")
	assert.False(t, out.FinishedAt.IsZero())
}

func TestEvSafeMakesOneRedactedLine(t *testing.T) {
	got := evSafe("gh api: \x1b]0;x\x07bad\nAuthorization: Bearer abc.def\tnext")

	assert.False(t, strings.ContainsAny(got, "\x1b\x07\n\t"), got)
	assert.NotContains(t, got, "abc.def")
	assert.Contains(t, got, "[REDACTED]")
}
