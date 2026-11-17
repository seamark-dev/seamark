package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the two pre-edit hooks of the lesson checks: lessons
// --guard --hook and lessons --baseline --hook (check-only). Both decode
// one native pre-edit event, resolve its paths at the git top level, load
// the check set strictly, and project the edit. The guard then decides
// and can deny the edit. The baseline handler never denies. Both store
// the pending baseline for the post-edit handler and append one record
// to the lesson-check audit stream.
//
// Neither handler is a part of lessons --hook. That hook stays advisory
// and fail-open, and it never runs a check.
// Neither handler reads the recording switch, because a lesson-check
// audit record does not depend on it.

// preEditConflicts lists the lessons modes and decision flags that
// --guard and --baseline refuse, in the order of the help text. The
// first flag that the call sets names the conflict.
// --hook is not here, because both modes need it. --json is not here,
// because verifyFlagError refuses it first. Each modifier flag, for
// example --client, keeps its own guard and is not here.
//
// The guards of --prepare, --verify, --set-mode, and --explore run
// first. Their lists do not name --guard or --baseline, so a call such
// as `--verify --guard` gets the text of this guard. Each order refuses
// the call before any handler runs.
var preEditConflicts = []string{
	"file", "list", "region", "stats", "distill", "proposals", "verify", "prepare", "set-mode",
	"apply", "edit", "dismiss", "prune", "retarget", "extract-triggers", "hook-reset", "explore",
}

// preEditFlagError checks the flags of a --guard or --baseline call
// before any dispatch. Each mode combines with no other mode, also not
// with the other pre-edit mode, and needs --hook. The mode conflict
// comes first, as for --explore, so a call that also lacks --hook names
// the flag that it must drop.
func preEditFlagError(cmd *cobra.Command, guard, baseline, hookMode bool) error {
	modes := []struct {
		name, other string
		set         bool
	}{
		{name: "guard", other: "baseline", set: guard},
		{name: "baseline", other: "guard", set: baseline},
	}

	for _, mode := range modes {
		if !mode.set {
			continue
		}

		for _, name := range append(slices.Clone(preEditConflicts), mode.other) {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s does not combine with --%s", mode.name, name)
			}
		}

		// The mode names the event that --hook reads. Without --hook
		// nothing reads the event, so the call is a usage error.
		if !hookMode {
			return fmt.Errorf("--%s needs --hook: it reads one native pre-edit event from stdin", mode.name)
		}
	}

	return nil
}

// hookLimits returns the limits of one hook run. It is DefaultLimits.
// Only tests replace it, to run a handler with a small budget without
// wall-clock thresholds. No production code assigns it.
var hookLimits = lessoncheck.DefaultLimits

// beforeGuardDecision runs right before the guard decides, when it is
// not nil. Only tests set it: it is the one point where a test can make
// the handler panic and check its panic recovery.
var beforeGuardDecision func()

// The audit errors of the pre-edit runs that the libraries do not name.
var (
	// errHandlerPanic is the error of a run that panicked. The record
	// keeps no panic value, because a value can quote the request.
	errHandlerPanic = errors.New("lesson checks: the pre-edit handler stopped on an internal error")
	// errBudgetCut is the error of a projection that the guard budget
	// ended. The post-edit handler then finds no baseline.
	errBudgetCut = errors.New("lesson checks: the guard budget ended the projection, so no pending baseline exists")
	// errNoMatchID is the error of an event without a tool-use ID. A
	// pending record needs the match digest as its key.
	errNoMatchID = errors.New("lesson checks: the event names no tool-use ID, so no pending baseline exists")
)

// maxAuditToolRunes bounds the tool name in a record. The name comes
// from the payload, and a tool outside the covered list can have a name
// of any length.
const maxAuditToolRunes = 64

