package lessoncheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixed selection texts of contract K4, written out here so that a
// change of the constants in worktree.go fails the tests.
const (
	wantDefaultHead   = "tracked changes against HEAD plus untracked nonignored files, staged and unstaged, as present in the working tree"
	wantDefaultEmpty  = "tracked changes against the empty tree plus untracked nonignored files, staged and unstaged, as present in the working tree"
	wantExplicitHead  = "the paths named by --file, against HEAD"
	wantExplicitEmpty = "the paths named by --file, against the empty tree"
)

// The fixed exclude texts of contract K4.
var (
	wantDefaultExcludes  = []string{"ignored files not named by --file", "writes outside this snapshot", "content inside submodules"}
	wantExplicitExcludes = []string{"paths not named by --file", "writes outside this snapshot"}
)

// wantSkippedExclude is the fixed exclude text that a snapshot adds after
// the texts of its kind when the selection holds an index entry with the
// skip-worktree or assume-unchanged bit (manager ruling S2).
const wantSkippedExclude = "worktree changes of skip-worktree or assume-unchanged index entries, such as paths outside a sparse checkout"

// TestLessonCheckSnapshotWorkingTree proves the default selection of
// contract K4 on one repository with every kind of change, the side
// fields, the JSON shape, the named limits, and the K4 errors (AC-29).
func TestLessonCheckSnapshotWorkingTree(t *testing.T) {
	g := newGitRepo(t)
	g.write(".gitignore", "*.log\n")
	g.write("keep.txt", "keep\n")
	g.write("mod.txt", "one\n")
	g.write("staged.txt", "s1\n")
	g.write("del.txt", "gone\n")
	g.write("old.txt", "moving content\n")
	g.write("run.sh", "echo hi\n")
	g.commitAll("base")

	commit := g.head()
	oids := map[string]string{}

	for _, p := range []string{"mod.txt", "staged.txt", "del.txt", "old.txt", "run.sh"} {
		oids[p] = g.blobID(p)
	}

	g.write("mod.txt", "one\ntwo\n")
	g.write("staged.txt", "s2\n")
	g.git("add", "staged.txt")
	g.remove("del.txt")
	g.git("mv", "old.txt", "new.txt")
	require.NoError(t, os.Chmod(g.path("run.sh"), 0o755))
	g.write("add.txt", "added\n")
	g.write(".seamark/notes.txt", "seamark state\n")
	g.write("bin.dat", "a\x00b")
	g.write("build.log", "ignored\n")

	snap := g.snapshot()

	t.Run("baseline, selection, and the change set", func(t *testing.T) {
		assert.Equal(t, Baseline{Kind: BaselineHead, Commit: commit}, snap.Baseline)
		assert.Equal(t, Selection{Kind: SelectionDefault, Description: wantDefaultHead, Excludes: wantDefaultExcludes}, snap.Selection)
		assert.True(t, snap.PathsComplete)
		assert.Nil(t, snap.Limits)
		assert.Nil(t, snap.Limitations)

		// .seamark/ is never excluded; the ignored build.log and the
		// unchanged keep.txt are not changes.
		assert.Equal(t, []string{
			"add .seamark/notes.txt",
			"add add.txt",
			"add bin.dat",
			"delete del.txt",
			"modify mod.txt",
			"move old.txt->new.txt",
			"modify run.sh",
			"modify staged.txt",
		}, changeList(snap))
	})

	t.Run("a content change reads both contents", func(t *testing.T) {
		ch := changeAt(t, snap, "mod.txt")

		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: oids["mod.txt"], SHA256: sha256Hex("one\n"), Size: 4,
			Content: ContentAvailable, text: []byte("one\n")}, ch.Before)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, SHA256: sha256Hex("one\ntwo\n"), Size: 8,
			Content: ContentAvailable, text: []byte("one\ntwo\n")}, ch.After)
		assert.True(t, ch.ContentChanged)
		assert.False(t, ch.ModeChanged)
		assert.False(t, ch.TypeChanged)
	})

	t.Run("a staged change shows the worktree content", func(t *testing.T) {
		ch := changeAt(t, snap, "staged.txt")

		assert.Equal(t, "s1\n", string(ch.Before.text))
		assert.Equal(t, "s2\n", string(ch.After.text))
	})

	t.Run("a staged rename is a git-rename move without a content read of HEAD", func(t *testing.T) {
		ch := changeAt(t, snap, "new.txt")

		assert.Equal(t, MoveGitRename, ch.Move)
		assert.Equal(t, "old.txt", ch.OldPath)
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: oids["old.txt"], SHA256: sha256Hex("moving content\n"), Size: 15,
			Content: ContentNotRead}, ch.Before)
		assert.Equal(t, sha256Hex("moving content\n"), ch.After.SHA256)
	})

	t.Run("a mode change keeps the content flag false", func(t *testing.T) {
		ch := changeAt(t, snap, "run.sh")

		assert.True(t, ch.ModeChanged)
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, modeFile, ch.Before.Mode)
		assert.Equal(t, modeExec, ch.After.Mode)
		assert.Equal(t, int64(8), ch.Before.Size)
	})

	t.Run("a delete and a binary add", func(t *testing.T) {
		del := changeAt(t, snap, "del.txt")
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: oids["del.txt"], Size: 5, Content: ContentNotRead}, del.Before)
		assert.True(t, del.ContentChanged)

		bin := changeAt(t, snap, "bin.dat")
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, SHA256: sha256Hex("a\x00b"), Size: 3, Content: ContentBinary}, bin.After)
	})

	t.Run("the JSON never carries content", func(t *testing.T) {
		data, err := json.Marshal(snap)
		require.NoError(t, err)

		assert.NotContains(t, string(data), "moving content")
		assert.NotContains(t, string(data), "seamark state")
		assert.Contains(t, string(data), `"schema":"lesson-snapshot/1"`)
		assert.Contains(t, string(data), `"paths_complete":true`)
		assert.Contains(t, string(data), `"snapshot_sha256":"`+snap.SHA256+`"`)
	})

	t.Run("max-snapshot-paths cuts the path set and names the limit", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.write("b.txt", "b\n")
		g.write("c.txt", "c\n")

		lim := DefaultLimits()
		lim.MaxSnapshotPaths = 2

		snap := g.snapshotWith(lim)
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitMaxSnapshotPaths, Value: 2, Paths: 1}}, snap.Limits)
		assert.Equal(t, []string{"add a.txt", "add b.txt"}, changeList(snap), "the cut keeps the first records by path")
	})

	t.Run("max-file-bytes marks both sides too-large with their true size", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("head.txt", "0123456789")
		g.commitAll("head")
		g.write("head.txt", "x\n")
		g.write("big.txt", "0123456789")
		g.write("small.txt", "ok")

		lim := DefaultLimits()
		lim.MaxFileBytes = 5

		snap := g.snapshotWith(lim)
		assert.True(t, snap.PathsComplete, "a content limit never cuts the path set")
		assert.Equal(t, []LimitHit{{Name: LimitMaxFileBytes, Value: 5, Paths: 2}}, snap.Limits)

		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Size: 10, Content: ContentTooLarge}, changeAt(t, snap, "big.txt").After)
		assert.Equal(t, ContentAvailable, changeAt(t, snap, "small.txt").After.Content)

		head := changeAt(t, snap, "head.txt")
		assert.Equal(t, ContentTooLarge, head.Before.Content)
		assert.Equal(t, int64(10), head.Before.Size)
		assert.Equal(t, ContentAvailable, head.After.Content)
	})

	t.Run("max-snapshot-bytes stops the reads after the budget", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "1234\n")
		g.write("b.txt", "5678\n")

		lim := DefaultLimits()
		lim.MaxSnapshotBytes = 6

		snap := g.snapshotWith(lim)
		assert.Equal(t, []LimitHit{{Name: LimitMaxSnapshotBytes, Value: 6, Paths: 1}}, snap.Limits)
		assert.Equal(t, ContentAvailable, changeAt(t, snap, "a.txt").After.Content)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Size: 5, Content: ContentTooLarge}, changeAt(t, snap, "b.txt").After)
	})

	t.Run("max-git-output-bytes on status cuts the path set", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("x.txt", "x\n")
		g.commitAll("x")

		for i := range 5 {
			g.write(fmt.Sprintf("untracked-file-with-a-long-name-%d.txt", i), "u\n")
		}

		lim := DefaultLimits()
		lim.MaxGitOutputBytes = 64

		snap := g.snapshotWith(lim)
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitMaxGitOutputBytes, Value: 64, Paths: 0}}, snap.Limits)
		assert.Empty(t, snap.Changes, "a cut status output is never parsed")
	})

	t.Run("a deadline after step 1 gives a snapshot that names verify-timeout", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("x.txt", "x\n")
		g.commitAll("x")
		g.write("x.txt", "changed\n")

		gitShim(t, `case " $* " in *" status "*) exec sleep 10;; esac`)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		lim := DefaultLimits()

		snap, err := ReadWorkingTree(ctx, g.root, nil, lim)
		require.NoError(t, err)
		requireValidSnapshot(t, snap)

		assert.Equal(t, BaselineHead, snap.Baseline.Kind)
		assert.False(t, snap.PathsComplete)
		assert.Empty(t, snap.Changes)
		assert.Equal(t, []LimitHit{{Name: LimitVerifyTimeout, Value: lim.VerifyTimeout.Milliseconds()}}, snap.Limits)

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", ""), snap)
		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonResourceLimit, result.Reason)
		assert.Equal(t, LimitVerifyTimeout, result.Limit)
	})

	t.Run("a cancellation during the read is an error", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("x.txt", "x\n")
		g.commitAll("x")

		gitShim(t, `case " $* " in *" status "*) exec sleep 10;; esac`)

		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Second, cancel)

		defer timer.Stop()

		snap, err := ReadWorkingTree(ctx, g.root, nil, DefaultLimits())
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "lesson checks: read the working tree: ")
		assert.Nil(t, snap)
	})

	t.Run("a context that ended before step 1 is an error", func(t *testing.T) {
		g := newGitRepo(t)

		canceled, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := ReadWorkingTree(canceled, g.root, nil, DefaultLimits())
		require.ErrorIs(t, err, context.Canceled)

		expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancelExpired()

		_, err = ReadWorkingTree(expired, g.root, nil, DefaultLimits())
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("a directory outside a git work tree is the K4 error", func(t *testing.T) {
		dir := t.TempDir()

		_, err := ReadWorkingTree(context.Background(), dir, nil, DefaultLimits())
		require.EqualError(t, err, fmt.Sprintf("lesson checks: %s is not inside a git work tree", dir))
	})

	t.Run("a path that is not repository-relative is refused", func(t *testing.T) {
		for _, p := range []string{"", ".", "..", "../x", "/abs", "a/../b", "a/", "./a"} {
			_, err := ReadWorkingTree(context.Background(), g.root, []string{p}, DefaultLimits())
			require.Error(t, err, p)
			assert.Contains(t, err.Error(), "is not repository-relative", p)
		}
	})

	t.Run("a zero Limits means the default limits", func(t *testing.T) {
		snap, err := ReadWorkingTree(context.Background(), g.root, nil, Limits{})
		require.NoError(t, err)
		assert.Equal(t, DefaultLimits(), snap.bounds)
		assert.Equal(t, g.snapshot().SHA256, snap.SHA256)
	})
}

// TestLessonCheckSnapshotUnbornRepository proves the empty-tree baseline:
// no commit id, the unborn selection texts, and no ls-tree step.
func TestLessonCheckSnapshotUnbornRepository(t *testing.T) {
	g := newGitRepo(t)
	g.write("a.txt", "a\n")
	g.write("staged.txt", "s\n")
	g.git("add", "staged.txt")
	g.write("gone.txt", "g\n")
	g.git("add", "gone.txt")
	g.remove("gone.txt")
	g.write(".gitignore", "*.bin\n")
	g.write("x.bin", "x\n")

	t.Run("the default selection", func(t *testing.T) {
		snap := g.snapshot()

		assert.Equal(t, Baseline{Kind: BaselineEmptyTree}, snap.Baseline)
		assert.Equal(t, Selection{Kind: SelectionDefault, Description: wantDefaultEmpty, Excludes: wantDefaultExcludes}, snap.Selection)
		assert.True(t, snap.PathsComplete)

		// gone.txt was added to the index and then deleted (AD): no change.
		assert.Equal(t, []string{"add .gitignore", "add a.txt", "add staged.txt"}, changeList(snap))

		staged := changeAt(t, snap, "staged.txt")
		assert.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, staged.Before)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, SHA256: sha256Hex("s\n"), Size: 2, Content: ContentAvailable, text: []byte("s\n")}, staged.After)

		data, err := json.Marshal(snap.Baseline)
		require.NoError(t, err)
		assert.JSONEq(t, `{"kind":"empty-tree"}`, string(data))
	})

	t.Run("an explicit selection, an ignored path included", func(t *testing.T) {
		snap := g.snapshot("x.bin", "a.txt", "missing.txt")

		assert.Equal(t, Selection{
			Kind:        SelectionExplicitPaths,
			Description: wantExplicitEmpty,
			Paths:       []string{"a.txt", "missing.txt", "x.bin"},
			Excludes:    wantExplicitExcludes,
		}, snap.Selection)
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, []string{"add a.txt", "add x.bin"}, changeList(snap))
	})
}

