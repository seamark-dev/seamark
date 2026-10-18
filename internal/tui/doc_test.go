package tui

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docMapFiles returns the file names of the file map in the package doc
// of doc.go, in map order. A map line is a list item of the form
// "  - name.go: responsibility".
func docMapFiles(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	require.NoError(t, err)
	require.NotNil(t, file.Doc, "doc.go must hold the package doc")

	var names []string

	for line := range strings.SplitSeq(file.Doc.Text(), "\n") {
		item, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
		if !ok {
			continue
		}

		name, _, ok := strings.Cut(item, ":")
		if ok && strings.HasSuffix(name, ".go") {
			names = append(names, name)
		}
	}

	return names
}

// TestDocListsEveryFile keeps the file map of doc.go true. Every non-test
// Go file of the package appears in the map once, and every name in the
// map is a file of the package. So a new, renamed or deleted file fails
// here until the map says where it belongs.
func TestDocListsEveryFile(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var files []string

	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			files = append(files, name)
		}
	}

	listed := docMapFiles(t)

	missing := slices.DeleteFunc(slices.Clone(files), func(n string) bool { return slices.Contains(listed, n) })
	assert.Empty(t, missing, "doc.go: add each file to the file map, in the group of its subject")

	gone := slices.DeleteFunc(slices.Clone(listed), func(n string) bool { return slices.Contains(files, n) })
	assert.Empty(t, gone, "doc.go: the file map names files that the package does not have")

	sorted := slices.Clone(listed)
	slices.Sort(sorted)
	assert.Equal(t, slices.Compact(slices.Clone(sorted)), sorted, "doc.go: the file map names a file twice")
}