// preEdit holds the state of one pre-edit hook run. The fields fill in
// step order, so the record of a failed run names what the run read.
type preEdit struct {
	cmd *cobra.Command
	// root is the git top level. lessons.yaml, the snapshot, the path
	// normalization, .seamark/checks, and the audit stream use it.
	// rootErr is set when no git work tree holds the -C directory, and
	// root is "" then.
	root    string
	rootErr error
	// idRoot is the identity root. Every session, match, and context
	// digest of the run hashes over it, so the records of one tool call
	// join the firing log on equal bytes.
	idRoot string
	// lim is DefaultLimits. A partly filled Limits value would set
	// pending-max-age to 0, and the sweep would expire every record.
	lim    lessoncheck.Limits
	client integration.Client
	// op is the event identity until the paths are normalized, and the
	// normalized operation after that. So no record names a raw path.
	op   lessoncheck.Operation
	cfg  *lessoncheck.Config
	snap *lessoncheck.Snapshot
	// recorded is true once the run tried to append its record. The
	// panic recovery then does not try again.
	recorded bool
}

// newPreEdit resolves the two roots of a pre-edit run.
func newPreEdit(cmd *cobra.Command, opts *options) *preEdit {
	root, err := verifyRoot(opts.workspace)

	return &preEdit{cmd: cmd, root: root, rootErr: err, idRoot: hookIdentityRoot(opts), lim: hookLimits()}
}

// preEditClient returns the client whose native pre-edit events a run
// translates. The ID comes from the installed hook command, never from
// the payload, as in gateHookClient. A command without --client keeps
// Claude Code semantics, because every installed Claude Code hook runs
// exactly that command.
//
// An unknown ID, or a client without a guard decoder, is a broken hook
// command. The guard denies then, as the gate does, and the baseline
// handler records the error and exits 0.
func preEditClient(clientID string) (integration.Client, error) {
	if clientID == "" {
		clientID = integration.ClaudeID
	}

	registry := integration.Builtin()

	client, ok := registry.Lookup(clientID)
	if !ok {
		return integration.Client{}, fmt.Errorf("unknown hook client %q (known: %s)",
			render.Sanitize(clientID), strings.Join(registry.IDs(), ", "))
	}

	if err := client.Require(integration.CapabilityLessonGuard); err != nil {
		return integration.Client{}, err
	}

	return client, nil
}

// runLessonsGuard implements lessons --guard --hook. It fails closed when
// enforcement is armed or cannot be established. A valid configuration
// without enforce pins never denies the edit. An allow writes nothing on
// stdout and exits 0, because the guard never forces an allow. A deny
// returns gate.Blocked, so Execute prints "seamark: blocked by policy:
// <reasons>" on stderr and exits 2.
//
// The run appends its guard record after it composes the reply. A
// failed pending write, audit append, or sweep never changes the reply,
// and the hook says nothing about it.
func runLessonsGuard(cmd *cobra.Command, opts *options, clientID string) (err error) {
	run := newPreEdit(cmd, opts)

	// A panic in any step denies with cause internal-error. DecideGuard
	// recovers its own panic, but the decode, the projection, and the
	// writers run outside it.
	defer func() {
		if recover() != nil {
			err = run.panicDeny()
		}

		run.sweep()
	}()

	client, err := preEditClient(clientID)
	if err != nil {
		// No adapter of the client can encode the deny. The run blocks in
		// the form that the gate gives a broken hook command.
		decision, _ := lessoncheck.DenyInternal("")
		decision.ReplyWritten = true
		decision.NativeBlocking = lessoncheck.GuardNativeUnverified
		run.appendGuard(decision, err)

		return gate.FailClosed(err, true)
	}

	run.client = client

	decision, texts, failure := run.decideGuard()
	decision.NativeBlocking = nativeBlocking(run.op)

	if decision.Decision == lessoncheck.GuardAllow {
		// The tool runs as soon as the hook exits, so the baseline goes
		// to disk before the exit.
		failure = errors.Join(failure, run.writePending(lessoncheck.ModeEnforce))
		run.appendGuard(decision, failure)

		return nil
	}

	written, blocked := run.denyReply(texts)
	decision.ReplyWritten = written
	run.appendGuard(decision, failure)

	return blocked
}

