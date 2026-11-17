package history

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	shared := FileCommits(context.Background(), root, "a.go")
	require.Len(t, shared, 2, "a.go was in two commits")

	funcs := PartnerFunctions(context.Background(), root, "b.go", shared, 5)
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

	shared := FileCommits(context.Background(), root, "a.go")
	require.Len(t, shared, 1)

	assert.Empty(t, PartnerFunctions(context.Background(), root, "b.go", shared, 5),
		"a pure file-creation commit names no functions")
}

func TestFuncGrainDegradesWithoutGit(t *testing.T) {
	root := t.TempDir() // not a git repo

	assert.Nil(t, FileCommits(context.Background(), root, "x.go"))
	assert.Nil(t, PartnerFunctions(context.Background(), root, "x.go", map[string]bool{"abc": true}, 3))
}

func TestPartnerFunctionsEmptySharedIsNoop(t *testing.T) {
	root, commit := contentRepo(t)
	commit("x", map[string]string{"a.go": "package a\n\nfunc Foo() {}\n"})

	assert.Nil(t, PartnerFunctions(context.Background(), root, "a.go", nil, 3), "no shared commits → no work")
}

func TestFuncGrainStopsAtCallerDeadline(t *testing.T) {
	root, commit := contentRepo(t)
	commit("x", map[string]string{"a.go": "package a\n\nfunc Foo() {}\n"})

	// A caller that shares one budget across many scans passes a context;
	// once it is spent every scan degrades to no output instead of running
	// on for its own full timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Nil(t, FileCommits(ctx, root, "a.go"))
	assert.Nil(t, PartnerFunctions(ctx, root, "a.go", map[string]bool{"abc": true}, 3))
}

func TestPartnerFunctionsIgnoresCommitsOutsideTheSharedSet(t *testing.T) {
	root, commit := contentRepo(t)

	commit("create", map[string]string{"b.go": "package b\n\nfunc Baz() int {\n\treturn 1\n}\n"})
	commit("only b", map[string]string{"b.go": "package b\n\nfunc Baz() int {\n\treturn 2\n}\n"})

	// The shared set names a hash git does not know; --stdin then has no
	// commit to show, and the pathspec alone must not widen the scan to the
	// partner's full history.
	assert.Empty(t, PartnerFunctions(context.Background(), root, "b.go",
		map[string]bool{strings.Repeat("0", 40): true}, 3))
}

// grainConfigRepo builds a repository where b.go changes together with an
// anchor file a.go. The anchor starts as old.go and is renamed in the third
// commit. sign holds git options that sign each commit, or nil.
func grainConfigRepo(t *testing.T, sign []string) string {
	t.Helper()

	root := t.TempDir()

	b := func(beta, delta string) string {
		return "package b\n\ntype Gamma struct{}\n\nfunc Beta() int {\n\treturn " + beta +
			"\n}\n\nfunc Delta() int {\n\treturn " + delta + "\n}\n"
	}

	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
	}

	// Each commit gets its own date, so git lists the commits in one
	// order and the tie between Beta and Delta always breaks the same way.
	day := 0
	commit := func(msg string) {
		day++
		date := fmt.Sprintf("2026-01-%02dT00:00:00Z", day)

		isolatedGit(t, root, "add", "-A")

		cmd := exec.Command("git", append(append([]string{}, sign...), "commit", "-q", "-m", msg)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)

		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}

	isolatedGit(t, root, "init", "-q", "-b", "main")

	write("old.go", "package a\n\nfunc Alpha() int {\n\treturn 1\n}\n")
	write("b.go", b("1", "1"))
	commit("create")

	write("old.go", "package a\n\nfunc Alpha() int {\n\treturn 2\n}\n")
	write("b.go", b("2", "1"))
	commit("change Alpha and Beta")

	isolatedGit(t, root, "mv", "old.go", "a.go")
	commit("rename the anchor")

	write("a.go", "package a\n\nfunc Alpha() int {\n\treturn 3\n}\n")
	write("b.go", b("3", "1"))
	commit("change Alpha and Beta again")

	write("a.go", "package a\n\nfunc Alpha() int {\n\treturn 4\n}\n")
	write("b.go", b("3", "2"))
	commit("change Alpha and Delta")

	return root
}

