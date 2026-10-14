package reviews

// Tests of the typed rule and note check of pins and mutes.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckAuthoredTextAcceptsOrdinaryText(t *testing.T) {
	require.NoError(t, CheckAuthoredText(TextPinRule, "Keep handlers thin"))
	require.NoError(t, CheckAuthoredText(TextPinRule, "RUF001 — ünïcode: «ok» 100%"))
	require.NoError(t, CheckAuthoredText(TextPinNote, "Line one.\n\tIndented: a # b\n\n  - list item"))
	require.NoError(t, CheckAuthoredText(TextPinNote, ""), "the caller decides whether a note can be empty")
	require.NoError(t, CheckAuthoredText(TextMuteRule, ""), "a region-only mute has no rule")
	require.NoError(t, CheckAuthoredText(TextMuteRule, "F541"))
}

func TestCheckAuthoredTextRefusesWhatChangesTheReading(t *testing.T) {
	cases := []struct {
		kind   TextKind
		text   string
		reason string
	}{
		{TextPinRule, "", "empty"},
		{TextPinRule, "   ", "empty"},
		{TextPinRule, " lead", "space"},
		{TextPinRule, "trail ", "space"},
		{TextPinRule, "two\nlines", "one line"},
		{TextPinRule, "cr\rhere", "one line"},
		{TextPinRule, "tab\there", "U+0009"},
		{TextMuteRule, "F5\n41", "one line"},
		{TextMuteRule, " F541", "space"},
		{TextPinNote, "esc \x1b[31m", "U+001B"},
		{TextPinNote, "nel \u0085", "U+0085"},
		{TextPinNote, "del \x7f", "U+007F"},
		{TextPinNote, "ls \u2028", "U+2028"},
		{TextPinNote, "bidi \u202e", "U+202E"},
		{TextPinNote, "isolate \u2066", "U+2066"},
		{TextPinNote, "carriage\r\nreturn", "U+000D"},
		{TextPinNote, "ends with a break\n", "empty line"},
	}

	for _, tc := range cases {
		t.Run(tc.reason+" "+tc.text, func(t *testing.T) {
			err := CheckAuthoredText(tc.kind, tc.text)
			require.ErrorIs(t, err, ErrAuthoredText)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}
