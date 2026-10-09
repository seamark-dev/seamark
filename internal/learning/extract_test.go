package learning

import (
	"context"
	"encoding/json"
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
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// extFindings are living findings at paths that extWorkspace creates, so
// an answer that names them passes the existence rung.
var extFindings = []model.Finding{
	{ID: 1, Path: "api/handlers.go", PR: 1, Reviewer: "person", Body: "EXT-BODY-ONE"},
	{ID: 2, Path: "web/schema.ts", PR: 2, Reviewer: "person", Body: "EXT-BODY-TWO"},
}

// extMarkerArgv is an extraction agent command that leaves a marker file
// and answers every item with a negative answer. A preview must never
// run it.
func extMarkerArgv(marker string) string {
	return fmt.Sprintf("agent:\n  argv: [\"sh\", \"-c\", \"touch %s; cat >/dev/null; echo '{\\\"triggers\\\":[]}'\"]\n",
		marker)
}

// extWorkspace builds an indexed workspace with the extraction findings,
// their files, and an agent command that leaves the returned marker.
func extWorkspace(t *testing.T) (ws workspace.Workspace, marker string) {
	t.Helper()

	ws, _ = distWorkspace(t, extFindings)

	for _, f := range extFindings {
		distWrite(t, ws, f.Path, "x\n")
	}

	marker = filepath.Join(t.TempDir(), "agent-ran")
	distWrite(t, ws, ".seamark/config.yaml", extMarkerArgv(marker))

	return ws, marker
}

// extPending is an unanswered pending row scoped to region.
func extPending(rule, region string, members ...int64) model.Proposal {
	return model.Proposal{
		Signature: "sig-" + rule, Rule: rule, Region: region, Note: "Note for " + rule + ".",
		Members: members, Status: model.ProposalProposed,
	}
}

// extApplied is an unanswered applied row scoped to region.
func extApplied(rule, region string, members ...int64) model.Proposal {
	p := extPending(rule, region, members...)
	p.Status = model.ProposalApplied

	return p
}

// extSeed stores rows and returns them as the store reads them back,
// with their IDs.
func extSeed(t *testing.T, ws workspace.Workspace, ps ...model.Proposal) []model.Proposal {
	t.Helper()

	// distSeed sets the IDs in place, because it inserts &ps[i].
	distSeed(t, ws, ps...)

	out := make([]model.Proposal, len(ps))
	for i := range ps {
		out[i] = extRow(t, ws, ps[i].ID)
	}

	return out
}

// extPin renders one pin entry of lessons.yaml.
func extPin(rule, region, note string) string {
	return fmt.Sprintf("  - rule: %s\n    region: %s\n    note: %s\n", rule, region, note)
}

// extPins writes lessons.yaml with the given entries.
func extPins(t *testing.T, ws workspace.Workspace, entries ...string) {
	t.Helper()

	distWrite(t, ws, ".seamark/lessons.yaml", "pin:\n"+strings.Join(entries, ""))
}

// extRow reads one row in any status.
func extRow(t *testing.T, ws workspace.Workspace, id int64) model.Proposal {
	t.Helper()

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	rows, err := st.ProposalsWithIDs([]int64{id})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// extStamped reports whether row id carries a trigger stamp.
func extStamped(t *testing.T, ws workspace.Workspace, id int64) bool {
	t.Helper()

	return extRow(t, ws, id).TriggerChecked != 0
}

// extPlan previews req and requires success.
func extPlan(t *testing.T, ws workspace.Workspace, req ExtractRequest) *ExtractPreview {
	t.Helper()

	p, err := PlanExtract(ws, integration.Builtin(), req)
	require.NoError(t, err)

	return p
}

// extIDs returns the IDs of rows.
func extIDs(rows ...model.Proposal) []int64 {
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}

	return ids
}

// extAgent is a scripted extraction invoker. It sees the call context,
// the 1-based call number, and the item IDs of the prompt. Without fn it
// answers every item with a negative answer.
type extAgent struct {
	mu      sync.Mutex
	prompts []string
	fn      func(ctx context.Context, call int, ids []int64) (string, error)
}

func (a *extAgent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	call := len(a.prompts)
	a.mu.Unlock()

	ids := extItemIDs(prompt)

	if a.fn == nil {
		return extNegative(ids), nil
	}

	return a.fn(ctx, call, ids)
}

func (a *extAgent) Name() string { return "fake" }

// sent returns the received prompts.
func (a *extAgent) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]string(nil), a.prompts...)
}

