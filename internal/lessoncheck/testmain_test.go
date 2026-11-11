package lessoncheck

import (
	"fmt"
	"os"
	"testing"
)

// TestMain installs the Git environment that every test in this package
// needs, once before the tests run.
//
// The global and the system git configuration stay hidden from every git
// process of the tests, the producer's processes included: ReadWorkingTree
// passes os.Environ() to git, so a user setting such as core.fsmonitor or
// status.showUntrackedFiles could otherwise change a snapshot. The identity
// variables let a commit run in every temporary repository, also in a
// submodule clone without its own configuration. LC_ALL=C keeps the git
// messages in English, because some tests compare the stderr line of git
// status; a git with gettext translates them in another locale.
//
// These values are invariant for the whole package, so a test can call
// t.Parallel. A variable that only one test needs stays under that test's
// t.Setenv.
func TestMain(m *testing.M) {
	env := []struct{ key, value string }{
		{"LC_ALL", "C"},
		{"GIT_CONFIG_GLOBAL", os.DevNull},
		{"GIT_CONFIG_SYSTEM", os.DevNull},
		{"GIT_DEFAULT_HASH", "sha1"},
		{"GIT_AUTHOR_NAME", "Seamark Test"},
		{"GIT_AUTHOR_EMAIL", "test@seamark.invalid"},
		{"GIT_COMMITTER_NAME", "Seamark Test"},
		{"GIT_COMMITTER_EMAIL", "test@seamark.invalid"},
	}

	for _, e := range env {
		if err := os.Setenv(e.key, e.value); err != nil {
			fmt.Fprintf(os.Stderr, "lessoncheck: set %s: %v\n", e.key, err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}
