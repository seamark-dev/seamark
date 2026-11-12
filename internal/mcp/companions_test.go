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

		// The check of the workspace diff reads pkg/moved.go as a diff
		// file, so it can name the path in its note and after "with".
		if id != "7" {
			assert.NotContains(t, text, "pkg/moved.go", "reply %s never names the renamed-away file", id)
		}
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

func TestCheckListsLivePartnersOfRenamedAwayFiles(t *testing.T) {
	// The server runs git itself, so the user's git configuration must
	// not change the diff that check reads.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	cases := []struct {
		name string
		move []string // the git mv arguments
		from string   // a rename from line that the piped diff must hold
		want []string // the live partners that check lists
		pin  bool     // a pin on pkg/orders must show in the advisory
	}{
		{
			name: "file rename",
			move: []string{"pkg/orders/repo.go", "pkg/orders/storage.go"},
			from: "rename from pkg/orders/repo.go",
			want: []string{"pkg/orders/service.go", "schema/order.py"},
		},
		{
			// The partners inside the directory move too, so only the
			// partner outside it stays. No live file is under pkg/orders,
			// so only the old paths give the pin of that directory.
			name: "directory rename",
			move: []string{"pkg/orders", "pkg/sales"},
			from: "rename from pkg/orders/service.go",
			want: []string{"schema/order.py"},
			pin:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()

			// Three files change together in three commits. Commits on
			// the notes alone keep the lift of their pairs above one.
			fixtureGit(t, root, "init", "-q", "-b", "main")
			fixtureWrite(t, root, "go.mod", "module example.com/shop\n")

			for v := 1; v <= 3; v++ {
				fixtureWrite(t, root, "pkg/orders/repo.go", fmt.Sprintf("package orders\n\nfunc Repo%d() {}\n", v))
				fixtureWrite(t, root, "pkg/orders/service.go", fmt.Sprintf("package orders\n\nfunc Service%d() {}\n", v))
				fixtureWrite(t, root, "schema/order.py", fmt.Sprintf("def order_%d():\n    return %d\n", v, v))
				fixtureGit(t, root, "add", "-A")
				fixtureGit(t, root, "commit", "-q", "-m", fmt.Sprintf("change %d", v))
			}

			for v := 1; v <= 3; v++ {
				fixtureWrite(t, root, "docs/notes.md", fmt.Sprintf("notes %d\n", v))
				fixtureGit(t, root, "add", "-A")
				fixtureGit(t, root, "commit", "-q", "-m", fmt.Sprintf("notes %d", v))
			}

			if c.pin {
				fixtureWrite(t, root, ".seamark/lessons.yaml", "pin:\n  - rule: orders-pin\n"+
					"    region: pkg/orders\n    note: Keep the orders schema in sync.\n")
			}

			fixtureGit(t, root, append([]string{"mv"}, c.move...)...)

			piped := fixtureGit(t, root, "diff", "-M", "HEAD")
			require.Contains(t, piped, c.from)

			checkArgs, err := json.Marshal(map[string]string{"diff": piped})
			require.NoError(t, err)

			call := func(id int, args string) string {
				return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"check","arguments":%s}}`, id, args)
			}

			resps := serve(t, root,
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				call(2, string(checkArgs)),
				call(3, `{}`),
			)

			// Reply 2 reads the piped rename records, reply 3 the fallback.
			for _, id := range []string{"2", "3"} {
				text, isErr := toolText(t, resps[id])
				require.False(t, isErr, text)

				assert.ElementsMatch(t, c.want, partnerNames(text), "reply %s\n%s", id, text)

				for _, line := range strings.Split(text, "\n") {
					fields := strings.Fields(line)
					if len(fields) > 0 {
						assert.NotEqual(t, "companions", fields[0], "reply %s: %s", id, line)
					}
				}

				if c.pin {
					_, advisory, found := strings.Cut(text, "advisory — recurring lessons for touched files")
					require.True(t, found, "reply %s: the old directory gives an advisory\n%s", id, text)
					assert.Contains(t, advisory, "orders-pin", "reply %s: the advisory names the pin of the old directory", id)
				}
			}
		})
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
