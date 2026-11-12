package gate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeFile is main.go of probeFixture. b reaches infra:mutate through
// exec. c has no effect, and its doc comment lies outside its span.
//
//	 1 package main
//	 2
//	 3 import "os/exec"
//	 4
//	 5 func b() {
//	 6 	exec.Command("kubectl", "apply").Run()
//	 7 	_ = 1
//	 8 }
//	 9
//	10 // c does nothing.
//	11 func c() {}
const probeFile = "package main\n\nimport \"os/exec\"\n\nfunc b() {\n" +
	"\texec.Command(\"kubectl\", \"apply\").Run()\n\t_ = 1\n}\n\n// c does nothing.\nfunc c() {}\n"

// probeHeader starts a diff record of main.go.
const probeHeader = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n"

// probeNote is the unknown-reach note for main.go alone.
const probeNote = "1 of 1 changed files have changes outside any indexed symbol (main.go) — " +
	"their effect reach is unknown, not clean"

// probeFixture indexes probeFile and loads the default policy. It
// returns a function that evaluates one diff against them.
func probeFixture(t *testing.T) func(diff string) *Decision {
	t.Helper()

	st, root := indexedWorkspace(t, map[string]string{
		"go.mod":  "module example.com/cx\n",
		"main.go": probeFile,
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

func TestEvalDiffIgnoresContextLines(t *testing.T) {
	eval := probeFixture(t)

	// A doc-comment edit of c with git's 3 context lines. The context
	// holds the end of b, which reaches infra:mutate.
	d := eval(probeHeader + "@@ -7,5 +7,5 @@\n" +
		" \t_ = 1\n }\n \n-// c does something.\n+// c does nothing.\n func c() {}\n")

	assert.Equal(t, VerdictAllow, d.Verdict)
	assert.Empty(t, d.Effects, "context lines are not part of the change")
	assert.Equal(t, []string{probeNote}, d.Notes)
}

func TestEvalDiffIgnoresContextOfAnImportEdit(t *testing.T) {
	eval := probeFixture(t)

	// An import edit with full context: the context reaches into b.
	d := eval(probeHeader + "@@ -1,6 +1,6 @@\n" +
		" package main\n \n-import \"os\"\n+import \"os/exec\"\n \n func b() {\n" +
		" \texec.Command(\"kubectl\", \"apply\").Run()\n")

	assert.Empty(t, d.Effects)
	assert.Equal(t, []string{probeNote}, d.Notes, "an import edit keeps its reach unknown")
}

func TestEvalDiffWalksHunkBodies(t *testing.T) {
	eval := probeFixture(t)

	infra := []string{"infra:mutate", "proc:exec"}

	cases := []struct {
		name    string
		diff    string
		effects []string
		notes   []string
	}{
		{
			name: "cut inside a body",
			diff: probeHeader + "@@ -4,7 +4,6 @@\n" +
				" \n func b() {\n \texec.Command(\"kubectl\", \"apply\").Run()\n-\t_ = 2\n \t_ = 1\n }\n \n",
			effects: infra,
		},
		{
			name:    "cut inside a body with no context",
			diff:    probeHeader + "@@ -7,1 +6,0 @@\n-\t_ = 2\n",
			effects: infra,
		},
		{
			name:    "cut of a blank line between functions",
			diff:    probeHeader + "@@ -10,1 +9,0 @@\n-\n",
			effects: []string{},
			notes:   []string{probeNote},
		},
		{
			name: "two hunks, the second adds a line inside b",
			diff: probeHeader +
				"@@ -1,1 +1,1 @@\n-package  main\n+package main\n" +
				"@@ -6,2 +6,2 @@\n \texec.Command(\"kubectl\", \"apply\").Run()\n-\t_ = 2\n+\t_ = 1\n",
			effects: infra,
		},
		{
			name: "two hunks, only the context of the second lies in b",
			diff: probeHeader +
				"@@ -1,1 +1,1 @@\n-package  main\n+package main\n" +
				"@@ -8,4 +8,4 @@\n }\n \n // c does nothing.\n-func c() { }\n+func c() {}\n",
			effects: []string{},
		},
	}

	for _, c := range cases {
		d := eval(c.diff)

		effects := d.Effects
		if effects == nil {
			effects = []string{}
		}

		assert.Equal(t, c.effects, effects, c.name)
		assert.Equal(t, c.notes, d.Notes, c.name)
	}
}

func TestEvalDiffReadsNoHeaderInsideAHunk(t *testing.T) {
	eval := probeFixture(t)

	// The body lines remove "-- old.go" and add "++ phantom.go" and
	// "++ /dev/null". They look like file headers but are body lines.
	diff := "diff --git a/notes.txt b/notes.txt\n--- a/notes.txt\n+++ b/notes.txt\n" +
		"@@ -1,2 +1,3 @@\n keep\n--- old.go\n+++ phantom.go\n++++ /dev/null\n"

	d := eval(diff)

	assert.Equal(t, []string{"1 of 1 changed files have changes outside any indexed symbol (notes.txt) — " +
		"their effect reach is unknown, not clean"}, d.Notes)
	assert.Equal(t, []string{"notes.txt"}, ChangedPaths(diff))

	// A hunk with no file header still walks its body. Its added line
	// "++ b/phantom.go" registers no file, so the input names no file.
	assert.Empty(t, ChangedPaths("@@ -1,1 +1,2 @@\n-a\n+++ b/phantom.go\n+b\n"))
}

func TestEvalDiffKeepsTheNextFileOfAPlainDiff(t *testing.T) {
	eval := probeFixture(t)

	// A hand-made diff with no diff --git line. The notes.txt hunk counts
	// one context line too many on both sides. The edit of b in main.go
	// must still reach its effects.
	d := eval("--- a/notes.txt\n+++ b/notes.txt\n@@ -1,3 +1,4 @@\n line one\n+added\n line two\n" +
		"--- a/main.go\n+++ b/main.go\n@@ -6,1 +6,1 @@\n" +
		"-\texec.Command(\"kubectl\").Run()\n+\texec.Command(\"kubectl\", \"apply\").Run()\n")

	assert.Equal(t, []string{"infra:mutate", "proc:exec"}, d.Effects)
	assert.Equal(t, []string{"1 of 2 changed files have changes outside any indexed symbol (notes.txt) — " +
		"their effect reach is unknown, not clean"}, d.Notes)
}

// pyFile is app.py of pyFixture, the new side of an edit. The edit
// removes a guard at the end of b and adds d below b.
//
//	1 import subprocess
//	2
//	3 def b():
//	4     subprocess.run(["kubectl", "apply"])
//	5
//	6
//	7 def d():
//	8     pass
const pyFile = "import subprocess\n\ndef b():\n    subprocess.run([\"kubectl\", \"apply\"])\n" +
	"\n\ndef d():\n    pass\n"

// pyToolFile is tool.py of pyFixture. A symbol with no effect starts
// on line 1, and deploy reaches proc:exec.
const pyToolFile = "def first():\n    return 1\n\n\nimport subprocess\n\n\n" +
	"def deploy():\n    subprocess.run([\"kubectl\", \"apply\"])\n"

// pyTailFile is tail.py of pyFixture, the new side of an edit. The edit
// replaces the guard below the blank line at the end of b with d. The
// span of b ends at line 6, so line 7 lies outside b.
//
//	 1 import os
//	 2 import subprocess
//	 3
//	 4
//	 5 def b():
//	 6     subprocess.run(["kubectl", "apply"])
//	 7
//	 8
//	 9 def d():
//	10     pass
//	11
//	12
//	13 def c():
//	14     pass
const pyTailFile = "import os\nimport subprocess\n\n\ndef b():\n    subprocess.run([\"kubectl\", \"apply\"])\n" +
	"\n\ndef d():\n    pass\n\n\ndef c():\n    pass\n"

// pyCutFile is cut.py of pyFixture, the new side of an edit. The edit
// deletes the guard and the blank line at the end of b and changes the
// body of c.
//
//	 1 import os
//	 2 import subprocess
//	 3
//	 4
//	 5 def b():
//	 6     subprocess.run(["kubectl", "apply"])
//	 7
//	 8
//	 9 def c():
//	10     return 1
const pyCutFile = "import os\nimport subprocess\n\n\ndef b():\n    subprocess.run([\"kubectl\", \"apply\"])\n" +
	"\n\ndef c():\n    return 1\n"

// pyFixture indexes the Python files and loads the default policy. It
// returns a function that evaluates one diff against them.
func pyFixture(t *testing.T) func(diff string) *Decision {
	t.Helper()

	st, root := indexedWorkspace(t, map[string]string{
		"app.py":  pyFile,
		"tool.py": pyToolFile,
		"tail.py": pyTailFile,
		"cut.py":  pyCutFile,
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

func TestEvalDiffAttributesAReplacedPythonTail(t *testing.T) {
	eval := pyFixture(t)

	// The removed guard was the end of b. The added lines lie below b,
	// and a Python function has no closing line inside the hunk. So the
	// line above the removed lines must count, or b is not touched.
	d := eval("diff --git a/app.py b/app.py\n--- a/app.py\n+++ b/app.py\n@@ -2,5 +2,7 @@\n" +
		" \n def b():\n     subprocess.run([\"kubectl\", \"apply\"])\n" +
		"-    if not approved():\n-        return\n+\n+\n+def d():\n+    pass\n")

	assert.Equal(t, []string{"proc:exec"}, d.Effects)
	assert.Empty(t, d.Notes)
}

func TestEvalDiffAttributesAPythonTailBelowABlankLine(t *testing.T) {
	eval := pyFixture(t)

	// Git writes these hunks for the two edits. In both, the line
	// directly above the removed guard is a blank line outside the span
	// of b. Another symbol of the file is touched, so the file has no
	// unknown-reach note. Only the indented guard can attribute b.
	cases := []struct {
		name string
		diff string
	}{
		{
			name: "replaced guard",
			diff: "diff --git a/tail.py b/tail.py\n--- a/tail.py\n+++ b/tail.py\n" +
				"@@ -5,8 +5,9 @@ import subprocess\n" +
				" def b():\n     subprocess.run([\"kubectl\", \"apply\"])\n \n" +
				"-    if not os.environ.get(\"APPROVED\"):\n-        return\n" +
				"+\n+def d():\n+    pass\n \n \n def c():\n",
		},
		{
			name: "deleted guard and an edit of c",
			diff: "diff --git a/cut.py b/cut.py\n--- a/cut.py\n+++ b/cut.py\n" +
				"@@ -5,9 +5,6 @@ import subprocess\n" +
				" def b():\n     subprocess.run([\"kubectl\", \"apply\"])\n \n" +
				"-    if not os.environ.get(\"APPROVED\"):\n-        return\n-\n" +
				" \n def c():\n-    pass\n+    return 1\n",
		},
	}

	for _, c := range cases {
		d := eval(c.diff)

		assert.Equal(t, []string{"proc:exec"}, d.Effects, c.name)
		assert.Empty(t, d.Notes, c.name)
	}
}

func TestEvalDiffDeletionOfAFileStillInTheTree(t *testing.T) {
	eval := pyFixture(t)

	// A saved or planned deletion of tool.py, while the file is still in
	// the tree. The new side has no line, so no symbol of the old file
	// counts as touched, and the reach of the file stays unknown.
	d := eval("diff --git a/tool.py b/tool.py\ndeleted file mode 100644\n" +
		"--- a/tool.py\n+++ /dev/null\n@@ -1,9 +0,0 @@\n" +
		"-def first():\n-    return 1\n-\n-\n-import subprocess\n-\n-\n" +
		"-def deploy():\n-    subprocess.run([\"kubectl\", \"apply\"])\n")

	assert.Empty(t, d.Effects)
	assert.Equal(t, []string{"1 of 1 changed files have changes outside any indexed symbol (tool.py) — " +
		"their effect reach is unknown, not clean"}, d.Notes)
}

func TestParseDiffChangedLines(t *testing.T) {
	// next is a second record. Each case must leave it readable, which
	// shows that the hunk before it ended at the right line.
	const next = "diff --git a/z.go b/z.go\n--- a/z.go\n+++ b/z.go\n@@ -1 +1 @@\n-x\n+y\n"

	cases := []struct {
		name string
		diff string
		want []uint32
	}{
		{
			name: "replacement records the line above and the added lines",
			diff: "@@ -3,3 +3,3 @@\n a\n-b\n+c\n d\n",
			want: []uint32{3, 4},
		},
		{
			name: "replacement at the first line records the added lines",
			diff: "@@ -1,2 +1,2 @@\n-a\n+b\n c\n",
			want: []uint32{1},
		},
		{
			name: "addition records the added lines only",
			diff: "@@ -3,2 +3,3 @@\n a\n+b\n c\n",
			want: []uint32{4},
		},
		{
			name: "cut records both neighbours",
			diff: "@@ -3,3 +3,2 @@\n a\n-b\n d\n",
			want: []uint32{3, 4},
		},
		{
			name: "cut with no context",
			diff: "@@ -6,1 +5,0 @@\n-x\n",
			want: []uint32{5, 6},
		},
		{
			name: "cut before the first line records nothing",
			diff: "@@ -1,1 +0,0 @@\n-x\n",
			want: nil,
		},
		{
			name: "no newline on the old side",
			diff: "@@ -3 +3 @@\n-old\n\\ No newline at end of file\n+new\n",
			want: []uint32{2, 3},
		},
		{
			name: "no newline on the new side",
			diff: "@@ -3 +3 @@\n-old\n+new\n\\ No newline at end of file\n",
			want: []uint32{2, 3},
		},
		{
			name: "no newline on both sides",
			diff: "@@ -3 +3 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n",
			want: []uint32{2, 3},
		},
		{
			name: "empty context line keeps later lines in place",
			diff: "@@ -1,3 +1,3 @@\n a\n\n-b\n+c\n",
			want: []uint32{2, 3},
		},
		{
			name: "two hunks",
			diff: "@@ -2,2 +2,2 @@\n a\n-b\n+c\n@@ -20,2 +20,3 @@\n d\n+e\n f\n",
			want: []uint32{2, 3, 21},
		},
		{
			name: "an added line after the new count ends the hunk",
			diff: "@@ -1,2 +1,1 @@\n-a\n+b\n+c\n",
			want: []uint32{1},
		},
		{
			name: "a removed line after the old count ends the hunk",
			diff: "@@ -1,1 +1,2 @@\n-a\n+b\n-c\n+d\n",
			want: []uint32{1},
		},
		{
			name: "a context line after the old count ends the hunk",
			diff: "@@ -1,1 +1,3 @@\n-a\n+b\n c\n+d\n",
			want: []uint32{1},
		},
		{
			name: "a truncated hunk records its open cut",
			diff: "@@ -1,5 +1,5 @@\n-a\n",
			want: []uint32{1},
		},
		{
			name: "indented replacement below a blank line records the text line above",
			diff: "@@ -3,3 +3,3 @@\n a\n \n-\tb\n+\tc\n",
			want: []uint32{3, 4, 5},
		},
		{
			name: "indented cut below a blank line records the text line above",
			diff: "@@ -3,4 +3,3 @@\n a\n \n-\tb\n d\n",
			want: []uint32{3, 4, 5},
		},
		{
			name: "a removed blank line does not hide the indentation of the run",
			diff: "@@ -3,5 +3,3 @@\n a\n \n-\n-\tb\n d\n",
			want: []uint32{3, 4, 5},
		},
		{
			name: "a cut that starts at column 0 does not look past a blank line",
			diff: "@@ -3,4 +3,3 @@\n a\n \n-b\n d\n",
			want: []uint32{4, 5},
		},
		{
			name: "an indented cut looks for text only inside its hunk",
			diff: "@@ -1,1 +1,1 @@\n-a\n+b\n@@ -10,2 +10,1 @@\n \n-\tc\n",
			want: []uint32{1, 10, 11},
		},
	}

	for _, c := range cases {
		got := parseDiff("diff --git a/m.go b/m.go\n--- a/m.go\n+++ b/m.go\n" + c.diff + next)

		assert.Equal(t, []string{"m.go", "z.go"}, got.paths, c.name)
		assert.Equal(t, c.want, got.lines["m.go"], c.name)
		assert.Equal(t, []uint32{1}, got.lines["z.go"], c.name)
	}

	// The last hunk can end at the end of the input, with no newline.
	got := parseDiff("--- a/m.go\n+++ b/m.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c")
	assert.Equal(t, []uint32{1, 2}, got.lines["m.go"])

	// A truncated last hunk still records its open cut at the end of the
	// input.
	got = parseDiff("--- a/m.go\n+++ b/m.go\n@@ -1,5 +1,5 @@\n a\n-b")
	assert.Equal(t, []uint32{1, 2}, got.lines["m.go"])
}

func TestParseDiffEndsAHunkAtAFileHeader(t *testing.T) {
	const m = "--- a/m.go\n+++ b/m.go\n"

	cases := []struct {
		name  string
		diff  string
		paths []string
		lines map[string][]uint32
	}{
		{
			name:  "a +++ line after the new count is a header",
			diff:  m + "@@ -1,2 +1,1 @@\n-a\n+b\n+++ b/x.go\n-c\n",
			paths: []string{"m.go", "x.go"},
			lines: map[string][]uint32{"m.go": {1}, "x.go": nil},
		},
		{
			name:  "a --- line after the old count is a header",
			diff:  m + "@@ -1,1 +1,2 @@\n-a\n+b\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"m.go", "x.go"},
			lines: map[string][]uint32{"m.go": {1}, "x.go": {1}},
		},
		{
			// A hand-made diff with no diff --git line and a count that is
			// larger than its body. Git never writes a hunk header inside
			// a hunk, so the three header lines start the next record.
			name:  "a count larger than the body keeps the next record",
			diff:  m + "@@ -1,5 +1,6 @@\n-a\n+b\n+c\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"m.go", "x.go"},
			lines: map[string][]uint32{"m.go": {1, 2}, "x.go": {1}},
		},
		{
			// Git output: the hunk ends with a removed "-- x" line and an
			// added "++ y" line, and a second hunk of m.go follows.
			name:  "removed and added lines that look like headers can end a hunk",
			diff:  "diff --git a/m.go b/m.go\n" + m + "@@ -1,2 +1,2 @@\n a\n--- x\n+++ y\n@@ -10 +10 @@\n-p\n+q\n",
			paths: []string{"m.go"},
			lines: map[string][]uint32{"m.go": {1, 2, 9, 10}},
		},
		{
			// A hand-made diff with no diff --git line that counts one
			// context line too many on both sides. Both counts have 1 left
			// when the header lines of the next record come.
			name:  "a plain diff with both counts one too large keeps the next record",
			diff:  m + "@@ -1,3 +1,4 @@\n a\n+b\n c\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"m.go", "x.go"},
			lines: map[string][]uint32{"m.go": {2}, "x.go": {1}},
		},
		{
			// A git record, then hand-made records with no diff --git line.
			// The git record must not let its exception reach them.
			name: "a plain record after a git record keeps the next record",
			diff: "diff --git a/m.go b/m.go\n" + m + "@@ -1 +1 @@\n-a\n+b\n" +
				"--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,4 @@\n a\n+b\n c\n" +
				"--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"m.go", "x.go", "y.go"},
			lines: map[string][]uint32{"m.go": {1}, "x.go": {2}, "y.go": {1}},
		},
		{
			// The git record has a hunk but no "---" line. The hunk ends
			// its header, so the "---" line after it starts a plain record.
			name: "a plain record after a git hunk with no --- line keeps the next record",
			diff: "diff --git a/m.go b/m.go\n@@ -1 +1 @@\n-a\n+b\n" +
				"--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,4 @@\n a\n+b\n c\n" +
				"--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"m.go", "x.go", "y.go"},
			lines: map[string][]uint32{"m.go": {1}, "x.go": {2}, "y.go": {1}},
		},
	}

	for _, c := range cases {
		got := parseDiff(c.diff)

		assert.Equal(t, c.paths, got.paths, c.name)
		assert.Equal(t, c.lines, got.lines, c.name)
	}
}

func TestParseDiffRegistersTheOldPathOfARename(t *testing.T) {
	cases := []struct {
		name  string
		diff  string
		paths []string
		lines map[string][]uint32
	}{
		{
			name:  "a rename with no hunk registers both paths",
			diff:  "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			paths: []string{"new.go", "old.go"},
			lines: map[string][]uint32{"new.go": nil, "old.go": nil},
		},
		{
			name: "the hunk lines of a rename stay on the new path",
			diff: "diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n" +
				"--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"new.go", "old.go"},
			lines: map[string][]uint32{"new.go": {1}, "old.go": nil},
		},
		{
			// The rename lines carry no a/ or b/ side prefix, so a top
			// directory named a keeps its name.
			name:  "an old path under a directory named a keeps that directory",
			diff:  "diff --git a/a/x.go b/a/y.go\nsimilarity index 100%\nrename from a/x.go\nrename to a/y.go\n",
			paths: []string{"a/y.go", "a/x.go"},
			lines: map[string][]uint32{"a/y.go": nil, "a/x.go": nil},
		},
		{
			// An agent can send a hand-made diff with no rename to line and
			// no file headers. The hunk then belongs to the diff --git path.
			name:  "a hunk after a lone rename from line stays on the new path",
			diff:  "diff --git a/old.go b/new.go\nrename from old.go\n@@ -1 +1 @@\n-x\n+y\n",
			paths: []string{"new.go", "old.go"},
			lines: map[string][]uint32{"new.go": {1}, "old.go": nil},
		},
		{
			name: "a C-quoted old path unwraps",
			diff: "diff --git \"a/pkg/\\303\\251.go\" b/pkg/e.go\nsimilarity index 100%\n" +
				"rename from \"pkg/\\303\\251.go\"\nrename to pkg/e.go\n",
			paths: []string{"pkg/e.go", "pkg/é.go"},
			lines: map[string][]uint32{"pkg/e.go": nil, "pkg/é.go": nil},
		},
		{
			name:  "a rename from line with no value registers no path",
			diff:  "diff --git a/old.go b/new.go\nrename from \nrename to new.go\n",
			paths: []string{"new.go"},
			lines: map[string][]uint32{"new.go": nil},
		},
		{
			// A copy keeps its source file, so only the target changes.
			name:  "a copy registers only its target",
			diff:  "diff --git a/a.go b/b.go\nsimilarity index 100%\ncopy from a.go\ncopy to b.go\n",
			paths: []string{"b.go"},
			lines: map[string][]uint32{"b.go": nil},
		},
	}

	for _, c := range cases {
		got := parseDiff(c.diff)

		assert.Equal(t, c.paths, got.paths, c.name)
		assert.Equal(t, c.lines, got.lines, c.name)
	}
}

func TestParseDiffRegistersTheRenameToPath(t *testing.T) {
	// Git writes this record for git mv "dir b/x.go" "dir b/y.go". The
	// diff --git line cannot show where a new path with " b/" starts.
	// So only the rename lines give both full paths.
	got := parseDiff("diff --git a/dir b/x.go b/dir b/y.go\nsimilarity index 100%\n" +
		"rename from dir b/x.go\nrename to dir b/y.go\n")

	assert.Contains(t, got.paths, "dir b/y.go", "the rename to line registers the new path")
	assert.Contains(t, got.paths, "dir b/x.go", "the rename from line registers the old path")
}