var extItemPattern = regexp.MustCompile(`--- item id=(\d+) `)

// extItemIDs reads the item IDs of an extraction prompt.
func extItemIDs(prompt string) []int64 {
	var ids []int64

	for _, m := range extItemPattern.FindAllStringSubmatch(prompt, -1) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		ids = append(ids, id)
	}

	return ids
}

// extReply answers each ID with its paths; a nil list is a negative
// answer.
func extReply(paths map[int64][]string, ids []int64) string {
	answers := make([]map[string]any, 0, len(ids))

	for _, id := range ids {
		named := paths[id]
		if named == nil {
			named = []string{}
		}

		answers = append(answers, map[string]any{"id": id, "trigger_paths": named})
	}

	data, _ := json.Marshal(map[string]any{"triggers": answers})

	return string(data)
}

// extNegative answers every ID with an explicit empty list.
func extNegative(ids []int64) string {
	return extReply(nil, ids)
}

// extSetGetwd replaces the working-directory reader for one test.
func extSetGetwd(t *testing.T, fn func() (string, error)) {
	t.Helper()

	saved := extGetwd
	extGetwd = fn

	t.Cleanup(func() { extGetwd = saved })
}

func TestPlanExtractWritesNothingAndRunsNoAgent(t *testing.T) {
	ws, marker := extWorkspace(t)
	rows := extSeed(t, ws, extPending("pend-rule", "api", 1), extApplied("live-rule", "web", 2))
	extPins(t, ws, extPin("live-rule", "web", "Note for live-rule."))

	dbBefore := distFileDigest(t, ws.DBPath)
	configBefore := distFileDigest(t, filepath.Join(ws.Root, ".seamark", "config.yaml"))
	lessonsBefore := distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml"))

	req := ExtractRequest{IDs: extIDs(rows...)}
	p := extPlan(t, ws, req)

	assert.Equal(t, dbBefore, distFileDigest(t, ws.DBPath), "planning leaves the database byte-identical")
	assert.Equal(t, configBefore, distFileDigest(t, filepath.Join(ws.Root, ".seamark", "config.yaml")))
	assert.Equal(t, lessonsBefore, distFileDigest(t, filepath.Join(ws.Root, ".seamark", "lessons.yaml")))
	assert.NoFileExists(t, marker, "a preview never runs the agent")

	for _, row := range rows {
		assert.False(t, extStamped(t, ws, row.ID), "a preview never stamps p%d", row.ID)
	}

	require.Len(t, p.Plan.Batches, 1)
	assert.Equal(t, extIDs(rows...), p.Plan.IDs(), "pending rows first, then applied rows")
	assert.NotContains(t, p.Plan.Batches[0].Prompt, "EXT-BODY", "no finding text is sent")
	assert.Zero(t, p.Plan.Preflight.BodyCap)
	assert.Equal(t, workspace.Probe(ws).Root, p.Plan.Options.Root)

	cwd, err := os.Getwd()
	require.NoError(t, err)

	assert.Equal(t, "custom", p.Invocation.Name)
	assert.Equal(t, "sh", p.Invocation.Argv[0])
	assert.Equal(t, p.Invocation.Argv, p.Plan.Preflight.Agent)
	assert.Equal(t, cwd, p.WorkDir, "an empty Dir runs the agent in the console's directory")
	assert.Equal(t, ws.DBPath, p.DBPath)
	assert.Equal(t, workspace.Probe(ws).Root, p.RepoRoot)
	assert.True(t, p.Config.Exists)
	assert.True(t, p.Lessons.Exists)
	assert.Empty(t, p.Blockers)
	assert.Nil(t, p.LiveNotes)
	assert.Empty(t, p.Ineligible())
	assert.False(t, p.NoOp())
	assert.Equal(t, p.Plan.PlannedAt, p.PlannedAt)
	assert.False(t, p.PlannedAt.IsZero())

	// The preview owns its request, so a later edit by the caller cannot
	// change what the user reviewed.
	req.IDs[0] = 999
	assert.Equal(t, rows[0].ID, p.Request.IDs[0])
}

