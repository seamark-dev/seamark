//go:build unix

package main

import (
	"io"
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

func TestInterruptKillsTheOwnedProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv("TESTSUMMARY_DESCENDANT_FILE", pidFile)

	signals := make(chan os.Signal, 1)
	type result struct {
		res outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := execute(invocation{
			args:    []string{"./..."},
			logDir:  t.TempDir(),
			slow:    3,
			grace:   200 * time.Millisecond,
			stdout:  io.Discard,
			signals: signals,
			command: helperCommand("interrupt"),
			env:     helperEnv("interrupt"),
		})
		done <- result{res, err}
	}()

	descendant := waitForPID(t, pidFile)
	signals <- os.Interrupt

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not return after interruption")
	}
	require.NoError(t, got.err)
	assert.Equal(t, 128+int(syscall.SIGINT), got.res.ExitCode, "interruption is never a pass")
	require.NotNil(t, got.res.Summary)
	assert.Equal(t, "interrupted", got.res.Summary.Status)
	assert.FileExists(t, filepath.Join(got.res.RunDir, "events.jsonl"), "partial artifacts survive")

	require.Eventually(t, func() bool {
		return syscall.Kill(descendant, 0) != nil
	}, 10*time.Second, 50*time.Millisecond, "the descendant process must be reaped")
}

func TestCooperativeDescendantGetsTheRemainingGrace(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	cleanFile := filepath.Join(t.TempDir(), "clean.txt")
	t.Setenv("TESTSUMMARY_DESCENDANT_FILE", pidFile)
	t.Setenv("TESTSUMMARY_CLEAN_FILE", cleanFile)

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = execute(invocation{
			args:    []string{"./..."},
			logDir:  t.TempDir(),
			slow:    3,
			grace:   5 * time.Second,
			stdout:  io.Discard,
			signals: signals,
			command: helperCommand("cooperative"),
			env:     helperEnv("cooperative"),
		})
	}()

	_ = waitForPID(t, pidFile)
	signals <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not return after the signal")
	}

	require.FileExists(t, cleanFile,
		"a descendant handling SIGTERM must get the remaining grace to finish")
}

func TestSignalKillsAStubbornDescendantAfterTheLeaderExits(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv("TESTSUMMARY_DESCENDANT_FILE", pidFile)

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = execute(invocation{
			args:    []string{"./..."},
			logDir:  t.TempDir(),
			slow:    3,
			grace:   200 * time.Millisecond,
			stdout:  io.Discard,
			signals: signals,
			command: helperCommand("stubborn"),
			env:     helperEnv("stubborn"),
		})
	}()

	descendant := waitForPID(t, pidFile)
	signals <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runner did not return after the signal")
	}

	require.Eventually(t, func() bool {
		return syscall.Kill(descendant, 0) != nil
	}, 10*time.Second, 50*time.Millisecond,
		"a descendant that ignored SIGTERM must be killed after the leader exits")
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return false
		}
		pid = n
		return true
	}, 10*time.Second, 20*time.Millisecond, "helper never recorded its descendant")
	return pid
}
