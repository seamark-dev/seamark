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
	"bytes"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
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
	InReplyTo int64  // id of the thread's top comment; 0 when this IS one
}

// Options bounds a mining pass. (The field for a `since` watermark
// lands with incremental mining.)
type Options struct {
	// Logf receives fetch and clustering progress; nil discards it. The
	// GitHub fetch is the silent long pole of a mine — pages of network
	// I/O — and silence reads as stuck.
	Logf func(format string, args ...any)
}

// Fetcher returns raw GitHub review-comment JSON (a single JSON array,
// paginated pages already concatenated) for owner/repo. Injected so
// tests never touch the network.
type Fetcher func(owner, repo string, opts Options) ([]byte, error)

// Result carries what one mining pass produced. Fetched distinguishes a
// genuine "zero comments now" (Fetched, empty Lessons — the set may be
// cleared) from a degraded run (not Fetched — the source was absent or
// unreachable, and stored lessons must be preserved). Note explains an
// empty result either way. Findings are the raw comments behind Lessons,
// stored alongside them so deeper passes work from full material.
type Result struct {
	Lessons  []model.Lesson
	Findings []model.Finding
	Fetched  bool
	Note     string
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

	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	logf("reviews: fetching PR comments for %s/%s…", owner, repo)

	raw, err := fetch(owner, repo, opts)
	if err != nil {
		return Result{Note: "review mining skipped: " + err.Error()}, nil
	}

	comments, err := parseComments(raw)
	if err != nil {
		return Result{Note: "review comments unreadable: " + err.Error()}, nil
	}

	logf("reviews: %d comments fetched; clustering…", len(comments))

	// From here the fetch succeeded: even zero comments is a real answer
	// (the repo has none), safe to replace the stored set with.
	if len(comments) == 0 {
		return Result{Fetched: true, Note: "no review comments found"}, nil
	}

	// Every recognized rule-code family is a Python linter today, so in
	// a repo with no Python at all any match is a token collision, never
	// a citation. Dropping the code sends the comment to the message
	// fingerprint instead. Split per-family when other languages' linters
	// join the list.
	if !hasPython(root) {
		for i := range comments {
			comments[i].RuleCode = ""
		}
	}

	lessons, findings := cluster(comments)

	return Result{Lessons: lessons, Findings: findings, Fetched: true}, nil
}

// hasPython reports whether the workspace contains Python source. git
// answers fast and gitignore-aware (-co: tracked and untracked alike);
// outside git a walk looks for the first .py file, skipping the usual
// dependency dirs.
func hasPython(root string) bool {
	cmd := exec.Command("git", "ls-files", "-z", "-co", "--exclude-standard", "--", "*.py")
	cmd.Dir = root

	if out, err := cmd.Output(); err == nil {
		return len(bytes.TrimSpace(out)) > 0
	}

	found := false

	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}

			return nil
		}

		if strings.HasSuffix(p, ".py") {
			found = true

			return filepath.SkipAll
		}

		return nil
	})

	return found
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
// the error surfaces as a skip Note upstream. Progress streams through
// opts.Logf as data arrives — a large repo is minutes of pages.
func ghFetch(owner, repo string, opts Options) ([]byte, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/comments?per_page=100", owner, repo)

	// For an array endpoint, --paginate concatenates every page into a
	// single flat JSON array (no --slurp, which older gh lacks).
	cmd := exec.Command("gh", "api", "--paginate", path)

	out := &progressWriter{logf: opts.Logf, next: progressStep}

	var errb bytes.Buffer

	cmd.Stdout = out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		// gh's auth hint is multi-line and noisy; keep it short.
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}

		if msg == "" {
			return nil, fmt.Errorf("gh api: %w (is the GitHub CLI installed?)", err)
		}

		return nil, fmt.Errorf("gh api: %s", msg)
	}

	return out.buf.Bytes(), nil
}

// progressStep is how much fetched data earns one progress line —
// roughly two pages of comments.
const progressStep = 512 * 1024

// progressWriter accumulates the fetch while reporting its size at
// coarse intervals.
type progressWriter struct {
	buf  bytes.Buffer
	logf func(format string, args ...any)
	next int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)

	for p.logf != nil && p.buf.Len() >= p.next {
		p.logf("reviews: %.1f MB fetched…", float64(p.next)/(1<<20))
		p.next += progressStep
	}

	return len(b), nil
}
