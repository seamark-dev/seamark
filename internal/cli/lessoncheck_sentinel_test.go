package cli

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file holds the AC-1 sentinel of the lesson-check surfaces (plan
// conventions). A test of a surface that must call no model runs the
// surface under the sentinel and ends with requireNoCall. Later
// milestones reuse the helper for every new CLI surface.
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

// sentinelExit is the exit code of every stub. No seamark path exits
// with it, so a stub that runs makes the surface fail visibly.
const sentinelExit = 97

// invokerSentinel records every start of an agent or of a tool that can
// call a model.
type invokerSentinel struct {
	marker string // each stub appends its name here
}

// newInvokerSentinel installs the sentinel for root and for the rest of
// the test. It adds an agent section to .seamark/config.yaml and keeps
// the other content. A test that writes config.yaml writes it before
// this call, because a later write removes the agent section. The test
// must not run in parallel, because the sentinel sets environment
// variables.
func newInvokerSentinel(t *testing.T, root string) *invokerSentinel {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the sentinel stubs are shell scripts")
	}

	// The stubs and the marker live outside root, so they never appear
	// as untracked files in a working-tree snapshot of root.
	dir := t.TempDir()
	s := &invokerSentinel{marker: filepath.Join(dir, "calls.txt")}

	agentStub := filepath.Join(dir, "agent.sh")
	s.writeStub(t, agentStub, "agent.argv")
	sentinelAgentConfig(t, root, agentStub)

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
