// Package inspecttest holds the one fixture matrix that the
// integration, doctor, status, and init tests share. Each fixture puts
// a workspace into one inspection state and states what every consumer
// must report for it, so the four commands are checked against one
// account of setup and never drift apart. It is test support only:
// nothing in the shipped binary imports it.
package inspecttest

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Binary is the seamark path the fixtures install hooks with.
const Binary = "/usr/local/bin/seamark"

// SharedID is the registry ID of the test-only client that shares the
// Codex skill directory. It exists so the matrix can pin how a shared
// destination is reported to every consumer, and how pending trust and
// unknown verification render.
const SharedID = "shared"

// Fixture is one inspection state with the expectations every consumer
// must meet.
type Fixture struct {
	Name string
	// Write puts a workspace into the state. It uses the real setup
	// where it can, so the matrix pins what the adapters read back from
	// what they wrote.
	Write func(root string) error
	// Client is the client the expectations below are about.
	Client string
	// States are the expected states of the named capabilities.
	States map[integration.Capability]integration.CapabilityState
	// GateMode is the expected installed gate mode of the client.
	GateMode string
	// Findings are substrings that the client's finding reasons must
	// contain, one finding per entry.
	Findings []string
	// InitFindings are the substrings init's output must contain instead
	// of Findings, when init changes the state it inspects: setup
	// installs the managed hook, so a definition that ran alone now runs
	// beside it. Nil means Findings.
	InitFindings []string
	// Words are substrings of the client's rendered state that every
	// consumer prints: the hooks phrase, the registration, the grants,
	// and the skills detail (see Rendered). A word about an unsupported
	// capability or a shared directory belongs to the integration test
	// alone: doctor and status print those in their own ways.
	Words []string
	// Skills is a substring of the skills summary line every consumer
	// prints, labelled by destination consumers ("codex+shared 3/3
	// current"); empty when the fixture says nothing about it.
	Skills string
	// Absent are substrings that must appear in no consumer's output:
	// a credential a hook command carries.
	Absent []string
	// Invoker is the expected name of the resolved inference command;
	// empty means the default Claude preset.
	Invoker string
	// InvokerMissing is true when the fixture points the invoker at an
	// executable that is not on PATH.
	InvokerMissing bool
}

// Rendered joins the client's rendered state the way the consumers
// print it, so a fixture's Words can be checked against one string
// here and against each consumer's output there.
func Rendered(insp integration.Inspection) string {
	mcp, _ := insp.Entry(integration.CapabilityMCPRegistration)
	grants, _ := insp.Entry(integration.CapabilityToolGrants)
	skillsEntry, _ := insp.Entry(integration.CapabilitySkills)

	return insp.DescribeHooks() + " · " + mcp.Describe() + " · " + grants.Describe() + " · " + skillsEntry.Describe()
}

// Registry is the built-in registry plus the shared test client.
func Registry() *integration.Registry {
	reg, err := integration.NewRegistry(append(integration.Builtin().Clients(), SharedClient())...)
	if err != nil {
		panic("inspecttest: " + err.Error())
	}

	return reg
}

// SharedClient describes the test-only client: it reads the Codex skill
// directory, decodes edits, and has a setup adapter that reports an
// installed edit hook with pending trust and no native evidence. It
// plans nothing and registers no MCP server.
func SharedClient() integration.Client {
	return integration.Client{
		ID:        SharedID,
		Name:      "Shared Agent",
		SkillDirs: []string{skills.AgentsDir},
		Setup:     sharedSetup{},
		Edits:     sharedEdits{},
	}
}

// sharedSetup reports one installed edit hook whose trust the fake
// client still waits for, with zero-value (unknown) verification.
type sharedSetup struct{}

func (sharedSetup) Inspect(string) integration.Inspection {
	return integration.Inspection{
		ClientID: SharedID,
		Capabilities: []integration.CapabilityInspection{{
			Capability: integration.CapabilityEdits, Supported: true,
			State: integration.StateCurrent, Trust: integration.TrustPending, Detail: "lessons hook installed",
		}},
		Findings: []integration.Finding{{
			Level:  integration.FindingInfo,
			Path:   ".shared/hooks.json",
			Reason: "Shared Agent waits for the user to approve the hook",
			Action: "approve the hook in Shared Agent",
		}},
	}
}

func (sharedSetup) Plan(string, string, integration.ClientSetup) (integration.ClientPlan, error) {
	return integration.ClientPlan{}, nil
}

// sharedEdits is the minimal edit codec the descriptor needs to declare
// the edits capability.
type sharedEdits struct{}

func (sharedEdits) DecodeEdit([]byte) (integration.EditEvent, error) {
	return integration.EditEvent{}, errors.New("not used")
}

func (sharedEdits) EncodeAdvice(text string) (integration.HookReply, error) {
	return integration.HookReply{Stdout: []byte(text)}, nil
}

