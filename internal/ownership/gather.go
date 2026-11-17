package ownership

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// State holds the facts of one database and the paths that a route names.
type State struct {
	Facts
	// Workspace is W, Recorded is R, and DB is the absolute path of F.
	Workspace, Recorded, DB string
	// ReadErr says why F gave no answer about R, or it is nil.
	ReadErr error
}

// Gather reads the state of the database db under the workspace root. It
// writes nothing and reads every schema. db must be absolute, because a
// relative path has no parent directories to walk.
//
// A stat of a dead network mount can block, and git starts a process.
// So no hook path may call this function.
func Gather(root, db string) State {
	s := State{Workspace: root, DB: db}

	info, err := os.Stat(db)

	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		// A path below a regular file holds no database, as statRoot
		// reads it. The build then fails on its directory in its own words.
		s.File = FileAbsent

		return s
	case err != nil:
		s.Root, s.ReadErr = RootUnreadable, err

		return s
	case info.Size() == 0:
		s.File = FileAbsent

		return s
	}

	s.File = fileClass(root, db)

	s.Recorded, err = store.ProbeRoot(db)
	if err != nil {
		s.Recorded, s.Root, s.ReadErr = "", RootUnreadable, err

		return s
	}

	if version, err := store.ProbeVersion(db); err == nil {
		s.NewerSchema = version > store.SupportedSchema()
	}

	if s.Recorded == "" {
		return s
	}

	s.Root = rootState(root, s.Recorded)
	if s.Root == RootSame || s.File == FileDefault {
		return s
	}

	s.Owner = ownerKind(root, s.Recorded, db)
	s.FileInWorkspace = store.LiesInside(db, root)
	s.RootDefault = store.ClassifyDatabase(s.Recorded, db) != store.DatabaseOther

	// Only a clear absence counts, because another stat failure proves nothing.
	_, err = os.Stat(store.DefaultPath(s.Recorded))
	s.RootOwnsFile = s.RootDefault || (s.Root != RootGone && errors.Is(err, fs.ErrNotExist))

	return s
}

// DirRefusesWrites reports a database whose recorded root does not read,
// because its directory refuses writes. SQLite then cannot create its
// sidecars, so no read works until the directory is writable. The facts
// and the decision stay those of an unreadable root: only the words change.
func (s State) DirRefusesWrites() bool {
	return s.Root == RootUnreadable && store.IsReadOnlyDirectory(s.ReadErr)
}

// ReadOnlyDirCause says why the database gives no answer when
// DirRefusesWrites holds. The path and the error are sanitized, because
// both reach a terminal or an MCP client.
func (s State) ReadOnlyDirCause() string {
	return fmt.Sprintf("the index database cannot be read, because its directory refuses writes (%s: %s), "+
		"and SQLite must create its WAL/SHM sidecars there to read it",
		render.Sanitize(s.DB), render.Sanitize(s.ReadErr.Error()))
}

// GatherCommand is Gather for a CLI pin write. Outside git, a directory
// inside R counts as R, because the command runs in that repository. A
// directory that marks a checkout keeps its answer. The console has no
// such case, and a build would record the inner directory.
func GatherCommand(root, db string) State {
	return Gather(root, db).Command()
}

// Command is the state as GatherCommand reads it. A surface that already
// holds the state of Gather uses it, so it reads git no second time.
func (s State) Command() State {
	if s.Recorded != "" && store.LiesInside(s.Workspace, s.Recorded) && !isCheckout(s.Workspace) {
		s.Root = RootSame
	}

	return s
}

// RefusedBuildRoute returns the route of a state in which a plain build in
// W is refused, or "". A surface prints it in place of a plain index run,
// because that run would fail here.
func (s State) RefusedBuildRoute() string {
	d := Decide(s.Facts)
	if d.Build != BuildRefused {
		return ""
	}

	return s.Route(d.Route, SurfaceBuild)
}

// fileClass relates a file that exists to the workspace.
func fileClass(root, db string) FileClass {
	switch store.ClassifyDatabase(root, db) {
	case store.DatabaseDefault:
		return FileDefault
	case store.DatabaseLinked:
		return FileLinked
	default:
		return FileOther
	}
}

