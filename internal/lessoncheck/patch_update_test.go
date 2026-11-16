package lessoncheck

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of applyChunks, the content projection of a
// Codex Update section. Each rule case names the case of the content
// oracle (internal/integration/testdata/codex/apply_patch_oracle.json)
// whose bytes it repeats, so a rule never stands without a recording of
// the applier.

// chunkOf returns a chunk from compact change lines. The first byte of
// each line is its kind, as in the patch: " ", "-", or "+".
func chunkOf(anchor string, eof bool, lines ...string) PatchChunk {
	chunk := PatchChunk{Anchor: anchor, EndOfFile: eof}

	for _, line := range lines {
		chunk.Lines = append(chunk.Lines, PatchLine{Kind: PatchLineKind(line[0]), Text: line[1:]})
	}

	return chunk
}

// plainChunk is a chunk without an anchor and without the end-of-file
// marker.
func plainChunk(change ...string) PatchChunk {
	return chunkOf("", false, change...)
}

// updateOp is a Codex Update File section whose chunks the decoder read.
func updateOp(path string, chunks ...PatchChunk) FileOp {
	return FileOp{Kind: OpPatchUpdate, Path: path, Hunks: true, Chunks: chunks}
}

// chunkRule is one case of applyChunks: the oracle case that pins the
// rule, the content before, the chunks, and the expected result.
type chunkRule struct {
	oracle string
	before string
	chunks []PatchChunk
	after  string
}

