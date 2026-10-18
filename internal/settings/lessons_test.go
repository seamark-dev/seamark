package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/scaffold"
)

// The inspection tests of the lessons document.

// starterLessons returns the lessons.yaml that seamark init writes. The
// test reads it from the scaffold, so the fixture follows the template.
func starterLessons(t *testing.T) string {
	t.Helper()

	for _, doc := range scaffold.Documents("") {
		if doc.Path == pinfile.File {
			body, err := doc.Compose(nil, false)
			require.NoError(t, err)

			return string(body)
		}
	}

	require.FailNow(t, "the scaffold has no lessons.yaml")

	return ""
}

// lessonsPath returns the absolute lessons.yaml path under root.
func lessonsPath(root string) string {
	return filepath.Join(root, ".seamark", "lessons.yaml")
}

// writeLessons replaces lessons.yaml under root.
func writeLessons(t testing.TB, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(lessonsPath(root), []byte(text), 0o644))
}

// withLessons returns a root whose lessons.yaml holds text and that has
// no config.yaml.
func withLessons(t testing.TB, text string) string {
	t.Helper()

	root := t.TempDir()
	writeLessons(t, root, text)

	return root
}

// readLessons returns the lessons.yaml bytes under root.
func readLessons(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(lessonsPath(root))
	require.NoError(t, err)

	return string(data)
}

// saveLessons plans and saves edits, and returns the result.
func saveLessons(t *testing.T, root string, edits ...Edit) Result {
	t.Helper()

	return save(t, root, edits...)
}

// entry returns the mute entry at YAML index i of a snapshot.
func entry(t *testing.T, snap *Snapshot, i int) MuteEntry {
	t.Helper()

	require.NotNil(t, snap.Lessons)
	require.Less(t, i, len(snap.Lessons.Mutes), "mute entry %d", i)

	return snap.Lessons.Mutes[i]
}

func TestInspectLessonsPathIsAbsoluteUnderTheRoot(t *testing.T) {
	root := t.TempDir()

	wd, err := os.Getwd()
	require.NoError(t, err)

	rel, err := filepath.Rel(wd, root)
	require.NoError(t, err)

	snap := inspect(t, rel)

	require.NotNil(t, snap.Lessons)
	assert.True(t, filepath.IsAbs(snap.Lessons.Path))
	assert.Equal(t, filepath.Join(snap.Root, ".seamark", "lessons.yaml"), snap.Lessons.Path)
	assert.False(t, snap.Lessons.Exists)
	assert.NoError(t, snap.Lessons.Blocked)
	assert.NoError(t, snap.Lessons.ReaderErr)
	assert.Equal(t, reviews.DefaultConfig(), snap.Lessons.Config, "an absent file reads as the defaults")
	assert.Empty(t, snap.Lessons.Mutes)
}

func TestFieldDocumentsAndLessonsGroups(t *testing.T) {
	assert.Equal(t, []Field{Threshold, PinBudget, ChangeBudget, HookDelivery}, GroupDelivery.Fields())
	assert.Equal(t, []Field{Mutes}, GroupMutes.Fields())

	assert.Equal(t, pinfile.File, GroupDelivery.Document())
	assert.Equal(t, pinfile.File, Mutes.Document())
	assert.Equal(t, File, DistillWrite.Document())
	assert.Equal(t, "lesson delivery", GroupDelivery.String())
	assert.Equal(t, "lesson mutes", GroupMutes.String())
	assert.Equal(t, ReaderLessons, HookDelivery.Reader())

	assert.Empty(t, Field("index.depth").Document(), "an unknown field has no document")
	assert.Equal(t, "unknown", Field("index.depth").Group().String(), "an unknown field joins no group")
	assert.Empty(t, Field("index.depth").Reader())

	order := []Field{
		AgentCLI, AgentArgv, DistillWrite, ReviewWindow, IndexGenerated, IndexExclude,
		Threshold, PinBudget, ChangeBudget, HookDelivery, Mutes,
	}

	var shown []Field
	for _, st := range inspect(t, t.TempDir()).Fields {
		shown = append(shown, st.Field)
	}

	assert.Equal(t, order, shown, "the config groups first, then delivery, then mutes")
}

