package distill

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// areaFindings builds two findings per directory. Every body holds words
// that no other finding shares, so the grouper makes exactly one
// directory group per directory. Directory i gets IDs 21+2i and 22+2i,
// each with its own PR, so two members are two events.
func areaFindings(dirs ...string) []model.Finding {
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

// promptID reads the first finding ID of a prompt, which names the group.
var promptID = regexp.MustCompile(`finding id=(\d+) `)

func firstID(prompt string) int64 {
	m := promptID.FindStringSubmatch(prompt)
	if m == nil {
		return 0
	}

	id, _ := strconv.ParseInt(m[1], 10, 64)

	return id
}

// patternReply answers with one valid pattern that cites ids.
func patternReply(rule, note string, ids ...int64) string {
	list := ""

	for i, id := range ids {
		if i > 0 {
			list += ","
		}

		list += strconv.FormatInt(id, 10)
	}

	return fmt.Sprintf(`{"patterns":[{"rule":%q,"note":%q,"finding_ids":[%s],"trigger_paths":[]}]}`, rule, note, list)
}

// openFileStore opens a writable store at a fixed path, so a test can
// reopen it read-only or hash its bytes.
func openFileStore(t *testing.T, findings []model.Finding) (st *store.Store, path string) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "index.db")

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, findings))

	return st, path
}

// fileDigest hashes a file's bytes.
func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	return sha256.Sum256(data)
}

func TestNewPlanWritesNothingAndWorksReadOnly(t *testing.T) {
	st, path := openFileStore(t, threeAreas)
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "gone", Rule: "stale-pending", Note: "Stale guidance.",
		Members: []int64{11, 12}, Status: model.ProposalProposed,
	}))
	require.NoError(t, st.Close())

	before := fileDigest(t, path)

	ro, err := store.OpenReadOnly(path)
	require.NoError(t, err)

	p, err := NewPlan(ro, NewLexicalGrouper(), PlanOptions{
		Limit: 1, Spec: agent.CommandSpec{Name: "fake", Argv: []string{"fake", "-p"}},
	})
	require.NoError(t, err)
	require.NoError(t, ro.Close())

	assert.Equal(t, before, fileDigest(t, path), "planning leaves the database byte-identical")
	require.Len(t, p.Cleanup, 1, "the cleanup is listed, not done")
	assert.Equal(t, "stale-pending", p.Cleanup[0].Rule)
	assert.Equal(t, []string{"fake", "-p"}, p.Preflight.Agent)
	assert.False(t, p.PlannedAt.IsZero())
}

func TestNewPlanMatchesTheRealRunSelection(t *testing.T) {
	// The stale proposal cites api/x. The plan models its removal, so it
	// selects what a real run sends (api/x). A plan that kept the stale
	// row would select web instead.
	st := openSeeded(t, []model.Finding{threeAreas[0], threeAreas[1], threeAreas[4], threeAreas[5]})
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "gone", Rule: "stale-pending", Note: "Stale guidance.",
		Members: []int64{11, 12}, Status: model.ProposalProposed,
	}))

	p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Limit: 1, Pins: threeAreasPin})
	require.NoError(t, err)
	require.Len(t, p.Groups, 1)
	assert.Equal(t, "api/x", p.Groups[0].Group.Region)

	var sent Preflight

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns":[]}`, nil }}

	_, err = Run(context.Background(), st, NewLexicalGrouper(), empty, Options{
		Limit: 1, Pins: threeAreasPin, OnPreflight: func(pf Preflight) { sent = pf },
	})
	require.NoError(t, err)
	assert.Equal(t, sent, p.Preflight, "the plan discloses exactly what the real run sends")
}

func TestNewPlanRejectsANegativeLimit(t *testing.T) {
	st := openSeeded(t, threeAreas)

	_, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Limit: -1})
	require.ErrorIs(t, err, ErrInvalidLimit)
}

