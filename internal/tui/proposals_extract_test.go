package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests in this file cover the Phase 3 parts of Lessons › Proposals.
// They test the read-only superseded history and the t key that opens a
// trigger extraction review. They also test the applied-row text that
// points to Pins.

// propExtractFake returns services over lesLedger whose extraction plan
// records each request. The plan is the default no-op preview, so a
// review opens without a run.
func propExtractFake(reqs *[]learning.ExtractRequest) *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.planExtract = func(req learning.ExtractRequest) (*learning.ExtractPreview, error) {
		*reqs = append(*reqs, req)

		return extPreview(extPreviewOpts{none: true}), nil
	}

	return f
}

// propDetailText renders the detail of the cursor row in one wide
// column and flattens it. A prose assertion then does not depend on the
// list box beside it.
func propDetailText(h *harness) string {
	p := lesProps(h)

	cur, ok := p.current(p.rows())
	if !ok {
		return ""
	}

	return lesFlat(strings.Join(p.detailLines(cur, 200), "\n"))
}

// propSupersededRow is a ledger row that a prune moved to superseded.
func propSupersededRow() domain.Proposal {
	return domain.Proposal{
		ID: 8, Rule: "retire-old-flag", Region: "internal/cli", Note: "Remove the --legacy flag from new code.",
		Members: []int64{104}, Agent: "claude/v2", Status: domain.ProposalSuperseded,
	}
}

func TestProposalsSupersededHistoryIsReadOnly(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	f.ledger.Superseded = []domain.Proposal{propSupersededRow()}

	h := lesOpen(t, f, 120, 40)
	h.press("f", "f", "f")

	p := lesProps(h)
	require.Equal(t, propSuperseded, p.filter)
	assert.Equal(t, []int64{8}, lesRowIDs(p))

	frame := h.view()
	assert.Contains(t, frame, "state pending 3  applied 1  dismissed 1 [superseded 1]")
	assert.Contains(t, frame, "Superseded proposals")
	assert.Contains(t, frame, "○ superseded", "the hollow glyph matches the superseded rows of Pins")
	assert.NotContains(t, frame, "● superseded")
	assert.Contains(t, propDetailText(h), "Superseded: its pin was pruned. Read-only; not restorable here.")
	assert.Contains(t, propDetailText(h), "Advisories cover pending and applied proposals only.")

	footer := shellAFooter(frame)
	for _, k := range []string{"space select", "a apply", "d dismiss", "t extract", "c record"} {
		assert.NotContains(t, footer, k, "a superseded row offers no action")
	}

	h.press("space", "a", "d", "c", "t")
	assert.Empty(t, p.selectedIDs())
	assert.Empty(t, f.called(), "no key plans or mutates a superseded row")
	assert.Nil(t, p.dec)

	// f cycles back to pending.
	h.press("f")
	assert.Equal(t, propPending, p.filter)
}

func TestProposalsExtractKeyOpensTheReviewOfTheSelection(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	h := lesOpen(t, f, 120, 40)

	// The cursor row stands in for an empty selection. The 120-column
	// footer keeps the keys of the 1a layout and drops t after f and /,
	// so a wide footer shows it.
	h.resize(200, 50)
	assert.Contains(t, shellAFooter(h.view()), "t extract triggers")
	h.resize(120, 40)
	assert.Contains(t, propDetailText(h), "t asks the agent where its mistake occurs (trigger extraction).")

	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{1}, reqs[0].IDs)
	assert.Equal(t, lesRun, hookLessons(h).sub, "the review opens in Lessons › Run")
	assert.Equal(t, extReview, extRecOf(t, h).step)
	assert.Zero(t, f.callCount("RunExtract"), "t only previews")

	// Marked rows replace the cursor row, ineligible ones included: the
	// review lists each of them with its reason.
	h.press("p", "esc", "p", "space", "down", "space", "down", "space", "t")
	require.Len(t, reqs, 2)
	assert.Equal(t, []int64{1, 2, 3}, reqs[1].IDs)
	assert.Empty(t, reqs[1].Skipped)
	assert.Zero(t, f.callCount("RunExtract"))
}

// propPinLinks returns a Pins snapshot whose links classify the given
// proposals, so the Proposals detail can read the pin link of a row.
func propPinLinks(links map[int64]pinfile.Link) *maintain.Inspection {
	return &maintain.Inspection{
		Doc:   &pinfile.Document{},
		Links: &pinfile.Links{ByProposal: links},
		Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
	}
}

func TestProposalsExtractKeyOnAnAppliedRow(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	f.pins = propPinLinks(map[int64]pinfile.Link{4: pinfile.LinkLinked})
	h := lesOpen(t, f, 120, 40)
	h.press("f")

	require.Equal(t, propApplied, lesProps(h).filter)

	flat := propDetailText(h)
	assert.Contains(t, flat, "The pin of this proposal is in Lessons › Pins (press m), which previews a retarget "+
		"or a prune of it.")
	assert.NotContains(t, flat, "stay on the CLI", "the applied row points to Pins now")
	assert.Contains(t, shellAFooter(h.view()), "t extract triggers")

	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{4}, reqs[0].IDs)
}

