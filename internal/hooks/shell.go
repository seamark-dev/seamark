package hooks

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// HookUse says whether a hook command runs a seamark hook.
type HookUse int

// The answers of SeamarkHookUse.
const (
	// HookNotRun means the command does not run the hook. The hook text
	// can still occur in it, for example as an argument of echo or in a
	// comment.
	HookNotRun HookUse = iota
	// HookMayRun means the command can run the hook, and the words do not
	// show that it always does. For example, another program gets the
	// seamark command as arguments, or the hook runs only on a condition
	// or in the background. The hook can also read another input than the
	// client's payload, or the command uses syntax that this package does
	// not read.
	HookMayRun
	// HookRuns means the shell executes the seamark binary with exactly
	// the hook arguments, in the foreground, each time the command runs.
	// The hook reads the client's payload. Its output and its exit status
	// can still go to another program; HookStatusPasses tells the second.
	HookRuns
)

// SeamarkHookUse reads a hook command the way a shell does. It splits
// the command into words with the quoting rules and removes comments.
// Then it parses the words into lists, AND-OR lists, pipelines,
// subshells, and brace groups. The answer depends on what the shell
// executes, not on which text occurs.
//
// The two wrong answers do not cost the same. A false HookRuns makes
// setup install nothing, and the user loses the hook. A false
// HookNotRun makes setup install a second handler, and nothing warns
// about it. The function therefore answers HookRuns only for the forms
// it fully reads. It answers HookMayRun for the rest, which keeps the
// install and names the command in a warning. The rest includes an
// expansion such as "$x", a here-document, and a compound command such
// as "if" or "for". It also includes a redirection that the shells read
// in different ways, such as "&>", and a command that is too long or too
// deep for the parser.
//
// A command other than the hook counts as one that exits 0 and does not
// read standard input. A guard such as "test -x seamark" or "cd /repo"
// normally does that. A hook after "&&" therefore runs, and a second
// handler beside it would run each time too. A hook after "||" runs only
// when the command before it fails, so it is HookMayRun. With errexit, a
// guard before the hook does not end the shell, by the same rule.
//
// A hook in the background ("seamark ... &") is HookMayRun, because the
// shell does not wait and the client can miss the output. A hook that
// reads the output of another program ("x | seamark ...") or a file
// ("seamark ... < f") is HookMayRun too. Its input is not the client's
// payload, and only the other program decides what the hook gets.
//
// A hook whose output or exit status goes elsewhere is still HookRuns.
// "seamark ... | tee log", "seamark ... > log", and "seamark ... || true"
// run it each time with the client's payload. The user chose where the
// output goes, so setup does not add a handler beside it. A gate verdict
// blocks only by exit status 2, so HookStatusPasses answers that question.
//
// A marker matches only as the complete argument list. "lessons --hook"
// is a prefix of the Codex marker and of the reset marker, and a longer
// argument list is another hook.
func SeamarkHookUse(cmd string, markers []string) HookUse {
	return ReadHook(cmd, markers).Use()
}

// HookReading is the reading of one hook command for the markers of
// one hook. Setup and inspection ask several questions about one
// command: whether it runs the hook, for which marker, and whether the
// exit status passes. One reading parses the command once and answers
// them all, so the answers come from one parse.
type HookReading struct {
	results map[string]hookResult
	markers []string
}

// ReadHook reads a hook command for the markers of one hook, by the
// rules of SeamarkHookUse and HookStatusPasses.
func ReadHook(cmd string, markers []string) HookReading {
	reading := HookReading{results: make(map[string]hookResult, len(markers)), markers: markers}
	list, expands, ok := shellList(cmd)

	// One parse memo serves every marker: a nested script is parsed once
	// per reading, not once per marker, per walk, and per level.
	state := shellState{cache: &parseCache{scripts: map[string]parsedScript{}}}

	for _, marker := range markers {
		reading.results[marker] = scriptUse(cmd, list, expands, ok, strings.Fields(marker), 0, state)
	}

	return reading
}

// Use is the answer of SeamarkHookUse: the strongest use over the
// markers.
func (r HookReading) Use() HookUse {
	use := HookNotRun

	for _, marker := range r.markers {
		use = max(use, r.results[marker].use)
	}

	return use
}

// UseFor is the use of one marker: HookNotRun for a marker that the
// reading does not hold.
func (r HookReading) UseFor(marker string) HookUse {
	return r.results[marker].use
}

// StatusPasses is the answer of HookStatusPasses: the exit status of
// some run of the hook can become the exit status of the command.
func (r HookReading) StatusPasses() bool {
	for _, marker := range r.markers {
		if r.StatusPassesFor(marker) {
			return true
		}
	}

	return false
}

// StatusPassesFor is StatusPasses for one marker.
func (r HookReading) StatusPassesFor(marker string) bool {
	result := r.results[marker]

	return result.use != HookNotRun && result.passes
}

