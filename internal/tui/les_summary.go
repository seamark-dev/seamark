package tui

import (
	"regexp"
	"strings"
)

// This file turns a review finding body into a one-line summary for the
// evidence list. Review bodies are markdown with emoji, and an emoji can
// take one or two cells depending on the terminal. The summary keeps
// plain text only, so the pane width arithmetic stays exact.

var (
	// lesDetailsRe removes a closed "<details>" block. Review bots put
	// analysis scripts and agent prompts there, not the finding.
	lesDetailsRe = regexp.MustCompile(`(?is)<details>.*?</details>`)
	// lesCommentRe removes an HTML comment.
	lesCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	// lesFenceRe removes a fenced code block. Code is not a summary.
	lesFenceRe = regexp.MustCompile("(?s)```.*?```")
	// lesImageRe removes an image, because its text is not prose.
	lesImageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	// lesLinkRe keeps the text of a markdown link and drops the target.
	lesLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// lesTagRe removes an HTML tag or an autolink in angle brackets.
	lesTagRe = regexp.MustCompile(`<[^>\n]*>`)
	// lesBoldRe finds a **bold** span at the start of a line. Review bots
	// put the one-line title of a finding there, after an italic severity
	// line. A bold word inside a sentence is not a title.
	lesBoldRe = regexp.MustCompile(`(?m)^\s*\*\*(.+?)\*\*`)
	// lesStrongRe removes the "**" wrappers around a span. A "__" pair
	// stays, because Python names such as __init__ use it.
	lesStrongRe = regexp.MustCompile(`\*\*(.+?)\*\*`)
	// lesItalicRe removes a "_…_" wrapper at word boundaries. The
	// boundaries keep a snake_case identifier intact.
	lesItalicRe = regexp.MustCompile(`(^|[\s(|])_([^_\n]+?)_($|[\s).,:;!?|])`)
	// lesTaglineRe matches a line of italic labels only, for example
	// "_Potential issue_ | _Major_". It carries no finding text.
	lesTaglineRe = regexp.MustCompile(`^\s*(_[^_\n]*_\s*\|?\s*)+$`)
	// lesMarkerRe removes a heading, quote or list marker at the start.
	lesMarkerRe = regexp.MustCompile(`^\s*(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)`)
	// lesSpaceRe collapses runs of white space.
	lesSpaceRe = regexp.MustCompile(`\s+`)
)

// lesSummary returns the first sentence of a finding body as plain
// text, or "" when the body has no text. It prefers the first bold span,
// then the first line that is not a severity tagline.
func lesSummary(body string) string {
	s := lesDetailsRe.ReplaceAllString(body, "\n")
	s = lesCommentRe.ReplaceAllString(s, "\n")
	s = lesFenceRe.ReplaceAllString(s, "\n")
	s = lesImageRe.ReplaceAllString(s, "")
	s = lesLinkRe.ReplaceAllString(s, "$1")
	s = lesTagRe.ReplaceAllString(s, " ")

	if m := lesBoldRe.FindStringSubmatch(s); m != nil {
		if title := lesPlain(m[1]); title != "" {
			return lesFirstSentence(title)
		}
	}

	for _, line := range strings.Split(s, "\n") {
		if lesTaglineRe.MatchString(line) {
			continue
		}

		if text := lesPlain(lesMarkerRe.ReplaceAllString(line, "")); text != "" {
			return lesFirstSentence(text)
		}
	}

	return ""
}

// lesPlain removes the inline markdown wrappers and every emoji of one
// line, and collapses its spaces.
func lesPlain(s string) string {
	s = lesStrongRe.ReplaceAllString(s, "$1")

	// A second pass removes a wrapper whose match shared a boundary
	// character with the previous one, for example "_a_ _b_".
	for range 2 {
		s = lesItalicRe.ReplaceAllString(s, "$1$2$3")
	}

	s = strings.ReplaceAll(s, "`", "")
	s = strings.Map(func(r rune) rune {
		if lesIsEmoji(r) {
			return -1
		}

		return r
	}, s)

	s = strings.TrimSpace(lesSpaceRe.ReplaceAllString(s, " "))

	// A tagline separator can stay at the start after its labels go.
	return strings.TrimSpace(strings.TrimLeft(s, "|:-"))
}

// lesFirstSentence keeps the text up to the first sentence end: a
// period, an exclamation mark or a question mark before a space. A
// period inside a word, as in "os.ErrNotExist", is not an end.
func lesFirstSentence(s string) string {
	for i := 0; i < len(s)-1; i++ {
		if strings.ContainsRune(".!?", rune(s[i])) && s[i+1] == ' ' {
			return s[:i+1]
		}
	}

	return s
}

// lesIsEmoji reports a rune from the emoji and pictograph blocks, or a
// joiner or selector that builds an emoji sequence. Terminals disagree
// on the width of these runes, so the summary drops them.
func lesIsEmoji(r rune) bool {
	switch {
	case r == 0x200D, r == 0x20E3, r == 0x2B50, r == 0x2B55:
		// Zero-width joiner, keycap, star and circle.
		return true
	case r >= 0xFE00 && r <= 0xFE0F:
		// Variation selectors.
		return true
	case r >= 0x2190 && r <= 0x21FF, r >= 0x2300 && r <= 0x23FF:
		// Arrows and technical symbols. The design allows only its own
		// glyph set, and "→" is not part of it.
		return true
	case r >= 0x2600 && r <= 0x27BF, r >= 0x2B00 && r <= 0x2BFF:
		// Miscellaneous symbols, dingbats, arrows and shapes.
		return true
	case r >= 0x1F000 && r <= 0x1FAFF, r >= 0xE0020 && r <= 0xE007F:
		// Pictographs, flags, emoticons and tag characters.
		return true
	}

	return false
}
