package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

func TestUnsafeFilesBlockEveryGroup(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		line               int
	}{
		{"duplicate top-level key", "distill:\n  write: true\ndistill:\n  write: false\n", `duplicate key "distill" (first at line 1)`, 3},
		{"multiple documents", "distill:\n  write: true\n---\nagent:\n  cli: codex\n", "more than one YAML document", 3},
		{"broken later document", "distill:\n  write: true\n---\nagent: [\n", "more than one YAML document", 4},
		{"tab indentation", "agent:\n\tcli: claude\n", "not valid YAML", 2},
		{"syntax error", "agent: [\n", "not valid YAML", 1},
		{"sequence root", "- a\n", "not a mapping", 1},
		{"scalar root", "~\n", "not a mapping", 1},
		{"flow root", "{agent: {cli: codex}}\n", "flow style", 1},
		{"top-level merge", "base: &b {x: 1}\n<<: *b\n", "merge key", 2},
		{"mixed line breaks", "a: 1\r\nb: 2\n", "mixes CRLF and LF", 0},
		{"bare carriage return", "a: 1\rb: 2\n", "bare CR", 1},
		// Review fix: yaml.v3 counts these characters as line breaks, and
		// the editor splits lines on LF only.
		{"line separator in a comment", "# note\u2028\nagent:\n  cli: claude\n", "line break U+2028", 1},
		{"next line in a comment", "# note\u0085\nagent:\n  cli: claude\n", "line break U+0085", 1},
		{"paragraph separator in a comment", "# note\u2029\nagent:\n  cli: claude\n", "line break U+2029", 1},
		{"line separator in an unowned value", "custom:\n  x: \"a\u2028b\"\ndistill:\n  write: false\n", "line break U+2028", 2},
		{"line break after the owned keys", "agent:\n  cli: claude # \u0085\n", "line break U+0085", 2},
		{"UTF-16 text", "\xff\xfea\x00:\x00 \x001\x00\n\x00", "not UTF-8 text", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, c.yaml)
			snap := inspect(t, root)

			require.ErrorIs(t, snap.Blocked, ErrBlocked)
			assert.Contains(t, snap.Blocked.Error(), c.reason)
			assert.Equal(t, c.line, snap.BlockedAt)

			for _, g := range []Group{GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing} {
				edit := Edit{Field: g.Fields()[0], Reset: true}

				_, err := PlanPatch(snap, integration.Builtin(), []Edit{edit})
				require.ErrorIs(t, err, ErrBlocked, "group %s", g)
			}

			assert.Equal(t, c.yaml, readConfig(t, root))
		})
	}
}

func TestUnsafeSectionsBlockOnlyTheirGroup(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		blocked            Group
		line               int
	}{
		{"duplicate owned key", "index:\n  generated: true\n  generated: false\n", `repeats the key "generated"`, GroupIndexing, 3},
		{"alias on agent", "base: &b\n  cli: codex\nagent: *b\n", "alias (*b)", GroupInference, 3},
		{"merge in index", "defaults: &d\n  generated: true\nindex:\n  <<: *d\n", "merge key", GroupIndexing, 4},
		{"anchor on reviews", "reviews: &r\n  window_days: 30\nother: *r\n", "anchor (&r)", GroupEvidence, 1},
		{"flow mapping", "agent: {cli: claude}\n", "flow style", GroupInference, 1},
		{"scalar section", "distill: on\n", "not a mapping", GroupPinWrite, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, c.yaml)
			snap := inspect(t, root)

			require.NoError(t, snap.Blocked)

			err := snap.Editable(c.blocked)
			require.ErrorIs(t, err, ErrBlocked)
			assert.Contains(t, err.Error(), c.reason)

			var blocked *BlockedError
			require.ErrorAs(t, err, &blocked)
			assert.Equal(t, c.line, blocked.Line)

			_, err = PlanPatch(snap, integration.Builtin(), []Edit{{Field: c.blocked.Fields()[0], Reset: true}})
			require.ErrorIs(t, err, ErrBlocked)
			assert.Equal(t, c.yaml, readConfig(t, root))

			// Another group still saves, and the unsafe text stays as it
			// was: the editor never flattens an alias or drops a key.
			other := GroupPinWrite
			edit := Edit{Field: DistillWrite, Bool: true}

			if c.blocked == GroupPinWrite {
				other, edit = GroupIndexing, Edit{Field: IndexGenerated, Bool: true}
			}

			require.NoError(t, snap.Editable(other))

			save(t, root, edit)
			assert.Contains(t, readConfig(t, root), c.yaml[:len(c.yaml)-1])
		})
	}
}