// HookStatusPasses reports whether the exit status of the seamark hook
// can become the exit status of the whole command. Claude Code and Codex
// block a tool call only on exit status 2. A gate hook therefore blocks
// only when this is true.
//
// The status must pass every level that holds the hook. The levels are
// the pipeline, the AND-OR list, and the list. Each subshell, brace
// group, and nested "sh -c" script is a level too. The shell discards
// the status when it does not wait ("&"). Another command discards it
// when that command decides the final status, as in "|| true", in "; echo
// done", and in "| cat" without pipefail. That command counts as one
// that exits 0, as in SeamarkHookUse, so the verdict does not block.
// errexit keeps the status, and so do exec and an exit command after
// "||" or ";". errexit does not apply in a pipeline that "&&" or "||"
// follows, also inside a brace group or a subshell there. The rules are
// those of POSIX sh "Shell Commands" and of bash(1) for pipefail and
// errexit.
//
// The answer is false when the command does not run the hook for any
// marker. It is true when the reader cannot tell, for example for an
// unknown wrapper program or unread syntax. A false "discarded" reports
// a gate that blocks as one that never blocks, and setup treats that as
// the worse error.
func HookStatusPasses(cmd string, markers []string) bool {
	return ReadHook(cmd, markers).StatusPasses()
}

// hookResult is how one part of a command runs the hook: a command, a
// pipeline, an AND-OR list, or a list. The results of several runs of
// the hook in one part combine with the or method.
type hookResult struct {
	use HookUse
	// passes is true when exit status 2 of some run of the hook becomes
	// the exit status of the part.
	passes bool
	// exits is true when that run also ends the shell: exec replaces the
	// shell, errexit stops it, or an exit command follows. The commands
	// after the part then never run.
	exits bool
}

// or combines the results of two parts. The use is the stronger one. The
// status passes when it passes for a part that runs the hook.
func (r hookResult) or(other hookResult) hookResult {
	switch {
	case other.use == HookNotRun:
		return r
	case r.use == HookNotRun:
		return other
	}

	return hookResult{
		use:    max(r.use, other.use),
		passes: r.passes || other.passes,
		exits:  (r.passes && r.exits) || (other.passes && other.exits),
	}
}

// uncertain lowers the use to HookMayRun, for a run that may not happen
// or that reads another input than the client's payload.
func (r hookResult) uncertain() hookResult {
	r.use = min(r.use, HookMayRun)

	return r
}

// follow sets the status of a run of the hook from status, the final
// status after the commands that follow the run. exits is true when the
// shell ends with that status. Exit status 2 of the hook passes only
// when the final status is still 2.
func (r hookResult) follow(status int, exits bool) hookResult {
	r.passes = status == blockingStatus
	r.exits = r.passes && exits

	return r
}

// cannotTell is the result for a command that the reader cannot read.
// The hook can run, and its status counts as passed, because a false
// "discarded" is the worse error for HookStatusPasses.
var cannotTell = hookResult{use: HookMayRun, passes: true}

// blockingStatus is the exit status that makes Claude Code and Codex
// block a tool call. The status model follows this value.
const blockingStatus = 2

// maxShellDepth bounds the nesting of the scripts that hookUse follows:
// `sh -c`, eval, and `env -S`. A deeper script gives HookMayRun.
const maxShellDepth = 3

// hookUse classifies a nested script for one marker, already split into
// words. state holds the options of the shell that runs the script.
func hookUse(cmd string, marker []string, depth int, state shellState) hookResult {
	list, expands, ok := state.parse(cmd)

	return scriptUse(cmd, list, expands, ok, marker, depth, state)
}

// scriptUse classifies a parsed script for one marker. An expansion
// changes what runs in a way that a word reader cannot follow. The
// words are not reliable then, so the test is loose. So is the test for
// a script that does not parse and for one nested too deep. The answer
// is then only HookMayRun, which never stops an install.
func scriptUse(cmd string, list []shellAndOr, expands, ok bool, marker []string, depth int, state shellState) hookResult {
	if !ok || expands || depth > maxShellDepth {
		return looseUse(cmd, marker)
	}

	return listUse(list, marker, depth, state)
}

// looseUse is the result of the loose test for text that the reader
// does not parse: cannotTell for a match, and no run otherwise.
func looseUse(text string, marker []string) hookResult {
	if looseMention(text, marker) {
		return cannotTell
	}

	return hookResult{}
}

// looseMention reports whether text holds a seamark word, then the
// marker words, wherever they stand. It is the test for text that the
// reader does not parse, so it answers a match when in doubt.
func looseMention(text string, marker []string) bool {
	words := looseWords(text)

	for i, word := range words {
		rest := words[i+1:]
		if IsSeamarkBinary(word) && len(rest) >= len(marker) && slices.Equal(rest[:len(marker)], marker) {
			return true
		}
	}

	return false
}

