package history

import (
	"os"
	"strconv"
)

// gitRefreshConfigKey is the git setting that stops `git diff` from
// refreshing the stat cache in .git/index. GIT_OPTIONAL_LOCKS does not
// stop that refresh, because git diff writes the index without the
// optional-lock check.
const gitRefreshConfigKey = "diff.autoRefreshIndex"

// ReadOnlyGitEnv returns the environment changes that keep git from
// rewriting .git/index while seamark reads the workspace.
//
// The index freshness check, the check fallback, the console, and the LSP
// server run `git status` and `git diff HEAD`. After a change to file
// metadata only, such as `touch`, both commands refresh the stat cache of
// .git/index and take .git/index.lock. The lock can make a concurrent git
// command of the user fail, for example `git add`.
//
// GIT_OPTIONAL_LOCKS=0 stops the write of `git status`. The
// diff.autoRefreshIndex=false setting stops the write of `git diff`. Each
// command needs its own part, because each part has no effect on the
// other command. Neither part changes the command output, so the
// freshness fingerprint stays the same. Both parts affect only the
// optional stat-cache write, so the git commands of an index build work
// as before.
//
// The setting goes in through GIT_CONFIG_COUNT, the git mechanism for
// configuration from the environment. The function appends to entries
// that already exist. It adds nothing when the count is not a number,
// because git then refuses every command and a new entry cannot help.
func ReadOnlyGitEnv(lookup func(string) (string, bool)) [][2]string {
	env := [][2]string{{"GIT_OPTIONAL_LOCKS", "0"}}

	n := 0

	if v, ok := lookup("GIT_CONFIG_COUNT"); ok && v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			return env
		}

		n = parsed
	}

	i := strconv.Itoa(n)

	return append(env,
		[2]string{"GIT_CONFIG_KEY_" + i, gitRefreshConfigKey},
		[2]string{"GIT_CONFIG_VALUE_" + i, "false"},
		[2]string{"GIT_CONFIG_COUNT", strconv.Itoa(n + 1)})
}

// ReadOnlyGitEnviron returns the process environment plus the
// ReadOnlyGitEnv entries, for the Env field of one git command. A later
// entry wins in os/exec, so the appended entries replace a value that the
// process environment holds.
func ReadOnlyGitEnviron() []string {
	env := os.Environ()

	for _, kv := range ReadOnlyGitEnv(os.LookupEnv) {
		env = append(env, kv[0]+"="+kv[1])
	}

	return env
}
