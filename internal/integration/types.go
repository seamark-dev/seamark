// Package integration holds the client registry and the adapter
// contracts that make a coding agent (Claude Code, Codex, …) a local
// contribution: one descriptor, one registration. The package owns
// client identity, capability declarations, and native payload and
// configuration translation. It never owns lesson ranking, policy
// rules, or proposal validation; those stay in the shared engines.
//
// Dependency direction is deliberate. This package may import agent,
// skills, approve, hooks, and gate contracts. It must not import the
// CLI, doctor, status, MCP, report, or distill packages, because they
// consume the registry and an import in the other direction is a cycle.
package integration

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Capability names one optional thing a client adapter can do. A
// client declares a capability through the matching descriptor field;
// an empty field means the client does not support it. There is no
// blanket "supports hooks" flag, because edit advice, command gating,
// and context resets are separate native surfaces. There is no blanket
// "supports MCP" flag either: registration and tool grants are separate
// setup operations.
type Capability string

// The capabilities a descriptor can declare, in presentation order.
const (
	// CapabilitySkills consumes skills from the declared skill directories.
	CapabilitySkills Capability = "skills"
	// CapabilitySetup plans the client's native configuration documents.
	CapabilitySetup Capability = "setup"
	// CapabilityMCPRegistration registers the seamark MCP server natively.
	CapabilityMCPRegistration Capability = "mcp-registration"
	// CapabilityToolGrants adds the exact per-tool grants. It does not
	// imply CapabilityMCPRegistration: a client can grant access to a
	// registration that another tool or the user made.
	CapabilityToolGrants Capability = "tool-grants"
	// CapabilityEdits decodes edit events and encodes advisory context.
	CapabilityEdits Capability = "edits"
	// CapabilityCommands decodes shell events and encodes gate decisions.
	CapabilityCommands Capability = "commands"
	// CapabilityResets decodes context-reset events.
	CapabilityResets Capability = "resets"
	// CapabilityInvocation builds the one-shot inference command.
	CapabilityInvocation Capability = "invocation"
)

// Capabilities lists every capability in stable presentation order.
var Capabilities = []Capability{
	CapabilitySkills, CapabilitySetup, CapabilityMCPRegistration, CapabilityToolGrants,
	CapabilityEdits, CapabilityCommands, CapabilityResets, CapabilityInvocation,
}

// ConfiguredCapabilities lists the capabilities that have project
// configuration an inspection can classify, in presentation order.
// Setup is the umbrella for the three setup operations, and invocation
// has no project document: the invoker resolver reports it. Both are
// declared, never configured.
var ConfiguredCapabilities = []Capability{
	CapabilitySkills, CapabilityMCPRegistration, CapabilityToolGrants,
	CapabilityEdits, CapabilityCommands, CapabilityResets,
}

// SetupSupport declares which optional setup operations the client's
// SetupAdapter can plan. One adapter composes all native documents, so
// the operations are metadata, not one interface per operation. The
// field names match the ClientSetup intents they answer.
type SetupSupport struct {
	// Hooks means the adapter can install the client's lifecycle hooks.
	// It is a setup operation, not a capability: the codecs that handle
	// the hook events are declared through Edits, Commands, and Resets.
	Hooks bool `json:"hooks"`
	// GateHook means the installed hooks include the command gate, so a
	// gate mode applies to the client. A client can install lesson hooks
	// and no gate hook, and init must then print no gate mode for it.
	GateHook bool `json:"gate_hook"`
	// ResetHook means the installed hooks include the context reset. A
	// client whose reset has no observable effect installs none, and
	// diagnostics must then not call the absent hook missing.
	ResetHook bool `json:"reset_hook"`
	// RegisterMCP means the adapter can register the seamark MCP server.
	RegisterMCP bool `json:"register_mcp"`
	// ApproveTools means the adapter can add the exact per-tool grants.
	// It is independent of RegisterMCP.
	ApproveTools bool `json:"approve_tools"`
}