// looseMentionApart reports whether text holds a seamark word and the
// marker words, anywhere in it. It is the loose test for a script whose
// expansion joins words that stand apart, such as `sh -c 'seamark "$@"'
// _ gate --hook`, so it is looser than looseMention on purpose.
func looseMentionApart(text string, marker []string) bool {
	words := looseWords(text)
	seamark := slices.ContainsFunc(words, IsSeamarkBinary)

	for i := range words {
		if seamark && len(words)-i >= len(marker) && slices.Equal(words[i:i+len(marker)], marker) {
			return true
		}
	}

	return false
}

// looseWords splits text for the loose tests: at blanks, at shell
// operator characters, at quotes, at backquotes, and at the braces and
// commas of a brace expansion. It removes each backslash and each line
// continuation first, as the shell removes them outside quotes.
func looseWords(text string) []string {
	text = strings.ReplaceAll(text, "\\\n", "")
	text = strings.ReplaceAll(text, "\\", "")

	return strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(";&|()<>'\"`{},", r)
	})
}

// listUse classifies a list: the AND-OR lists of a script, a subshell,
// or a brace group, in order. The status of a list is the status of the
// last AND-OR list that runs. The caller decides what an exit ends,
// because an exit in a brace group ends the shell around it.
func listUse(list []shellAndOr, marker []string, depth int, state shellState) hookResult {
	var (
		result hookResult
		// ended is true when an earlier AND-OR list can end the shell. A
		// later one then runs only on a condition.
		ended bool
	)

	for i, andOr := range list {
		// With noexec ("set -n"), the shell reads the commands after it and
		// runs none of them.
		if state.noexec {
			break
		}

		r, ends := andOrUse(andOr, marker, depth, state)
		if ended {
			r = r.uncertain()
		}

		next := state.after(andOr)

		// The AND-OR lists after the hook run, and the last one gives the
		// status of the list. They keep exit status 2 only when an exit
		// command ends the shell with it.
		if r.passes && !r.exits && i+1 < len(list) {
			r = r.follow(runList(list[i+1:], blockingStatus, next))
		}

		result = result.or(r)

		// An exit or an exec that runs ends the shell. With errexit, a
		// failing hook ends it too, and the reader cannot tell whether the
		// hook fails. So does a "false" that the list runs last. Another
		// command counts as one that exits 0.
		_, exits := runAndOr(andOr, 0, 0, state)
		ended = ended || ends || (state.errexit && r.use != HookNotRun && !andOr.background) || (exits && !andOr.background)
		state = next
	}

	return result
}

// andOrUse classifies an AND-OR list: pipelines joined by "&&" and "||".
// A command other than the hook counts as one that exits 0, so "&&" runs
// the next pipeline and "||" skips it. "false" and an exit with another
// status always fail: "&&" then skips the next pipeline and "||" runs
// it. A pipeline that an operator skips runs only on a condition, and
// it leaves the status as it is. So does every pipeline after one that
// runs the hook, because the hook can exit with any status. ends is
// true when the list surely ends the shell: it runs an exit or an exec
// with a command. The pipelines after that one run only on a condition.
func andOrUse(andOr shellAndOr, marker []string, depth int, state shellState) (result hookResult, ends bool) {
	// zero is true while the status before the next operator is 0, and
	// nonzero while it is surely not 0.
	zero, nonzero := true, false

	for j, pipeline := range andOr.pipelines {
		runs := j == 0 || (zero && andOr.ops[j-1] == "&&") || (nonzero && andOr.ops[j-1] == "||")
		inner := state.inAndOr(andOr, j)

		r := pipelineUse(pipeline, marker, depth, inner)
		if !runs || ends || state.uncertain {
			r = r.uncertain()
		}

		// Only a pipeline that runs changes the status. The status model
		// gives it: a pipeline that holds the hook has an unknown status,
		// and any other pipeline the status that runPipeline finds, so
		// "(false)", "{ false; }", and "(exit 1)" fail like "false".
		status, exits := runPipeline(pipeline, 0, inner)

		if runs {
			zero, nonzero = r.use == HookNotRun && status == 0, r.use == HookNotRun && status != 0
		}

		if runs && exits {
			ends = true
		}

		// The pipelines after the hook decide the status, by the POSIX sh
		// "AND-OR Lists" rule. "&&" skips the next pipeline, and the status
		// stays. "||" runs the next pipeline, and its status replaces the
		// status of the hook.
		if r.passes && !r.exits {
			r = r.follow(runAndOr(andOr, j+1, blockingStatus, state))
		}

		result = result.or(r)

		// A set or an exec in this pipeline changes the state of the
		// pipelines after it, which run on a condition.
		state = state.maybe(state.afterPipeline(pipeline))
	}

	// The shell does not wait for a list in the background. The client
	// can miss its output, and its exit status never reaches the client.
	if andOr.background {
		result = result.uncertain()
		result.passes, result.exits = false, false

		return result, false
	}

	return result, ends
}

