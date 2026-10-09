package tui

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/distill"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the apply preview of Lessons › Proposals: the folded pin
// block, the pins that exist already, and the text when distill.write is
// off.

// lesFixLongNote is a realistic distilled note. Real notes run to 240
// characters, so their pin lines are wider than a normal terminal.
const lesFixLongNote = "Wrap every error returned by the SQLite store with the query name before you " +
	"return it, so the log names the failing statement."

// lesFixParse parses a pin block under a "pin:" key, as lessons.yaml
// holds it. The test parses by itself, so it does not trust the guard
// in the code under test.
func lesFixParse(t *testing.T, block string) any {
	t.Helper()

	var v any
	require.NoError(t, yaml.Unmarshal([]byte("pin:\n"+block), &v), "block:\n%s", block)

	return v
}

// lesFixPins renders one pending proposal with a note as the service
// renders it.
func lesFixPins(t *testing.T, note string) string {
	t.Helper()

	block, err := distill.RenderPins([]domain.Proposal{{
		ID: 1, Rule: "wrap-store-errors", Regions: []string{"internal/store"}, Note: note,
		Agent: "a", Members: []int64{7},
	}})
	require.NoError(t, err)

	return block
}

// TestLessonsFixPinFoldRoundTrips covers finding 4: a folded pin block
// must parse to the same value as Preview.YAML, and a note line folds
// only at a single space.
func TestLessonsFixPinFoldRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name string
		note string
		// fold is true when the note line must fold at 72 cells.
		fold bool
	}{
		{"plain", lesFixLongNote, true},
		{"quotes", `Say "use the wrapper" and 'never' the raw call when the store returns a value that you use later on.`, true},
		{"colons", "Handle errors: wrap them with context: the query name, the table, and the caller in each store method.", true},
		{"hash", "Keep issue refs like #123 and #456 in the commit body, never in the subject line of the change itself.", true},
		{"multiple spaces", "Keep  two  spaces  between  these  words, and single spaces between the other words in this long note.", true},
		{"backslash", `Escape the path C:\temp\new with a double backslash \\ before you pass it to the shell command runner.`, true},
		{"tab escape", "Use\ttabs in Makefile recipes and spaces everywhere else in the repository files, as the linter expects.", true},
		{"leading dash", "- start with a dash and keep - dashes - in the middle of this long note that needs a fold somewhere.", true},
		{"trailing space", "This note ends with a space that the YAML must keep after the fold in the middle of the text ", true},
		{"unicode", "Prefer the — em dash and é accents in docs, and keep every line of prose readable in the terminal pane.", true},
		{"long word", "Use " + strings.Repeat("x", 90) + " as the identifier.", false},
		{"block scalar", "First line of a note.\nSecond line of the note is long enough to be wider than the pane at this width.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := lesFixPins(t, tc.note)
			want := lesFixParse(t, block)

			for _, w := range []int{24, 40, 72} {
				// The raw fold is correct by itself, not only after the guard.
				raw := propFoldLines(strings.Split(strings.TrimRight(block, "\n"), "\n"), w)
				assert.Equal(t, want, lesFixParse(t, strings.Join(raw.lines, "\n")), "raw fold at %d", w)

				got := propFoldPins(block, w)
				display := strings.Join(got.lines, "\n")
				assert.Equal(t, want, lesFixParse(t, display), "display at %d", w)

				for _, line := range got.lines {
					if ui.Width(line) > w {
						assert.True(t, got.overflow, "a line wider than %d needs the overflow note: %q", w, line)
					}
				}
			}

			got := propFoldPins(block, 72)
			assert.Equal(t, tc.fold, got.folded)

			if !tc.fold {
				assert.Equal(t, strings.Split(strings.TrimRight(block, "\n"), "\n"), got.lines,
					"a line that cannot fold stays whole")
				assert.True(t, got.overflow)

				return
			}

			assert.False(t, got.overflow)

			// Continuation lines sit at the key indent plus two.
			noteAt := -1

			for i, line := range got.lines {
				if strings.HasPrefix(line, "    note: ") {
					noteAt = i
				}
			}

			require.GreaterOrEqual(t, noteAt, 0)
			require.Greater(t, len(got.lines), noteAt+1)

			for _, line := range got.lines[noteAt+1:] {
				assert.True(t, strings.HasPrefix(line, "      ") && line[6] != ' ', "continuation %q", line)
			}
		})
	}
}

