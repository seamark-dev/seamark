package lessoncheck

// This file declares the data types of the capability report
// (lesson-capability/1). CapabilityReportOf in capability.go fills them.
// The lesson preparation of the delivery package carries the pin badges.
// Each closed list is in the comment of its type.

// InstalledMode is the lesson-check setup of one client. Closed list:
// "off", "check", "enforce", "partial".
type InstalledMode string

// HookStage is the hook that a capability path belongs to. Closed list:
// "pre-write", "post-edit", "exploration".
type HookStage string

// PathContext is the context path of a capability entry. Closed list:
// "root", "child". A nested child reports as child, because the hook
// payload names the agent ID only.
type PathContext string

// PathState is the state of one capability path. Closed list: "active",
// "active-unverified", "inactive", "unsupported".
type PathState string

// CapReason tells why a path is not plainly active. Closed list:
// "not-installed", "check-only-installed", "report-only-wrapper",
// "untrusted", "trust-unknown", "hooks-disabled", "other-binary",
// "binary-missing", "capability-version-old", "capability-unknown",
// "document-unreadable", "partial-install", "native-deny-unverified",
// "native-timeout-unverified", "content-unprojectable",
// "configuration-error", "tool-uncovered".
type CapReason string

// CapVerification has the same fields, JSON names, and level words as
// integration.VerificationEvidence. integration imports lessoncheck, so
// lessoncheck cannot import integration. The inspection therefore copies
// the evidence into this type. The client version is the verified one,
// because inspection runs no client binary.
type CapVerification struct {
	Level         string `json:"level"`                    // closed: "unknown", "unverified", "pending", "verified"
	ClientVersion string `json:"client_version,omitempty"` // the client version the recorded check ran against
	Surface       string `json:"surface,omitempty"`        // the native surface the check exercised
}

// AttributionItem is one identity or timing fact that the delivery
// report needs from a client path. Closed list:
//
//	"match-identity"  the hook payload tool-use ID equals the transcript's
//	"lane-identity"   the hook payload names the receiving lane
//	"context-reset"   reset events reach the reset hook
//	"turn-boundary"   an injection of one turn reaches the model request of the next turn
//	"model-boundary"  a hook result reaches a new model request inside one turn
type AttributionItem string

// CapAttribution is the state of one attribution item in one context.
type CapAttribution struct {
	Item         AttributionItem `json:"item"`
	Context      PathContext     `json:"context"`
	State        string          `json:"state"` // closed: "verified", "unverified", "unsupported"
	Verification CapVerification `json:"verification"`
}

// InstalledPath is one installed or missing hook path of a client, as
// the inspection reads it. It is an input of the report, so it has no
// JSON form.
type InstalledPath struct {
	Client, Tool  string
	Stage         HookStage
	Context       PathContext
	Installed     bool
	Marker        string // the owned command tail found
	Binary        string // binary path of the hook command
	BinaryVersion string // read statically; "" when unknown
	BinaryState   string // closed: "current", "other", "old", "unknown", "missing"
	Reasons       []CapReason
	Verification  CapVerification
}

// CapabilityReport is the lesson-capability/1 document: the requested
// modes of the configuration against the installed hook paths.
type CapabilityReport struct {
	Schema    string             `json:"schema"` // "lesson-capability/1"
	Config    ConfigReport       `json:"config"`
	Clients   []ClientCapability `json:"clients"`
	Pins      []PinCapability    `json:"pins"`
	Uncovered []UncoveredWriter  `json:"uncovered_writers"`
}

// ClientCapability is the setup and the paths of one client.
type ClientCapability struct {
	Client       string           `json:"client"`
	Installed    InstalledMode    `json:"installed"`
	Paths        []PathCapability `json:"paths"`
	Attributions []CapAttribution `json:"attributions"` // one per item and context
}

// PathCapability is the state of one tool, hook stage, and context.
type PathCapability struct {
	Tool          string          `json:"tool"`
	HookStage     HookStage       `json:"hook_stage"`
	Context       PathContext     `json:"context"`
	State         PathState       `json:"state"`
	Reasons       []CapReason     `json:"reasons,omitempty"`
	BinaryVersion string          `json:"binary_version,omitempty"` // the identified hook executable
	Verification  CapVerification `json:"verification"`
}

// PinCapability is the requested mode of one pin and its state per
// client.
type PinCapability struct {
	PinRef     string      `json:"pin_ref"`
	PinVersion string      `json:"pin_version"`
	Requested  Mode        `json:"requested"`
	Check      *CheckRef   `json:"check,omitempty"`
	Clients    []PinClient `json:"clients"`
}

// PinClient is the badge of one pin for one client.
type PinClient struct {
	Client  string      `json:"client"`
	Badge   string      `json:"badge"` // closed: "enforced", "enforced-unverified", "checked", "requested-inactive", "advisory"
	Reasons []CapReason `json:"reasons,omitempty"`
}

// UncoveredWriter names a writer path that no lesson-check hook covers,
// so the report never claims coverage that it does not have.
type UncoveredWriter struct {
	Client  string      `json:"client"`
	Tool    string      `json:"tool"` // for example "NotebookEdit", "Bash", "shell"
	Context PathContext `json:"context"`
}
