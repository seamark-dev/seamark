package settings

// The patch planner. It validates one group of edits and splices the
// text of the observed file. It then verifies the result with every
// reader. Last, the setup engine guards and previews the write. The
// splices and the verifier of lessons.yaml are in lessons_patch.go; the
// validation, the guard, and the save are shared by both documents.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// Edit is one field change of a draft. Reset removes the key, so the
// reader default applies; it never writes today's default value. The
// value fields that apply depend on Field.
type Edit struct {
	Field Field
	// Reset removes the key from the file.
	Reset bool
	// String is the agent.cli preset (a registered invocation ID) or the
	// hook_delivery mode.
	String string
	// Bool is the value of distill.write or index.generated.
	Bool bool
	// Days is a positive reviews.window_days value.
	Days int
	// Unlimited sets reviews.window_days to 0, the reader's unlimited.
	Unlimited bool
	// List is the index.exclude patterns. An empty list is a reset.
	List []string
	// Int is the threshold, pin_budget, or change_budget value, from 1
	// to math.MaxInt32. The editor writes no value that the reader
	// replaces with its default.
	Int int
	// Mute is the one mute operation of a mute field edit.
	Mute *MuteEdit
}

// MuteOp names one mute operation.
type MuteOp int

// The mute operations. The zero value is no operation, so a mute edit
// without an operation is invalid.
const (
	// MuteAdd appends one entry after the last entry.
	MuteAdd MuteOp = iota + 1
	// MuteChange changes the rule and the region of one entry. Unknown
	// keys and comments of the entry stay.
	MuteChange
	// MuteRemove removes one entry.
	MuteRemove
)

// MuteRef names one entry of the mute list: its YAML item index and the
// hash of its text when the draft started (MuteEntry.RawSHA). A plan
// refuses a reference whose entry text changed.
type MuteRef struct {
	Index  int
	RawSHA [32]byte
}

// MuteEdit is one mute operation. Rule and Region are the typed values
// of an add or a change; an empty value has no key. A removal takes
// only Ref.
type MuteEdit struct {
	Op   MuteOp
	Ref  MuteRef
	Rule string
	// Region is the typed region; CheckMute normalizes it.
	Region string
}

// isReset reports whether the edit removes its key.
func (e Edit) isReset() bool {
	return e.Reset || (e.Field == IndexExclude && len(e.List) == 0)
}

// Activation says when a saved field takes effect, and what else the
// user must know about it.
type Activation struct {
	Field Field
	// When names the action that first uses the new value.
	When string
	// Notes explain the effect and its limits.
	Notes []string
}

// Plan is a validated, guarded patch of one settings group. It holds
// the exact bytes to write; nothing is written yet.
type Plan struct {
	// Snapshot is the observation that the plan starts from.
	Snapshot *Snapshot
	Group    Group
	Edits    []Edit
	// File is the edited document relative to the root: File or
	// pinfile.File, as Group.Document returns it.
	File string
	// Bytes holds the reviewed and the planned file bytes of a lessons
	// plan, a no-op included; zero for a config.yaml plan. After equals
	// the bytes of the guarded write. A selection sample reads them.
	Bytes PlanBytes
	// Setup is the guarded write plan; nil for a no-op.
	Setup *integration.SetupPlan
	// Preview is the redacted file diff; zero for a no-op.
	Preview integration.WritePreview
	// Before and After are the states of the group's fields, from the
	// owning readers.
	Before, After []FieldState
	// Activation holds one entry per edited field.
	Activation []Activation
	// Notes apply to the save as a whole.
	Notes []string
	// NoOp is true when the edits leave the file bytes unchanged.
	NoOp bool
}

// PlanBytes are the file bytes before and after a plan.
type PlanBytes struct {
	Before, After []byte
}

// PinWriteScope names every write that distill.write governs (spec R-5).
// The proposal apply, the Pins retarget and prune, and the pin authoring
// of Lessons › Pins read the same gate. Every text that says what the
// gate governs uses this phrase, so no text names a part of the scope.
const PinWriteScope = "apply, retarget, prune, and new pins, pin edits, and pin removals in Lessons › Pins"

// Activation text: when a field takes effect (spec R-13). The pin-write
// gate names its whole scope, because every pin write reads the gate.
const (
	whenDistillation = "next distillation or trigger extraction"
	whenPreview      = "next pin-write preview: " + PinWriteScope
	whenEvidence     = "next confirmed evidence refresh"
	whenIndexBuild   = "next explicit index build"
)

// Notes that every config.yaml save shows. The index fingerprint hashes
// the whole file, so any save marks the index stale; saying otherwise is
// false. The fingerprint does not cover lessons.yaml, so a lessons save
// shows the commit note only. The commit note is advice, not a fact: the
// file can be new, untracked, or ignored, and the plan does not ask git.
const (
	noteIndexStale = "The index reads as stale until the next index build, because its fingerprint covers the whole configuration file."
	noteCommit     = "Review the diff, then commit the file so the team shares it."
)

// saveNotes are the notes of every config.yaml save.
var saveNotes = []string{noteIndexStale, noteCommit}

// noteNoOp is the one note of a plan that changes no byte, for both
// documents.
const noteNoOp = "Nothing changes: the file already holds these values."