func TestLessonCheckApplyChunksRules(t *testing.T) {
	rules := []chunkRule{
		// One chunk with context, and two equal chunks in order.
		{"01_one_hunk", "a\nb\nc\n", []PatchChunk{plainChunk(" a", "-b", "+B", " c")}, "a\nB\nc\n"},
		{"11_repeated_context_two_hunks", "x\ny\nx\ny\n", []PatchChunk{plainChunk(" x", "-y", "+Y"), plainChunk(" x", "-y", "+Z")}, "x\nY\nx\nZ\n"},
		// The end-of-file marker matches only at the end.
		{"04_end_of_file_repeated_tail", "x\ny\nx\ny\n", []PatchChunk{chunkOf("", true, " x", "-y", "+Y")}, "x\ny\nx\nY\n"},
		{"57_end_of_file_second_equal_chunk", "x\ny\nx\ny\n",
			[]PatchChunk{plainChunk(" x", "-y", "+Y"), chunkOf("", true, " x", "-y", "+Z")}, "x\nY\nx\nZ\n"},
		// New lines take the line end of the first line.
		{"07_crlf_file_lf_patch", "a\r\nb\r\nc\r\n", []PatchChunk{plainChunk(" a", "-b", "+B", " c")}, "a\r\nB\r\nc\r\n"},
		{"36_eol_first_lf_rest_crlf", "a\nb\r\nc\r\nd\r\n", []PatchChunk{plainChunk(" b", "-c", "+C", " d")}, "a\nb\r\nC\nd\r\n"},
		{"37_eol_first_crlf_rest_lf", "a\r\nb\nc\nd\n", []PatchChunk{plainChunk(" b", "-c", "+C", " d")}, "a\r\nb\nC\r\nd\n"},
		{"39_eol_insertion_first_line_crlf", "a\r\nb\nc\n", []PatchChunk{plainChunk("+z")}, "a\r\nb\nc\nz\r\n"},
		// A last line without a line end gets one before any chunk.
		{"05_no_final_newline_last_line", "a\nb\nc", []PatchChunk{plainChunk(" b", "-c", "+C")}, "a\nb\nC\n"},
		{"40_single_line_without_final_newline", "a", []PatchChunk{plainChunk("-a", "+A", "+B")}, "A\nB\n"},
		{"41_crlf_without_final_newline_tail_kept", "a\r\nb\r\nc", []PatchChunk{plainChunk("-a", "+A", " b")}, "A\r\nb\r\nc\r\n"},
		// An anchor moves the cursor below its line.
		{"10_repeated_context_anchor", "func a\nx\ny\nfunc b\nx\ny\n",
			[]PatchChunk{chunkOf("func b", false, " x", "-y", "+Y")}, "func a\nx\ny\nfunc b\nx\nY\n"},
		{"51_anchor_equal_to_context", "x\ny\nx\ny\n", []PatchChunk{chunkOf("x", false, " x", "-y", "+Y")}, "x\ny\nx\nY\n"},
		{"52_anchor_leading_spaces", "def f\nx\n  def f\nx\n", []PatchChunk{chunkOf("  def f", false, "-x", "+X")}, "def f\nx\n  def f\nX\n"},
		{"95_anchor_matches_after_normalization", "func \u201cb\u201d\nx\n",
			[]PatchChunk{chunkOf(`func "b"`, false, "-x", "+X")}, "func \u201cb\u201d\nX\n"},
		// A pure insertion goes to the end of the file.
		{"20_pure_insertion", "a\nb\n", []PatchChunk{plainChunk("+z")}, "a\nb\nz\n"},
		{"44_insertion_anchor_goes_to_end", "func a\nx\nfunc b\ny\n",
			[]PatchChunk{chunkOf("func a", false, "+z")}, "func a\nx\nfunc b\ny\nz\n"},
		{"45_insertion_after_trailing_blank_line", "a\nb\n\n", []PatchChunk{plainChunk("+z")}, "a\nb\n\nz\n"},
		{"46_insertion_into_empty_file", "", []PatchChunk{plainChunk("+z")}, "z\n"},
		{"47_insertion_with_end_of_file", "a\nb\n", []PatchChunk{chunkOf("", true, "+z")}, "a\nb\nz\n"},
		// Context lines keep the file bytes after a fuzzy match.
		{"18_context_trailing_whitespace", "a  \nb\n", []PatchChunk{plainChunk(" a", "-b", "+B")}, "a  \nB\n"},
		{"19_context_unicode_dash", "a \u2013 b\nc\n", []PatchChunk{plainChunk("-a - b", "+a - B", " c")}, "a - B\nc\n"},
		{"30_context_keeps_trailing_spaces", "a\nb  \nc\nd\n", []PatchChunk{plainChunk(" a", " b", "-c", "+C", " d")}, "a\nb  \nC\nd\n"},
		{"32_context_keeps_tab", "\ta\nb\n", []PatchChunk{plainChunk("     a", "-b", "+B")}, "\ta\nB\n"},
		{"33_fuzzy_delete_then_equal_add", "a  \n", []PatchChunk{plainChunk("-a", "+a")}, "a\n"},
		{"35_fuzzy_context_then_equal_add", "a  \nb\n", []PatchChunk{plainChunk(" a", "+a", " b")}, "a  \na\nb\n"},
		{"83_context_unicode_trailing_whitespace", "a\u00a0\nb\u1680\nc\u3000\nd\n",
			[]PatchChunk{plainChunk(" a", " b", " c", "-d", "+D")}, "a\u00a0\nb\u1680\nc\u3000\nD\n"},
		{"88_normalize_patch_side", "a-b\n\"a\"\nc\n", []PatchChunk{plainChunk(" a\u2013b", " \u201ca\u201d", "-c", "+C")}, "a-b\n\"a\"\nC\n"},
		// The four passes run in order, each over every position.
		{"50_exact_match_before_fuzzy_match", "a \nb\na\nb\n", []PatchChunk{plainChunk(" a", "-b", "+B")}, "a \nb\na\nB\n"},
		{"93_trailing_unicode_space_before_leading_space", " x\ny\nx\u00a0\ny\n", []PatchChunk{plainChunk(" x", "-y", "+Y")}, " x\ny\nx\u00a0\nY\n"},
		{"94_trimmed_match_before_normalized_match", "a\u2013b\nc\n  a-b\nc\n", []PatchChunk{plainChunk(" a-b", "-c", "+C")}, "a\u2013b\nc\n  a-b\nC\n"},
		// The retry drops a trailing empty line from the old side.
		{"58_end_of_file_with_trailing_empty_context", "a\nb", []PatchChunk{chunkOf("", true, " a", "-b", "+B", " ")}, "a\nB\n"},
		{"59_retry_empty_delete_and_empty_add", "a\nb", []PatchChunk{plainChunk(" a", "-b", "+B", "-", "+")}, "a\nB\n"},
		{"60_retry_context_then_add", "a\nb", []PatchChunk{plainChunk(" b", "+X", " ")}, "a\nb\nX\n"},
		{"61_retry_empty_delete", "a\nb", []PatchChunk{plainChunk(" a", "-b", "+B", "-")}, "a\nB\n"},
		{"62_retry_empty_file_context", "", []PatchChunk{plainChunk(" ", "+z")}, "\nz\n"},
		{"63_retry_keeps_fuzzy_context", "a  \nb", []PatchChunk{plainChunk(" a", "-b", "+B", " ")}, "a  \nB\n"},
		// An added line goes before the first line of its run.
		{"66_insertion_then_replace_last_line", "a\nb\n", []PatchChunk{plainChunk("+Z"), plainChunk(" a", "-b", "+B")}, "a\nB\nZ\n"},
		{"67_insertion_then_add_at_end", "a\nb\n", []PatchChunk{plainChunk("+Z"), plainChunk(" a", " b", "+C")}, "a\nb\nZ\nC\n"},
		{"68_insertion_then_runs_around_context", "a\nb\nc\n",
			[]PatchChunk{plainChunk("+Z"), plainChunk("-a", "+A", " b", " c", "+C")}, "A\nb\nc\nZ\nC\n"},
		{"69_insertions_at_one_line_keep_chunk_order", "a\nb\n", []PatchChunk{plainChunk(" a", "+X"), plainChunk("+Y", " b")}, "a\nX\nY\nb\n"},
		// Other shapes.
		{"73_context_only_chunk", "a\nb\n", []PatchChunk{plainChunk(" a", " b")}, "a\nb\n"},
		{"74_delete_every_line", "a\nb\n", []PatchChunk{plainChunk("-a", "-b")}, ""},
		{"81_added_line_with_lone_cr", "a\nb\n", []PatchChunk{plainChunk(" a", "-b", "+B\rC")}, "a\nB\rC\n"},
		{"82_line_separators_inside_line", "a\u2028b\u0085c\vd\fe\nf\n",
			[]PatchChunk{plainChunk(" a\u2028b\u0085c\vd\fe", "-f", "+F")}, "a\u2028b\u0085c\vd\fe\nF\n"},
	}

	for _, tc := range rules {
		t.Run(tc.oracle, func(t *testing.T) {
			got, tooLarge, note, err := applyChunks(context.Background(), []byte(tc.before), tc.chunks, DefaultLimits().MaxFileBytes)
			require.NoError(t, err)
			require.Empty(t, note)
			require.Zero(t, tooLarge)
			assert.Equal(t, tc.after, string(got))
		})
	}
}

