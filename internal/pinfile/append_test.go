package pinfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// The notes in these tests compare the append with the old writer of the
// CLI apply, distill.ApplyPins. The CLI and the console now share the
// append, and the old writer is deleted; git history keeps it. A note
// on a case says what the old writer did on that layout.

// appendBlock is the text that distill.RenderPins writes for two rows:
// one region, and a region set with a block note. The blank line of the
// note holds the two spaces of the render indentation.
const appendBlock = "  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p1)\n" +
	"  - rule: first-rule\n" +
	"    region: api\n" +
	"    note: The first pending pin.\n" +
	"  # distilled by claude/v6 from 3 findings (seamark lessons --distill, p2)\n" +
	"  - rule: multi\n" +
	"    region: api\n" +
	"    regions: [api, db]\n" +
	"    note: |-\n" +
	"      first line\n" +
	"      # not a comment\n" +
	"  \n" +
	"      last line\n"

// handEntry is a hand-written entry that every append must keep.
const handEntry = "  # Keep scripts ASCII.\n" +
	"  - rule: hand-written\n" +
	"    region: web\n" +
	"    note: A person wrote this pin.\n"

// shiftBlock returns appendBlock with its lines at indent spaces.
func shiftBlock(indent int) string {
	pad := strings.Repeat(" ", indent)
	lines := strings.SplitAfter(strings.TrimSuffix(appendBlock, "\n"), "\n")

	var b strings.Builder

	for _, l := range lines {
		b.WriteString(pad + strings.TrimPrefix(l, "  "))
	}

	return b.String() + "\n"
}

// planAppendText plans the append of appendBlock to the existing text
// before and returns the candidate as text.
func planAppendText(t *testing.T, before string) (string, error) {
	t.Helper()

	d := Parse([]byte(before))
	d.Guard.Exists = true

	after, err := PlanAppend(d, appendBlock)

	return string(after), err
}

func TestPlanAppendKeepsEveryExistingLine(t *testing.T) {
	tests := []struct {
		name   string
		before string
		want   string
	}{
		{
			// The bytes are the bytes that the old writer wrote.
			name:   "existing list",
			before: "# My tuning notes.\nthreshold: 3\n\npin:\n" + handEntry,
			want:   "# My tuning notes.\nthreshold: 3\n\npin:\n" + appendBlock + handEntry,
		},
		{
			name:   "empty section before another key",
			before: "pin:\nmute:\n  - rule: F541\n",
			want:   "pin:\n" + appendBlock + "mute:\n  - rule: F541\n",
		},
		{
			name:   "pin key on the last line without a final line break",
			before: "threshold: 3\npin:",
			want:   "threshold: 3\npin:\n" + strings.TrimSuffix(appendBlock, "\n"),
		},
		{
			name:   "no pin section",
			before: "threshold: 2\n",
			want:   "threshold: 2\n\npin:\n" + appendBlock,
		},
		{
			name:   "no pin section and no final line break",
			before: "threshold: 2",
			want:   "threshold: 2\n\npin:\n" + appendBlock,
		},
		{
			name:   "no pin section after one blank line",
			before: "threshold: 2\n\n",
			want:   "threshold: 2\n\npin:\n" + appendBlock,
		},
		{
			// The old writer removed the trailing blank lines down to one.
			// The append adds lines only, so the user's blank lines stay.
			name:   "no pin section after several blank lines",
			before: "threshold: 2\n\n\n",
			want:   "threshold: 2\n\n\npin:\n" + appendBlock,
		},
		{
			// The old writer wrote a blank first line here.
			name:   "empty file",
			before: "",
			want:   "pin:\n" + appendBlock,
		},
		{
			// The old writer refused the comment as a flow-style section.
			name:   "comment on the pin line",
			before: "pin: # my pins\n" + handEntry,
			want:   "pin: # my pins\n" + appendBlock + handEntry,
		},
		{
			// The old writer put two-space entries above this list, and
			// the result did not parse.
			name:   "list at column 4",
			before: "pin:\n" + shiftEntry(4),
			want:   "pin:\n" + shiftBlock(4) + shiftEntry(4),
		},
		{
			name:   "list at column 0",
			before: "pin:\n" + shiftEntry(0),
			want:   "pin:\n" + shiftBlock(0) + shiftEntry(0),
		},
		{
			// The old writer added a second pin key at column 0 that no
			// reader delivered.
			name:   "indented top level",
			before: "  threshold: 3\n  pin:\n" + shiftEntry(4),
			want:   "  threshold: 3\n  pin:\n" + shiftBlock(4) + shiftEntry(4),
		},
		{
			name:   "indented top level without a pin section",
			before: "  threshold: 3\n",
			want:   "  threshold: 3\n\n  pin:\n" + shiftBlock(4),
		},
		{
			// The old writer appended the section after the end marker,
			// where no reader reads it.
			name:   "document end marker",
			before: "threshold: 2\n...\n# after the end\n",
			want:   "threshold: 2\n\npin:\n" + appendBlock + "...\n# after the end\n",
		},
		{
			// The old writer wrote LF lines into the CRLF file.
			name:   "CRLF line breaks",
			before: crlf("threshold: 3\npin:\n" + handEntry),
			want:   crlf("threshold: 3\npin:\n" + appendBlock + handEntry),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planAppendText(t, tt.before)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			// The new entries read back first, and every earlier pin follows.
			cfg, err := reviews.ParseConfig([]byte(got))
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(cfg.Pin), 2)
			assert.Equal(t, "first-rule", cfg.Pin[0].Rule)
			assert.Equal(t, "first line\n# not a comment\n\nlast line", cfg.Pin[1].Note)
		})
	}
}

