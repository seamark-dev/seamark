package mcp

import (
	"encoding/json"
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

func TestToolsKeepLivePartnersOfGoneFiles(t *testing.T) {
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
	// edit of pkg/a.go puts it in the workspace diff too.
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "gone.go")))
	git("mv", "pkg/moved.go", "pkg/renamed.go")
	write("pkg/a.go", "package pkg\n\nfunc a4() {}\n")

	// A diff that deletes only pkg/gone.go, as an agent sends it.
	cmd := exec.Command("git", "diff", "HEAD", "--", "pkg/gone.go")
	cmd.Dir = root
	deletion, err := cmd.Output()
	require.NoError(t, err)
	require.Contains(t, string(deletion), "deleted file mode")

	checkArgs, err := json.Marshal(map[string]string{"diff": string(deletion)})
	require.NoError(t, err)

	call := func(id int, name, args string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args)
	}

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		call(2, "why", `{"query":"pkg/gone.go"}`),
		call(3, "change_set", `{"files":["pkg/gone.go"]}`),
		call(4, "check", string(checkArgs)),
		call(5, "why", `{"query":"pkg/a.go"}`),
		call(6, "change_set", `{"files":["pkg/a.go"]}`),
		call(7, "check", `{}`),
	)

	// Each tool call refreshes the index first, so every reply already
	// reads the gone list of the current workspace. The deleted file
	// keeps its live partners, and no reply names a gone partner.
	for id, want := range map[string][]string{
		"2": {"pkg/a.go", "pkg/live.go"},
		"3": {"pkg/a.go", "pkg/live.go"},
		"4": {"pkg/a.go", "pkg/live.go"},
		"5": {"pkg/live.go"},
		"6": {"pkg/live.go"},
		"7": {"pkg/live.go"},
	} {
		text, isErr := toolText(t, resps[id])
		require.False(t, isErr, text)

		names := partnerNames(text)

		for _, f := range want {
			assert.Contains(t, names, f, "reply %s names the live partner %s\n%s", id, f, text)
		}

		assert.NotContains(t, names, "pkg/gone.go", "reply %s names no deleted partner", id)
		assert.NotContains(t, names, "pkg/moved.go", "reply %s names no renamed-away partner", id)
		assert.NotContains(t, text, "pkg/moved.go", "reply %s never names the renamed-away file", id)
	}

	// The deleted file has live partners, so check lists them and prints
	// no status line in place of the list.
	for _, id := range []string{"4", "7"} {
		text, _ := toolText(t, resps[id])

		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				assert.NotEqual(t, "companions", fields[0], "reply %s: %s", id, line)
			}
		}
	}
}

// partnerNames returns the partner paths of a tool reply. It reads the
// "usually changed with" section of why, where the path is the fifth
// field, and the closing companion list of change_set and check, where
// the path is the first field. Reason lines are indented further, so
// they are skipped.
func partnerNames(text string) []string {
	var names []string

	section := ""

	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "usually changed with"):
			section = "why"

			continue
		case strings.HasPrefix(line, report.CompanionsTitle):
			section = "companions"

			continue
		case !strings.HasPrefix(line, "  "):
			section = ""

			continue
		}

		fields := strings.Fields(line)

		switch {
		case section == "why" && len(fields) >= 5:
			names = append(names, fields[4])
		case section == "companions" && !strings.HasPrefix(line, "    ") && len(fields) > 0:
			names = append(names, fields[0])
		}
	}

	return names
}