// TestLessonCheckSnapshotExplicitIgnoredPath proves the explicit-paths
// selection: a named ignored path is inspected, a named directory takes
// every ignored file below it (K4 step 3a, K-5, §5 row 18), and an
// unchanged or absent named path is no change (K-1).
func TestLessonCheckSnapshotExplicitIgnoredPath(t *testing.T) {
	g := newGitRepo(t)
	g.write(".gitignore", "ign/\n*.bin\n")
	g.write("keep.txt", "keep\n")
	g.write("src/tracked.txt", "t\n")
	g.commitAll("base")
	g.write("ign/x.bin", "x\n")
	g.write("ign/sub/y.txt", "y\n")
	g.write("top.bin", "top\n")
	g.write("u.txt", "u\n")
	g.write("src/new.txt", "n\n")

	t.Run("the default selection leaves every ignored file out", func(t *testing.T) {
		assert.Equal(t, []string{"add src/new.txt", "add u.txt"}, changeList(g.snapshot()))
	})

	t.Run("a named ignored directory takes every file below it", func(t *testing.T) {
		snap := g.snapshot("ign")

		assert.Equal(t, Selection{
			Kind:        SelectionExplicitPaths,
			Description: wantExplicitHead,
			Paths:       []string{"ign"},
			Excludes:    wantExplicitExcludes,
		}, snap.Selection)
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, []string{"add ign/sub/y.txt", "add ign/x.bin"}, changeList(snap))
	})

	t.Run("a named ignored file is an add", func(t *testing.T) {
		snap := g.snapshot("top.bin")

		assert.Equal(t, []string{"add top.bin"}, changeList(snap))
		assert.Equal(t, "top\n", string(snap.Changes[0].After.text))
	})

	t.Run("a named directory takes its untracked files", func(t *testing.T) {
		assert.Equal(t, []string{"add src/new.txt"}, changeList(g.snapshot("src")))
	})

	t.Run("an unchanged or absent named path is no change", func(t *testing.T) {
		snap := g.snapshot("keep.txt", "missing.txt")

		assert.Empty(t, changeList(snap))
		assert.True(t, snap.PathsComplete)
	})

	t.Run("several paths are sorted and deduplicated", func(t *testing.T) {
		snap := g.snapshot("u.txt", "ign/x.bin", "u.txt")

		assert.Equal(t, []string{"ign/x.bin", "u.txt"}, snap.Selection.Paths)
		assert.Equal(t, []string{"add ign/x.bin", "add u.txt"}, changeList(snap))
	})

	t.Run("a name that holds pattern characters is a literal path", func(t *testing.T) {
		g.write("star*.bin", "s\n")
		g.write("starX.bin", "x\n")

		assert.Equal(t, []string{"add star*.bin"}, changeList(g.snapshot("star*.bin")))
	})
}

// TestLessonCheckSnapshotSymlinks proves that every symlink is a link
// whose content is its target, and that the producer never reads through
// a symlinked parent directory (§5 row 17).
func TestLessonCheckSnapshotSymlinks(t *testing.T) {
	skipWithoutSymlinks(t)

	g := newGitRepo(t)
	g.write("target.txt", "target\n")
	g.write("swap.txt", "file\n")
	g.write("dir/inner.txt", "inner\n")
	g.write("tracked-dir/f.txt", "f\n")
	g.symlink("target.txt", "link")
	g.commitAll("base")

	linkOid := g.blobID("link")

	g.remove("link")
	g.symlink("other.txt", "link")
	g.remove("swap.txt")
	g.symlink("target.txt", "swap.txt")
	g.symlink("dir", "dirlink")
	g.symlink("/etc/hosts", "outside")
	require.NoError(t, os.Rename(g.path("tracked-dir"), g.path("real-dir")))
	g.symlink("real-dir", "tracked-dir")

	snap := g.snapshot()

	t.Run("the change set", func(t *testing.T) {
		assert.Equal(t, []string{
			"add dirlink",
			"modify link",
			"add outside",
			"add real-dir/f.txt",
			"modify swap.txt",
			"add tracked-dir",
			"delete tracked-dir/f.txt",
		}, changeList(snap))
	})

	t.Run("a link to a directory is a link and is never followed", func(t *testing.T) {
		ch := changeAt(t, snap, "dirlink")

		assert.Equal(t, Side{Type: ObjectSymlink, Mode: modeSymlink, SHA256: sha256Hex("dir"), Size: 3, Content: ContentAvailable, text: []byte("dir")}, ch.After)
	})

	t.Run("a link outside the worktree is read as its target string", func(t *testing.T) {
		ch := changeAt(t, snap, "outside")

		assert.Equal(t, "/etc/hosts", string(ch.After.text))
	})

	t.Run("a retargeted link compares the targets", func(t *testing.T) {
		ch := changeAt(t, snap, "link")

		assert.Equal(t, Side{Type: ObjectSymlink, Mode: modeSymlink, Oid: linkOid, SHA256: sha256Hex("target.txt"), Size: 10,
			Content: ContentAvailable, text: []byte("target.txt")}, ch.Before)
		assert.Equal(t, "other.txt", string(ch.After.text))
		assert.True(t, ch.ContentChanged)
		assert.False(t, ch.TypeChanged)
	})

	t.Run("a file replaced by a link is a type change", func(t *testing.T) {
		ch := changeAt(t, snap, "swap.txt")

		assert.True(t, ch.TypeChanged)
		assert.False(t, ch.ModeChanged, "a mode change is a change between two files")
		assert.Equal(t, ObjectFile, ch.Before.Type)
		assert.Equal(t, ObjectSymlink, ch.After.Type)
		assert.Equal(t, "file\n", string(ch.Before.text))
	})

	t.Run("a tracked path below a new link reads as deleted through the link", func(t *testing.T) {
		ch := changeAt(t, snap, "tracked-dir/f.txt")

		assert.Equal(t, []string{LimitationPathThroughLink}, ch.Limitations)
		assert.Equal(t, ObjectAbsent, ch.After.Type)
		assert.Equal(t, ChangeAdd, changeAt(t, snap, "real-dir/f.txt").Kind, "a delete through a link is never paired")
	})

	t.Run("a named path through a symlinked directory is unreadable", func(t *testing.T) {
		snap := g.snapshot("dirlink/inner.txt")

		assert.Equal(t, []string{"add dirlink/inner.txt"}, changeList(snap))

		ch := snap.Changes[0]
		assert.Equal(t, []string{LimitationPathThroughLink}, ch.Limitations)
		assert.Equal(t, Side{Type: ObjectFile, Content: ContentUnreadable}, ch.After)
		assert.True(t, snap.PathsComplete)

		result := onlyResult(t, onePin(t, KindNoAddedPattern, "inner", "dirlink"), snap)
		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonContentUnavailable, result.Reason)
	})

	// The producer does not look through the link, so it observes no
	// difference for a named path below it. A forbid pin then reads
	// unknown, never violation (review fixes snapshot-git:F5, checkers:K3-F2).
	t.Run("a forbid pin reads a named path through a symlinked directory as unknown", func(t *testing.T) {
		forbid := onePin(t, KindForbidPathChange, "", "dirlink")

		for _, p := range []string{"dirlink/inner.txt", "dirlink/missing.txt"} {
			result := onlyResult(t, forbid, g.snapshot(p))

			assert.Equal(t, OutcomeUnknown, result.Outcome, p)
			assert.Equal(t, ReasonContentUnavailable, result.Reason, p)
			assert.Empty(t, result.Evidence, p)
			assert.Equal(t, []Evidence{{Path: p, Change: ChangeAdd, Reason: ReasonContentUnavailable}}, result.UnknownPaths, p)
			assert.Equal(t, Coverage{PathsInScope: 1}, result.Coverage, p)
		}
	})

	t.Run("an observed change beside a named path through a link is a violation", func(t *testing.T) {
		forbid := onePin(t, KindForbidPathChange, "", "dirlink", "real-dir")
		result := onlyResult(t, forbid, g.snapshot("dirlink/inner.txt", "real-dir/f.txt"))

		assert.Equal(t, OutcomeViolation, result.Outcome)
		assert.Equal(t, []Evidence{{Path: "real-dir/f.txt", Change: ChangeAdd}}, result.Evidence)
		assert.Equal(t, []Evidence{{Path: "dirlink/inner.txt", Change: ChangeAdd, Reason: ReasonContentUnavailable}}, result.UnknownPaths,
			"the undecided path stays in the result")
		assert.Equal(t, Coverage{PathsInScope: 2, PathsEvaluated: 1}, result.Coverage)
	})

	t.Run("a delete that git reports through a link stays a violation", func(t *testing.T) {
		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "tracked-dir"), snap)

		assert.Equal(t, OutcomeViolation, result.Outcome)
		assert.Contains(t, result.Evidence, Evidence{Path: "tracked-dir/f.txt", Change: ChangeDelete})
		assert.Empty(t, result.UnknownPaths)
	})
}

