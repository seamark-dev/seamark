package reviews

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/seamark-dev/seamark/internal/model"
)

// cluster groups comments into lessons, and keeps each accepted comment
// as a Finding linked to its lesson — the full evidence behind the
// fingerprint. The clustering key answers "the same kind of feedback, in
// the same area": a cited rule code clusters by (directory, code) so a
// linter finding that recurs across a package reads as one lesson; an
// un-coded comment clusters by (file, normalized message) so repeated
// prose about one file collapses too.
//
// Not every comment is a finding: thread replies are conversation about a
// finding (the author's "fixed", the reviewer's follow-up), and top-level
// remarks without substance ("Very smart!") teach nothing. Both are
// dropped here — mining them was the dominant noise source on real repos.
func cluster(comments []Comment) ([]model.Lesson, []model.Finding) {
	byKey := map[string]*model.Lesson{}
	reviewers := map[string]map[string]bool{} // key -> set of reviewers
	uncoded := map[string]bool{}              // keys clustered by message, not code

	var findings []model.Finding

	for i := range comments {
		c := &comments[i]

		if c.Path == "" {
			continue // not a file-anchored comment; nothing to attach it to
		}

		if c.InReplyTo != 0 {
			continue // a reply discusses a finding; the finding is the lesson
		}

		region, symptom := regionAndSymptom(c)
		if symptom == "" {
			continue // no substance to learn from
		}

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
			uncoded[key] = c.RuleCode == ""
		}

		lesson.Occurrences++
		reviewers[key][c.Reviewer] = true

		// Keep the most recent comment as the representative example.
		if c.CreatedAt >= lesson.LastTS {
			lesson.LastTS = c.CreatedAt
			lesson.ExampleURL = c.URL
		}

		findings = append(findings, model.Finding{
			ID: c.ID, LessonKey: key, Path: c.Path, PR: c.PR,
			Reviewer: c.Reviewer, Body: findingBody(c.Body),
			URL: c.URL, CreatedAt: c.CreatedAt, Source: model.SourceReview,
		})
	}

	remap := mergeAcrossFiles(byKey, reviewers, uncoded)

	for i := range findings {
		if to, ok := remap[findings[i].LessonKey]; ok {
			findings[i].LessonKey = to
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

	return out, findings
}

// findingBodyCap bounds one stored finding. Real findings run well under
// this once details blocks are gone; the cap only guards against a
// pathological body bloating the index.
const findingBodyCap = 4096

// findingBody is the stored form of a comment: details blocks and HTML
// comments removed (reviewer-bot machinery), code fences deliberately
// KEPT — a ```suggestion fence often carries the fix itself — and the
// result capped at a rune boundary. This is what distillation and
// provenance display read, so it must stay faithful prose, not a
// fingerprint.
func findingBody(body string) string {
	s := detailsBlockRe.ReplaceAllString(body, " ")
	s = detailsTailRe.ReplaceAllString(s, " ")
	s = htmlNoteRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	if len(s) > findingBodyCap {
		cut := findingBodyCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}

		s = strings.TrimSpace(s[:cut])
	}

	return s
}

// mergeAcrossFiles widens un-coded lessons whose fingerprint recurs in
// several files to their deepest common directory: identical feedback in
// three files is a habit of the area, not a property of any one file —
// and per-file counting would leave each copy below the surface
// threshold. Merging stops at the repo root: a fingerprint scattered
// across unrelated top-level trees would otherwise become a repo-wide
// lesson that fires on every edit. The returned map records every
// absorbed key's new home, so finding links can follow the merge.
func mergeAcrossFiles(byKey map[string]*model.Lesson,
	reviewers map[string]map[string]bool, uncoded map[string]bool,
) map[string]string {
	bySymptom := map[string][]string{} // symptom -> keys of un-coded lessons

	for key := range byKey {
		if uncoded[key] {
			bySymptom[byKey[key].Symptom] = append(bySymptom[byKey[key].Symptom], key)
		}
	}

	remap := map[string]string{}

	for symptom, keys := range bySymptom {
		if len(keys) < 2 {
			continue
		}

		// Deterministic merge order, so ties on LastTS always elect the
		// same representative ExampleURL run to run.
		sort.Strings(keys)

		regions := make([]string, len(keys))
		for i, key := range keys {
			regions[i] = byKey[key].Region
		}

		dir := commonDir(regions)
		if dir == "" {
			continue // only the root is shared; keep the lessons file-scoped
		}

		merged := &model.Lesson{
			ClusterKey: dir + "\x00" + symptom,
			Region:     dir,
			Symptom:    symptom,
		}
		set := map[string]bool{}

		for _, key := range keys {
			l := byKey[key]
			merged.Occurrences += l.Occurrences

			if l.LastTS >= merged.LastTS {
				merged.LastTS = l.LastTS
				merged.ExampleURL = l.ExampleURL
			}

			for r := range reviewers[key] {
				set[r] = true
			}

			delete(byKey, key)
			delete(reviewers, key)
			remap[key] = merged.ClusterKey
		}

		byKey[merged.ClusterKey] = merged
		reviewers[merged.ClusterKey] = set
	}

	return remap
}

