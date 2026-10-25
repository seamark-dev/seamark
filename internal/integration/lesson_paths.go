package integration

// The lesson-check paths of the capability report (contract K10). Each
// adapter reads its lesson-check and exploration hook entries into
// Inspection.LessonHooks. LessonCheckPaths turns that reading into one
// installed path per client, tool, hook stage, and context, with the
// sources of each reason. lessoncheck.CapabilityReportOf then computes
// the states and the badges. Nothing here runs a binary.

import (
	"debug/buildinfo"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// BinaryIdentity names the running seamark binary. The CLI passes it,
// because the version lives in the unexported cli.version, and this
// package cannot import cli.
type BinaryIdentity struct {
	Path    string // os.Executable with symlinks resolved
	Version string // cli.version
}

// SelfIdentity returns the identity of the running binary with the
// given version. An empty version is read from the build information of
// the running binary, by the rule of the hook binaries. So a caller
// without the CLI version still names the version of a release build.
// A path that os.Executable cannot give stays empty.
func SelfIdentity(version string) BinaryIdentity {
	id := BinaryIdentity{Version: version}

	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}

		id.Path = exe
	}

	if id.Version == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			id.Version = buildVersionOf(info)
		}
	}

	return id
}

// versionSetting is the -ldflags part that sets the CLI version. The
// Makefile builds every release with it, and Go records -ldflags in the
// build information of the binary.
const versionSetting = "github.com/seamark-dev/seamark/internal/cli.version="

// readBuildVersion reads the seamark version of a binary from its build
// information, without running it. It returns "" when the file has no
// readable Go build information or names no version. Tests replace it.
var readBuildVersion = func(path string) string {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return ""
	}

	return buildVersionOf(info)
}

// buildVersionOf returns the version that -ldflags sets, else the main
// module version unless it is "(devel)" (§5 row 116).
func buildVersionOf(info *debug.BuildInfo) string {
	for _, s := range info.Settings {
		if s.Key != "-ldflags" {
			continue
		}

		if i := strings.Index(s.Value, versionSetting); i >= 0 {
			v := s.Value[i+len(versionSetting):]
			if end := strings.IndexAny(v, " \t'\""); end >= 0 {
				v = v[:end]
			}

			if v != "" {
				return v
			}
		}
	}

	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}

	return ""
}

// releaseCore reads the vMAJOR.MINOR.PATCH core of a version. A suffix
// after the core, such as the "-N-g<sha>" of git describe or "-dirty",
// does not change the core, so a build after a tag counts as that tag.
func releaseCore(version string) ([3]int, bool) {
	var core [3]int

	text, ok := strings.CutPrefix(version, "v")
	if !ok {
		return core, false
	}

	if end := strings.IndexAny(text, "-+"); end >= 0 {
		text = text[:end]
	}

	parts := strings.Split(text, ".")
	if len(parts) != len(core) {
		return core, false
	}

	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return core, false
		}

		core[i] = n
	}

	return core, true
}

// binaryReading is the static identification of one hook binary.
type binaryReading struct {
	state   string
	version string
}

