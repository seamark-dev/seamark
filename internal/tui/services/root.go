package services

import (
	"errors"
	"fmt"
	"path/filepath"

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
// selection sample is then unavailable. A maintenance commit refuses it
// unless the database is another file than the default path names.
var ErrForeignRoot = errors.New("the index records another repository root")

// foreignRoot returns an error that wraps ErrForeignRoot when R names
// another directory than the workspace root, else nil. The Phase 4
// lessons operations use it with no --db exception (P4-16). They write
// lessons.yaml under R but plan for this workspace. The text names both
// roots and no route, because each caller adds the route of its screen.
func (e *engine) foreignRoot(probe workspace.IndexProbe) error {
	if sameDir(probe.Root, e.ws.Root) {
		return nil
	}

	return fmt.Errorf("%w (%s), not this workspace (%s)", ErrForeignRoot, probe.Root, e.ws.Root)
}

// ownRoot refuses a maintenance write when the default path of this
// workspace names the database and the database records another
// repository root, as in a copied checkout. The engine writes the pin
// file under the recorded root and the ledger in this database, so one
// commit would change two repositories. Only another file makes another
// root normal for maintenance, so the check compares files, not path
// strings. A link spelling of the default path keeps the refusal. The
// Pins view hides the keys for the same case; this check holds for
// every caller.
func (e *engine) ownRoot(probe workspace.IndexProbe) error {
	if e.databaseKind() == store.DatabaseOther {
		return nil
	}

	if err := e.foreignRoot(probe); err != nil {
		return fmt.Errorf("%w; run seamark index here first", err)
	}

	return nil
}

// databaseKind relates the database of this workspace to its root. It
// reads the file system, so a view gets the answer from databaseFacts.
func (e *engine) databaseKind() store.DatabaseKind {
	return store.ClassifyDatabase(e.ws.Root, e.ws.DBPath)
}

// databaseFacts answers what a view must not read from the disk. It uses
// the rules of the engine, so the screens and the commits agree.
func (e *engine) databaseFacts(probe workspace.IndexProbe) workspace.DatabaseFacts {
	facts := workspace.DatabaseFacts{Kind: e.databaseKind()}

	if e.foreignRoot(probe) != nil {
		facts.ForeignRoot = probe.Root
	}

	return facts
}

// sameDir reports whether two paths name one directory. It compares the
// clean paths first and then the resolved links, so a link spelling of
// the same directory is not a foreign root.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}

	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)

	return errA == nil && errB == nil && ra == rb
}
