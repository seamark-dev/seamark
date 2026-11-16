package lessoncheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of ObserveAfter and of EvaluatePostEdit.
// Every repository lives under t.TempDir(), so no test reads or writes
// the checkout that runs it.

// recordCall projects op in g and stores its pending record, as a
// pre-edit hook does. It returns the record as ReadPending reads it, so
// the record carries the digest of its file bytes. lim bounds the
// write only.
func (g *gitRepo) recordCall(cfg *Config, op Operation, lim Limits) *Pending {
	g.t.Helper()

	p := pendingFor(cfg, g.project(op), rawSession, rawMatch)
	p.Client, p.Tool = op.Client, op.Tool

	require.NoError(g.t, WritePending(context.Background(), g.root, p, lim))

	read, err := ReadPending(g.root, p.Client, p.MatchSHA, DefaultLimits())
	require.NoError(g.t, err)

	return read
}

// observe observes a record with the default limits. The result must be
// a valid actual-edit snapshot.
func (g *gitRepo) observe(p *Pending) *Snapshot {
	g.t.Helper()

	snap, err := ObserveAfter(context.Background(), g.root, p, DefaultLimits())
	require.NoError(g.t, err)
	requireValidObservation(g.t, g.root, snap, p)

	return snap
}

// requireValidObservation checks the invariants of every actual-edit
// snapshot: the shared invariants of an operation snapshot with the
// pre-edit baseline that holds the digest of the record file, and no
// unprojectable change.
func requireValidObservation(t *testing.T, root string, snap *Snapshot, p *Pending) {
	t.Helper()

	data, err := os.ReadFile(pendingFile(root, p))
	require.NoError(t, err)

	sum := sha256.Sum256(data)
	baseline := Baseline{Kind: BaselinePreEdit, PendingSHA: hex.EncodeToString(sum[:])}

	require.Equal(t, baseline, snap.Baseline, "pending_sha256 is the SHA-256 of the record bytes")
	requireValidOperationSnapshot(t, snap, BoundaryActualEdit, baseline, sortedUnique(recordPaths(p)))

	for _, ch := range snap.Changes {
		require.Empty(t, ch.unprojectable, "an observed change is never unprojectable: %s", ch.Path)
	}
}

// limitedPaths returns the paths of the changes that carry the
// limitation.
func limitedPaths(snap *Snapshot, limitation string) []string {
	var out []string

	for _, ch := range snap.Changes {
		if slices.Contains(ch.Limitations, limitation) {
			out = append(out, ch.Path)
		}
	}

	return out
}

