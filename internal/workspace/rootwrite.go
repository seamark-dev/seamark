package workspace

import (
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
// of the recorded root. A directory that marks a checkout keeps the
// refusal: a copy nested in the root, or a repository of its own, whatever
// file it names. The console asks CheckRootWrite, so it keeps its refusal
// and its route.
func (ws Workspace) CheckCommandRootWrite(recorded string) RootWrite {
	decision := ws.CheckRootWrite(recorded)
	if decision != RootWriteUnowned || !store.LiesInside(ws.Root, recorded) || store.IsCheckout(ws.Root) {
		return decision
	}

	return RootWriteAllowed
}

// CustomDatabase reports a --db file that a command in root must name.
// The plain route opens the default database of root, another ledger,
// so the rows of this file stay out of the list. A file of another
// checkout is no such file. A command in root with it passes every
// guard, because root is then the workspace, and it would write two
// repositories. MoveRootRoute and OwnerRoute name the route of such a file.
func (ws Workspace) CustomDatabase(root string) bool {
	_, other := store.OtherOwner(ws.Root, root, ws.AbsoluteDB())

	return ws.namesOtherFile(root) && !store.BelongsToCheckout(ws.Root, root, ws.AbsoluteDB()) && !other
}

// MoveRootRoute returns the step that replaces a route to the recorded
// root, else "". Both surfaces print this text, and `seamark index` asks
// the same rule (store.MoveTarget). The step records a directory in the
// file, and that run needs the flag.
func (ws Workspace) MoveRootRoute(recorded string) string {
	db := ws.AbsoluteDB()
	top, _ := history.WidenedRoot(recorded)

	dir, ok := store.MoveTarget(ws.Root, recorded, db, top)
	if !ok {
		return ""
	}

	return moveRootRoute(db, dir)
}

// OwnerRoute returns the route of a --db file of a third checkout, else
// "". A command in the recorded root would write two repositories with
// this file, and without it an id names a row of another ledger. So the
// route records the owner of the file in it. The console has no such
// route: its route to the recorded root opens a list, and the user picks
// from it, so no id can name another row.
func (ws Workspace) OwnerRoute(recorded string) string {
	db := ws.AbsoluteDB()

	if owner, ok := store.OtherOwner(ws.Root, recorded, db); ok {
		return moveRootRoute(db, owner)
	}

	return ""
}

// moveRootRoute names the index run that records root in the file db.
func moveRootRoute(db, root string) string {
	return "run `seamark index --move-root --db " + render.ShellWord(db) + "` in " + render.Sanitize(root) + " first"
}

// namesOtherFile reports a file that is the default database of neither
// directory.
func (ws Workspace) namesOtherFile(root string) bool {
	db := ws.AbsoluteDB()

	return store.ClassifyDatabase(ws.Root, db) == store.DatabaseOther &&
		store.ClassifyDatabase(root, db) == store.DatabaseOther
}

// AbsoluteDB returns the database path in absolute form. The commands
// pass that form already, and this method holds for any other caller.
// store.ClassifyDatabase cannot walk the parent directories of a relative
// path, and a route in another directory needs a path that stays valid
// there.
func (ws Workspace) AbsoluteDB() string {
	return store.AbsolutePath(ws.DBPath)
}
