package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// doclReport has one check with a Settings link, one with an Evidence
// link, and one without a workflow.
func doclReport() *doctor.Report {
	return &doctor.Report{
		Checks: []doctor.Check{
			{Name: "agent", State: doctor.StateWarn, Detail: `agent CLI "claude" not found on PATH`,
				Fix: "install it, or point agent.argv in .seamark/config.yaml at a CLI you have"},
			{Name: "gh", State: doctor.StateInfo, Detail: "gh not installed — only `seamark index --reviews` needs it",
				Fix: "install GitHub CLI and `gh auth login` to enable review mining"},
			{Name: "policy", State: doctor.StateFail, Detail: ".seamark/policy.yaml does not compile",
				Fix: "fix .seamark/policy.yaml, then run `seamark doctor` again"},
		},
		Warns: 1,
		Fails: 1,
	}
}

func TestDoctorLinksCheckActionMapsStableNames(t *testing.T) {
	st := &state{svc: newFake(), theme: newTheme(false)}
	st.integrations.set([]integration.Inspection{{ClientID: "codex", Name: "Codex"}}, nil, st.integrations.at)

	check := func(name, state string) doctor.Check {
		return doctor.Check{Name: name, State: state, Detail: "any text; the mapping never reads it"}
	}

	cases := []struct {
		check doctor.Check
		want  ovAction
	}{
		{check("agent", doctor.StateWarn), ovNav(destSettings, "group:inference", "open inference settings")},
		{check("gh", doctor.StateInfo), ovNav(destLessons, "evidence", "open Evidence")},
		{check("index", doctor.StateInfo), ovBuildAction},
		{check("hooks", doctor.StateWarn), ovNav(destIntegrations, "setup", "open setup")},
		{check("mcp", doctor.StateWarn), ovNav(destIntegrations, "setup", "open setup")},
		{check("approvals", doctor.StateInfo), ovNav(destIntegrations, "setup", "open setup")},
		{check("skills", doctor.StateWarn), ovNav(destIntegrations, "", "open Integrations")},
		{check("codex", doctor.StateWarn), ovNav(destIntegrations, "client:codex", "inspect Codex")},
		{check("policy", doctor.StateFail), ovAction{}},
		{check("unknown", doctor.StateFail), ovAction{}},
		{check("agent", doctor.StateOK), ovAction{}},
		{check("gh", doctor.StateOK), ovAction{}},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.want, checkAction(st, tc.check), "%s %s", tc.check.Name, tc.check.State)
	}

	st.index.set(IndexView{Probe: workspace.IndexProbe{State: workspace.IndexNewer}}, nil, st.index.at)
	assert.Equal(t, ovAction{}, checkAction(st, check("index", doctor.StateFail)),
		"seamark never rebuilds a newer database")
}

func TestDoctorLinksAgentOpensInferenceSettings(t *testing.T) {
	h, f := docOpen(t, doclReport(), 120, 40)
	stub := ovlRecorder(h, destSettings)

	frame := h.view()
	assert.Contains(t, footerLine(frame), "enter open inference settings", "the footer names the workflow")
	assert.Contains(t, frame, "NEXT")
	assert.Contains(t, docRight(frame), "enter open inference settings")
	assert.Contains(t, docFlat(docRight(frame)), "Run it in a shell when you choose to.", "the fix stays text")

	h.press("enter")
	assert.Equal(t, destSettings, h.m.active)
	assert.Equal(t, []string{"group:inference"}, stub.intents)
	assert.Empty(t, f.called(), "the link opens a view; it plans and saves nothing")
}

func TestDoctorLinksGHOpensEvidence(t *testing.T) {
	h, f := docOpen(t, doclReport(), 120, 40)

	h.press("down")
	assert.Contains(t, footerLine(h.view()), "enter open Evidence", "an info check can still have a workflow")

	h.press("enter")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesEvidence, ovlLessons(h).sub)
	assert.Empty(t, f.called())
}

func TestDoctorLinksWithoutAWorkflowKeepTheDetailRole(t *testing.T) {
	h, f := docOpen(t, doclReport(), 120, 40)

	h.press("end")

	frame := h.view()
	assert.Contains(t, footerLine(frame), "enter open", "enter still focuses the detail")
	assert.NotContains(t, footerLine(frame), "settings")
	assert.NotContains(t, docRight(frame), "NEXT", "no workflow, no NEXT section")

	h.press("enter")
	assert.Equal(t, destDoctor, h.m.active)
	assert.True(t, docView(h).focusDetail)
	assert.Empty(t, f.called())
}

func TestDoctorLinksFocusedDetailOpensTheWorkflow(t *testing.T) {
	h, _ := docOpen(t, doclReport(), 120, 40)
	stub := ovlRecorder(h, destSettings)

	h.press("tab")
	require.True(t, docView(h).focusDetail)
	assert.Contains(t, footerLine(h.view()), "enter open inference settings")

	h.press("enter")
	assert.Equal(t, []string{"group:inference"}, stub.intents)
}

func TestDoctorLinksNarrowOpensTheDetailFirst(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
		h, _ := docOpen(t, doclReport(), size[0], size[1])
		stub := ovlRecorder(h, destSettings)

		h.press("enter")

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Equal(t, destDoctor, h.m.active, "size %v: the first enter shows the detail", size)
		assert.Contains(t, frame, "NEXT", "size %v", size)
		assert.Contains(t, footerLine(frame), "esc back", "size %v: esc stays first", size)
		assert.Empty(t, stub.intents, "size %v", size)

		h.press("enter")
		assert.Equal(t, destSettings, h.m.active, "size %v", size)
		assert.Equal(t, []string{"group:inference"}, stub.intents, "size %v", size)
	}
}

func TestDoctorLinksIndexOpensTheBuildConfirmation(t *testing.T) {
	h, f := docOpen(t, docReport(), 120, 40)

	h.press("down")
	assert.Contains(t, docRight(h.view()), "enter build the index")

	h.press("enter")
	assert.Equal(t, destOverview, h.m.active)
	assert.True(t, ovScreen(h).confirm, "the build waits for its own confirmation")
	assert.Empty(t, f.called(), "nothing is built before the confirmation")

	h.press("esc")
	assert.False(t, ovScreen(h).confirm)
	assert.Empty(t, f.called())
}

func TestDoctorLinksOverviewSharesTheMapping(t *testing.T) {
	f := ovPopulated()
	f.doctor = doclReport()

	h := newHarness(t, f, 120, 40)

	it := ovFind(t, ovScreen(h), "doctor:0:agent")
	assert.Equal(t, ovNav(destSettings, "group:inference", "open inference settings"), it.act)

	ovSelect(t, h, "doctor:0:agent")
	assert.Regexp(t, `! agent: .+Settings ›`, h.view())
}