// PlanPatch computes a guarded patch of one settings group. It writes
// nothing and runs no command. A file that changed after snap was taken
// returns ErrStale; an unsafe file returns a BlockedError; an invalid
// draft returns ErrInvalidEdit.
func PlanPatch(snap *Snapshot, reg *integration.Registry, edits []Edit) (*Plan, error) {
	// A snapshot without a root did not come from Inspect. Its relative
	// paths would resolve against the process directory.
	if snap == nil || snap.Root == "" {
		return nil, errors.New("settings: no snapshot from Inspect")
	}

	if reg == nil {
		return nil, errors.New("settings: no client registry")
	}

	group, err := validate(snap, edits)
	if err != nil {
		return nil, err
	}

	if group.Document() == pinfile.File {
		return planLessons(snap, reg, group, edits)
	}

	if err := snap.Editable(group); err != nil {
		return nil, err
	}

	// The patch starts from the snapshot bytes. They must still be the
	// file bytes, or the before values in the preview would be false.
	if err := snap.unchanged(File); err != nil {
		return nil, err
	}

	ordered := orderEdits(edits)

	after, err := splice(snap.data, ordered)
	if err != nil {
		return nil, err
	}

	afterRead, err := verify(snap, reg, group, ordered, after)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Snapshot: snap,
		Group:    group,
		Edits:    slices.Clone(edits),
		File:     File,
		Before:   groupFields(snap.Fields, group),
		After:    groupFields(afterRead.fields(snap.Presets), group),
	}

	if bytes.Equal(after, snap.data) {
		plan.NoOp = true
		plan.Notes = []string{noteNoOp}

		return plan, nil
	}

	written := after
	if snap.crlf {
		written = bytes.ReplaceAll(after, []byte("\n"), []byte("\r\n"))
	}

	if err := plan.guard(reg, written); err != nil {
		return nil, err
	}

	plan.Activation = activation(snap, ordered)
	plan.Notes = slices.Clone(saveNotes)

	return plan, nil
}

// Path returns the absolute path of the document that the plan writes.
// A save and a refused save both name this path, so a lessons plan never
// names config.yaml.
func (p *Plan) Path() string {
	if p == nil || p.Snapshot == nil {
		return ""
	}

	_, path := p.Snapshot.observed(p.Group.Document())

	return path
}

// guard builds the setup plan (Route A) of the plan's document: one
// client-less common document whose Compose returns the planned bytes.
// The setup engine then owns the guard, the link refusal, the redacted
// diff, and the atomic write.
func (p *Plan) guard(reg *integration.Registry, written []byte) error {
	snap := p.Snapshot
	observed, _ := snap.observed(p.File)

	// Compose receives the exact bytes that the plan guard hashes. A
	// difference from the snapshot means the file changed meanwhile.
	compose := func(existing []byte, exists bool) ([]byte, error) {
		if exists != observed.Exists || (exists && sha256.Sum256(existing) != observed.SHA256) {
			return nil, ErrStale
		}

		return written, nil
	}

	setup, err := integration.PlanSetup(reg, integration.SetupRequest{
		Root: snap.Root,
		Common: []integration.Document{{
			Path:       p.File,
			Compose:    compose,
			Detail:     "update " + p.Group.String() + " settings",
			KeptDetail: "no change",
		}},
	})

	switch {
	case errors.Is(err, ErrStale), errors.Is(err, integration.ErrStalePlan):
		return staleError(p.File)
	case err != nil:
		return err
	case len(setup.Reads) != 1 || setup.Reads[0] != observed || len(setup.Writes) != 1:
		return staleError(p.File)
	}

	previews := integration.PreviewWrites(setup)
	if len(previews) != 1 || previews[0].Stale || previews[0].Err != nil {
		return staleError(p.File)
	}

	p.Setup, p.Preview = setup, previews[0]

	return nil
}

// staleError is the ErrStale that names the file.
func staleError(file string) error {
	return fmt.Errorf("%w: %s; inspect it again", ErrStale, file)
}

// groupFields returns the states of the group's fields.
func groupFields(all []FieldState, g Group) []FieldState {
	var out []FieldState

	for _, st := range all {
		if st.Field.Group() == g {
			out = append(out, st)
		}
	}

	return out
}

// validate checks a draft against the rules of its group and returns
// the group. It reads only the snapshot.
func validate(snap *Snapshot, edits []Edit) (Group, error) {
	if len(edits) == 0 {
		return 0, fmt.Errorf("%w: no edits", ErrInvalidEdit)
	}

	group := edits[0].Field.Group()
	seen := map[Field]bool{}

	for _, e := range edits {
		if !slices.Contains(allFields, e.Field) {
			return 0, fmt.Errorf("%w: unknown field %q", ErrInvalidEdit, e.Field)
		}

		if g := e.Field.Group(); g != group {
			return 0, fmt.Errorf("%w: one plan edits one settings group; %s is %s, %s is %s",
				ErrInvalidEdit, edits[0].Field, group, e.Field, g)
		}

		if seen[e.Field] {
			return 0, fmt.Errorf("%w: %s appears twice", ErrInvalidEdit, e.Field)
		}

		seen[e.Field] = true

		if err := validateEdit(snap, e); err != nil {
			return 0, err
		}
	}

	// A preset under a custom argv would be saved but stay ineffective
	// (spec R-12). The plan must show the argv removal instead.
	argvSet := snap.Agent != nil && len(snap.Agent.Agent.Argv) > 0

	if argvSet && slices.ContainsFunc(edits, func(e Edit) bool { return e.Field == AgentCLI && !e.Reset }) &&
		!slices.ContainsFunc(edits, func(e Edit) bool { return e.Field == AgentArgv && e.Reset }) {
		return 0, fmt.Errorf("%w: agent.argv overrides agent.cli, so the preset would stay ineffective; "+
			"remove agent.argv in the same plan", ErrInvalidEdit)
	}

	return group, nil
}

