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

import "regexp"

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
	// Bearer tokens in headers: Authorization: Bearer eyJ….
	{regexp.MustCompile(`(?i)\b(bearer\s+)\S+`), "${1}[REDACTED]"},
}

// Secrets scrubs known secret-bearing patterns from s.
func Secrets(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.replacement)
	}

	return s
}