// TestLessonsFixPinFoldKeepsSpaceRuns covers finding 4: a break never
// falls inside a run of spaces, because YAML trims the spaces at a
// break.
func TestLessonsFixPinFoldKeepsSpaceRuns(t *testing.T) {
	assert.Equal(t, []string{"a  b", "c", "d   e"}, propSplitSingleSpaces("a  b c d   e"))
	assert.Equal(t, []string{"one"}, propSplitSingleSpaces("one"))

	block := lesFixPins(t, "Keep  two  spaces  between  these  words, and single spaces between the other words.")
	got := propFoldPins(block, 60)

	require.True(t, got.folded)
	assert.Contains(t, strings.Join(got.lines, "\n"), "Keep  two  spaces  between  these  words,")
}

// TestLessonsFixPinFoldFallsBackWhenYAMLChanges covers finding 4: a line
// that looks like a note inside a block scalar must not fold, because
// a block scalar keeps its line breaks. The guard keeps the source.
func TestLessonsFixPinFoldFallsBackWhenYAMLChanges(t *testing.T) {
	block := lesFixPins(t, "Intro line.\nnote: this inner line looks like a note key and it is long enough to need a fold.")
	src := strings.Split(strings.TrimRight(block, "\n"), "\n")

	raw := propFoldLines(src, 40)
	require.True(t, raw.folded, "the naive fold changes the inner line")
	assert.NotEqual(t, lesFixParse(t, block), lesFixParse(t, strings.Join(raw.lines, "\n")))

	got := propFoldPins(block, 40)
	assert.Equal(t, src, got.lines)
	assert.False(t, got.folded)
	assert.True(t, got.overflow, "the pane shows the sideways note instead")
}

// lesFixYAMLLine matches a numbered line of the modal pin block.
var lesFixYAMLLine = regexp.MustCompile(`^ *\d+ {2}(.*)$`)

// lesFixModalYAML extracts the pin block from the open modal the way a
// reader copies it: the numbered lines without their numbers. A line
// without a number continues the line above on the screen only, so it
// joins that line again.
func lesFixModalYAML(t *testing.T, h *harness) string {
	t.Helper()

	var out []string

	for _, l := range lesModalLines(t, h) {
		if m := lesFixYAMLLine.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])

			continue
		}

		if len(out) > 0 && strings.HasPrefix(l, "    ") {
			out[len(out)-1] += " " + strings.TrimSpace(l)

			continue
		}

		if len(out) > 0 {
			break
		}
	}

	return strings.Join(out, "\n")
}

// lesFixRealPreview returns a preview whose YAML the shared renderer
// built, like the service does.
func lesFixRealPreview(t *testing.T, a proposals.Action, ids []int64, write bool, l *proposals.Ledger) *proposals.Preview {
	t.Helper()

	pv := &proposals.Preview{Action: a, Write: write, Target: proposals.LessonsFile}

	for _, id := range ids {
		for _, pr := range l.Pending {
			if pr.ID == id {
				pv.Proposals = append(pv.Proposals, pr)
			}
		}
	}

	if a == proposals.ActionApply {
		block, err := distill.RenderPins(pv.Proposals)
		require.NoError(t, err)

		pv.YAML = block
	}

	return pv
}