func TestLessonCheckObserveAfter(t *testing.T) {
	cfg := parseValid(t, projectTodoYAML)
	ctx := context.Background()

	t.Run("an edit gives the actual delta against the state before it", func(t *testing.T) {
		g := todoRepo(t)

		// The working tree holds a TODO marker before the edit. The
		// working-tree check counts it against HEAD, and the actual-edit
		// check does not, because the edit did not add it.
		g.write("src/a.go", "package a\n\n// TODO(old)\nfunc A() {}\n")

		op := claudeOp("Edit", editOp("src/a.go", "func A() {}\n", "func A() {}\n\nfunc B() {}\n", false))
		p := g.recordCall(cfg, op, DefaultLimits())
		after := projectedText(t, changeAt(t, g.project(op), "src/a.go"))
		g.write("src/a.go", after)

		snap := g.observe(p)

		assert.Equal(t, []string{"modify src/a.go"}, changeList(snap))
		assert.True(t, snap.PathsComplete)
		assert.Equal(t, AttributionProjectionMatch, snap.attribution)
		assert.Empty(t, changeAt(t, snap, "src/a.go").Limitations)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomePass, res.Outcome, "the marker was there before the edit")
		assert.Equal(t, 2, res.Coverage.AddedLines, "the edit adds an empty line and func B")

		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, g.snapshot()), "no-todo").Outcome,
			"the working tree against HEAD holds the marker")
	})

	t.Run("an added marker is a violation at its line", func(t *testing.T) {
		g := todoRepo(t)

		op := claudeOp("Write", writeOp("src/a.go", "package a\n\n// TODO(x)\nfunc A() {}\n"))
		p := g.recordCall(cfg, op, DefaultLimits())
		g.write("src/a.go", "package a\n\n// TODO(x)\nfunc A() {}\n")

		res := resultOfRule(t, Evaluate(cfg, g.observe(p)), "no-todo")
		require.Equal(t, OutcomeViolation, res.Outcome)
		require.Len(t, res.Evidence, 1)
		assert.Equal(t, 3, res.Evidence[0].Line)
	})

	t.Run("a tool that wrote nothing gives no change", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("generated/a.txt", "changed\n")), DefaultLimits())

		snap := g.observe(p)

		assert.Empty(t, snap.Changes, "a path whose state did not change is not a change")
		assert.Equal(t, AttributionProjectionDiffers, snap.attribution, "the projected write did not happen")
		assert.Equal(t, OutcomeNotApplicable, resultOfRule(t, Evaluate(cfg, snap), "frozen-assets").Outcome)
	})

	t.Run("another writer gives projection-differs", func(t *testing.T) {
		g := todoRepo(t)

		op := claudeOp("Edit", editOp("src/a.go", "func A() {}\n", "func A() { _ = 1 }\n", false))
		p := g.recordCall(cfg, op, DefaultLimits())
		g.write("src/a.go", "package a\n\n// TODO(other)\nfunc A() { _ = 1 }\n")

		snap := g.observe(p)

		assert.Equal(t, AttributionProjectionDiffers, snap.attribution)
		assert.Equal(t, []string{"src/a.go"}, limitedPaths(snap, LimitationProjectionDiffers))

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeViolation, res.Outcome, "the check runs on the observed delta")
	})

	t.Run("an add, a delete, and a move", func(t *testing.T) {
		g := todoRepo(t)
		g.write("src/old.go", "package a\n")
		g.commitAll("old")

		op := codexOp(
			addOp("src/b.go", "package b"),
			FileOp{Kind: OpPatchDelete, Path: "generated/a.txt"},
			FileOp{Kind: OpPatchMove, Path: "src/old.go", MoveTo: "src/new.go"},
		)
		p := g.recordCall(cfg, op, DefaultLimits())

		g.write("src/b.go", "package b")
		g.remove("generated/a.txt")
		require.NoError(t, os.Rename(g.path("src/old.go"), g.path("src/new.go")))

		snap := g.observe(p)

		assert.Equal(t, []string{"delete generated/a.txt", "add src/b.go", "move src/old.go->src/new.go"}, changeList(snap))
		assert.Equal(t, MoveProjection, changeAt(t, snap, "src/new.go").Move)
		assert.False(t, changeAt(t, snap, "src/new.go").ContentChanged)
		assert.Equal(t, AttributionProjectionMatch, snap.attribution)
		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, snap), "frozen-assets").Outcome)
		assert.Equal(t, OutcomePass, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})

	t.Run("a move that did not happen gives a new target and an unchanged source", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, codexOp(FileOp{Kind: OpPatchMove, Path: "src/a.go", MoveTo: "src/c.go"}), DefaultLimits())
		g.write("src/c.go", "package c\n")

		snap := g.observe(p)

		assert.Equal(t, []string{"add src/c.go"}, changeList(snap))
		assert.Equal(t, AttributionProjectionDiffers, snap.attribution)
	})

	t.Run("an update with a projection reads projection-match when the result agrees", func(t *testing.T) {
		g := todoRepo(t)

		op := codexOp(updateOp("src/a.go", plainChunk(" func A() {}", "+", "+// TODO(update)")))
		p := g.recordCall(cfg, op, DefaultLimits())
		require.NotContains(t, p.Limitations, LimitationNoProjection)

		// The bytes that the applier writes for this chunk.
		g.write("src/a.go", "package a\n\nfunc A() {}\n\n// TODO(update)\n")

		snap := g.observe(p)

		assert.Equal(t, AttributionProjectionMatch, snap.attribution)
		assert.Equal(t, OutcomeViolation, resultOfRule(t, Evaluate(cfg, snap), "no-todo").Outcome)
	})

	t.Run("an update with a projection reads projection-differs when the result differs", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, codexOp(updateOp("src/a.go", plainChunk("+// end"))), DefaultLimits())
		g.write("src/a.go", "package a\n\nfunc A() {}\n// other\n")

		assert.Equal(t, AttributionProjectionDiffers, g.observe(p).attribution)
	})

	t.Run("an update without a projection is checked on the actual result", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, codexOp(FileOp{Kind: OpPatchUpdate, Path: "src/a.go", Hunks: true}), DefaultLimits())
		require.Contains(t, p.Limitations, LimitationNoProjection)

		g.write("src/a.go", "package a\n\nfunc A() {}\n\n// TODO(update)\n")

		snap := g.observe(p)

		assert.Equal(t, AttributionNoProjection, snap.attribution)
		assert.Equal(t, []string{"src/a.go"}, limitedPaths(snap, LimitationNoProjection))

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		require.Equal(t, OutcomeViolation, res.Outcome, "the actual result has content, also without a projection")
		assert.Equal(t, 5, res.Evidence[0].Line)
	})

	t.Run("a record without usable text gives missing-baseline", func(t *testing.T) {
		for name, content := range map[string]string{"no content": "", "other content": "b3RoZXIK"} {
			g := todoRepo(t)

			op := claudeOp("Edit", editOp("src/a.go", "func A() {}\n", "func A() { _ = 2 }\n", false))
			p := pendingFor(cfg, g.project(op), rawSession, rawMatch)
			require.NotEmpty(t, p.Paths[0].ContentB64)
			p.Paths[0].ContentB64 = content

			require.NoError(t, WritePending(ctx, g.root, p, DefaultLimits()))

			read, err := ReadPending(g.root, p.Client, p.MatchSHA, DefaultLimits())
			require.NoError(t, err)

			g.write("src/a.go", "package a\n\nfunc A() { _ = 2 }\n")

			snap := g.observe(read)
			assert.Equal(t, ContentNotRead, changeAt(t, snap, "src/a.go").Before.Content, name)

			res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
			assert.Equal(t, OutcomeUnknown, res.Outcome, name)
			assert.Equal(t, ReasonMissingBaseline, res.Reason, name)
		}
	})

	t.Run("a dropped baseline gives resource-limit max-baseline-bytes", func(t *testing.T) {
		g := todoRepo(t)

		small := DefaultLimits()
		small.MaxBaselineBytes = 4

		op := claudeOp("Edit", editOp("src/a.go", "func A() {}\n", "func A() { _ = 3 }\n", false))
		p := g.recordCall(cfg, op, small)
		require.Contains(t, p.Limitations, LimitationBaselineTooLarge)

		g.write("src/a.go", "package a\n\nfunc A() { _ = 3 }\n")

		snap := g.observe(p)
		assert.Equal(t, []LimitHit{{Name: LimitMaxBaselineBytes, Value: DefaultLimits().MaxBaselineBytes, Paths: 1}}, snap.Limits)
		assert.True(t, snap.PathsComplete, "a content limit does not cut the path set")

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitMaxBaselineBytes, res.Limit)
	})

	t.Run("a path below a link is never read through it", func(t *testing.T) {
		skipWithoutSymlinks(t)

		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("generated/a.txt", "frozen 2\n")), DefaultLimits())

		outside := filepath.Join(t.TempDir(), "outside")
		require.NoError(t, os.Rename(g.path("generated"), outside))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "a.txt"), []byte("frozen 2\n"), 0o644))
		g.symlink(outside, "generated")

		snap := g.observe(p)

		ch := changeAt(t, snap, "generated/a.txt")
		assert.True(t, ch.unobserved)
		assert.Contains(t, ch.Limitations, LimitationPathThroughLink)
		assert.Equal(t, ContentUnreadable, ch.After.Content)

		res := resultOfRule(t, Evaluate(cfg, snap), "frozen-assets")
		assert.Equal(t, OutcomeUnknown, res.Outcome, "an unobserved change is never a violation")
		assert.Equal(t, ReasonContentUnavailable, res.Reason)
	})

	t.Run("the post-edit budget gives resource-limit", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("src/a.go", "package a\n")), DefaultLimits())

		lim := DefaultLimits()
		lim.PostEditBudget = time.Nanosecond

		snap, err := ObserveAfter(ctx, g.root, p, lim)
		require.NoError(t, err)

		assert.Empty(t, snap.Changes)
		assert.False(t, snap.PathsComplete)
		assert.Equal(t, []LimitHit{{Name: LimitPostEditBudget, Value: 0}}, snap.Limits)
		assert.Empty(t, snap.attribution, "no comparison exists")
		assert.Equal(t, snapshotDigest(snap), snap.SHA256)

		res := resultOfRule(t, Evaluate(cfg, snap), "no-todo")
		assert.Equal(t, OutcomeUnknown, res.Outcome)
		assert.Equal(t, ReasonResourceLimit, res.Reason)
		assert.Equal(t, LimitPostEditBudget, res.Limit)
	})

	t.Run("refusals", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("src/a.go", "package a\n")), DefaultLimits())

		_, err := ObserveAfter(ctx, g.root, nil, DefaultLimits())
		require.Error(t, err)

		unread := *p
		unread.fileSHA256 = ""
		_, err = ObserveAfter(ctx, g.root, &unread, DefaultLimits())
		require.ErrorContains(t, err, "the pending record was not read from its file")

		for _, bad := range []string{"../outside.go", "/etc/passwd", ""} {
			planted := *p
			planted.Paths = []PendingPath{{Path: bad, Before: Side{Type: ObjectAbsent, Content: ContentNotRead}}}

			_, err = ObserveAfter(ctx, g.root, &planted, DefaultLimits())
			require.ErrorContains(t, err, "is not repository-relative", "path %q", bad)
		}

		_, err = ObserveAfter(ctx, t.TempDir(), p, DefaultLimits())
		require.ErrorContains(t, err, "is not inside a git work tree")

		canceled, cancel := context.WithCancel(ctx)
		cancel()

		_, err = ObserveAfter(canceled, g.root, p, DefaultLimits())
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("the observation writes nothing", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("src/a.go", "package a\n// x\n")), DefaultLimits())
		g.write("src/a.go", "package a\n// x\n")

		before := listTree(t, g.root)
		g.observe(p)
		assert.Equal(t, before, listTree(t, g.root), "ObserveAfter reads only")
	})

	t.Run("the post-edit evaluation without a record", func(t *testing.T) {
		in := PostEditInput{
			Config: cfg, Paths: []string{"src/a.go", "README.md"}, PathsComplete: true,
			Root: "/work/repo", Tool: "Edit", Outcome: ToolSucceeded,
		}

		out := EvaluatePostEdit(in)

		require.Len(t, out.Results, 1, "only the pin whose scope holds a path applies")
		assert.Equal(t, "no-todo", out.Results[0].Rule)
		assert.Equal(t, OutcomeUnknown, out.Results[0].Outcome)
		assert.Equal(t, ReasonMissingBaseline, out.Results[0].Reason)
		assert.Equal(t, CheckRef{ID: "no-todo", Kind: KindNoAddedPattern, Version: out.Results[0].Check.Version}, out.Results[0].Check)
		assert.NotEmpty(t, out.Results[0].Check.Version)
		assert.Equal(t, AttributionNoBaseline, out.Attribution)
		assert.Equal(t, SummaryIncomplete, out.State)
		assert.Len(t, out.Pins, 1)

		assert.Contains(t, out.Findings, "seamark post-edit lesson checks after this Edit call: incomplete")
		assert.Contains(t, out.Findings, "- unknown/missing-baseline: pin "+out.Results[0].PinRef+", check no-todo (no-added-pattern/1): src/a.go")
		assert.Contains(t, out.Findings, "No pre-edit baseline exists for this call")
		assert.Contains(t, out.Findings, "Check again: seamark lessons --verify --file /work/repo/README.md --file /work/repo/src/a.go")
		assert.Contains(t, out.Findings, "snapshot none")

		in.PathsComplete = false
		in.NoBaseline = ReasonUnparseableInput

		out = EvaluatePostEdit(in)
		require.Len(t, out.Results, 2, "an unresolved path set holds every executable pin")

		for _, r := range out.Results {
			assert.Equal(t, ReasonUnparseableInput, r.Reason)
		}

		assert.Contains(t, out.Findings, "Check again: seamark lessons --verify\n")
	})

	t.Run("the post-edit evaluation of an observed result", func(t *testing.T) {
		g := todoRepo(t)

		op := claudeOp("Write", writeOp("src/a.go", "package a\n\n// TODO(secret-line)\n"))
		p := g.recordCall(cfg, op, DefaultLimits())
		g.write("src/a.go", "package a\n\n// TODO(secret-line)\n")

		snap := g.observe(p)

		out := EvaluatePostEdit(PostEditInput{
			Config: cfg, Pending: p, Snapshot: snap, Paths: []string{"src/a.go"}, PathsComplete: true,
			Root: g.root, Tool: "Write", Outcome: ToolFailed,
		})

		require.Len(t, out.Results, 1)
		assert.Equal(t, SummaryViolation, out.State)
		assert.Equal(t, AttributionProjectionMatch, out.Attribution)
		assert.Contains(t, out.Findings, "violation: pin "+out.Results[0].PinRef+", check no-todo (no-added-pattern/1): src/a.go:3 adds a line that matches the pattern")
		assert.Contains(t, out.Findings, "snapshot "+snap.SHA256[:12])
		assert.Contains(t, out.Findings, "post-edit")
		assert.Contains(t, out.Findings, "The Write tool failed, so the checks ran on the files as they are now.")
		assert.Contains(t, out.Findings, "The files stay as written; repair the violation in a later edit.")
		assert.NotContains(t, out.Findings, "secret-line", "a hook reply carries no excerpt")
		assert.NotContains(t, out.Findings, "Do not add TODO markers", "a hook reply carries no note")

		for _, word := range []string{"blocked", "prevented", "denied"} {
			assert.NotContains(t, strings.ToLower(out.Findings), word)
		}

		// An event path that the record does not hold has no baseline.
		out = EvaluatePostEdit(PostEditInput{
			Config: cfg, Pending: p, Snapshot: snap, Paths: []string{"src/a.go", "src/z.go"}, PathsComplete: true,
			Root: g.root, Tool: "Write", Outcome: ToolSucceeded,
		})
		assert.Equal(t, OutcomeViolation, out.Results[0].Outcome, "an observed violation stays")

		// A second write of the same content changes nothing.
		again := g.recordCall(cfg, op, DefaultLimits())

		quiet := EvaluatePostEdit(PostEditInput{
			Config: cfg, Pending: again, Snapshot: g.observe(again), Paths: []string{"src/a.go"}, PathsComplete: true,
			Root: g.root, Tool: "Write", Outcome: ToolSucceeded,
		})
		require.Len(t, quiet.Results, 1)
		assert.Equal(t, OutcomeNotApplicable, quiet.Results[0].Outcome)
		assert.Equal(t, SummaryNoApplicableChecks, quiet.State)
		assert.Empty(t, quiet.Findings, "a run without a violation or an unknown result says nothing")
	})

	t.Run("an event path outside the record has no baseline", func(t *testing.T) {
		g := todoRepo(t)

		p := g.recordCall(cfg, claudeOp("Write", writeOp("src/a.go", "package a\n// y\n")), DefaultLimits())
		g.write("src/a.go", "package a\n// y\n")

		out := EvaluatePostEdit(PostEditInput{
			Config: cfg, Pending: p, Snapshot: g.observe(p), Paths: []string{"src/a.go", "src/z.go"}, PathsComplete: true,
			Root: g.root, Tool: "Write", Outcome: ToolSucceeded,
		})

		require.Len(t, out.Results, 1)
		assert.Equal(t, OutcomeUnknown, out.Results[0].Outcome)
		assert.Equal(t, ReasonMissingBaseline, out.Results[0].Reason)
		assert.Contains(t, out.Findings, "src/a.go, src/z.go")
	})

	t.Run("an invalid check set gives configuration-error for every executable pin", func(t *testing.T) {
		broken, errs, err := Parse([]byte(strings.Replace(projectTodoYAML, `'TODO\('`, `'TODO('`, 1)))
		require.NoError(t, err)
		require.NotEmpty(t, errs)

		out := EvaluatePostEdit(PostEditInput{Config: broken, Paths: []string{"README.md"}, PathsComplete: true, Root: "/r", Tool: "Edit"})
		require.Len(t, out.Results, 2)

		for _, r := range out.Results {
			assert.Equal(t, ReasonConfigurationError, r.Reason)
		}

		assert.Equal(t, SummaryIncomplete, out.State)
		assert.Contains(t, out.Findings, "- unknown/configuration-error: lesson checks cannot be established: ")

		out = EvaluatePostEdit(PostEditInput{LoadErr: errors.New("lessons config: yaml: line 1: did not find expected node content"), Root: "/r"})
		assert.Empty(t, out.Results)
		assert.NotNil(t, out.Results)
		assert.Equal(t, SummaryIncomplete, out.State)
		assert.Contains(t, out.Findings, "lesson checks cannot be established: yaml: line 1: did not find expected node content; ask the user to fix .seamark/lessons.yaml")
	})

	t.Run("a check set without executable pins says nothing", func(t *testing.T) {
		plain := parseValid(t, "pin:\n  - {rule: keep, region: src}\n")

		out := EvaluatePostEdit(PostEditInput{Config: plain, Paths: []string{"src/a.go"}, PathsComplete: true, Root: "/r"})
		assert.Empty(t, out.Results)
		assert.Equal(t, SummaryNoExecutableChecks, out.State)
		assert.Empty(t, out.Findings)
	})

	t.Run("the evaluation file imports nothing that does I/O", func(t *testing.T) {
		file, err := parser.ParseFile(token.NewFileSet(), "postedit.go", nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			assert.NotContains(t, impureImports, path, "postedit.go imports %s", path)
		}
	})
}
