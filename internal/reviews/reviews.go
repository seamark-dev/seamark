// Package reviews mines pull-request review comments into lessons: the
// recurring feedback a repository's reviewers — CodeRabbit, Copilot, or
// humans — leave on the same kind of change. It is the capture half of
// RFC-001 §5.4's anti-repeat loop, and the source is reviewer-agnostic:
// every reviewer's comments arrive through the same GitHub API, so a bot
// and a human are clustered the same way.
//
// The network is reached through an injectable Fetcher; the default
// shells out to `gh`, and every failure mode (no gh, no auth, no GitHub
// remote, no PRs) degrades to an empty result with a note, never an
// error that aborts indexing.
package reviews

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// Comment is one review comment, normalized from the GitHub API.
type Comment struct {
	ID        int64
	Reviewer  string // classified: coderabbit | copilot | bot | human
	Author    string // raw user.login
	Body      string
	Path      string // repo-relative file the comment lands on
	Line      int    // resolved line (line, else original_line)
	URL       string // html_url, for provenance
	CreatedAt int64  // unix seconds
	PR        int    // pull-request number
	RuleCode  string // extracted linter code (RUF001, reportArgumentType…), or ""
}

// Options bounds a mining pass. Empty today; the field for a `since`
// watermark lands with incremental mining.
type Options struct{}

// Fetcher returns raw GitHub review-comment JSON (a single JSON array,
// paginated pages already concatenated) for owner/repo. Injected so
// tests never touch the network.
type Fetcher func(owner, repo string, opts Options) ([]byte, error)

// Result carries what one mining pass produced. Fetched distinguishes a
// genuine "zero comments now" (Fetched, empty Lessons — the set may be
// cleared) from a degraded run (not Fetched — the source was absent or
// unreachable, and stored lessons must be preserved). Note explains an
// empty result either way.
type Result struct {
	Lessons []model.Lesson
	Fetched bool
	Note    string
}

// Mine fetches review comments for the repository at root, parses and
// clusters them, and returns the resulting lessons. A nil fetch uses the
// gh-backed default. It never returns an error for an absent or
// unreachable source — that is a Note with Fetched=false, so callers know
// not to overwrite good data with the fruits of a failed fetch.
func Mine(root string, opts Options, fetch Fetcher) (Result, error) {
	owner, repo, ok := githubSlug(root)
	if !ok {
		return Result{Note: "not a GitHub repository"}, nil
	}

	if fetch == nil {
		fetch = ghFetch
	}

	raw, err := fetch(owner, repo, opts)
	if err != nil {
		return Result{Note: "review mining skipped: " + err.Error()}, nil
	}

	comments, err := parseComments(raw)
	if err != nil {
		return Result{Note: "review comments unreadable: " + err.Error()}, nil
	}

	// From here the fetch succeeded: even zero comments is a real answer
	// (the repo has none), safe to replace the stored set with.
	if len(comments) == 0 {
		return Result{Fetched: true, Note: "no review comments found"}, nil
	}

	return Result{Lessons: cluster(comments), Fetched: true}, nil
}

// remoteRe extracts owner/repo from the common github.com remote forms:
//
//	git@github.com:owner/repo.git
//	https://github.com/owner/repo(.git)
//	ssh://git@github.com/owner/repo.git
var remoteRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/(.+?)(?:\.git)?/?$`)

// githubSlug resolves the origin remote to (owner, repo). Only github.com
// is supported — the default fetcher speaks the GitHub API through gh.
func githubSlug(root string) (owner, repo string, ok bool) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}

	m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", "", false
	}

	return m[1], m[2], true
}

// ghFetch is the default Fetcher: it asks the GitHub CLI for every review
// comment on the repo's pull requests, following pagination. gh handles
// authentication and host resolution; if it is missing or unauthenticated
// the error surfaces as a skip Note upstream.
func ghFetch(owner, repo string, opts Options) ([]byte, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/comments?per_page=100", owner, repo)

	// For an array endpoint, --paginate concatenates every page into a
	// single flat JSON array (no --slurp, which older gh lacks).
	cmd := exec.Command("gh", "api", "--paginate", path)

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(ee.Stderr))
			// gh's auth hint is multi-line and noisy; keep it short.
			if i := strings.IndexByte(msg, '\n'); i > 0 {
				msg = msg[:i]
			}

			return nil, fmt.Errorf("gh api: %s", msg)
		}

		return nil, fmt.Errorf("gh api: %w (is the GitHub CLI installed?)", err)
	}

	return out, nil
}
