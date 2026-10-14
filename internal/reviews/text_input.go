package reviews

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrAuthoredText marks a rule or a note that a person typed and that
// lessons.yaml must not hold. Every error of CheckAuthoredText wraps it.
var ErrAuthoredText = errors.New("invalid text")

// TextKind names the field that a person types.
type TextKind int

// The fields that CheckAuthoredText checks.
const (
	// TextPinRule is the rule of a pin: one nonblank line.
	TextPinRule TextKind = iota
	// TextPinNote is the note of a pin: lines of guidance. The caller
	// decides whether the note can be empty.
	TextPinNote
	// TextMuteRule is the rule of a mute: one line, or empty for a
	// region-only mute.
	TextMuteRule
)

// CheckAuthoredText checks one rule or note that a person typed in the
// console. The text reaches the agent context through every surface, so
// the check refuses characters that can change how the text reads on a
// screen or in a prompt: control characters, line and paragraph
// separators, and bidirectional formatting characters. A note can hold
// line breaks and tabs, because guidance can need them.
//
// The check never changes the text. A form shows the reason and the
// user edits the text, so no character disappears without a trace. The
// rule of a pin and of a mute is one line without surrounding spaces:
// the pin key trims spaces, but the firing identity keeps them, so two
// spellings of one key would count as two lessons.
//
// A note must not end with a line break. The pin writer trims trailing
// line breaks, and a kept break needs a YAML form that later edits
// refuse; so the user removes the empty last line.
func CheckAuthoredText(kind TextKind, s string) error {
	for _, r := range s {
		if bad := refusedRune(kind, r); bad != "" {
			return authoredTextError(bad)
		}
	}

	switch kind {
	case TextPinRule, TextMuteRule:
		if kind == TextPinRule && strings.TrimSpace(s) == "" {
			return authoredTextError("the rule is empty")
		}

		if s != strings.TrimSpace(s) {
			return authoredTextError("the rule starts or ends with a space; remove it")
		}
	case TextPinNote:
		if strings.HasSuffix(s, "\n") {
			return authoredTextError("the note ends with an empty line; remove it")
		}
	}

	return nil
}

// refusedRune returns why one character is refused in a field of kind,
// or "". The reason names the character as an escape, because the
// character itself may not show on a screen.
func refusedRune(kind TextKind, r rune) string {
	if kind == TextPinNote && (r == '\n' || r == '\t') {
		return ""
	}

	switch {
	case kind != TextPinNote && (r == '\n' || r == '\r'):
		return "the rule must be one line"
	case unicode.IsControl(r), r == 0x2028, r == 0x2029, isBidiControl(r):
		return fmt.Sprintf("the text contains the control character %U", r)
	}

	return ""
}

// authoredTextError wraps ErrAuthoredText with a reason in plain words.
func authoredTextError(reason string) error {
	return fmt.Errorf("%w: %s", ErrAuthoredText, reason)
}