// Every console exclusion of D-17 names its reason. The link exclusions
// come from pinfile.Classify, the status, answer, and evidence exclusions
// from the shared plan rules, and the disagreeing region fields of a row
// in any status from pinfile.FieldsAgree.
func TestPlanExtractExcludesWhatThePinFileDoesNotProve(t *testing.T) {
	ws, marker := extWorkspace(t)

	stamped := extApplied("answered", "api", 1)
	stamped.TriggerPaths, stamped.TriggerChecked = []string{"api/handlers.go"}, 1
	stamped.TriggerPromptVersion = distill.TriggerPromptVersion

	fieldsApplied := extApplied("fields-applied", "web", 1)
	fieldsApplied.Regions = []string{"api"}

	fieldsPending := extPending("fields-pending", "web", 1)
	fieldsPending.Regions = []string{"api"}

	dismissed := extPending("dismissed", "api", 1)
	dismissed.Status = model.ProposalDismissed

	superseded := extApplied("superseded", "api", 1)
	superseded.Status = model.ProposalSuperseded

	ledgerTwin := extApplied("dup-ledger", "api", 1)
	ledgerTwin.Signature = "sig-dup-ledger-2"

	rows := extSeed(t, ws,
		extApplied("linked", "api", 1),     // 0 eligible
		extApplied("gone", "api", 1),       // 1 pin not present
		extApplied("dup-file", "api", 1),   // 2 duplicate pins
		extApplied("dup-ledger", "api", 1), // 3 several applied rows
		ledgerTwin,                         // 4 several applied rows
		fieldsApplied,                      // 5 fields disagree
		extPending("carried", "api", 1),    // 6 a pin already carries it
		fieldsPending,                      // 7 fields disagree
		extPending("open", "api", 1),       // 8 eligible
		dismissed,                          // 9
		superseded,                         // 10
		stamped,                            // 11 answered with paths
		extPending("aged", "api", 99),      // 12 no living evidence
	)

	extPins(t, ws,
		extPin("linked", "api", "Note for linked."),
		extPin("dup-file", "api", "First copy."),
		extPin("dup-file", "api", "Second copy."),
		extPin("dup-ledger", "api", "Note for dup-ledger."),
		extPin("fields-applied", "api", "Note for fields-applied."),
		extPin("carried", "api", "Note for carried."),
		extPin("answered", "api", "Note for answered."),
	)

	skipped := distill.ExtractSkip{ID: 0, Reason: "no linked proposal"}
	ids := append(extIDs(rows...), 9999)

	p := extPlan(t, ws, ExtractRequest{IDs: ids, Skipped: []distill.ExtractSkip{skipped}})

	assert.Equal(t, []int64{rows[8].ID, rows[0].ID}, p.Plan.IDs(), "only the proven rows are asked")

	fieldsReason := pinfile.LinkAmbiguousFields.Reason()

	want := []distill.ExtractSkip{
		skipped,
		{ID: rows[1].ID, Reason: "pin not present"},
		{ID: rows[2].ID, Reason: "ambiguous: duplicate pins"},
		{ID: rows[3].ID, Reason: "ambiguous: several applied proposals"},
		{ID: rows[4].ID, Reason: "ambiguous: several applied proposals"},
		{ID: rows[5].ID, Reason: fieldsReason},
		{ID: rows[6].ID, Reason: "a pin already carries it; record it as applied first (Proposals › c)"},
		{ID: rows[7].ID, Reason: fieldsReason},
		{ID: rows[9].ID, Reason: model.ProposalDismissed},
		{ID: rows[10].ID, Reason: model.ProposalSuperseded},
		{ID: rows[11].ID, Reason: distill.TriggerAnsweredPositive.String()},
		{ID: rows[12].ID, Reason: "no living evidence: every cited finding aged out of the index"},
		{ID: 9999, Reason: "not found in the ledger"},
	}
	assert.Equal(t, want, p.Ineligible(), "the skipped Pins entries first, then the plan's reasons by ID")
	assert.Equal(t, []distill.ExtractSkip{skipped}, p.Request.Skipped)
	assert.Equal(t, "ambiguous: stored region fields disagree", fieldsReason)
	assert.NoFileExists(t, marker)
}

