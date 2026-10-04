package gate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecideFollowsThePolicyMode(t *testing.T) {
	root := t.TempDir()

	// The embedded default policy: warn mode, force-push to main denied.
	d, err := Decide(CommandRequest{Root: root, Command: "git push --force origin main"}, nil)
	require.NoError(t, err)
	assert.Equal(t, VerdictDeny, d.Verdict)
	assert.Equal(t, "warn", d.Mode)
	assert.False(t, d.Blocking(), "warn reports and never blocks")

	d, err = Decide(CommandRequest{Root: root, Command: "git push --force origin main", Enforce: true}, nil)
	require.NoError(t, err)
	assert.True(t, d.Blocking(), "--enforce overrides the policy mode")

	d, err = Decide(CommandRequest{Root: root, Command: "ls -la", Enforce: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, VerdictAllow, d.Verdict)
	assert.False(t, d.Blocking())

	// A decision is audited.
	assert.FileExists(t, filepath.Join(root, ".seamark", "audit.jsonl"))
}

func TestDecideFailsClosedOnlyUnderEnforcement(t *testing.T) {
	root := t.TempDir()

	// An empty command and a malformed shell line are the gate's own
	// failures. Under explicit enforcement they block; under warn they
	// are plain errors.
	for name, command := range map[string]string{"empty": "  ", "unparseable": "echo 'unterminated"} {
		_, err := Decide(CommandRequest{Root: root, Command: command, Enforce: true}, nil)
		assert.ErrorIs(t, err, ErrBlocked, name)

		_, err = Decide(CommandRequest{Root: root, Command: command}, nil)
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, ErrBlocked, name)
	}

	// A broken policy under explicit enforcement blocks too.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: [broken\n"), 0o644))

	_, err := Decide(CommandRequest{Root: root, Command: "ls", Enforce: true}, nil)
	assert.ErrorIs(t, err, ErrBlocked)

	_, err = Decide(CommandRequest{Root: root, Command: "ls"}, nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBlocked, "a broken policy without enforcement is reported, not blocking")
}

func TestDecideEnforcesThePolicyFileMode(t *testing.T) {
	// An enforce policy makes a failure after the policy load block,
	// with no --enforce on the command line: the policy file is the
	// source of truth once it loads.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: enforce\n"), 0o644))

	_, err := Decide(CommandRequest{Root: root, Command: "echo 'unterminated"}, nil)
	assert.ErrorIs(t, err, ErrBlocked)

	d, err := Decide(CommandRequest{Root: root, Command: "ls"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "enforce", d.Mode)
}

func TestDecideReportsAFailedAuditAndKeepsTheDecision(t *testing.T) {
	// .seamark linked elsewhere: the policy still loads through the
	// link, and the audit refuses to write through it.
	root := t.TempDir()
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".seamark")))

	var reported error

	d, err := Decide(CommandRequest{Root: root, Command: "ls"}, func(err error) { reported = err })
	require.NoError(t, err)
	assert.Equal(t, VerdictAllow, d.Verdict)
	require.Error(t, reported, "the audit failure reaches the caller")

	// A nil reporter is allowed.
	_, err = Decide(CommandRequest{Root: root, Command: "ls"}, nil)
	require.NoError(t, err)
}

func TestBlockedKeepsTheCauseAndMatchesErrBlocked(t *testing.T) {
	cause := errors.New("empty command")
	err := FailClosed(cause, true)

	assert.ErrorIs(t, err, ErrBlocked)
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "blocked by policy: empty command", err.Error())

	var blocked *Blocked
	require.ErrorAs(t, err, &blocked)
	assert.Same(t, cause, blocked.Cause)

	assert.Same(t, cause, FailClosed(cause, false), "without enforcement the error is returned as it is")
	assert.Equal(t, "blocked by policy", (&Blocked{}).Error())
}
