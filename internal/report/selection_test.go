package report

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests in this file hold the typed selection of A3 step 2: the
// lists of a Selection, the reasons of an Exclusion, the config check
// of the exported entry points, and the change_set naming rule.

// assertSelectionAccounts checks that the lists of sel account for
// every applicable pin and every selected lesson, and that Lessons and
// HeldBack follow from the lists. everyPin is the applicable pin list
// of the union, with the repeats of an identity. The hook puts each of
// these pins in one list. change_set keeps one pin per identity, so its
// lists hold each identity once and a repeat is in no list.
func assertSelectionAccounts(t *testing.T, sel Selection, everyPin []reviews.SurfacedPin, hook bool) {
	t.Helper()

	lessons := make([]model.Lesson, 0, len(sel.Pins)+len(sel.Mined))
	for _, sp := range sel.Pins {
		lessons = append(lessons, sp.Lesson())
	}

	lessons = append(lessons, sel.Mined...)
	assert.Equal(t, lessons, append([]model.Lesson{}, sel.Lessons...), "Lessons are Pins, then Mined")

	listed := slices.Concat(sel.Pins, sel.Restated, sel.OverPinCap, sel.OverTotal)

	if hook {
		assert.Len(t, everyPin, len(listed), "every applicable pin is selected or in one list of held-back pins")
		assert.Equal(t, pinSlots(everyPin), pinSlots(listed), "each pin identity is in the lists as often as in the union")
	} else {
		assert.Equal(t, pinIdentities(everyPin), pinIdentities(listed), "every pin identity is in a list")
		assert.Len(t, listed, len(pinIdentities(listed)), "a repeat of a pin identity is in no list")
	}

	held := len(sel.Restated) + len(sel.OverPinCap) + len(sel.OverTotal)
	if !hook {
		held += len(sel.MinedOverTotal)
	}

	assert.Equal(t, held, sel.HeldBack)

	selected := map[string]bool{}
	for _, l := range sel.Mined {
		selected[l.ClusterKey] = true
	}

	for _, ex := range sel.Excluded {
		assert.False(t, selected[ex.Lesson.ClusterKey], "a selected cluster is not an exclusion")
	}
}

// pinSlots returns the pins as a set of identity and ordinal pairs. The
// ordinal counts the earlier pins of the same identity in pins, as the
// union counts them. So two lists give the same set only when each
// identity occurs in them equally often, whatever the order.
func pinSlots(pins []reviews.SurfacedPin) map[pinSlot]bool {
	out := make(map[pinSlot]bool, len(pins))
	seen := map[reviews.PinKey]int{}

	for _, sp := range pins {
		key := reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)
		out[pinSlot{key: key, ordinal: seen[key]}] = true
		seen[key]++
	}

	return out
}

// pinIdentities returns the set of pin identities in pins. The union
// keys its pin slots on the same identity, so the set counts each
// identity once.
func pinIdentities(pins []reviews.SurfacedPin) map[reviews.PinKey]bool {
	out := make(map[reviews.PinKey]bool, len(pins))
	for _, sp := range pins {
		out[reviews.NewPinKey(sp.Pin.Rule, sp.Pin.Region, sp.Pin.Regions)] = true
	}

	return out
}

func TestHookSelectionClampsThePinCapBeforeTheCuts(t *testing.T) {
	// pin_budget 20 is above the hook total of 8. The clamp lowers the
	// pin cap to 8 before the two cuts, so the two pins above it are
	// over the pin cap, not over the total.
	st, _ := seedStore(t)

	cfg := goldenConfig(distinctPins("*", 10)...)
	cfg.PinBudget = 20
	files := []string{"scripts/task.py"}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	sel, err := ev.HookSelection(cfg)
	require.NoError(t, err)

	require.Len(t, sel.Pins, HookLessonTotal)
	require.Len(t, sel.OverPinCap, 2)
	assert.Equal(t, "india-guard", sel.OverPinCap[0].Pin.Rule)
	assert.Equal(t, "juliet-guard", sel.OverPinCap[1].Pin.Rule)
	assert.Empty(t, sel.OverTotal, "the total cut removes no pin after the clamp")
	assert.Empty(t, sel.Restated)
	assert.Empty(t, sel.Mined, "eight pins fill the total cap")
	assert.Equal(t, "RUF001\n", symptomsOf(sel.MinedOverTotal))
	assert.Equal(t, 2, sel.HeldBack, "the mined lesson above the total is not a held-back pin")
	assertSelectionAccounts(t, sel, ev.union(cfg, true).everyPin, true)

	lessons, held, err := LessonsForFilesBudget(st, cfg, files, HookBudget(cfg))
	require.NoError(t, err)
	assert.Equal(t, lessons, sel.Lessons)
	assert.Equal(t, held, sel.HeldBack)
}

