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

	"github.com/seamark-dev/seamark/internal/report"
)

func TestToolsNameNoGonePartner(t *testing.T) {
	// The server runs git itself, so the user's git configuration must
	// not change the diff that check reads.
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
	write("go.mod", "module example.com/gone\n")

	// pkg/a.go changes with three partners in three commits. Commits on
	// the notes alone keep the lift of these pairs above one.
	for v := 1; v <= 3; v++ {
		for _, name := range []string{"a", "live", "gone", "moved"} {
			write("pkg/"+name+".go", fmt.Sprintf("package pkg\n\nfunc %s%d() {}\n", name, v))
		}

		git("add", "-A")
		git("commit", "-q", "-m", fmt.Sprintf("change %d", v))
	}

	for v := 1; v <= 3; v++ {
		write("docs/notes.md", fmt.Sprintf("notes %d\n", v))
		git("add", "-A")
		git("commit", "-q", "-m", fmt.Sprintf("notes %d", v))
	}

	// One partner is deleted and one renamed, neither committed. The
	// edit of pkg/a.go puts it in the diff, so check has an anchor.
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "gone.go")))
	git("mv", "pkg/moved.go", "pkg/renamed.go")
	write("pkg/a.go", "package pkg\n\nfunc a4() {}\n")

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"why","arguments":{"query":"pkg/a.go"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"change_set","arguments":{"files":["pkg/a.go"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"check","arguments":{}}}`,
	)

	// Each tool call refreshes the index first, so every reply already
	// reads the pairs of the current workspace.
	for _, id := range []string{"2", "3", "4"} {
		text, isErr := toolText(t, resps[id])
		require.False(t, isErr, text)

		// The check verdict names the deleted file as a changed file,
		// which is true, so only its companion list is read.
		if id == "4" {
			start := strings.Index(text, report.CompanionsTitle)
			require.NotEqual(t, -1, start, text)
			text = text[start:]
		}

		assert.Contains(t, text, "pkg/live.go", "reply %s still names the live partner", id)
		assert.NotContains(t, text, "pkg/gone.go", "reply %s names no deleted partner", id)
		assert.NotContains(t, text, "pkg/moved.go", "reply %s names no renamed-away partner", id)
	}
}
