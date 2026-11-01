package services

import (
	"errors"
	"fmt"

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
	// workspace. A build then refuses, so the route is consoleRoute.
	OtherDB bool
	// CustomDB reports a database that the route must name. See
	// workspace.Workspace.CustomDatabase.
	CustomDB bool
	// RootRoute replaces the console of Root in a route, else "". See
	// rootRoute.
	RootRoute string
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
		Root: probe.Root, Workspace: e.ws.Root,
		OtherDB:   e.databaseKind() != store.DatabaseDefault,
		CustomDB:  e.ws.CustomDatabase(probe.Root),
		RootRoute: e.rootRoute(probe.Root),
	}
}

// rootRoute returns the step that replaces the console of root, or "".
// That console cannot open a root that a stat cannot find or reach. The
// step names --db, because a shell run without it builds another file.
// When no such step can run, only another file remains.
func (e *engine) rootRoute(root string) string {
	if route := e.ws.MoveRootRoute(root); route != "" {
		return route
	}

	if store.StatRoot(root) != store.RootFound {
		return "use another --db"
	}

	return ""
}

// consoleRoute names the next step of a write under root. That step is
// the console of root, unless rootRoute names another.
func (e *engine) consoleRoute(root string) string {
	if route := e.rootRoute(root); route != "" {
		return route
	}

	if e.ws.CustomDatabase(root) {
		return "open the console in " + root + " with this --db"
	}

	return "open the console in " + root
}

// ownRoot refuses a write under the recorded root when that write and
// this database lie in two repositories. The engine writes the pin file
// and config.yaml there and the ledger in this database. The rule is
// workspace.CheckRootWrite, which `seamark lessons` shares; this method
// adds the words of the console. The Pins view hides the keys for the
// same case; this check holds for every caller. A build refuses every
// database but the default one, so the route of a refused file is
// consoleRoute.
func (e *engine) ownRoot(probe workspace.IndexProbe) error {
	err := e.foreignRoot(probe)
	if err == nil {
		return nil
	}

	switch e.ws.CheckRootWrite(probe.Root) {
	case workspace.RootWriteCopied:
		return fmt.Errorf("%w; run seamark index here first", err)
	case workspace.RootWriteUnowned:
		return fmt.Errorf("%w; %s", err, e.consoleRoute(probe.Root))
	default:
		return nil
	}
}

// buildBlock returns why an index build must not run, or nil. The rule
// is store.RootMovedByBuild, which `seamark index` shares; this method
// adds the words of the console. The console has no flag that allows the
// move, so it refuses every such build, also one that would skip. The
// probe stops before the root for an older or a newer schema, so the
// rule reads the root by itself. A root that does not read fails closed.
func (e *engine) buildBlock() error {
	recorded, err := store.RootMovedByBuild(e.ws.Root, e.ws.DBPath)

	switch {
	case err != nil:
		return fmt.Errorf("%w (%s: %v), and a build could move the root of another repository; "+
			"fix the file or use another --db", errRootUnknown, e.ws.DBPath, err)
	case recorded == "":
		return nil
	default:
		return fmt.Errorf("%w, so a build here would move it; %s", ErrForeignRoot, e.consoleRoute(recorded))
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
		facts.ForeignRoot, facts.CustomDB = probe.Root, e.ws.CustomDatabase(probe.Root)
		facts.RootRoute = e.rootRoute(probe.Root)
	}

	return facts
}