func TestInspectLessonsKeepsTheStoredTextAndExplainsIt(t *testing.T) {
	cases := []struct {
		yaml      string
		field     Field
		raw       string
		effective string
		note      string
		source    Source
	}{
		{"threshold: 0\n", Threshold, "0", "2", "stored 0 → default 2", SourceFile},
		{"threshold: -1\n", Threshold, "-1", "2", "stored -1 → default 2", SourceFile},
		{"threshold: 1.5\n", Threshold, "1.5", "1", "stored 1.5 → 1", SourceFile},
		{"threshold: 0x10\n", Threshold, "0x10", "16", "stored 0x10 → 16", SourceFile},
		{"threshold: 4\n", Threshold, "4", "4", "", SourceFile},
		{"pin_budget: 100\n", PinBudget, "100", "8", "stored 100 → at most 8 pins per edit (the hook total cap)", SourceFile},
		{"pin_budget: 0\n", PinBudget, "0", "3", "stored 0 → default 3", SourceFile},
		{"pin_budget: 0x10\n", PinBudget, "0x10", "8", "stored 0x10 → at most 8 pins per edit (the hook total cap)", SourceFile},
		{"change_budget: -1\n", ChangeBudget, "-1", "6", "stored -1 → default 6", SourceFile},
		{"change_budget: 1.5\n", ChangeBudget, "1.5", "1", "stored 1.5 → 1", SourceFile},
		{"hook_delivery: ''\n", HookDelivery, "", "always", "", SourceFile},
		{"hook_delivery: ~\n", HookDelivery, "~", "always", "", SourceDefault},
		{"hook_delivery: once-per-context\n", HookDelivery, "once-per-context", "once-per-context", "", SourceFile},
	}

	for _, c := range cases {
		t.Run(c.yaml, func(t *testing.T) {
			st := field(t, inspect(t, withLessons(t, c.yaml)), c.field)

			require.NoError(t, st.Err)
			assert.True(t, st.Stored.Present)
			assert.Equal(t, 1, st.Stored.Line)
			assert.Equal(t, c.raw, st.Stored.Raw, "the stored text shows as written")
			assert.Equal(t, c.effective, st.Effective)
			assert.Equal(t, c.source, st.Source)

			if c.note == "" {
				assert.Empty(t, st.Notes)

				return
			}

			assert.Equal(t, []string{c.note}, st.Notes)
		})
	}
}

// B3: the number editor marks its number choice with Number. It reads
// the stored node, because the reader keeps the default in place of a
// nonpositive threshold.
func TestNumberIsTheStoredValueThatTheReaderUses(t *testing.T) {
	cases := []struct {
		yaml  string
		field Field
		n     int
		ok    bool
	}{
		{"threshold: 4\n", Threshold, 4, true},
		{"threshold: 2\n", Threshold, 2, true},
		{"threshold: 0\n", Threshold, 0, false},
		{"threshold: -1\n", Threshold, 0, false},
		{"threshold: 0x10\n", Threshold, 16, true},
		{"threshold: ~\n", Threshold, 0, false},
		{"pin_budget: 100\n", PinBudget, 100, true},
		{"change_budget: 1.5\n", ChangeBudget, 1, true},
		{"other: 1\n", ChangeBudget, 0, false},
		{"hook_delivery: always\n", HookDelivery, 0, false},
	}

	for _, c := range cases {
		t.Run(c.yaml, func(t *testing.T) {
			n, ok := inspect(t, withLessons(t, c.yaml)).Lessons.Number(c.field)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.n, n)
		})
	}
}

