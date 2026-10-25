package lessoncheck

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixed object ids for the parser cases. The parser does not check
// them against a repository.
const (
	oidA = "1111111111111111111111111111111111111111"
	oidB = "2222222222222222222222222222222222222222"
	oidC = "3333333333333333333333333333333333333333"
	oidZ = "0000000000000000000000000000000000000000"
)

// TestLessonCheckStatusV2Mapping proves the status v2 record mapping of
// contract K4: the parser on raw -z bytes, the mapping table, and the
// eight K4 test vectors on real repositories (one per rule, §5 row 125).
func TestLessonCheckStatusV2Mapping(t *testing.T) {
	t.Run("the parser keeps paths with spaces and reads every record type", func(t *testing.T) {
		raw := "# branch.oid " + oidA + "\x00" +
			"1 .M N... 100644 100644 100755 " + oidA + " " + oidA + " dir/a file.txt\x00" +
			"2 R. N... 100644 100644 100644 " + oidB + " " + oidB + " R100 new name.txt\x00old name.txt\x00" +
			"u UU N... 100644 100644 100644 100644 " + oidA + " " + oidB + " " + oidC + " both.txt\x00" +
			"? untracked file.txt\x00" +
			"! ignored dir/x.bin\x00"

		records, err := parseStatusV2([]byte(raw))
		require.NoError(t, err)

		assert.Equal(t, []statusRecord{
			{
				kind: '1', xy: ".M", sub: "N...", modeHead: modeFile, modeIndex: modeFile, modeTree: modeExec,
				oidHead: oidA, oidIndex: oidA, path: "dir/a file.txt",
			},
			{
				kind: '2', xy: "R.", sub: "N...", modeHead: modeFile, modeIndex: modeFile, modeTree: modeFile,
				oidHead: oidB, oidIndex: oidB, path: "new name.txt", origPath: "old name.txt",
			},
			{kind: 'u', xy: "UU", sub: "N...", modeTree: modeFile, path: "both.txt"},
			{kind: '?', path: "untracked file.txt"},
			{kind: '!', path: "ignored dir/x.bin"},
		}, records)
	})

	t.Run("an empty output has no records", func(t *testing.T) {
		records, err := parseStatusV2(nil)
		require.NoError(t, err)
		assert.Empty(t, records)
	})

	t.Run("a record that does not parse is an error, never a shorter path set", func(t *testing.T) {
		for name, raw := range map[string]string{
			"short ordinary record":       "1 .M N... 100644\x00",
			"rename without its source":   "2 R. N... 100644 100644 100644 " + oidA + " " + oidA + " R100 b.txt\x00",
			"rename with an empty source": "2 R. N... 100644 100644 100644 " + oidA + " " + oidA + " R100 b.txt\x00\x00",
			"bad submodule field":         "1 .M N.. 100644 100644 100644 " + oidA + " " + oidA + " a.txt\x00",
			"unknown record type":         "x something\x00",
			"empty record in the middle":  "? a.txt\x00\x00? b.txt\x00",
			"untracked without a path":    "?\x00",
		} {
			_, err := parseStatusV2([]byte(raw))
			assert.Error(t, err, name)
		}
	})

	t.Run("the mapping table", func(t *testing.T) {
		ordinary := func(xy, sub, mH, mI, mW, hH, hI, path string) statusRecord {
			return statusRecord{kind: '1', xy: xy, sub: sub, modeHead: mH, modeIndex: mI, modeTree: mW, oidHead: hH, oidIndex: hI, path: path}
		}

		cands := mapRecords([]statusRecord{
			// AD: added to the index, then deleted from the worktree.
			ordinary("AD", "N...", modeAbsent, modeFile, modeAbsent, oidZ, oidA, "ad.txt"),
			// mW=000000 takes the Lstat rule, and its '?' record folds in.
			ordinary("D.", "N...", modeFile, modeAbsent, modeAbsent, oidA, oidZ, "cached.txt"),
			{kind: '?', path: "cached.txt"},
			ordinary("A.", "N...", modeAbsent, modeFile, modeFile, oidZ, oidA, "added.txt"),
			ordinary(".M", "N...", modeFile, modeFile, modeFile, oidA, oidA, "modified.txt"),
			// M.: the worktree equals the index, which differs from HEAD.
			ordinary("M.", "N...", modeFile, modeFile, modeFile, oidA, oidB, "staged.txt"),
			// 2 RD: the destination contributes nothing.
			{kind: '2', xy: "RD", sub: "N...", modeHead: modeFile, modeIndex: modeFile, modeTree: modeAbsent,
				oidHead: oidB, oidIndex: oidB, path: "renamed.txt", origPath: "source.txt"},
			{kind: '2', xy: "R.", sub: "N...", modeHead: modeFile, modeIndex: modeFile, modeTree: modeFile,
				oidHead: oidC, oidIndex: oidC, path: "to.txt", origPath: "from.txt"},
			{kind: 'u', xy: "UU", sub: "N...", modeTree: modeFile, path: "conflict.txt"},
			// A dirty submodule without a new commit is no change.
			ordinary(".M", "S.M.", modeGitlink, modeGitlink, modeGitlink, oidA, oidA, "dirty-sub"),
			ordinary(".M", "SC..", modeGitlink, modeGitlink, modeGitlink, oidA, oidA, "moved-sub"),
			ordinary("M.", "S...", modeGitlink, modeGitlink, modeGitlink, oidA, oidB, "staged-sub"),
			// git prints a nested repository as one "dir/" record.
			{kind: '?', path: "nested/"},
			{kind: '!', path: "ignored.bin"},
		})

		byPath := map[string]*candidate{}
		for _, c := range cands {
			require.NotContains(t, byPath, c.path, "a path appears in one candidate at most")
			byPath[c.path] = c
		}

		assert.NotContains(t, byPath, "ad.txt")
		assert.NotContains(t, byPath, "renamed.txt")
		assert.NotContains(t, byPath, "dirty-sub")

		assert.Equal(t, &candidate{path: "cached.txt", kind: ChangeDelete, lstatRule: true, headMode: modeFile, headOid: oidA}, byPath["cached.txt"])
		assert.Equal(t, &candidate{path: "added.txt", kind: ChangeAdd, treeMode: modeFile}, byPath["added.txt"])
		assert.Equal(t, &candidate{path: "modified.txt", kind: ChangeModify, headMode: modeFile, headOid: oidA, treeMode: modeFile, dropIfSame: true}, byPath["modified.txt"])
		assert.Equal(t, &candidate{path: "staged.txt", kind: ChangeModify, headMode: modeFile, headOid: oidA, treeMode: modeFile, dropIfSame: true, gitObserved: true},
			byPath["staged.txt"], "git observed the content difference of a staged change")
		assert.Equal(t, &candidate{path: "source.txt", kind: ChangeDelete, lstatRule: true, headMode: modeFile, headOid: oidB}, byPath["source.txt"])
		assert.Equal(t, &candidate{path: "to.txt", oldPath: "from.txt", kind: ChangeMove, move: MoveGitRename, headMode: modeFile, headOid: oidC, treeMode: modeFile}, byPath["to.txt"])
		assert.Equal(t, &candidate{path: "conflict.txt", kind: ChangeModify, treeMode: modeFile, needsHead: true, limitations: []string{LimitationUnmerged}}, byPath["conflict.txt"])
		assert.Equal(t, &candidate{path: "moved-sub", kind: ChangeModify, headMode: modeGitlink, headOid: oidA, treeMode: modeGitlink}, byPath["moved-sub"],
			"a new submodule commit has no known worktree id")
		assert.Equal(t, &candidate{path: "staged-sub", kind: ChangeModify, headMode: modeGitlink, headOid: oidA, treeMode: modeGitlink, treeOid: oidB}, byPath["staged-sub"])
		assert.Equal(t, &candidate{path: "nested", kind: ChangeAdd, untracked: true}, byPath["nested"])
		assert.Equal(t, &candidate{path: "ignored.bin", kind: ChangeAdd, untracked: true}, byPath["ignored.bin"])
	})

	t.Run("vector: git rm --cached with the file unchanged on disk is no change", func(t *testing.T) {
		g := cachedRemoval(t)

		assert.Empty(t, changeList(g.snapshot()))
		assert.Empty(t, changeList(g.snapshot("f.txt")))
	})

	t.Run("vector: git rm --cached with the file edited is a modify", func(t *testing.T) {
		g := cachedRemoval(t)
		g.write("f.txt", "one\nedited\n")

		ch := changeAt(t, g.snapshot(), "f.txt")
		assert.Equal(t, ChangeModify, ch.Kind)
		assert.True(t, ch.ContentChanged)
		assert.False(t, ch.ModeChanged)
		assert.False(t, ch.TypeChanged)
		assert.Equal(t, ContentAvailable, ch.Before.Content, "the checker needs the HEAD content of a changed file")
		assert.Equal(t, "one\n", string(ch.Before.text))
		assert.Equal(t, "one\nedited\n", string(ch.After.text))
	})

	t.Run("vector: git rm --cached with chmod +x and the same content is a mode change", func(t *testing.T) {
		g := cachedRemoval(t)
		require.NoError(t, os.Chmod(g.path("f.txt"), 0o755))

		ch := changeAt(t, g.snapshot(), "f.txt")
		assert.Equal(t, ChangeModify, ch.Kind)
		assert.False(t, ch.ContentChanged)
		assert.True(t, ch.ModeChanged)
		assert.Equal(t, modeFile, ch.Before.Mode)
		assert.Equal(t, modeExec, ch.After.Mode)
	})

	t.Run("vector: git rm --cached of an excluded file without a ? record is no change", func(t *testing.T) {
		g := cachedRemoval(t)
		g.write(".git/info/exclude", "f.txt\n")

		require.NotContains(t, g.git("status", "--porcelain=v2", "--untracked-files=all"), "? f.txt", "the fixture must have no ? record")
		assert.Empty(t, changeList(g.snapshot()))
	})

	t.Run("vector: git rm --cached, then rm, is a delete", func(t *testing.T) {
		g := cachedRemoval(t)
		g.remove("f.txt")

		ch := changeAt(t, g.snapshot(), "f.txt")
		assert.Equal(t, ChangeDelete, ch.Kind)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: g.blobID("f.txt"), Size: 4, Content: ContentNotRead}, ch.Before)
		assert.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, ch.After)
	})

	t.Run("vector: 1 MM reverted in the worktree is dropped, and a forbid pin on --file reads not-applicable", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("f.txt", "one\n")
		g.commitAll("f")
		g.write("f.txt", "staged\n")
		g.git("add", "f.txt")
		g.write("f.txt", "one\n")

		require.Contains(t, g.git("status", "--porcelain=v2"), "1 MM ", "the fixture must be a 1 MM record")

		assert.Empty(t, changeList(g.snapshot()))

		snap := g.snapshot("f.txt")
		assert.Empty(t, changeList(snap))

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "f.txt"), snap)
		assert.Equal(t, OutcomeNotApplicable, result.Outcome)
	})

	t.Run("vector: 2 RD is a delete of the source path only", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "alpha\n")
		g.commitAll("a")
		g.git("mv", "a.txt", "b.txt")
		g.remove("b.txt")

		require.Contains(t, g.git("status", "--porcelain=v2"), "2 RD ", "the fixture must be a 2 RD record")

		snap := g.snapshot()
		assert.Equal(t, []string{"delete a.txt"}, changeList(snap))
	})

	t.Run("an unmerged path is a modify against the HEAD side that ls-tree gives", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("c.txt", "base\n")
		g.commitAll("base")
		g.git("checkout", "-q", "-b", "side")
		g.write("c.txt", "side\n")
		g.commitAll("side")
		g.git("checkout", "-q", "main")
		g.write("c.txt", "main\n")
		g.commitAll("main")

		cmd := exec.Command("git", "merge", "-q", "side")
		cmd.Dir = g.root
		require.Error(t, cmd.Run(), "the merge must stop on the conflict")
		require.Contains(t, g.git("status", "--porcelain=v2"), "u UU ", "the fixture must be a u record")

		snap := g.snapshot()
		require.Equal(t, []string{"modify c.txt"}, changeList(snap))

		ch := snap.Changes[0]
		assert.Equal(t, []string{LimitationUnmerged}, ch.Limitations)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: g.blobID("c.txt"), SHA256: sha256Hex("main\n"), Size: 5,
			Content: ContentAvailable, text: []byte("main\n")}, ch.Before)
		assert.Contains(t, string(ch.After.text), "<<<<<<<")
		assert.True(t, ch.ContentChanged)
	})

	t.Run("vector: --file of an ignored directory adds the files below it", func(t *testing.T) {
		g := newGitRepo(t)
		g.write(".gitignore", "d/\n")
		g.commitAll("ignore")
		g.write("d/x.bin", "x\x00y")

		snap := g.snapshot("d")
		assert.Equal(t, []string{"add d/x.bin"}, changeList(snap))
		assert.Equal(t, ContentBinary, snap.Changes[0].After.Content)

		assert.Empty(t, changeList(g.snapshot()), "the default selection leaves ignored files out")
	})
}

// cachedRemoval is a repository with f.txt committed and then removed
// from the index with `git rm --cached`, so status prints a record with
// mW=000000 for it (contract K4, Lstat rule).
func cachedRemoval(t *testing.T) *gitRepo {
	t.Helper()

	g := newGitRepo(t)
	g.write("f.txt", "one\n")
	g.write("other.txt", "other\n")
	g.commitAll("f")
	g.git("rm", "-q", "--cached", "f.txt")

	status := g.git("status", "--porcelain=v2")
	require.True(t, strings.Contains(status, "1 D. N... 100644 000000 000000"), "the fixture must be a D. record: %s", status)

	return g
}
