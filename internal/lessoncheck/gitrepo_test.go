package lessoncheck

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file holds the temporary git repositories of the snapshot tests.
// Every repository lives under t.TempDir(), so no test reads or writes
// the checkout that runs it.

// isolateGit hides the global and the system git configuration from
// every git process of the test, the producer's processes included.
// ReadWorkingTree passes os.Environ() to git, so a user setting such as
// core.fsmonitor or status.showUntrackedFiles could otherwise change the
// snapshot. The identity variables let a commit run in every repository
// of the test, also in a submodule clone without its own configuration.
// LC_ALL=C keeps the git messages in English, because some tests compare
// the stderr line of git status. A git with gettext translates them in
// another locale. A test that calls t.Setenv cannot call t.Parallel,
// and these tests do not call it.
func isolateGit(t *testing.T) {
	t.Helper()

	t.Setenv("LC_ALL", "C")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	t.Setenv("GIT_AUTHOR_NAME", "Seamark Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@seamark.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Seamark Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@seamark.invalid")
}

// gitRepo is one temporary repository.
type gitRepo struct {
	t    *testing.T
	root string
}

// newGitRepo initializes a repository in a new temporary directory.
// initArgs go to `git init`, for example "--object-format=sha256".
func newGitRepo(t *testing.T, initArgs ...string) *gitRepo {
	t.Helper()
	isolateGit(t)

	g := &gitRepo{t: t, root: t.TempDir()}
	g.git(append([]string{"init", "-q", "-b", "main"}, initArgs...)...)

	return g
}

// git runs git in the repository and returns its stdout. A failure
// stops the test, because every fixture step must hold.
func (g *gitRepo) git(args ...string) string {
	g.t.Helper()

	return runGitIn(g.t, g.root, args...)
}

// runGitIn runs git in dir and returns its stdout.
func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)

	return string(out)
}

// path returns the absolute form of a repository-relative path.
func (g *gitRepo) path(rel string) string {
	return filepath.Join(g.root, filepath.FromSlash(rel))
}

// write writes content to a repository-relative path. It creates the
// parent directories.
func (g *gitRepo) write(rel, content string) {
	g.t.Helper()

	require.NoError(g.t, os.MkdirAll(filepath.Dir(g.path(rel)), 0o755))
	require.NoError(g.t, os.WriteFile(g.path(rel), []byte(content), 0o644))
}

// remove deletes a path and everything below it.
func (g *gitRepo) remove(rel string) {
	g.t.Helper()

	require.NoError(g.t, os.RemoveAll(g.path(rel)))
}

// chmodUntilCleanup sets the mode of name to mode, and sets it to
// restore when the test ends. A directory without the read or search bit
// stops the removal of t.TempDir(), so each test that removes a bit must
// set it again.
func chmodUntilCleanup(t *testing.T, name string, mode, restore fs.FileMode) {
	t.Helper()

	require.NoError(t, os.Chmod(name, mode))
	t.Cleanup(func() { _ = os.Chmod(name, restore) })
}

// symlink creates a symbolic link at rel with the given target.
func (g *gitRepo) symlink(target, rel string) {
	g.t.Helper()

	require.NoError(g.t, os.MkdirAll(filepath.Dir(g.path(rel)), 0o755))
	require.NoError(g.t, os.Symlink(target, g.path(rel)))
}

// commitAll stages every path and commits it.
func (g *gitRepo) commitAll(message string) {
	g.t.Helper()

	g.git("add", "-A")
	g.git("commit", "-q", "-m", message)
}

// head returns the commit id of HEAD.
func (g *gitRepo) head() string {
	g.t.Helper()

	return strings.TrimSpace(g.git("rev-parse", "HEAD"))
}

// blobID returns the git object id of HEAD:<rel>.
func (g *gitRepo) blobID(rel string) string {
	g.t.Helper()

	return strings.TrimSpace(g.git("rev-parse", "HEAD:"+rel))
}

// snapshot reads the working-tree snapshot with the default limits. The
// result must be a valid snapshot.
func (g *gitRepo) snapshot(paths ...string) *Snapshot {
	g.t.Helper()

	return g.snapshotWith(DefaultLimits(), paths...)
}

// snapshotWith reads the working-tree snapshot with the given limits.
func (g *gitRepo) snapshotWith(lim Limits, paths ...string) *Snapshot {
	g.t.Helper()

	snap, err := ReadWorkingTree(context.Background(), g.root, paths, lim)
	require.NoError(g.t, err)
	requireValidSnapshot(g.t, snap)

	return snap
}

