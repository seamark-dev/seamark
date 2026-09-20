package hooks

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// HookUse says whether a hook command runs a seamark hook.
type HookUse int

// The answers of SeamarkHookUse.
const (
	// HookNotRun means the command does not run the hook. The hook text
	// can still occur in it, for example as an argument of echo.
	HookNotRun HookUse = iota
	// HookMayRun means the command names the seamark binary and the hook
	// arguments as words of another program, or uses shell syntax that
	// this package does not read. The other program can run them or not.
	HookMayRun
	// HookRuns means the shell executes the seamark binary with exactly
	// the hook arguments.
	HookRuns
)

// SeamarkHookUse reads a hook command the way a shell does: it splits
// the command into words with the quoting rules, and into simple
// commands at the control operators. The answer depends on what the
// shell executes, not on which text occurs.
//
// The two wrong answers do not cost the same. A false HookRuns makes
// setup install nothing, and the user loses the hook. A false
// HookNotRun makes setup install a second handler. The function
// therefore answers HookRuns only for the forms it fully reads, and
// HookMayRun for the rest.
//
// A marker matches only as the complete argument list. "lessons --hook"
// is a prefix of the Codex marker and of the reset marker, and a longer
// argument list is another hook.
func SeamarkHookUse(cmd string, markers []string) HookUse {
	use := HookNotRun

	for _, marker := range markers {
		use = max(use, hookUse(cmd, strings.Fields(marker), 0))
	}

	return use
}

// maxShellDepth bounds the nesting of `sh -c` scripts that hookUse
// follows. A deeper script gives HookMayRun.
const maxShellDepth = 3

// hookUse is SeamarkHookUse for one marker, already split into words.
func hookUse(cmd string, marker []string, depth int) HookUse {
	// Substitutions, here-documents, and variables change what runs in a
	// way that a word reader cannot follow.
	unread := strings.ContainsAny(cmd, "`$") || strings.Contains(cmd, "<<")

	commands, ok := shellCommands(cmd)
	if !ok || unread || depth > maxShellDepth {
		// The words are not reliable here, so the test is loose: a seamark
		// word, then the marker words, wherever they stand. The answer is
		// only HookMayRun, which never stops an install.
		words := strings.Fields(cmd)

		for i, word := range words {
			rest := words[i+1:]
			if IsSeamarkBinary(strings.Trim(word, "'\"(")) && len(rest) >= len(marker) && slices.Equal(rest[:len(marker)], marker) {
				return HookMayRun
			}
		}

		return HookNotRun
	}

	use := HookNotRun

	for _, argv := range commands {
		use = max(use, argvUse(argv, marker, depth))
	}

	return use
}

// execPrefixes are programs that run the rest of their arguments as a
// command. The list is short on purpose: a program outside it gives
// HookMayRun, never a wrong HookRuns.
var execPrefixes = []string{"env", "exec", "command", "nohup", "nice", "time", "timeout"}

// shells are the programs whose -c argument is a script.
var shells = []string{"sh", "bash", "zsh", "dash", "ksh"}

