package cli

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

func TestCLIKeepsLivePartnersOfDeletedFile(t *testing.T) {
	// check falls back to git diff HEAD, so the user's git configuration
	// must not change the diff that it reads.
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

	// Three files change together in three commits. Commits on the
	// notes alone keep the lift of their pairs above one.
	for v := 1; v <= 3; v++ {
		for _, name := range []string{"a", "live", "gone"} {
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

	// The deletion is the whole diff, and it is not staged.
	require.NoError(t, os.Remove(filepath.Join(root, "pkg", "gone.go")))

	_, _, err := runIn(t, "", "-C", root, "index")
	require.NoError(t, err)

	why, _, err := runIn(t, "", "-C", root, "why", "pkg/gone.go")
	require.NoError(t, err)

	partners := whyPartnerLines(why)
	require.Len(t, partners, 2, why)
	assert.Contains(t, strings.Join(partners, "\n"), "pkg/a.go")
	assert.Contains(t, strings.Join(partners, "\n"), "pkg/live.go")

	// The deleted file is never a partner or a hub of a live file.
	whyA, _, err := runIn(t, "", "-C", root, "why", "pkg/a.go")
	require.NoError(t, err)
	assert.Contains(t, whyA, "pkg/live.go")
	assert.NotContains(t, whyA, "pkg/gone.go")

	orient, _, err := runIn(t, "", "-C", root, "orient")
	require.NoError(t, err)
	assert.Contains(t, orient, "pkg/live.go", "the live files are hubs")
	assert.NotContains(t, orient, "pkg/gone.go")

	// Zero-byte stdin makes check read git diff HEAD, which holds only
	// the deletion. The deleted file anchors its live partners.
	check, _, err := runIn(t, "", "-C", root, "check")
	require.NoError(t, err)

	start := strings.Index(check, report.CompanionsTitle)
	require.NotEqual(t, -1, start, check)
	assert.Contains(t, check[start:], "pkg/a.go")
	assert.Contains(t, check[start:], "pkg/live.go")

	for _, line := range strings.Split(check, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			assert.NotEqual(t, "companions", fields[0], "no status line replaces the list: %s", line)
		}
	}

	// A full refresh gives the same partners.
	_, _, err = runIn(t, "", "-C", root, "index", "--force")
	require.NoError(t, err)

	forced, _, err := runIn(t, "", "-C", root, "why", "pkg/gone.go")
	require.NoError(t, err)
	assert.Equal(t, partners, whyPartnerLines(forced))
}

// whyPartnerLines returns the lines of the "usually changed with"
// section of a why report, without the function names, because those
// come from a git call with its own time limit.
func whyPartnerLines(text string) []string {
	var lines []string

	in := false

	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "usually changed with"):
			in = true
		case in && strings.HasPrefix(line, "  "):
			entry, _, _ := strings.Cut(line, "  · ")
			lines = append(lines, strings.TrimSpace(entry))
		default:
			in = false
		}
	}

	return lines
}

