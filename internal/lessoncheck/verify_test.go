package lessoncheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the tests of Verify, the text renderer, and Combine
// (contract K5). Every repository lives under t.TempDir(), so no test
// reads or writes the checkout that runs it.

// verifyTestVersion is the seamark version that the tests pass.
const verifyTestVersion = "v0.8.0-31-g96e14ef"

// checkedAt is the time that the tests inject, and its RFC 3339 form.
var checkedAt = time.Date(2026, 10, 21, 10, 15, 2, 0, time.UTC)

const checkedAtText = "2026-10-21T10:15:02Z"

// advisoryYAML holds two advisory pins: one without a check and one
// with a check. Neither pin is executable.
const advisoryYAML = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: prose
    region: src
    note: An advisory pin without a check.
  - rule: later
    region: src
    note: An advisory pin with a check.
    check: no-todo
`

// verifyRepo returns a repository with one commit of three files, one
// per scope of mixedYAML and one outside both scopes. lessons is the
// text of .seamark/lessons.yaml, or "" for no file. The repository
// ignores .seamark/, so the configuration is never a change.
func verifyRepo(t *testing.T, lessons string) *gitRepo {
	t.Helper()

	g := newGitRepo(t)
	g.write(".git/info/exclude", ".seamark/\n")
	g.write("generated/a.txt", "a\n")
	g.write("src/a.go", "package a\n")
	g.write("docs/readme.md", "doc\n")
	g.commitAll("base")

	if lessons != "" {
		writeLessons(t, g.root, lessons)
	}

	return g
}

// verifyRequest is the request of the tests: the CLI caller, the given
// limits, a fixed time, and a fixed version.
func verifyRequest(root string, lim Limits, paths ...string) VerifyRequest {
	return VerifyRequest{
		Root:           root,
		Paths:          paths,
		Caller:         CallerCLI,
		Limits:         lim,
		Now:            func() time.Time { return checkedAt },
		SeamarkVersion: verifyTestVersion,
	}
}

// verify runs Verify with the default limits. The result must be a valid
// result.
func (g *gitRepo) verify(paths ...string) *Verification {
	g.t.Helper()

	return g.verifyCtx(context.Background(), DefaultLimits(), paths...)
}

// verifyCtx runs Verify with the given context and limits.
func (g *gitRepo) verifyCtx(ctx context.Context, lim Limits, paths ...string) *Verification {
	g.t.Helper()

	v, err := Verify(ctx, verifyRequest(g.root, lim, paths...))
	require.NoError(g.t, err)
	requireValidVerification(g.t, v)

	return v
}

// onRevalidate runs fn between the evaluation and the revalidation of
// every Verify call of the test.
func onRevalidate(t *testing.T, fn func()) {
	t.Helper()

	beforeRevalidate = fn

	t.Cleanup(func() { beforeRevalidate = nil })
}

// requireValidVerification checks the invariants that every result
// holds: each enum value is in its closed list, the slices encode as
// [], the counts add up, and the rerun command exists exactly for the
// states that exit non-zero.
func requireValidVerification(t *testing.T, v *Verification) {
	t.Helper()

	require.Equal(t, VerificationSchema, v.Schema)
	require.Equal(t, CallerCLI, v.Caller)
	require.Equal(t, BoundaryWorkingTree, v.Boundary)
	require.Contains(t, []BaselineKind{BaselineHead, BaselineEmptyTree}, v.Baseline.Kind)
	require.Contains(t, []Freshness{FreshnessFresh, FreshnessStale}, v.Freshness)
	require.Equal(t, checkedAtText, v.CheckedAt)
	require.Equal(t, verifyTestVersion, v.SeamarkVersion)
	require.NotNil(t, v.Results, "results encode as [], never null")
	require.NotNil(t, v.Listings, "listings encode as [], never null")
	require.NotNil(t, v.Binding.CheckerVersions, "checker_versions encode as [], never null")
	require.Len(t, v.Binding.SnapshotSHA256, 64)
	require.Len(t, v.Binding.PinsSHA256, 64)
	require.Equal(t, v.Config.SHA256, v.Binding.ConfigSHA256)
	requireValidUnreadable(t, v.PathsComplete, v.UnreadableDirectories, v.UnreadableDirectoriesOmitted)

	s := v.Summary
	require.Contains(t, []SummaryState{
		SummaryPassed, SummaryNoApplicableChecks, SummaryNoExecutableChecks, SummaryIncomplete, SummaryViolation,
	}, s.State)
	require.Equal(t, len(v.Results), s.PinsEvaluated)
	require.Equal(t, s.PinsEvaluated, s.Pass+s.Violation+s.NotApplicable+s.Unknown)
	require.Equal(t, len(v.Listings), s.NotCheckable+s.AdvisoryWithCheck)
	require.Equal(t, len(v.Changes), s.Changes)

	exitZero := s.State == SummaryPassed || s.State == SummaryNoApplicableChecks || s.State == SummaryNoExecutableChecks
	require.Equal(t, exitZero, v.Rerun == "", "rerun is set unless the state exits 0")

	for _, r := range v.Results {
		require.Contains(t, []Mode{ModeCheck, ModeEnforce}, r.Mode, "only an executable pin has a result")
		require.NotNil(t, r.Scope, "scope encodes as [], never null")
		require.Contains(t, []Outcome{OutcomePass, OutcomeViolation, OutcomeNotApplicable, OutcomeUnknown}, r.Outcome)
		require.Equal(t, r.Outcome == OutcomeUnknown, r.Reason != "", "reason is set iff the outcome is unknown")
		require.Equal(t, r.Reason == ReasonResourceLimit, r.Limit != "", "limit is set iff the reason is resource-limit")
	}

	data, err := json.Marshal(v)
	require.NoError(t, err)

	var back Verification
	require.NoError(t, json.Unmarshal(data, &back))
	require.Equal(t, v.Summary, back.Summary)
}

// renderText returns the text report of a result.
func renderText(v *Verification) string {
	var b strings.Builder

	RenderText(&b, v)

	return b.String()
}

// resultOf returns the result of the pin with the given entry.
func resultOf(t *testing.T, v *Verification, entry int) Result {
	t.Helper()

	for _, r := range v.Results {
		if r.Entry == entry {
			return r
		}
	}

	require.Failf(t, "no result", "no result for entry %d", entry)

	return Result{}
}

// outcomes returns "entry:outcome[/reason]" for each result, in order.
func outcomes(v *Verification) []string {
	out := make([]string, 0, len(v.Results))

	for _, r := range v.Results {
		text := fmt.Sprintf("%d:%s", r.Entry, r.Outcome)
		if r.Reason != "" {
			text += "/" + string(r.Reason)
		}

		if r.Limit != "" {
			text += "(" + r.Limit + ")"
		}

		out = append(out, text)
	}

	return out
}

// pinsDigestOf is the pins_sha256 composition written out part by part,
// without the compose helper of the package.
func pinsDigestOf(parts ...string) string {
	var b strings.Builder

	for _, part := range append([]string{"lesson-pins/1"}, parts...) {
		fmt.Fprintf(&b, "%d:%s", len(part), part)
	}

	sum := sha256.Sum256([]byte(b.String()))

	return hex.EncodeToString(sum[:])
}

// TestLessonCheckVerifySummaryStates proves the five summary states of
// contract K5, their exit errors and stderr texts, the counts, the
// listings with the --set-mode hint, the binding, the rerun command,
// and the request errors (AC-35).
func TestLessonCheckVerifySummaryStates(t *testing.T) {
	t.Run("no lessons.yaml gives no-executable-checks", func(t *testing.T) {
		g := verifyRepo(t, "")
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		v := g.verify()

		assert.Equal(t, SummaryNoExecutableChecks, v.Summary.State)
		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.Empty(t, v.Results)
		assert.Empty(t, v.Listings)
		assert.Equal(t, 1, v.Summary.Changes)
		assert.Equal(t, ConfigReport{Valid: true}, v.Config)
		assert.Equal(t, pinsDigestOf(), v.Binding.PinsSHA256, "no result gives the digest of the label alone")
		assert.Empty(t, v.Binding.CheckerVersions)
		assert.Empty(t, v.Rerun)
		require.NoError(t, v.ExitError())
		assert.True(t, v.PathsComplete)
		assert.Equal(t, Baseline{Kind: BaselineHead, Commit: g.head()}, v.Baseline)
	})

	t.Run("advisory pins are listed and never evaluated", func(t *testing.T) {
		g := verifyRepo(t, advisoryYAML)
		// The added line matches the check of the advisory pin. The
		// check never runs, so the line is no violation (§5 row 20).
		g.write("src/a.go", "package a\n\n// TODO(x)\n")

		v := g.verify()
		cfg := parseValid(t, advisoryYAML)

		assert.Equal(t, SummaryNoExecutableChecks, v.Summary.State)
		assert.Empty(t, v.Results)
		assert.Equal(t, []PinListing{
			{PinRef: cfg.Pins[0].Ref, PinVersion: cfg.Pins[0].Version, Entry: 1, Rule: "prose", Listing: ListingNotCheckable},
			{
				PinRef: cfg.Pins[1].Ref, PinVersion: cfg.Pins[1].Version, Entry: 2, Rule: "later", Listing: ListingAdvisoryWithCheck,
				Check: &CheckRef{ID: "no-todo", Kind: KindNoAddedPattern, Version: noTodoVersion},
			},
		}, v.Listings)
		assert.Equal(t, 1, v.Summary.NotCheckable)
		assert.Equal(t, 1, v.Summary.AdvisoryWithCheck)
		require.NoError(t, v.ExitError())

		text := renderText(v)
		assert.Contains(t, text, "advisory-with-check  pin "+cfg.Pins[1].Ref+" later (entry 2)")
		assert.Contains(t, text, "to run it: seamark lessons --set-mode check --pin "+cfg.Pins[1].Ref+"\n")
		assert.NotContains(t, text, "--pin "+cfg.Pins[0].Ref, "a pin without a check gets no hint")
	})

	t.Run("no change in scope gives no-applicable-checks", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("docs/readme.md", "changed\n")

		v := g.verify()

		assert.Equal(t, SummaryNoApplicableChecks, v.Summary.State)
		assert.Equal(t, []string{"1:not-applicable", "2:not-applicable"}, outcomes(v))
		assert.Equal(t, 2, v.Summary.NotApplicable)
		assert.Empty(t, v.Rerun)
		require.NoError(t, v.ExitError())
	})

	t.Run("a pass gives passed and binds the evaluated pins", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		v := g.verify()
		cfg := parseValid(t, mixedYAML)

		assert.Equal(t, SummaryPassed, v.Summary.State)
		assert.Equal(t, []string{"1:not-applicable", "2:pass"}, outcomes(v))
		assert.Equal(t, 1, v.Summary.Pass)
		assert.Equal(t, []Kind{KindForbidPathChange, KindNoAddedPattern}, v.Binding.CheckerVersions)
		assert.Equal(t, pinsDigestOf("1:"+cfg.Pins[0].Version, "2:"+cfg.Pins[1].Version), v.Binding.PinsSHA256)
		assert.Equal(t, sha256Hex(mixedYAML), v.Binding.ConfigSHA256)
		assert.Equal(t, ConfigReport{SHA256: sha256Hex(mixedYAML), LessonChecksVersion: 1, Valid: true}, v.Config)
		assert.Len(t, v.Listings, 2, "the two advisory pins of mixedYAML")
		require.NoError(t, v.ExitError())

		text := renderText(v)
		assert.Contains(t, text, "lesson checks: passed\n")
		assert.Contains(t, text, "against HEAD "+g.head()[:12]+", 1 change, fresh\n")
		assert.NotContains(t, text, "rerun:")
	})

	t.Run("an unknown gives incomplete with the K5 stderr text", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/logo.bin", "\x00\x01")

		v := g.verify()

		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:not-applicable", "2:unknown/unsupported-content"}, outcomes(v))
		assert.Equal(t, verifyCommand, v.Rerun)

		err := v.ExitError()
		require.ErrorIs(t, err, ErrIncomplete)
		assert.NotErrorIs(t, err, ErrViolation)
		assert.EqualError(t, err, "lesson checks incomplete: 1 unknown; rerun seamark lessons --verify")
		assert.Contains(t, renderText(v), "rerun: seamark lessons --verify\n")
	})

	t.Run("a violation keeps the unknown results", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("generated/a.txt", "edited\n")
		g.write("src/logo.bin", "\x00\x01")

		v := g.verify()

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{"1:violation", "2:unknown/unsupported-content"}, outcomes(v))
		assert.Equal(t, []Evidence{{Path: "generated/a.txt", Change: ChangeModify}}, resultOf(t, v, 1).Evidence)
		assert.Equal(t, verifyCommand, v.Rerun)

		err := v.ExitError()
		require.ErrorIs(t, err, ErrViolation)
		assert.NotErrorIs(t, err, ErrIncomplete)
		assert.EqualError(t, err, "lesson check violation: 1 of 2 evaluated pins (see the report)")

		text := renderText(v)
		assert.Contains(t, text, "lesson checks: violation\n")
		assert.Contains(t, text, "  violation  pin "+resultOf(t, v, 1).PinRef+" frozen (entry 1, mode enforce)\n")
		assert.Contains(t, text, "    modify generated/a.txt\n")
		assert.Contains(t, text, "  unknown/unsupported-content  pin ")
		assert.Contains(t, text, "    undecided: add src/logo.bin (unsupported-content)\n")
	})

	t.Run("a pattern match shows the line and the excerpt", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\n// TODO(yuri): remove\n")

		v := g.verify()

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []Evidence{{Path: "src/a.go", Change: ChangeModify, Line: 3, Excerpt: "// TODO(yuri): remove"}},
			resultOf(t, v, 2).Evidence)
		assert.Contains(t, renderText(v), "    modify src/a.go:3: // TODO(yuri): remove\n")
	})

	t.Run("explicit paths give an absolute rerun command", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("generated/a.txt", "edited\n")
		g.write("src/my file.go", "package a\n")

		v := g.verify("src/my file.go", "generated/a.txt")

		assert.Equal(t, SelectionExplicitPaths, v.Selection.Kind)
		assert.Equal(t, []string{"generated/a.txt", "src/my file.go"}, v.Selection.Paths)
		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, verifyCommand+
			" --file "+QuoteArg(filepath.Join(g.root, "generated", "a.txt"))+
			" --file "+QuoteArg(filepath.Join(g.root, "src", "my file.go")), v.Rerun)
		assert.Contains(t, v.Rerun, "my file.go'", "a path with a space is quoted")
	})

	t.Run("a rerun argument is quoted only when it needs it", func(t *testing.T) {
		assert.Equal(t, "/repo/src/a_b-c.go", QuoteArg("/repo/src/a_b-c.go"))
		assert.Equal(t, "'/repo/my file.go'", QuoteArg("/repo/my file.go"))
		assert.Equal(t, `'/repo/it'\''s.go'`, QuoteArg("/repo/it's.go"))
		assert.Equal(t, "'/repo/$x'", QuoteArg("/repo/$x"))
		assert.Equal(t, "'/repo/ä.go'", QuoteArg("/repo/ä.go"))
		assert.Equal(t, "''", QuoteArg(""))
	})

	t.Run("a request that no verification can serve is an error", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)

		req := verifyRequest(g.root, DefaultLimits())
		req.Caller = "robot"
		_, err := Verify(context.Background(), req)
		require.EqualError(t, err, `lesson checks: unknown caller "robot"`)

		_, err = Verify(context.Background(), verifyRequest("relative/root", DefaultLimits()))
		require.EqualError(t, err, `lesson checks: root "relative/root" is not an absolute path`)

		dir := t.TempDir()
		_, err = Verify(context.Background(), verifyRequest(dir, DefaultLimits()))
		require.EqualError(t, err, fmt.Sprintf("lesson checks: %s is not inside a git work tree", dir))

		_, err = Verify(context.Background(), verifyRequest(g.root, DefaultLimits(), "../outside"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not repository-relative")
	})

	t.Run("every caller is accepted and recorded", func(t *testing.T) {
		g := verifyRepo(t, "")

		for _, caller := range []Caller{CallerCLI, CallerMCP, CallerTUI, CallerHook} {
			req := verifyRequest(g.root, Limits{})
			req.Caller = caller

			v, err := Verify(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, caller, v.Caller)
		}
	})
}

