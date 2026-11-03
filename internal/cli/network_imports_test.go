package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sentinel stubs see a child process only. Go caches the proxy
// settings, so the sentinel cannot see a request of the test process.
// This file checks the source code instead: production code imports no
// network package, apart from one known file that only reads HTTP status
// constants.

// statusOnlyFile is the one production file that imports net/http. It
// classifies the exit of the gh tool by status code and sends no
// request, because gh does the network work.
const statusOnlyFile = "internal/reviews/reviews.go"

// networkImport reports whether a standard import path can open a
// connection. Outside net, only crypto/tls and log/syslog export a Dial
// function. crypto/x509 and expvar import net too, but they export no
// function that opens a connection. net/url only parses text, and three
// packages use it.
//
// A third-party path stays outside this scan. cobra, pflag, and sqlite
// already reach net, so the reach alone cannot show a new use.
func networkImport(path string) bool {
	if path == "crypto/tls" || path == "log/syslog" {
		return true
	}

	return path == "net" || strings.HasPrefix(path, "net/") && path != "net/url"
}

// networkUses scans the non-test Go files under dirs of root and returns
// each network use, sorted. A file under testdata is no production code.
func networkUses(t *testing.T, root string, dirs ...string) []string {
	t.Helper()

	var found []string

	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && d.Name() == "testdata":
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			uses, err := networkUsesOfFile(path, filepath.ToSlash(rel))
			found = append(found, uses...)

			return err
		})
		require.NoError(t, err)
	}

	slices.Sort(found)

	return found
}

// networkUsesOfFile returns the network uses of one Go file. rel is its
// path from the repository root, and the findings name it.
func networkUsesOfFile(path, rel string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	var found []string

	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}

		switch {
		case !networkImport(imported):
		case rel == statusOnlyFile && imported == "net/http":
			found = append(found, httpNamesOf(file, spec, rel)...)
		default:
			found = append(found, rel+": imports "+imported)
		}
	}

	return found, nil
}

// httpNamesOf returns each name of the net/http import that is no status
// constant. A dot import hides its names from this scan, so it counts as
// a finding by itself.
func httpNamesOf(file *ast.File, spec *ast.ImportSpec, rel string) []string {
	local := "http"

	if spec.Name != nil {
		local = spec.Name.Name
	}

	if local == "." {
		return []string{rel + ": dot import of net/http"}
	}

	var found []string

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == local && !strings.HasPrefix(sel.Sel.Name, "Status") {
			found = append(found, rel+": uses http."+sel.Sel.Name)
		}

		return true
	})

	return found
}

func TestProductionCodeHasNoNewNetworkUse(t *testing.T) {
	assert.Empty(t, networkUses(t, filepath.Join("..", ".."), "cmd", "internal"),
		"a request from the process passes the sentinel; route it through a tool or add no such code")
}

// TestNetworkScanReadsEachForm plants each form in a temporary tree. An
// empty result of the real scan means nothing unless the scan can find
// these forms.
func TestNetworkScanReadsEachForm(t *testing.T) {
	root := t.TempDir()

	plant := func(rel, body string) {
		t.Helper()

		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}

	plant("cmd/tool/main.go", "package main\n\nimport \"net\"\n\nvar _ net.Conn\n")
	plant("internal/a/plain.go", "package a\n\nimport \"net/http\"\n\nvar _ = http.StatusOK\n")
	plant("internal/a/alias.go", "package a\n\nimport web \"net/http\"\n\nvar _ = web.StatusOK\n")
	plant("internal/a/rpc.go", "package a\n\nimport _ \"net/rpc\"\n")
	plant("internal/a/tls.go", "package a\n\nimport \"crypto/tls\"\n\nvar _ = tls.Dial\n")
	plant("internal/a/syslog.go", "package a\n\nimport \"log/syslog\"\n\nvar _ = syslog.Dial\n")

	// The known file may read a status constant and nothing else.
	plant(statusOnlyFile, "package reviews\n\nimport web \"net/http\"\n\nvar (\n\t_ = web.StatusForbidden\n\t_ = web.Get\n)\n")

	// None of these is a network use of production code.
	plant("internal/a/parse.go", "package a\n\nimport \"net/url\"\n\nvar _ = url.Parse\n")
	plant("internal/a/a_test.go", "package a\n\nimport \"net/http\"\n\nvar _ = http.Get\n")
	plant("internal/a/testdata/b.go", "package b\n\nimport \"net\"\n\nvar _ net.Conn\n")

	assert.Equal(t, []string{
		"cmd/tool/main.go: imports net",
		"internal/a/alias.go: imports net/http",
		"internal/a/plain.go: imports net/http",
		"internal/a/rpc.go: imports net/rpc",
		"internal/a/syslog.go: imports log/syslog",
		"internal/a/tls.go: imports crypto/tls",
		"internal/reviews/reviews.go: uses http.Get",
	}, networkUses(t, root, "cmd", "internal"))

	plant(statusOnlyFile, "package reviews\n\nimport . \"net/http\"\n\nvar _ = Get\n")
	assert.Contains(t, networkUses(t, root, "internal"), "internal/reviews/reviews.go: dot import of net/http")

	// The exception of the known file covers net/http only, so another
	// network package in that file is a finding like any other.
	plant(statusOnlyFile, "package reviews\n\nimport \"crypto/tls\"\n\nvar _ = tls.Dial\n")
	assert.Equal(t, []string{"internal/reviews/reviews.go: imports crypto/tls"}, networkUses(t, root, "internal/reviews"))
}
