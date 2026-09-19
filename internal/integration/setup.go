package integration

// The setup coordinator. It turns one SetupRequest into one complete
// SetupPlan before anything is written, then applies the plan with
// stale-input checks. The native merge semantics (JSON, TOML) stay in
// the client adapters; this file owns only what every client shares:
// selection, input guards, one write per document, skill destinations,
// ordered application, and accurate results.
//
// The protection is bounded. A guard detects a file that changed
// between plan and apply. It is not a cross-file transaction, and it
// does not defend against a hostile process that replaces files while
// apply runs.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/skills"
)

// ErrStalePlan reports an input that changed after it was planned.
// Apply stops and the caller must plan again: a silent re-plan could
// apply an intent the user never saw in the preview.
var ErrStalePlan = errors.New("setup plan is stale")

// ErrPlanConflict reports two planners that want different content for
// one document. The coordinator never lets the last writer win.
var ErrPlanConflict = errors.New("setup plan conflict")

// defaultFileMode is the permission of a new configuration file.
const defaultFileMode fs.FileMode = 0o644

// isCleanRel reports whether rel is a clean, slash-separated path that
// stays inside the repository. A clean relative path leaves the
// repository only as ".." or with a "../" prefix, so these checks cover
// every escape.
func isCleanRel(rel string) bool {
	return rel != "" && path.Clean(rel) == rel && !path.IsAbs(rel) &&
		rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

// ReadGuarded reads one input document and records its observed state.
// Adapters plan from the returned bytes, so a plan and its guard always
// describe the same file state. A missing file is not an error: the
// guard records the absence, and a later creation makes the plan stale.
//
// It rejects a symbolic link at any path component and a path that is
// not a regular file. A link committed in a cloned repository must
// never redirect a read or a write outside the tree.
func ReadGuarded(root, rel string) (FileGuard, []byte, error) {
	if !isCleanRel(rel) {
		return FileGuard{}, nil, fmt.Errorf("%q is not a clean repository-relative path", rel)
	}

	link, err := skills.SymlinkIn(root, rel)
	if err != nil {
		return FileGuard{}, nil, err
	}

	if link != "" {
		return FileGuard{}, nil, fmt.Errorf("%s: symlink at %s; seamark writes only real paths inside the repository", rel, link)
	}

	abs := filepath.Join(root, filepath.FromSlash(rel))

	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return FileGuard{Path: rel}, nil, nil
	}

	if err != nil {
		return FileGuard{}, nil, fmt.Errorf("%s: %w", rel, err)
	}

	if !info.Mode().IsRegular() {
		return FileGuard{}, nil, fmt.Errorf("%s: not a regular file", rel)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return FileGuard{}, nil, fmt.Errorf("%s: %w", rel, err)
	}

	return FileGuard{Path: rel, Exists: true, SHA256: sha256.Sum256(data), Mode: info.Mode().Perm()}, data, nil
}

// verifyGuard observes the guarded file again and compares. A changed
// file, a new link, and a changed file type all make the plan stale.
func verifyGuard(root string, g FileGuard) error {
	now, _, err := ReadGuarded(root, g.Path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStalePlan, err)
	}

	if now != g {
		return fmt.Errorf("%w: %s changed since it was planned; run the command again", ErrStalePlan, g.Path)
	}

	return nil
}

