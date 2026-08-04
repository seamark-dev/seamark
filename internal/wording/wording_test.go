package wording

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The pairs below are verbatim from a real pin file (trading-tools,
// 65 applied pins) where 23% turned out to restate something already
// pinned. They are the calibration set: every "same" pair was judged a
// duplicate by hand, every "distinct" pair is genuinely separate
// guidance that must survive.
func TestRestatesOnRealPins(t *testing.T) {
	same := []struct{ aRule, aNote, bRule, bNote string }{
		{
			"leaky-error-payloads",
			"Never return or persist raw exception text/backend details in error payloads. Log full context internally and surface only a thin, generic, client-safe message.",
			"no-internal-details-in-client-errors",
			"Never surface raw exception or library text (ValueError, UUID parse errors) in HTTP responses; log internally and return a generic message.",
		},
		{
			"docs-code-drift",
			"When you change a tool payload, stage list, or behavior, update every doc, RFC, and index that describes it.",
			"docs-out-of-sync-with-code",
			"Keep docstrings, comments, README examples, and OpenAPI descriptions matching the code.",
		},
		{
			"empty-collection-guard",
			"Guard collections for emptiness before calling min(), max(), or positional indexing.",
			"guard-empty-before-reduction",
			"Check that DataFrames, index slices, and arrays are non-empty before calling .max()/.min().",
		},
		{
			"atomic-write-and-promote",
			"Don't leave persisted state inconsistent under partial failure or concurrency: wrap dependent writes in one transaction.",
			"atomic-multi-step-writes",
			"Never leave persisted state invalid between dependent writes: wrap trade+leg inserts in one transaction.",
		},
		{
			// Same label, different wording — the case that slipped
			// through a note-only comparison.
			"mask-internal-errors-from-clients",
			"Route every failure path through api.errors.log_and_raise so clients never see internals.",
			"mask-internal-errors-from-clients",
			"Wrap DB/stats/deserialization failures with log_and_raise: log richly, return a thin message.",
		},
	}

	for _, c := range same {
		assert.True(t, New(c.aRule, c.aNote).Restates(New(c.bRule, c.bNote)),
			"%q and %q say the same thing", c.aRule, c.bRule)
	}

	distinct := []struct{ aRule, aNote, bRule, bNote string }{
		{
			"holiday-aware-session-dates",
			"Compute next/prev trading session with the holiday-and-weekend-aware calendar, never calendar arithmetic.",
			"train-serve-parity",
			"Live serving must compute features exactly as the historical materialization does — same normalization denominator, same source key.",
		},
		{
			"no-silent-default-quantity",
			"Never substitute a default quantity when the source lacks or zeroes it; fail fast instead.",
			"bound-refetch-triggers",
			"Do not key a refetch effect on a value that changes with every live tick; bucket or debounce the key.",
		},
		{
			// Both about guarding inputs, but different mistakes: NaN
			// contamination vs unvalidated request bounds.
			"guard-nan-and-missing-numerics",
			"Never feed possibly-missing or non-finite numbers into comparisons or float(); drop NaN and sentinels explicitly.",
			"unvalidated-input-bounds",
			"Validate CLI args and request fields before acting on them: reject non-positive limits and out-of-range offsets.",
		},
	}

	for _, c := range distinct {
		assert.False(t, New(c.aRule, c.aNote).Restates(New(c.bRule, c.bNote)),
			"%q and %q are separate guidance", c.aRule, c.bRule)
	}
}

// The agent-noun fold: "linters" and "lint" must count as one topic.
// Measured miss on a real pin file — two applied pins about running
// lint stayed unmatched because inflection hid the shared topic.
func TestAgentNounFoldConnectsLintPins(t *testing.T) {
	assert.True(t,
		New("run-linters-before-commit", "").Restates(New("lint-before-commit", "")),
		"linters/lint must fold to one topic")

	// The fold needs a ≥4-char stem: short words keep their trailing
	// "er" ("user", "order") rather than degrading into stubs.
	assert.False(t, New("user-facing-copy", "").Restates(New("use-transactions", "")),
		"a short stem must not fold into an unrelated word")
}

func TestIdenticalLabelsRestateWhateverTheTokens(t *testing.T) {
	// "no-io" tokenizes to nothing (both words too short), so overlap
	// arithmetic sees two empty sets — but the same normalized label IS
	// the same pattern, notes or none.
	assert.True(t, New("no-io", "").Restates(New("no-io", "")),
		"identical short-token labels are one pattern")
	assert.False(t, New("no-io", "").Restates(New("no-op", "")),
		"empty token sets alone must not match differing labels")
}

func TestLinterCodesRestateOnlyOnEquality(t *testing.T) {
	// RUF001 and RUF003 both tokenize to "ruf": the digits ARE the
	// rule, and every dedup surface must treat codes the same way.
	assert.False(t, New("RUF001", "ambiguous unicode sneaks in from chat").
		Restates(New("RUF003", "ambiguous unicode sneaks into comments")),
		"distinct codes are distinct rules, however their notes rhyme")
	assert.True(t, New("RUF001", "").Restates(New("ruf001", "")),
		"the same code is the same rule, case aside")
	assert.False(t, New("F541", "f-strings without placeholders add noise to the diff").
		Restates(New("docs-code-drift", "Keep docstrings and README examples matching the code.")),
		"a code never merges into a kebab rule on note overlap")
}

func TestCleanRule(t *testing.T) {
	assert.Equal(t, "pooled-state-reset", CleanRule("Pooled State reset"))
	assert.Equal(t, "nonewlines", CleanRule("no\nnewlines"), "injection-shaped labels flatten")
	assert.Equal(t, "", CleanRule("---"))

	long := CleanRule("a-very-long-rule-label-that-keeps-going-and-going-and-going")
	assert.LessOrEqual(t, len(long), 40, "labels echoed into prompts stay bounded")
}
