package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goValueFixture holds a const that a function passes to
// exec.CommandContext, a var with no call, and a struct type. No edge
// records that run reads timeout, so a change to the const alone
// reaches no effect.
const goValueFixture = `package git

import (
	"context"
	"os/exec"
	"time"
)

const timeout = 15 * time.Second

var plain = map[string]int{
	"a": 1,
}

type Config struct {
	Dir string
}

func run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return exec.CommandContext(ctx, "git", "status").Run()
}
`

const goValueHeader = "diff --git a/git/git.go b/git/git.go\n--- a/git/git.go\n+++ b/git/git.go\n"

// The hunks below change one symbol each of goValueFixture.
const (
	goConstHunk = "@@ -9,1 +9,1 @@\n-const timeout = 15 * time.Second\n+const timeout = 30 * time.Second\n"
	goVarHunk   = "@@ -12,1 +12,2 @@\n \t\"a\": 1,\n+\t\"b\": 2,\n"
	goTypeHunk  = "@@ -16,1 +16,2 @@\n \tDir string\n+\tEnv []string\n"
	goFuncHunk  = "@@ -20,1 +20,1 @@\n" +
		"-\tctx, cancel := context.WithTimeout(ctx, timeout)\n" +
		"+\tctx, cancel := context.WithTimeout(ctx, 30*time.Second)\n"
)

// valueNote1 is the note for one value or type symbol.
func valueNote1(fqn string) string {
	return "1 changed value or type has no effects of its own (" + fqn + ") — " +
		"seamark does not track readers of a value, so its effect reach is unknown, not clean"
}

// goValueWorkspace indexes goValueFixture under the default policy and
// returns a function that evaluates one diff against it.
func goValueWorkspace(t *testing.T) func(diff string) *Decision {
	t.Helper()

	st, root := indexedWorkspace(t, map[string]string{
		"go.mod":     "module example.com/cx\n",
		"git/git.go": goValueFixture,
	})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	return func(diff string) *Decision {
		t.Helper()

		d, err := EvalDiff(p, st, diff, SourceStdin)
		require.NoError(t, err)

		return d
	}
}

// TestEvalDiffNotesAChangedGoValue proves that a change confined to a Go
// const, var or type says that its reach is unknown. The verdict and the
// effects stay as before: the note never changes them.
func TestEvalDiffNotesAChangedGoValue(t *testing.T) {
	eval := goValueWorkspace(t)

	for name, tc := range map[string]struct{ hunk, fqn string }{
		"const": {goConstHunk, "git.timeout"},
		"var":   {goVarHunk, "git.plain"},
		"type":  {goTypeHunk, "git.Config"},
	} {
		d := eval(goValueHeader + tc.hunk)

		assert.Empty(t, d.Effects, name)
		assert.Equal(t, VerdictAllow, d.Verdict, name)
		assert.Equal(t, []string{valueNote1(tc.fqn)}, d.Notes, name)
	}
}

// TestEvalDiffInsideAGoFunctionHasNoValueNote proves that the same value
// written inline in the function reports the effect of the function and
// no note.
func TestEvalDiffInsideAGoFunctionHasNoValueNote(t *testing.T) {
	eval := goValueWorkspace(t)

	d := eval(goValueHeader + goFuncHunk)

	assert.Contains(t, d.Effects, "proc:exec")
	assert.Empty(t, d.Notes)
}

// TestEvalDiffNamesEveryChangedValueOnce proves that a diff that changes
// several values gives one note in the plural form, with sorted names.
// A function edit in the same diff adds its effect and no name.
func TestEvalDiffNamesEveryChangedValueOnce(t *testing.T) {
	eval := goValueWorkspace(t)

	d := eval(goValueHeader + goConstHunk + goVarHunk + goTypeHunk + goFuncHunk)

	assert.Contains(t, d.Effects, "proc:exec")
	assert.Equal(t, []string{"3 changed values or types have no effects of their own " +
		"(git.Config, git.plain, git.timeout) — seamark does not track readers of a value, " +
		"so their effect reach is unknown, not clean"}, d.Notes)
}

// TestEvalDiffBoundsTheNamedValues proves that the note names at most ten
// symbols and counts the rest, so a change to a generated file of
// constants keeps the note short.
func TestEvalDiffBoundsTheNamedValues(t *testing.T) {
	var src, removed, added strings.Builder

	src.WriteString("package main\n\n")

	for i := range 12 {
		fmt.Fprintf(&src, "const c%02d = %d\n", i, i)
		fmt.Fprintf(&removed, "-const c%02d = %d\n", i, i)
		fmt.Fprintf(&added, "+const c%02d = %d\n", i, i+100)
	}

	st, root := indexedWorkspace(t, map[string]string{
		"go.mod":  "module example.com/cx\n",
		"main.go": src.String(),
	})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n" +
		"@@ -3,12 +3,12 @@\n" + removed.String() + added.String()

	d, err := EvalDiff(p, st, diff, SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, []string{"12 changed values or types have no effects of their own " +
		"(c00, c01, c02, c03, c04, c05, c06, c07, c08, c09, and 2 more) — " +
		"seamark does not track readers of a value, so their effect reach is unknown, not clean"}, d.Notes)
}

