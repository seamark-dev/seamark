package settings

// The patch planner. It validates one group of edits and splices the
// text of the observed file. It then verifies the result with every
// reader. Last, the setup engine guards and previews the write.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
)

// Edit is one field change of a draft. Reset removes the key, so the
// reader default applies; it never writes today's default value. The
// value fields that apply depend on Field.
type Edit struct {
	Field Field
	// Reset removes the key from the file.
	Reset bool
	// String is the agent.cli preset: a registered invocation ID.
	String string
	// Bool is the value of distill.write or index.generated.
	Bool bool
	// Days is a positive reviews.window_days value.
	Days int
	// Unlimited sets reviews.window_days to 0, the reader's unlimited.
	Unlimited bool
	// List is the index.exclude patterns. An empty list is a reset.
	List []string
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

// Activation text: when a field takes effect (spec R-13).
const (
	whenDistillation = "next prepared distillation"
	whenPreview      = "next proposal preview"
	whenEvidence     = "next confirmed evidence refresh"
	whenIndexBuild   = "next explicit index build"
)

// Notes that every save shows. The index fingerprint hashes the whole
// file, so any save marks the index stale; saying otherwise is false.
// The commit note is advice, not a fact: the file can be new, untracked,
// or ignored, and the plan does not ask git.
var saveNotes = []string{
	"The index reads as stale until the next index build, because its fingerprint covers the whole configuration file.",
	"Review the diff, then commit the file so the team shares it.",
}

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

	if err := snap.Editable(group); err != nil {
		return nil, err
	}

	// The patch starts from the snapshot bytes. They must still be the
	// file bytes, or the before values in the preview would be false.
	if now, _, err := integration.ReadInput(snap.Root, File); err != nil || now != snap.Guard {
		return nil, staleError()
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
		Before:   groupFields(snap.Fields, group),
		After:    groupFields(afterRead.fields(snap.Presets), group),
	}

	if bytes.Equal(after, snap.data) {
		plan.NoOp = true
		plan.Notes = []string{"Nothing changes: the file already holds these values."}

		return plan, nil
	}

	written := after
	if snap.crlf {
		written = bytes.ReplaceAll(after, []byte("\n"), []byte("\r\n"))
	}

	if err := plan.guard(reg, written); err != nil {
		return nil, err
	}

	plan.Activation = activation(snap, group, ordered)
	plan.Notes = slices.Clone(saveNotes)

	return plan, nil
}

// guard builds the setup plan (Route A): one client-less common document
// whose Compose returns the planned bytes. The setup engine then owns
// the guard, the link refusal, the redacted diff, and the atomic write.
func (p *Plan) guard(reg *integration.Registry, written []byte) error {
	snap := p.Snapshot

	// Compose receives the exact bytes that the plan guard hashes. A
	// difference from the snapshot means the file changed meanwhile.
	compose := func(existing []byte, exists bool) ([]byte, error) {
		if exists != snap.Guard.Exists || (exists && sha256.Sum256(existing) != snap.Guard.SHA256) {
			return nil, ErrStale
		}

		return written, nil
	}

	setup, err := integration.PlanSetup(reg, integration.SetupRequest{
		Root: snap.Root,
		Common: []integration.Document{{
			Path:       File,
			Compose:    compose,
			Detail:     "update " + p.Group.String() + " settings",
			KeptDetail: "no change",
		}},
	})

	switch {
	case errors.Is(err, ErrStale), errors.Is(err, integration.ErrStalePlan):
		return staleError()
	case err != nil:
		return err
	case len(setup.Reads) != 1 || setup.Reads[0] != snap.Guard || len(setup.Writes) != 1:
		return staleError()
	}

	previews := integration.PreviewWrites(setup)
	if len(previews) != 1 || previews[0].Stale || previews[0].Err != nil {
		return staleError()
	}

	p.Setup, p.Preview = setup, previews[0]

	return nil
}