// TestLessonCheckVerifyStaleOnChange proves the freshness rule of
// contract K5 (AC-30): each input that changes between the read and the
// publication makes the result stale. A stale result publishes no pass.
// An observed violation stays a violation with its evidence and exit 2
// (ruling K-3), and every other result becomes unknown/stale-snapshot.
func TestLessonCheckVerifyStaleOnChange(t *testing.T) {
	// passing returns a repository whose verification passes: a clean
	// line in src, nothing in generated.
	passing := func(t *testing.T) *gitRepo {
		t.Helper()

		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		return g
	}

	requireStale := func(t *testing.T, v *Verification) {
		t.Helper()

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:unknown/stale-snapshot", "2:unknown/stale-snapshot"}, outcomes(v))
		assert.Equal(t, verifyCommand, v.Rerun)
		assert.EqualError(t, v.ExitError(), "lesson checks incomplete: 2 unknown; rerun seamark lessons --verify")

		text := renderText(v)
		assert.Contains(t, text, ", stale\n")
		assert.Contains(t, text, "  stale: the inputs changed after the read")
		assert.Contains(t, text, "    first run: pass (not confirmed at publication)\n")
		assert.Contains(t, text, "    first run: not-applicable (not confirmed at publication)\n")
	}

	t.Run("unchanged inputs keep the result fresh", func(t *testing.T) {
		v := passing(t).verify()

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.Equal(t, SummaryPassed, v.Summary.State)
		assert.NotContains(t, renderText(v), "first run:")
	})

	t.Run("a new untracked file changes the status output", func(t *testing.T) {
		g := passing(t)
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		requireStale(t, g.verify())
	})

	t.Run("a new commit moves HEAD", func(t *testing.T) {
		g := passing(t)
		onRevalidate(t, func() {
			g.write("docs/readme.md", "new\n")
			g.git("commit", "-q", "-m", "docs", "--", "docs/readme.md")
		})

		requireStale(t, g.verify())
	})

	t.Run("a content edit that keeps the size and the modification time", func(t *testing.T) {
		g := passing(t)
		path := g.path("src/a.go")

		onRevalidate(t, func() {
			before, err := os.Lstat(path)
			require.NoError(t, err)

			// The status output of a modified file does not change, and
			// the Lstat identity stays. Only the content SHA-256 tells.
			require.NoError(t, os.WriteFile(path, []byte("package a\n\nfunc B() {}\n"), 0o644))
			require.NoError(t, os.Chtimes(path, before.ModTime(), before.ModTime()))

			after, err := os.Lstat(path)
			require.NoError(t, err)
			require.Equal(t, before.Size(), after.Size())
			require.Equal(t, before.ModTime(), after.ModTime())
		})

		requireStale(t, g.verify())
	})

	t.Run("a new modification time of a file whose content was not read", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/big.txt", "small\n")
		g.commitAll("big")
		g.write("src/big.txt", "this content is larger than the limit\n")

		lim := DefaultLimits()
		lim.MaxFileBytes = 16

		// The content is above max-file-bytes, so the producer keeps only
		// its Lstat identity, and the pattern pin is unknown.
		v := g.verifyCtx(context.Background(), lim)
		require.Equal(t, FreshnessFresh, v.Freshness)
		require.Equal(t, []string{"1:not-applicable", "2:unknown/resource-limit(max-file-bytes)"}, outcomes(v))

		onRevalidate(t, func() {
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(g.path("src/big.txt"), later, later))
		})

		v = g.verifyCtx(context.Background(), lim)
		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, []string{"1:unknown/stale-snapshot", "2:unknown/stale-snapshot"}, outcomes(v))
	})

	t.Run("an edited lessons.yaml", func(t *testing.T) {
		g := passing(t)
		onRevalidate(t, func() { writeLessons(t, g.root, mixedYAML+"# edited\n") })

		requireStale(t, g.verify())
	})

	t.Run("a removed lessons.yaml", func(t *testing.T) {
		g := passing(t)
		onRevalidate(t, func() { g.remove(".seamark/lessons.yaml") })

		requireStale(t, g.verify())
	})

	t.Run("a raced violation stays a violation", func(t *testing.T) {
		g := passing(t)
		g.write("generated/a.txt", "edited\n")
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verify()

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{"1:violation", "2:unknown/stale-snapshot"}, outcomes(v))
		assert.Equal(t, []Evidence{{Path: "generated/a.txt", Change: ChangeModify}}, resultOf(t, v, 1).Evidence,
			"an observed violation keeps its evidence")
		assert.Equal(t, verifyCommand, v.Rerun)

		err := v.ExitError()
		require.ErrorIs(t, err, ErrViolation)
		assert.EqualError(t, err, "lesson check violation: 1 of 2 evaluated pins (see the report)")
		assert.Equal(t, 2, Combine(v.Summary.State, false).ExitCode)

		text := renderText(v)
		assert.Contains(t, text, "    first run: pass (not confirmed at publication)\n")
		assert.NotContains(t, text, "first run: violation")
	})

	t.Run("a stale unknown loses its first-run findings", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/logo.bin", "\x00\x01")
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verify()

		r := resultOf(t, v, 2)
		assert.Equal(t, OutcomeUnknown, r.Outcome)
		assert.Equal(t, ReasonStaleSnapshot, r.Reason)
		assert.Empty(t, r.UnknownPaths, "the undecided paths of the first run are not confirmed")
		assert.Equal(t, 1, r.Coverage.PathsInScope, "the coverage of the first run stays")
	})

	t.Run("a stale result without an executable pin is incomplete", func(t *testing.T) {
		g := verifyRepo(t, "")
		g.write("src/a.go", "package a\n\nfunc A() {}\n")
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verify()

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, SummaryIncomplete, v.Summary.State, "a stale result never exits 0 (K5 exit table)")
		assert.EqualError(t, v.ExitError(), "lesson checks incomplete: 0 unknown; rerun seamark lessons --verify")
	})

	t.Run("a status output that was never read is never fresh", func(t *testing.T) {
		g := passing(t)

		lim := DefaultLimits()
		lim.MaxGitOutputBytes = 64

		// The status output passes the limit, so the producer reads no
		// status and has no status digest to compare.
		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.False(t, v.PathsComplete)
		assert.Equal(t, []string{
			"1:unknown/resource-limit(max-git-output-bytes)", "2:unknown/resource-limit(max-git-output-bytes)",
		}, outcomes(v), "an unknown result keeps the reason that names the real cause")
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
	})

	t.Run("an explicit selection reads its status again", func(t *testing.T) {
		g := passing(t)
		g.write("ignored/x.txt", "x\n")
		g.write(".git/info/exclude", ".seamark/\nignored/\n")

		v := g.verify("src", "ignored")
		require.Equal(t, FreshnessFresh, v.Freshness)

		// Step 3a lists the ignored files below a named path. A new
		// ignored file changes only that output.
		onRevalidate(t, func() { g.write("ignored/y.txt", "y\n") })

		v = g.verify("src", "ignored")
		assert.Equal(t, FreshnessStale, v.Freshness)
	})
}