// PlanSetup builds the complete plan for one request and writes
// nothing. Every selected client and every common document is planned
// and validated here, so a malformed selected file stops the run before
// the first write. A client that is not in the request is never read: a
// broken configuration of an unselected client cannot block the run.
//
// An intent the client does not support is removed and reported as an
// informational finding. A missing optional capability is not a broken
// installation.
func PlanSetup(reg *Registry, req SetupRequest) (*SetupPlan, error) {
	if reg == nil {
		return nil, errors.New("setup: no client registry")
	}

	if req.Root == "" {
		return nil, errors.New("setup: workspace root is empty")
	}

	intents, err := resolveIntents(reg, req.Clients)
	if err != nil {
		return nil, err
	}

	b := &planBuilder{plan: &SetupPlan{Root: req.Root}}

	for _, doc := range req.Common {
		if err := b.addCommon(doc); err != nil {
			return nil, err
		}
	}

	var skillClients []Client

	for _, intent := range intents {
		// resolveIntents already proved the ID is registered.
		client, _ := reg.Lookup(intent.ClientID)

		effective, findings := supportedIntent(client, intent)
		b.plan.Findings = append(b.plan.Findings, findings...)

		if effective.Skills {
			skillClients = append(skillClients, client)
		}

		if !effective.Hooks && !effective.RegisterMCP && !effective.ApproveTools {
			continue
		}

		clientPlan, err := client.Setup.Plan(req.Root, req.Binary, effective)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", client.ID, err)
		}

		if err := b.addClient(client.ID, clientPlan); err != nil {
			return nil, err
		}
	}

	b.plan.Destinations = SkillDestinations(skillClients)

	if b.plan.Skills, err = skills.Plan(req.Root, skillTargets(b.plan.Destinations)); err != nil {
		return nil, err
	}

	for _, entry := range b.plan.Skills {
		guard, err := guardSkill(req.Root, entry)
		if err != nil {
			return nil, err
		}

		b.plan.SkillGuards = append(b.plan.SkillGuards, guard)
	}

	return b.plan, nil
}

// resolveIntents validates the requested clients and returns their
// intents in registry order. An unknown ID fails before any planning.
// The same client twice is accepted when both entries agree; two
// different intents for one client have no defined meaning, so they
// fail.
func resolveIntents(reg *Registry, requested []ClientSetup) ([]ClientSetup, error) {
	byID := map[string]ClientSetup{}
	ids := make([]string, 0, len(requested))

	for _, intent := range requested {
		if m := intent.GateMode; m != "" && m != hooks.ModeWarn && m != hooks.ModeEnforce {
			return nil, fmt.Errorf("setup: client %q: gate mode must be %s or %s, got %q",
				intent.ClientID, hooks.ModeWarn, hooks.ModeEnforce, m)
		}

		if seen, dup := byID[intent.ClientID]; dup && seen != intent {
			return nil, fmt.Errorf("setup: client %q is requested twice with different intent", intent.ClientID)
		}

		byID[intent.ClientID] = intent
		ids = append(ids, intent.ClientID)
	}

	selected, err := reg.Select(ids)
	if err != nil {
		return nil, err
	}

	out := make([]ClientSetup, 0, len(selected))

	for _, c := range selected {
		out = append(out, byID[c.ID])
	}

	return out, nil
}

// supportedIntent removes the operations the descriptor does not
// declare and names each one in a finding.
func supportedIntent(c Client, intent ClientSetup) (ClientSetup, []Finding) {
	var findings []Finding

	skip := func(requested *bool, supported bool, what string) {
		if *requested && !supported {
			*requested = false

			findings = append(findings, Finding{
				Level:  FindingInfo,
				Reason: fmt.Sprintf("%s: %s not supported by this integration; skipped", c.Name, what),
			})
		}
	}

	skip(&intent.Skills, c.Supports(CapabilitySkills), "skills are")
	skip(&intent.Hooks, c.SetupOps.Hooks, "lifecycle hooks are")
	skip(&intent.RegisterMCP, c.SetupOps.RegisterMCP, "MCP registration is")
	skip(&intent.ApproveTools, c.SetupOps.ApproveTools, "tool grants are")

	// The gate mode belongs to the hooks. Without them it has no meaning,
	// and an adapter must not see a mode it cannot install.
	if !intent.Hooks {
		intent.GateMode = ""
	}

	return intent, findings
}

// skillTargets converts destinations into installer targets. The
// client label is for narration only, so it names every consumer.
func skillTargets(dests []SkillDestination) []skills.Target {
	targets := make([]skills.Target, 0, len(dests))

	for _, d := range dests {
		targets = append(targets, skills.Target{Client: strings.Join(d.Consumers, "+"), Dir: d.Dir})
	}

	return targets
}

