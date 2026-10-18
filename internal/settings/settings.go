// Package settings reads and edits the focused Seamark settings in two
// documents. .seamark/config.yaml holds the inference preset, the
// pin-write gate, the review window, and the indexing options. Three
// packages read that one file, each its own section.
// .seamark/lessons.yaml holds the lesson delivery settings and the
// mutes; the pins in it belong to the pin editors (internal/pinfile).
// This package therefore never re-encodes a document. It splices the
// text of one field group, verifies the result with every reader, and
// writes it through the guarded setup engine of internal/integration.
//
// Inspect reads only. PlanPatch computes a guarded diff and writes
// nothing. Save writes exactly the planned bytes, or refuses with
// ErrStale when the file changed after it was inspected. After a failed
// write, Save reads the file again and reports the outcome.
//
// A save changes only the lines of one field group in one document. It
// keeps comments, unknown keys, other sections and the file mode, and it
// replaces the file atomically. A missing file is created with the
// planned keys only. A file that the splice cannot change safely returns
// a BlockedError that names the line to fix by hand: malformed or
// ambiguous YAML (for example duplicate keys or aliases), a symbolic
// link, a read-only file, or a file that this user cannot write. The two
// documents are independent: a problem in one never blocks a group of
// the other. The activation texts of the plan say when each field takes
// effect; docs/configuration.md lists them for the user.
package settings

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/fixes"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// File is the shared configuration document, relative to the root.
const File = ".seamark/config.yaml"

// Field names one supported setting by its YAML path. A config.yaml
// field is a "section.key" path. A lessons.yaml field is a top-level key.
type Field string

// The supported fields of config.yaml. agent.argv is read-only: the only
// edit is its removal, because authoring a custom command is out of
// scope.
const (
	AgentCLI       Field = "agent.cli"
	AgentArgv      Field = "agent.argv"
	DistillWrite   Field = "distill.write"
	ReviewWindow   Field = "reviews.window_days"
	IndexGenerated Field = "index.generated"
	IndexExclude   Field = "index.exclude"
)

// The supported fields of lessons.yaml. Each is a top-level key of
// reviews.Config. Mutes is the mute list; a plan changes one entry of
// it. The pin list is not a field: the pin editors own it.
const (
	Threshold    Field = "threshold"
	PinBudget    Field = "pin_budget"
	ChangeBudget Field = "change_budget"
	HookDelivery Field = "hook_delivery"
	Mutes        Field = "mute"
)

// allFields lists the fields in display order: the config.yaml groups
// first, then lesson delivery, then the mutes.
var allFields = []Field{
	AgentCLI, AgentArgv, DistillWrite, ReviewWindow, IndexGenerated, IndexExclude,
	Threshold, PinBudget, ChangeBudget, HookDelivery, Mutes,
}

// documentFields lists the fields of one document in display order.
func documentFields(doc string) []Field {
	var out []Field

	for _, f := range allFields {
		if f.Document() == doc {
			out = append(out, f)
		}
	}

	return out
}

// section returns the top-level key that holds the field.
func (f Field) section() string {
	s, _, _ := strings.Cut(string(f), ".")

	return s
}

// key returns the field key inside its section.
func (f Field) key() string {
	_, k, _ := strings.Cut(string(f), ".")

	return k
}

// Group returns the settings group that edits the field. An unknown
// field has no group, so a new field cannot join a form by accident.
func (f Field) Group() Group {
	switch f {
	case AgentCLI, AgentArgv:
		return GroupInference
	case DistillWrite:
		return GroupPinWrite
	case ReviewWindow:
		return GroupEvidence
	case IndexGenerated, IndexExclude:
		return GroupIndexing
	case Threshold, PinBudget, ChangeBudget, HookDelivery:
		return GroupDelivery
	case Mutes:
		return GroupMutes
	default:
		return groupNone
	}
}

// Document returns the file that holds the field, relative to the root:
// File or pinfile.File. An unknown field returns "".
func (f Field) Document() string {
	return f.Group().Document()
}

// Reader returns the name of the reader that owns the field, as used
// in Snapshot.ReaderErrs. An unknown field returns "".
func (f Field) Reader() string {
	switch f {
	case AgentCLI, AgentArgv:
		return ReaderAgent
	case DistillWrite:
		return ReaderDistill
	case ReviewWindow, IndexGenerated, IndexExclude:
		return ReaderIndex
	case Threshold, PinBudget, ChangeBudget, HookDelivery, Mutes:
		return ReaderLessons
	default:
		return ""
	}
}

