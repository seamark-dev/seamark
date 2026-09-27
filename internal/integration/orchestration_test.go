package integration_test

// The extension proof through the shared engines (specification AC-1):
// the test-only third client goes through the real setup coordinator,
// the shared delivery service, the gate helpers, the inspection, and
// the shared invoker and distillation pipeline. No engine and no
// built-in adapter changes for it. The client is the one the contract
// harness registers; it shares the Codex skill directory, decodes edits,
// grants tools, invokes a fake process, and lacks the command, reset,
// hook, and MCP-registration capabilities.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/store"
)

// registryWith returns the built-in clients plus the given ones.
func registryWith(t *testing.T, extra ...integration.Client) *integration.Registry {
	t.Helper()

	reg, err := integration.NewRegistry(append(integration.Builtin().Clients(), extra...)...)
	require.NoError(t, err)

	return reg
}

// entryOf returns the capability entry of one client from an inspection.
func entryOf(t *testing.T, insps []integration.Inspection, id string, capability integration.Capability) integration.CapabilityInspection {
	t.Helper()

	for _, insp := range insps {
		if insp.ClientID != id {
			continue
		}

		entry, ok := insp.Entry(capability)
		require.True(t, ok, "%s: no %s entry", id, capability)

		return entry
	}

	t.Fatalf("no inspection for %s", id)

	return integration.CapabilityInspection{}
}

func TestThirdClientSetupSharesTheSkillDirectoryAndSkipsWhatItLacks(t *testing.T) {
	root := t.TempDir()
	reg := registryWith(t, integration.ThirdClient())

	// Everything is requested; the coordinator keeps what the
	// descriptor declares and names the rest as informational.
	setups := []integration.ClientSetup{
		{ClientID: integration.CodexID, Skills: true},
		{ClientID: integration.ThirdID, Skills: true, Hooks: true, RegisterMCP: true, ApproveTools: true, GateMode: "enforce"},
	}

	plan, err := integration.PlanSetup(reg, integration.SetupRequest{Root: root, Binary: "/usr/local/bin/seamark", Clients: setups})
	require.NoError(t, err)

	assert.Equal(t, []integration.SkillDestination{
		{Dir: skills.AgentsDir, Consumers: []string{integration.CodexID, integration.ThirdID}},
	}, plan.Destinations, "one shared destination with both consumers")

	for _, entry := range plan.Skills {
		assert.True(t, strings.HasPrefix(entry.Rel, skills.AgentsDir+"/"), "%s: every skill entry is under the shared directory", entry.Rel)
	}

	require.NotEmpty(t, plan.Skills)

	var reasons []string

	for _, f := range plan.Findings {
		assert.Equal(t, integration.FindingInfo, f.Level, "an unsupported intent is informational: %s", f.Reason)
		reasons = append(reasons, f.Reason)
	}

	joined := strings.Join(reasons, "\n")
	assert.Contains(t, joined, "Fake Agent: lifecycle hooks are not supported")
	assert.Contains(t, joined, "Fake Agent: MCP registration is not supported")
	assert.NotContains(t, joined, "tool grants are not supported", "the declared operation is kept")
	assert.NotContains(t, joined, "skills are not supported")
	assert.Empty(t, plan.GateHooks, "no client of this run installs a gate hook")

	// Apply writes the shared directory once, for both consumers.
	result, err := integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)
	require.Len(t, result.Skills, len(plan.Skills))

	for _, op := range result.Skills {
		assert.Equal(t, integration.OpApplied, op.Status, op.Path)
		assert.Equal(t, []string{integration.CodexID, integration.ThirdID}, op.Consumers, op.Path)
	}

	assert.DirExists(t, filepath.Join(root, skills.AgentsDir, "seamark-plan-change"))
	assert.NoDirExists(t, filepath.Join(root, skills.ClaudeDir), "an unselected client's directory is never written")

	// A second run converges: nothing to write.
	plan, err = integration.PlanSetup(reg, integration.SetupRequest{Root: root, Binary: "/usr/local/bin/seamark", Clients: setups})
	require.NoError(t, err)

	result, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)

	for _, op := range result.Skills {
		assert.Equal(t, integration.OpKept, op.Status, op.Path)
	}

	// Inspection reports the shared directory to both consumers with
	// the same state, and labels it by every consumer.
	insps := reg.Inspect(root)

	codexSkills := entryOf(t, insps, integration.CodexID, integration.CapabilitySkills)
	thirdSkills := entryOf(t, insps, integration.ThirdID, integration.CapabilitySkills)
	assert.Equal(t, integration.StateCurrent, codexSkills.State)
	assert.Equal(t, integration.StateCurrent, thirdSkills.State)
	assert.Contains(t, codexSkills.Detail, "shared with fakeagent")
	assert.Contains(t, thirdSkills.Detail, "shared with codex")

	var labels []string

	for _, state := range reg.InspectSkills(root) {
		labels = append(labels, state.Client)
	}

	assert.Contains(t, labels, integration.CodexID+"+"+integration.ThirdID, "the destination is labelled by every consumer")
}

