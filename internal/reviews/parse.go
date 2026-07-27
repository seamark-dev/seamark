package reviews

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ghComment mirrors the fields we use from a GitHub pull-request review
// comment (GET /repos/{owner}/{repo}/pulls/comments). Everything else in
// the payload is ignored.
type ghComment struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"` // "Bot" for app accounts
	} `json:"user"`
	Body           string `json:"body"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	OriginalLine   int    `json:"original_line"`
	HTMLURL        string `json:"html_url"`
	CreatedAt      string `json:"created_at"` // RFC3339
	PullRequestURL string `json:"pull_request_url"`
	InReplyTo      int64  `json:"in_reply_to_id"` // 0 for a top-level comment
}

// parseComments decodes the GitHub review-comment payload into
// normalized Comments. It reads successive top-level JSON arrays rather
// than a single one: `gh api --paginate` concatenates each page's array
// back-to-back (`[…][…]`) instead of merging them, so a plain Unmarshal
// fails at the second page. The decoder loop handles both the merged and
// concatenated shapes.
func parseComments(raw []byte) ([]Comment, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))

	var out []Comment

	for {
		var page []ghComment

		err := dec.Decode(&page)
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		for _, g := range page {
			out = append(out, normalize(g))
		}
	}

	return out, nil
}

// normalize converts a raw GitHub comment to our Comment shape.
func normalize(g ghComment) Comment {
	line := g.Line
	if line == 0 {
		line = g.OriginalLine // outdated comments keep only original_line
	}

	return Comment{
		ID:        g.ID,
		Reviewer:  classifyReviewer(g.User.Login, g.User.Type),
		Author:    g.User.Login,
		Body:      g.Body,
		Path:      g.Path,
		Line:      line,
		URL:       g.HTMLURL,
		CreatedAt: parseTime(g.CreatedAt),
		PR:        prNumber(g.PullRequestURL),
		RuleCode:  extractRuleCode(g.Body),
		InReplyTo: g.InReplyTo,
	}
}

// classifyReviewer buckets a comment author. The bot names are stable
// GitHub app logins; anything else with user type Bot is a generic bot,
// and the rest are humans.
func classifyReviewer(login, userType string) string {
	l := strings.ToLower(login)

	switch {
	case strings.Contains(l, "coderabbit"):
		return "coderabbit"
	case strings.Contains(l, "copilot"):
		return "copilot"
	case userType == "Bot" || strings.HasSuffix(l, "[bot]"):
		return "bot"
	default:
		return "human"
	}
}

// The rule-code families seamark recognizes: Ruff / flake8 / pycodestyle
// / pyflakes / pylint (PLC/PLE/PLR/PLW) and their plugins. Anchoring to
// real prefixes keeps a bare `[A-Z]{1,4}\d+` from swallowing tokens like
// SHA256, RFC3339, HTTP200, or ISO8601 — which would forge a rule code
// and mis-cluster unrelated comments. Extend as new linters appear;
// unknown codes just fall to the message fingerprint, so a miss
// degrades, a false positive corrupts.
//
// The families split by how safe their prefix is in prose. Three letters
// and up (RUF001) collide with nothing and match anywhere; one- and
// two-letter prefixes are ordinary word shapes (`rg -A10` once minted a
// fake "A10" lesson from a CodeRabbit analysis script), so those only
// count when cited the way linters cite them — wrapped in backticks or
// parentheses. Longer alternatives come first: Go regexps take the
// leftmost-first alternative, so `EM` must be tried before `E`.
const (
	longPrefixes = `RUF|PL[CERW]|ANN|ASYNC|BLE|COM|DTZ|ERA|EXE|FBT|FLY|FURB|` +
		`ICN|INP|ISC|NPY|PERF|PGH|PIE|PTH|PYI|RSE|RET|SIM|SLF|SLOT|TCH|TID|TRY|YTT|ARG`
	shortPrefixes = `EM|FA|PD|PT|UP|C4|C9|T1|T2|E|W|F|B|A|D|G|N|Q|S`
	shortCode     = `(?:` + shortPrefixes + `)\d{2,4}`
)

var (
	longCodeRe = regexp.MustCompile(
		`\b(?:` + longPrefixes + `)\d{2,4}\b|\breport[A-Z][A-Za-z]+\b`)
	shortCodeRe = regexp.MustCompile(
		"`(" + shortCode + ")`" + `|\((` + shortCode + `)\)`)
)

// extractRuleCode returns the first linter code cited in a comment body,
// or "" — the opportunistic fast path that lets bot comments cluster by
// (tool, rule) instead of by fuzzy message text. It reads the stripped
// body: a code inside an executed script, a fenced example, or a details
// block is quoted machinery, not a citation.
func extractRuleCode(body string) string {
	s := stripBoilerplate(body)

	if code := longCodeRe.FindString(s); code != "" {
		return code
	}

	if m := shortCodeRe.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			return m[1]
		}

		return m[2]
	}

	return ""
}

// prNumber pulls the PR number off a pull_request_url
// (…/pulls/123). Zero when absent.
func prNumber(url string) int {
	i := strings.LastIndexByte(url, '/')
	if i < 0 {
		return 0
	}

	n, err := strconv.Atoi(url[i+1:])
	if err != nil {
		return 0
	}

	return n
}

// parseTime converts a GitHub RFC3339 timestamp to unix seconds; an
// unparseable stamp yields 0, which sorts oldest rather than dropping the
// comment.
func parseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}

	return t.Unix()
}
