package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
)

func TestCheckGitDiffHeadIgnoresDiffConfig(t *testing.T) {
	// The fallback of check runs git diff HEAD, and the companion hint
	// reads git log. A repository config that changes the git text must
	// not change the decision or the text. Without the fix, color.ui=always
	// and diff.external gave allow for a change that needs approval, and
	// color.ui=always dropped the "mostly Load" line.
	cases := []struct{ key, value string }{
		{key: "color.ui", value: "always"},
		{key: "diff.external", value: "true"},
		{key: "diff.noprefix", value: "true"},
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			root := checkLegacyFixture(t)

			want, _, err := runIn(t, "", "-C", root, "check", "--json")
			require.NoError(t, err)
			require.Contains(t, want, `"verdict":"require_approval"`)

			wantText, _, err := runIn(t, "", "-C", root, "check")
			require.NoError(t, err)
			require.Contains(t, wantText, "    mostly Load\n")

			out, err := exec.Command("git", "-C", root, "config", c.key, c.value).CombinedOutput()
			require.NoError(t, err, "%s", out)

			got, _, err := runIn(t, "", "-C", root, "check", "--json")
			require.NoError(t, err)
			assert.Equal(t, want, got)

			gotText, _, err := runIn(t, "", "-C", root, "check")
			require.NoError(t, err)
			assert.Equal(t, wantText, gotText)

			_, _, err = runIn(t, "", "-C", root, "check", "--enforce")
			require.ErrorIs(t, err, gate.ErrBlocked, "enforce blocks the change")
			assert.Equal(t, "blocked by policy: the change reaches file writes", err.Error())
		})
	}
}

func TestCheckGitDiffHeadIgnoresRenameConfig(t *testing.T) {
	// The fallback reads a rename or a copy as one record with the lines
	// that differ. diff.renames=false turns a rename into a deletion and
	// an addition of every line, and diff.renames=copies turns a new
	// near-copy into such a record. Without the fix, false added fs:write
	// to a pure rename, and copies turned a blocked copy into an allow.
	// The empty user and system config leave the repository config as
	// the only setting.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	gitIn := func(t *testing.T, root string, args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	// base commits the fixture and keeps only the fs:write rule, so the
	// verdict depends on the effects alone.
	base := func(t *testing.T) string {
		t.Helper()

		root := checkLegacyFixture(t)
		policy := "mode: warn\nrequire_approval:\n" +
			"  - id: store-writes\n" +
			"    when: 'diff.effects.contains(\"fs:write\")'\n" +
			"    message: the change reaches file writes\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(policy), 0o644))
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", "Keep payloads private")

		return root
	}

	rename := func(t *testing.T) string {
		t.Helper()

		root := base(t)
		gitIn(t, root, "mv", "store/save.go", "store/write.go")

		return root
	}

	// nearCopy edits the doc comment above Save and stages a near copy of
	// save.go in another package. The doc comment is outside every
	// symbol, so that edit reaches no effect. As a new file, the copy adds
	// the body of Save, which reaches fs:write. As a copy record, it lists
	// only the comment lines that differ, which reach no effect.
	nearCopy := func(t *testing.T) string {
		t.Helper()

		root := base(t)
		save := filepath.Join(root, "store", "save.go")

		data, err := os.ReadFile(save)
		require.NoError(t, err)

		edited := strings.Replace(string(data), "// Save writes the payload.", "// Save stores the payload.", 1)
		require.NotEqual(t, string(data), edited, "the doc comment of Save")
		require.NoError(t, os.WriteFile(save, []byte(edited), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "archive"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "archive", "save.go"),
			[]byte(edited+"\n// Archive keeps old payloads.\n"), 0o644))
		gitIn(t, root, "add", "-A")

		return root
	}

	cases := []struct {
		name    string
		fixture func(t *testing.T) string
		value   string
		blocked bool
	}{
		{name: "rename diff.renames=false", fixture: rename, value: "false"},
		{name: "near-copy diff.renames=copies", fixture: nearCopy, value: "copies", blocked: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := c.fixture(t)

			plain := func() string {
				out, err := exec.Command("git", "-C", root, "diff", "HEAD").Output()
				require.NoError(t, err)

				return string(out)
			}

			want, _, err := runIn(t, "", "-C", root, "check", "--json")
			require.NoError(t, err)

			wantText, _, err := runIn(t, "", "-C", root, "check")
			require.NoError(t, err)

			_, _, wantEnforce := runIn(t, "", "-C", root, "check", "--enforce")
			if c.blocked {
				require.ErrorIs(t, wantEnforce, gate.ErrBlocked, "enforce blocks the copy")
			} else {
				require.NoError(t, wantEnforce)
			}

			before := plain()

			out, err := exec.Command("git", "-C", root, "config", "diff.renames", c.value).CombinedOutput()
			require.NoError(t, err, "%s", out)

			// The case is real only when it changes a plain git diff HEAD.
			after := plain()
			require.NotEqual(t, before, after, "plain git diff HEAD")

			// The changed text must also change the check result. Check
			// reads a piped diff as it is, so the piped text shows the
			// result that the config gives without the fixed rename option.
			beforeJSON, _, err := runIn(t, before, "-C", root, "check", "--json")
			require.NoError(t, err)

			afterJSON, _, err := runIn(t, after, "-C", root, "check", "--json")
			require.NoError(t, err)
			require.NotEqual(t, beforeJSON, afterJSON, "check of the plain git text")

			if c.blocked {
				_, _, err = runIn(t, after, "-C", root, "check", "--enforce")
				require.NoError(t, err, "the copy record alone allows the change")
			}

			got, _, err := runIn(t, "", "-C", root, "check", "--json")
			require.NoError(t, err)
			assert.Equal(t, want, got)

			gotText, _, err := runIn(t, "", "-C", root, "check")
			require.NoError(t, err)
			assert.Equal(t, wantText, gotText)

			_, _, gotEnforce := runIn(t, "", "-C", root, "check", "--enforce")
			assert.Equal(t, wantEnforce, gotEnforce)
		})
	}
}
