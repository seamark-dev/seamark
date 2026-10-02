package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// This file holds the fixtures of the distillation tests: a plan in
// engine shape, typed executions, and helpers that drive the record
// through the harness.

// distPlannedAt is the fixed plan time of the fixtures.
var distPlannedAt = time.Date(2026, 9, 29, 9, 53, 12, 0, time.Local)

// distFixtureGroup is one planned group of the fixture.
type distFixtureGroup struct {
	region string
	chars  int
	ev     []distill.FindingPlan
}

// distFixtureGroups are six groups in engine order with real-looking
// evidence: review findings with pull requests and fix findings without.
func distFixtureGroups() []distFixtureGroup {
	review := func(id int64, pr int, path string) distill.FindingPlan {
		return distill.FindingPlan{ID: id, Source: "review", PR: pr, Path: path}
	}

	fix := func(id int64, source, path string) distill.FindingPlan {
		return distill.FindingPlan{ID: id, Source: source, Path: path}
	}

	return []distFixtureGroup{
		{"internal/cli", 7600, []distill.FindingPlan{
			review(88, 12, "internal/cli/lessons.go"), review(91, 25, "internal/cli/init_approve.go"),
			fix(93, "fix:conventional", "internal/cli/ui.go"), review(97, 19, "internal/cli/root.go"),
		}},
		{"internal/status", 4800, []distill.FindingPlan{
			review(102, 19, "internal/status/status.go"), review(104, 31, "internal/status/status.go"),
			fix(110, "fix:subject", "internal/status/render.go"),
		}},
		{"internal/index", 3600, []distill.FindingPlan{
			fix(121, "revert", "internal/index/index.go"), review(122, 40, "internal/index/config.go"),
		}},
		{"internal/api", 5600, []distill.FindingPlan{
			review(130, 45, "internal/api/handler.go"), review(131, 45, "internal/api/handler.go"),
			fix(138, "fix:issue-link", "internal/api/db.go"),
		}},
		{"", 9200, []distill.FindingPlan{
			review(140, 2, "README.md"), review(141, 7, "docs/tui.md"), review(149, 9, "Makefile"),
			fix(150, "fix:conventional", "go.mod"), {ID: 152, Source: "review", PR: 11, Paths: []string{".golangci.yml", "go.sum"}},
		}},
		{"internal/gate", 3200, []distill.FindingPlan{
			review(160, 50, "internal/gate/policy.go"), review(161, 50, "internal/gate/match.go"),
		}},
	}
}

// distPreviewOpts shapes a fixture preview.
type distPreviewOpts struct {
	groups   int
	cleanup  bool
	deferred int
	skipped  int
	blockers []string
	region   string
	req      learning.DistillRequest
}

// distPreview returns a preview in the shape of PlanDistill. The
// preflight groups are parallel to the planned groups. The options add
// cleanup rows, skipped signatures and deferred groups. The agent is the
// real claude preset, so the captures show a command that the preset
// runs.
func distPreview(o distPreviewOpts) *learning.DistillPreview {
	all := distFixtureGroups()
	plan := &distill.Plan{PlannedAt: distPlannedAt}

	for i := range min(o.groups, len(all)) {
		g := all[i]
		plan.Preflight.Groups = append(plan.Preflight.Groups, distill.GroupPlan{
			Signature: "sig-" + strconv.Itoa(i), Region: g.region, Findings: len(g.ev),
			PromptChars: g.chars, Evidence: g.ev,
		})
		plan.Preflight.PromptChars += g.chars
		plan.Preflight.Findings += len(g.ev)
		plan.Groups = append(plan.Groups, distill.PlannedGroup{})
	}

	if o.cleanup {
		plan.Cleanup = []domain.Proposal{
			{ID: 4, Rule: "wrap-legacy-errors", Region: "internal/store", Status: domain.ProposalProposed},
			{ID: 7, Rule: "retire-old-flag", Status: domain.ProposalProposed},
		}
	}

	for i := range o.deferred {
		plan.Deferred = append(plan.Deferred, distill.GroupPlan{
			Signature: "def-" + strconv.Itoa(i), Region: "internal/extra" + strconv.Itoa(i), Findings: 2 + i,
		})
	}

	for i := range o.skipped {
		plan.Skipped = append(plan.Skipped, "done-"+strconv.Itoa(i))
	}

	plan.Total = len(plan.Groups) + len(plan.Deferred) + len(plan.Skipped)
	plan.Corpus = plan.Preflight.Findings + 3*o.skipped

	req := o.req
	if !req.AllGroups && req.Limit == 0 {
		req.Limit = distill.DefaultGroupLimit
	}

	return &learning.DistillPreview{
		Request: req, Plan: plan, Region: o.region, RepoRoot: "/work/repo", DBPath: "/work/repo/.seamark/index.db",
		Invocation: agent.ClaudeCommand(),
		PlannedAt:  distPlannedAt, Blockers: o.blockers,
	}
}

