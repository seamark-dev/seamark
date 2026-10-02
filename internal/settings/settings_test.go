package settings

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/scaffold"
)

// starterConfig returns the config.yaml that seamark init writes. The
// test reads it from the scaffold, so the fixture follows the template.
func starterConfig(t *testing.T) string {
	t.Helper()

	for _, doc := range scaffold.Documents("") {
		if doc.Path == File {
			body, err := doc.Compose(nil, false)
			require.NoError(t, err)

			return string(body)
		}
	}

	require.FailNow(t, "the scaffold has no config.yaml")

	return ""
}

// workspace creates a root with config.yaml holding text. A nil text
// leaves the file absent.
func workspace(t *testing.T, text *string) string {
	t.Helper()

	root := t.TempDir()

	if text != nil {
		writeConfig(t, root, *text)
	}

	return root
}

// withConfig returns a root whose config.yaml holds text.
func withConfig(t *testing.T, text string) string {
	t.Helper()

	return workspace(t, &text)
}

// writeConfig replaces config.yaml under root.
func writeConfig(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(configPath(root), []byte(text), 0o644))
}

// configPath returns the absolute config.yaml path under root.
func configPath(root string) string {
	return filepath.Join(root, ".seamark", "config.yaml")
}

// readConfig returns the config.yaml bytes under root.
func readConfig(t *testing.T, root string) string {
	t.Helper()

	data, err := os.ReadFile(configPath(root))
	require.NoError(t, err)

	return string(data)
}

// inspect takes a snapshot with the built-in registry.
func inspect(t *testing.T, root string) *Snapshot {
	t.Helper()

	snap, err := Inspect(root, integration.Builtin())
	require.NoError(t, err)

	return snap
}

// field returns one field state of a snapshot.
func field(t *testing.T, snap *Snapshot, f Field) FieldState {
	t.Helper()

	st, ok := snap.Field(f)
	require.True(t, ok, "field %s", f)

	return st
}

// tree maps every regular file under root to its content digest, so a
// test can prove that nothing else was written.
func tree(t *testing.T, root string) map[string][32]byte {
	t.Helper()

	out := map[string][32]byte{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(root, path)
		out[rel] = sha256.Sum256(data)

		return nil
	})
	require.NoError(t, err)

	return out
}

func TestInspectAbsentFileShowsDefaults(t *testing.T) {
	root := workspace(t, nil)
	snap := inspect(t, root)

	assert.False(t, snap.Guard.Exists)
	assert.NoError(t, snap.Blocked)
	assert.Empty(t, snap.ReaderErrs)
	assert.Equal(t, []string{"claude", "codex"}, snap.Presets)
	assert.Equal(t, "claude", snap.Invocation.Name)
	assert.Equal(t, filepath.Join(snap.Root, ".seamark", "config.yaml"), snap.Path)

	want := map[Field][2]string{
		AgentCLI:       {"claude", "claude"},
		AgentArgv:      {"none", "none"},
		DistillWrite:   {"off", "off"},
		ReviewWindow:   {"730 days", "730 days"},
		IndexGenerated: {"skip generated files", "skip generated files"},
		IndexExclude:   {"none", "none"},
	}

	require.Len(t, snap.Fields, len(want))

	for _, st := range snap.Fields {
		assert.Equal(t, want[st.Field][0], st.Default, "default of %s", st.Field)
		assert.Equal(t, want[st.Field][1], st.Effective, "effective of %s", st.Field)
		assert.Equal(t, SourceDefault, st.Source, "source of %s", st.Field)
		assert.False(t, st.Stored.Present, "stored of %s", st.Field)
		assert.NoError(t, st.Err)
	}

	for _, g := range []Group{GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing} {
		assert.NoError(t, snap.Editable(g), "group %s", g)
	}
}

func TestInspectStarterFile(t *testing.T) {
	root := withConfig(t, starterConfig(t))
	snap := inspect(t, root)

	require.NoError(t, snap.Blocked)

	generated := field(t, snap, IndexGenerated)
	assert.Equal(t, Stored{Present: true, Raw: "false", Line: 8}, generated.Stored)
	assert.Equal(t, SourceFile, generated.Source, "the starter stores false explicitly")
	assert.Equal(t, "skip generated files", generated.Effective)

	exclude := field(t, snap, IndexExclude)
	assert.True(t, exclude.Stored.Present)
	assert.Equal(t, SourceDefault, exclude.Source, "a null list is the default")
	assert.Equal(t, "none", exclude.Effective)

	assert.Equal(t, SourceDefault, field(t, snap, AgentCLI).Source, "the agent example is a comment")
	assert.Equal(t, "off", field(t, snap, DistillWrite).Effective)
}