// decideGuard runs the guard up to its decision: it reads the event,
// loads the check set, projects the edit, and decides, and it covers the
// fail-closed cases. It returns the decision, the deny texts for stderr,
// and the failure that the audit record names.
func (r *preEdit) decideGuard() (decision lessoncheck.GuardDecision, texts []string, failure error) {
	op, err := r.readOperation()
	r.op = identityOf(op)

	switch {
	case errors.Is(err, integration.ErrNotApplicable):
		// A tool outside the covered list has no projection. The guard
		// allows it, and the record names the cause.
		return lessoncheck.AllowUncovered(), nil, nil
	case errors.Is(err, integration.ErrUnsupportedGrammar):
		// The decoder cannot name every path. The operation holds the
		// identity only, with paths_complete false, so every enforce pin
		// gets incomplete-paths and an armed guard denies.
		failure = err
	case err != nil:
		return r.unreadableDecision(op, err)
	}

	if r.rootErr != nil {
		// Without a work tree no check set and no snapshot exist. The
		// guard cannot decide, so it denies.
		decision, texts = lessoncheck.DenyInternal(r.op.Tool)

		return decision, texts, r.rootErr
	}

	r.op = normalizeOperation(r.root, op)

	// One deadline bounds the load, the projection, and the evaluation.
	// ProjectOperation applies the guard budget again from its own start,
	// and the earlier deadline wins.
	ctx, cancel := context.WithTimeout(r.cmd.Context(), r.lim.GuardBudget)
	defer cancel()

	// Load is strict: a tier-1 or tier-2 error denies, and no fallback
	// to the defaults exists.
	cfg, _, loadErr := lessoncheck.Load(r.root)
	r.cfg = cfg

	var projectErr error
	if loadErr == nil && len(cfg.Errors) == 0 {
		r.snap, projectErr = lessoncheck.ProjectOperation(ctx, r.root, r.op, r.lim)
	}

	if beforeGuardDecision != nil {
		beforeGuardDecision()
	}

	decision, texts = lessoncheck.DecideGuard(lessoncheck.GuardInput{
		Config: cfg, LoadErr: loadErr, Operation: r.op, Snapshot: r.snap, ProjectErr: projectErr,
		Context: ctx,
	})

	return decision, texts, errors.Join(failure, loadErr, projectErr)
}

// unreadableDecision establishes arming from the selected workspace only.
// A valid check-only configuration never denies an unreadable request.
// An unknown root or configuration cannot prove that enforcement is absent.
func (r *preEdit) unreadableDecision(op lessoncheck.Operation, readErr error) (lessoncheck.GuardDecision, []string, error) {
	if r.rootErr != nil {
		decision, texts := lessoncheck.DenyInternal(r.op.Tool)
		return decision, texts, errors.Join(readErr, r.rootErr)
	}

	cfg, _, loadErr := lessoncheck.Load(r.root)
	r.cfg = cfg
	decision, texts := lessoncheck.DecideGuard(lessoncheck.GuardInput{Config: cfg, LoadErr: loadErr, Operation: r.op})
	if decision.Decision == lessoncheck.GuardAllow || loadErr != nil || cfg == nil || len(cfg.Errors) > 0 {
		return decision, texts, errors.Join(readErr, loadErr)
	}

	decision, texts = lessoncheck.DenyUnparseable(op.Tool, readErr)
	return decision, texts, readErr
}

// runLessonsBaseline implements lessons --baseline --hook (check-only).
// It stores the pending baseline for the post-edit handler. It never
// denies: every run exits 0 and writes nothing on stdout. A run that
// stores no baseline names the reason in its baseline record.
func runLessonsBaseline(cmd *cobra.Command, opts *options, clientID string) error {
	run := newPreEdit(cmd, opts)

	// A panic gives exit 0 too: a check-only hook never stops the edit.
	defer func() {
		if recover() != nil && !run.recorded {
			run.appendBaseline(errHandlerPanic)
		}

		run.sweep()
	}()

	run.appendBaseline(run.baseline(clientID))

	return nil
}

