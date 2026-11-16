package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds lessons --prepare. The command opens the index
// read-only and calls delivery.Prepare, the one preparation service of
// the CLI and of MCP change_set. The command decides no selection
// itself. It writes no index and no delivery state. When Prepare selects
// a lesson, the command appends the one firing record of the call
// before it prints the result.

// prepareConflicts lists the lessons modes and decision flags that
// --prepare refuses, in the order of the help text. The first flag that
// the call sets names the conflict. Each modifier flag, for example
// --client, keeps its own guard and is not here.
//
// Skills grant the prefix `seamark lessons --prepare *` without a
// prompt. Every guard runs before the dispatch, so no call with that
// prefix reaches a command that writes. A later lessons mode joins this
// list, because --prepare combines with no other mode.
var prepareConflicts = []string{
	"file", "list", "region", "stats", "distill", "proposals", "verify", "set-mode",
	"apply", "dismiss", "prune", "retarget", "extract-triggers", "hook", "explore", "hook-reset",
}

// prepareFlagError checks the flags of a --prepare call before any
// dispatch. --prepare combines with no other mode and needs at least one
// path. paths counts the positional arguments of the call.
func prepareFlagError(cmd *cobra.Command, prepare bool, paths int) error {
	if !prepare {
		return nil
	}

	for _, name := range prepareConflicts {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--prepare does not combine with --%s", name)
		}
	}

	if paths == 0 {
		return errors.New("--prepare needs at least one path: seamark lessons --prepare <path>...") //nolint:staticcheck // the "..." usage form is part of the fixed refusal text.
	}

	return nil
}

// runLessonsPrepare runs lessons --prepare. It prints the full text of
// the preparation, or with asJSON only the lesson-preparation/1
// document. A relative path starts at the process directory, as in every
// shell command, not at the -C directory.
//
// Every error exits 1, never 2, because a preparation blocks nothing.
// An error prints nothing on stdout.
func runLessonsPrepare(cmd *cobra.Command, opts *options, paths []string, asJSON bool) error {
	st, root, mined, err := openPrepareIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// The process directory has its links resolved by the rule of
	// lessons --verify, so the two commands read one relative path alike.
	cwd, err := processDir()
	if err != nil {
		return err
	}

	// The badges of each check come from the capability report of this
	// workspace, so preparation computes no badge. Every preparation runs
	// this read, so the narrow reading reads only the hook documents.
	capability := integration.LessonCapability(integration.Builtin().InspectLessonHooks(root), root, integration.SelfIdentity(version))

	// Config and ConfigErr stay nil, so Prepare loads lessons.yaml from
	// root itself. The guidance status then names a file that does not
	// decode as invalid, which loadLessonsConfig would hide.
	p, err := delivery.Prepare(cmd.Context(), st, delivery.PrepareRequest{
		Root:           root,
		CWD:            cwd,
		Paths:          paths,
		Surface:        delivery.SurfacePrepare,
		MinedStatus:    mined,
		MinedFreshness: prepareFreshness(st, root, mined),
		SeamarkVersion: version,
		Capability:     &capability,
	})
	if err != nil {
		return err
	}

	// Prepare returns a preparation without a selection when no path is
	// inside the workspace. The command asks for at least one such path.
	// The guard gives at least one path, so a rejection exists then.
	if len(p.Request.Paths) == 0 && len(p.Request.Rejected) > 0 {
		first := p.Request.Rejected[0]

		return fmt.Errorf("no path of --prepare is inside the workspace: %d rejected (first %q: %s)",
			p.Request.RejectedCount, first.Path, first.Reason)
	}

	// The result is complete, so its record goes to the firing log now.
	// The JSON document names the record, so Record runs before printing.
	p.Record()

	out := cmd.OutOrStdout()

	if !asJSON {
		delivery.RenderPreparation(out, p, delivery.FormFull)

		return nil
	}

	// Two-space indentation matches the other JSON documents of the CLI
	// (lessons --verify, status, doctor). The indentation is not part of
	// the format.
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")

	return enc.Encode(p)
}

// prepareFreshness returns delivery.MinedStale when the fingerprint
// that the index records does not match root now, else "". The command
// refreshes no index, so a stale index can hold old mined lessons. The
// rule is the one of status and orient. root is the root that the index
// records, so the whole preparation describes that root and no other
// root needs a name. A failed meta read says nothing about freshness.
func prepareFreshness(st *store.Store, root, mined string) string {
	if mined != delivery.MinedOK {
		return ""
	}

	recorded, err := st.GetMeta(store.MetaIndexedState)
	if err != nil || recorded == "" {
		return ""
	}

	if indexhealth.Freshness(recorded, index.WorkspaceState(root)) != indexhealth.FreshStale {
		return ""
	}

	return delivery.MinedStale
}

// openPrepareIndex opens the index for a preparation and returns the
// store, the root, and the mined status of the preparation.
//
// The index opens read-only, so the command never migrates or creates
// a database. Without a database, or with a database that no index run
// completed, an empty in-memory store stands in (no-index): only an
// index run mines lessons. A database that this binary cannot read
// gives the in-memory store too (unavailable), because the pins of
// lessons.yaml need no mined data.
//
// The root is the root that the index records, else the git top level.
// The edit hook and lessons --stats read the same root, so the firing
// record of a preparation goes to the log that --stats reads.
func openPrepareIndex(opts *options) (st *store.Store, root, mined string, err error) {
	// Like openHookIndex, the command widens -C to the git top level
	// also with --db. Locate applies that rule.
	ws, err := workspace.Locate(opts.workspace, opts.dbPath)
	if err != nil {
		return nil, "", "", err
	}

	// Probe reads the database without a write. Its root is the
	// workspace root when it reads no meta.
	p := workspace.Probe(ws)

	switch p.State {
	case workspace.IndexReady:
		if indexed, openErr := store.OpenReadOnly(ws.DBPath); openErr == nil {
			return indexed, p.Root, delivery.MinedOK, nil
		}

		mined = delivery.MinedUnavailable
	case workspace.IndexAbsent, workspace.IndexNotIndexed:
		mined = delivery.MinedNoIndex
	default:
		mined = delivery.MinedUnavailable
	}

	st, err = store.OpenMemory()
	if err != nil {
		return nil, "", "", err
	}

	return st, p.Root, mined, nil
}