func TestLessonCheckApplyChunksNormalization(t *testing.T) {
	// Every character of the table, each in its own case. The oracle
	// holds them by class in cases 84 to 87.
	table := map[string]string{
		"-": "\u2010\u2011\u2012\u2013\u2014\u2015\u2212",
		"'": "\u2018\u2019\u201a\u201b",
		`"`: "\u201c\u201d\u201e\u201f",
		" ": "\u00a0\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u202f\u205f\u3000",
	}

	for ascii, chars := range table {
		for _, r := range chars {
			before := "a" + string(r) + "b\nc\n"
			got, _, note, err := applyChunks(context.Background(), []byte(before), []PatchChunk{plainChunk(" a"+ascii+"b", "-c", "+C")}, 1<<20)
			require.NoError(t, err)
			require.Empty(t, note, "U+%04X maps to %q", r, ascii)
			assert.Equal(t, "a"+string(r)+"b\nC\n", string(got), "U+%04X keeps its byte in the context line", r)
		}
	}

	// Near characters outside the table stay as they are, so the chunk
	// does not match. The oracle holds one per class in cases 89 to 92.
	unmapped := map[rune]string{
		'\uff0d': "-", '\ufe58': "-", '\u2043': "-", '\u00ad': "-",
		'\u02bc': "'", '\u00b4': "'", '\u2032': "'",
		'\uff02': `"`,
		'\u2000': " ", '\u2001': " ", '\u200b': " ", '\u1680': " ", '\u2028': " ", '\u0085': " ",
	}

	for r, ascii := range unmapped {
		before := "a" + string(r) + "b\nc\n"
		_, _, note, err := applyChunks(context.Background(), []byte(before), []PatchChunk{plainChunk(" a"+ascii+"b", "-c", "+C")}, 1<<20)
		require.NoError(t, err)
		assert.Equal(t, noteNoMatch, note, "U+%04X stays unmapped", r)
	}
}

