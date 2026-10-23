// Package redact scrubs secret-shaped values from text seamark stores
// or displays: audited command lines (the gate's opt-in raw log),
// mined review-comment bodies, and fix-commit patches. It is
// best-effort pattern scrubbing, not a guarantee — but a credential a
// reviewer quoted once must not be re-broadcast into agent context on
// every edit, which is exactly what an unredacted finding does.
//
// A leaf package: the gate and the miners (reviews, fixes) must scrub
// identically, and none of them may import another for it.
package redact

import (
	"regexp"
	"strings"
)

// redaction is one secret-shaped pattern and its replacement.
type redaction struct {
	re          *regexp.Regexp
	replacement string
}

// secretValue matches a flag or assignment payload: a quoted string
// (whitespace and escaped quotes included — `--password 'correct horse
// battery staple'` and PASSWORD="ab\"cd" must not leak their tails) or
// one unquoted token. The unquoted branch refuses to start at `=`, so
// a comparison (`password == provided`) is never mistaken for an
// assignment and mangled.
const secretValue = `('(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"|[^\s=]\S*)`

// secretNames are the identifier fragments that mark a name as
// secret-bearing. colonNames drops AUTH: an assignment (`AUTH=x`) is
// unambiguous, but review prose opens sentences with "auth:" all the
// time, and mangling a comment corrupts the very text a lesson needs.
const (
	secretNames = `(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|CREDENTIALS?|AUTH|ACCESS_?KEY|PRIVATE_?KEY)`
	colonNames  = `(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|CREDENTIALS?|ACCESS_?KEY|PRIVATE_?KEY)`
)

// termEscape matches a CSI (ESC "[" or U+009B, as in "\x1b[31m") or a
// short escape (as in "\x1bc" or "\x1b(B"). The final byte can be a word
// character or another byte. After a word character, "\b" finds no
// boundary before the next word. render.Sanitize drops only the ESC
// byte, so a token after the escape reaches the screen. OSC and DCS
// strings end in BEL or ST, and "\b" holds after them. The hex ranges
// are the ECMA-48 parameter (0x30–0x3F), intermediate (0x20–0x2F), and
// final (0x40–0x7E or 0x30–0x7E) bytes.
//
// RE2 takes the leftmost match. So the escape branch wins even where
// "\b" also holds, and the match then starts at the escape. leadGroup
// names that branch, and SecretsExcept removes it from the text it
// gives keep.
const termEscape = `(?:(?:\x1b\[|\x{9b})[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b[\x20-\x2f]*[\x30-\x7e])`

// leadGroup names the group that holds a termEscape before a keyword.
// The group is empty when "\b" starts the match.
const leadGroup = "lead"

// redactions cover the common ways secrets ride in command lines,
// review comments, and patches. Pattern scrubbing is best-effort by
// design — entropy detection of bare token literals is deliberately
// out: it cannot be precise, and a false positive corrupts the very
// text a lesson needs.
var redactions = []redaction{
	// Assignments whose name smells secret-bearing, in shell, env, and
	// code shapes: MY_TOKEN=abc, API_TOKEN = "abc", dbPassword := "x".
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*` + secretNames + `[A-Z0-9_]*\s*:?=\s*)` + secretValue),
		"${1}[REDACTED]"},
	// Value-taking secret flags: --password x, --token=x, --api-key x.
	{regexp.MustCompile(`(?i)(--?(?:password|passwd|token|api-?key|secret|access-?key|private-?key|auth(?:orization)?)[= ])` + secretValue),
		"${1}[REDACTED]"},
	// Config-file key/value forms: password: hunter2 (YAML),
	// "api_key": "abc" (JSON) — with colonNames, see above.
	{regexp.MustCompile(`(?i)(['"]?[A-Z0-9_.-]*` + colonNames + `[A-Z0-9_.-]*['"]?\s*:\s*)` + secretValue),
		"${1}[REDACTED]"},
	// URL userinfo: scheme://user:pass@host keeps user and host. The
	// password class excludes "/" (RFC 3986 requires it percent-encoded
	// in userinfo), or host:port/path@… — a URL whose PATH carries an
	// "@" — would read as credentials and be mangled.
	{regexp.MustCompile(`(://[^/\s@:]+:)[^@/\s]+@`), "${1}[REDACTED]@"},
	// Bearer tokens in headers: Authorization: Bearer eyJ…. A
	// termEscape can take the place of the word boundary. Group 1
	// (leadGroup) keeps that escape in the output. Text without an
	// escape matches as before.
	{regexp.MustCompile(`(?i)(?P<` + leadGroup + `>` + termEscape + `|\b)(bearer\s+)\S+`), "${1}${2}[REDACTED]"},
}

// Secrets scrubs known secret-bearing patterns from s.
func Secrets(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.replacement)
	}

	return s
}

// SecretsExcept scrubs s like Secrets, but it keeps each match for which
// keep returns true. A display of a command line keeps a value that
// holds shell syntax. That value is code that runs, and a redaction
// would hide it from the person who confirms the command.
//
// keep gets the match without a leading terminal escape (leadGroup).
// The escape bytes can hold ";", "<" or ">", and a keep that looks for
// shell syntax would then keep the token. A kept match keeps its escape.
func SecretsExcept(s string, keep func(match string) bool) string {
	for _, r := range redactions {
		var b strings.Builder

		last := 0
		lead := r.re.SubexpIndex(leadGroup)

		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			b.WriteString(s[last:m[0]])

			from := m[0]
			if lead > 0 && m[2*lead+1] >= 0 {
				from = m[2*lead+1]
			}

			if keep(s[from:m[1]]) {
				b.WriteString(s[m[0]:m[1]])
			} else {
				b.Write(r.re.ExpandString(nil, r.replacement, s, m))
			}

			last = m[1]
		}

		b.WriteString(s[last:])
		s = b.String()
	}

	return s
}
