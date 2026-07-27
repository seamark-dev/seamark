package reviews

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// ghPage builds a GitHub pulls/comments JSON array from terse specs, so
// tests read as data. Fields mirror the real API payload.
func ghPage(comments ...string) string {
	out := "["
	for i, c := range comments {
		if i > 0 {
			out += ","
		}

		out += c
	}

	return out + "]"
}

func comment(id int, login, userType, path string, line int, pr int, body string) string {
	return fmt.Sprintf(`{
		"id": %d,
		"user": {"login": %q, "type": %q},
		"body": %q,
		"path": %q,
		"line": %d,
		"original_line": %d,
		"html_url": "https://github.com/o/r/pull/%d#discussion_r%d",
		"created_at": "2026-07-2%dT10:00:00Z",
		"pull_request_url": "https://api.github.com/repos/o/r/pulls/%d"
	}`, id, login, userType, body, path, line, line, pr, id, (id%9)+1, pr)
}

// reply marks a comment JSON as a thread reply to parent.
func reply(parent int, commentJSON string) string {
	return strings.Replace(commentJSON, "{",
		fmt.Sprintf(`{"in_reply_to_id": %d,`, parent), 1)
}

type clustered struct {
	region, symptom, reviewer string
	occ                       int
}

// mine drives the parse+cluster pipeline directly (no git remote, no
// network) so clustering behaviour can be asserted in isolation.
func mine(t *testing.T, json string) []clustered {
	t.Helper()

	got, _ := mineWithFindings(t, json)

	return got
}

func mineWithFindings(t *testing.T, json string) ([]clustered, []model.Finding) {
	t.Helper()

	comments, err := parseComments([]byte(json))
	require.NoError(t, err)

	lessons, findings := cluster(comments)

	got := make([]clustered, len(lessons))
	for i, l := range lessons {
		got[i] = clustered{l.Region, l.Symptom, l.Reviewer, l.Occurrences}
	}

	return got, findings
}

func TestRuleCodeClustersByDirectory(t *testing.T) {
	// Same Ruff code flagged by CodeRabbit in three files of one package
	// must collapse to a single directory-scoped lesson.
	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "research/a.py", 10, 100, "Docstring uses a non-ASCII character. `RUF001`"),
		comment(2, "coderabbitai[bot]", "Bot", "research/b.py", 20, 101, "Ambiguous unicode: `RUF001` please fix"),
		comment(3, "coderabbitai[bot]", "Bot", "research/c.py", 30, 102, "RUF001 again here"),
	)

	got := mine(t, page)

	require.Len(t, got, 1)
	assert.Equal(t, "research", got[0].region)
	assert.Equal(t, "RUF001", got[0].symptom)
	assert.Equal(t, "coderabbit", got[0].reviewer)
	assert.Equal(t, 3, got[0].occ)
}

func TestReviewerClassification(t *testing.T) {
	cases := []struct{ login, typ, want string }{
		{"coderabbitai[bot]", "Bot", "coderabbit"},
		{"Copilot", "Bot", "copilot"},
		{"copilot-pull-request-reviewer[bot]", "Bot", "copilot"},
		{"dependabot[bot]", "Bot", "bot"},
		{"yuribuerov", "User", "human"},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, classifyReviewer(c.login, c.typ), c.login)
	}
}

func TestMixedReviewersLabelMixed(t *testing.T) {
	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "api/x.py", 5, 1, "Prefer explicit timezone. `DTZ005`"),
		comment(2, "yuribuerov", "User", "api/x.py", 6, 2, "Prefer explicit timezone `DTZ005`"),
	)

	got := mine(t, page)

	require.Len(t, got, 1)
	assert.Equal(t, "DTZ005", got[0].symptom)
	assert.Equal(t, "mixed", got[0].reviewer)
	assert.Equal(t, 2, got[0].occ)
}

func TestUncodedCommentsClusterByFileAndFuzzyMessage(t *testing.T) {
	// No rule code: cluster by file + normalized message. Slight wording
	// and number differences must still collapse.
	page := ghPage(
		comment(1, "yuribuerov", "User", "api/service.py", 5, 1, "This function is too long, 250 lines is hard to review."),
		comment(2, "yuribuerov", "User", "api/service.py", 90, 2, "this function is too long 300 lines is hard to review"),
	)

	got := mine(t, page)

	require.Len(t, got, 1)
	assert.Equal(t, "api/service.py", got[0].region)
	assert.Equal(t, 2, got[0].occ)
	assert.NotContains(t, got[0].symptom, "250", "digits are normalized out of the fingerprint")
}

