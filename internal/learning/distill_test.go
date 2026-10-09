package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// distAreaFindings builds two findings per directory with words no other
// finding shares, so the grouper makes one directory group per
// directory. Directory i gets IDs 21+2i and 22+2i.
func distAreaFindings(dirs ...string) []model.Finding {
	var out []model.Finding

	for i, dir := range dirs {
		for j := range 2 {
			id := int64(21 + 2*i + j)
			out = append(out, model.Finding{
				ID: id, Path: fmt.Sprintf("%s/f%d.go", dir, id), PR: int(id), Reviewer: "person",
				Body: fmt.Sprintf("Uniq%dalpha uniq%dbravo uniq%dcharlie.", id, id, id),
			})
		}
	}

	return out
}

// distMarkerArgv is an agent command that leaves a marker file when it
// runs. A preview must never run it.
func distMarkerArgv(marker string) string {
	return fmt.Sprintf("agent:\n  argv: [\"sh\", \"-c\", \"touch %s; cat >/dev/null; echo '{\\\"patterns\\\":[]}'\"]\n", marker)
}

// distWorkspace builds an indexed workspace with stored findings and an
// agent command that leaves the returned marker when it runs.
func distWorkspace(t *testing.T, findings []model.Finding) (ws workspace.Workspace, marker string) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/dist\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package dist\n\n// A returns one.\nfunc A() int { return 1 }\n"), 0o644))

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: ws.Root, DBPath: ws.DBPath})
	require.NoError(t, err)

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, findings))
	require.NoError(t, st.Close())

	marker = filepath.Join(t.TempDir(), "agent-ran")
	distWrite(t, ws, ".seamark/config.yaml", distMarkerArgv(marker))

	return ws, marker
}

// distWrite writes a repository-relative file of the workspace.
func distWrite(t *testing.T, ws workspace.Workspace, rel, content string) {
	t.Helper()

	path := filepath.Join(ws.Root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// distFileDigest returns the SHA-256 of a file, or "absent".
func distFileDigest(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}

	require.NoError(t, err)

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// distAgent is a scripted invoker that records the first finding ID of
// every prompt and sees the call context.
type distAgent struct {
	mu    sync.Mutex
	calls []int64
	fn    func(ctx context.Context, prompt string) (string, error)
}

func (a *distAgent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, distFirstID(prompt))
	a.mu.Unlock()

	if a.fn == nil {
		return `{"patterns":[]}`, nil
	}

	return a.fn(ctx, prompt)
}

func (a *distAgent) Name() string { return "fake" }

func (a *distAgent) called() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]int64(nil), a.calls...)
}

var distPromptID = regexp.MustCompile(`finding id=(\d+) `)

// distFirstID reads the first finding ID of a prompt.
func distFirstID(prompt string) int64 {
	m := distPromptID.FindStringSubmatch(prompt)
	if m == nil {
		return 0
	}

	id, _ := strconv.ParseInt(m[1], 10, 64)

	return id
}

// distReply answers with one valid pattern citing ids.
func distReply(rule, note string, ids ...int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}

	return fmt.Sprintf(`{"patterns":[{"rule":%q,"note":%q,"finding_ids":[%s],"trigger_paths":[]}]}`,
		rule, note, strings.Join(parts, ","))
}

// distPlan previews with the default request and requires success.
func distPlan(t *testing.T, ws workspace.Workspace, req DistillRequest) *DistillPreview {
	t.Helper()

	p, err := PlanDistill(ws, integration.Builtin(), req)
	require.NoError(t, err)

	return p
}

// distMarks reads the recorded group signatures.
func distMarks(t *testing.T, ws workspace.Workspace) map[string]bool {
	t.Helper()

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	marks, err := st.DistilledSignatures()
	require.NoError(t, err)

	return marks
}

// distPending reads the pending proposals.
func distPending(t *testing.T, ws workspace.Workspace) []model.Proposal {
	t.Helper()

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)

	return pending
}

// distSeed stores proposals directly, as an earlier run or decision would.
func distSeed(t *testing.T, ws workspace.Workspace, ps ...model.Proposal) {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	for i := range ps {
		require.NoError(t, storetest.InsertProposal(st, &ps[i]))
	}
}

