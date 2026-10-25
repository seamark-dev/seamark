package maintain

// Tests of the explicit mode edit (contract K6). The engine resolves a
// pin-ref, splices only the mode and check keys of one entry, validates
// the whole candidate, and refuses a target that changed after the read.
// The tests do not read SetModePlan.Active, because milestone P4 may
// retype it (K-4), and a later milestone never edits these tests (F8).

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// setModeHead is the top of every set-mode fixture: a tuning value and
// the spec example of a check configuration.
const setModeHead = `threshold: 3
lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
`

// setModeLessons holds the spec example pin without a mode or a check
// (spec L137-140) and the second example pin of contract K2.
const setModeLessons = setModeHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// setModeEnforced is setModeLessons after
// --set-mode enforce --pin 3215e7b483d1 --check frozen-assets: both keys
// follow the note at the indentation of the entry (K6 example).
const setModeEnforced = setModeHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// setModeAdvisoryWithCheck is setModeEnforced after
// --set-mode advisory --pin 3215e7b483d1: the mode key goes, and the
// check key stays (K6 example).
const setModeAdvisoryWithCheck = setModeHead + `  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    check: frozen-assets
  - rule: keep-parser-strict
    region: internal/parse
    note: Reject unknown fields in the parser.
`

// The K2 references and versions of the spec example pin.
const (
	setModeRef          = "3215e7b483d1"
	setModeSecondRef    = "ed5d4af5de3a"
	setModeVersionPlain = "e79201579c97670797201977d5d764110397a5eaf44ebb056646ee5a32817d6f"
	setModeVersionEnf   = "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e"
)

// setModeRoot writes lessons as the pin file of a new root and sets the
// write gate. It returns the root and the dependencies of the engine.
func setModeRoot(t *testing.T, lessons string, gateOn bool) (string, Deps) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, pinfile.File), []byte(lessons), 0o644))

	gate := "distill:\n  write: false\n"
	if gateOn {
		gate = "distill:\n  write: true\n"
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"), []byte(gate), 0o644))

	return root, Deps{Root: root, Reg: integration.Builtin()}
}

// setModeFile returns the pin file text under root.
func setModeFile(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, pinfile.File))
	require.NoError(t, err)

	return string(data)
}

// setModeTree lists every path under root, so a test can prove that an
// edit creates no other file: no hook, no index, no backup.
func setModeTree(t *testing.T, root string) []string {
	t.Helper()

	var paths []string

	require.NoError(t, filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		paths = append(paths, filepath.ToSlash(rel))

		return err
	}))

	return paths
}