func TestPlanExtractEmptySelectionNeverRuns(t *testing.T) {
	cases := []struct {
		name string
		req  func(rows []model.Proposal) ExtractRequest
	}{
		{"no selection", func([]model.Proposal) ExtractRequest { return ExtractRequest{} }},
		{"only ineligible rows", func(rows []model.Proposal) ExtractRequest {
			return ExtractRequest{IDs: []int64{rows[1].ID, 9999}}
		}},
		{"only skipped Pins entries", func(rows []model.Proposal) ExtractRequest {
			return ExtractRequest{IDs: []int64{rows[0].ID}, Skipped: []distill.ExtractSkip{
				{ID: rows[0].ID, Reason: "ambiguous: duplicate pins"},
				{Reason: "no linked proposal"},
			}}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := extWorkspace(t)

			dismissed := extPending("dismissed", "api", 1)
			dismissed.Status = model.ProposalDismissed

			rows := extSeed(t, ws, extPending("open", "api", 1), dismissed)

			p := extPlan(t, ws, tc.req(rows))
			assert.True(t, p.NoOp())
			assert.Empty(t, p.Plan.Batches, "an empty selection never means all")
			assert.Zero(t, p.Plan.Preflight.PromptChars)

			agent := &extAgent{}

			x, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
			require.ErrorIs(t, err, ErrNothingToExtract)
			assert.Nil(t, x)
			assert.Empty(t, agent.sent(), "no eligible proposal, no agent call")
			assert.NoFileExists(t, marker)
			assert.False(t, extStamped(t, ws, rows[0].ID))
		})
	}
}

func TestPlanExtractDoesNotAskSkippedIDs(t *testing.T) {
	ws, _ := extWorkspace(t)
	rows := extSeed(t, ws, extPending("first", "api", 1), extPending("second", "api", 1))

	skipped := []distill.ExtractSkip{
		{ID: rows[1].ID, Reason: "ambiguous: duplicate pins"},
		{Reason: "no linked proposal"},
	}

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...), Skipped: skipped})

	assert.Equal(t, []int64{rows[0].ID}, p.Plan.IDs(), "the selecting surface already found it ineligible")
	assert.Equal(t, []int64{rows[0].ID}, p.Plan.Options.IDs)
	assert.Equal(t, skipped, p.Ineligible(), "each skipped item is listed once")
	assert.Equal(t, extIDs(rows...), p.Request.IDs, "the request stays as the user made it")

	// The preview owns its skipped list too.
	skipped[0].Reason = "edited"
	assert.Equal(t, "ambiguous: duplicate pins", p.Ineligible()[0].Reason)
}