func TestPlanDistillWritesNothingAndRunsNoAgent(t *testing.T) {
	ws, marker := distWorkspace(t, distAreaFindings("a", "b"))
	distSeed(t, ws, model.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{21, 22}, Status: model.ProposalProposed,
	})

	dbBefore := distFileDigest(t, ws.DBPath)
	configBefore := distFileDigest(t, filepath.Join(ws.Root, ".seamark", "config.yaml"))

	p := distPlan(t, ws, DistillRequest{})

	assert.Equal(t, dbBefore, distFileDigest(t, ws.DBPath), "planning leaves the database byte-identical")
	assert.Equal(t, configBefore, distFileDigest(t, filepath.Join(ws.Root, ".seamark", "config.yaml")))
	assert.Equal(t, "absent", distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml")))
	assert.NoFileExists(t, marker, "a preview never runs the agent")

	assert.Len(t, distPending(t, ws), 1, "the cleanup is listed, not done")
	require.Len(t, p.Plan.Cleanup, 1)
	assert.Equal(t, "stale", p.Plan.Cleanup[0].Rule)

	assert.Len(t, p.Plan.Groups, 2)
	assert.Equal(t, distill.DefaultGroupLimit, p.Plan.Options.Limit, "the default is the CLI default")
	assert.Equal(t, "custom", p.Invocation.Name)
	assert.Equal(t, "sh", p.Invocation.Argv[0])
	assert.Equal(t, p.Invocation.Argv, p.Plan.Preflight.Agent)
	assert.Equal(t, ws.DBPath, p.DBPath)
	assert.Equal(t, workspace.Probe(ws).Root, p.RepoRoot)
	assert.True(t, p.Config.Exists)
	assert.False(t, p.Lessons.Exists)
	assert.Empty(t, p.Blockers)
	assert.False(t, p.PlannedAt.IsZero())
	assert.False(t, p.NoOp())
	assert.False(t, p.CleanupOnly())
}

func TestPlanDistillAppliesTheLimitRules(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a", "b", "c"))

	one := distPlan(t, ws, DistillRequest{Limit: 1})
	assert.Len(t, one.Plan.Groups, 1)
	assert.Len(t, one.Plan.Deferred, 2)

	all := distPlan(t, ws, DistillRequest{AllGroups: true})
	assert.Zero(t, all.Plan.Options.Limit, "all groups map to the engine's zero limit")
	assert.Len(t, all.Plan.Groups, 3)

	for _, req := range []DistillRequest{{Limit: -1}, {Limit: 5, AllGroups: true}} {
		_, err := PlanDistill(ws, nil, req)
		require.ErrorIs(t, err, distill.ErrInvalidLimit, "request %+v", req)
	}
}

func TestPlanDistillNormalizesTheRegion(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a", "b"))

	p := distPlan(t, ws, DistillRequest{RegionInput: "./a/"})
	assert.Equal(t, "a", p.Region)
	assert.Equal(t, "a", p.Plan.Options.Region)
	assert.Len(t, p.Plan.Groups, 1)

	whole := distPlan(t, ws, DistillRequest{RegionInput: ws.Root})
	assert.Empty(t, whole.Region, "the root means the whole repository")

	_, err := PlanDistill(ws, nil, DistillRequest{RegionInput: "../elsewhere"})
	require.ErrorIs(t, err, distill.ErrRegionEscapes)

	// A valid scope without findings is a normal empty state.
	empty := distPlan(t, ws, DistillRequest{RegionInput: "nowhere"})
	assert.True(t, empty.NoOp())
	assert.Zero(t, empty.Plan.Corpus)
}

func TestPlanDistillBlocksOnConfigurationErrors(t *testing.T) {
	cases := []struct {
		name, file, content, path, text string
		is                              error
	}{
		{"malformed config", ".seamark/config.yaml", "agent: [\n", ".seamark/config.yaml", "agent config:", nil},
		{"unknown agent", ".seamark/config.yaml", "agent:\n  cli: hal9000\n", ".seamark/config.yaml", "hal9000", nil},
		{"malformed pins", ".seamark/lessons.yaml", "pin: [\n", ".seamark/lessons.yaml", "lessons config:", reviews.ErrLessonsConfig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := distWorkspace(t, distAreaFindings("a"))
			distWrite(t, ws, tc.file, tc.content)

			_, err := PlanDistill(ws, integration.Builtin(), DistillRequest{})
			require.ErrorIs(t, err, ErrDistillConfig)

			var cfgErr *DistillConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tc.path, cfgErr.Path, "the error names the file to repair")
			assert.Contains(t, err.Error(), tc.text, "the reader error is shown as is")

			if tc.is != nil {
				require.ErrorIs(t, err, tc.is)
			}

			assert.NoFileExists(t, marker)
		})
	}
}

