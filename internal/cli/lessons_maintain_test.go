package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// maintainDeps opens the index of root as `lessons --retarget` does and
// returns the selected applied rows with the maintenance inputs. A test
// then calls maintainPins with its own pin file writer.
func maintainDeps(t *testing.T, root, raw string) (maintain.Deps, []model.Proposal) {
	t.Helper()

	st, probe, err := openIndexProbe(&options{workspace: root})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)

	ps, err := resolveSelection(applied, raw, "applied")
	require.NoError(t, err)

	return maintain.Deps{Root: probe.Root, Reg: integration.Builtin(), Reader: st, Probe: probe}, ps
}

// failingRestore writes the pin file through the guarded write and
// fails every restore. With a failing ledger it gives a partial commit:
// the file changed, and neither the ledger nor the old file came back.
type failingRestore struct{ err error }

func (failingRestore) Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error {
	p, err := pinfile.Prepare(reg, root, expect, after)
	if err != nil {
		return err
	}

	return p.Apply()
}

func (f failingRestore) Restore(*integration.Registry, string, [32]byte, []byte) error {
	return f.err
}

// The CLI keeps no session, so it cannot offer the recovery proof of the
// console. A partial commit names the state and the way back instead.
// The test follows that advice and reaches a consistent state.
func TestLessonsMaintainPartialCommitNamesTheWayBack(t *testing.T) {
	root := retargetGoldenWorkspace(t, retargetGoldenLessons)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainFailUpdates(t, root, "region")

	deps, ps := maintainDeps(t, root, "p1")
	deps.Files = failingRestore{err: errors.New("injected: disk full")}

	var out strings.Builder

	err := maintainPins(&out, deps, maintain.ActionRetarget, ps)
	require.Error(t, err)
	assert.Equal(t, "retarget incomplete: reconciliation required: the ledger update failed, the pin file was not "+
		"restored, and the ledger rows keep their earlier values: store: retarget p1: constraint failed: injected "+
		"ledger failure (1811); the restore failed: injected: disk full — undo the change to .seamark/lessons.yaml "+
		"(`git diff .seamark/lessons.yaml` shows it) and run `seamark lessons --retarget p1` again", err.Error())
	assert.Empty(t, out.String(), "no line claims a retargeted pin")

	// The file holds the new regions, and the ledger keeps the old ones.
	content, _ := maintainLessons(t, root)
	assert.Equal(t, strings.Replace(retargetGoldenLessons, "  - rule: Guard-Empty-Datasets\n    region: '*'\n",
		"  - rule: Guard-Empty-Datasets\n    region: scripts\n", 1), content)
	assert.Equal(t, retargetGoldenLedger, maintainLedger(t, root, 0))

	// The advice: undo the file change, then run the command again.
	maintainWriteLessons(t, root, retargetGoldenLessons, 0o600)
	maintainHealUpdates(t, root, "region")

	stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1")
	assert.Equal(t, "  p1    guard-empty-datasets → scripts\n"+
		"retargeted 1 pin(s) — review the lessons.yaml diff and commit it\n", stdout)
	assert.Empty(t, stderr)
	assert.Contains(t, maintainLedger(t, root, 0),
		`p1 applied    "guard-empty-datasets" region="scripts" regions=["scripts"]`)
}

