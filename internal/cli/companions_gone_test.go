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