func TestInspectLessonsListsEveryMuteItem(t *testing.T) {
	text := `mute:
  -
  - rule: F541   # noisy
  - {}
  - rule: f541
  - region: api/
  - region: api
    extra: x
  - {rule: E1, region: 7}
`
	snap := inspect(t, withLessons(t, text))

	require.NoError(t, snap.Lessons.Blocked)
	require.NoError(t, snap.Lessons.ReaderErr)
	require.Len(t, snap.Lessons.Mutes, 7, "a null item is an entry too")

	assert.Equal(t, MuteEntry{Index: 0, Line: 2, NoOp: true, DuplicateOf: -1, RawSHA: entry(t, snap, 0).RawSHA}, entry(t, snap, 0))

	assert.Equal(t, "F541", entry(t, snap, 1).Rule)
	assert.Equal(t, 3, entry(t, snap, 1).Line)
	assert.Equal(t, -1, entry(t, snap, 1).DuplicateOf)

	assert.True(t, entry(t, snap, 2).NoOp, "{} matches nothing")
	assert.True(t, entry(t, snap, 2).Flow)
	assert.Equal(t, -1, entry(t, snap, 2).DuplicateOf, "an entry that matches nothing is no duplicate")

	assert.Equal(t, 1, entry(t, snap, 3).DuplicateOf, "the rule matches in any case")
	assert.Equal(t, -1, entry(t, snap, 5).DuplicateOf, `"api" and "api/" are different regions`)
	assert.Equal(t, []string{"extra"}, entry(t, snap, 5).Extra)
	assert.Equal(t, 7, entry(t, snap, 5).Line)

	flow := entry(t, snap, 6)
	assert.True(t, flow.Flow)
	assert.Equal(t, "E1", flow.Rule)
	assert.Equal(t, "7", flow.Region, "the reader reads the number as text")

	for i, e := range snap.Lessons.Mutes {
		assert.Equal(t, i, e.Index)
		assert.NotZero(t, e.RawSHA, "entry %d", i)
	}

	assert.Len(t, snap.Lessons.Config.Mute, 6, "the reader drops the null item")
	assert.Equal(t, "6 entries", field(t, snap, Mutes).Effective)
	assert.Equal(t, "7 entries", field(t, snap, Mutes).Stored.Raw)
}

func TestLessonsReaderErrorBlocksOnlyTheLessonsGroups(t *testing.T) {
	root := withConfig(t, richConfig)
	writeLessons(t, root, "hook_delivery: Always\nthreshold: 3\n")

	snap := inspect(t, root)

	require.NoError(t, snap.Lessons.Blocked, "a reader error is not a global block")
	require.Error(t, snap.Lessons.ReaderErr)
	assert.Equal(t, snap.Lessons.ReaderErr, snap.ReaderErrs[ReaderLessons])
	assert.NotContains(t, snap.ReaderErrs, ReaderAgent)

	for _, g := range []Group{GroupDelivery, GroupMutes} {
		err := snap.Editable(g)
		require.ErrorIs(t, err, ErrBlocked, "group %s", g)
		assert.Contains(t, err.Error(), "the lessons reader rejects the file")
		assert.Contains(t, err.Error(), "edit .seamark/lessons.yaml by hand")
	}

	for _, g := range []Group{GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing} {
		assert.NoError(t, snap.Editable(g), "group %s", g)
	}

	st := field(t, snap, Threshold)
	require.Error(t, st.Err)
	assert.Equal(t, "unknown", st.Effective, "a failed reader never shows the default")
	assert.Equal(t, "3", st.Stored.Raw, "the stored text stays visible")

	_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: HookDelivery, String: "always"}})
	require.ErrorIs(t, err, ErrBlocked, "the editor never repairs a file that the reader rejects")
	assert.Equal(t, "hook_delivery: Always\nthreshold: 3\n", readLessons(t, root))

	save(t, root, Edit{Field: DistillWrite, Bool: true})
	assert.Equal(t, "on", field(t, inspect(t, root), DistillWrite).Effective)
}

