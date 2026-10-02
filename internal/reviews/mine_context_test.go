package reviews

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// originRepo creates a git repository with origin set to url. An empty
// url creates a repository without a remote.
func originRepo(t *testing.T, url string) string {
	t.Helper()

	root := t.TempDir()
	cmds := [][]string{{"init"}}

	if url != "" {
		cmds = append(cmds, []string{"remote", "add", "origin", url})
	}

	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	return root
}

// exitError returns a real *exec.ExitError with the given code. A fake
// fetcher returns it to act like a failed gh process.
func exitError(t *testing.T, code int) error {
	t.Helper()

	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)

	return err
}

// ghScript puts a gh script first on PATH. The rest of PATH stays, so
// git still resolves.
func ghScript(t *testing.T, body string) {
	t.Helper()

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestResolveRepositoryStates(t *testing.T) {
	repo, state := ResolveRepository(t.TempDir())
	assert.Equal(t, RepositoryNoOrigin, state, "a directory outside git has no origin")
	assert.Empty(t, repo.Slug())

	_, state = ResolveRepository(originRepo(t, ""))
	assert.Equal(t, RepositoryNoOrigin, state)

	_, state = ResolveRepository(originRepo(t, "git@gitlab.com:o/r.git"))
	assert.Equal(t, RepositoryNotGitHub, state)

	repo, state = ResolveRepository(originRepo(t, "https://github.com/o/r.git"))
	assert.Equal(t, RepositoryGitHub, state)
	assert.Equal(t, Repository{Remote: "origin", Owner: "o", Name: "r"}, repo)
	assert.Equal(t, "o/r", repo.Slug())

	assert.Equal(t, "github", RepositoryGitHub.String())
	assert.Equal(t, "no-origin", RepositoryNoOrigin.String())
	assert.Equal(t, "not-github", RepositoryNotGitHub.String())
	assert.Equal(t, "unresolved", RepositoryState(0).String(), "the zero value is never GitHub")
}

func TestMineContextClassifiesEveryOutcome(t *testing.T) {
	page := ghPage(
		comment(1, "rev", "User", "a.go", 1, 1, "Reset pooled state before reuse here."),
		comment(2, "rev", "User", "", 0, 1, "Overall fine."),
	)
	missing := fmt.Errorf("gh api: %w (is the GitHub CLI installed?)",
		&exec.Error{Name: "gh", Err: exec.ErrNotFound})
	auth := &ghError{msg: "gh api: To get started with GitHub CLI", err: exitError(t, 4)}

	cases := []struct {
		name     string
		raw      string
		err      error
		outcome  Outcome
		fetched  bool
		lessons  int
		comments int
		note     string
	}{
		{name: "fetched", raw: page, outcome: OutcomeFetched, fetched: true, lessons: 1, comments: 2},
		{name: "empty", raw: "[]", outcome: OutcomeFetched, fetched: true, note: "no review comments found"},
		{name: "gh missing", err: missing, outcome: OutcomeGHMissing, note: "review mining skipped: " + missing.Error()},
		{name: "auth", err: auth, outcome: OutcomeAuth, note: "review mining skipped: gh api: To get started with GitHub CLI"},
		{name: "bad token", err: newGHError("gh: Bad credentials (HTTP 401)", exitError(t, 1)),
			outcome: OutcomeAuth, note: "review mining skipped: gh api: gh: Bad credentials (HTTP 401)"},
		{name: "rate limited", err: newGHError("gh: API rate limit exceeded for user ID 1. (HTTP 403)", exitError(t, 1)),
			outcome: OutcomeRateLimited, note: "review mining skipped: gh api: gh: API rate limit exceeded"},
		{name: "other exit", err: &ghError{msg: "gh api: HTTP 404", err: exitError(t, 1)},
			outcome: OutcomeFetchFailed, note: "review mining skipped: gh api: HTTP 404"},
		{name: "plain error", err: assert.AnError, outcome: OutcomeFetchFailed},
		{name: "unreadable", raw: "not json", outcome: OutcomeUnreadable, note: "review comments unreadable: "},
	}

	root := originRepo(t, "git@github.com:o/r.git")

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := MineContext(context.Background(), root, Options{},
				func(_ context.Context, owner, repo string, _ Options) ([]byte, error) {
					assert.Equal(t, "o", owner)
					assert.Equal(t, "r", repo)

					if c.err != nil {
						return nil, c.err
					}

					return []byte(c.raw), nil
				})
			require.NoError(t, err, "a degraded source is a Note, never an error")

			assert.Equal(t, c.outcome, res.Outcome)
			assert.Equal(t, c.fetched, res.Fetched)
			assert.Len(t, res.Lessons, c.lessons)
			assert.Equal(t, c.comments, res.Comments)
			assert.Equal(t, "o/r", res.Repository.Slug())
			assert.True(t, strings.HasPrefix(res.Note, c.note), res.Note)

			if c.err != nil {
				assert.ErrorIs(t, res.Err, c.err, "the cause stays in the result")
			}

			if c.outcome == OutcomeUnreadable {
				require.Error(t, res.Err)
			}
		})
	}
}

