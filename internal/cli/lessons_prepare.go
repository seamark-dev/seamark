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
	"github.com/seamark-dev/seamark/internal/ownership"
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
	ix, err := openPrepareIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = ix.st.Close() }()

	// The process directory has its links resolved by the rule of
	// lessons --verify, so the two commands read one relative path alike.
	cwd, err := processDir()
	if err != nil {
		return err
	}

	// The badges of each check come from the capability report of this
	// workspace, so preparation computes no badge. Every preparation runs
	// this read, so the narrow reading reads only the hook documents.
	capability := integration.LessonCapability(integration.Builtin().InspectLessonHooks(ix.root), ix.root,
		integration.SelfIdentity(version))

	freshness, staleReason := prepareFreshness(ix.st, ix.root, ix.mined)

	// Config and ConfigErr stay nil, so Prepare loads lessons.yaml from
	// the root itself. The guidance status then names a file that does
	// not decode as invalid, which loadLessonsConfig would hide.
	p, err := delivery.Prepare(cmd.Context(), ix.st, delivery.PrepareRequest{
		Root:           ix.root,
		CWD:            cwd,
		Paths:          paths,
		Surface:        delivery.SurfacePrepare,
		MinedStatus:    ix.mined,
		MinedFreshness: freshness, MinedStaleReason: staleReason,
		MinedRoot:      ix.minedRoot,
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

// prepareFreshness returns delivery.MinedStale and its reason when
// another seamark version built the index, or when the fingerprint that
// the index records does not match root now, else two empty strings. The
// command refreshes no index, so a stale index can hold old mined
// lessons. The rule is the one of status and orient. root is the root
// that the preparation describes, so the fingerprint compares the index
// with the files that the preparation names. A failed meta read says
// nothing about freshness.
func prepareFreshness(st *store.Store, root, mined string) (freshness, reason string) {
	if mined != delivery.MinedOK {
		return "", ""
	}

	if other, err := index.OtherVersion(st); err == nil && other {
		return delivery.MinedStale, indexhealth.StaleOtherVersion
	}

	recorded, err := st.GetMeta(store.MetaIndexedState)
	if err != nil || recorded == "" {
		return "", ""
	}

	if indexhealth.Freshness(recorded, index.WorkspaceState(root, st.Path())) != indexhealth.FreshStale {
		return "", ""
	}

	return delivery.MinedStale, indexhealth.StaleWorkspaceChanged
}

// prepareIndex is the index of one preparation and the roots that the
// preparation reads.
type prepareIndex struct {
	st *store.Store
	// root is the root that the preparation describes. Path
	// normalization, lessons.yaml, the freshness fingerprint, and the
	// firing log use it.
	root string
	// minedRoot is the root that the index records when the preparation
	// describes another root, else "". It fills mined_root.
	minedRoot string
	// mined is the mined status of the preparation.
	mined string
}

// openPrepareIndex opens the index for a preparation and returns it with
// the roots and the mined status of the preparation.
//
// The index opens read-only, so the command never migrates or creates
// a database. Without a database, or with a database that no index run
// completed, an empty in-memory store stands in (no-index): only an
// index run mines lessons. A database that this binary cannot read
// gives the in-memory store too (unavailable), because the pins of
// lessons.yaml need no mined data.
//
// preparationRoot chooses the root.
func openPrepareIndex(opts *options) (prepareIndex, error) {
	// Like openHookIndex, the command widens -C to the git top level
	// also with --db. Locate applies that rule.
	ws, err := workspace.Locate(opts.workspace, opts.dbPath)
	if err != nil {
		return prepareIndex{}, err
	}

	// Probe reads the database without a write. Its root is the
	// workspace root when it reads no meta.
	p := workspace.Probe(ws)

	ix := prepareIndex{}
	ix.root, ix.minedRoot = preparationRoot(ws, p.Root)

	switch p.State {
	case workspace.IndexReady:
		if indexed, openErr := store.OpenReadOnly(ws.DBPath); openErr == nil {
			ix.st, ix.mined = indexed, delivery.MinedOK

			return ix, nil
		}

		ix.mined = delivery.MinedUnavailable
	case workspace.IndexAbsent, workspace.IndexNotIndexed:
		ix.mined = delivery.MinedNoIndex
	default:
		ix.mined = delivery.MinedUnavailable
	}

	ix.st, err = store.OpenMemory()
	if err != nil {
		return prepareIndex{}, err
	}

	return ix, nil
}

// preparationRoot returns the root that a preparation describes, and
// the recorded root when the index records another root, else "".
// recorded is the root that the probe of the index read.
//
// When the index records another root, as in a copied or moved checkout
// before its first index run, the preparation describes the -C checkout.
// Its paths, its lessons.yaml, and its firing log then belong to the
// checkout that the caller edits. The pin writes refuse this state,
// because one decision would change two repositories. A preparation
// changes no pin and no ledger, so it prepares and names the recorded
// root, as MCP change_set does. lessons --stats reads the log of the
// recorded root until an index run in the checkout, so it counts these
// records only after that run.
//
// Otherwise the preparation describes the recorded root. The edit hook
// and lessons --stats read the same root, so the firing record goes to
// the log that --stats reads.
//
// The test is the one of the status root line: outside git, a directory
// inside the recorded root counts as that root. That test reads the
// database again and can start git, so it runs only when the two paths
// name two directories.
func preparationRoot(ws workspace.Workspace, recorded string) (root, minedRoot string) {
	if store.SameDirectory(recorded, ws.Root) {
		return recorded, ""
	}

	if ownership.GatherCommand(ws.Root, ws.AbsoluteDB()).Root == ownership.RootSame {
		return recorded, ""
	}

	return ws.Root, recorded
}