func (sharedEdits) AdviceMechanism() string { return "shared-stdout" }

// Named returns the fixture with the name; it panics on an unknown
// name, so a test never runs against the wrong state in silence.
func Named(name string) Fixture {
	for _, f := range Fixtures() {
		if f.Name == name {
			return f
		}
	}

	panic("inspecttest: no fixture named " + name)
}

// Secret is the credential the "redacted wrapper" fixture puts into a
// hook command; no consumer may print it.
const Secret = "sk-live-1234567890abcdef"

// Fixtures returns the matrix. The names follow the plan's list:
// absent, current, partial, foreign, malformed, restricted, pending
// trust, unknown verification, shared skills, missing invoker, plus the
// limitations the adapters report: hooks turned off, a handler that
// runs twice, a wrapped handler in the personal file, an enforcing
// inline handler beside the managed warn hook, and a wrapper that
// carries a credential.
func Fixtures() []Fixture {
	return []Fixture{
		{
			Name:   "absent",
			Write:  func(string) error { return nil },
			Client: integration.ClaudeID,
			States: allAbsent(),
			Words:  []string{"no hooks installed", "not registered", "not configured", "not installed"},
		},
		{
			Name: "current",
			Write: func(root string) error {
				return apply(root, integration.ClientSetup{
					ClientID: integration.ClaudeID, Skills: true, Hooks: true, RegisterMCP: true, ApproveTools: true,
				})
			},
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilitySkills: integration.StateCurrent, integration.CapabilityMCPRegistration: integration.StateCurrent,
				integration.CapabilityToolGrants: integration.StateCurrent, integration.CapabilityEdits: integration.StateCurrent,
				integration.CapabilityCommands: integration.StateCurrent, integration.CapabilityResets: integration.StateCurrent,
			},
			GateMode: "warn",
			Words: []string{
				"gate (warn) + lessons hooks installed", `registered in .mcp.json as "seamark"`, "8/8 rules", "3/3 current",
			},
			Skills: "claude 3/3 current",
		},
		{
			Name: "partial",
			Write: func(root string) error {
				if err := apply(root, integration.ClientSetup{ClientID: integration.CodexID, RegisterMCP: true}); err != nil {
					return err
				}

				// The lessons hook alone, as a user who removed the gate hook
				// leaves it.
				return write(root, ".codex/hooks.json", `{"hooks":{"PreToolUse":[{"matcher":"apply_patch","hooks":[`+
					`{"type":"command","command":"`+Binary+` lessons --hook --client codex"}]}]}}`)
			},
			Client: integration.CodexID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityMCPRegistration: integration.StateCurrent, integration.CapabilityToolGrants: integration.StatePartial,
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateAbsent,
			},
			Findings: []string{"reviews and trusts it", "once-per-context"},
			Words:    []string{"lessons hook installed, gate hook missing", "0/5 tools approved"},
		},
		{
			Name: "foreign",
			Write: func(root string) error {
				return write(root, ".claude/skills/seamark-plan-change/SKILL.md",
					"---\nname: seamark-plan-change\ndescription: mine\n---\nMine.\n")
			},
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{integration.CapabilitySkills: integration.StateConflict},
			Words:  []string{"not installed, 1 not managed"},
			Skills: "claude not installed, 1 not managed",
		},
		{
			Name:   "malformed",
			Write:  func(root string) error { return write(root, ".claude/settings.json", "{not json") },
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityToolGrants: integration.StateUnreadable, integration.CapabilityEdits: integration.StateUnreadable,
				integration.CapabilityCommands: integration.StateUnreadable, integration.CapabilityResets: integration.StateUnreadable,
			},
			Words: []string{"hooks unreadable", "unreadable"},
		},
		{
			Name: "restricted",
			Write: func(root string) error {
				return write(root, ".codex/config.toml", "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n"+
					"[mcp_servers.seamark.tools.why]\napproval_mode = \"prompt\"\n")
			},
			Client: integration.CodexID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityMCPRegistration: integration.StateCurrent, integration.CapabilityToolGrants: integration.StateConflict,
			},
			Words: []string{`registered in .codex/config.toml as "seamark"`, `tools.why.approval_mode = "prompt"`},
		},
		{
			Name: "pending trust",
			Write: func(root string) error {
				return apply(root, integration.ClientSetup{ClientID: integration.CodexID, Hooks: true})
			},
			Client: integration.CodexID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateCurrent,
				integration.CapabilityResets: integration.StateAbsent,
			},
			GateMode: "warn",
			Findings: []string{"reviews and trusts it", "once-per-context"},
			Words:    []string{"gate (warn) + lessons hooks installed"},
		},
		{
			Name:   "unknown verification",
			Write:  func(string) error { return nil },
			Client: SharedID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateAbsent,
			},
			Findings: []string{"waits for the user to approve"},
			Words:    []string{"lessons hook installed"},
		},
		{
			Name:   "shared skills",
			Write:  func(root string) error { return installSkills(root, skills.ModeCodex) },
			Client: SharedID,
			States: map[integration.Capability]integration.CapabilityState{integration.CapabilitySkills: integration.StateCurrent},
			Skills: "codex+shared 3/3 current",
		},
		{
			Name: "missing invoker",
			Write: func(root string) error {
				return write(root, ".seamark/config.yaml", "agent:\n  argv: [\"no-such-agent-binary-xyz\"]\n")
			},
			Client:         integration.ClaudeID,
			States:         allAbsent(),
			Invoker:        "custom",
			InvokerMissing: true,
		},
		{
			Name: "disabled",
			Write: func(root string) error {
				if err := apply(root, integration.ClientSetup{ClientID: integration.CodexID, Hooks: true}); err != nil {
					return err
				}

				return write(root, ".codex/config.toml", "[features]\nhooks = false\n")
			},
			Client: integration.CodexID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateCurrent,
			},
			GateMode: "warn",
			Findings: []string{"turns every Codex hook off"},
			Words:    []string{"gate (warn) + lessons hooks installed"},
		},
		{
			Name: "duplicate",
			Write: func(root string) error {
				if err := apply(root, integration.ClientSetup{ClientID: integration.ClaudeID, Hooks: true}); err != nil {
					return err
				}

				return write(root, ".claude/settings.local.json", `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[`+
					`{"type":"command","command":"`+Binary+` lessons --hook"}]}]}}`)
			},
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateCurrent,
			},
			GateMode: "warn",
			Findings: []string{"runs twice"},
			Words:    []string{"gate (warn) + lessons hooks installed"},
		},
		{
			// A wrapped enforcing gate only in the personal file: it runs,
			// it enforces, and setup does not manage it.
			Name: "local wrapper",
			Write: func(root string) error {
				return write(root, ".claude/settings.local.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
					`{"type":"command","command":"test -x `+Binary+` && `+Binary+` gate --enforce --hook"}]}]}}`)
			},
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateAbsent, integration.CapabilityCommands: integration.StateCurrent,
			},
			GateMode:     "enforce",
			Findings:     []string{"setup does not manage that definition"},
			InitFindings: []string{"the managed gate hook runs too, so the hook runs twice", "runs in enforce mode, and both apply"},
			Words:        []string{"gate hook installed (enforce), lessons hook missing"},
		},
		{
			// The managed warn hook beside an enforcing inline hook: both
			// run, and the enforcing one blocks.
			Name: "inline enforce",
			Write: func(root string) error {
				if err := apply(root, integration.ClientSetup{ClientID: integration.CodexID, Hooks: true}); err != nil {
					return err
				}

				return write(root, ".codex/config.toml", "[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n"+
					"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"seamark gate --enforce --hook --client codex\"\n")
			},
			Client: integration.CodexID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityEdits: integration.StateCurrent, integration.CapabilityCommands: integration.StateCurrent,
			},
			GateMode: "enforce",
			Findings: []string{"runs twice"},
			Words:    []string{"gate (enforce) + lessons hooks installed"},
		},
		{
			// A wrapper that carries a credential: the hook is reported, the
			// credential is not.
			Name: "redacted wrapper",
			Write: func(root string) error {
				return write(root, ".claude/settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
					`{"type":"command","command":"hook-runner --token=`+Secret+` `+Binary+` gate --hook"}]}]}}`)
			},
			Client: integration.ClaudeID,
			States: map[integration.Capability]integration.CapabilityState{
				integration.CapabilityCommands: integration.StatePartial,
			},
			Findings: []string{"can run the seamark gate hook"},
			Words:    []string{"gate hook may run from .claude/settings.json"},
			Absent:   []string{Secret},
		},
	}
}

// allAbsent is the state of a fresh workspace for a built-in client.
func allAbsent() map[integration.Capability]integration.CapabilityState {
	out := map[integration.Capability]integration.CapabilityState{}

	for _, capability := range integration.ConfiguredCapabilities {
		out[capability] = integration.StateAbsent
	}

	return out
}

// apply runs the real setup for one client intent.
func apply(root string, intent integration.ClientSetup) error {
	plan, err := integration.PlanSetup(Registry(), integration.SetupRequest{
		Root: root, Binary: Binary, Clients: []integration.ClientSetup{intent},
	})
	if err != nil {
		return err
	}

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{SkillsLog: io.Discard})

	return err
}

// installSkills installs the shipped skills for one legacy mode.
func installSkills(root, mode string) error {
	targets, err := skills.Targets(root, mode)
	if err != nil {
		return err
	}

	return skills.Install(io.Discard, root, targets, false)
}

// write writes one repository-relative file, creating its parents.
func write(root, rel, body string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}

	return os.WriteFile(abs, []byte(body), 0o644)
}