// Client describes one coding agent. Built-in descriptors are assembled
// explicitly in the registry; there is no init-time registration and no
// dynamic loading. Optional capabilities are nil when absent.
//
// The registry copies SkillDirs at its boundaries, so callers cannot
// change registered data. The registry does not copy adapter values:
// reflection or a generic deep copy would add machinery for no current
// need. An adapter implementation must therefore be immutable after
// registration.
type Client struct {
	// ID is the stable identifier used by `--client` and by `agent.cli`
	// in .seamark/config.yaml: lowercase letters, digits, and hyphens.
	ID string
	// Name is the display name for narration ("Claude Code").
	Name string
	// SkillDirs lists the repository-relative, slash-separated skill
	// directories the client reads. A directory can be shared between
	// clients; the destination identity is the directory, not the client.
	// A non-empty list declares CapabilitySkills; there is no separate
	// flag that could disagree with the list.
	SkillDirs []string
	// Setup plans native configuration. Nil when the client has none.
	Setup SetupAdapter
	// SetupOps declares the optional operations Setup can plan. The
	// registry rejects a declared operation when Setup is nil.
	SetupOps SetupSupport
	// Edits translates native edit events and advisory replies.
	Edits EditHooks
	// Commands translates native shell events and gate decisions.
	Commands CommandHooks
	// Resets translates native context-reset events.
	Resets ResetHooks
	// Invocation returns the one-shot inference command for a workspace
	// root. It is pure: no PATH lookup, no login, no process start.
	Invocation func(root string) (agent.CommandSpec, error)
}

// Supports reports whether the descriptor declares a capability.
func (c Client) Supports(capability Capability) bool {
	switch capability {
	case CapabilitySkills:
		return len(c.SkillDirs) > 0
	case CapabilitySetup:
		return c.Setup != nil
	case CapabilityMCPRegistration:
		return c.SetupOps.RegisterMCP
	case CapabilityToolGrants:
		return c.SetupOps.ApproveTools
	case CapabilityEdits:
		return c.Edits != nil
	case CapabilityCommands:
		return c.Commands != nil
	case CapabilityResets:
		return c.Resets != nil
	case CapabilityInvocation:
		return c.Invocation != nil
	default:
		return false
	}
}

// Declared lists the capabilities the descriptor declares, in the
// order of Capabilities, for diagnostics and contract tests.
func (c Client) Declared() []Capability {
	var out []Capability

	for _, capability := range Capabilities {
		if c.Supports(capability) {
			out = append(out, capability)
		}
	}

	return out
}

// clone returns the descriptor with its own SkillDirs backing array.
// The registry calls it at every boundary, so neither the registering
// caller nor a reader can change registered skill destinations.
func (c Client) clone() Client {
	c.SkillDirs = slices.Clone(c.SkillDirs)

	return c
}

// SetupAdapter plans a client's native configuration. Plan reads only;
// the shared setup coordinator guards inputs, deduplicates skill
// destinations, and applies writes. Inspect is offline and read-only.
type SetupAdapter interface {
	// Inspect reports the current configuration state without writing.
	Inspect(root string) Inspection
	// ManagedGateMode returns the mode of the gate hook that setup
	// manages: warn, enforce, or "" when none runs or the adapter has no
	// gate hook. It reads the managed hook document alone. init calls it
	// before it plans, and the plan reads every document again, so a full
	// Inspect here doubles the reads. The value must equal Inspect's
	// ManagedGateMode and the mode that Plan keeps, so the adapter reads
	// all three through one helper.
	ManagedGateMode(root string) string
	// Plan composes every edit to the client's native documents for one
	// request. Each native document appears at most once in Writes.
	Plan(root, binary string, request ClientSetup) (ClientPlan, error)
}

// EditHooks translates a client's edit lifecycle. DecodeEdit turns one
// native payload into the complete set of affected paths; EncodeAdvice
// turns rendered advisory text into the native, nonblocking reply.
//
// AdviceMechanism names the native path that EncodeAdvice uses to reach
// the agent, for example "pre-tool-use-context". The firing log records
// it beside the client, so statistics can tell two delivery paths
// apart. The name is a short constant: lowercase letters, digits, and
// hyphens. It names the path only and claims nothing about attention.
type EditHooks interface {
	DecodeEdit(payload []byte) (EditEvent, error)
	EncodeAdvice(text string) (HookReply, error)
	AdviceMechanism() string
}