// baseline runs the steps of the baseline handler and returns the
// reason when it stores no baseline. It returns nil also when no
// executable pin scopes the edit, because then no baseline is needed.
func (r *preEdit) baseline(clientID string) error {
	client, err := preEditClient(clientID)
	if err != nil {
		return err
	}

	r.client = client

	op, err := r.readOperation()
	r.op = identityOf(op)

	if err != nil {
		return err
	}

	if r.rootErr != nil {
		return r.rootErr
	}

	r.op = normalizeOperation(r.root, op)

	ctx, cancel := context.WithTimeout(r.cmd.Context(), r.lim.GuardBudget)
	defer cancel()

	cfg, errs, err := lessoncheck.Load(r.root)
	if err != nil {
		return err
	}

	r.cfg = cfg

	// An invalid check set gives configuration-error after the edit
	// anyway, so a baseline of it has no use.
	if len(errs) > 0 {
		return errs[0]
	}

	r.snap, err = lessoncheck.ProjectOperation(ctx, r.root, r.op, r.lim)
	if err != nil {
		return err
	}

	return r.writePending(lessoncheck.ModeCheck)
}

// readOperation reads the payload under its size cap and decodes the
// operation. On an error the operation holds the event identity only,
// as far as the payload names it (integration.GuardHooks).
func (r *preEdit) readOperation() (lessoncheck.Operation, error) {
	payload, err := integration.ReadHookPayload(r.cmd.InOrStdin())
	if err != nil {
		return lessoncheck.Operation{Client: r.client.ID}, err
	}

	return r.client.Guards.DecodeGuard(payload)
}

// identityOf returns the event identity of op without its file
// operations. A record of a run that stops before the normalization
// then names no raw client path.
func identityOf(op lessoncheck.Operation) lessoncheck.Operation {
	op.Ops, op.Dropped, op.PathsComplete = nil, nil, false

	return op
}

// normalizeOperation resolves the native paths of op at root with
// delivery.NormalizePaths. Delete and move sources keep their final entry.
// It resolves one path at a time, so each file operation keeps its own
// paths. A file operation stays in the result only when every path of it
// resolves inside root:
//
//   - A path outside root by its text alone is outside every pin scope.
//     An operation with only such paths leaves the evaluation, and
//     Dropped lists its paths. The decision then names outside-root.
//   - Each other rejected path can lie inside the work tree: a link
//     that leads out of root, a loop of links, or a malformed path. The
//     guard cannot name its pin scope, so the operation leaves the
//     evaluation and PathsComplete becomes false. Every enforce pin
//     then applies (fail closed).
//   - A move with one end outside root and one end inside it changes a
//     path in root with content from outside, or removes one. The
//     projection has no exact form for it, so the same rule applies.
func normalizeOperation(root string, op lessoncheck.Operation) lessoncheck.Operation {
	cwd := op.CWD
	if cwd == "" {
		// A client that sends no working directory sends paths relative
		// to the hook process, as for the edit hook.
		cwd, _ = os.Getwd()
	}

	out := op
	out.Ops = make([]lessoncheck.FileOp, 0, len(op.Ops))

	for _, fop := range op.Ops {
		ends := []*string{&fop.Path}
		if fop.MoveTo != "" {
			ends = append(ends, &fop.MoveTo)
		}

		var outside []string

		resolved := 0

		for i, end := range ends {
			normalize := delivery.NormalizePaths
			if i == 0 && (fop.Kind == lessoncheck.OpPatchDelete || fop.MoveTo != "") {
				// Delete and move remove the source entry, even if it is a link.
				normalize = delivery.NormalizeEntryPaths
			}
			n := normalize(root, cwd, []string{*end})

			switch {
			case len(n.Files) == 1:
				*end = n.Files[0]
				resolved++
			case len(n.Rejected) == 1 && n.Rejected[0].Reason == delivery.RejectOutside && outsideByText(root, cwd, *end):
				outside = append(outside, *end)
			}
		}

		out.Dropped = append(out.Dropped, outside...)

		switch {
		case resolved == len(ends):
			out.Ops = append(out.Ops, fop)
		case len(outside) < len(ends):
			out.PathsComplete = false
		}
	}

	return out
}

