package integration

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
)

func TestBuiltinRegistryIsStableAndOrdered(t *testing.T) {
	r := Builtin()

	assert.Equal(t, []string{ClaudeID, CodexID}, r.IDs(),
		"help, selection, and diagnostics all present clients in this order")

	claude, ok := r.Lookup(ClaudeID)
	require.True(t, ok)
	assert.Equal(t, "Claude Code", claude.Name)
	assert.Equal(t, []string{".claude/skills"}, claude.SkillDirs)

	codex, ok := r.Lookup(CodexID)
	require.True(t, ok)
	assert.Equal(t, []string{".agents/skills"}, codex.SkillDirs)

	_, ok = r.Lookup("hal9000")
	assert.False(t, ok)
}

func TestNewRegistryRejectsInvalidDescriptors(t *testing.T) {
	valid := Client{ID: "one", Name: "One"}

	_, err := NewRegistry(valid, Client{ID: "one", Name: "Again"})
	require.Error(t, err, "a repeated id is a programming error, not a merge")
	assert.Contains(t, err.Error(), "twice")

	for _, id := range []string{"", "Claude", "1st", "with space", "a/b"} {
		_, err := NewRegistry(Client{ID: id, Name: "X"})
		assert.Error(t, err, "id %q must be rejected", id)
	}

	_, err = NewRegistry(Client{ID: "blank", Name: "  "})
	require.Error(t, err)

	for _, dir := range []string{"", "/abs/skills", "..", "../up/skills", "a/../b", "./x", "."} {
		_, err := NewRegistry(Client{ID: "dirs", Name: "Dirs", SkillDirs: []string{dir}})
		assert.Error(t, err, "skill dir %q must be rejected", dir)
	}
}