// TestPlanSetMode proves the K6 plan: the pin-ref resolution, the
// candidate splice, the strict check of the whole candidate, the
// unchanged request, and every refusal of the engine. A plan writes
// nothing. A commit changes only the mode and check keys of the pin.
func TestPlanSetMode(t *testing.T) {
	t.Run("enforce with a check writes both keys after the note", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)
		tree := setModeTree(t, root)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.NoError(t, err)

		assert.Equal(t, 1, plan.Entry)
		assert.Equal(t, reviews.NewPinKey("frozen-assets", "generated/", nil), plan.Key)
		assert.Equal(t, setModeRef, plan.Before.Ref)
		assert.Equal(t, lessoncheck.ModeAdvisory, plan.Before.Mode)
		assert.Empty(t, plan.Before.CheckID)
		assert.Equal(t, setModeVersionPlain, plan.Before.Version, "the K2 vector of the pin without a mode")
		assert.Equal(t, lessoncheck.ModeEnforce, plan.After.Mode)
		assert.Equal(t, "frozen-assets", plan.After.CheckID)
		assert.Equal(t, setModeVersionEnf, plan.After.Version, "the K2 vector of the enforced pin")
		assert.Equal(t, setModeRef, plan.After.Ref, "a mode edit keeps the pin-ref")

		predicate, err := lessoncheck.Describe(reviews.CheckDef{Kind: string(lessoncheck.KindForbidPathChange)})
		require.NoError(t, err)
		assert.Equal(t, predicate, plan.Predicate)

		require.NotNil(t, plan.Change)
		assert.Equal(t, setModeLessons, string(plan.Change.Before))
		assert.Equal(t, setModeEnforced, string(plan.Change.After))
		assert.True(t, plan.Write)
		require.NoError(t, plan.GateErr)

		require.Len(t, plan.Impact, 1, "the pin becomes executable, so it has a K1 block")
		assert.Equal(t, lessoncheck.ModeEnforce, plan.Impact[0].Mode)
		assert.Equal(t, setModeVersionPlain, plan.Impact[0].VersionBefore)
		assert.Equal(t, setModeVersionEnf, plan.Impact[0].VersionAfter)

		assert.Equal(t, setModeLessons, setModeFile(t, root), "a plan writes nothing")

		require.NoError(t, CommitSetMode(deps, plan))
		assert.Equal(t, setModeEnforced, setModeFile(t, root),
			"only the two pin keys change; no top-level key and no checks entry")
		assert.Equal(t, tree, setModeTree(t, root), "no other file appears")
	})

	t.Run("advisory removes the mode and keeps the check", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeEnforced, true)

		plan, err := PlanSetMode(deps, SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeAdvisory})
		require.NoError(t, err)

		assert.Equal(t, lessoncheck.ModeEnforce, plan.Before.Mode)
		assert.Equal(t, lessoncheck.ModeAdvisory, plan.After.Mode)
		assert.Equal(t, "frozen-assets", plan.After.CheckID, "--set-mode never removes a check")
		require.Len(t, plan.Impact, 1, "the pin was executable before the edit")
		assert.Equal(t, lessoncheck.ModeAdvisory, plan.Impact[0].Mode)

		require.NoError(t, CommitSetMode(deps, plan))
		assert.Equal(t, setModeAdvisoryWithCheck, setModeFile(t, root))
	})

	t.Run("a bound advisory pin gets no impact", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeAdvisory, CheckID: "frozen-assets",
		})
		require.NoError(t, err)
		assert.Empty(t, plan.Impact, "advisory before and after the edit")

		require.NoError(t, CommitSetMode(deps, plan))
		assert.Equal(t, setModeAdvisoryWithCheck, setModeFile(t, root))
	})

	t.Run("an unchanged request plans no change", func(t *testing.T) {
		explicit := strings.Replace(setModeLessons, "    note: Reject unknown fields in the parser.\n",
			"    note: Reject unknown fields in the parser.\n    mode: advisory\n", 1)

		for name, c := range map[string]struct {
			lessons string
			req     SetModeRequest
		}{
			"the same mode and check": {
				setModeEnforced,
				SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets"},
			},
			"the same mode and the kept check": {
				setModeEnforced, SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeEnforce},
			},
			"an absent mode reads as advisory": {
				setModeLessons, SetModeRequest{PinRef: setModeSecondRef, Mode: lessoncheck.ModeAdvisory},
			},
			"an explicit advisory mode stays": {
				explicit, SetModeRequest{PinRef: setModeSecondRef, Mode: lessoncheck.ModeAdvisory},
			},
		} {
			root, deps := setModeRoot(t, c.lessons, true)

			plan, err := PlanSetMode(deps, c.req)
			require.NoError(t, err, name)
			assert.Nil(t, plan.Change, name)
			assert.Equal(t, plan.Before, plan.After, name)
			assert.Empty(t, plan.Impact, name)

			require.Error(t, CommitSetMode(deps, plan), name)
			assert.Equal(t, c.lessons, setModeFile(t, root), name)
		}
	})

	t.Run("an unknown mode text can be repaired", func(t *testing.T) {
		broken := strings.Replace(setModeLessons, "    note: Reject unknown fields in the parser.\n",
			"    note: Reject unknown fields in the parser.\n    mode: strict\n", 1)
		root, deps := setModeRoot(t, broken, true)

		plan, err := PlanSetMode(deps, SetModeRequest{PinRef: setModeSecondRef, Mode: lessoncheck.ModeAdvisory})
		require.NoError(t, err)
		require.NotNil(t, plan.Change, "an unknown mode never reads as advisory")

		require.NoError(t, CommitSetMode(deps, plan))
		assert.Equal(t, setModeLessons, setModeFile(t, root))
	})

	t.Run("the refusals name the K6 and K2 texts and write nothing", func(t *testing.T) {
		invalidOther := strings.Replace(setModeLessons, "    note: Reject unknown fields in the parser.\n",
			"    note: Reject unknown fields in the parser.\n    mode: check\n", 1)

		for name, c := range map[string]struct {
			lessons string
			req     SetModeRequest
			want    string
		}{
			"a bad mode word": {
				setModeLessons, SetModeRequest{PinRef: setModeRef, Mode: "Enforce"},
				`--set-mode takes advisory, check, or enforce, got "Enforce"`,
			},
			"check without a check": {
				setModeLessons, SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeCheck},
				"--set-mode check needs --check <check-id>: pin " + setModeRef + " has no check",
			},
			"an unknown check": {
				setModeLessons,
				SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "no-such"},
				`check "no-such" is not defined under checks in .seamark/lessons.yaml; define it by hand, then rerun`,
			},
			"a candidate that would not validate": {
				invalidOther,
				SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets"},
				"lessons --set-mode: the result would not validate: pin 2 (keep-parser-strict @ internal/parse): " +
					"mode check requires check: <check-id> (and 0 more); nothing was written",
			},
			"a malformed pin-ref": {
				setModeLessons, SetModeRequest{PinRef: "3215e7", Mode: lessoncheck.ModeAdvisory},
				`pin reference "3215e7": give at least 8 hex characters from the pin column of ` +
					"`seamark lessons --verify`",
			},
		} {
			root, deps := setModeRoot(t, c.lessons, true)

			plan, err := PlanSetMode(deps, c.req)
			require.Error(t, err, name)
			assert.Nil(t, plan, name)
			assert.Equal(t, c.want, err.Error(), name)
			assert.Equal(t, c.lessons, setModeFile(t, root), name)
		}
	})

	// Milestone P5 appends the --delivery clause to the K2 missing text,
	// so the test asserts only the head that every form keeps (§4.2
	// subset rule, F8).
	t.Run("a missing pin-ref keeps its K2 kind and text head", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)

		plan, err := PlanSetMode(deps, SetModeRequest{PinRef: "00000000", Mode: lessoncheck.ModeCheck})
		require.Error(t, err)
		assert.Nil(t, plan)
		assert.ErrorIs(t, err, reviews.ErrPinRef)
		assert.True(t, strings.HasPrefix(err.Error(), "pin reference 00000000 names no pin in .seamark/lessons.yaml; "+
			"`seamark lessons --verify` lists the current references"), err.Error())
		assert.Equal(t, setModeLessons, setModeFile(t, root))
	})

	t.Run("a file that does not load refuses", func(t *testing.T) {
		root, deps := setModeRoot(t, "pin: [\n", true)

		_, err := PlanSetMode(deps, SetModeRequest{PinRef: setModeRef, Mode: lessoncheck.ModeCheck})
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "lessons --set-mode: .seamark/lessons.yaml does not load: "),
			err.Error())
		assert.Equal(t, "pin: [\n", setModeFile(t, root))
	})

	t.Run("an entry that the editor cannot splice refuses", func(t *testing.T) {
		flow := setModeHead + "  - {rule: frozen-assets, region: generated/, note: Flow style.}\n"
		root, deps := setModeRoot(t, flow, true)

		_, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.Error(t, err)
		require.ErrorIs(t, err, pinfile.ErrUnsupported)
		assert.True(t, strings.HasPrefix(err.Error(), "lessons --set-mode: pin "+setModeRef+": "), err.Error())
		assert.True(t, strings.HasSuffix(err.Error(), "; nothing was written — edit .seamark/lessons.yaml by hand"),
			err.Error())
		assert.Equal(t, flow, setModeFile(t, root))
	})

	t.Run("the gate off plans the change and refuses the write", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, false)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.NoError(t, err)
		require.NotNil(t, plan.Change)
		assert.False(t, plan.Write)
		require.NoError(t, plan.GateErr)

		assert.ErrorIs(t, CommitSetMode(deps, plan), ErrWriteDisabled)
		assert.Equal(t, setModeLessons, setModeFile(t, root))
	})

	t.Run("a gate that turns off after the plan blocks the write", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.NoError(t, err)
		require.True(t, plan.Write)

		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
			[]byte("distill:\n  write: false\n"), 0o644))

		assert.ErrorIs(t, CommitSetMode(deps, plan), ErrWriteDisabled)
		assert.Equal(t, setModeLessons, setModeFile(t, root))
	})
}

