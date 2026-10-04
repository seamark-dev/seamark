package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// hookCase is one hook command and the answer the shell reader must give.
type hookCase struct {
	name string
	cmd  string
	want HookUse
}

// assertHookUse runs each case as a subtest against the markers.
func assertHookUse(t *testing.T, markers []string, cases []hookCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SeamarkHookUse(tc.cmd, markers), tc.cmd)
		})
	}
}

func TestSeamarkHookUseFollowsExecutionAndQuoting(t *testing.T) {
	full := "/usr/local/bin/seamark " + CodexLessonsMarker

	assertHookUse(t, []string{CodexLessonsMarker}, []hookCase{
		// The reported defect: the full command as printed text. One quoted
		// word is one argument of echo, and nothing executes it.
		{"echo with the command in single quotes", "echo '" + full + "'", HookNotRun},
		{"echo with the command in double quotes", `echo "` + full + `"`, HookNotRun},
		{"printf with the command as its format", "printf '%s\\n' '" + full + "'", HookNotRun},
		{"a quoted command inside a longer text", `logger "would run: ` + full + ` (disabled)"`, HookNotRun},
		{"a comment-like note in an argument", "true '" + full + "'", HookNotRun},
		{"the marker text without a binary", `echo "` + CodexLessonsMarker + `"`, HookNotRun},
		{"a lookalike binary", "/opt/seamark2 " + CodexLessonsMarker, HookNotRun},
		{"another seamark hook", "/usr/local/bin/seamark lessons --hook", HookNotRun},
		{"the reset hook", "/usr/local/bin/seamark lessons --hook-reset --client codex", HookNotRun},
		{"more arguments than the hook has", full + " --verbose", HookNotRun},
		{"nothing", "", HookNotRun},

		// The shell executes the seamark command.
		{"the bare command", full, HookRuns},
		{"a quoted binary path", "'/opt/my tools/seamark' " + CodexLessonsMarker, HookRuns},
		{"a double-quoted binary path", `"/opt/my tools/seamark" ` + CodexLessonsMarker, HookRuns},
		{"a shell condition", "test -x /usr/local/bin/seamark && " + full, HookRuns},
		{"a command list", "cd /repo; " + full, HookRuns},
		{"a fallback", full + " || true", HookRuns},
		{"redirects", full + " >> /tmp/log 2>&1", HookRuns},
		{"a redirect with an attached target", full + " 2>/dev/null", HookRuns},
		{"a redirect with a blank after >&", full + " >& 2", HookRuns},
		{"an environment assignment", "SEAMARK_DEBUG=1 " + full, HookRuns},
		{"env", "env SEAMARK_DEBUG=1 " + full, HookRuns},
		{"timeout", "timeout 5 " + full, HookRuns},
		{"a subshell", "(cd /repo && " + full + ")", HookRuns},
		{"a pipeline", full + " | tee /tmp/log", HookRuns},
		{"sh -c with the command as the script", "sh -c '" + full + "'", HookRuns},
		{"bash -lc with a redirect inside", `bash -lc "` + full + ` >> /tmp/log"`, HookRuns},
		{"escaped spaces in the binary path", `/opt/my\ tools/seamark ` + CodexLessonsMarker, HookRuns},
		{"a single-quoted dollar sign", `echo '$x'; ` + full, HookRuns},
		{"a script without an expansion ignores its operands", "sh -c 'echo hi' sh " + full, HookNotRun},

		// A redirection attached to a word starts a new word (POSIX sh
		// "Token Recognition", rule 6).
		{"an attached redirect", full + ">/tmp/log", HookRuns},
		{"an attached append and a descriptor", full + ">>/tmp/log 2>&1", HookRuns},
		{"an attached input file", full + "</dev/null", HookMayRun},

		// A backslash before a newline continues the line.
		{"a line continuation", "/usr/local/bin/seamark lessons \\\n--hook --client codex", HookRuns},
		{"a line continuation in double quotes", `bash -c "/usr/local/bin/seamark lessons \` + "\n" + `--hook --client codex"`, HookRuns},

		// Another program gets the seamark command as its arguments. A
		// wrapper runs them, and echo prints them: the words do not say.
		{"an unknown wrapper", "/opt/wrapper " + full, HookMayRun},
		{"eval with the command", "eval '" + full + "'", HookRuns},
		{"eval with the command in words", "eval " + full, HookRuns},
		{"eval that only prints", `eval "echo '` + full + `'"`, HookNotRun},
		{"env -S with the command", "env -S '" + full + "'", HookRuns},
		{"env -S attached", "env '-S" + full + "'", HookRuns},
		{"env -S with a list", "env -S 'true; " + full + "'", HookMayRun},
		{"busybox sh -c", "busybox sh -c '" + full + "'", HookRuns},
		{"busybox timeout", "busybox timeout 5 " + full, HookRuns},
		{"a brace expansion in the command", "/usr/local/bin/seamark {lessons,} --hook --client codex", HookMayRun},
		{"a brace expansion in the binary", "/usr/local/{bin,sbin}/seamark " + CodexLessonsMarker, HookMayRun},
		{"command -v prints the path", "command -v /usr/local/bin/seamark " + CodexLessonsMarker, HookNotRun},
		{"command -V prints the kind", "command -pV /usr/local/bin/seamark " + CodexLessonsMarker, HookNotRun},
		{"command runs the command", "command " + full, HookRuns},
		{"echo without quotes", "echo " + full, HookMayRun},
		{"a substitution the reader does not follow", "$(which seamark) " + CodexLessonsMarker + "; " + full, HookMayRun},
		{"a substitution as the binary", `"$(command -v seamark)" ` + CodexLessonsMarker, HookMayRun},
		{"a variable in double quotes", `echo "$x"; ` + full, HookMayRun},
		{"a script that runs its operands", `sh -c '"$@"' sh ` + full, HookMayRun},
		{"an unclosed quote around real words", "/opt/wrapper " + full + " '", HookMayRun},

		// The reported defect: the operands were read on their own, so a
		// script that joins "$0" or "$@" with the marker read as no run.
		{"a script that runs $0 with the marker", `sh -c '"$0" ` + CodexLessonsMarker + `' /usr/local/bin/seamark`, HookMayRun},
		{"a script that runs seamark with $@", `sh -c '/usr/local/bin/seamark "$@"' _ ` + CodexLessonsMarker, HookMayRun},
		{"a script that runs $0 without seamark", `sh -c '"$0"' /opt/other ` + CodexLessonsMarker, HookNotRun},

		// The reported defect: an unquoted glob read as a plain word. The
		// shell replaces it by the files it matches, or by nothing.
		{"a glob in the binary path", "/opt/*/seamark " + CodexLessonsMarker, HookMayRun},
		{"a glob in a guard", "test -f /tmp/a?b && " + full, HookMayRun},
		{"a bracket pattern in the binary path", "/opt/[ab]/seamark " + CodexLessonsMarker, HookMayRun},
		{"a test command with brackets", "[ -x /usr/local/bin/seamark ] && " + full, HookRuns},
		{"a quoted glob", "echo '*'; " + full, HookRuns},
		{"an escaped glob", `echo \*; ` + full, HookRuns},

		// An echo inside a script is still an echo.
		{"sh -c that only prints", `sh -c "echo '` + full + `'"`, HookNotRun},

		// The loose test reads the text that the parser does not read. A
		// backquote ends a word. The shell removes a backslash, and a
		// backslash before a newline.
		{"a backquote substitution", "echo `" + full + "`", HookMayRun},
		{"an escaped command name in an if", "if :; then \\seamark " + CodexLessonsMarker + "; fi", HookMayRun},
		{"an escaped letter after a negation", "! /usr/local/bin/s\\eamark " + CodexLessonsMarker, HookMayRun},
		{"a line continuation after a negation", "! /usr/local/bin/seamark lessons \\\n--hook --client codex", HookMayRun},
	})

	// One marker is a prefix of another. The Claude Code marker must not
	// match inside the Codex command or the reset command.
	claude := []string{LessonsMarker}
	assert.Equal(t, HookNotRun, SeamarkHookUse(full, claude))
	assert.Equal(t, HookNotRun, SeamarkHookUse("/bin/seamark lessons --hook-reset", claude))
	assert.Equal(t, HookRuns, SeamarkHookUse("sh -c '/bin/seamark lessons --hook'", claude))
}

func TestSeamarkHookUseReadsShellOptions(t *testing.T) {
	full := "/usr/local/bin/seamark " + CodexLessonsMarker

	assertHookUse(t, []string{CodexLessonsMarker}, []hookCase{
		// The reported defect: a long option holds the letter c, and the
		// reader took it for -c, so it read the wrong word as the script.
		{"bash --norc -c", "bash --norc -c '" + full + "'", HookRuns},
		{"bash --rcfile f -c", "bash --rcfile f -c '" + full + "'", HookRuns},
		{"bash --init-file f -c", "bash --init-file f -c '" + full + "'", HookRuns},
		{"two long options", "bash --noprofile --norc -c '" + full + "'", HookRuns},

		// Short options. Only "o", and "O" in bash, take the next word.
		{"bash -o pipefail -c", "bash -o pipefail -c '" + full + "'", HookRuns},
		{"bash -O extglob -c", "bash -O extglob -c '" + full + "'", HookRuns},
		{"bash +o posix -c", "bash +o posix -c '" + full + "'", HookRuns},
		{"bash -ec", "bash -ec '" + full + "'", HookRuns},
		{"bash -euo pipefail -c", "bash -euo pipefail -c '" + full + "'", HookRuns},
		{"zsh -c", "zsh -c '" + full + "'", HookRuns},
		// bash, dash, ksh, and zsh all read "+c" as "-c".
		{"bash +c", "bash +c '" + full + "'", HookRuns},

		// The script is the first operand after the options, which need not
		// be the word after -c.
		{"bash -c -e script", "bash -c -e '" + full + "'", HookRuns},
		{"bash -c -- script", "bash -c -- '" + full + "'", HookRuns},

		// "--" ends the options, so a -c after it names a script file. A
		// script file can run the words that follow it as its arguments.
		{"bash -- -c", "bash -- -c '" + full + "'", HookMayRun},
		{"bash -- script", "bash -- /opt/run.sh " + full, HookMayRun},
		{"sh script", "sh /opt/run.sh " + full, HookMayRun},
		{"bash -s", "bash -s " + full, HookMayRun},

		// The reader cannot pick the script with certainty. dash and zsh
		// reject "--norc", and bash rejects a long option after a short one.
		{"a long option for sh", "sh --norc -c '" + full + "'", HookMayRun},
		{"a long option after a short one", "bash -c --norc '" + full + "'", HookMayRun},
		{"an unknown long option", "bash --bogus -c '" + full + "'", HookMayRun},
		{"-O for a shell other than bash", "zsh -O extglob -c '" + full + "'", HookMayRun},
		{"--rcfile without its file", "bash --rcfile", HookNotRun},

		// An exec prefix in front of the shell runs the shell.
		{"env in front of bash", "env bash -c '" + full + "'", HookRuns},
		{"env with a path and a long option", "/usr/bin/env bash --norc -c '" + full + "'", HookRuns},
		{"exec in front of sh", "exec sh -c '" + full + "'", HookRuns},
		{"timeout in front of bash", "timeout 5 bash -c '" + full + "'", HookRuns},
		{"nice with a value", "nice -n 5 bash -c '" + full + "'", HookRuns},
		{"two prefixes", "nohup env SEAMARK_DEBUG=1 sh -c '" + full + "'", HookRuns},

		// A prefix reads its options, each with its value, and runs the
		// first word after them. Only timeout takes a number there, as its
		// duration. Another word is an unknown program.
		{"env in front of a wrapper", "env /opt/wrapper " + full, HookMayRun},
		{"an option with a value", "timeout -s KILL 5 " + full, HookRuns},
		{"an option without a value", "timeout --foreground 5 " + full, HookRuns},
		{"env with an unset option", "env -u SEAMARK_DEBUG " + full, HookRuns},
		{"env with an argv0 option", "env -a seamark " + full, HookRuns},
		{"env with the end of its options", "env -- " + full, HookRuns},
		{"an option after the duration", "timeout 5 --foreground " + full, HookMayRun},

		// The reported defect: every prefix skipped a number, so "env 5"
		// read as env, and "busybox seamark" read as seamark. env runs the
		// program "5", and busybox has no applet seamark.
		{"env with a number as the program", "env 5 " + full, HookMayRun},
		{"nice with a number as the program", "nice 5 " + full, HookMayRun},
		{"busybox with seamark as the applet", "busybox " + full, HookMayRun},
		{"busybox with a path as the applet", "busybox /bin/sh -c '" + full + "'", HookRuns},
		{"busybox with env as the applet", "busybox env " + full, HookRuns},

		// env -S puts the split words in front of the remaining arguments.
		{"env -S with the rest of the command after it", "env -S '/usr/local/bin/seamark lessons' --hook --client codex", HookRuns},
		{"env --split-string with the rest after it", "env --split-string='/usr/local/bin/seamark lessons' --hook --client codex", HookRuns},
		{"env -S with an option in the string", "env -S '-u SEAMARK_DEBUG " + full + "'", HookRuns},

		// noexec makes the shell read the script and run nothing.
		{"bash -n", "bash -n -c '" + full + "'", HookNotRun},
		{"sh -nc", "sh -nc '" + full + "'", HookNotRun},
		{"-o noexec", "bash -o noexec -c '" + full + "'", HookNotRun},
		{"set -n in the script", "bash -c 'set -n; " + full + "'", HookNotRun},
		{"bash +n", "bash +n -c '" + full + "'", HookRuns},

		// No script runs the hook.
		{"-c without a script", "bash -c", HookNotRun},
		{"a script that only prints", `bash --norc -c "echo '` + full + `'"`, HookNotRun},
	})
}

func TestSeamarkHookUseSkipsComments(t *testing.T) {
	full := "/usr/local/bin/seamark " + CodexLessonsMarker

	assertHookUse(t, []string{CodexLessonsMarker}, []hookCase{
		// The reported defect: the comment words became extra arguments.
		{"a comment after the hook", full + " # deliver lessons", HookRuns},
		{"a comment without a blank", full + " #note", HookRuns},
		{"a comment after an operator", full + ";# note", HookRuns},
		{"a comment that ends at the newline", "# lessons hook\n" + full, HookRuns},

		// The hook text inside a comment runs nothing.
		{"the hook after a comment sign", "true # " + full, HookNotRun},
		{"a hook that is commented out", "# " + full, HookNotRun},

		// "#" starts a comment only at the start of a word, and never when
		// quoted or escaped.
		{"# inside the last word", full + "#note", HookNotRun},
		{"# inside an earlier word", "echo foo#bar; " + full, HookRuns},
		{"a quoted #", "echo '#'; " + full, HookRuns},
		{"an escaped #", `echo \#; ` + full, HookRuns},
		{"# inside a quoted text", `echo "# ` + full + `"`, HookNotRun},

		// The shell expands nothing in a comment, so a "$", a backquote, or
		// "<<" there leaves the words as they are.
		{"a dollar sign in the comment", full + " # costs $5", HookRuns},
		{"a backquote in the comment", full + " # uses `x`", HookRuns},
		{"a here-document sign in the comment", full + " # see <<doc", HookRuns},
		{"an expansion after the comment", "# note\necho $x; " + full, HookMayRun},
	})
}

func TestHookUseAndStatusFollowTheControlOperators(t *testing.T) {
	markers := []string{CodexGateMarker(ModeEnforce)}
	gate := "/usr/local/bin/seamark " + CodexGateMarker(ModeEnforce)

	for _, tc := range []struct {
		name string
		cmd  string
		use  HookUse
		// passes is the answer of HookStatusPasses.
		passes bool
	}{
		// The hook gives the exit status of the whole command.
		{"the bare command", gate, HookRuns, true},
		{"after a command list", "cd /repo; " + gate, HookRuns, true},
		{"a trailing separator", gate + ";", HookRuns, true},
		{"before &&", gate + " && echo ok", HookRuns, true},
		{"before && and a newline", gate + " &&\necho ok", HookRuns, true},
		{"redirected output", gate + " 2>/dev/null", HookRuns, true},
		{"an exec prefix", "timeout 5 " + gate, HookRuns, true},

		// The reported defect: the hook runs, and another command decides
		// the exit status, so a verdict never blocks.
		{"a fallback", gate + " || true", HookRuns, false},
		{"a fallback with :", gate + " || :", HookRuns, false},
		{"an exit with another status", gate + " || exit 1", HookRuns, false},
		{"a command after ;", gate + "; true", HookRuns, false},
		{"a command after a newline", gate + "\necho done", HookRuns, false},
		{"a pipe into cat", gate + " | cat", HookRuns, false},
		{"a pipe into tee", gate + " | tee /tmp/log", HookRuns, false},
		{"a pipe into tee with zsh pipefail", "setopt pipefail; " + gate + " | tee /tmp/log", HookRuns, true},
		{"a pipe into tee with zsh pipefail off again", "setopt pipefail; unsetopt pipefail; " + gate + " | tee /tmp/log", HookRuns, false},
		{"a pipe into tee with zsh nopipefail", "setopt no_pipefail; " + gate + " | tee /tmp/log", HookRuns, false},
		{"a pipe into tee with shopt -so pipefail", "shopt -so pipefail; " + gate + " | tee /tmp/log", HookRuns, true},
		{"a pipe into tee with shopt -s -o pipefail", "shopt -s -o pipefail; " + gate + " | tee /tmp/log", HookRuns, true},
		{"a pipe into tee with shopt -s extglob", "shopt -s extglob; " + gate + " | tee /tmp/log", HookRuns, false},
		{"zsh noexec", "setopt noexec; " + gate, HookNotRun, false},
		{"&& and then a fallback", gate + " && echo ok || true", HookRuns, false},
		{"an exec prefix with a fallback", "timeout 5 " + gate + " || true", HookRuns, false},

		// An exit command and exec keep the exit status.
		{"exit 2 as the fallback", gate + " || exit 2", HookRuns, true},
		{"exit as the fallback", gate + " || exit", HookRuns, true},
		{"exit after ;", gate + "; exit", HookRuns, true},
		{"exec replaces the shell", "exec " + gate + "; true", HookRuns, true},

		// The shell does not wait for a command in the background.
		{"the background", gate + " &", HookMayRun, false},
		{"a pipeline in the background", gate + " | tee /tmp/log &", HookMayRun, false},
		{"the background, then more", gate + " & wait", HookMayRun, false},

		// A command other than the hook counts as one that exits 0, as a
		// guard normally does: "&&" runs the next pipeline, "||" skips it.
		{"after &&", "test -x /usr/local/bin/seamark && " + gate, HookRuns, true},
		{"after || and then &&", "test -d /repo || mkdir /repo && " + gate, HookRuns, true},
		{"after a guard that exits when it fails", "command -v seamark >/dev/null || exit 0; " + gate, HookRuns, true},
		{"after ||", "/opt/primary-gate || " + gate, HookMayRun, true},
		{"after false &&", "false && " + gate, HookMayRun, true},
		{"after false ||", "false || " + gate, HookRuns, true},
		{"after false with errexit", "set -e; false; " + gate, HookMayRun, true},
		{"false as the fallback", gate + " || false", HookRuns, false},
		{"an arithmetic command as the guard", "(( 1 )) && " + gate, HookMayRun, true},
		{"after the hook", gate + " || " + gate, HookRuns, true},
		{"after an exit", "exit 0; " + gate, HookMayRun, true},

		// The reported defect: a pipeline that "&&" or "||" skipped counted
		// as one that ran and exited 0, and an exit before "&&" was
		// ignored. A skipped pipeline leaves the status as it is.
		{"after false and a skipped command", "false && /opt/guard && " + gate, HookMayRun, true},
		{"after false, a skipped command, and ||", "false && /opt/guard || " + gate, HookRuns, true},
		{"after true and a skipped fallback", "true || /opt/fallback && " + gate, HookRuns, true},
		{"after exit 0 and &&", "exit 0 && " + gate, HookMayRun, true},
		{"after exec and &&", "exec /opt/other && " + gate, HookMayRun, true},

		// The reported defect: only a bare "false" counted as a failure
		// before "&&", while the status model knew more forms. One status
		// model decides both.
		{"after a subshell that fails", "(false) && " + gate, HookMayRun, true},
		{"after a brace group that fails", "{ false; } && " + gate, HookMayRun, true},
		{"after a subshell that exits 1", "(exit 1) && " + gate, HookMayRun, true},
		{"after a subshell that exits 1 and ||", "(exit 1) || " + gate, HookRuns, true},
		{"after eval false", "eval false && " + gate, HookMayRun, true},
		{"after a pipeline that fails", "true | false && " + gate, HookMayRun, true},
		{"after a pipeline that succeeds", "false | true && " + gate, HookRuns, true},

		// "false" fails by any path and behind an exec prefix.
		{"after /bin/false", "/bin/false && " + gate, HookMayRun, true},
		{"after command false", "command false && " + gate, HookMayRun, true},
		{"after env false", "env FOO=1 false && " + gate, HookMayRun, true},
		{"after /usr/bin/false and ||", "/usr/bin/false || " + gate, HookRuns, true},
		{"/bin/false as the fallback", gate + " || /bin/false", HookRuns, false},
		{"exec false as the fallback", gate + " || exec false", HookRuns, false},
		{"exec true as the fallback", gate + " || exec true", HookRuns, false},

		// The reported defect: only an input operator counted as a
		// replaced standard input. Any redirection of descriptor 0 takes
		// the client's payload away.
		{"descriptor 0 opened for output", gate + " 0>/dev/null", HookMayRun, true},
		{"descriptor 0 closed with >&-", gate + " 0>&-", HookMayRun, true},
		{"descriptor 0 copied with >&", gate + " 0>&3", HookMayRun, true},
		{"exec opens descriptor 0 for output", "exec 0>/dev/null; " + gate, HookMayRun, true},
		{"descriptor 1 opened for output", gate + " 1>/dev/null", HookRuns, true},

		// The hook reads another input than the client's payload.
		{"a pipe into the hook", "cat | " + gate, HookMayRun, true},
		{"a file as standard input", gate + " < /dev/null", HookMayRun, true},
		{"a closed standard input", gate + " <&-", HookMayRun, true},
		{"a copied standard input", gate + " <&3", HookMayRun, true},
		{"exec copies standard input", "exec 0<&3; " + gate, HookMayRun, true},
		{"a copied standard error", gate + " 2<&3", HookRuns, true},
		{"a file opened for reading and writing", gate + " <> /dev/null", HookMayRun, true},
		{"exec redirects standard input", "exec </dev/null; " + gate, HookMayRun, true},
		{"exec redirects standard input after a guard", "true && exec </dev/null; " + gate, HookMayRun, true},
		{"exec redirects standard input in the same AND-OR list", "exec </dev/null && " + gate, HookMayRun, true},
		{"exec redirects standard input in a brace group", "{ exec </dev/null; }; " + gate, HookMayRun, true},
		{"exec redirects only standard error", "exec 2>&1; " + gate, HookRuns, true},
		{"another descriptor opened for reading and writing", gate + " 3<> /tmp/state", HookRuns, true},

		// bash and zsh read "&>" and ">&file" as redirections of the output
		// and the errors. dash reads "x &>f" as "x &" and then ">f", and it
		// rejects ">&file". The reader cannot tell which shell runs the
		// hook, so the loose test answers.
		{"&> after a blank", gate + " &>/dev/null", HookMayRun, true},
		{"&> attached to a word", gate + "&>/dev/null", HookMayRun, true},
		{"&>> before a blank", gate + " &>> /tmp/log", HookMayRun, true},
		{"&>> attached to a word", gate + "&>>/tmp/log", HookMayRun, true},
		{">& with a file", gate + " >&/dev/null", HookMayRun, true},
		{">& and a blank before a file", gate + " >& /tmp/log", HookMayRun, true},
		{"&> in a guard", "cd /repo &>/dev/null && " + gate, HookMayRun, true},
		{">& with a descriptor", gate + " 2>&1", HookRuns, true},
		{">& that closes a descriptor", gate + " 2>&-", HookRuns, true},

		// A subshell passes its status to the commands around it.
		{"a subshell", "(cd /repo; " + gate + ")", HookRuns, true},
		{"a guard in a subshell", "(cd /repo && " + gate + ")", HookRuns, true},
		{"a fallback before the hook in a subshell", "(/opt/primary-gate || " + gate + ")", HookMayRun, true},
		{"a fallback in a subshell", "(" + gate + " || true)", HookRuns, false},
		{"a fallback after a subshell", "(" + gate + ") || true", HookRuns, false},
		{"a command after a subshell", "(" + gate + "); true", HookRuns, false},
		{"an exit in a subshell ends only the subshell", "(" + gate + " || exit 2); true", HookRuns, false},
		{"exec in a subshell ends only the subshell", "(exec " + gate + "); true", HookRuns, false},
		{"a subshell in the background", "(" + gate + ") &", HookMayRun, false},

		// A brace group runs in the shell itself, so an exit in it ends the
		// shell. The reported defect: the closing "}" read as one more
		// command, which took the status from the hook.
		{"a brace group", "{ " + gate + "; }", HookRuns, true},
		{"a brace group with a redirect", "{ cd /repo; " + gate + "; } 2>>/tmp/log", HookRuns, true},
		{"a guard before a brace group", "cd /repo && { " + gate + "; }", HookRuns, true},
		{"a fallback after a brace group", "{ " + gate + "; } || true", HookRuns, false},
		{"a brace group in the background", "{ " + gate + "; } &", HookMayRun, false},
		{"a brace group that exits as the fallback", gate + " || { echo blocked >&2; exit 2; }", HookRuns, true},
		{"a brace group that exits with another status", gate + " || { echo failed >&2; exit 1; }", HookRuns, false},
		{"an exit in a brace group ends the shell", "{ " + gate + "; exit; }; true", HookRuns, true},
		{"a brace group that exits before the hook", "{ echo skip; exit 0; }; " + gate, HookMayRun, true},
		{"a subshell that exits 2 as the fallback", gate + " || (exit 2)", HookRuns, true},

		// The parser does not read other compound commands. The loose test
		// answers, so the status counts as passed.
		{"if", "if command -v seamark >/dev/null; then " + gate + "; fi", HookMayRun, true},
		{"if with a negation", "if ! " + gate + "; then exit 2; fi", HookMayRun, true},
		{"for", "for x in 1; do " + gate + "; done", HookMayRun, true},
		{"a negation", "! " + gate, HookMayRun, true},
		{"a bash test", "[[ -x /usr/local/bin/seamark ]] && " + gate, HookMayRun, true},
		{"a brace group without a separator", "{ " + gate + " }", HookMayRun, true},
		{"a closing brace without its group", gate + "; }", HookMayRun, true},

		// Every level of nested scripts must pass the status on.
		{"sh -c", "sh -c '" + gate + "'", HookRuns, true},
		{"a fallback inside sh -c", "sh -c '" + gate + " || true'", HookRuns, false},
		{"a fallback outside sh -c", "sh -c '" + gate + "' || true", HookRuns, false},
		{"the background inside sh -c", "sh -c '" + gate + " &'", HookMayRun, false},
		{"two levels", `bash -c "sh -c '` + gate + `'"`, HookRuns, true},
		{"a pipe at the outer of two levels", `bash -c "sh -c '` + gate + `' | cat"`, HookRuns, false},
		{"exec in front of a shell", "exec sh -c '" + gate + "'; true", HookRuns, true},
		{"a shell in front of a fallback", "env bash -c '" + gate + "' || true", HookRuns, false},

		// errexit ends the shell when the last pipeline of a list fails.
		{"bash -e", "bash -ec '" + gate + "; echo done'", HookRuns, true},
		{"set -e", "bash -c 'set -e; " + gate + "; echo done'", HookRuns, true},
		{"errexit ignores a pipeline before &&", "bash -ec '" + gate + " && echo ok; echo done'", HookRuns, false},
		{"errexit with a guard before the hook", "bash -ec 'cd /repo; " + gate + "'", HookRuns, true},
		{"errexit after an earlier run of the hook", "bash -ec '" + gate + "; " + gate + "'", HookRuns, true},
		{"errexit does not reach a nested shell", "bash -ec \"sh -c '" + gate + "; true'\"", HookRuns, false},
		{"errexit ignores a list in the background", "bash -ec '" + gate + " & " + gate + "'", HookRuns, true},

		// bash(1) ignores errexit in every command of a compound command
		// that "&&" or "||" follows.
		{"errexit ignores a brace group before &&", "set -e; { " + gate + "; echo after; } && true", HookRuns, false},
		{"errexit ignores a brace group before ||", "set -e; { " + gate + "; } || true", HookRuns, false},
		{"errexit ignores a fallback in a subshell", "set -e; ({ " + gate + "; } || true) >/dev/null 2>&1", HookRuns, false},
		{"errexit ignores a brace group in bash -e", "bash -ec '{ " + gate + "; } || true'", HookRuns, false},
		{"&& skips the command after a brace group", "set -e; { " + gate + "; } && true", HookRuns, true},

		// A set command that the reader does not follow can turn errexit or
		// pipefail on. The status then counts as passed.
		{"set -e after a guard", "true && set -e; " + gate + "; true", HookRuns, true},
		{"set -e in a brace group", "{ set -e; }; " + gate + "; true", HookRuns, true},
		{"set -e before the hook in one AND-OR list", "set -e && " + gate + "; true", HookRuns, true},
		{"set -o pipefail after a guard", "true && set -o pipefail; " + gate + " | cat", HookRuns, true},

		// pipefail gives a pipeline the status of its last failing command.
		{"bash -o pipefail", "bash -o pipefail -c '" + gate + " | cat'", HookRuns, true},
		{"set -o pipefail", "bash -c 'set -euo pipefail; " + gate + " | tee /tmp/log'", HookRuns, true},
		{"exec in a pipeline ends only its subshell", "bash -o pipefail -c 'exec " + gate + " | cat; true'", HookRuns, false},

		// The reported defect: with pipefail, a later command that fails
		// takes the status from the hook, and the reader ignored it.
		{"pipefail and false after the hook", "set -o pipefail; " + gate + " | false", HookRuns, false},
		{"pipefail and false at the end", "set -o pipefail; " + gate + " | cat | false", HookRuns, false},
		{"pipefail and a subshell that fails", "set -o pipefail; " + gate + " | (exit 1)", HookRuns, false},
		{"pipefail and false before the hook", "set -o pipefail; false | " + gate + " | cat", HookMayRun, true},

		// The reported defect: eval read its script as if the shell options
		// were off. eval runs the script in the shell itself.
		{"eval under pipefail", "set -o pipefail; eval '" + gate + " | cat'", HookRuns, true},
		{"eval under errexit", "set -e; eval '" + gate + "; true'", HookRuns, true},
		{"eval that sets pipefail", "eval 'set -o pipefail'; " + gate + " | cat", HookRuns, true},
		{"eval that exits 2 as the fallback", gate + " || eval 'exit 2'", HookRuns, true},
		{"eval that exits 1 as the fallback", gate + " || eval 'exit 1'", HookRuns, false},
		{"eval that exits before the hook", "eval 'exit 0'; " + gate, HookMayRun, true},

		// The reader cannot tell, so the status counts as passed.
		{"an unknown wrapper", "/opt/wrapper " + gate, HookMayRun, true},
		{"an unknown wrapper with a fallback", "/opt/wrapper " + gate + " || true", HookMayRun, false},
		{"the hook after an unknown wrapper", "/opt/wrapper " + gate + " && " + gate, HookMayRun, true},
		{"an exit operand that is not a number", gate + " || exit two", HookRuns, true},
		{"unread syntax", `"$(command -v seamark)" ` + CodexGateMarker(ModeEnforce), HookMayRun, true},
		{"a syntax error", gate + " &&", HookMayRun, true},
		{"a case statement", "case x in x) " + gate + ";; esac", HookMayRun, true},

		// No run of the hook, so no status.
		{"only printed", "echo '" + gate + "'", HookNotRun, false},
		{"another hook", "/usr/local/bin/seamark gate --hook --client codex", HookNotRun, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.use, SeamarkHookUse(tc.cmd, markers), tc.cmd)
			assert.Equal(t, tc.passes, HookStatusPasses(tc.cmd, markers), tc.cmd)
		})
	}
}

