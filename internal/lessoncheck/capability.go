package lessoncheck

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// CapabilitySchema names the format of the capability report.
const CapabilitySchema = "lesson-capability/1"

// MinHandlerRelease is the first seamark release whose binary has the
// lesson-check hook handlers. A hook binary of an older release cannot
// run them. The value is a placeholder until the release that ships the
// handlers sets its tag.
const MinHandlerRelease = "v0.9.0"

// The installed lesson-check modes of a client.
const (
	InstalledOff     InstalledMode = "off"
	InstalledCheck   InstalledMode = "check"
	InstalledEnforce InstalledMode = "enforce"
	InstalledPartial InstalledMode = "partial"
)

// The hook stages of a capability path.
const (
	StagePreWrite    HookStage = "pre-write"
	StagePostEdit    HookStage = "post-edit"
	StageExploration HookStage = "exploration"
)

// The context paths. The values equal the lanes of the receiving
// context, so a lane converts to a context without a table.
const (
	ContextRoot  PathContext = "root"
	ContextChild PathContext = "child"
)

// The path states, from the most permissive to the most restrictive.
const (
	PathActive           PathState = "active"
	PathActiveUnverified PathState = "active-unverified"
	PathInactive         PathState = "inactive"
	PathUnsupported      PathState = "unsupported"
)

// The capability reasons. The inspection puts some of them on the
// installed paths, and CapabilityReportOf adds the others.
// inactiveReasons lists the reasons that make a path inactive.
const (
	CapNotInstalled            CapReason = "not-installed"
	CapCheckOnlyInstalled      CapReason = "check-only-installed"
	CapReportOnlyWrapper       CapReason = "report-only-wrapper"
	CapUntrusted               CapReason = "untrusted"
	CapTrustUnknown            CapReason = "trust-unknown"
	CapHooksDisabled           CapReason = "hooks-disabled"
	CapOtherBinary             CapReason = "other-binary"
	CapBinaryMissing           CapReason = "binary-missing"
	CapVersionOld              CapReason = "capability-version-old"
	CapCapabilityUnknown       CapReason = "capability-unknown"
	CapDocumentUnreadable      CapReason = "document-unreadable"
	CapPartialInstall          CapReason = "partial-install"
	CapNativeDenyUnverified    CapReason = "native-deny-unverified"
	CapNativeTimeoutUnverified CapReason = "native-timeout-unverified"
	CapContentUnprojectable    CapReason = "content-unprojectable"
	CapConfigurationError      CapReason = "configuration-error"
	CapToolUncovered           CapReason = "tool-uncovered"
)

// inactiveReasons are the reasons that make a path inactive. Every
// other reason keeps a path at most active-unverified.
var inactiveReasons = []CapReason{
	CapNotInstalled, CapReportOnlyWrapper, CapUntrusted, CapHooksDisabled, CapDocumentUnreadable,
	CapConfigurationError, CapToolUncovered, CapVersionOld, CapCapabilityUnknown, CapBinaryMissing,
}

// The binary states of a hook command, from the static identification
// of the hook binary.
const (
	BinaryCurrent = "current"
	BinaryOther   = "other"
	BinaryOld     = "old"
	BinaryUnknown = "unknown"
	BinaryMissing = "missing"
)

// The badges of a pin for one client.
const (
	BadgeEnforced           = "enforced"
	BadgeEnforcedUnverified = "enforced-unverified"
	BadgeChecked            = "checked"
	BadgeRequestedInactive  = "requested-inactive"
	BadgeAdvisory           = "advisory"
)

// The attribution items, in the order of the report.
const (
	ItemMatchIdentity AttributionItem = "match-identity"
	ItemLaneIdentity  AttributionItem = "lane-identity"
	ItemContextReset  AttributionItem = "context-reset"
	ItemTurnBoundary  AttributionItem = "turn-boundary"
	ItemModelBoundary AttributionItem = "model-boundary"
)

// AttributionItems lists every attribution item in report order.
var AttributionItems = []AttributionItem{
	ItemMatchIdentity, ItemLaneIdentity, ItemContextReset, ItemTurnBoundary, ItemModelBoundary,
}

