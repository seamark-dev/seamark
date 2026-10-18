package workspace

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// newWorkspace resolves a fresh temporary directory with default paths.
func newWorkspace(t *testing.T) Workspace {
	t.Helper()

	ws, err := Resolve(t.TempDir(), "")
	require.NoError(t, err)

	return ws
}

// writeFixture writes a minimal Go module that index.Run can parse.
func writeFixture(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/fixture\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"),
		[]byte("package fixture\n\n// A returns one.\nfunc A() int { return 1 }\n\n// B calls A.\nfunc B() int { return A() }\n"), 0o644))
}

// openFresh creates a database with the current schema through the
// writable opener and returns it for setup writes.
func openFresh(t *testing.T, ws Workspace) *store.Store {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	return st
}

// setMeta writes meta values into a fresh database and closes it.
func setMeta(t *testing.T, ws Workspace, kv map[string]string) {
	t.Helper()

	st := openFresh(t, ws)

	for k, v := range kv {
		require.NoError(t, st.SetMeta(k, v))
	}

	require.NoError(t, st.Close())
}

// digest returns the SHA-256 of the main database file. The -wal and
// -shm side files are excluded, because a read-only SQLite open may
// create them.
func digest(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// assertUnchanged runs Probe and OpenReadOnly and checks that the main
// database bytes stay the same.
func assertUnchanged(t *testing.T, ws Workspace) {
	t.Helper()

	before := digest(t, ws.DBPath)

	Probe(ws)

	st, _, err := OpenReadOnly(ws)
	if err == nil {
		require.NoError(t, st.Close())
	}

	assert.Equal(t, before, digest(t, ws.DBPath), "a probe must leave the database bytes unchanged")
}

// sameDir compares two directory paths after symlink resolution. The
// macOS temporary directory is a symlink, and git prints its target.
func sameDir(t *testing.T, want, got string) {
	t.Helper()

	w, err := filepath.EvalSymlinks(want)
	require.NoError(t, err)

	g, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)

	assert.Equal(t, w, g)
}

func TestResolveUsesDefaultDatabasePath(t *testing.T) {
	dir := t.TempDir()

	ws, err := Resolve(dir, "")
	require.NoError(t, err)

	sameDir(t, dir, ws.Root)
	assert.True(t, filepath.IsAbs(ws.Root))
	assert.Equal(t, store.DefaultPath(ws.Root), ws.DBPath)

	_, err = os.Stat(filepath.Join(ws.Root, store.DefaultDir))
	assert.ErrorIs(t, err, os.ErrNotExist, "Resolve must create nothing")
}

func TestResolveEmptyDirMeansCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	ws, err := Resolve("", "")
	require.NoError(t, err)

	sameDir(t, dir, ws.Root)
	assert.Equal(t, store.DefaultPath(ws.Root), ws.DBPath)
}

func TestResolveWidensToGitTopLevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	dir := t.TempDir()
	out, err := exec.Command("git", "init", "-q", dir).CombinedOutput()
	require.NoError(t, err, string(out))

	sub := filepath.Join(dir, "pkg", "inner")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	ws, err := Resolve(sub, "")
	require.NoError(t, err)

	sameDir(t, dir, ws.Root)
	assert.Equal(t, store.DefaultPath(ws.Root), ws.DBPath)
}

func TestResolveMakesRelativeDBFlagAbsoluteAgainstCwd(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)

	ws, err := Resolve(dir, filepath.Join("custom", "x.db"))
	require.NoError(t, err)

	wd, err := os.Getwd()
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(wd, "custom", "x.db"), ws.DBPath,
		"a relative --db names the file that index.Run opens from the process directory")
	sameDir(t, dir, ws.Root)

	_, err = os.Stat(filepath.Join(cwd, "custom"))
	assert.ErrorIs(t, err, os.ErrNotExist, "Resolve must create nothing")
}

func TestResolveKeepsAbsoluteDBFlag(t *testing.T) {
	db := filepath.Join(t.TempDir(), "elsewhere.db")

	ws, err := Resolve(t.TempDir(), db)
	require.NoError(t, err)

	assert.Equal(t, db, ws.DBPath)
}

func TestResolveRejectsMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := Resolve(missing, "")
	require.Error(t, err)

	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), missing)
	assert.NotErrorIs(t, err, ErrNotDirectory)
}

func TestResolveRejectsRegularFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	_, err := Resolve(file, "")
	require.Error(t, err)

	assert.ErrorIs(t, err, ErrNotDirectory)
	assert.Contains(t, err.Error(), file)
}

func TestLocateMatchesResolveForADirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	dir := t.TempDir()
	out, err := exec.Command("git", "init", "-q", dir).CombinedOutput()
	require.NoError(t, err, string(out))

	sub := filepath.Join(dir, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	located, err := Locate(sub, "")
	require.NoError(t, err)

	resolved, err := Resolve(sub, "")
	require.NoError(t, err)

	assert.Equal(t, resolved, located, "without --db both derive one root and one database path")
	sameDir(t, dir, located.Root)
}

func TestLocateEmptyDirMeansCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	ws, err := Locate("", "")
	require.NoError(t, err)

	sameDir(t, dir, ws.Root)
	assert.Equal(t, store.DefaultPath(ws.Root), ws.DBPath)
}

func TestLocateKeepsARelativeDBFlag(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.Join("custom", "x.db")

	ws, err := Locate(dir, rel)
	require.NoError(t, err)

	assert.Equal(t, rel, ws.DBPath, "the CLI prints --db as the user wrote it")
	sameDir(t, dir, ws.Root)
}

func TestLocateAcceptsAMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	ws, err := Locate(missing, "")
	require.NoError(t, err, "the CLI reports a missing workspace in its own words")

	assert.Equal(t, missing, ws.Root, "the root falls back to the absolute path")
	assert.Equal(t, store.DefaultPath(missing), ws.DBPath)
	assert.NoDirExists(t, missing, "Locate must create nothing")
}

func TestLocateAcceptsARegularFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	ws, err := Locate(file, "")
	require.NoError(t, err)

	assert.Equal(t, file, ws.Root)
}

func TestIndexStateString(t *testing.T) {
	cases := map[IndexState]string{
		IndexAbsent:     "absent",
		IndexUnreadable: "unreadable",
		IndexNewer:      "newer",
		IndexOlder:      "older",
		IndexNotIndexed: "not indexed",
		IndexReady:      "ready",
		IndexState(42):  "IndexState(42)",
	}

	for state, want := range cases {
		assert.Equal(t, want, state.String())
	}
}

func TestIndexStateReadable(t *testing.T) {
	readable := map[IndexState]bool{
		IndexAbsent:     false,
		IndexUnreadable: false,
		IndexNewer:      false,
		IndexOlder:      false,
		IndexNotIndexed: true,
		IndexReady:      true,
	}

	for state, want := range readable {
		assert.Equal(t, want, state.Readable(), state.String())
	}
}

func TestProbeAbsentCreatesNothing(t *testing.T) {
	ws := newWorkspace(t)

	p := Probe(ws)

	assert.Equal(t, IndexAbsent, p.State)
	assert.Equal(t, ws.DBPath, p.Path)
	assert.Equal(t, ws.Root, p.Root)
	assert.Equal(t, store.SupportedSchema(), p.Supported)
	assert.Zero(t, p.Schema)
	assert.Zero(t, p.IndexedAt)
	assert.NoError(t, p.Err)

	st, p, err := OpenReadOnly(ws)
	require.Error(t, err)
	assert.Nil(t, st)
	assert.Equal(t, IndexAbsent, p.State)
	assert.Contains(t, err.Error(), "absent")
	assert.Contains(t, err.Error(), "seamark index")

	_, err = os.Stat(filepath.Join(ws.Root, store.DefaultDir))
	assert.ErrorIs(t, err, os.ErrNotExist, "a probe must not create the state directory")
}

func TestProbeReadyAfterIndexRun(t *testing.T) {
	ws := newWorkspace(t)
	writeFixture(t, ws.Root)

	sum, err := index.Run(index.Options{Root: ws.Root})
	require.NoError(t, err)
	require.Equal(t, ws.DBPath, sum.DBPath, "the workspace must name the database that index.Run writes")

	p := Probe(ws)

	require.Equal(t, IndexReady, p.State, "probe error: %v", p.Err)
	assert.NoError(t, p.Err)
	assert.Equal(t, store.SupportedSchema(), p.Schema)
	assert.Equal(t, store.SupportedSchema(), p.Supported)
	assert.Positive(t, p.IndexedAt)
	assert.Equal(t, sum.Root, p.Root, "the root comes from the index meta")

	st, p, err := OpenReadOnly(ws)
	require.NoError(t, err)
	assert.Equal(t, IndexReady, p.State)

	stats, err := st.Stats()
	require.NoError(t, err)
	assert.Positive(t, stats.Symbols, "the read-only store sees the indexed symbols")
	require.NoError(t, st.Close())

	assertUnchanged(t, ws)
}

func TestProbeNotIndexedDatabaseWithDecisions(t *testing.T) {
	ws := newWorkspace(t)

	st := openFresh(t, ws)
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "sig-1",
		Rule:      "check-errors",
		Note:      "Check every error.",
		Status:    model.ProposalProposed,
		CreatedAt: 1700000000,
	}))
	require.NoError(t, st.Close())

	p := Probe(ws)

	assert.Equal(t, IndexNotIndexed, p.State)
	assert.True(t, p.State.Readable())
	assert.NoError(t, p.Err)
	assert.Equal(t, store.SupportedSchema(), p.Schema)
	assert.Zero(t, p.IndexedAt)
	assert.Equal(t, ws.Root, p.Root)

	ro, p, err := OpenReadOnly(ws)
	require.NoError(t, err)
	assert.Equal(t, IndexNotIndexed, p.State)

	pending, err := ro.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "a database without an index run still shows its decisions")
	require.NoError(t, ro.Close())

	assertUnchanged(t, ws)
}