func TestPlanDistillNeedsAReadyIndex(t *testing.T) {
	root := t.TempDir()

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = PlanDistill(ws, nil, DistillRequest{})
	require.ErrorIs(t, err, ErrDistillNotReady)
	assert.NoFileExists(t, ws.DBPath, "a preview never creates the database")

	// A database with decisions but no index run is not ready either.
	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	_, err = PlanDistill(ws, nil, DistillRequest{})
	require.ErrorIs(t, err, ErrDistillNotReady)
}

func TestPlanDistillBlocksAMissingAgentAndRunRefusesIt(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a"))
	distWrite(t, ws, ".seamark/config.yaml", "agent:\n  argv: [\"no-such-agent-binary-for-tests\"]\n")

	p := distPlan(t, ws, DistillRequest{})
	require.Len(t, p.Blockers, 1)
	assert.Contains(t, p.Blockers[0], `agent cli "no-such-agent-binary-for-tests" not found on PATH`)
	assert.Len(t, p.Plan.Groups, 1, "a blocked preview still discloses the plan")

	agent := &distAgent{}

	x, err := RunDistill(context.Background(), ws, nil, p, agent, nil)
	require.ErrorIs(t, err, ErrDistillBlocked)
	assert.Nil(t, x)
	assert.Empty(t, agent.called())
	assert.Empty(t, distMarks(t, ws))
}

func TestNoOpAndCleanupOnlyPreviewsStartNoAgent(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a"))

	// Nothing new: every group is recorded.
	first := distPlan(t, ws, DistillRequest{})
	require.Len(t, first.Plan.Groups, 1)

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, storetest.MarkDistilled(st, first.Plan.Groups[0].Group.Signature, "", 1))
	require.NoError(t, st.Close())

	p := distPlan(t, ws, DistillRequest{})
	assert.True(t, p.NoOp())
	assert.Len(t, p.Plan.Skipped, 1, "the recorded signature explains why nothing is new")

	agent := &distAgent{}

	_, err = RunDistill(context.Background(), ws, nil, p, agent, nil)
	require.ErrorIs(t, err, ErrNothingToDistill)

	_, err = CleanupPending(ws, p)
	require.ErrorIs(t, err, ErrNotCleanupOnly)

	// Cleanup only: a stale pending row and a live one.
	distSeed(t, ws,
		model.Proposal{Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: model.ProposalProposed},
		model.Proposal{Signature: first.Plan.Groups[0].Group.Signature, Rule: "live", Note: "n",
			Members: []int64{21, 22}, Status: model.ProposalProposed},
	)

	p = distPlan(t, ws, DistillRequest{})
	require.True(t, p.CleanupOnly())

	_, err = RunDistill(context.Background(), ws, nil, p, agent, nil)
	require.ErrorIs(t, err, ErrNothingToDistill, "a cleanup never starts the agent")
	assert.Empty(t, agent.called())

	removed, err := CleanupPending(ws, p)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	pending := distPending(t, ws)
	require.Len(t, pending, 1)
	assert.Equal(t, "live", pending[0].Rule, "only the planned row is deleted")
}

func TestCleanupPendingRefusesAChangedLedger(t *testing.T) {
	ws, _ := distWorkspace(t, nil)
	distSeed(t, ws, model.Proposal{
		Signature: "gone", Rule: "stale", Note: "n", Members: []int64{1, 2}, Status: model.ProposalProposed,
	})

	p := distPlan(t, ws, DistillRequest{})
	require.True(t, p.CleanupOnly())

	// Another writer decides the planned row after the preview.
	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{p.Plan.Cleanup[0].ID}, model.ProposalDismissed)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	removed, err := CleanupPending(ws, p)
	require.ErrorIs(t, err, distill.ErrStale)
	assert.Zero(t, removed)
}

