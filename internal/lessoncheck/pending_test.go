package lessoncheck

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// The tests of the pending baselines. Every root is a temporary
// directory, so no test reads or writes the checkout that runs it.

// The raw IDs of the test calls. A record and an audit line hold only
// their digests, so the raw IDs never persist.
const (
	rawSession  = "session-raw-4f1c"
	rawMatch    = "toolu-raw-9a2e"
	rawReceiver = "agent-raw-77b0"
)

// identityRoot is the identity root that the test digests hash over.
// The pending functions never read it, so it needs no directory.
const identityRoot = "/identity/root"

// posixModes is false where the platform has no POSIX modes. The mode
// assertions then skip, because the writers are best effort there.
const posixModes = runtime.GOOS != "windows"

// requireMode checks the permission bits of path where the platform
// enforces them.
func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	if !posixModes {
		return
	}

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm(), "mode of %s", path)
}

// pendingFor builds the record of one call from a proposed-edit
// snapshot, as a pre-edit handler does.
func pendingFor(cfg *Config, snap *Snapshot, session, match string) *Pending {
	paths, lims := PendingPathsOf(cfg, snap)

	return &Pending{
		Client:        "claude",
		Tool:          "Edit",
		InstalledMode: "enforce",
		SessionSHA:    reviews.SessionDigest(identityRoot, session),
		MatchSHA:      reviews.MatchDigest(identityRoot, session, match),
		ContextSHA:    reviews.ContextDigest(identityRoot, "claude", rawReceiver),
		ConfigSHA256:  cfg.SHA256,
		PathsComplete: snap.PathsComplete,
		Paths:         paths,
		Limitations:   lims,
	}
}

// simplePending is a record with one path under src/ and its content.
func simplePending(t *testing.T, session, match string) *Pending {
	t.Helper()

	snap := snapshotOf(BoundaryProposedEdit, modifyChange("src/a.go", fileSide("package a\n"), fileSide("package a\n// x\n")))

	return pendingFor(parseValid(t, mixedYAML), snap, session, match)
}

// pendingFile returns the record path of a call under root.
func pendingFile(root string, p *Pending) string {
	return filepath.Join(root, ".seamark", "checks", "pending", PendingKey(p.Client, p.MatchSHA)+".json")
}

// readAuditLines returns the lines of the audit stream under root.
func readAuditLines(t *testing.T, root string) []string {
	t.Helper()

	f, err := os.Open(auditPath(root))
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	defer func() { _ = f.Close() }()

	var lines []string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	for sc.Scan() {
		lines = append(lines, sc.Text())
	}

	require.NoError(t, sc.Err())

	return lines
}

// readAudit returns the decoded records of the audit stream under root.
func readAudit(t *testing.T, root string) []AuditRecord {
	t.Helper()

	var recs []AuditRecord

	for _, line := range readAuditLines(t, root) {
		var rec AuditRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)

		recs = append(recs, rec)
	}

	return recs
}

// ageFile sets the modification time of path to age ago.
func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()

	old := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, old, old))
}

