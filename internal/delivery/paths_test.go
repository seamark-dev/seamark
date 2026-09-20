package delivery

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workspace returns a temporary root with a few real files. The root
// keeps the spelling of t.TempDir, which on macOS sits behind a link.
func workspace(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	for _, file := range []string{"a.go", "api/handler.go", "db/query.go"} {
		path := filepath.Join(root, file)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("package x\n"), 0o644))
	}

	return root
}

func TestNormalizePathsResolvesAgainstTheEventDirectory(t *testing.T) {
	root := workspace(t)

	got := NormalizePaths(root, filepath.Join(root, "api"), []string{
		"handler.go",                             // relative to the event directory
		"../db/query.go",                         // leaves the directory, stays inside
		filepath.Join(root, "a.go"),              // absolute
		"./handler.go",                           // a second spelling of the first path
		filepath.Join(root, "api", "..", "a.go"), // a second spelling of the third
	})

	assert.Equal(t, []string{"api/handler.go", "db/query.go", "a.go"}, got.Files,
		"first-appearance order, one entry per file")
	assert.Zero(t, got.RejectedCount)

	// No event directory: a relative path resolves against the root.
	got = NormalizePaths(root, "", []string{"api/handler.go"})
	assert.Equal(t, []string{"api/handler.go"}, got.Files)

	// A relative event directory is relative to the root too.
	got = NormalizePaths(root, "api", []string{"handler.go"})
	assert.Equal(t, []string{"api/handler.go"}, got.Files)
}

func TestNormalizePathsRejectsAPathBehindAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	// The locked directory hides a link that leaves the workspace. The
	// function cannot see the link, so the real location is unknown.
	root := workspace(t)
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(locked, "ext")))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got := NormalizePaths(root, root, []string{"locked/ext/hosts", "a.go/child.go"})

	assert.Equal(t, []string{"a.go/child.go"}, got.Files,
		"a regular file in the ancestor position is an absent path, not a hidden one")
	assert.Equal(t, []RejectedPath{{Path: "locked/ext/hosts", Reason: RejectUnresolvable}}, got.Rejected)
}

func TestNormalizePathsKeepsAbsentFiles(t *testing.T) {
	// New, deleted, and moved files are in scope by path alone.
	root := workspace(t)

	got := NormalizePaths(root, root, []string{
		"api/new_file.go",
		"brand/new/tree/file.go",
		filepath.Join(root, "db", "deleted.go"),
	})

	assert.Equal(t, []string{"api/new_file.go", "brand/new/tree/file.go", "db/deleted.go"}, got.Files)
	assert.Zero(t, got.RejectedCount)
}

func TestNormalizePathsRejectsWhatIsNotAWorkspaceFile(t *testing.T) {
	root := workspace(t)
	outside := t.TempDir()

	got := NormalizePaths(root, root, []string{
		"",
		"bad\x00name.go",
		"../escape.go",
		filepath.Join(outside, "other.go"),
		root,
		"api/handler.go",
	})

	assert.Equal(t, []string{"api/handler.go"}, got.Files, "a rejected path does not stop the others")
	assert.Equal(t, 5, got.RejectedCount)
	assert.Equal(t, []RejectedPath{
		{Path: "", Reason: RejectMalformed},
		{Path: "bad\x00name.go", Reason: RejectMalformed},
		{Path: "../escape.go", Reason: RejectOutside},
		{Path: filepath.Join(outside, "other.go"), Reason: RejectOutside},
		{Path: root, Reason: RejectOutside},
	}, got.Rejected, "an outside path is reported, not dropped without a trace")
}

