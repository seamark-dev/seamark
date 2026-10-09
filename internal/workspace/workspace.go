// Package workspace resolves a repository workspace and describes its
// index database without writing to it. The console (`seamark tui`)
// reads the index only through it, because the store opener migrates.
// The CLI commands classify the database with Probe before they open
// it, so both read the index meta by one rule.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// ErrNotDirectory reports a workspace path that does not name a
// directory. The console checks this before it enters full-screen mode.
var ErrNotDirectory = errors.New("workspace is not a directory")

// Workspace is a resolved repository root and its index database path.
type Workspace struct {
	// Root is the absolute repository root. It is the git top level
	// when git knows the directory, else the absolute directory.
	Root string
	// DBPath is the index database path. It is the --db value when set,
	// else <Root>/.seamark/index.db, the same path `seamark index` writes.
	// Resolve makes a --db value absolute; Locate keeps it as given.
	DBPath string
}

// Resolve validates dir and derives the database path. It creates
// nothing and runs only `git rev-parse`.
func Resolve(dir, dbFlag string) (Workspace, error) {
	if dir == "" {
		dir = "."
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace %s: %w", dir, err)
	}

	// Locate ignores a missing directory, because git fails and the
	// root falls back to the path. This check makes the error visible.
	info, err := os.Stat(abs)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace %s: %w", abs, statCause(err))
	}

	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("%w: %s", ErrNotDirectory, abs)
	}

	ws, err := Locate(abs, dbFlag)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace %s: %w", abs, err)
	}

	// index.Run opens a relative --db against the process directory.
	// The absolute form names the same file and stays valid in messages.
	if dbFlag != "" {
		ws.DBPath, err = filepath.Abs(dbFlag)
		if err != nil {
			return Workspace{}, fmt.Errorf("index database %s: %w", dbFlag, err)
		}
	}

	return ws, nil
}

// Locate derives the root and the database path without the checks of
// Resolve. A missing dir is no error, and a relative dbFlag stays as
// given. The CLI commands use it, because they report a missing
// workspace in their own words and print --db as the user wrote it.
// It returns the resolver error unchanged for the same reason.
func Locate(dir, dbFlag string) (Workspace, error) {
	// ResolveRoot is the resolver that index.Run uses. The same rule
	// makes every reader find the database that an index build writes.
	root, err := index.ResolveRoot(dir)
	if err != nil {
		return Workspace{}, err
	}

	dbPath := dbFlag
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	return Workspace{Root: root, DBPath: dbPath}, nil
}

// statCause removes the path wrapper from a stat error. The caller
// names the path itself, so the wrapper only repeats it. The cause
// still matches os.ErrNotExist and os.ErrPermission.
func statCause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}

	return err
}

// IndexState classifies the index database for display and actions.
type IndexState int

const (
	// IndexAbsent means no database file exists.
	IndexAbsent IndexState = iota
	// IndexUnreadable means the database file exists but a read failed.
	IndexUnreadable
	// IndexNewer means a newer seamark wrote the schema.
	IndexNewer
	// IndexOlder means an older schema; `seamark index` migrates it.
	IndexOlder
	// IndexNotIndexed means the database holds decisions but no index
	// run, for example after `seamark state import`.
	IndexNotIndexed
	// IndexReady means the database is indexed and readable.
	IndexReady
)

// String returns a stable lower-case name for the state.
func (s IndexState) String() string {
	switch s {
	case IndexAbsent:
		return "absent"
	case IndexUnreadable:
		return "unreadable"
	case IndexNewer:
		return "newer"
	case IndexOlder:
		return "older"
	case IndexNotIndexed:
		return "not indexed"
	case IndexReady:
		return "ready"
	default:
		// A value outside the enumeration is a programming error. The
		// number stays visible so that a log shows the bad value.
		return "IndexState(" + strconv.Itoa(int(s)) + ")"
	}
}

// Readable reports whether OpenReadOnly can open the database.
func (s IndexState) Readable() bool {
	return s == IndexNotIndexed || s == IndexReady
}

// IndexProbe describes the index database. Probe fills it without a
// write; SQLite may still create its -wal and -shm side files.
type IndexProbe struct {
	State IndexState
	// Path is the database path that was probed.
	Path string
	// Schema is the stored schema version; 0 when unknown.
	Schema int
	// Supported is the schema version this binary reads.
	Supported int
	// Root is the effective repository root. It is the meta repo_root
	// when the probe reads one, else the workspace root.
	Root string
	// IndexedAt is the last index time in Unix seconds; 0 when unknown.
	IndexedAt int64
	// Err holds the probe failure for IndexUnreadable, IndexNewer and
	// IndexOlder. It is nil for the other states.
	Err error
}

