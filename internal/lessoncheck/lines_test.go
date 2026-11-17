package lessoncheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLessonCheckLineSplitting(t *testing.T) {
	t.Run("split rules", func(t *testing.T) {
		cases := []struct {
			name    string
			content string
			lines   []string
		}{
			{"empty content has zero lines", "", nil},
			{"one terminator is one empty line", "\n", []string{""}},
			{"two terminators are two empty lines", "\n\n", []string{"", ""}},
			{"a final terminator adds no empty line", "a\nb\n", []string{"a", "b"}},
			{"a final segment without a terminator is a line", "a\nb", []string{"a", "b"}},
			{"one carriage return before the newline is part of the terminator", "a\r\nb\r\n", []string{"a", "b"}},
			{"only one carriage return belongs to the terminator", "a\r\r\n", []string{"a\r"}},
			{"a lone carriage return stays in the line", "a\rb\n", []string{"a\rb"}},
			{"a final carriage return without a newline stays", "a\r", []string{"a\r"}},
			{"inner empty lines are lines", "a\n\nb\n", []string{"a", "", "b"}},
		}

		for _, tc := range cases {
			assert.Equal(t, tc.lines, splitLines([]byte(tc.content)), tc.name)
			assert.Equal(t, len(tc.lines), lineCount([]byte(tc.content)), "the count without the split: "+tc.name)
		}
	})

	t.Run("the line count of a side", func(t *testing.T) {
		// The max-diff-lines test counts the lines of a side before the
		// split, so the count must equal the length of sideLines.
		for _, side := range []Side{fileSide(""), fileSide("a\nb"), fileSide("a\r\nb\r\n"), linkSide("../target")} {
			assert.Equal(t, len(sideLines(side)), sideLineCount(side), string(side.text))
		}

		assert.Equal(t, 1, sideLineCount(linkSide("a\nb")), "a link target is one line")
	})

	t.Run("text test", func(t *testing.T) {
		assert.Equal(t, ContentAvailable, textState([]byte("plain text é\n")))
		assert.Equal(t, ContentAvailable, textState(nil), "empty content is text")
		assert.Equal(t, ContentBinary, textState([]byte("a\x00b")))
		assert.Equal(t, ContentBinary, textState([]byte("\xff\x00")), "a NUL byte wins over invalid UTF-8")
		assert.Equal(t, ContentInvalidUTF8, textState([]byte("a\xffb")))
	})

	t.Run("the line rules hold in the checker", func(t *testing.T) {
		cases := []struct {
			name    string
			pattern string
			content string
			outcome Outcome
		}{
			// "\r\n" is a terminator, so "foo$" matches "foo\r\n".
			{"an end anchor matches before CRLF", `foo$`, "foo\r\n", OutcomeViolation},
			{"the terminator is not part of the line", `\r`, "a\r\n", OutcomePass},
			{"a lone carriage return is part of the line", `\r`, "a\rb\n", OutcomeViolation},
			{"a final line without a terminator is matched", `^last$`, "first\nlast", OutcomeViolation},
			{"no empty last line is matched", `^$`, "a\n", OutcomePass},
			{"an inner empty line is matched", `^$`, "a\n\nb\n", OutcomeViolation},
		}

		for _, tc := range cases {
			cfg := onePin(t, KindNoAddedPattern, tc.pattern, "src")
			res := onlyResult(t, cfg, snapshotOf(BoundaryWorkingTree, addChange("src/a.txt", fileSide(tc.content))))

			assert.Equal(t, tc.outcome, res.Outcome, tc.name)
		}
	})

	t.Run("added line numbers", func(t *testing.T) {
		before := []string{"a", "b", "c"}
		after := []string{"a", "x", "c", "y"}

		assert.Equal(t, []addedLine{{number: 2, text: "x"}, {number: 4, text: "y"}}, diffLines(before, after))
		assert.Equal(t, []addedLine{{number: 1, text: "a"}, {number: 2, text: "b"}}, everyLine([]string{"a", "b"}))
		assert.Empty(t, diffLines(before, before))
		assert.Empty(t, diffLines(before, nil), "a removal adds no line")
	})
}