// CommandHooks translates a client's shell-command lifecycle for the
// gate. DecodeCommand turns one native payload into the shell text;
// ErrNotApplicable names an event of another tool, which the gate
// never evaluates. EncodeDecision renders a decision: a reply with a
// nonzero ExitCode blocks the tool with Stderr as the reason, and
// `seamark gate` then exits 2 for every client. EncodeFailure renders
// a gate failure in the native blocking form; the caller decides
// whether the mode requires blocking.
type CommandHooks interface {
	DecodeCommand(payload []byte) (CommandEvent, error)
	EncodeDecision(decision *gate.Decision) (HookReply, error)
	EncodeFailure(err error) HookReply
}

// ResetHooks translates a client's context-reset lifecycle.
type ResetHooks interface {
	DecodeReset(payload []byte) (ResetEvent, error)
}

// ReceivingContext identifies the conversation that can see injected
// advice. It is the actual receiver, not merely a shared parent
// session: a subagent reports the session id of its parent, and that
// id alone is not a context of its own. An adapter that cannot name
// the actual receiver leaves the context nil, and suppression stays off.
type ReceivingContext struct {
	// ID is the client-native identity, kept in memory only. Persisted
	// state stores a repository-scoped digest, never the raw value.
	ID string
	// Resettable is true when the adapter's compatibility checks
	// established that a reset event reaches this exact context.
	Resettable bool
}

// EventMeta carries the identity every native event shares. Values are
// bounded input from the client, kept in memory for one hook run.
type EventMeta struct {
	// SessionID is the client's session identifier, useful for audit
	// even when it is insufficient for suppression.
	SessionID string
	// MatchID identifies the native tool call (Claude tool_use_id, Codex
	// tool_use_id) so repeated matches join in the audit log.
	MatchID string
	// NativeTool is the client's tool name as reported ("Edit",
	// "apply_patch"), recorded for statistics.
	NativeTool string
	// CWD is the event working directory, used to resolve relative paths
	// inside the workspace.
	CWD string
	// Context is the receiving context, or nil when the adapter cannot
	// identify one. Nil disables once-per-context suppression.
	Context *ReceivingContext
}

// EditEvent is one normalized edit opportunity: the complete set of
// paths the native operation affects. A partially parsed patch must
// never become an EditEvent; the decoder returns an error instead.
type EditEvent struct {
	EventMeta
	// Paths lists the affected paths as the client reported them. The
	// shared normalization resolves them against CWD and the workspace,
	// preserving order and removing duplicates.
	Paths []string
}

// CommandEvent is one validated shell-command event. Command is the
// shell text the gate evaluates; patch text never reaches this type.
type CommandEvent struct {
	EventMeta
	Command string
}

// ResetEvent marks a context whose delivery generation becomes eligible
// again after a native reset such as compaction.
type ResetEvent struct {
	Context ReceivingContext
}

// HookReply is what a hook run writes back to the client. Advice is
// always nonblocking; only the gate uses a blocking exit code.
type HookReply struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CapabilityState classifies the on-disk configuration of one
// capability. It is separate from whether the client supports the
// capability at all, from project trust, and from native verification.
type CapabilityState int

// The configuration states an inspection reports.
const (
	// StateAbsent means no seamark-owned configuration is present.
	StateAbsent CapabilityState = iota
	// StateCurrent means the owned configuration matches what init writes.
	StateCurrent
	// StatePartial means some owned entries are present or outdated.
	StatePartial
	// StateConflict means foreign or restrictive settings prevent setup.
	StateConflict
	// StateUnreadable means the native document cannot be read or parsed.
	StateUnreadable
)

// capabilityStateNames are the JSON and narration names, by value.
var capabilityStateNames = []string{"absent", "current", "partial", "conflict", "unreadable"}

// String names a state for narration and test output.
func (s CapabilityState) String() string {
	return enumName(capabilityStateNames, int(s))
}

// MarshalText writes the name, so JSON consumers read "current", not 1.
func (s CapabilityState) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText reads a name written by MarshalText.
func (s *CapabilityState) UnmarshalText(text []byte) error {
	v, err := enumValue(capabilityStateNames, "capability state", text)
	*s = CapabilityState(v)

	return err
}

// TrustState classifies whether the user trusts the current project's
// native configuration in the client (for example, reviewed hooks).
// Seamark only reports trust; it never grants it.
type TrustState int

