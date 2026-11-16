package integration

// The contract harness shows how a new client joins: a third client
// needs one descriptor and one registration, and nothing in the shared
// engines or the existing descriptors changes. The test-only client
// below is permanent contract coverage, never a shipped integration.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
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

func (fakeEdits) AdviceMechanism() string { return "fake-stdout" }

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

// ManagedGateMode repeats the inspection, because SetupAdapter requires
// that it equals the ManagedGateMode of Inspect.
func (f fakeSetup) ManagedGateMode(string) string { return f.inspection.ManagedGateMode }

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

// builtinWith returns a registry of the built-in clients plus the given
// ones. Most tests of a test-only client register it beside the
// built-ins, because a real contribution joins them the same way.
func builtinWith(t *testing.T, extra ...Client) *Registry {
	t.Helper()

	reg, err := NewRegistry(append(Builtin().Clients(), extra...)...)
	require.NoError(t, err)

	return reg
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

// checkInspectionContract checks an adapter's offline inspection. The
// inspection names its client, every entry is valid, and Supported
// repeats the descriptor. The narrow read of the gate mode agrees with
// the full one. Incomplete verified evidence fails here.
func checkInspectionContract(t *testing.T, c Client) {
	t.Helper()

	root := t.TempDir()
	inspection := c.Setup.Inspect(root)
	assert.Equal(t, c.ID, inspection.ClientID)
	assert.Equal(t, inspection.ManagedGateMode, c.Setup.ManagedGateMode(root), "%s: managed gate mode", c.ID)

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

		// The firing log stores the name, so it follows the ID rule.
		assert.Regexp(t, clientIDPattern, c.Edits.AdviceMechanism(), "%s: mechanism name", c.ID)
	}

	if c.Explores != nil {
		// Exploration advice arrives after the read, so neither reply
		// form may block or report an error to the client.
		for _, read := range []bool{true, false} {
			reply, err := c.Explores.EncodeExploreAdvice("advice", read)
			require.NoError(t, err, "%s: explore advice encoding (read %v)", c.ID, read)
			assert.Zero(t, reply.ExitCode, "%s: explore advice is always nonblocking (read %v)", c.ID, read)
			assert.Empty(t, reply.Stderr, "%s: explore advice writes no stderr (read %v)", c.ID, read)
			assert.NotEmpty(t, reply.Stdout, "%s: explore advice carries the text (read %v)", c.ID, read)
		}

		assert.Regexp(t, clientIDPattern, c.Explores.ExploreMechanism(), "%s: explore mechanism name", c.ID)
	}

	if c.Guards != nil {
		checkGuardContract(t, c)
	}

	if c.Completions != nil {
		checkCompletionContract(t, c)
	}
}

// checkGuardContract checks a pre-write guard codec. A deny always
// blocks with the reason on stderr and writes no stdout. A payload
// that the codec cannot read is an error with no file operation. The
// error result names the descriptor's client, never a client from the
// payload.
func checkGuardContract(t *testing.T, c Client) {
	t.Helper()

	for _, reasons := range [][]string{{"lesson check denies"}, nil} {
		reply := c.Guards.EncodeDeny(reasons)
		assert.Equal(t, hookBlockExit, reply.ExitCode, "%s: a deny blocks (reasons %q)", c.ID, reasons)
		assert.Empty(t, reply.Stdout, "%s: a deny writes no stdout (reasons %q)", c.ID, reasons)
	}

	assert.Equal(t, "lesson check denies", string(c.Guards.EncodeDeny([]string{"lesson check denies"}).Stderr),
		"%s: the deny reason travels on stderr", c.ID)

	for _, payload := range []string{`not json`, `{}`, `{"client": "other", "tool_name": ""}`} {
		op, err := c.Guards.DecodeGuard([]byte(payload))
		require.ErrorIs(t, err, ErrMalformedEvent, "%s: %s", c.ID, payload)
		assert.Empty(t, op.Ops, "%s: an error carries no file operation (%s)", c.ID, payload)
		assert.False(t, op.PathsComplete, "%s: an error never claims complete paths (%s)", c.ID, payload)
		assert.Equal(t, c.ID, op.Client, "%s: the client comes from the descriptor (%s)", c.ID, payload)
	}
}