// TestLessonCheckVerifyStaleKeepsFirstRunCause proves fix F-C of the P1
// review: the stale rewrite of a first-run unknown keeps its reason and
// its limit. The JSON keeps them in first_run beside stale-snapshot, and
// the text keeps a first-run line, so the cause that the user can act on
// survives the race.
func TestLessonCheckVerifyStaleKeepsFirstRunCause(t *testing.T) {
	// resultJSON returns the JSON object of the result of one pin entry.
	resultJSON := func(t *testing.T, v *Verification, entry int) map[string]any {
		t.Helper()

		data, err := json.Marshal(resultOf(t, v, entry))
		require.NoError(t, err)

		var out map[string]any
		require.NoError(t, json.Unmarshal(data, &out))

		return out
	}

	lim := DefaultLimits()
	lim.MaxDiffLines = 2

	t.Run("a raced max-diff-lines result keeps its reason and limit", func(t *testing.T) {
		// The edit gives src/a.go three lines, above the two of lim, so the
		// pattern pin is unknown/resource-limit (max-diff-lines) at the read.
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, []string{"1:unknown/stale-snapshot", "2:unknown/stale-snapshot"}, outcomes(v))

		pattern := resultJSON(t, v, 2)
		assert.Equal(t, "stale-snapshot", pattern["reason"])
		assert.NotContains(t, pattern, "limit", "limit stays set iff the reason is resource-limit")
		assert.Equal(t, map[string]any{"reason": "resource-limit", "limit": "max-diff-lines"}, pattern["first_run"])

		assert.NotContains(t, resultJSON(t, v, 1), "first_run", "a first-run not-applicable stays in the text output only")

		text := renderText(v)
		assert.Contains(t, text, "    first run: unknown/resource-limit (max-diff-lines) (not confirmed at publication)\n")
		assert.Contains(t, text, "    first run: not-applicable (not confirmed at publication)\n")
	})

	t.Run("a raced unknown without a limit keeps its reason", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/logo.bin", "\x00\x01")
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verify()

		assert.Equal(t, map[string]any{"reason": "unsupported-content"}, resultJSON(t, v, 2)["first_run"])
		assert.Contains(t, renderText(v), "    first run: unknown/unsupported-content (not confirmed at publication)\n")
	})

	t.Run("a fresh unknown has no first run", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, FreshnessFresh, v.Freshness)
		assert.Equal(t, []string{"1:not-applicable", "2:unknown/resource-limit(max-diff-lines)"}, outcomes(v))
		assert.NotContains(t, resultJSON(t, v, 2), "first_run")
		assert.NotContains(t, renderText(v), "first run:")
	})

	// A read that stopped before a snapshot existed has no first run: the
	// unknown results of the stand-in snapshot evaluated nothing.
	t.Run("a cancellation during the read keeps no first run", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")
		gitShim(t, `case " $* " in *" status "*) exec sleep 10;; esac`)

		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Second, cancel)

		defer timer.Stop()

		v := g.verifyCtx(ctx, lim)

		assert.Equal(t, []string{"1:unknown/interrupted", "2:unknown/interrupted"}, outcomes(v))
		assert.NotContains(t, resultJSON(t, v, 1), "first_run")
		assert.NotContains(t, resultJSON(t, v, 2), "first_run")
		assert.NotContains(t, renderText(v), "first run:")
	})
}