func TestTheTwoDocumentsAreIndependent(t *testing.T) {
	t.Run("an unsafe config.yaml never blocks a lessons group", func(t *testing.T) {
		for _, text := range []string{"agent: [\n", "distill:\n  write: yes\ndistill:\n  write: no\n"} {
			root := withConfig(t, text)
			writeLessons(t, root, "threshold: 2\n")

			snap := inspect(t, root)
			require.ErrorIs(t, snap.Blocked, ErrBlocked)
			require.NoError(t, snap.Lessons.Blocked)
			assert.NotContains(t, snap.ReaderErrs, ReaderLessons)

			assert.NoError(t, snap.Editable(GroupDelivery))
			assert.NoError(t, snap.Editable(GroupMutes))

			res := saveLessons(t, root, Edit{Field: Threshold, Int: 4})
			assert.True(t, res.Written)
			assert.Equal(t, "threshold: 4\n", readLessons(t, root))
			assert.Equal(t, text, readConfig(t, root), "config.yaml stays as it was")
		}
	})

	t.Run("an unreadable config.yaml never blocks a lessons group", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(configPath(root), 0o755))

		snap := inspect(t, root)
		require.ErrorIs(t, snap.Blocked, ErrBlocked)
		require.NotNil(t, snap.Lessons, "the lessons document is read on every path")
		assert.NoError(t, snap.Editable(GroupMutes))

		saveLessons(t, root, Edit{Field: Mutes, Mute: &MuteEdit{Op: MuteAdd, Rule: "F541"}})
		assert.Equal(t, "mute:\n  - rule: F541\n", readLessons(t, root))
	})

	t.Run("an unsafe lessons.yaml never blocks a config group", func(t *testing.T) {
		root := withConfig(t, richConfig)
		writeLessons(t, root, "threshold: 2\r\n")

		snap := inspect(t, root)
		require.ErrorIs(t, snap.Lessons.Blocked, ErrBlocked)
		require.NoError(t, snap.Blocked)

		for _, g := range []Group{GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing} {
			assert.NoError(t, snap.Editable(g), "group %s", g)
		}

		save(t, root, Edit{Field: DistillWrite, Bool: true})
		assert.Equal(t, "threshold: 2\r\n", readLessons(t, root))
	})

	t.Run("a change of one file never makes a plan of the other stale", func(t *testing.T) {
		root := withConfig(t, richConfig)
		writeLessons(t, root, "threshold: 2\n")

		snap := inspect(t, root)

		writeConfig(t, root, richConfig+"# edited\n")

		p, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: Threshold, Int: 3}})
		require.NoError(t, err)

		writeConfig(t, root, richConfig+"# edited again\n")

		_, err = Save(p)
		require.NoError(t, err)
		assert.Equal(t, "threshold: 3\n", readLessons(t, root))

		writeLessons(t, root, "threshold: 7\n")

		cp, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
		require.NoError(t, err)

		writeLessons(t, root, "threshold: 8\n")

		_, err = Save(cp)
		require.NoError(t, err)
	})
}

func TestUnsafeLessonsFilesBlockBothLessonsGroups(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		line               int
	}{
		{"CRLF line breaks", "threshold: 2\r\nmute:\r\n  - rule: a\r\n", "the file uses CRLF line breaks", 0},
		{"mixed line breaks", "threshold: 2\r\nmute:\n", "mixes CRLF and LF", 0},
		{"bare carriage return", "threshold: 2\rmute:\n", "the file uses bare CR line breaks", 1},
		{"two documents", "threshold: 2\n---\nmute: []\n", "more than one YAML document", 2},
		{"syntax error", "mute: [\n", "not valid YAML", 1},
		{"sequence root", "- a\n", "not a mapping", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withLessons(t, c.yaml)
			snap := inspect(t, root)

			require.ErrorIs(t, snap.Lessons.Blocked, ErrBlocked)
			assert.Contains(t, snap.Lessons.Blocked.Error(), c.reason)
			assert.Contains(t, snap.Lessons.Blocked.Error(), "edit .seamark/lessons.yaml by hand")
			assert.Equal(t, c.line, snap.Lessons.BlockedAt)

			for _, e := range []Edit{
				{Field: Threshold, Int: 5},
				{Field: HookDelivery, Reset: true},
				{Field: Mutes, Mute: &MuteEdit{Op: MuteAdd, Rule: "F541"}},
			} {
				_, err := PlanPatch(snap, integration.Builtin(), []Edit{e})
				require.ErrorIs(t, err, ErrBlocked, "edit %s", e.Field)
			}

			assert.Equal(t, c.yaml, readLessons(t, root))
		})
	}

	t.Run("CRLF values stay visible", func(t *testing.T) {
		snap := inspect(t, withLessons(t, "threshold: 4\r\n"))

		assert.Equal(t, "4", field(t, snap, Threshold).Effective)
		assert.Equal(t, "4", field(t, snap, Threshold).Stored.Raw)
	})
}

