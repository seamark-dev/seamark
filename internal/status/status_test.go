package status

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/skills"
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
	assert.Contains(t, out, "no gate hook installed")
	assert.Contains(t, out, "clients        claude  no hooks installed; MCP registration not registered")
	assert.Contains(t, out, "               codex   no hooks installed; MCP registration not registered")
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

func TestGatherResolvesTheCodexAgentThroughTheRegistry(t *testing.T) {
	st, root := seededStore(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: codex\n"), 0o644))

	s, err := Gather(st, root)
	require.NoError(t, err)
	assert.Contains(t, s.DistillAgent, "codex exec --ephemeral --sandbox read-only -C ")
	assert.Contains(t, s.DistillAgent, "features.hooks=false")

	// Unresolvable clients leave the command field empty.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: hal9000\n"), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)
	assert.Empty(t, s.DistillAgent)
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
	assert.Contains(t, out, "fixes          1 finding mined from local git")
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

	// The JSON field holds the bare reason, in the format of earlier
	// versions. The word "unreadable" belongs to the per-client detail.
	assert.True(t, strings.HasPrefix(s.GateHookError, ".claude/settings.json: invalid character"), s.GateHookError)
	assert.Empty(t, s.GateHookMode)

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
	assert.Equal(t, s, &back, "every field must survive the JSON round trip")
}

func TestGatherReportsSkills(t *testing.T) {
	st, root := seededStore(t)

	s, err := Gather(st, root)
	require.NoError(t, err)
	require.Len(t, s.Skills, 2, "both clients are reported, detected or not")
	assert.False(t, s.Skills[0].Installed())

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "skills         not installed (`seamark init --skills`)")

	targets, err := skills.Targets(root, skills.ModeClaude)
	require.NoError(t, err)
	require.NoError(t, skills.Install(&bytes.Buffer{}, root, targets, false))

	s, err = Gather(st, root)
	require.NoError(t, err)
	assert.Equal(t, 3, s.Skills[0].Current)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "skills         claude 3/3 current · codex not installed")

	// A stale managed copy must be visible beside the gate line, with
	// the refresh named: a client would load text that no longer matches
	// this binary's tool surface.
	skillMD := filepath.Join(root, ".claude", "skills", "seamark-plan-change", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD,
		[]byte("---\nname: seamark-plan-change\nmetadata:\n  seamark: managed\n---\nold body\n"), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "1 stale")
	assert.Contains(t, b.String(), "re-run seamark init --skills")

	// An unreadable client directory never fails Gather; it is described.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "skills"), []byte("oops"), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)
	assert.NotEmpty(t, s.Skills[1].Err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "codex unreadable")
}

func TestPrintNamesForeignSkillDirectories(t *testing.T) {
	st, root := seededStore(t)

	// The user's own skill under a shipped name and nothing installed:
	// a bare "not installed" would hide the collision that makes
	// `seamark init --skills` skip that directory.
	dir := filepath.Join(root, ".claude", "skills", "seamark-plan-change")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: seamark-plan-change\ndescription: mine\n---\nMine.\n"), 0o644))

	s, err := Gather(st, root)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Skills[0].Foreign)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "skills         claude not installed, 1 not managed · codex not installed")
}

func TestGatherReportsApprovals(t *testing.T) {
	st, root := seededStore(t)

	s, err := Gather(st, root)
	require.NoError(t, err)
	require.Len(t, s.Approvals, 2)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "approvals      not configured (`seamark init --approve-tools`)")

	// A registration with no approvals is still a fact the text states,
	// so status and doctor agree on what exists.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "approvals      claude not configured · codex registered as \"seamark\", 0/5 tools approved (re-run seamark init --approve-tools)")

	// The same for a Claude Code registration without rules: the hint
	// stays on the line.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mcp.json"),
		[]byte(`{"mcpServers":{"seamark":{"command":"seamark","args":["mcp"]}}}`), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "(re-run seamark init --approve-tools)")
	require.NoError(t, os.Remove(filepath.Join(root, ".mcp.json")))

	p, err := approve.PlanCodex(root)
	require.NoError(t, err)
	require.NoError(t, approve.ApplyCodex(&bytes.Buffer{}, root, p, false))

	s, err = Gather(st, root)
	require.NoError(t, err)
	assert.Equal(t, approve.StateCurrent, s.Approvals[1].State())

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "approvals      claude not configured · codex registered as \"seamark\", 5/5 tools approved")

	// A malformed Codex file never fails Gather; it is described, with
	// repository bytes sanitized before they reach a terminal.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("[\x1b[31m\n"), 0o644))

	s, err = Gather(st, root)
	require.NoError(t, err)
	assert.NotEmpty(t, s.Approvals[1].Err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "codex unreadable")
	assert.NotContains(t, b.String(), "\x1b")
}

