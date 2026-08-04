// Package wording decides whether two short rule/note pairs say the
// same thing. It is the deterministic restatement check behind proposal
// deduplication (internal/distill) and ambient pin budgeting
// (internal/reviews) — two importers that must reach one answer, which
// is why the logic lives in a leaf package neither owns.
//
// The check is calibrated, not guessed: thresholds were tuned against a
// real pin file where 23% of 65 applied pins restated something already
// pinned — every hand-identified duplicate cluster is caught, and no
// distinct pin is merged (the calibration set lives in wording_test.go).
package wording

import (
	"regexp"
	"strings"
)

const (
	ruleOverlap = 0.40 // rule labels alone are conclusive at this overlap
	noteOverlap = 0.30 // …or the guidance text is
	// Weaker agreement in BOTH counts as duplication: differently-named
	// rules whose notes rhyme (leaky-error-payloads vs
	// no-internal-details-in-client-errors).
	ruleWeak = 0.25
	noteWeak = 0.18
)

// minNoteWords is how much guidance text a note needs before its
// overlap means anything: two four-word notes sharing one incidental
// word score 0.3 on pure arithmetic and say nothing to a reader.
const minNoteWords = 4

// MaxRuleLen bounds a normalized rule label — long enough for any
// honest kebab slug, short enough that a label echoed into prompts and
// terminals can never blow a budget. Exported because prompt-budget
// arithmetic downstream stands on this exact bound.
const MaxRuleLen = 40

var wordRe = regexp.MustCompile(`[a-z]+`)

// stopWords carry no topic: they inflate overlap between unrelated
// rules and hide it between related ones.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "when": true, "never": true,
	"always": true, "must": true, "not": true, "dont": true, "are": true, "this": true,
	"that": true, "from": true, "into": true, "before": true, "after": true, "use": true,
	"using": true, "set": true, "get": true, "make": true, "sure": true, "keep": true,
	"only": true, "same": true, "its": true, "their": true, "them": true, "they": true,
	"you": true, "your": true, "our": true, "any": true, "all": true, "each": true,
	"every": true, "than": true, "then": true, "instead": true, "rather": true,
}

var ruleCleanRe = regexp.MustCompile(`[^a-z0-9-]+`)

// CleanRule normalizes a label to pin-safe kebab-case: lowercased,
// spaces to hyphens, everything outside [a-z0-9-] stripped, length
// capped. Labels are echoed back — into agent prompts and onto
// terminals — and pins are hand-written in committed config, which on
// a cloned repo is no more trusted than a review comment: unnormalized,
// a multiline rule would inject lines into a prompt's instruction block.
func CleanRule(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = ruleCleanRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")

	if len(s) > MaxRuleLen {
		s = strings.Trim(s[:MaxRuleLen], "-")
	}

	return s
}

// words tokenizes text into topic words of at least minLen length, with
// two folds so inflection cannot hide a shared topic:
//
//   - plurals: "error" and "errors" are one topic, and rules about the
//     same mistake routinely disagree on number
//     ("leaky-error-payloads" vs "no-internal-details-in-client-errors"
//     shared nothing until this).
//   - agent nouns: a trailing "er" folds when the stem keeps ≥4
//     characters — "linters"→"linter"→"lint", "handlers"→"handl". Two
//     real pins about running lint stayed unmatched because "linters"
//     and "lint" counted as different topics.
func words(s string, minLen int) map[string]bool {
	out := map[string]bool{}

	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = w[:len(w)-1]
		}

		if len(w) >= 6 && strings.HasSuffix(w, "er") {
			w = w[:len(w)-2]
		}

		if len(w) > minLen && !stopWords[w] {
			out[w] = true
		}
	}

	return out
}

// jaccard is the overlap of two token sets, 0 when either is empty.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	shared := 0

	for w := range a {
		if b[w] {
			shared++
		}
	}

	return float64(shared) / float64(len(a)+len(b)-shared)
}

// Topic is the comparable form of one pattern: its label and guidance
// reduced to topic words.
type Topic struct {
	label string
	rule  map[string]bool
	note  map[string]bool
}

// New builds a Topic from a pattern's rule label and guidance note.
// The label is normalized through CleanRule; matching reads the raw
// text — words() lowercases and drops punctuation itself, so
// normalization changes nothing about which patterns restate each other.
func New(rule, note string) Topic {
	return Topic{
		label: CleanRule(rule),
		rule:  words(strings.ReplaceAll(rule, "-", " "), 2),
		note:  words(note, 3),
	}
}

// Label returns the normalized (prompt- and terminal-safe) rule label.
func (t Topic) Label() string { return t.label }

// codeRe matches a normalized bare linter-code label (ruf001, e702,
// reportargumenttype) as opposed to a kebab rule name. Codes carry
// their meaning in the digits, which words() drops — RUF001 and RUF003
// both tokenize to "ruf" — so overlap arithmetic cannot tell them
// apart. The shape check runs on CleanRule output, hence lowercase.
var codeRe = regexp.MustCompile(`^[a-z]{1,8}\d{1,4}$|^report[a-z]+$`)

// Restates reports whether two patterns say the same thing.
func (t Topic) Restates(o Topic) bool {
	// The same normalized label IS the same pattern — token overlap
	// can miss it when the label holds only short words ("no-io"
	// tokenizes to nothing) and must not be asked.
	if t.label != "" && t.label == o.label {
		return true
	}

	// Distinct labels where either is a bare linter code: distinct
	// rules, whatever the tokens or notes suggest — the digits are the
	// rule, and they were just compared above.
	if codeRe.MatchString(t.label) || codeRe.MatchString(o.label) {
		return false
	}

	r := jaccard(t.rule, o.rule)
	if r >= ruleOverlap {
		return true
	}

	if len(t.note) < minNoteWords || len(o.note) < minNoteWords {
		return false // too little prose to judge; the labels already disagreed
	}

	n := jaccard(t.note, o.note)

	return n >= noteOverlap || (r >= ruleWeak && n >= noteWeak)
}