// planBuilder merges client plans and common documents into one plan
// with one guard, and at most one write or keep, per path.
type planBuilder struct {
	plan *SetupPlan
}

// addCommon plans one client-independent document.
func (b *planBuilder) addCommon(doc Document) error {
	if doc.Compose == nil {
		return fmt.Errorf("setup: common document %q has no compose function", doc.Path)
	}

	guard, existing, err := ReadGuarded(b.plan.Root, doc.Path)
	if err != nil {
		return err
	}

	after, err := doc.Compose(existing, guard.Exists)
	if err != nil {
		return fmt.Errorf("%s: %w", doc.Path, err)
	}

	part := ClientPlan{Reads: []FileGuard{guard}}

	if guard.Exists && bytes.Equal(after, existing) {
		part.Kept = []FileKeep{{Path: doc.Path, Detail: doc.KeptDetail}}
	} else {
		part.Writes = []FileWrite{{Path: doc.Path, After: after, Detail: doc.Detail}}
	}

	return b.addClient("", part)
}

// addClient merges one client's plan. consumer is the client ID, or
// empty for a common document.
func (b *planBuilder) addClient(consumer string, part ClientPlan) error {
	for _, guard := range part.Reads {
		if err := b.addGuard(guard); err != nil {
			return err
		}
	}

	for _, write := range part.Writes {
		if err := b.addWrite(consumer, write); err != nil {
			return err
		}
	}

	for _, keep := range part.Kept {
		if err := b.addKeep(consumer, keep); err != nil {
			return err
		}
	}

	b.plan.Findings = append(b.plan.Findings, part.Findings...)

	return nil
}

// addGuard records one observed input. Two planners that read the same
// file must have seen the same state; otherwise the file changed while
// the plan was made, and no single plan describes it.
func (b *planBuilder) addGuard(guard FileGuard) error {
	if !isCleanRel(guard.Path) {
		return fmt.Errorf("setup: guard path %q is not a clean repository-relative path", guard.Path)
	}

	if i := b.guardIndex(guard.Path); i >= 0 {
		if b.plan.Reads[i] != guard {
			return fmt.Errorf("%w: %s changed while the plan was made; run the command again", ErrStalePlan, guard.Path)
		}

		return nil
	}

	b.plan.Reads = append(b.plan.Reads, guard)

	return nil
}

// addWrite records one composed document. A write without a guard
// cannot be checked again before apply, so it is a broken adapter, not
// a runtime state.
func (b *planBuilder) addWrite(consumer string, write FileWrite) error {
	if b.guardIndex(write.Path) < 0 {
		return fmt.Errorf("setup: write to %q has no read guard", write.Path)
	}

	write.Consumers = withConsumer(write.Consumers, consumer)

	if i := slices.IndexFunc(b.plan.Writes, func(w FileWrite) bool { return w.Path == write.Path }); i >= 0 {
		prior := &b.plan.Writes[i]

		if !bytes.Equal(prior.After, write.After) {
			return fmt.Errorf("%w: %s and %s plan different content for %s", ErrPlanConflict,
				strings.Join(prior.Consumers, "+"), strings.Join(write.Consumers, "+"), write.Path)
		}

		for _, c := range write.Consumers {
			prior.Consumers = withConsumer(prior.Consumers, c)
		}

		return nil
	}

	// A write replaces an earlier keep of the same document: the writer
	// composed its bytes from the same guarded content the keeper read.
	b.plan.Kept = slices.DeleteFunc(b.plan.Kept, func(k FileKeep) bool { return k.Path == write.Path })
	b.plan.Writes = append(b.plan.Writes, write)

	return nil
}

// addKeep records one unchanged document, unless another planner
// already writes it.
func (b *planBuilder) addKeep(consumer string, keep FileKeep) error {
	if b.guardIndex(keep.Path) < 0 {
		return fmt.Errorf("setup: kept document %q has no read guard", keep.Path)
	}

	if slices.ContainsFunc(b.plan.Writes, func(w FileWrite) bool { return w.Path == keep.Path }) {
		return nil
	}

	keep.Consumers = withConsumer(keep.Consumers, consumer)

	if i := slices.IndexFunc(b.plan.Kept, func(k FileKeep) bool { return k.Path == keep.Path }); i >= 0 {
		for _, c := range keep.Consumers {
			b.plan.Kept[i].Consumers = withConsumer(b.plan.Kept[i].Consumers, c)
		}

		return nil
	}

	b.plan.Kept = append(b.plan.Kept, keep)

	return nil
}