func TestUnsafeLessonsKeysBlockOnlyTheirGroup(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		blocked, other     Group
		line               int
	}{
		{"alias in the mute list", "base: &b\n  rule: a\nmute:\n  - *b\n", "alias (*b)", GroupMutes, GroupDelivery, 4},
		{"anchor on the mute key", "mute: &m\n  - rule: a\nother: *m\n", "anchor (&m)", GroupMutes, GroupDelivery, 1},
		{"flow mute list", "mute: [{rule: a}]\n", "flow style [ ]", GroupMutes, GroupDelivery, 1},
		{"empty flow mute list", "mute: []\n", "flow style [ ]", GroupMutes, GroupDelivery, 1},
		{"anchor on a delivery value", "threshold: &t 3\nother: *t\n", "anchor (&t)", GroupDelivery, GroupMutes, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := inspect(t, withLessons(t, c.yaml))

			require.NoError(t, snap.Lessons.Blocked)
			require.NoError(t, snap.Lessons.ReaderErr)

			err := snap.Editable(c.blocked)
			require.ErrorIs(t, err, ErrBlocked)
			assert.Contains(t, err.Error(), c.reason)

			var blocked *BlockedError
			require.ErrorAs(t, err, &blocked)
			assert.Equal(t, c.line, blocked.Line)
			assert.Equal(t, pinfile.File, blocked.File)

			assert.NoError(t, snap.Editable(c.other))
		})
	}

	// The reader rejects a duplicate key in a mute item, so the reader
	// error blocks both lessons groups before the structural issue.
	t.Run("duplicate key in a mute item", func(t *testing.T) {
		snap := inspect(t, withLessons(t, "mute:\n  - rule: a\n    rule: b\n"))

		require.Error(t, snap.Lessons.ReaderErr)
		require.ErrorIs(t, snap.Editable(GroupDelivery), ErrBlocked)
		require.ErrorIs(t, snap.Editable(GroupMutes), ErrBlocked)
	})

	t.Run("a flow list still lists its entries", func(t *testing.T) {
		snap := inspect(t, withLessons(t, "mute: [{rule: a}, {region: b}]\n"))

		require.Len(t, snap.Lessons.Mutes, 2)
		assert.True(t, snap.Lessons.Mutes[1].Flow)
		assert.Equal(t, "b", snap.Lessons.Mutes[1].Region)
	})
}

func TestLinkedAndReadOnlyLessonsFilesAreRefused(t *testing.T) {
	t.Run("symbolic link", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "real.yaml"), []byte("threshold: 3\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.Symlink("../real.yaml", lessonsPath(root)))

		snap := inspect(t, root)
		assert.Equal(t, "3", field(t, snap, Threshold).Effective, "the reader follows the link")
		require.ErrorIs(t, snap.Lessons.Blocked, ErrBlocked)
		assert.Contains(t, snap.Lessons.Blocked.Error(), "symlink at .seamark/lessons.yaml")

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: Threshold, Int: 4}})
		require.ErrorIs(t, err, ErrBlocked)
	})

	t.Run("read-only file", func(t *testing.T) {
		root := withLessons(t, "threshold: 3\n")
		require.NoError(t, os.Chmod(lessonsPath(root), 0o444))

		snap := inspect(t, root)
		require.ErrorIs(t, snap.Lessons.Blocked, ErrBlocked)
		assert.Contains(t, snap.Lessons.Blocked.Error(), "read-only")
	})
}

