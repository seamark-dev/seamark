package fixes

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestClassify(t *testing.T) {
	cases := []struct{ subject, body, want string }{
		{"fix: nil check in loader", "", model.SourceFixConventional},
		{"fix(engine): reset state", "", model.SourceFixConventional},
		{"Fix race in worker", "", model.SourceFixSubject},
		{"harden worker", "This fixes #12 for real", model.SourceFixIssueLink},
		{`Revert "add cache"`, "", model.SourceRevert},
		// Chores are real fixes with no transferable lesson.
		{"fix: typo in README", "", ""},
		{"fix lint errors", "", ""},
		{"fix: import ordering for CI", "", ""},
		// Not fixes at all — substring matches must not classify.
		{"add prefix support", "", ""},
		{"update fixtures", "", ""},
		{"Fixed point arithmetic", "", ""},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, Classify(c.subject, c.body), "%q / %q", c.subject, c.body)
	}
}

// repo builds a scratch git repository and returns a commit helper plus
// the raw git runner, for tests that need branches or merges.
func repo(t *testing.T) (root string, commit func(msg string, files map[string]string),
	git func(args ...string),
) {
	t.Helper()

	root = t.TempDir()

	git = func(args ...string) {
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

	git("init", "-b", "main")

	commit = func(msg string, files map[string]string) {
		t.Helper()

		for rel, content := range files {
			p := filepath.Join(root, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		}

		git("add", "-A")
		git("commit", "--allow-empty", "-m", msg)
	}

	return root, commit, git
}

func TestMineEndToEnd(t *testing.T) {
	root, commit, _ := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})
	commit("fix: nil check before dereference (#42)", map[string]string{
		"a.go": "package a\n\nfunc Run() {\n\tif x := load(); x != nil {\n\t\tx.use()\n\t}\n}\n"})
	commit("add feature", map[string]string{"b.go": "package a\n\nfunc Feat() {}\n"})
	commit("fix: typo in comment", map[string]string{"b.go": "package a\n\n// Feat does things.\nfunc Feat() {}\n"})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.True(t, res.Mined)
	findings := res.Findings
	require.Len(t, findings, 1, "one real fix; the chore and the feature don't classify")

	f := findings[0]
	assert.Equal(t, model.SourceFixConventional, f.Source)
	assert.Equal(t, "a.go", f.Path)
	assert.Equal(t, 42, f.PR, "the squash-merge PR reference is the cross-provider key")
	assert.Positive(t, f.ID)
	assert.Contains(t, f.Body, "nil check before dereference")
	assert.Contains(t, f.Body, "x != nil", "the patch is the signal")
	assert.NotContains(t, f.Body, "diff --git", "patch noise lines are stripped")
	assert.NotZero(t, f.CreatedAt)
	assert.Empty(t, f.LessonKey, "fix findings feed distillation, not the lessons table")

	// Stable id: a second mine yields the identical finding id.
	again, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, again.Findings, 1)
	assert.Equal(t, f.ID, again.Findings[0].ID)
}

func TestRevertedFixesAreExcludedAndRevertsIncluded(t *testing.T) {
	root, commit, _ := repo(t)

	commit("initial", map[string]string{"a.go": "package a\nvar x = 1\n"})
	commit("fix: wrong constant", map[string]string{"a.go": "package a\nvar x = 2\n"})

	// Revert the fix the way git does it, sha in the body.
	sha := strings.TrimSpace(gitStdout(t, root, "rev-parse", "HEAD"))
	commit(`Revert "fix: wrong constant"`+"\n\nThis reverts commit "+sha+".",
		map[string]string{"a.go": "package a\nvar x = 1\n"})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.True(t, res.Mined)
	findings := res.Findings
	require.Len(t, findings, 1, "the reverted fix is out; the revert itself is in")
	assert.Equal(t, model.SourceRevert, findings[0].Source)
}

func TestBulkFixesAreExcluded(t *testing.T) {
	root, commit, _ := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n"})

	files := map[string]string{}
	for i := 0; i < 31; i++ {
		files[fmt.Sprintf("f%02d.go", i)] = "package a\n// fixed\n"
	}

	commit("fix: mass correction", files)

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.True(t, res.Mined)
	findings := res.Findings
	assert.Empty(t, findings, "a 31-file commit is a refactor, not a discrete mistake")
}

