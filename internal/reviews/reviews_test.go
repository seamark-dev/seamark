package reviews

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

type clustered struct {
	region, symptom, reviewer string
	occ                       int
}

// mine drives the parse+cluster pipeline directly (no git remote, no
// network) so clustering behaviour can be asserted in isolation.
func mine(t *testing.T, json string) []clustered {
	t.Helper()

	comments, err := parseComments([]byte(json))
	require.NoError(t, err)

	lessons := cluster(comments)

	got := make([]clustered, len(lessons))
	for i, l := range lessons {
		got[i] = clustered{l.Region, l.Symptom, l.Reviewer, l.Occurrences}
	}

	return got
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
		{"E501 line too long", "E501"},
		{"B008 in a default arg", "B008"},
		{"PLC0414 useless import alias", "PLC0414"},
		{"DTZ005 needs a tz", "DTZ005"},
		{"reportArgumentType: wrong type passed", "reportArgumentType"},
		{"pyright: reportOptionalMemberAccess", "reportOptionalMemberAccess"},
		// Not linter codes — must NOT be treated as such (finding #2).
		{"use RFC3339 for timestamps", ""},
		{"hash with SHA256 please", ""},
		{"returns HTTP200 on success", ""},
		{"the ISO8601 format", ""},
		{"This uses HTTP and TODO but no code", ""},
		{"See PR 123 for context", ""},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, extractRuleCode(c.body), c.body)
	}
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