func TestPrintSkillsSanitizesTheSummary(t *testing.T) {
	// A read error carries the path, and a path can carry terminal
	// escapes; the status line must not.
	var b bytes.Buffer
	Print(&b, &Status{Skills: []skills.ClientState{{Client: "claude", Err: "open r\x1b[2J/.claude/skills: boom"}}})

	assert.Contains(t, b.String(), "skills         ")
	assert.Contains(t, b.String(), "boom")
	assert.NotContains(t, b.String(), "\x1b")
}

// gatherFixture writes one matrix fixture into the seeded workspace and
// gathers status over the matrix registry.
func gatherFixture(t *testing.T, f inspecttest.Fixture) (s *Status, out string) {
	t.Helper()

	st, root := seededStore(t)
	require.NoError(t, f.Write(root))

	s, err := gather(inspecttest.Registry(), st, root)
	require.NoError(t, err, "status must describe a broken setup, not fail on it")

	var b bytes.Buffer
	Print(&b, s)

	return s, b.String()
}

func TestGatherFollowsTheInspectionMatrix(t *testing.T) {
	// One fixture matrix for init, doctor, and status: the per-client
	// view carries the typed states, and the text carries the words
	// every consumer prints for them.
	for _, f := range inspecttest.Fixtures() {
		t.Run(f.Name, func(t *testing.T) {
			s, out := gatherFixture(t, f)

			i := slices.IndexFunc(s.Clients, func(insp integration.Inspection) bool { return insp.ClientID == f.Client })
			require.GreaterOrEqual(t, i, 0)
			insp := s.Clients[i]

			for capability, want := range f.States {
				entry, ok := insp.Entry(capability)
				require.True(t, ok)
				assert.Equal(t, want, entry.State, "%s", capability)
			}

			assert.Equal(t, f.GateMode, insp.GateMode)
			assert.Equal(t, f.PossibleGateMode, insp.PossibleGateMode)
			assert.Equal(t, f.ManagedGateMode, insp.ManagedGateMode)

			// The legacy fields describe the Claude Code hook alone, from
			// the same inspection.
			if f.Client == integration.ClaudeID {
				assert.Equal(t, f.ManagedGateMode, s.GateHookMode)
				assert.Equal(t, insp.HookDocumentError, s.GateHookError)
			}

			// The client line prints the hooks and the registration; the
			// skills and approvals lines print the rest per client.
			for _, word := range f.Words {
				assert.Contains(t, out, word)
			}

			if f.Skills != "" {
				assert.Contains(t, out, f.Skills)
			}

			for _, reason := range f.Findings {
				assert.Contains(t, out, reason, "a limitation the adapter reports is printed")
			}

			if f.Invoker != "" {
				assert.Equal(t, f.Invoker, s.DistillClient)
				assert.Contains(t, out, "(invoker "+f.Invoker+")")
			}

			for _, secret := range f.Absent {
				assert.NotContains(t, out, secret, "a credential in a hook command never reaches the text")
			}

			// The JSON view carries the same typed fields, by name.
			data, err := json.Marshal(s)
			require.NoError(t, err)

			var back Status
			require.NoError(t, json.Unmarshal(data, &back))
			assert.Equal(t, s, &back)
			assert.Contains(t, string(data), `"clients":[`)
			assert.Contains(t, string(data), `"declared":[`)

			for _, secret := range f.Absent {
				assert.NotContains(t, string(data), secret, "a credential in a hook command never reaches JSON")
			}
		})
	}
}

func TestPrintGateReadsTheEffectiveModeOfEveryCodexSource(t *testing.T) {
	s, out := gatherFixture(t, inspecttest.Named("inline enforce"))

	i := slices.IndexFunc(s.Clients, func(insp integration.Inspection) bool { return insp.ClientID == integration.CodexID })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, "enforce", s.Clients[i].GateMode)
	assert.Equal(t, "warn", s.Clients[i].ManagedGateMode)
	assert.Contains(t, out, "gate           enforce (codex hook carries --enforce; blocking verdicts exit 2)")
}

