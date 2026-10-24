package delivery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package path suffixes that the advisory surfaces must not call
// into for evaluation.
const (
	lessoncheckPath = "/internal/lessoncheck"
	historyPath     = "/internal/history"
)

// evaluatingCalls are the lessoncheck functions that read a snapshot or
// run a checker. Evaluate, Verify, and ReadWorkingTree exist now. The
// other names are the K4, K5, and K8 functions of milestone P4: the list
// names them now, so the test still holds when they land.
var evaluatingCalls = []string{
	"Evaluate", "Verify", "ReadWorkingTree", "Revalidate", "ProjectOperation", "ObserveAfter", "DecideGuard",
}

// TestAdvisorySurfacesNeverEvaluateChecks holds BRIEF rule 15 for the
// advisory surfaces of this package: the edit hook (delivery.go),
// preparation (prepare.go), and exploration (explore.go). No file of the
// package calls a checker, a snapshot reader, or history.RunGit. The
// walk reads every file of the package, so a new file of an advisory
// surface is covered without a change here.
func TestAdvisorySurfacesNeverEvaluateChecks(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var walked []string

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		walked = append(walked, name)

		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		require.NoError(t, err)

		for _, call := range forbiddenUses(t, file) {
			assert.Fail(t, "an advisory surface evaluates a check", "%s uses %s", name, call)
		}
	}

	// The walk must reach the surfaces that it guards, so a renamed file
	// cannot make the test pass with nothing to read.
	for _, surface := range []string{"delivery.go", "prepare.go", "prepare_text.go", "explore.go"} {
		assert.Contains(t, walked, surface)
	}

	t.Run("the walk sees an aliased call", func(t *testing.T) {
		src := `package delivery

import (
	lc "github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/history"
)

func f() {
	_ = lc.Evaluate(nil, nil)
	_, _ = history.RunGit(nil, history.GitCall{})
	_ = lc.Describe
}
`
		file, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, 0)
		require.NoError(t, err)

		assert.Equal(t, []string{"lessoncheck.Evaluate", "history.RunGit"}, forbiddenUses(t, file),
			"Describe is a text function and stays allowed")
	})
}

// forbiddenUses returns each selector of file that names an evaluating
// lessoncheck function or history.RunGit. It resolves the local import
// names, so an alias does not hide a call.
func forbiddenUses(t *testing.T, file *ast.File) []string {
	t.Helper()

	var found []string

	locals := map[string]string{}

	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		require.NoError(t, err)

		local := path.Base(importPath)
		if imp.Name != nil {
			local = imp.Name.Name
		}

		locals[local] = importPath
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		switch importPath := locals[x.Name]; {
		case strings.HasSuffix(importPath, lessoncheckPath) && isEvaluatingCall(sel.Sel.Name):
			found = append(found, "lessoncheck."+sel.Sel.Name)
		case strings.HasSuffix(importPath, historyPath) && sel.Sel.Name == "RunGit":
			found = append(found, "history.RunGit")
		}

		return true
	})

	return found
}

// isEvaluatingCall reports whether a lessoncheck name is a checker or a
// snapshot reader.
func isEvaluatingCall(name string) bool {
	for _, call := range evaluatingCalls {
		if call == name {
			return true
		}
	}

	return false
}