// validateEdit checks one edit.
func validateEdit(snap *Snapshot, e Edit) error {
	if foreignValue(e) {
		return fmt.Errorf("%w: %s does not take that kind of value", ErrInvalidEdit, e.Field)
	}

	// The mute list has no reset: removing every mute at once is not an
	// operation of the form, and one plan changes one entry (R-11).
	if e.Field == Mutes && e.Reset {
		return fmt.Errorf("%w: the mute list has no reset; remove one entry at a time", ErrInvalidEdit)
	}

	if e.Reset {
		return nil
	}

	switch e.Field {
	case AgentCLI:
		if !slices.Contains(snap.Presets, e.String) {
			return fmt.Errorf("%w: agent.cli must be one of: %s", ErrInvalidEdit, strings.Join(snap.Presets, ", "))
		}
	case AgentArgv:
		return fmt.Errorf("%w: agent.argv can only be removed here; write a custom command in the file", ErrInvalidEdit)
	case ReviewWindow:
		return validateWindow(e)
	case Threshold, PinBudget, ChangeBudget:
		if err := CheckNumber(e.Field, e.Int); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEdit, err)
		}
	case HookDelivery:
		if mode := reviews.HookDeliveryMode(e.String); mode != reviews.HookDeliveryAlways && mode != reviews.HookDeliveryOncePerContext {
			return fmt.Errorf("%w: hook_delivery must be %q or %q", ErrInvalidEdit,
				reviews.HookDeliveryAlways, reviews.HookDeliveryOncePerContext)
		}
	case Mutes:
		return validateMute(*e.Mute)
	case IndexExclude:
		for _, pattern := range e.List {
			if strings.ContainsFunc(pattern, unicode.IsControl) {
				return fmt.Errorf("%w: exclude %q holds a control character", ErrInvalidEdit, pattern)
			}

			if err := index.CheckExclude(pattern); err != nil {
				return fmt.Errorf("%w: exclude %q: %w", ErrInvalidEdit, pattern, err)
			}
		}
	}

	return nil
}

// CheckNumber checks a typed value of threshold, pin_budget, or
// change_budget. The planner and the number editor of the console both
// call it, so the editor shows the verdict of the save. The value must
// be from 1 to math.MaxInt32. The upper bound keeps the value an int on
// every platform and in every reader. A positive value is never replaced
// by a default, so the editor writes no value that the reader ignores.
func CheckNumber(f Field, n int) error {
	if n < 1 || n > math.MaxInt32 {
		return fmt.Errorf("%s must be a whole number from 1 to %d", f, math.MaxInt32)
	}

	return nil
}

// validateWindow checks a review window edit. The UI writes 0 only for
// an explicit unlimited choice; a negative value is legacy input that
// the reader accepts but the editor never writes.
func validateWindow(e Edit) error {
	switch {
	case e.Unlimited && e.Days != 0:
		return fmt.Errorf("%w: choose a day count or unlimited, not both", ErrInvalidEdit)
	case e.Unlimited:
		return nil
	case e.Days < 0:
		return fmt.Errorf("%w: a review window cannot be negative; choose unlimited for no window", ErrInvalidEdit)
	case e.Days == 0:
		return fmt.Errorf("%w: choose a positive day count, unlimited, or the default", ErrInvalidEdit)
	}

	return nil
}

// foreignValue reports a value set for a field that does not take it,
// or any value on a reset. A silent drop would save a different intent.
// A mute field edit needs its operation, so a missing one is foreign
// too.
func foreignValue(e Edit) bool {
	str, boolean, window, list := e.String != "", e.Bool, e.Days != 0 || e.Unlimited, len(e.List) > 0
	integer, mute := e.Int != 0, e.Mute != nil

	if e.Reset {
		return str || boolean || window || list || integer || mute
	}

	switch e.Field {
	case AgentCLI, HookDelivery:
		return boolean || window || list || integer || mute
	case AgentArgv:
		return str || boolean || window || list || integer || mute
	case DistillWrite, IndexGenerated:
		return str || window || list || integer || mute
	case ReviewWindow:
		return str || boolean || list || integer || mute
	case IndexExclude:
		return str || boolean || window || integer || mute
	case Threshold, PinBudget, ChangeBudget:
		return str || boolean || window || list || mute
	case Mutes:
		return str || boolean || window || list || integer || !mute
	default:
		return true
	}
}

// orderEdits puts every set before every reset. A section then never
// disappears and comes back at the end of the file within one plan.
func orderEdits(edits []Edit) []Edit {
	out := slices.Clone(edits)

	slices.SortStableFunc(out, func(a, b Edit) int {
		switch {
		case a.isReset() == b.isReset():
			return 0
		case b.isReset():
			return -1
		default:
			return 1
		}
	})

	return out
}

// splice applies the edits one at a time. It parses the text again
// before each edit, so every edit locates its key in the current text.
func splice(data []byte, edits []Edit) ([]byte, error) {
	cur := data

	for _, e := range edits {
		doc, blocked := parseDocument(cur)
		if blocked != nil {
			return nil, refusal(File, "the edited text does not parse: "+blocked.Reason)
		}

		next, err := applyEdit(doc, e)
		if err != nil {
			return nil, err
		}

		cur = next
	}

	return cur, nil
}