// The states of an attribution.
const (
	AttrVerified    = "verified"
	AttrUnverified  = "unverified"
	AttrUnsupported = "unsupported"
)

// LevelVerified is the verification level word of verified evidence. A
// path or an attribution is verified only at this level.
const LevelVerified = "verified"

// MarkerGuard and MarkerBaseline start the pre-write markers of every
// client. A Codex marker adds " --client codex". The report reads the
// installed mode from them, because InstalledPath has no mode field.
const (
	MarkerGuard    = "lessons --guard --hook"
	MarkerBaseline = "lessons --baseline --hook"
)

// LoadForCapability reads lessons.yaml for the capability report. It
// differs from Load only for a file that does not read or decode (tier
// 1). Such a file gives an empty Config that keeps the digest of the
// bytes, and one decode-error entry in the errors. The report then names
// the file as invalid in the form of the verification result. So the
// report never reads a broken file as absent.
func LoadForCapability(root string) (*Config, []ConfigError) {
	data, err := os.ReadFile(configPath(root))

	return ParseForCapability(data, err)
}

// ParseForCapability is LoadForCapability on an existing file-read result.
// readErr carries absence (os.ErrNotExist) and unreadability exactly as
// the file read does. The caller can share these bytes with other readers.
func ParseForCapability(data []byte, readErr error) (*Config, []ConfigError) {
	loaded := parseLoadedConfig(data, readErr)
	if loaded.cfg != nil {
		return loaded.cfg, loaded.report.Errors
	}

	cfg, _ := newConfig(reviews.DefaultConfig(), loaded.report.SHA256)

	return cfg, loaded.report.Errors
}

// CapabilityReportOf joins the configuration and the installed paths.
// cfgErrs are the tier-1 and tier-2 errors of the configuration,
// version-unsupported included. attributions is keyed by client ID, and
// the report copies each list unchanged.
//
// The installed paths come from integration.LessonCheckPaths. A path
// without a hook stage is a writer that no lesson-check hook covers, and
// the report lists it under uncovered_writers. A path that is not
// installed and has no reason is a path that the client cannot run, and
// its state is unsupported.
//
// The clients keep the order of their first path, and a client with
// attributions only follows in ID order. Every slice of the result is
// non-nil, so an empty list encodes as [].
func CapabilityReportOf(cfg *Config, cfgErrs []ConfigError, installed []InstalledPath,
	attributions map[string][]CapAttribution,
) CapabilityReport {
	report := CapabilityReport{
		Schema:    CapabilitySchema,
		Config:    configReportOf(cfg, cfgErrs),
		Clients:   []ClientCapability{},
		Pins:      []PinCapability{},
		Uncovered: []UncoveredWriter{},
	}

	join := capabilityJoin{invalid: len(cfgErrs) > 0, patternPin: enforcesPatternPin(cfg)}

	var clients []clientJoin

	for _, id := range clientOrder(installed, attributions) {
		c := clientJoin{capability: ClientCapability{
			Client: id, Paths: []PathCapability{}, Attributions: append([]CapAttribution{}, attributions[id]...),
		}}

		for _, p := range installed {
			if p.Client != id {
				continue
			}

			if p.Stage == "" {
				report.Uncovered = append(report.Uncovered, UncoveredWriter{Client: id, Tool: p.Tool, Context: p.Context})

				continue
			}

			c.read(p)
			c.capability.Paths = append(c.capability.Paths, join.path(p))
		}

		c.capability.Installed = c.mode()
		clients = append(clients, c)
		report.Clients = append(report.Clients, c.capability)
	}

	if cfg == nil {
		return report
	}

	for _, pin := range cfg.Pins {
		if pin.Mode == ModeAdvisory && pin.CheckID == "" {
			continue
		}

		report.Pins = append(report.Pins, pinCapabilityOf(cfg, pin, clients))
	}

	return report
}