// pipelineUse classifies a pipeline: commands joined by "|". A command
// after the first reads the output of the command before it, not the
// client's payload. The status of a pipeline is the status of its last
// command. With pipefail, it is the status of the last command that
// fails. That is the hook only when every command after it exits 0, by
// the rule of runPipeline.
func pipelineUse(pipeline shellPipeline, marker []string, depth int, state shellState) hookResult {
	var result hookResult

	last := len(pipeline) - 1

	for k, command := range pipeline {
		r := commandUse(command, marker, depth, state)
		if k > 0 {
			r = r.uncertain()
		}

		r.passes = r.passes && (k == last || (state.pipefail && allExitZero(pipeline[k+1:], state)))
		// Each command of a longer pipeline runs in a subshell, so exec
		// ends only that subshell.
		r.exits = r.exits && last == 0

		result = result.or(r)
	}

	return result
}

// allExitZero reports whether each command of the pipeline part exits
// 0 in the status model. With pipefail, a later command that fails
// takes the status from the hook.
func allExitZero(commands shellPipeline, state shellState) bool {
	for _, command := range commands {
		if code, _ := runCommand(command, 0, state); code != 0 {
			return false
		}
	}

	return true
}

// commandUse classifies one command of a pipeline. A redirection of
// standard input replaces the client's payload, so the run is uncertain.
func commandUse(command shellCommand, marker []string, depth int, state shellState) hookResult {
	var r hookResult

	switch {
	case command.subshell != nil:
		// An exit in a subshell ends only the subshell.
		r = listUse(command.subshell, marker, depth, state)
		r.exits = false
	case command.group != nil:
		// A brace group runs in the shell itself, so an exit in it ends the
		// shell.
		r = listUse(command.group, marker, depth, state)
	default:
		r = argvUse(command.argv, marker, depth, state)
	}

	if command.stdin {
		r = r.uncertain()
	}

	return r
}

// The run functions model the commands that run after a run of the
// hook, to find the status that the whole part ends with. Each simple
// command other than exit and false counts as one that exits 0, as in
// SeamarkHookUse. An exit command ends the shell with its operand, or
// with the last status when it has none. exec with a command replaces
// the shell. The answer is the final status and whether the shell ended.

// runList runs the AND-OR lists of a list in order, from the status
// before them. The shell does not wait for a list in the background,
// and the status of such a list is 0 (POSIX sh "Asynchronous Lists").
func runList(list []shellAndOr, status int, state shellState) (int, bool) {
	for _, andOr := range list {
		if andOr.background {
			status = 0

			continue
		}

		var exits bool

		if status, exits = runAndOr(andOr, 0, status, state); exits {
			return status, true
		}
	}

	return status, false
}

// runAndOr runs the pipelines of an AND-OR list from index from on.
// "&&" runs the next pipeline after status 0, and "||" runs it after
// any other status.
func runAndOr(andOr shellAndOr, from, status int, state shellState) (int, bool) {
	last := from - 1

	for j := from; j < len(andOr.pipelines); j++ {
		if j > 0 && (andOr.ops[j-1] == "&&") != (status == 0) {
			continue
		}

		var exits bool

		if status, exits = runPipeline(andOr.pipelines[j], status, state.inAndOr(andOr, j)); exits {
			return status, true
		}

		last = j
	}

	// errexit ends the shell when the last pipeline of the list fails.
	// bash(1) and POSIX sh ignore a failing pipeline that "&&" or "||"
	// follows.
	return status, state.errexit && status != 0 && last == len(andOr.pipelines)-1
}

// runPipeline runs one pipeline. Each command of a longer pipeline runs
// in a subshell, so an exit there ends only that subshell. The status is
// the status of the last command, or with pipefail the status of the
// last command that fails.
func runPipeline(pipeline shellPipeline, status int, state shellState) (int, bool) {
	if len(pipeline) == 1 {
		return runCommand(pipeline[0], status, state)
	}

	final := 0

	for k, command := range pipeline {
		code, _ := runCommand(command, status, state)
		if (state.pipefail && code != 0) || (!state.pipefail && k == len(pipeline)-1) {
			final = code
		}
	}

	return final, false
}

// runCommand runs one command of a pipeline.
func runCommand(command shellCommand, status int, state shellState) (int, bool) {
	switch {
	case command.subshell != nil:
		code, _ := runList(command.subshell, status, state)

		return code, false
	case command.group != nil:
		return runList(command.group, status, state)
	case len(command.argv) == 0:
		// A command of redirections alone exits 0.
		return 0, false
	case command.argv[0] == "exit":
		if len(command.argv) == 1 {
			return status, true
		}

		code, err := strconv.Atoi(command.argv[1])
		if err != nil {
			// The shells give different statuses for an operand that is not
			// a number. Keeping the status answers true when in doubt.
			return status, true
		}

		return code & 0xff, true
	case command.argv[0] == "exec" && len(command.argv) > 1:
		// exec ends the shell with the status of its command.
		if failsSurely(command.argv[1:]) {
			return 1, true
		}

		return 0, true
	case failsSurely(command.argv):
		return 1, false
	case command.argv[0] == "eval":
		// eval runs its script in the shell itself, so an exit in the
		// script ends the shell. A script that does not parse counts as a
		// command that exits 0, like any other command.
		if list, deeper, ok := evalScript(command.argv, state); ok {
			return runList(list, status, deeper)
		}

		return 0, false
	default:
		return 0, false
	}
}