// refusal reports a patch of file that failed its own checks. It means a
// gap in the splice rules, so the file is left as it is.
func refusal(file, reason string) error {
	return fmt.Errorf("settings: refusing to write %s: %s", file, reason)
}

// applyEdit splices one edit into the document text.
func applyEdit(doc *yamledit.Doc, e Edit) ([]byte, error) {
	section, key := e.Field.section(), e.Field.key()

	if e.isReset() {
		return removeKey(doc, section, key)
	}

	switch e.Field {
	case AgentCLI:
		return setScalar(doc, section, key, e.String)
	case DistillWrite, IndexGenerated:
		return setScalar(doc, section, key, e.Bool)
	case ReviewWindow:
		return setScalar(doc, section, key, e.Days)
	case IndexExclude:
		return setList(doc, section, key, e.List)
	default:
		return nil, fmt.Errorf("%w: %s cannot be set here", ErrInvalidEdit, e.Field)
	}
}

// encodeScalar encodes a value through yaml.v3, never by concatenation:
// a pattern such as *.pb.go must be quoted, or it reads as an alias.
func encodeScalar(v any) (string, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEdit, err)
	}

	text := strings.TrimSuffix(string(out), "\n")
	if strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("%w: the value %v needs several lines", ErrInvalidEdit, v)
	}

	return text, nil
}

// spaces returns n spaces.
func spaces(n int) string {
	return strings.Repeat(" ", n)
}