// identifyBinary identifies a hook binary statically (contract K10).
// The path comes from a hook document that the repository can commit,
// so the function never runs it. It reads only a regular file with an
// execute bit, because a FIFO or a device would block the read.
//
// A path that is not absolute depends on the environment of the client,
// so it reads as unknown. A stat error other than absence is unknown
// too, never missing: an unreadable file is not an absent one.
func identifyBinary(binary string, self BinaryIdentity) binaryReading {
	if !filepath.IsAbs(binary) {
		return binaryReading{state: lessoncheck.BinaryUnknown}
	}

	info, err := os.Stat(binary)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return binaryReading{state: lessoncheck.BinaryMissing}
	case err != nil:
		return binaryReading{state: lessoncheck.BinaryUnknown}
	case !info.Mode().IsRegular(), runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		return binaryReading{state: lessoncheck.BinaryMissing}
	}

	// The same file needs no read of its content.
	if self.Path != "" {
		if selfInfo, err := os.Stat(self.Path); err == nil && os.SameFile(info, selfInfo) {
			return binaryReading{state: lessoncheck.BinaryCurrent, version: self.Version}
		}
	}

	version := readBuildVersion(binary)

	switch version {
	case "":
		return binaryReading{state: lessoncheck.BinaryUnknown}
	case self.Version:
		return binaryReading{state: lessoncheck.BinaryCurrent, version: version}
	}

	core, ok := releaseCore(version)
	minimum, _ := releaseCore(lessoncheck.MinHandlerRelease)

	switch {
	case !ok:
		return binaryReading{state: lessoncheck.BinaryUnknown, version: version}
	case slices.Compare(core[:], minimum[:]) >= 0:
		return binaryReading{state: lessoncheck.BinaryOther, version: version}
	default:
		return binaryReading{state: lessoncheck.BinaryOld, version: version}
	}
}

// lessonHookClients are the lesson-check hooks of the built-in clients,
// by client ID. setup and the capability report read the same specs.
var lessonHookClients = map[string]lessonHookClient{
	ClaudeID: claudeLessonHooks,
	CodexID:  codexLessonHooks,
}

// uncoveredWriters lists, per client, the writer tools that no
// lesson-check hook covers (contract K10, reason tool-uncovered). The
// report names them, so it never claims coverage that it does not have
// (AC-32).
var uncoveredWriters = map[string][]string{
	ClaudeID: {"NotebookEdit", "Bash"},
	CodexID:  {"shell"},
}

// explorationTool names the exploration path of a client without the
// structured Read path. The path takes the tool of the client that has
// one, so every report names the stage with one tool.
const explorationTool = claudeReadTool

// postEditEvidence is the native evidence that the post-edit reply
// reaches the model. No reviewed recording exists yet, so the level is
// unverified for every path (contracts §5 row 38).
func postEditEvidence() VerificationEvidence {
	return VerificationEvidence{Level: VerificationUnverified}
}

// explorationEvidence is the native evidence of the exploration path.
// It has no reviewed recording yet, so it stays unverified (contract
// L2, plan step P3.14).
func explorationEvidence() VerificationEvidence {
	return VerificationEvidence{Level: VerificationUnverified}
}

// capVerification copies native evidence into the mirror type of the
// report, field for field.
func capVerification(e VerificationEvidence) lessoncheck.CapVerification {
	return lessoncheck.CapVerification{Level: e.Level.String(), ClientVersion: e.ClientVersion, Surface: e.Surface}
}

// lessonHookEntries reads the owned lesson-check and exploration
// commands of one parsed hook document. A command counts by ownership,
// under any matcher and type. Its tools are the tools that the client
// runs it for, so a narrowed matcher shows as a coverage gap.
func lessonHookEntries(document map[string]any, client lessonHookClient) []LessonHookEntry {
	specs := client.specs(hooks.LessonChecksEnforce)

	type stagedSpec struct {
		spec  hooks.Spec
		stage lessoncheck.HookStage
	}

	staged := []stagedSpec{{specs[0], lessoncheck.StagePreWrite}}
	for _, spec := range specs[1:] {
		staged = append(staged, stagedSpec{spec, lessoncheck.StagePostEdit})
	}

	if client.exploration != nil {
		staged = append(staged, stagedSpec{*client.exploration, lessoncheck.StageExploration})
	}

	hookMap, _ := document["hooks"].(map[string]any)

	var entries []LessonHookEntry

	for _, s := range staged {
		eventHooks, _ := hookMap[s.spec.Event].([]any)

		hooks.ForEachCommand(eventHooks, func(matcher string, h map[string]any, cmd string) {
			binary, marker, ok := hooks.OwnedCommand(cmd, s.spec.Markers())
			if !ok {
				return
			}

			entry := LessonHookEntry{Stage: s.stage, Event: s.spec.Event, Marker: marker, Binary: binary}
			if hookType, _ := h["type"].(string); hookType == "command" {
				entry.Tools = firingTools(s.spec, matcher, client.fires)
			}

			entries = append(entries, entry)
		})
	}

	return entries
}