func TestDuplicatePatchesCountOnce(t *testing.T) {
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\nvar x = 1\n"})

	// The same patch under two shas — the cherry-pick/backport shape —
	// via two branches making the identical edit, merged.
	git("checkout", "-b", "side")
	commit("fix: guard divide by zero", map[string]string{"a.go": "package a\nvar x = 1\nvar guard = true\n"})
	original := strings.TrimSpace(gitStdout(t, root, "rev-parse", "HEAD"))
	git("checkout", "main")
	commit("fix: guard divide by zero", map[string]string{"a.go": "package a\nvar x = 1\nvar guard = true\n"})
	git("merge", "--no-ff", "-m", "merge side", "side")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.True(t, res.Mined)
	require.Len(t, res.Findings, 1, "identical patches are one event, one finding")

	// The ORIGINAL survives, not whichever copy landed last: the finding
	// id is sha-derived, and a later backport must not change it — that
	// id is what the distillation signature (and its bill) rests on.
	assert.Equal(t, shaID(original), res.Findings[0].ID,
		"dedup keeps the oldest commit so ids stay stable as backports arrive")
}

func TestPathsWithSpacesAreMined(t *testing.T) {
	root, commit, _ := repo(t)

	commit("initial", map[string]string{"pkg/my file.go": "package a\n"})
	commit("fix: guard the spaced path", map[string]string{
		"pkg/my file.go": "package a\n\nfunc F() { if ok { return } }\n"})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)

	// git's --numstat is tab-delimited; splitting on whitespace would
	// drop this file from the churn ranking and the bulk-commit count.
	assert.Equal(t, "pkg/my file.go", res.Findings[0].Path)
}

func TestMineOutsideGitIsEmpty(t *testing.T) {
	res, err := Mine(t.TempDir(), Options{})
	require.NoError(t, err)
	assert.False(t, res.Mined, "no git: unmined, so stored findings are preserved")
	assert.Empty(t, res.Findings)
}

func gitStdout(t *testing.T, root string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root

	out, err := cmd.Output()
	require.NoError(t, err)

	return string(out)
}

func TestFixFindingSecretsAreRedacted(t *testing.T) {
	// The patch of a credential-removal fix carries the credential being
	// removed — in the "-" lines. Stored findings are broadcast into
	// distillation prompts and agent context, so they are scrubbed.
	root, commit, _ := repo(t)

	commit("initial", map[string]string{
		"conf.py": "DB = \"postgresql://svc:hunter2secret@db:5432/prod\"\nAPI_TOKEN=\"abc123xyz\"\n"})
	commit("fix: read database credentials from the environment",
		map[string]string{"conf.py": "import os\n\nDB = os.environ[\"DB_URL\"]\n"})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)

	body := res.Findings[0].Body
	assert.NotContains(t, body, "hunter2secret", "removed credentials must not survive in the patch excerpt")
	assert.NotContains(t, body, "abc123xyz")
	assert.Contains(t, body, "[REDACTED]")
}

func TestSemanticHomeBeatsChurnierTest(t *testing.T) {
	// The test routinely out-churns the fix it covers; churn alone
	// elected tests/ as the finding's path and dragged proposal regions
	// to `*`. The semantic home leads; the test stays in the footprint.
	root, commit, _ := repo(t)

	commit("initial", map[string]string{
		"workers/tracker.py":    "def track():\n    pass\n",
		"tests/test_tracker.py": "def test_track():\n    pass\n",
		"docs/tracker.md":       "# tracker\n",
	})
	commit("fix: normalize by total sampled minutes", map[string]string{
		"workers/tracker.py":    "def track():\n    return normalize()\n",
		"tests/test_tracker.py": "def test_track():\n    a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\n",
		"docs/tracker.md":       "# tracker\n\nlots\nof\nnew\nprose\nlines\nhere\ntoo\nnow\nok\nyes\nmore\n",
	})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)

	f := res.Findings[0]
	assert.Equal(t, "workers/tracker.py", f.Path,
		"the code file leads even when test and docs out-churn it")
	require.NotEmpty(t, f.Paths)
	assert.Equal(t, f.Path, f.Paths[0], "the footprint leads with the semantic home")
	assert.Contains(t, f.Paths, "tests/test_tracker.py", "tests stay in the footprint as evidence")
	assert.Contains(t, f.Paths, "docs/tracker.md")
}

func TestTestOnlyFixKeepsTestAsHome(t *testing.T) {
	root, commit, _ := repo(t)

	commit("initial", map[string]string{"tests/test_a.py": "def test_a():\n    pass\n"})
	commit("fix: deflake the retry assertion", map[string]string{
		"tests/test_a.py": "def test_a():\n    retry()\n"})

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, "tests/test_a.py", res.Findings[0].Path,
		"a test-only fix is legitimately about the tests")
}