func TestPrintGateCoversEveryClientWithAGateHook(t *testing.T) {
	st, root := seededStore(t)

	// A Codex gate hook alone: the gate line must not say "no hook".
	require.NoError(t, inspecttest.Named("pending trust").Write(root))

	s, err := gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)
	assert.Empty(t, s.GateHookMode, "the legacy field stays Claude Code's")

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "gate           hook installed (codex); policy mode warn governs")

	// Mixed modes: an enforcing Codex hook beside a warn Claude Code hook
	// names both, because one enforcing hook blocks whatever the other
	// says.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"`+inspecttest.Binary+` gate --enforce --hook --client codex"}]}]}}`), 0o644))
	require.NoError(t, inspecttest.Named("current").Write(root))

	s, err = gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)
	assert.Equal(t, "warn", s.GateHookMode)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "gate           enforce for codex (hook carries --enforce; blocking verdicts exit 2); "+
		"the claude hook follows policy mode warn")

	// A broken policy under the enforcing hook names the client that
	// fails closed.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: [broken\n"), 0o644))

	s, err = gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)

	b.Reset()
	Print(&b, s)
	assert.Contains(t, b.String(), "FAILS CLOSED (codex)")
}

func TestPrintGateNamesAHookThatDiscardsItsExitStatus(t *testing.T) {
	// The reported defect: a wrapper with "|| true" read as a warn hook,
	// and the line said that an enforcing policy governs it. A verdict
	// blocks only by exit status 2, so no policy makes that hook block.
	st, root := seededStore(t)

	require.NoError(t, inspecttest.Named("pending trust").Write(root))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"`+inspecttest.Binary+` gate --enforce --hook --client codex || true"}]}]}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: enforce\n"), 0o644))

	s, err := gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "gate           report-only (codex hook discards its exit status); "+
		"nothing blocks, whatever policy mode enforce says")
	assert.NotContains(t, b.String(), "governs")
	assert.Contains(t, b.String(), "codex   gate hook installed (report-only)")

	// Beside a warn hook, the policy governs that one, and the line names
	// the hook that never blocks.
	inspection := func(id, mode string) integration.Inspection {
		return integration.Inspection{ClientID: id, GateMode: mode, Capabilities: []integration.CapabilityInspection{
			{Capability: integration.CapabilityCommands, Supported: true, State: integration.StateCurrent},
		}}
	}

	b.Reset()
	Print(&b, &Status{GatePolicyMode: "enforce", Clients: []integration.Inspection{
		inspection("claude", "warn"), inspection("codex", integration.GateModeReportOnly),
	}})
	assert.Contains(t, b.String(), "gate           hook installed (claude); policy mode enforce governs\n"+
		"               the codex hook discards its exit status, so it never blocks")

	// A broken policy changes nothing for such a hook: it never blocks.
	b.Reset()
	Print(&b, &Status{GatePolicyError: "boom", Clients: []integration.Inspection{inspection("codex", integration.GateModeReportOnly)}})
	assert.Contains(t, b.String(), "the report-only hook discards its exit status (codex): nothing blocks")
	assert.NotContains(t, b.String(), "no operational gate hook")

	// The reported defect: a definition that may run the gate, such as
	// an echo with the gate command as its arguments, made the line say
	// "enforce". Nothing is known to block, and the line says so.
	possible := inspection("claude", "")
	possible.PossibleGateMode = "enforce"

	b.Reset()
	Print(&b, &Status{GatePolicyMode: "warn", Clients: []integration.Inspection{possible}})
	assert.Contains(t, b.String(), "gate           policy mode warn; a definition of claude may run a gate hook; "+
		"seamark cannot tell what it does, so nothing is known to block")
	assert.NotContains(t, b.String(), "carries --enforce")

	// Beside a hook that certainly runs, the possible one is a note.
	b.Reset()
	Print(&b, &Status{GatePolicyMode: "warn", Clients: []integration.Inspection{possible, inspection("codex", "warn")}})
	assert.Contains(t, b.String(), "gate           hook installed (codex); policy mode warn governs\n"+
		"               a definition of claude may also run a gate hook; seamark cannot tell what it does")

	// A broken policy and a possible gate: the behaviour is unknown.
	b.Reset()
	Print(&b, &Status{GatePolicyError: "boom", Clients: []integration.Inspection{possible}})
	assert.Contains(t, b.String(), "POLICY BROKEN (boom)\n               a definition of claude may run a gate hook")
	assert.Contains(t, b.String(), "effective behaviour unknown")
}

func TestPrintClientsSanitizesAdapterText(t *testing.T) {
	// Adapter details and findings carry repository bytes (paths,
	// commands); the status lines must not carry terminal escapes.
	s := &Status{Clients: []integration.Inspection{{
		ClientID: "codex",
		Capabilities: []integration.CapabilityInspection{
			{Capability: integration.CapabilityMCPRegistration, Supported: true,
				State: integration.StateUnreadable, Detail: "unreadable (\x1b[2Jboom)"},
			{Capability: integration.CapabilityEdits, Supported: true, State: integration.StateCurrent, Detail: "lessons hook installed"},
			{Capability: integration.CapabilityCommands, Supported: true},
		},
		Findings: []integration.Finding{{Level: integration.FindingWarning, Reason: "runs \x1b]0;forged\x07 twice"}},
	}}}

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), "boom")
	assert.Contains(t, b.String(), "warning: runs")
	assert.NotContains(t, b.String(), "\x1b")
}