func TestLessonsMaintainFailureMapsTheCommitOutcome(t *testing.T) {
	busy := errors.New("database is locked")
	diskFull := errors.New("disk full")

	tests := []struct {
		name string
		res  maintain.Result
		want string
	}{
		{
			name: "done",
			res:  maintain.Result{Action: maintain.ActionPrune, FileWritten: true, LedgerDone: true, Moved: []int64{2}},
		},
		{
			name: "another writer completed the ledger",
			res:  maintain.Result{Action: maintain.ActionPrune, FileWritten: true, LedgerDone: true},
		},
		{
			name: "the file write changed nothing",
			res:  maintain.Result{Action: maintain.ActionRetarget, FileErr: diskFull},
			want: "retarget aborted, nothing changed: disk full",
		},
		{
			name: "the ledger failed and the file came back",
			res: maintain.Result{Action: maintain.ActionPrune, FileWritten: true, LedgerErr: busy,
				Restored: true},
			want: "prune aborted, lessons.yaml restored: database is locked",
		},
		{
			name: "the file write failed after the bytes landed, and the file came back",
			res: maintain.Result{Action: maintain.ActionRetarget, FileWritten: true, FileErr: diskFull,
				Restored: true},
			want: "retarget aborted, lessons.yaml restored: disk full",
		},
		{
			name: "another writer changed the file, so nothing was restored",
			res: maintain.Result{Action: maintain.ActionPrune, FileWritten: true, LedgerErr: busy,
				RestoreErr: pinfile.ErrStale, Note: "reconciliation required: the ledger update failed, the pin file " +
					"was not restored, and the ledger rows keep their earlier values"},
			want: "prune incomplete: reconciliation required: the ledger update failed, the pin file was not " +
				"restored, and the ledger rows keep their earlier values: database is locked; the restore failed: " +
				"the pin file changed since it was read — undo the change to .seamark/lessons.yaml " +
				"(`git diff .seamark/lessons.yaml` shows it) and run `seamark lessons --prune p2,p7` again",
		},
		{
			name: "the rows could not be read again",
			res: maintain.Result{Action: maintain.ActionRetarget, FileWritten: true, LedgerErr: busy,
				ObservedErr: errors.New("database disk image is malformed"),
				Note:        "reconciliation required: the ledger rows could not be read again, so nothing was restored"},
			want: "retarget incomplete: reconciliation required: the ledger rows could not be read again, so " +
				"nothing was restored: database is locked; the rows could not be read again: database disk image " +
				"is malformed — undo the change to .seamark/lessons.yaml (`git diff .seamark/lessons.yaml` shows " +
				"it) and run `seamark lessons --retarget p2,p7` again",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := maintainFailure(tt.res, []int64{2, 7})
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// The pin file editor refuses a file that this user cannot write before
// any write. Before, prune and retarget failed inside os.WriteFile with
// "open <root>/.seamark/lessons.yaml: permission denied".
//
// The refusal reads the owner write bit, not access(2). So it holds for
// root too, and the test does not skip a run as root.
func TestLessonsMaintainRefusesAReadOnlyPinFile(t *testing.T) {
	const want = "the pin file cannot be replaced: the file is read-only (-r--r--r--); seamark does not replace it"

	t.Run("prune", func(t *testing.T) {
		root := pruneGoldenWorkspace(t)
		maintainWriteLessons(t, root, pruneGoldenLessons, 0o444)

		// The gate-off answer names the refusal in its last line. Before,
		// it said that distill.write on lets prune edit the file.
		stdout, stderr := goldenRun(t, root, "lessons", "--prune", "p1")
		assert.Equal(t, `distill.write is off in .seamark/config.yaml — remove these pins from .seamark/lessons.yaml by hand:

  p1    retire-me

(the proposals stay applied; with distill.write on, prune still refuses this file: `+
			"the file is read-only (-r--r--r--); seamark does not replace it)\n", stdout)
		assert.Empty(t, stderr)

		maintainWriteConfig(t, root, maintainGateOn)

		stdout, stderr, errText := maintainRunErr(t, root, "lessons", "--prune", "p1")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Equal(t, want, errText)

		content, _ := maintainLessons(t, root)
		assert.Equal(t, pruneGoldenLessons, content)
		assert.Equal(t, pruneGoldenLedger, maintainLedger(t, root, 0))
	})

	t.Run("retarget", func(t *testing.T) {
		root := retargetGoldenWorkspace(t, retargetGoldenLessons)
		maintainWriteConfig(t, root, maintainGateOn)
		maintainWriteLessons(t, root, retargetGoldenLessons, 0o444)

		stdout, stderr, errText := maintainRunErr(t, root, "lessons", "--retarget", "p1")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Equal(t, want, errText)

		content, _ := maintainLessons(t, root)
		assert.Equal(t, retargetGoldenLessons, content)
		assert.Equal(t, retargetGoldenLedger, maintainLedger(t, root, 0))
	})
}

// A symbolic link at the pin file path blocks the write, as in Pins.
// Before, prune and retarget wrote through the link into its target.
// The gate-off answer writes nothing, so it still lists the change. Its
// last line names the refusal: before, it said that distill.write on
// lets the command edit the file.
func TestLessonsMaintainRefusesALinkedPinFile(t *testing.T) {
	const reason = "symlink at .seamark/lessons.yaml; seamark writes only real paths inside the repository"

	// link puts a link to a copy of content outside the root in place of
	// the pin file, and returns the path of the copy.
	link := func(t *testing.T, root, content string) string {
		t.Helper()

		target := filepath.Join(t.TempDir(), "shared-lessons.yaml")
		require.NoError(t, os.WriteFile(target, []byte(content), 0o644))

		path := filepath.Join(root, ".seamark", "lessons.yaml")
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(target, path))

		return target
	}

	t.Run("prune", func(t *testing.T) {
		root := pruneGoldenWorkspace(t)
		target := link(t, root, pruneGoldenLessons)

		stdout, stderr := goldenRun(t, root, "lessons", "--prune", "p1")
		assert.Equal(t, `distill.write is off in .seamark/config.yaml — remove these pins from .seamark/lessons.yaml by hand:

  p1    retire-me

(the proposals stay applied; with distill.write on, prune still refuses this file: `+reason+`)
`, stdout)
		assert.Empty(t, stderr)

		maintainWriteConfig(t, root, maintainGateOn)

		stdout, stderr, errText := maintainRunErr(t, root, "lessons", "--prune", "p1")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Equal(t, "the pin file cannot be replaced: "+reason, errText)

		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, pruneGoldenLessons, string(content))
		assert.Equal(t, pruneGoldenLedger, maintainLedger(t, root, 0))
	})

	t.Run("retarget", func(t *testing.T) {
		root := retargetGoldenWorkspace(t, retargetGoldenLessons)
		target := link(t, root, retargetGoldenLessons)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1")
		assert.Equal(t, `distill.write is off in .seamark/config.yaml — change the regions of these pins in .seamark/lessons.yaml by hand:

  p1    guard-empty-datasets → scripts

--- a/.seamark/lessons.yaml
+++ b/.seamark/lessons.yaml
@@ -6,7 +6,7 @@
     note: A hand-written pin without a ledger row.
   # my own comment about the datasets pin
   - rule: Guard-Empty-Datasets
-    region: '*'
+    region: scripts
     note: HAND EDITED live note that must survive.
     extra_field: unknown
   - rule: already-current

(nothing was changed; with distill.write on, retarget still refuses this file: `+reason+`)
`, stdout)
		assert.Empty(t, stderr)

		maintainWriteConfig(t, root, maintainGateOn)

		stdout, stderr, errText := maintainRunErr(t, root, "lessons", "--retarget", "p1")
		assert.Empty(t, stdout)
		assert.Empty(t, stderr)
		assert.Equal(t, "the pin file cannot be replaced: "+reason, errText)

		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, retargetGoldenLessons, string(content))
		assert.Equal(t, retargetGoldenLedger, maintainLedger(t, root, 0))
	})
}

