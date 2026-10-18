package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
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
// paths: the console with its packages ui and services, the
// maintenance engine, the pin file engine and the console trigger
// extraction.
func consolePathSources(t *testing.T) []string {
	t.Helper()

	var files []string

	for _, dir := range []string{".", "ui", "services", "../maintain", "../pinfile"} {
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

// consoleBannedRefs returns every reference in one parsed file to a
// name that banned lists for its import path, as "pkg.Name". It resolves
// the local import names, so an aliased import does not hide a call. A
// reference without a call, for example a function value or a type, also
// counts.
func consoleBannedRefs(file *ast.File, banned map[string][]string) []string {
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

		for _, fn := range banned[local[pkg.Name]] {
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

		assert.Empty(t, consoleBannedRefs(file, consoleBannedCalls), "%s references a CLI write path (D-2)", name)
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

	assert.Equal(t, []string{"d.ExtractTriggers", "os.WriteFile"}, consoleBannedRefs(file, consoleBannedCalls),
		"an alias does not hide a reference, and a comment is not one")
}

// R-14: the Phase 4 files style text only through the tones and styles
// of ui.Theme. Only ui/theme.go holds colors. It pairs a light and a
// dark value, and the tests fix the ASCII color profile. A color in a
// screen file skips both, so this guard bans the color constructors of
// Lip Gloss and termenv, a hex color literal, and an SGR escape literal.
// It reuses the syntax reader of the D-2 guard, so an alias or a
// comment does not change the verdict.

// phase4StylePatterns name the non-test sources of Phase 4 (contracts
// §7, C2), relative to internal/tui.
var phase4StylePatterns = []string{
	"pins_author*.go", "settings_lessons*.go", "sample*.go", "state_saved.go", "ui/textarea.go",
}

// themeBannedColors maps an import path to its color constructors.
var themeBannedColors = map[string][]string{
	"github.com/charmbracelet/lipgloss": {"Color", "AdaptiveColor", "CompleteColor", "CompleteAdaptiveColor", "ANSIColor"},
	"github.com/muesli/termenv":         {"RGBColor", "ANSI256Color", "ANSIColor"},
}

// rawColor matches a string that is a hex color, such as "#E8B23A", or
// that holds an SGR escape sequence, such as "\x1b[31m".
var rawColor = regexp.MustCompile(`^#(?:[0-9A-Fa-f]{3}){1,2}$|\x1b\[[0-9;]*m`)

// rawColorLiterals returns every string literal of a parsed file that
// rawColor matches, as it stands in the source.
func rawColorLiterals(file *ast.File) []string {
	var found []string

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}

		if s, err := strconv.Unquote(lit.Value); err == nil && rawColor.MatchString(s) {
			found = append(found, lit.Value)
		}

		return true
	})

	return found
}

func TestPhase4FilesStyleOnlyThroughThemeTones(t *testing.T) {
	for _, pattern := range phase4StylePatterns {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)

		var files []string

		for _, name := range matches {
			if !strings.HasSuffix(name, "_test.go") {
				files = append(files, name)
			}
		}

		require.NotEmpty(t, files, "%s names no source; a moved file keeps its guard only when the pattern follows it", pattern)

		for _, name := range files {
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
			require.NoError(t, err)

			assert.Empty(t, consoleBannedRefs(file, themeBannedColors), "%s builds a color; use a tone of ui.Theme", name)
			assert.Empty(t, rawColorLiterals(file), "%s holds a raw color literal; use a tone of ui.Theme", name)
		}
	}
}

func TestThemeToneGuardFindsAliasedColorsAndSkipsComments(t *testing.T) {
	const src = `package x

import (
	lg "github.com/charmbracelet/lipgloss"
)

// lg.Color("#E8B23A") in a comment is no color.
var (
	a = lg.Color("205")
	b = lg.AdaptiveColor{Light: "#fff", Dark: "#000000"}
	c = "\x1b[31m" + "red"
	d = lg.NewStyle()
	e = "#tag"
)
`

	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments)
	require.NoError(t, err)

	assert.Equal(t, []string{"lg.Color", "lg.AdaptiveColor"}, consoleBannedRefs(file, themeBannedColors),
		"an alias does not hide a color, and a comment is not one")
	assert.Equal(t, []string{`"#fff"`, `"#000000"`, `"\x1b[31m"`}, rawColorLiterals(file),
		"a hex color and an SGR escape count; a style without a color and a plain # text do not")
}