// checkCompletionContract checks a post-edit completion codec. The
// findings reply never blocks, for every tool outcome, because the
// tool already ran. A payload that the codec cannot read is an
// error with no path, and it names the descriptor's client.
func checkCompletionContract(t *testing.T, c Client) {
	t.Helper()

	for _, outcome := range []lessoncheck.ToolOutcome{
		lessoncheck.ToolSucceeded, lessoncheck.ToolFailed, lessoncheck.ToolInterrupted,
		lessoncheck.ToolDenied, lessoncheck.ToolUnknown,
	} {
		reply, err := c.Completions.EncodeFindings("finding", outcome)
		require.NoError(t, err, "%s: findings encoding (%s)", c.ID, outcome)
		assert.Zero(t, reply.ExitCode, "%s: post-edit findings never block (%s)", c.ID, outcome)
		assert.Contains(t, string(reply.Stdout)+string(reply.Stderr), "finding",
			"%s: the reply carries the text (%s)", c.ID, outcome)
	}

	completion, err := c.Completions.DecodeCompletion([]byte(`not json`))
	require.ErrorIs(t, err, ErrMalformedEvent, "%s: a malformed completion", c.ID)
	assert.Empty(t, completion.Paths, "%s: an error carries no path", c.ID)
	assert.Equal(t, c.ID, completion.Client, "%s: the client comes from the descriptor", c.ID)
}

func TestBuiltinClientsSatisfyTheContract(t *testing.T) {
	for _, c := range Builtin().Clients() {
		t.Run(c.ID, func(t *testing.T) { checkClientContract(t, c) })
	}
}