// maintain.Commit plans again before it writes. Another writer can
// change the pin file after the plan of the CLI. The commit then
// refuses before any write, and the run prints no row after the lines
// of the rows that it leaves.
func TestLessonsMaintainStopsWhenTheCommitFindsAChange(t *testing.T) {
	root := pruneGoldenWorkspace(t)
	maintainWriteConfig(t, root, maintainGateOn)

	deps, ps := maintainDeps(t, root, "p1,p5")

	// NewPlan reads the clock once, after it reads the file. The first
	// read belongs to the plan of the CLI, and the second one to the plan
	// of the commit. So the change lands between the two plans.
	changed := pruneGoldenLessons + "# another writer\n"
	plans := 0

	deps.Now = func() time.Time {
		plans++
		if plans == 1 {
			maintainWriteLessons(t, root, changed, 0o640)
		}

		return time.Unix(1700000000, 0)
	}

	var out strings.Builder

	err := maintainPins(&out, deps, maintain.ActionPrune, ps)
	require.ErrorIs(t, err, maintain.ErrStale)
	assert.Equal(t, "prune aborted, nothing changed: the pin file, the ledger, the write gate, or the evidence "+
		"changed since the preview: the pin file changed; the planned file content changed", err.Error())
	assert.Equal(t, "  p5    absent-pin — not found in lessons.yaml; left untouched\n", out.String())
	assert.Equal(t, 2, plans)

	content, _ := maintainLessons(t, root)
	assert.Equal(t, changed, content)
	assert.Equal(t, pruneGoldenLedger, maintainLedger(t, root, 0))
}

// A selection mixes a ready row with rows that cannot change. The run
// changes the ready row and names each other row with its reason. The
// earlier CLI loops did the same with their own reasons.
func TestLessonsMaintainChangesTheReadyRowsOfASelection(t *testing.T) {
	root := pruneGoldenWorkspace(t)
	maintainWriteConfig(t, root, maintainGateOn)

	stdout, stderr := goldenRun(t, root, "lessons", "--prune", "p1,p3..p5")
	assert.Equal(t, `  p3    dup-guidance — ambiguous: duplicate pins; resolve it in .seamark/lessons.yaml or in the ledger first
  p4    dup-guidance — ambiguous: duplicate pins; resolve it in .seamark/lessons.yaml or in the ledger first
  p5    absent-pin — not found in lessons.yaml; left untouched
pruned 1 pin(s) from .seamark/lessons.yaml, 1 proposal(s) marked superseded — review the diff and commit it
  p1    retire-me
`, stdout)
	assert.Empty(t, stderr)
	assert.Equal(t, pruneGoldenSuperseded(1), maintainLedger(t, root, 0))
}
