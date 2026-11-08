package integration

// The lesson-check paths of the capability report. Each adapter reads
// its lesson-check and exploration hook entries into
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
// given version. For an empty version, it reads the build information
// of the running binary with buildVersionOf, as identifyBinary does for
// a hook binary. So a caller without the CLI version still names the
// version of a release build. A path that os.Executable cannot give
// stays empty.
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
// module version. A "(devel)" module version names a local build
// without a release version, so it gives "".
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
// after the core does not change it, for example the "-N-g<sha>" of git
// describe or "-dirty". So a build after a tag counts as that tag.
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

// statHookBinary decides whether a hook binary can start, without
// running it. It returns the file information of a binary that can
// start, and an empty state. Otherwise it returns BinaryUnknown or
// BinaryMissing. A relative path depends on the environment of the
// client, so it is unknown. Absence is missing. Another stat error is
// unknown, because an unreadable file is not an absent one. A file that
// is not regular, or that has no execute bit, is missing, because the
// client cannot start it. identifyBinary and the lifecycle hook
// inspection share this rule, so they never disagree about one path.
func statHookBinary(binary string) (info fs.FileInfo, state string) {
	if !filepath.IsAbs(binary) {
		return nil, lessoncheck.BinaryUnknown
	}

	info, err := os.Stat(binary)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, lessoncheck.BinaryMissing
	case err != nil:
		return nil, lessoncheck.BinaryUnknown
	case !info.Mode().IsRegular(), runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		return nil, lessoncheck.BinaryMissing
	}

	return info, ""
}

// hookBinaryMissing reports whether the client cannot start the binary
// of a lifecycle hook, by the rule of statHookBinary. An unknown state is
// not missing, so a relative command never gets a warning. A fixture
// binary of the tests counts as present.
func hookBinaryMissing(binary string) bool {
	if fixtureHookBinaries[binary] {
		return false
	}

	_, state := statHookBinary(binary)

	return state == lessoncheck.BinaryMissing
}

// fixtureHookBinaries holds the hook binary paths that the lifecycle
// hook inspection treats as present. Only test support fills it, before
// any test runs, so no lock guards it.
var fixtureHookBinaries = map[string]bool{}

// TreatHookBinaryAsPresent makes the lifecycle hook inspection treat one
// binary path as present. The shared test fixtures name a path such as
// /usr/local/bin/seamark, which a test machine does not need to hold.
// Without this call, every fixture hook reads as one that cannot start.
// The shipped binary never calls it. The lesson-check capability report
// ignores it, because its tests expect the real state of that path.
func TreatHookBinaryAsPresent(binary string) {
	fixtureHookBinaries[binary] = true
}

// binaryReading is the static identification of one hook binary.
type binaryReading struct {
	state   string
	version string
}

// identifyBinary identifies a hook binary statically. The path comes
// from a hook document that the repository can commit, so the function
// never runs it. It reads only a regular file with an execute bit,
// because a FIFO or a device would block the read.
//
// A path that is not absolute depends on the environment of the client,
// so it reads as unknown. A stat error other than absence is unknown
// too, never missing: an unreadable file is not an absent one.
func identifyBinary(binary string, self BinaryIdentity) binaryReading {
	info, state := statHookBinary(binary)
	if state != "" {
		return binaryReading{state: state}
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
// by client ID. setup and the capability report read the same specs, so
// the report checks the hooks that setup installs.
var lessonHookClients = map[string]lessonHookClient{
	ClaudeID: claudeLessonHooks,
	CodexID:  codexLessonHooks,
}

// explorationTool names the exploration path of a client without the
// structured Read path. The path takes the tool of the client that has
// one, so every report names the stage with one tool.
const explorationTool = claudeReadTool

// postEditEvidence is the native evidence that the post-edit reply
// reaches the model. It takes no client, tool, or context, so it cannot
// name the client version of a reviewed recording. The level stays
// unverified for every path, also for the replies that the reviewed
// Claude Code guard recording shows.
func postEditEvidence() VerificationEvidence {
	return VerificationEvidence{Level: VerificationUnverified}
}

// explorationEvidence is the native evidence of the exploration path.
// It takes no client, so it stays unverified, also for the replies that
// the reviewed Claude Code timing recording shows.
func explorationEvidence() VerificationEvidence {
	return VerificationEvidence{Level: VerificationUnverified}
}

// capVerification copies native evidence into the mirror type of the
// report, field for field.
func capVerification(e VerificationEvidence) lessoncheck.CapVerification {
	return lessoncheck.CapVerification{
		Level: e.Level.String(), ClientVersion: e.ClientVersion, Surface: e.Surface, Limit: e.Limit,
	}
}

// lessonHookEntries reads the owned lesson-check and exploration
// commands of one parsed hook document. A command counts by ownership,
// under any matcher and type. Its tools are the tools that the client
// runs it for, so a narrowed matcher shows as a coverage gap. An entry
// with an if filter gets no tools: setup cannot tell which calls the
// client runs it for, so it claims none, and the path reads
// tool-uncovered.
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
			if hookType, _ := h["type"].(string); hookType == "command" && client.exec(h).Filter == "" {
				entry.Tools = firingTools(s.spec, matcher, client.fires)
			}

			entries = append(entries, entry)
		})
	}

	return entries
}

