// Package reviews mines pull-request review comments into lessons: the
// recurring feedback left on the same kind of change. It is the capture
// half of RFC-001 §5.4's anti-repeat loop. The source is reviewer-agnostic:
// all review comments arrive through the same GitHub API and use the same
// clustering rules.
//
// The network is reached through an injectable Fetcher; the default
// shells out to `gh`, and every failure mode (no gh, no auth, no GitHub
// remote, no PRs) degrades to an empty result with a note, never an
// error that aborts indexing.
package reviews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

// Comment is one review comment, normalized from the GitHub API.
type Comment struct {
	ID        int64
	Reviewer  string // classified: coderabbit | copilot | bot | person
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
	// WindowDays bounds how old a review comment may be and still
	// enter the corpus (RFC-002 §9): fix mining always had a shelf
	// life; review comments were immortal, and seven-year-old feedback
	// weighed like last month's. 0 means DefaultReviewWindowDays;
	// negative means unlimited (the config's `window_days: 0`).
	WindowDays int
	// Progress receives typed progress events; nil discards them. The
	// console renders them, so it never parses the Logf prose.
	Progress func(FetchEvent)
}

// DefaultReviewWindowDays is the review-mining recency window — twice
// the fix window, because review prose ages better than patches.
const DefaultReviewWindowDays = 730

// reviewCountFloor keeps slow repositories alive: at least this many
// of the NEWEST comments survive the window, so a repo with three pull
// requests a year keeps a working corpus with zero configuration.
const reviewCountFloor = 200

// windowComments applies the recency window with its count floor:
// newest first, everything inside the window, topped up to the floor
// from beyond it.
func windowComments(comments []Comment, windowDays int, now time.Time) []Comment {
	if windowDays < 0 {
		return comments
	}

	if windowDays == 0 {
		windowDays = DefaultReviewWindowDays
	}

	cutoff := now.AddDate(0, 0, -windowDays).Unix()

	sorted := make([]Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt > sorted[j].CreatedAt
	})

	var out []Comment

	for _, c := range sorted {
		if c.CreatedAt >= cutoff || len(out) < reviewCountFloor {
			out = append(out, c)
		}
	}

	return out
}

// Fetcher returns raw GitHub review-comment JSON (a single JSON array,
// paginated pages already concatenated) for owner/repo. Injected so
// tests never touch the network. Mine takes it; MineContext takes the
// context-aware FetchFunc.
type Fetcher func(owner, repo string, opts Options) ([]byte, error)

// FetchFunc is the context-aware form of Fetcher. A fetcher must stop
// its work when ctx ends, because a stop request from the console
// cancels ctx and expects no further network use.
type FetchFunc func(ctx context.Context, owner, repo string, opts Options) ([]byte, error)

// Outcome classifies one mining pass, so callers do not parse Note. The
// zero value means that no mining pass ran.
type Outcome int

const (
	// OutcomeFetched means the fetch succeeded. The result may be empty,
	// and an empty result is still an authoritative answer.
	OutcomeFetched Outcome = iota + 1
	// OutcomeNoRepository means origin does not name a GitHub repository.
	// The miner does not call the fetcher.
	OutcomeNoRepository
	// OutcomeGHMissing means the gh executable is not on PATH.
	OutcomeGHMissing
	// OutcomeAuth means an authentication problem. gh exits with code 4
	// when it has no login. GitHub answers HTTP 401 for a bad or expired
	// token. It answers HTTP 403 when the token lacks a scope, an SSO
	// grant, or access.
	OutcomeAuth
	// OutcomeRateLimited means that a GitHub rate limit refused the
	// fetch: HTTP 429, or HTTP 403 with a rate-limit message. A later
	// fetch can succeed, so the guidance differs from a login problem.
	OutcomeRateLimited
	// OutcomeFetchFailed means any other fetch error, for example a
	// network error, a server error, or an unknown repository.
	OutcomeFetchFailed
	// OutcomeUnreadable means the fetched bytes are not the API JSON.
	OutcomeUnreadable
	// OutcomeStopped means ctx ended during or after the fetch. The miner
	// then returns no data, so a partial corpus never replaces the stored one.
	OutcomeStopped
)