// TestLessonsFixShowYAMLBlockIsNotClipped covers finding 4: at 80
// columns the numbered pin block parses to the full note, no line is
// cut, and the preview names the CLI command that prints the same
// block.
func TestLessonsFixShowYAMLBlockIsNotClipped(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.ledger.Pending[0].Note = lesFixLongNote

	var pv *proposals.Preview

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv = lesFixRealPreview(t, a, ids, false, f.ledger)

		return pv, nil
	}

	h := lesOpen(t, f, 80, 60)
	h.press("space", "down", "down", "space", "a")

	frame := h.view()
	assertFrame(t, frame, 80, 60)
	require.NotNil(t, pv)
	assert.Contains(t, frame, "YAML p1, p3 · 2 proposals")

	modal := lesModalText(t, h)
	assert.Contains(t, modal, "Long notes continue on indented lines.")
	assert.Contains(t, modal, "seamark lessons --apply p1,p3", "the command names the block's proposals")
	assert.Contains(t, modal, "This command prints the same block in a shell.")

	for _, line := range lesModalLines(t, h) {
		assert.False(t, strings.HasSuffix(line, "…"), "no line of the modal is cut: %q", line)
	}

	shown := lesFixModalYAML(t, h)
	assert.Equal(t, lesFixParse(t, pv.YAML), lesFixParse(t, shown), "the shown block is the full pin:\n%s", shown)
	assert.Contains(t, lesFlat(shown), "failing statement.")

	// The reviewed preview keeps the exact service YAML for the commit.
	block, err := distill.RenderPins(pv.Proposals)
	require.NoError(t, err)
	assert.Equal(t, block, lesProps(h).dec.preview.YAML)
}

// TestLessonsFixShowYAMLExplainsAnUnfoldableLine covers finding 4: a
// line that cannot fold wraps on the screen only, and a visible note
// says that c copies the exact block.
func TestLessonsFixShowYAMLExplainsAnUnfoldableLine(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.ledger.Pending[0].Note = "Use " + strings.Repeat("y", 120) + " here."
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesFixRealPreview(t, a, ids, false, f.ledger), nil
	}

	h := lesOpen(t, f, 80, 60)
	h.press("a")

	assertFrame(t, h.view(), 80, 60)

	modal := lesModalText(t, h)
	assert.Contains(t, modal, "Some lines cannot fold without a change to the YAML.")
	assert.Contains(t, modal, "c copies the exact block")
	assert.Contains(t, modal, "seamark lessons --apply p1")

	for _, line := range lesModalLines(t, h) {
		assert.False(t, strings.HasSuffix(line, "…"), "no line of the modal is cut: %q", line)
	}
}

// TestLessonsFixGateOffExistingPinsAreNamed covers findings 7 and 14:
// with the gate off, existing pins come first. The preview names them,
// offers the ledger-only record, and shows a block only for the
// proposals without a pin.
func TestLessonsFixGateOffExistingPinsAreNamed(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false

	var reconciled []int64

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesFixRealPreview(t, a, ids, false, f.ledger)
		if a == proposals.ActionApply {
			pv.AlreadyPinned = []int64{1}
		}

		if a == proposals.ActionReconcile {
			reconciled = ids
			pv.AlreadyPinned = ids
		}

		return pv, nil
	}

	h := lesOpen(t, f, 120, 60)
	h.press("space", "down", "space", "a")

	assertFrame(t, h.view(), 120, 60)
	assert.Contains(t, h.view(), "Pins already exist p1")

	frame := lesModalText(t, h)
	assert.NotContains(t, h.view(), "YAML p1")
	assert.Contains(t, frame, "Pins already exist for p1.")
	assert.Contains(t, frame, "Press c to record p1 as applied (ledger only).")
	assert.Contains(t, frame, "The record works with distill.write off, and the pin file stays unchanged.")
	assert.Contains(t, frame, "The block below holds only p2, without a pin yet")

	// The effect lines promise no change that will not happen.
	assert.Contains(t, frame, "WON'T")
	assert.NotContains(t, frame, "WILL")
	assert.NotContains(t, frame, "pending › applied")
	assert.Contains(t, frame, "write .seamark/lessons.yaml: pins already exist for p1")
	assert.Contains(t, frame, "change the ledger until you press c to record p1 as applied")
	assert.Contains(t, frame, "distill.write off")

	// The block holds p2 only, and the command prints the same block.
	block := lesFixModalYAML(t, h)
	assert.Contains(t, block, "- rule: close-db-handles")
	assert.NotContains(t, block, "check-nil-errors", "the block never re-pastes an existing pin")
	assert.Contains(t, frame, "seamark lessons --apply p2")
	assert.NotContains(t, frame, "--apply p1")
	assert.Contains(t, frame, "c record as applied (ledger only)")
	assert.NotContains(t, frame, "copy to clipboard", "a copy would paste the existing pin again")

	// The reviewed preview keeps the service YAML for both proposals.
	assert.Contains(t, lesProps(h).dec.preview.YAML, "check-nil-errors")

	// c works with the gate off and records only the existing pin.
	h.press("c")
	assert.Equal(t, []int64{1}, reconciled)
	assert.Contains(t, h.view(), "Record p1 · check-nil-errors as applied")
	assert.Contains(t, h.view(), "ledger only")
	assert.Equal(t, []string{"PreviewDecision:apply", "PreviewDecision:reconcile"}, f.called())
}