// lessonHookReading returns the reading, or nil when it holds no
// lesson-check hook, wrapper, or unreadable supplemental source. The
// hooks-disabled flag of the project configuration alone changes no
// path, because only an installed path carries that reason.
func lessonHookReading(reading LessonHookInspection) *LessonHookInspection {
	if len(reading.Entries) == 0 && !reading.ReportOnly && !reading.SourceUnreadable {
		return nil
	}

	return &reading
}

// LessonCheckPaths reads the owned lesson-check hook entries of an
// inspection and identifies each hook binary statically. It runs
// nothing. It returns one installed path per tool, hook stage, and
// context, and one path without a stage per writer that no hook covers.
//
// A client that has no lesson-check hook specs gives no path. A
// built-in client without installed hooks gets its paths with reason
// not-installed.
func LessonCheckPaths(insp Inspection, self BinaryIdentity) []lessoncheck.InstalledPath {
	return lessonCheckPaths(insp, self, map[string]binaryReading{})
}

// lessonCheckPaths is LessonCheckPaths with a cache of the binary
// readings by path. The clients of one workspace often name the same
// hook binary, so one report shares the cache and reads each binary
// once.
func lessonCheckPaths(insp Inspection, self BinaryIdentity,
	binaries map[string]binaryReading,
) []lessoncheck.InstalledPath {
	client, ok := lessonHookClients[insp.ClientID]
	if !ok {
		return nil
	}

	r := pathReader{client: client, self: self, binaries: binaries, documentErr: insp.HookDocumentError}

	if insp.LessonHooks != nil {
		r.lessons = *insp.LessonHooks
	}

	// The trust of the lessons hook is the trust of its document, which
	// holds the lesson-check hooks too.
	if edits, ok := insp.Entry(CapabilityEdits); ok {
		r.trust = edits.Trust
	}

	return r.paths()
}