func TestPlanExtractDisclosesTheLiveNote(t *testing.T) {
	ws, _ := extWorkspace(t)

	answered := extApplied("answered", "api", 1)
	answered.TriggerPaths, answered.TriggerChecked = []string{"api/handlers.go"}, 1
	answered.TriggerPromptVersion = distill.TriggerPromptVersion

	rows := extSeed(t, ws, extApplied("edited", "api", 1), extApplied("same", "web", 2), answered)
	extPins(t, ws,
		extPin("edited", "api", "Live wording of the edited pin."),
		extPin("same", "web", "Note for same."),
		extPin("answered", "api", "Another live wording."),
	)

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Equal(t, []int64{rows[1].ID, rows[0].ID}, p.Plan.IDs())

	assert.Equal(t, map[int64]string{rows[0].ID: "Live wording of the edited pin."}, p.LiveNotes,
		"only a planned row whose live note differs")

	prompt := p.Plan.Batches[0].Prompt
	assert.Contains(t, prompt, "note: Note for edited.", "the question sends the recorded note")
	assert.NotContains(t, prompt, "Live wording", "the live note is disclosed, never sent")
}

func TestPlanExtractBlocksOnConfigurationErrors(t *testing.T) {
	cases := []struct {
		name, file, content, path, text string
		is                              error
	}{
		{"malformed config", ".seamark/config.yaml", "agent: [\n", ".seamark/config.yaml", "agent config:", nil},
		{"unknown agent", ".seamark/config.yaml", "agent:\n  cli: hal9000\n", ".seamark/config.yaml", "hal9000", nil},
		{"malformed pins", ".seamark/lessons.yaml", "pin: [\n", ".seamark/lessons.yaml", "lessons config:",
			reviews.ErrLessonsConfig},
		{"pins of the wrong type", ".seamark/lessons.yaml", "pin:\n  - rule: [a]\n", ".seamark/lessons.yaml",
			"lessons config:", reviews.ErrLessonsConfig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, marker := extWorkspace(t)
			rows := extSeed(t, ws, extPending("open", "api", 1))
			distWrite(t, ws, tc.file, tc.content)

			p, err := PlanExtract(ws, integration.Builtin(), ExtractRequest{IDs: extIDs(rows...)})
			require.ErrorIs(t, err, ErrExtractConfig)
			assert.Nil(t, p, "a paid run never falls back to defaults")

			var cfgErr *ExtractConfigError
			require.ErrorAs(t, err, &cfgErr)
			assert.Equal(t, tc.path, cfgErr.Path, "the error names the file to repair")
			assert.Contains(t, err.Error(), tc.text, "the reader error is shown as is")
			assert.True(t, strings.HasPrefix(err.Error(), tc.path+": "))

			if tc.is != nil {
				require.ErrorIs(t, err, tc.is)
			}

			assert.NoFileExists(t, marker)
		})
	}
}

func TestPlanExtractNeedsAReadyIndex(t *testing.T) {
	root := t.TempDir()

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = PlanExtract(ws, nil, ExtractRequest{IDs: []int64{1}})
	require.ErrorIs(t, err, ErrExtractNotReady)
	assert.Contains(t, err.Error(), "build the index first")
	assert.NoFileExists(t, ws.DBPath, "a preview never creates the database")

	// A database with decisions but no index run is not ready either.
	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	_, err = PlanExtract(ws, nil, ExtractRequest{IDs: []int64{1}})
	require.ErrorIs(t, err, ErrExtractNotReady)
}

func TestPlanExtractBlocksAMissingAgentAndRunRefusesIt(t *testing.T) {
	ws, _ := extWorkspace(t)
	rows := extSeed(t, ws, extPending("open", "api", 1))
	distWrite(t, ws, ".seamark/config.yaml", "agent:\n  argv: [\"no-such-agent-binary-for-tests\"]\n")

	p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
	require.Len(t, p.Blockers, 1)
	assert.Contains(t, p.Blockers[0], `agent cli "no-such-agent-binary-for-tests" not found on PATH`)
	assert.Len(t, p.Plan.Batches, 1, "a blocked preview still discloses the plan")

	agent := &extAgent{}

	x, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
	require.ErrorIs(t, err, ErrExtractBlocked)
	assert.Contains(t, err.Error(), "no-such-agent-binary-for-tests")
	assert.Nil(t, x)
	assert.Empty(t, agent.sent())
	assert.False(t, extStamped(t, ws, rows[0].ID))
}