// staleError is the ErrStale that names the file.
func staleError() error {
	return fmt.Errorf("%w: %s; inspect it again", ErrStale, File)
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
func foreignValue(e Edit) bool {
	str, boolean, window, list := e.String != "", e.Bool, e.Days != 0 || e.Unlimited, len(e.List) > 0

	if e.Reset {
		return str || boolean || window || list
	}

	switch e.Field {
	case AgentCLI:
		return boolean || window || list
	case AgentArgv:
		return str || boolean || window || list
	case DistillWrite, IndexGenerated:
		return str || window || list
	case ReviewWindow:
		return str || boolean || list
	default:
		return str || boolean || window
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
			return nil, refusal("the edited text does not parse: " + blocked.Reason)
		}

		next, err := applyEdit(doc, e)
		if err != nil {
			return nil, err
		}

		cur = next
	}

	return cur, nil
}

// refusal reports a patch that failed its own checks. It means a gap in
// the splice rules, so the file is left as it is.
func refusal(reason string) error {
	return fmt.Errorf("settings: refusing to write %s: %s", File, reason)
}

// applyEdit splices one edit into the document text.
func applyEdit(doc *document, e Edit) ([]byte, error) {
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
func setScalar(doc *document, section, key string, value any) ([]byte, error) {
	text, err := encodeScalar(value)
	if err != nil {
		return nil, err
	}

	if sk, _, _ := doc.section(section); sk == nil {
		return appendSection(doc, section, []string{key + ": " + text}), nil
	}

	k, v := doc.child(section, key)
	if k == nil {
		return insertChild(doc, section, []string{key + ": " + text}), nil
	}

	if err := singleLine(doc, section, key, k, v); err != nil {
		return nil, err
	}

	lines := slices.Clone(doc.lines)
	line := lines[k.Line-1]

	// The node column is at the tag when the value has one. The tag goes
	// with the old value, because the new value is canonical.
	start := doc.offset(k.Line, v.Column)
	token := skipProperties(line, start)

	// An empty value ("cli:" or a bare tag) has no token: the value goes
	// after the colon, and the rest of the line keeps only its comment.
	if token == len(line) || line[token] == '#' {
		lines[k.Line-1] = strings.TrimRight(line[:start], " \t") + " " + text + trailingComment(line, start)

		return joinLines(lines, doc.finalEOL), nil
	}

	end, ok := tokenEnd(line, token, v.Style)
	if !ok {
		return nil, &BlockedError{Reason: "the value of " + section + "." + key + " spans several lines", Line: k.Line}
	}

	lines[k.Line-1] = line[:start] + text + line[end:]

	return joinLines(lines, doc.finalEOL), nil
}

// singleLine refuses a scalar value that is not a single token on its
// key line. The node tree has no end position, so a value that spans
// lines has no safe splice. The lines of a multi-line quoted or block
// value count as content here, also when they start with "#".
func singleLine(doc *document, section, key string, k, v *yaml.Node) error {
	field := section + "." + key

	switch {
	case v.Kind != yaml.ScalarNode:
		return &BlockedError{Reason: field + " does not hold a single value", Line: k.Line}
	case v.Line != k.Line:
		return &BlockedError{Reason: "the value of " + field + " starts on its own line", Line: k.Line}
	case len(doc.contentLines(k.Line+1, doc.childEnd(section, key))) > 0:
		return &BlockedError{Reason: "the value of " + field + " spans several lines", Line: k.Line}
	}

	return nil
}

// setList replaces one list field. The key line keeps its comment, the
// new items follow in block style, and comment lines of the old list
// stay after the items: the starter file keeps its commented example.
// An item that stays keeps its old line, with its trailing comment.
func setList(doc *document, section, key string, items []string) ([]byte, error) {
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

	if sk, _, _ := doc.section(section); sk == nil {
		return appendSection(doc, section, fresh()), nil
	}

	k, v := doc.child(section, key)
	if k == nil {
		return insertChild(doc, section, fresh()), nil
	}

	field := section + "." + key
	end := doc.childEnd(section, key)

	if err := listShape(doc, field, k, v, end); err != nil {
		return nil, err
	}

	lines := slices.Clone(doc.lines)
	keyLine := lines[k.Line-1]

	// A value on the key line (a flow list, a null, or a tag) moves below
	// it. The comment after a flow list starts after its "]".
	if v.Line == k.Line {
		start := doc.offset(k.Line, v.Column)
		rest := start

		if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0 {
			var ok bool
			if rest, ok = doc.flowEnd(v); !ok {
				return nil, &BlockedError{Reason: "the flow list of " + field + " has no safe splice", Line: k.Line}
			}
		}

		keyLine = strings.TrimRight(keyLine[:start], " \t") + trailingComment(keyLine, rest)
	}

	block := append([]string{keyLine}, listItems(doc, k, v, items, encoded)...)

	for n := k.Line + 1; n <= end; n++ {
		if doc.comment(n) {
			block = append(block, lines[n-1])
		}
	}

	lines = slices.Replace(lines, k.Line-1, end, block...)

	return joinLines(lines, doc.finalEOL), nil
}

// listItems returns the item lines of a new list. An item that was in
// the old block list keeps its line, so its text and its trailing
// comment stay. Only a string item stays verbatim: other text, such as
// 5, reads as another type, so it gets the new text and its old comment.
// A removed item goes with its comment.
func listItems(doc *document, k, v *yaml.Node, items, encoded []string) []string {
	indent := k.Column - 1 + 2
	old := map[string][]*yaml.Node{}

	if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
		// The first item line gives the dash column; the node column is
		// at a tag when the list has one.
		indent = leadingSpaces(doc.lines[v.Content[0].Line-1])

		for _, item := range v.Content {
			old[item.Value] = append(old[item.Value], item)
		}
	}

	out := make([]string, 0, len(items))

	for i, item := range items {
		line := spaces(indent) + "- " + encoded[i]

		if prev := old[item]; len(prev) > 0 {
			old[item] = prev[1:]
			line = keptItem(doc, prev[0], line)
		}

		out = append(out, line)
	}

	return out
}