func TestMineContextWithoutGitHubNeverFetches(t *testing.T) {
	res, err := MineContext(context.Background(), originRepo(t, "git@gitlab.com:o/r.git"), Options{},
		func(context.Context, string, string, Options) ([]byte, error) {
			t.Fatal("the fetcher must not run without a GitHub origin")

			return nil, nil
		})
	require.NoError(t, err)

	assert.Equal(t, OutcomeNoRepository, res.Outcome)
	assert.Equal(t, "not a GitHub repository", res.Note)
	assert.False(t, res.Fetched)
}

func TestMineContextKeepsTheWindowCounts(t *testing.T) {
	page := ghPage(
		comment(1, "rev", "User", "a.go", 1, 1, "Reset pooled state before reuse here."),
		comment(2, "rev", "User", "b.go", 2, 2, "Close the file handle on every error path."),
	)

	res, err := MineContext(context.Background(), originRepo(t, "git@github.com:o/r.git"),
		Options{WindowDays: -1},
		func(context.Context, string, string, Options) ([]byte, error) { return []byte(page), nil })
	require.NoError(t, err)

	assert.Equal(t, 2, res.Comments)
	assert.Equal(t, 2, res.Kept)
}

func TestMineContextNeverPublishesAfterStop(t *testing.T) {
	root := originRepo(t, "git@github.com:o/r.git")
	page := ghPage(comment(1, "rev", "User", "a.go", 1, 1, "Reset pooled state before reuse here."))

	// The fetcher returns complete data and no error, but the stop came
	// first. Late data can be partial, so the pass must discard it.
	ctx, cancel := context.WithCancel(context.Background())

	res, err := MineContext(ctx, root, Options{},
		func(context.Context, string, string, Options) ([]byte, error) {
			cancel()

			return []byte(page), nil
		})
	require.NoError(t, err)

	assert.Equal(t, OutcomeStopped, res.Outcome)
	assert.False(t, res.Fetched)
	assert.Empty(t, res.Lessons)
	assert.Empty(t, res.Findings)
	require.ErrorIs(t, res.Err, context.Canceled)
}

func TestMineContextDoesNotFetchAfterAnEarlierStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := MineContext(ctx, originRepo(t, "git@github.com:o/r.git"), Options{},
		func(context.Context, string, string, Options) ([]byte, error) {
			t.Fatal("a stopped pass must not start the fetch")

			return nil, nil
		})
	require.NoError(t, err)

	assert.Equal(t, OutcomeStopped, res.Outcome)
	assert.Equal(t, "o/r", res.Repository.Slug())
}

func TestMineContextReportsTypedProgress(t *testing.T) {
	page := ghPage(comment(1, "rev", "User", "a.go", 1, 1, "Reset pooled state before reuse here."))

	var (
		events []FetchEvent
		lines  []string
	)

	_, err := MineContext(context.Background(), originRepo(t, "git@github.com:o/r.git"), Options{
		Logf:     func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		Progress: func(e FetchEvent) { events = append(events, e) },
	}, func(context.Context, string, string, Options) ([]byte, error) { return []byte(page), nil })
	require.NoError(t, err)

	repo := Repository{Remote: "origin", Owner: "o", Name: "r"}
	assert.Equal(t, []FetchEvent{
		{Stage: StageFetching, Repository: repo},
		{Stage: StageFetched, Repository: repo, Comments: 1, Kept: 1},
		{Stage: StageClustering, Repository: repo},
	}, events)

	// The typed events are additional: the Logf lines stay the same.
	assert.Equal(t, []string{
		"reviews: fetching PR comments for o/r…",
		"reviews: 1 comments fetched; clustering…",
	}, lines)
}

func TestProgressWriterEmitsTypedBytes(t *testing.T) {
	var events []FetchEvent

	pw := &progressWriter{next: progressStep, progress: func(e FetchEvent) { events = append(events, e) }}

	chunk := make([]byte, 200*1024)

	for range 6 { // 1.2 MB in 200 KB writes
		_, err := pw.Write(chunk)
		require.NoError(t, err)
	}

	require.Len(t, events, 2, "one event per 512 KB step, also without Logf")
	assert.Equal(t, FetchEvent{Stage: StageBytes, Bytes: progressStep}, events[0])
	assert.Equal(t, FetchEvent{Stage: StageBytes, Bytes: 2 * progressStep}, events[1])
}

func TestGHFetchKeepsTheErrorTextAndTheExitCode(t *testing.T) {
	ghScript(t, "echo 'To get started with GitHub CLI, please run:  gh auth login' >&2\n"+
		"echo 'second line' >&2\nexit 4")

	_, err := ghFetch(context.Background(), "o", "r", Options{})
	require.Error(t, err)

	assert.Equal(t, "gh api: To get started with GitHub CLI, please run:  gh auth login", err.Error())

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 4, exitErr.ExitCode())
	assert.Equal(t, OutcomeAuth, classifyFetch(err))
}

