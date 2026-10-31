package services

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The root rules of the adapter. The effective root R is the root that
// a readable index records, else the workspace root (engine.probe). The
// Pins read, the ledger, and both settings documents use R. When R is
// not the workspace root, one write could change two repositories, so
// the adapter refuses it. The Phase 3 maintenance and the Phase 4
// lessons operations apply this rule with one difference: --db.

// ErrForeignRoot reports an index that records another repository root
// than the workspace, as in a copied checkout or a --db of another
// repository. A pin authoring commit and a lessons save refuse it, and a
// selection sample is then unavailable. A maintenance commit, a proposal
// apply, and a config.yaml save refuse it unless the recorded root owns
// the database.
var ErrForeignRoot = errors.New("the index records another repository root")

// errRootUnknown reports a database whose recorded root does not read. A
// build then refuses, because the database can belong to another
// repository.
var errRootUnknown = errors.New("the recorded root of the index cannot be read")

// ForeignRootError is an ErrForeignRoot of one read. It carries the answer
// about the database, because a settings block reaches the view only as
// an error, and a view must not read the disk.
type ForeignRootError struct {
	// Root is the root that the index records.
	Root string
	// Workspace is the workspace root.
	Workspace string
	// OtherDB reports a database that is not the default database of the
	// workspace. A build then refuses, so the route is the console of Root.
	OtherDB bool
}

// Error names both roots and no route, because each caller adds the route
// of its screen.
func (e *ForeignRootError) Error() string {
	return fmt.Sprintf("%v (%s), not this workspace (%s)", ErrForeignRoot, e.Root, e.Workspace)
}

// Unwrap makes the error match ErrForeignRoot.
func (e *ForeignRootError) Unwrap() error { return ErrForeignRoot }

// foreignRoot returns an error that wraps ErrForeignRoot when R names
// another directory than the workspace root, else nil. The Phase 4
// lessons operations use it with no --db exception (P4-16). They write
// lessons.yaml under R but plan for this workspace. The text names both
// roots and no route, because each caller adds the route of its screen.
func (e *engine) foreignRoot(probe workspace.IndexProbe) error {
	if store.SameDirectory(probe.Root, e.ws.Root) {
		return nil
	}

	return &ForeignRootError{
		Root: probe.Root, Workspace: e.ws.Root, OtherDB: e.databaseKind() != store.DatabaseDefault,
	}
}

// ownRoot refuses a write under the recorded root when that write and
// this database lie in two repositories. The engine writes the pin file
// and config.yaml there and the ledger in this database. The default
// database of this workspace never fits another root, as in a copied
// checkout. Any other file fits when the recorded root owns it
// (store.RootOwnsDatabase), also through a link at the default path. The
// check compares files, not path strings. The Pins view hides the keys
// for the same case; this check holds for every caller. A build refuses
// every database but the default one, so the route of a refused file is
// the console of the recorded root.
func (e *engine) ownRoot(probe workspace.IndexProbe) error {
	err := e.foreignRoot(probe)
	if err == nil {
		return nil
	}

	switch {
	case e.databaseKind() == store.DatabaseDefault:
		return fmt.Errorf("%w; run seamark index here first", err)
	case store.RootOwnsDatabase(probe.Root, e.ws.DBPath):
		return nil
	default:
		return fmt.Errorf("%w; open the console in %s", err, probe.Root)
	}
}

// buildBlock returns why an index build must not run, or nil. A build
// writes this workspace into the database and records it as the root.
// The database of another repository then loses its root, its pins, and
// the maintenance of its own checkout. The default database has no such
// owner, and a rebuild is the fix of a copied checkout. A database that
// does not exist, records no root, or records this workspace builds too.
//
// The probe stops before the root for an older or a newer schema, so
// this reads the root by itself. A root that does not read fails closed.
func (e *engine) buildBlock() error {
	if e.databaseKind() == store.DatabaseDefault {
		return nil
	}

	_, err := os.Stat(e.ws.DBPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	recorded := ""
	if err == nil {
		recorded, err = store.ProbeRoot(e.ws.DBPath)
	}

	switch {
	case err != nil:
		return fmt.Errorf("%w (%s: %v), and a build could move the root of another repository; "+
			"fix the file or use another --db", errRootUnknown, e.ws.DBPath, err)
	case recorded == "", store.SameDirectory(recorded, e.ws.Root):
		return nil
	default:
		return fmt.Errorf("%w, so a build here would move it; open the console in %s", ErrForeignRoot, recorded)
	}
}

// databaseKind relates the database of this workspace to its root. It
// reads the file system, so a view gets the answer from databaseFacts.
func (e *engine) databaseKind() store.DatabaseKind {
	return store.ClassifyDatabase(e.ws.Root, e.ws.DBPath)
}

// databaseFacts answers what a view must not read from the disk. It uses
// the rules of the engine, so the screens and the commits agree.
func (e *engine) databaseFacts(probe workspace.IndexProbe) workspace.DatabaseFacts {
	facts := workspace.DatabaseFacts{
		Kind: e.databaseKind(), WriteBlock: e.ownRoot(probe), BuildBlock: e.buildBlock(),
	}

	if e.foreignRoot(probe) != nil {
		facts.ForeignRoot = probe.Root
	}

	return facts
}