func TestBlockedErrorNamesItsFile(t *testing.T) {
	err := &BlockedError{Reason: "duplicate key", Line: 3, File: pinfile.File}

	assert.Equal(t, "duplicate key (line 3); edit .seamark/lessons.yaml by hand", err.Error())
	assert.Equal(t, "no line; edit .seamark/config.yaml by hand", (&BlockedError{Reason: "no line"}).Error(),
		"an empty file names config.yaml for the callers before the lessons document")
}

func TestCheckMute(t *testing.T) {
	cases := []struct {
		name, rule, region string
		want               MuteInput
		err                error
	}{
		{"rule only", "F541", "", MuteInput{Rule: "F541"}, nil},
		{"region only keeps a trailing slash", "", "api/", MuteInput{Region: "api/"}, nil},
		{"region is normalized", "E1", " ./api ", MuteInput{Rule: "E1", Region: "api", Notes: []string{"surrounding spaces removed", `leading "./" removed`}}, nil},
		{"a comma stays literal", "", "a, b", MuteInput{Region: "a, b"}, nil},
		{"nothing", "", "", MuteInput{}, ErrEmptyMute},
		{"spaces around the rule", " F541", "", MuteInput{}, reviews.ErrAuthoredText},
		{"control character in the rule", "F\x1b541", "", MuteInput{}, reviews.ErrAuthoredText},
		{"rule on two lines", "F5\n41", "", MuteInput{}, reviews.ErrAuthoredText},
		{"a star region", "", "*", MuteInput{}, reviews.ErrRegionInput},
		{"a blank region", "F541", "   ", MuteInput{}, reviews.ErrRegionInput},
		{"an absolute region", "", "/etc", MuteInput{}, reviews.ErrRegionInput},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CheckMute(c.rule, c.region)

			if c.err != nil {
				require.ErrorIs(t, err, c.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestOverlapsAskTheMatcher(t *testing.T) {
	snap := inspect(t, withLessons(t, `mute:
  - region: api/
  - rule: F541
  - rule: E1
    region: db
  - {}
`))

	lines := func(entries []MuteEntry) []int {
		var out []int
		for _, e := range entries {
			out = append(out, e.Line)
		}

		return out
	}

	cases := []struct {
		rule, region string
		want         []int
	}{
		{"", "api", []int{2}},
		{"", "api/x", []int{2}},
		{"", "apix", nil},
		{"f541", "web", []int{3}},
		{"E1", "", []int{4}},
		{"E2", "db/sub", nil},
		{"", "db", []int{4}},
		{"W1", "api", []int{2}},
		{"W1", "web", nil},
		{"E1", "db/sub", []int{4}},
		{"e1", "api", []int{2}},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, lines(snap.Lessons.Overlaps(c.rule, c.region, -1)), "rule %q region %q", c.rule, c.region)
	}

	assert.Empty(t, snap.Lessons.Overlaps("", "api", 0), "the entry itself is no overlap")

	d, ok := snap.Lessons.Duplicate("f541", "", -1)
	require.True(t, ok)
	assert.Equal(t, 3, d.Line)

	_, ok = snap.Lessons.Duplicate("", "api", -1)
	assert.False(t, ok, `"api" overlaps "api/" but does not duplicate it`)

	_, ok = snap.Lessons.Duplicate("F541", "", 1)
	assert.False(t, ok, "the changed entry is not its own duplicate")
}

func TestInspectLessonsWritesNothing(t *testing.T) {
	root := withLessons(t, starterLessons(t))
	before := tree(t, root)

	snap := inspect(t, root)
	require.NoError(t, snap.Lessons.Blocked)

	assert.Equal(t, before, tree(t, root))
}