func TestLessonCheckApplyChunksUnknown(t *testing.T) {
	for _, tc := range []struct {
		oracle string
		before string
		chunks []PatchChunk
		note   string
	}{
		{"16_context_mismatch", "a\nb\nc\n", []PatchChunk{plainChunk(" q", "-b", "+B")}, noteNoMatch},
		{"48_insertion_anchor_missing", "a\nb\n", []PatchChunk{chunkOf("zzz", false, "+z")}, noteNoMatch},
		{"54_end_of_file_context_not_at_end", "a\nb\nc\n", []PatchChunk{chunkOf("", true, " a", "-b", "+B")}, noteNoMatch},
		{"55_end_of_file_before_trailing_blank_line", "a\nb\n\n", []PatchChunk{chunkOf("", true, " a", "-b", "+B")}, noteNoMatch},
		{"56_end_of_file_overlapping_chunk", "a\nb\n", []PatchChunk{plainChunk(" a", "-b", "+B"), chunkOf("", true, "-b", "+C")}, noteNoMatch},
		{"64_chunks_out_of_order", "l1\nl2\nl3\nl4\nl5\nl6\n", []PatchChunk{plainChunk(" l5", "-l6", "+L6"), plainChunk(" l1", "-l2", "+L2")}, noteNoMatch},
		{"65_chunks_overlap", "a\nb\nc\nd\n", []PatchChunk{plainChunk(" a", "-b", "+B", " c"), plainChunk(" c", "-d", "+D")}, noteNoMatch},
		{"75_byte_order_mark", "\ufeffa\nb\n", []PatchChunk{plainChunk(" a", "-b", "+B")}, noteNoMatch},
		// The applier reads a lone CR as a line end. No rule here copies
		// that, so such a file stays unknown.
		{"76_lone_cr_line_ends", "a\rb\rc\r", []PatchChunk{plainChunk("-b", "+B")}, noteLoneCR},
		{"77_lone_cr_inside_line", "a\nb\rc\nd\n", []PatchChunk{plainChunk(" a", "-b", "+B", " c")}, noteLoneCR},
		{"a lone CR at the end of the content", "a\nb\r", []PatchChunk{plainChunk("+z")}, noteLoneCR},
	} {
		t.Run(tc.oracle, func(t *testing.T) {
			got, tooLarge, note, err := applyChunks(context.Background(), []byte(tc.before), tc.chunks, 1<<20)
			require.NoError(t, err)
			assert.Equal(t, tc.note, note)
			assert.Nil(t, got, "an unknown result has no content")
			assert.Zero(t, tooLarge)
		})
	}
}

func TestLessonCheckApplyChunksLimits(t *testing.T) {
	t.Run("a result above the byte limit gives its size", func(t *testing.T) {
		got, tooLarge, note, err := applyChunks(context.Background(), []byte("a\n"), []PatchChunk{plainChunk("+" + strings.Repeat("x", 100))}, 50)
		require.NoError(t, err)
		assert.Empty(t, note)
		assert.Nil(t, got)
		assert.EqualValues(t, len("a\n")+100+len("\n"), tooLarge)
	})

	t.Run("a near match over a large file stops when the budget ends", func(t *testing.T) {
		before := strings.Repeat("a\n", 20000)
		pattern := []string{}

		for range 100 {
			pattern = append(pattern, " a")
		}

		ctx := &countingContext{Context: context.Background(), free: 3}

		_, _, note, err := applyChunks(ctx, []byte(before), []PatchChunk{plainChunk(append(pattern, "-b")...)}, 1<<30)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Empty(t, note, "the end of the budget is not a failed match")
		assert.EqualValues(t, 4, ctx.calls.Load(), "the seek stops at the first ended check")
	})
}