func TestSelectDeduplicatesAndFollowsRegistryOrder(t *testing.T) {
	r := Builtin()

	// Input order and repetition do not matter; registry order does, so
	// reordered flags plan the same run.
	selected, err := r.Select([]string{CodexID, ClaudeID, CodexID})
	require.NoError(t, err)
	require.Len(t, selected, 2)
	assert.Equal(t, ClaudeID, selected[0].ID)
	assert.Equal(t, CodexID, selected[1].ID)

	none, err := r.Select(nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	_, err = r.Select([]string{ClaudeID, "hal9000"})
	require.ErrorIs(t, err, ErrUnknownClient, "an unknown id fails before any work")
	assert.Contains(t, err.Error(), "claude, codex", "the error names the known clients")
}

func TestClientsReturnsACopy(t *testing.T) {
	r := Builtin()

	clients := r.Clients()
	clients[0].Name = "mutated"

	again, _ := r.Lookup(ClaudeID)
	assert.Equal(t, "Claude Code", again.Name, "the registry is immutable after construction")
}

func TestSkillDirsAreCopiedAtEveryRegistryBoundary(t *testing.T) {
	const outside = "../outside"

	input := []string{".one/skills", ".shared/skills"}

	r, err := NewRegistry(Client{ID: "one", Name: "One", SkillDirs: input})
	require.NoError(t, err)

	// Each route hands out a descriptor; writing through its SkillDirs
	// must not reach the registry, or a validated path could leave the
	// repository after validation.
	routes := map[string]func() []string{
		"constructor input": func() []string { return input },
		"Clients":           func() []string { return r.Clients()[0].SkillDirs },
		"Lookup": func() []string {
			c, ok := r.Lookup("one")
			require.True(t, ok)

			return c.SkillDirs
		},
		"Select": func() []string {
			selected, err := r.Select([]string{"one"})
			require.NoError(t, err)

			return selected[0].SkillDirs
		},
	}

	for name, dirs := range routes {
		t.Run(name, func(t *testing.T) {
			dirs()[0] = outside

			got, ok := r.Lookup("one")
			require.True(t, ok)
			assert.Equal(t, []string{".one/skills", ".shared/skills"}, got.SkillDirs)

			assert.Equal(t, []SkillDestination{
				{Dir: ".one/skills", Consumers: []string{"one"}},
				{Dir: ".shared/skills", Consumers: []string{"one"}},
			}, SkillDestinations(r.Clients()), "destination grouping stays as validated")
		})
	}
}

func TestAbsentCapabilitiesReportUnsupported(t *testing.T) {
	codex, _ := Builtin().Lookup(CodexID)

	// Codex declares its skill directory and its setup adapter so far.
	// The lifecycle codecs and the invocation preset are still absent.
	declared := []Capability{
		CapabilitySkills, CapabilitySetup, CapabilityMCPRegistration, CapabilityToolGrants,
	}
	assert.Equal(t, declared, codex.Declared())
	assert.False(t, codex.SetupOps.Hooks, "codex hook installation waits for its native evidence")

	for _, capability := range Capabilities {
		if slices.Contains(declared, capability) {
			continue
		}

		assert.False(t, codex.Supports(capability), "codex declares no %s yet", capability)

		err := codex.Require(capability)
		require.ErrorIs(t, err, ErrUnsupported)
		assert.Contains(t, err.Error(), string(capability))
	}

	assert.False(t, codex.Supports(Capability("telepathy")), "an unknown capability is never supported")
}

func TestSetupOperationsAreDeclaredIndependently(t *testing.T) {
	cases := []struct {
		name     string
		client   Client
		declared []Capability
	}{
		{
			name:     "registration only",
			client:   Client{ID: "reg", Name: "Reg", Setup: fakeSetup{}, SetupOps: SetupSupport{RegisterMCP: true}},
			declared: []Capability{CapabilitySetup, CapabilityMCPRegistration},
		},
		{
			// Grants apply to a registration that something else made.
			name:     "grants only",
			client:   Client{ID: "grant", Name: "Grant", Setup: fakeSetup{}, SetupOps: SetupSupport{ApproveTools: true}},
			declared: []Capability{CapabilitySetup, CapabilityToolGrants},
		},
		{
			name:     "adapter without optional operations",
			client:   Client{ID: "plain", Name: "Plain", Setup: fakeSetup{}},
			declared: []Capability{CapabilitySetup},
		},
		{
			name:   "no setup support",
			client: Client{ID: "none", Name: "None"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRegistry(tc.client)
			require.NoError(t, err)

			assert.Equal(t, tc.declared, tc.client.Declared())
			checkCapabilityViewsAgree(t, tc.client)
		})
	}
}

func TestSetupOperationsNeedAnAdapter(t *testing.T) {
	for _, ops := range []SetupSupport{
		{Hooks: true},
		{RegisterMCP: true},
		{ApproveTools: true},
		{RegisterMCP: true, ApproveTools: true},
	} {
		_, err := NewRegistry(Client{ID: "broken", Name: "Broken", SetupOps: ops})
		require.Error(t, err, "%+v without an adapter is a broken descriptor", ops)
		assert.NotErrorIs(t, err, ErrUnsupported, "broken configuration is not \"unsupported\"")
		assert.Contains(t, err.Error(), "setup adapter")
	}
}

func TestInspectionKeepsTrustAndVerificationIndependent(t *testing.T) {
	verified, err := VerifiedEvidence("0.42.0", "PreToolUse apply_patch")
	require.NoError(t, err)

	cases := []struct {
		name       string
		inspection CapabilityInspection
	}{
		{
			// A compatibility test for a CLI version does not establish
			// that this project's hooks are trusted.
			name: "current, unknown trust, verified version and surface",
			inspection: CapabilityInspection{
				Capability: CapabilityEdits, Supported: true, State: StateCurrent, Verification: verified,
			},
		},
		{
			name: "current, established trust, unknown verification",
			inspection: CapabilityInspection{
				Capability: CapabilityEdits, Supported: true, State: StateCurrent, Trust: TrustEstablished,
			},
		},
		{
			name:       "absent configuration",
			inspection: CapabilityInspection{Capability: CapabilityCommands, Supported: true, State: StateAbsent},
		},
		{
			name:       "unsupported capability",
			inspection: CapabilityInspection{Capability: CapabilityResets},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.inspection.Validate())
		})
	}

	first := cases[0].inspection
	assert.Equal(t, TrustUnknown, first.Trust, "verified compatibility leaves trust unknown")
	assert.Equal(t, "0.42.0", first.Verification.ClientVersion)
	assert.Equal(t, "PreToolUse apply_patch", first.Verification.Surface)

	second := cases[1].inspection
	assert.Equal(t, VerificationUnknown, second.Verification.Level, "trust is not compatibility evidence")
}

func TestZeroValueEvidenceStaysUnknown(t *testing.T) {
	var inspection CapabilityInspection

	assert.False(t, inspection.Supported)
	assert.Equal(t, StateAbsent, inspection.State)
	assert.Equal(t, TrustUnknown, inspection.Trust)
	assert.Equal(t, VerificationUnknown, inspection.Verification.Level)
	assert.Empty(t, inspection.Verification.ClientVersion)
	require.NoError(t, inspection.Verification.Validate())
}