// evalScript parses the script of an eval command: its arguments joined
// with blanks, as eval joins them. It returns the state of the script,
// one level deeper, and false for a script that the status model does
// not follow: one with an expansion, one that does not parse, and one
// below maxShellDepth. The bound matters: a chain of evals re-parses
// the script at each level, and a hook command is untrusted input.
func evalScript(argv []string, state shellState) ([]shellAndOr, shellState, bool) {
	if state.depth >= maxShellDepth {
		return nil, state, false
	}

	list, expands, ok := state.parse(strings.Join(argv[1:], " "))
	if !ok || expands {
		return nil, state, false
	}

	state.depth++

	return list, state, true
}

// execPrefixes are programs that run the rest of their arguments as a
// command, each with the options that take the next word as their
// value. The list is short on purpose: a program outside it gives
// HookMayRun, never a wrong HookRuns. The options come from the GNU
// manuals and from bash(1) for exec. An option outside the list that
// takes a value makes its value read as an unknown program, which gives
// HookMayRun too.
var execPrefixes = map[string][]string{
	"env":     {"-u", "--unset", "-C", "--chdir", "-a", "--argv0"},
	"exec":    {"-a"},
	"command": nil,
	"nohup":   nil,
	"nice":    {"-n", "--adjustment"},
	"time":    {"-f", "--format", "-o", "--output"},
	"timeout": {"-s", "--signal", "-k", "--kill-after"},
	"busybox": nil,
}

// shells are the programs whose -c argument is a script.
var shells = []string{"sh", "bash", "zsh", "dash", "ksh"}

// duration matches the first operand of timeout, such as "5" or "1.5m".
var duration = regexp.MustCompile(`^\d+(\.\d+)?[smhd]?$`)

// assignment matches a variable assignment in front of a command word.
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// argvUse classifies one simple command. state holds the options of the
// shell that runs it, for a script that eval runs in that shell.
func argvUse(argv, marker []string, depth int, state shellState) hookResult {
	if len(argv) == 0 {
		return hookResult{}
	}

	program := filepath.Base(argv[0])
	options, isPrefix := execPrefixes[program]

	switch {
	case IsSeamarkBinary(argv[0]):
		if slices.Equal(argv[1:], marker) {
			return hookResult{use: HookRuns, passes: true}
		}

		return hookResult{}
	case slices.Contains(shells, program):
		return shellUse(argv, program == "bash", marker, depth, state)
	case program == "eval":
		// eval joins its arguments with blanks and runs them as a script,
		// in the shell itself, with the options of that shell. The status
		// model inside the script starts a level deeper too.
		state.depth++

		return hookUse(strings.Join(argv[1:], " "), marker, depth+1, state)
	case isPrefix:
		return prefixUse(program, options, argv, marker, depth, state)
	case mentionsHook(argv[1:], marker):
		// An unknown program gets the seamark command as its arguments. A
		// wrapper runs them, and echo prints them. The reader cannot tell
		// what the program returns.
		return cannotTell
	default:
		return hookResult{}
	}
}

// prefixUse classifies a command that an exec prefix runs. The prefix
// reads its options first, each with its value. Only env takes an
// assignment there. "--" ends the options. The first word after them
// is the command, with two exceptions. The first operand of timeout is
// the duration. The first operand of busybox names the applet, and only
// a shell or another prefix is an applet that the reader knows. Another
// word there is an unknown program, which gives HookMayRun.
func prefixUse(program string, options, argv, marker []string, depth int, state shellState) hookResult {
	parts := splitPrefix(program, options, argv)

	switch {
	case parts.prints:
		return hookResult{}
	case parts.splits:
		return envSplitUse(parts.split, parts.rest, marker, depth, state)
	case parts.unknown:
		return unknownProgramUse(argv, marker)
	}

	r := argvUse(parts.rest, marker, depth, state)
	// Each prefix returns the status of the command it runs. exec also
	// replaces the shell, so that status ends the shell.
	r.exits = r.exits || program == "exec"

	return r
}

// prefixParts is what an exec prefix does with its arguments.
type prefixParts struct {
	// rest is the command that the prefix runs, or after a split the
	// words after the split string.
	rest []string
	// prints is true for "command -v" and "command -V", which run nothing.
	prints bool
	// splits is true for "env -S"; split is then its string.
	splits bool
	split  string
	// unknown is true when the reader cannot tell which word is the
	// command: timeout without a duration, or busybox with an applet
	// that the reader does not know.
	unknown bool
}

