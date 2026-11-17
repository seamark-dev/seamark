package integration

// Tests of how scripts/native-record.sh prints text. The script runs under
// a plain POSIX sh. The echo of some of those shells expands backslash
// escapes, and "\c" ends its output. A client text, a path, or an
// environment value can hold a backslash, so the script prints each value
// with printf '%s\n'. These tests run the helpers under sh and scan the
// script text, so they need no model and no client CLI.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNativeRecordHelpersPrintTextAsGiven proves that ok, fail, blocked
// and note print a text with backslash escapes, a printf directive, or a
// leading dash as given, on one line after the prefix.
func TestNativeRecordHelpersPrintTextAsGiven(t *testing.T) {
	texts := []string{`API Error: path C:\new\table and \c end`, `100% %s %d`, `-n`}

	helpers := []struct{ name, prefix string }{
		{"ok", "  ok       "},
		{"fail", "  FAIL     "},
		{"blocked", "  blocked  "},
		{"note", "  note     "},
	}

	var program, want strings.Builder

	for _, helper := range helpers {
		for _, text := range texts {
			program.WriteString(helper.name + " '" + text + "'\n")
			want.WriteString(helper.prefix + text + "\n")
		}
	}

	program.WriteString(`printf 'counts=%s %s %s\n' "$OK" "$FAILED" "$BLOCKED"` + "\n")
	want.WriteString("counts=3 3 3\n")

	got, code := recordScript(t, program.String())
	require.Equal(t, 0, code, got)

	assert.Equal(t, want.String(), got)
}

// TestNativeRecordHelpersPrintToTheirStreams proves that fail prints to
// stderr, and that ok, blocked and note print to stdout. A caller that
// sends stdout to a file then still sees each FAIL line.
func TestNativeRecordHelpersPrintToTheirStreams(t *testing.T) {
	calls := "{ ok a; fail b; blocked c; note d; }"

	program := "printf 'stdout:\\n'\n" + calls + " 2>/dev/null\n" +
		"printf 'stderr:\\n'\n" + calls + " >/dev/null\n"

	got, code := recordScript(t, program)
	require.Equal(t, 0, code, got)

	assert.Equal(t, "stdout:\n  ok       a\n  blocked  c\n  note     d\nstderr:\n  FAIL     b\n", got)
}

// TestNativeRecordPrintsValuesWithPrintf proves that no echo command of
// the script prints a value. An echo with fixed text prints the same in
// every shell, so the scan allows it.
func TestNativeRecordPrintsValuesWithPrintf(t *testing.T) {
	t.Run("the scan finds a value echo", func(t *testing.T) {
		// Each line proves one part of the scan. The scan must return
		// exactly the lines with flag set. If one part stops working,
		// the scan returns a different list of lines.
		lines := []struct {
			text string
			flag bool
		}{
			{`    echo "$start"`, true},                   // at the line start
			{`echo fixed; echo "$semi"`, true},            // after ";"
			{`true && echo "$and"`, true},                 // after "&&"
			{`false || echo "$or"`, true},                 // after "||"
			{`printf x | echo "$pipe"`, true},             // after "|"
			{`x() { echo "$brace"; }`, true},              // after "{"
			{`*) echo "$paren" >&2; exit 2 ;;`, true},     // after ")"
			{`if a; then echo "$then"; fi`, true},         // after "then"
			{`for f in a; do echo "$do"; done`, true},     // after "do"
			{`if a; then :; else echo "$else"; fi`, true}, // after "else"
			{`echo "$one"; echo "$two"`, true},            // one line number for two echoes
			{`[ -n "$a" ] || echo off`, false},            // fixed text
			{`echo fixed; x=$y`, false},                   // the value is in the next command
			{`echo fixed && x=$y`, false},                 // the value is in the next command
			{`echo fixed || x=$y`, false},                 // the value is in the next command
			{`echoes "$z"`, false},                        // another command name
			{`undo echo "$u"`, false},                     // "do" only as a full word
			{`xthen echo "$v"`, false},                    // "then" only as a full word
			{`xelse echo "$w"`, false},                    // "else" only as a full word
			{`# x; echo "$comment"`, false},               // a comment line
			{`cat > f <<EOF`, false},                      // an unquoted heredoc start
			{`echo "$body"`, false},                       // a heredoc body line
			{`EOF`, false},                                // the heredoc end word
			{`cat > g <<'PY'`, false},                     // a quoted heredoc start
			{`echo "$quoted"`, false},                     // a heredoc body line
			{`PY`, false},                                 // the heredoc end word
			{`echo "$after"`, true},                       // a command after the heredoc
		}

		var sample strings.Builder

		var want []int

		for i, line := range lines {
			sample.WriteString(line.text + "\n")

			if line.flag {
				want = append(want, i+1)
			}
		}

		assert.Equal(t, want, valueEchoLines(sample.String()))
	})

	t.Run("the script", func(t *testing.T) {
		lines := valueEchoLines(nativeRecordScript(t))

		names := make([]string, 0, len(lines))
		for _, line := range lines {
			names = append(names, strconv.Itoa(line))
		}

		assert.Empty(t, lines, "use printf '%%s\\n' on lines %s", strings.Join(names, ", "))
	})
}

// echoCommand finds echo at a command position: the line start, or after
// a separator or a keyword that starts a command. The single "|" also
// matches the second bar of "||", so "||" needs no separate alternative.
var echoCommand = regexp.MustCompile(`(^|;|&&|\||\{|\)|\bthen|\bdo|\belse)\s*echo\b`)

// heredocStart finds a heredoc operator at the end of a line and captures
// its end word.
var heredocStart = regexp.MustCompile(`<<-?\s*'?([A-Za-z_]+)'?\s*$`)

// commandEnd splits the words of one command from the next command.
var commandEnd = regexp.MustCompile(`;|&&|\|\|`)

// valueEchoLines returns the 1-based numbers of the lines with an echo
// command whose words expand a "$" value. It skips comment lines and
// heredoc bodies, because they are text, not commands of the script.
func valueEchoLines(script string) []int {
	var hits []int

	heredocEnd := ""

	for i, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)

		if heredocEnd != "" {
			if trimmed == heredocEnd {
				heredocEnd = ""
			}

			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		if m := heredocStart.FindStringSubmatch(trimmed); m != nil {
			heredocEnd = m[1]
		}

		for _, loc := range echoCommand.FindAllStringIndex(trimmed, -1) {
			words := commandEnd.Split(trimmed[loc[1]:], 2)[0]
			if strings.Contains(words, "$") {
				hits = append(hits, i+1)

				break
			}
		}
	}

	return hits
}