// String returns a stable lower-case name for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeFetched:
		return "fetched"
	case OutcomeNoRepository:
		return "no-repository"
	case OutcomeGHMissing:
		return "gh-missing"
	case OutcomeAuth:
		return "auth"
	case OutcomeRateLimited:
		return "rate-limited"
	case OutcomeFetchFailed:
		return "fetch-failed"
	case OutcomeUnreadable:
		return "unreadable"
	case OutcomeStopped:
		return "stopped"
	default:
		return "none"
	}
}

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
	// Outcome is the typed form of Fetched and Note.
	Outcome Outcome
	// Err is the cause of a degraded pass, or nil. It can quote gh
	// output, so a caller sanitizes and redacts it before display.
	Err error
	// Repository is the resolved GitHub target. It is empty when origin
	// does not name a GitHub repository.
	Repository Repository
	// Comments counts the parsed comments before the recency window.
	Comments int
	// Kept counts the comments that the window and its floor keep.
	Kept int
}

// FetchStage names one step of a mining pass for typed progress.
type FetchStage int

const (
	// StageFetching means the fetch starts. No total size is known.
	StageFetching FetchStage = iota + 1
	// StageBytes reports the bytes received so far. It is activity, not
	// a completion estimate, because GitHub does not announce a total.
	StageBytes
	// StageFetched reports the parsed and kept comment counts.
	StageFetched
	// StageClustering means the comments are clustered into lessons.
	StageClustering
)

// FetchEvent is one typed progress event of a mining pass. The console
// shows it; the CLI keeps its Logf lines.
type FetchEvent struct {
	Stage FetchStage
	// Repository is the target of the fetch.
	Repository Repository
	// Bytes is the received byte count for StageBytes.
	Bytes int
	// Comments and Kept carry the counts for StageFetched.
	Comments int
	Kept     int
}

// Mine fetches review comments for the repository at root, parses and
// clusters them, and returns the resulting lessons. A nil fetch uses the
// gh-backed default. It never returns an error for an absent or
// unreachable source — that is a Note with Fetched=false, so callers know
// not to overwrite good data with the fruits of a failed fetch.
func Mine(root string, opts Options, fetch Fetcher) (Result, error) {
	var ctxFetch FetchFunc

	// A nil Fetcher must stay a nil FetchFunc, so MineContext selects gh.
	if fetch != nil {
		ctxFetch = func(_ context.Context, owner, repo string, o Options) ([]byte, error) {
			return fetch(owner, repo, o)
		}
	}

	return MineContext(context.Background(), root, opts, ctxFetch)
}

// MineContext is Mine with a context and a context-aware fetcher. When
// ctx ends during or after the fetch, it returns OutcomeStopped without
// data, even if the fetcher returned data. A stopped pass therefore
// never publishes a partial corpus. A nil fetch uses gh.
func MineContext(ctx context.Context, root string, opts Options, fetch FetchFunc) (Result, error) {
	repo, state := ResolveRepository(root)
	if state != RepositoryGitHub {
		return Result{Note: "not a GitHub repository", Outcome: OutcomeNoRepository}, nil
	}

	// A stop before the fetch must not start gh at all.
	if err := ctx.Err(); err != nil {
		return stopped(repo, err), nil
	}

	if fetch == nil {
		fetch = ghFetch
	}

	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	progress := opts.Progress
	if progress == nil {
		progress = func(FetchEvent) {}
	}

	logf("reviews: fetching PR comments for %s/%s…", repo.Owner, repo.Name)
	progress(FetchEvent{Stage: StageFetching, Repository: repo})

	raw, err := fetch(ctx, repo.Owner, repo.Name, opts)

	// The context wins over the fetch result: data that arrives after a
	// stop may be partial, and a stop is the user's decision.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stopped(repo, ctxErr), nil
	}

	if err != nil {
		return Result{
			Note: "review mining skipped: " + err.Error(), Outcome: classifyFetch(err),
			Err: err, Repository: repo,
		}, nil
	}

	comments, err := parseComments(raw)
	if err != nil {
		return Result{
			Note: "review comments unreadable: " + err.Error(), Outcome: OutcomeUnreadable,
			Err: err, Repository: repo,
		}, nil
	}

	fetched := len(comments)
	comments = windowComments(comments, opts.WindowDays, time.Now())

	if dropped := fetched - len(comments); dropped > 0 {
		logf("reviews: %d comments fetched; %d beyond the recency window dropped, clustering…",
			fetched, dropped)
	} else {
		logf("reviews: %d comments fetched; clustering…", fetched)
	}

	progress(FetchEvent{Stage: StageFetched, Repository: repo, Comments: fetched, Kept: len(comments)})

	// From here the fetch succeeded: even zero comments is a real answer
	// (the repo has none), safe to replace the stored set with.
	if len(comments) == 0 {
		return Result{
			Fetched: true, Note: "no review comments found", Outcome: OutcomeFetched,
			Repository: repo, Comments: fetched,
		}, nil
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

	progress(FetchEvent{Stage: StageClustering, Repository: repo})

	lessons, findings := cluster(comments)

	// Clustering takes time on a large corpus. A stop during it still
	// discards the result, because the caller has not persisted it yet.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stopped(repo, ctxErr), nil
	}

	return Result{
		Lessons: lessons, Findings: findings, Fetched: true, Outcome: OutcomeFetched,
		Repository: repo, Comments: fetched, Kept: len(comments),
	}, nil
}