func TestInspectShowsArgvOverride(t *testing.T) {
	root := withConfig(t, "agent:\n  cli: bogus\n  argv: [\"my-llm\", \"--token=abc123\"]\n")
	snap := inspect(t, root)

	cli := field(t, snap, AgentCLI)
	assert.Equal(t, SourceShadowed, cli.Source)
	assert.Equal(t, "custom command (agent.argv)", cli.Effective)
	assert.Contains(t, cli.Notes, "Ignored while agent.argv is set.")
	assert.Contains(t, cli.Notes, `"bogus" is not a registered preset.`)
	assert.NoError(t, cli.Err, "the resolver never validates a shadowed preset")

	argv := field(t, snap, AgentArgv)
	assert.Equal(t, SourceFile, argv.Source)
	assert.Equal(t, "my-llm --token=[REDACTED]", argv.Effective, "argv may hold secret-shaped flags")
	assert.NotContains(t, argv.Stored.Raw, "abc123")
	assert.Equal(t, "custom", snap.Invocation.Name)
}

// Review fix: the stored text of agent.cli and of values that a reader
// rejects showed secrets in full. The reader error under them was
// redacted, so the same screen hid a secret in one row only.
func TestInspectRedactsSecretShapedTextInEveryField(t *testing.T) {
	const secret = "hunter2SEKRETzz"

	cases := []struct {
		name, yaml string
		field      Field
	}{
		{"agent.cli with a flag", "agent:\n  cli: claude --api-key " + secret + "\n", AgentCLI},
		{"agent.cli under argv", "agent:\n  cli: claude --api-key " + secret + "\n  argv: [my-agent, \"-\"]\n", AgentCLI},
		{"agent.cli as a sequence", "agent:\n  cli: [claude, --api-key, " + secret + "]\n", AgentCLI},
		{"argv with the flag and its value as two items", "agent:\n  argv: [my-llm, --token, " + secret + "]\n", AgentArgv},
		{"argv with a quoted value item", "agent:\n  argv: [my-llm, --password, \"" + secret + " horse\"]\n", AgentArgv},
		{"distill.write as a string", "distill:\n  write: \"--token=" + secret + "\"\n", DistillWrite},
		{"window_days as a string", "reviews:\n  window_days: \"password: " + secret + "\"\n", ReviewWindow},
		{"exclude as a string", "index:\n  exclude: \"password: " + secret + "\"\n", IndexExclude},
		{"exclude pattern", "index:\n  exclude: [\"token=" + secret + "\"]\n", IndexExclude},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := inspect(t, withConfig(t, c.yaml))
			st := field(t, snap, c.field)

			require.True(t, st.Stored.Present)
			assert.Contains(t, st.Stored.Raw, "[REDACTED]")
			assert.NotContains(t, st.Stored.Raw, secret)
			assert.NotContains(t, st.Effective, secret)

			for _, note := range st.Notes {
				assert.NotContains(t, note, secret)
			}

			if err := snap.Editable(c.field.Group()); err != nil {
				assert.NotContains(t, err.Error(), secret, "a reader error can quote the value")
			}
		})
	}
}

func TestInspectKeepsPlainSequenceItems(t *testing.T) {
	st := field(t, inspect(t, withConfig(t, "agent:\n  argv: [my-llm, --model, fast, \"-\"]\n")), AgentArgv)

	assert.Equal(t, "[my-llm, --model, fast, -]", st.Stored.Raw, "an item after a plain flag stays")
}

func TestInspectReportsAnArgvWithoutCommand(t *testing.T) {
	snap := inspect(t, withConfig(t, "agent:\n  argv: [\"\"]\n"))

	require.Error(t, snap.InvocationErr)
	assert.EqualError(t, field(t, snap, AgentArgv).Err, "agent.argv must start with a command")
	assert.Equal(t, "unknown", field(t, snap, AgentArgv).Effective)
}

func TestInspectReportsAnUnknownPreset(t *testing.T) {
	snap := inspect(t, withConfig(t, "agent:\n  cli: Claude\n"))

	cli := field(t, snap, AgentCLI)
	require.Error(t, cli.Err, "matching is case-sensitive")
	assert.Contains(t, cli.Err.Error(), `unknown agent cli "Claude"`)
	assert.Equal(t, "unknown", cli.Effective)
	assert.NoError(t, snap.Editable(GroupInference), "choosing a preset repairs it")
}

func TestInspectMapsTheReviewWindow(t *testing.T) {
	cases := []struct {
		yaml, effective, note string
	}{
		{"reviews:\n  window_days: 0\n", "unlimited", ""},
		{"reviews:\n  window_days: 30\n", "30 days", ""},
		{"reviews:\n  window_days: 1\n", "1 day", ""},
		{"reviews:\n  window_days:\n", "730 days", ""},
		{"reviews:\n  window_days: -5\n", "unlimited", "A negative value means unlimited; the editor writes 0 for unlimited."},
		{"reviews:\n  window_days: 1.5\n", "1 day", `The reader reads "1.5" as 1.`},
		{"reviews:\n  window_days: 0x1e\n", "30 days", `The reader reads "0x1e" as 30.`},
	}

	for _, c := range cases {
		st := field(t, inspect(t, withConfig(t, c.yaml)), ReviewWindow)

		assert.Equal(t, c.effective, st.Effective, "yaml %q", c.yaml)
		assert.Contains(t, st.Notes, "The newest 200 review comments are always kept.")
		assert.Contains(t, st.Notes, "Fix mining keeps its fixed 365-day window.")

		if c.note != "" {
			assert.Contains(t, st.Notes, c.note, "yaml %q", c.yaml)
		}
	}
}