// distProposals returns saved proposal rows with the given IDs.
func distProposals(rule string, ids ...int64) []domain.Proposal {
	out := make([]domain.Proposal, len(ids))
	for i, id := range ids {
		out[i] = domain.Proposal{ID: id, Rule: rule + "-" + strconv.FormatInt(id, 10), Status: domain.ProposalProposed}
	}

	return out
}

// distExec returns an execution of p whose outcomes are set by fill. The
// outcomes start unattempted, as the engine starts them.
func distExec(p *learning.DistillPreview, fill func(gs []distill.GroupOutcome, x *distill.Execution)) *distill.Execution {
	x := &distill.Execution{Groups: make([]distill.GroupOutcome, len(p.Plan.Groups))}

	for i := range x.Groups {
		x.Groups[i] = distill.GroupOutcome{Plan: p.Plan.Preflight.Groups[i]}
	}

	for _, c := range p.Plan.Cleanup {
		x.CleanupIDs = append(x.CleanupIDs, c.ID)
	}

	if fill != nil {
		fill(x.Groups, x)
	}

	return x
}

// distDone marks group i completed with proposals and traffic.
func distDone(g *distill.GroupOutcome, ps ...domain.Proposal) {
	g.Status, g.Proposals = distill.GroupCompleted, ps
	g.PromptChars, g.ReplyChars = g.Plan.PromptChars, 1400
}

// distFail marks a group failed with a kind and a cause.
func distFail(g *distill.GroupOutcome, kind distill.FailureKind, err error) {
	g.Status, g.Failure, g.Err = distill.GroupFailed, kind, err
	g.PromptChars = g.Plan.PromptChars
}

// distPartialExec is a run with every outcome kind: saved groups, a
// duplicate-only group, an invalid reply, and unattempted groups after a
// user stop.
func distPartialExec(p *learning.DistillPreview) *distill.Execution {
	return distExec(p, func(gs []distill.GroupOutcome, x *distill.Execution) {
		distDone(&gs[0], distProposals("check-errors", 21, 22)...)
		distDone(&gs[1])
		gs[1].Duplicates = []distill.Duplicate{{Rule: "close-handles", Of: "close-db-handles"}}
		distDone(&gs[2], distProposals("keep-index-fresh", 23)...)
		distFail(&gs[3], distill.FailInvalidReply, errors.New("parse reply: invalid character 'S' looking for beginning of value"))
		gs[4].Status, gs[4].Err = distill.GroupCancelled, context.Canceled
		x.Removed = len(x.CleanupIDs)
		x.Stop, x.StopErr = distill.StopCancelled, context.Canceled
	})
}

// distFake returns services whose plan returns p. run is the scripted
// RunDistill; nil keeps the fake's default. The index is ready, because
// PlanDistill refuses to plan without a ready index, so the header of a
// plan never shows "no index".
func distFake(p *learning.DistillPreview, run func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error)) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.index = ovReadyIndex(f.root, f.dbPath)
	f.planDistill = func(req learning.DistillRequest) (*learning.DistillPreview, error) {
		if err := distEngineShape(p, req); err != nil {
			return nil, err
		}

		out := *p
		out.Request = req

		return &out, nil
	}
	f.runDistill = run

	return f
}

// distEngineShape refuses a fixture plan that the engine cannot make for
// req. The engine defers a group only when the planned groups reach the
// limit, so deferred groups need a limit equal to the planned count. A
// fixture that breaks this rule shows contradicting facts, for example
// "6 of 10 eligible" at a limit of 10.
func distEngineShape(p *learning.DistillPreview, req learning.DistillRequest) error {
	if p.Plan == nil || len(p.Plan.Deferred) == 0 {
		return nil
	}

	if n := len(p.Plan.Groups); req.AllGroups || req.Limit != n {
		return fmt.Errorf("fixture: %d planned and %d deferred groups need a limit of %d, not %+v",
			n, len(p.Plan.Deferred), n, req)
	}

	return nil
}