// configReportOf is the configuration part of the report, in the form
// of the verification result.
func configReportOf(cfg *Config, errs []ConfigError) ConfigReport {
	report := ConfigReport{Valid: len(errs) == 0, Errors: errs}

	if cfg != nil {
		report.SHA256 = cfg.SHA256

		if cfg.Raw != nil {
			report.LessonChecksVersion = cfg.Raw.LessonChecksVersion
		}
	}

	return report
}

// enforcesPatternPin reports whether the configuration holds an enforce
// pin whose check is of kind no-added-pattern/1. The guard cannot
// project the content of a Codex update with hunks yet, so it denies
// such an update in the scope of that pin.
func enforcesPatternPin(cfg *Config) bool {
	if cfg == nil || cfg.Raw == nil {
		return false
	}

	return slices.ContainsFunc(cfg.Pins, func(pin Pin) bool {
		def, ok := cfg.Raw.Checks[pin.CheckID]

		return pin.Mode == ModeEnforce && ok && Kind(def.Kind) == KindNoAddedPattern
	})
}

// clientOrder returns the client IDs: first the clients of the paths in
// path order, then the clients with attributions only, sorted.
func clientOrder(installed []InstalledPath, attributions map[string][]CapAttribution) []string {
	var ids []string

	for _, p := range installed {
		if !slices.Contains(ids, p.Client) {
			ids = append(ids, p.Client)
		}
	}

	var rest []string

	for id := range attributions {
		if !slices.Contains(ids, id) {
			rest = append(rest, id)
		}
	}

	slices.Sort(rest)

	return append(ids, rest...)
}

// capabilityJoin holds the facts of the configuration that the state of
// a path needs.
type capabilityJoin struct {
	// invalid is true when the configuration does not validate. An
	// installed guard then denies every covered edit.
	invalid bool
	// patternPin is true when an enforce no-added-pattern/1 pin exists.
	patternPin bool
}

// path computes the state and the reasons of one path. The most
// restrictive state wins: inactive over active-unverified over active.
// A path is active only with verified evidence and no reason.
func (j capabilityJoin) path(p InstalledPath) PathCapability {
	out := PathCapability{
		Tool: p.Tool, HookStage: p.Stage, Context: p.Context,
		BinaryVersion: p.BinaryVersion, Verification: p.Verification,
	}

	// LessonCheckPaths gives a reason to every path that the client can
	// run but that is not installed. So a path with neither an install
	// nor a reason is one that the client cannot run.
	if !p.Installed && len(p.Reasons) == 0 {
		out.State = PathUnsupported

		return out
	}

	verified := p.Verification.Level == LevelVerified
	reasons := slices.DeleteFunc(slices.Clone(p.Reasons), func(r CapReason) bool {
		// The guard denies an update with hunks only in the scope of an
		// enforce pattern pin, so the reason holds only while one exists.
		return r == CapContentUnprojectable && (!j.patternPin || !p.Installed || !IsGuardMarker(p.Marker))
	})

	if p.Installed {
		reasons = append(reasons, binaryReasons(p.BinaryState)...)

		// Only a reviewed native recording shows that the client honors a
		// deny and what it does when the guard exceeds its timeout.
		if p.Stage == StagePreWrite && IsGuardMarker(p.Marker) && !verified {
			reasons = append(reasons, CapNativeDenyUnverified, CapNativeTimeoutUnverified)
		}
	}

	if j.invalid && (p.Stage == StagePreWrite || p.Stage == StagePostEdit) {
		reasons = append(reasons, CapConfigurationError)
	}

	out.Reasons = sortedCapReasons(reasons)

	switch {
	case !p.Installed || slices.ContainsFunc(out.Reasons, isInactiveReason):
		out.State = PathInactive
	case !verified || len(out.Reasons) > 0:
		out.State = PathActiveUnverified
	default:
		out.State = PathActive
	}

	return out
}

