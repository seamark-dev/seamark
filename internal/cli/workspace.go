package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	st, p, err := openIndexProbe(opts)
	if err != nil {
		return nil, "", err
	}

	return st, p.Root, nil
}

// openIndexProbe is openIndex with the whole probe of the index. The
// probe root is the root that openIndex returns. Pin maintenance needs
// the probe, because its plan binds the index run (IndexedAt).
func openIndexProbe(opts *options) (*store.Store, workspace.IndexProbe, error) {
	var none workspace.IndexProbe

	ws, err := resolveWorkspace(opts)
	if err != nil {
		return nil, none, err
	}

	// The probe classifies the database without a write, so a missing
	// database is refused before store.Open can create it.
	p := workspace.Probe(ws)

	switch p.State {
	case workspace.IndexAbsent:
		return nil, none, errNoIndex
	case workspace.IndexUnreadable:
		// Probe calls a stat failure other than "not exist" unreadable.
		// These commands report any stat failure as a missing index, so
		// this second stat keeps their message.
		if _, err := os.Stat(ws.DBPath); err != nil {
			return nil, none, errNoIndex
		}
	}

	// store.Open migrates an older schema. For a newer or unreadable
	// database it returns the error that these commands print.
	st, err := store.Open(ws.DBPath)
	if err != nil {
		return nil, none, err
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

		return nil, none, errNoIndex
	}

	return st, p, nil
}

// openPinWriter opens the index of a command that writes the pin file
// under the recorded root and the ledger of this database: lessons
// --apply, --retarget, and --prune. It refuses when the two lie in two
// repositories, as the console does. The refusal comes before the open,
// because the open migrates an older schema. It also comes before the
// deletion replay of each command, which writes the index and the
// archive.
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

	if err := refusalBeforeOpen(ws); err != nil {
		return nil, none, err
	}

	st, probe, err := openIndexProbe(opts)
	if err != nil {
		return nil, none, err
	}

	if err := refusalOfStore(ws, st); err != nil {
		_ = st.Close()

		return nil, none, err
	}

	return st, probe, nil
}

// refusalBeforeOpen applies the rule without a write, because
// store.ProbeRoot reads every schema. A failed read waits for the open:
// the open waits longer for a lock, and it reports a missing or a broken
// database in its own words.
//
// A newer schema also goes to the open. The open refuses it without a
// write and names the upgrade, and the index run of a refusal route
// would fail on that schema.
func refusalBeforeOpen(ws workspace.Workspace) error {
	version, err := store.ProbeVersion(ws.DBPath)
	if err != nil || version > store.SupportedSchema() {
		return nil
	}

	recorded, err := store.ProbeRoot(ws.DBPath)
	if err != nil {
		return nil
	}

	return pinWriteRefusal(ws, recorded)
}

// refusalOfStore applies the rule to the root of the open store, so a
// read that failed before the open still reaches the rule. A root that
// does not read fails closed, because the file can belong to another
// repository.
func refusalOfStore(ws workspace.Workspace, st *store.Store) error {
	recorded, err := st.GetMeta(store.MetaRepoRoot)
	if err != nil {
		return fmt.Errorf("the recorded root of the index cannot be read (%s: %w), so this "+
			"command could change two repositories; no pin changed; run it again",
			render.Sanitize(ws.DBPath), err)
	}

	return pinWriteRefusal(ws, recorded)
}

// pinWriteRefusal returns why a pin write must not run when the database
// of ws records the root recorded, or nil.
func pinWriteRefusal(ws workspace.Workspace, recorded string) error {
	decision := ws.CheckCommandRootWrite(recorded)
	if decision == workspace.RootWriteAllowed {
		return nil
	}

	return errors.New(pinWriteRefusalText(ws, decision, recorded))
}

// pinWriteRefusalText says why a pin write refuses and names the next
// step. The recorded root comes from the database, so the text sanitizes
// it.
func pinWriteRefusalText(ws workspace.Workspace, decision workspace.RootWrite, recorded string) string {
	root := render.Sanitize(recorded)
	route := pinWriteRoute(ws, decision, recorded)

	subject := "this database"
	if decision == workspace.RootWriteCopied {
		subject = "the index of this checkout"
	}

	if rootIsGone(recorded) {
		return fmt.Sprintf("%s records a directory that no longer exists (%s), and a write would "+
			"recreate it; nothing changed; %s", subject, root, route)
	}

	if decision == workspace.RootWriteCopied {
		return fmt.Sprintf("%s records another repository (%s), so this command would change the pin "+
			"file there and the ledger in this database; nothing changed; %s", subject, root, route)
	}

	return fmt.Sprintf("%s records another repository (%s) that does not own it, so this command would "+
		"change the pin file there and the ledger in this database; nothing changed; %s", subject, root, route)
}

// pinWriteRoute names a next step that can run in this state. The route
// names an absolute --db, because a relative one means another file in
// the recorded root.
//
// The recorded root is a route only with a ledger there. Without a
// default database it answers that it has no index. After a move, a file
// inside this workspace leaves with the restore, so its --db path stops
// being valid. An index run then records this workspace. That run needs
// --force, because an unchanged workspace skips the run and keeps the
// old root.
func pinWriteRoute(ws workspace.Workspace, decision workspace.RootWrite, recorded string) string {
	root := render.Sanitize(recorded)
	here := render.Sanitize(ws.Root)
	db := render.Sanitize(ws.AbsoluteDB())

	there := "run this command in " + root

	if ws.CustomDatabase(recorded) {
		there += " with --db " + db
	}

	switch {
	case decision == workspace.RootWriteCopied:
		indexRun := "run `seamark index` in " + here + " first"

		if _, err := os.Stat(store.DefaultPath(recorded)); err != nil {
			return indexRun
		}

		return indexRun + ", or " + there
	case !rootIsGone(recorded):
		return there
	case store.LiesInside(ws.AbsoluteDB(), ws.Root):
		return "run `seamark index --force --db " + db + "` in " + here + " first"
	default:
		return "restore " + root + ", then " + there
	}
}

// rootIsGone reports a recorded root that no longer exists.
func rootIsGone(recorded string) bool {
	_, err := os.Stat(recorded)

	return errors.Is(err, fs.ErrNotExist)
}
