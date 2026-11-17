package integration

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// grammarClass names the error class of a patch decoder result: "" for
// no error, else the sentinel that the error wraps.
func grammarClass(t *testing.T, err error) string {
	t.Helper()

	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrMalformedEvent):
		return "malformed"
	case errors.Is(err, ErrUnsupportedGrammar):
		return "unsupported"
	}

	t.Fatalf("an error outside the documented classes: %v", err)

	return ""
}

// TestApplyPatchOpsKeepThePathOracle checks the operation decoder
// against the native parser, for every patch of
// testdata/codex/apply_patch_oracle.json. applyPatchPaths reads its
// paths from applyPatchOps, so the error-class and path asserts hold by
// construction. They stay to catch a later split of the two functions.
// The oracle check is the Subset assertion: for each patch that Codex
// accepts, the operations name every file that Codex touched.
func TestApplyPatchOpsKeepThePathOracle(t *testing.T) {
	var oracle struct {
		Cases []struct {
			Name     string   `json:"name"`
			Patch    string   `json:"patch"`
			Accepted bool     `json:"accepted"`
			Touched  []string `json:"touched"`
		} `json:"cases"`
	}

	require.NoError(t, json.Unmarshal(codexFixture(t, "apply_patch_oracle.json"), &oracle))
	require.NotEmpty(t, oracle.Cases)

	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ops, opsErr := applyPatchOps(tc.Patch)
			paths, pathsErr := applyPatchPaths(tc.Patch)

			require.Equal(t, grammarClass(t, pathsErr), grammarClass(t, opsErr), "both decoders give one error class")

			if pathsErr != nil {
				assert.Empty(t, ops, "an error gives no operation")

				return
			}

			assert.Equal(t, paths, patchOpPaths(ops), "the operations name the paths of applyPatchPaths, in order")

			if tc.Accepted {
				assert.Subset(t, patchOpPaths(ops), tc.Touched, "every file that Codex touched is in an operation")
			}

			for _, op := range ops {
				switch op.Kind {
				case lessoncheck.OpPatchAdd:
					require.NotNil(t, op.Content, "no added line of a path case ends with two carriage returns, so every add has a content")
				case lessoncheck.OpPatchDelete:
					assert.Nil(t, op.Content)
					assert.Empty(t, op.MoveTo)
				case lessoncheck.OpPatchUpdate, lessoncheck.OpPatchMove:
					assert.Nil(t, op.Content, "an update holds chunks, not content")
				default:
					t.Fatalf("an apply_patch section gave the kind %q", op.Kind)
				}
			}
		})
	}

	t.Run("add content is the plus lines, each ended by a line feed", func(t *testing.T) {
		for _, tc := range []struct {
			patch   string
			content []string // the content of each add section, in order
		}{
			// Oracle case 05: a header with leading spaces after an add ends the add.
			{"*** Begin Patch\n*** Add File: n.txt\n+hi\n  *** Delete File: b.txt\n*** End Patch", []string{"hi\n"}},
			// Oracle case 14: an add without lines has an empty, non-nil content.
			{"*** Begin Patch\n*** Add File: n.txt\n*** Delete File: b.txt\n*** End Patch", []string{""}},
			// Oracle case 19: header text after the "+" is content.
			{"*** Begin Patch\n*** Add File: n.txt\n+*** Delete File: b.txt\n*** End Patch", []string{"*** Delete File: b.txt\n"}},
			// One carriage return per line end goes, as in the Codex parser.
			{"*** Begin Patch\r\n*** Add File: n.txt\r\n+a\r\n+\r\n+ b \r\n*** End Patch", []string{"a\n\n b \n"}},
			{"*** Begin Patch\n*** Add File: x.txt\n+1\n*** Add File: y.txt\n+2\n+3\n*** End Patch", []string{"1\n", "2\n3\n"}},
		} {
			ops, err := applyPatchOps(tc.patch)
			require.NoError(t, err)

			var contents []string

			for _, op := range ops {
				if op.Kind == lessoncheck.OpPatchAdd {
					require.NotNil(t, op.Content)
					contents = append(contents, *op.Content)
				}
			}

			assert.Equal(t, tc.content, contents, strings.ReplaceAll(tc.patch, "\n", `\n`))
		}
	})

	// The applier removes two carriage returns at a line end and patchBody
	// one, so a line that ends with two or more of them has other bytes on
	// disk than in the decoded line (oracle cases 97 and 98).
	t.Run("an added line that ends with more than one carriage return gives no content", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			patch   string
			paths   []string
			content []*string // the content of each add section, in order
		}{
			{"two carriage returns", "*** Begin Patch\n*** Add File: n.txt\n+x\r\r\n*** End Patch",
				[]string{"n.txt"}, []*string{nil}},
			{"three carriage returns between other lines", "*** Begin Patch\n*** Add File: n.txt\n+a\n+x\r\r\r\n+b\n*** End Patch",
				[]string{"n.txt"}, []*string{nil}},
			{"an empty line with two carriage returns", "*** Begin Patch\n*** Add File: n.txt\n+\r\r\n*** End Patch",
				[]string{"n.txt"}, []*string{nil}},
			{"a CRLF patch with one more carriage return", "*** Begin Patch\r\n*** Add File: n.txt\r\n+x\r\r\n*** End Patch",
				[]string{"n.txt"}, []*string{nil}},
			{"only the section with the line loses its content", "*** Begin Patch\n*** Add File: x.txt\n+1\r\r\n*** Add File: y.txt\n+2\r\n*** End Patch",
				[]string{"x.txt", "y.txt"}, []*string{nil, strPtr("2\n")}},
			{"a carriage return inside a line stays content", "*** Begin Patch\n*** Add File: n.txt\n+x\ry\n*** End Patch",
				[]string{"n.txt"}, []*string{strPtr("x\ry\n")}},
		} {
			ops, err := applyPatchOps(tc.patch)
			require.NoError(t, err, tc.name)

			var contents []*string

			for _, op := range ops {
				require.Equal(t, lessoncheck.OpPatchAdd, op.Kind, tc.name)
				contents = append(contents, op.Content)
			}

			assert.Equal(t, tc.paths, patchOpPaths(ops), "%s: the paths stay complete", tc.name)
			assert.Equal(t, tc.content, contents, tc.name)
		}
	})

	// A new recording that changes the add bytes must fail here, offline,
	// and not only in the native check that needs an installed Codex.
	t.Run("add content equals every recorded native add", func(t *testing.T) {
		_, oracle := loadApplyPatchOracle(t)
		require.NotNil(t, oracle.Content, "the oracle file holds a content part")

		var seen, unknown []string

		for _, tc := range oracle.Content.Cases {
			if tc.ExitStatus != 0 {
				continue
			}

			ops, err := applyPatchOps(tc.Patch)
			require.NoError(t, err, tc.Name)

			if !onlyAdds(ops) {
				continue
			}

			// A case without content must be one that the projection
			// keeps unknown on purpose, with a written reason.
			if slices.ContainsFunc(ops, func(op lessoncheck.FileOp) bool { return op.Content == nil }) {
				assert.Contains(t, oracleUnknown, tc.Name, "%s: an add without content is not listed as unknown", tc.Name)

				unknown = append(unknown, tc.Name)

				continue
			}

			for _, op := range ops {
				want, ok := tc.After[op.Path]
				require.True(t, ok, "%s: Codex wrote no %s", tc.Name, op.Path)
				assert.Equal(t, want, *op.Content, "%s: the projection of %s differs from the native bytes", tc.Name, op.Path)
			}

			seen = append(seen, tc.Name)
		}

		// An empty list would let the comparison pass with no evidence.
		assert.Equal(t, []string{
			"22_add_two_lines", "23_add_last_line_empty", "24_add_patch_final_newline",
			"25_add_empty", "26_add_over_existing", "27_add_crlf_patch",
		}, seen, "the recorded add cases")
		assert.Equal(t, []string{
			"97_add_lines_end_with_two_carriage_returns", "98_add_line_ends_with_three_carriage_returns",
		}, unknown, "the recorded add cases without content")
	})

	t.Run("any line under an update header is a hunk", func(t *testing.T) {
		for _, patch := range []string{
			"*** Begin Patch\n*** Update File: a.txt\n@@\n-x\n+y\n*** End Patch",
			"*** Begin Patch\n*** Update File: a.txt\n-x\n+y\n*** End Patch",
			"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n\n*** End Patch",
			"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End of File\n*** End Patch",
		} {
			ops, err := applyPatchOps(patch)
			require.NoError(t, err)
			require.Len(t, ops, 1)
			assert.Equal(t, lessoncheck.OpPatchUpdate, ops[0].Kind, patch)
			assert.True(t, ops[0].Hunks, patch)
		}
	})
}

