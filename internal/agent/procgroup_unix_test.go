//go:build unix

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pgidScript prints the process ID and the process group ID of the
// shell that runs it. ps is on every supported platform.
const pgidScript = `cat >/dev/null; echo "$$ $(ps -o pgid= -p $$)"`

// pidAndGroup parses the output of pgidScript.
func pidAndGroup(t *testing.T, out string) (pid, pgid int) {
	t.Helper()

	fields := strings.Fields(out)
	require.Len(t, fields, 2, "output %q", out)

	pid, err := strconv.Atoi(fields[0])
	require.NoError(t, err)

	pgid, err = strconv.Atoi(fields[1])
	require.NoError(t, err)

	return pid, pgid
}

func TestCommandKeepsTheCallerProcessGroup(t *testing.T) {
	// The commands rely on this: a terminal interrupt reaches the client
	// and its descendants only in the foreground process group.
	inv, err := NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", pgidScript}})
	require.NoError(t, err)

	out, err := inv.Invoke(context.Background(), "x")
	require.NoError(t, err)

	_, pgid := pidAndGroup(t, out)
	assert.Equal(t, syscall.Getpgrp(), pgid)
}

func TestGroupCommandLeadsANewProcessGroup(t *testing.T) {
	inv, err := NewGroupCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", pgidScript}})
	require.NoError(t, err)

	out, err := inv.Invoke(context.Background(), "x")
	require.NoError(t, err)

	pid, pgid := pidAndGroup(t, out)
	assert.Equal(t, pid, pgid, "the client leads its own group")
	assert.NotEqual(t, syscall.Getpgrp(), pgid)
}

// Review fix: a stop in the console killed only the direct client. A
// wrapper script or a launcher left its model call running.
func TestGroupCommandCancellationKillsTheDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")

	// The wrapper starts a descendant, records its ID, and waits for it,
	// like a launcher that forwards only catchable signals.
	script := `cat >/dev/null; sleep 60 & echo $! > "$1.tmp" && mv "$1.tmp" "$1"; wait`

	inv, err := NewGroupCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", script, "sh", pidFile}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	start := time.Now()

	go func() {
		_, err := inv.Invoke(ctx, "x")
		done <- err
	}()

	var descendant int

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}

		descendant, err = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "the wrapper starts its descendant")

	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(waitDelay):
		require.FailNow(t, "Invoke waits for waitDelay, so a descendant still holds the pipes")
	}

	assert.Less(t, time.Since(start), waitDelay)

	// The killed descendant is a zombie until init reaps it, and
	// signal 0 still finds a zombie. So the check waits for ESRCH.
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(descendant, 0), syscall.ESRCH)
	}, 5*time.Second, 20*time.Millisecond, "the descendant %d outlives the stop", descendant)
}

// Security review: exec cancels only while the client runs. A descendant
// that kept stdout open after the client exited ignored a stop, and its
// work went on.
func TestGroupCommandStopKillsADescendantAfterTheClientExits(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "mark")

	// The client replies and exits at once. Its descendant inherits
	// stdout, so Wait still waits for it.
	script := `cat >/dev/null; ( sleep 1; touch "$1/mark" ) & echo $$ > "$1/leader"; echo '{"triggers":[]}'`

	inv, err := NewGroupCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", script, "sh", dir}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		out string
		err error
	}

	done := make(chan result, 1)

	go func() {
		out, err := inv.Invoke(ctx, "x")
		done <- result{out, err}
	}()

	var leader int

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "leader"))
		if err != nil {
			return false
		}

		leader, err = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil && errors.Is(syscall.Kill(leader, 0), syscall.ESRCH)
	}, 5*time.Second, 10*time.Millisecond, "the client exits")

	start := time.Now()

	cancel()

	select {
	case r := <-done:
		require.NoError(t, r.err, "the client exited before the stop, so its reply stands")
		assert.Contains(t, r.out, `{"triggers":[]}`)
	case <-time.After(waitDelay):
		require.FailNow(t, "the stop did not end the descendant that holds stdout")
	}

	assert.Less(t, time.Since(start), time.Second, "the stop ends the run before the descendant would finish")

	time.Sleep(1500 * time.Millisecond)
	assert.NoFileExists(t, mark, "the descendant outlived the stop")
}

// Security review: a descendant that closed its pipes never delayed
// Wait, so it went on after a call that succeeded.
func TestGroupCommandEndsADetachedDescendantAfterTheCall(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "mark")

	script := `cat >/dev/null; ( sleep 1; touch "$1/mark" ) >/dev/null 2>&1 </dev/null & echo '{"triggers":[]}'`

	inv, err := NewGroupCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", script, "sh", dir}})
	require.NoError(t, err)

	out, err := inv.Invoke(context.Background(), "x")
	require.NoError(t, err)
	assert.Contains(t, out, `{"triggers":[]}`)

	time.Sleep(1500 * time.Millisecond)
	assert.NoFileExists(t, mark, "the detached descendant outlived the call")
}

func TestCommandKeepsADetachedDescendantOfTheCaller(t *testing.T) {
	// NewCommand shares the process group of the caller, so it never
	// kills a group. The CLI uses it, so a terminal interrupt reaches the
	// agent (internal/learning/doc.go names this difference).
	dir := t.TempDir()
	mark := filepath.Join(dir, "mark")

	script := `cat >/dev/null; ( sleep 0.2; touch "$1/mark" ) >/dev/null 2>&1 </dev/null & echo ok`

	inv, err := NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", script, "sh", dir}})
	require.NoError(t, err)

	_, err = inv.Invoke(context.Background(), "x")
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		_, err := os.Stat(mark)

		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
}