func TestExtractRuleCode(t *testing.T) {
	cases := []struct{ body, want string }{
		{"Please fix `RUF001` here", "RUF001"},
		{"PLC0414 useless import alias", "PLC0414"},
		{"DTZ005 needs a tz", "DTZ005"},
		{"reportArgumentType: wrong type passed", "reportArgumentType"},
		{"pyright: reportOptionalMemberAccess", "reportOptionalMemberAccess"},
		// Short prefixes are ordinary word shapes, so they only count when
		// cited the way linters cite them — backticks or parentheses.
		{"line too long `E501`", "E501"},
		{"flake8 (B008) in a default arg", "B008"},
		{"E501 line too long", ""},
		{"B008 in a default arg", ""},
		// Not linter codes — must NOT be treated as such (finding #2).
		{"use RFC3339 for timestamps", ""},
		{"hash with SHA256 please", ""},
		{"returns HTTP200 on success", ""},
		{"the ISO8601 format", ""},
		{"This uses HTTP and TODO but no code", ""},
		{"See PR 123 for context", ""},
		// The token that minted a fake "A10" lesson on a real repo: a
		// ripgrep flag inside CodeRabbit's executed-script block.
		{"rg -n -B3 -A10 'fieldRenderer' resolve/*.go", ""},
		{"benchmarked on an A100 GPU", ""},
		// Quoted machinery is not a citation: codes inside fences and
		// details blocks don't count, even for long prefixes.
		{"see the output:\n```\nRUF001 a.py:1\n```\nlooks stale", ""},
		{"<details>runner log RUF001</details>rest of the comment", ""},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, extractRuleCode(c.body), c.body)
	}
}

func TestRepliesAreNotLessons(t *testing.T) {
	// The finding is the top-level comment; everything below it in the
	// thread is conversation ABOUT the finding — the author's "fixed",
	// the reviewer's follow-up — and mining it inflated author acks into
	// the top lessons of a real repo ("fixed" ×15).
	page := ghPage(
		comment(1, "reviewer", "User", "api/x.py", 5, 1, "Reset pooled state before reuse."),
		reply(1, comment(2, "author", "User", "api/x.py", 5, 1, "fixed")),
		reply(1, comment(3, "reviewer", "User", "api/x.py", 5, 1, "Thanks — also please clear the arena refs on this path.")),
	)

	got := mine(t, page)

	require.Len(t, got, 1, "only the top-level finding becomes a lesson")
	assert.Equal(t, 1, got[0].occ)
	assert.Equal(t, "reset pooled state before reuse", got[0].symptom)
}

func TestInsubstantialCommentsAreDropped(t *testing.T) {
	// Top-level but content-free: reactions, shorthand, bare links, and
	// bodies that normalize to nothing. None of these can guide an agent.
	page := ghPage(
		comment(1, "author", "User", "api/a.py", 1, 1, "Fixed."),
		comment(2, "reviewer", "User", "api/b.py", 2, 1, "Very smart!"),
		comment(3, "reviewer", "User", "api/c.py", 3, 1, "looks good to me"),
		comment(4, "author", "User", "api/d.py", 4, 1, "Updated in https://github.com/o/r/pull/5"),
		comment(5, "reviewer", "User", "api/e.py", 5, 1, "```suggestion\nfoo()\n```"),
	)

	assert.Empty(t, mine(t, page))
}

