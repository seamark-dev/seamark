package cli

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the invoker sentinel of the lesson-check surfaces. A
// test of a surface that must call no model runs the surface under the
// sentinel and ends with requireNoCall. A new CLI surface reuses the
// helper.
//
// The sentinel has three parts:
//   - agent.argv in .seamark/config.yaml names a stub script;
//   - stubs named claude, codex, and gh come first on PATH;
//   - the proxy variables name a closed local port.
//
// Each stub appends its name to one marker file and exits with
// sentinelExit. git stays on PATH, because the surfaces read the
// repository through git. The proxy part is no proof inside the test
// process, because Go caches the proxy settings after the first read.
// It catches a child process that reaches the network.
//
// newInvokerEnvironment sets the process environment and the stubs for
// one serial test; install writes only the agent argv into one root. An
// exit table whose cases run in parallel creates one environment at its
// serial parent and checks the marker in the parent's cleanup, after
// every child has finished.

// sentinelExit is the exit code of every stub. No seamark path exits
// with it, so a stub that runs makes the surface fail visibly.
const sentinelExit = 97

// invokerSentinel records every start of an agent or of a tool that can
// call a model.
type invokerSentinel struct {
	marker    string // each stub appends its name here
	agentStub string // the agent.argv target that install writes to a root
}

// newInvokerEnvironment prepares the sentinel for the rest of the test:
// a private marker and stub directory, the agent stub, the PATH stubs,
// and the proxy settings. It changes no repository; install adds the
// agent section to one root. The test that owns this environment must
// not run in parallel, because the environment variables are process
// wide. A parallel child of that test may call install, which writes
// only a root-local file.
func newInvokerEnvironment(t *testing.T) *invokerSentinel {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the sentinel stubs are shell scripts")
	}

	// The stubs and the marker live outside a root, so they never appear
	// as untracked files in a working-tree snapshot of that root.
	dir := t.TempDir()
	s := &invokerSentinel{
		marker:    filepath.Join(dir, "calls.txt"),
		agentStub: filepath.Join(dir, "agent.sh"),
	}

	s.writeStub(t, s.agentStub, "agent.argv")

	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))

	for _, name := range []string{"claude", "codex", "gh"} {
		s.writeStub(t, filepath.Join(bin, name), name)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	proxy := closedLocalURL(t)
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy)
	}

	// A NO_PROXY entry would let a request pass the proxy.
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}

	return s
}

// install writes this sentinel's agent argv into the fixture config of
// root and keeps the other content. It never sets an environment
// variable, so a parallel child may call it for its own root. A test
// that writes config.yaml writes it before this call, because a later
// write removes the agent section.
func (s *invokerSentinel) install(t *testing.T, root string) {
	t.Helper()

	sentinelAgentConfig(t, root, s.agentStub)
}

// newInvokerSentinel is the serial convenience operation: it prepares
// the environment and installs the agent section in root. A test that
// runs no parallel child uses it.
func newInvokerSentinel(t *testing.T, root string) *invokerSentinel {
	t.Helper()

	s := newInvokerEnvironment(t)
	s.install(t, root)

	return s
}

// newInvokerEnvironmentForTable prepares the shared sentinel of an exit
// table whose cases run in parallel. On Windows the stubs are shell
// scripts, so it returns nil and only the sentinel-dependent cases skip;
// a case that never uses the sentinel still runs.
func newInvokerEnvironmentForTable(t *testing.T) *invokerSentinel {
	t.Helper()

	if runtime.GOOS == "windows" {
		return nil
	}

	return newInvokerEnvironment(t)
}

// requireNoCall fails the test when any stub ran.
func (s *invokerSentinel) requireNoCall(t *testing.T) {
	t.Helper()

	data, err := os.ReadFile(s.marker)
	if errors.Is(err, os.ErrNotExist) {
		return
	}

	require.NoError(t, err)
	require.Empty(t, string(data), "a model or network stub ran; the marker names it")
}