// TestLessonCheckSnapshotMovePairing proves the unstaged move rule of
// contract K4: one deleted path pairs with one untracked path by the git
// blob id and the type, and every ambiguous case stays delete plus add.
func TestLessonCheckSnapshotMovePairing(t *testing.T) {
	t.Run("an unstaged move of identical content is a move", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "same\n")
		g.commitAll("a")

		oid := g.blobID("a.txt")

		require.NoError(t, os.MkdirAll(g.path("moved"), 0o755))
		require.NoError(t, os.Rename(g.path("a.txt"), g.path("moved/a2.txt")))

		snap := g.snapshot()
		require.Equal(t, []string{"move a.txt->moved/a2.txt"}, changeList(snap))

		// The K4 example shape: the before side keeps its oid and size
		// and is not read; both sides carry the content SHA-256.
		ch := snap.Changes[0]
		assert.Equal(t, MoveIdenticalContent, ch.Move)
		assert.False(t, ch.ContentChanged)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, Oid: oid, SHA256: sha256Hex("same\n"), Size: 5, Content: ContentNotRead}, ch.Before)
		assert.Equal(t, Side{Type: ObjectFile, Mode: modeFile, SHA256: sha256Hex("same\n"), Size: 5, Content: ContentAvailable, text: []byte("same\n")}, ch.After)

		t.Run("one named end is enough to show the move", func(t *testing.T) {
			assert.Equal(t, []string{"move a.txt->moved/a2.txt"}, changeList(g.snapshot("a.txt")))
			assert.Equal(t, []string{"move a.txt->moved/a2.txt"}, changeList(g.snapshot("moved")))
		})
	})

	t.Run("an edited move stays delete plus add", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("b.txt", "before\n")
		g.commitAll("b")
		g.remove("b.txt")
		g.write("b2.txt", "after\n")

		assert.Equal(t, []string{"delete b.txt", "add b2.txt"}, changeList(g.snapshot()))
	})

	t.Run("an id that two deleted paths share is not paired", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("c1.txt", "dup\n")
		g.write("c2.txt", "dup\n")
		g.commitAll("c")
		g.remove("c1.txt")
		g.remove("c2.txt")
		g.write("c3.txt", "dup\n")

		assert.Equal(t, []string{"delete c1.txt", "delete c2.txt", "add c3.txt"}, changeList(g.snapshot()))
	})

	t.Run("an id that two untracked paths share is not paired", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("m.txt", "mm\n")
		g.commitAll("m")
		g.remove("m.txt")
		g.write("m1.txt", "mm\n")
		g.write("m2.txt", "mm\n")

		assert.Equal(t, []string{"delete m.txt", "add m1.txt", "add m2.txt"}, changeList(g.snapshot()))
	})

	t.Run("a file and a link with the same blob id are not paired", func(t *testing.T) {
		skipWithoutSymlinks(t)

		g := newGitRepo(t)
		g.write("t.txt", "x")
		g.commitAll("t")
		g.remove("t.txt")
		g.symlink("x", "tl")

		assert.Equal(t, []string{"delete t.txt", "add tl"}, changeList(g.snapshot()))
	})

	t.Run("a path is never paired with its own untracked record", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("p.txt", "k\n")
		g.write("q.txt", "k\n")
		g.commitAll("pq")
		g.git("rm", "-q", "--cached", "p.txt")
		g.remove("q.txt")

		// p.txt takes the Lstat rule (no change) and folds its ? record
		// in, so the deleted q.txt has no partner.
		assert.Equal(t, []string{"delete q.txt"}, changeList(g.snapshot()))
	})

	t.Run("a staged rename with an edit stays a git-rename move", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("long.txt", strings.Repeat("a line that stays the same\n", 20))
		g.commitAll("long")
		g.git("mv", "long.txt", "renamed.txt")
		g.write("renamed.txt", strings.Repeat("a line that stays the same\n", 20)+"one more\n")
		g.git("add", "renamed.txt")

		snap := g.snapshot()
		require.Equal(t, []string{"move long.txt->renamed.txt"}, changeList(snap))
		assert.Equal(t, MoveGitRename, snap.Changes[0].Move)
		assert.True(t, snap.Changes[0].ContentChanged)
		assert.Equal(t, ContentAvailable, snap.Changes[0].Before.Content)
	})
}

// superproject is a repository with one committed submodule at "sub".
// The submodule clone comes from a second temporary repository.
func superproject(t *testing.T) *gitRepo {
	t.Helper()

	g := newGitRepo(t)

	upstream := t.TempDir()
	runGitIn(t, upstream, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(upstream, "s.txt"), []byte("s\n"), 0o644))
	runGitIn(t, upstream, "add", "-A")
	runGitIn(t, upstream, "commit", "-q", "-m", "s")

	g.write("a.txt", "a\n")
	g.commitAll("a")
	g.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "sub")
	g.git("commit", "-q", "-m", "sub")

	return g
}

// TestLessonCheckSnapshotGitlink proves the submodule rows of the K4
// mapping: a new commit is a gitlink change, content inside a submodule
// is a snapshot limitation, and the Lstat rule covers a removed one.
func TestLessonCheckSnapshotGitlink(t *testing.T) {
	t.Run("a clean submodule is no change", func(t *testing.T) {
		snap := superproject(t).snapshot()

		assert.Empty(t, changeList(snap))
		assert.Nil(t, snap.Limitations)
	})

	t.Run("content inside a submodule is a limitation, not a change", func(t *testing.T) {
		g := superproject(t)
		g.write("sub/s.txt", "dirty\n")

		snap := g.snapshot()
		assert.Empty(t, changeList(snap))
		assert.Equal(t, []string{LimitationSubmoduleContentNotInspected}, snap.Limitations)

		t.Run("an explicit selection without the submodule names no limitation", func(t *testing.T) {
			assert.Nil(t, g.snapshot("a.txt").Limitations)
			assert.Equal(t, []string{LimitationSubmoduleContentNotInspected}, g.snapshot("sub").Limitations)
		})
	})

	t.Run("a new commit in the submodule is a gitlink change", func(t *testing.T) {
		g := superproject(t)
		old := g.blobID("sub")

		g.write("sub/s.txt", "c2\n")
		runGitIn(t, g.path("sub"), "commit", "-q", "-a", "-m", "c2")

		snap := g.snapshot()
		require.Equal(t, []string{"modify sub"}, changeList(snap))

		ch := snap.Changes[0]
		assert.Equal(t, Side{Type: ObjectGitlink, Mode: modeGitlink, Oid: old, Content: ContentNotRead}, ch.Before)
		assert.Equal(t, Side{Type: ObjectGitlink, Mode: modeGitlink, Content: ContentNotRead}, ch.After,
			"status gives no worktree commit for a new commit")
		assert.True(t, ch.ContentChanged)
		assert.False(t, ch.TypeChanged)

		result := onlyResult(t, onePin(t, KindNoAddedPattern, "x", "sub"), snap)
		assert.Equal(t, ReasonUnsupportedContent, result.Reason)

		t.Run("a staged commit carries the new commit id", func(t *testing.T) {
			g.git("add", "sub")

			ch := changeAt(t, g.snapshot(), "sub")
			assert.Equal(t, strings.TrimSpace(runGitIn(t, g.path("sub"), "rev-parse", "HEAD")), ch.After.Oid)
			assert.True(t, ch.ContentChanged)
		})
	})

	t.Run("a removed submodule directory is a delete", func(t *testing.T) {
		g := superproject(t)
		g.remove("sub")

		snap := g.snapshot()
		require.Equal(t, []string{"delete sub"}, changeList(snap))
		assert.Equal(t, ObjectGitlink, snap.Changes[0].Before.Type)
	})

	t.Run("git rm --cached of a present submodule is a limitation, not a change", func(t *testing.T) {
		g := superproject(t)
		g.git("rm", "-q", "--cached", "sub")

		snap := g.snapshot()
		assert.Empty(t, changeList(snap))
		assert.Equal(t, []string{LimitationSubmoduleContentNotInspected}, snap.Limitations)
	})
}

// TestLessonCheckSnapshotPathInsideSubmodule proves that a --file path
// inside a submodule or a nested repository is never a silent clean
// result. The snapshot names the limitation and keeps the change of the
// submodule itself, because git gives no other form of that change
// (review fix snapshot-git:F3).
func TestLessonCheckSnapshotPathInsideSubmodule(t *testing.T) {
	inside := []string{LimitationSubmoduleContentNotInspected}

	t.Run("a dirty submodule names the limitation for a path inside it", func(t *testing.T) {
		g := superproject(t)
		g.write("sub/s.txt", "dirty\n")

		for _, p := range []string{"sub/s.txt", "sub/new.txt"} {
			snap := g.snapshot(p)
			assert.Empty(t, changeList(snap), p)
			assert.Equal(t, inside, snap.Limitations, p)
		}

		assert.Nil(t, g.snapshot("a.txt").Limitations, "a dirty submodule outside the selection names no limitation")
	})

	t.Run("a new commit in the submodule keeps the gitlink change", func(t *testing.T) {
		g := superproject(t)
		g.write("sub/s.txt", "c2\n")
		runGitIn(t, g.path("sub"), "commit", "-q", "-a", "-m", "c2")

		snap := g.snapshot("sub/s.txt")
		assert.Equal(t, []string{"modify sub"}, changeList(snap))
		assert.Equal(t, inside, snap.Limitations)

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "sub"), snap)
		assert.Equal(t, OutcomeViolation, result.Outcome, "the named file did change")
	})

	t.Run("a removed submodule keeps the delete", func(t *testing.T) {
		g := superproject(t)
		g.remove("sub")

		snap := g.snapshot("sub/s.txt")
		assert.Equal(t, []string{"delete sub"}, changeList(snap))
		assert.Equal(t, inside, snap.Limitations)
	})

	t.Run("git rm --cached of a present submodule names the limitation", func(t *testing.T) {
		g := superproject(t)
		g.git("rm", "-q", "--cached", "sub")

		snap := g.snapshot("sub/s.txt")
		assert.Empty(t, changeList(snap))
		assert.Equal(t, inside, snap.Limitations)
	})

	t.Run("an untracked nested repository keeps its add", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.commitAll("a")
		runGitIn(t, t.TempDir(), "init", "-q", g.path("nested"))
		g.write("nested/n.txt", "n\n")
		require.Contains(t, g.git("status", "--porcelain=v2", "--untracked-files=all"), "? nested/",
			"git must print the nested repository as one record")

		snap := g.snapshot("nested/n.txt")
		assert.Equal(t, []string{"add nested"}, changeList(snap))
		assert.Equal(t, inside, snap.Limitations)

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "nested"), snap)
		assert.Equal(t, OutcomeViolation, result.Outcome)
	})
}

// TestLessonCheckSnapshotObjectFormats proves move pairing and the Lstat
// rule in a SHA-256 repository, a digest that does not depend on the
// object format, and the fallback for a git that cannot name its format.
func TestLessonCheckSnapshotObjectFormats(t *testing.T) {
	// formatFixture commits three files and then makes an unstaged move,
	// a content change, a reverted staged change, and a cached removal.
	formatFixture := func(t *testing.T, initArgs ...string) *gitRepo {
		t.Helper()

		g := newGitRepo(t, initArgs...)
		g.write("a.txt", "same\n")
		g.write("m.txt", "one\n")
		g.write("r.txt", "r\n")
		g.write("f.txt", "f\n")
		g.commitAll("base")

		require.NoError(t, os.Rename(g.path("a.txt"), g.path("b.txt")))
		g.write("m.txt", "two\n")
		g.write("r.txt", "staged\n")
		g.git("add", "r.txt")
		g.write("r.txt", "r\n")
		g.git("rm", "-q", "--cached", "f.txt")

		return g
	}

	t.Run("sha256", func(t *testing.T) {
		g := formatFixture(t, "--object-format=sha256")
		require.Equal(t, "sha256\n", g.git("rev-parse", "--show-object-format"))

		snap := g.snapshot()
		assert.Len(t, snap.Baseline.Commit, 64)
		assert.Equal(t, []string{"move a.txt->b.txt", "modify m.txt"}, changeList(snap))
		assert.Nil(t, snap.Limitations)

		move := changeAt(t, snap, "b.txt")
		assert.Equal(t, MoveIdenticalContent, move.Move)
		assert.Equal(t, g.blobID("a.txt"), move.Before.Oid)
		assert.Len(t, move.Before.Oid, 64)
	})

	t.Run("sha1", func(t *testing.T) {
		g := formatFixture(t, "--object-format=sha1")

		snap := g.snapshot()
		assert.Len(t, snap.Baseline.Commit, 40)
		assert.Equal(t, []string{"move a.txt->b.txt", "modify m.txt"}, changeList(snap))
	})

	t.Run("the digest uses content hashes, so equal trees give equal digests", func(t *testing.T) {
		build := func(initArgs ...string) *Snapshot {
			g := newGitRepo(t, initArgs...)
			g.write("a.txt", "a\n")
			g.write("s.txt", "s\n")
			g.git("add", "s.txt")

			return g.snapshot()
		}

		inSHA1 := build("--object-format=sha1")
		inSHA256 := build("--object-format=sha256")

		assert.Equal(t, inSHA1.SHA256, inSHA256.SHA256)
	})

	t.Run("a git that cannot name its format disables move pairing", func(t *testing.T) {
		g := formatFixture(t)

		gitShim(t, `case " $* " in *" --show-object-format "*) echo "unknown option" >&2; exit 129;; esac`)

		snap := g.snapshot()
		assert.Equal(t, []string{LimitationMovePairingUnavailable}, snap.Limitations)
		assert.True(t, snap.PathsComplete, "the snapshot still completes")

		// Without blob ids the Lstat rule and the dropped 1 MM record
		// compare the content itself.
		assert.Equal(t, []string{"delete a.txt", "add b.txt", "modify m.txt"}, changeList(snap))
	})
}

// treeState is what one path looks like on disk: its mode, size,
// modification time, and content hash.
type treeState struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
	sha256  string
}