// The trust states an inspection reports.
const (
	// TrustUnknown means nothing is known; never treat it as trusted.
	TrustUnknown TrustState = iota
	// TrustPending means the client still waits for the user's review.
	TrustPending
	// TrustEstablished means the client records the project as trusted.
	TrustEstablished
)

// trustStateNames are the JSON and narration names, by value.
var trustStateNames = []string{"unknown", "pending", "established"}

// String names a trust state for narration and test output.
func (s TrustState) String() string {
	return enumName(trustStateNames, int(s))
}

// MarshalText writes the name, so JSON consumers read "pending", not 1.
func (s TrustState) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// UnmarshalText reads a name written by MarshalText.
func (s *TrustState) UnmarshalText(text []byte) error {
	v, err := enumValue(trustStateNames, "trust state", text)
	*s = TrustState(v)

	return err
}

// Verification classifies native compatibility evidence for a
// capability. Missing evidence is never reported as verified.
type Verification int

// The verification levels an inspection reports.
const (
	// VerificationUnknown means nothing is known; treat as unverified.
	VerificationUnknown Verification = iota
	// VerificationUnverified means no native check covers the capability.
	VerificationUnverified
	// VerificationPending means a check is defined but not yet run.
	VerificationPending
	// VerificationVerified means a recorded native check passed for the
	// tested client version and surface.
	VerificationVerified
)

// verificationNames are the JSON and narration names, by value.
var verificationNames = []string{"unknown", "unverified", "pending", "verified"}

// String names a verification level for narration and test output.
func (v Verification) String() string {
	return enumName(verificationNames, int(v))
}

// MarshalText writes the name, so JSON consumers read "verified", not 3.
func (v Verification) MarshalText() ([]byte, error) {
	return []byte(v.String()), nil
}

// UnmarshalText reads a name written by MarshalText.
func (v *Verification) UnmarshalText(text []byte) error {
	n, err := enumValue(verificationNames, "verification", text)
	*v = Verification(n)

	return err
}

// VerificationEvidence is the native compatibility evidence for one
// capability. Its scope is the tested client version and surface only.
// It does not show that the current project's hooks are trusted or
// operational; TrustState reports project trust separately. The zero
// value means unknown.
type VerificationEvidence struct {
	Level Verification `json:"level"`
	// ClientVersion is the client version the recorded check ran against.
	ClientVersion string `json:"client_version,omitempty"`
	// Surface names the native surface the check exercised, for example
	// "PreToolUse apply_patch".
	Surface string `json:"surface,omitempty"`
}

// VerifiedEvidence builds evidence at VerificationVerified. It rejects
// a blank version or surface, because a verified claim without its
// scope reads as a claim about every version.
func VerifiedEvidence(clientVersion, surface string) (VerificationEvidence, error) {
	e := VerificationEvidence{Level: VerificationVerified, ClientVersion: clientVersion, Surface: surface}

	if err := e.Validate(); err != nil {
		return VerificationEvidence{}, err
	}

	return e, nil
}

// Validate checks that verified evidence names its tested version and
// surface. Other levels carry optional scope, so they always pass.
func (e VerificationEvidence) Validate() error {
	if e.Level != VerificationVerified {
		return nil
	}

	if strings.TrimSpace(e.ClientVersion) == "" {
		return errors.New("verified evidence names no client version")
	}

	if strings.TrimSpace(e.Surface) == "" {
		return errors.New("verified evidence names no surface")
	}

	return nil
}

// CapabilityInspection is the state of one capability for one client.
// Supported, State, Trust, and Verification are independent dimensions:
// a current configuration can have unknown trust, and verified
// compatibility says nothing about this project's trust.
//
// The JSON names are part of `seamark status --json`; they are additive
// and must stay stable.
type CapabilityInspection struct {
	Capability Capability `json:"capability"`
	// Supported is whether the descriptor declares the capability.
	Supported bool            `json:"supported"`
	State     CapabilityState `json:"state"`
	// Trust is the project's native trust state for this capability.
	Trust TrustState `json:"trust"`
	// Verification is the native compatibility evidence.
	Verification VerificationEvidence `json:"verification"`
	// Detail is a bounded, sanitized explanation for narration. It is
	// supplemental: consumers must read the typed fields, never parse it.
	// It never names the client: the consumer adds the client label.
	Detail string `json:"detail,omitempty"`
	// Action is the command or edit that changes the state, from the
	// same adapter knowledge that installs the artifact; empty when
	// nothing needs to change. init, doctor, and status print it as
	// the corrective action, so the three never disagree on the fix.
	Action string `json:"action,omitempty"`
}

