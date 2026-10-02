package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// These tests pin the exact stdout of `seamark index --fixes-only` and
// `seamark index --reviews`. They are characterization tests: the
// evidence engine moves under these commands, and the CLI text must not
// change. A fake gh script stands in for GitHub, so the tests run offline.

// sourcesGit runs git in root with a hermetic identity and config.
// Global config must not leak in, because it can rewrite remotes.
func sourcesGit(t *testing.T, root string, args ...string) {
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

// sourcesRepo builds an indexed repository with one fix commit. With
// origin set, the origin remote names the GitHub repository o/r. The
// first index run happens here, so each test run takes the fast path
// and prints a stable first line.
func sourcesRepo(t *testing.T, origin bool) (root, dbPath string) {
	t.Helper()

	root = writeFixture(t)
	gitify(t, root)

	path := filepath.Join(root, "a.go")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("\nfunc resetHelperState() {}\n")...), 0o644))

	sourcesGit(t, root, "add", "-A")
	sourcesGit(t, root, "commit", "-m", "fix: reset helper state before reuse (#42)")

	if origin {
		sourcesGit(t, root, "remote", "add", "origin", "git@github.com:o/r.git")
	}

	_, err = run(t, "-C", root, "index")
	require.NoError(t, err)

	resolved, err := index.ResolveRoot(root)
	require.NoError(t, err)

	return root, store.DefaultPath(resolved)
}

// seedReviews stores one review lesson and pins both mine stamps to a
// known value. A test then proves that a skipped source keeps them.
func seedReviews(t *testing.T, dbPath string) {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	require.NoError(t, st.ReplaceLessons([]model.Lesson{{
		ClusterKey: "a.go\x00seeded", Region: "a.go", Reviewer: "person",
		Symptom: "seeded", Occurrences: 1,
	}}, []model.Finding{{
		ID: 900, LessonKey: "a.go\x00seeded", Path: "a.go",
		Body: "seeded review", Source: model.SourceReview,
	}}))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, "1000"))
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, "1000"))
}

// storedEvidence is the part of the store that a source refresh may
// change: the lesson count, the finding count per source, and the stamps.
type storedEvidence struct {
	lessons        int
	findings       map[string]int
	reviewsMinedAt string
	fixesMinedAt   string
}

// readEvidence reads storedEvidence from the database at dbPath.
func readEvidence(t *testing.T, dbPath string) storedEvidence {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	stats, err := st.Stats()
	require.NoError(t, err)

	counts, err := st.FindingCounts()
	require.NoError(t, err)

	reviewsAt, err := st.GetMeta(store.MetaReviewsMinedAt)
	require.NoError(t, err)

	fixesAt, err := st.GetMeta(store.MetaFixesMinedAt)
	require.NoError(t, err)

	return storedEvidence{
		lessons: stats.Lessons, findings: counts,
		reviewsMinedAt: reviewsAt, fixesMinedAt: fixesAt,
	}
}

// fakeGH puts a gh script first on PATH and keeps the rest of PATH, so
// git still resolves. The script records its arguments in the returned
// file. The file is outside the repository, because an untracked file
// would change the index fingerprint.
func fakeGH(t *testing.T, body string) (argvFile string) {
	t.Helper()

	bin := t.TempDir()
	argvFile = filepath.Join(bin, "gh-argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\n" + body + "\n"

	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return argvFile
}

// pathWithoutGH replaces PATH with one directory that holds only git.
// The fix mine still runs, and gh is certainly missing.
func pathWithoutGH(t *testing.T) {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	bin := t.TempDir()
	require.NoError(t, os.Symlink(gitPath, filepath.Join(bin, "git")))
	t.Setenv("PATH", bin)
}

// upToDate is the first stdout line of an index run on an unchanged
// workspace.
func upToDate(dbPath string) string {
	return "index already up to date (" + dbPath + "); --force rebuilds\n"
}

// ghComments is one gh page with an inline comment and a PR-level
// comment. The inline comment clusters into one lesson.
const ghComments = `[{"id":11,"user":{"login":"rev","type":"User"},` +
	`"body":"Reset pooled state before reuse here.","path":"a.go","line":3,` +
	`"html_url":"https://github.com/o/r/pull/1#discussion_r11","created_at":"2026-07-21T10:00:00Z",` +
	`"pull_request_url":"https://api.github.com/repos/o/r/pulls/1"},` +
	`{"id":12,"user":{"login":"rev","type":"User"},"body":"Overall fine.","path":"",` +
	`"created_at":"2026-07-22T10:00:00Z","pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

// ghNoLessons holds only a PR-level comment. The fetch succeeds, but
// clustering keeps nothing, so the CLI prints its "none ()" line.
const ghNoLessons = `[{"id":12,"user":{"login":"rev","type":"User"},"body":"Overall fine.","path":"",` +
	`"created_at":"2026-07-22T10:00:00Z","pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

func TestIndexFixesOnlyPrintsTheFixLine(t *testing.T) {
	root, dbPath := sourcesRepo(t, false)
	seedReviews(t, dbPath)

	stdout, stderr, err := runErr(t, "-C", root, "index", "--fixes-only")
	require.NoError(t, err)

	assert.Equal(t, upToDate(dbPath)+
		"  fixes    1 findings mined from fix commits\n", stdout)
	assert.Equal(t, "fixes: 2 commits scanned, 1 findings kept\n", stderr,
		"progress lines stay on stderr")

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.lessons, "a local refresh keeps the review lessons")
	assert.Equal(t, 1, got.findings[model.SourceReview])
	assert.Equal(t, 1, got.findings[model.SourceFixConventional])
	assert.Equal(t, "1000", got.reviewsMinedAt)
	assert.NotEqual(t, "1000", got.fixesMinedAt, "a successful mine stamps the fix source")
}

func TestIndexFixesOnlyOutsideGitKeepsStoredFixes(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	dbPath := store.DefaultPath(root)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceFixFindings([]model.Finding{{
		ID: 5, Path: "a.go", Body: "fix: old", Source: model.SourceFixConventional,
	}}))
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, "1000"))
	require.NoError(t, st.Close())

	stdout, err := run(t, "-C", root, "index", "--fixes-only")
	require.NoError(t, err)

	// Outside git the fix mine does not run. The CLI still prints 0, the
	// same text as an authoritative empty mine (an existing quirk).
	assert.Contains(t, stdout, "  fixes    0 findings mined from fix commits\n")

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.findings[model.SourceFixConventional], "an unmined source keeps its rows")
	assert.Equal(t, "1000", got.fixesMinedAt)
}

