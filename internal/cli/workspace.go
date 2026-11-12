package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// errNoIndex is the refusal of every command that needs an index run.
var errNoIndex = errors.New("no index found; run `seamark index` first")

// resolveWorkspace derives the workspace for openIndex, doctor, and
// state. It creates nothing, and a missing -C directory is no error.
//
// Without --db it is workspace.Locate. With --db these commands keep
// the absolute -C directory as the root and do not widen it to the git
// top level, as Locate does. doctor reads the policy, the hooks, and
// the agent setup from that root, so this adapter keeps the rule.
func resolveWorkspace(opts *options) (workspace.Workspace, error) {
	if opts.dbPath == "" {
		return workspace.Locate(opts.workspace, "")
	}

	root, err := filepath.Abs(opts.workspace)
	if err != nil {
		return workspace.Workspace{}, err
	}

	return workspace.Workspace{Root: root, DBPath: opts.dbPath}, nil
}

// openIndex opens the index database of a completed index run and
// returns the root to read it against. It refuses a missing database
// and a database without an index run with errNoIndex, and it creates
// neither.
//
// The root is the repo_root that the index records, not the -C
// directory. A copied workspace thus reads the original repository. A
// command that writes under that root uses openPinWriter, which refuses
// that case.
func openIndex(opts *options) (*store.Store, string, error) {
	st, p, _, err := openIndexWorkspace(opts)
	if err != nil {
		return nil, "", err
	}

	return st, p.Root, nil
}

// openIndexProbe is openIndex with the whole probe of the index. The
// probe root is the root that openIndex returns. Pin maintenance needs
// the probe, because its plan binds the index run (IndexedAt).
func openIndexProbe(opts *options) (*store.Store, workspace.IndexProbe, error) {
	st, p, _, err := openIndexWorkspace(opts)

	return st, p, err
}

// openIndexWorkspace is openIndexProbe that also returns the workspace
// that it resolved. status compares that workspace with the recorded
// root. A second resolution starts a second git process, so status
// uses this one.
func openIndexWorkspace(opts *options) (*store.Store, workspace.IndexProbe, workspace.Workspace, error) {
	var none workspace.IndexProbe

	ws, err := resolveWorkspace(opts)
	if err != nil {
		return nil, none, ws, err
	}

	// The probe classifies the database without a write, so a missing
	// database is refused before store.Open can create it.
	p := workspace.Probe(ws)

	switch p.State {
	case workspace.IndexAbsent:
		return nil, none, ws, errNoIndex
	case workspace.IndexUnreadable:
		// Probe calls a stat failure other than "not exist" unreadable.
		// These commands report any stat failure as a missing index, so
		// this second stat keeps their message.
		if _, err := os.Stat(ws.DBPath); err != nil {
			return nil, none, ws, errNoIndex
		}
	}

	// store.Open migrates an older schema. For a newer or unreadable
	// database it returns the error that these commands print.
	st, err := store.Open(ws.DBPath)
	if err != nil {
		return nil, none, ws, err
	}

	// The probe reads the meta only with the supported schema. After
	// the open the database has that schema, so a second probe reads it.
	if !p.State.Readable() {
		p = workspace.Probe(ws)
	}

	// A database from `seamark state import` has the schema and maybe
	// decisions, but no graph. An empty answer from it would look like
	// a valid answer, so these commands ask for an index run instead.
	if p.State != workspace.IndexReady {
		_ = st.Close()

		return nil, none, ws, errNoIndex
	}

	return st, p, ws, nil
}