func TestRunDistillExecutesExactlyThePreview(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("ok", "dup", "bad", "fail"))
	distWrite(t, ws, ".seamark/lessons.yaml",
		"pin:\n  - rule: known-pin-rule\n    note: Keep the known pin guidance exactly.\n")

	lessonsBefore := distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml"))

	p := distPlan(t, ws, DistillRequest{})
	require.Len(t, p.Plan.Groups, 4)

	agent := &distAgent{fn: func(_ context.Context, prompt string) (string, error) {
		switch id := distFirstID(prompt); id {
		case 21:
			return distReply("ok-rule", "Keep the ok handlers aligned.", 21, 22), nil
		case 23:
			return distReply("known-pin-rule", "Keep the known pin guidance exactly.", 23, 24), nil
		case 25:
			return "not json", nil
		default:
			return "", errors.New("agent fake: not logged in")
		}
	}}

	var events []distill.Event

	x, err := RunDistill(context.Background(), ws, integration.Builtin(), p, agent,
		func(ev distill.Event) { events = append(events, ev) })
	require.NoError(t, err)
	require.NotNil(t, x)
	assert.Equal(t, distill.StopNone, x.Stop)
	assert.Len(t, agent.called(), 4)

	byFirst := map[int64]distill.GroupOutcome{}
	for i, pg := range p.Plan.Groups {
		byFirst[pg.Group.Findings[0].ID] = x.Groups[i]
	}

	assert.Equal(t, distill.GroupCompleted, byFirst[21].Status)
	require.Len(t, byFirst[21].Proposals, 1)
	assert.Equal(t, distill.GroupCompleted, byFirst[23].Status)
	assert.Len(t, byFirst[23].Duplicates, 1, "the strictly loaded pin deduplicates")
	assert.Equal(t, distill.FailInvalidReply, byFirst[25].Failure)
	assert.Equal(t, distill.FailAgent, byFirst[27].Failure)
	assert.NotEmpty(t, events)

	pending := distPending(t, ws)
	require.Len(t, pending, 1)
	assert.Equal(t, byFirst[21].Proposals[0].ID, pending[0].ID, "the outcome names the stored ID")
	assert.Len(t, distMarks(t, ws), 2)
	assert.Equal(t, lessonsBefore, distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml")),
		"no pin is applied")
}

func TestRunDistillRejectsChangedInputs(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, ws workspace.Workspace, p *DistillPreview) workspace.Workspace
		reason string
	}{
		{"findings", func(t *testing.T, ws workspace.Workspace, _ *DistillPreview) workspace.Workspace {
			edited := distAreaFindings("a", "b")
			edited[0].Body += " Edited."

			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, st.ReplaceLessons(nil, edited))
			require.NoError(t, st.Close())

			return ws
		}, "the findings in scope changed"},
		{"decisions", func(t *testing.T, ws workspace.Workspace, _ *DistillPreview) workspace.Workspace {
			distSeed(t, ws, model.Proposal{Signature: "s", Rule: "decided", Note: "n",
				Members: []int64{21, 22}, Status: model.ProposalDismissed})

			return ws
		}, "the proposal ledger changed"},
		{"signatures", func(t *testing.T, ws workspace.Workspace, p *DistillPreview) workspace.Workspace {
			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, storetest.MarkDistilled(st, p.Plan.Groups[1].Group.Signature, "", 1))
			require.NoError(t, st.Close())

			return ws
		}, "the distilled signatures changed"},
		{"pins", func(t *testing.T, ws workspace.Workspace, _ *DistillPreview) workspace.Workspace {
			distWrite(t, ws, ".seamark/lessons.yaml", "pin:\n  - rule: new-pin\n    note: A new pin.\n")

			return ws
		}, ".seamark/lessons.yaml changed"},
		{"invocation settings", func(t *testing.T, ws workspace.Workspace, _ *DistillPreview) workspace.Workspace {
			distWrite(t, ws, ".seamark/config.yaml", "agent:\n  cli: codex\n")

			return ws
		}, ".seamark/config.yaml changed"},
		{"resolved command", func(_ *testing.T, ws workspace.Workspace, p *DistillPreview) workspace.Workspace {
			// The same bytes can resolve to another command, for
			// example through another registry. The run compares.
			p.Invocation.Argv = []string{"sh", "-c", "something else"}

			return ws
		}, "the agent command changed"},
		{"workspace database", func(t *testing.T, ws workspace.Workspace, _ *DistillPreview) workspace.Workspace {
			ws.DBPath = filepath.Join(t.TempDir(), "other.db")

			return ws
		}, "the workspace database changed"},
	}

	// The plan names each changed input of the store snapshot. Each case
	// changes one input, so the error names no other one.
	snapshot := []string{"the findings in scope changed", "the proposal ledger changed",
		"the distilled signatures changed"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := distWorkspace(t, distAreaFindings("a", "b"))

			// The seed of the decisions case also records the fixture
			// mark. That mark exists before the plan, so the seed changes
			// the ledger only.
			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, storetest.MarkDistilled(st, storetest.FixtureMark, "", 0))
			require.NoError(t, st.Close())

			p := distPlan(t, ws, DistillRequest{})

			runWS := tc.change(t, ws, p)
			agent := &distAgent{}

			x, err := RunDistill(context.Background(), runWS, nil, p, agent, nil)
			require.ErrorIs(t, err, distill.ErrStale)
			assert.Contains(t, err.Error(), tc.reason)

			for _, other := range snapshot {
				if other != tc.reason {
					assert.NotContains(t, err.Error(), other)
				}
			}
			assert.Nil(t, x, "a refused run returns no execution")
			assert.Empty(t, agent.called(), "execution rejects changed intent before any call")
			assert.NoFileExists(t, marker)
			assert.Empty(t, distPending(t, ws))
		})
	}
}