func (b *planBuilder) guardIndex(rel string) int {
	return slices.IndexFunc(b.plan.Reads, func(g FileGuard) bool { return g.Path == rel })
}

// withConsumer appends a consumer once. An empty consumer is a common
// document, which belongs to no client.
func withConsumer(consumers []string, consumer string) []string {
	if consumer == "" || slices.Contains(consumers, consumer) {
		return consumers
	}

	return append(consumers, consumer)
}

// OpStatus is the outcome of one document operation.
type OpStatus int

// The outcomes ApplySetup reports.
const (
	// OpKept means the document needs no change.
	OpKept OpStatus = iota
	// OpPlanned means a preview: the document would be written.
	OpPlanned
	// OpApplied means the document was written.
	OpApplied
	// OpFailed means the write failed; Err holds the cause.
	OpFailed
	// OpNotAttempted means an earlier failure stopped the run first.
	OpNotAttempted
)

// String names a status for narration and test output.
func (s OpStatus) String() string {
	switch s {
	case OpKept:
		return "kept"
	case OpPlanned:
		return "planned"
	case OpApplied:
		return "applied"
	case OpFailed:
		return "failed"
	case OpNotAttempted:
		return "not attempted"
	default:
		return "unknown"
	}
}

// OpKind names what one operation acts on.
type OpKind int

// The operation kinds.
const (
	// OpDocument is one configuration document or common document.
	OpDocument OpKind = iota
	// OpSkill is one skill directory.
	OpSkill
)

// OpResult is the outcome for one document or one skill directory.
type OpResult struct {
	Kind OpKind
	// Path is the document, or the skill directory, repository-relative.
	Path   string
	Status OpStatus
	// Created is true when the path did not exist before the run.
	Created   bool
	Detail    string
	Consumers []string
	// Err is the cause of OpFailed; nil otherwise.
	Err error
}

// SetupResult reports every operation of one run, so a partial failure
// says exactly what landed and what did not. Ops holds the documents in
// document order. Skills holds the skill directories in plan order; they
// are applied after the documents and report through the same type.
type SetupResult struct {
	Ops    []OpResult
	Skills []OpResult
}

// ApplyOptions controls one ApplySetup run.
type ApplyOptions struct {
	// Preview reports every write as planned and writes nothing.
	Preview bool
	// Observe receives each result in order, as soon as it is known:
	// every document, then every skill directory. The caller narrates
	// from it, so its lines keep the order of the run. Nil is allowed.
	Observe func(OpResult)
	// SkillsLog receives the skill installer's own line for each skill
	// directory, in init's words. Nil discards it. A caller that prints
	// these lines skips the OpSkill results in Observe.
	SkillsLog io.Writer
}