// Reader names, the keys of Snapshot.ReaderErrs. The index reader also
// owns reviews.window_days. The lessons reader (reviews.ParseConfig)
// reads lessons.yaml; the other three read config.yaml.
const (
	ReaderAgent   = "agent"
	ReaderDistill = "distill"
	ReaderIndex   = "index"
	ReaderLessons = "lessons"
)

// Group is one settings form. A plan edits exactly one group, so
// enabling pin writes is always a save of its own (spec R-12).
type Group int

// The settings groups.
const (
	// GroupInference edits agent.cli and removes agent.argv.
	GroupInference Group = iota
	// GroupPinWrite edits distill.write only.
	GroupPinWrite
	// GroupEvidence edits reviews.window_days.
	GroupEvidence
	// GroupIndexing edits index.generated and index.exclude.
	GroupIndexing
	// GroupDelivery edits the four delivery keys of lessons.yaml.
	GroupDelivery
	// GroupMutes adds, changes, or removes one mute entry of lessons.yaml.
	GroupMutes
)

// groupNone is the group of an unknown field. It has no fields and no
// document.
const groupNone Group = -1

// String names the group for display.
func (g Group) String() string {
	switch g {
	case GroupInference:
		return "inference"
	case GroupPinWrite:
		return "pin writing"
	case GroupEvidence:
		return "evidence"
	case GroupIndexing:
		return "indexing"
	case GroupDelivery:
		return "lesson delivery"
	case GroupMutes:
		return "lesson mutes"
	default:
		return "unknown"
	}
}

// Document returns the file that the group edits, relative to the root.
// The lessons groups edit pinfile.File, the one name of lessons.yaml. An
// unknown group returns "".
func (g Group) Document() string {
	switch g {
	case GroupInference, GroupPinWrite, GroupEvidence, GroupIndexing:
		return File
	case GroupDelivery, GroupMutes:
		return pinfile.File
	default:
		return ""
	}
}

// Fields lists the fields of the group in display order.
func (g Group) Fields() []Field {
	var out []Field

	for _, f := range allFields {
		if f.Group() == g {
			out = append(out, f)
		}
	}

	return out
}

// section returns the config.yaml section that holds the group's
// fields. The lessons groups edit top-level keys and have no section.
func (g Group) section() string {
	switch g {
	case GroupInference:
		return sectionAgent
	case GroupPinWrite:
		return sectionDistill
	case GroupEvidence:
		return sectionReviews
	case GroupIndexing:
		return sectionIndex
	case GroupDelivery, GroupMutes:
		return ""
	default:
		return ""
	}
}

// Source says where the effective value of a field comes from.
type Source int

// The value sources.
const (
	// SourceDefault means no explicit value: the reader default applies.
	SourceDefault Source = iota
	// SourceFile means the file sets the value explicitly.
	SourceFile
	// SourceShadowed means agent.argv overrides the field.
	SourceShadowed
)

// Stored is the raw state of one key in the file.
type Stored struct {
	// Present is true when the key exists in the file.
	Present bool
	// Raw is the sanitized source text of the value, for display.
	Raw string
	// Line is the 1-based line of the key; 0 when absent.
	Line int
}

// FieldState is the stored, default, and effective value of one field.
type FieldState struct {
	Field  Field
	Stored Stored
	// Default and Effective are display text. They come from the owning
	// reader, so the UI never computes a default itself.
	Default   string
	Effective string
	Source    Source
	// Notes explain the effective value, for example an override.
	Notes []string
	// Err is set when the owning reader or the invocation resolver
	// fails. The value is then unknown, not the default.
	Err error
}