// lessonHookReading returns the reading, or nil when it holds no
// lesson-check hook, wrapper, or unreadable supplemental source. The flag of the project
// configuration alone changes no path, because only an installed path
// carries it.
func lessonHookReading(reading LessonHookInspection) *LessonHookInspection {
	if len(reading.Entries) == 0 && !reading.ReportOnly && !reading.SourceUnreadable {
		return nil
	}

	return &reading
}

// LessonCheckPaths reads the owned lesson-check hook entries of an
// inspection and identifies each hook binary statically. It runs
// nothing. It returns one installed path per tool, hook stage, and
// context, one path without a stage per writer that no hook covers, and
// the attributions of the client, keyed by client ID.
//
// A client that has no lesson-check hook specs gives no path. A
// built-in client without installed hooks gets its paths with reason
// not-installed. The attributions do not depend on the lesson-check
// hooks, because the delivery report reads them for the advisory hooks
// too.
func LessonCheckPaths(insp Inspection, self BinaryIdentity) (
	paths []lessoncheck.InstalledPath, attributions map[string][]lessoncheck.CapAttribution,
) {
	attributions = map[string][]lessoncheck.CapAttribution{}
	if attrs := attributionsOf(insp); attrs != nil {
		attributions[insp.ClientID] = attrs
	}

	client, ok := lessonHookClients[insp.ClientID]
	if !ok {
		return nil, attributions
	}

	r := pathReader{
		id: insp.ClientID, client: client, self: self, binaries: map[string]binaryReading{},
		documentErr: insp.HookDocumentError,
	}

	if insp.LessonHooks != nil {
		r.lessons = *insp.LessonHooks
	}

	// The trust of the lessons hook is the trust of its document, which
	// holds the lesson-check hooks too.
	if edits, ok := insp.Entry(CapabilityEdits); ok {
		r.trust = edits.Trust
	}

	return r.paths(), attributions
}

// pathReader builds the installed paths of one client.
type pathReader struct {
	id       string
	lessons  LessonHookInspection
	client   lessonHookClient
	self     BinaryIdentity
	binaries map[string]binaryReading
	// documentErr is the reason when the hook document cannot be read.
	// The hooks are then unknown, never absent.
	documentErr string
	// trust is the project trust of the hook document.
	trust TrustState
}

// paths returns the pre-write, post-edit, and exploration paths, then
// the uncovered writers.
func (r *pathReader) paths() []lessoncheck.InstalledPath {
	specs := r.client.specs(hooks.LessonChecksEnforce)
	partial := r.partial(specs[1:])

	var out []lessoncheck.InstalledPath

	for _, tool := range specs[0].Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StagePreWrite, context, GuardEvidence(r.id, tool, context))
			r.preWrite(&p, partial)
			out = append(out, p)
		}
	}

	for _, tool := range specs[1].Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StagePostEdit, context, postEditEvidence())
			r.postEdit(&p, specs[1:], partial)
			out = append(out, p)
		}
	}

	out = append(out, r.exploration()...)

	for _, tool := range uncoveredWriters[r.id] {
		for _, context := range lessonContexts {
			out = append(out, lessoncheck.InstalledPath{
				Client: r.id, Tool: tool, Context: context, Reasons: []lessoncheck.CapReason{lessoncheck.CapToolUncovered},
			})
		}
	}

	return out
}

// path returns a path that is not installed yet, with its evidence.
func (r *pathReader) path(tool string, stage lessoncheck.HookStage, context lessoncheck.PathContext,
	evidence VerificationEvidence,
) lessoncheck.InstalledPath {
	return lessoncheck.InstalledPath{
		Client: r.id, Tool: tool, Stage: stage, Context: context, Verification: capVerification(evidence),
	}
}