func TestPrintGateNeverSaysNothingBlocksBesideAnUnreadableDocument(t *testing.T) {
	// A report-only Claude Code hook beside an unreadable Codex hook
	// document: the Codex document can hold an enforcing gate, so the
	// line reports the unknown, not "nothing blocks".
	inspection := func(id, mode string, state integration.CapabilityState, detail string) integration.Inspection {
		return integration.Inspection{ClientID: id, GateMode: mode, Capabilities: []integration.CapabilityInspection{
			{Capability: integration.CapabilityCommands, Supported: true, State: state, Detail: detail},
		}}
	}

	var b bytes.Buffer
	Print(&b, &Status{GatePolicyMode: "warn", Clients: []integration.Inspection{
		inspection("claude", integration.GateModeReportOnly, integration.StateCurrent, ""),
		inspection("codex", "", integration.StateUnreadable, "unreadable: boom"),
	}})
	assert.Contains(t, b.String(), "gate           policy mode warn; hook configuration UNREADABLE (codex: unreadable: boom)")
	assert.Contains(t, b.String(), "the claude hook discards its exit status, so it never blocks")
	assert.NotContains(t, b.String(), "nothing blocks")
	assert.Equal(t, 1, strings.Count(b.String(), "UNREADABLE"), "the unknown is named once")

	b.Reset()
	Print(&b, &Status{GatePolicyError: "boom", Clients: []integration.Inspection{
		inspection("claude", integration.GateModeReportOnly, integration.StateCurrent, ""),
		inspection("codex", "", integration.StateUnreadable, "unreadable: boom"),
	}})
	assert.Contains(t, b.String(), "effective behaviour unknown")
	assert.NotContains(t, b.String(), "nothing blocks")
}

func TestPrintGateReadsTheLegacyFieldsWithoutClients(t *testing.T) {
	// A status decoded from an older JSON document has no per-client
	// view; the gate line then falls back to the Claude Code fields.
	var b bytes.Buffer
	Print(&b, &Status{GatePolicyMode: "warn", GateHookMode: "enforce"})
	assert.Contains(t, b.String(), "gate           enforce (claude hook carries --enforce")

	b.Reset()
	Print(&b, &Status{GatePolicyMode: "warn", GateHookError: "boom"})
	assert.Contains(t, b.String(), "UNREADABLE (claude: boom)")
}

func TestGatherReadsTheClaudeManagedGateModeByTheInspectionRule(t *testing.T) {
	// The legacy gate fields and the per-client view read one rule: the
	// gate hook fires for Bash under Claude Code's matcher rule, and
	// enforce wins. The integration tests hold the full layout list.
	enforce := inspecttest.Binary + " gate --enforce --hook"
	warn := inspecttest.Binary + " gate --hook"

	entry := func(matcher, command string) string {
		return `{"matcher":"` + matcher + `","hooks":[{"type":"command","command":"` + command + `"}]}`
	}

	for name, entries := range map[string]string{
		"star matcher":             entry("*", enforce),
		"empty matcher":            entry("", enforce),
		"regular expression":       entry("Ba.*", enforce),
		"name list":                entry("Bash|Edit", enforce),
		"enforce first, warn last": entry("Bash", enforce) + "," + entry("Bash", warn),
	} {
		t.Run(name, func(t *testing.T) {
			st, root := seededStore(t)
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
				[]byte(`{"hooks":{"PreToolUse":[`+entries+`]}}`), 0o644))

			s, err := gather(inspecttest.Registry(), st, root)
			require.NoError(t, err)
			assert.Equal(t, "enforce", s.GateHookMode)
			assert.Empty(t, s.GateHookError)

			i := slices.IndexFunc(s.Clients, func(insp integration.Inspection) bool { return insp.ClientID == integration.ClaudeID })
			require.GreaterOrEqual(t, i, 0)
			assert.Equal(t, "enforce", s.Clients[i].GateMode)
			assert.Equal(t, "enforce", s.Clients[i].ManagedGateMode)

			var b bytes.Buffer
			Print(&b, s)
			assert.Contains(t, b.String(), "gate           enforce (claude hook carries --enforce; blocking verdicts exit 2)")
		})
	}
}
