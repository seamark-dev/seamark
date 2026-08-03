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

	root = t.TempDir()
	files := map[string]string{
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
	}
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

	d, err := EvalDiff(p, st, mainDiff)
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

	// A hunk touching only helperOnly (line 7) reaches nothing.
	quiet := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -7,1 +7,2 @@
 func helperOnly() {}
+// harmless
`

	d, err := EvalDiff(p, st, quiet)
	require.NoError(t, err)
	assert.Empty(t, d.Effects)
	assert.Equal(t, VerdictAllow, d.Verdict)
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
		d, err := EvalDiff(p, st, tc.diff)
		require.NoError(t, err, name)
		require.NotEmpty(t, d.Notes, "%s: a hunkless change must carry uncertainty", name)
		assert.Contains(t, d.Notes[0], tc.path, "%s: the affected path must be named", name)
	}
}
