package cli

import (
	"errors"
	"os"
	"path/filepath"

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
// directory. A copied workspace thus reads the original repository
// (A0 defect D15, kept as is).
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