// TestLessonsFixGateOffAllPinnedShowsNoBlock covers finding 7: when
// every selected proposal has its pin, nothing is left to copy.
func TestLessonsFixGateOffAllPinnedShowsNoBlock(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesFixRealPreview(t, a, ids, false, f.ledger)
		pv.AlreadyPinned = []int64{1}

		return pv, nil
	}

	h := lesOpen(t, f, 120, 40)
	h.press("a")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "Pins already exist for p1.")
	assert.Contains(t, frame, "Every selected proposal has its pin, so there is no block to copy.")
	assert.Empty(t, lesFixModalYAML(t, h))
	assert.NotContains(t, frame, "seamark lessons --apply")
	assert.NotContains(t, frame, "Copy this block")
	assert.Contains(t, frame, "WON'T")
}

// TestLessonsFixGateOnExistingPinsListNoWrite covers finding 14: with
// the gate on, the effect table matches the offered action, and no
// block with the existing pins appears.
func TestLessonsFixGateOnExistingPinsListNoWrite(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesFixRealPreview(t, a, ids, true, f.ledger)
		pv.AlreadyPinned = []int64{1}

		return pv, nil
	}

	h := lesOpen(t, f, 120, 40)
	h.press("space", "down", "space", "a")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "Pins already exist for p1.")
	assert.Contains(t, frame, "a blind apply would duplicate them")
	assert.Contains(t, frame, "Then apply p2 again.")
	assert.Contains(t, frame, "WON'T")
	assert.NotContains(t, frame, "WILL")
	assert.NotContains(t, frame, "pending › applied")
	assert.Contains(t, frame, "write .seamark/lessons.yaml: pins already exist for p1")
	assert.Contains(t, frame, "change the ledger until you press c to record p1 as applied")
	assert.Contains(t, h.view(), "distill.write on")
	assert.Empty(t, lesFixModalYAML(t, h))
	assert.NotContains(t, frame, "seamark lessons --apply")
	assert.NotContains(t, frame, "y apply")
}

// TestLessonsFixGateOffLinkedPinFileSaysApplyCannotWrite proves that a
// gate-off preview over a linked pin file still shows the block, and says
// that enabling the gate would not let the console write it.
func TestLessonsFixGateOffLinkedPinFileSaysApplyCannotWrite(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, false)
		pv.LessonsErr = errors.New(".seamark/lessons.yaml: symlink at .seamark/lessons.yaml")

		return pv, nil
	}

	h := lesOpen(t, f, 120, 32)
	h.press("space", "a")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "The console cannot write .seamark/lessons.yaml")
	assert.Contains(t, frame, "Enabling distill.write does not change this")
	assert.Contains(t, frame, "- rule: check-nil-errors")
	assert.Contains(t, frame, "c copy to clipboard", "a hand paste stays possible")
	assert.NotContains(t, frame, "y apply")

	h.press("y")
	assert.NotContains(t, f.called(), "CommitDecision:apply")
}

// A review finding of Lessons: the gate-off apply text names the
// Settings route.

func TestLessonsGateOffApplyNamesTheSettingsRoute(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		return lesPreview(a, ids, false), nil
	}

	h := lesOpen(t, f, 120, 40)
	h.press("a")

	text := lesModalText(t, h)
	assert.Contains(t, text, "turn on distill.write in Settings › Pin writing (5), then press a again for a new preview")
	assert.NotContains(t, text, "refresh with r", "every a reads the gate again")
}