// Probe classifies the index database of ws without writing to it.
func Probe(ws Workspace) IndexProbe {
	p := IndexProbe{
		Path:      ws.DBPath,
		Supported: store.SupportedSchema(),
		Root:      ws.Root,
	}

	if _, err := os.Stat(ws.DBPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			p.State = IndexAbsent

			return p
		}

		return unreadable(p, fmt.Errorf("index database %s: %w", ws.DBPath, statCause(err)))
	}

	// ProbeVersion reads the stamp without schema or migration. The
	// store openers either migrate (Open) or refuse without a reason
	// that a view can classify (OpenReadOnly).
	version, err := store.ProbeVersion(ws.DBPath)
	if err != nil {
		return unreadable(p, fmt.Errorf("index database %s: %w", ws.DBPath, err))
	}

	p.Schema = version

	switch {
	case version > p.Supported:
		p.State = IndexNewer
		p.Err = fmt.Errorf("index schema is v%d but this seamark reads v%d: "+
			"a newer seamark wrote it, so upgrade seamark and keep the database", version, p.Supported)

		return p
	case version < p.Supported:
		p.State = IndexOlder
		p.Err = fmt.Errorf("index schema is %s but this seamark reads v%d: "+
			"run `seamark index` to migrate it", describeSchema(version), p.Supported)

		return p
	}

	return probeMeta(p)
}

// probeMeta opens a database with the supported schema read-only and
// reads the index meta. It closes the store before it returns, because
// a probe must not hold the database.
func probeMeta(p IndexProbe) IndexProbe {
	st, err := store.OpenReadOnly(p.Path)
	if err != nil {
		return unreadable(p, err)
	}
	defer func() { _ = st.Close() }()

	at, err := st.GetMeta(store.MetaIndexedAt)
	if err != nil {
		return unreadable(p, fmt.Errorf("index database %s: read %s: %w", p.Path, store.MetaIndexedAt, err))
	}

	root, err := st.GetMeta(store.MetaRepoRoot)
	if err != nil {
		return unreadable(p, fmt.Errorf("index database %s: read %s: %w", p.Path, store.MetaRepoRoot, err))
	}

	// The CLI index readers and the console use the root that the index
	// records, not the -C directory. This probe holds that rule for both.
	if root != "" {
		p.Root = root
	}

	// An empty indexed_at means that no index run completed. A database
	// from `seamark state import` holds decisions only.
	if at == "" {
		p.State = IndexNotIndexed

		return p
	}

	p.State = IndexReady

	// A malformed time is not a read failure. IndexedAt stays 0, which
	// callers show as an unknown time.
	if n, err := strconv.ParseInt(at, 10, 64); err == nil {
		p.IndexedAt = n
	}

	return p
}

// unreadable marks p as IndexUnreadable with err as the reason.
func unreadable(p IndexProbe, err error) IndexProbe {
	p.State = IndexUnreadable
	p.Err = err

	return p
}

// describeSchema names a stored schema version. Version 0 means that
// no stamp exists, which a pre-versioning seamark left.
func describeSchema(version int) string {
	if version == 0 {
		return "unstamped"
	}

	return "v" + strconv.Itoa(version)
}

// OpenReadOnly opens the index database read-only and returns a fresh
// probe. It fails unless the probe state is readable. The caller closes
// the store promptly, because a view snapshot is not a lock.
func OpenReadOnly(ws Workspace) (*store.Store, IndexProbe, error) {
	p := Probe(ws)

	if !p.State.Readable() {
		if p.Err != nil {
			return nil, p, fmt.Errorf("index database %s is %s: %w", p.Path, p.State, p.Err)
		}

		return nil, p, fmt.Errorf("index database %s is %s: run `seamark index` to build it", p.Path, p.State)
	}

	// Another process can replace the database between the probe and
	// this open. The probe then reports the failure, so the state that
	// callers show agrees with the returned error.
	st, err := store.OpenReadOnly(ws.DBPath)
	if err != nil {
		p = unreadable(p, err)

		return nil, p, err
	}

	return st, p, nil
}