// TestEvalDiffLetsARuleSeeAChangedValue proves that a rule over
// diff.unknown_reach_symbols fires on a value change and not on a
// function change, while the default policy still allows the value change.
func TestEvalDiffLetsARuleSeeAChangedValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{
		"go.mod":     "module example.com/cx\n",
		"git/git.go": goValueFixture,
	})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	d, err := EvalDiff(p, st, goValueHeader+goConstHunk, SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, VerdictAllow, d.Verdict, "the default policy has no rule over the count")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(`
require_approval:
  - id: value-reach-unknown
    when: 'diff.unknown_reach_symbols > 0'
    message: a changed value has readers that seamark does not track
`), 0o644))

	p, err = LoadPolicy(root)
	require.NoError(t, err)

	d, err = EvalDiff(p, st, goValueHeader+goConstHunk, SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, VerdictApproval, d.Verdict)
	require.Len(t, d.Matches, 1)
	assert.Equal(t, "value-reach-unknown", d.Matches[0].RuleID)

	d, err = EvalDiff(p, st, goValueHeader+goFuncHunk, SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, VerdictAllow, d.Verdict, "a function change counts no value")
}

// ecmaValueFixture holds a module const, a type alias, and a class whose
// only write lies in a method. The class carries no effect of its own,
// because the calls of dump stay with dump.
const ecmaValueFixture = `import * as fs from "fs";

export const timeout = 15000;

export type Mode = "fast" | "slow";

export class Reader {
	label = "reader";

	dump(p: string) {
		fs.writeFileSync(p, "");
	}
}
`

// TestEvalDiffNotesAChangedEcmaValue proves the note for a JS/TS module
// const, a type alias, and a class field, which is code of its class.
func TestEvalDiffNotesAChangedEcmaValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{"store.ts": ecmaValueFixture})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	header := "diff --git a/store.ts b/store.ts\n--- a/store.ts\n+++ b/store.ts\n"

	for name, tc := range map[string]struct{ hunk, fqn string }{
		"const": {"@@ -3,1 +3,1 @@\n-export const timeout = 15000;\n+export const timeout = 30000;\n", "store.timeout"},
		"type": {"@@ -5,1 +5,1 @@\n-export type Mode = \"fast\" | \"slow\";\n" +
			"+export type Mode = \"fast\" | \"slow\" | \"off\";\n", "store.Mode"},
		"class field": {"@@ -8,1 +8,1 @@\n-\tlabel = \"reader\";\n+\tlabel = \"Reader\";\n", "store.Reader"},
	} {
		d, err := EvalDiff(p, st, header+tc.hunk, SourceStdin)
		require.NoError(t, err, name)

		assert.Empty(t, d.Effects, name)
		assert.Equal(t, VerdictAllow, d.Verdict, name)
		assert.Equal(t, []string{valueNote1(tc.fqn)}, d.Notes, name)
	}

	// A change inside the method reports its write and no note. The line
	// lies in the class span too, but a method holds it.
	d, err := EvalDiff(p, st, header+"@@ -11,1 +11,1 @@\n"+
		"-\t\tfs.writeFileSync(p, \"\");\n+\t\tfs.writeFileSync(p, \"x\");\n", SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, []string{"fs:write"}, d.Effects)
	assert.Empty(t, d.Notes)

	// A signature edit counts its removed line as the blank line above,
	// which lies in the class and in no method. The class is not named,
	// because the same change group edits its method.
	d, err = EvalDiff(p, st, header+"@@ -10,1 +10,1 @@\n"+
		"-\tdump(p: string) {\n+\tdump(p: string, q = \"\") {\n", SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, []string{"fs:write"}, d.Effects)
	assert.Empty(t, d.Notes)
}

// pyValueFixture holds a module constant that a method passes to
// subprocess.run, and a class attribute.
const pyValueFixture = `import subprocess

TIMEOUT = 15


class Runner:
    retries = 3

    def run(self):
        subprocess.run(["git", "status"], timeout=TIMEOUT)
`

// TestEvalDiffNotesAChangedPythonValue proves the note for a Python
// module constant and a class attribute, and no note for method edits.
func TestEvalDiffNotesAChangedPythonValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{"runner.py": pyValueFixture})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	header := "diff --git a/runner.py b/runner.py\n--- a/runner.py\n+++ b/runner.py\n"

	for name, tc := range map[string]struct{ hunk, fqn string }{
		"module constant": {"@@ -3,1 +3,1 @@\n-TIMEOUT = 15\n+TIMEOUT = 30\n", "runner.TIMEOUT"},
		"class attribute": {"@@ -7,1 +7,1 @@\n-    retries = 3\n+    retries = 5\n", "runner.Runner"},
	} {
		d, err := EvalDiff(p, st, header+tc.hunk, SourceStdin)
		require.NoError(t, err, name)

		assert.Empty(t, d.Effects, name)
		assert.Equal(t, VerdictAllow, d.Verdict, name)
		assert.Equal(t, []string{valueNote1(tc.fqn)}, d.Notes, name)
	}

	for name, hunk := range map[string]string{
		"method body": "@@ -10,1 +10,1 @@\n" +
			"-        subprocess.run([\"git\", \"status\"], timeout=TIMEOUT)\n" +
			"+        subprocess.run([\"git\", \"status\", \"-s\"], timeout=TIMEOUT)\n",
		"method signature": "@@ -9,1 +9,1 @@\n-    def run(self):\n+    def run(self, quiet=False):\n",
	} {
		d, err := EvalDiff(p, st, header+hunk, SourceStdin)
		require.NoError(t, err, name)

		assert.Equal(t, []string{"proc:exec"}, d.Effects, name)
		assert.Empty(t, d.Notes, name)
	}
}