// TestLessonCheckPendingWriteAndConsume proves the write, read, and
// delete of one record. It covers the layout and the modes, the key,
// the content rule, and the temporary file and the rename. It also
// covers the refusal of a record that names another call.
func TestLessonCheckPendingWriteAndConsume(t *testing.T) {
	cfg := parseValid(t, mixedYAML)

	snap := snapshotOf(BoundaryProposedEdit,
		modifyChange("src/a.go", fileSide("package a\n"), fileSide("package a\n// note\n")),
		modifyChange("generated/a.txt", fileSide("asset\n"), fileSide("asset 2\n")),
		modifyChange("docs/readme.md", fileSide("doc\n"), fileSide("doc 2\n")),
		addChange("src/new.go", fileSide("package a\n")),
		modifyChange("src/empty.go", fileSide(""), fileSide("package a\n")),
	)

	t.Run("only the paths of executable pins are kept", func(t *testing.T) {
		paths, lims := PendingPathsOf(cfg, snap)
		assert.Nil(t, lims)

		require.Len(t, paths, 4, "docs/readme.md has no executable pin")
		assert.Equal(t, []string{"src/a.go", "generated/a.txt", "src/new.go", "src/empty.go"},
			[]string{paths[0].Path, paths[1].Path, paths[2].Path, paths[3].Path})

		// The pattern pin scopes src/, so its before text goes into the
		// record. The forbid pin reads paths only.
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("package a\n")), paths[0].ContentB64)
		assert.Empty(t, paths[1].ContentB64, "a forbid-path-change/1 pin needs no content")
		assert.Empty(t, paths[2].ContentB64, "an add has no before content")
		assert.Empty(t, paths[3].ContentB64, "empty content is omitted")
		assert.Equal(t, ContentAvailable, paths[3].Before.Content, "available with size 0 marks empty content")
		assert.Zero(t, paths[3].Before.Size)

		assert.Equal(t, fileSide("package a\n// note\n").SHA256, paths[0].ProjectedAfterSHA256)
		assert.Nil(t, paths[0].Before.text, "the record holds content in content_b64 only")
	})

	t.Run("a path without projection gives the limitation", func(t *testing.T) {
		ch := modifyChange("generated/b.txt", fileSide("b\n"), Side{Type: ObjectFile, Content: ContentNotRead})
		ch.Limitations = []string{LimitationNoProjection, LimitationPathThroughLink}

		paths, lims := PendingPathsOf(cfg, snapshotOf(BoundaryProposedEdit, ch))
		require.Len(t, paths, 1)
		assert.Empty(t, paths[0].ProjectedAfterSHA256)
		assert.Equal(t, []string{LimitationNoProjection, LimitationPathThroughLink}, lims)
	})

	t.Run("the record is written, read, and deleted", func(t *testing.T) {
		root := t.TempDir()
		p := pendingFor(cfg, snap, rawSession, rawMatch)

		require.NoError(t, WritePending(context.Background(), root, p, DefaultLimits()))
		assert.Empty(t, p.Schema, "WritePending never changes the caller's record")
		assert.Empty(t, p.CreatedAt)

		// The key is the compose digest of the label, the client, and
		// the match digest. The test builds it by hand, so a change of the
		// composer shows here.
		composed := fmt.Sprintf("%d:%s%d:%s%d:%s", len(PendingSchema), PendingSchema, len("claude"), "claude", len(p.MatchSHA), p.MatchSHA)
		sum := sha256.Sum256([]byte(composed))
		assert.Equal(t, hex.EncodeToString(sum[:]), PendingKey("claude", p.MatchSHA))

		checks := filepath.Join(root, ".seamark", "checks")
		requireMode(t, checks, 0o700)
		requireMode(t, filepath.Join(checks, "pending"), 0o700)
		requireMode(t, filepath.Join(checks, ".gitignore"), 0o600)
		requireMode(t, pendingFile(root, p), 0o600)

		ignore, err := os.ReadFile(filepath.Join(checks, ".gitignore"))
		require.NoError(t, err)
		assert.Equal(t, "*\n", string(ignore))

		entries, err := os.ReadDir(filepath.Join(checks, "pending"))
		require.NoError(t, err)
		require.Len(t, entries, 1, "the rename leaves no temporary file")

		data, err := os.ReadFile(pendingFile(root, p))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(data), `{"schema":"lesson-check-pending/1","created_at":"`), "the schema is the first key")

		for _, raw := range []string{rawSession, rawMatch, rawReceiver} {
			assert.NotContains(t, string(data), raw, "a record holds digests, never a raw ID")
		}

		assert.NoDirExists(t, filepath.Join(root, ".seamark", "private"))

		got, err := ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.NoError(t, err)

		_, err = time.Parse(time.RFC3339, got.CreatedAt)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(got.CreatedAt, "Z"))

		fileSum := sha256.Sum256(data)
		assert.Equal(t, hex.EncodeToString(fileSum[:]), got.fileSHA256)

		want := *p
		want.Schema = PendingSchema
		want.CreatedAt = got.CreatedAt
		want.fileSHA256 = got.fileSHA256
		assert.Equal(t, &want, got)

		require.NoError(t, DeletePending(root, "claude", p.MatchSHA))
		assert.NoFileExists(t, pendingFile(root, p))

		_, err = ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
		require.NoError(t, DeletePending(root, "claude", p.MatchSHA), "an absent record is not an error")
	})

	t.Run("a record serves its own call only", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, rawMatch)
		require.NoError(t, WritePending(context.Background(), root, p, DefaultLimits()))

		_, err := ReadPending(root, "codex", p.MatchSHA, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending, "another client")

		other := reviews.MatchDigest(identityRoot, rawSession, "toolu-other")
		_, err = ReadPending(root, "claude", other, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending, "another call")

		// A record copied under the key of another call must never serve
		// as the baseline of that call.
		data, err := os.ReadFile(pendingFile(root, p))
		require.NoError(t, err)

		renamed := filepath.Join(filepath.Dir(pendingFile(root, p)), PendingKey("claude", other)+".json")
		require.NoError(t, os.WriteFile(renamed, data, 0o600))

		_, err = ReadPending(root, "claude", other, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
		assert.Contains(t, err.Error(), "another call")
	})

	t.Run("a record of another schema reads as absent", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, rawMatch)
		require.NoError(t, WritePending(context.Background(), root, p, DefaultLimits()))

		data, err := os.ReadFile(pendingFile(root, p))
		require.NoError(t, err)

		newer := strings.Replace(string(data), PendingSchema, "lesson-check-pending/2", 1)
		require.NoError(t, os.WriteFile(pendingFile(root, p), []byte(newer), 0o600))

		_, err = ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)

		require.NoError(t, os.WriteFile(pendingFile(root, p), []byte("{not json"), 0o600))

		_, err = ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
	})

	t.Run("a call without a tool-use ID has no record", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, rawMatch)
		p.MatchSHA = ""

		assert.Empty(t, PendingKey("claude", ""))
		require.Error(t, WritePending(context.Background(), root, p, DefaultLimits()))
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))

		_, err := ReadPending(root, "claude", "", DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
	})

	t.Run("a cancelled handler leaves no file", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, rawMatch)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		require.ErrorIs(t, WritePending(ctx, root, p, DefaultLimits()), context.Canceled)

		entries, err := os.ReadDir(filepath.Join(root, ".seamark", "checks", "pending"))
		require.NoError(t, err)
		assert.Empty(t, entries, "no record and no temporary file")
	})

	t.Run("a reader creates nothing", func(t *testing.T) {
		root := t.TempDir()

		_, err := ReadPending(root, "claude", reviews.MatchDigest(identityRoot, rawSession, rawMatch), DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
		require.NoError(t, DeletePending(root, "claude", reviews.MatchDigest(identityRoot, rawSession, rawMatch)))
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	})

	t.Run("loose modes are tightened on every open", func(t *testing.T) {
		if !posixModes {
			t.Skip("POSIX modes are not enforced on this platform")
		}

		root := t.TempDir()
		checks := filepath.Join(root, ".seamark", "checks")
		require.NoError(t, os.MkdirAll(filepath.Join(checks, "pending"), 0o755))
		require.NoError(t, os.Chmod(checks, 0o755))
		require.NoError(t, os.Chmod(filepath.Join(checks, "pending"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(checks, ".gitignore"), []byte("*\n"), 0o644))
		require.NoError(t, os.Chmod(filepath.Join(checks, ".gitignore"), 0o644))

		require.NoError(t, WritePending(context.Background(), root, simplePending(t, rawSession, rawMatch), DefaultLimits()))

		requireMode(t, checks, 0o700)
		requireMode(t, filepath.Join(checks, "pending"), 0o700)
		requireMode(t, filepath.Join(checks, ".gitignore"), 0o600)
	})
}

// TestLessonCheckPendingSweep proves the crash recovery. The sweep
// removes expired records, unsafe entries, and old temporary files. It
// appends one baseline-discarded record per removed record and leaves
// every other entry. A discarded record is never a baseline.
func TestLessonCheckPendingSweep(t *testing.T) {
	root := t.TempDir()
	lim := DefaultLimits()

	expired := simplePending(t, rawSession, rawMatch)
	fresh := simplePending(t, rawSession, "toolu-fresh")

	require.NoError(t, WritePending(context.Background(), root, expired, lim))
	require.NoError(t, WritePending(context.Background(), root, fresh, lim))

	dir := filepath.Join(root, ".seamark", "checks", "pending")
	ageFile(t, pendingFile(root, expired), 2*time.Hour)

	junk := filepath.Join(dir, strings.Repeat("a", 64)+".json")
	require.NoError(t, os.WriteFile(junk, []byte("{broken"), 0o600))
	ageFile(t, junk, 2*time.Hour)

	oldTmp := filepath.Join(dir, ".tmp-old")
	require.NoError(t, os.WriteFile(oldTmp, []byte("partial"), 0o600))
	ageFile(t, oldTmp, 11*time.Minute)

	youngTmp := filepath.Join(dir, ".tmp-young")
	require.NoError(t, os.WriteFile(youngTmp, []byte("partial"), 0o600))
	ageFile(t, youngTmp, time.Minute)

	foreign := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(foreign, []byte("keep"), 0o600))
	ageFile(t, foreign, 48*time.Hour)

	var linkTarget string

	if runtime.GOOS != "windows" {
		linkTarget = filepath.Join(t.TempDir(), "target.json")
		require.NoError(t, os.WriteFile(linkTarget, []byte("keep"), 0o600))
		require.NoError(t, os.Symlink(linkTarget, filepath.Join(dir, strings.Repeat("b", 64)+".json")))
	}

	removed, err := Sweep(root, CallerHook, lim)
	require.NoError(t, err)

	wantRemoved := 3
	if linkTarget == "" {
		wantRemoved = 2
	}

	assert.Equal(t, wantRemoved, removed)

	assert.NoFileExists(t, pendingFile(root, expired))
	assert.NoFileExists(t, junk)
	assert.NoFileExists(t, oldTmp)
	assert.FileExists(t, pendingFile(root, fresh))
	assert.FileExists(t, youngTmp)
	assert.FileExists(t, foreign, "the sweep leaves names outside its namespace")

	if linkTarget != "" {
		data, err := os.ReadFile(linkTarget)
		require.NoError(t, err)
		assert.Equal(t, "keep", string(data), "the sweep removes the link, never its target")
	}

	recs := readAudit(t, root)
	require.Len(t, recs, wantRemoved, "one record per removed record; a temporary file gets none")

	byCause := map[string][]AuditRecord{}
	for _, rec := range recs {
		assert.Equal(t, AuditBaselineDiscarded, rec.Kind)
		assert.Equal(t, CallerHook, rec.Caller)
		byCause[rec.Discarded] = append(byCause[rec.Discarded], rec)
	}

	require.Len(t, byCause[DiscardExpired], 2)

	var named, unnamed AuditRecord

	for _, rec := range byCause[DiscardExpired] {
		if rec.MatchSHA != "" {
			named = rec
		} else {
			unnamed = rec
		}
	}

	assert.Equal(t, expired.MatchSHA, named.MatchSHA, "the record of an expired call names the call")
	assert.Equal(t, expired.SessionSHA, named.SessionSHA)
	assert.Equal(t, expired.ContextSHA, named.ContextSHA)
	assert.Equal(t, "claude", named.Client)
	assert.Equal(t, "Edit", named.Tool)
	assert.Equal(t, []string{"src/a.go"}, named.Paths)
	assert.Empty(t, unnamed.Client, "a record that does not parse names no call")

	if linkTarget != "" {
		assert.Len(t, byCause[DiscardUnsafe], 1)
	}

	_, err = ReadPending(root, "claude", expired.MatchSHA, lim)
	require.ErrorIs(t, err, ErrNoPending, "a discarded record is never a baseline")

	t.Run("a repository without pending records gets nothing", func(t *testing.T) {
		empty := t.TempDir()

		removed, err := Sweep(empty, CallerCLI, lim)
		require.NoError(t, err)
		assert.Zero(t, removed)
		assert.NoDirExists(t, filepath.Join(empty, ".seamark"), "the sweep creates nothing")
	})

	t.Run("two concurrent sweeps record one discard", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, "toolu-race")
		require.NoError(t, WritePending(context.Background(), root, p, lim))
		ageFile(t, pendingFile(root, p), 2*time.Hour)

		var wg sync.WaitGroup

		counts := make([]int, 8)

		for i := range counts {
			wg.Add(1)

			go func() {
				defer wg.Done()

				counts[i], _ = Sweep(root, CallerHook, lim)
			}()
		}

		wg.Wait()

		total := 0
		for _, n := range counts {
			total += n
		}

		assert.Equal(t, 1, total)
		assert.Len(t, readAudit(t, root), 1)
	})

	t.Run("a smaller pending-max-age expires a fresh record", func(t *testing.T) {
		root := t.TempDir()
		p := simplePending(t, rawSession, "toolu-short")
		require.NoError(t, WritePending(context.Background(), root, p, lim))
		ageFile(t, pendingFile(root, p), 2*time.Minute)

		short := lim
		short.PendingMaxAge = time.Minute

		removed, err := Sweep(root, CallerCLI, short)
		require.NoError(t, err)
		assert.Equal(t, 1, removed)
	})
}

