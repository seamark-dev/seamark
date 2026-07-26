package history

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFuncName(t *testing.T) {
	cases := []struct{ ctx, want string }{
		{"func (x *W) Run() {", "Run"},
		{"func Greet(name string) {", "Greet"},
		{"def greet(self):", "greet"},
		{"class TradeCreate(BaseModel):", "TradeCreate"},
		{"export interface components {", "components"},
		{"type Options struct {", "Options"},
		{"  async def fetch(self, path):", "fetch"},
		{"// just a comment", ""},
		{"x = 1", ""},
		{"", ""},
		// A keyword mid-sentence must NOT be mistaken for a declaration
		// (the anchoring fix): git's context is normally the decl line, but
		// prose can slip through the xfuncname heuristic.
		{"return interface value", ""},
		{"the func to call is x", ""},
		// Anonymous / typeless forms carry no name.
		{"func() {", ""},
		{"interface{}", ""},
		{"struct{}", ""},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, funcName(c.ctx), "ctx: %q", c.ctx)
	}
}

// contentRepo builds a throwaway repo whose commits write caller-supplied
// file contents (so tests can commit real functions, unlike history_test's
// placeholder-content helper).
func contentRepo(t *testing.T) (root string, commit func(msg string, files map[string]string)) {
	t.Helper()

	root = t.TempDir()

	git := func(args ...string) {
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
		for name, body := range files {
			require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
		}

		git("add", "-A")
		git("commit", "-m", msg)
	}

	return root, commit
}

func TestPartnerFunctionsNamesSharedCommitFunctions(t *testing.T) {
	root, commit := contentRepo(t)

	aV1 := "package a\n\nfunc Foo() int { return 1 }\n\nfunc Bar() int { return 2 }\n"
	bV1 := "package b\n\nfunc Baz() int {\n\treturn 10\n}\n"

	commit("create both", map[string]string{"a.go": aV1, "b.go": bV1})

	// A commit that modifies a function IN BOTH files: Foo in a.go, Baz in b.go.
	commit("touch Foo and Baz together", map[string]string{
		"a.go": "package a\n\nfunc Foo() int { return 100 }\n\nfunc Bar() int { return 2 }\n",
		"b.go": "package b\n\nfunc Baz() int {\n\treturn 999\n}\n",
	})

	// A commit touching only b.go (must NOT be attributed to a.go's shared set).
	commit("touch Baz alone", map[string]string{
		"b.go": "package b\n\nfunc Baz() int {\n\treturn 7\n}\n\nfunc Solo() {}\n",
	})

	shared := FileCommits(root, "a.go")
	require.Len(t, shared, 2, "a.go was in two commits")

	funcs := PartnerFunctions(root, "b.go", shared, 5)
	assert.Contains(t, funcs, "Baz", "Baz changed in the commit shared with a.go")
	assert.NotContains(t, funcs, "Solo", "Solo changed only in a b.go-alone commit")
}

func TestPartnerFunctionsNewFileHasNoFuncContext(t *testing.T) {
	root, commit := contentRepo(t)

	// A file's CREATION hunk (@@ -0,0 +1,N @@) carries no enclosing
	// function, so a commit that only creates the partner contributes no
	// names — grain reports only what was genuinely modified.
	commit("create both", map[string]string{
		"a.go": "package a\n\nfunc Foo() {}\n",
		"b.go": "package b\n\nfunc Baz() {}\n",
	})

	shared := FileCommits(root, "a.go")
	require.Len(t, shared, 1)

	assert.Empty(t, PartnerFunctions(root, "b.go", shared, 5),
		"a pure file-creation commit names no functions")
}

func TestFuncGrainDegradesWithoutGit(t *testing.T) {
	root := t.TempDir() // not a git repo

	assert.Nil(t, FileCommits(root, "x.go"))
	assert.Nil(t, PartnerFunctions(root, "x.go", map[string]bool{"abc": true}, 3))
}

func TestPartnerFunctionsEmptySharedIsNoop(t *testing.T) {
	root, commit := contentRepo(t)
	commit("x", map[string]string{"a.go": "package a\n\nfunc Foo() {}\n"})

	assert.Nil(t, PartnerFunctions(root, "a.go", nil, 3), "no shared commits → no work")
}