func TestHookSelectionWithoutAPinCapCutsPinsAtTheTotal(t *testing.T) {
	// A pin cap of zero means no pin cap, so the clamp changes nothing.
	// The total cut then removes the pins above the total cap, and they
	// go to OverTotal. This is the view budget of the golden "view total
	// 8 pins 0" (finding A3s2-R3).
	st, _ := seedStore(t)

	cfg := goldenConfig(distinctPins("*", 10)...)
	files := []string{"scripts/task.py"}
	budget := LessonBudget{Total: HookLessonTotal}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	sel := ev.hookFold(cfg, budget)

	require.Len(t, sel.Pins, HookLessonTotal)
	assert.Empty(t, sel.OverPinCap, "no pin cap, so no pin is above it")
	require.Len(t, sel.OverTotal, 2)
	assert.Equal(t, "india-guard", sel.OverTotal[0].Pin.Rule)
	assert.Equal(t, "juliet-guard", sel.OverTotal[1].Pin.Rule)
	assert.Empty(t, sel.Restated, "a budget without a pin cap does not collapse restatements")
	assert.Empty(t, sel.Mined, "eight pins fill the total cap")
	assert.Equal(t, "RUF001\n", symptomsOf(sel.MinedOverTotal))
	assert.Equal(t, 2, sel.HeldBack, "the pins above the total are held back")
	assertSelectionAccounts(t, sel, ev.union(cfg, false).everyPin, true)

	lessons, held, err := LessonsForFilesBudget(st, cfg, files, budget)
	require.NoError(t, err)
	assert.Equal(t, lessons, sel.Lessons)
	assert.Equal(t, held, sel.HeldBack)
}

func TestChangeSetSelectionCutsPinsAndMinedAtOneBudget(t *testing.T) {
	// change_set has no pin cap. Its one budget cuts the pins first and
	// then the mined lessons, and its count includes both.
	st, _ := seedStore(t)

	cfg := goldenConfig(distinctPins("*", 10)...)
	files := []string{"scripts/task.py"}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	sel, err := ev.ChangeSetSelection(cfg)
	require.NoError(t, err)

	require.Len(t, sel.Pins, reviews.DefaultChangeBudget)
	assert.Len(t, sel.OverTotal, 10-reviews.DefaultChangeBudget)
	assert.Empty(t, sel.OverPinCap, "change_set has no pin cap")
	assert.Empty(t, sel.Mined)
	assert.Equal(t, "RUF001\n", symptomsOf(sel.MinedOverTotal))
	assert.Equal(t, 10-reviews.DefaultChangeBudget+1, sel.HeldBack, "the trimmed count includes mined lessons")
	assertSelectionAccounts(t, sel, ev.union(cfg, true).everyPin, false)

	lessons, trimmed, err := LessonsForFiles(st, cfg, files, cfg.ChangeSetBudget())
	require.NoError(t, err)
	assert.Equal(t, lessons, sel.Lessons)
	assert.Equal(t, trimmed, sel.HeldBack)
}

