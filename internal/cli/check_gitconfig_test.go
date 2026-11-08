package cli

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
)

func TestCheckGitDiffHeadIgnoresDiffConfig(t *testing.T) {
	// The fallback of check runs git diff HEAD. A repository config that
	// changes the diff text must not change the decision. Without the fix,
	// color.ui=always and diff.external gave allow for a change that needs
	// approval. The JSON holds the decision alone, because the companion
	// hint reads git log and is not part of this fallback.
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

			out, err := exec.Command("git", "-C", root, "config", c.key, c.value).CombinedOutput()
			require.NoError(t, err, "%s", out)

			got, _, err := runIn(t, "", "-C", root, "check", "--json")
			require.NoError(t, err)
			assert.Equal(t, want, got)

			_, _, err = runIn(t, "", "-C", root, "check", "--enforce")
			require.ErrorIs(t, err, gate.ErrBlocked, "enforce blocks the change")
			assert.Equal(t, "blocked by policy: the change reaches file writes", err.Error())
		})
	}
}