// binaryReasons returns the reasons of a binary state. An unread state
// counts as unknown, because a path must never read as current without
// a check.
func binaryReasons(state string) []CapReason {
	switch state {
	case BinaryCurrent:
		return nil
	case BinaryOther:
		return []CapReason{CapOtherBinary}
	case BinaryOld:
		return []CapReason{CapOtherBinary, CapVersionOld}
	case BinaryMissing:
		return []CapReason{CapOtherBinary, CapBinaryMissing}
	default:
		return []CapReason{CapOtherBinary, CapCapabilityUnknown}
	}
}

// isInactiveReason reports whether a reason makes a path inactive.
func isInactiveReason(r CapReason) bool {
	return slices.Contains(inactiveReasons, r)
}

// IsGuardMarker reports whether a marker is the pre-write marker of
// enforce. It matches a prefix, because a Codex marker adds a client
// selector. The inspection and doctor also call it, so one rule reads
// the installed mode.
func IsGuardMarker(marker string) bool {
	return strings.HasPrefix(marker, MarkerGuard)
}

// IsBaselineMarker reports whether a marker is the pre-write marker of
// check. It matches a prefix for the same reason as IsGuardMarker.
func IsBaselineMarker(marker string) bool {
	return strings.HasPrefix(marker, MarkerBaseline)
}

// sortedCapReasons sorts and deduplicates reasons. An empty list gives
// nil, so the JSON omits the key.
func sortedCapReasons(reasons []CapReason) []CapReason {
	if len(reasons) == 0 {
		return nil
	}

	out := slices.Clone(reasons)
	slices.Sort(out)

	return slices.Compact(out)
}

// clientJoin is the report of one client and the installed facts that
// the badges need beside the path states.
type clientJoin struct {
	capability ClientCapability
	// guard and baseline are true when an installed pre-write path runs
	// the guard or the baseline marker.
	guard, baseline bool
	// post is true when an installed post-edit path exists.
	post bool
	// partial is true when a path carries partial-install.
	partial bool
}

// read records the installed facts of one path.
func (c *clientJoin) read(p InstalledPath) {
	c.partial = c.partial || slices.Contains(p.Reasons, CapPartialInstall)

	if !p.Installed {
		return
	}

	switch p.Stage {
	case StagePreWrite:
		c.guard = c.guard || IsGuardMarker(p.Marker)
		c.baseline = c.baseline || IsBaselineMarker(p.Marker)
	case StagePostEdit:
		c.post = true
	}
}

// mode returns the installed mode of the client. Enforce wins over
// check, because one guard denies whatever the baseline does. A client
// with only one of the pre-write and the post-edit hook is partial, and
// so is a client with a partial-install reason.
func (c clientJoin) mode() InstalledMode {
	pre := c.guard || c.baseline

	switch {
	case c.partial, pre != c.post:
		return InstalledPartial
	case c.guard:
		return InstalledEnforce
	case c.baseline:
		return InstalledCheck
	default:
		return InstalledOff
	}
}

// pinCapabilityOf joins one pin with the paths of every client. A client
// without a path has no lesson-check hook to report, so it gets no badge.
func pinCapabilityOf(cfg *Config, pin Pin, clients []clientJoin) PinCapability {
	out := PinCapability{
		PinRef: pin.Ref, PinVersion: pin.Version, Requested: pin.Mode, Clients: []PinClient{},
	}

	if pin.CheckID != "" {
		ref := rawCheckRef(cfg.Raw, pin.CheckID)
		out.Check = &ref
	}

	for _, c := range clients {
		if len(c.capability.Paths) == 0 {
			continue
		}

		out.Clients = append(out.Clients, badgeOf(pin.Mode, c))
	}

	return out
}

