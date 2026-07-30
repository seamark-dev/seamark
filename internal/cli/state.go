package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/store"
)

// maxStateBytes bounds an import read; a legitimate bundle is orders of
// magnitude smaller.
const maxStateBytes = 64 << 20

func newStateCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Export or import durable decisions (proposals, distillation memory)",
		Long: `The index database holds two kinds of state: derived graph data that
any reindex regenerates, and durable decisions — proposal history with
your applied/dismissed verdicts, and the distillation memory that keeps
paid agent calls from repeating. Rebuilds preserve the durable part;
deleting the database destroys it.

state export writes that durable subset as JSON; state import merges it
back (into this or another clone of the same repository). Identity is
the evidence signature, which is stable across machines. Local decisions
are never overwritten: importing only adds missing rows, or resolves a
still-pending proposal with an imported decision.`,
	}

	cmd.AddCommand(newStateExportCmd(opts), newStateImportCmd(opts))

	return cmd
}

func newStateExportCmd(opts *options) *cobra.Command {
	var outPath string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write the durable decisions as JSON (stdout by default)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Unlike graph commands, export does not require an INDEXED
			// database — exporting right after an import-only restore is
			// legitimate — but it refuses to fabricate a bundle from
			// nothing.
			dbPath, root, err := resolveDBPath(opts)
			if err != nil {
				return err
			}

			if _, err := os.Stat(dbPath); err != nil {
				return errors.New("no index found; run `seamark index` first")
			}

			st, err := store.Open(dbPath)
			if err != nil {
				return err
			}

			state, err := st.ExportState()
			_ = st.Close() // read-only

			if err != nil {
				return err
			}

			state.Repo = gitRootCommit(root)

			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}

			data = append(data, '\n')

			if outPath == "" {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}

			// The destination must never be the database itself — that
			// would replace the very data being exported.
			if samePath(outPath, dbPath) {
				return fmt.Errorf("state export: --out %s is the index database; choose another path", outPath)
			}

			// Atomic replace: an interrupted or failed write must leave a
			// previous export exactly as it was. The 0600 mode also
			// applies to a pre-existing looser destination, because the
			// rename installs a fresh file.
			if err := writeAtomicMode(outPath, data, 0o600); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "exported %d proposals, %d distillation marks → %s\n",
				len(state.Proposals), len(state.Distilled), outPath)

			return nil
		},
	}

	cmd.Flags().StringVar(&outPath, "out", "", "write to a file instead of stdout (atomic, 0600)")

	return cmd
}

func newStateImportCmd(opts *options) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Merge exported decisions into this repository's database",
		Long: `Reads a state export (a file argument, or stdin) and merges it in.
Works before the first index run: restoring a backup on a fresh clone is
the point. Existing local decisions always win; only missing rows are
added, and a still-pending proposal may adopt an imported decision.

A bundle records which repository it came from (the root commit id);
importing into a different repository is refused unless --force is
given.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := cmd.InOrStdin()

			if len(args) == 1 {
				f, err := os.Open(args[0])
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()

				in = f
			}

			data, err := io.ReadAll(io.LimitReader(in, maxStateBytes+1))
			if err != nil {
				return err
			}

			if len(data) > maxStateBytes {
				return fmt.Errorf("state import: input exceeds %d MiB", maxStateBytes>>20)
			}

			dec := json.NewDecoder(bytes.NewReader(data))

			var state store.State
			if err := dec.Decode(&state); err != nil {
				return fmt.Errorf("state import: %w", err)
			}

			// Trailing content would be silently dropped otherwise — a
			// concatenated or corrupted file must not half-import.
			if dec.More() {
				return errors.New("state import: trailing data after the state document")
			}

			dbPath, root, err := resolveDBPath(opts)
			if err != nil {
				return err
			}

			// A bundle from another repository would pollute reports and
			// suppress distillation of unrelated evidence.
			if local := gitRootCommit(root); !force && state.Repo != "" && local != "" && state.Repo != local {
				return fmt.Errorf("state import: bundle is from a different repository "+
					"(root commit %.12s, this repository is %.12s) — pass --force to import anyway",
					state.Repo, local)
			}

			// Open creates the database when absent: importing into a
			// fresh clone (before any index run) is the main use case.
			st, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			stats, err := st.ImportState(&state)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"proposals: %d added, %d adopted decisions, %d kept local\n"+
					"distillation marks: %d added, %d kept local\n",
				stats.ProposalsAdded, stats.ProposalsUpdated, stats.ProposalsSkipped,
				stats.DistilledAdded, stats.DistilledSkipped)

			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "import a bundle recorded from a different repository")

	return cmd
}

// resolveDBPath resolves the index location and workspace root without
// requiring the database to exist yet. openIndex shares it — one
// resolution rule, not two copies that drift.
func resolveDBPath(opts *options) (dbPath, root string, err error) {
	root, err = filepath.Abs(opts.workspace)
	if err != nil {
		return "", "", err
	}

	if opts.dbPath != "" {
		return opts.dbPath, root, nil
	}

	// Mirror the indexer: the index lives at the git toplevel.
	if r, err := gitToplevel(root); err == nil {
		root = r
	}

	return store.DefaultPath(root), root, nil
}

// gitRootCommit identifies the repository by its root commit — stable
// across clones and machines, unlike paths or remote URLs. Empty when
// not a git repository (or an unborn branch).
func gitRootCommit(root string) string {
	out, err := gitOutput(root, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")

	return strings.TrimSpace(lines[len(lines)-1]) // deterministic pick for multi-root histories
}

// samePath reports whether two paths name the same file: by absolute
// form, and by file identity when both exist (covers symlinks).
func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)

	if errA == nil && errB == nil && absA == absB {
		return true
	}

	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)

	return errA == nil && errB == nil && os.SameFile(ia, ib)
}
