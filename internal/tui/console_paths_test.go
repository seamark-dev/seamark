package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D-2: the console writes lessons.yaml only through pinfile and Route A.
// A plain os.WriteFile is not atomic and proves nothing, so no console
// path may call it. The CLI pin commands share these engines now. The
// CLI trigger extraction keeps a wider selection than the console, so
// the console must not call it. A grep can miss an aliased import and
// can match a comment, so this guard reads the syntax trees of the
// console packages.

// consolePathSources are the non-test Go sources of the console write
// paths: the console with its package ui, the maintenance engine, the
// pin file engine and the console trigger extraction.
func consolePathSources(t *testing.T) []string {
	t.Helper()

	var files []string

	for _, dir := range []string{".", "ui", "../maintain", "../pinfile"} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)

		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				files = append(files, filepath.Join(dir, name))
			}
		}
	}

	return append(files, "../learning/extract.go")
}

// consoleBannedCalls maps an import path to the functions of it that the
// console must not reference (D-2).
var consoleBannedCalls = map[string][]string{
	"github.com/seamark-dev/seamark/internal/distill": {"ExtractTriggers"},
	"os": {"WriteFile"},
}

// consoleBannedImport is the CLI package. Its adapters print the CLI
// lines and keep the CLI selection, which the console does not use.
const consoleBannedImport = "github.com/seamark-dev/seamark/internal/cli"

// consoleBannedRefs returns every reference to a banned function in one
// parsed file, as "pkg.Func". It resolves the local import names, so an
// aliased import does not hide a call. A reference without a call, for
// example a function value, also counts.
func consoleBannedRefs(file *ast.File) []string {
	local := map[string]string{}

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}

		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}

		local[name] = path
	}

	var found []string

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}

		for _, fn := range consoleBannedCalls[local[pkg.Name]] {
			if sel.Sel.Name == fn {
				found = append(found, pkg.Name+"."+fn)
			}
		}

		return true
	})

	return found
}

func TestConsolePathsAvoidTheCLIWrites(t *testing.T) {
	files := consolePathSources(t)
	require.Greater(t, len(files), 10, "the guard reads the console packages")

	for _, name := range files {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		for _, imp := range file.Imports {
			assert.NotEqual(t, strconv.Quote(consoleBannedImport), imp.Path.Value,
				"%s imports the CLI package (D-2)", name)
		}

		assert.Empty(t, consoleBannedRefs(file), "%s references a CLI write path (D-2)", name)
	}
}

func TestConsolePathsGuardFindsAliasedCallsAndSkipsComments(t *testing.T) {
	const src = `package x

import (
	d "github.com/seamark-dev/seamark/internal/distill"
	"os"
)

// os.WriteFile in a comment is no call.
func f() {
	_ = d.ExtractTriggers
	_ = os.WriteFile("a", nil, 0o600)
	_ = os.ReadFile
}
`

	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments)
	require.NoError(t, err)

	assert.Equal(t, []string{"d.ExtractTriggers", "os.WriteFile"}, consoleBannedRefs(file),
		"an alias does not hide a reference, and a comment is not one")
}
