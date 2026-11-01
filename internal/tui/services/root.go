package services

import (
	"errors"
	"fmt"

	"github.com/seamark-dev/seamark/internal/ownership"
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
	// workspace. No route may then name a plain index build.
	OtherDB bool
	// CustomDB reports a database that the route to Root must name.
	CustomDB bool
	// RootRoute replaces the console of Root in a route, else "".
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
// another directory than the workspace root, else nil. The lessons
// operations use it with no --db exception. They write lessons.yaml under
// R but plan for this workspace. The text names both roots and no route,
// because each caller adds the route of its screen.
func (e *engine) foreignRoot(probe workspace.IndexProbe) error {
	if foreign := e.databaseFacts(probe).foreign; foreign != nil {
		return foreign
	}

	return nil
}

// ownRoot refuses a write under the recorded root when that write and
// this database lie in two repositories. The engine writes the pin file
// and config.yaml there and the ledger in this database. The Pins view
// hides the keys for the same case; this check holds for every caller.
func (e *engine) ownRoot(probe workspace.IndexProbe) error {
	return e.databaseFacts(probe).WriteBlock
}

// buildBlock returns why an index build must not run, or nil. The console
// has no flag that allows the move, so it refuses every such build, also
// one that would skip. The probe stops before the root for an older or a
// newer schema, so the rule reads the root by itself.
func (e *engine) buildBlock() error {
	return e.databaseFacts(workspace.IndexProbe{Root: e.ws.Root}).BuildBlock
}

// rootFacts holds the answers of one read and the error behind them.
type rootFacts struct {
	workspace.DatabaseFacts
	foreign *ForeignRootError
}

// databaseFacts answers what a view must not read from the disk. It uses
// one gather and one decision, so the screens and the commits agree, and
// the console and the CLI agree. It adds only the words of the console.
func (e *engine) databaseFacts(probe workspace.IndexProbe) rootFacts {
	state := ownership.Gather(e.ws.Root, e.ws.AbsoluteDB())
	d := ownership.Decide(state.Facts)
	route := state.Route(d.Route, ownership.SurfaceConsole)
	facts := rootFacts{DatabaseFacts: workspace.DatabaseFacts{Kind: e.databaseKind()}}

	switch {
	case d.Build != ownership.BuildRefused:
	case state.Root == ownership.RootUnreadable:
		facts.BuildBlock = fmt.Errorf("%w (%s: %v), and a build could move the root of another repository; %s",
			errRootUnknown, e.ws.DBPath, state.ReadErr, route)
	default:
		facts.BuildBlock = fmt.Errorf("%w, so a build here would move it; %s", ErrForeignRoot, route)
	}

	if store.SameDirectory(probe.Root, e.ws.Root) {
		return facts
	}

	facts.ForeignRoot, facts.CustomDB = probe.Root, d.Route == ownership.RouteRunInRootDB

	// A view adds its own words to a route to the recorded root. A plain
	// index run belongs to this workspace, so its view has own words too.
	switch d.Route {
	case ownership.RouteRunInRoot, ownership.RouteRunInRootDB, ownership.RouteIndexHere:
	default:
		facts.RootRoute = route
	}

	facts.foreign = &ForeignRootError{
		Root: probe.Root, Workspace: e.ws.Root,
		OtherDB: facts.Kind != store.DatabaseDefault, CustomDB: facts.CustomDB, RootRoute: facts.RootRoute,
	}

	if d.Write == ownership.WriteRefused {
		facts.WriteBlock = fmt.Errorf("%w; %s", facts.foreign, route)
	}

	return facts
}

// databaseKind relates the database of this workspace to its root. It
// reads the file system, so a view gets the answer from databaseFacts.
func (e *engine) databaseKind() store.DatabaseKind {
	return store.ClassifyDatabase(e.ws.Root, e.ws.DBPath)
}