// Snapshot is one read-only observation of the two settings files. The
// fields without a document name describe config.yaml; Lessons describes
// lessons.yaml.
type Snapshot struct {
	// Root is the absolute workspace root; Path is the absolute
	// config.yaml file.
	Root, Path string
	// Guard is the observation that every config.yaml plan binds to.
	Guard integration.FileGuard
	// Blocked is set when no config.yaml group can be saved: the file is
	// unreadable, linked, read-only, or structurally unsafe to edit.
	// BlockedAt is the 1-based line to open, or 0. It never blocks a
	// lessons group.
	Blocked   error
	BlockedAt int
	// ReadErr is the failure of reading config.yaml itself, before any
	// reader runs; nil when the read worked or the file is absent. A
	// reader that rejects the text sets ReaderErrs, not ReadErr. The
	// console checks its reload after a save with it, because a reader
	// error of another section does not mean that the read failed.
	ReadErr error
	// Fields holds every supported field of both documents in display
	// order.
	Fields []FieldState
	// Presets lists the valid agent.cli values in registry order.
	Presets []string
	// Invocation is the resolved agent command; zero with InvocationErr.
	Invocation    agent.CommandSpec
	InvocationErr error
	// ReaderErrs holds each reader's failure, keyed by ReaderAgent,
	// ReaderDistill, ReaderIndex, or ReaderLessons. A missing key means
	// success. The ReaderLessons entry is Lessons.ReaderErr, so a lookup
	// by Field.Reader works for every field.
	ReaderErrs map[string]error
	// Lessons is the observation of lessons.yaml. Inspect always sets it.
	Lessons *LessonsPart
	// Agent, Distill, and Index are the decoded sections; nil when the
	// reader failed. The UI seeds its drafts from them.
	Agent   *agent.Config
	Distill *distill.Config
	Index   *index.Config
	// InspectedAt is the time of the observation.
	InspectedAt time.Time

	// data is the observed file content (LF form); plans start from it.
	data []byte
	// crlf is true when the file uses CRLF line breaks.
	crlf bool
	// doc is the parsed file; nil when the file cannot be parsed.
	doc *yamledit.Doc
	// issues holds the per-section problems, keyed by section name.
	issues map[string]*BlockedError
}

// ErrStale reports a file that changed after it was inspected or
// planned. The caller inspects again; a silent re-plan could save an
// intent the user never saw.
var ErrStale = errors.New("configuration changed since it was inspected")

// ErrBlocked reports a file that cannot be edited safely. Every
// BlockedError matches it with errors.Is.
var ErrBlocked = errors.New("configuration cannot be edited safely")

// ErrInvalidEdit reports an edit that the settings form must not send:
// an unknown preset, a negative window, a bad exclude pattern, or edits
// from two groups.
var ErrInvalidEdit = errors.New("invalid settings edit")

// BlockedError explains why an edit is unsafe and names the line to
// open, so the UI can offer a manual route.
type BlockedError struct {
	Reason string
	// Line is the 1-based line in the file, or 0 when unknown.
	Line int
	// File is the document to edit by hand, relative to the root. Empty
	// means config.yaml, the only document of the callers before the
	// lessons document.
	File string
}

// Error names the reason, the line, and the file.
func (e *BlockedError) Error() string {
	file := e.File
	if file == "" {
		file = File
	}

	if e.Line > 0 {
		return fmt.Sprintf("%s (line %d); edit %s by hand", e.Reason, e.Line, file)
	}

	return fmt.Sprintf("%s; edit %s by hand", e.Reason, file)
}

// Is makes every BlockedError match ErrBlocked.
func (e *BlockedError) Is(target error) bool {
	return target == ErrBlocked
}

// Field returns the state of one field.
func (s *Snapshot) Field(f Field) (FieldState, bool) {
	i := slices.IndexFunc(s.Fields, func(st FieldState) bool { return st.Field == f })
	if i < 0 {
		return FieldState{}, false
	}

	return s.Fields[i], true
}

// Editable returns nil when the group can be planned, or the reason it
// cannot. The reason is the block of the group's document, a problem in
// the group's section, or a failing owning reader. The UI shows the
// reason before a draft starts. A group checks only its own document, so
// a problem in one file never blocks a group of the other.
func (s *Snapshot) Editable(g Group) error {
	switch g.Document() {
	case pinfile.File:
		return s.Lessons.editable(g)
	case "":
		return fmt.Errorf("%w: unknown settings group %d", ErrInvalidEdit, int(g))
	}

	if s.Blocked != nil {
		return s.Blocked
	}

	section := g.section()

	if issue := s.issues[section]; issue != nil {
		return issue
	}

	reader := g.Fields()[0].Reader()

	// The editor never repairs a section that its reader rejects: the
	// fix is the user's, and a silent repair could change its meaning.
	// A reader error can quote the rejected value, so it gets the
	// redaction of the stored values.
	if err := s.ReaderErrs[reader]; err != nil {
		return &BlockedError{
			Reason: fmt.Sprintf("the %s reader rejects the file: %s", reader, displayText(err.Error())),
			Line:   yamledit.ErrorLine(err),
		}
	}

	return nil
}

