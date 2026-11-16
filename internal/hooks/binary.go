package hooks

// This file names the binary that a hook command runs: BinaryPath gives
// the path that setup writes, and the ownership rule reads it back. Both
// live here, so setup and every reader agree on that path.

import (
	"os"
	"path/filepath"
	"strings"
)

// executable returns the path of the running binary. Tests replace it
// to stand for a Homebrew install or for a binary with another name.
var executable = os.Executable

// BinaryPath returns the absolute path of the running seamark binary
// for hook commands. A Homebrew Cellar path maps to the stable opt path
// so an upgrade does not break installed hooks.
//
// The hooks invoke this exact install, whatever the PATH of the agent
// is. The path resolves symbolic links, so its basename can differ from
// "seamark". The ownership rule therefore also accepts this path. When
// the path cannot be resolved, the bare name is the fallback.
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

// ownsHookBinary reports whether the binary word of a hook command is
// seamark's: the basename rule of IsSeamarkBinary, or exactly the path
// that BinaryPath gives. Setup writes BinaryPath into every hook, and a
// renamed build or a resolved link gives it another basename. Without
// the second rule, setup, doctor, and status miss the hooks of such a
// binary, and each setup run adds a second copy.
//
// The second rule accepts only the path of the running binary. So the
// command of another program that ends with a seamark marker stays
// foreign. The path is computed only when the basename rule fails.
func ownsHookBinary(word string) bool {
	return IsSeamarkBinary(word) || word == BinaryPath()
}