// constantScript writes an executable script that prints a constant line
// and touches marker when it runs. It stands in for an external diff
// program and a textconv driver.
func constantScript(t *testing.T) (script, marker string) {
	t.Helper()

	dir := t.TempDir()
	script = filepath.Join(dir, "const.sh")
	marker = filepath.Join(dir, "ran")
	body := "#!/bin/sh\ntouch '" + marker + "'\necho constant\n"

	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	return script, marker
}

// sshSigning returns git options that sign a commit with a new SSH key,
// and the config that lets git log check that signature. It skips the
// test when ssh-keygen is missing.
func sshSigning(t *testing.T) (sign []string, verify [][2]string) {
	t.Helper()

	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is missing")
	}

	dir := t.TempDir()
	key := filepath.Join(dir, "key")

	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "t", "-f", key).CombinedOutput()
	require.NoError(t, err, "%s", out)

	pub, err := os.ReadFile(key + ".pub")
	require.NoError(t, err)

	signers := filepath.Join(dir, "allowed")
	require.NoError(t, os.WriteFile(signers, []byte("t@example.com "+string(pub)), 0o644))

	sign = []string{"-c", "gpg.format=ssh", "-c", "user.signingKey=" + key, "-c", "commit.gpgSign=true"}
	verify = [][2]string{{"gpg.format", "ssh"}, {"gpg.ssh.allowedSignersFile", signers}}

	return sign, verify
}

func TestPartnerFunctionsIgnoresGitConfig(t *testing.T) {
	// Each case sets a config that changes the plain git output that the
	// companion names read. FileCommits and PartnerFunctions must still
	// return what they return with no config.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	script, marker := constantScript(t)

	cases := []struct {
		name   string
		config [][2]string
		attrs  string
		signed bool
	}{
		{name: "color.ui always", config: [][2]string{{"color.ui", "always"}}},
		{name: "color.diff always", config: [][2]string{{"color.diff", "always"}}},
		{
			name:   "textconv",
			config: [][2]string{{"diff.hide.textconv", script}},
			attrs:  "*.go diff=hide\n",
		},
		{name: "log.showSignature", config: [][2]string{{"log.showSignature", "true"}}, signed: true},
		{name: "log.follow", config: [][2]string{{"log.follow", "true"}}},
		{name: "diff.noprefix", config: [][2]string{{"diff.noprefix", "true"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var (
				sign   []string
				verify [][2]string
			)

			if c.signed {
				sign, verify = sshSigning(t)
			}

			root := grainConfigRepo(t, sign)
			ctx := context.Background()

			wantCommits := FileCommits(ctx, root, "a.go")
			require.Len(t, wantCommits, 3)

			wantFuncs := PartnerFunctions(ctx, root, "b.go", wantCommits, 5)
			require.Equal(t, []string{"Delta", "Beta"}, wantFuncs)

			plainLog := []string{"log", "--no-renames", "--format=%H", "--", "a.go"}
			plainPatch := []string{"log", "--no-renames", "-U0", "-p", "--format=", "--", "b.go"}
			before := isolatedGit(t, root, plainLog...) + isolatedGit(t, root, plainPatch...)

			for _, kv := range append(verify, c.config...) {
				isolatedGit(t, root, "config", kv[0], kv[1])
			}

			if c.attrs != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte(c.attrs), 0o644))
			}

			// The case is real only when it changes the plain git output.
			after := isolatedGit(t, root, plainLog...) + isolatedGit(t, root, plainPatch...)
			assert.NotEqual(t, before, after, "plain git log")

			_ = os.Remove(marker)

			gotCommits := FileCommits(ctx, root, "a.go")
			assert.Equal(t, wantCommits, gotCommits)
			assert.Equal(t, wantFuncs, PartnerFunctions(ctx, root, "b.go", gotCommits, 5))
			assert.NoFileExists(t, marker, "the driver never runs")
		})
	}
}

func TestPartnerFunctionsKeepsRepositoryFuncname(t *testing.T) {
	// --no-textconv drops only the content conversion of a diff driver.
	// The xfuncname rule of the same driver still sets the function
	// context, so a repository keeps its own function names.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	script, _ := constantScript(t)
	root := grainConfigRepo(t, nil)

	isolatedGit(t, root, "config", "diff.types.textconv", script)
	isolatedGit(t, root, "config", "diff.types.xfuncname", "^type .*$")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.go diff=types\n"), 0o644))

	ctx := context.Background()
	shared := FileCommits(ctx, root, "a.go")

	assert.Equal(t, []string{"Gamma"}, PartnerFunctions(ctx, root, "b.go", shared, 5))
}