func TestSelectionListsShareNoSpareCapacity(t *testing.T) {
	// A caller can append to one list of a Selection. The kept part and
	// the cut part share one array, so the append must not change the
	// cut part.
	st, _ := seedStore(t)

	cfg := goldenConfig(distinctPins("*", 10)...)
	cfg.PinBudget = 20

	ev, err := ReadEvidence(st, []string{"scripts/task.py"}, cfg)
	require.NoError(t, err)

	sel, err := ev.HookSelection(cfg)
	require.NoError(t, err)
	require.Len(t, sel.OverPinCap, 2)
	require.Len(t, sel.MinedOverTotal, 1)

	overPinCap := slices.Clone(sel.OverPinCap)
	minedOver := slices.Clone(sel.MinedOverTotal)

	_ = append(sel.Pins, reviews.SurfacedPin{Pin: reviews.PinRule{Rule: "appended"}})
	_ = append(sel.Mined, model.Lesson{Symptom: "appended"})

	assert.Equal(t, overPinCap, sel.OverPinCap)
	assert.Equal(t, minedOver, sel.MinedOverTotal)
}

func TestSelectionListsTheRestatedPins(t *testing.T) {
	// Both surfaces collapse restatements. The dropped pins come back
	// by value, so a sample can name them and not only count them.
	st, _ := seedStore(t)

	cfg := goldenConfig(
		reviews.PinRule{Rule: "guard-empty-datasets", Region: "scripts", Note: "Guard datasets before reductions."},
		reviews.PinRule{Rule: "guard-empty-datasets", Region: "*", Note: "Guard datasets before reductions."},
		reviews.PinRule{Rule: "transaction-atomicity", Region: "scripts/task.py", Note: "Wrap dependent writes in one transaction."},
		reviews.PinRule{Rule: "E702", Region: "scripts"},
		reviews.PinRule{Rule: "e702", Region: "*"},
	)
	files := []string{"scripts/task.py"}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	hook, err := ev.HookSelection(cfg)
	require.NoError(t, err)

	changeSet, err := ev.ChangeSetSelection(cfg)
	require.NoError(t, err)

	for _, sel := range []Selection{hook, changeSet} {
		require.Len(t, sel.Restated, 2)
		assert.Equal(t, cfg.Pin[1], sel.Restated[0].Pin, "the repo-wide wording restates the scripts pin")
		assert.Equal(t, cfg.Pin[4], sel.Restated[1].Pin, "an equal code restates the scripts pin")
		assert.Len(t, sel.Pins, 3)
		assert.Equal(t, 2, sel.HeldBack)
	}

	assertSelectionAccounts(t, hook, ev.union(cfg, true).everyPin, true)
	assertSelectionAccounts(t, changeSet, ev.union(cfg, true).everyPin, false)
}

func TestChangeSetSelectionKeepsOnePinPerIdentity(t *testing.T) {
	// Two pins share one identity and carry different notes. The hook
	// accounts for both. change_set keeps the first pin only, so the
	// second pin is in no list and in no count (finding A3s2-R1). A
	// sample must not show that pin as removed or excluded.
	st := openLessonStore(t)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "error-handling", Region: "api", Note: "Wrap errors with context."},
		{Rule: "error-handling", Region: "api/", Note: "Never swallow an error."},
	}

	ev, err := ReadEvidence(st, []string{"api/a.go", "api/b.go"}, cfg)
	require.NoError(t, err)

	union := ev.union(cfg, true)
	require.Len(t, union.everyPin, 2)
	require.Len(t, union.pins, 1, "the two pins share one identity")

	first, repeat := union.everyPin[0], union.everyPin[1]
	assert.Equal(t, cfg.Pin[1], repeat.Pin)

	changeSet, err := ev.ChangeSetSelection(cfg)
	require.NoError(t, err)
	assert.Equal(t, []reviews.SurfacedPin{first}, changeSet.Pins)
	assert.Empty(t, changeSet.Restated, "the fold drops the repeat before the collapse")
	assert.Empty(t, changeSet.OverPinCap)
	assert.Empty(t, changeSet.OverTotal)
	assert.Zero(t, changeSet.HeldBack, "the repeat is not part of the trimmed count")
	assertSelectionAccounts(t, changeSet, union.everyPin, false)

	// The ambient hook keeps the repeat, and the collapse drops it as a
	// restatement of the first pin.
	hook, err := ev.HookSelection(cfg)
	require.NoError(t, err)
	assert.Equal(t, []reviews.SurfacedPin{first}, hook.Pins)
	assert.Equal(t, []reviews.SurfacedPin{repeat}, hook.Restated)
	assert.Equal(t, 1, hook.HeldBack)
	assertSelectionAccounts(t, hook, union.everyPin, true)

	// A budget without a pin cap does not collapse, so it selects both.
	view := ev.hookFold(cfg, LessonBudget{Total: HookLessonTotal})
	require.Len(t, view.Pins, 2)
	assert.Equal(t, repeat.Pin, view.Pins[1].Pin)
	assert.Empty(t, view.Restated)
	assertSelectionAccounts(t, view, ev.union(cfg, false).everyPin, true)
}