// fingerprint returns the state of every path below root, .git included.
func fingerprint(t *testing.T, root string) map[string]treeState {
	t.Helper()

	out := map[string]treeState{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		state := treeState{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}

		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}

			sum := sha256.Sum256(data)
			state.sha256 = hex.EncodeToString(sum[:])
		}

		out[p] = state

		return nil
	})
	require.NoError(t, err)

	return out
}

// TestLessonCheckSnapshotNeverWrites proves AC-29: the producer runs only
// read-only git commands with GIT_OPTIONAL_LOCKS=0 and leaves every byte
// and every modification time of the repository unchanged, also when the
// index is stale, so a plain `git status` would rewrite it.
func TestLessonCheckSnapshotNeverWrites(t *testing.T) {
	g := newGitRepo(t)
	g.write(".gitignore", "ign/\n")
	g.write("touched.txt", "same\n")
	g.write("edited.txt", "one\n")
	g.write("moved.txt", "move me\n")
	g.commitAll("base")

	// A new modification time without a content change makes the index
	// stat information stale.
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(g.path("touched.txt"), later, later))
	g.write("edited.txt", "two\n")
	require.NoError(t, os.Rename(g.path("moved.txt"), g.path("moved-to.txt")))
	g.write("ign/x.bin", "x\n")
	g.write("new.txt", "new\n")

	log := filepath.Join(t.TempDir(), "git-calls.log")
	gitShim(t, `printf '%s|%s\n' "${GIT_OPTIONAL_LOCKS-unset}" "$*" >> `+shellQuote(log))
	require.NoError(t, os.Remove(log), "the log starts after the warm-up call of gitShim")

	before := fingerprint(t, g.root)

	g.snapshot()
	g.snapshot("ign", "touched.txt", "edited.txt", "moved.txt")

	assert.Equal(t, before, fingerprint(t, g.root), "the snapshot must not write a byte or a modification time")
	assert.NoFileExists(t, g.path(".git/index.lock"))

	data, err := os.ReadFile(log)
	require.NoError(t, err)

	readOnly := map[string]bool{"rev-parse": true, "status": true, "ls-files": true, "cat-file": true, "ls-tree": true}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	seen := map[string]bool{}

	for _, call := range calls {
		env, args, _ := strings.Cut(call, "|")
		sub := gitSubcommand(strings.Fields(args))
		seen[sub] = true

		assert.True(t, readOnly[sub], "a write-capable git command ran: %q", args)

		// history.IsRepo runs `rev-parse --is-inside-work-tree` with the
		// plain environment. That command reads no index.
		if args == "rev-parse --is-inside-work-tree" {
			continue
		}

		assert.Equal(t, "0", env, "every producer call stops the optional index lock: %q", args)
	}

	assert.Equal(t, map[string]bool{"rev-parse": true, "status": true, "ls-files": true, "cat-file": true, "ls-tree": true}, seen,
		"the fixture must exercise every producer command")

	// The control: a plain status takes the optional lock and rewrites
	// the stale index, so the fixture above was able to show a write.
	index := fingerprint(t, g.path(".git"))[g.path(".git/index")]

	g.git("status", "--porcelain")
	assert.NotEqual(t, index, fingerprint(t, g.path(".git"))[g.path(".git/index")], "a plain git status must refresh the stale index")
}

// gitSubcommand returns the git subcommand of an argument list. It skips
// the global options that the producer passes before the subcommand.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			i++
		case "--literal-pathspecs":
		default:
			return args[i]
		}
	}

	return ""
}

// TestLessonCheckSnapshotCopyConfig proves that step 3 finds renames
// only. A user setting that turns on copy detection must not make a copy
// a move, because a move from a source that still exists hides the
// added lines of the copy (review fix snapshot-git:F1).
func TestLessonCheckSnapshotCopyConfig(t *testing.T) {
	// copyFixture copies src/a.go to src/b.go, adds a line to src/a.go,
	// and stages both. isolateGit hides the global configuration, so the
	// copy setting goes into the configuration of the repository.
	copyFixture := func(t *testing.T, key string) *gitRepo {
		t.Helper()

		g := newGitRepo(t)
		g.write("src/a.go", "// TODO( old\n")
		g.commitAll("a")
		g.git("config", key, "copies")
		g.write("src/b.go", "// TODO( old\n")
		g.write("src/a.go", "// TODO( old\nmore\n")
		g.git("add", "-A")

		return g
	}

	for _, key := range []string{"diff.renames", "status.renames"} {
		t.Run(key+"=copies keeps a copy an add", func(t *testing.T) {
			g := copyFixture(t, key)
			require.Contains(t, g.git("status", "--porcelain=v2"), "2 C. ", "the fixture must make git print a copy record")

			snap := g.snapshot()
			assert.Equal(t, []string{"modify src/a.go", "add src/b.go"}, changeList(snap))

			result := onlyResult(t, onePin(t, KindNoAddedPattern, `TODO\(`, "src"), snap)
			assert.Equal(t, OutcomeViolation, result.Outcome, "a new file adds all its lines")
		})
	}

	t.Run("a deleted copy destination leaves one entry for the source", func(t *testing.T) {
		g := copyFixture(t, "diff.renames")
		g.remove("src/b.go")
		require.Contains(t, g.git("status", "--porcelain=v2"), "2 CD ", "the fixture must make git print a deleted copy")

		assert.Equal(t, []string{"modify src/a.go"}, changeList(g.snapshot()))
	})
}

// TestLessonCheckSnapshotUndecidedContent proves that a byte limit or a
// failed read never turns a path whose difference the producer did not
// observe into a change. Such a path leaves the path set incomplete, so a
// forbid pin reads unknown with the cause and never violation (review
// fixes checkers:K3-F1, snapshot-git:F4).
func TestLessonCheckSnapshotUndecidedContent(t *testing.T) {
	headText := strings.Repeat("0123456789abcdef", 4)
	sameSize := strings.Repeat("fedcba9876543210", 4)

	small := DefaultLimits()
	small.MaxFileBytes = 16

	forbid := onePin(t, KindForbidPathChange, "", "generated")

	// bigRepo commits generated/big.txt with 64 bytes, above the 16 bytes
	// of small, and one more file outside the scope.
	bigRepo := func(t *testing.T) *gitRepo {
		t.Helper()

		g := newGitRepo(t)
		g.write("generated/big.txt", headText)
		g.write("other.txt", "other\n")
		g.commitAll("base")

		return g
	}

	// requireUnknown checks an undecided snapshot: no change, an
	// incomplete path set, and a forbid result that names the cause. An
	// empty limit means a failed read without a limit.
	requireUnknown := func(t *testing.T, snap *Snapshot, limit string) {
		t.Helper()

		assert.Empty(t, changeList(snap), "an undecided path is never a change")
		assert.False(t, snap.PathsComplete)

		result := onlyResult(t, forbid, snap)
		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Empty(t, result.Evidence)

		if limit == "" {
			assert.Equal(t, ReasonIncompletePaths, result.Reason)
			assert.Empty(t, result.Limit)

			return
		}

		assert.Equal(t, ReasonResourceLimit, result.Reason)
		assert.Equal(t, limit, result.Limit)
	}

	t.Run("git rm --cached of an unchanged file above max-file-bytes is unknown", func(t *testing.T) {
		g := bigRepo(t)
		g.git("rm", "-q", "--cached", "generated/big.txt")

		requireUnknown(t, g.snapshotWith(small), LimitMaxFileBytes)
		requireUnknown(t, g.snapshotWith(small, "generated/big.txt"), LimitMaxFileBytes)

		pattern := onlyResult(t, onePin(t, KindNoAddedPattern, "x", "generated"), g.snapshotWith(small))
		assert.Equal(t, OutcomeUnknown, pattern.Outcome)
		assert.Equal(t, LimitMaxFileBytes, pattern.Limit)

		// The K4 vector still holds under the default limits.
		snap := g.snapshot()
		assert.Empty(t, changeList(snap))
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, OutcomeNotApplicable, onlyResult(t, forbid, snap).Outcome)
	})

	t.Run("git rm --cached with a size change stays a violation", func(t *testing.T) {
		g := bigRepo(t)
		g.git("rm", "-q", "--cached", "generated/big.txt")
		g.write("generated/big.txt", headText+"x")

		snap := g.snapshotWith(small)
		assert.Equal(t, []string{"modify generated/big.txt"}, changeList(snap))
		assert.True(t, snap.PathsComplete, "unequal sizes prove a content change")
		assert.Equal(t, OutcomeViolation, onlyResult(t, forbid, snap).Outcome)
	})

	t.Run("a reverted 1 MM above max-file-bytes is unknown", func(t *testing.T) {
		g := bigRepo(t)
		g.write("generated/big.txt", sameSize)
		g.git("add", "generated/big.txt")
		g.write("generated/big.txt", headText)
		require.Contains(t, g.git("status", "--porcelain=v2"), "1 MM ", "the fixture must be a 1 MM record")

		requireUnknown(t, g.snapshotWith(small), LimitMaxFileBytes)
	})

	t.Run("a staged change of the same size above max-file-bytes stays a violation", func(t *testing.T) {
		g := bigRepo(t)
		g.write("generated/big.txt", sameSize)
		g.git("add", "generated/big.txt")
		require.Contains(t, g.git("status", "--porcelain=v2"), "1 M. ", "the fixture must be a 1 M. record")

		// git observed it: the worktree equals the index, and the index
		// blob differs from the HEAD blob.
		snap := g.snapshotWith(small)
		assert.Equal(t, []string{"modify generated/big.txt"}, changeList(snap))
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, OutcomeViolation, onlyResult(t, forbid, snap).Outcome)
	})

	t.Run("max-snapshot-bytes names its own limit", func(t *testing.T) {
		g := bigRepo(t)
		g.git("rm", "-q", "--cached", "generated/big.txt")

		lim := DefaultLimits()
		lim.MaxSnapshotBytes = 16

		requireUnknown(t, g.snapshotWith(lim), LimitMaxSnapshotBytes)
	})

	t.Run("an unreadable file under the Lstat rule is unknown without a limit", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a file that the user cannot open")
		}

		g := bigRepo(t)
		g.git("rm", "-q", "--cached", "generated/big.txt")
		require.NoError(t, os.Chmod(g.path("generated/big.txt"), 0))
		t.Cleanup(func() { _ = os.Chmod(g.path("generated/big.txt"), 0o644) })

		requireUnknown(t, g.snapshot(), "")
	})

	t.Run("a git without an object format compares the content, then gives unknown", func(t *testing.T) {
		g := bigRepo(t)
		g.git("rm", "-q", "--cached", "generated/big.txt")

		gitShim(t, `case " $* " in *" --show-object-format "*) echo "unknown option" >&2; exit 129;; esac`)

		requireUnknown(t, g.snapshotWith(small), LimitMaxFileBytes)
		assert.Empty(t, changeList(g.snapshot()), "the content comparison drops an unchanged path")
	})
}

