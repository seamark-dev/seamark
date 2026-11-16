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

	// The removed signature lies between the blank line above and the
	// new signature, and both lines lie in the class. The class is not
	// named, because the same change group edits its method.
	d, err = EvalDiff(p, st, header+"@@ -10,1 +10,1 @@\n"+
		"-\tdump(p: string) {\n+\tdump(p: string, q = \"\") {\n", SourceStdin)
	require.NoError(t, err)
	assert.Equal(t, []string{"fs:write"}, d.Effects)
	assert.Empty(t, d.Notes)

	// An added method has an added blank line before it, which lies in
	// the class and in no method. The class is not named, because the
	// same change group adds its method.
	d, err = EvalDiff(p, st, header+"@@ -8,0 +9,4 @@\n+\n"+
		"+\tdump(p: string) {\n+\t\tfs.writeFileSync(p, \"\");\n+\t}\n", SourceStdin)
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

// adjacentGoFixture puts values next to each other and next to a
// function, with no blank line between them. The diff marks the line
// above a replaced or removed line as touched. That neighbour did not
// change, so the note must not name it.
//
//	 1 package cfg
//	 2
//	 3 import "os"
//	 4
//	 5 const (
//	 6 	first  = 1
//	 7 	second = 2
//	 8 )
//	 9
//	10 var defaults = map[string]int{
//	11 	"a": 1,
//	12 	"b": 2,
//	13 }
//	14 func write(p string) error {
//	15 	return os.WriteFile(p, nil, 0o600)
//	16 }
//	17
//	18 type Config struct {
//	19 	Dir string
//	20 	Env []string
//	21 }
const adjacentGoFixture = "package cfg\n\nimport \"os\"\n\nconst (\n\tfirst  = 1\n\tsecond = 2\n)\n\n" +
	"var defaults = map[string]int{\n\t\"a\": 1,\n\t\"b\": 2,\n}\n" +
	"func write(p string) error {\n\treturn os.WriteFile(p, nil, 0o600)\n}\n\n" +
	"type Config struct {\n\tDir string\n\tEnv []string\n}\n"

// TestEvalDiffNamesOnlyTheChangedGoValue proves that the note names the
// value that the change edits, and not the unchanged value or function
// on the line above it. Lines removed inside a value name that value.
func TestEvalDiffNamesOnlyTheChangedGoValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{
		"go.mod":     "module example.com/cx\n",
		"cfg/cfg.go": adjacentGoFixture,
	})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	const header = "diff --git a/cfg/cfg.go b/cfg/cfg.go\n--- a/cfg/cfg.go\n+++ b/cfg/cfg.go\n"

	cases := []struct {
		name    string
		hunk    string
		effects []string
		notes   []string
	}{
		{
			name:  "a replaced const below another const of the block",
			hunk:  "@@ -5,4 +5,4 @@\n const (\n \tfirst  = 1\n-\tsecond = 1\n+\tsecond = 2\n )\n",
			notes: []string{valueNote1("cfg.second")},
		},
		{
			// The removed const has no symbol on the new side. The const
			// above it did not change.
			name: "a removed const of the block",
			hunk: "@@ -5,5 +5,4 @@\n const (\n \tfirst  = 1\n \tsecond = 2\n-\tthird  = 3\n )\n",
		},
		{
			name:  "a removed entry inside a var",
			hunk:  "@@ -11,4 +11,3 @@\n \t\"a\": 1,\n \t\"b\": 2,\n-\t\"c\": 3,\n }\n",
			notes: []string{valueNote1("cfg.defaults")},
		},
		{
			name:  "a removed field inside a struct type",
			hunk:  "@@ -19,4 +19,3 @@\n \tDir string\n \tEnv []string\n-\tExtra bool\n }\n",
			notes: []string{valueNote1("cfg.Config")},
		},
		{
			// The closing line of defaults lies directly above the
			// replaced signature. The change reaches the write of the
			// function, and defaults did not change.
			name: "a function signature directly below a var",
			hunk: "@@ -13,3 +13,3 @@\n }\n-func write(path string) error {\n+func write(p string) error {\n" +
				" \treturn os.WriteFile(p, nil, 0o600)\n",
			effects: []string{"fs:write"},
		},
	}

	for _, c := range cases {
		d, err := EvalDiff(p, st, header+c.hunk, SourceStdin)
		require.NoError(t, err, c.name)

		effects := d.Effects
		if effects == nil {
			effects = []string{}
		}

		want := c.effects
		if want == nil {
			want = []string{}
		}

		assert.Equal(t, want, effects, c.name)
		assert.Equal(t, VerdictAllow, d.Verdict, c.name)
		assert.Equal(t, c.notes, d.Notes, c.name)
	}
}