func TestSelectionNamesEveryExclusionReason(t *testing.T) {
	// Each excluded cluster appears once, at its first file, with every
	// reason that applies. The mute entries come back by value, in file
	// order.
	st := goldenStore(t)

	cfg := goldenConfig(reviews.PinRule{
		Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment.",
	})
	cfg.Mute = []reviews.MuteRule{
		{Rule: "solitary finding"},
		{Region: "scripts"},
		{Rule: "e702"},
	}

	// The second file repeats every scripts cluster of the first one.
	files := []string{"scripts/x.py", "scripts/sub/y.py", "web/z.ts"}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	byRegion := reviews.MuteRule{Region: "scripts"}
	want := []struct {
		symptom string
		covered bool
		below   bool
		mutes   []reviews.MuteRule
	}{
		{"RUF001", false, false, []reviews.MuteRule{byRegion}},
		{"hard codes a postgres url including credentials", true, false, []reviews.MuteRule{byRegion}},
		{"solitary finding", false, true, []reviews.MuteRule{{Rule: "solitary finding"}, byRegion}},
		{"E702", false, false, []reviews.MuteRule{byRegion, {Rule: "e702"}}},
	}

	hook, err := ev.HookSelection(cfg)
	require.NoError(t, err)

	changeSet, err := ev.ChangeSetSelection(cfg)
	require.NoError(t, err)

	for _, sel := range []Selection{hook, changeSet} {
		require.Len(t, sel.Excluded, len(want))

		for i, w := range want {
			ex := sel.Excluded[i]
			assert.Equal(t, w.symptom, ex.Lesson.Symptom)
			assert.Equal(t, w.covered, ex.Covered, w.symptom)
			assert.Equal(t, w.below, ex.BelowThreshold, w.symptom)
			assert.Equal(t, w.mutes, ex.Mutes, w.symptom)
		}

		assert.Equal(t, "E501\n", symptomsOf(sel.Mined), "the web cluster is selected")
	}

	// Each reason agrees with the production filter. Coverage is a
	// separate gate, so only an uncovered lesson must fail SurfacesMined.
	for _, ex := range hook.Excluded {
		assert.Equal(t, cfg.Muted(ex.Lesson), len(ex.Mutes) > 0, ex.Lesson.Symptom)

		if !ex.Covered {
			assert.False(t, cfg.SurfacesMined(ex.Lesson), ex.Lesson.Symptom)
			assert.True(t, ex.BelowThreshold || len(ex.Mutes) > 0, ex.Lesson.Symptom)
		}
	}

	// The exclusion keeps a copy of each mute entry, not a reference.
	cfg.Mute[1].Region = "changed"
	assert.Equal(t, byRegion, hook.Excluded[0].Mutes[0])
}

func TestExclusionThresholdAgreesWithSurfacesMined(t *testing.T) {
	// SurfacesMined owns the threshold rule, and exclusionOf states its
	// threshold half again (finding A3s2-R2). Without a mute, the two
	// must agree at each count around each threshold, not only at the
	// fixture values.
	for threshold := 1; threshold <= 3; threshold++ {
		cfg := &reviews.Config{Threshold: threshold}

		for occurrences := 0; occurrences <= threshold+1; occurrences++ {
			l := model.Lesson{Symptom: "E501", Region: "web", Occurrences: occurrences}

			assert.Equal(t, !cfg.SurfacesMined(l), exclusionOf(cfg, l, false).BelowThreshold,
				"threshold %d, occurrences %d", threshold, occurrences)
		}
	}
}

