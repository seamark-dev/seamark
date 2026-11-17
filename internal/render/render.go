// Package render holds small text-shaping helpers shared by the CLI and
// LSP surfaces: everything user-visible passes through here so untrusted
// input (git metadata, source-derived signatures) is handled once.
package render

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ShellWord returns s as one word of a shell command, so a printed route
// runs when the user pastes it. A plain path stays as it is, so normal
// output does not change. The text passes Sanitize first.
func ShellWord(s string) string {
	s = Sanitize(s)
	if s != "" && !strings.ContainsFunc(s, needsShellQuote) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// needsShellQuote reports a rune that a shell can read as syntax.
func needsShellQuote(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("/._-+,:=@%", r)
}

// Sanitize strips control characters before untrusted text reaches a
// terminal or editor: commit titles and authors come straight from git
// history, where ANSI/OSC escape sequences are legal and can manipulate
// the viewer's terminal (title changes, cursor moves, fake hyperlinks).
//
// The result is one line. A dropped line break joins the two words next
// to it, so each inner break becomes one space instead. Breaks at either
// end go away, so a YAML block note gains no trailing space. A lone
// carriage return is still dropped, and "\r\n" becomes one space.
//
// Sanitize reuses SanitizeBlock and only joins its lines, so the two
// functions never scrub a different set of runes.
func Sanitize(s string) string {
	return strings.ReplaceAll(strings.Trim(SanitizeBlock(s), "\n"), "\n", " ")
}

// isUnsafeRune reports whether a terminal can act on r instead of
// showing it. SanitizeBlock uses it, and Sanitize builds on
// SanitizeBlock, so a one-line field and a block get the same protection.
func isUnsafeRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		// C1 controls: U+009B is a one-byte CSI on some terminals.
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		// Bidirectional overrides and isolates reorder display text.
		return true
	}

	return false
}

// Truncate shortens s to at most n runes; byte slicing would cut
// multi-byte characters in half.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}

	runes := []rune(s)
	if len(runes) <= n {
		return s
	}

	return string(runes[:n-1]) + "…"
}

// CutBytes returns s when it fits in limit bytes. Else it cuts s at or
// before limit, on a rune boundary, so a byte bound never splits a rune.
// The limit must not be negative, because a slice bound cannot be.
//
// CutBytes adds no suffix, because the callers differ on purpose: one adds
// a mark past the limit, others keep it inside the limit, and others add
// none. The walk back has no cap. So a run of continuation bytes in
// invalid UTF-8 moves the cut to the nearest rune start, or to 0.
func CutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut]
}

// SanitizeBlock strips control characters from multi-line untrusted
// text but keeps newlines and tabs. It also strips C1 controls and
// bidirectional overrides, which can reorder or hide terminal text.
func SanitizeBlock(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && isUnsafeRune(r) {
			return -1
		}

		return r
	}, s)
}
