package cli

// The parity test of lessons --set-mode and the console pin form (plan
// step P4.9, R-17). Both surfaces call one engine route: the K6 splice
// of the mode and check keys, and the K10 join of the active state. So
// the same request gives the same file bytes and the same impact block.
// Every root is a temporary directory; the hook entries name a link to
// the test binary and never run.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// parityRoot returns a root with an index, setModeYAML, the write gate
// on, Claude Code enforce hooks, and the AC-1 sentinel. The pin form
// needs the index, because it reads the ledger for the link of a pin.
func parityRoot(t *testing.T) (string, *invokerSentinel) {
	t.Helper()

	root := maintainWorkspace(t, nil)
	maintainWriteLessons(t, root, setModeYAML, 0o600)
	maintainWriteConfig(t, root, maintainGateOn)
	capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})

	return root, newInvokerSentinel(t, root)
}

// TestLessonsSetModeMatchesTUIForm proves R-17 parity for the mode edit
// (K6 step 6, AC-27). Each step runs lessons --set-mode on one root and
// the console pin form, through the production services engine, on a
// copy. The two pin files stay byte-equal after every step, and the
// form review shows the impact block that the command prints, the K10
// active line included.
func TestLessonsSetModeMatchesTUIForm(t *testing.T) {
	cliRoot, cliSentinel := parityRoot(t)
	tuiRoot, tuiSentinel := parityRoot(t)

	svc := services.New(workspace.Workspace{Root: tuiRoot, DBPath: store.DefaultPath(tuiRoot)}, version, "seamark")

	steps := []struct {
		name  string
		args  []string
		pin   string
		mode  lessoncheck.Mode
		check string
		want  string
	}{
		{
			name: "enforce binds a check",
			args: []string{"--set-mode", "enforce", "--pin", setModePinRef, "--check", "frozen-assets"},
			pin:  setModePinRef, mode: lessoncheck.ModeEnforce, check: "frozen-assets",
			want: setModeYAMLEnforced,
		},
		{
			name: "advisory removes the mode and keeps the check",
			args: []string{"--set-mode", "advisory", "--pin", setModePinRef},
			pin:  setModePinRef, mode: lessoncheck.ModeAdvisory,
			want: setModeYAMLAdvisory,
		},
		{
			name: "check binds a check to the other entry",
			args: []string{"--set-mode", "check", "--pin", setModeSecondRef, "--check", "no-todo"},
			pin:  setModeSecondRef, mode: lessoncheck.ModeCheck, check: "no-todo",
			want: strings.Replace(setModeYAMLAdvisory, "    note: Reject unknown fields in the parser.\n",
				"    note: Reject unknown fields in the parser.\n    mode: check\n    check: no-todo\n", 1),
		},
	}

	for _, step := range steps {
		stdout, stderr := goldenRun(t, cliRoot, append([]string{"lessons"}, step.args...)...)
		require.Empty(t, stderr, step.name)

		req := maintain.AuthorRequest{
			Op: maintain.AuthorEdit, Ref: parityRef(t, svc, step.pin),
			Mode: step.mode, ModeSet: true, Check: step.check, CheckSet: step.check != "",
		}

		plan, err := svc.PlanAuthor(req)
		require.NoError(t, err, step.name)
		require.True(t, plan.Confirmable, "%s: blocked: %v issues: %v", step.name, plan.Blockers, plan.Issues)

		// The form shows the block that the command prints first,
		// with the K10 active line, not the fixed text of milestone P1.
		block := strings.Join(lessoncheck.ImpactLines(plan.Impact), "\n") + "\n"
		require.NotEqual(t, "\n", block, step.name)
		assert.True(t, strings.HasPrefix(stdout, block), "%s: the form block\n%s\nthe command output\n%s",
			step.name, block, stdout)

		res, err := svc.CommitAuthor(plan)
		require.NoError(t, err, step.name)
		require.NoError(t, res.Err, step.name)
		assert.Equal(t, maintain.Written, res.Outcome, step.name)

		cliText, _ := maintainLessons(t, cliRoot)
		tuiText, _ := maintainLessons(t, tuiRoot)
		assert.Equal(t, step.want, cliText, step.name)
		assert.Equal(t, cliText, tuiText, "%s: the two surfaces write the same bytes", step.name)
	}

	// The first step proves the join: Claude Code enforce hooks are
	// installed, so the block names the badges of both clients.
	first := maintain.AuthorRequest{
		Op: maintain.AuthorEdit, Ref: parityRef(t, svc, setModePinRef),
		Mode: lessoncheck.ModeEnforce, ModeSet: true,
	}

	plan, err := svc.PlanAuthor(first)
	require.NoError(t, err)
	require.Len(t, plan.Impact, 1)
	assert.Equal(t, strings.TrimSpace(activeEnforceBlock), "active: "+plan.Impact[0].Active)

	cliSentinel.requireNoCall(t)
	tuiSentinel.requireNoCall(t)
}

// parityRef returns the binding of the pin with the pin-ref ref, as the
// Pins view of the console reads it. The pin-ref resolves by the K2 rule
// of the file that Pins lists.
func parityRef(t *testing.T, svc services.All, ref string) pinfile.EntryRef {
	t.Helper()

	in := svc.Pins()
	require.NotNil(t, in)
	require.NoError(t, in.DocErr)

	cfg, err := reviews.ParseConfig(in.Doc.Bytes)
	require.NoError(t, err)

	at, err := cfg.ResolvePinRef(ref)
	require.NoError(t, err)

	pin := cfg.Pin[at]
	key := reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions)

	for _, p := range in.Pins {
		if p.Entry.Key == key {
			return p.Entry.Ref()
		}
	}

	require.Failf(t, "no pin", "no pin with the pin-ref %s", ref)

	return pinfile.EntryRef{}
}