// Validate checks one capability inspection. It rejects incomplete
// verified evidence. It also rejects established trust or verified
// evidence on an unsupported capability, because both claim success
// for a surface the client does not have.
func (i CapabilityInspection) Validate() error {
	if err := i.Verification.Validate(); err != nil {
		return fmt.Errorf("capability %s: %w", i.Capability, err)
	}

	if i.Supported {
		return nil
	}

	if i.Trust == TrustEstablished {
		return fmt.Errorf("capability %s: unsupported but trust is established", i.Capability)
	}

	if i.Verification.Level == VerificationVerified {
		return fmt.Errorf("capability %s: unsupported but evidence is verified", i.Capability)
	}

	return nil
}

// Inspection is the offline, read-only view of one client's setup. An
// adapter reports the capabilities it configures; Registry.Inspect
// completes the view with the name, the declared capabilities, the
// setup operations, and the shared skill destinations, in the order of
// ConfiguredCapabilities. Narration reads the typed fields only: an
// absent hook is missing when Setup says setup installs it, and absent
// by design otherwise.
type Inspection struct {
	ClientID string `json:"client"`
	// Name is the display name, filled by the registry.
	Name string `json:"name,omitempty"`
	// Declared lists every capability the descriptor declares, filled by
	// the registry. It covers the capabilities without project
	// configuration too (setup, invocation), which Capabilities omits.
	Declared []Capability `json:"declared,omitempty"`
	// Setup is what setup installs for the client, filled by the
	// registry, so narration can tell a missing hook from one that setup
	// never installs.
	Setup        SetupSupport           `json:"setup"`
	Capabilities []CapabilityInspection `json:"capabilities"`
	// GateMode is the mode the client's gate hooks run in, from every
	// definition that certainly runs one, managed or not. It is enforce
	// when any enforces, and warn when any follows the policy file. It is
	// GateModeReportOnly when each one discards its exit status. It is
	// empty when none runs or the document cannot be read. One enforcing
	// definition blocks whatever the managed hook says. The repository
	// policy file can still enforce on top of a warn hook, and never on
	// top of a report-only one.
	GateMode string `json:"gate_mode,omitempty"`
	// PossibleGateMode is the mode of the definitions that can run a
	// gate hook, by the rule of GateMode. The reader cannot tell whether
	// they do. It is empty without such a definition. A reader must not
	// take it for GateMode. A gate that may enforce is not one that
	// enforces, and not one that never blocks.
	PossibleGateMode string `json:"possible_gate_mode,omitempty"`
	// ManagedGateMode is the mode of the hook setup manages, or empty.
	// Setup keeps it when no mode is requested; it never reads a mode
	// from a definition it does not own.
	ManagedGateMode string `json:"managed_gate_mode,omitempty"`
	// HookDocumentError is the reason when the hook document that setup
	// manages cannot be read, and empty otherwise. The reason names the
	// document and is sanitized. The hook entries carry the same reason
	// in their detail, after the word "unreadable". Status repeats it in
	// its legacy gate_hook_error field, so no consumer reads it back from
	// the detail.
	HookDocumentError string `json:"hook_document_error,omitempty"`
	// Findings name the limitations and the duplicate handlers the
	// adapter sees: trust it cannot read, a receiving context it does not
	// identify, a second source that runs the same hook.
	Findings []Finding `json:"findings,omitempty"`
}

// FindingLevel grades a finding for presentation.
type FindingLevel int

// The finding levels.
const (
	// FindingInfo is informational; nothing needs to change.
	FindingInfo FindingLevel = iota
	// FindingWarning names a limitation the user should know.
	FindingWarning
	// FindingError names a state that stops setup or breaks a capability.
	FindingError
)

// findingLevelNames are the JSON and narration names, by value.
var findingLevelNames = []string{"info", "warning", "error"}

// String names a level for narration and test output.
func (l FindingLevel) String() string {
	return enumName(findingLevelNames, int(l))
}

