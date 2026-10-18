// Package scaffold holds the client-independent setup documents and the
// setup request assembly that `seamark init` and the console share. The
// code moved here from internal/cli so both surfaces plan the same
// files; the CLI keeps its own narration.
package scaffold

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
)

// Documents returns the client-independent setup documents: the starter
// policy, lessons and config files, and the .gitignore carve-outs.
// gateMode selects the starter policy mode line.
//
// The setup coordinator guards and writes these documents with the
// client documents, so one preflight covers every write. Narrate stays
// nil, because each surface describes an operation in its own words.
func Documents(gateMode string) []integration.Document {
	var docs []integration.Document

	for _, f := range []struct {
		rel, body string
	}{
		{".seamark/policy.yaml", StarterPolicy(gateMode)},
		{".seamark/lessons.yaml", starterLessons},
		{".seamark/config.yaml", starterConfig},
	} {
		docs = append(docs, integration.Document{
			Path:       f.rel,
			CreateOnly: true,
			Detail:     "starter",
			KeptDetail: "already present",
			// A scaffold is a starter. Setup never replaces an existing file.
			Compose: func(existing []byte, exists bool) ([]byte, error) {
				if exists {
					return existing, nil
				}

				return []byte(f.body), nil
			},
		})
	}

	return append(docs, integration.Document{
		Path:       ".gitignore",
		Detail:     "seamark carve-outs",
		KeptDetail: "seamark carve-outs already present",
		Compose:    composeGitignore,
	})
}

// ResolveGateMode picks the gate mode for a setup run: the requested
// mode, else the installed managed mode, else warn. An empty request
// keeps what a previous run installed, because setup must never add or
// remove enforcement implicitly.
func ResolveGateMode(installed, requested string) string {
	if requested != "" {
		return requested
	}

	if installed != "" {
		return installed
	}

	return hooks.ModeWarn
}

// Request assembles the setup request that init and the console plan.
// requestedGate is "" to keep the installed gate mode. It returns the
// request and the gate mode that the starter policy uses.
//
// Only the clients that get a gate hook in this run supply the
// installed mode. A client without a gate hook cannot keep a mode.
func Request(reg *integration.Registry, root, binary string,
	setups []integration.ClientSetup, requestedGate string) (req integration.SetupRequest, gateMode string) {
	hookClients := integration.GateHookClients(reg, setups)
	gateMode = ResolveGateMode(integration.InstalledGateMode(reg, root, hookClients), requestedGate)

	req = integration.SetupRequest{
		Root: root, Binary: binary, Clients: setups, Common: Documents(gateMode),
	}

	return req, gateMode
}

// executable returns the path of the running binary. Tests replace it
// to check the Homebrew mapping without a real Homebrew install.
var executable = os.Executable

// BinaryPath returns the absolute path of the running seamark binary
// for hook commands. A Homebrew Cellar path maps to the stable opt path
// so an upgrade does not break installed hooks.
//
// The hooks invoke this exact install, whatever the PATH of the agent
// is. When the path cannot be resolved, the bare name is the fallback.
func BinaryPath() string {
	exe, err := executable()
	if err != nil {
		return "seamark"
	}

	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	return stableInstallPath(exe)
}

// stableInstallPath maps a versioned Homebrew Cellar path to the keg's
// stable opt symlink. Homebrew deletes the old Cellar directory on every
// upgrade, so hooks that keep it break. The opt symlink survives
// upgrades. Every other path returns unchanged.
func stableInstallPath(path string) string {
	prefix, rest, found := strings.Cut(path, "/Cellar/seamark/")
	if !found {
		return path
	}

	// The remainder must be exactly <version>/bin/seamark. A different
	// layout is not a Homebrew keg of this binary.
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "bin" || parts[2] != "seamark" {
		return path
	}

	// Substitute only when the opt symlink really exists, so a broken
	// or partial install keeps the path that is known to work.
	opt := filepath.Join(prefix, "opt", "seamark", "bin", "seamark")
	if _, err := os.Stat(opt); err != nil {
		return path
	}

	return opt
}