// TestLessonCheckVerifyListsCheckerLimits proves fix F-D of the P1
// review: a limit that a checker reaches is in Verification.Limits, as
// K3 Bounds says. The snapshot and the checkers share one list, and the
// list counts each undecided path once.
func TestLessonCheckVerifyListsCheckerLimits(t *testing.T) {
	// twoPatternsYAML holds two pattern pins on the same scope, so both
	// reach max-diff-lines on the same path.
	const twoPatternsYAML = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
  no-fixme:
    kind: no-added-pattern/1
    pattern: 'FIXME'
pin:
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: no-fixme
    region: src
    note: Do not add FIXME markers.
    mode: check
    check: no-fixme
`

	lim := DefaultLimits()
	lim.MaxDiffLines = 2

	// threeLines is above the two lines of lim, so a diff against it
	// reaches max-diff-lines.
	const threeLines = "package a\n\nfunc A() {}\n"

	t.Run("two pins that reach max-diff-lines on one path list it once", func(t *testing.T) {
		g := verifyRepo(t, twoPatternsYAML)
		g.write("src/a.go", threeLines)

		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, []string{"1:unknown/resource-limit(max-diff-lines)", "2:unknown/resource-limit(max-diff-lines)"}, outcomes(v))
		assert.Equal(t, []LimitHit{{Name: LimitMaxDiffLines, Value: 2, Paths: 1}}, v.Limits)
		assert.True(t, v.PathsComplete, "a checker limit cuts one comparison, not the path set")
		assert.Contains(t, renderText(v), "  limit reached: max-diff-lines 2 (1 path left undecided)\n")
	})

	t.Run("a hit beside a violation is listed", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", threeLines)
		g.write("src/b.go", "// TODO(x): later\n")

		v := g.verifyCtx(context.Background(), lim)

		r := resultOf(t, v, 2)
		require.Equal(t, OutcomeViolation, r.Outcome)
		assert.Empty(t, r.Limit, "a violation names no limit")
		assert.Equal(t, []LimitHit{{Name: LimitMaxDiffLines, Value: 2, Paths: 1}}, v.Limits)
	})

	t.Run("the stale rewrite keeps the hit in the list", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", threeLines)
		onRevalidate(t, func() { g.write("src/new.go", "package a\n") })

		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, FreshnessStale, v.Freshness)
		assert.Equal(t, []LimitHit{{Name: LimitMaxDiffLines, Value: 2, Paths: 1}}, v.Limits)
	})

	t.Run("a limit that the snapshot lists is not repeated", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() { /* longer than the limit */ }\n")

		small := DefaultLimits()
		small.MaxFileBytes = 16

		v := g.verifyCtx(context.Background(), small)

		assert.Equal(t, "2:unknown/resource-limit(max-file-bytes)", outcomes(v)[1])
		assert.Equal(t, []LimitHit{{Name: LimitMaxFileBytes, Value: 16, Paths: 1}}, v.Limits,
			"the producer already counted the path")
	})

	// Verify appends a checker hit after the snapshot hits. Only a run
	// with both kinds shows whether the merged list is sorted by name.
	t.Run("a checker hit and a snapshot hit are sorted by name", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		// Both sides of src/a.go are at most 16 bytes, so only the checker
		// reaches a limit there: three lines against two.
		g.write("src/a.go", "a\nb\nc\n")
		// src/b.go is above 16 bytes, so the snapshot reaches max-file-bytes.
		g.write("src/b.go", "package a\n\nfunc B() {}\n")

		small := DefaultLimits()
		small.MaxFileBytes = 16
		small.MaxDiffLines = 2

		v := g.verifyCtx(context.Background(), small)

		assert.Equal(t, []LimitHit{
			{Name: LimitMaxDiffLines, Value: 2, Paths: 1},
			{Name: LimitMaxFileBytes, Value: 16, Paths: 1},
		}, v.Limits)
	})
}

// TestLessonCheckVerifyTier1ConfigError proves ruling P1-b for a file
// that does not read or decode: no result rows, one decode-error entry,
// version 0, state incomplete, exit 1 with the decode error. The
// snapshot is still read (K5 Addition).
func TestLessonCheckVerifyTier1ConfigError(t *testing.T) {
	cases := []struct {
		name, text string
		// message is the expected decode-error message; "" means the text
		// of reviews.ParseConfig without the prefix.
		message string
	}{
		{name: "unparsable YAML", text: "pin: [\n"},
		{name: "a type error", text: "pin: 3\n"},
		{name: "a negative version", text: "lesson_checks_version: -1\n", message: "lesson_checks_version must be 1, got -1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := verifyRepo(t, tc.text)
			g.write("generated/a.txt", "edited\n")

			_, parseErr := reviews.ParseConfig([]byte(tc.text))
			require.Error(t, parseErr)

			want := tc.message
			if want == "" {
				want = strings.TrimPrefix(parseErr.Error(), lessonsConfigHead)
			}

			v := g.verify()

			assert.Equal(t, ConfigReport{
				SHA256: sha256Hex(tc.text),
				Valid:  false,
				Errors: []ConfigError{{Code: CodeDecodeError, Subject: "lessons.yaml", Message: want}},
			}, v.Config)
			assert.Equal(t, sha256Hex(tc.text), v.Binding.ConfigSHA256)
			assert.Empty(t, v.Results)
			assert.Empty(t, v.Listings)
			assert.Empty(t, v.Binding.CheckerVersions)
			assert.Equal(t, pinsDigestOf(), v.Binding.PinsSHA256)
			assert.Equal(t, SummaryIncomplete, v.Summary.State)
			assert.Equal(t, FreshnessFresh, v.Freshness, "the snapshot is read and revalidated")
			assert.Equal(t, 1, v.Summary.Changes)
			assert.Equal(t, verifyCommand, v.Rerun)

			// The JSON message keeps the text of the decoder. The stderr
			// line and the text report drop control characters, so a
			// multi-line yaml error stays on one line.
			oneLine := render.Sanitize(lessonsConfigHead + want)

			err := v.ExitError()
			require.ErrorIs(t, err, ErrIncomplete)
			assert.EqualError(t, err, "lesson checks incomplete: "+oneLine+"; rerun seamark lessons --verify")
			assert.NotContains(t, err.Error(), "\n")

			text := renderText(v)
			assert.Contains(t, text, "config: invalid; no check ran\n")
			assert.Contains(t, text, "  - "+oneLine+"\n")
		})
	}

	t.Run("an unreadable file", func(t *testing.T) {
		g := verifyRepo(t, "")
		require.NoError(t, os.MkdirAll(filepath.Join(g.root, ".seamark", "lessons.yaml"), 0o755))

		v := g.verify()

		require.Len(t, v.Config.Errors, 1)
		assert.Equal(t, CodeDecodeError, v.Config.Errors[0].Code)
		assert.Contains(t, v.Config.Errors[0].Message, "is a directory")
		assert.NotContains(t, v.Config.Errors[0].Message, lessonsConfigHead)
		assert.Empty(t, v.Config.SHA256, "no bytes were read")
		assert.False(t, v.Config.Valid)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		require.ErrorIs(t, v.ExitError(), ErrIncomplete)
	})
}

// TestLessonCheckVerifyTier2ConfigError proves ruling P1-b for a file
// that decodes and fails Validate: the snapshot is read, no checker
// runs, each executable pin gets one unknown/configuration-error result,
// every other pin is listed, and config.errors holds every error.
func TestLessonCheckVerifyTier2ConfigError(t *testing.T) {
	t.Run("the invalid example of contract K1", func(t *testing.T) {
		g := verifyRepo(t, invalidYAML)
		// The added line would match a valid TODO pattern. No checker
		// runs, so it is no violation.
		g.write("src/a.go", "package a\n\n// TODO(x)\n")

		v := g.verify()

		cfg, errs, err := Parse([]byte(invalidYAML))
		require.NoError(t, err)
		require.Len(t, errs, 2)

		assert.Equal(t, ConfigReport{SHA256: sha256Hex(invalidYAML), LessonChecksVersion: 1, Valid: false, Errors: errs}, v.Config)
		assert.Equal(t, []Result{{
			PinRef:      cfg.Pins[1].Ref,
			PinIdentity: cfg.Pins[1].Identity,
			PinVersion:  cfg.Pins[1].Version,
			Entry:       2,
			Rule:        "no-todo",
			Mode:        ModeCheck,
			Scope:       []string{"src"},
			Check:       CheckRef{ID: "no-todos"},
			Outcome:     OutcomeUnknown,
			Reason:      ReasonConfigurationError,
		}}, v.Results)
		assert.Equal(t, []PinListing{
			{PinRef: cfg.Pins[0].Ref, PinVersion: cfg.Pins[0].Version, Entry: 1, Rule: "frozen-assets", Listing: ListingNotCheckable},
		}, v.Listings)
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, 1, v.Summary.Unknown)
		assert.Equal(t, 1, v.Summary.PinsEvaluated, "a configuration-error result counts as evaluated")
		assert.Empty(t, v.Binding.CheckerVersions, "no checker ran")
		assert.Equal(t, pinsDigestOf("2:"+cfg.Pins[1].Version), v.Binding.PinsSHA256)
		assert.Equal(t, 1, v.Summary.Changes, "the snapshot is read")
		assert.NotEmpty(t, v.Changes)
		assert.Equal(t, FreshnessFresh, v.Freshness)

		err = v.ExitError()
		require.ErrorIs(t, err, ErrIncomplete)
		assert.EqualError(t, err, "lesson checks incomplete: "+errs[0].Error()+"; rerun seamark lessons --verify")

		text := renderText(v)
		assert.Contains(t, text, "config: invalid; no check ran\n")
		assert.Contains(t, text, "  - "+errs[0].Error()+"\n")
		assert.Contains(t, text, "  - "+errs[1].Error()+"\n")
		assert.Contains(t, text, "  unknown/configuration-error  pin "+cfg.Pins[1].Ref+" no-todo (entry 2, mode check)\n")
		assert.Contains(t, text, "    check: no-todos\n")
	})

	t.Run("a newer version and an unknown mode", func(t *testing.T) {
		const newer = `lesson_checks_version: 2
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen
    region: generated
    note: Do not edit generated files.
    mode: enforce
    check: frozen
  - rule: strict
    region: generated
    note: An unknown mode.
    mode: strict
    check: frozen
  - rule: prose
    region: src
    note: Advisory.
`
		g := verifyRepo(t, newer)
		g.write("generated/a.txt", "edited\n")

		v := g.verify()

		cfg, errs, err := Parse([]byte(newer))
		require.NoError(t, err)
		require.Equal(t, []ConfigErrorCode{CodeVersionUnsupported}, codesOf(errs))

		frozen := CheckRef{ID: "frozen", Kind: KindForbidPathChange, Version: forbidVersion}

		assert.Equal(t, 2, v.Config.LessonChecksVersion)
		assert.Equal(t, errs, v.Config.Errors)
		assert.Equal(t, []string{"1:unknown/configuration-error"}, outcomes(v),
			"a forbidden change is no violation while the config is invalid")
		assert.Equal(t, frozen, v.Results[0].Check)
		assert.Empty(t, v.Binding.CheckerVersions, "no checker ran, although the result names a known kind")
		assert.Equal(t, []PinListing{
			{PinRef: cfg.Pins[1].Ref, PinVersion: cfg.Pins[1].Version, Entry: 2, Rule: "strict", Listing: ListingAdvisoryWithCheck, Check: &frozen},
			{PinRef: cfg.Pins[2].Ref, PinVersion: cfg.Pins[2].Version, Entry: 3, Rule: "prose", Listing: ListingNotCheckable},
		}, v.Listings, "a pin with an unknown mode lists as advisory")
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.EqualError(t, v.ExitError(),
			"lesson checks incomplete: lessons config: lesson_checks_version 2 is not supported; this seamark reads up to 1; rerun seamark lessons --verify")
	})
}

// codesOf returns the codes of the errors, in order.
func codesOf(errs []ConfigError) []ConfigErrorCode {
	out := make([]ConfigErrorCode, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}

	return out
}

// goldenYAML is the configuration of the JSON golden: the four pin
// states of the K5 example.
const goldenYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
  no-print:
    kind: no-added-pattern/1
    pattern: 'fmt\.Print'
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: prefer-errors-is
    region: internal
    note: Compare errors with errors.Is.
  - rule: no-print
    region: src
    note: Do not print from library code.
    check: no-print
`

// goldenJSON is the lesson-verification/1 document of the golden
// repository. Equal inputs give byte-equal JSON, except checked_at and
// seamark_version (contracts §1.4), which the test fixes.
const goldenJSON = `{
  "schema": "lesson-verification/1",
  "seamark_version": "v0.8.0-31-g96e14ef",
  "checked_at": "2026-10-21T10:15:02Z",
  "caller": "cli",
  "boundary": "working-tree",
  "baseline": {
    "kind": "head",
    "commit": "932c7382a98fb24a006704789ab481168d8d450b"
  },
  "selection": {
    "kind": "default",
    "description": "tracked changes against HEAD plus untracked nonignored files, staged and unstaged, as present in the working tree",
    "excludes": [
      "ignored files not named by --file",
      "writes outside this snapshot",
      "content inside submodules"
    ]
  },
  "binding": {
    "snapshot_sha256": "10cf0d11c31c212dc64d6a0f8f4b6be5e51348631e52ed306000c22189f6a7b7",
    "config_sha256": "c42f27d83834d740e1e46ba8ce4a3d9b79bbbcdee22c37eef88f0e9f4c751e3e",
    "pins_sha256": "9587236368efa48ee9b1d6527e4c0ac6c83e62db4cc4dd671ddeec782e40a44b",
    "checker_versions": [
      "forbid-path-change/1",
      "no-added-pattern/1"
    ]
  },
  "freshness": "fresh",
  "rerun": "seamark lessons --verify",
  "paths_complete": true,
  "config": {
    "sha256": "c42f27d83834d740e1e46ba8ce4a3d9b79bbbcdee22c37eef88f0e9f4c751e3e",
    "lesson_checks_version": 1,
    "valid": true
  },
  "summary": {
    "state": "violation",
    "pins_evaluated": 2,
    "pass": 0,
    "violation": 1,
    "not_applicable": 0,
    "unknown": 1,
    "not_checkable": 1,
    "advisory_with_check": 1,
    "changes": 3
  },
  "results": [
    {
      "pin_ref": "3215e7b483d1",
      "pin_identity": "3215e7b483d134443a8204970767f1a0057d4726f235e946b6c0c27abd8cd055",
      "pin_version": "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e",
      "entry": 1,
      "rule": "frozen-assets",
      "mode": "enforce",
      "scope": [
        "generated"
      ],
      "check": {
        "id": "frozen-assets",
        "kind": "forbid-path-change/1",
        "version": "9ceb9cddb5053035c0d9779ffab326503647b5295cb3f43791d5fe1e9be9cf7a"
      },
      "outcome": "violation",
      "evidence": [
        {
          "path": "generated/a.txt",
          "change": "modify"
        }
      ],
      "coverage": {
        "paths_in_scope": 1,
        "paths_evaluated": 1
      }
    },
    {
      "pin_ref": "ebb54aefeb52",
      "pin_identity": "ebb54aefeb52a35b666cc7f42172ad1ed755bc192b81d8771a8b6126d16b5746",
      "pin_version": "866ec2cc378044bb53adca5e0a00edd57752552678fba7b3740284576705c6a2",
      "entry": 2,
      "rule": "no-todo",
      "mode": "check",
      "scope": [
        "src"
      ],
      "check": {
        "id": "no-todo",
        "kind": "no-added-pattern/1",
        "version": "c044c91d812033b1da35c51ad301b6a3a06b09503b8e1126b392c1d643d7aa3b"
      },
      "outcome": "unknown",
      "reason": "unsupported-content",
      "unknown_paths": [
        {
          "path": "src/logo.bin",
          "change": "add",
          "reason": "unsupported-content"
        }
      ],
      "coverage": {
        "paths_in_scope": 2,
        "paths_evaluated": 1,
        "added_lines": 2
      }
    }
  ],
  "listings": [
    {
      "pin_ref": "eb68bad58df7",
      "pin_version": "384a5972a048d0e532d8e3cec0912b54280cf2c9a7a019052138e9791b9b7955",
      "entry": 3,
      "rule": "prefer-errors-is",
      "listing": "not-checkable"
    },
    {
      "pin_ref": "f28d63876a46",
      "pin_version": "c5fad2eaef0294fe17048b2cb56d2f89720d4c55f1aa111c8d12b85a7c05c85e",
      "entry": 4,
      "rule": "no-print",
      "listing": "advisory-with-check",
      "check": {
        "id": "no-print",
        "kind": "no-added-pattern/1",
        "version": "04d9504fca74636510e28eea6236258ba1610a2870eddd52a4cc727e8d236c36"
      }
    }
  ],
  "changes": [
    {
      "path": "generated/a.txt",
      "kind": "modify",
      "before": {
        "type": "file",
        "mode": "100644",
        "oid": "86d4c2dd380eb2e9e2a050e882f40bc65dc2a48b",
        "sha256": "9f5936ff15d3a2ba7d3d8f21858338a6c1e2adc9fe34c685c7de5b4a00caa29a",
        "size": 10,
        "content": "available"
      },
      "after": {
        "type": "file",
        "mode": "100644",
        "sha256": "df97460881f270d6a559ab7f9594e3403ac50ca15098fe58ff7a489ec2aa81f6",
        "size": 15,
        "content": "available"
      },
      "content_changed": true,
      "mode_changed": false,
      "type_changed": false
    },
    {
      "path": "src/handler.go",
      "kind": "modify",
      "before": {
        "type": "file",
        "mode": "100644",
        "oid": "c6d5f5462d9d70f2b02d1bbdd672da30c0209fb1",
        "sha256": "ec016cc15d1b6ac2ddd18a8986d7cec46fced460244c9daa761ddfd5b9a10907",
        "size": 12,
        "content": "available"
      },
      "after": {
        "type": "file",
        "mode": "100644",
        "sha256": "f9a7d1e860dd7cdb2942f65452d7c40d844c61e50eae7e95de949c75c30d0ed4",
        "size": 30,
        "content": "available"
      },
      "content_changed": true,
      "mode_changed": false,
      "type_changed": false
    },
    {
      "path": "src/logo.bin",
      "kind": "add",
      "before": {
        "type": "absent",
        "size": 0,
        "content": "not-read"
      },
      "after": {
        "type": "file",
        "mode": "100644",
        "sha256": "09824c6bec844d272dd1a01cd876409dfaf7b650d0ec2fc2e88a1e6a8596f762",
        "size": 6,
        "content": "binary"
      },
      "content_changed": true,
      "mode_changed": false,
      "type_changed": false
    }
  ]
}`

// emptyGoldenJSON is the document of a clean repository without
// lessons.yaml. Every slice without omitempty encodes as [] (K-11).
const emptyGoldenJSON = `{"schema":"lesson-verification/1","seamark_version":"v0.8.0-31-g96e14ef","checked_at":"2026-10-21T10:15:02Z","caller":"cli","boundary":"working-tree","baseline":{"kind":"head","commit":"9c0c8314d5587b1e6070566f0a08f0c95aae6014"},"selection":{"kind":"default","description":"tracked changes against HEAD plus untracked nonignored files, staged and unstaged, as present in the working tree","excludes":["ignored files not named by --file","writes outside this snapshot","content inside submodules"]},"binding":{"snapshot_sha256":"94075b6588039c67a3635a31b21ba86f1b33e8564cd8a49b12bff3da5ddbe70a","config_sha256":"","pins_sha256":"2989b6177168ee9da4f58c8fd83e4061a80619ef7b1685e907cb4cc7297dd4af","checker_versions":[]},"freshness":"fresh","paths_complete":true,"config":{"sha256":"","lesson_checks_version":0,"valid":true},"summary":{"state":"no-executable-checks","pins_evaluated":0,"pass":0,"violation":0,"not_applicable":0,"unknown":0,"not_checkable":0,"advisory_with_check":0,"changes":0},"results":[],"listings":[]}`

// TestLessonCheckVerifyJSONGolden freezes the JSON bytes of
// lesson-verification/1 (AC-35): the field order, the enums, the
// binding digests, and [] for every empty slice without omitempty.
func TestLessonCheckVerifyJSONGolden(t *testing.T) {
	// The fixed dates make the commit id, and so the baseline and the
	// snapshot digest, equal on every run.
	t.Setenv("GIT_AUTHOR_DATE", "2026-10-21T10:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-21T10:00:00Z")

	t.Run("the four pin states", func(t *testing.T) {
		g := newGitRepo(t)
		g.write(".git/info/exclude", ".seamark/\n")
		g.write("generated/a.txt", "generated\n")
		g.write("src/handler.go", "package src\n")
		g.commitAll("base")

		writeLessons(t, g.root, goldenYAML)
		g.write("generated/a.txt", "edited by hand\n")
		g.write("src/handler.go", "package src\n\nfunc Handle() {}\n")
		g.write("src/logo.bin", "\x89PNG\x00\x01")

		v := g.verify()

		data, err := json.MarshalIndent(v, "", "  ")
		require.NoError(t, err)
		assert.Equal(t, goldenJSON, string(data))

		// A second run gives the same bytes except the two fields that
		// contracts §1.4 names.
		req := verifyRequest(g.root, DefaultLimits())
		req.Now = time.Now
		req.SeamarkVersion = "dev"

		again, err := Verify(context.Background(), req)
		require.NoError(t, err)
		assert.NotEqual(t, v.CheckedAt, again.CheckedAt)

		again.CheckedAt, again.SeamarkVersion = v.CheckedAt, v.SeamarkVersion

		data2, err := json.MarshalIndent(again, "", "  ")
		require.NoError(t, err)
		assert.Equal(t, string(data), string(data2))
	})

	t.Run("an empty result encodes every list as []", func(t *testing.T) {
		g := newGitRepo(t)
		g.write("a.txt", "a\n")
		g.commitAll("base")

		v := g.verify()

		data, err := json.Marshal(v)
		require.NoError(t, err)
		assert.Equal(t, emptyGoldenJSON, string(data))
		assert.Contains(t, string(data), `"results":[]`)
		assert.Contains(t, string(data), `"listings":[]`)
		assert.Contains(t, string(data), `"checker_versions":[]`)
		assert.NotContains(t, string(data), "null")
	})
}

// TestLessonCheckVerifyTimeout proves that Verify owns the
// verify-timeout deadline and that a deadline or a cancellation is a
// result, not an error (AC-30): unknown/resource-limit with
// verify-timeout, or unknown/interrupted, never a pass, and exit 1.
func TestLessonCheckVerifyTimeout(t *testing.T) {
	// requireCut checks a result whose every pin carries the cause.
	requireCut := func(t *testing.T, v *Verification, want string) {
		t.Helper()

		assert.Equal(t, FreshnessStale, v.Freshness, "a result that was not read again is never fresh")
		assert.Equal(t, SummaryIncomplete, v.Summary.State)
		assert.Equal(t, []string{"1:" + want, "2:" + want}, outcomes(v))
		require.ErrorIs(t, v.ExitError(), ErrIncomplete)
		assert.Equal(t, 1, Combine(v.Summary.State, false).ExitCode)
	}

	t.Run("a deadline during the read gives resource-limit", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")
		gitShim(t, `case " $* " in *" status "*) exec sleep 10;; esac`)

		lim := DefaultLimits()
		lim.VerifyTimeout = time.Second

		start := time.Now()
		v := g.verifyCtx(context.Background(), lim)

		assert.Less(t, time.Since(start), 8*time.Second, "the deadline stops the read")
		requireCut(t, v, "unknown/resource-limit(verify-timeout)")
		assert.False(t, v.PathsComplete)
		assert.Empty(t, v.Changes)
		assert.Equal(t, []LimitHit{{Name: LimitVerifyTimeout, Value: 1000}}, v.Limits)
		assert.Contains(t, renderText(v), "limit reached: verify-timeout 1000 (0 paths left undecided)\n")
	})

	t.Run("a deadline during step 1 gives a result with the baseline", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		mark := filepath.Join(t.TempDir(), "slept")
		gitShim(t, `case " $* " in *" --verify "*) if [ ! -e `+shellQuote(mark)+` ]; then : > `+shellQuote(mark)+`; exec sleep 10; fi;; esac`)

		lim := DefaultLimits()
		lim.VerifyTimeout = time.Second

		v := g.verifyCtx(context.Background(), lim)

		requireCut(t, v, "unknown/resource-limit(verify-timeout)")
		assert.Equal(t, Baseline{Kind: BaselineHead, Commit: g.head()}, v.Baseline)
		assert.Equal(t, SelectionDefault, v.Selection.Kind)
		assert.Equal(t, []LimitHit{{Name: LimitVerifyTimeout, Value: 1000}}, v.Limits)
	})

	t.Run("no baseline at all is an error", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		gitShim(t, `case " $* " in *" --verify "*) exec sleep 10;; esac`)

		lim := DefaultLimits()
		lim.VerifyTimeout = time.Second

		v, err := Verify(context.Background(), verifyRequest(g.root, lim))
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Nil(t, v)
	})

	t.Run("a cancellation during the read gives interrupted", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")
		gitShim(t, `case " $* " in *" status "*) exec sleep 10;; esac`)

		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Second, cancel)

		defer timer.Stop()

		v := g.verifyCtx(ctx, DefaultLimits())

		requireCut(t, v, "unknown/interrupted")
		assert.Equal(t, Baseline{Kind: BaselineHead, Commit: g.head()}, v.Baseline)
		assert.Empty(t, v.Limits)
		assert.Empty(t, v.Changes)
	})

	t.Run("a cancellation before the revalidation gives interrupted", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		onRevalidate(t, cancel)

		v := g.verifyCtx(ctx, DefaultLimits())

		requireCut(t, v, "unknown/interrupted")
		assert.Len(t, v.Changes, 1, "the snapshot was read")

		text := renderText(v)
		assert.Contains(t, text, "    first run: pass (not confirmed at publication)\n")
		assert.Contains(t, text, "    first run: not-applicable (not confirmed at publication)\n")
	})

	t.Run("a deadline before the revalidation gives resource-limit", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		lim := DefaultLimits()
		lim.VerifyTimeout = 2 * time.Second

		start := time.Now()
		onRevalidate(t, func() { time.Sleep(time.Until(start.Add(lim.VerifyTimeout + 200*time.Millisecond))) })

		v := g.verifyCtx(context.Background(), lim)

		requireCut(t, v, "unknown/resource-limit(verify-timeout)")
		assert.Equal(t, []LimitHit{{Name: LimitVerifyTimeout, Value: 2000}}, v.Limits)
	})

	t.Run("a violation found before the deadline stays", func(t *testing.T) {
		g := verifyRepo(t, mixedYAML)
		g.write("generated/a.txt", "edited\n")

		lim := DefaultLimits()
		lim.VerifyTimeout = 2 * time.Second

		start := time.Now()
		onRevalidate(t, func() { time.Sleep(time.Until(start.Add(lim.VerifyTimeout + 200*time.Millisecond))) })

		v := g.verifyCtx(context.Background(), lim)

		assert.Equal(t, SummaryViolation, v.Summary.State)
		assert.Equal(t, []string{"1:violation", "2:unknown/resource-limit(verify-timeout)"}, outcomes(v))
		require.ErrorIs(t, v.ExitError(), ErrViolation)
	})
}

