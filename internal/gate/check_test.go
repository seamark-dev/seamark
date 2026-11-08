package gate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// checkFixture builds an indexed workspace where editing main.go reaches
// infra:mutate transitively (main -> deploy -> kubectl-ish sink via the
// workspace overlay).
func checkFixture(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	return indexedWorkspace(t, map[string]string{
		"go.mod": "module example.com/cx\n",
		"main.go": `package main

func main() {
	deploy()
}

func helperOnly() {}
`,
		"infra.go": `package main

import "os/exec"

func deploy() {
	exec.Command("kubectl", "apply").Run()
}
`,
	})
}

// indexedWorkspace writes files into a new root and indexes it. An
// overlay marks every os/exec Command call as infra:mutate, so the
// default diff rule can fire on a change that reaches exec.
func indexedWorkspace(t *testing.T, files map[string]string) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	// proc:exec comes from the default catalogue; add an overlay tag that
	// marks this deploy helper as infra so the default check rule fires.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"), []byte(`
sinks:
  - language: go
    import: os/exec
    names: [Command]
    tag: infra:mutate
`), 0o644))

	sum, err := index.Run(index.Options{Root: root})
	require.NoError(t, err)

	st, err = store.Open(sum.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	return st, root
}

const mainDiff = `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -3,3 +3,4 @@
 func main() {
 	deploy()
+	// touched
 }
`

func TestCheckDiffBlastRadius(t *testing.T) {
	st, root := checkFixture(t)

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	d, err := EvalDiff(p, st, mainDiff, SourceStdin)
	require.NoError(t, err)

	assert.Contains(t, d.Effects, "infra:mutate",
		"main.go does not call kubectl itself; the blast radius is transitive")
	assert.Contains(t, d.Effects, "proc:exec")
	assert.Equal(t, VerdictApproval, d.Verdict, "the default diff rule fires")
	require.NotEmpty(t, d.Matches)
	assert.Equal(t, "diff-reaches-infra", d.Matches[0].RuleID)
}

func TestCheckDiffOutsideBlastRadius(t *testing.T) {
	st, root := checkFixture(t)

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	// The hunk adds line 8, below helperOnly (line 7). Only the added
	// line counts, so no symbol is touched: the verdict allows with no
	// effects, and the note says that the reach of main.go is unknown.
	quiet := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -7,1 +7,2 @@
 func helperOnly() {}
+// harmless
`

	d, err := EvalDiff(p, st, quiet, SourceStdin)
	require.NoError(t, err)
	assert.Empty(t, d.Effects)
	assert.Equal(t, VerdictAllow, d.Verdict)
	require.Len(t, d.Notes, 1)
	assert.Contains(t, d.Notes[0], "(main.go)")
}

func TestEvalDiffRegistersHunklessRecords(t *testing.T) {
	st, root := checkFixture(t)

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	// Records without any `@@` hunk still alter behaviour-relevant state
	// and must enter diff.files and the uncertainty accounting — a
	// binary swap or a +x bit must never read as an empty diff.
	for name, tc := range map[string]struct{ diff, path string }{
		"binary": {
			"diff --git a/logo.png b/logo.png\nindex abc1234..def5678 100644\n" +
				"Binary files a/logo.png and b/logo.png differ\n", "logo.png"},
		"mode-only": {
			"diff --git a/script.sh b/script.sh\nold mode 100644\nnew mode 100755\n", "script.sh"},
		"rename-only": {
			"diff --git a/old.go b/new.go\nsimilarity index 100%\n" +
				"rename from old.go\nrename to new.go\n", "new.go"},
		"empty-file": {
			"diff --git a/empty.go b/empty.go\nnew file mode 100644\nindex 0000000..e69de29\n", "empty.go"},
	} {
		d, err := EvalDiff(p, st, tc.diff, SourceStdin)
		require.NoError(t, err, name)
		require.NotEmpty(t, d.Notes, "%s: a hunkless change must carry uncertainty", name)
		assert.Contains(t, d.Notes[0], tc.path, "%s: the affected path must be named", name)
	}
}

func TestChangedPaths(t *testing.T) {
	diff := "diff --git a/api/a.go b/api/a.go\n" +
		"--- a/api/a.go\n+++ b/api/a.go\n@@ -1,2 +1,2 @@\n-x\n+y\n" +
		"diff --git a/old.go b/new.go\nrename from old.go\nrename to new.go\n" +
		"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-z\n" +
		// A --no-prefix deletion: the `diff --git` header has no ` b/`
		// marker to split on, so the old-file fallback is the ONLY
		// thing that can register the path.
		"diff --git gone2.go gone2.go\n--- gone2.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-w\n"

	got := ChangedPaths(diff)

	// A rename registers its NEW path — the file going forward, the
	// same reading EvalDiff uses; a whole-file deletion registers the
	// old path (what vanished must not look like an empty diff).
	assert.Equal(t, []string{"api/a.go", "new.go", "gone.go", "gone2.go"}, got,
		"edits, renames, and whole-file deletions all register")
}