// argvUse classifies one simple command.
func argvUse(argv, marker []string, depth int) HookUse {
	if len(argv) == 0 {
		return HookNotRun
	}

	program := filepath.Base(argv[0])

	switch {
	case IsSeamarkBinary(argv[0]):
		if slices.Equal(argv[1:], marker) {
			return HookRuns
		}

		return HookNotRun
	case slices.Contains(shells, program):
		// The script is the word after the first option that holds "c".
		for i, word := range argv[1:] {
			if strings.HasPrefix(word, "-") && strings.Contains(word, "c") && i+2 < len(argv) {
				return hookUse(argv[i+2], marker, depth+1)
			}
		}

		return HookNotRun
	case slices.Contains(execPrefixes, program):
		// The prefix takes options and operands of its own, so the seamark
		// word can stand anywhere after it. The words from there on are the
		// command that the prefix runs.
		for i := 1; i < len(argv); i++ {
			if IsSeamarkBinary(argv[i]) {
				return argvUse(argv[i:], marker, depth)
			}
		}

		return HookNotRun
	case mentionsHook(argv[1:], marker):
		// An unknown program gets the seamark command as its arguments. A
		// wrapper runs them, and echo prints them.
		return HookMayRun
	default:
		return HookNotRun
	}
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

// redirect matches one redirection word: an optional descriptor, the
// operator, and an optional attached target such as "&1" or a path.
var redirect = regexp.MustCompile(`^\d*(>>|>|<|&>)(.*)$`)

// assignment matches a variable assignment in front of a command word.
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellCommands splits a command line into its simple commands, each as
// its words after quote removal. It drops the redirections and the
// leading variable assignments, because neither changes which program
// runs. ok is false for an unclosed quote.
func shellCommands(cmd string) (commands [][]string, ok bool) {
	tokens, ok := shellTokens(cmd)
	if !ok {
		return nil, false
	}

	var argv []string

	flush := func() {
		if len(argv) > 0 {
			commands = append(commands, argv)
		}

		argv = nil
	}

	for i := 0; i < len(tokens); i++ {
		token := tokens[i]

		switch {
		case token.operator:
			flush()
		case !token.quoted && redirect.MatchString(token.text):
			// A bare operator takes the next word as its target.
			if redirect.FindStringSubmatch(token.text)[2] == "" {
				i++
			}
		case !token.quoted && len(argv) == 0 && assignment.MatchString(token.text):
			// An assignment in front of the command word sets a variable.
		default:
			argv = append(argv, token.text)
		}
	}

	flush()

	return commands, true
}

// shellToken is one word or one control operator of a command line.
type shellToken struct {
	text     string
	operator bool // a control operator: ; & && | || ( ) or a newline
	quoted   bool // a part of the word was quoted or escaped
}

// shellTokens splits a command line by the POSIX quoting rules. Single
// quotes keep every character. Double quotes keep every character
// except the backslash in front of a special character. A backslash
// outside quotes keeps the next character. ok is false for an unclosed
// quote.
func shellTokens(cmd string) (tokens []shellToken, ok bool) {
	var (
		word   strings.Builder
		inWord bool
		quoted bool
	)

	flush := func() {
		if inWord {
			tokens = append(tokens, shellToken{text: word.String(), quoted: quoted})
		}

		word.Reset()

		inWord, quoted = false, false
	}

	runes := []rune(cmd)

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case r == '\'':
			end := slices.Index(runes[i+1:], '\'')
			if end < 0 {
				return nil, false
			}

			word.WriteString(string(runes[i+1 : i+1+end]))

			inWord, quoted = true, true
			i += end + 1
		case r == '"':
			inWord, quoted = true, true

			for i++; ; i++ {
				if i >= len(runes) {
					return nil, false
				}

				if runes[i] == '"' {
					break
				}

				if runes[i] == '\\' && i+1 < len(runes) && strings.ContainsRune("\"\\$`", runes[i+1]) {
					i++
				}

				word.WriteRune(runes[i])
			}
		case r == '\\' && i+1 < len(runes):
			i++
			word.WriteRune(runes[i])

			inWord, quoted = true, true
		case r == ' ' || r == '\t':
			flush()
		case strings.ContainsRune(";|()\n", r) || (r == '&' && !redirectAmpersand(runes, i, word.String())):
			flush()

			tokens = append(tokens, shellToken{operator: true})
		default:
			word.WriteRune(r)

			inWord = true
		}
	}

	flush()

	return tokens, true
}

// redirectAmpersand reports whether the "&" at position i belongs to a
// redirection such as "2>&1" or "&>file", and is no control operator.
func redirectAmpersand(runes []rune, i int, word string) bool {
	if strings.HasSuffix(word, ">") || strings.HasSuffix(word, "<") {
		return true
	}

	return word == "" && i+1 < len(runes) && runes[i+1] == '>'
}