// splitPrefix reads the arguments of an exec prefix: its options, each
// with its value, and "--". The use walk and the status model share it,
// so both see the same command behind "env", "command", or "timeout".
func splitPrefix(program string, options, argv []string) prefixParts {
	i := 1

	for ; i < len(argv); i++ {
		word := argv[i]

		if word == "--" {
			i++

			break
		}

		if !strings.HasPrefix(word, "-") && (program != "env" || !assignment.MatchString(word)) {
			break
		}

		switch {
		case program == "command" && !strings.HasPrefix(word, "--") && strings.ContainsAny(word, "vV"):
			return prefixParts{prints: true}
		case program == "env" && (word == "-S" || word == "--split-string") && i+1 < len(argv):
			return prefixParts{splits: true, split: argv[i+1], rest: argv[i+2:]}
		case program == "env" && strings.HasPrefix(word, "-S"):
			return prefixParts{splits: true, split: word[2:], rest: argv[i+1:]}
		case program == "env" && strings.HasPrefix(word, "--split-string="):
			return prefixParts{splits: true, split: strings.TrimPrefix(word, "--split-string="), rest: argv[i+1:]}
		case slices.Contains(options, word):
			i++
		}
	}

	rest := argv[i:]

	switch {
	case program == "timeout" && len(rest) > 0 && duration.MatchString(rest[0]):
		rest = rest[1:]
	case program == "timeout":
		return prefixParts{unknown: true}
	case program == "busybox" && len(rest) > 0 && isApplet(rest[0]):
		// busybox takes the last path component of its first operand as
		// the applet name, so "busybox /bin/sh" runs sh.
		rest = append([]string{filepath.Base(rest[0])}, rest[1:]...)
	case program == "busybox":
		return prefixParts{unknown: true}
	}

	return prefixParts{rest: rest}
}

// failsSurely reports whether a simple command surely exits with a
// status other than 0: "false" by any path, also behind an exec prefix
// such as "command" or "env". A prefix that prints, splits, or runs an
// unknown word gives no sure status.
func failsSurely(argv []string) bool {
	for len(argv) > 0 {
		program := filepath.Base(argv[0])
		if program == "false" {
			return true
		}

		options, isPrefix := execPrefixes[program]
		if !isPrefix {
			return false
		}

		parts := splitPrefix(program, options, argv)
		if parts.prints || parts.splits || parts.unknown {
			return false
		}

		argv = parts.rest
	}

	return false
}

// isApplet reports whether the word names a busybox applet that the
// reader knows: a shell or an exec prefix.
func isApplet(word string) bool {
	name := filepath.Base(word)
	_, isPrefix := execPrefixes[name]

	return isPrefix || slices.Contains(shells, name)
}

// unknownProgramUse is the result for a command whose program the reader
// does not know. It is cannotTell when the seamark command is among the
// arguments, and no run otherwise.
func unknownProgramUse(argv, marker []string) hookResult {
	if mentionsHook(argv[1:], marker) {
		return cannotTell
	}

	return hookResult{}
}

// envSplitUse classifies the string of "env -S" with the words after
// it. env splits the string into words by the shell quoting rules and
// puts the words in front of the remaining arguments. Then it reads
// them as its own arguments again: options, assignments, then the
// command. A string that holds an operator, an expansion, a
// redirection, or only assignments is not one command. It gets the
// loose test.
func envSplitUse(script string, rest, marker []string, depth int, state shellState) hookResult {
	text := strings.TrimSpace(script + " " + strings.Join(rest, " "))

	if depth > maxShellDepth {
		return looseUse(text, marker)
	}

	list, expands, ok := state.parse(script)
	if !ok || expands || len(list) != 1 || len(list[0].pipelines) != 1 || len(list[0].pipelines[0]) != 1 {
		return looseUse(text, marker)
	}

	command := list[0].pipelines[0][0]
	if command.argv == nil || command.stdin {
		return looseUse(text, marker)
	}

	argv := append([]string{"env"}, command.argv...)

	return argvUse(append(argv, rest...), marker, depth+1, state)
}

// mentionsHook reports whether the words hold a seamark binary word
// directly followed by the marker words and nothing more.
func mentionsHook(words, marker []string) bool {
	for i, word := range words {
		if IsSeamarkBinary(word) && slices.Equal(words[i+1:], marker) {
			return true
		}
	}

	return false
}

