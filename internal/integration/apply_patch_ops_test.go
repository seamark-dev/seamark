package integration

import (
	"encoding/json"
	"errors"
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
					require.NotNil(t, op.Content, "an add always has a content")
				case lessoncheck.OpPatchDelete:
					assert.Nil(t, op.Content)
					assert.Empty(t, op.MoveTo)
				case lessoncheck.OpPatchUpdate, lessoncheck.OpPatchMove:
					assert.Nil(t, op.Content, "update content has no verified projection")
				default:
					t.Fatalf("an apply_patch section gave the kind %q", op.Kind)
				}
			}
		})
	}

	t.Run("add content is the plus lines joined with a line feed", func(t *testing.T) {
		for _, tc := range []struct {
			patch   string
			content []string // the content of each add section, in order
		}{
			// Oracle case 05: a header with leading spaces after an add ends the add.
			{"*** Begin Patch\n*** Add File: n.txt\n+hi\n  *** Delete File: b.txt\n*** End Patch", []string{"hi"}},
			// Oracle case 14: an add without lines has an empty, non-nil content.
			{"*** Begin Patch\n*** Add File: n.txt\n*** Delete File: b.txt\n*** End Patch", []string{""}},
			// Oracle case 19: header text after the "+" is content.
			{"*** Begin Patch\n*** Add File: n.txt\n+*** Delete File: b.txt\n*** End Patch", []string{"*** Delete File: b.txt"}},
			// One carriage return per line end goes, as in the Codex parser.
			{"*** Begin Patch\r\n*** Add File: n.txt\r\n+a\r\n+\r\n+ b \r\n*** End Patch", []string{"a\n\n b "}},
			{"*** Begin Patch\n*** Add File: x.txt\n+1\n*** Add File: y.txt\n+2\n+3\n*** End Patch", []string{"1", "2\n3"}},
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