// shiftEntry returns handEntry with its lines at indent spaces.
func shiftEntry(indent int) string {
	pad := strings.Repeat(" ", indent)

	var b strings.Builder

	for _, l := range strings.SplitAfter(strings.TrimSuffix(handEntry, "\n"), "\n") {
		b.WriteString(pad + strings.TrimPrefix(l, "  "))
	}

	return b.String() + "\n"
}

// crlf returns text with CRLF line breaks.
func crlf(text string) string {
	return strings.ReplaceAll(text, "\n", "\r\n")
}

func TestPlanAppendStartsAnAbsentFileWithTheHeader(t *testing.T) {
	// The bytes are the bytes that the old writer wrote.
	after, err := PlanAppend(Parse(nil), appendBlock)
	require.NoError(t, err)
	assert.Equal(t, "# Review-lesson tuning (see `seamark lessons --list`).\n\npin:\n"+appendBlock, string(after))

	// An existing empty file is a file: it gets no header.
	d := Parse(nil)
	d.Guard.Exists = true

	after, err = PlanAppend(d, appendBlock)
	require.NoError(t, err)
	assert.Equal(t, "pin:\n"+appendBlock, string(after))
}

func TestPlanAppendRefusesWhatItCannotExtend(t *testing.T) {
	tests := []struct {
		name, before, want string
	}{
		{
			// The text of the old writer stays.
			name:   "flow list",
			before: "pin: []\n",
			want:   "lessons.yaml has a flow-style pin section; add the entries by hand",
		},
		{
			// The old writer called this a flow-style section.
			name:   "null token",
			before: "pin: ~\n",
			want:   "lessons.yaml has a pin section that is not a plain block list; add the entries by hand",
		},
		{
			name:   "anchor on the list",
			before: "pin: &pins\n" + handEntry,
			want:   "lessons.yaml has a pin section that is not a plain block list; add the entries by hand",
		},
		{
			// The old writer appended the section in the second document,
			// where no reader reads it.
			name:   "two documents",
			before: "threshold: 2\n---\nmute: []\n",
			want: "lessons.yaml cannot take the new pins safely: the file holds more than one YAML " +
				"document (line 2); add the entries by hand",
		},
		{
			// The old writer added LF lines to the mixed file.
			name:   "mixed line breaks",
			before: "threshold: 3\r\npin:\n" + handEntry,
			want:   "lessons.yaml cannot take the new pins safely: the file mixes CRLF and LF line breaks; add the entries by hand",
		},
		{
			name:   "file that the reader rejects",
			before: "hook_delivery: sometimes\n",
			want: "lessons.yaml cannot take the new pins safely: the lessons reader rejects the file: " +
				`lessons config: hook_delivery must be "always" or "once-per-context", got "sometimes"; add the entries by hand`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planAppendText(t, tt.before)
			require.ErrorIs(t, err, ErrUnsupported)
			assert.Equal(t, tt.want, err.Error())
			assert.Empty(t, got)
		})
	}
}

func TestPlanAppendNeedsARenderedBlock(t *testing.T) {
	for name, block := range map[string]string{
		"empty block":           "",
		"line without indent":   "- rule: x\n",
		"entries that are keys": "  rule: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanAppend(Parse([]byte("pin:\n")), block)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), "pinfile: "), err.Error())
		})
	}

	_, err := PlanAppend(nil, appendBlock)
	require.Error(t, err)
}