// Only Codex pins the agent's directory to the root. Otherwise the agent
// runs in the console's directory, and the preview must name it.
func TestExtractPreviewWorkDirFollowsTheCommand(t *testing.T) {
	t.Run("custom argv", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		extSetGetwd(t, func() (string, error) { return "/console/dir", nil })

		p := extPlan(t, ws, ExtractRequest{})
		assert.Empty(t, p.Invocation.Dir)
		assert.Equal(t, "/console/dir", p.WorkDir)
		assert.Empty(t, p.Blockers)
		assert.NoError(t, p.WorkDirErr)
	})

	t.Run("codex preset", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		distWrite(t, ws, ".seamark/config.yaml", "agent:\n  cli: codex\n")
		extSetGetwd(t, func() (string, error) { return "/console/dir", nil })

		p := extPlan(t, ws, ExtractRequest{})
		assert.Equal(t, "codex", p.Invocation.Name)
		assert.Equal(t, p.RepoRoot, p.Invocation.Dir)
		assert.Equal(t, p.RepoRoot, p.WorkDir, "the preset's directory, not the console's")
	})

	t.Run("unknown directory", func(t *testing.T) {
		ws, _ := extWorkspace(t)
		rows := extSeed(t, ws, extPending("open", "api", 1))
		extSetGetwd(t, func() (string, error) { return "", errors.New("getwd: \x1b[31mgone") })

		p := extPlan(t, ws, ExtractRequest{IDs: extIDs(rows...)})
		assert.Empty(t, p.WorkDir)
		require.Len(t, p.Blockers, 1, "an undisclosed directory blocks the run")
		assert.Contains(t, p.Blockers[0], "the agent's working directory is unknown")
		assert.NotContains(t, p.Blockers[0], "\x1b", "a blocker is a safe display line")

		// The typed signal marks the line, so a surface never parses it.
		require.Error(t, p.WorkDirErr)
		assert.True(t, p.IsWorkDirBlocker(p.Blockers[0]))
		assert.False(t, p.IsWorkDirBlocker(`agent cli "claude" not found on PATH`))

		same := *p
		same.WorkDirErr = nil
		assert.False(t, same.IsWorkDirBlocker(p.Blockers[0]), "the text alone never marks a blocker")

		agent := &extAgent{}

		_, err := RunExtract(context.Background(), ws, nil, p, agent, nil)
		require.ErrorIs(t, err, ErrExtractBlocked)
		assert.Empty(t, agent.sent())
	})
}

// A test of the read helpers: an unreadable pin file never makes a row
// look absent, so it never lets a row through either.
func TestFieldsDisagreeMatchesClassify(t *testing.T) {
	// Classify is the oracle: an applied row whose pin is the only entry
	// is ambiguous exactly when its fields disagree. A pending row with
	// the same fields must get the same verdict from the extraction.
	cases := []struct {
		name     string
		region   string
		regions  []string
		disagree bool
	}{
		{name: "single region", region: "api"},
		{name: "repo-wide", region: ""},
		{name: "trailing slash", region: "api/"},
		{name: "set holds the region", region: "api", regions: []string{"api", "web"}},
		{name: "set without a region", regions: []string{"web", "api"}},
		{name: "region outside the set", region: "web", regions: []string{"api"}, disagree: true},
		{name: "wildcard beside a real region", region: "api", regions: []string{"*", "api"}, disagree: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applied := extApplied("fields", tc.region, 1)
			applied.ID, applied.Regions = 1, tc.regions

			pending := applied
			pending.ID, pending.Status = 2, model.ProposalProposed

			doc := &pinfile.Document{Entries: []pinfile.Entry{{Rule: "fields", Key: reviews.ProposalKey(applied)}}}
			links := pinfile.Classify(doc, []model.Proposal{applied}, nil, nil)
			oracle := links.ByProposal[applied.ID] == pinfile.LinkAmbiguousFields

			assert.Equal(t, tc.disagree, oracle, "the table describes the pinfile rule")
			assert.Equal(t, oracle, !pinfile.FieldsAgree(applied), "the predicate and Classify agree")

			v := &pinView{doc: doc, links: links, rows: map[int64]model.Proposal{applied.ID: applied}}

			reason, excluded := v.exclusion(applied)
			assert.Equal(t, oracle, excluded, "an applied row is excluded only as Classify says")

			if oracle {
				assert.Equal(t, links.ByProposal[applied.ID].Reason(), reason)
			}

			v.links = pinfile.Classify(&pinfile.Document{}, nil, []model.Proposal{pending}, nil)

			reason, excluded = v.exclusion(pending)
			assert.Equal(t, oracle, excluded, "a pending row follows the same fields rule")

			if oracle {
				assert.Equal(t, pinfile.LinkAmbiguousFields.Reason(), reason)
			}
		})
	}
}

