package status

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

func seededStore(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	st, err := store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	run := model.Symbol{FQN: "pkg.Run", Name: "Run", Kind: model.KindFunction, File: "a.go"}
	helper := model.Symbol{FQN: "pkg.Helper", Name: "Helper", Kind: model.KindFunction, File: "b.go"}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for _, s := range []*model.Symbol{&run, &helper} {
			if err := tx.InsertSymbol(s); err != nil {
				return err
			}
		}

		if err := tx.InsertEdge(model.Edge{Src: run.ID, Dst: helper.ID,
			Kind: model.EdgeCalls, Origin: model.OriginQualified}); err != nil {
			return err
		}

		// A structural edge: must never dilute the call-confidence
		// distribution.
		if err := tx.InsertEdge(model.Edge{Src: run.ID, Dst: helper.ID,
			Kind: model.EdgeDefines, Origin: "parse"}); err != nil {
			return err
		}

		if err := tx.InsertEffect(helper.ID, "db:write", "direct", 0); err != nil {
			return err
		}

		if err := tx.InsertEffect(run.ID, "db:write", "propagated", 1); err != nil {
			return err
		}

		return tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit,
			Ref: "abc", TS: 1700000000, Title: "seed"})
	}))

	require.NoError(t, st.SetMeta("index_summary",
		`{"files_seen":10,"files_parsed":8,"files_skipped":1,"parse_errors":1}`))

	return st, root
}

func TestGatherCollectsHealth(t *testing.T) {
	st, root := seededStore(t)

	s, err := Gather(st, root)
	require.NoError(t, err)

	assert.NotEmpty(t, s.SchemaVersion, "the schema stamp is part of health")
	assert.True(t, s.Coverage.Known)
	assert.Equal(t, 8, s.Coverage.FilesParsed)
	assert.Equal(t, 1, s.Coverage.ParseErrors)

	assert.Equal(t, 2, s.Symbols)
	assert.Equal(t, map[string]int{string(model.OriginQualified): 1}, s.EdgeOrigins,
		"only CALL edges belong in the confidence distribution — DEFINES/IMPORTS have no resolution uncertainty")
	assert.Equal(t, 1, s.EffectsDirect)
	assert.Equal(t, 1, s.EffectsPropagated)
	assert.Equal(t, 1, s.History.Decisions)
	assert.Equal(t, int64(1700000000), s.History.MedianTS)

	assert.Empty(t, s.GateHookMode, "no hook installed in a bare fixture")
	assert.Equal(t, "warn", s.GatePolicyMode, "the embedded default policy is warn")
	assert.Equal(t, "claude -p", s.DistillAgent, "the default preset resolves")
}

func TestPrintSurfacesTheUncomfortableParts(t *testing.T) {
	st, root := seededStore(t)

	s, err := Gather(st, root)
	require.NoError(t, err)

	var b bytes.Buffer
	Print(&b, s)
	out := b.String()

	assert.Contains(t, out, "PARSE ERRORS", "coverage holes must shout")
	assert.Contains(t, out, "invisible to every answer")
	assert.Contains(t, out, "freshness unknown", "no fingerprint must not read as current")
	assert.Contains(t, out, "external data processing", "distillation privacy state is stated")
	assert.Contains(t, out, "no Claude hook installed")
}

func TestGatherReportsBrokenPolicy(t *testing.T) {
	st, root := seededStore(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))

	s, err := Gather(st, root)
	require.NoError(t, err, "status must describe a broken setup, not fail on it")
	assert.NotEmpty(t, s.GatePolicyError)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "POLICY BROKEN")
}

func TestGatherSanitizesAgentArgv(t *testing.T) {
	st, root := seededStore(t)

	// Repository-controlled argv with a secret and a control sequence:
	// neither may reach a terminal or MCP client verbatim.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"my-agent\", \"--token=sk-live-12345\", \"\\u001b]0;forged\\u0007\"]\n"), 0o644))

	s, err := Gather(st, root)
	require.NoError(t, err)

	assert.Contains(t, s.DistillAgent, "my-agent")
	assert.Contains(t, s.DistillAgent, "[REDACTED]")
	assert.NotContains(t, s.DistillAgent, "sk-live-12345")
	assert.NotContains(t, s.DistillAgent, "\x1b", "control sequences must be stripped")
}

func TestPrintSeparatesFixMiningFromReviews(t *testing.T) {
	st, root := seededStore(t)

	// Fix findings exist, review mining never ran: the reviews line must
	// say so instead of dressing fix findings up as review evidence.
	require.NoError(t, st.ReplaceFixFindings([]model.Finding{
		{ID: 1, Path: "a.go", Body: "fix: reset state", Source: "fix:subject"},
	}))

	s, err := Gather(st, root)
	require.NoError(t, err)
	assert.Zero(t, s.ReviewsMinedAt)

	var b bytes.Buffer
	Print(&b, s)
	out := b.String()

	assert.Contains(t, out, "reviews        never mined")
	assert.Contains(t, out, "fixes          1 findings mined from local git")
}

func TestPrintBrokenPolicyStatesHookConsequence(t *testing.T) {
	st, root := seededStore(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))

	writeHook := func(cmd string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
			[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
				`{"type":"command","command":"`+cmd+`"}]}]}}`), 0o644))
	}

	// The same broken policy means opposite things under the two hooks.
	writeHook("/bin/seamark gate --enforce --hook")
	s, err := Gather(st, root)
	require.NoError(t, err)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "FAILS CLOSED")

	writeHook("/bin/seamark gate --hook")
	s, err = Gather(st, root)
	require.NoError(t, err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "fails open")
	assert.Contains(t, b.String(), "nothing is being checked")
}

func TestGatherReportsUnreadableHookConfig(t *testing.T) {
	st, root := seededStore(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte("{not json"), 0o644))

	s, err := Gather(st, root)
	require.NoError(t, err)
	assert.NotEmpty(t, s.GateHookError)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "UNREADABLE",
		"an unreadable hook config is not the same finding as no hook")
}

func TestStatusJSONRoundTrips(t *testing.T) {
	st, root := seededStore(t)

	s, err := Gather(st, root)
	require.NoError(t, err)

	data, err := json.Marshal(s)
	require.NoError(t, err)

	var back Status
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, s.Symbols, back.Symbols)
	assert.Equal(t, s.Coverage, back.Coverage)
}