func TestThirdClientDeliversAdviceThroughTheSharedService(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package x\n"), 0o644))

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	third := integration.ThirdClient()

	// The adapter decodes the native event; the service does the rest.
	event, err := third.Edits.DecodeEdit([]byte(`{}`))
	require.NoError(t, err)
	event.CWD = root

	// Once-per-context is requested, and the adapter names no receiver:
	// the service must fall back to repeated delivery and say why.
	cfg := reviews.DefaultConfig()
	cfg.Delivery = reviews.HookDeliveryOncePerContext
	cfg.Pin = []reviews.PinRule{{Rule: "reset-pooled-state", Note: "Reset every pooled field before reuse."}}

	var stdout bytes.Buffer

	emit := func(advice string) error {
		reply, err := third.Edits.EncodeAdvice(advice)
		if err != nil {
			return err
		}

		stdout.Write(reply.Stdout)

		return nil
	}

	out, err := delivery.Deliver(context.Background(), st, delivery.Request{
		Root: root, ClientID: integration.ThirdID, Mechanism: third.Edits.AdviceMechanism(), Event: event, Config: cfg,
	}, emit)
	require.NoError(t, err)

	assert.Equal(t, delivery.StatusEmitted, out.Status)
	assert.Equal(t, []string{"a.go"}, out.Files)
	assert.Equal(t, delivery.SuppressionNoContext, out.Suppression, "the adapter names no receiver, so advice repeats")
	assert.Contains(t, stdout.String(), "Reset every pooled field before reuse.", "the advice reaches the client's native reply")

	// The firing log attributes the record to the third client and its
	// mechanism, and to nothing else.
	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)
	assert.Equal(t, integration.ThirdID, firings[0].Client)
	assert.Equal(t, "fake-stdout", firings[0].Mechanism)
	assert.Equal(t, reviews.DeliveryInjected, firings[0].Delivery)
	assert.Empty(t, firings[0].ContextSHA, "no receiving context, no context digest")
}

func TestThirdClientCommandGateIsUnsupportedNotBroken(t *testing.T) {
	root := t.TempDir()
	reg := registryWith(t, integration.ThirdClient())

	third, ok := reg.Lookup(integration.ThirdID)
	require.True(t, ok)
	assert.ErrorIs(t, third.Require(integration.CapabilityCommands), integration.ErrUnsupported)
	assert.ErrorIs(t, third.Require(integration.CapabilityResets), integration.ErrUnsupported)

	// The gate helpers that init reads never list a client without a
	// gate hook, and never read a mode for it.
	setups, err := integration.ExplicitSetups(reg, []string{integration.ThirdID}, false, false, "enforce")
	require.NoError(t, err)
	assert.Empty(t, integration.GateHookClients(reg, setups))
	assert.Empty(t, integration.UngatedHookClients(reg, setups), "a client without hook installation is not an ungated hook client either")
	assert.Empty(t, integration.InstalledGateMode(reg, root, []string{integration.ThirdID}))

	// Inspection shows the capability as unsupported and absent, with no
	// evidence and no trust claimed for a surface the client lacks.
	insps := reg.Inspect(root)
	commands := entryOf(t, insps, integration.ThirdID, integration.CapabilityCommands)
	assert.False(t, commands.Supported)
	assert.Equal(t, integration.StateAbsent, commands.State)
	assert.Equal(t, integration.TrustUnknown, commands.Trust)
	assert.NotEqual(t, integration.VerificationVerified, commands.Verification.Level)
	assert.Empty(t, commands.Action, "nothing to fix: the client has no such surface")
	require.NoError(t, commands.Validate())

	resets := entryOf(t, insps, integration.ThirdID, integration.CapabilityResets)
	assert.False(t, resets.Supported)
	assert.Equal(t, integration.StateAbsent, resets.State)
}

