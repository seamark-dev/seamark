package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWhyHistoryHeadersNameTheFileOverMCP proves that the MCP why answer
// for a symbol names the file in its history headers. The history comes
// from the file that defines the symbol, and the MCP answer is the text
// that agents read, so the headers must say which file they describe.
func TestWhyHistoryHeadersNameTheFileOverMCP(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	write := func(rel, content string) {
		t.Helper()

		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	git("init", "-q", "-b", "main")
	write("go.mod", "module example.com/history\n")

	// pkg/tax.go changes with pkg/rates.go in three commits. Commits on
	// the notes alone keep the lift of the pair above one.
	for v := 1; v <= 3; v++ {
		write("pkg/tax.go", fmt.Sprintf("package pkg\n\nfunc Tax() int { return %d }\n", v))
		write("pkg/rates.go", fmt.Sprintf("package pkg\n\nfunc Rate() int { return %d }\n", v))
		git("add", "-A")
		git("commit", "-q", "-m", fmt.Sprintf("change %d", v))
	}

	for v := 1; v <= 3; v++ {
		write("docs/notes.md", fmt.Sprintf("notes %d\n", v))
		git("add", "-A")
		git("commit", "-q", "-m", fmt.Sprintf("notes %d", v))
	}

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"why","arguments":{"query":"pkg.Tax"}}}`,
	)

	text, isErr := toolText(t, resps["2"])
	require.False(t, isErr, text)

	lines := strings.Split(text, "\n")

	assert.Contains(t, lines, "usually changed with  (whole file pkg/tax.go; empirical, lift > 1 means beyond chance)", text)
	assert.Contains(t, lines, "recent decisions  (whole file pkg/tax.go)", text)
}