// setScalar sets one scalar field. An existing value is replaced on its
// own line, and a comment after it stays.
func setScalar(doc *yamledit.Doc, section, key string, value any) ([]byte, error) {
	text, err := encodeScalar(value)
	if err != nil {
		return nil, err
	}

	if sk, _, _ := doc.Section(section); sk == nil {
		return appendSection(doc, section, []string{key + ": " + text}), nil
	}

	k, v := doc.Child(section, key)
	if k == nil {
		return insertChild(doc, section, []string{key + ": " + text}), nil
	}

	if err := singleLine(doc, section, key, k, v); err != nil {
		return nil, err
	}

	lines := slices.Clone(doc.Lines)
	line := lines[k.Line-1]

	// The node column is at the tag when the value has one. The tag goes
	// with the old value, because the new value is canonical.
	start := doc.Offset(k.Line, v.Column)
	token := yamledit.SkipProperties(line, start)

	// An empty value ("cli:" or a bare tag) has no token: the value goes
	// after the colon, and the rest of the line keeps only its comment.
	if token == len(line) || line[token] == '#' {
		lines[k.Line-1] = strings.TrimRight(line[:start], " \t") + " " + text + yamledit.TrailingComment(line, start)

		return yamledit.JoinLines(lines, doc.FinalEOL), nil
	}

	end, ok := yamledit.TokenEnd(line, token, v.Style)
	if !ok {
		return nil, &BlockedError{Reason: "the value of " + section + "." + key + " spans several lines", Line: k.Line}
	}

	lines[k.Line-1] = line[:start] + text + line[end:]

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// singleLine refuses a scalar value that is not a single token on its
// key line. The node tree has no end position, so a value that spans
// lines has no safe splice. The lines of a multi-line quoted or block
// value count as content here, also when they start with "#".
func singleLine(doc *yamledit.Doc, section, key string, k, v *yaml.Node) error {
	field := section + "." + key

	switch {
	case v.Kind != yaml.ScalarNode:
		return &BlockedError{Reason: field + " does not hold a single value", Line: k.Line}
	case v.Line != k.Line:
		return &BlockedError{Reason: "the value of " + field + " starts on its own line", Line: k.Line}
	case len(doc.ContentLines(k.Line+1, doc.ChildEnd(section, key))) > 0:
		return &BlockedError{Reason: "the value of " + field + " spans several lines", Line: k.Line}
	}

	return nil
}

// setList replaces one list field. The key line keeps its comment, the
// new items follow in block style, and comment lines of the old list
// stay after the items: the starter file keeps its commented example.
// An item that stays keeps its old line, with its trailing comment.
func setList(doc *yamledit.Doc, section, key string, items []string) ([]byte, error) {
	encoded := make([]string, 0, len(items))

	for _, item := range items {
		text, err := encodeScalar(item)
		if err != nil {
			return nil, err
		}

		encoded = append(encoded, text)
	}

	fresh := func() []string {
		out := []string{key + ":"}
		for _, text := range encoded {
			out = append(out, "  - "+text)
		}

		return out
	}

	if sk, _, _ := doc.Section(section); sk == nil {
		return appendSection(doc, section, fresh()), nil
	}

	k, v := doc.Child(section, key)
	if k == nil {
		return insertChild(doc, section, fresh()), nil
	}

	field := section + "." + key
	end := doc.ChildEnd(section, key)

	if err := listShape(doc, field, k, v, end); err != nil {
		return nil, err
	}

	lines := slices.Clone(doc.Lines)
	keyLine := lines[k.Line-1]

	// A value on the key line (a flow list, a null, or a tag) moves below
	// it. The comment after a flow list starts after its "]".
	if v.Line == k.Line {
		start := doc.Offset(k.Line, v.Column)
		rest := start

		if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0 {
			var ok bool
			if rest, ok = doc.FlowEnd(v); !ok {
				return nil, &BlockedError{Reason: "the flow list of " + field + " has no safe splice", Line: k.Line}
			}
		}

		keyLine = strings.TrimRight(keyLine[:start], " \t") + yamledit.TrailingComment(keyLine, rest)
	}

	kept, err := listItems(doc, field, k, v, items, encoded)
	if err != nil {
		return nil, err
	}

	block := append([]string{keyLine}, kept...)

	for n := k.Line + 1; n <= end; n++ {
		if doc.Comment(n) {
			block = append(block, lines[n-1])
		}
	}

	lines = slices.Replace(lines, k.Line-1, end, block...)

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// listItems returns the item lines of a new list. An item that was in
// the old block list keeps its line, so its text and its trailing
// comment stay. Only a string item stays verbatim: other text, such as
// 5, reads as another type, so it gets the new text and its old comment.
// A removed item with a comment after its text blocks the edit, because
// the comment would go with the line. A flow list keeps the comment of
// its key line (setList), so it has no item line to check.
func listItems(doc *yamledit.Doc, field string, k, v *yaml.Node, items, encoded []string) ([]string, error) {
	indent := k.Column - 1 + 2
	old := map[string][]*yaml.Node{}

	var olds []*yaml.Node

	if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
		olds = v.Content

		// The first item line gives the dash column; the node column is
		// at a tag when the list has one.
		indent = yamledit.LeadingSpaces(doc.Lines[olds[0].Line-1])

		for _, item := range olds {
			old[item.Value] = append(old[item.Value], item)
		}
	}

	out := make([]string, 0, len(items))
	kept := map[*yaml.Node]bool{}

	for i, item := range items {
		line := spaces(indent) + "- " + encoded[i]

		if prev := old[item]; len(prev) > 0 {
			old[item] = prev[1:]
			kept[prev[0]] = true
			line = keptItem(doc, prev[0], line)
		}

		out = append(out, line)
	}

	for _, item := range olds {
		if !kept[item] && doc.CommentIn(item.Line, item.Line) {
			return nil, &BlockedError{Reason: "a removed item of " + field + " holds a comment, which the edit would drop", Line: item.Line}
		}
	}

	return out, nil
}

// keptItem returns the line of an old item that stays in the list. line
// is the freshly encoded line, for an item that is not a string.
func keptItem(doc *yamledit.Doc, item *yaml.Node, line string) string {
	old := doc.Lines[item.Line-1]

	if item.ShortTag() == "!!str" {
		return old
	}

	end, ok := yamledit.TokenEnd(old, yamledit.SkipProperties(old, doc.Offset(item.Line, item.Column)), item.Style)
	if !ok {
		return line
	}

	return line + yamledit.TrailingComment(old, end)
}

// listShape refuses a list value that has no safe splice: a list that
// spans lines in flow style, or an item that spans lines.
func listShape(doc *yamledit.Doc, field string, k, v *yaml.Node, end int) error {
	content := doc.ContentLines(k.Line+1, end)

	switch {
	case yamledit.IsNull(v):
		if len(content) > 0 {
			return &BlockedError{Reason: "the value of " + field + " spans several lines", Line: k.Line}
		}
	case v.Kind != yaml.SequenceNode:
		return &BlockedError{Reason: field + " is not a list", Line: k.Line}
	case v.Style&yaml.FlowStyle != 0:
		if v.Line != k.Line || len(content) > 0 {
			return &BlockedError{Reason: "the flow list of " + field + " spans several lines", Line: k.Line}
		}
	default:
		for _, item := range v.Content {
			if item.Kind != yaml.ScalarNode || item.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
				return &BlockedError{Reason: "an item of " + field + " spans several lines", Line: item.Line}
			}
		}

		if len(content) != len(v.Content) {
			return &BlockedError{Reason: "an item of " + field + " spans several lines", Line: k.Line}
		}
	}

	return nil
}

// indentAll prefixes each line with n spaces.
func indentAll(lines []string, n int) []string {
	out := make([]string, 0, len(lines))

	for _, l := range lines {
		out = append(out, spaces(n)+l)
	}

	return out
}

// insertChild adds child lines at the end of an existing section. The
// lines get the indentation of the other children, or two spaces more
// than the section key. A null token on the section line ("agent: ~")
// goes away, because a mapping follows now.
func insertChild(doc *yamledit.Doc, section string, child []string) []byte {
	sk, sv, idx := doc.Section(section)
	lines := slices.Clone(doc.Lines)

	indent := sk.Column - 1 + 2
	if sv.Kind == yaml.MappingNode && len(sv.Content) > 0 {
		indent = sv.Content[0].Column - 1
	}

	// A null token ("~", "null", or a bare "!!null" tag) holds no quote,
	// so the comment search can start at the token itself. A line with
	// no token and no tag stays as it is.
	if yamledit.IsNull(sv) && sv.Line == sk.Line && (sv.Value != "" || sv.Style&yaml.TaggedStyle != 0) {
		line := lines[sk.Line-1]
		start := doc.Offset(sk.Line, sv.Column)
		lines[sk.Line-1] = strings.TrimRight(line[:start], " \t") + yamledit.TrailingComment(line, start)
	}

	end := doc.SectionEnd(idx)
	lines = slices.Insert(lines, end, indentAll(child, indent)...)

	return yamledit.JoinLines(lines, doc.FinalEOL)
}

