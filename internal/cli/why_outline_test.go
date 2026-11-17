package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manyFuncs returns Go source with n functions f1..fn. Each takes three
// lines: the header, the closing brace, and a blank line.
func manyFuncs(n int) string {
	var b strings.Builder

	b.WriteString("package main\n\n")

	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "func f%d() {\n}\n\n", i)
	}

	return b.String()
}

func TestWhyFileOutlineOverflow(t *testing.T) {
	root := writeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "many.go"), []byte(manyFuncs(21)), 0o644))

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "why", "many.go")
	require.NoError(t, err)

	assert.Contains(t, out, "\ndefines (21; showing 20)\n")
	assert.Equal(t, 20, strings.Count(out, "\n  function "))

	// Each entry holds its real inclusive lines and its signature.
	assert.Contains(t, out, fmt.Sprintf("  %-8s %-44s %s\n           func f1()\n", "function", "f1", "many.go:3-4"))
	assert.Contains(t, out, fmt.Sprintf("  %-8s %-44s %s\n           func f20()\n", "function", "f20", "many.go:60-61"))
	assert.Contains(t, out, "  … 1 more not shown; source of the next one (f21): expand many.go:63-64 (MCP)\n")
}

func TestWhyFileOutlineCallers(t *testing.T) {
	root := writeFixture(t)

	// ta.go calls t twice from one function, so it adds one caller. The
	// edge key holds the caller, not the call expression. The test caller
	// stays out of the definitions, because callers outside tests exist.
	files := map[string]string{
		"t.go":      "package main\n\nfunc t() {\n}\n",
		"ta.go":     "package main\n\nfunc twice() {\n\tt()\n\tt()\n}\n",
		"tb.go":     "package main\n\nfunc once() {\n\tt()\n}\n",
		"t_test.go": "package main\n\nimport \"testing\"\n\nfunc TestT(x *testing.T) {\n\tt()\n}\n",
	}

	for rel, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "why", "t.go")
	require.NoError(t, err)

	assert.Contains(t, out, fmt.Sprintf("  %-8s %-44s %s\n           func t()\n", "function", "t", "t.go:3-4")+
		"           callers  3 · 2 in other non-test files · 1 in test files\n"+
		"           caller definitions  once tb.go:3 · twice ta.go:3 · +1 more: why t\n")
}