// adjacentPyFixture holds two module constants on consecutive lines and
// a class of attributes with no method.
//
//	 1 import subprocess
//	 2
//	 3 TIMEOUT = 15
//	 4 RETRIES = 3
//	 5
//	 6
//	 7 class Color:
//	 8     RED = 1
//	 9     GREEN = 2
//	10
//	11
//	12 def run():
//	13     subprocess.run(["git", "status"], timeout=TIMEOUT)
const adjacentPyFixture = "import subprocess\n\nTIMEOUT = 15\nRETRIES = 3\n\n\n" +
	"class Color:\n    RED = 1\n    GREEN = 2\n\n\n" +
	"def run():\n    subprocess.run([\"git\", \"status\"], timeout=TIMEOUT)\n"

// TestEvalDiffNamesOnlyTheChangedPythonValue proves that a change to one
// of two consecutive constants names that constant only, that a removed
// constant names no survivor, and that a removed last attribute of a
// class names the class, although the class span ends above the removed
// line.
func TestEvalDiffNamesOnlyTheChangedPythonValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{"settings.py": adjacentPyFixture})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	const header = "diff --git a/settings.py b/settings.py\n--- a/settings.py\n+++ b/settings.py\n"

	for name, tc := range map[string]struct {
		hunk  string
		notes []string
	}{
		"a replaced constant below another constant": {
			hunk:  "@@ -3,2 +3,2 @@\n TIMEOUT = 15\n-RETRIES = 2\n+RETRIES = 3\n",
			notes: []string{valueNote1("settings.RETRIES")},
		},
		"a removed constant below another constant": {
			hunk: "@@ -4,3 +4,2 @@\n RETRIES = 3\n-MAX = 9\n \n",
		},
		"a removed last attribute of a class": {
			hunk:  "@@ -8,4 +8,3 @@\n     RED = 1\n     GREEN = 2\n-    BLUE = 3\n \n",
			notes: []string{valueNote1("settings.Color")},
		},
	} {
		d, err := EvalDiff(p, st, header+tc.hunk, SourceStdin)
		require.NoError(t, err, name)

		assert.Empty(t, d.Effects, name)
		assert.Equal(t, VerdictAllow, d.Verdict, name)
		assert.Equal(t, tc.notes, d.Notes, name)
	}
}

// TestEvalDiffNamesOnlyTheChangedEcmaValue proves that a change to one of
// two consecutive JS/TS module consts names that const only.
func TestEvalDiffNamesOnlyTheChangedEcmaValue(t *testing.T) {
	st, root := indexedWorkspace(t, map[string]string{
		"limits.ts": "export const timeout = 15000;\nexport const retries = 3;\n",
	})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	d, err := EvalDiff(p, st, "diff --git a/limits.ts b/limits.ts\n--- a/limits.ts\n+++ b/limits.ts\n"+
		"@@ -1,2 +1,2 @@\n export const timeout = 15000;\n-export const retries = 2;\n+export const retries = 3;\n",
		SourceStdin)
	require.NoError(t, err)

	assert.Empty(t, d.Effects)
	assert.Equal(t, []string{valueNote1("limits.retries")}, d.Notes)
}

// belowMethodCase is one diff of a test that changes a class attribute
// or field below a method, and the effects and notes that it gives.
type belowMethodCase struct {
	name    string
	hunk    string
	effects []string
	notes   []string
}

// evalBelowMethod indexes the one file name with src and requires each
// case to give its effects and notes under the default policy.
func evalBelowMethod(t *testing.T, name, src string, cases []belowMethodCase) {
	t.Helper()

	st, root := indexedWorkspace(t, map[string]string{name: src})

	p, err := LoadPolicy(root)
	require.NoError(t, err)

	header := "diff --git a/" + name + " b/" + name + "\n--- a/" + name + "\n+++ b/" + name + "\n"

	for _, c := range cases {
		d, err := EvalDiff(p, st, header+c.hunk, SourceStdin)
		require.NoError(t, err, c.name)

		effects := d.Effects
		if effects == nil {
			effects = []string{}
		}

		assert.Equal(t, c.effects, effects, c.name)
		assert.Equal(t, VerdictAllow, d.Verdict, c.name)
		assert.Equal(t, c.notes, d.Notes, c.name)
	}
}

// belowMethodPyFixture holds class attributes below a method, once after
// a blank line and once directly below the last statement. Deco holds a
// method that lost its decorator.
//
//	 1 import subprocess
//	 2
//	 3
//	 4 class Runner:
//	 5     retries = 3
//	 6
//	 7     def run(self):
//	 8         subprocess.run(["git", "status"])
//	 9
//	10     label = "runner"
//	11
//	12     def stop(self):
//	13         return 0
//	14
//	15
//	16 class Plain:
//	17     def f(self):
//	18         subprocess.run(["ls"])
//	19     size = 2
//	20
//	21
//	22 class Deco:
//	23     color = "red"
//	24
//	25     def make():
//	26         return 1
const belowMethodPyFixture = "import subprocess\n\n\n" +
	"class Runner:\n    retries = 3\n\n    def run(self):\n        subprocess.run([\"git\", \"status\"])\n\n" +
	"    label = \"runner\"\n\n    def stop(self):\n        return 0\n\n\n" +
	"class Plain:\n    def f(self):\n        subprocess.run([\"ls\"])\n    size = 2\n\n\n" +
	"class Deco:\n    color = \"red\"\n\n    def make():\n        return 1\n"