// onlyAdds reports whether ops holds at least one operation and only
// add operations. The comparison here reads the add bytes of the
// decoder alone; TestCodexPatchProjectionMatchesTheContentOracle
// compares the projection of every kind.
func onlyAdds(ops []lessoncheck.FileOp) bool {
	for _, op := range ops {
		if op.Kind != lessoncheck.OpPatchAdd {
			return false
		}
	}

	return len(ops) > 0
}

// TestCodexPatchChunksFollowTheVerifiedGrammar checks the chunk grammar
// of an update section. A section in the grammar gives its chunks. Any
// other section gives nil chunks, so the projection stays unknown.
func TestCodexPatchChunksFollowTheVerifiedGrammar(t *testing.T) {
	line := func(kind lessoncheck.PatchLineKind, text string) lessoncheck.PatchLine {
		return lessoncheck.PatchLine{Kind: kind, Text: text}
	}

	ctx, del, add := lessoncheck.PatchContext, lessoncheck.PatchDelete, lessoncheck.PatchAdd

	t.Run("a section in the grammar gives its chunks", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			body   string
			chunks []lessoncheck.PatchChunk
		}{
			{"@@ alone opens a chunk without an anchor", "@@\n a\n-b\n+B",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(ctx, "a"), line(del, "b"), line(add, "B")}}}},
			{"the first chunk may omit @@", " a\n-b\n+B",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(ctx, "a"), line(del, "b"), line(add, "B")}}}},
			{"@@ with trailing spaces has no anchor", "@@  \n+z",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(add, "z")}}}},
			{"an anchor drops its trailing spaces", "@@ func b  \n-x\n+X",
				[]lessoncheck.PatchChunk{{Anchor: "func b", Lines: []lessoncheck.PatchLine{line(del, "x"), line(add, "X")}}}},
			{"an anchor keeps its leading spaces", "@@   def f\n-x\n+X",
				[]lessoncheck.PatchChunk{{Anchor: "  def f", Lines: []lessoncheck.PatchLine{line(del, "x"), line(add, "X")}}}},
			{"change lines keep their text without a trim", "@@\n   \n-b \n+B   ",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(ctx, "  "), line(del, "b "), line(add, "B   ")}}}},
			{"an empty line is empty context", "@@\n a\n\n-b",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(ctx, "a"), line(ctx, ""), line(del, "b")}}}},
			{"the end-of-file line closes a chunk", "@@\n-a\n+A\n*** End of File  \n@@ x\n+z",
				[]lessoncheck.PatchChunk{
					{Lines: []lessoncheck.PatchLine{line(del, "a"), line(add, "A")}, EndOfFile: true},
					{Anchor: "x", Lines: []lessoncheck.PatchLine{line(add, "z")}},
				}},
			{"header text after the first byte is content", "@@\n+*** End of File\n-@@",
				[]lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(add, "*** End of File"), line(del, "@@")}}}},
		} {
			ops, err := applyPatchOps("*** Begin Patch\n*** Update File: f.txt\n" + tc.body + "\n*** End Patch")
			require.NoError(t, err, tc.name)
			require.Len(t, ops, 1, tc.name)
			assert.True(t, ops[0].Hunks, tc.name)
			assert.Equal(t, tc.chunks, ops[0].Chunks, tc.name)
		}
	})

	t.Run("a section outside the grammar keeps its paths and gives no chunks", func(t *testing.T) {
		for _, tc := range []struct{ name, body string }{
			{"an @@ line without change lines", "@@"},
			{"two anchor lines in a row", "@@ class B\n@@  def f\n-  x\n+  y"},
			{"the end-of-file line opens the section", "*** End of File\n a"},
			{"a change line after the end-of-file line", "@@\n a\n-b\n+B\n*** End of File\n+c"},
			{"two end-of-file lines", "@@\n-a\n*** End of File\n*** End of File"},
			{"an @@ line without a space before the anchor", "@@\n a\n@@x\n+z"},
			{"a later chunk without @@", "@@\n-a\n*** End of File\n-b"},
			{"a line of whitespace that is not a space", "@@\n a\n\t"},
			{"an @@ line at the end", "@@\n a\n@@"},
			{"an added line that ends with a carriage return", "@@\n a\n+B\r\r"},
			{"a context line that ends with a carriage return", "@@\n a\r\r\n-b"},
			{"a line of one carriage return", "@@\n a\n\r\r\n-b"},
		} {
			ops, err := applyPatchOps("*** Begin Patch\n*** Update File: f.txt\n" + tc.body + "\n*** End Patch")
			require.NoError(t, err, tc.name)
			require.Len(t, ops, 1, tc.name)
			assert.Equal(t, "f.txt", ops[0].Path, tc.name)
			assert.True(t, ops[0].Hunks, tc.name)
			assert.Nil(t, ops[0].Chunks, tc.name)
		}
	})

	t.Run("each update section gets its own chunks", func(t *testing.T) {
		ops, err := applyPatchOps("*** Begin Patch\n*** Update File: a.txt\n@@\n+x\n*** Add File: b.txt\n+y\n" +
			"*** Update File: c.txt\n*** Move to: d.txt\n@@\n-z\n*** Update File: e.txt\n@@\n*** End Patch")
		require.NoError(t, err)
		require.Len(t, ops, 4)

		assert.Equal(t, []lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(add, "x")}}}, ops[0].Chunks)
		assert.Nil(t, ops[1].Chunks, "an add section has content, not chunks")
		assert.Equal(t, []lessoncheck.PatchChunk{{Lines: []lessoncheck.PatchLine{line(del, "z")}}}, ops[2].Chunks)
		assert.Equal(t, "d.txt", ops[2].MoveTo)
		assert.Nil(t, ops[3].Chunks, "an @@ line without change lines is outside the grammar")
	})

	t.Run("a move without hunks gives no chunks", func(t *testing.T) {
		ops, err := applyPatchOps("*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End Patch")
		require.NoError(t, err)
		require.Len(t, ops, 1)
		assert.Equal(t, lessoncheck.OpPatchMove, ops[0].Kind)
		assert.Nil(t, ops[0].Chunks)
	})
}