func TestCLICheckListsLivePartnersOfRenamedAwayFiles(t *testing.T) {
	// The fallback runs git diff HEAD, so the user's git configuration
	// must not change the diff that it reads.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	cases := []struct {
		name string
		move []string // the git mv arguments
		from string   // a rename from line that the piped diff must hold
		want []string // the live partners that check lists
		note string   // a part of the unknown-reach note
		pin  bool     // a pin on pkg/orders must show in the advisory
	}{
		{
			name: "file rename",
			move: []string{"pkg/orders/repo.go", "pkg/orders/storage.go"},
			from: "rename from pkg/orders/repo.go",
			want: []string{"pkg/orders/service.go", "schema/order.py"},
			note: "2 of 2 changed files have changes outside any indexed symbol (pkg/orders/repo.go, pkg/orders/storage.go)",
		},
		{
			// The partners inside the directory move too, so only the
			// partner outside it stays.
			name: "directory rename",
			move: []string{"pkg/orders", "pkg/sales"},
			from: "rename from pkg/orders/service.go",
			want: []string{"schema/order.py"},
			note: "4 of 4 changed files have changes outside any indexed symbol (pkg/orders/repo.go, pkg/orders/service.go, ",
			pin:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := renameFixture(t)

			if c.pin {
				write := filepath.Join(root, ".seamark", "lessons.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(write), 0o755))
				require.NoError(t, os.WriteFile(write, []byte("pin:\n  - rule: orders-pin\n"+
					"    region: pkg/orders\n    note: Keep the orders schema in sync.\n"), 0o644))
			}

			gitIn(t, root, append([]string{"mv"}, c.move...)...)

			_, _, err := runIn(t, "", "-C", root, "index")
			require.NoError(t, err)

			cmd := exec.Command("git", "diff", "-M", "HEAD")
			cmd.Dir = root
			piped, err := cmd.Output()
			require.NoError(t, err)
			require.Contains(t, string(piped), c.from)

			// The piped rename record and the fallback with zero-byte
			// stdin give the same partners. Check --lessons prints the plain
			// check text through its own calls, so it runs too.
			for source, stdin := range map[string]string{"piped": string(piped), "fallback": ""} {
				for _, args := range [][]string{{"check"}, {"check", "--lessons"}} {
					run := source + " " + strings.Join(args, " ")

					out, _, err := runIn(t, stdin, append([]string{"-C", root}, args...)...)
					require.NoError(t, err, run)

					assert.Contains(t, out, c.note, run)

					partners, anchors := checkPartners(out)
					assert.ElementsMatch(t, c.want, partners, "%s\n%s", run, out)

					for _, a := range anchors {
						assert.True(t, strings.HasPrefix(a, "pkg/orders/"), "%s: %s anchors on a gone path\n%s", run, a, out)
					}

					for _, line := range strings.Split(out, "\n") {
						fields := strings.Fields(line)
						if len(fields) > 0 {
							assert.NotEqual(t, "companions", fields[0], "%s: no status line replaces the list: %s", run, line)
						}
					}

					if c.pin {
						_, advisory, found := strings.Cut(out, "advisory — recurring lessons for touched files")
						require.True(t, found, "%s: the old directory gives an advisory\n%s", run, out)
						assert.Contains(t, advisory, "orders-pin", "%s: the advisory names the pin of the old directory", run)
					}
				}
			}
		})
	}
}

// renameFixture builds a repository where pkg/orders/repo.go,
// pkg/orders/service.go and schema/order.py change together in three
// commits. Commits on the notes alone keep the lift of the pairs above one.
func renameFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	write := func(rel, content string) {
		t.Helper()

		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	gitIn(t, root, "init", "-q", "-b", "main")
	write("go.mod", "module example.com/shop\n")

	for v := 1; v <= 3; v++ {
		write("pkg/orders/repo.go", fmt.Sprintf("package orders\n\nfunc Repo%d() {}\n", v))
		write("pkg/orders/service.go", fmt.Sprintf("package orders\n\nfunc Service%d() {}\n", v))
		write("schema/order.py", fmt.Sprintf("def order_%d():\n    return %d\n", v, v))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", fmt.Sprintf("change %d", v))
	}

	for v := 1; v <= 3; v++ {
		write("docs/notes.md", fmt.Sprintf("notes %d\n", v))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", fmt.Sprintf("notes %d", v))
	}

	return root
}

// gitIn runs git in root with a fixed identity.
func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// checkPartners reads the companion list of a check report. It returns
// each partner path and the diff file that the partner line names after
// "with". Reason lines are indented further, so they are skipped.
func checkPartners(text string) (partners, anchors []string) {
	in := false

	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, report.CompanionsTitle):
			in = true

			continue
		case !strings.HasPrefix(line, "  "):
			in = false

			continue
		case !in || strings.HasPrefix(line, "    "):
			continue
		}

		fields := strings.Fields(line)
		partners = append(partners, fields[0])

		for i, f := range fields {
			if f == "with" && i+1 < len(fields) {
				anchors = append(anchors, strings.TrimSuffix(fields[i+1], ","))
			}
		}
	}

	return partners, anchors
}