func TestVerifiedEvidenceNeedsVersionAndSurface(t *testing.T) {
	for _, tc := range [][2]string{{"", "PreToolUse Edit"}, {"2.1.0", ""}, {" ", " "}} {
		_, err := VerifiedEvidence(tc[0], tc[1])
		require.Error(t, err, "version %q surface %q", tc[0], tc[1])
	}

	// A literal cannot skip the constructor's check: the inspection
	// validation that the contract harness runs rejects it too.
	incomplete := CapabilityInspection{
		Capability: CapabilityEdits, Supported: true,
		Verification: VerificationEvidence{Level: VerificationVerified},
	}
	require.Error(t, incomplete.Validate())

	// Other levels may omit the scope.
	require.NoError(t, VerificationEvidence{Level: VerificationPending}.Validate())
}

func TestUnsupportedCapabilityCannotClaimSuccess(t *testing.T) {
	verified, err := VerifiedEvidence("2.1.0", "PostCompact")
	require.NoError(t, err)

	require.Error(t, CapabilityInspection{Capability: CapabilityResets, Trust: TrustEstablished}.Validate())
	require.Error(t, CapabilityInspection{Capability: CapabilityResets, Verification: verified}.Validate())
	require.NoError(t, CapabilityInspection{Capability: CapabilityResets, Trust: TrustPending}.Validate())
}

func TestClaudeInvocationIsPureAndReusesThePreset(t *testing.T) {
	claude, _ := Builtin().Lookup(ClaudeID)
	require.True(t, claude.Supports(CapabilityInvocation))
	assert.Equal(t, []Capability{
		CapabilitySkills, CapabilitySetup, CapabilityMCPRegistration, CapabilityToolGrants,
		CapabilityEdits, CapabilityResets, CapabilityInvocation,
	}, claude.Declared())

	spec, err := claude.Invocation(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, spec.Validate())
	assert.Equal(t, agent.ClaudeCommand(), spec,
		"the registry and the compatibility resolver share one command value")
	assert.Empty(t, spec.Dir, "the legacy preset inherits the caller's working directory")

	// A pure spec never needs the binary: the resolver must work for a
	// dry run on a machine without the client installed.
	spec.Argv[0] = "mutated"
	assert.Equal(t, "claude", agent.ClaudeCommand().Argv[0], "the preset is copied, not shared")
}

func TestSkillDestinationsGroupSharedDirectories(t *testing.T) {
	shared := Client{ID: "third", Name: "Third", SkillDirs: []string{".agents/skills", ".third/skills"}}
	clients := append(Builtin().Clients(), shared)

	got := SkillDestinations(clients)

	assert.Equal(t, []SkillDestination{
		{Dir: ".claude/skills", Consumers: []string{ClaudeID}},
		{Dir: ".agents/skills", Consumers: []string{CodexID, "third"}},
		{Dir: ".third/skills", Consumers: []string{"third"}},
	}, got, "one destination per directory, consumers in client order")

	assert.Empty(t, SkillDestinations(nil))
}

func TestEnumStringsAreStable(t *testing.T) {
	assert.Equal(t, "absent", StateAbsent.String())
	assert.Equal(t, "current", StateCurrent.String())
	assert.Equal(t, "partial", StatePartial.String())
	assert.Equal(t, "conflict", StateConflict.String())
	assert.Equal(t, "unreadable", StateUnreadable.String())
	assert.Equal(t, "unknown", CapabilityState(99).String())

	assert.Equal(t, "unknown", VerificationUnknown.String())
	assert.Equal(t, "unverified", VerificationUnverified.String())
	assert.Equal(t, "pending", VerificationPending.String())
	assert.Equal(t, "verified", VerificationVerified.String())

	assert.Equal(t, "unknown", TrustUnknown.String())
	assert.Equal(t, "pending", TrustPending.String())
	assert.Equal(t, "established", TrustEstablished.String())
	assert.Equal(t, "unknown", TrustState(99).String())

	assert.Equal(t, "info", FindingInfo.String())
	assert.Equal(t, "warning", FindingWarning.String())
	assert.Equal(t, "error", FindingError.String())
	assert.Equal(t, "unknown", FindingLevel(99).String())
}