// requireValidSnapshot checks the invariants that every snapshot holds:
// each enum value is in its closed list, the changes are sorted and
// non-nil, and the digest is the digest of the fields. A producer bug
// such as an empty content state fails here, in every test.
func requireValidSnapshot(t *testing.T, snap *Snapshot) {
	t.Helper()

	require.Equal(t, SnapshotSchema, snap.Schema)
	require.Equal(t, BoundaryWorkingTree, snap.Boundary)
	require.Contains(t, []BaselineKind{BaselineHead, BaselineEmptyTree}, snap.Baseline.Kind)
	require.NotNil(t, snap.Changes, "changes encode as [], never null")
	require.NotNil(t, snap.Selection.Excludes)
	require.True(t, slices.IsSorted(snap.Selection.Paths))
	require.True(t, slices.IsSorted(snap.Limitations))
	requireValidUnreadable(t, snap.PathsComplete, snap.UnreadableDirectories, snap.UnreadableDirectoriesOmitted)

	for i, ch := range snap.Changes {
		require.Contains(t, []ChangeKind{ChangeAdd, ChangeDelete, ChangeModify, ChangeMove}, ch.Kind, ch.Path)
		require.Contains(t, []MoveDetection{MoveNone, MoveGitRename, MoveIdenticalContent}, ch.Move, ch.Path)
		requireValidSide(t, ch.Path, ch.Before)
		requireValidSide(t, ch.Path, ch.After)

		for _, l := range ch.Limitations {
			require.Contains(t, []string{LimitationUnmerged, LimitationPathThroughLink}, l, ch.Path)
		}

		if i > 0 {
			prev := snap.Changes[i-1]
			require.True(t, prev.Path < ch.Path || (prev.Path == ch.Path && prev.OldPath < ch.OldPath),
				"changes must be sorted by path and old path: %q before %q", prev.Path, ch.Path)
		}
	}

	for _, l := range snap.Limitations {
		require.Contains(t, []string{LimitationMovePairingUnavailable, LimitationSubmoduleContentNotInspected, LimitationGitStderr}, l)
	}

	require.Equal(t, snapshotDigest(snap), snap.SHA256)
}

// requireValidUnreadable checks the census evidence of a snapshot or a
// verification: sorted, within max-unreadable-directories, a count only
// beside a full list, and set only on an incomplete path set.
func requireValidUnreadable(t *testing.T, complete bool, dirs []string, omitted int) {
	t.Helper()

	require.True(t, slices.IsSorted(dirs))
	require.LessOrEqual(t, len(dirs), maxUnreadableDirectories)
	require.GreaterOrEqual(t, omitted, 0)

	if omitted > 0 {
		require.Len(t, dirs, maxUnreadableDirectories, "the cap cuts only a full list")
	}

	if len(dirs) > 0 {
		require.False(t, complete, "an unreadable directory makes the path set incomplete")
	}
}

// requireValidSide checks one side of a change.
func requireValidSide(t *testing.T, path string, side Side) {
	t.Helper()

	require.Contains(t, []ObjectType{ObjectAbsent, ObjectFile, ObjectSymlink, ObjectGitlink, ObjectOther}, side.Type, path)
	require.Contains(t, []ContentState{
		ContentAvailable, ContentBinary, ContentInvalidUTF8, ContentTooLarge, ContentUnreadable, ContentNotRead,
	}, side.Content, path)

	if side.Content == ContentAvailable {
		require.Equal(t, sha256Hex(string(side.text)), side.SHA256, "an available side carries the SHA-256 of its text: %s", path)
	} else {
		require.Nil(t, side.text, "only an available side carries text: %s", path)
	}

	if side.Type == ObjectAbsent {
		require.Equal(t, Side{Type: ObjectAbsent, Content: ContentNotRead}, side, path)
	}
}

// changeAt returns the change whose path is path. The test fails when no
// change or more than one change has the path.
func changeAt(t *testing.T, snap *Snapshot, path string) Change {
	t.Helper()

	var found []Change

	for _, ch := range snap.Changes {
		if ch.Path == path {
			found = append(found, ch)
		}
	}

	require.Len(t, found, 1, "changes at %q in %v", path, changeList(snap))

	return found[0]
}

// changeList returns "kind path" or "kind old->path" for each change, in
// snapshot order. Tests compare it to state the whole change set.
func changeList(snap *Snapshot) []string {
	out := make([]string, 0, len(snap.Changes))

	for _, ch := range snap.Changes {
		if ch.OldPath != "" {
			out = append(out, string(ch.Kind)+" "+ch.OldPath+"->"+ch.Path)

			continue
		}

		out = append(out, string(ch.Kind)+" "+ch.Path)
	}

	return out
}

// skipWithoutSymlinks skips a test on a platform where the test cannot
// create symbolic links without extra rights.
func skipWithoutSymlinks(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on windows")
	}
}

// gitShim puts a git wrapper first on PATH for the rest of the test. The
// wrapper is a shell script: script runs before it hands the call to
// the real git with `exec "$real" "$@"`. It can log the call, fail a
// command, or delay one. The variable $real holds the real git path.
func gitShim(t *testing.T, script string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a shell script")
	}

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	body := "#!/bin/sh\nreal=" + shellQuote(gitPath) + "\n" + script + "\nexec \"$real\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0o755))

	// The first run of a new executable can take a moment on some hosts,
	// for example while macOS scans it. This run takes that time before a
	// test starts its deadline.
	out, err := exec.Command(filepath.Join(dir, "git"), "--version").CombinedOutput()
	require.NoError(t, err, "%s", out)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
