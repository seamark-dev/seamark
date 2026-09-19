package integration

// The contract harness is the extension proof (specification AC-1): a
// third client needs one descriptor and one registration, and nothing
// in the shared engines or the existing descriptors changes. The
// test-only client below is permanent contract coverage, never a
// shipped integration.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/gate"
)

// thirdID is the test-only client's registry ID.
const thirdID = "fakeagent"

// fakeEdits is a minimal edit codec: it reports the paths it was
// constructed with and echoes advice as stdout.
type fakeEdits struct{ paths []string }

func (f fakeEdits) DecodeEdit([]byte) (EditEvent, error) {
	return EditEvent{Paths: f.paths}, nil
}

func (fakeEdits) EncodeAdvice(text string) (HookReply, error) {
	return HookReply{Stdout: []byte(text)}, nil
}

// fakeCommands is a minimal command codec used only to prove the
// negative assertions: the third client deliberately omits it.
type fakeCommands struct{}

func (fakeCommands) DecodeCommand([]byte) (CommandEvent, error) {
	return CommandEvent{}, errors.New("not used")
}

func (fakeCommands) EncodeDecision(*gate.Decision) (HookReply, error) {
	return HookReply{}, errors.New("not used")
}

func (fakeCommands) EncodeFailure(err error) HookReply {
	return HookReply{Stderr: []byte(err.Error()), ExitCode: 2}
}

// fakeSetup is a minimal setup adapter. It reports the inspection it
// was constructed with and plans nothing, so tests stay offline.
type fakeSetup struct{ inspection Inspection }

func (f fakeSetup) Inspect(string) Inspection { return f.inspection }

func (fakeSetup) Plan(string, string, ClientSetup) (ClientPlan, error) {
	return ClientPlan{}, nil
}

// thirdClient shares Codex's skill directory and omits the command and
// reset capabilities, the shape a real minimal contribution would take.
//
// Its setup adapter grants tools but cannot register the MCP server,
// so the harness covers partial setup support and an inspection with
// verified compatibility beside unknown project trust.
func thirdClient() Client {
	verified, err := VerifiedEvidence("1.0.0", "edit hook")
	if err != nil {
		panic(err)
	}

	inspection := Inspection{
		ClientID: thirdID,
		Capabilities: []CapabilityInspection{
			{Capability: CapabilitySetup, Supported: true, State: StateCurrent},
			{Capability: CapabilityToolGrants, Supported: true, State: StateAbsent},
			{Capability: CapabilityMCPRegistration},
			{Capability: CapabilityEdits, Supported: true, State: StateCurrent, Verification: verified},
			{Capability: CapabilityCommands},
		},
	}

	return Client{
		ID:        thirdID,
		Name:      "Fake Agent",
		SkillDirs: []string{".agents/skills"},
		Setup:     fakeSetup{inspection: inspection},
		SetupOps:  SetupSupport{ApproveTools: true},
		Edits:     fakeEdits{paths: []string{"a.go"}},
		Invocation: func(root string) (agent.CommandSpec, error) {
			return agent.CommandSpec{Name: thirdID, Argv: []string{"sh", "-c", "cat"}, Dir: root}, nil
		},
	}
}

// invocationOnlyClient declares nothing but the one-shot command: no
// skills, no setup, no lifecycle codecs. Every capability is optional,
// so this descriptor must pass the same harness as a full client.
func invocationOnlyClient() Client {
	return Client{
		ID:   "invoker",
		Name: "Invoker",
		Invocation: func(string) (agent.CommandSpec, error) {
			return agent.CommandSpec{Name: "invoker", Argv: []string{"sh", "-c", "cat"}}, nil
		},
	}
}

// checkCapabilityViewsAgree checks that Supports, Require, and Declared
// give one answer for every capability, so callers can rely on any.
func checkCapabilityViewsAgree(t *testing.T, c Client) {
	t.Helper()

	for _, capability := range Capabilities {
		if c.Supports(capability) {
			assert.NoError(t, c.Require(capability), "%s: %s", c.ID, capability)
			assert.Contains(t, c.Declared(), capability)
		} else {
			assert.ErrorIs(t, c.Require(capability), ErrUnsupported, "%s: %s", c.ID, capability)
			assert.NotContains(t, c.Declared(), capability)
		}
	}
}

// checkInspectionContract checks an adapter's offline inspection: it
// names its client, every entry is valid, and Supported repeats the
// descriptor. Incomplete verified evidence fails here.
func checkInspectionContract(t *testing.T, c Client) {
	t.Helper()

	inspection := c.Setup.Inspect(t.TempDir())
	assert.Equal(t, c.ID, inspection.ClientID)

	for _, entry := range inspection.Capabilities {
		require.NoError(t, entry.Validate(), "%s: inspection", c.ID)
		assert.Equal(t, c.Supports(entry.Capability), entry.Supported,
			"%s: inspection and descriptor disagree on %s", c.ID, entry.Capability)
	}
}