// outsideByText reports whether a native path lies outside root by its
// text alone, without a link in the path. A relative path starts at the
// real location of cwd, as the operating system resolves it. A client
// can spell root through a link, for example /var for /private/var on
// macOS. So the path is outside only when it is outside root and outside
// the real location of root.
//
// The callers ask only after delivery rejected the path as outside by
// its on-disk location. That location uses the on-disk spelling of
// every name, so a case alias of root never reaches this test as an
// outside path. The text test here only decides between a drop and an
// incomplete path set, and it errs toward the incomplete set.
func outsideByText(root, cwd, native string) bool {
	path := native

	if !filepath.IsAbs(path) {
		base := cwd
		if !filepath.IsAbs(base) {
			base = filepath.Join(root, base)
		}

		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}

		path = filepath.Join(base, path)
	}

	path = filepath.Clean(path)

	forms := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		forms = append(forms, resolved)
	}

	for _, form := range forms {
		rel, err := filepath.Rel(form, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}

	return true
}

// nativeBlocking returns the native_blocking level of a decision. It is
// verified only when the evidence of the pre-write path of this client,
// tool, and context is verified.
//
// The decoders name the receiver and not its lane. The lane comes from
// ReceivingContextOf, the one receiver derivation that the decoders
// use: the root receiver of the session is the root lane, and another
// receiver is a child lane. Without a receiver the run cannot tell the
// lanes apart, so both lanes must be verified.
func nativeBlocking(op lessoncheck.Operation) string {
	lanes := []string{integration.LaneRoot, integration.LaneChild}

	if op.Receiver != "" {
		lanes = []string{integration.LaneChild}

		if root := integration.ReceivingContextOf(op.Client, op.SessionID, ""); root != nil && root.ID == op.Receiver {
			lanes = []string{integration.LaneRoot}
		}
	}

	for _, lane := range lanes {
		if integration.GuardEvidence(op.Client, op.Tool, lessoncheck.PathContext(lane)).Level != integration.VerificationVerified {
			return lessoncheck.GuardNativeUnverified
		}
	}

	return lessoncheck.GuardNativeVerified
}

// denyReply writes the native deny reply and returns the error that
// makes Execute exit 2. hookReply writes the stdout part of the reply,
// which is empty for the exit protocol, and returns gate.Blocked. A
// failed write still blocks, because the guard fails closed. written is
// false then.
func (r *preEdit) denyReply(texts []string) (written bool, blocked error) {
	reply := r.client.Guards.EncodeDeny(texts)

	err := hookReply(r.cmd, reply)
	if errors.Is(err, gate.ErrBlocked) {
		return true, err
	}

	if reason := strings.TrimSpace(string(reply.Stderr)); reason != "" {
		return false, &gate.Blocked{Cause: errors.New(reason)}
	}

	return false, &gate.Blocked{}
}

// panicDeny returns the deny of a guard run that panicked. The run
// appends its record unless it already tried, because a second panic in
// the writer would end the process.
func (r *preEdit) panicDeny() error {
	decision, texts := lessoncheck.DenyInternal(r.op.Tool)
	decision.ReplyWritten = true
	decision.NativeBlocking = nativeBlocking(r.op)

	if !r.recorded {
		r.appendGuard(decision, errHandlerPanic)
	}

	return &gate.Blocked{Cause: errors.New(strings.Join(texts, "; "))}
}

// writePending stores the pending baseline of an allowed call. It keeps
// the paths that an executable pin scopes, so a run without such a path
// writes nothing. installed is the mode of the installed pre-edit hook.
// A run whose guard budget ended the projection returns that reason,
// because the post-edit handler then finds no baseline. The snapshot of
// such a run holds no change.
func (r *preEdit) writePending(installed lessoncheck.Mode) error {
	if r.cfg == nil || r.snap == nil {
		return nil
	}

	if lessoncheck.HasLimit(r.snap.Limits, lessoncheck.LimitGuardBudget) && lessoncheck.HasExecutablePin(r.cfg) {
		return errBudgetCut
	}

	paths, limitations := lessoncheck.PendingPathsOf(r.cfg, r.snap)
	if len(paths) == 0 {
		return nil
	}

	session, match, contextSHA := r.digests()
	if match == "" {
		return errNoMatchID
	}

	return lessoncheck.WritePending(r.cmd.Context(), r.root, &lessoncheck.Pending{
		Client:        r.client.ID,
		Tool:          r.op.Tool,
		InstalledMode: string(installed),
		SessionSHA:    session,
		MatchSHA:      match,
		ContextSHA:    contextSHA,
		ConfigSHA256:  r.cfg.SHA256,
		PathsComplete: r.snap.PathsComplete,
		Paths:         paths,
		Limitations:   limitations,
	}, r.lim)
}