// TestEvalDiffNamesAPythonClassAttributeBelowAMethod proves that a
// changed attribute below a method names its class. The last line of the
// method lies above the change, so it counts for the effects of the
// group, but the method did not change. A removed last line of the
// method is indented deeper, so it still belongs to the method and names
// nothing. A decorator lies outside the span of its method, so a removed
// decorator directly above a method changes that method and names
// nothing either.
func TestEvalDiffNamesAPythonClassAttributeBelowAMethod(t *testing.T) {
	evalBelowMethod(t, "jobs.py", belowMethodPyFixture, []belowMethodCase{
		{
			name: "a replaced attribute below a method and a blank line",
			hunk: "@@ -7,7 +7,7 @@\n     def run(self):\n         subprocess.run([\"git\", \"status\"])\n \n" +
				"-    label = \"worker\"\n+    label = \"runner\"\n \n     def stop(self):\n         return 0\n",
			effects: []string{"proc:exec"},
			notes:   []string{valueNote1("jobs.Runner")},
		},
		{
			name: "a replaced attribute directly below a method",
			hunk: "@@ -16,4 +16,4 @@\n class Plain:\n     def f(self):\n         subprocess.run([\"ls\"])\n" +
				"-    size = 1\n+    size = 2\n",
			effects: []string{"proc:exec"},
			notes:   []string{valueNote1("jobs.Plain")},
		},
		{
			name: "a removed attribute below a method and a blank line",
			hunk: "@@ -7,6 +7,5 @@\n     def run(self):\n         subprocess.run([\"git\", \"status\"])\n \n" +
				"-    mode = \"fast\"\n     label = \"runner\"\n \n",
			effects: []string{"proc:exec"},
			notes:   []string{valueNote1("jobs.Runner")},
		},
		{
			name: "a removed last line of a method",
			hunk: "@@ -7,4 +7,3 @@\n     def run(self):\n         subprocess.run([\"git\", \"status\"])\n" +
				"-        return None\n \n",
			effects: []string{"proc:exec"},
		},
		{
			name: "a removed decorator directly above a method",
			hunk: "@@ -22,6 +22,5 @@\n class Deco:\n     color = \"red\"\n \n" +
				"-    @staticmethod\n     def make():\n         return 1\n",
			effects: []string{},
		},
	})
}

// belowMethodEcmaFixture holds class fields below a method, once after a
// blank line and once directly below the closing line of the method.
//
//	 1 import * as fs from "fs";
//	 2
//	 3 export class Reader {
//	 4 	dump(p: string) {
//	 5 		fs.writeFileSync(p, "");
//	 6 	}
//	 7
//	 8 	label = "reader";
//	 9 }
//	10
//	11 export class Writer {
//	12 	save(p: string) {
//	13 		fs.writeFileSync(p, "");
//	14 	}
//	15 	size = 3;
//	16 }
const belowMethodEcmaFixture = "import * as fs from \"fs\";\n\n" +
	"export class Reader {\n\tdump(p: string) {\n\t\tfs.writeFileSync(p, \"\");\n\t}\n\n\tlabel = \"reader\";\n}\n\n" +
	"export class Writer {\n\tsave(p: string) {\n\t\tfs.writeFileSync(p, \"\");\n\t}\n\tsize = 3;\n}\n"

// TestEvalDiffNamesAnEcmaClassFieldBelowAMethod proves that a changed
// JS/TS class field below a method names its class. The closing line of
// the method lies above the change, so it counts for the effects of the
// group, but the method did not change.
func TestEvalDiffNamesAnEcmaClassFieldBelowAMethod(t *testing.T) {
	evalBelowMethod(t, "fields.ts", belowMethodEcmaFixture, []belowMethodCase{
		{
			name: "a replaced field below a method and a blank line",
			hunk: "@@ -5,5 +5,5 @@\n \t\tfs.writeFileSync(p, \"\");\n \t}\n \n" +
				"-\tlabel = \"Reader\";\n+\tlabel = \"reader\";\n }\n",
			effects: []string{"fs:write"},
			notes:   []string{valueNote1("fields.Reader")},
		},
		{
			name: "a replaced field directly below a method",
			hunk: "@@ -12,5 +12,5 @@\n \tsave(p: string) {\n \t\tfs.writeFileSync(p, \"\");\n \t}\n" +
				"-\tsize = 2;\n+\tsize = 3;\n }\n",
			effects: []string{"fs:write"},
			notes:   []string{valueNote1("fields.Writer")},
		},
	})
}