func TestRunDistillStopsWhenAFileChangesBetweenGroups(t *testing.T) {
	ws, _ := distWorkspace(t, distAreaFindings("a", "b"))
	p := distPlan(t, ws, DistillRequest{})

	agent := &distAgent{fn: func(context.Context, string) (string, error) {
		distWrite(t, ws, ".seamark/lessons.yaml", "pin:\n  - rule: added-meanwhile\n    note: A pin.\n")

		return `{"patterns":[]}`, nil
	}}

	x, err := RunDistill(context.Background(), ws, nil, p, agent, nil)
	require.NoError(t, err, "a conflict is a typed stop, not an error")

	assert.Len(t, agent.called(), 1)
	assert.Equal(t, distill.StopConflict, x.Stop)
	require.ErrorIs(t, x.StopErr, distill.ErrStale)
	assert.Contains(t, x.StopErr.Error(), ".seamark/lessons.yaml changed")
	assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status, "the committed group stays")
	assert.Equal(t, distill.GroupUnattempted, x.Groups[1].Status)
}

func TestRunDistillStops(t *testing.T) {
	t.Run("before the next group", func(t *testing.T) {
		ws, _ := distWorkspace(t, distAreaFindings("a", "b", "c"))
		p := distPlan(t, ws, DistillRequest{})
		agent := &distAgent{}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		x, err := RunDistill(ctx, ws, nil, p, agent, func(ev distill.Event) {
			if ev.Kind == distill.EventGroupFinished {
				cancel()
			}
		})
		require.NoError(t, err)

		assert.Len(t, agent.called(), 1, "no invocation starts after the stop")
		assert.Equal(t, distill.StopCancelled, x.Stop)
		assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status)
		assert.Equal(t, distill.GroupUnattempted, x.Groups[1].Status)
		assert.Equal(t, distill.GroupUnattempted, x.Groups[2].Status)
		assert.Len(t, distMarks(t, ws), 1)
	})

	t.Run("during a group", func(t *testing.T) {
		ws, _ := distWorkspace(t, distAreaFindings("a", "b"))
		p := distPlan(t, ws, DistillRequest{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent := &distAgent{fn: func(callCtx context.Context, _ string) (string, error) {
			cancel()
			<-callCtx.Done()

			return "", fmt.Errorf("agent fake: %w", callCtx.Err())
		}}

		x, err := RunDistill(ctx, ws, nil, p, agent, nil)
		require.NoError(t, err)

		assert.Len(t, agent.called(), 1)
		assert.Equal(t, distill.GroupCancelled, x.Groups[0].Status)
		assert.Equal(t, distill.GroupUnattempted, x.Groups[1].Status)
		assert.Empty(t, distMarks(t, ws), "a cancelled group is not recorded")
	})

	t.Run("late reply", func(t *testing.T) {
		ws, _ := distWorkspace(t, distAreaFindings("a", "b"))
		p := distPlan(t, ws, DistillRequest{})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		agent := &distAgent{fn: func(_ context.Context, prompt string) (string, error) {
			cancel()

			id := distFirstID(prompt)

			return distReply("late-rule", "Keep the late reply.", id, id+1), nil
		}}

		x, err := RunDistill(ctx, ws, nil, p, agent, nil)
		require.NoError(t, err)

		assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status, "the reply passed the commit boundary")
		assert.Equal(t, distill.GroupUnattempted, x.Groups[1].Status)
		assert.Len(t, distPending(t, ws), 1)
	})
}

