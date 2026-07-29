package fixes

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