// TestLessonCheckCombine proves the combined verdict of check --lessons
// (ruling P1-c, K-10): the exit code by the K5 table, the state by the
// exit code, and the policy word by the blocking flag. An unknown
// summary state never passes.
func TestLessonCheckCombine(t *testing.T) {
	cases := []struct {
		lessons  SummaryState
		blocking bool
		want     Combined
	}{
		{SummaryPassed, false, Combined{State: CombinedPass, ExitCode: 0, Policy: "pass"}},
		{SummaryNoApplicableChecks, false, Combined{State: CombinedPass, ExitCode: 0, Policy: "pass"}},
		{SummaryNoExecutableChecks, false, Combined{State: CombinedPass, ExitCode: 0, Policy: "pass"}},
		{SummaryIncomplete, false, Combined{State: CombinedIncomplete, ExitCode: 1, Policy: "pass"}},
		{SummaryViolation, false, Combined{State: CombinedFail, ExitCode: 2, Policy: "pass"}},
		{SummaryPassed, true, Combined{State: CombinedFail, ExitCode: 2, Policy: "blocked"}},
		{SummaryNoExecutableChecks, true, Combined{State: CombinedFail, ExitCode: 2, Policy: "blocked"}},
		{SummaryIncomplete, true, Combined{State: CombinedFail, ExitCode: 2, Policy: "blocked"}},
		{SummaryViolation, true, Combined{State: CombinedFail, ExitCode: 2, Policy: "blocked"}},
		{"", false, Combined{State: CombinedIncomplete, ExitCode: 1, Policy: "pass"}},
		{"bogus", false, Combined{State: CombinedIncomplete, ExitCode: 1, Policy: "pass"}},
		{"bogus", true, Combined{State: CombinedFail, ExitCode: 2, Policy: "blocked"}},
	}

	byExit := map[int]CombinedState{0: CombinedPass, 1: CombinedIncomplete, 2: CombinedFail}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/blocking=%t", tc.lessons, tc.blocking), func(t *testing.T) {
			got := Combine(tc.lessons, tc.blocking)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, byExit[got.ExitCode], got.State, "the state follows the exit code")
			assert.Equal(t, got, Combine(tc.lessons, tc.blocking), "Combine is pure")
		})
	}

	// lessons --verify and check --lessons without a blocking policy
	// exit with the same code for every summary state.
	t.Run("the exit error agrees with Combine", func(t *testing.T) {
		for _, state := range []SummaryState{
			SummaryPassed, SummaryNoApplicableChecks, SummaryNoExecutableChecks, SummaryIncomplete, SummaryViolation, "bogus",
		} {
			v := &Verification{Summary: Summary{State: state}, Config: ConfigReport{Valid: true}}
			err := v.ExitError()

			switch Combine(state, false).ExitCode {
			case 0:
				assert.NoError(t, err, state)
			case 1:
				assert.True(t, errors.Is(err, ErrIncomplete) && !errors.Is(err, ErrViolation), state)
			case 2:
				assert.True(t, errors.Is(err, ErrViolation) && !errors.Is(err, ErrIncomplete), state)
			}
		}
	})
}

