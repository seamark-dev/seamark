package hooks

// This file names the binary that a hook command runs: BinaryPath gives
// the path that setup writes, and the ownership rule and the shell reader
// read it back. They live here, so setup and every reader agree on that
// path.

import (
	"os"
	"path/filepath"
	"slices"
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
// "seamark". The ownership rule and the shell reader therefore also
// accept this path. When the path cannot be resolved, the bare name is
// the fallback.
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

// hookBinary is the running binary as the shell reader compares it. The
// reader reads a wrapper, which ownership leaves alone. It finds the
// path once per reading, because a crafted hook command can hold many
// words, and each lookup reads the file system.
type hookBinary struct {
	// path is the path that BinaryPath gives. Empty means no path.
	path string
	// words is path as looseWords splits it. The loose split cuts a path
	// at each blank, so a path with a blank gives more than one word.
	words []string
}

// newHookBinary returns the hookBinary for a path that BinaryPath gives.
func newHookBinary(path string) hookBinary {
	return hookBinary{path: path, words: looseWords(path)}
}

// names reports whether one parsed word names the seamark binary: by the
// basename rule, or as the path of the running binary. It is the rule of
// ownsHookBinary, with the path found in advance.
func (b hookBinary) names(word string) bool {
	return IsSeamarkBinary(word) || (b.path != "" && word == b.path)
}

// looseAt returns how many loose words at the start of words name the
// seamark binary, or zero. A word with the basename "seamark" is one
// word. The running binary is as many words as its loose split gives.
func (b hookBinary) looseAt(words []string) int {
	switch {
	case len(words) > 0 && IsSeamarkBinary(words[0]):
		return 1
	case len(b.words) > 0 && len(words) >= len(b.words) && slices.Equal(words[:len(b.words)], b.words):
		return len(b.words)
	default:
		return 0
	}
}

// looseIn reports whether the loose words name the seamark binary
// anywhere in them.
func (b hookBinary) looseIn(words []string) bool {
	for i := range words {
		if b.looseAt(words[i:]) > 0 {
			return true
		}
	}

	return false
}