// appendSection adds a new section at the end of the document content.
func appendSection(doc *yamledit.Doc, section string, child []string) []byte {
	return appendTopLevel(doc, append([]string{section + ":"}, indentAll(child, 2)...))
}

// appendTopLevel adds new top-level lines at the end of the document
// content, after a blank line. The indentation follows the existing
// top-level keys. Text after a "..." end marker is not read, so the
// lines go before the marker. Both documents add a key here.
func appendTopLevel(doc *yamledit.Doc, block []string) []byte {
	lines := slices.Clone(doc.Lines)
	pos, indent, lead := doc.SectionStart()

	lead = append(lead, indentAll(block, indent)...)

	finalEOL := doc.FinalEOL || pos == len(lines)
	lines = slices.Insert(lines, pos, lead...)

	return yamledit.JoinLines(lines, finalEOL)
}

// removeKey deletes one field. Its comment lines stay, because they are
// the user's prose. The lines of a multi-line value are value text, also
// when they start with "#", so they go. A comment after the value text
// of a line below the key line blocks the reset, because it would go
// with its line. The comment on the key line goes with the key, because
// it describes the key. A section left without keys and without
// comments goes too.
func removeKey(doc *yamledit.Doc, section, key string) ([]byte, error) {
	k, _ := doc.Child(section, key)
	if k == nil {
		return yamledit.JoinLines(doc.Lines, doc.FinalEOL), nil
	}

	end := doc.ChildEnd(section, key)
	lines := slices.Clone(doc.Lines)

	var keep []string

	for n := k.Line + 1; n <= end; n++ {
		switch {
		case doc.Comment(n):
			keep = append(keep, lines[n-1])
		case doc.CommentIn(n, n):
			return nil, &BlockedError{Reason: "the value of " + section + "." + key + " holds a comment, which the edit would drop", Line: n}
		}
	}

	lines = slices.Replace(lines, k.Line-1, end, keep...)
	data := yamledit.JoinLines(lines, doc.FinalEOL)

	after, blocked := parseDocument(data)
	if blocked != nil {
		return nil, refusal(File, "the edited text does not parse: "+blocked.Reason)
	}

	return removeEmptySection(after, section), nil
}

// removeEmptySection deletes a section that holds no key and no comment.
// A comment directly above, on, or inside the section keeps it as a bare
// "section:" line, which the readers decode as the defaults. A section on
// line 1 gives its byte order mark to the new line 1 (keepMark).
func removeEmptySection(doc *yamledit.Doc, section string) []byte {
	sk, sv, idx := doc.Section(section)
	if sk == nil || !yamledit.IsNull(sv) {
		return yamledit.JoinLines(doc.Lines, doc.FinalEOL)
	}

	end := doc.SectionEnd(idx)

	// CommentIn also finds a comment after the text of the section line.
	// Its scan starts after the key text, as every comment check does.
	commented := doc.CommentIn(sk.Line, end) || (sk.Line > 1 && doc.Comment(sk.Line-1))

	if commented {
		return yamledit.JoinLines(doc.Lines, doc.FinalEOL)
	}

	lines := slices.Delete(slices.Clone(doc.Lines), sk.Line-1, end)

	// Keep one blank line between the neighbours, not two.
	at := sk.Line - 1

	switch {
	case at < len(lines) && yamledit.IsBlank(lines[at]) && (at == 0 || yamledit.IsBlank(lines[at-1])):
		lines = slices.Delete(lines, at, at+1)
	case at == len(lines) && at > 0 && yamledit.IsBlank(lines[at-1]):
		lines = slices.Delete(lines, at-1, at)
	}

	// The blank-line rule runs first: a line with only the mark is not
	// blank, so a mark on a blank line would keep that line.
	return yamledit.JoinLines(keepMark(doc, lines), doc.FinalEOL)
}

// keepMark puts the byte order mark of doc at the start of lines. A
// splice that deletes line 1 deletes the mark with it, but the mark
// belongs to the file, not to the line. An empty result stays empty: it
// holds no text to mark, and every reader reads it as the defaults.
func keepMark(doc *yamledit.Doc, lines []string) []string {
	mark := yamledit.ByteOrderMark

	if len(lines) > 0 && len(doc.Lines) > 0 && strings.HasPrefix(doc.Lines[0], mark) && !strings.HasPrefix(lines[0], mark) {
		lines[0] = mark + lines[0]
	}

	return lines
}

// verify is the safety net under the splice rules. It proves that only
// the intended keys changed and that every reader still accepts the
// file. It returns the reading of the new text.
func verify(snap *Snapshot, reg *integration.Registry, group Group, edits []Edit, after []byte) (reading, error) {
	afterDoc, blocked := parseDocument(after)
	if blocked != nil {
		return reading{}, refusal(File, "the edited text does not parse: "+blocked.Reason)
	}

	section := group.section()

	if err := sameOtherSections(snap.doc, afterDoc, section); err != nil {
		return reading{}, err
	}

	if err := intendedSection(snap.doc, afterDoc, section, edits); err != nil {
		return reading{}, err
	}

	was, now, err := lineDocs(File, snap.doc, snap.data, afterDoc, after)
	if err != nil {
		return reading{}, err
	}

	if err := sameComments(File, was, now, resetKeyLines(was, edits)); err != nil {
		return reading{}, err
	}

	r := read(snap.Root, reg, after)
	afterErrs := r.errs()

	// A reader that accepted the file before must accept it after. The
	// owning reader accepted it before, or Editable refused the plan.
	for _, name := range []string{ReaderAgent, ReaderDistill, ReaderIndex} {
		if snap.ReaderErrs[name] == nil && afterErrs[name] != nil {
			return reading{}, refusal(File, fmt.Sprintf("the %s reader would reject the result: %v", name, afterErrs[name]))
		}
	}

	if group == GroupInference && r.invocationErr != nil {
		return reading{}, fmt.Errorf("%w: after the change the agent does not resolve: %w", ErrInvalidEdit, r.invocationErr)
	}

	return r, nil
}