// stopped is the result of a pass that ctx ended. Fetched stays false,
// so every caller keeps the stored corpus.
func stopped(repo Repository, err error) Result {
	return Result{
		Note: "review mining stopped", Outcome: OutcomeStopped, Err: err, Repository: repo,
	}
}

// ghAuthExitCode is the gh exit code for an authentication problem, as
// `gh help exit-codes` documents it.
const ghAuthExitCode = 4

// classifyFetch maps a fetch error to an Outcome. It reads only typed
// values of the error chain, never the error text. ghFetch sets these
// values when it reads the gh output.
func classifyFetch(err error) Outcome {
	if errors.Is(err, exec.ErrNotFound) {
		return OutcomeGHMissing
	}

	var ghErr *ghError
	if errors.As(err, &ghErr) {
		if o, ok := ghErr.outcome(); ok {
			return o
		}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == ghAuthExitCode {
		return OutcomeAuth
	}

	return OutcomeFetchFailed
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

// OriginRemote is the only remote that the miner reads.
const OriginRemote = "origin"

// Repository is the GitHub repository that the miner fetches from. It
// holds no URL, because a remote URL can carry credentials.
type Repository struct {
	// Remote is the git remote name. It is always OriginRemote today.
	Remote string
	Owner  string
	Name   string
}

// Slug returns "owner/name", or "" for an unresolved repository.
func (r Repository) Slug() string {
	if r.Owner == "" {
		return ""
	}

	return r.Owner + "/" + r.Name
}

// RepositoryState tells whether ResolveRepository found a GitHub target.
type RepositoryState int

const (
	// RepositoryUnresolved is the zero value: nothing resolved the
	// origin yet. It never reads as a GitHub target by accident.
	RepositoryUnresolved RepositoryState = iota
	// RepositoryGitHub means origin names a github.com repository.
	RepositoryGitHub
	// RepositoryNoOrigin means git fails or no origin remote exists.
	RepositoryNoOrigin
	// RepositoryNotGitHub means origin names another host.
	RepositoryNotGitHub
)

// String returns a stable lower-case name for the state.
func (s RepositoryState) String() string {
	switch s {
	case RepositoryGitHub:
		return "github"
	case RepositoryNoOrigin:
		return "no-origin"
	case RepositoryNotGitHub:
		return "not-github"
	default:
		return "unresolved"
	}
}

// ResolveRepository resolves the origin remote at root to a GitHub
// repository. It runs git locally and never contacts the network. The
// miner and the console preview call it both, so the preview names the
// repository that the fetch uses. Only github.com is supported, because
// the default fetcher speaks the GitHub API through gh.
func ResolveRepository(root string) (Repository, RepositoryState) {
	cmd := exec.Command("git", "remote", "get-url", OriginRemote)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return Repository{}, RepositoryNoOrigin
	}

	m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return Repository{}, RepositoryNotGitHub
	}

	return Repository{Remote: OriginRemote, Owner: m[1], Name: m[2]}, RepositoryGitHub
}

// ghWaitDelay bounds pipe cleanup after gh exits or is killed, even
// when a descendant keeps a pipe open. It has the value of the agent
// invoker's delay, which has the same job. Tests can shorten it.
var ghWaitDelay = 5 * time.Second