// TestLessonCheckSnapshotLstatError proves ruling 3 of the P1 review (fix
// F-B): an Lstat error other than absence is no observed change. A forbid
// pin then lists that path as unknown/content-unavailable, never as a
// violation, and Verify reports incomplete.
//
// The directory census cannot search generated in each fixture, so the
// path set is incomplete (ruling D2, TestLessonCheckSnapshotDirectoryCensus).
// K3 ranks incomplete-paths above content-unavailable. So the result
// reason is incomplete-paths, and each unknown path keeps
// content-unavailable.
func TestLessonCheckSnapshotLstatError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory that the user cannot search")
	}

	// The fixture gives git a '1 D.' record for generated/a.txt, whose '?'
	// record folds in (the Lstat rule), and a '?' record for
	// generated/new.txt. The directory keeps its read bit, so git lists
	// both names. It loses its search bit, so Lstat of each file fails
	// with a permission error.
	g := verifyRepo(t, mixedYAML)
	g.write("generated/new.txt", "new\n")
	g.git("rm", "-q", "--cached", "generated/a.txt")
	require.NoError(t, os.Chmod(g.path("generated"), 0o600))
	t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

	_, err := os.Lstat(g.path("generated/a.txt"))
	require.ErrorIs(t, err, fs.ErrPermission, "the fixture must give an Lstat error other than absence")

	undecided := []Evidence{
		{Path: "generated/a.txt", Change: ChangeModify, Reason: ReasonContentUnavailable},
		{Path: "generated/new.txt", Change: ChangeAdd, Reason: ReasonContentUnavailable},
	}

	t.Run("a forbid pin reads the paths as unknown, never as a violation", func(t *testing.T) {
		snap := g.snapshot()
		require.Equal(t, []string{"modify generated/a.txt", "add generated/new.txt"}, changeList(snap))

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "generated"), snap)

		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonIncompletePaths, result.Reason, "the census cannot search generated, so the path set is incomplete")
		assert.Empty(t, result.Evidence)
		assert.Equal(t, undecided, result.UnknownPaths)
		assert.Equal(t, Coverage{PathsInScope: 2}, result.Coverage)
	})

	t.Run("an observed change beside the paths is a violation that keeps them", func(t *testing.T) {
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		defer g.write("src/a.go", "package a\n")

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "generated", "src"), g.snapshot())

		assert.Equal(t, OutcomeViolation, result.Outcome)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeModify}}, result.Evidence)
		assert.Equal(t, undecided, result.UnknownPaths)
	})

	t.Run("a pattern pin reads the paths as content-unavailable", func(t *testing.T) {
		result := onlyResult(t, onePin(t, KindNoAddedPattern, "x", "generated"), g.snapshot())

		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonIncompletePaths, result.Reason, "the census cannot search generated, so the path set is incomplete")
		assert.Equal(t, undecided, result.UnknownPaths)
	})

	t.Run("Verify reports incomplete with exit 1", func(t *testing.T) {
		v := g.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness, "an unchanged Lstat error stays the same at the revalidation")
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))

		err := v.ExitError()
		require.ErrorIs(t, err, ErrIncomplete)
		assert.Equal(t, 1, Combine(v.Summary.State, false).ExitCode)
	})

	// git 2.50.1 prints no status record for an edited tracked file below
	// a directory without the search bit. It writes "Permission denied" to
	// stderr and exits 0. So only the Lstat of the --file path can tell
	// that the producer did not observe the path.
	t.Run("an explicit path that git prints no record for reads as unknown", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		h.write("generated/a.txt", "edited\n")
		require.NoError(t, os.Chmod(h.path("generated"), 0o600))
		t.Cleanup(func() { _ = os.Chmod(h.path("generated"), 0o755) })

		_, err := os.Lstat(h.path("generated/a.txt"))
		require.ErrorIs(t, err, fs.ErrPermission, "the fixture must give an Lstat error other than absence")

		snap := h.snapshot("generated/a.txt")
		require.Equal(t, []string{"modify generated/a.txt"}, changeList(snap))

		ch := changeAt(t, snap, "generated/a.txt")
		assert.Equal(t, Side{Type: ObjectFile, Content: ContentUnreadable}, ch.After)
		assert.Empty(t, ch.Limitations, "no link is on the path, so no path-through-link")

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "generated"), snap)

		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonIncompletePaths, result.Reason, "the census cannot search generated, so the path set is incomplete")
		assert.Empty(t, result.Evidence)
		assert.Equal(t, []Evidence{{Path: "generated/a.txt", Change: ChangeModify, Reason: ReasonContentUnavailable}}, result.UnknownPaths)
		assert.Equal(t, Coverage{PathsInScope: 1}, result.Coverage)

		v := h.verify("generated/a.txt")

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
		require.ErrorIs(t, v.ExitError(), ErrIncomplete)
		assert.Equal(t, 1, Combine(v.Summary.State, false).ExitCode)
	})
}

// TestLessonCheckSnapshotStatusStderr proves ruling D2: text that git
// status writes to stderr is a git-stderr notice. It never makes the path
// set incomplete, because a configuration warning that no rerun clears
// must not give exit 1 on every run. The directory census decides the
// path set instead (TestLessonCheckSnapshotDirectoryCensus).
func TestLessonCheckSnapshotStatusStderr(t *testing.T) {
	// RunGit drops the GIT_TRACE variables of the environment. Trace
	// output names no path of the tree, so a trace setting of the user's
	// shell must not add a notice to every verification.
	t.Run("git trace variables keep a clean tree fresh and complete", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)

		// The fixture runs git before the variables exist, so no trace line
		// mixes into the output of a fixture command.
		t.Setenv("GIT_TRACE", "1")
		t.Setenv("GIT_TRACE2", "1")

		v := h.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Empty(t, v.Limitations, "git writes no trace line, so no notice")
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
	})

	// A deprecated configuration key makes git write a warning on every
	// run. The warning names no path of the tree, so the path set stays
	// complete, and the verification exits 0 with the notice.
	t.Run("stderr alone is a notice, and a clean tree exits 0", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		h.git("config", "core.fsyncObjectFiles", "true")

		v := h.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{LimitationGitStderr}, v.Limitations)
		assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		require.NoError(t, v.ExitError())
		assert.Contains(t, renderText(v), "  git stderr: warning: core.fsyncObjectFiles is deprecated; use core.fsync instead\n")
	})

	// git, a filter, or an fsmonitor hook can write any bytes to stderr,
	// and the CLI and MCP text print the notice. So the notice keeps the
	// first non-blank line only, through the K3 excerpt helper: redacted,
	// sanitized, and cut to max-excerpt-runes.
	t.Run("the notice is the first line, redacted, sanitized, and cut", func(t *testing.T) {
		lim := DefaultLimits()
		lim.MaxExcerptRunes = 30

		r := newTreeReader(context.Background(), t.TempDir(), lim)
		r.noteStderr([]byte("\n  \x1b[31m: Bearer s3cr3t " + strings.Repeat("é", 40) + "\nsecond line\n"))

		assert.True(t, r.limitations[LimitationGitStderr])
		assert.Equal(t, "[31m: Bearer [REDACTED] ééééé…", r.statusStderr)

		blank := newTreeReader(context.Background(), t.TempDir(), lim)
		blank.noteStderr([]byte(""))
		blank.noteStderr([]byte(" \n\t\r\n"))

		assert.Empty(t, blank.limitations, "blank stderr is no notice")
		assert.Empty(t, blank.statusStderr)
	})

	// The status digest holds no stderr (ruling D2). So a warning that
	// appears or goes away between the read and the revalidation keeps
	// the result fresh. The notice shows the stderr of the read.
	t.Run("a warning that appears only at the revalidation keeps the result fresh", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		onRevalidate(t, func() { h.git("config", "core.fsyncObjectFiles", "true") })

		v := h.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Empty(t, v.Limitations, "the read wrote no stderr")
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		require.NoError(t, v.ExitError())
	})

	t.Run("a warning that goes away before the revalidation keeps the result fresh", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		h.git("config", "core.fsyncObjectFiles", "true")
		onRevalidate(t, func() { h.git("config", "--unset", "core.fsyncObjectFiles") })

		v := h.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{LimitationGitStderr}, v.Limitations)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		require.NoError(t, v.ExitError())
	})

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("the other subtests need a directory that the user cannot search")
	}

	g := verifyRepo(t, mixedYAML)
	g.write("generated/a.txt", "edited\n")
	require.NoError(t, os.Chmod(g.path("generated"), 0o600))
	t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

	_, err := os.Lstat(g.path("generated/a.txt"))
	require.ErrorIs(t, err, fs.ErrPermission, "the fixture must hide the edit from git")

	// The census names the directory that hides the edit. The first git
	// status stderr line stays as the notice.
	const (
		evidence = "  path set: incomplete (unreadable directory: generated)\n"
		notice   = "  git stderr: generated/a.txt: Permission denied\n"
	)

	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{name: "the default selection", paths: nil},
		{name: "a --file path that names the directory", paths: []string{"generated"}},
	} {
		t.Run(tc.name+" reads the edit as incomplete-paths", func(t *testing.T) {
			snap := g.snapshot(tc.paths...)

			assert.Empty(t, changeList(snap), "git prints no record for the edit")
			assert.False(t, snap.PathsComplete)
			assert.Equal(t, []string{LimitationGitStderr}, snap.Limitations)

			result := onlyResult(t, onePin(t, KindForbidPathChange, "", "generated"), snap)

			assert.Equal(t, OutcomeUnknown, result.Outcome)
			assert.Equal(t, ReasonIncompletePaths, result.Reason)
			assert.Empty(t, result.Limit)

			v := g.verify(tc.paths...)

			assert.Equal(t, FreshnessFresh, v.Freshness, "the same census at the revalidation keeps the result fresh")
			assert.False(t, v.PathsComplete)
			assert.Equal(t, SummaryIncomplete, v.Summary.State)
			assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
			require.ErrorIs(t, v.ExitError(), ErrIncomplete)
			assert.Equal(t, 1, Combine(v.Summary.State, false).ExitCode)

			text := renderText(v)
			assert.Contains(t, text, evidence)
			assert.Contains(t, text, notice)
		})
	}

	// Step 3 reads the whole worktree for every selection, so git writes
	// stderr about generated also for --file src. The census reads only
	// the directories of the named paths, so the path set stays complete.
	t.Run("a --file path beside the directory stays complete with the notice", func(t *testing.T) {
		v := g.verify("src")

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{LimitationGitStderr}, v.Limitations)
		assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		require.NoError(t, v.ExitError())

		text := renderText(v)
		assert.Contains(t, text, notice)
		assert.NotContains(t, text, "path set: incomplete")
	})

	// git does not open an ignored directory at step 3, so the default
	// selection gets no stderr, and the census does not read the
	// directory. A --file path that names the directory puts it in the
	// census, so the path set is incomplete. Step 3a writes the notice.
	t.Run("an ignored directory that git cannot open is incomplete only when --file names it", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		h.write(".git/info/exclude", ".seamark/\ncache/\n")
		h.write("cache/x.bin", "x\n")
		require.NoError(t, os.Chmod(h.path("cache"), 0))
		t.Cleanup(func() { _ = os.Chmod(h.path("cache"), 0o755) })

		snap := h.snapshot()
		assert.True(t, snap.PathsComplete, "no index entry and no status record names the ignored directory")
		assert.Empty(t, snap.Limitations, "step 3 does not open an ignored directory, so git writes no stderr")

		v := h.verify("cache")

		assert.False(t, v.PathsComplete)
		assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))

		text := renderText(v)
		assert.Contains(t, text, "  path set: incomplete (unreadable directory: cache)\n")
		assert.Contains(t, text, "  git stderr: warning: could not open directory 'cache/': Permission denied\n")
	})

	// The revalidation (contract K5) runs the census again. A directory
	// that loses its search bit after the read hides docs/readme.md, so
	// the census finds a new unreadable directory. The directory holds no
	// change, so the status stdout stays the same.
	t.Run("a directory that loses its search bit before the revalidation makes the result stale", func(t *testing.T) {
		h := verifyRepo(t, mixedYAML)
		t.Cleanup(func() { _ = os.Chmod(h.path("docs"), 0o755) })

		onRevalidate(t, func() { require.NoError(t, os.Chmod(h.path("docs"), 0o600)) })

		v := h.verify()

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:unknown/stale-snapshot", "2:unknown/stale-snapshot"}, outcomes(v))
	})
}