func TestValuesWithoutASafeSpliceAreBlocked(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		edit               Edit
	}{
		{"multi-line plain scalar", "agent:\n  cli: cla\n    ude\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"value on its own line", "agent:\n  cli:\n    codex\n", "starts on its own line", Edit{Field: AgentCLI, String: "codex"}},
		{"literal block scalar", "agent:\n  cli: |\n    codex\n", "spans several lines", Edit{Field: AgentCLI, String: "claude"}},
		{"multi-line flow list", "index:\n  exclude: [a,\n    b]\n", "flow list of index.exclude spans several lines", Edit{Field: IndexExclude, List: []string{"c"}}},
		{"multi-line item", "index:\n  exclude:\n    - \"a\n      b\"\n", "an item of index.exclude spans several lines", Edit{Field: IndexExclude, List: []string{"c"}}},
		// Review fix: a continuation line that starts with "#" is value
		// text. A splice that keeps it would turn part of the value into a
		// comment.
		{"double-quoted value with a comment-like line", "agent:\n  cli: \"cla\n    # ude\"\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"single-quoted value with a comment-like line", "agent:\n  cli: 'cla\n    # ude'\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"quoted continuation at column 0", "agent:\n  cli: \"cla\n# ude\"\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"block scalar with comment-like lines only", "agent:\n  cli: |\n    # x\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"tag alone on the key line", "agent:\n  cli: !!str\n    \"claude\"\n", "spans several lines", Edit{Field: AgentCLI, String: "codex"}},
		{"list item with a comment-like line", "index:\n  exclude:\n    - \"a\n      # b\"\n", "an item of index.exclude spans several lines", Edit{Field: IndexExclude, List: []string{"c"}}},
		{"flow list with a comment-like line", "index:\n  exclude: [\"a\n    # b\"]\n", "flow list of index.exclude spans several lines", Edit{Field: IndexExclude, List: []string{"c"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, c.yaml)

			_, err := PlanPatch(inspect(t, root), integration.Builtin(), []Edit{c.edit})
			require.ErrorIs(t, err, ErrBlocked)
			assert.Contains(t, err.Error(), c.reason)
			assert.Equal(t, c.yaml, readConfig(t, root))
		})
	}
}

func TestUnicodeLineBreaksNeverPanic(t *testing.T) {
	edits := []Edit{
		{Field: AgentCLI, String: "codex"},
		{Field: AgentCLI, Reset: true},
		{Field: DistillWrite, Bool: true},
		{Field: ReviewWindow, Days: 30},
		{Field: IndexExclude, List: []string{"gen/"}},
	}

	for _, r := range []string{"\u0085", "\u2028", "\u2029"} {
		for _, text := range []string{
			"# note" + r + "\nagent:\n  cli: claude\ndistill:\n  write: false\n",
			"custom:\n  x: \"a" + r + "b\"\ndistill:\n  write: false\nindex:\n  exclude: [a]\n",
			"agent:\n  cli: claude\ndistill:\n  write: false # " + r + "\n",
		} {
			root := withConfig(t, text)
			snap := inspect(t, root)
			require.ErrorIs(t, snap.Blocked, ErrBlocked, "text %q", text)

			for _, e := range edits {
				require.NotPanics(t, func() {
					_, err := PlanPatch(snap, integration.Builtin(), []Edit{e})
					require.ErrorIs(t, err, ErrBlocked, "text %q, edit %v", text, e)
				})
			}

			assert.Equal(t, text, readConfig(t, root))
		}
	}
}

func TestLinkedFilesAreRefused(t *testing.T) {
	t.Run("linked file", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "real.yaml"), []byte("distill:\n  write: true\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
		require.NoError(t, os.Symlink("../real.yaml", configPath(root)))

		snap := inspect(t, root)
		assert.Equal(t, "on", field(t, snap, DistillWrite).Effective, "the readers follow the link")
		require.ErrorIs(t, snap.Blocked, ErrBlocked)
		assert.Contains(t, snap.Blocked.Error(), "symlink at .seamark/config.yaml")

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: false}})
		require.ErrorIs(t, err, ErrBlocked)

		data, err := os.ReadFile(filepath.Join(root, "real.yaml"))
		require.NoError(t, err)
		assert.Equal(t, "distill:\n  write: true\n", string(data))
	})

	t.Run("linked directory", func(t *testing.T) {
		root := t.TempDir()
		elsewhere := filepath.Join(root, "elsewhere")
		require.NoError(t, os.MkdirAll(elsewhere, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "config.yaml"), []byte("agent:\n  cli: codex\n"), 0o644))
		require.NoError(t, os.Symlink("elsewhere", filepath.Join(root, ".seamark")))

		snap := inspect(t, root)
		require.ErrorIs(t, snap.Blocked, ErrBlocked)
		assert.Contains(t, snap.Blocked.Error(), "symlink at .seamark;")

		_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: AgentCLI, String: "claude"}})
		require.ErrorIs(t, err, ErrBlocked)
	})
}

