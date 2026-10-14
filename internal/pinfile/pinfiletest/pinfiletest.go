// Package pinfiletest builds large pin files and the index rows around
// them, for the Phase 4 benchmarks (make tui-measure). One package holds
// the generators, so every benchmark of the console and its engines
// measures the same fixture shape.
//
// The shape:
//
//   - Pin i has a rule of two invented words and the region
//     Region(base, i/PinsPerRegion). So a path sees PinsPerRegion pins,
//     whatever the size of the file.
//   - No two rules share a topic word, and a short note holds too few
//     words for a note comparison. So the restatement check of
//     internal/wording keeps every pin, and a selection works on the
//     whole live set.
//   - Mutes match no mined lesson and no path. So a long mute list adds
//     the cost of its checks and changes no selection.
//
// Only tests import this package.
package pinfiletest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// PinsPerRegion is the number of pins that share one region. It keeps
// the live set of one path small and constant at every file size.
const PinsPerRegion = 10

// consonants and vowels build the invented words. A word ends with a
// vowel, so the plural and agent-noun folds of internal/wording never
// merge two words.
const (
	consonants = "bdfgklmnprstvz"
	vowels     = "aeiou"
)

// Word returns invented word i: three consonant-vowel syllables, such
// as "bebaba". Words differ for every i below 343000. The one stop word
// of internal/wording with this shape, "before", is word 226311, far
// above the sizes of the benchmarks.
func Word(i int) string {
	syllables := len(consonants) * len(vowels)

	var b strings.Builder

	for range 3 {
		s := i % syllables
		i /= syllables

		b.WriteByte(consonants[s/len(vowels)])
		b.WriteByte(vowels[s%len(vowels)])
	}

	return b.String()
}

// Region returns region slot k under base, such as "src/r007".
func Region(base string, k int) string {
	return fmt.Sprintf("%s/r%03d", base, k)
}

// Note returns a note of exactly n runes. Short sentences fill it, with
// a line break after every sixth sentence, so a view wraps and breaks
// it as real prose. The note neither starts nor ends with white space,
// and no line ends with a space. So CheckAuthoredText and
// pinfile.EncodeNote accept it. A size below 1 gives "".
func Note(n int) string {
	if n < 1 {
		return ""
	}

	var b strings.Builder

	for k := 0; b.Len() < n; k++ {
		if k > 0 {
			sep := " "
			if k%6 == 0 {
				sep = "\n"
			}

			b.WriteString(sep)
		}

		fmt.Fprintf(&b, "Step %d checks the value before the store uses it.", k)
	}

	// The text is ASCII, so a byte cut is a rune cut. A cut can end
	// on a space or a line break; a period replaces it.
	note := []byte(b.String()[:n])
	if last := note[len(note)-1]; last == ' ' || last == '\n' {
		note[len(note)-1] = '.'
	}

	return string(note)
}

// Pins returns n pins. Pin i has the rule "<word>-<word>", the region
// Region(base, i/PinsPerRegion), and the note "Mind <word>." An empty
// note keeps that short note. A long note goes to pin 0 only, because
// two long notes of one vocabulary restate each other.
func Pins(n int, base, note string) []reviews.PinRule {
	pins := make([]reviews.PinRule, n)

	for i := range pins {
		pins[i] = reviews.PinRule{
			Rule:   Word(2*i) + "-" + Word(2*i+1),
			Region: Region(base, i/PinsPerRegion),
			Note:   "Mind " + Word(i) + ".",
		}
	}

	if n > 0 && note != "" {
		pins[0].Note = note
	}

	return pins
}

// Mutes returns n mutes that hide nothing. An even mute names a rule
// that no mined lesson has. An odd mute names a region that no path
// enters. So both checks of reviews.Config.MuteMatches run.
func Mutes(n int) []reviews.MuteRule {
	mutes := make([]reviews.MuteRule, n)

	for i := range mutes {
		if i%2 == 0 {
			mutes[i] = reviews.MuteRule{Rule: "mute-" + Word(i)}
		} else {
			mutes[i] = reviews.MuteRule{Region: "muted/" + Word(i) + "/"}
		}
	}

	return mutes
}

// Mined returns perRegion mined lessons in each of the first regions
// region slots under base. Every lesson recurs three times, so it
// passes the default threshold of 2.
func Mined(regions, perRegion int, base string) []model.Lesson {
	lessons := make([]model.Lesson, 0, regions*perRegion)

	for k := range regions * perRegion {
		region := Region(base, k/perRegion)
		symptom := "lesson " + Word(k)

		lessons = append(lessons, model.Lesson{
			ClusterKey: region + "\x00" + symptom, Region: region, Reviewer: "coderabbit",
			Symptom: symptom, Occurrences: 3, LastTS: int64(1000 + k),
		})
	}

	return lessons
}

// Paths returns n file paths. Path j lies in region slot j, so it sees
// the pins and the mined lessons of that slot. A path names no file on
// disk: lessons match a path that does not exist yet.
func Paths(n int, base string) []string {
	paths := make([]string, n)

	for j := range paths {
		paths[j] = Region(base, j) + "/" + Word(j) + ".go"
	}

	return paths
}

// Applied returns n applied proposal rows. Row i < len(pins) holds the
// fields of pin i, so the pin file and the ledger link them. A later row
// holds a pin that the file does not hold, as after a removal by hand.
// So the ledger has n rows at every pin count. Row i has ID i+1. The
// store sets its own IDs on insert; an in-memory ledger uses these.
func Applied(pins []reviews.PinRule, n int) []model.Proposal {
	rows := make([]model.Proposal, n)

	for i := range rows {
		p := reviews.PinRule{Rule: "removed-" + Word(i), Region: "gone", Note: "Mind " + Word(i) + "."}
		if i < len(pins) {
			p = pins[i]
		}

		rows[i] = model.Proposal{
			ID: int64(i + 1), Signature: "scale-" + Word(i), Rule: p.Rule, Region: p.Region, Regions: p.Regions,
			Note: p.Note, Agent: "claude/v2", Status: model.ProposalApplied,
		}
	}

	return rows
}

// Lessons renders a lessons.yaml text with the mute list first and the
// pin list second. Each pin comes from pinfile.RenderPin, so the pin
// lines have the layout of a console create. An empty list leaves its
// key out.
func Lessons(tb testing.TB, pins []reviews.PinRule, mutes []reviews.MuteRule) string {
	tb.Helper()

	var b strings.Builder

	if len(mutes) > 0 {
		b.WriteString("mute:\n")

		for _, m := range mutes {
			if m.Rule != "" {
				b.WriteString("  - rule: " + m.Rule + "\n")
			} else {
				b.WriteString("  - region: " + m.Region + "\n")
			}
		}
	}

	if len(pins) > 0 {
		b.WriteString("pin:\n")

		for _, p := range pins {
			entry, err := pinfile.RenderPin(p)
			require.NoError(tb, err, "render pin %s", p.Rule)

			b.WriteString(entry)
		}
	}

	return b.String()
}