// staleSetModeText is the exact K6 refusal of a stale target.
const staleSetModeText = "lessons --set-mode: .seamark/lessons.yaml changed after it was read; " +
	"nothing was written — rerun the command"

// raceWriter is a FileWriter that lets another writer change the pin
// file between Prepare and Apply. Apply checks the guard again right
// before the rename, so the write must refuse.
type raceWriter struct {
	t     *testing.T
	other []byte
}

// Replace prepares the write, changes the file, and then applies.
func (w raceWriter) Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error {
	p, err := pinfile.Prepare(reg, root, expect, after)
	require.NoError(w.t, err)
	require.NoError(w.t, os.WriteFile(filepath.Join(root, pinfile.File), w.other, 0o644))

	return p.Apply()
}

// Restore is not used by a mode edit.
func (w raceWriter) Restore(*integration.Registry, string, [32]byte, []byte) error {
	w.t.Fatal("a mode edit never restores")

	return nil
}

// TestSetModeRefusesStaleTarget proves the K6 stale refusal (AC-27). A
// change of the pin file after the read, before Prepare or between
// Prepare and Apply, refuses the write with the exact K6 text. The file
// keeps the bytes of the other writer.
func TestSetModeRefusesStaleTarget(t *testing.T) {
	other := setModeLessons + "# a hand edit\n"

	t.Run("a change before the write", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, pinfile.File), []byte(other), 0o644))

		err = CommitSetMode(deps, plan)
		require.Error(t, err)
		assert.Equal(t, staleSetModeText, err.Error())
		assert.ErrorIs(t, err, ErrStale)
		assert.Equal(t, other, setModeFile(t, root), "the other writer's bytes stay")
	})

	t.Run("a change between prepare and apply", func(t *testing.T) {
		root, deps := setModeRoot(t, setModeLessons, true)

		plan, err := PlanSetMode(deps, SetModeRequest{
			PinRef: setModeRef, Mode: lessoncheck.ModeEnforce, CheckID: "frozen-assets",
		})
		require.NoError(t, err)

		deps.Files = raceWriter{t: t, other: []byte(other)}

		err = CommitSetMode(deps, plan)
		require.Error(t, err)
		assert.Equal(t, staleSetModeText, err.Error())
		assert.ErrorIs(t, err, ErrStale)
		assert.Equal(t, other, setModeFile(t, root), "the guard is checked again before the rename")
	})
}