// writeStub writes one stub script. It appends name to the marker and
// exits with sentinelExit.
func (s *invokerSentinel) writeStub(t *testing.T, path, name string) {
	t.Helper()

	body := "#!/bin/sh\necho " + maintainShellQuote(name) + " >> " + maintainShellQuote(s.marker) + "\n" +
		"exit " + strconv.Itoa(sentinelExit) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

// agentSection finds a top-level agent key in config.yaml.
var agentSection = regexp.MustCompile(`(?m)^agent:`)

// sentinelAgentConfig appends an agent section with argv = [sh, stub]
// to .seamark/config.yaml. A second agent key would make the YAML
// invalid, so a config that already has one stops the test.
func sentinelAgentConfig(t *testing.T, root, stub string) {
	t.Helper()

	path := filepath.Join(root, ".seamark", "config.yaml")

	data, err := os.ReadFile(path)
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
	}

	content := string(data)
	require.False(t, agentSection.MatchString(content), "the sentinel owns the agent section of config.yaml")

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	content += "agent:\n  argv: [\"sh\", " + strconv.Quote(stub) + "]\n"

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// closedLocalURL returns an http URL of a local port that no process
// listens on. A connection to it fails at once.
func closedLocalURL(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return "http://" + addr
}

// sentinelProbeEnv makes TestInvokerSentinelLateCallProbe run as its own
// child process, where it fails on purpose. sentinelProbeSharedEnv names
// the directory in which that child proves that its normal child ran.
const (
	sentinelProbeEnv       = "SEAMARK_TEST_SENTINEL_PROBE"
	sentinelProbeSharedEnv = "SEAMARK_TEST_SENTINEL_PROBE_SHARED"
)

// requireSentinelExit runs one stub and requires its exit code.
func requireSentinelExit(t *testing.T, name string, args ...string) {
	t.Helper()

	cmd := exec.Command(name, args...)

	var exitErr *exec.ExitError

	require.ErrorAs(t, cmd.Run(), &exitErr, "%s is a stub that exits non-zero", name)
	require.Equal(t, sentinelExit, exitErr.ExitCode(), "%s exits with sentinelExit", name)
}

// TestInvokerSentinelStubsTrap proves both traps of the sentinel: a bare
// PATH stub, and the agent stub that a config.yaml agent.argv names,
// each append their name to the marker and exit with sentinelExit. The
// test expects the calls, so it does not end with requireNoCall.
func TestInvokerSentinelStubsTrap(t *testing.T) {
	sentinel := newInvokerEnvironment(t)

	for _, name := range []string{"claude", "codex", "gh"} {
		requireSentinelExit(t, name)
	}

	// install writes argv = ["sh", agentStub], so the agent runs this way.
	requireSentinelExit(t, "sh", sentinel.agentStub)

	data, err := os.ReadFile(sentinel.marker)
	require.NoError(t, err)
	assert.Equal(t, "claude\ncodex\ngh\nagent.argv\n", string(data), "each stub names itself once")
}

// TestInvokerSentinelLateCallProbe is the intentional failure of the
// regression pair below. Its late parallel child calls a stub, so the
// parent cleanup fails although every child passes its own assertions.
// The probe runs only in the child process that the pair starts; in the
// normal suite it skips, so the suite stays green.
func TestInvokerSentinelLateCallProbe(t *testing.T) {
	shared, ok := os.LookupEnv(sentinelProbeSharedEnv)
	if !ok {
		t.Skip("the probe fails on purpose and runs only in its own child process")
	}

	sentinel := newInvokerEnvironment(t)
	t.Cleanup(func() { sentinel.requireNoCall(t) })

	// The children share the directory of the parent process. The quick
	// child writes its proof last, so the file exists only when every
	// assertion of the normal child passed.
	quickDone := filepath.Join(shared, "quick.txt")

	t.Run("quick child", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, os.WriteFile(quickDone, []byte("done\n"), 0o644))
		require.FileExists(t, quickDone, "the normal child ran")
	})

	t.Run("late child", func(t *testing.T) {
		t.Parallel()

		// Wait for the quick child instead of sleeping a fixed time:
		// the proof must hold under a loaded or race-instrumented run.
		// This call then lands while the parent already waits for its
		// parallel children.
		require.Eventually(t, func() bool {
			_, err := os.Stat(quickDone)
			return err == nil
		}, 10*time.Second, 10*time.Millisecond, "the late child finishes last")

		requireSentinelExit(t, "claude")
	})
}

// TestInvokerSentinelParentCleanupCatchesLateCall proves the guard of
// the shared sentinel: a stub that a late-finishing parallel child runs
// fails the serial parent cleanup, after every child has finished. The
// probe fails on purpose in its own process, so this test and the suite
// stay green.
func TestInvokerSentinelParentCleanupCatchesLateCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel stubs are shell scripts")
	}

	exe, err := os.Executable()
	require.NoError(t, err)

	shared := t.TempDir()

	cmd := exec.Command(exe, "-test.run=^TestInvokerSentinelLateCallProbe$", "-test.v")
	cmd.Env = append(os.Environ(), sentinelProbeEnv+"=1", sentinelProbeSharedEnv+"="+shared)

	out, err := cmd.CombinedOutput()
	text := string(out)

	var exitErr *exec.ExitError

	require.ErrorAs(t, err, &exitErr, "the parent cleanup must fail the probe:\n%s", text)
	assert.NotZero(t, exitErr.ExitCode(), "the probe process fails")

	assert.FileExists(t, filepath.Join(shared, "quick.txt"), "the normal child stays green")
	assert.Contains(t, text, "Should be empty, but was claude", "the marker names the late call")
	// requireNoCall runs only in the serial parent's cleanup, so this
	// message proves the failure came from there after every child ended.
	assert.Contains(t, text, "a model or network stub ran", "the parent cleanup fails the table")
}