// keptItem returns the line of an old item that stays in the list. line
// is the freshly encoded line, for an item that is not a string.
func keptItem(doc *document, item *yaml.Node, line string) string {
	old := doc.lines[item.Line-1]

	if item.ShortTag() == "!!str" {
		return old
	}

	end, ok := tokenEnd(old, skipProperties(old, doc.offset(item.Line, item.Column)), item.Style)
	if !ok {
		return line
	}

	return line + trailingComment(old, end)
}

// listShape refuses a list value that has no safe splice: a list that
// spans lines in flow style, or an item that spans lines.
func listShape(doc *document, field string, k, v *yaml.Node, end int) error {
	content := doc.contentLines(k.Line+1, end)

	switch {
	case isNull(v):
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
func insertChild(doc *document, section string, child []string) []byte {
	sk, sv, idx := doc.section(section)
	lines := slices.Clone(doc.lines)

	indent := sk.Column - 1 + 2
	if sv.Kind == yaml.MappingNode && len(sv.Content) > 0 {
		indent = sv.Content[0].Column - 1
	}

	// A null token ("~", "null", or a bare "!!null" tag) holds no quote,
	// so the comment search can start at the token itself. A line with
	// no token and no tag stays as it is.
	if isNull(sv) && sv.Line == sk.Line && (sv.Value != "" || sv.Style&yaml.TaggedStyle != 0) {
		line := lines[sk.Line-1]
		start := doc.offset(sk.Line, sv.Column)
		lines[sk.Line-1] = strings.TrimRight(line[:start], " \t") + trailingComment(line, start)
	}

	end := doc.sectionEnd(idx)
	lines = slices.Insert(lines, end, indentAll(child, indent)...)

	return joinLines(lines, doc.finalEOL)
}

// appendSection adds a new section at the end of the document content,
// after a blank line. The indentation follows the existing top-level
// keys. Text after a "..." end marker is not read, so the section goes
// before the marker.
func appendSection(doc *document, section string, child []string) []byte {
	lines := slices.Clone(doc.lines)

	indent := 0
	if doc.root != nil {
		indent = doc.root.Content[0].Column - 1
	}

	pos := doc.limit - 1

	var block []string
	if pos > 0 && !isBlank(lines[pos-1]) {
		block = append(block, "")
	}

	block = append(block, spaces(indent)+section+":")
	block = append(block, indentAll(child, indent+2)...)

	finalEOL := doc.finalEOL || pos == len(lines)
	lines = slices.Insert(lines, pos, block...)

	return joinLines(lines, finalEOL)
}

// removeKey deletes one field. Its comment lines stay, because they are
// the user's prose. The lines of a multi-line value are value text, also
// when they start with "#", so they go. A section left without keys and
// without comments goes too.
func removeKey(doc *document, section, key string) ([]byte, error) {
	k, _ := doc.child(section, key)
	if k == nil {
		return joinLines(doc.lines, doc.finalEOL), nil
	}

	end := doc.childEnd(section, key)
	lines := slices.Clone(doc.lines)

	var keep []string

	for n := k.Line + 1; n <= end; n++ {
		if doc.comment(n) {
			keep = append(keep, lines[n-1])
		}
	}

	lines = slices.Replace(lines, k.Line-1, end, keep...)
	data := joinLines(lines, doc.finalEOL)

	after, blocked := parseDocument(data)
	if blocked != nil {
		return nil, refusal("the edited text does not parse: " + blocked.Reason)
	}

	return removeEmptySection(after, section), nil
}

// removeEmptySection deletes a section that holds no key and no comment.
// A comment directly above, on, or inside the section keeps it as a bare
// "section:" line, which the readers decode as the defaults.
func removeEmptySection(doc *document, section string) []byte {
	sk, sv, idx := doc.section(section)
	if sk == nil || !isNull(sv) {
		return joinLines(doc.lines, doc.finalEOL)
	}

	end := doc.sectionEnd(idx)

	commented := doc.hasComment(sk.Line+1, end) ||
		commentStart(doc.lines[sk.Line-1], 0) < len(doc.lines[sk.Line-1]) ||
		(sk.Line > 1 && doc.comment(sk.Line-1))

	if commented {
		return joinLines(doc.lines, doc.finalEOL)
	}

	lines := slices.Delete(slices.Clone(doc.lines), sk.Line-1, end)

	// Keep one blank line between the neighbours, not two.
	at := sk.Line - 1

	switch {
	case at < len(lines) && isBlank(lines[at]) && (at == 0 || isBlank(lines[at-1])):
		lines = slices.Delete(lines, at, at+1)
	case at == len(lines) && at > 0 && isBlank(lines[at-1]):
		lines = slices.Delete(lines, at-1, at)
	}

	return joinLines(lines, doc.finalEOL)
}

// verify is the safety net under the splice rules. It proves that only
// the intended keys changed and that every reader still accepts the
// file. It returns the reading of the new text.
func verify(snap *Snapshot, reg *integration.Registry, group Group, edits []Edit, after []byte) (reading, error) {
	afterDoc, blocked := parseDocument(after)
	if blocked != nil {
		return reading{}, refusal("the edited text does not parse: " + blocked.Reason)
	}

	section := group.section()

	if err := sameOtherSections(snap.doc, afterDoc, section); err != nil {
		return reading{}, err
	}

	if err := intendedSection(snap.doc, afterDoc, section, edits); err != nil {
		return reading{}, err
	}

	r := read(snap.Root, reg, after)
	afterErrs := r.errs()

	// A reader that accepted the file before must accept it after. The
	// owning reader accepted it before, or Editable refused the plan.
	for _, name := range []string{ReaderAgent, ReaderDistill, ReaderIndex} {
		if snap.ReaderErrs[name] == nil && afterErrs[name] != nil {
			return reading{}, refusal(fmt.Sprintf("the %s reader would reject the result: %v", name, afterErrs[name]))
		}
	}

	if group == GroupInference && r.invocationErr != nil {
		return reading{}, fmt.Errorf("%w: after the change the agent does not resolve: %w", ErrInvalidEdit, r.invocationErr)
	}

	return r, nil
}

// sameOtherSections requires every other top-level entry to be equal in
// order and structure. Comments and positions are not compared: a new
// neighbour can move a comment between nodes without changing a value.
func sameOtherSections(before, after *document, section string) error {
	others := func(doc *document) (pairs [][2]*yaml.Node, pos int) {
		pos = -1

		if doc.root == nil {
			return nil, pos
		}

		for i := 0; i < len(doc.root.Content); i += 2 {
			if doc.root.Content[i].Value == section {
				pos = len(pairs)

				continue
			}

			pairs = append(pairs, [2]*yaml.Node{doc.root.Content[i], doc.root.Content[i+1]})
		}

		return pairs, pos
	}

	was, wasPos := others(before)
	now, nowPos := others(after)

	if len(was) != len(now) {
		return refusal("the other top-level keys changed")
	}

	for i := range was {
		if !nodeEqual(was[i][0], now[i][0]) || !nodeEqual(was[i][1], now[i][1]) {
			return refusal(fmt.Sprintf("the %s section changed", was[i][0].Value))
		}
	}

	if wasPos >= 0 && nowPos >= 0 && wasPos != nowPos {
		return refusal(fmt.Sprintf("the %s section moved", section))
	}

	return nil
}

// nodeEqual compares two node trees by value, tag, style, and anchors.
func nodeEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || a.Anchor != b.Anchor ||
		a.Style != b.Style || len(a.Content) != len(b.Content) {
		return false
	}

	for i := range a.Content {
		if !nodeEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}

	return true
}

// intendedSection requires the edited section to equal the section as
// it was with the edits applied in memory. Unknown keys of the section
// must survive, and no other key may change.
func intendedSection(before, after *document, section string, edits []Edit) error {
	want, err := sectionMap(before, section)
	if err != nil {
		return refusal(err.Error())
	}

	got, err := sectionMap(after, section)
	if err != nil {
		return refusal(err.Error())
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
		return refusal(fmt.Sprintf("the %s section does not hold exactly the intended change", section))
	}

	return nil
}

// sectionMap decodes one section, or returns nil when it is absent or
// null. Owned sections hold no duplicate key and no alias, so the
// decode is exact.
func sectionMap(doc *document, section string) (map[string]any, error) {
	if doc.root == nil {
		return nil, nil
	}

	_, v, _ := doc.section(section)
	if v == nil || isNull(v) {
		return nil, nil
	}

	var m map[string]any
	if err := v.Decode(&m); err != nil {
		return nil, fmt.Errorf("the %s section does not decode: %w", section, err)
	}

	return m, nil
}

// activation explains when each edited field takes effect (spec R-13).
func activation(snap *Snapshot, group Group, edits []Edit) []Activation {
	argvSet := snap.Agent != nil && len(snap.Agent.Agent.Argv) > 0
	removesArgv := slices.ContainsFunc(edits, func(e Edit) bool { return e.Field == AgentArgv && e.Reset })

	var out []Activation

	for _, e := range edits {
		a := Activation{Field: e.Field}

		switch group {
		case GroupInference:
			a.When = whenDistillation
			a.Notes = []string{
				"Claude and Codex integration files do not change.",
				"An open distillation preview must be prepared again.",
				"Saving runs no agent and checks no login.",
			}

			if e.Field == AgentCLI && argvSet && !removesArgv {
				a.Notes = append(a.Notes, "agent.argv stays in effect, so the command that runs does not change.")
			}

			if e.Field == AgentArgv {
				a.Notes = append(a.Notes, "The custom command goes away; the agent.cli preset takes effect.")
			}
		case GroupPinWrite:
			a.When = whenPreview
			a.Notes = []string{
				"Saving applies no proposal; a new proposal preview is necessary.",
				"The setting also governs seamark lessons --apply, --prune, and --retarget.",
			}
		case GroupEvidence:
			a.When = whenEvidence
			a.Notes = append([]string{"The current corpus does not change until an evidence refresh succeeds."}, windowNotes()...)
		case GroupIndexing:
			a.When = whenIndexBuild
			a.Notes = []string{"Nothing is scanned or removed until the build."}
		}

		out = append(out, a)
	}

	return out
}