// pathReader builds the installed paths of one client.
type pathReader struct {
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
	preEntries, postEntries := r.stageEntries(lessoncheck.StagePreWrite), r.stageEntries(lessoncheck.StagePostEdit)

	var out []lessoncheck.InstalledPath

	for _, tool := range specs[0].Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StagePreWrite, context, GuardEvidence(r.client.id, tool, context))
			r.preWrite(&p, preEntries, partial)
			out = append(out, p)
		}
	}

	for _, tool := range specs[1].Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StagePostEdit, context, postEditEvidence())
			r.postEdit(&p, postEntries, specs[1:], partial)
			out = append(out, p)
		}
	}

	out = append(out, r.exploration()...)

	for _, tool := range r.client.uncovered {
		for _, context := range lessonContexts {
			out = append(out, lessoncheck.InstalledPath{
				Client: r.client.id, Tool: tool, Context: context, Reasons: []lessoncheck.CapReason{lessoncheck.CapToolUncovered},
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
		Client: r.client.id, Tool: tool, Stage: stage, Context: context, Verification: capVerification(evidence),
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
// runs for its tool. It prefers a guard entry to a baseline entry,
// because one guard denies whatever the baseline does. An owned entry
// of the stage that never fires for the tool gives tool-uncovered: a
// matcher edited by hand omits the tool. No entry gives not-installed.
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

		if chosen == nil || lessoncheck.IsGuardMarker(e.Marker) {
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

// preWrite fills one pre-write path from the pre-write entries. A
// definition that discards the exit status, or that the client runs in
// the background, keeps every pre-write path inactive, even beside an
// owned entry. The guard cannot project a Codex
// update with hunks yet. The report keeps that reason only while an
// enforce pattern pin exists.
func (r *pathReader) preWrite(p *lessoncheck.InstalledPath, entries []LessonHookEntry, partial bool) {
	r.install(p, entries)
	r.shared(p, partial)

	if r.lessons.ReportOnly {
		p.Reasons = append(p.Reasons, lessoncheck.CapReportOnlyWrapper)
	}

	if p.Tool == r.client.patchTool {
		p.Reasons = append(p.Reasons, lessoncheck.CapContentUnprojectable)
	}
}

// postEdit fills one post-edit path from the post-edit entries. Claude
// Code reports a failed tool through its own event, so the path needs an
// entry on every post-edit event. A path with some of them runs, and it
// carries partial-install.
func (r *pathReader) postEdit(p *lessoncheck.InstalledPath, entries []LessonHookEntry, posts []hooks.Spec,
	partial bool,
) {
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
// evidence, which the report reads as unsupported.
func (r *pathReader) exploration() []lessoncheck.InstalledPath {
	var out []lessoncheck.InstalledPath

	if r.client.exploration == nil {
		for _, context := range lessonContexts {
			out = append(out, r.path(explorationTool, lessoncheck.StageExploration, context, VerificationEvidence{}))
		}

		return out
	}

	entries := r.stageEntries(lessoncheck.StageExploration)

	for _, tool := range r.client.exploration.Tools() {
		for _, context := range lessonContexts {
			p := r.path(tool, lessoncheck.StageExploration, context, explorationEvidence())
			r.install(&p, entries)
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
// itself: LessonCheckPaths reads the paths, attributionsOf reads the
// attributions, and lessoncheck.CapabilityReportOf computes every state
// and badge. Doctor, status, --set-mode, preparation, the MCP server,
// and the console call it, so they report one account. A caller that
// needs no other view of the clients passes the result of
// Registry.InspectLessonHooks.
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
	binaries := map[string]binaryReading{}

	for _, insp := range insps {
		installed = append(installed, lessonCheckPaths(insp, self, binaries)...)

		// The attributions do not depend on the lesson-check hooks, because
		// the delivery report reads them for the advisory hooks too.
		if attrs := attributionsOf(insp); attrs != nil {
			attributions[insp.ClientID] = attrs
		}
	}

	return lessoncheck.CapabilityReportOf(cfg, errs, installed, attributions)
}

// LessonInstalledText names the installed mode of one client for a
// summary line. Doctor, status, and the console print it, so they use
// one set of words.
//
// The installed mode is a closed set without an unknown value, so the
// report keeps "off" for a document that it cannot read. A summary must
// not print that value alone, because an error is not absence. A path
// with the reason document-unreadable gives the qualifier. documentErr
// is the reason from LessonDocumentError. It is set when the hook
// document cannot be read, and no mode is then known. Without it, a
// supplemental source is unreadable. The readable document keeps its
// mode, and the effect of the other source is unknown.
func LessonInstalledText(c lessoncheck.ClientCapability, documentErr string) string {
	switch {
	case !LessonSourceUnreadable(c):
		return string(c.Installed)
	case documentErr != "":
		return "unreadable (" + documentErr + ")"
	default:
		return string(c.Installed) + " (a hook source is unreadable)"
	}
}

// LessonSourceUnreadable reports whether a path of the client carries
// the reason document-unreadable.
func LessonSourceUnreadable(c lessoncheck.ClientCapability) bool {
	return slices.ContainsFunc(c.Paths, func(p lessoncheck.PathCapability) bool {
		return slices.Contains(p.Reasons, lessoncheck.CapDocumentUnreadable)
	})
}

// LessonDocumentError returns the reason that the hook document of a
// client cannot be read, or "" when it can. The text starts with the
// file.
func LessonDocumentError(insps []Inspection, client string) string {
	for _, insp := range insps {
		if insp.ClientID == client {
			return insp.HookDocumentError
		}
	}

	return ""
}

// LessonHooksInstalled reports whether the inspection reads an owned
// lesson-check or exploration entry. Doctor and status print their
// lesson-check lines when one exists for some client.
func (i Inspection) LessonHooksInstalled() bool {
	return i.LessonHooks != nil && len(i.LessonHooks.Entries) > 0
}

// LessonGuardInstalled reports whether the inspection reads an owned
// guard entry. An installed guard denies every covered edit while the
// configuration does not validate, so doctor fails only then.
func (i Inspection) LessonGuardInstalled() bool {
	return i.LessonHooks != nil && slices.ContainsFunc(i.LessonHooks.Entries, func(e LessonHookEntry) bool {
		return e.Stage == lessoncheck.StagePreWrite && lessoncheck.IsGuardMarker(e.Marker)
	})
}
