package cli

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// This file holds test helpers that tests of several commands share.

// stored reads one proposal row in any status.
func stored(t *testing.T, root string, id int64) model.Proposal {
	t.Helper()

	st, err := store.OpenReadOnly(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	rows, err := st.ProposalsWithIDs([]int64{id})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// gitRoot returns Git's view of the worktree root, which resolves
// symbolic links in the temporary path.
func gitRoot(t *testing.T, dir string) string {
	t.Helper()

	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)

	return string(out[:len(out)-1])
}

// runMCP serves one MCP request script through `seamark mcp` with the
// global flags, for example -C and --db, and returns the raw responses.
func runMCP(t *testing.T, flags []string, requests ...string) string {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(strings.NewReader(strings.Join(requests, "\n") + "\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append(append([]string{}, flags...), "mcp"))
	require.NoError(t, cmd.Execute(), "stderr: %s", errOut.String())

	return out.String() + errOut.String()
}

// requireExitOne asserts a refusal that Execute maps to exit code 1,
// never 2. Exit code 2 means a blocked gate or a lesson violation, and a
// refusal is neither.
func requireExitOne(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.False(t, errors.Is(err, gate.ErrBlocked) || errors.Is(err, lessoncheck.ErrViolation),
		"a refusal never exits with 2")
}