// lineDocs refuses a candidate that loses or gains the byte order mark
// of the reviewed text. An empty candidate holds no text to mark, so it
// passes. lineDocs then returns the docs of the reviewed and the
// candidate text without the mark, for the line checks of both
// verifiers. A reset of line 1 moves the mark onto the next line. Also,
// yamledit does not read a marked comment line as a comment. So the line
// checks compare the text without the mark.
func lineDocs(file string, before *yamledit.Doc, was []byte, after *yamledit.Doc, now []byte) (b, a *yamledit.Doc, err error) {
	mark := []byte(yamledit.ByteOrderMark)

	if len(now) > 0 && bytes.HasPrefix(was, mark) != bytes.HasPrefix(now, mark) {
		return nil, nil, refusal(file, "the byte order mark at the start of the file would change")
	}

	if b, err = unmarked(file, before, was); err != nil {
		return nil, nil, err
	}

	if a, err = unmarked(file, after, now); err != nil {
		return nil, nil, err
	}

	return b, a, nil
}

// unmarked returns doc, the parse of data, when data has no byte order
// mark. Else it parses data again without the mark. The parse gives the
// same nodes and line numbers, because yaml.v3 skips the mark.
func unmarked(file string, doc *yamledit.Doc, data []byte) (*yamledit.Doc, error) {
	rest, ok := bytes.CutPrefix(data, []byte(yamledit.ByteOrderMark))
	if !ok {
		return doc, nil
	}

	plain, issue := yamledit.Parse(rest)
	if issue != nil {
		return nil, refusal(file, "the edited text does not parse: "+issue.Reason)
	}

	return plain, nil
}

// sameComments refuses a candidate that loses or changes a comment. The
// comment lines must stay the same, in the same order. The comments
// after the value text of the lines must stay too. Their order can
// change, because a new order of a list moves each item with its
// comment. skip holds the lines of before whose comment the plan may
// drop. The check reads only the lines of the two texts, never the
// spans of a splice. So a splice that skips its comment check cannot
// pass.
func sameComments(file string, before, after *yamledit.Doc, skip []int) error {
	if !slices.Equal(commentLines(before), commentLines(after)) {
		return refusal(file, "a comment line changed")
	}

	was, now := valueComments(before, skip), valueComments(after, nil)
	slices.Sort(was)
	slices.Sort(now)

	if !slices.Equal(was, now) {
		return refusal(file, "a comment after a value changed")
	}

	return nil
}

// commentLines lists the lines of doc that hold only a comment, in
// order.
func commentLines(doc *yamledit.Doc) []string {
	var out []string

	for n := 1; n <= len(doc.Lines); n++ {
		if doc.Comment(n) {
			out = append(out, doc.Lines[n-1])
		}
	}

	return out
}

// valueComments lists the comments after the value text of the lines of
// doc. It leaves out the comment lines and the lines in skip.
func valueComments(doc *yamledit.Doc, skip []int) []string {
	var out []string

	for n := 1; n <= len(doc.Lines); n++ {
		if doc.Comment(n) || slices.Contains(skip, n) {
			continue
		}

		if c := doc.LineComment(n); c != "" {
			out = append(out, c)
		}
	}

	return out
}

// resetKeyLines returns the key lines in doc of the keys that edits
// reset. A reset deletes its key line with the comment on it, by design:
// the comment describes the key, and the key goes.
func resetKeyLines(doc *yamledit.Doc, edits []Edit) []int {
	var out []int

	for _, e := range edits {
		if !e.isReset() {
			continue
		}

		k, _, _ := doc.Section(e.Field.section())
		if key := e.Field.key(); key != "" {
			k, _ = doc.Child(e.Field.section(), key)
		}

		if k != nil {
			out = append(out, k.Line)
		}
	}

	return out
}

// sameOtherSections requires every other top-level entry to be equal in
// order and structure. Comments and positions are not compared: a new
// neighbour can move a comment between nodes without changing a value.
func sameOtherSections(before, after *yamledit.Doc, section string) error {
	was, wasPos := yamledit.PairsExcept(before.Root, section)
	now, nowPos := yamledit.PairsExcept(after.Root, section)

	if key, same := yamledit.SamePairs(was, now); !same {
		if key == "" {
			return refusal(File, "the other top-level keys changed")
		}

		return refusal(File, fmt.Sprintf("the %s section changed", key))
	}

	if wasPos >= 0 && nowPos >= 0 && wasPos != nowPos {
		return refusal(File, fmt.Sprintf("the %s section moved", section))
	}

	return nil
}