func TestProposalsExtractKeyNeedsAnOpenQuestion(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fakeServices)
		keys  []string
		why   string
	}{
		{
			name: "answered with paths",
			setup: func(f *fakeServices) {
				f.ledger.Pending[0].TriggerPaths = []string{"internal/api/handler.go"}
			},
			why: "already answered with trigger paths",
		},
		{
			name:  "no living evidence",
			keys:  []string{"down", "down"},
			setup: func(*fakeServices) {},
			why:   "no living evidence",
		},
		{
			name: "applied pin not present",
			setup: func(f *fakeServices) {
				f.pins = &maintain.Inspection{
					Doc:   &pinfile.Document{},
					Links: &pinfile.Links{ByProposal: map[int64]pinfile.Link{4: pinfile.LinkNotPresent}},
					Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
				}
			},
			keys: []string{"f"},
			why:  "pin not present",
		},
		{
			name: "pending row already carried by a pin",
			setup: func(f *fakeServices) {
				f.pins = &maintain.Inspection{
					Doc:   &pinfile.Document{},
					Links: &pinfile.Links{ByProposal: map[int64]pinfile.Link{1: pinfile.LinkPendingMatch}},
					Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
				}
			},
			why: "a pin already carries it",
		},
		{
			name:  "dismissed",
			setup: func(*fakeServices) {},
			keys:  []string{"f", "f"},
			why:   "Dismissed and superseded proposals have no trigger question to ask.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reqs []learning.ExtractRequest

			f := propExtractFake(&reqs)
			c.setup(f)

			h := lesOpen(t, f, 120, 40)
			h.press(c.keys...)

			assert.NotContains(t, shellAFooter(h.view()), "t extract", "no key without an eligible target")

			h.press("t")
			assert.Empty(t, reqs, "t opens nothing")
			assert.Contains(t, lesFlat(h.view()), c.why)
			assert.Empty(t, f.called())
		})
	}
}

func TestProposalsFocusShowsTheProposalOfAResult(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	p := lesProps(h)

	// A focus on an applied row switches the state filter.
	h.send(focusProposalMsg{id: 4})
	assert.Equal(t, propApplied, p.filter)
	assert.Equal(t, int64(4), p.cursorID)

	// A region filter that hides the row ends, as a filter change does.
	h.press("f", "f", "f", "/")
	h.typeText("docs")
	h.press("enter")
	require.Equal(t, "docs", p.region)

	h.send(focusProposalMsg{id: 2})
	assert.Equal(t, propPending, p.filter)
	assert.Empty(t, p.region)
	assert.Equal(t, int64(2), p.cursorID)

	// A marked row stays marked when the focus changes no filter.
	h.press("space")
	require.Equal(t, []int64{2}, p.selectedIDs())

	h.send(focusProposalMsg{id: 1})
	assert.Equal(t, int64(1), p.cursorID)
	assert.Equal(t, []int64{2}, p.selectedIDs())

	// An unknown ID changes nothing.
	h.send(focusProposalMsg{id: 99})
	assert.Equal(t, int64(1), p.cursorID)
	assert.Empty(t, f.called())
}

func TestProposalsAppliedRowFollowsItsPinLink(t *testing.T) {
	cases := []struct {
		name  string
		pins  *maintain.Inspection
		want  []string
		never []string
	}{
		{
			name: "linked",
			pins: propPinLinks(map[int64]pinfile.Link{4: pinfile.LinkLinked}),
			want: []string{"The pin of this proposal is in Lessons › Pins (press m), which previews a retarget or a " +
				"prune of it."},
		},
		{
			name: "not present",
			pins: propPinLinks(map[int64]pinfile.Link{4: pinfile.LinkNotPresent}),
			want: []string{
				"No entry of .seamark/lessons.yaml carries the pin of this proposal. Lessons › Pins lists it under " +
					"not present (press m, then f). Nothing is superseded automatically.",
				"Trigger question: pin not present.",
			},
			never: []string{"The pin of this proposal is in"},
		},
		{
			name: "ambiguous",
			pins: propPinLinks(map[int64]pinfile.Link{4: pinfile.LinkAmbiguousFile}),
			want: []string{"Lessons › Pins (press m) shows every match (ambiguous: duplicate pins). Retarget and " +
				"prune stay blocked until the match is resolved."},
			never: []string{"The pin of this proposal is in"},
		},
		{
			name: "unknown",
			want: []string{"Lessons › Pins (press m) shows the installed pins and previews a retarget or a prune " +
				"of a linked pin."},
			never: []string{"The pin of this proposal is in"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var reqs []learning.ExtractRequest

			f := propExtractFake(&reqs)
			f.pins = c.pins
			h := lesOpen(t, f, 120, 40)
			h.press("f")
			require.Equal(t, propApplied, lesProps(h).filter)

			flat := propDetailText(h)
			for _, w := range c.want {
				assert.Contains(t, flat, w)
			}

			for _, n := range c.never {
				assert.NotContains(t, flat, n, "an applied row is no proof that a pin exists")
			}
		})
	}
}

func TestProposalsExtractLineFollowsTheMarks(t *testing.T) {
	var reqs []learning.ExtractRequest

	f := propExtractFake(&reqs)
	h := lesOpen(t, f, 120, 40)

	// Mark p3, which has no living evidence, and put the cursor on p1.
	h.press("down", "down", "space", "up", "up")
	require.Equal(t, int64(1), lesProps(h).cursorID)

	flat := propDetailText(h)
	assert.Contains(t, flat, "Trigger question: no selected proposal has an open question; p3: no living evidence.")
	assert.NotContains(t, flat, "t asks the agent where its mistake occurs", "t does not ask about the cursor row")
	assert.NotContains(t, shellAFooter(h.view()), "t extract")

	// With an eligible row marked too, t asks about the marked rows.
	h.press("space")
	require.ElementsMatch(t, []int64{1, 3}, lesProps(h).selectedIDs())

	flat = propDetailText(h)
	assert.Contains(t, flat, "t asks the agent about the 2 selected proposals (trigger extraction). The review "+
		"lists the ones that it does not ask; nothing is sent before y.")

	h.press("t")
	require.Len(t, reqs, 1)
	assert.Equal(t, []int64{1, 3}, reqs[0].IDs)
}