func TestCodeRabbitFindingBetweenDetailsBlocks(t *testing.T) {
	// The real CodeRabbit layout (from wundergraph/graphql-go-tools
	// PR 1605): severity tagline, an Analysis-chain details block with an
	// executed script, THEN the bold finding, then the AI-prompt details
	// block and fingerprint comments. Greedy details-stripping used to
	// delete the finding and the script's `rg -A10` minted a fake "A10"
	// rule-code lesson.
	body := "_🎯 Functional Correctness_ | _🟠 Major_ | _⚡ Quick win_\n\n" +
		"<details>\n<summary>🧩 Analysis chain</summary>\n\n🏁 Script executed:\n\n" +
		"```shell\nrg -n -B3 -A10 'fieldRenderer' v2/pkg/engine/resolve/*.go\n```\n\n" +
		"Repository: o/r\n</details>\n\n" +
		"**Include the response-shaping context in this grouping guard.** " +
		"`resolutionGroupKey` still ignores per-subscriber output knobs.\n\n" +
		"<details>\n<summary>🤖 Prompt for AI Agents</summary>\nprompt text\n</details>\n\n" +
		"<!-- fingerprinting:phantom -->"

	page := ghPage(comment(1, "coderabbitai[bot]", "Bot", "v2/pkg/engine/resolve/resolve.go", 100, 1605, body))

	got := mine(t, page)

	require.Len(t, got, 1)
	assert.Equal(t, "include the response shaping context in this grouping guard",
		got[0].symptom, "the bold finding between the details blocks is the fingerprint")
	assert.Equal(t, "v2/pkg/engine/resolve/resolve.go", got[0].region)
}

func TestMergeAcrossFiles(t *testing.T) {
	// The same un-coded fingerprint in several files is a habit of the
	// area: merged to the deepest common directory, occurrences summed —
	// per-file counting would leave each copy below the threshold.
	page := ghPage(
		comment(1, "reviewer", "User", "engine/resolve/a.go", 1, 1, "Reset pooled state before reuse."),
		comment(2, "coderabbitai[bot]", "Bot", "engine/resolve/b.go", 2, 2, "reset pooled state before reuse"),
		comment(3, "reviewer", "User", "engine/visitor/c.go", 3, 3, "Reset pooled state before reuse!"),
	)

	got, findings := mineWithFindings(t, page)

	require.Len(t, got, 1)
	assert.Equal(t, "engine", got[0].region, "widened to the deepest common directory")
	assert.Equal(t, 3, got[0].occ)
	assert.Equal(t, "mixed", got[0].reviewer, "reviewer sets merge too")

	// The findings follow the merge: all three link to the merged lesson.
	require.Len(t, findings, 3)

	for _, f := range findings {
		assert.Equal(t, "engine\x00reset pooled state before reuse", f.LessonKey,
			"finding %d must be remapped to the merged lesson", f.ID)
	}
}

func TestFindingsCarryTheEvidence(t *testing.T) {
	// Every comment that becomes (part of) a lesson is kept as a Finding
	// linked to it; dropped comments (replies, no substance) leave none.
	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "research/a.py", 10, 100, "Docstring uses a non-ASCII character. `RUF001`"),
		comment(2, "coderabbitai[bot]", "Bot", "research/b.py", 20, 101, "Ambiguous unicode: `RUF001` please fix"),
		reply(1, comment(3, "author", "User", "research/a.py", 10, 100, "fixed")),
	)

	got, findings := mineWithFindings(t, page)

	require.Len(t, got, 1)
	require.Len(t, findings, 2, "two accepted comments, the reply left out")

	for _, f := range findings {
		assert.Equal(t, "research\x00RUF001", f.LessonKey)
		assert.NotEmpty(t, f.Body)
		assert.NotEmpty(t, f.URL)
	}

	assert.Equal(t, int64(1), findings[0].ID, "GitHub comment id is the finding id")
	assert.Equal(t, 100, findings[0].PR)
	assert.Equal(t, "research/a.py", findings[0].Path)
}

func TestFindingBodyKeepsFencesDropsMachinery(t *testing.T) {
	body := "**Fix the guard.**\n\n<details>\n<summary>🧩 Analysis chain</summary>\nscript output\n</details>\n\n" +
		"```suggestion\nif x == nil { return }\n```\n\n<!-- fingerprint -->"

	got := findingBody(body)

	assert.Contains(t, got, "**Fix the guard.**")
	assert.Contains(t, got, "```suggestion", "the suggested fix is the valuable part")
	assert.Contains(t, got, "if x == nil { return }")
	assert.NotContains(t, got, "Analysis chain", "details blocks are machinery")
	assert.NotContains(t, got, "fingerprint", "HTML comments are machinery")

	// A pathological body is capped without splitting a rune.
	long := strings.Repeat("é", findingBodyCap)
	capped := findingBody(long)
	assert.LessOrEqual(t, len(capped), findingBodyCap)
	assert.True(t, strings.HasSuffix(capped, "é"), "cap must cut at a rune boundary")
}