// badgeOf computes the badge of a pin mode for one client. An enforce
// pin needs every pre-write path, in every context, and a check pin
// every post-edit path. No path of the stage may be inactive or
// unsupported.
func badgeOf(mode Mode, c clientJoin) PinClient {
	out := PinClient{Client: c.capability.Client}

	switch mode {
	case ModeEnforce:
		// A baseline records and never denies. So a client with a baseline
		// and no guard cannot enforce the pin, whatever the state of its
		// paths.
		if c.baseline && !c.guard {
			out.Badge, out.Reasons = BadgeRequestedInactive, []CapReason{CapCheckOnlyInstalled}

			return out
		}

		out.Badge, out.Reasons = stageBadge(c.capability.Paths, StagePreWrite, BadgeEnforced, BadgeEnforcedUnverified)
		if out.Badge == BadgeEnforced {
			out.Limits = c.capability.Limits(StagePreWrite)
		}
	case ModeCheck:
		out.Badge, out.Reasons = stageBadge(c.capability.Paths, StagePostEdit, BadgeChecked, BadgeChecked)
	default:
		out.Badge = BadgeAdvisory
	}

	return out
}

// stageBadge reads the paths of one stage. verified is the badge when
// each path is active with verified evidence, and unverified the badge
// when each path is active or active-unverified. Any other path gives
// requested-inactive, and so does a stage without a path. The reasons
// are the sorted union of the paths.
func stageBadge(paths []PathCapability, stage HookStage, verified, unverified string) (string, []CapReason) {
	var (
		reasons  []CapReason
		all      = true
		allValid = true
		found    bool
	)

	for _, p := range paths {
		if p.HookStage != stage {
			continue
		}

		found = true
		reasons = append(reasons, p.Reasons...)

		switch {
		case p.State == PathActive && p.Verification.Level == LevelVerified:
		case p.State == PathActive || p.State == PathActiveUnverified:
			all = false
		default:
			all, allValid = false, false
		}
	}

	reasons = sortedCapReasons(reasons)

	switch {
	case !found || !allValid:
		return BadgeRequestedInactive, reasons
	case all:
		return verified, reasons
	default:
		return unverified, reasons
	}
}

// Pin returns the capability of the pin with the printed pin-ref, and
// false when the report holds none: an advisory pin without a check.
func (r CapabilityReport) Pin(ref string) (PinCapability, bool) {
	i := slices.IndexFunc(r.Pins, func(p PinCapability) bool { return p.PinRef == ref })
	if i < 0 {
		return PinCapability{}, false
	}

	return r.Pins[i], true
}

// ActiveText returns the one-line active state of a pin, for the
// checker impact block. A pin that no client covers reads "requested;
// inactive: <reasons>", the same form as ActiveRequested. Else the text
// names the badge of each client.
func (r CapabilityReport) ActiveText(ref string) string {
	pin, fixed := r.joinedPin(ref)
	if fixed != "" {
		return fixed
	}

	var (
		badges  []string
		reasons []CapReason
		covered bool
	)

	for _, c := range pin.Clients {
		badge := c.Client + " " + c.Badge
		if len(c.Limits) > 0 {
			badge += " (" + LimitText(c.Limits) + ")"
		}

		badges = append(badges, badge)
		reasons = append(reasons, c.Reasons...)
		covered = covered || c.Badge != BadgeRequestedInactive
	}

	if !covered {
		return "requested; inactive: " + JoinCapReasons(sortedCapReasons(reasons))
	}

	return "requested; " + strings.Join(badges, ", ")
}

// ActiveLines returns the active state of a pin as lines: one line per
// client, with its badge, and for a covered pin the state of each tool
// of the hook stage that the mode needs. A client that does not cover
// the pin reads "requested; inactive: <reasons>".
func (r CapabilityReport) ActiveLines(ref string) []string {
	pin, fixed := r.joinedPin(ref)
	if fixed != "" {
		return []string{"active: " + fixed}
	}

	stage := StagePreWrite
	if pin.Requested == ModeCheck {
		stage = StagePostEdit
	}

	lines := make([]string, 0, len(pin.Clients))

	for _, pc := range pin.Clients {
		if pc.Badge == BadgeRequestedInactive {
			lines = append(lines, "active: "+pc.Client+" requested; inactive: "+JoinCapReasons(pc.Reasons))

			continue
		}

		line := "active: " + pc.Client + " " + pc.Badge
		if note := pc.Note(); note != "" {
			line += " (" + note + ")"
		}

		if c, ok := r.Client(pc.Client); ok {
			line += " · " + c.StageText(stage)
		}

		lines = append(lines, line)
	}

	return lines
}