// openPinWriter opens the index of a command that writes the pin file
// under the recorded root and the ledger of this database: lessons
// --apply, --retarget, and --prune. It refuses when the two lie in two
// repositories, as the console does. The refusal comes before the open,
// because the open migrates an older schema.
//
// A refused command writes nothing and prints no change to make by
// hand, also with distill.write off. That answer reads the write gate of
// the recorded root, and it promises a file that this workspace does not
// use.
func openPinWriter(opts *options) (*store.Store, workspace.IndexProbe, error) {
	var none workspace.IndexProbe

	// Locate widens -C to the git top level also with --db, as the
	// console does. So a directory inside a copy finds its default
	// database, and the rule reads that file as the default database.
	ws, err := workspace.Locate(opts.workspace, opts.dbPath)
	if err != nil {
		return nil, none, err
	}

	state := ownership.GatherCommand(ws.Root, ws.AbsoluteDB())

	// A root that does not read waits for the open: the open waits longer
	// for a lock, and it reports a broken database in its own words.
	if state.Root != ownership.RootUnreadable {
		if err := pinWriteRefusal(state); err != nil {
			return nil, none, err
		}
	}

	st, probe, err := openIndexProbe(opts)
	if err != nil {
		return nil, none, err
	}

	// The open can end the failure of the first read, so a root that
	// reads now reaches the rule. A root that still does not read fails
	// closed, because the file can belong to another repository.
	if state.Root == ownership.RootUnreadable {
		if err := pinWriteRefusal(ownership.GatherCommand(ws.Root, ws.AbsoluteDB())); err != nil {
			_ = st.Close()

			return nil, none, err
		}
	}

	return st, probe, nil
}

// recordedRootNotice says that the index records another root than the
// workspace ws, so status describes that root. It returns "" when the
// two are one directory. ws is the workspace that opened the index, so
// the equal case starts no git process. Status runs often, so that cost
// counts. The words and the subject follow pinWriteRefusal.
//
// The notice names only a route that makes the index record this
// workspace. A route into the recorded root would print the same report
// again, so the notice then has no route.
func recordedRootNotice(ws workspace.Workspace, recorded string) string {
	if recorded == "" || store.SameDirectory(recorded, ws.Root) {
		return ""
	}

	// GatherCommand counts a directory inside a recorded root outside
	// git as that root, because a command there runs in that repository.
	s := ownership.GatherCommand(ws.Root, ws.AbsoluteDB())
	if s.Root == ownership.RootNone || s.Root == ownership.RootSame {
		return ""
	}

	subject := "this database"
	if s.File == ownership.FileDefault {
		subject = "the index of this checkout"
	}

	root := render.Sanitize(recorded)

	what := "records another repository"
	if s.Root == ownership.RootGone {
		what = "records a directory that no longer exists"
	}

	notice := fmt.Sprintf("%s %s (%s), so this report describes %s, not this workspace", subject, what, root, root)

	switch d := ownership.Decide(s.Facts); d.Route {
	case ownership.RouteIndexHere, ownership.RouteIndexHereDB, ownership.RouteMoveHere:
		notice += "; " + s.Route(d.Route, ownership.SurfaceCommand)
	}

	return notice
}

// pinWriteRefusal says why a pin write must not run in this state and
// names the next step, or it returns nil. Only an open that follows can
// report a file without an index or a newer schema. The recorded root
// comes from the database, so the text sanitizes it.
func pinWriteRefusal(s ownership.State) error {
	d := ownership.Decide(s.Facts)
	if d.Write != ownership.WriteRefused {
		return nil
	}

	root := render.Sanitize(s.Recorded)
	route := s.Route(d.Route, ownership.SurfaceCommand)

	subject := "this database"
	if s.File == ownership.FileDefault {
		subject = "the index of this checkout"
	}

	switch {
	case s.Root == ownership.RootUnreadable:
		return fmt.Errorf("the recorded root of the index cannot be read (%s: %w), so this "+
			"command could change two repositories; no pin changed; run it again, %s",
			render.Sanitize(s.DB), s.ReadErr, route)
	case s.Root == ownership.RootGone:
		return fmt.Errorf("%s records a directory that no longer exists (%s), and a write would "+
			"recreate it; nothing changed; %s", subject, root, route)
	case s.File == ownership.FileDefault:
		return fmt.Errorf("%s records another repository (%s), so this command would change the pin "+
			"file there and the ledger in this database; nothing changed; %s", subject, root, route)
	default:
		return fmt.Errorf("%s records another repository (%s) that does not own it, so this command would "+
			"change the pin file there and the ledger in this database; nothing changed; %s", subject, root, route)
	}
}