func TestSelectionServesTwoConfigsFromOneEvidence(t *testing.T) {
	// The sample compares the saved config with a candidate on one
	// evidence read. Each selection equals its production wrapper, and
	// the typed lists show what the candidate changes.
	st := goldenStore(t)

	current := goldenConfig(
		reviews.PinRule{Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment."},
		reviews.PinRule{Rule: "multi-source-guard", Region: "db", Note: "Close cursors in a finally block."},
		reviews.PinRule{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
	)

	candidate := goldenConfig(
		reviews.PinRule{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
	)
	candidate.Mute = []reviews.MuteRule{{Rule: "RUF001"}}

	files := []string{"scripts/x.py", "db/a.go", "api/b.go"}

	ev, err := ReadEvidence(st, files, current, candidate)
	require.NoError(t, err)

	for _, cfg := range []*reviews.Config{current, candidate} {
		hook, err := ev.HookSelection(cfg)
		require.NoError(t, err)

		lessons, held, err := LessonsForFilesBudget(st, cfg, files, HookBudget(cfg))
		require.NoError(t, err)
		assert.Equal(t, lessons, hook.Lessons)
		assert.Equal(t, held, hook.HeldBack)
		assertSelectionAccounts(t, hook, ev.union(cfg, true).everyPin, true)

		changeSet, err := ev.ChangeSetSelection(cfg)
		require.NoError(t, err)

		lessons, trimmed, err := LessonsForFiles(st, cfg, files, cfg.ChangeSetBudget())
		require.NoError(t, err)
		assert.Equal(t, lessons, changeSet.Lessons)
		assert.Equal(t, trimmed, changeSet.HeldBack)
		assertSelectionAccounts(t, changeSet, ev.union(cfg, true).everyPin, false)
	}

	// The live pins of the current config cover two clusters. The
	// solitary finding is below the threshold under both configs.
	cur, err := ev.ChangeSetSelection(current)
	require.NoError(t, err)
	assert.Equal(t, "hard codes a postgres url including credentials\nsolitary finding\nclose every cursor\n",
		symptomsOf(excludedLessons(cur.Excluded)))
	assert.Equal(t, []bool{true, false, true}, []bool{
		cur.Excluded[0].Covered, cur.Excluded[1].Covered, cur.Excluded[2].Covered,
	})
	assert.True(t, cur.Excluded[1].BelowThreshold)
	assert.Equal(t, "RUF001\nwrap engine context\n", symptomsOf(cur.Mined))

	// The candidate removes those pins, so the two clusters return. The
	// candidate mute hides RUF001.
	cand, err := ev.ChangeSetSelection(candidate)
	require.NoError(t, err)
	assert.Equal(t, "RUF001\nsolitary finding\n", symptomsOf(excludedLessons(cand.Excluded)))
	assert.Equal(t, Exclusion{Lesson: cand.Excluded[0].Lesson, Mutes: []reviews.MuteRule{{Rule: "RUF001"}}},
		cand.Excluded[0])
	assert.Equal(t, Exclusion{Lesson: cand.Excluded[1].Lesson, BelowThreshold: true}, cand.Excluded[1])
	assert.Equal(t, "hard codes a postgres url including credentials\nclose every cursor\nwrap engine context\n",
		symptomsOf(cand.Mined))
}

func TestSelectionRefusesAForeignConfig(t *testing.T) {
	// The evidence holds the citations of the configs it read for. A
	// selection with another config could miss a covered cluster, so
	// both entry points refuse it. An equal copy is another config too.
	st := goldenStore(t)

	read := goldenConfig(reviews.PinRule{
		Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment.",
	})
	other := goldenConfig()
	equalCopy := *read

	ev, err := ReadEvidence(st, []string{"scripts/x.py"}, read)
	require.NoError(t, err)

	for _, cfg := range []*reviews.Config{other, &equalCopy, nil} {
		_, err := ev.HookSelection(cfg)
		require.ErrorIs(t, err, ErrForeignConfig)

		_, err = ev.ChangeSetSelection(cfg)
		require.ErrorIs(t, err, ErrForeignConfig)
	}

	_, err = ev.HookSelection(read)
	require.NoError(t, err)

	_, err = ev.ChangeSetSelection(read)
	require.NoError(t, err)
}

func TestSelectionWritesNothing(t *testing.T) {
	// A sample must not record a firing or touch the hook state. The
	// evidence read and both selections leave the workspace as it was.
	// The index files are left out: the store owns its own files.
	st := goldenStore(t)

	root, err := st.GetMeta(store.MetaRepoRoot)
	require.NoError(t, err)

	writeLessonsYAML(t, root, "pin:\n  - rule: hardcoded-db-credentials\n    region: scripts\n"+
		"    note: Read credentials from the environment.\n")

	current, err := reviews.LoadConfig(root)
	require.NoError(t, err)

	candidate := goldenConfig()
	candidate.Mute = []reviews.MuteRule{{Region: "scripts"}}

	before := workspaceTree(t, root)

	ev, err := ReadEvidence(st, []string{"scripts/x.py", "db/a.go", "web/new.ts"}, current, candidate)
	require.NoError(t, err)

	for _, cfg := range []*reviews.Config{current, candidate} {
		hook, err := ev.HookSelection(cfg)
		require.NoError(t, err)
		require.NotEmpty(t, hook.Lessons)

		_, err = ev.ChangeSetSelection(cfg)
		require.NoError(t, err)
	}

	assert.Equal(t, before, workspaceTree(t, root), "the workspace files are unchanged")
	assert.NoFileExists(t, reviews.AuditLogPath(root), "no firing record")
	// The hook state and its lock live next to the audit log. The tree
	// comparison above covers them, and these names make the failure clear.
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.json"))
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-hook-state.lock"))
}

// workspaceTree returns the path, mode, and content hash of every entry
// under root, except the index files.
func workspaceTree(t *testing.T, root string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if strings.HasPrefix(rel, "index.db") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		entry := info.Mode().String()

		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}

		tree[rel] = entry

		return nil
	})
	require.NoError(t, err)

	return tree
}