func TestMergeStopsAtRepoRoot(t *testing.T) {
	// A fingerprint scattered across unrelated top-level trees must NOT
	// merge: the common region would be the repo root, and a root lesson
	// fires on every edit everywhere.
	page := ghPage(
		comment(1, "reviewer", "User", "engine/a.go", 1, 1, "Reset pooled state before reuse."),
		comment(2, "reviewer", "User", "docs/b.md", 2, 2, "Reset pooled state before reuse."),
	)

	got := mine(t, page)

	require.Len(t, got, 2, "cross-tree fingerprints stay file-scoped")

	for _, l := range got {
		assert.Equal(t, 1, l.occ)
	}
}

func TestRuleCodeLessonsDoNotMergeAcrossDirectories(t *testing.T) {
	// Coded lessons cluster per directory by design — a package's habit.
	// The cross-file merge must leave them alone.
	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "pkg1/a.py", 1, 1, "Non-ascii `RUF001` in the docstring"),
		comment(2, "coderabbitai[bot]", "Bot", "pkg2/b.py", 2, 2, "Non-ascii `RUF001` in the docstring"),
	)

	got := mine(t, page)

	require.Len(t, got, 2, "one lesson per package, not one merged")
}

func TestParseConcatenatedPages(t *testing.T) {
	// `gh api --paginate` concatenates each page's array back-to-back;
	// this is the exact shape the streaming decoder exists to handle, and
	// a plain json.Unmarshal would fail on the second '['.
	page1 := ghPage(comment(1, "coderabbitai[bot]", "Bot", "a.py", 1, 1, "`RUF001`"))
	page2 := ghPage(comment(2, "yuribuerov", "User", "b.py", 2, 2, "looks fine"))

	got, err := parseComments([]byte(page1 + page2))
	require.NoError(t, err)
	require.Len(t, got, 2, "both concatenated pages must decode")
	assert.Equal(t, int64(1), got[0].ID)
	assert.Equal(t, int64(2), got[1].ID)
}

func TestMineFetchedFlag(t *testing.T) {
	root := t.TempDir()

	for _, args := range [][]string{
		{"init"}, {"remote", "add", "origin", "git@github.com:o/r.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run())
	}

	// A successful fetch — even of zero comments — is Fetched (safe to
	// replace the stored set with).
	res, err := Mine(root, Options{}, func(string, string, Options) ([]byte, error) {
		return []byte("[]"), nil
	})
	require.NoError(t, err)
	assert.True(t, res.Fetched)
	assert.Empty(t, res.Lessons)

	// A fetch failure is NOT Fetched — callers must not wipe on it.
	res, err = Mine(root, Options{}, func(string, string, Options) ([]byte, error) {
		return nil, assert.AnError
	})
	require.NoError(t, err)
	assert.False(t, res.Fetched, "a failed fetch must not license a set-swap")
}

func TestCommentsWithoutPathAreDropped(t *testing.T) {
	// A PR-level (non-inline) comment has an empty path and cannot anchor
	// to a region.
	page := ghPage(comment(1, "coderabbitai[bot]", "Bot", "", 0, 1, "Overall looks good. `RUF001` mentioned"))

	got := mine(t, page)
	assert.Empty(t, got)
}

func TestGithubSlugParsing(t *testing.T) {
	cases := []struct {
		url, owner, repo string
		ok               bool
	}{
		{"git@github.com:YuriBuerov/edge-monitor.git", "YuriBuerov", "edge-monitor", true},
		{"https://github.com/o/r.git", "o", "r", true},
		{"https://github.com/o/r", "o", "r", true},
		{"ssh://git@github.com/o/r.git", "o", "r", true},
		{"git@gitlab.com:o/r.git", "", "", false},
	}

	for _, c := range cases {
		m := remoteRe.FindStringSubmatch(c.url)
		if !c.ok {
			assert.Nil(t, m, c.url)
			continue
		}

		require.NotNil(t, m, c.url)
		assert.Equal(t, c.owner, m[1], c.url)
		assert.Equal(t, c.repo, m[2], c.url)
	}
}

func TestMineEndToEndWithInjectedFetcher(t *testing.T) {
	root := t.TempDir()

	for _, args := range [][]string{
		{"init"}, {"remote", "add", "origin", "git@github.com:o/r.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run())
	}

	// The workspace has Python, so Python linter codes are live.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "a.py"), []byte("x = 1\n"), 0o644))

	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "pkg/a.py", 1, 1, "`RUF001` non-ascii"),
		comment(2, "coderabbitai[bot]", "Bot", "pkg/b.py", 2, 2, "`RUF001` again"),
	)

	fetch := func(owner, repo string, _ Options) ([]byte, error) {
		assert.Equal(t, "o", owner)
		assert.Equal(t, "r", repo)

		return []byte(page), nil
	}

	res, err := Mine(root, Options{}, fetch)
	require.NoError(t, err)
	require.Len(t, res.Lessons, 1)
	assert.Equal(t, "RUF001", res.Lessons[0].Symptom)
	assert.Equal(t, 2, res.Lessons[0].Occurrences)
}