// shellUse classifies a shell invocation. With -c, the first operand
// after the options is the script. The words after it become $0, $1,
// and so on, and the script reaches them only through an expansion.
// Without -c, the first operand is a script file, or -s reads the script
// from standard input. The operands are then the arguments of an
// unknown script. bash is true for the options of bash(1). parent is
// the state of the shell around the invocation; the new shell starts
// with its own options and shares the parse memo.
func shellUse(argv []string, bash bool, marker []string, depth int, parent shellState) hookResult {
	state := shellState{cache: parent.cache}

	operand, command, ok := readOptions(argv[1:], &state, bash)
	if !ok {
		// The reader cannot tell which word is the script. A missed handler
		// and a second handler both cost, and HookMayRun keeps the install
		// with a warning.
		return looseUse(strings.Join(argv[1:], " "), marker)
	}

	operands := argv[1+operand:]

	switch {
	case !command:
		return looseUse(strings.Join(operands, " "), marker)
	case len(operands) == 0:
		// -c without a script is an error, and the shell runs nothing.
		return hookResult{}
	}

	// One parse serves the script and the check of its operands.
	list, expands, read := state.parse(operands[0])
	r := scriptUse(operands[0], list, expands, read, marker, depth+1, state)

	// A script with an expansion such as "$0" or "$@" can run the words
	// after it, and the seamark word and the marker can then stand apart:
	// `sh -c '"$0" gate --hook' seamark` and `sh -c 'seamark "$@"' _ gate
	// --hook` both run the hook. The loose test reads the script and the
	// operands together, and it does not ask the words to be adjacent.
	if (expands || !read) && looseMentionApart(strings.Join(operands, " "), marker) {
		r = r.or(cannotTell)
	}

	// The script ends only the nested shell. The command around it goes
	// on with the status of that shell.
	r.exits = false

	return r
}

// bashLongOptions are the long options of bash(1) without an argument.
var bashLongOptions = []string{
	"--debugger", "--dump-po-strings", "--dump-strings", "--help", "--login", "--noediting",
	"--noprofile", "--norc", "--posix", "--pretty-print", "--restricted", "--verbose", "--version",
}

// bashFileOptions are the long options of bash(1) that take the next
// word as a file name.
var bashFileOptions = []string{"--init-file", "--rcfile"}

// readOptions reads the option words at the start of words into state,
// and returns the index of the first operand. The shell invocation and
// the set builtin share these rules, from the POSIX sh synopsis and the
// bash(1) INVOCATION section:
//
//   - A word of one "-" or "+" and letters holds short options. Each "o"
//     takes the next word as an option name, and so does each "O" of
//     bash. "c" makes the first operand the script.
//   - Only bash takes the long options here, and only before the short
//     ones. "--rcfile" and "--init-file" take the next word as a file.
//   - "--" or "-" ends the options. Otherwise the first word that does not
//     start with "-" or "+" is the first operand.
//
// bash is true for the bash rules. command is true for a -c option. ok
// is false for a word that the reader cannot classify with certainty.
// The reader checks the option syntax only, not whether the shell knows
// an option name.
func readOptions(words []string, state *shellState, bash bool) (operand int, command, ok bool) {
	short := false

	for i := 0; i < len(words); i++ {
		word := words[i]

		switch {
		case word == "--" || word == "-":
			return i + 1, command, true
		case strings.HasPrefix(word, "--"):
			// The long options differ between shells: dash rejects them, and
			// zsh and ksh have their own. bash rejects a long option after a
			// short one.
			if !bash || short {
				return i, command, false
			}

			switch {
			case slices.Contains(bashFileOptions, word):
				i++
				if i >= len(words) {
					return i, command, false
				}
			case !slices.Contains(bashLongOptions, word):
				return i, command, false
			}
		case len(word) > 1 && (word[0] == '-' || word[0] == '+'):
			short = true
			on := word[0] == '-'

			for _, letter := range word[1:] {
				switch letter {
				case 'c':
					// bash, dash, ksh, and zsh read "+c" as "-c": the sign does
					// not change it.
					command = true
				case 'e':
					state.errexit = on
				case 'n':
					state.noexec = on
				case 'O':
					// Only bash takes "-O name". Another shell rejects it, or
					// reads it in a way that this reader does not know.
					if !bash {
						return i, command, false
					}

					i++
					if i >= len(words) {
						return i, command, false
					}
				case 'o':
					i++
					if i >= len(words) {
						return i, command, false
					}

					state.option(words[i], on)
				}
			}
		default:
			return i, command, true
		}
	}

	return len(words), command, true
}

// shellState holds what a shell carries from one command to the next
// and that changes how the hook runs.
type shellState struct {
	// errexit ends the shell when a command fails: "set -e".
	errexit bool
	// pipefail gives a pipeline the status of its last failing command.
	pipefail bool
	// noexec makes the shell read the commands and run none: "set -n".
	noexec bool
	// uncertain is true when the commands after a point may not run the
	// hook with the client's payload. After "exec <file", they read the
	// file. After a "set -n" on a condition, they may not run.
	uncertain bool
	// depth counts the eval scripts that the status model has entered,
	// against maxShellDepth. It is not an option of the shell: the walk
	// carries it here because every run function already carries the
	// state.
	depth int
	// cache is the parse memo of the reading, shared by every copy of the
	// state. The use walk and the status model parse a nested script at
	// each level and for each marker, and a hook command is untrusted
	// input, so a long script must cost one parse. Nil means no memo.
	cache *parseCache
}

// parseCache memoizes shellList by the text of the script. The parsed
// forms are read-only after construction, so the walks can share them.
type parseCache struct {
	scripts map[string]parsedScript
}

// parsedScript is one result of shellList.
type parsedScript struct {
	list    []shellAndOr
	expands bool
	ok      bool
}