func TestHookStatusPassesForSomeMarker(t *testing.T) {
	// A wrapper can run the gate in both modes. The status passes when it
	// passes for one marker that the command runs.
	gate := CodexSpecs(ModeWarn)[0]
	cmd := "/bin/seamark gate --hook --client codex || true; /bin/seamark gate --enforce --hook --client codex"

	assert.True(t, HookStatusPasses(cmd, gate.Markers()))
	assert.False(t, HookStatusPasses(cmd, []string{gate.Marker}), "the warn gate is followed by a fallback")
	assert.True(t, HookStatusPasses(cmd, gate.Legacy), "the enforce gate ends the command")

	// One reading answers the same questions for each marker from one
	// parse of the command.
	reading := ReadHook(cmd, gate.Markers())
	assert.Equal(t, HookRuns, reading.Use())
	assert.True(t, reading.StatusPasses())
	assert.Equal(t, HookRuns, reading.UseFor(gate.Marker))
	assert.False(t, reading.StatusPassesFor(gate.Marker))
	assert.Equal(t, HookRuns, reading.UseFor(gate.Legacy[0]))
	assert.True(t, reading.StatusPassesFor(gate.Legacy[0]))
	assert.Equal(t, HookNotRun, reading.UseFor(LessonsMarker), "a marker outside the reading")
	assert.False(t, reading.StatusPassesFor(LessonsMarker))

	// A command that only prints the hook runs it for no marker.
	printed := ReadHook("echo '"+cmd+"'", gate.Markers())
	assert.Equal(t, HookNotRun, printed.Use())
	assert.False(t, printed.StatusPasses())
}

