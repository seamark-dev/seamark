// Package settings reads and edits the focused Seamark settings in
// .seamark/config.yaml: the inference preset, the pin-write gate, the
// review window, and the indexing options. Three packages read that one
// file, each its own section. This package therefore never re-encodes
// the document. It splices the text of one field group, verifies the
// result with every reader, and writes it through the guarded setup
// engine of internal/integration.
//
// Inspect reads only. PlanPatch computes a guarded diff and writes
// nothing. Save writes exactly the planned bytes, or refuses with
// ErrStale when the file changed after it was inspected.
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
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// File is the shared configuration document, relative to the root.
const File = ".seamark/config.yaml"

// Field names one supported setting by its YAML path.
type Field string

// The supported fields. agent.argv is read-only: the only edit is its
// removal, because authoring a custom command is out of scope.
const (
	AgentCLI       Field = "agent.cli"
	AgentArgv      Field = "agent.argv"
	DistillWrite   Field = "distill.write"
	ReviewWindow   Field = "reviews.window_days"
	IndexGenerated Field = "index.generated"
	IndexExclude   Field = "index.exclude"
)

// allFields lists the fields in display order.
var allFields = []Field{AgentCLI, AgentArgv, DistillWrite, ReviewWindow, IndexGenerated, IndexExclude}

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

// Group returns the settings group that edits the field.
func (f Field) Group() Group {
	switch f {
	case AgentCLI, AgentArgv:
		return GroupInference
	case DistillWrite:
		return GroupPinWrite
	case ReviewWindow:
		return GroupEvidence
	default:
		return GroupIndexing
	}
}

// Reader returns the name of the reader that owns the field, as used
// in Snapshot.ReaderErrs.
func (f Field) Reader() string {
	switch f.section() {
	case sectionAgent:
		return ReaderAgent
	case sectionDistill:
		return ReaderDistill
	default:
		return ReaderIndex
	}
}

// Reader names, the keys of Snapshot.ReaderErrs. The index reader also
// owns reviews.window_days.
const (
	ReaderAgent   = "agent"
	ReaderDistill = "distill"
	ReaderIndex   = "index"
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
)

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
	default:
		return "unknown"
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

// section returns the top-level key that holds the group's fields.
func (g Group) section() string {
	return g.Fields()[0].section()
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

// Snapshot is one read-only observation of the settings file.
type Snapshot struct {
	// Root is the absolute workspace root; Path is the absolute file.
	Root, Path string
	// Guard is the observation that every plan binds to.
	Guard integration.FileGuard
	// Blocked is set when no group can be saved: the file is unreadable,
	// linked, read-only, or structurally unsafe to edit. BlockedAt is
	// the 1-based line to open, or 0.
	Blocked   error
	BlockedAt int
	// Fields holds every supported field in display order.
	Fields []FieldState
	// Presets lists the valid agent.cli values in registry order.
	Presets []string
	// Invocation is the resolved agent command; zero with InvocationErr.
	Invocation    agent.CommandSpec
	InvocationErr error
	// ReaderErrs holds each reader's failure, keyed by ReaderAgent,
	// ReaderDistill, or ReaderIndex. A missing key means success.
	ReaderErrs map[string]error
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
}

// Error names the reason and the line.
func (e *BlockedError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s (line %d); edit %s by hand", e.Reason, e.Line, File)
	}

	return fmt.Sprintf("%s; edit %s by hand", e.Reason, File)
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
// cannot: the global block, a problem in the group's section, or a
// failing owning reader. The UI shows the reason before a draft starts.
func (s *Snapshot) Editable(g Group) error {
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

	// ReadInput follows a link for the read, as the readers do, and
	// records it. The link then blocks every write below.
	guard, raw, err := integration.ReadInput(abs, File)
	if err != nil {
		// Every reader fails on an unreadable file, so every value is
		// unknown. The fields still carry their defaults for display.
		snap.apply(reading{agentErr: err, distillErr: err, indexErr: err})
		snap.block(&BlockedError{Reason: render.Sanitize(err.Error())})

		return snap, nil
	}

	snap.Guard = guard

	var issue *yamledit.Issue

	snap.data, snap.crlf, issue = yamledit.NormalizeBreaks(raw)
	gate := blockedError(issue)

	r := read(abs, reg, snap.data)
	snap.apply(r)

	// The engine owns the refusal of a link and of a file that this user
	// cannot write. The view shows the same reason before the user asks
	// for a write.
	refusal := integration.WriteRefusal(guard)

	switch {
	case refusal != "":
		snap.block(&BlockedError{Reason: refusal})
	case gate != nil:
		snap.block(gate)
	case r.blocked != nil:
		snap.block(r.blocked)
	}

	return snap, nil
}

// block records the global block.
func (s *Snapshot) block(b *BlockedError) {
	s.Blocked, s.BlockedAt = b, b.Line
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

// fields builds the state of every field from one reading.
func (r reading) fields(presets []string) []FieldState {
	out := make([]FieldState, 0, len(allFields))

	for _, f := range allFields {
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