// TestLessonCheckPendingRefusesUnsafeDirectory proves the unsafe
// directory rule. A link or a non-directory at .seamark,
// .seamark/checks, or .seamark/checks/pending stops every write, read,
// and sweep with the fixed text. Nothing goes through the link. A record
// file that is a link never serves as a baseline.
func TestLessonCheckPendingRefusesUnsafeDirectory(t *testing.T) {
	skipWithoutSymlinks(t)

	cases := []struct {
		name  string
		plant func(t *testing.T, root, outside string)
	}{
		{
			name: "the checks directory is a link",
			plant: func(t *testing.T, root, outside string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, ".seamark", "checks")))
			},
		},
		{
			name: "the checks directory is a file",
			plant: func(t *testing.T, root, _ string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "checks"), []byte("x"), 0o600))
			},
		},
		{
			name: "the pending directory is a link",
			plant: func(t *testing.T, root, outside string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "checks"), 0o700))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, ".seamark", "checks", "pending")))
			},
		},
		{
			name: "the .seamark directory is a link",
			plant: func(t *testing.T, root, outside string) {
				require.NoError(t, os.Symlink(outside, filepath.Join(root, ".seamark")))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()

			// The outside directory holds a valid record of the call, so
			// a read through the link would find a baseline.
			p := simplePending(t, rawSession, rawMatch)
			require.NoError(t, WritePending(context.Background(), outside, p, DefaultLimits()))

			before := listTree(t, outside)

			tc.plant(t, root, outside)

			err := WritePending(context.Background(), root, p, DefaultLimits())
			require.ErrorIs(t, err, ErrUnsafeChecksDir)
			assert.Equal(t, "lesson checks: .seamark/checks is not a real directory", err.Error())

			_, err = ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
			require.ErrorIs(t, err, ErrUnsafeChecksDir)

			_, err = Sweep(root, CallerHook, DefaultLimits())
			require.ErrorIs(t, err, ErrUnsafeChecksDir)

			require.ErrorIs(t, DeletePending(root, "claude", p.MatchSHA), ErrUnsafeChecksDir)

			assert.Equal(t, before, listTree(t, outside), "nothing changes through the link")
		})
	}

	t.Run("a record file that is a link is never read", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		p := simplePending(t, rawSession, rawMatch)

		require.NoError(t, WritePending(context.Background(), outside, p, DefaultLimits()))
		require.NoError(t, WritePending(context.Background(), root, simplePending(t, rawSession, "toolu-other"), DefaultLimits()))
		require.NoError(t, os.Symlink(pendingFile(outside, p), pendingFile(root, p)))

		_, err := ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.ErrorIs(t, err, ErrNoPending)
	})

	t.Run("a .gitignore that is a link is refused", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))

		checks := filepath.Join(root, ".seamark", "checks")
		require.NoError(t, os.MkdirAll(checks, 0o700))
		require.NoError(t, os.Symlink(target, filepath.Join(checks, ".gitignore")))

		err := WritePending(context.Background(), root, simplePending(t, rawSession, rawMatch), DefaultLimits())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a symbolic link")

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})
}