// distOverLimit is the review fixture with four groups over a custom
// limit of six, and distOverLimitKeys set that limit in the scope form.
func distOverLimit() *learning.DistillPreview {
	return distPreview(distPreviewOpts{groups: 6, cleanup: true, deferred: 4, skipped: 23})
}

// distOverLimitKeys choose the custom limit, type 6, and plan.
var distOverLimitKeys = []string{"down", "down", "space", "6", "enter", "enter"}

// distOpen opens a distillation draft in Lessons › Run.
func distOpen(t *testing.T, f *fakeServices, w, h int) *harness {
	t.Helper()

	hs := newHarness(t, f, w, h)
	hs.send(navigateMsg{dest: destLessons, intent: "distill"})
	require.Equal(t, lesRun, hs.m.screens[destLessons].(*lessons).sub)

	return hs
}

// distRecOf returns the distillation record that the Run sub-view shows.
func distRecOf(t *testing.T, h *harness) *distRecord {
	t.Helper()

	rec, ok := runViewOf(h).cur.(*distRecord)
	require.True(t, ok, "the Run sub-view shows no distillation record")

	return rec
}

// distStartHeld plans and starts a run without running its worker, so a
// test can send progress events one by one. The harness runs commands
// on one goroutine, so a real worker would finish before any frame.
func distStartHeld(t *testing.T, h *harness) *distRecord {
	t.Helper()

	h.press("enter")

	rec := distRecOf(t, h)
	require.Equal(t, distReview, rec.step)
	require.True(t, rec.runnable())

	require.NotNil(t, rec.start(), "start returns the worker and the wait")
	require.Equal(t, distRunning, rec.step)

	return rec
}

// distClosed is a closed queue: a wait on it ends at once, so an event
// sent by a test does not block the harness.
func distClosed() <-chan tea.Msg {
	ch := make(chan tea.Msg)
	close(ch)

	return ch
}

// distSend delivers one progress event to the held record. The event
// time is now, so the activity lines agree with the start time.
func distSend(h *harness, rec *distRecord, ev distill.Event) {
	h.send(distEventMsg{rec: rec, ch: distClosed(), ev: ev, at: time.Now()})
}

// distStarted builds the start event of group i, as the engine sends it.
func distStarted(rec *distRecord, i int) distill.Event {
	g := rec.plan.preview.Plan.Preflight.Groups[i]

	return distill.Event{Kind: distill.EventGroupStarted, Index: i, Total: len(rec.run.outcomes), Group: &g}
}

// distFinished builds the finish event of group i with outcome o.
func distFinished(rec *distRecord, i int, o distill.GroupOutcome) distill.Event {
	o.Plan = rec.plan.preview.Plan.Preflight.Groups[i]

	return distill.Event{Kind: distill.EventGroupFinished, Index: i, Total: len(rec.run.outcomes), Outcome: &o}
}

// distMidRun drives a held run to "group 4 of 6 sending": the cleanup
// is done, three groups finished, one of them with duplicates only.
func distMidRun(t *testing.T, h *harness) *distRecord {
	t.Helper()

	rec := distStartHeld(t, h)

	distSend(h, rec, distill.Event{Kind: distill.EventCleanupDone, Removed: 2})

	for i, o := range []distill.GroupOutcome{
		{Status: distill.GroupCompleted, Proposals: distProposals("check-errors", 21, 22), PromptChars: 7600, ReplyChars: 1400},
		{Status: distill.GroupCompleted, Duplicates: []distill.Duplicate{{Rule: "a", Of: "b"}}, PromptChars: 4800, ReplyChars: 600},
		{Status: distill.GroupCompleted, Proposals: distProposals("keep-index-fresh", 23), PromptChars: 3600, ReplyChars: 900},
	} {
		distSend(h, rec, distStarted(rec, i))
		distSend(h, rec, distFinished(rec, i, o))
	}

	distSend(h, rec, distStarted(rec, 3))

	return rec
}