// commonDir returns the deepest directory shared by every file path, ""
// when only the repo root is.
func commonDir(files []string) string {
	segs := strings.Split(path.Dir(files[0]), "/")

	for _, f := range files[1:] {
		other := strings.Split(path.Dir(f), "/")

		if len(other) < len(segs) {
			segs = segs[:len(other)]
		}

		for i := range segs {
			if segs[i] != other[i] {
				segs = segs[:i]
				break
			}
		}
	}

	common := path.Join(segs...)
	if common == "." {
		return ""
	}

	return common
}

// regionAndSymptom derives a lesson's clustering coordinates from one
// comment. A cited rule code widens the region to the directory (the
// finding is about a coding habit, not one line); otherwise the region
// is the specific file and the symptom is a coarse fingerprint of the
// message so slightly different wordings still collapse. An empty
// symptom means the comment carries nothing worth learning.
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
	// detailsBlockRe removes each closed "<details>…</details>" block.
	// CodeRabbit wraps its boilerplate in them ("🧩 Analysis chain" with
	// executed scripts BEFORE the finding, "🤖 Prompt for AI Agents"
	// after) — the finding itself sits between the blocks, so removal
	// must be per-block, never greedy-to-the-end.
	detailsBlockRe = regexp.MustCompile(`(?s)<details>.*?</details>`)
	// detailsTailRe cuts an unclosed trailing block, the pre-block
	// fallback for truncated bodies.
	detailsTailRe = regexp.MustCompile(`(?s)<details>.*`)
	htmlNoteRe    = regexp.MustCompile(`(?s)<!--.*?-->`)
	fenceRe       = regexp.MustCompile("(?s)```.*?```")
	urlRe         = regexp.MustCompile(`https?://\S+`)
	// boldRe captures the first **bold** span. Reviewers (CodeRabbit
	// especially) put a one-line issue title there, after an italic
	// severity tagline — a far better fingerprint than the raw first line.
	boldRe      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	htmlTagRe   = regexp.MustCompile(`<[^>]+>`)
	nonWordRe   = regexp.MustCompile(`[^a-z0-9 ]+`)
	spaceRunRe  = regexp.MustCompile(`\s+`)
	numberRunRe = regexp.MustCompile(`\b\d+\b`)
)

// stripBoilerplate removes the non-finding matter of a comment body:
// details blocks, HTML comments, code fences, and URLs. Shared by the
// fingerprint and the rule-code extractor — a linter code inside an
// executed script or a fenced example is quoted machinery, not a citation
// (`rg -A10` in a CodeRabbit analysis script once minted a fake "A10"
// lesson).
func stripBoilerplate(body string) string {
	s := detailsBlockRe.ReplaceAllString(body, " ")
	s = detailsTailRe.ReplaceAllString(s, " ")
	s = htmlNoteRe.ReplaceAllString(s, " ")
	s = fenceRe.ReplaceAllString(s, " ")

	return urlRe.ReplaceAllString(s, " ")
}

// ackFingerprints are normalized fingerprints that pass the word floor
// yet still teach nothing: reactions to a change, not feedback about it.
var ackFingerprints = map[string]bool{
	"looks good to me": true, "sounds good to me": true, "makes sense to me": true,
	"this looks good": true, "this is fine": true, "good catch thanks": true,
	"thanks good catch": true, "thanks for fixing": true, "you are right": true,
	"yes you are right": true, "will fix in a follow up": true,
}

// normalizeSymptom reduces a comment body to a coarse fingerprint of the
// issue it raises, or "" when there is no issue to fingerprint. It
// prefers the reviewer's own bold title, falling back to the first
// sentence, then lowercases and strips markup, digits, and punctuation so
// near-identical wordings cluster together. Distinct issues get distinct
// fingerprints — and correctly stay one-offs that never cross the
// recurrence threshold. Fingerprints under three words are dropped: what
// survives normalization that short ("fixed", "stale comment", "fmt") is
// reaction or shorthand, never guidance an agent could apply.
func normalizeSymptom(body string) string {
	s := stripBoilerplate(body)

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

	if strings.Count(s, " ") < 2 || ackFingerprints[s] {
		return ""
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