func TestNormalizePathsDoesNotFollowLinksOutOfTheWorkspace(t *testing.T) {
	root := workspace(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package x\n"), 0o644))

	require.NoError(t, os.Symlink(outside, filepath.Join(root, "vendor_link")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "linked.go")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "nowhere.go"), filepath.Join(root, "dangling_out.go")))
	require.NoError(t, os.Symlink(filepath.Join(root, "api", "nowhere.go"), filepath.Join(root, "dangling_in.go")))
	require.NoError(t, os.Symlink(filepath.Join(root, "api"), filepath.Join(root, "current")))
	require.NoError(t, os.Symlink("api/../db", filepath.Join(root, "relative")))
	require.NoError(t, os.Symlink("loop_b", filepath.Join(root, "loop_a")))
	require.NoError(t, os.Symlink("loop_a", filepath.Join(root, "loop_b")))

	got := NormalizePaths(root, root, []string{
		"vendor_link/secret.go", // a linked directory that leaves the workspace
		"vendor_link/new.go",    // a new file behind the same link
		"linked.go",             // a linked file that leaves the workspace
		"dangling_out.go",       // a write through this link creates a file outside
		"loop_a/file.go",        // a loop of links has no real location
		"current/handler.go",    // a link that stays inside
		"relative/query.go",     // a relative link target with a parent step
		"dangling_in.go",        // a write through this link creates a file inside
	})

	assert.Equal(t, []string{"api/handler.go", "db/query.go", "api/nowhere.go"}, got.Files,
		"an inside link resolves to the real file, because lessons follow the real file")
	assert.Equal(t, []RejectedPath{
		{Path: "vendor_link/secret.go", Reason: RejectOutside},
		{Path: "vendor_link/new.go", Reason: RejectOutside},
		{Path: "linked.go", Reason: RejectOutside},
		{Path: "dangling_out.go", Reason: RejectOutside},
		{Path: "loop_a/file.go", Reason: RejectUnresolvable},
	}, got.Rejected)
}

func TestNormalizePathsAppliesAParentStepAfterTheLink(t *testing.T) {
	// "link/.." is the parent of the link TARGET. A lexical clean turns
	// "link/../target.go" into "target.go" inside the workspace, while
	// the edit reaches a file next to the external directory.
	root := workspace(t)
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "existing.go"), []byte("package x\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(external, "nested"), filepath.Join(root, "link")))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "api", "v2"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "api", "v2"), filepath.Join(root, "current")))

	got := NormalizePaths(root, root, []string{
		"link/../existing.go",                         // an existing file outside
		"link/../absent.go",                           // an absent file outside
		filepath.Join(root, "link") + "/../absent.go", // the same through an absolute path
		"newdir/../link/../absent.go",                 // an absent directory before the link
		"current/../handler.go",                       // inside: api/handler.go, not handler.go
		"current/../new.go",                           // inside and absent: api/new.go
		"api/../a.go",                                 // no link: the plain parent step
		"..notes.go",                                  // a name that only starts with two dots
	})

	assert.Equal(t, []string{"api/handler.go", "api/new.go", "a.go", "..notes.go"}, got.Files,
		"a parent step after an inside link selects the region of the real file")
	assert.Equal(t, []RejectedPath{
		{Path: "link/../existing.go", Reason: RejectOutside},
		{Path: "link/../absent.go", Reason: RejectOutside},
		{Path: filepath.Join(root, "link") + "/../absent.go", Reason: RejectOutside},
		{Path: "newdir/../link/../absent.go", Reason: RejectOutside},
	}, got.Rejected)

	// The event directory follows the same rule.
	got = NormalizePaths(root, filepath.Join(root, "link"), []string{"../absent.go", "inside_target.go"})
	assert.Empty(t, got.Files, "the event directory is a link to an external directory")
	assert.Equal(t, 2, got.RejectedCount)

	got = NormalizePaths(root, "current/..", []string{"handler.go"})
	assert.Equal(t, []string{"api/handler.go"}, got.Files)
}

func TestNormalizePathsAcceptsARootBehindALink(t *testing.T) {
	// /tmp on macOS is a link. A client can report the resolved path
	// while the workspace flag holds the linked spelling, or the reverse.
	target := workspace(t)
	linked := filepath.Join(t.TempDir(), "link-to-root")
	require.NoError(t, os.Symlink(target, linked))

	got := NormalizePaths(linked, "", []string{filepath.Join(target, "api", "handler.go")})
	assert.Equal(t, []string{"api/handler.go"}, got.Files)

	got = NormalizePaths(target, "", []string{filepath.Join(linked, "api", "new.go")})
	assert.Equal(t, []string{"api/new.go"}, got.Files)
}

func TestNormalizePathsBoundsTheRejectionList(t *testing.T) {
	root := workspace(t)

	var paths []string
	for i := range maxRejections + 5 {
		paths = append(paths, fmt.Sprintf("../out-%d.go", i))
	}

	got := NormalizePaths(root, root, paths)

	assert.Empty(t, got.Files)
	assert.Len(t, got.Rejected, maxRejections, "the diagnostic stays small for a huge patch")
	assert.Equal(t, maxRejections+5, got.RejectedCount, "the count stays exact")
}