// partial reports whether one half of the stage pair is installed
// without the other, by the rule of hooks.InstalledLessonChecks.
func (r *pathReader) partial(posts []hooks.Spec) bool {
	pre := slices.ContainsFunc(r.lessons.Entries, func(e LessonHookEntry) bool {
		return e.Stage == lessoncheck.StagePreWrite
	})

	owned := 0

	for _, spec := range posts {
		if slices.ContainsFunc(r.lessons.Entries, func(e LessonHookEntry) bool {
			return e.Stage == lessoncheck.StagePostEdit && e.Event == spec.Event
		}) {
			owned++
		}
	}

	return (pre && owned != len(posts)) || (!pre && owned > 0)
}

// install fills a path from the entries of its stage that the client
// runs for its tool. A guard entry wins over a baseline entry, because
// one guard denies whatever the baseline does. An owned entry of the
// stage that never fires for the tool gives tool-uncovered: a matcher
// edited by hand omits the tool. No entry gives not-installed.
func (r *pathReader) install(p *lessoncheck.InstalledPath, entries []LessonHookEntry) {
	if r.documentErr != "" {
		p.Reasons = append(p.Reasons, lessoncheck.CapDocumentUnreadable)

		return
	}

	var chosen *LessonHookEntry

	for i, e := range entries {
		if !slices.Contains(e.Tools, p.Tool) {
			continue
		}

		if chosen == nil || strings.HasPrefix(e.Marker, lessoncheck.MarkerGuard) {
			chosen = &entries[i]
		}
	}

	switch {
	case chosen != nil:
		reading, ok := r.binaries[chosen.Binary]
		if !ok {
			reading = identifyBinary(chosen.Binary, r.self)
			r.binaries[chosen.Binary] = reading
		}

		p.Installed, p.Marker, p.Binary = true, chosen.Marker, chosen.Binary
		p.BinaryState, p.BinaryVersion = reading.state, reading.version
	case len(entries) > 0:
		p.Reasons = append(p.Reasons, lessoncheck.CapToolUncovered)
	default:
		p.Reasons = append(p.Reasons, lessoncheck.CapNotInstalled)
	}
}

// stageEntries returns the entries of one stage.
func (r *pathReader) stageEntries(stage lessoncheck.HookStage) []LessonHookEntry {
	var out []LessonHookEntry

	for _, e := range r.lessons.Entries {
		if e.Stage == stage {
			out = append(out, e)
		}
	}

	return out
}

// preWrite fills one pre-write path. A wrapper that discards the exit
// status keeps every pre-write path inactive, also beside an owned
// entry. The guard cannot project a Codex update with hunks yet. The
// report keeps that reason only while an enforce pattern pin exists.
func (r *pathReader) preWrite(p *lessoncheck.InstalledPath, partial bool) {
	r.install(p, r.stageEntries(lessoncheck.StagePreWrite))
	r.shared(p, partial)

	if r.lessons.ReportOnly {
		p.Reasons = append(p.Reasons, lessoncheck.CapReportOnlyWrapper)
	}

	if p.Tool == r.client.patchTool {
		p.Reasons = append(p.Reasons, lessoncheck.CapContentUnprojectable)
	}
}

// postEdit fills one post-edit path. Claude Code reports a failed tool
// through its own event, so the path needs an entry on every post-edit
// event. A path with some of them runs, and it carries partial-install.
func (r *pathReader) postEdit(p *lessoncheck.InstalledPath, posts []hooks.Spec, partial bool) {
	entries := r.stageEntries(lessoncheck.StagePostEdit)
	r.install(p, entries)

	if p.Installed {
		for _, spec := range posts {
			if !slices.ContainsFunc(entries, func(e LessonHookEntry) bool {
				return e.Event == spec.Event && slices.Contains(e.Tools, p.Tool)
			}) {
				partial = true
			}
		}
	}

	r.shared(p, partial)
}