// parse is shellList through the memo of the state, when it has one.
func (s shellState) parse(cmd string) ([]shellAndOr, bool, bool) {
	if s.cache == nil {
		return shellList(cmd)
	}

	if p, seen := s.cache.scripts[cmd]; seen {
		return p.list, p.expands, p.ok
	}

	list, expands, ok := shellList(cmd)
	s.cache.scripts[cmd] = parsedScript{list: list, expands: expands, ok: ok}

	return list, expands, ok
}

// option sets the named option of "-o name" or "+o name". Options other
// than errexit, pipefail, and noexec do not change how the hook runs.
func (s *shellState) option(name string, on bool) {
	switch name {
	case "errexit":
		s.errexit = on
	case "pipefail":
		s.pipefail = on
	case "noexec":
		s.noexec = on
	}
}

// after returns the state that an AND-OR list leaves for the lists after
// it. A list in the background runs in a subshell and changes nothing.
// The pipelines of a longer AND-OR list run on a condition, so their
// changes merge by the rule of maybe.
func (s shellState) after(andOr shellAndOr) shellState {
	switch {
	case andOr.background:
		return s
	case len(andOr.pipelines) == 1:
		return s.afterPipeline(andOr.pipelines[0])
	}

	for _, pipeline := range andOr.pipelines {
		s = s.maybe(s.afterPipeline(pipeline))
	}

	return s
}

// afterPipeline returns the state that one pipeline leaves. Only a
// pipeline of one command runs in the shell itself. The command changes
// the state when it is set with options, or exec that only redirects
// standard input. So does a brace group or an eval script that holds
// such a command.
func (s shellState) afterPipeline(pipeline shellPipeline) shellState {
	if len(pipeline) != 1 {
		return s
	}

	command := pipeline[0]

	switch {
	case command.group != nil:
		for _, andOr := range command.group {
			s = s.after(andOr)
		}
	case len(command.argv) == 0:
	case command.argv[0] == "set":
		readOptions(command.argv[1:], &s, false)
	case command.argv[0] == "setopt", command.argv[0] == "unsetopt":
		s.zshOptions(command.argv[1:], command.argv[0] == "setopt")
	case command.argv[0] == "shopt":
		s.bashShopt(command.argv[1:])
	case command.argv[0] == "exec" && len(command.argv) == 1 && command.stdin:
		s.uncertain = true
	case command.argv[0] == "eval":
		if list, deeper, ok := evalScript(command.argv, s); ok {
			for _, andOr := range list {
				deeper = deeper.after(andOr)
			}

			// The script changes the options of the shell; the depth is the
			// walk's own, so the caller keeps its level.
			deeper.depth = s.depth
			s = deeper
		}
	}

	return s
}

// zshOptions reads the option names of zsh "setopt" and "unsetopt". zsh
// ignores case and underscores in a name, and a "no" in front of a name
// reverses its sense. The zsh name of noexec is NO_EXEC, so "setopt
// noexec" reads as EXEC off.
func (s *shellState) zshOptions(names []string, on bool) {
	for _, name := range names {
		name = strings.ReplaceAll(strings.ToLower(name), "_", "")
		set := on

		if rest, negated := strings.CutPrefix(name, "no"); negated {
			name, set = rest, !on
		}

		switch name {
		case "errexit":
			s.errexit = set
		case "pipefail":
			s.pipefail = set
		case "exec":
			s.noexec = !set
		}
	}
}

// bashShopt reads bash "shopt -s -o name" and "shopt -u -o name", which
// set and unset the options of "set -o". Without "-o", shopt changes
// the options of its own, which do not change how the hook runs.
func (s *shellState) bashShopt(words []string) {
	flags := ""

	var names []string

	for _, word := range words {
		if strings.HasPrefix(word, "-") {
			flags += word[1:]
		} else {
			names = append(names, word)
		}
	}

	if !strings.Contains(flags, "o") || !strings.ContainsAny(flags, "su") {
		return
	}

	for _, name := range names {
		s.option(name, strings.Contains(flags, "s"))
	}
}

// maybe merges the state that a command on a condition can leave. The
// reader does not know whether the command runs. An option that keeps
// the exit status therefore counts as on, and a replaced input or a
// "set -n" makes the later runs uncertain. Both keep the answers on the
// side that costs less: HookMayRun, and a status that passes.
func (s shellState) maybe(other shellState) shellState {
	s.errexit = s.errexit || other.errexit
	s.pipefail = s.pipefail || other.pipefail
	s.uncertain = s.uncertain || other.uncertain || (other.noexec && !s.noexec)

	return s
}

// inAndOr returns the state for pipeline j of an AND-OR list. bash(1)
// ignores errexit in each pipeline that "&&" or "||" follows. It ignores
// it in every command of a brace group or a subshell there too.
func (s shellState) inAndOr(andOr shellAndOr, j int) shellState {
	s.errexit = s.errexit && j == len(andOr.pipelines)-1

	return s
}