// TestGHFetchClassifiesTheHTTPStatus replays the stderr of gh 2.20.2
// against a fake GitHub API. gh api exits with code 1 for every HTTP
// error, so only the status suffix tells a bad token from a network error.
func TestGHFetchClassifiesTheHTTPStatus(t *testing.T) {
	cases := []struct {
		name    string
		stderr  string
		msg     string
		outcome Outcome
	}{
		{name: "bad or expired token", stderr: "gh: Bad credentials (HTTP 401)",
			msg: "gh api: gh: Bad credentials (HTTP 401)", outcome: OutcomeAuth},
		{name: "primary rate limit",
			stderr:  "gh: API rate limit exceeded for user ID 1. (HTTP 403)",
			msg:     "gh api: gh: API rate limit exceeded for user ID 1. (HTTP 403)",
			outcome: OutcomeRateLimited},
		{name: "secondary rate limit",
			stderr:  "gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. (HTTP 403)",
			msg:     "gh api: gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. (HTTP 403)",
			outcome: OutcomeRateLimited},
		{name: "too many requests", stderr: "gh: You have exceeded a secondary rate limit. (HTTP 429)",
			msg: "gh api: gh: You have exceeded a secondary rate limit. (HTTP 429)", outcome: OutcomeRateLimited},
		{name: "SSO enforcement",
			stderr:  "gh: Resource protected by organization SAML enforcement. (HTTP 403)",
			msg:     "gh api: gh: Resource protected by organization SAML enforcement. (HTTP 403)",
			outcome: OutcomeAuth},
		{name: "missing scope",
			stderr: "gh: Resource not accessible by personal access token (HTTP 403)\n" +
				"gh: This API operation needs the \"repo\" scope. To request it, run:  gh auth refresh -s repo",
			msg:     "gh api: gh: Resource not accessible by personal access token (HTTP 403)",
			outcome: OutcomeAuth},
		{name: "unknown repository", stderr: "gh: Not Found (HTTP 404)",
			msg: "gh api: gh: Not Found (HTTP 404)", outcome: OutcomeFetchFailed},
		{name: "server error without a message", stderr: "gh: HTTP 502",
			msg: "gh api: gh: HTTP 502", outcome: OutcomeFetchFailed},
		{name: "no status", stderr: "unexpected end of JSON input",
			msg: "gh api: unexpected end of JSON input", outcome: OutcomeFetchFailed},
		{name: "status inside the message only", stderr: "gh: proxy said (HTTP 401) earlier",
			msg: "gh api: gh: proxy said (HTTP 401) earlier", outcome: OutcomeFetchFailed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ghScript(t, "printf '%s\\n' '"+strings.ReplaceAll(c.stderr, "'", `'\''`)+"' >&2\nexit 1")

			_, err := ghFetch(context.Background(), "o", "r", Options{})
			require.Error(t, err)

			assert.Equal(t, c.msg, err.Error(), "the note text stays the first stderr line")
			assert.Equal(t, c.outcome, classifyFetch(err))

			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "the exit code stays in the chain")
			assert.Equal(t, 1, exitErr.ExitCode())
		})
	}
}

func TestOutcomeNames(t *testing.T) {
	assert.Equal(t, "auth", OutcomeAuth.String())
	assert.Equal(t, "rate-limited", OutcomeRateLimited.String())
	assert.Equal(t, "fetch-failed", OutcomeFetchFailed.String())
	assert.Equal(t, "none", Outcome(0).String())
}

func TestGHFetchWithoutStderrKeepsTheInstallHint(t *testing.T) {
	ghScript(t, "exit 1")

	_, err := ghFetch(context.Background(), "o", "r", Options{})
	require.Error(t, err)

	assert.Equal(t, "gh api: exit status 1 (is the GitHub CLI installed?)", err.Error())
	assert.Equal(t, OutcomeFetchFailed, classifyFetch(err))
}

func TestGHFetchReportsAMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := ghFetch(context.Background(), "o", "r", Options{})
	require.Error(t, err)

	assert.Equal(t, `gh api: exec: "gh": executable file not found in $PATH (is the GitHub CLI installed?)`,
		err.Error())
	assert.Equal(t, OutcomeGHMissing, classifyFetch(err))
}

func TestGHFetchReturnsPromptlyAfterCancel(t *testing.T) {
	// The background sleep keeps the output pipe open after gh is
	// killed. ghWaitDelay must bound the wait, not the descendant.
	previous := ghWaitDelay
	ghWaitDelay = 200 * time.Millisecond

	t.Cleanup(func() { ghWaitDelay = previous })

	ghScript(t, "echo '['\nsleep 10 &\nexec sleep 10")

	root := originRepo(t, "git@github.com:o/r.git")
	ctx, cancel := context.WithCancel(context.Background())

	time.AfterFunc(300*time.Millisecond, cancel)

	start := time.Now()
	res, err := MineContext(ctx, root, Options{}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, OutcomeStopped, res.Outcome)
	assert.False(t, res.Fetched, "a partial page must never become a corpus")
	assert.True(t, errors.Is(res.Err, context.Canceled), res.Err)
	assert.Less(t, elapsed, 5*time.Second, "the stop returns within the wait bound")
}