// Inspect reads the settings file under root. It runs no agent, no git,
// and no network call, and it writes nothing. A reader failure or an
// unsafe file is part of the snapshot, not an error: the UI shows every
// field it can. The error is for invalid arguments only.
func Inspect(root string, reg *integration.Registry) (*Snapshot, error) {
	if reg == nil {
		return nil, errors.New("settings: no client registry")
	}

	if root == "" {
		return nil, errors.New("settings: workspace root is empty")
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}

	snap := &Snapshot{
		Root:        abs,
		Path:        filepath.Join(abs, filepath.FromSlash(File)),
		Presets:     reg.InvocationIDs(),
		ReaderErrs:  map[string]error{},
		InspectedAt: time.Now(),
		issues:      map[string]*BlockedError{},
	}

	snap.inspectConfig(reg)

	// The lessons document is read on every path, also when config.yaml
	// is unreadable: the two documents are independent.
	var lessonsFields []FieldState

	snap.Lessons, lessonsFields = inspectLessons(abs)
	snap.Fields = append(snap.Fields, lessonsFields...)

	if snap.Lessons.ReaderErr != nil {
		snap.ReaderErrs[ReaderLessons] = snap.Lessons.ReaderErr
	}

	return snap, nil
}

// inspectConfig reads config.yaml into the snapshot.
func (s *Snapshot) inspectConfig(reg *integration.Registry) {
	// ReadInput follows a link for the read, as the readers do, and
	// records it. The link then blocks every write below.
	guard, raw, err := integration.ReadInput(s.Root, File)
	if err != nil {
		// Every reader fails on an unreadable file, so every value is
		// unknown. The fields still carry their defaults for display.
		s.ReadErr = err
		s.apply(reading{agentErr: err, distillErr: err, indexErr: err})
		s.block(&BlockedError{Reason: render.Sanitize(err.Error())})

		return
	}

	s.Guard = guard

	var issue *yamledit.Issue

	s.data, s.crlf, issue = yamledit.NormalizeBreaks(raw)
	gate := blockedError(issue)

	r := read(s.Root, reg, s.data)
	s.apply(r)

	// The engine owns the refusal of a link and of a file that this user
	// cannot write. The view shows the same reason before the user asks
	// for a write.
	refusal := integration.WriteRefusal(guard)

	switch {
	case refusal != "":
		s.block(&BlockedError{Reason: refusal})
	case gate != nil:
		s.block(gate)
	case r.blocked != nil:
		s.block(r.blocked)
	}
}

// block records the global block.
func (s *Snapshot) block(b *BlockedError) {
	s.Blocked, s.BlockedAt = b, b.Line
}

// observed returns the guard and the absolute path of one document of
// the snapshot. A plan and a save bind to the guard of their own
// document only, so a change of the other file never makes them stale.
func (s *Snapshot) observed(file string) (guard integration.FileGuard, path string) {
	switch {
	case file == File:
		return s.Guard, s.Path
	case file == pinfile.File && s.Lessons != nil:
		return s.Lessons.Guard, s.Lessons.Path
	default:
		return integration.FileGuard{}, ""
	}
}

// unchanged returns nil when a fresh observation of one document equals
// its guard in the snapshot. Otherwise it returns the ErrStale that names
// the file. A plan asks it, because the patch starts from the snapshot
// bytes. A no-op save asks it, because it has no engine guard.
func (s *Snapshot) unchanged(file string) error {
	guard, _ := s.observed(file)

	if now, _, err := integration.ReadInput(s.Root, file); err != nil || now != guard {
		return staleError(file)
	}

	return nil
}

// apply copies one reading into the snapshot.
func (s *Snapshot) apply(r reading) {
	s.doc = r.doc
	s.Agent, s.Distill, s.Index = r.agent, r.distill, r.index
	s.Invocation, s.InvocationErr = r.invocation, r.invocationErr
	s.Fields = r.fields(s.Presets)

	for name, err := range r.errs() {
		s.ReaderErrs[name] = err
	}

	if r.doc != nil {
		for _, name := range ownedSections {
			if issue := sectionIssue(r.doc, name); issue != nil {
				s.issues[name] = issue
			}
		}
	}
}

