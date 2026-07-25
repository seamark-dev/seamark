package history

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

// gitRepo builds a throwaway repo and returns its root plus a commit helper
// that writes the given files (content = file name + revision) and commits.
func gitRepo(t *testing.T) (root string, commit func(msg string, files ...string)) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
	git("init", "-b", "main")

	rev := 0
	commit = func(msg string, files ...string) {
		t.Helper()
		rev++
		for _, f := range files {
			p := filepath.Join(root, f)
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("%s rev %d\n", f, rev)), 0o644))
		}
		git("add", "-A")
		git("commit", "-m", msg, "--allow-empty")
	}
	return root, commit
}

func TestMineDecisionsAndCoChange(t *testing.T) {
	root, commit := gitRepo(t)

	// a.go and b.go move together three times; c.go churns independently
	// so that lift for (a,b) clears 1.0 comfortably.
	commit("feat: a+b together", "pkg/a.go", "pkg/b.go")
	commit("chore: c alone", "pkg/c.go")
	commit("fix: a+b again\n\nlonger body\nwith\ttabs", "pkg/a.go", "pkg/b.go")
	commit("chore: c again", "pkg/c.go")
	commit("fix: a+b third time", "pkg/a.go", "pkg/b.go")
	commit("touch everything once", "pkg/a.go", "pkg/b.go", "pkg/c.go")

	decisions, pairs, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, decisions, 6)

	// git log is newest-first; the last commit made is first.
	newest := decisions[0]
	assert.Equal(t, "touch everything once", newest.Title)
	assert.Len(t, newest.Files, 3)
	assert.Equal(t, "tester", newest.Author)
	assert.NotZero(t, newest.TS)
	assert.NotEmpty(t, newest.Ref)

	// Multi-line body with tabs must not corrupt titles or file lists.
	for _, d := range decisions {
		assert.NotContains(t, d.Title, "\n", "body leaked into title")
		if d.Title == "fix: a+b again" {
			assert.Len(t, d.Files, 2, "tabbed body corrupted numstat parsing")
		}
	}

	var ab *model.CoChange
	for i := range pairs {
		if pairs[i].FileA == "pkg/a.go" && pairs[i].FileB == "pkg/b.go" {
			ab = &pairs[i]
		}
	}
	require.NotNil(t, ab, "no (a,b) pair mined; pairs = %+v", pairs)
	assert.Equal(t, 4, ab.Together)
	assert.Equal(t, 6, ab.Total)
	// n(a)=4, n(b)=4, together=4, N=6 → lift = 4·6/(4·4) = 1.5
	assert.InDelta(t, 1.5, ab.Lift, 0.01)
}

func TestMineRevertDetection(t *testing.T) {
	root, commit := gitRepo(t)
	commit("feat: add x", "x.go")
	commit(`Revert "feat: add x"`, "x.go")

	decisions, _, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	assert.Equal(t, model.DecisionRevert, decisions[0].Kind)
	assert.Equal(t, model.DecisionCommit, decisions[1].Kind)
}

func TestCoChangeFilters(t *testing.T) {
	root, commit := gitRepo(t)

	// Bulk commit exceeding MaxFilesPerCommit must not produce pairs.
	bulk := make([]string, 12)
	for i := range bulk {
		bulk[i] = fmt.Sprintf("bulk/f%d.go", i)
	}
	commit("mass reformat", bulk...)
	commit("mass reformat 2", bulk...)
	// Lockfiles and vendored paths are excluded even in small commits.
	commit("bump deps", "go.sum", "main.go")
	commit("bump deps again", "go.sum", "main.go")
	commit("vendor sync", "vendor/lib/x.go", "main.go")
	commit("vendor sync 2", "vendor/lib/x.go", "main.go")

	_, pairs, err := Mine(root, Options{MaxFilesPerCommit: 10})
	require.NoError(t, err)
	for _, p := range pairs {
		for _, banned := range []string{"go.sum", "bulk/f0.go", "vendor/lib/x.go"} {
			assert.NotEqual(t, banned, p.FileA, "excluded path leaked into co-change: %+v", p)
			assert.NotEqual(t, banned, p.FileB, "excluded path leaked into co-change: %+v", p)
		}
	}
}

func TestMinTogetherThreshold(t *testing.T) {
	root, commit := gitRepo(t)
	commit("once", "p/a.go", "p/b.go")
	commit("unrelated", "p/c.go")

	_, pairs, err := Mine(root, Options{})
	require.NoError(t, err)
	assert.Empty(t, pairs, "single co-occurrence must not be stored (MinTogether=2)")
}

func TestMineHostileCommitBody(t *testing.T) {
	root, commit := gitRepo(t)

	// A body carrying a forged record: with in-band separators this used to
	// insert an attacker-chosen decision row and steal the real commit's
	// files. NUL framing makes it inert message text.
	forged := "evil\n\n" +
		"\x01" + strings.Repeat("a", 40) +
		"\x02Forged Author\x021111111111\x02FORGED TITLE\x02x\x03" +
		"1\t1\tstolen.go"
	commit(forged, "real.go")
	commit("stray separators\n\nbody with \x02 and \x03 inside", "real.go", "other.go")

	decisions, _, err := Mine(root, Options{})
	require.NoError(t, err)
	require.Len(t, decisions, 2, "each git commit yields exactly one decision")

	for _, d := range decisions {
		assert.Regexp(t, "^[0-9a-f]{40,64}$", d.Ref)
		assert.Equal(t, "tester", d.Author)
		assert.NotContains(t, d.Title, "FORGED")
		assert.NotContains(t, d.Files, "stolen.go",
			"numstat-looking body lines must not leak into the file list")
	}
	// In-band separators are inert message text: titles, bodies, and file
	// lists all survive verbatim.
	assert.Equal(t, "stray separators", decisions[0].Title)
	assert.Equal(t, "body with \x02 and \x03 inside", decisions[0].Body)
	assert.ElementsMatch(t, []string{"real.go", "other.go"}, decisions[0].Files)
	assert.Equal(t, "evil", decisions[1].Title)
	assert.Equal(t, []string{"real.go"}, decisions[1].Files)
}

func TestMineNonASCIIPaths(t *testing.T) {
	root, commit := gitRepo(t)
	commit("täst one", "päth.go", "main.go")
	commit("täst two", "päth.go", "main.go")

	decisions, pairs, err := Mine(root, Options{})
	require.NoError(t, err)
	// Without core.quotePath=off git C-quotes the name ("p\303\244th.go"),
	// which would never match the parse layer's file paths.
	assert.Contains(t, decisions[0].Files, "päth.go")
	require.Len(t, pairs, 1)
	assert.Equal(t, "main.go", pairs[0].FileA)
	assert.Equal(t, "päth.go", pairs[0].FileB)
}

func TestMineEmptyRepo(t *testing.T) {
	root, _ := gitRepo(t) // init only, no commits

	decisions, pairs, err := Mine(root, Options{})
	require.NoError(t, err, "a repo with no commits is empty history, not an error")
	assert.Empty(t, decisions)
	assert.Empty(t, pairs)
}

func TestIsRepo(t *testing.T) {
	root, _ := gitRepo(t)
	assert.True(t, IsRepo(root))
	assert.False(t, IsRepo(t.TempDir()))
}