// TestBuiltinClientsDeclareLessonCheckCapabilities checks that the
// three lesson-check capabilities join Capabilities and follow the
// descriptor fields, but never ConfiguredCapabilities, so an inspection
// and the support line stay as they were.
func TestBuiltinClientsDeclareLessonCheckCapabilities(t *testing.T) {
	lessonCaps := []Capability{CapabilityLessonVerify, CapabilityLessonGuard, CapabilityLessonExploration}

	assert.Equal(t, []Capability{
		CapabilitySkills, CapabilitySetup, CapabilityMCPRegistration, CapabilityToolGrants,
		CapabilityEdits, CapabilityCommands, CapabilityResets, CapabilityInvocation,
		CapabilityLessonVerify, CapabilityLessonGuard, CapabilityLessonExploration,
	}, Capabilities, "the new names follow the existing ones")
	assert.Equal(t, []string{"lesson-verify", "lesson-guard", "lesson-exploration"},
		[]string{string(lessonCaps[0]), string(lessonCaps[1]), string(lessonCaps[2])}, "the lesson-check names are frozen")
	assert.Equal(t, []Capability{
		CapabilitySkills, CapabilityMCPRegistration, CapabilityToolGrants,
		CapabilityEdits, CapabilityCommands, CapabilityResets,
	}, ConfiguredCapabilities, "no lesson-check capability has a configured state")

	claude, ok := Builtin().Lookup(ClaudeID)
	require.True(t, ok)

	codex, ok := Builtin().Lookup(CodexID)
	require.True(t, ok)

	assert.Subset(t, claude.Declared(), lessonCaps, "Claude Code has a guard, a completion, and an explore codec")
	assert.Subset(t, codex.Declared(), []Capability{CapabilityLessonVerify, CapabilityLessonGuard})
	assert.NotContains(t, codex.Declared(), CapabilityLessonExploration, "Codex reports no structured read")
	assert.Equal(t, "skills, hooks, command gate, MCP registration, tool grants, inference", claude.DescribeSupport(),
		"the support line does not change")

	t.Run("verify needs the guard and the completion codec", func(t *testing.T) {
		guardOnly := Client{ID: "guard-only", Name: "Guard only", Guards: claudeGuards{}}
		completionOnly := Client{ID: "completion-only", Name: "Completion only", Completions: claudeCompletions{}}

		assert.Equal(t, []Capability{CapabilityLessonGuard}, guardOnly.Declared())
		assert.Empty(t, completionOnly.Declared(), "a completion without a baseline decoder verifies nothing")

		for _, c := range []Client{guardOnly, completionOnly} {
			_, err := NewRegistry(c)
			require.NoError(t, err)
			checkCapabilityViewsAgree(t, c)
		}
	})

	t.Run("only a reviewed recording verifies a pre-write path", func(t *testing.T) {
		// The reviewed Claude Code 2.1.319 recording verifies Write and
		// Edit in both contexts, and the reviewed codex-cli 0.169.0
		// recording verifies apply_patch in both contexts. Both clients
		// let the write run after their 10 s hook timeout, so each
		// verified path names the limit. Claude Code made no MultiEdit
		// call, so that path waits for a recording. It names no version,
		// because the recording sets the version it ran against.
		for _, tc := range []struct {
			client  string
			tool    string
			version string // "" for a path that waits for a recording
		}{
			{ClaudeID, "Edit", "Claude Code 2.1.319"},
			{ClaudeID, "Write", "Claude Code 2.1.319"},
			{ClaudeID, "MultiEdit", ""},
			{CodexID, "apply_patch", "codex-cli 0.169.0"},
		} {
			for _, context := range []lessoncheck.PathContext{"root", "child"} {
				label := tc.client + " " + tc.tool + " " + string(context)
				evidence := GuardEvidence(tc.client, tc.tool, context)
				require.NoError(t, evidence.Validate(), label)
				assert.Contains(t, evidence.Surface, tc.tool, label)
				assert.Contains(t, evidence.Surface, string(context), label)

				if tc.version == "" {
					assert.Equal(t, VerificationPending, evidence.Level, label)
					assert.Empty(t, evidence.ClientVersion, "%s: the recording names the version it ran against", label)
					assert.Empty(t, evidence.Limit, label)

					continue
				}

				assert.Equal(t, VerificationVerified, evidence.Level, label)
				assert.Equal(t, tc.version, evidence.ClientVersion, label)
				assert.Equal(t, "the client lets the write run after its 10 s hook timeout", evidence.Limit, label)
			}
		}

		// A writer without a guard path, a guard tool of another client,
		// and an unknown context have no evidence at all.
		for _, tc := range []struct{ client, tool, context string }{
			{ClaudeID, "NotebookEdit", "root"},
			{ClaudeID, "Bash", "root"},
			{ClaudeID, "apply_patch", "root"},
			{CodexID, "Edit", "root"},
			{CodexID, "Bash", "child"},
			{thirdID, "Edit", "root"},
			{ClaudeID, "Edit", "unknown"},
		} {
			assert.Equal(t, VerificationEvidence{}, GuardEvidence(tc.client, tc.tool, lessoncheck.PathContext(tc.context)),
				"%s %s %s", tc.client, tc.tool, tc.context)
		}
	})

	t.Run("an inspection synthesizes no lesson-check entry", func(t *testing.T) {
		root := t.TempDir()

		for _, insp := range Builtin().Inspect(root) {
			assert.Subset(t, insp.Declared, []Capability{CapabilityLessonVerify, CapabilityLessonGuard},
				"%s: the declared list carries them", insp.ClientID)

			for _, entry := range insp.Capabilities {
				assert.NotContains(t, lessonCaps, entry.Capability,
					"%s: the TUI Overview and status clients[].capabilities stay as they were", insp.ClientID)
			}
		}
	})
}

func TestThirdClientNeedsOnlyADescriptorAndARegistration(t *testing.T) {
	third := thirdClient()
	checkClientContract(t, third)

	// One registration beside the unchanged built-ins.
	r := builtinWith(t, third)
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

	r := builtinWith(t, invoker)

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
	// Consumers take a registry value. A test can give them one that
	// holds only the fake client and check that no built-in client
	// appears.
	r, err := NewRegistry(thirdClient())
	require.NoError(t, err)

	assert.Equal(t, []string{thirdID}, r.IDs())

	_, err = r.Select([]string{ClaudeID})
	require.ErrorIs(t, err, ErrUnknownClient)
	assert.Contains(t, err.Error(), "known: fakeagent")
}

func TestCommandFailureEncodingIsBlockingOnlyWhenAsked(t *testing.T) {
	// The fake command codec shows the CommandHooks rule for failures. A
	// failure translation carries the reason on stderr with exit 2. The
	// caller's mode decides whether to use it.
	reply := fakeCommands{}.EncodeFailure(errors.New("policy unreadable"))
	assert.Equal(t, 2, reply.ExitCode)
	assert.Equal(t, "policy unreadable", string(reply.Stderr))
}