// exploration returns the exploration paths. A client without the
// structured Read path gets paths without a reason and without
// evidence, which the report reads as unsupported (§5 row 42).
func (r *pathReader) exploration() []lessoncheck.InstalledPath {
	var out []lessoncheck.InstalledPath

	if r.client.exploration == nil {
		for _, context := range lessonContexts {
			out = append(out, lessoncheck.InstalledPath{
				Client: r.id, Tool: explorationTool, Stage: lessoncheck.StageExploration, Context: context,
				Verification: capVerification(VerificationEvidence{}),
			})
		}

		return out
	}

	for _, tool := range r.client.exploration.Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StageExploration, context, explorationEvidence())
			r.install(&p, r.stageEntries(lessoncheck.StageExploration))
			r.shared(&p, false)
			out = append(out, p)
		}
	}

	return out
}

// shared adds the reasons of the hook sources that limit every installed
// path: a partial pair, the project trust, and hooks that the project
// configuration turns off.
func (r *pathReader) shared(p *lessoncheck.InstalledPath, partial bool) {
	if !p.Installed {
		return
	}

	if partial {
		p.Reasons = append(p.Reasons, lessoncheck.CapPartialInstall)
	}

	switch {
	case r.trust == TrustPending:
		p.Reasons = append(p.Reasons, lessoncheck.CapUntrusted)
	case r.client.trustReview && r.trust == TrustUnknown:
		p.Reasons = append(p.Reasons, lessoncheck.CapTrustUnknown)
	}

	if r.lessons.HooksDisabled {
		p.Reasons = append(p.Reasons, lessoncheck.CapHooksDisabled)
	}

	if r.lessons.SourceUnreadable {
		p.Reasons = append(p.Reasons, lessoncheck.CapDocumentUnreadable)
	}
}

// LessonCapability joins the inspections that the caller holds with
// lessons.yaml at root into the capability report. It derives nothing
// itself: LessonCheckPaths reads the paths and the attributions, and
// lessoncheck.CapabilityReportOf computes every state and badge. Doctor,
// status, --set-mode, preparation, the MCP server, and the console call
// it, so they report one account.
func LessonCapability(insps []Inspection, root string, self BinaryIdentity) lessoncheck.CapabilityReport {
	cfg, errs := lessoncheck.LoadForCapability(root)

	return LessonCapabilityOf(insps, cfg, errs, self)
}

// LessonCapabilityOf is LessonCapability on a configuration that the
// caller already read with lessoncheck.LoadForCapability. Doctor and
// status read the file once for the report and for their printing rule.
func LessonCapabilityOf(insps []Inspection, cfg *lessoncheck.Config, errs []lessoncheck.ConfigError,
	self BinaryIdentity,
) lessoncheck.CapabilityReport {
	var installed []lessoncheck.InstalledPath

	attributions := map[string][]lessoncheck.CapAttribution{}

	for _, insp := range insps {
		paths, attrs := LessonCheckPaths(insp, self)
		installed = append(installed, paths...)

		for id, list := range attrs {
			attributions[id] = list
		}
	}

	return lessoncheck.CapabilityReportOf(cfg, errs, installed, attributions)
}

// LessonHooksInstalled reports whether the inspection reads an owned
// lesson-check or exploration entry. Doctor and status print their
// lesson-check lines when one exists for some client (contract K10).
func (i Inspection) LessonHooksInstalled() bool {
	return i.LessonHooks != nil && len(i.LessonHooks.Entries) > 0
}

// LessonGuardInstalled reports whether the inspection reads an owned
// guard entry. An installed guard denies every covered edit while the
// configuration does not validate, so doctor fails only then.
func (i Inspection) LessonGuardInstalled() bool {
	return i.LessonHooks != nil && slices.ContainsFunc(i.LessonHooks.Entries, func(e LessonHookEntry) bool {
		return e.Stage == lessoncheck.StagePreWrite && strings.HasPrefix(e.Marker, lessoncheck.MarkerGuard)
	})
}