// reading is the result of every reader on one version of the file.
type reading struct {
	doc     *yamledit.Doc
	blocked *BlockedError

	agent      *agent.Config
	agentErr   error
	distill    *distill.Config
	distillErr error
	index      *index.Config
	indexErr   error

	invocation    agent.CommandSpec
	invocationErr error
}

// read runs the structural gate, the three readers, and the invocation
// resolver on data. The readers are the shared bytes entry points, so
// the settings view and the commands agree on every value and error.
func read(root string, reg *integration.Registry, data []byte) reading {
	var r reading

	r.doc, r.blocked = parseDocument(data)
	r.agent, r.agentErr = agent.ParseConfig(data)
	r.distill, r.distillErr = distill.ParseConfig(data)
	r.index, r.indexErr = index.ParseConfig(data)

	if r.agentErr == nil {
		r.invocation, r.invocationErr = reg.ResolveInvocation(r.agent, root)
	}

	return r
}

// errs returns the failing readers by name.
func (r reading) errs() map[string]error {
	out := map[string]error{}

	for name, err := range map[string]error{ReaderAgent: r.agentErr, ReaderDistill: r.distillErr, ReaderIndex: r.indexErr} {
		if err != nil {
			out[name] = err
		}
	}

	return out
}

// stored returns the raw state of one field in the parsed file.
func (r reading) stored(f Field) (Stored, bool) {
	if r.doc == nil {
		return Stored{}, false
	}

	k, v := r.doc.Child(f.section(), f.key())
	if k == nil {
		return Stored{}, false
	}

	return Stored{Present: true, Raw: rawText(v), Line: k.Line}, !yamledit.IsNull(v)
}

// rawText renders a stored value for display. Repository-controlled text
// can hold control sequences and secret-shaped flags. Every field gets
// the redaction, because a wrong value, such as a command in agent.cli,
// can hold a secret too.
func rawText(v *yaml.Node) string {
	var text string

	switch v.Kind {
	case yaml.ScalarNode:
		text = v.Value
	case yaml.SequenceNode:
		items := make([]string, 0, len(v.Content))
		for _, c := range v.Content {
			items = append(items, c.Value)
		}

		text = "[" + strings.Join(secretItems(items), ", ") + "]"
	case yaml.AliasNode:
		text = "*" + v.Value
	default:
		text = "{…}"
	}

	return render.Truncate(displayText(text), 200)
}

// redacted replaces a secret value, as redact.Secrets does.
const redacted = "[REDACTED]"

// secretItems redacts the items of a sequence. A flag and its value can
// be two items, for example "--api-key" and the key. The comma list
// hides that pair from redact.Secrets, so each item is also checked
// together with the item before it, as a command line joins them.
func secretItems(items []string) []string {
	out := make([]string, len(items))

	for i, item := range items {
		out[i] = redact.Secrets(item)

		if i == 0 || out[i] != item {
			continue
		}

		pair := items[i-1] + " " + item
		if strings.HasPrefix(redact.Secrets(pair), items[i-1]+" "+redacted) {
			out[i] = redacted
		}
	}

	return out
}

// Display text for defaults and effective values.
const (
	textNone           = "none"
	textOn             = "on"
	textOff            = "off"
	textUnknown        = "unknown"
	textUnlimited      = "unlimited"
	textSkipGenerated  = "skip generated files"
	textIndexGenerated = "index generated files"
	textCustom         = "custom command (agent.argv)"
)

// days renders a day count.
func days(n int) string {
	if n == 1 {
		return "1 day"
	}

	return strconv.Itoa(n) + " days"
}

// onOff renders a gate.
func onOff(b bool) string {
	if b {
		return textOn
	}

	return textOff
}

// displayText makes repository text safe for display, with the status
// treatment: it removes secret-shaped values and control sequences.
func displayText(text string) string {
	return render.Sanitize(redact.Secrets(text))
}

// commandText renders an argv for display.
func commandText(argv []string) string {
	return displayText(strings.Join(argv, " "))
}

