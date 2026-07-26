// Package render holds small text-shaping helpers shared by the CLI and
// LSP surfaces: everything user-visible passes through here so untrusted
// input (git metadata, source-derived signatures) is handled once.
package render

import "strings"

// Sanitize strips control characters before untrusted text reaches a
// terminal or editor: commit titles and authors come straight from git
// history, where ANSI/OSC escape sequences are legal and can manipulate
// the viewer's terminal (title changes, cursor moves, fake hyperlinks).
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return -1
		}

		return r
	}, s)
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