// MarshalText writes the name, so JSON consumers read "warning", not 1.
func (l FindingLevel) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// UnmarshalText reads a name written by MarshalText.
func (l *FindingLevel) UnmarshalText(text []byte) error {
	v, err := enumValue(findingLevelNames, "finding level", text)
	*l = FindingLevel(v)

	return err
}

// enumName returns the name of an enum value, or "unknown" for a value
// outside the table. The typed enums share it, so every one of them
// names an unknown value the same way.
func enumName(names []string, v int) string {
	if v < 0 || v >= len(names) {
		return "unknown"
	}

	return names[v]
}

// enumValue returns the value of an enum name. It rejects "unknown" for
// an enum whose table does not hold it, and every other name outside
// the table: a JSON document from a newer binary must fail to decode,
// not decode to the zero value in silence.
func enumValue(names []string, kind string, text []byte) (int, error) {
	i := slices.Index(names, string(text))
	if i < 0 {
		return 0, fmt.Errorf("unknown %s %q", kind, text)
	}

	return i, nil
}

// Finding is one structured observation with its corrective action, so
// init, doctor, and status can narrate the same fact the same way.
type Finding struct {
	Level FindingLevel `json:"level"`
	// Path is the repository-relative document the finding is about.
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	// Action is the command or edit that resolves the finding; empty
	// when nothing needs to change.
	Action string `json:"action,omitempty"`
}

// ClientSetup is the per-client intent of one setup run. Legacy init
// forms and explicit `--client` selection both translate into it, so
// the orchestration never inspects flags.
type ClientSetup struct {
	ClientID string
	// Skills installs the client's skill directories.
	Skills bool
	// Hooks installs the client's lifecycle hooks.
	Hooks bool
	// RegisterMCP registers the seamark MCP server natively.
	RegisterMCP bool
	// ApproveTools adds the exact per-tool grants (opt-in).
	ApproveTools bool
	// GateMode is warn or enforce; empty preserves the installed mode.
	GateMode string
	// CheckHookSources makes the adapter inspect the client's other hook
	// sources for seamark handlers. An init run without --client leaves
	// it off: that form never read those files, and it must stay as it
	// was.
	CheckHookSources bool
}

// SetupRequest is one complete setup run. Selection is resolved once,
// in registry order, before planning.
type SetupRequest struct {
	Root    string
	Binary  string
	Clients []ClientSetup
	// Common lists the client-independent documents of the run, such as
	// the .seamark scaffolds and the .gitignore entries. The caller owns
	// their content; the coordinator guards, plans, and writes them with
	// the client documents, so one preflight covers every write.
	Common []Document
}

// Narrator prints the lines for one finished operation in init's
// vocabulary. The status is kept, planned (a preview), or applied; the
// caller prints failed and not-attempted operations itself. An adapter
// supplies a narrator when its document needs more than one generic
// line, for example the list of hook commands. The text lives with the
// adapter, because only the adapter knows what it composed.
type Narrator func(w io.Writer, status OpStatus)

// Document is one client-independent file of a setup run. Compose
// receives the current bytes and returns the bytes the run leaves. When
// it returns the input unchanged, the coordinator keeps the file. Compose
// must be pure: the coordinator may call it for a preview.
type Document struct {
	// Path is repository-relative and slash-separated.
	Path string
	// CreateOnly marks a starter file. An existing path is kept without a
	// read, whatever it is; Compose then only supplies the new content.
	CreateOnly bool
	// Compose returns the complete file. exists is false for an absent
	// file; existing is then nil.
	Compose func(existing []byte, exists bool) ([]byte, error)
	// Detail says what the write adds, for narration. KeptDetail says
	// why an unchanged file needs nothing.
	Detail     string
	KeptDetail string
	// Narrate replaces the generic line when set.
	Narrate Narrator
}

// GuardKind names how much of a file a guard observes. The kinds exist
// because the files of a setup run differ in what setup may do to them.
type GuardKind int

// The guard kinds.
const (
	// GuardDocument guards a native document that setup owns and may
	// rewrite. No component of its path may be a symbolic link, for the
	// read too: a link committed in a cloned repository must never
	// redirect a read or a write of a client's configuration.
	GuardDocument GuardKind = iota
	// GuardInput guards a file that setup reads and at most extends, such
	// as a registration file it only takes a name from. The read may
	// follow a link. A write through a link is still refused.
	GuardInput
	// GuardPresence guards a create-only file by its existence alone. An
	// existing file is kept whatever it is, even when it cannot be read,
	// because setup never looks inside a file it must not clobber.
	GuardPresence
)