// joinedPin returns the capability of a pin and the fixed text of a pin
// without a client badge. An advisory or unknown pin gets
// ActiveAdvisory, and a pin that no client reports gets ActiveRequested.
// fixed is "" when a client badge exists. ActiveText and ActiveLines
// share it, so the one line and the client lines always agree.
func (r CapabilityReport) joinedPin(ref string) (pin PinCapability, fixed string) {
	pin, ok := r.Pin(ref)

	switch {
	case !ok || pin.Requested == ModeAdvisory:
		return pin, ActiveAdvisory
	case len(pin.Clients) == 0:
		return pin, ActiveRequested
	}

	return pin, ""
}

// Client returns the capability of one client, and false when the
// report holds none.
func (r CapabilityReport) Client(id string) (ClientCapability, bool) {
	i := slices.IndexFunc(r.Clients, func(c ClientCapability) bool { return c.Client == id })
	if i < 0 {
		return ClientCapability{}, false
	}

	return r.Clients[i], true
}

// Note returns the text that follows the badge in parentheses: the
// reasons, then the limits. It is "" when the client has neither. The
// surfaces that print a badge share it, so a limit never goes missing
// from one of them.
func (c PinClient) Note() string {
	var parts []string

	if len(c.Reasons) > 0 {
		parts = append(parts, JoinCapReasons(c.Reasons))
	}

	if len(c.Limits) > 0 {
		parts = append(parts, LimitText(c.Limits))
	}

	return strings.Join(parts, "; ")
}

// LimitText joins the limits of a claim after the word "limit:", for
// example "limit: the client lets the write run after its 10 s hook
// timeout".
func LimitText(limits []string) string {
	return "limit: " + strings.Join(limits, "; ")
}

// Limits returns the distinct limits of the active paths of one stage
// with verified evidence, in path order. Only such a path makes a
// verified claim, so only its limit narrows one.
func (c ClientCapability) Limits(stage HookStage) []string {
	var out []string

	for _, p := range c.Paths {
		limit := pathLimit(p)
		if p.HookStage == stage && limit != "" && !slices.Contains(out, limit) {
			out = append(out, limit)
		}
	}

	return out
}

// pathLimit returns the limit of an active path with verified evidence,
// and "" for any other path.
func pathLimit(p PathCapability) string {
	if p.State != PathActive || p.Verification.Level != LevelVerified {
		return ""
	}

	return p.Verification.Limit
}

// HasStage reports whether the client has a path of the stage that it
// can run.
func (c ClientCapability) HasStage(stage HookStage) bool {
	return slices.ContainsFunc(c.Paths, func(p PathCapability) bool {
		return p.HookStage == stage && p.State != PathUnsupported
	})
}

// StageText summarizes the paths of one stage, for example "pre-write
// Edit, Write, MultiEdit active-unverified". A tool whose contexts share
// a state reads once. Else each context reads as "<tool> in <context>".
// Tools of one state form a group, and "; " separates the groups. An
// inactive group names its reasons, and an active group names the limit
// of its verified claim. A stage other than pre-write with
// one group names no tool, because its tools repeat the pre-write tools.
func (c ClientCapability) StageText(stage HookStage) string {
	type group struct {
		state   PathState
		reasons string
		limit   string
		labels  []string
	}

	var groups []group

	add := func(label string, p PathCapability) {
		text := ""
		if p.State == PathInactive {
			text = JoinCapReasons(p.Reasons)
		}

		limit := pathLimit(p)

		i := slices.IndexFunc(groups, func(g group) bool {
			return g.state == p.State && g.reasons == text && g.limit == limit
		})
		if i < 0 {
			groups = append(groups, group{state: p.State, reasons: text, limit: limit})
			i = len(groups) - 1
		}

		groups[i].labels = append(groups[i].labels, label)
	}

	for _, tool := range c.stageTools(stage) {
		var paths []PathCapability

		for _, p := range c.Paths {
			if p.HookStage == stage && p.Tool == tool {
				paths = append(paths, p)
			}
		}

		if slices.ContainsFunc(paths, func(p PathCapability) bool {
			return p.State != paths[0].State || !slices.Equal(p.Reasons, paths[0].Reasons) ||
				pathLimit(p) != pathLimit(paths[0])
		}) {
			for _, p := range paths {
				add(tool+" in "+string(p.Context), p)
			}

			continue
		}

		add(tool, paths[0])
	}

	parts := make([]string, 0, len(groups))

	for _, g := range groups {
		text := string(g.state)
		if g.reasons != "" {
			text += " (" + g.reasons + ")"
		}

		if g.limit != "" {
			text += " (" + LimitText([]string{g.limit}) + ")"
		}

		if stage == StagePreWrite || len(groups) > 1 {
			text = strings.Join(g.labels, ", ") + " " + text
		}

		parts = append(parts, text)
	}

	if len(parts) == 0 {
		return string(stage) + " " + string(PathUnsupported)
	}

	return string(stage) + " " + strings.Join(parts, "; ")
}