// ApplySetup executes a plan. Before the first write it checks every
// guard, the skill directories included; a difference returns
// ErrStalePlan with nothing written. It checks the inputs once more
// right before each write.
//
// A failed write stops the run. The result then names the failed
// operation and marks every later write as not attempted. Nothing is
// rolled back: an earlier write stays, and a repeated run converges,
// because the next plan starts from the files as they are.
func ApplySetup(plan *SetupPlan, opts ApplyOptions) (SetupResult, error) {
	result := SetupResult{Ops: plannedOps(plan), Skills: plannedSkillOps(plan)}

	report := func(op OpResult) {
		if opts.Observe != nil {
			opts.Observe(op)
		}
	}

	stop := func(err error) (SetupResult, error) {
		markNotAttempted(result.Ops)
		markNotAttempted(result.Skills)

		return result, err
	}

	if !opts.Preview {
		if err := verifyPlan(plan); err != nil {
			return stop(err)
		}
	}

	// written holds the documents this run already replaced. Their guards
	// describe the old content, so later boundary checks skip them.
	written := map[string]bool{}

	for i := range result.Ops {
		op := &result.Ops[i]

		if op.Status == OpPlanned && !opts.Preview {
			if err := applyWrite(plan, op.Path, written); err != nil {
				op.Status, op.Err = OpFailed, err
				report(*op)

				return stop(fmt.Errorf("%s: %w", op.Path, err))
			}

			written[op.Path] = true
			op.Status = OpApplied
		}

		report(*op)
	}

	log := opts.SkillsLog
	if log == nil {
		log = io.Discard
	}

	for i := range result.Skills {
		op := &result.Skills[i]

		if err := applySkill(log, plan, i, opts.Preview); err != nil {
			op.Status, op.Err = OpFailed, err
			report(*op)

			return stop(err)
		}

		if op.Status == OpPlanned && !opts.Preview {
			op.Status = OpApplied
		}

		report(*op)
	}

	return result, nil
}

// plannedOps lists the kept and written documents in document order,
// which is the order of their guards. Every write starts as planned.
func plannedOps(plan *SetupPlan) []OpResult {
	var ops []OpResult

	for _, guard := range plan.Reads {
		for _, k := range plan.Kept {
			if k.Path == guard.Path {
				ops = append(ops, OpResult{Path: k.Path, Status: OpKept, Detail: k.Detail, Consumers: k.Consumers})
			}
		}

		for _, w := range plan.Writes {
			if w.Path == guard.Path {
				ops = append(ops, OpResult{
					Path: w.Path, Status: OpPlanned, Created: !guard.Exists,
					Detail: w.Detail, Consumers: w.Consumers,
				})
			}
		}
	}

	return ops
}

// plannedSkillOps lists the skill directories in plan order. A current
// or foreign directory is kept; a foreign one is never touched, because
// it is the user's directory, not seamark's.
func plannedSkillOps(plan *SetupPlan) []OpResult {
	ops := make([]OpResult, 0, len(plan.Skills))

	for _, entry := range plan.Skills {
		op := OpResult{Kind: OpSkill, Path: entry.Rel, Status: OpKept, Created: entry.State == skills.Absent}

		for _, dest := range plan.Destinations {
			if dest.Dir == path.Dir(entry.Rel) {
				op.Consumers = dest.Consumers
			}
		}

		switch entry.State {
		case skills.Current:
			op.Detail = "current"
		case skills.Foreign:
			op.Detail = "not managed by seamark: " + entry.Reason
		case skills.Stale:
			op.Status, op.Detail = OpPlanned, "refreshed managed copy"
		case skills.Absent:
			op.Status = OpPlanned
		}

		ops = append(ops, op)
	}

	return ops
}

// markNotAttempted marks every write that is still only planned. A kept
// path stays kept: nothing was going to happen to it.
func markNotAttempted(ops []OpResult) {
	for i := range ops {
		if ops[i].Status == OpPlanned {
			ops[i].Status = OpNotAttempted
		}
	}
}

// verifyPlan checks every input of the plan against the tree.
func verifyPlan(plan *SetupPlan) error {
	for _, guard := range plan.Reads {
		if err := verifyGuard(plan.Root, guard); err != nil {
			return err
		}
	}

	for i := range plan.Skills {
		if err := verifySkill(plan, i); err != nil {
			return err
		}
	}

	return nil
}

// guardSkill records the guard for one plan entry. Only an entry that
// setup writes gets a content digest. A current or foreign directory is
// kept whatever it holds, so its classification is its whole guard, and
// setup never reads further into a directory that is not its own.
func guardSkill(root string, entry skills.Entry) (SkillGuard, error) {
	guard := SkillGuard{Rel: entry.Rel}

	if !entry.Writes() {
		return guard, nil
	}

	digest, err := skills.Snapshot(root, entry)
	if err != nil {
		return SkillGuard{}, err
	}

	guard.Digest = digest

	return guard, nil
}