func TestChangeSetNamesKeepsTheRequestOrder(t *testing.T) {
	// One name per requested file, in request order, repeats included.
	// A file that the index does not know is in both lists, so its
	// lessons still apply.
	st, root := seedStore(t)

	files := []string{"./scripts/task.py", "web/new.ts", "scripts/task.py", "web/new.ts"}

	names, unindexed := ChangeSetNames(st, root, files)
	assert.Equal(t, []string{"scripts/task.py", "web/new.ts", "scripts/task.py", "web/new.ts"}, names)
	assert.Equal(t, []string{"web/new.ts", "web/new.ts"}, unindexed)

	names, unindexed = ChangeSetNames(st, root, []string{"scripts/task.py"})
	assert.Equal(t, []string{"scripts/task.py"}, names)
	assert.Nil(t, unindexed)

	// ChangeSet prints one line per unindexed name first, then one
	// section per indexed name.
	var b strings.Builder
	require.NoError(t, ChangeSet(&b, st, root, files))

	out := b.String()
	assert.True(t, strings.HasPrefix(out, "web/new.ts: not in the index (new file, or run `seamark index`)\n\n"+
		"web/new.ts: not in the index (new file, or run `seamark index`)\n\n"+
		"scripts/task.py\n"), out)
	assert.Equal(t, 2, strings.Count(out, "scripts/task.py\n  defines"), "one section per indexed name")
}

// excludedLessons returns the lesson of each exclusion, in order.
func excludedLessons(excluded []Exclusion) []model.Lesson {
	out := make([]model.Lesson, 0, len(excluded))
	for _, ex := range excluded {
		out = append(out, ex.Lesson)
	}

	return out
}