// TestLessonCheckSnapshotDirectoryCensus proves the directory census of
// ruling D2. git status prints no record for a tracked file below a
// directory that the user cannot search, and it exits 0. So the producer
// reads each directory that holds a selected path. A directory that it
// cannot read leaves the path set incomplete, with the directory as the
// evidence. The census never reads the stderr text of git.
func TestLessonCheckSnapshotDirectoryCensus(t *testing.T) {
	// The census counts directories against its own limit,
	// max-census-directories, so a large tree cannot make the census read
	// without a bound (D2 follow-up, ruling 1).
	t.Run("max-census-directories bounds the census", func(t *testing.T) {
		assert.Equal(t, 50000, DefaultLimits().MaxCensusDirectories)

		g := verifyRepo(t, mixedYAML)

		lim := DefaultLimits()
		lim.MaxCensusDirectories = 2

		snap := g.snapshotWith(lim)

		assert.Empty(t, snap.Changes)
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitMaxCensusDirectories, Value: 2, Paths: 2}}, snap.Limits,
			"the census holds four directories: ., docs, generated, and src")

		result := onlyResult(t, onePin(t, KindForbidPathChange, "", "generated"), snap)

		assert.Equal(t, OutcomeUnknown, result.Outcome)
		assert.Equal(t, ReasonResourceLimit, result.Reason)
		assert.Equal(t, LimitMaxCensusDirectories, result.Limit)
	})

	// max-snapshot-paths bounds the status records only. A clean tree has
	// no record, so a low max-snapshot-paths keeps it complete, whatever
	// the count of its directories.
	t.Run("max-snapshot-paths does not bound the census", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)

		lim := DefaultLimits()
		lim.MaxSnapshotPaths = 1

		snap := g.snapshotWith(lim)

		assert.True(t, snap.PathsComplete)
		assert.Empty(t, snap.Limits)
	})

	// The bound counts directories. A named file is no directory, so it
	// adds only its parents: this census holds one directory, ".".
	t.Run("named files do not count against the census bound", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.write("b.txt", "b\n")
		g.write("c.txt", "c\n")
		g.commitAll("base")

		lim := DefaultLimits()
		lim.MaxCensusDirectories = 1

		snap := g.snapshotWith(lim, "a.txt", "b.txt", "c.txt")

		assert.True(t, snap.PathsComplete)
		assert.Empty(t, snap.Limits)
	})

	// Step 6 and the revalidation run Lstat on each parent again before a
	// read. So the census leaves no cached directory state, and the time
	// between a parent Lstat and a file read does not grow.
	t.Run("the census leaves no cached directory state", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)

		r := newTreeReader(context.Background(), g.root, DefaultLimits())
		_, _, complete := r.statusRecords(nil)

		require.True(t, complete)
		assert.Empty(t, r.dirs)
	})

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("the other subtests need a directory that the user cannot read or search")
	}

	// git 2.50.1 prints no record for a tracked file deleted from a
	// directory without the search bit. Its check of the delete fails with
	// "Permission denied", and git exits 0. The directory is then empty,
	// so no listed name shows the missing search bit. The census looks up
	// "." in the directory instead, so the evidence names the directory.
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{name: "the default selection", paths: nil},
		{name: "a --file path that names the file", paths: []string{"generated/a.txt"}},
	} {
		t.Run("an empty directory without the search bit hides a delete under "+tc.name, func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			g.remove("generated/a.txt")
			require.NoError(t, os.Chmod(g.path("generated"), 0o600))
			t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

			assert.False(t, g.snapshot(tc.paths...).PathsComplete)

			v := g.verify(tc.paths...)

			assert.Equal(t, FreshnessFresh, v.Freshness)
			assert.False(t, v.PathsComplete)
			assert.Equal(t, SummaryIncomplete, v.Summary.State)
			assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
			require.ErrorIs(t, v.ExitError(), ErrIncomplete)
			assert.Contains(t, renderText(v), "  path set: incomplete (unreadable directory: generated)\n")
		})
	}

	// Mode 0300 keeps the search bit, so git prints the record of the edit
	// and warns that it cannot list the directory. Under --file src only
	// the records at or below src count, so the census does not read
	// generated, and the path set stays complete.
	t.Run("a record outside the --file paths does not put its directory in the census", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("generated/a.txt", "edited\n")
		require.NoError(t, os.Chmod(g.path("generated"), 0o300))
		t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

		require.Equal(t, []string{"modify generated/a.txt"}, changeList(g.snapshot()), "git prints the record of the edit")

		v := g.verify("src")

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		require.NoError(t, v.ExitError())
		assert.NotContains(t, renderText(v), "unreadable directory")
	})

	// O_NOFOLLOW covers the last name of a path only. A parent that becomes
	// a link after the Lstat of dirState leads the open to a directory
	// outside the root. The census compares the opened directory with that
	// Lstat, as readNoFollow does for a file, and never takes its verdict.
	t.Run("a parent swapped for a link after the Lstat reads as unreadable", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("deep/a/b.txt", "b\n")
		g.commitAll("deep")

		outside := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(outside, "a"), 0o755))

		r := newTreeReader(context.Background(), g.root, DefaultLimits())
		require.Equal(t, pathPresent, r.dirState("deep/a"))

		require.NoError(t, os.Rename(g.path("deep"), g.path("deep.old")))
		g.symlink(outside, "deep")

		assert.False(t, r.readableDir("deep/a"), "the opened directory is not the one that Lstat saw")
	})

	// A directory without the read bit does not open. A directory without
	// the search bit opens and lists its names, but each entry lookup
	// fails. Both modes hide the tracked file from git on a clean tree.
	for _, mode := range []fs.FileMode{0, 0o600} {
		t.Run(fmt.Sprintf("an unreadable directory that holds a tracked file is incomplete (mode %04o)", mode), func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			require.NoError(t, os.Chmod(g.path("generated"), mode))
			t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

			snap := g.snapshot()

			assert.Empty(t, changeList(snap))
			assert.False(t, snap.PathsComplete)

			v := g.verify()

			assert.Equal(t, FreshnessFresh, v.Freshness, "the same census at the revalidation keeps the result fresh")
			assert.False(t, v.PathsComplete)
			assert.Equal(t, SummaryIncomplete, v.Summary.State)
			assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
			require.ErrorIs(t, v.ExitError(), ErrIncomplete)
			assert.Contains(t, renderText(v), "  path set: incomplete (unreadable directory: generated)\n")
		})
	}

	// The JSON form carries the census evidence too, so a consumer of
	// lesson-snapshot/1 or lesson-verification/1 (also inside the
	// check-lessons/1 envelope) sees the directory (D2 follow-up, ruling
	// 2). A complete path set has no such key.
	t.Run("the JSON names the unreadable directories", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)

		complete, err := json.Marshal(g.verify())
		require.NoError(t, err)
		assert.NotContains(t, string(complete), "unreadable_directories")

		require.NoError(t, os.Chmod(g.path("generated"), 0o600))
		t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

		for name, doc := range map[string]any{"lesson-snapshot/1": g.snapshot(), "lesson-verification/1": g.verify()} {
			data, err := json.Marshal(doc)
			require.NoError(t, err)

			assert.Contains(t, string(data), `"paths_complete":false,"unreadable_directories":["generated"],`, name)
			assert.NotContains(t, string(data), "unreadable_directories_omitted", name)
		}
	})

	// The JSON lists at most max-unreadable-directories directories and
	// counts the rest, so the output stays bounded. The text line counts
	// every directory, and the cut never changes the outcome.
	t.Run("the JSON keeps max-unreadable-directories directories and counts the rest", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)

		var dirs []string

		for i := range maxUnreadableDirectories + 1 {
			dir := fmt.Sprintf("u%02d", i)
			g.write(dir+"/f.txt", "f\n")
			dirs = append(dirs, dir)
		}

		g.commitAll("unreadable")

		for _, dir := range dirs {
			require.NoError(t, os.Chmod(g.path(dir), 0))
			t.Cleanup(func() { _ = os.Chmod(g.path(dir), 0o755) })
		}

		v := g.verify()

		for name, doc := range map[string]any{"lesson-snapshot/1": g.snapshot(), "lesson-verification/1": v} {
			data, err := json.Marshal(doc)
			require.NoError(t, err)

			var got struct {
				Dirs    []string `json:"unreadable_directories"`
				Omitted int      `json:"unreadable_directories_omitted"`
			}

			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, dirs[:maxUnreadableDirectories], got.Dirs, name)
			assert.Equal(t, 1, got.Omitted, name)
		}

		assert.False(t, v.PathsComplete)
		assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
		assert.Contains(t, renderText(v), "  path set: incomplete (unreadable directory: u00, and 50 more)\n")
	})

	// No index entry and no status record names the directory, so the
	// census does not read it. git still writes stderr, which stays a
	// notice. With the default selection this is the accepted residual of
	// ruling D2: the untracked files there stay unseen, because the census
	// does not parse the git warning.
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{name: "a --file path elsewhere", paths: []string{"src"}},
		{name: "the default selection", paths: nil},
	} {
		t.Run("an unreadable directory without a tracked file stays complete under "+tc.name, func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			g.write("secret/x.txt", "x\n")
			require.NoError(t, os.Chmod(g.path("secret"), 0))
			t.Cleanup(func() { _ = os.Chmod(g.path("secret"), 0o755) })

			v := g.verify(tc.paths...)

			assert.Equal(t, FreshnessFresh, v.Freshness)
			assert.True(t, v.PathsComplete)
			assert.Equal(t, []string{LimitationGitStderr}, v.Limitations)
			assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
			assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
			require.NoError(t, v.ExitError())

			text := renderText(v)
			assert.Contains(t, text, "  git stderr: warning: could not open directory 'secret/': Permission denied\n")
			assert.NotContains(t, text, "path set: incomplete")
		})
	}

	// The revalidation (contract K5) compares the status digest. A census
	// change must reach the digest without a change of the status output,
	// and a new modification time must not reach it.
	t.Run("the status digest holds the unreadable directories, never a time", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		require.NoError(t, os.Chmod(g.path("generated"), 0o600))
		t.Cleanup(func() { _ = os.Chmod(g.path("generated"), 0o755) })

		read := func() (string, []string) {
			r := newTreeReader(context.Background(), g.root, DefaultLimits())
			_, sum, _ := r.statusRecords(nil)

			return sum, r.unreadableDirs
		}

		first, dirs := read()
		assert.Equal(t, []string{"generated"}, dirs)

		later := time.Now().Add(time.Hour)
		require.NoError(t, os.Chtimes(g.path("generated"), later, later))

		again, _ := read()
		assert.Equal(t, first, again, "a new modification time keeps the digest")

		require.NoError(t, os.Chmod(g.path("generated"), 0o755))

		readable, dirs := read()
		assert.Empty(t, dirs)
		assert.NotEqual(t, first, readable, "a directory that becomes readable changes the digest")
	})

	// The census does not read below an unreadable directory, because the
	// whole subtree is undecided. So the evidence names the top directory
	// only.
	t.Run("an unreadable directory names only itself, not its subtree", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("deep/a/b/c.txt", "c\n")
		g.commitAll("deep")
		require.NoError(t, os.Chmod(g.path("deep"), 0o600))
		t.Cleanup(func() { _ = os.Chmod(g.path("deep"), 0o755) })

		v := g.verify()

		assert.False(t, v.PathsComplete)
		assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
		assert.Contains(t, renderText(v), "  path set: incomplete (unreadable directory: deep)\n")
	})

	// linkGenerated replaces the tracked directory generated with a link to
	// a directory outside the root that holds a.txt and has the given mode.
	linkGenerated := func(t *testing.T, g *gitRepo, mode fs.FileMode) {
		t.Helper()

		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "a.txt"), []byte("a\n"), 0o644))
		require.NoError(t, os.Chmod(outside, mode))
		t.Cleanup(func() { _ = os.Chmod(outside, 0o755) })

		g.remove("generated")
		g.symlink(outside, "generated")
	}

	// K4 inspects a link as a link, so the census never opens a directory
	// through a link. git 2.50.1 runs lstat through the link instead. When
	// the target has no search bit, git writes "Permission denied", exits
	// 0, and prints no ".D" record for generated/a.txt, also when the link
	// is ignored. An index entry below a link without a status record
	// makes the path set incomplete (D2 follow-up, ruling 3). A --file
	// path of the link or below it selects the same index entry, so the
	// rule applies there too. A --file path below the link keeps its K4
	// change with the content unreadable, so the forbid pin on generated
	// lists it in unknown_paths.
	unknownBelowLink := []Evidence{{Path: "generated/a.txt", Change: ChangeModify, Reason: ReasonContentUnavailable}}

	for _, tc := range []struct {
		name    string
		mode    fs.FileMode
		ignored bool
		paths   []string
		changes []string
		results []string
		unknown []Evidence // the unknown_paths of the forbid pin on generated
	}{
		{name: "mode 0000", mode: 0, changes: []string{"add generated"}, results: []string{"1:violation", "2:unknown/incomplete-paths"}},
		{name: "mode 0600", mode: 0o600, changes: []string{"add generated"}, results: []string{"1:violation", "2:unknown/incomplete-paths"}},
		{name: "mode 0000, link ignored", mode: 0, ignored: true, changes: []string{}, results: []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}},
		{name: "mode 0000, --file of the link", mode: 0, paths: []string{"generated"}, changes: []string{"add generated"}, results: []string{"1:violation", "2:unknown/incomplete-paths"}},
		{name: "mode 0000, link ignored, --file of the link", mode: 0, ignored: true, paths: []string{"generated"}, changes: []string{"add generated"}, results: []string{"1:violation", "2:unknown/incomplete-paths"}},
		{name: "mode 0000, --file of the file below the link", mode: 0, paths: []string{"generated/a.txt"}, changes: []string{"modify generated/a.txt"}, results: []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, unknown: unknownBelowLink},
		{name: "mode 0000, link ignored, --file of the file below the link", mode: 0, ignored: true, paths: []string{"generated/a.txt"}, changes: []string{"modify generated/a.txt"}, results: []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, unknown: unknownBelowLink},
	} {
		t.Run("an index entry below a link to an unsearchable directory is incomplete ("+tc.name+")", func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			if tc.ignored {
				g.write(".git/info/exclude", ".seamark/\ngenerated\n")
			}

			linkGenerated(t, g, tc.mode)

			snap := g.snapshot(tc.paths...)

			assert.Equal(t, tc.changes, changeList(snap), "git prints no record for generated/a.txt")
			assert.False(t, snap.PathsComplete)
			assert.Empty(t, snap.UnreadableDirectories, "the census opens nothing through the link")
			assert.Equal(t, []string{LimitationGitStderr}, snap.Limitations)

			// Each unknown path is a --file path below the link, so its
			// change names path-through-link (K4 Validation).
			for _, e := range tc.unknown {
				assert.Equal(t, []string{LimitationPathThroughLink}, changeAt(t, snap, e.Path).Limitations)
			}

			v := g.verify(tc.paths...)

			assert.Equal(t, FreshnessFresh, v.Freshness)
			assert.False(t, v.PathsComplete)
			assert.Equal(t, tc.results, outcomes(v))
			assert.Equal(t, tc.unknown, resultOf(t, v, 1).UnknownPaths)

			text := renderText(v)
			assert.Contains(t, text, "  path set: incomplete\n")
			assert.Contains(t, text, "  git stderr: generated/a.txt: Permission denied\n")
		})
	}

	// Mode 0300 keeps the search bit, so git prints the ".D" record of
	// generated/a.txt, and the record covers the index entry. The census
	// never opens the target: an open through the link fails at mode 0300,
	// because the read bit is not set.
	t.Run("a link to a directory that git can search is never opened and keeps the path set complete", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		linkGenerated(t, g, 0o300)

		snap := g.snapshot()

		assert.Equal(t, []string{"add generated", "delete generated/a.txt"}, changeList(snap))
		assert.True(t, snap.PathsComplete)

		v := g.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Empty(t, v.Limitations, "git can search the target, so it writes no stderr")
		assert.Equal(t, []string{"1:violation", "2:not-applicable"}, outcomes(v))
		assert.NotContains(t, renderText(v), "path set: incomplete")
	})

	// An ignored link gives no status record, so the status stdout of a
	// clean tree stays empty when a link replaces generated. The census
	// result is in the status digest, so the revalidation sees the new
	// undecided entry and makes the result stale.
	t.Run("an ignored link that replaces a directory before the revalidation makes the result stale", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write(".git/info/exclude", ".seamark/\ngenerated\n")

		onRevalidate(t, func() { linkGenerated(t, g, 0) })

		v := g.verify()

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:unknown/stale-snapshot", "2:unknown/stale-snapshot"}, outcomes(v))
	})

	// The rule of ruling 3 looks at the parents inside root only. The top
	// level "." is root itself, so a link to the top level as root does
	// not put a top-level index entry below a link. The exported
	// ReadWorkingTree accepts such a root, although the CLI and MCP
	// callers resolve it with git rev-parse --show-toplevel first.
	t.Run("a top-level tracked file read through a link to the root keeps the path set complete", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("top.txt", "top\n")
		g.commitAll("top")

		link := filepath.Join(t.TempDir(), "root")
		require.NoError(t, os.Symlink(g.root, link))

		snap, err := ReadWorkingTree(context.Background(), link, nil, DefaultLimits())
		require.NoError(t, err)
		requireValidSnapshot(t, snap)

		assert.Empty(t, snap.Changes)
		assert.True(t, snap.PathsComplete)
	})

	// git does not read the worktree side of an index entry with the
	// skip-worktree or the assume-unchanged bit. A sparse checkout sets
	// skip-worktree on each path outside its cone, docs/readme.md included
	// here. So git prints no record and no stderr for such an entry below a
	// link. The user keeps these entries out of the worktree on purpose, so
	// they stay out of the rule of ruling 3. Their parents join the census,
	// but the census never opens a link (ruling S2, option (c)). The
	// selection then names them once in its excludes, in the JSON and in
	// the text (manager ruling S2). An entry without a bit keeps ruling 3,
	// and a bit outside the --file paths adds no exclude. A --file path of
	// the entry keeps its K4 change through the link, so its content stays
	// unavailable.
	skipWorktree := []string{"update-index", "--skip-worktree", "generated/a.txt"}
	assumeUnchanged := []string{"update-index", "--assume-unchanged", "generated/a.txt"}
	withSkipped := func(excludes []string) []string {
		return append(append([]string{}, excludes...), wantSkippedExclude)
	}

	for _, tc := range []struct {
		name        string
		setup       [][]string // the git commands that set the bits
		paths       []string
		changes     []string
		complete    bool
		excludes    []string
		limitations []string
		results     []string
		unknown     []Evidence // the unknown_paths of the forbid pin on generated
	}{
		{
			name: "an index entry with the skip-worktree bit below a link keeps the path set complete", setup: [][]string{skipWorktree},
			changes: []string{"add generated"}, complete: true, excludes: withSkipped(wantDefaultExcludes),
			results: []string{"1:violation", "2:not-applicable"},
		},
		{
			name: "an index entry with the assume-unchanged bit below a link keeps the path set complete", setup: [][]string{assumeUnchanged},
			changes: []string{"add generated"}, complete: true, excludes: withSkipped(wantDefaultExcludes),
			results: []string{"1:violation", "2:not-applicable"},
		},
		{
			name: "an index entry with both bits below a link keeps the path set complete", setup: [][]string{skipWorktree, assumeUnchanged},
			changes: []string{"add generated"}, complete: true, excludes: withSkipped(wantDefaultExcludes),
			results: []string{"1:violation", "2:not-applicable"},
		},
		{
			name: "a sparse-checkout entry outside the cone below a link keeps the path set complete", setup: [][]string{{"sparse-checkout", "set", "src"}},
			changes: []string{"add generated"}, complete: true, excludes: withSkipped(wantDefaultExcludes),
			results: []string{"1:violation", "2:not-applicable"},
		},
		{
			name: "an index entry with the skip-worktree bit below a link keeps the path set complete under --file of the link", setup: [][]string{skipWorktree},
			paths: []string{"generated"}, changes: []string{"add generated"}, complete: true, excludes: withSkipped(wantExplicitExcludes),
			results: []string{"1:violation", "2:not-applicable"},
		},
		{
			name: "an index entry with the skip-worktree bit below a link keeps its K4 change under --file of the entry", setup: [][]string{skipWorktree},
			paths: []string{"generated/a.txt"}, changes: []string{"modify generated/a.txt"}, complete: true, excludes: withSkipped(wantExplicitExcludes),
			results: []string{"1:unknown/content-unavailable", "2:not-applicable"}, unknown: unknownBelowLink,
		},
		{
			name: "a skip-worktree bit outside the --file paths adds no exclude", setup: [][]string{skipWorktree},
			paths: []string{"src"}, changes: []string{}, complete: true, excludes: wantExplicitExcludes,
			results: []string{"1:not-applicable", "2:not-applicable"},
		},
		{
			name:    "an index entry without a bit below a link keeps the path set incomplete",
			changes: []string{"add generated"}, complete: false, excludes: wantDefaultExcludes, limitations: []string{LimitationGitStderr},
			results: []string{"1:violation", "2:unknown/incomplete-paths"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			for _, args := range tc.setup {
				g.git(args...)
			}

			linkGenerated(t, g, 0)

			snap := g.snapshot(tc.paths...)

			assert.Equal(t, tc.changes, changeList(snap))
			assert.Equal(t, tc.complete, snap.PathsComplete)
			assert.Empty(t, snap.UnreadableDirectories, "the census opens nothing through the link")
			assert.Equal(t, tc.excludes, snap.Selection.Excludes)
			assert.Equal(t, tc.limitations, snap.Limitations, "git writes no stderr for an entry with a bit")

			for _, e := range tc.unknown {
				assert.Equal(t, []string{LimitationPathThroughLink}, changeAt(t, snap, e.Path).Limitations)
			}

			v := g.verify(tc.paths...)

			assert.Equal(t, FreshnessFresh, v.Freshness)
			assert.Equal(t, tc.complete, v.PathsComplete)
			assert.Equal(t, tc.results, outcomes(v))
			assert.Equal(t, tc.unknown, resultOf(t, v, 1).UnknownPaths)
			assert.Equal(t, tc.excludes, v.Selection.Excludes)

			// The text and the JSON list the exclude once, or not at all.
			want := 0
			if slices.Contains(tc.excludes, wantSkippedExclude) {
				want = 1
			}

			data, err := json.Marshal(v)
			require.NoError(t, err)

			text := renderText(v)

			assert.Equal(t, want, strings.Count(string(data), wantSkippedExclude), "lesson-verification/1")
			assert.Equal(t, want, strings.Count(text, wantSkippedExclude), "text")
			assert.Equal(t, !tc.complete, strings.Contains(text, "  path set: incomplete\n"))
		})
	}

	// A deadline can cut the read after the census. The snapshot then
	// keeps only the fixed texts of its kind, so the result of a cut read
	// does not depend on the time of the deadline. The edit of src/a.go
	// makes the read call cat-file after the census.
	t.Run("a deadline after the census drops the skipped-entry exclude", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.git(skipWorktree...)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		gitShim(t, `case " $* " in *" cat-file "*) exec sleep 10;; esac`)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		lim := DefaultLimits()

		snap, err := ReadWorkingTree(ctx, g.root, nil, lim)
		require.NoError(t, err)
		requireValidSnapshot(t, snap)

		assert.Equal(t, []LimitHit{{Name: LimitVerifyTimeout, Value: lim.VerifyTimeout.Milliseconds()}}, snap.Limits)
		assert.Equal(t, wantDefaultExcludes, snap.Selection.Excludes)
	})

	// The census reads the tag before each path. git ls-files -v tags an
	// index entry with H, S, or M, or with the lowercase form of one. Any
	// other record is a git failure and not an entry, so the path set is
	// not complete. The rows "g x/a.txt" and "x generated/a.txt" have a
	// space as the second byte, so only the tag set stops their lowercase
	// first letter from hiding an entry.
	for _, tc := range []struct{ name, record string }{
		{name: "an index record without a tag makes the path set incomplete", record: "generated/a.txt"},
		{name: "an index record without a tag whose second byte is a space makes the path set incomplete", record: "g x/a.txt"},
		{name: "an index record with a tag that git ls-files -v does not print makes the path set incomplete", record: "x generated/a.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)

			gitShim(t, `case " $* " in *" ls-files "*) printf '%s\0' `+shellQuote(tc.record)+`; exit 0;; esac`)

			snap := g.snapshot()

			assert.False(t, snap.PathsComplete)
			assert.Equal(t, wantDefaultExcludes, snap.Selection.Excludes)
		})
	}

	// git cannot list a directory with mode 0000, so it prints no record
	// for an untracked file in it. When each index entry there is a skipped
	// entry, no other entry puts the directory in the census. So the
	// parents of a skipped entry join the census, and such a directory
	// makes the path set incomplete (manager ruling S2, option (c)). The
	// rows are the reviewer probes P8 and P3. A --file path of a parent
	// selects the same skipped entry, but it does not name the directory.
	// So there too, only the skipped entry puts the directory in the census.
	for _, tc := range []struct {
		probe     string
		suffix    string   // the name part between the rule and the probe
		paths     []string // the --file paths; none for the default selection
		dir       string   // the directory that loses every mode bit
		entry     string   // the skipped entry
		untracked string   // the untracked file that git cannot see
	}{
		{probe: "P8", dir: "src/x", entry: "src/x/a.go", untracked: "src/x/new.go"},
		{probe: "P3", dir: "generated", entry: "generated/a.txt", untracked: "generated/b.txt"},
		{
			probe: "P8", suffix: " under --file of its parent", paths: []string{"src"},
			dir: "src/x", entry: "src/x/a.go", untracked: "src/x/new.go",
		},
	} {
		excludes := withSkipped(wantDefaultExcludes)
		if len(tc.paths) > 0 {
			excludes = withSkipped(wantExplicitExcludes)
		}

		for _, bit := range []string{"--skip-worktree", "--assume-unchanged"} {
			t.Run("an unreadable directory whose index entries are all skipped entries is incomplete"+tc.suffix+" ("+tc.probe+", "+bit+")", func(t *testing.T) {
				g := verifyRepo(t, mixedYAML)
				if tc.probe == "P8" {
					g.write(tc.entry, "package x\n")
					g.commitAll("x")
				}

				g.git("update-index", bit, tc.entry)
				g.write(tc.untracked, "// TODO(x)\n")
				require.NoError(t, os.Chmod(g.path(tc.dir), 0))
				t.Cleanup(func() { _ = os.Chmod(g.path(tc.dir), 0o755) })

				snap := g.snapshot(tc.paths...)

				assert.Empty(t, changeList(snap), "git prints no record for the untracked file")
				assert.False(t, snap.PathsComplete)
				assert.Equal(t, []string{tc.dir}, snap.UnreadableDirectories)
				assert.Equal(t, excludes, snap.Selection.Excludes)
				assert.Equal(t, []string{LimitationGitStderr}, snap.Limitations)

				v := g.verify(tc.paths...)

				assert.Equal(t, FreshnessFresh, v.Freshness)
				assert.False(t, v.PathsComplete)
				assert.Equal(t, SummaryIncomplete, v.Summary.State)
				assert.Equal(t, []string{"1:unknown/incomplete-paths", "2:unknown/incomplete-paths"}, outcomes(v))
				require.ErrorIs(t, v.ExitError(), ErrIncomplete)
				assert.Contains(t, renderText(v), "  path set: incomplete (unreadable directory: "+tc.dir+")\n")
			})
		}
	}

	// A sparse checkout removes each directory outside its cone. Such a
	// parent of a skipped entry costs one Lstat and drops out before
	// max-census-directories counts it, so a small cone in a large tree
	// stays complete. Here the census holds two directories: . and src.
	t.Run("a sparse checkout keeps its absent directories out of the census bound", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		for i := range 5 {
			g.write(fmt.Sprintf("d%d/e/f.txt", i), "f\n")
		}

		g.commitAll("outside the cone")
		g.git("sparse-checkout", "set", "src")
		require.NoDirExists(t, g.path("d0"), "git removes each directory outside the cone")

		lim := DefaultLimits()
		lim.MaxCensusDirectories = 2

		snap := g.snapshotWith(lim)

		assert.True(t, snap.PathsComplete)
		assert.Empty(t, snap.Limits)
		assert.Empty(t, snap.UnreadableDirectories)
		assert.Equal(t, withSkipped(wantDefaultExcludes), snap.Selection.Excludes)

		v := g.verifyCtx(context.Background(), lim)

		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
	})

	// A parent of a skipped entry below a link is a census directory, but
	// the census never opens a link, and a skipped entry never joins the
	// rule of ruling 3. So the path set stays complete, as in the rows of
	// ruling S2 above. Here generated/x holds only a skipped entry.
	t.Run("a skipped entry two directories below a link keeps the path set complete", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("generated/x/b.txt", "b\n")
		g.commitAll("x")
		g.git("update-index", "--skip-worktree", "generated/a.txt", "generated/x/b.txt")

		linkGenerated(t, g, 0)

		snap := g.snapshot()

		assert.Equal(t, []string{"add generated"}, changeList(snap))
		assert.True(t, snap.PathsComplete)
		assert.Empty(t, snap.UnreadableDirectories, "the census opens nothing through the link")
		assert.Equal(t, withSkipped(wantDefaultExcludes), snap.Selection.Excludes)

		v := g.verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.True(t, v.PathsComplete)
		assert.Equal(t, []string{"1:violation", "2:not-applicable"}, outcomes(v))
	})

	// A link is not absent, so a parent of a skipped entry reached through
	// a link stays in the census. These rows pin the contract words "and
	// it counts" of ruling S2, option (c). A filter that keeps only a real
	// directory drops the link and gives no hit (mutant m48 of the P1 log).
	// With the default selection the census holds ., docs, generated, and
	// src. Under --file generated it holds . and generated. The census
	// still opens nothing through the link.
	for _, tc := range []struct {
		suffix string   // the name part after the rule
		paths  []string // the --file paths; none for the default selection
		limit  int      // max-census-directories, one below the census size
	}{
		{limit: 3},
		{suffix: " under --file of the link", paths: []string{"generated"}, limit: 1},
	} {
		t.Run("a parent of a skipped entry reached through a link counts against max-census-directories"+tc.suffix, func(t *testing.T) {
			g := verifyRepo(t, mixedYAML)
			g.git(skipWorktree...)

			linkGenerated(t, g, 0o755)

			lim := DefaultLimits()
			lim.MaxCensusDirectories = tc.limit

			snap := g.snapshotWith(lim, tc.paths...)

			assert.False(t, snap.PathsComplete)
			assert.Equal(t, []LimitHit{{Name: LimitMaxCensusDirectories, Value: int64(tc.limit), Paths: 1}}, snap.Limits,
				"the link generated counts as one census directory")
			assert.Empty(t, snap.UnreadableDirectories, "the census opens nothing through the link")
		})
	}
}