func TestIndexReviewsWithoutOrigin(t *testing.T) {
	root, dbPath := sourcesRepo(t, false)
	seedReviews(t, dbPath)

	stdout, stderr, err := runErr(t, "-C", root, "index", "--reviews")
	require.NoError(t, err)

	assert.Equal(t, upToDate(dbPath)+
		"  reviews  skipped: not a GitHub repository (kept existing)\n"+
		"  fixes    1 findings mined from fix commits\n", stdout)
	assert.Equal(t, "fixes: 2 commits scanned, 1 findings kept\n"+
		"note: not a GitHub repository\n", stderr)

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.lessons)
	assert.Equal(t, "1000", got.reviewsMinedAt)
	assert.NotEqual(t, "1000", got.fixesMinedAt, "the local source refreshes on its own")
}

func TestIndexReviewsWithGHSuccess(t *testing.T) {
	root, dbPath := sourcesRepo(t, true)
	seedReviews(t, dbPath)

	argvFile := fakeGH(t, "cat <<'EOF'\n"+ghComments+"\nEOF")

	stdout, stderr, err := runErr(t, "-C", root, "index", "--reviews")
	require.NoError(t, err)

	assert.Equal(t, upToDate(dbPath)+
		"  reviews  1 lessons mined from 1 findings\n"+
		"  fixes    1 findings mined from fix commits\n", stdout)
	assert.Equal(t, "fixes: 2 commits scanned, 1 findings kept\n"+
		"reviews: fetching PR comments for o/r…\n"+
		"reviews: 2 comments fetched; clustering…\n", stderr)

	argv, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	assert.Equal(t, "api\n--paginate\nrepos/o/r/pulls/comments?per_page=100\n", string(argv))

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.lessons, "the fetched lesson replaces the seeded one")
	assert.Equal(t, 1, got.findings[model.SourceReview])
	assert.NotEqual(t, "1000", got.reviewsMinedAt)
}

func TestIndexReviewsWithGHEmpty(t *testing.T) {
	root, dbPath := sourcesRepo(t, true)
	seedReviews(t, dbPath)

	fakeGH(t, "echo '[]'")

	stdout, stderr, err := runErr(t, "-C", root, "index", "--reviews")
	require.NoError(t, err)

	assert.Equal(t, upToDate(dbPath)+
		"  reviews  none (no review comments found)\n"+
		"  fixes    1 findings mined from fix commits\n", stdout)
	assert.Equal(t, "fixes: 2 commits scanned, 1 findings kept\n"+
		"reviews: fetching PR comments for o/r…\n"+
		"reviews: 0 comments fetched; clustering…\n"+
		"note: no review comments found\n", stderr)

	// An authoritative empty fetch replaces the corpus and stamps it.
	got := readEvidence(t, dbPath)
	assert.Zero(t, got.lessons)
	assert.Zero(t, got.findings[model.SourceReview])
	assert.NotEqual(t, "1000", got.reviewsMinedAt)
}