func TestReadOnlyFileIsRefused(t *testing.T) {
	root := withConfig(t, richConfig)
	require.NoError(t, os.Chmod(configPath(root), 0o444))

	snap := inspect(t, root)
	require.ErrorIs(t, snap.Blocked, ErrBlocked)
	assert.Contains(t, snap.Blocked.Error(), "read-only")

	_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
	require.ErrorIs(t, err, ErrBlocked)
	assert.Equal(t, richConfig, readConfig(t, root))
}

func TestSaveKeepsTheFileMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640, 0o664} {
		root := withConfig(t, richConfig)
		require.NoError(t, os.Chmod(configPath(root), mode))

		save(t, root, Edit{Field: DistillWrite, Bool: true})

		info, err := os.Stat(configPath(root))
		require.NoError(t, err)
		assert.Equal(t, mode, info.Mode().Perm())
	}
}

func TestChangesAfterInspectMakeThePlanStale(t *testing.T) {
	cases := []struct {
		name   string
		start  *string
		change func(t *testing.T, root string)
	}{
		{"content changed", ptr(richConfig), func(t *testing.T, root string) {
			writeConfig(t, root, richConfig+"# edited\n")
		}},
		{"mode changed", ptr(richConfig), func(t *testing.T, root string) {
			require.NoError(t, os.Chmod(configPath(root), 0o600))
		}},
		{"file created", nil, func(t *testing.T, root string) {
			writeConfig(t, root, "agent:\n  cli: codex\n")
		}},
		{"file removed", ptr(richConfig), func(t *testing.T, root string) {
			require.NoError(t, os.Remove(configPath(root)))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := workspace(t, c.start)
			snap := inspect(t, root)

			c.change(t, root)
			changed := tree(t, root)

			_, err := PlanPatch(snap, integration.Builtin(), []Edit{{Field: DistillWrite, Bool: true}})
			require.ErrorIs(t, err, ErrStale)
			assert.Contains(t, err.Error(), "inspect it again")
			assert.Equal(t, changed, tree(t, root), "a stale plan writes nothing")
		})
	}
}

// ptr returns a pointer to s.
func ptr(s string) *string {
	return &s
}

func TestChangesAfterPlanMakeTheSaveStale(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, root string)
		check  func(t *testing.T, root string)
	}{
		{
			"content changed",
			func(t *testing.T, root string) { writeConfig(t, root, "agent:\n  cli: codex\n") },
			func(t *testing.T, root string) { assert.Equal(t, "agent:\n  cli: codex\n", readConfig(t, root)) },
		},
		{
			"mode changed",
			func(t *testing.T, root string) { require.NoError(t, os.Chmod(configPath(root), 0o600)) },
			func(t *testing.T, root string) { assert.Equal(t, richConfig, readConfig(t, root)) },
		},
		{
			"replaced by a directory",
			func(t *testing.T, root string) {
				require.NoError(t, os.Remove(configPath(root)))
				require.NoError(t, os.MkdirAll(configPath(root), 0o755))
			},
			func(t *testing.T, root string) {
				info, err := os.Stat(configPath(root))
				require.NoError(t, err)
				assert.True(t, info.IsDir())
			},
		},
		{
			"directory replaced by a link",
			func(t *testing.T, root string) {
				moved := filepath.Join(root, "moved")
				require.NoError(t, os.Rename(filepath.Join(root, ".seamark"), moved))
				require.NoError(t, os.Symlink("moved", filepath.Join(root, ".seamark")))
			},
			func(t *testing.T, root string) {
				data, err := os.ReadFile(filepath.Join(root, "moved", "config.yaml"))
				require.NoError(t, err)
				assert.Equal(t, richConfig, string(data), "the link target is untouched")
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := withConfig(t, richConfig)
			p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})

			c.change(t, root)

			res, err := Save(p)
			require.ErrorIs(t, err, ErrStale)
			assert.False(t, res.Written)
			require.ErrorIs(t, res.Err, ErrStale)
			c.check(t, root)
		})
	}
}

