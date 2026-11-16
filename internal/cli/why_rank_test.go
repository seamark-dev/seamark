package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

// writeRankFixture writes a repository whose directory package api and
// constant client.api share the name api: the indexer stores the package
// grouping symbol first, so it has the lower id. Five directories define
// show, as many as why's limit, beside the directory package show. It
// also holds a Python package with an empty __init__.py, and imports fmt
// from outside.
func writeRankFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n",
		"main.go": `package main

import (
	"example.com/fix/api"
	"example.com/fix/client"
)

func main() {
	api.Serve()
	client.Show()
}
`,
		"api/api.go": `package api

// Serve starts the server.
func Serve() {}
`,
		"client/client.go": `package client

import "fmt"

const api = "v1"

// Show prints the API version.
func Show() { fmt.Println(api) }
`,
		"show/show.go":      "package show\n\nfunc Run() {}\n",
		"tools/__init__.py": "",
		"tools/report.py":   "def render():\n    return 1\n",
	}

	for _, dir := range []string{"a", "b", "c", "d", "e"} {
		files[dir+"/"+dir+".go"] = "package " + dir + "\n\nfunc show() {}\n"
	}

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	return root
}

// TestWhyPrefersADefinedSymbolOverAPackage proves that why reports the
// symbol that has a definition when a package grouping symbol has the
// same name, and lists the package, labelled as this repository's,
// under also matched. Before, the package with the lower id won and the
// report was empty.
func TestWhyPrefersADefinedSymbolOverAPackage(t *testing.T) {
	root := writeRankFixture(t)

	out, err := run(t, "-C", root, "why", "api")
	require.NoError(t, err)

	lines := strings.Split(out, "\n")
	assert.Equal(t, "client.api  (const)", lines[0], out)
	assert.Equal(t, "  defined  client/client.go:5", lines[1], out)

	_, also, found := strings.Cut(out, "\nalso matched\n")
	require.True(t, found, out)
	assert.Contains(t, also, fmt.Sprintf("  %-40s %s\n", "api", "(package)"))
	assert.NotContains(t, out, "(external)")
}

// TestExpandPrefersADefinedSymbolOverAPackage proves that expand, which
// takes the first match, prints the source of the constant instead of
// failing on the package, with the also matched note; a query that only
// a package matches keeps its error.
func TestExpandPrefersADefinedSymbolOverAPackage(t *testing.T) {
	root := writeRankFixture(t)

	st, err := store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = st.Close() })

	var buf bytes.Buffer
	require.NoError(t, report.Expand(&buf, st, root, "api"))
	assert.Equal(t,
		"note: \"api\" also matched api — expanding client.api\n"+
			"client/client.go:5-5\n"+
			"   5\tconst api = \"v1\"\n",
		buf.String())

	buf.Reset()
	err = report.Expand(&buf, st, root, "fmt")
	require.Error(t, err)
	assert.Equal(t, "fmt is external — no source in this repo", err.Error())
}

// TestWhyLabelsRepositoryPackages proves that a package of this
// repository is not called external: a Go directory and a Python package
// with an empty __init__.py get the repository label on the definition
// line and (package) in the list, while the import target fmt keeps
// (external).
func TestWhyLabelsRepositoryPackages(t *testing.T) {
	root := writeRankFixture(t)

	const ours = "  defined  (package of this repository; `why <file>` or `why <symbol>` for content)"

	for _, query := range []string{"client", "tools"} {
		out, err := run(t, "-C", root, "why", query)
		require.NoError(t, err)

		lines := strings.Split(out, "\n")
		assert.Equal(t, query+"  (package)", lines[0], out)
		assert.Equal(t, ours, lines[1], out)
	}

	out, err := run(t, "-C", root, "why", "tools")
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("  %-40s %s\n", "tools/report", "(package)"))

	out, err = run(t, "-C", root, "why", "fmt")
	require.NoError(t, err)
	assert.Equal(t, "fmt  (package)\n  defined  (external)\n", out)
}

// TestWhyKeepsAPackageThatDefinitionsWouldCut proves that a package whose
// name as many definitions share as why shows still appears: why reports
// the first definition as before and lists the package as the last also
// matched line, and expand, which reads two rows, names it in its note.
func TestWhyKeepsAPackageThatDefinitionsWouldCut(t *testing.T) {
	root := writeRankFixture(t)

	out, err := run(t, "-C", root, "why", "show")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, "a.show  (function)", lines[0], out)
	assert.Equal(t, fmt.Sprintf("  %-40s %s", "show", "(package)"), lines[len(lines)-1], out)

	_, also, found := strings.Cut(out, "\nalso matched\n")
	require.True(t, found, out)
	assert.Len(t, strings.Split(strings.TrimRight(also, "\n"), "\n"), 4, "why still shows five matches")

	st, err := store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = st.Close() })

	var buf bytes.Buffer
	require.NoError(t, report.Expand(&buf, st, root, "show"))

	note, _, _ := strings.Cut(buf.String(), "\n")
	assert.Equal(t, "note: \"show\" also matched show — expanding a.show", note)
}