// listTree returns every path under dir with its size and its
// modification time. Two equal listings show that nothing changed.
func listTree(t *testing.T, dir string) map[string]string {
	t.Helper()

	out := map[string]string{}

	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		out[p] = fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())

		return nil
	})
	require.NoError(t, err)

	return out
}

// TestLessonCheckPendingDropsOversizedContent proves the content bound.
// The writer keeps content in path order while the total stays at or
// below max-baseline-bytes. It drops the content of a path that does not
// fit, marks its before side too-large, and adds the limitation
// baseline-too-large.
func TestLessonCheckPendingDropsOversizedContent(t *testing.T) {
	cfg := parseValid(t, mixedYAML)

	snap := snapshotOf(BoundaryProposedEdit,
		modifyChange("src/a.go", fileSide("123456"), fileSide("x")),
		modifyChange("src/b.go", fileSide("abcdef"), fileSide("x")),
		modifyChange("src/c.go", fileSide("xyzw"), fileSide("x")),
	)

	noProjection := modifyChange("src/d.go", fileSide(""), Side{Type: ObjectFile, Content: ContentNotRead})
	noProjection.Limitations = []string{LimitationNoProjection}
	snap.Changes = append(snap.Changes, noProjection)

	p := pendingFor(cfg, snap, rawSession, rawMatch)
	require.Equal(t, []string{LimitationNoProjection}, p.Limitations)

	lim := DefaultLimits()
	lim.MaxBaselineBytes = 10

	root := t.TempDir()
	require.NoError(t, WritePending(context.Background(), root, p, lim))

	got, err := ReadPending(root, "claude", p.MatchSHA, lim)
	require.NoError(t, err)

	require.Len(t, got.Paths, 4)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("123456")), got.Paths[0].ContentB64, "6 bytes fit")
	assert.Empty(t, got.Paths[1].ContentB64, "6 more bytes do not fit")
	assert.Equal(t, ContentTooLarge, got.Paths[1].Before.Content)
	assert.Equal(t, fileSide("abcdef").SHA256, got.Paths[1].Before.SHA256, "the before side keeps its digest")
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("xyzw")), got.Paths[2].ContentB64, "the total is exactly the bound")
	assert.Equal(t, ContentAvailable, got.Paths[2].Before.Content)

	assert.Equal(t, []string{LimitationBaselineTooLarge, LimitationNoProjection}, got.Limitations, "sorted")

	assert.Equal(t, ContentAvailable, p.Paths[1].Before.Content, "WritePending never changes the caller's record")
	assert.Equal(t, []string{LimitationNoProjection}, p.Limitations)

	t.Run("a record within the bound keeps every content", func(t *testing.T) {
		root := t.TempDir()

		require.NoError(t, WritePending(context.Background(), root, p, DefaultLimits()))

		got, err := ReadPending(root, "claude", p.MatchSHA, DefaultLimits())
		require.NoError(t, err)
		assert.Equal(t, []string{LimitationNoProjection}, got.Limitations)

		for _, pp := range got.Paths[:3] {
			assert.NotEmpty(t, pp.ContentB64, pp.Path)
		}
	})

	t.Run("a record above the read bound is never read", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, WritePending(context.Background(), root, p, DefaultLimits()))

		small := DefaultLimits()
		small.MaxBaselineBytes = 0

		// The read bound is the base64 size of max-baseline-bytes plus
		// the metadata room. A file above it reads as absent.
		big := strings.Repeat(" ", int(pendingReadBound(small))+1)
		require.NoError(t, os.WriteFile(pendingFile(root, p), []byte(big), 0o600))

		_, err := ReadPending(root, "claude", p.MatchSHA, small)
		require.ErrorIs(t, err, ErrNoPending)
		assert.Contains(t, err.Error(), "read bound")
	})
}