// TestLessonCheckRevalidate proves the exported Revalidate (contract K5,
// Freshness; K-13; AC-30). A result stays fresh while its inputs stay.
// A change of content, baseline, scope, mode, or check definition makes
// it stale. Revalidate runs no checker and writes nothing, so a refresh
// of the console never adds an audit record.
func TestLessonCheckRevalidate(t *testing.T) {
	// fresh returns a repository and its fresh verification. The check
	// pin passes on a clean line in src, and nothing is in generated.
	fresh := func(t *testing.T, paths ...string) (*gitRepo, *Verification) {
		t.Helper()

		g := verifyRepo(t, mixedYAML)
		g.write("src/a.go", "package a\n\nfunc A() {}\n")

		v := g.verify(paths...)
		require.Equal(t, FreshnessFresh, v.Freshness)
		require.Equal(t, SummaryPassed, v.Summary.State)

		return g, v
	}

	revalidate := func(t *testing.T, g *gitRepo, v *Verification) Freshness {
		t.Helper()

		got, err := Revalidate(context.Background(), g.root, v)
		require.NoError(t, err)

		return got
	}

	t.Run("unchanged inputs stay fresh and nothing is written", func(t *testing.T) {
		g, v := fresh(t)
		state := listTree(t, filepath.Join(g.root, ".seamark"))

		assert.Equal(t, FreshnessFresh, revalidate(t, g, v))
		assert.Equal(t, FreshnessFresh, revalidate(t, g, v), "a second refresh reads the same inputs")
		assert.Equal(t, state, listTree(t, filepath.Join(g.root, ".seamark")),
			"no audit record, no sweep, no other file")
	})

	stale := []struct {
		name   string
		change func(g *gitRepo)
	}{
		{"a content edit in the selection", func(g *gitRepo) { g.write("src/a.go", "package a\n\nfunc B() {}\n") }},
		{"a new untracked file", func(g *gitRepo) { g.write("src/new.go", "package a\n") }},
		{"a new commit moves the baseline", func(g *gitRepo) {
			g.write("docs/readme.md", "new\n")
			g.git("commit", "-q", "-m", "docs", "--", "docs/readme.md")
		}},
		{"a mode edit", func(g *gitRepo) {
			writeLessons(g.t, g.root, strings.Replace(mixedYAML, "    mode: check\n", "    mode: enforce\n", 1))
		}},
		{"a check definition edit", func(g *gitRepo) {
			writeLessons(g.t, g.root, strings.Replace(mixedYAML, `'TODO\('`, `'FIXME'`, 1))
		}},
		{"a scope edit", func(g *gitRepo) {
			writeLessons(g.t, g.root, strings.Replace(mixedYAML,
				"    region: src\n    note: Do not add TODO markers.", "    region: docs\n    note: Do not add TODO markers.", 1))
		}},
		{"a removed lessons.yaml", func(g *gitRepo) { g.remove(".seamark/lessons.yaml") }},
	}

	for _, tc := range stale {
		t.Run(tc.name+" makes the result stale", func(t *testing.T) {
			g, v := fresh(t)
			tc.change(g)

			assert.Equal(t, FreshnessStale, revalidate(t, g, v))
		})
	}

	t.Run("an explicit selection ignores a change outside it", func(t *testing.T) {
		g, v := fresh(t, "src")
		require.Equal(t, []string{"src"}, v.Selection.Paths)

		g.write("docs/readme.md", "changed\n")
		assert.Equal(t, FreshnessFresh, revalidate(t, g, v), "docs is outside the selection")

		g.write("src/a.go", "package a\n\nfunc B() {}\n")
		assert.Equal(t, FreshnessStale, revalidate(t, g, v))
	})

	// The stand-in results of configBinding must give the digests that
	// Verify computes from the results of Evaluate, for a valid, an
	// invalid, an undecodable, and an absent configuration.
	t.Run("the binding equals the binding of Verify for every configuration state", func(t *testing.T) {
		for name, lessons := range map[string]string{
			"valid":   mixedYAML,
			"tier 2":  strings.Replace(mixedYAML, `'TODO\('`, `'TODO('`, 1),
			"tier 1":  "pin: [\n",
			"absent":  "",
			"no pins": "lesson_checks_version: 1\n",
		} {
			g := verifyRepo(t, lessons)
			v := g.verify()

			pins, kinds := configBinding(loadConfig(g.root).cfg)
			assert.Equal(t, v.Binding.PinsSHA256, pins, name)
			assert.Equal(t, v.Binding.CheckerVersions, kinds, name)
			assert.Equal(t, FreshnessFresh, revalidate(t, g, v), name)
		}
	})

	// An absent and an unreadable lessons.yaml both have the digest "".
	// A read error is never an absent file, so the result goes stale.
	t.Run("an unreadable lessons.yaml is not an absent one", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file without read permission")
		}

		g := verifyRepo(t, "")
		v := g.verify()
		require.True(t, v.Config.Valid)

		writeLessons(t, g.root, mixedYAML)
		require.NoError(t, os.Chmod(configPath(g.root), 0))
		t.Cleanup(func() { _ = os.Chmod(configPath(g.root), 0o600) })

		assert.Equal(t, FreshnessStale, revalidate(t, g, v))
	})

	t.Run("a stale result stays stale without a read", func(t *testing.T) {
		_, v := fresh(t)
		v.Freshness = FreshnessStale

		// The root is no repository, so a read would fail.
		got, err := Revalidate(context.Background(), t.TempDir(), v)
		require.NoError(t, err)
		assert.Equal(t, FreshnessStale, got)
	})

	t.Run("a canceled context gives stale without an error", func(t *testing.T) {
		g, v := fresh(t)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := Revalidate(ctx, g.root, v)
		require.NoError(t, err)
		assert.Equal(t, FreshnessStale, got)
	})

	t.Run("a usage or root failure gives stale with an error", func(t *testing.T) {
		g, v := fresh(t)

		got, err := Revalidate(context.Background(), g.root, nil)
		require.EqualError(t, err, "lesson checks: revalidate needs a result")
		assert.Equal(t, FreshnessStale, got)

		got, err = Revalidate(context.Background(), "repo", v)
		require.EqualError(t, err, `lesson checks: root "repo" is not an absolute path`)
		assert.Equal(t, FreshnessStale, got)

		got, err = Revalidate(context.Background(), t.TempDir(), v)
		require.ErrorContains(t, err, "is not inside a git work tree")
		assert.Equal(t, FreshnessStale, got)
	})
}