func TestEvalDiffSortsEffects(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{
		"go.mod": "module example.com/cx\n",
		"main.go": `package main

import (
	"net/http"
	"os"
	"os/exec"
)

func main() {
	exec.Command("kubectl", "apply").Run()
	_ = os.WriteFile("out.txt", nil, 0o644)
	_, _ = http.Get("https://x.example")
}
`,
	})

	// The rule indexes diff.effects. It matches only when CEL sees the
	// sorted list, the same order as the decision.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
require_approval:
  - id: first-effect-is-fs-write
    when: 'diff.effects[0] == "fs:write"'
    message: the first effect is fs:write
`), 0o644))

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -10,1 +10,2 @@
 	exec.Command("kubectl", "apply").Run()
+	// touched
`

	// Map order in Go changes between runs, so one run can pass by chance.
	for run := range 20 {
		d, err := EvalDiff(p, st, diff, SourceStdin)
		require.NoError(t, err)

		assert.Equal(t, []string{"fs:write", "infra:mutate", "net:egress", "proc:exec"}, d.Effects, "run %d", run)
		assert.Equal(t, VerdictApproval, d.Verdict, "run %d: CEL sees the sorted effects", run)
	}
}

func TestEvalDiffSortsFiles(t *testing.T) {
	st, root := checkFixture(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
require_approval:
  - id: first-file-is-a
    when: 'diff.files[0] == "a.go"'
    message: the first file is a.go
`), 0o644))

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	// The diff names b.go first. The rule matches only when diff.files
	// is sorted.
	diff := "diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-x\n+y\n" +
		"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n"

	for run := range 20 {
		d, err := EvalDiff(p, st, diff, SourceStdin)
		require.NoError(t, err)

		assert.Equal(t, VerdictApproval, d.Verdict, "run %d: diff.files is sorted", run)
	}
}

func TestEvalDiffNotesAnEmptyGitDiff(t *testing.T) {
	st, root := checkFixture(t)

	// Empty output of git diff HEAD is a clean tree or a tree with only
	// untracked files. Both modes allow it with a note, because a clean
	// tree is a normal state.
	for _, mode := range []string{"warn", "enforce"} {
		p, err := LoadPolicy(root)
		require.NoError(t, err)

		p.Mode = mode

		d, err := EvalDiff(p, st, "", SourceGitDiffHead)
		require.NoError(t, err, mode)

		assert.Equal(t, VerdictAllow, d.Verdict, mode)
		assert.False(t, d.Blocking(), mode)
		assert.Equal(t, []string{"no changed files in git diff HEAD; untracked files are not part of it"}, d.Notes, mode)
	}
}

func TestEvalDiffNoFileInput(t *testing.T) {
	st, root := checkFixture(t)

	// Each input has bytes but registers no path. The last one is a
	// --no-prefix binary record: its header has no b/ marker, and it has
	// no ---, +++ or rename line.
	inputs := map[string]string{
		"text":            "not a diff\n",
		"blank line":      "\n",
		"headerless hunk": "@@ -1 +1 @@\n-a\n+b\n",
		"no-prefix binary": "diff --git logo.png logo.png\n" +
			"Binary files logo.png and logo.png differ\n",
	}

	for name, input := range inputs {
		p, err := LoadPolicy(root)
		require.NoError(t, err)

		d, err := EvalDiff(p, st, input, SourceStdin)
		require.NoError(t, err, name)

		assert.Equal(t, VerdictAllow, d.Verdict, name)
		assert.Equal(t, []string{"stdin names no changed file (no diff --git, ---, +++ or rename header with a path); " +
			"this verdict covers no change"}, d.Notes, name)

		// Under enforcement the gate cannot read the input, so it blocks.
		p.Mode = "enforce"

		_, err = EvalDiff(p, st, input, SourceDiffArgument)
		require.ErrorIs(t, err, ErrBlocked, name)
		assert.Equal(t, "blocked by policy: the diff argument names no changed file "+
			"(no diff --git, ---, +++ or rename header with a path); "+
			"enforce mode blocks input that it cannot read as a diff", err.Error(), name)
	}
}