// fields builds the state of every config.yaml field from one reading.
func (r reading) fields(presets []string) []FieldState {
	configFields := documentFields(File)
	out := make([]FieldState, 0, len(configFields))

	for _, f := range configFields {
		st := FieldState{Field: f}

		var explicit bool

		st.Stored, explicit = r.stored(f)

		if explicit {
			st.Source = SourceFile
		}

		switch f {
		case AgentCLI:
			r.cliState(&st, presets)
		case AgentArgv:
			r.argvState(&st)
		case DistillWrite:
			st.Default = textOff
			st.Notes = []string{"Also governs seamark lessons --apply, --prune, and --retarget."}

			if st.Err = r.distillErr; st.Err == nil {
				st.Effective = onOff(r.distill.Distill.Write)
			}
		case ReviewWindow:
			r.windowState(&st)
		case IndexGenerated:
			st.Default = textSkipGenerated

			if st.Err = r.indexErr; st.Err == nil {
				st.Effective = textSkipGenerated
				if r.index.Index.Generated {
					st.Effective = textIndexGenerated
				}
			}
		case IndexExclude:
			st.Default = textNone

			if st.Err = r.indexErr; st.Err == nil {
				st.Effective = textNone
				if len(r.index.Index.Exclude) > 0 {
					st.Effective = displayText(strings.Join(r.index.Index.Exclude, ", "))
				}
			}
		}

		if st.Err != nil {
			st.Effective = textUnknown
		}

		out = append(out, st)
	}

	return out
}

// cliState fills agent.cli. A custom argv overrides the preset, and the
// resolver then never validates agent.cli, so the view does.
func (r reading) cliState(st *FieldState, presets []string) {
	st.Default = integration.DefaultInvocationID

	if st.Err = r.agentErr; st.Err != nil {
		return
	}

	cli := r.agent.Agent.CLI

	if len(r.agent.Agent.Argv) > 0 {
		st.Source = SourceShadowed
		st.Effective = textCustom
		st.Notes = append(st.Notes, "Ignored while agent.argv is set.")

		if cli != "" && !slices.Contains(presets, cli) {
			st.Notes = append(st.Notes, fmt.Sprintf("%q is not a registered preset.", displayText(cli)))
		}

		return
	}

	if cli == "" {
		st.Source = SourceDefault
		cli = integration.DefaultInvocationID
	}

	if st.Err = r.invocationErr; st.Err != nil {
		return
	}

	st.Effective = displayText(cli)
}

// argvState fills agent.argv. It is read-only here: the only edit is
// its removal.
func (r reading) argvState(st *FieldState) {
	st.Default = textNone

	if st.Err = r.agentErr; st.Err != nil {
		return
	}

	argv := r.agent.Agent.Argv
	if len(argv) == 0 {
		st.Source = SourceDefault
		st.Effective = textNone

		return
	}

	st.Source = SourceFile
	st.Effective = commandText(argv)
	st.Notes = []string{"Overrides agent.cli.", "Read-only here; the only edit is its removal."}
	st.Err = r.invocationErr
}

// windowNotes explains the limits of the review window. The newest
// comments survive any window, so the window is not a strict cutoff,
// and fix mining has its own fixed window.
func windowNotes() []string {
	return []string{
		fmt.Sprintf("The newest %d review comments are always kept.", reviews.CountFloor),
		fmt.Sprintf("Fix mining keeps its fixed %d-day window.", int(fixes.DefaultWindow/(24*time.Hour))),
	}
}

// windowState fills reviews.window_days with the reader's mapping:
// absent means the default window, 0 and negative values mean unlimited.
func (r reading) windowState(st *FieldState) {
	st.Default = days(reviews.DefaultReviewWindowDays)
	st.Notes = windowNotes()

	if st.Err = r.indexErr; st.Err != nil {
		return
	}

	stored := r.index.Reviews.WindowDays

	switch mapped := r.index.ReviewWindowDays(); {
	case mapped == 0:
		st.Effective = st.Default
	case mapped < 0:
		st.Effective = textUnlimited
	default:
		st.Effective = days(mapped)
	}

	if stored == nil {
		return
	}

	if *stored < 0 {
		st.Notes = append(st.Notes, "A negative value means unlimited; the editor writes 0 for unlimited.")
	}

	if st.Stored.Raw != strconv.Itoa(*stored) {
		st.Notes = append(st.Notes, fmt.Sprintf("The reader reads %q as %d.", st.Stored.Raw, *stored))
	}
}