func TestIndexReviewsWithZeroLessons(t *testing.T) {
	root, dbPath := sourcesRepo(t, true)
	seedReviews(t, dbPath)

	fakeGH(t, "cat <<'EOF'\n"+ghNoLessons+"\nEOF")

	stdout, err := run(t, "-C", root, "index", "--reviews")
	require.NoError(t, err)

	// The empty parentheses are an existing quirk: the note is empty.
	assert.Equal(t, upToDate(dbPath)+
		"  reviews  none ()\n"+
		"  fixes    1 findings mined from fix commits\n", stdout)

	got := readEvidence(t, dbPath)
	assert.Zero(t, got.lessons)
	assert.NotEqual(t, "1000", got.reviewsMinedAt)
}

func TestIndexReviewsWithGHFailure(t *testing.T) {
	// note is the Note text. The CLI prints it in the skip line on
	// stdout and in a "note:" line on stderr.
	cases := []struct {
		name, body, note string
	}{
		{
			name: "exit 1 with stderr",
			body: "echo 'HTTP 401: Bad credentials' >&2\necho 'try again' >&2\nexit 1",
			note: "review mining skipped: gh api: HTTP 401: Bad credentials",
		},
		{
			// gh 2.20.2 writes this line for a bad or expired token. The
			// typed status in the error chain adds no output.
			name: "HTTP 401 for a bad token",
			body: "echo 'gh: Bad credentials (HTTP 401)' >&2\nexit 1",
			note: "review mining skipped: gh api: gh: Bad credentials (HTTP 401)",
		},
		{
			name: "HTTP 403 for a rate limit",
			body: "echo 'gh: API rate limit exceeded for user ID 1. (HTTP 403)' >&2\nexit 1",
			note: "review mining skipped: gh api: gh: API rate limit exceeded for user ID 1. (HTTP 403)",
		},
		{
			name: "exit 4 for authentication",
			body: "echo 'To get started with GitHub CLI, please run:  gh auth login' >&2\nexit 4",
			note: "review mining skipped: gh api: To get started with GitHub CLI, please run:  gh auth login",
		},
		{
			name: "exit without stderr",
			body: "exit 2",
			note: "review mining skipped: gh api: exit status 2 (is the GitHub CLI installed?)",
		},
		{
			name: "invalid JSON",
			body: "echo 'not json'",
			note: "review comments unreadable: invalid character 'o' in literal null (expecting 'u')",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, dbPath := sourcesRepo(t, true)
			seedReviews(t, dbPath)

			fakeGH(t, c.body)

			stdout, stderr, err := runErr(t, "-C", root, "index", "--reviews")
			require.NoError(t, err)

			assert.Equal(t, upToDate(dbPath)+
				"  reviews  skipped: "+c.note+" (kept existing)\n"+
				"  fixes    1 findings mined from fix commits\n", stdout)
			assert.Equal(t, "fixes: 2 commits scanned, 1 findings kept\n"+
				"reviews: fetching PR comments for o/r…\n"+
				"note: "+c.note+"\n", stderr)

			// A failed fetch keeps the stored reviews, and the fixes that
			// were mined before the fetch stay persisted.
			got := readEvidence(t, dbPath)
			assert.Equal(t, 1, got.lessons)
			assert.Equal(t, 1, got.findings[model.SourceReview])
			assert.Equal(t, "1000", got.reviewsMinedAt)
			assert.Equal(t, 1, got.findings[model.SourceFixConventional])
			assert.NotEqual(t, "1000", got.fixesMinedAt)
		})
	}
}

func TestIndexReviewsWithoutGH(t *testing.T) {
	root, dbPath := sourcesRepo(t, true)
	seedReviews(t, dbPath)

	pathWithoutGH(t)

	stdout, err := run(t, "-C", root, "index", "--reviews")
	require.NoError(t, err)

	assert.Equal(t, upToDate(dbPath)+
		`  reviews  skipped: review mining skipped: gh api: exec: "gh": executable file not found in $PATH `+
		"(is the GitHub CLI installed?) (kept existing)\n"+
		"  fixes    1 findings mined from fix commits\n", stdout)

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.lessons)
	assert.Equal(t, "1000", got.reviewsMinedAt)
}

func TestIndexReviewsMalformedConfigFailsBeforeMining(t *testing.T) {
	root, dbPath := sourcesRepo(t, true)
	seedReviews(t, dbPath)

	argvFile := fakeGH(t, "echo '[]'")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("reviews: [unclosed\n"), 0o644))

	stdout, err := run(t, "-C", root, "index", "--reviews")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "index config: "), err.Error())
	assert.Empty(t, stdout)
	assert.NoFileExists(t, argvFile, "the index build fails first, so gh never runs")

	got := readEvidence(t, dbPath)
	assert.Equal(t, 1, got.lessons)
	assert.Equal(t, "1000", got.reviewsMinedAt)
}