// digests returns the session, match, and context digests of the
// run over the identity root. Each digest needs its raw IDs, as in the
// firing log (reviews.RecordEvent), so the records join on equal bytes.
func (r *preEdit) digests() (session, match, contextSHA string) {
	if r.idRoot == "" {
		return "", "", ""
	}

	if r.op.SessionID != "" {
		session = reviews.SessionDigest(r.idRoot, r.op.SessionID)
	}

	if r.op.MatchID != "" {
		match = reviews.MatchDigest(r.idRoot, r.op.SessionID, r.op.MatchID)
	}

	if r.client.ID != "" && r.op.Receiver != "" {
		contextSHA = reviews.ContextDigest(r.idRoot, r.client.ID, r.op.Receiver)
	}

	return session, match, contextSHA
}

// record returns the audit record of the run with the fields that the
// guard and the baseline records share. It holds digests, codes, and
// repository-relative paths only: no raw ID, no content, and no patch
// text. AppendAudit redacts and bounds the error.
func (r *preEdit) record(kind string, failure error) lessoncheck.AuditRecord {
	session, match, contextSHA := r.digests()

	rec := lessoncheck.AuditRecord{
		Kind:          kind,
		Caller:        lessoncheck.CallerHook,
		Client:        r.client.ID,
		Tool:          render.Truncate(render.Sanitize(r.op.Tool), maxAuditToolRunes),
		SessionSHA:    session,
		MatchSHA:      match,
		ContextSHA:    contextSHA,
		Paths:         operationPaths(r.op),
		PathsComplete: r.op.PathsComplete,
	}

	if r.cfg != nil {
		rec.ConfigSHA256 = r.cfg.SHA256
	}

	if r.snap != nil {
		rec.Boundary = r.snap.Boundary
		rec.SnapshotSHA = r.snap.SHA256
		rec.PathsComplete = r.snap.PathsComplete
	}

	if failure != nil {
		rec.Error = failure.Error()
	}

	return rec
}

// appendGuard appends the guard record of the run. The record carries
// the decision, and its pins also at the top level, because a reader
// counts the evaluated pins of every record kind as len(pins). A failed
// append stays silent and never changes the reply.
func (r *preEdit) appendGuard(decision lessoncheck.GuardDecision, failure error) {
	r.recorded = true

	if r.root == "" {
		// No work tree holds the run, so no audit stream exists for it.
		return
	}

	rec := r.record(lessoncheck.AuditGuard, failure)
	rec.PathsComplete = decision.PathsComplete
	rec.Pins = decision.Pins
	rec.Guard = &decision

	emitted := decision.ReplyWritten
	rec.Emitted = &emitted

	_ = lessoncheck.AppendAudit(r.root, rec)
}

// appendBaseline appends the baseline record of the run. failure is the
// reason when the run stores no baseline. A failed append stays silent.
func (r *preEdit) appendBaseline(failure error) {
	r.recorded = true

	if r.root == "" {
		return
	}

	_ = lessoncheck.AppendAudit(r.root, r.record(lessoncheck.AuditBaseline, failure))
}

// sweep removes the pending records that a crash or a cancelled tool
// left behind. Each pre-edit run sweeps after its record, so the sweep
// never delays a decision. A failure and a panic stay silent, because
// the next run sweeps again.
func (r *preEdit) sweep() {
	defer func() { _ = recover() }()

	if r.root == "" {
		return
	}

	_, _ = lessoncheck.Sweep(r.root, lessoncheck.CallerHook, r.lim)
}

// operationPaths returns the paths of the file operations, both ends of
// a move included, sorted and without duplicates. A path outside root
// is no longer in the operation, so no record names it.
func operationPaths(op lessoncheck.Operation) []string {
	var paths []string

	for _, fop := range op.Ops {
		paths = append(paths, fop.Paths()...)
	}

	sort.Strings(paths)

	return slices.Compact(paths)
}