func TestMergeTopologyRecoversPRs(t *testing.T) {
	// Merge-commit workflow: branch commits carry no (#N) suffix and no
	// issue link — measured on a real repo, every fix finding had pr=0
	// and review↔fix event dedup never fired. The merge subject names
	// the PR; rev-list names the commits it brought in.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/nil-check")
	commit("fix: nil check before dereference", map[string]string{
		"a.go": "package a\n\nfunc Run() {\n\tif x := load(); x != nil {\n\t\tx.use()\n\t}\n}\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/nil-check", "-m", "Merge pull request #29 from yuri/fix/nil-check")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, 29, res.Findings[0].PR,
		"the merged branch commit inherits its pull request")
}

func TestExplicitPRReferenceBeatsTopology(t *testing.T) {
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/late-cherry-pick")
	commit("fix: guard empty input (#42)", map[string]string{
		"a.go": "package a\n\nfunc Run() { guard() }\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/late-cherry-pick", "-m", "Merge pull request #50 from yuri/fix/late-cherry-pick")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, 42, res.Findings[0].PR, "an explicit reference always wins over inference")
}

func TestBranchNameDeclaresTheFix(t *testing.T) {
	// A pull request whose only fix declaration is its branch name:
	// the commits inside say nothing fix-shaped, so without this tier
	// the PR contributes zero findings.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"core/session.py": "def next_session():\n    return d + 1\n"})

	git("checkout", "-b", "fix/holiday-session-ux")
	commit("handle exchange holidays in session arithmetic", map[string]string{
		"core/session.py": "def next_session():\n    return calendar.next_rth(d)\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/holiday-session-ux",
		"-m", "Merge pull request #29 from yuri/fix/holiday-session-ux")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)

	f := res.Findings[0]
	assert.Equal(t, model.SourceFixBranch, f.Source)
	assert.Equal(t, 29, f.PR)
	assert.Equal(t, "core/session.py", f.Path)
	assert.Contains(t, f.Body, "calendar.next_rth", "the merge diff is the patch")
}

func TestBranchFixStaysOutWhenACommitClassified(t *testing.T) {
	// Any classified commit inside means the fix is already mined —
	// the branch tier must not double it.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/nil-check")
	commit("fix: nil check before dereference", map[string]string{
		"a.go": "package a\n\nfunc Run() {\n\tif x := load(); x != nil {\n\t\tx.use()\n\t}\n}\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/nil-check", "-m", "Merge pull request #30 from yuri/fix/nil-check")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, model.SourceFixConventional, res.Findings[0].Source)
}

func TestLocalMergeBranchFormCounts(t *testing.T) {
	// `git merge fix/x` without a PR: the branch name is the same
	// signal; there is just no pull request to attribute.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/local-only")
	commit("adjust retry ceiling", map[string]string{
		"a.go": "package a\n\nfunc Run() { retry(3) }\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/local-only", "-m", "Merge branch 'fix/local-only'")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, model.SourceFixBranch, res.Findings[0].Source)
	assert.Zero(t, res.Findings[0].PR)
}

