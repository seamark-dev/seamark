package lessonreport

import (
	"go/build"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modulePath is the module of this repository.
const modulePath = "github.com/seamark-dev/seamark"

// allowedInternalImports are the internal packages that the non-test
// files of lessonreport may import directly. The list keeps the engine
// out of surfaces and writers. A listed package is allowed, not required:
// the test does not fail when no file imports a listed package.
var allowedInternalImports = []string{
	modulePath + "/internal/distill",
	modulePath + "/internal/integration",
	modulePath + "/internal/lessoncheck",
	modulePath + "/internal/model",
	modulePath + "/internal/render",
	modulePath + "/internal/reviews",
}

// bannedImports are the packages that the engine never imports
// directly. outcome would join the capture reading to the outcome
// reading. The others are surfaces, writers, or a way to a model
// or the network.
var bannedImports = []string{
	modulePath + "/internal/outcome",
	modulePath + "/internal/store",
	modulePath + "/internal/agent",
	modulePath + "/internal/tui",
	modulePath + "/internal/cli",
	modulePath + "/internal/mcp",
	"net/http",
}

// TestLessonreportImportSet keeps the direct import rule of the engine.
// No linter rule checks it, so this test does. The test covers direct
// imports only, because integration and distill bring agent, store, and
// net/http into the closure. The report surfaces prove their own import
// sets in their own sentinel tests. The direct imports come from every
// file of the package, also the files of other platforms.
func TestLessonreportImportSet(t *testing.T) {
	ctx := build.Default
	ctx.UseAllFiles = true

	pkg, err := ctx.ImportDir(".", 0)
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Imports, "the import list must not be empty, or the test checks nothing")

	for _, path := range pkg.Imports {
		first, _, _ := strings.Cut(path, "/")

		switch {
		case strings.HasPrefix(path, modulePath+"/"):
			assert.Contains(t, allowedInternalImports, path,
				"lessonreport imports %s; only reviews, integration, lessoncheck, model, render, and distill are allowed", path)
		case strings.Contains(first, "."):
			assert.Fail(t, "lessonreport imports the module "+path+"; the engine uses the standard library only")
		}

		assert.NotContains(t, bannedImports, path, "lessonreport must not import %s directly", path)
	}

	// A misspelled name would make the loop above pass for nothing, so
	// each banned internal package must exist.
	for _, path := range bannedImports {
		if rel, ok := strings.CutPrefix(path, modulePath+"/"); ok {
			require.DirExists(t, filepath.Join("..", "..", rel))
		}
	}
}