// rootState reads the recorded root. A directory below a git top level
// counts as widened, because a command there runs in that top level. That
// test comes last, because it starts a git process.
func rootState(root, recorded string) RootState {
	if store.SameDirectory(recorded, root) {
		return RootSame
	}

	state := statRoot(recorded)
	if state != RootFound {
		return state
	}

	if top, ok := history.TopLevel(recorded); ok && !store.SameDirectory(top, recorded) {
		return RootWidened
	}

	return RootFound
}

// statRoot says what a stat of the recorded root shows.
func statRoot(root string) RootState {
	_, err := os.Stat(root)

	switch {
	case err == nil:
		return RootFound
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return RootGone
	default:
		return RootHidden
	}
}

// ownerKind names the owner of the file db. A file that lies in the
// recorded root stays with that root, also in a submodule of it.
func ownerKind(root, recorded, db string) OwnerKind {
	owner, ok := checkoutOf(db)

	switch {
	case !ok:
		return OwnerNone
	case store.SameDirectory(owner, root):
		return OwnerWorkspace
	case store.SameDirectory(owner, recorded), store.LiesInside(db, recorded):
		return OwnerRoot
	case holdsState(owner):
		return OwnerThird
	default:
		return OwnerGitOnly
	}
}

// isCheckout reports a directory that marks a checkout: it holds .git,
// which is a file in a linked worktree, or the state directory.
func isCheckout(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}

	return holdsState(dir)
}

// holdsState reports a directory with a state directory.
func holdsState(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, store.DefaultDir))

	return err == nil && info.IsDir()
}

// checkoutOf returns the nearest directory above the file at path that
// marks a checkout. A link does not move the file, so the walk follows it.
// A mark in the home directory says nothing, because an earlier run or
// dotfiles can leave one there.
func checkoutOf(path string) (string, bool) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	home, err := os.UserHomeDir()

	return nearestDirectory(filepath.Dir(path), func(dir string) bool {
		return isCheckout(dir) && (err != nil || !store.SameDirectory(dir, home))
	})
}

// nearestDirectory returns the first directory from dir up to the
// filesystem root that marks reports. The filesystem root takes part,
// because a container can hold a workspace there.
func nearestDirectory(dir string, marks func(string) bool) (string, bool) {
	for ; ; dir = filepath.Dir(dir) {
		if marks(dir) {
			return dir, true
		}

		if dir == filepath.Dir(dir) {
			return "", false
		}
	}
}

// Surface selects the words of a route. The kind of step stays the same.
type Surface int

// The surfaces that print a route.
const (
	SurfaceBuild   Surface = iota // A build, also inside another command.
	SurfaceCommand                // A CLI pin write.
	SurfaceConsole                // The console, which prints each command.
)

// Route returns the literal step of r. The recorded root comes from the
// database, so the text sanitizes it. The path of F is absolute, because
// a relative path means another file in another directory.
func (s State) Route(r Route, surface Surface) string {
	here, there, db := render.Sanitize(s.Workspace), render.Sanitize(s.Recorded), render.ShellWord(s.DB)

	switch {
	case r == RouteIndexHere && surface == SurfaceConsole:
		return "run seamark index here first"
	case r == RouteIndexHere:
		return "run `seamark index` in " + here + " first"
	case r == RouteIndexHereDB:
		return "run `seamark index --db " + db + "` in " + here + " first"
	case r == RouteMoveHere && surface == SurfaceBuild:
		return "run `seamark index --move-root --db " + db + "` in " + here + " to move the root"
	case r == RouteMoveHere:
		return "run `seamark index --move-root --db " + db + "` in " + here + " first"
	case r == RouteRunInRoot && surface == SurfaceConsole:
		return "open the console in " + there
	case r == RouteRunInRoot && surface == SurfaceCommand:
		return "run this command in " + there
	case r == RouteRunInRoot:
		return "run `seamark index` in " + there
	case r == RouteRunInRootDB && surface == SurfaceConsole:
		return "open the console in " + there + " with this --db"
	case r == RouteRunInRootDB && surface == SurfaceCommand:
		return "run this command in " + there + " with --db " + db
	case r == RouteRunInRootDB:
		return "run `seamark index --db " + db + "` in " + there
	case r == RouteAnotherDB:
		return "use another --db"
	case r == RouteFixFile && s.DirRefusesWrites():
		return "make " + render.Sanitize(filepath.Dir(s.DB)) + " writable or use another --db"
	case r == RouteFixFile:
		return "fix the file or use another --db"
	default:
		return ""
	}
}
