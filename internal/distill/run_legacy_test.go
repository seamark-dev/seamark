package distill

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// These tests characterize the legacy Run callbacks before Run became a
// wrapper over Plan and Execute. The CLI prints these strings, so their
// text and order must not change.

// threeAreas gives three directory groups with disjoint vocabulary. In
// coverage order they are api/x (11, 12), web (15, 16), api/y (13, 14).
var threeAreas = []model.Finding{
	{ID: 11, Path: "api/x/a.go", PR: 1, Body: "Quokka marmalade trampoline."},
	{ID: 12, Path: "api/x/b.go", PR: 2, Body: "Zeppelin harmonica lighthouse."},
	{ID: 13, Path: "api/y/c.go", PR: 3, Body: "Cactus velvet saxophone."},
	{ID: 14, Path: "api/y/d.go", PR: 4, Body: "Glacier pumpkin origami."},
	{ID: 15, Path: "web/e.go", PR: 5, Body: "Walrus tambourine canyon."},
	{ID: 16, Path: "web/f.go", PR: 6, Body: "Nebula croissant kayak."},
}

// threeAreasReply answers each group of threeAreas differently: a pin
// restatement plus a new pattern, an agent error, or prose.
func threeAreasReply(prompt string) (string, error) {
	switch {
	case strings.Contains(prompt, "finding id=11 "):
		return `{"patterns":[` +
			`{"rule":"pooled-state-reset","note":"Reset pooled state before reuse.","finding_ids":[11,12],"trigger_paths":[]},` +
			`{"rule":"api-x-rule","note":"Keep the api x handlers aligned.","finding_ids":[11,12],"trigger_paths":[]}]}`, nil
	case strings.Contains(prompt, "finding id=15 "):
		return "", errors.New("agent fake: rate limited (exit status 1)")
	default:
		return "I could not find patterns, sorry!", nil
	}
}

// threeAreasPin is the pin the api/x reply restates.
var threeAreasPin = []model.Proposal{{Rule: "pooled-state-reset", Note: "Reset pooled state before reuse."}}

// legacySeconds matches the rounded duration in a group outcome.
var legacySeconds = regexp.MustCompile(`^\d+(m\d+)?s,`)

func TestRunCallbackTranscriptIsStable(t *testing.T) {
	st := openSeeded(t, threeAreas)

	var transcript []string

	record := func(kind string) func(string) {
		return func(text string) {
			transcript = append(transcript, kind+" "+legacySeconds.ReplaceAllString(text, "<dur>,"))
		}
	}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), &fakeAgent{fn: threeAreasReply}, Options{
		Pins:         threeAreasPin,
		OnGroupStart: record("start"),
		OnGroupDone:  record("done"),
		Logf: func(format string, args ...any) {
			record("log")(fmt.Sprintf(format, args...))
		},
	})
	require.NoError(t, err)

	// Duplicates always reach Logf, even when the callbacks drive a
	// spinner. A failure ends with the retry hint.
	assert.Equal(t, []string{
		"start 2 findings (api/x, 04c79745da3ca579)",
		"log   skipped pooled-state-reset — restates pooled-state-reset",
		"done <dur>, ~602 tokens sent / ~58 back, 1 proposal(s)",
		"start 2 findings (web, 49090c39ddcbba17)",
		"done failed: agent fake: rate limited (exit status 1) (retried next run)",
		"start 2 findings (api/y, cfd8f2c7e78a7980)",
		"done failed: reply is not the requested JSON: invalid character 'I' looking for beginning of value (retried next run)",
	}, transcript)

	assert.Equal(t, 3, res.GroupsTotal)
	assert.Equal(t, 1, res.GroupsRead)
	assert.Equal(t, 2, res.GroupsFailed)
	assert.Equal(t, 1, res.Duplicates)
	assert.Zero(t, res.GroupsPending)
	assert.Zero(t, res.GroupsSkipped)
	assert.Positive(t, res.Duration)
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, "api-x-rule", res.Proposals[0].Rule)
	assert.Positive(t, res.Proposals[0].ID, "the result carries the stored IDs")
}

func TestRunStoreFailureKeepsEarlierGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, threeAreas))

	var dones []string

	calls := 0
	agent := &fakeAgent{fn: func(string) (string, error) {
		calls++

		// The second reply arrives after the store broke, so its save
		// fails after the paid call.
		if calls == 2 {
			require.NoError(t, st.Close())
		}

		return `{"patterns":[]}`, nil
	}}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), agent, Options{
		OnGroupStart: func(string) {},
		OnGroupDone:  func(o string) { dones = append(dones, o) },
	})
	require.Error(t, err)
	require.NotNil(t, res, "the partial result rides along with the store error")

	assert.Equal(t, 1, res.GroupsRead, "the first group was saved before the failure")
	assert.Zero(t, res.GroupsFailed, "a store failure is not an agent failure")
	assert.Zero(t, res.Duration, "the duration stays unset on an error")
	assert.Positive(t, res.PromptChars, "both paid calls are metered")
	assert.Len(t, dones, 1, "a store failure reports no group outcome")
	assert.Equal(t, 2, calls, "the run stops at the store failure")

	reopened, err := store.Open(path)
	require.NoError(t, err)

	defer func() { _ = reopened.Close() }()

	marks, err := reopened.DistilledSignatures()
	require.NoError(t, err)
	assert.Len(t, marks, 1, "only the first group is marked")
}

func TestRunDryRunPlansTheGroupsOfTheRealRun(t *testing.T) {
	// A stale pending proposal cites api/x. Kept in the known patterns, it
	// would make api/x look covered and move web first. The dry run models
	// the prune instead, so both runs select api/x.
	st := openSeeded(t, []model.Finding{threeAreas[0], threeAreas[1], threeAreas[4], threeAreas[5]})

	stale := &model.Proposal{
		Signature: "gone", Rule: "stale-pending", Note: "Stale guidance.",
		Members: []int64{11, 12}, Status: model.ProposalProposed,
	}
	require.NoError(t, st.InsertProposal(stale))

	var dry Preflight

	dryRes, err := Run(context.Background(), st, NewLexicalGrouper(), nil, Options{
		DryRun: true, Limit: 1, OnPreflight: func(p Preflight) { dry = p },
	})
	require.NoError(t, err)
	require.Len(t, dry.Groups, 1)
	assert.Equal(t, "api/x", dry.Groups[0].Region, "the dry run plans without the stale row")
	assert.Equal(t, []int64{stale.ID}, dryRes.StalePending, "the dry run names the row the real run prunes")
	assert.Zero(t, dryRes.PrunedStale, "a dry run prunes nothing")

	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "a dry run prunes nothing")

	var sent Preflight

	empty := &fakeAgent{fn: func(string) (string, error) { return `{"patterns":[]}`, nil }}

	res, err := Run(context.Background(), st, NewLexicalGrouper(), empty, Options{
		Limit: 1, OnPreflight: func(p Preflight) { sent = p },
	})
	require.NoError(t, err)
	require.Len(t, sent.Groups, 1)
	assert.Equal(t, "api/x", sent.Groups[0].Region, "the real run prunes first")
	assert.Equal(t, dry, sent, "the dry run discloses exactly what the real run sends")
	assert.Equal(t, 1, res.PrunedStale)
	assert.Empty(t, res.StalePending, "only a dry run lists rows instead of pruning them")
	assert.Equal(t, 1, res.GroupsPending)
}