// scriptedThird is the third client with an invocation that answers
// every prompt with one proposal, the way a real client's one-shot
// mode would. The prompt is consumed from stdin as the contract asks.
func scriptedThird() integration.Client {
	c := integration.ThirdClient()
	reply := `{"patterns":[{"rule":"pooled-state-reset","note":"Reset pooled state before reuse.","finding_ids":[1,2,3],"trigger_paths":[]}]}`

	c.Invocation = func(root string) (agent.CommandSpec, error) {
		return agent.CommandSpec{
			Name: integration.ThirdID,
			Argv: []string{"sh", "-c", "cat >/dev/null; printf '%s\\n' '" + reply + "'"},
			Dir:  root,
		}, nil
	}

	return c
}

func TestThirdClientInvokerDistillsThroughTheSharedPipeline(t *testing.T) {
	root := t.TempDir()
	reg := registryWith(t, scriptedThird())

	// `agent.cli: fakeagent` resolves through the registry like any
	// shipped client, without the client on PATH being checked here.
	cfg := &agent.Config{}
	cfg.Agent.CLI = integration.ThirdID

	spec, err := reg.ResolveInvocation(cfg, root)
	require.NoError(t, err)
	assert.Equal(t, integration.ThirdID, spec.Name)
	assert.Equal(t, root, spec.Dir)
	assert.Contains(t, reg.InvocationIDs(), integration.ThirdID)

	inv, err := agent.NewCommand(spec)
	require.NoError(t, err)
	assert.Equal(t, integration.ThirdID, inv.Name(), "provenance names the client")

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "v2/pkg/engine/context.go", Body: "Clear pooled sizes in Free to avoid stale retention when the context is pooled and reused."},
		{ID: 2, Path: "v2/pkg/engine/resolvable.go", Body: "Reset pooled extension state between resolves; the pooled resolvable leaks the old extensions."},
		{ID: 3, Path: "v2/pkg/engine/loader.go", Body: "Reset pooled cache flags on loader reuse, otherwise the pooled loader keeps stale state."},
	}))

	res, err := distill.Run(context.Background(), st, distill.NewLexicalGrouper(), inv, distill.Options{Root: root})
	require.NoError(t, err)

	assert.Equal(t, 1, res.GroupsRead)
	assert.Zero(t, res.GroupsFailed)
	require.Len(t, res.Proposals, 1)
	assert.Equal(t, "pooled-state-reset", res.Proposals[0].Rule)
	assert.True(t, strings.HasPrefix(res.Proposals[0].Agent, integration.ThirdID+"/"), "provenance: %s", res.Proposals[0].Agent)
	assert.Equal(t, model.ProposalProposed, res.Proposals[0].Status, "a proposal is never applied by the pipeline")

	// The unscripted contract client echoes its prompt: the shared
	// invoker delivers the prompt on stdin and returns stdout.
	echo, err := integration.ThirdClient().Invocation(root)
	require.NoError(t, err)

	echoInv, err := agent.NewCommand(echo)
	require.NoError(t, err)

	got, err := echoInv.Invoke(context.Background(), "prompt on stdin")
	require.NoError(t, err)
	assert.Equal(t, "prompt on stdin", got)
}