func TestLessonCheckProjectChunkBudget(t *testing.T) {
	g := todoRepo(t)
	g.write("src/big.go", strings.Repeat("a\n", 20000))

	change := []string{}
	for range 100 {
		change = append(change, " a")
	}

	fop := updateOp("src/big.go", plainChunk(append(change, "-b")...))

	// Count the context checks of the whole projection, so the test ends
	// the budget inside the seek, after the read.
	probe := &countingContext{Context: context.Background(), free: 1 << 30}
	p := &projector{r: newTreeReader(probe, g.root, DefaultLimits()), repeated: repeatedPaths([]FileOp{fop})}
	ch := p.update(fop)
	require.NoError(t, p.r.stopped)
	require.Equal(t, noteNoMatch, ch[0].unprojectable, "without an end the seek finds no match")

	checks := probe.calls.Load()
	require.Greater(t, checks, int64(2), "the read and the seek each check the context")

	ctx := &countingContext{Context: context.Background(), free: checks - 2}
	p = &projector{r: newTreeReader(ctx, g.root, DefaultLimits()), repeated: repeatedPaths([]FileOp{fop})}
	p.update(fop)

	require.ErrorIs(t, p.r.stopped, context.DeadlineExceeded, "the end inside the seek stops the projection")
}

// FuzzLessonCheckApplyChunks checks that applyChunks never panics, that
// it gives the same result for the same input, and that a result stays
// within the size that the input bounds. The section text uses the
// compact form of chunkOf, one change line per text line, with "@@"
// lines that open a chunk and "$" lines that close a chunk at the end of
// the file.
func FuzzLessonCheckApplyChunks(f *testing.F) {
	f.Add([]byte("a\nb\nc\n"), " a\n-b\n+B\n c")
	f.Add([]byte("a\r\nb\r\nc"), "@@\n b\n-c\n+C\n$")
	f.Add([]byte("x\ny\nx\ny\n"), "@@ x\n x\n-y\n+Y\n@@\n+z")
	f.Add([]byte("a  \nb"), " a\n-b\n+B\n \n-\n+")
	f.Add([]byte("a\u2013b\nc\n"), " a-b\n-c\n+C")
	f.Add([]byte("a\rb\n"), "-b\n+B")

	f.Fuzz(func(t *testing.T, before []byte, section string) {
		chunks := fuzzChunks(section)
		if len(chunks) == 0 {
			return
		}

		got, tooLarge, note, err := applyChunks(context.Background(), before, chunks, 1<<20)
		require.NoError(t, err)

		again, tooLargeAgain, noteAgain, _ := applyChunks(context.Background(), before, chunks, 1<<20)
		require.Equal(t, got, again)
		require.Equal(t, tooLarge, tooLargeAgain)
		require.Equal(t, note, noteAgain)

		bound := len(before) + len("\r\n")
		for _, chunk := range chunks {
			for _, line := range chunk.Lines {
				bound += len(line.Text) + len("\r\n")
			}
		}

		require.LessOrEqual(t, len(got), bound, "the result holds the file, one line end, and the added lines at most")
	})
}

// fuzzChunks reads the compact section form of the fuzz target. A line
// of another form ends the section.
func fuzzChunks(section string) []PatchChunk {
	var (
		chunks []PatchChunk
		open   *PatchChunk
	)

	for _, line := range strings.Split(section, "\n") {
		switch {
		case line == "$" && open != nil && len(open.Lines) > 0:
			open.EndOfFile = true
			open = nil
		case line == "@@" || strings.HasPrefix(line, "@@ "):
			chunks = append(chunks, PatchChunk{Anchor: strings.TrimPrefix(strings.TrimPrefix(line, "@@"), " ")})
			open = &chunks[len(chunks)-1]
		case line != "" && strings.ContainsRune(" -+", rune(line[0])):
			if open == nil {
				chunks = append(chunks, PatchChunk{})
				open = &chunks[len(chunks)-1]
			}

			open.Lines = append(open.Lines, PatchLine{Kind: PatchLineKind(line[0]), Text: line[1:]})
		default:
			return validChunks(chunks)
		}
	}

	return validChunks(chunks)
}

// validChunks drops the chunks without lines, because the decoder never
// gives one.
func validChunks(chunks []PatchChunk) []PatchChunk {
	var out []PatchChunk

	for _, chunk := range chunks {
		if len(chunk.Lines) > 0 {
			out = append(out, chunk)
		}
	}

	return out
}