func TestShellReaderBoundsItsWork(t *testing.T) {
	// A hook command comes from a file in the repository, so a crafted
	// command must not stop init, doctor, status, or the MCP server. Each
	// command runs the hook. Over the bounds, the loose test answers.
	gate := "/bin/seamark " + CodexGateMarker(ModeEnforce)
	markers := []string{CodexGateMarker(ModeEnforce)}

	for _, tc := range []struct {
		name string
		cmd  string
		want HookUse
	}{
		{"subshells at the deepest level the parser reads", nest("( ", gate, " )", maxShellNesting), HookRuns},
		{"one subshell more", nest("( ", gate, " )", maxShellNesting+1), HookMayRun},
		{"brace groups one level too deep", nest("{ ", gate, "; }", maxShellNesting+1), HookMayRun},
		// bash and zsh read "((" as an arithmetic command and reject this
		// form; dash reads nested subshells. The reader cannot tell which
		// shell runs the hook.
		{"subshells without blanks", nest("(", gate, ")", 2), HookMayRun},
		// Without the bound, this recursion overflows the stack of the
		// parser, which is fatal.
		{"a million subshells", nest("( ", gate, " )", 1_000_000), HookMayRun},
		// Without the bound, the status model walks the rest of the list
		// after each run of the hook. This list then takes about 11 seconds.
		{"a list of forty thousand runs", strings.Repeat(gate+"; ", 40_000), HookMayRun},
		// The reported defect: the status model followed eval without a
		// depth bound, and each level parsed the script again. A thousand
		// evals in front of a long word took about 20 seconds.
		{"a thousand evals in front of a long word", strings.Repeat("eval ", 1_000) + gate + " '" + strings.Repeat("x", 50_000) + "'", HookMayRun},
		// A line over maxShellBytes gets the loose test, whatever its words.
		{"a line longer than the byte bound", gate + " '" + strings.Repeat("x", maxShellBytes) + "'", HookMayRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()

			assert.Equal(t, tc.want, SeamarkHookUse(tc.cmd, markers))
			assert.True(t, HookStatusPasses(tc.cmd, markers))
			assert.Less(t, time.Since(start), 2*time.Second)
		})
	}

	// The longest list that the parser reads costs the most work in the
	// status model. It takes less than one millisecond, and about 15
	// milliseconds with the race detector. The bound is loose on purpose,
	// for a slow machine.
	longest := strings.Repeat(gate+"; ", maxShellTokens/7)
	start := time.Now()

	assert.Equal(t, HookRuns, SeamarkHookUse(longest, markers))
	assert.Less(t, time.Since(start), time.Second)

	// The status model walks the rest of the list after each run of the
	// hook, and the rest here holds an eval chain with a long word. The
	// parse memo makes each level cost one parse per reading. The last
	// command has one argument too many, so no run passes its status.
	chain := strings.Repeat(gate+"; ", 100) + "eval eval eval " + gate + " '" + strings.Repeat("x", 50_000) + "'"
	start = time.Now()

	assert.Equal(t, HookRuns, SeamarkHookUse(chain, markers))
	assert.False(t, HookStatusPasses(chain, markers), "a command after each run discards its status")
	assert.Less(t, time.Since(start), time.Second)
}

// nest wraps body in depth pairs of opening and closing text.
func nest(opening, body, closing string, depth int) string {
	return strings.Repeat(opening, depth) + body + strings.Repeat(closing, depth)
}