func TestVerifyAppendRefusesEveryOtherChange(t *testing.T) {
	const before = "# Notes.\nthreshold: 3\npin:\n" + handEntry

	a := analyze([]byte(before))
	add, err := parseBlock(appendBlock)
	require.NoError(t, err)

	ins := add.shifted(2)
	good := "# Notes.\nthreshold: 3\npin:\n" + appendBlock + handEntry

	require.NoError(t, a.verifyAppend([]byte(before), []byte(good), add, 3, ins, true))

	tests := []struct {
		name string
		cand string
		add  func(*addition) *addition
		want string
	}{
		{
			name: "candidate that does not parse",
			cand: good + "pin: [\n",
			want: "refusing to write: edited lessons.yaml would not parse: ",
		},
		{
			name: "candidate that the reader rejects",
			cand: good + "hook_delivery: sometimes\n",
			want: "refusing to write: edited lessons.yaml would not parse: lessons config: ",
		},
		{
			name: "other line breaks",
			cand: crlf(good),
			want: "refusing to write: the edit would change the line breaks of lessons.yaml",
		},
		{
			name: "changed setting",
			cand: strings.Replace(good, "threshold: 3", "threshold: 4", 1),
			want: "refusing to write: the edit would change a setting outside the pin list",
		},
		{
			name: "lost earlier entry",
			cand: "# Notes.\nthreshold: 3\npin:\n" + appendBlock,
			want: "refusing to write: the pin list would differ from the new entries followed by the earlier entries",
		},
		{
			name: "entries that the reader delivers in another way",
			cand: good,
			add: func(x *addition) *addition {
				y := *x
				y.pins = append([]reviews.PinRule{{Rule: "other"}}, x.pins[1:]...)

				return &y
			},
			want: "refusing to write: the delivered pins would differ from the new pins followed by the earlier pins",
		},
		{
			name: "changed comment outside the new entries",
			cand: strings.Replace(good, "# Notes.", "# Other notes.", 1),
			want: "refusing to write: the edit would change a line outside the new entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := add
			if tt.add != nil {
				x = tt.add(add)
			}

			err := a.verifyAppend([]byte(before), []byte(tt.cand), x, 3, ins, true)
			require.ErrorIs(t, err, ErrUnverified)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), err.Error())
		})
	}
}

func TestPrepareAppendCreatesAnAbsentFile(t *testing.T) {
	root := t.TempDir()

	d, err := Read(root)
	require.NoError(t, err)

	after, err := PlanAppend(d, appendBlock)
	require.NoError(t, err)

	p, err := PrepareAppend(integration.Builtin(), root, d.Guard, after)
	require.NoError(t, err)
	assert.NoFileExists(t, pinPath(root), "PrepareAppend writes nothing")

	require.NoError(t, p.Apply())
	assert.Equal(t, string(after), readPins(t, root))

	// A new file gets the permission of a plain write: 0644 through the
	// umask, as the old writer gave it.
	info, err := os.Stat(pinPath(root))
	require.NoError(t, err)
	assert.Zero(t, info.Mode().Perm()&^0o644, "mode %v", info.Mode().Perm())
}

func TestPrepareAppendNeverReplacesAnotherWritersFile(t *testing.T) {
	const other = "pin:\n  - rule: other writer\n"

	t.Run("created before PrepareAppend", func(t *testing.T) {
		root := t.TempDir()

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)

		require.NoError(t, os.MkdirAll(filepath.Dir(pinPath(root)), 0o755))
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o644))

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, other, readPins(t, root))
	})

	t.Run("created between PrepareAppend and Apply", func(t *testing.T) {
		root := t.TempDir()

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)

		p, err := PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.NoError(t, err)

		require.NoError(t, os.MkdirAll(filepath.Dir(pinPath(root)), 0o755))
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o644))

		require.ErrorIs(t, p.Apply(), ErrStale)
		assert.Equal(t, other, readPins(t, root))
	})

	t.Run("removed after the read", func(t *testing.T) {
		root := writeFile(t, "pin:\n"+handEntry, 0o644)

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)
		require.NoError(t, os.Remove(pinPath(root)))

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrStale)
		assert.NoFileExists(t, pinPath(root), "the write never creates a file that the plan saw")
	})

	t.Run("changed after the read", func(t *testing.T) {
		root := writeFile(t, "pin:\n"+handEntry, 0o644)

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(pinPath(root), []byte(other), 0o644))

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrStale)
		assert.Equal(t, other, readPins(t, root))
	})
}

func TestPrepareAppendRefusesWhatTheEngineDoesNotReplace(t *testing.T) {
	t.Run("read-only file", func(t *testing.T) {
		root := writeFile(t, "pin:\n"+handEntry, 0o444)

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrNotWritable)
		assert.Equal(t, "the pin file cannot be replaced: the file is read-only (-r--r--r--); seamark does not replace it",
			err.Error())
		assert.Equal(t, "pin:\n"+handEntry, readPins(t, root))
	})

	t.Run("linked directory of an absent file", func(t *testing.T) {
		root, elsewhere := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".seamark")))

		d, err := Read(root)
		require.NoError(t, err)

		after, err := PlanAppend(d, appendBlock)
		require.NoError(t, err)

		_, err = PrepareAppend(integration.Builtin(), root, d.Guard, after)
		require.ErrorIs(t, err, ErrNotWritable)
		assert.NoFileExists(t, filepath.Join(elsewhere, "lessons.yaml"), "nothing lands behind the link")
	})
}