// ghError keeps the cause of a gh failure in the error chain. Its text
// is the short message that Note and the CLI have always shown, so the
// chain adds the exit code and the HTTP status without a change to any
// output.
type ghError struct {
	msg string
	err error
	// status is the HTTP status that gh reports; 0 when gh names none,
	// for example after a network error.
	status int
	// rateLimited is true when the GitHub message reports a rate limit.
	rateLimited bool
}

// Error returns the short message.
func (e *ghError) Error() string { return e.msg }

// Unwrap returns the cause, usually an *exec.ExitError.
func (e *ghError) Unwrap() error { return e.err }

// outcome maps the HTTP status of the failure to an Outcome. ok is false
// when the status alone does not classify the failure; the exit code
// then decides.
func (e *ghError) outcome() (o Outcome, ok bool) {
	switch {
	case e.status == http.StatusTooManyRequests:
		return OutcomeRateLimited, true
	case e.status == http.StatusForbidden && e.rateLimited:
		return OutcomeRateLimited, true
	case e.status == http.StatusUnauthorized, e.status == http.StatusForbidden:
		// A 403 without a rate limit means that the token lacks a scope,
		// an SSO grant, or access. The user repairs each case with gh auth.
		return OutcomeAuth, true
	default:
		return 0, false
	}
}

// ghStatusRe matches the first stderr line of a failed gh api call. gh
// writes "gh: <message> (HTTP <status>)" when GitHub sends a message,
// and "gh: HTTP <status>" when it sends none.
var ghStatusRe = regexp.MustCompile(`^gh: (?:(.*) \(HTTP (\d{3})\)|HTTP (\d{3}))$`)

// newGHError builds the error of a failed gh run from the first stderr
// line. A line without the status suffix gives status 0, and the exit
// code then sets the outcome. If gh changes its format, a rate limit or
// a bad token reads as a fetch error again, never as a success.
func newGHError(line string, err error) *ghError {
	e := &ghError{msg: "gh api: " + line, err: err}

	m := ghStatusRe.FindStringSubmatch(line)
	if m == nil {
		return e
	}

	code := m[2]
	if code == "" {
		code = m[3]
	}

	// The pattern admits exactly three digits, so the conversion cannot fail.
	e.status, _ = strconv.Atoi(code)
	e.rateLimited = isRateLimitMessage(m[1])

	return e
}

// isRateLimitMessage reports whether a GitHub error message names a rate
// limit. GitHub documents that a 403 rate-limit reply says so in its
// message. Without the reply headers, the message is the only signal.
func isRateLimitMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "rate limit")
}

// ghFetch is the default FetchFunc: it asks the GitHub CLI for every
// review comment on the repo's pull requests, following pagination. gh
// handles authentication and host resolution; if it is missing or
// unauthenticated the error surfaces as a skip Note upstream. Progress
// streams through opts.Logf and opts.Progress as data arrives — a large
// repo is minutes of pages. Cancelling ctx kills gh, and the partial
// output is discarded.
func ghFetch(ctx context.Context, owner, repo string, opts Options) ([]byte, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/comments?per_page=100", owner, repo)

	// For an array endpoint, --paginate concatenates every page into a
	// single flat JSON array (no --slurp, which older gh lacks).
	cmd := exec.CommandContext(ctx, "gh", "api", "--paginate", path)
	cmd.WaitDelay = ghWaitDelay

	out := &progressWriter{
		logf: opts.Logf, progress: opts.Progress, next: progressStep,
		repo: Repository{Remote: OriginRemote, Owner: owner, Name: repo},
	}

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

		return nil, newGHError(msg, err)
	}

	return out.buf.Bytes(), nil
}

// progressStep is how much fetched data earns one progress line —
// roughly two pages of comments.
const progressStep = 512 * 1024

// progressWriter accumulates the fetch while reporting its size at
// coarse intervals, as a Logf line and as a typed event.
type progressWriter struct {
	buf      bytes.Buffer
	logf     func(format string, args ...any)
	progress func(FetchEvent)
	repo     Repository
	next     int
}

// Write buffers b and reports each progress step that the buffer passes.
func (p *progressWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)

	for (p.logf != nil || p.progress != nil) && p.buf.Len() >= p.next {
		if p.logf != nil {
			p.logf("reviews: %.1f MB fetched…", float64(p.next)/(1<<20))
		}

		if p.progress != nil {
			p.progress(FetchEvent{Stage: StageBytes, Repository: p.repo, Bytes: p.next})
		}

		p.next += progressStep
	}

	return len(b), nil
}