// checkClientContract is the reusable adapter check every descriptor,
// built-in or contributed, must pass. It runs offline: no binary, no
// login, no process.
func checkClientContract(t *testing.T, c Client) {
	t.Helper()

	require.NoError(t, validateClient(c), "descriptor invariants")
	checkCapabilityViewsAgree(t, c)

	// Skill support is the declared directories, nothing else.
	assert.Equal(t, len(c.SkillDirs) > 0, c.Supports(CapabilitySkills), "%s: skills", c.ID)

	if c.Setup != nil {
		checkInspectionContract(t, c)
	}

	if c.Invocation != nil {
		spec, err := c.Invocation(t.TempDir())
		require.NoError(t, err, "%s: invocation must resolve without the client installed", c.ID)
		require.NoError(t, spec.Validate(), "%s: invocation spec", c.ID)
		assert.NotEmpty(t, spec.Name, "%s: provenance needs a name", c.ID)
	}

	if c.Edits != nil {
		reply, err := c.Edits.EncodeAdvice("advice")
		require.NoError(t, err, "%s: advice encoding", c.ID)
		assert.Zero(t, reply.ExitCode, "%s: advice is always nonblocking", c.ID)
	}
}

func TestBuiltinClientsSatisfyTheContract(t *testing.T) {
	for _, c := range Builtin().Clients() {
		t.Run(c.ID, func(t *testing.T) { checkClientContract(t, c) })
	}
}

func TestThirdClientNeedsOnlyADescriptorAndARegistration(t *testing.T) {
	third := thirdClient()
	checkClientContract(t, third)

	// One registration beside the unchanged built-ins.
	r, err := NewRegistry(append(Builtin().Clients(), third)...)
	require.NoError(t, err)
	assert.Equal(t, []string{ClaudeID, CodexID, thirdID}, r.IDs())

	// Selection treats it like any other client.
	selected, err := r.Select([]string{thirdID, ClaudeID})
	require.NoError(t, err)
	require.Len(t, selected, 2)
	assert.Equal(t, ClaudeID, selected[0].ID)
	assert.Equal(t, thirdID, selected[1].ID)

	// The shared skill location is one destination with both consumers,
	// so neither client can report a status the other contradicts.
	dests := SkillDestinations(selected)
	assert.Contains(t, dests, SkillDestination{Dir: ".claude/skills", Consumers: []string{ClaudeID}})
	assert.Contains(t, dests, SkillDestination{Dir: ".agents/skills", Consumers: []string{thirdID}})

	all, err := r.Select(r.IDs())
	require.NoError(t, err)
	assert.Contains(t, SkillDestinations(all),
		SkillDestination{Dir: ".agents/skills", Consumers: []string{CodexID, thirdID}})

	// The missing optional capabilities are unsupported, not broken.
	assert.ErrorIs(t, third.Require(CapabilityCommands), ErrUnsupported)
	assert.ErrorIs(t, third.Require(CapabilityResets), ErrUnsupported)
	assert.ErrorIs(t, third.Require(CapabilityMCPRegistration), ErrUnsupported,
		"tool grants do not imply registration")
	assert.Equal(t, []Capability{
		CapabilitySkills, CapabilitySetup, CapabilityToolGrants, CapabilityEdits, CapabilityInvocation,
	}, third.Declared())

	// The declared capabilities work through the contract types alone.
	event, err := third.Edits.DecodeEdit([]byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a.go"}, event.Paths)
	assert.Nil(t, event.Context, "no receiving context means suppression stays off")

	spec, err := third.Invocation("/tmp/root")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/root", spec.Dir, "a contributed preset may pin its working directory")
}

func TestInvocationOnlyClientSatisfiesTheContract(t *testing.T) {
	invoker := invocationOnlyClient()
	checkClientContract(t, invoker)

	r, err := NewRegistry(append(Builtin().Clients(), invoker)...)
	require.NoError(t, err)

	got, ok := r.Lookup(invoker.ID)
	require.True(t, ok)
	assert.Equal(t, []Capability{CapabilityInvocation}, got.Declared())

	for _, capability := range Capabilities {
		if capability != CapabilityInvocation {
			assert.ErrorIs(t, got.Require(capability), ErrUnsupported, "%s", capability)
		}
	}

	// No skill directory means no destination, alone or beside others.
	assert.Empty(t, SkillDestinations([]Client{got}))

	for _, dest := range SkillDestinations(r.Clients()) {
		assert.NotContains(t, dest.Consumers, invoker.ID)
	}
}

func TestRegistryInjectionReplacesTheBuiltins(t *testing.T) {
	// Consumers take a registry value; a test can hand them one that
	// holds only the fake client and observe no built-in leaking in.
	r, err := NewRegistry(thirdClient())
	require.NoError(t, err)

	assert.Equal(t, []string{thirdID}, r.IDs())

	_, err = r.Select([]string{ClaudeID})
	require.ErrorIs(t, err, ErrUnknownClient)
	assert.Contains(t, err.Error(), "known: fakeagent")
}

func TestCommandFailureEncodingIsBlockingOnlyWhenAsked(t *testing.T) {
	// The fake command codec documents the gate's contract: a failure
	// translation carries the reason on stderr with exit 2, and it is
	// the caller's mode decision whether to use it.
	reply := fakeCommands{}.EncodeFailure(errors.New("policy unreadable"))
	assert.Equal(t, 2, reply.ExitCode)
	assert.Equal(t, "policy unreadable", string(reply.Stderr))
}