// stageTools returns the tools of the stage's paths in path order.
func (c ClientCapability) stageTools(stage HookStage) []string {
	var tools []string

	for _, p := range c.Paths {
		if p.HookStage == stage && !slices.Contains(tools, p.Tool) {
			tools = append(tools, p.Tool)
		}
	}

	return tools
}

// JoinCapReasons joins reasons with ", ".
func JoinCapReasons(reasons []CapReason) string {
	words := make([]string, 0, len(reasons))
	for _, r := range reasons {
		words = append(words, string(r))
	}

	return strings.Join(words, ", ")
}

// UsesLessonChecks reports whether lessons.yaml uses a lesson-check key:
// a checks map, a check or enforce mode, or a check reference. A file
// with a semantic error counts too, because only these keys give one.
// Doctor and status print their lesson-check lines only then, or when a
// lesson-check or exploration hook is installed. A file that does not
// decode (tier 1) does not count, so a repository without these keys
// keeps its doctor and status bytes.
func UsesLessonChecks(cfg *Config, errs []ConfigError) bool {
	if slices.ContainsFunc(errs, func(e ConfigError) bool { return e.Code != CodeDecodeError }) {
		return true
	}

	return cfg != nil && cfg.Raw != nil && usesLessonChecks(cfg.Raw)
}

// PinModeCounts counts the pins of each normalized mode. A pin with an
// unknown mode counts as advisory, because the advisory hooks deliver it.
func PinModeCounts(cfg *Config) (enforce, check, advisory int) {
	if cfg == nil {
		return 0, 0, 0
	}

	for _, pin := range cfg.Pins {
		switch pin.Mode {
		case ModeEnforce:
			enforce++
		case ModeCheck:
			check++
		default:
			advisory++
		}
	}

	return enforce, check, advisory
}

// InactiveClients returns the clients, in report order, on which some
// pin of the mode has the badge requested-inactive.
func (r CapabilityReport) InactiveClients(mode Mode) []string {
	var ids []string

	for _, c := range r.Clients {
		if slices.ContainsFunc(r.Pins, func(p PinCapability) bool {
			return p.Requested == mode && slices.ContainsFunc(p.Clients, func(pc PinClient) bool {
				return pc.Client == c.Client && pc.Badge == BadgeRequestedInactive
			})
		}) {
			ids = append(ids, c.Client)
		}
	}

	return ids
}

// ConfigText is the configuration part of the doctor detail and of the
// status pins line: "config valid" or "config invalid". A newer
// lesson_checks_version adds a note, because the advisory hooks still
// deliver every pin of such a file.
func (r CapabilityReport) ConfigText() string {
	if r.Config.Valid {
		return "config valid"
	}

	text := "config invalid"

	if slices.ContainsFunc(r.Config.Errors, func(e ConfigError) bool { return e.Code == CodeVersionUnsupported }) {
		text += " (lesson_checks_version " + strconv.Itoa(r.Config.LessonChecksVersion) +
			" is not supported; advisory pins still deliver)"
	}

	return text
}