// TestLessonCheckSnapshotDigest proves the lesson-snapshot/1 composition
// of contract K4 and the revalidation inputs that a snapshot keeps.
func TestLessonCheckSnapshotDigest(t *testing.T) {
	// One change written out part by part. Each part is "<length>:<bytes>"
	// (contracts §1.2), so the test does not call compose.
	snap := &Snapshot{
		Schema:   SnapshotSchema,
		Boundary: BoundaryWorkingTree,
		Baseline: Baseline{Kind: BaselineHead, Commit: "abc"},
		Selection: Selection{
			Kind: SelectionExplicitPaths, Description: wantExplicitHead,
			Paths: []string{"generated"}, Excludes: wantExplicitExcludes,
		},
		Changes: []Change{{
			Path: "generated/b.txt", OldPath: "generated/a.txt", Kind: ChangeMove,
			Before:         Side{Type: ObjectFile, Mode: modeFile, Oid: oidA, SHA256: "s1", Size: 4, Content: ContentNotRead},
			After:          Side{Type: ObjectFile, Mode: modeExec, SHA256: "s2", Size: 4, Content: ContentAvailable},
			ContentChanged: true,
			Move:           MoveIdenticalContent,
		}},
		PathsComplete: true,
		Limitations:   []string{LimitationMovePairingUnavailable},
	}

	composition := "17:lesson-snapshot/1" + "12:working-tree" + "4:head" + "3:abc" + "0:" +
		"14:explicit-paths" + "1:1" + "9:generated" + "1:1" +
		"15:generated/b.txt" + "15:generated/a.txt" + "4:move" +
		"4:file" + "6:100644" + "2:s1" +
		"4:file" + "6:100755" + "2:s2" +
		"1:1" + "1:1" + "24:move-pairing-unavailable"
	sum := sha256.Sum256([]byte(composition))

	t.Run("the composition of the K4 parts", func(t *testing.T) {
		assert.Equal(t, hex.EncodeToString(sum[:]), snapshotDigest(snap))

		// The frozen value (shasum -a 256 of the composition above), so a
		// change of the composition test itself also shows.
		assert.Equal(t, "2e9577c5339a0ea96d6a73ee2d36f59c2a94eefaf596b09c8b557554dcf16b1c", snapshotDigest(snap))
	})

	t.Run("object ids, flags, sizes, content states, and limit hits are not parts", func(t *testing.T) {
		other := *snap
		other.Changes = []Change{snap.Changes[0]}
		other.Changes[0].Before.Oid = oidB
		other.Changes[0].After.Oid = oidC
		other.Changes[0].Before.Size = 99
		other.Changes[0].After.Content = ContentTooLarge
		other.Changes[0].ContentChanged = false
		other.Changes[0].Move = MoveGitRename
		other.Changes[0].Limitations = []string{LimitationUnmerged}
		other.Limits = []LimitHit{{Name: LimitMaxFileBytes, Value: 1, Paths: 1}}
		other.Selection.Description = "another text"

		assert.Equal(t, snapshotDigest(snap), snapshotDigest(&other))
	})

	t.Run("each K4 part changes the digest", func(t *testing.T) {
		for name, edit := range map[string]func(s *Snapshot){
			"baseline commit":    func(s *Snapshot) { s.Baseline.Commit = "abd" },
			"pending sha":        func(s *Snapshot) { s.Baseline.PendingSHA = "p" },
			"selection kind":     func(s *Snapshot) { s.Selection.Kind = SelectionDefault },
			"selection paths":    func(s *Snapshot) { s.Selection.Paths = []string{"generated", "src"} },
			"change path":        func(s *Snapshot) { s.Changes[0].Path = "generated/c.txt" },
			"change kind":        func(s *Snapshot) { s.Changes[0].Kind = ChangeModify },
			"after content hash": func(s *Snapshot) { s.Changes[0].After.SHA256 = "s3" },
			"after mode":         func(s *Snapshot) { s.Changes[0].After.Mode = modeFile },
			"before type":        func(s *Snapshot) { s.Changes[0].Before.Type = ObjectSymlink },
			"paths complete":     func(s *Snapshot) { s.PathsComplete = false },
			"limitations":        func(s *Snapshot) { s.Limitations = nil },
		} {
			other := *snap
			other.Changes = []Change{snap.Changes[0]}
			other.Selection.Paths = append([]string(nil), snap.Selection.Paths...)
			edit(&other)

			assert.NotEqual(t, snapshotDigest(snap), snapshotDigest(&other), name)
		}
	})

	t.Run("a real tree gives a stable digest that follows its content", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.commitAll("a")
		g.write("a.txt", "b\n")
		g.write("u.txt", "u\n")

		first := g.snapshot()
		second := g.snapshot()
		assert.Equal(t, first.SHA256, second.SHA256)
		assert.Equal(t, first.statusSHA256, second.statusSHA256)
		assert.Len(t, first.statusSHA256, 64)

		// A new modification time keeps the digest, because the digest
		// holds content hashes only. The identity records the new time.
		later := time.Now().Add(time.Hour)
		require.NoError(t, os.Chtimes(g.path("u.txt"), later, later))

		touched := g.snapshot()
		assert.Equal(t, first.SHA256, touched.SHA256)
		assert.NotEqual(t, first.identities, touched.identities)

		g.write("u.txt", "v\n")
		assert.NotEqual(t, first.SHA256, g.snapshot().SHA256)
	})

	t.Run("the identities name every inspected path with the hash of each read", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.commitAll("a")
		g.write("a.txt", "b\n")
		g.write("u.txt", "u\n")

		snap := g.snapshot()

		byPath := map[string]fileID{}
		for _, id := range snap.identities {
			byPath[id.path] = id
		}

		require.Contains(t, byPath, "u.txt")
		assert.Equal(t, pathPresent, byPath["u.txt"].state)
		assert.Equal(t, sha256Hex("u\n"), byPath["u.txt"].sha256)
		assert.Equal(t, int64(2), byPath["u.txt"].size)
		assert.NotZero(t, byPath["u.txt"].mtimeNS)
		assert.Equal(t, sha256Hex("b\n"), byPath["a.txt"].sha256)
	})

	t.Run("an explicit selection and the default selection differ", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.commitAll("a")

		assert.NotEqual(t, g.snapshot().SHA256, g.snapshot("a.txt").SHA256)
	})
}