func TestMineGatesLinterCodesOnLanguage(t *testing.T) {
	// Every recognized rule-code family is a Python linter; in a repo
	// with no Python a match like `RUF001` in quoted output is a token
	// collision. The gate drops the code and the comment clusters by its
	// message instead.
	root := t.TempDir()

	for _, args := range [][]string{
		{"init"}, {"remote", "add", "origin", "git@github.com:o/r.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run())
	}

	page := ghPage(
		comment(1, "coderabbitai[bot]", "Bot", "pkg/a.go", 1, 1, "Docstring uses a non-ascii char `RUF001`"),
	)
	fetch := func(string, string, Options) ([]byte, error) { return []byte(page), nil }

	res, err := Mine(root, Options{}, fetch)
	require.NoError(t, err)
	require.Len(t, res.Lessons, 1)
	assert.NotEqual(t, "RUF001", res.Lessons[0].Symptom, "no Python in the repo — not a citation")
	assert.Equal(t, "pkg/a.go", res.Lessons[0].Region, "falls back to file-scoped message clustering")

	// The same repo grown a Python file flips the gate.
	require.NoError(t, os.WriteFile(filepath.Join(root, "tool.py"), []byte("x = 1\n"), 0o644))

	res, err = Mine(root, Options{}, fetch)
	require.NoError(t, err)
	require.Len(t, res.Lessons, 1)
	assert.Equal(t, "RUF001", res.Lessons[0].Symptom)
	assert.Equal(t, "pkg", res.Lessons[0].Region)
}

func TestMineDegradesWithoutGitHubRemote(t *testing.T) {
	root := t.TempDir() // no git repo, no remote

	res, err := Mine(root, Options{}, func(string, string, Options) ([]byte, error) {
		t.Fatal("fetcher must not be called without a GitHub remote")
		return nil, nil
	})

	require.NoError(t, err, "an absent source is a Note, never an error")
	assert.Empty(t, res.Lessons)
	assert.Contains(t, res.Note, "not a GitHub")
}

func TestMineDegradesWhenFetchFails(t *testing.T) {
	root := t.TempDir()

	for _, args := range [][]string{
		{"init"}, {"remote", "add", "origin", "https://github.com/o/r.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run())
	}

	res, err := Mine(root, Options{}, func(string, string, Options) ([]byte, error) {
		return nil, assert.AnError
	})

	require.NoError(t, err, "a fetch failure (no auth, no gh) must not abort indexing")
	assert.Empty(t, res.Lessons)
	assert.Contains(t, res.Note, "skipped")
}

func TestParseDropsDuplicatesAcrossPages(t *testing.T) {
	// GitHub pagination is not a snapshot: an item shifting across a page
	// boundary mid-fetch arrives on two pages. The duplicate must not
	// double-count a lesson — or collide on the finding table's primary
	// key and fail the entire mine.
	dup := comment(7, "coderabbitai[bot]", "Bot", "a.py", 1, 1, "Non-ascii char in the docstring `RUF001`")

	got, err := parseComments([]byte(ghPage(dup) + ghPage(dup)))
	require.NoError(t, err)
	require.Len(t, got, 1, "the duplicated page item must be dropped")
}

func TestParseHandlesEmptyAndGarbage(t *testing.T) {
	empty, err := parseComments([]byte("  "))
	require.NoError(t, err)
	assert.Empty(t, empty)

	empty, err = parseComments([]byte("[]"))
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, err = parseComments([]byte("{not an array"))
	require.Error(t, err)
}
