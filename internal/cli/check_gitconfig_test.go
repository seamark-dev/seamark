package cli

import (
	"os/exec"
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