// FileGuard records the observed state of one input file, so apply can
// detect a change between plan and write and abort as stale.
type FileGuard struct {
	Path   string
	Kind   GuardKind
	Exists bool
	// Linked is the first component of the path that is a symbolic link,
	// or empty. Setup never writes a linked path.
	Linked string
	SHA256 [32]byte
	Mode   fs.FileMode
}

// FileWrite is one composed native document to write, with every
// client that contributed to it.
type FileWrite struct {
	Path  string
	After []byte
	// Mode is the permission for a new file. An existing file keeps its
	// own permission. Zero means 0644.
	Mode      fs.FileMode
	Consumers []string
	// Detail says what the write adds, for narration. It must never hold
	// file content: a preview must not print native configuration.
	Detail string
	// Narrate replaces the generic line when set.
	Narrate Narrator
}

// FileKeep is one document the plan inspected and leaves unchanged, so
// the result can say "kept" with the reason.
type FileKeep struct {
	Path      string
	Consumers []string
	// Detail says why nothing changes, for narration.
	Detail string
	// Narrate replaces the generic line when set.
	Narrate Narrator
}

// GateModeReportOnly is the mode of a gate hook whose definition
// discards the exit status of the hook, for example with "|| true" or
// "&". The hook runs and reports its verdicts. A verdict blocks only by
// exit status 2, so none blocks, whatever --enforce or the policy file
// says. Only a definition that setup does not manage has this mode.
const GateModeReportOnly = "report-only"

// GateHook is one command-gate hook that the client really runs after
// the plan is applied. A plan lists every one it knows: the hook setup
// manages, and a hook in another source that setup found and left as it
// is. The caller's gate summary reads the list, because one enforcing
// hook blocks whatever the managed hook's mode says, and a summary that
// names only the managed mode would then be false.
type GateHook struct {
	// ClientID is filled by the coordinator.
	ClientID string
	// Path is the document that holds the hook, repository-relative.
	Path string
	// Mode is warn, enforce, or GateModeReportOnly. Only a hook that
	// setup does not manage can be report-only.
	Mode string
	// Managed is true for the hook setup installs and updates. False
	// means another source, which setup never edits.
	Managed bool
	// Uncertain is true when the reader cannot tell whether the
	// definition runs the gate. For example, another program gets the
	// gate command as its arguments. Mode is then the mode the
	// definition has when it runs.
	Uncertain bool
}

// ClientPlan is one client's read guards, composed writes, kept
// documents, and findings. A native document appears once in Writes or
// once in Kept, never in both, and every such document has a guard in
// Reads. A file that was only read, for example to find a registration
// name, has a guard and no other entry.
type ClientPlan struct {
	Reads     []FileGuard
	Writes    []FileWrite
	Kept      []FileKeep
	GateHooks []GateHook
	Findings  []Finding
}

// SkillDestination is one skill directory and every selected client
// that reads it. The directory is written once for all consumers.
type SkillDestination struct {
	Dir       string
	Consumers []string
}

// SkillGuard records the observed state of one skill directory: a
// digest of everything a refresh can overwrite. The plan entry's
// classification is not enough, because an edited stale copy is still
// stale.
type SkillGuard struct {
	// Rel is the skill directory, repository-relative.
	Rel    string
	Digest [32]byte
}

// SetupPlan is the complete, validated plan for one setup run. It is
// also the preview: it holds every change and nothing was written.
type SetupPlan struct {
	// Root is the workspace the plan was made for.
	Root string
	// Reads guards every input. Its order is the document order of the
	// run: common documents first, then each client in registry order.
	Reads  []FileGuard
	Writes []FileWrite
	Kept   []FileKeep
	Skills []skills.Entry
	// SkillGuards is parallel to Skills: one guard per entry.
	SkillGuards  []SkillGuard
	Destinations []SkillDestination
	// GateHooks lists the gate hooks the selected clients run after apply.
	GateHooks []GateHook
	Findings  []Finding
}
