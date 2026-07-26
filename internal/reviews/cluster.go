package reviews

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// cluster groups comments into lessons. The clustering key answers "the
// same kind of feedback, in the same area": a cited rule code clusters by
// (directory, code) so a linter finding that recurs across a package
// reads as one lesson; an un-coded comment clusters by (file, normalized
// message) so repeated prose about one file collapses too.
func cluster(comments []Comment) []model.Lesson {
	byKey := map[string]*model.Lesson{}
	reviewers := map[string]map[string]bool{} // key -> set of reviewers

	for i := range comments {
		c := &comments[i]

		if c.Path == "" {
			continue // not a file-anchored comment; nothing to attach it to
		}

		region, symptom := regionAndSymptom(c)
		key := region + "\x00" + symptom

		lesson := byKey[key]
		if lesson == nil {
			lesson = &model.Lesson{
				ClusterKey: key,
				Region:     region,
				Symptom:    symptom,
				ExampleURL: c.URL,
			}
			byKey[key] = lesson
			reviewers[key] = map[string]bool{}
		}

		lesson.Occurrences++
		reviewers[key][c.Reviewer] = true

		// Keep the most recent comment as the representative example.
		if c.CreatedAt >= lesson.LastTS {
			lesson.LastTS = c.CreatedAt
			lesson.ExampleURL = c.URL
		}
	}

	out := make([]model.Lesson, 0, len(byKey))

	for key, lesson := range byKey {
		lesson.Reviewer = summarizeReviewers(reviewers[key])
		out = append(out, *lesson)
	}

	// Strongest first: most occurrences, then most recent.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Occurrences != out[j].Occurrences {
			return out[i].Occurrences > out[j].Occurrences
		}

		return out[i].LastTS > out[j].LastTS
	})

	return out
}

// regionAndSymptom derives a lesson's clustering coordinates from one
// comment. A cited rule code widens the region to the directory (the
// finding is about a coding habit, not one line); otherwise the region
// is the specific file and the symptom is a coarse fingerprint of the
// message so slightly different wordings still collapse.
func regionAndSymptom(c *Comment) (region, symptom string) {
	if c.RuleCode != "" {
		// A cited code recurring across a package is a habit; cluster by
		// directory. But the repo root is not a cohesive package — lumping
		// every root file together cross-contaminates unrelated files
		// (a markdown lint on README landing on a root .py), so root-level
		// comments stay file-scoped.
		if dir := path.Dir(c.Path); dir != "." {
			return dir, c.RuleCode
		}

		return c.Path, c.RuleCode
	}

	return c.Path, normalizeSymptom(c.Body)
}

var (
	// detailsRe cuts CodeRabbit's appended "<details>🤖 Prompt for AI
	// Agents</details>" block and everything after it — pure boilerplate.
	detailsRe = regexp.MustCompile(`(?s)<details>.*`)
	fenceRe   = regexp.MustCompile("(?s)```.*?```")
	// boldRe captures the first **bold** span. Reviewers (CodeRabbit
	// especially) put a one-line issue title there, after an italic
	// severity tagline — a far better fingerprint than the raw first line.
	boldRe      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	htmlTagRe   = regexp.MustCompile(`<[^>]+>`)
	nonWordRe   = regexp.MustCompile(`[^a-z0-9 ]+`)
	spaceRunRe  = regexp.MustCompile(`\s+`)
	numberRunRe = regexp.MustCompile(`\b\d+\b`)
)

// normalizeSymptom reduces a comment body to a coarse fingerprint of the
// issue it raises. It prefers the reviewer's own bold title, falling back
// to the first sentence, then lowercases and strips markup, digits, and
// punctuation so near-identical wordings cluster together. Distinct
// issues get distinct fingerprints — and correctly stay one-offs that
// never cross the recurrence threshold.
func normalizeSymptom(body string) string {
	s := detailsRe.ReplaceAllString(body, " ")
	s = fenceRe.ReplaceAllString(s, " ")

	if m := boldRe.FindStringSubmatch(s); m != nil {
		s = m[1] // the issue title
	} else if i := strings.IndexAny(s, ".\n!?"); i > 0 {
		s = s[:i] // first sentence-ish
	}

	s = htmlTagRe.ReplaceAllString(s, " ")
	s = strings.ToLower(s)
	s = numberRunRe.ReplaceAllString(s, " ")
	s = nonWordRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRunRe.ReplaceAllString(s, " "))

	const limit = 80
	if len(s) > limit {
		s = strings.TrimSpace(s[:limit])
	}

	if s == "" {
		s = "review comment"
	}

	return s
}

// summarizeReviewers collapses the set of reviewers behind a cluster into
// one label: a single reviewer keeps its name, several become "mixed".
func summarizeReviewers(set map[string]bool) string {
	names := make([]string, 0, len(set))
	for r := range set {
		names = append(names, r)
	}

	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return "mixed"
	}
}