func TestInspectKeepsReaderFailuresPerSection(t *testing.T) {
	// A string window fails only the index reader. The other groups
	// stay editable, because their readers still accept the file.
	snap := inspect(t, withConfig(t, "reviews:\n  window_days: \"30\"\ndistill:\n  write: yes\n"))

	require.NoError(t, snap.Blocked)
	require.Contains(t, snap.ReaderErrs, ReaderIndex)
	assert.NotContains(t, snap.ReaderErrs, ReaderAgent)
	assert.NotContains(t, snap.ReaderErrs, ReaderDistill)

	window := field(t, snap, ReviewWindow)
	require.Error(t, window.Err)
	assert.Equal(t, "unknown", window.Effective, "a failed reader never shows the default")

	write := field(t, snap, DistillWrite)
	assert.Equal(t, "on", write.Effective, "YAML 1.1 yes means true")
	assert.Equal(t, "yes", write.Stored.Raw)

	err := snap.Editable(GroupEvidence)
	require.ErrorIs(t, err, ErrBlocked)

	var blocked *BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, 2, blocked.Line)

	assert.NoError(t, snap.Editable(GroupPinWrite))
	assert.NoError(t, snap.Editable(GroupInference))
}

func TestInspectDropsNullExcludeItems(t *testing.T) {
	st := field(t, inspect(t, withConfig(t, "index:\n  exclude: [~]\n")), IndexExclude)

	assert.Equal(t, "none", st.Effective, "yaml.v3 drops a null string item")
	assert.Equal(t, "[~]", st.Stored.Raw, "the stored text differs from the effective value")
}

func TestInspectBlocksAnUnquotedStarPatternForEveryGroup(t *testing.T) {
	snap := inspect(t, withConfig(t, "index:\n  exclude:\n    - *.pb.go\n"))

	require.ErrorIs(t, snap.Blocked, ErrBlocked)
	assert.Equal(t, 3, snap.BlockedAt)
	assert.Len(t, snap.ReaderErrs, 3, "a syntax error fails every reader")

	for _, g := range []Group{GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing} {
		assert.ErrorIs(t, snap.Editable(g), ErrBlocked)
	}
}

func TestInspectSanitizesStoredText(t *testing.T) {
	snap := inspect(t, withConfig(t, "agent:\n  cli: \"\\e[31mred\"\n"))

	st := field(t, snap, AgentCLI)
	assert.Equal(t, "[31mred", st.Stored.Raw, "the escape byte is removed")
	assert.NotContains(t, st.Stored.Raw, "\x1b")
}

func TestInspectEmptyAndCommentOnlyFiles(t *testing.T) {
	for _, text := range []string{"", "# nothing yet\n", "---\n"} {
		snap := inspect(t, withConfig(t, text))

		assert.True(t, snap.Guard.Exists)
		assert.NoError(t, snap.Blocked, "text %q", text)
		assert.Empty(t, snap.ReaderErrs, "text %q", text)
		assert.Equal(t, "claude", field(t, snap, AgentCLI).Effective)
	}
}

func TestInspectWritesNothing(t *testing.T) {
	root := withConfig(t, starterConfig(t))
	before := tree(t, root)

	inspect(t, root)
	inspect(t, workspace(t, nil))

	assert.Equal(t, before, tree(t, root))
}

func TestInspectRejectsInvalidArguments(t *testing.T) {
	_, err := Inspect("", integration.Builtin())
	require.Error(t, err)

	_, err = Inspect(t.TempDir(), nil)
	require.Error(t, err)
}

func TestInspectReportsAnUnreadableFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(configPath(root), 0o755))

	snap := inspect(t, root)

	require.ErrorIs(t, snap.Blocked, ErrBlocked)
	assert.Contains(t, snap.Blocked.Error(), "not a regular file")
	assert.Len(t, snap.ReaderErrs, 3)

	st := field(t, snap, DistillWrite)
	assert.Equal(t, "off", st.Default, "the default stays visible")
	assert.Equal(t, "unknown", st.Effective)
}

func TestFieldGroupsAndReaders(t *testing.T) {
	assert.Equal(t, []Field{AgentCLI, AgentArgv}, GroupInference.Fields())
	assert.Equal(t, []Field{DistillWrite}, GroupPinWrite.Fields())
	assert.Equal(t, []Field{ReviewWindow}, GroupEvidence.Fields())
	assert.Equal(t, []Field{IndexGenerated, IndexExclude}, GroupIndexing.Fields())

	assert.Equal(t, ReaderIndex, ReviewWindow.Reader(), "the index reader owns reviews.window_days")
	assert.Equal(t, ReaderAgent, AgentArgv.Reader())
	assert.Equal(t, ReaderDistill, DistillWrite.Reader())

	assert.Equal(t, "pin writing", GroupPinWrite.String())
	assert.Equal(t, "unknown", Group(99).String())
}