func TestChangesAfterANoOpPlanMakeTheSaveStale(t *testing.T) {
	cases := []struct {
		name   string
		start  *string
		edit   Edit
		change func(t *testing.T, root string)
		want   *string
	}{
		{
			"content changed", ptr("distill:\n  write: true\n"), Edit{Field: DistillWrite, Bool: true},
			func(t *testing.T, root string) { writeConfig(t, root, "distill:\n  write: false\n") },
			ptr("distill:\n  write: false\n"),
		},
		{
			"file created", nil, Edit{Field: AgentCLI, Reset: true},
			func(t *testing.T, root string) { writeConfig(t, root, "agent:\n  cli: codex\n") },
			ptr("agent:\n  cli: codex\n"),
		},
		{
			"file removed", ptr("distill:\n  write: true\n"), Edit{Field: DistillWrite, Bool: true},
			func(t *testing.T, root string) { require.NoError(t, os.Remove(configPath(root))) },
			nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := workspace(t, c.start)

			p := planOf(t, root, c.edit)
			require.True(t, p.NoOp)

			c.change(t, root)

			res, err := Save(p)
			require.ErrorIs(t, err, ErrStale)
			assert.Contains(t, err.Error(), "inspect it again")
			assert.False(t, res.Written)
			require.ErrorIs(t, res.Err, ErrStale)

			if c.want == nil {
				_, err := os.Stat(configPath(root))
				assert.ErrorIs(t, err, os.ErrNotExist)

				return
			}

			assert.Equal(t, *c.want, readConfig(t, root), "a stale no-op writes nothing")
		})
	}
}

func TestAPlanSavesOnce(t *testing.T) {
	root := withConfig(t, richConfig)
	p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})

	_, err := Save(p)
	require.NoError(t, err)

	saved := readConfig(t, root)

	_, err = Save(p)
	require.ErrorIs(t, err, ErrStale, "the first save changed the guarded bytes")
	assert.Equal(t, saved, readConfig(t, root))
	assert.Equal(t, "on", field(t, inspect(t, root), DistillWrite).Effective)
}

func TestFailedWriteLeavesTheFileIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	root := withConfig(t, richConfig)
	p := planOf(t, root, Edit{Field: DistillWrite, Bool: true})

	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res, err := Save(p)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStale)
	assert.False(t, res.Written)
	assert.Equal(t, err, res.Err)
	assert.Equal(t, richConfig, readConfig(t, root))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file remains")
}

func TestSaveRejectsAnEmptyPlan(t *testing.T) {
	_, err := Save(nil)
	require.Error(t, err)

	snap := inspect(t, t.TempDir())

	// A refusal after the plan check reports its cause in Result.Err, as
	// every other refusal of SaveWith does.
	res, err := Save(&Plan{Snapshot: snap})
	require.ErrorContains(t, err, "the plan has no guarded write")
	assert.Equal(t, err, res.Err)
	assert.Equal(t, NotWritten, res.Outcome)
	assert.Equal(t, snap.Path, res.Path)

	root := withLessons(t, richLessons)
	p := planOf(t, root, Edit{Field: Threshold, Int: 4})
	p.Setup.Writes = nil

	res, err = Save(p)
	require.ErrorContains(t, err, "the plan has no guarded write")
	assert.Equal(t, err, res.Err)
	assert.Equal(t, NotWritten, res.Outcome)
	assert.Equal(t, lessonsPath(inspect(t, root).Root), res.Path)
	assert.Equal(t, richLessons, readLessons(t, root))
}