func TestNewPlanReportsSkippedDeferredAndCorpus(t *testing.T) {
	st := openSeeded(t, append(areaFindings("a", "b", "c"), model.Finding{
		ID: 99, Path: "lonely/x.go", Body: "Solitary words nobody repeats.",
	}))

	all, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{})
	require.NoError(t, err)
	require.Len(t, all.Groups, 3)

	// Mark the first group as read, then plan one group only.
	require.NoError(t, storetest.MarkDistilled(st, all.Groups[0].Group.Signature, "", 1))

	p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Limit: 1})
	require.NoError(t, err)

	assert.Equal(t, 3, p.Total)
	assert.Equal(t, 7, p.Corpus, "the lone finding is in scope but in no group")
	assert.Equal(t, []string{all.Groups[0].Group.Signature}, p.Skipped)
	require.Len(t, p.Groups, 1)
	assert.Equal(t, all.Groups[1].Group.Signature, p.Groups[0].Group.Signature)
	require.Len(t, p.Deferred, 1)

	deferred := p.Deferred[0]
	assert.Equal(t, all.Groups[2].Group.Signature, deferred.Signature)
	assert.Equal(t, 2, deferred.Findings)
	assert.Positive(t, deferred.BodyCap)
	assert.Zero(t, deferred.PromptChars, "a deferred prompt is not built")
	assert.Empty(t, deferred.Evidence)

	require.Len(t, p.Preflight.Groups, 1, "the preflight is parallel to Groups")
	assert.Equal(t, p.Groups[0].Group.Signature, p.Preflight.Groups[0].Signature)
}

func TestNewPlanRegionalCleanupMatchesTheRegionalPrune(t *testing.T) {
	st := openSeeded(t, []model.Finding{
		{ID: 1, Path: "api/a.go", Body: "Guard optional handler state before access."},
		{ID: 2, Path: "api/b.go", Body: "Wrap worker failures with operation context."},
	})

	// Evidence outside the corpus keeps a proposal; evidence wholly
	// inside it with a vanished group does not.
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "partial", Rule: "partial-rule", Note: "n", Members: []int64{1, 90},
		Status: model.ProposalProposed,
	}))
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "gone", Rule: "stale-rule", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalProposed,
	}))

	p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Region: "api"})
	require.NoError(t, err)
	require.Len(t, p.Cleanup, 1)
	assert.Equal(t, "stale-rule", p.Cleanup[0].Rule)

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns":[]}`, nil }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{Region: "api"})
	require.NoError(t, err)
	assert.Equal(t, len(p.Cleanup), res.PrunedStale)
}

func TestPlanRevalidateNamesEachChangedInput(t *testing.T) {
	findings := areaFindings("api", "web")
	st := openSeeded(t, findings)

	plan := func(region string) *Plan {
		p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{Region: region})
		require.NoError(t, err)
		require.NoError(t, p.Revalidate(st), "an unchanged store is current")

		return p
	}

	// A changed body keeps the signature but changes what is sent.
	p := plan("")
	edited := append([]model.Finding(nil), findings...)
	edited[0].Body = "Uniq21alpha uniq21bravo uniq21charlie, edited."
	require.NoError(t, st.ReplaceLessons(nil, edited))

	err := p.Revalidate(st)
	require.ErrorIs(t, err, ErrStale)
	assert.Contains(t, err.Error(), "the findings in scope changed")

	// A change outside the region does not touch a regional plan.
	web := plan("web")

	edited[0].Body = "Uniq21alpha uniq21bravo uniq21charlie, edited twice."
	require.NoError(t, st.ReplaceLessons(nil, edited))
	require.NoError(t, web.Revalidate(st))

	// A decision or a new proposal changes the ledger. The seed also
	// records the fixture mark. That mark exists before the plan, so the
	// seed changes the ledger only, and the error names no other input.
	require.NoError(t, storetest.MarkDistilled(st, storetest.FixtureMark, "", 0))

	p = plan("")
	require.NoError(t, storetest.InsertProposal(st, &model.Proposal{
		Signature: "other", Rule: "new-rule", Note: "n", Members: []int64{21, 22},
		Status: model.ProposalDismissed,
	}))

	err = p.Revalidate(st)
	require.ErrorIs(t, err, ErrStale)
	assert.EqualError(t, err, ErrStale.Error()+": the proposal ledger changed")

	// Another writer records a group, and the corpus changes as well.
	p = plan("")
	require.NoError(t, storetest.MarkDistilled(st, p.Groups[0].Group.Signature, "", 1))
	require.NoError(t, st.ReplaceLessons(nil, findings))

	err = p.Revalidate(st)
	require.ErrorIs(t, err, ErrStale)
	assert.Contains(t, err.Error(), "the findings in scope changed; the distilled signatures changed",
		"every changed input is named")
}

func TestPlanRevalidateReportsAReadFailure(t *testing.T) {
	st, _ := openFileStore(t, threeAreas)

	p, err := NewPlan(st, NewLexicalGrouper(), PlanOptions{})
	require.NoError(t, err)
	require.NoError(t, st.Close())

	err = p.Revalidate(st)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrStale, "a read failure is not a staleness verdict")

	var nilPlan *Plan
	require.Error(t, nilPlan.Revalidate(st))
}