// verifySkill checks one skill directory against its plan entry and its
// guard. The classification covers ownership and links: a directory
// that became foreign, or a path that became a link, classifies
// differently. The digest covers content: a managed copy that the user
// edited after the plan is still stale, and only its bytes show the edit.
func verifySkill(plan *SetupPlan, i int) error {
	entry := plan.Skills[i]
	stale := fmt.Errorf("%w: %s changed since it was planned; run the command again", ErrStalePlan, entry.Rel)

	now, err := skills.Check(plan.Root, entry)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStalePlan, err)
	}

	if now != entry {
		return stale
	}

	guard, err := guardSkill(plan.Root, entry)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStalePlan, err)
	}

	if guard != plan.SkillGuards[i] {
		return stale
	}

	return nil
}

// applySkill applies one skill entry after a last check of its guard,
// the same write boundary rule the documents follow. The installer
// keeps its own ownership logic and narrates its own line.
func applySkill(log io.Writer, plan *SetupPlan, i int, preview bool) error {
	entry := plan.Skills[i]

	if entry.Writes() && !preview {
		if err := verifySkill(plan, i); err != nil {
			return err
		}
	}

	return skills.ApplyEntry(log, plan.Root, entry, preview)
}

// applyWrite writes one planned document after a last check of every
// input that is still unwritten. The check covers the read-only inputs
// too: a registration name that changed makes the planned rules wrong,
// even when the document itself did not change.
func applyWrite(plan *SetupPlan, rel string, written map[string]bool) error {
	for _, guard := range plan.Reads {
		if written[guard.Path] {
			continue
		}

		if err := verifyGuard(plan.Root, guard); err != nil {
			return err
		}
	}

	guard := plan.Reads[slices.IndexFunc(plan.Reads, func(g FileGuard) bool { return g.Path == rel })]
	write := plan.Writes[slices.IndexFunc(plan.Writes, func(w FileWrite) bool { return w.Path == rel })]
	target := filepath.Join(plan.Root, filepath.FromSlash(rel))

	if !guard.Exists {
		mode := write.Mode
		if mode == 0 {
			mode = defaultFileMode
		}

		return replaceFile(target, write.After, mode, false)
	}

	// The rename would replace a file the user made read-only. A plain
	// write fails on such a file, and setup must respect that choice too.
	if guard.Mode&0o200 == 0 {
		return fmt.Errorf("the file is read-only (%s); seamark does not replace it", guard.Mode)
	}

	return replaceFile(target, write.After, guard.Mode, true)
}

// replaceFile writes data to a temporary file beside the target and
// renames it into place. A failure in the middle of the write then
// leaves the user's configuration complete, never truncated. The rename
// replaces the directory entry, so it cannot write through a link at
// the final path component.
//
// exactMode selects how mode applies. A replaced file keeps its exact
// permission. A new file is created through the process umask, the same
// as a plain write, so a private umask yields a private file.
func replaceFile(target string, data []byte, mode fs.FileMode, exactMode bool) error {
	dir := filepath.Dir(target)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := createTemp(dir, mode)
	if err != nil {
		return err
	}

	if err := fillAndClose(tmp, data, mode, exactMode); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	return nil
}

// createTemp creates a new file with a random name in dir. os.CreateTemp
// always uses 0600, so it cannot give a new file the umask-filtered
// permission that a plain write gives. O_EXCL makes the create fail on
// an existing name, a symbolic link included.
func createTemp(dir string, mode fs.FileMode) (*os.File, error) {
	var suffix [8]byte

	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}

	name := filepath.Join(dir, ".seamark-"+hex.EncodeToString(suffix[:])+".tmp")

	return os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
}

// fillAndClose writes the content, sets the exact permission when
// asked, and closes the file. It closes the file on every path, so the
// caller only removes it.
func fillAndClose(f *os.File, data []byte, mode fs.FileMode, exactMode bool) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		return err
	}

	if exactMode {
		if err := f.Chmod(mode); err != nil {
			_ = f.Close()

			return err
		}
	}

	return f.Close()
}