// A run with a limit of 1 has one planned group. A stop in that group
// must still report a stopped run, because a surface labels the result
// from Execution.Stop and the final EventStopped.
func TestRunDistillStopsInTheOnlyPlannedGroup(t *testing.T) {
	cases := []struct {
		name string
		// inCall stops during the agent call; otherwise the stop comes
		// in the EventGroupStarted receiver, before the call.
		inCall bool
	}{
		{"during the call", true},
		{"during the start event", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := distWorkspace(t, distAreaFindings("a", "b"))
			p := distPlan(t, ws, DistillRequest{Limit: 1})
			require.Len(t, p.Plan.Groups, 1)
			require.Len(t, p.Plan.Deferred, 1)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			agent := &distAgent{fn: func(callCtx context.Context, _ string) (string, error) {
				cancel()
				<-callCtx.Done()

				return "", fmt.Errorf("agent fake: %w", callCtx.Err())
			}}

			var events []distill.Event

			x, err := RunDistill(ctx, ws, nil, p, agent, func(ev distill.Event) {
				events = append(events, ev)

				if !tc.inCall && ev.Kind == distill.EventGroupStarted {
					cancel()
				}
			})
			require.NoError(t, err, "a stop is data, not an error")
			require.NotNil(t, x)

			assert.Equal(t, distill.StopCancelled, x.Stop)
			require.ErrorIs(t, x.StopErr, context.Canceled)
			assert.Equal(t, distill.GroupCancelled, x.Groups[0].Status)

			if tc.inCall {
				assert.Len(t, agent.called(), 1)
			} else {
				assert.Empty(t, agent.called(), "no call starts after the stop")
			}

			require.NotEmpty(t, events)

			last := events[len(events)-1]
			assert.Equal(t, distill.EventStopped, last.Kind)
			assert.Equal(t, 1, last.Index, "no planned group is left, so the index is the total")
			assert.Equal(t, 1, last.Total)
			assert.Equal(t, distill.StopCancelled, last.Stop)

			assert.Empty(t, distMarks(t, ws), "a cancelled group is not recorded")
			assert.NoFileExists(t, marker, "the fake invoker replaces the configured command")
		})
	}
}

// Only Codex pins the agent's working directory to the root. The
// default Claude preset and a custom agent.argv leave Dir empty, so the
// process runs in the console's working directory. A surface that
// discloses the directory depends on this contract.
func TestDistillPreviewInvocationDirFollowsTheCommand(t *testing.T) {
	cases := []struct {
		name, config, command string
		// atRoot says the preset sets Dir to the recorded root.
		atRoot bool
	}{
		{"default preset", "", "claude", false},
		{"custom argv", "agent:\n  argv: [\"sh\", \"-c\", \"cat >/dev/null\"]\n", "custom", false},
		{"codex preset", "agent:\n  cli: codex\n", "codex", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, _ := distWorkspace(t, distAreaFindings("a"))
			distWrite(t, ws, ".seamark/config.yaml", tc.config)

			p := distPlan(t, ws, DistillRequest{})
			assert.Equal(t, tc.command, p.Invocation.Name)

			if tc.atRoot {
				assert.Equal(t, p.RepoRoot, p.Invocation.Dir)
			} else {
				assert.Empty(t, p.Invocation.Dir, "the process inherits the console's working directory")
			}
		})
	}
}

func TestRunDistillRunsThePreviewedCommand(t *testing.T) {
	ws, marker := distWorkspace(t, distAreaFindings("a"))
	p := distPlan(t, ws, DistillRequest{})

	// No fake: the run builds the invoker from the previewed command.
	x, err := RunDistill(context.Background(), ws, integration.Builtin(), p, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, x)
	assert.Equal(t, distill.GroupCompleted, x.Groups[0].Status)
	assert.FileExists(t, marker, "the previewed command ran")
	assert.Len(t, distMarks(t, ws), 1)
}