// TestLessonCheckDiscardSession proves the capture deletion.
// DiscardSession finds the directory through the git top level that the
// caller passes. It removes the records of the session hashed over the
// identity root and appends one session-deleted record per removed
// record. It leaves the records of other sessions and of other roots.
func TestLessonCheckDiscardSession(t *testing.T) {
	skipWithoutSymlinks(t)

	g := newGitRepo(t)
	g.write("src/a.go", "package a\n")
	g.commitAll("base")

	// The identity root is a link to the worktree, like an index meta
	// root through a symbolic link. The git top level is the resolved
	// path.
	idRoot := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(g.root, idRoot))

	top, err := filepath.EvalSymlinks(g.root)
	require.NoError(t, err)

	record := func(hashRoot, session, match string) *Pending {
		p := simplePending(t, session, match)
		p.SessionSHA = reviews.SessionDigest(hashRoot, session)
		p.MatchSHA = reviews.MatchDigest(hashRoot, session, match)

		require.NoError(t, WritePending(context.Background(), top, p, DefaultLimits()))

		return p
	}

	gone1 := record(idRoot, "s1", "m1")
	gone2 := record(idRoot, "s1", "m2")
	otherSession := record(idRoot, "s2", "m3")
	otherRoot := record(top, "s1", "m4")

	removed, err := DiscardSession(top, idRoot, "s1")
	require.NoError(t, err)
	assert.Equal(t, 2, removed)

	assert.NoFileExists(t, pendingFile(top, gone1))
	assert.NoFileExists(t, pendingFile(top, gone2))
	assert.FileExists(t, pendingFile(top, otherSession))
	assert.FileExists(t, pendingFile(top, otherRoot), "no other root is tried")

	recs := readAudit(t, top)
	require.Len(t, recs, 2)

	matches := []string{}

	for _, rec := range recs {
		assert.Equal(t, AuditBaselineDiscarded, rec.Kind)
		assert.Equal(t, DiscardSessionDeleted, rec.Discarded)
		assert.Equal(t, CallerCLI, rec.Caller)
		assert.Equal(t, reviews.SessionDigest(idRoot, "s1"), rec.SessionSHA)
		matches = append(matches, rec.MatchSHA)
	}

	assert.ElementsMatch(t, []string{gone1.MatchSHA, gone2.MatchSHA}, matches)

	t.Run("the directory comes from the top level and never from the identity root", func(t *testing.T) {
		// An identity root that names another repository must not select
		// its directory. That repository keeps its record of the same
		// session, and this top level loses its own.
		foreign := newGitRepo(t)
		foreignTop, err := filepath.EvalSymlinks(foreign.root)
		require.NoError(t, err)

		mine := record(foreignTop, "s9", "m9")
		theirs := simplePending(t, "s9", "m10")
		theirs.SessionSHA = reviews.SessionDigest(foreignTop, "s9")
		theirs.MatchSHA = reviews.MatchDigest(foreignTop, "s9", "m10")
		require.NoError(t, WritePending(context.Background(), foreignTop, theirs, DefaultLimits()))

		removed, err := DiscardSession(top, foreignTop, "s9")
		require.NoError(t, err)
		assert.Equal(t, 1, removed)
		assert.NoFileExists(t, pendingFile(top, mine))
		assert.FileExists(t, pendingFile(foreignTop, theirs))
		assert.Empty(t, readAudit(t, foreignTop), "the other workspace gets no audit record")
	})

	t.Run("an empty session ID removes nothing", func(t *testing.T) {
		removed, err := DiscardSession(top, idRoot, "")
		require.NoError(t, err)
		assert.Zero(t, removed)
		assert.FileExists(t, pendingFile(top, otherSession))
	})

	t.Run("a repository without pending records gets nothing", func(t *testing.T) {
		other := newGitRepo(t)

		removed, err := DiscardSession(other.root, other.root, "s1")
		require.NoError(t, err)
		assert.Zero(t, removed)
		assert.NoDirExists(t, filepath.Join(other.root, ".seamark"))
	})
}