func TestPinViewNeverTreatsAnUnreadableFileAsAbsence(t *testing.T) {
	applied := extApplied("installed", "api", 1)
	applied.ID = 1

	pending := extPending("waiting", "api", 1)
	pending.ID = 2

	for name, doc := range map[string]*pinfile.Document{
		"no document":  nil,
		"syntax error": pinfile.Parse([]byte("pin: [\n")),
		"reader error": pinfile.Parse([]byte("pin:\n  - rule: [a]\n")),
	} {
		t.Run(name, func(t *testing.T) {
			v := &pinView{
				doc:   doc,
				links: pinfile.Classify(doc, []model.Proposal{applied}, []model.Proposal{pending}, nil),
				rows:  map[int64]model.Proposal{applied.ID: applied, pending.ID: pending},
			}

			for _, row := range []model.Proposal{applied, pending} {
				assert.True(t, v.current(row))

				reason, excluded := v.exclusion(row)
				assert.True(t, excluded, "p%d", row.ID)
				assert.Equal(t, "pin file unreadable", reason)
			}

			assert.Nil(t, v.liveNotes([]int64{applied.ID, pending.ID}))
		})
	}
}

func TestPinViewCurrentNeedsTheSameGuardedRow(t *testing.T) {
	row := extApplied("installed", "api", 1)
	row.ID = 7

	moved := row
	moved.Regions = []string{"web"}

	dismissed := row
	dismissed.Status = model.ProposalDismissed

	noted := row
	noted.Note, noted.TriggerChecked = "Another note.", 5

	cases := []struct {
		name string
		rows map[int64]model.Proposal
		want bool
	}{
		{"same row", map[int64]model.Proposal{7: row}, true},
		{"note and stamp are not guarded", map[int64]model.Proposal{7: noted}, true},
		{"regions changed", map[int64]model.Proposal{7: moved}, false},
		{"status changed", map[int64]model.Proposal{7: dismissed}, false},
		{"row gone", map[int64]model.Proposal{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &pinView{rows: tc.rows}
			assert.Equal(t, tc.want, v.current(row))
		})
	}
}

func TestExtractConfigErrorUnwrapsBothCauses(t *testing.T) {
	cause := fmt.Errorf("%w: bad", reviews.ErrLessonsConfig)
	err := error(&ExtractConfigError{Path: ".seamark/lessons.yaml", Err: cause})

	assert.Equal(t, ".seamark/lessons.yaml: lessons config: bad", err.Error())
	require.ErrorIs(t, err, ErrExtractConfig)
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)
	assert.NotErrorIs(t, err, ErrDistillConfig, "each preview kind has its own sentinel")
}

func TestWithoutSkippedKeepsTheSelectionOrder(t *testing.T) {
	got := withoutSkipped([]int64{5, 3, 9, 3}, []distill.ExtractSkip{{ID: 9}, {ID: 0}, {ID: 42}})
	assert.Equal(t, []int64{5, 3, 3}, got, "the plan removes repeats itself")
	assert.Nil(t, withoutSkipped(nil, nil))
}
