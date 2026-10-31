package lessoncheck

import (
	"go/build"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modulePath is the module of this repository.
const modulePath = "github.com/seamark-dev/seamark"

// lessonCheckInternalImports are the internal packages that lessoncheck
// may import directly. The package doc comment states the same rule.
var lessonCheckInternalImports = []string{
	modulePath + "/internal/history",
	modulePath + "/internal/privatedir",
	modulePath + "/internal/redact",
	modulePath + "/internal/render",
	modulePath + "/internal/reviews",
}

// lessonCheckModuleImports are the modules outside the standard library
// that lessoncheck may import directly: the line diff of no-added-pattern/1
// and the YAML decoder.
var lessonCheckModuleImports = []string{
	"github.com/pmezard/go-difflib/difflib",
	"gopkg.in/yaml.v3",
}

// lessonCheckBannedDeps are the internal packages that must not be in
// the import closure of lessoncheck. The hook handlers call lessoncheck
// on every edit. These packages pull in a model client, the index store,
// sqlite, or the policy gate, and integration imports lessoncheck, so an
// edge to it is a cycle.
var lessonCheckBannedDeps = []string{
	"agent", "distill", "gate", "integration", "report", "store",
}

// TestLessonCheckImportSet keeps the import rule of the package. No
// linter rule checks it, so this test does. The direct imports come from
// every file of the package, also the files of other platforms. The
// closure comes from go list, because a banned package can arrive
// through an allowed one.
func TestLessonCheckImportSet(t *testing.T) {
	ctx := build.Default
	ctx.UseAllFiles = true

	pkg, err := ctx.ImportDir(".", 0)
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Imports, "the import list must not be empty, or the test checks nothing")

	for _, path := range pkg.Imports {
		// The go tool uses this rule too: a standard library path has no
		// dot in its first element, and a module path has one.
		first, _, _ := strings.Cut(path, "/")

		switch {
		case strings.HasPrefix(path, modulePath+"/"):
			assert.Contains(t, lessonCheckInternalImports, path,
				"lessoncheck imports %s; only reviews, redact, render, history, and privatedir are allowed", path)
		case strings.Contains(first, "."):
			assert.Contains(t, lessonCheckModuleImports, path,
				"lessoncheck imports the module %s; only go-difflib and yaml.v3 are allowed", path)
		default:
			// reviews holds net/http for the review fetch. A direct use
			// here would let a checker reach the network.
			assert.NotContains(t, []string{"net", "net/http"}, path,
				"lessoncheck must not import %s directly; a check never uses the network", path)
		}
	}

	cmd := exec.Command("go", "list", "-deps", modulePath+"/internal/lessoncheck")
	cmd.Dir = "../.."

	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps of lessoncheck")

	deps := strings.Fields(string(out))
	require.Contains(t, deps, modulePath+"/internal/reviews",
		"the closure holds reviews, so the list is complete")

	for _, name := range lessonCheckBannedDeps {
		// A misspelled name would make the assertion below pass for
		// nothing, so each banned package must exist.
		require.DirExists(t, filepath.Join("..", name))

		// slices.Contains keeps the failure text short: NotContains
		// would print the whole closure.
		assert.False(t, slices.Contains(deps, modulePath+"/internal/"+name),
			"the import closure of lessoncheck must not hold internal/%s", name)
	}
}