// stampSchema replaces or removes the stored schema version. It writes
// through database/sql directly, because no supported opener writes an
// older stamp. The store package registers the sqlite driver.
func stampSchema(t *testing.T, path string, version int) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	if version == 0 {
		_, err = db.Exec(`DELETE FROM meta WHERE key = ?`, store.MetaSchemaVersion)
	} else {
		_, err = db.Exec(`UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(version), store.MetaSchemaVersion)
	}

	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestProbeOlderSchema(t *testing.T) {
	cases := map[string]struct {
		version int
		label   string
	}{
		"previous version": {version: store.SupportedSchema() - 1, label: "v" + strconv.Itoa(store.SupportedSchema()-1)},
		"unstamped":        {version: 0, label: "unstamped"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws := newWorkspace(t)
			setMeta(t, ws, map[string]string{store.MetaIndexedAt: "1700000000"})
			stampSchema(t, ws.DBPath, tc.version)

			p := Probe(ws)

			assert.Equal(t, IndexOlder, p.State)
			assert.False(t, p.State.Readable())
			assert.Equal(t, tc.version, p.Schema)
			require.Error(t, p.Err)
			assert.Contains(t, p.Err.Error(), tc.label)
			assert.Contains(t, p.Err.Error(), "`seamark index`")
			assert.Zero(t, p.IndexedAt, "an older database is not opened, so its meta stays unknown")

			st, p, err := OpenReadOnly(ws)
			require.Error(t, err)
			assert.Nil(t, st)
			assert.Equal(t, IndexOlder, p.State)
			assert.Contains(t, err.Error(), "older")

			assertUnchanged(t, ws)

			v, err := store.ProbeVersion(ws.DBPath)
			require.NoError(t, err)
			assert.Equal(t, tc.version, v, "a probe must not migrate the database")
		})
	}
}

func TestProbeNewerSchema(t *testing.T) {
	ws := newWorkspace(t)
	newer := store.SupportedSchema() + 1
	setMeta(t, ws, map[string]string{store.MetaSchemaVersion: strconv.Itoa(newer)})

	p := Probe(ws)

	assert.Equal(t, IndexNewer, p.State)
	assert.False(t, p.State.Readable())
	assert.Equal(t, newer, p.Schema)
	require.Error(t, p.Err)
	assert.Contains(t, p.Err.Error(), "newer seamark")

	st, _, err := OpenReadOnly(ws)
	require.Error(t, err)
	assert.Nil(t, st)
	assert.Contains(t, err.Error(), "newer")

	assertUnchanged(t, ws)
}

func TestProbeUnreadableGarbageFile(t *testing.T) {
	ws := newWorkspace(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(ws.DBPath), 0o755))
	require.NoError(t, os.WriteFile(ws.DBPath, []byte("this is not a SQLite database, only text bytes"), 0o644))

	p := Probe(ws)

	assert.Equal(t, IndexUnreadable, p.State)
	assert.False(t, p.State.Readable())
	require.Error(t, p.Err)
	assert.Contains(t, p.Err.Error(), ws.DBPath)
	assert.Equal(t, ws.Root, p.Root)

	st, p, err := OpenReadOnly(ws)
	require.Error(t, err)
	assert.Nil(t, st)
	assert.Equal(t, IndexUnreadable, p.State)
	assert.Contains(t, err.Error(), "unreadable")

	assertUnchanged(t, ws)
}

func TestProbeUnreadableWhenStatFails(t *testing.T) {
	// A regular file as the parent directory makes stat fail with an
	// error other than "not exist". The probe must not call it absent.
	parent := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o644))

	ws, err := Resolve(t.TempDir(), filepath.Join(parent, "index.db"))
	require.NoError(t, err)

	p := Probe(ws)

	assert.Equal(t, IndexUnreadable, p.State)
	require.Error(t, p.Err)
	assert.Contains(t, p.Err.Error(), ws.DBPath)
}

func TestProbeUsesRecordedRepoRoot(t *testing.T) {
	ws := newWorkspace(t)
	recorded := filepath.Join(t.TempDir(), "recorded-root")
	setMeta(t, ws, map[string]string{
		store.MetaIndexedAt: "1700000000",
		store.MetaRepoRoot:  recorded,
	})

	p := Probe(ws)

	assert.Equal(t, IndexReady, p.State)
	assert.Equal(t, recorded, p.Root, "the recorded root wins, the same rule the CLI applies")
	assert.Equal(t, int64(1700000000), p.IndexedAt)

	st, p, err := OpenReadOnly(ws)
	require.NoError(t, err)
	assert.Equal(t, recorded, p.Root, "OpenReadOnly returns the same effective root")
	require.NoError(t, st.Close())

	assertUnchanged(t, ws)
}

func TestProbeReadyWithMalformedIndexTime(t *testing.T) {
	ws := newWorkspace(t)
	setMeta(t, ws, map[string]string{store.MetaIndexedAt: "yesterday"})

	p := Probe(ws)

	assert.Equal(t, IndexReady, p.State, "the CLI tests only for a non-empty indexed_at")
	assert.Zero(t, p.IndexedAt, "a malformed time is unknown, not a failure")
	assert.NoError(t, p.Err)
}
