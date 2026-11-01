package workspace

import (
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// RootWrite is the answer of the root write rule. A pin write goes to
// the file under the recorded root, and its ledger write goes to the
// index database. One decision must change one repository, so both must
// belong together. The console and the CLI ask this one rule, so they
// refuse the same states. Each surface adds its own words and routes.
type RootWrite int

const (
	// RootWriteAllowed means that both writes change one repository.
	RootWriteAllowed RootWrite = iota
	// RootWriteCopied means that the default database of this workspace
	// records another directory, as in a copied checkout. An index run
	// in this workspace records the correct root.
	RootWriteCopied
	// RootWriteUnowned means that the recorded root does not own this
	// database. An index run here can move the recorded root of another
	// repository, so the route is the recorded root.
	RootWriteUnowned
)

// CheckRootWrite decides a write that reaches recorded, the root that
// the database of ws records. An empty value names no other repository.
// The default database of this workspace never fits another root. Any
// other file fits when the recorded root owns it (store.RootOwnsDatabase),
// also through a link at the default path. A link to this workspace is
// the same directory, so a moved repository keeps working. Each check
// compares files, not path strings.
func (ws Workspace) CheckRootWrite(recorded string) RootWrite {
	if recorded == "" || store.SameDirectory(recorded, ws.Root) {
		return RootWriteAllowed
	}

	db := ws.AbsoluteDB()

	switch {
	case store.ClassifyDatabase(ws.Root, db) == store.DatabaseDefault:
		return RootWriteCopied
	case store.RootOwnsDatabase(recorded, db):
		return RootWriteAllowed
	default:
		return RootWriteUnowned
	}
}

// CheckCommandRootWrite is CheckRootWrite for a CLI command. Without a
// git top level, Locate keeps the -C directory as the root. A directory
// inside the recorded root then reads as another repository, although
// the command runs in that repository. So that directory gets the answer
// of the recorded root. The default database of such a directory stays
// refused, because it is a copy in that directory. A directory with its
// own git top level is another repository. The console asks
// CheckRootWrite, so it keeps its refusal and its route.
func (ws Workspace) CheckCommandRootWrite(recorded string) RootWrite {
	decision := ws.CheckRootWrite(recorded)
	if decision != RootWriteUnowned || !store.LiesInside(ws.Root, recorded) {
		return decision
	}

	if _, ok := history.TopLevel(ws.Root); ok {
		return decision
	}

	return RootWriteAllowed
}

// CustomDatabase reports a --db file that a command in root must name.
// The plain route opens the default database of root, another ledger,
// so the rows of this file stay out of the list. A file of another
// checkout is no such file. A command in root with it passes every
// guard, because root is then the workspace, and it would write two
// repositories.
func (ws Workspace) CustomDatabase(root string) bool {
	db := ws.AbsoluteDB()

	return store.ClassifyDatabase(ws.Root, db) == store.DatabaseOther &&
		store.ClassifyDatabase(root, db) == store.DatabaseOther &&
		!store.LiesInOtherCheckout(root, db)
}

// MoveRootRoute returns the route of a --db file whose recorded root
// cannot take a command, else "". Nothing exists at that root, and a stat
// cannot tell a moved repository from a detached disk. So the user
// decides, and the route is the run that records this workspace in the
// file. Both surfaces print this text.
func (ws Workspace) MoveRootRoute(recorded string) string {
	db := ws.AbsoluteDB()

	if store.StatRoot(recorded) != store.RootGone || !store.FileFitsRoot(ws.Root, db) {
		return ""
	}

	return moveRootRoute(db, ws.Root)
}

// moveRootRoute names the index run that records root in the file db.
func moveRootRoute(db, root string) string {
	return "run `seamark index --move-root --db " + render.Sanitize(db) + "` in " + render.Sanitize(root) + " first"
}

// AbsoluteDB returns the database path in absolute form. The CLI keeps a
// relative --db as the user wrote it. store.ClassifyDatabase cannot walk
// the parent directories of such a path, and a route in another directory
// needs a path that stays valid there. A path that has no absolute form
// stays as given, because no store can open it either.
func (ws Workspace) AbsoluteDB() string {
	abs, err := filepath.Abs(ws.DBPath)
	if err != nil {
		return ws.DBPath
	}

	return abs
}