func TestFeatureBranchesTeachNothing(t *testing.T) {
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "feat/new-endpoint")
	commit("add the endpoint", map[string]string{"b.go": "package a\n\nfunc Serve() {}\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "feat/new-endpoint", "-m", "Merge pull request #31 from yuri/feat/new-endpoint")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	assert.Empty(t, res.Findings, "a feature branch declares no fix")
}

func TestChoreBranchesTeachNothing(t *testing.T) {
	// fix/lint is a real fix branch and a chore: the commit tier
	// excludes "fix: lint" subjects deliberately, and the branch tier
	// must not smuggle the same chore back in through the branch name.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/lint")
	commit("appease the linter", map[string]string{"a.go": "package a\n\nfunc Run() {}\n\n// ok\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/lint", "-m", "Merge pull request #40 from yuri/fix/lint")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	assert.Empty(t, res.Findings, "a chore-shaped branch name stays out of the corpus")
}

func TestBranchFixWithRevertedMemberStaysOut(t *testing.T) {
	// A member commit reverted after the merge means the fix was at
	// least partly undone — a fix bad enough to undo teaches the wrong
	// lesson, through every tier.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	git("checkout", "-b", "fix/retry-ceiling")
	commit("adjust retry ceiling", map[string]string{"a.go": "package a\n\nfunc Run() { retry(3) }\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/retry-ceiling", "-m", "Merge pull request #41 from yuri/fix/retry-ceiling")
	git("revert", "--no-edit", "HEAD^2")

	res, err := Mine(root, Options{})
	require.NoError(t, err)

	for _, f := range res.Findings {
		assert.NotEqual(t, model.SourceFixBranch, f.Source,
			"a branch fix with a reverted member must not survive")
	}
}

// commitDated is commit with an explicit author+committer date — for
// branches whose commits predate the mining window.
func commitDated(t *testing.T, root, msg, when string, files map[string]string) {
	t.Helper()

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	for _, args := range [][]string{{"add", "-A"}, {"commit", "--allow-empty", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when,
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
}

func TestTrimPatchPreservesCompactFileIdentity(t *testing.T) {
	patch := "diff --git a/api/handler.go b/api/handler.go\n" +
		"index 111..222 100644\n--- a/api/handler.go\n+++ b/api/handler.go\n" +
		"@@ -1 +1 @@ func handle()\n-old\n+new\n"

	got := trimPatch(patch, 1_000)
	assert.Contains(t, got, "file: api/handler.go")
	assert.NotContains(t, got, "diff --git")
	assert.NotContains(t, got, "index 111")
	assert.Contains(t, got, "@@ -1 +1 @@ func handle()")
}

func TestTrimPatchParsesGitPathHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
		path   string
	}{
		{
			name:   "spaces",
			header: "diff --git a/api/my handler.go b/api/my handler.go",
			path:   "api/my handler.go",
		},
		{
			name:   "C-quoted",
			header: `diff --git "a/api/tab\tname.go" "b/api/tab\tname.go"`,
			path:   "api/tab\tname.go",
		},
		{
			name:   "C-quoted octal UTF-8",
			header: `diff --git "a/api/\303\251.go" "b/api/\303\251.go"`,
			path:   "api/é.go",
		},
		{
			name:   "mixed rename",
			header: `diff --git a/api/old.go "b/api/tab\tname.go"`,
			path:   "api/tab\tname.go",
		},
		{
			name:   "leading and trailing spaces",
			header: "diff --git a/ name.go  b/ name.go ",
			path:   " name.go ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch := tt.header + "\n@@ -1 +1 @@ func handle()\n-old\n+new\n"
			got := trimPatch(patch, 1_000)

			assert.Contains(t, got, "file: "+tt.path+"\n")
			assert.NotContains(t, got, "diff --git")
		})
	}
}

func TestTrimPatchKeepsAmbiguousGitHeader(t *testing.T) {
	header := "diff --git a/api/old b/name.go b/api/new.go"
	got := trimPatch(header+"\n@@ -1 +1 @@ func handle()\n-old\n+new\n", 1_000)

	assert.Contains(t, got, header,
		"an ambiguous pathname must not become an incorrect file marker")
	assert.NotContains(t, got, "file:")
}

func TestOutOfWindowChoreMemberStillExcludes(t *testing.T) {
	// A branch can sit unmerged past the mining window: --since bounds
	// the commit log, not the merge's rev-list walk. The chore commit
	// inside must still be seen and still exclude the merge — an absent
	// bySha entry must never read as "no signal".
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	old := time.Now().Add(-2 * DefaultWindow).Format(time.RFC3339)

	git("checkout", "-b", "fix/stale-branch")
	commitDated(t, root, "fix lint errors", old,
		map[string]string{"a.go": "package a\n\nfunc Run() {}\n\n// ok\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/stale-branch",
		"-m", "Merge pull request #50 from yuri/fix/stale-branch")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	assert.Empty(t, res.Findings, "a chore member outside the window must still exclude the merge")
}

func TestOutOfWindowNeutralMemberKeepsBranchFix(t *testing.T) {
	// The flip side: an old member that carries no fix, chore, or revert
	// signal must not cost the merge its finding — resolution, not
	// blanket exclusion, is the contract.
	root, commit, git := repo(t)

	commit("initial", map[string]string{"a.go": "package a\n\nfunc Run() {}\n"})

	old := time.Now().Add(-2 * DefaultWindow).Format(time.RFC3339)

	git("checkout", "-b", "fix/slow-burner")
	commitDated(t, root, "handle the session-boundary edge case", old,
		map[string]string{"a.go": "package a\n\nfunc Run() { guard() }\n"})
	git("checkout", "main")
	git("merge", "--no-ff", "fix/slow-burner",
		"-m", "Merge pull request #51 from yuri/fix/slow-burner")

	res, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, model.SourceFixBranch, res.Findings[0].Source)
	assert.Equal(t, 51, res.Findings[0].PR)
}