// intendedSection requires the edited section to equal the section as
// it was with the edits applied in memory. Unknown keys of the section
// must survive, and no other key may change.
func intendedSection(before, after *yamledit.Doc, section string, edits []Edit) error {
	want, err := sectionMap(before, section)
	if err != nil {
		return refusal(File, err.Error())
	}

	got, err := sectionMap(after, section)
	if err != nil {
		return refusal(File, err.Error())
	}

	if want == nil {
		want = map[string]any{}
	}

	for _, e := range edits {
		key := e.Field.key()

		switch {
		case e.isReset():
			delete(want, key)
		case e.Field == AgentCLI:
			want[key] = e.String
		case e.Field == DistillWrite, e.Field == IndexGenerated:
			want[key] = e.Bool
		case e.Field == ReviewWindow:
			want[key] = e.Days
		case e.Field == IndexExclude:
			items := make([]any, 0, len(e.List))
			for _, s := range e.List {
				items = append(items, s)
			}

			want[key] = items
		}
	}

	// An empty section, a null section, and no section all decode to
	// the defaults, so they are equal here.
	if len(want) == 0 {
		want = nil
	}

	if len(got) == 0 {
		got = nil
	}

	if !reflect.DeepEqual(want, got) {
		return refusal(File, fmt.Sprintf("the %s section does not hold exactly the intended change", section))
	}

	return nil
}

// sectionMap decodes one section, or returns nil when it is absent or
// null. Owned sections hold no duplicate key and no alias, so the
// decode is exact.
func sectionMap(doc *yamledit.Doc, section string) (map[string]any, error) {
	if doc.Root == nil {
		return nil, nil
	}

	_, v, _ := doc.Section(section)
	if v == nil || yamledit.IsNull(v) {
		return nil, nil
	}

	var m map[string]any
	if err := v.Decode(&m); err != nil {
		return nil, fmt.Errorf("the %s section does not decode: %w", section, err)
	}

	return m, nil
}

// activation explains when each edited field takes effect (spec R-13).
// The texts that depend on the snapshot join the texts of the field.
func activation(snap *Snapshot, edits []Edit) []Activation {
	argvSet := snap.Agent != nil && len(snap.Agent.Agent.Argv) > 0
	removesArgv := slices.ContainsFunc(edits, func(e Edit) bool { return e.Field == AgentArgv && e.Reset })

	var out []Activation

	for _, e := range edits {
		a := e.Field.Activation()

		if e.Field == AgentCLI && argvSet && !removesArgv {
			a.Notes = append(a.Notes, "agent.argv stays in effect, so the command that runs does not change.")
		}

		if e.Field == AgentArgv {
			a.Notes = append(a.Notes, "The custom command goes away; the agent.cli preset takes effect.")
		}

		out = append(out, a)
	}

	return out
}

// Activation returns when a saved value of the field takes effect and
// what else the user must know, without the notes that depend on one
// snapshot. The Settings detail shows it before a draft exists, and
// every plan starts from it.
func (f Field) Activation() Activation {
	a := Activation{Field: f}

	switch f.Group() {
	case GroupInference:
		a.When = whenDistillation
		a.Notes = []string{
			"Claude and Codex integration files do not change.",
			"An open distillation preview or trigger extraction review must be prepared again.",
			"Saving runs no agent and checks no login.",
		}
	case GroupPinWrite:
		a.When = whenPreview
		a.Notes = []string{
			"Saving writes no pin; only a new preview offers the write.",
			"The setting also governs seamark lessons --apply, --prune, and --retarget.",
		}
	case GroupEvidence:
		a.When = whenEvidence
		a.Notes = append([]string{"The current corpus does not change until an evidence refresh succeeds."}, windowNotes()...)
	case GroupIndexing:
		a.When = whenIndexBuild
		a.Notes = []string{"Nothing is scanned or removed until the build."}
	case GroupDelivery:
		a.When = lessonsWhen(f)
		a.Notes = []string{noteNextRead, noteDeliveryWont, noteHookState}
	case GroupMutes:
		a.When = lessonsWhen(f)
		a.Notes = []string{noteNextRead}
	}

	return a
}

// Activation texts of the lessons fields (spec R-9). They name every
// consumer of the value, and what the value never changes. A consumer
// reads lessons.yaml when it runs, so a save changes no running hook.
const (
	whenMined        = "the next edit hook, change_set, check, why, seamark lessons --file, and the Stats lesson counts; mined lessons only, never pins"
	whenChangeBudget = "one change_set answer or one check advisory, shared by all its files; not the hook or why"
	whenHookDelivery = "repetition in the edit hook only; change_set, check, why, and CLI views always show their selection"

	noteNextRead     = "Applies when a consumer next reads the file; a hook that runs now does not change."
	noteDeliveryWont = "Saving does not install or change hooks, grant trust, reset or delete delivery state, re-mine, or call a model."
	noteHookState    = "The hook state file stays; switching back can reuse still-valid context state."
)

// lessonsWhen names the consumers of one lessons field. The pin budget
// text takes the hook total cap from its owner, internal/report.
func lessonsWhen(f Field) string {
	switch f {
	case Threshold, Mutes:
		return whenMined
	case PinBudget:
		return fmt.Sprintf("the edit hook only, at most %d lessons in total per edit; "+
			"pins come first, so a budget at the total leaves no room for mined lessons", report.HookLessonTotal)
	case ChangeBudget:
		return whenChangeBudget
	case HookDelivery:
		return whenHookDelivery
	default:
		return ""
	}
}
