package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds lessons --verify --hook, the post-edit hook of the
// lesson checks (contract K9). It decodes one native completion event,
// reads the pending baseline of the same call, observes the actual
// result on disk, evaluates every check and enforce pin in scope, and
// reports the findings to the agent. It appends one post-edit record to
// the lesson-check audit stream and deletes the pending baseline.
//
// The hook never denies, rewinds, or reverts an edit, because the tool
// already ran (spec L122). Every run exits 0. The hook needs no capture
// opt-in and never opens the capture archive (spec L263).

// errVerifyHookFlags is the refusal of a post-edit hook call with a
// selection or an output form (contracts §4.1). The hook reads its
// paths from the event and writes a native reply, so --file and --json
// have no meaning there. Skills grant `seamark lessons --verify --json*`
// and `seamark lessons --verify --file *` without a prompt (plan step
// P3.12), so such a call must never reach the hook.
var errVerifyHookFlags = errors.New("--verify --hook takes no --file or --json")

// hookConflicts lists the flags that the plain --hook and --hook-reset
// calls refuse, in the order of the help text (P3 review ruling R3).
// Skills grant `seamark lessons --file *` and `seamark lessons --region
// *` without a prompt. Without this list, such a call with --hook or
// --hook-reset reaches an advisory hook and writes its delivery state
// and a firing record that no native event produced.
var hookConflicts = []string{"file", "list", "region", "stats"}

// hookFlagError refuses a plain --hook or --hook-reset call that also
// sets a listed flag, with the mode-conflict template of contracts §4.2.
// The guards of --verify, --explore, --guard, and --baseline run first,
// so a call with one of those modes keeps its own text.
func hookFlagError(cmd *cobra.Command, hookMode, hookReset bool) error {
	modes := []struct {
		name string
		set  bool
	}{
		{name: "hook", set: hookMode},
		{name: "hook-reset", set: hookReset},
	}

	for _, mode := range modes {
		if !mode.set {
			continue
		}

		for _, name := range hookConflicts {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s does not combine with --%s", mode.name, name)
			}
		}
	}

	return nil
}

// errPostEditPanic is the error of a post-edit run that panicked. The
// record keeps no panic value, because a value can quote the event.
var errPostEditPanic = errors.New("lesson checks: the post-edit handler stopped on an internal error")

// postEdit holds the state of one post-edit hook run. The fields fill in
// step order, so the record of a failed run names what the run read.
type postEdit struct {
	cmd *cobra.Command
	// root is the git top level. lessons.yaml, the observation, the path
	// normalization, .seamark/checks, and the audit stream use it. rootErr
	// is set when no git work tree holds the -C directory.
	root    string
	rootErr error
	// idRoot is the K2 identity root. The session and match digests hash
	// over it, as in the pre-edit hooks, so both hooks of one call compute
	// one pending key and their records join (contract K7).
	idRoot string
	// lim is DefaultLimits. A partly filled Limits value would set
	// pending-max-age to 0, and the sweep would expire every record.
	lim        lessoncheck.Limits
	client     integration.Client
	completion lessoncheck.Completion
	pending    *lessoncheck.Pending
	result     lessoncheck.PostEdit
	snap       *lessoncheck.Snapshot
	cfgSHA     string
	paths      []string
	complete   bool
	emitted    bool
	failure    error
	// recorded is true once the run tried to append its record. The
	// panic recovery then does not try again.
	recorded bool
	// read is true once the run tried to read the pending record of a
	// covered completion. Only such a run deletes the record, so an
	// uncovered event never removes the baseline of a covered call.
	read bool
}

// beforePostEditEvaluate runs right before the post-edit evaluation, when
// it is not nil. Only tests set it: a test waits there until the post-edit
// budget ends, so it proves the budget bound without a wall-clock limit.
var beforePostEditEvaluate func()

// runLessonsVerifyHook implements lessons --verify --hook (contract K9).
// It fails open: every failure gives exit 0, and the record names it
// when a work tree holds the run. A failed reply write also gives exit
// 0, because the edit is done and the findings also reach the audit
// stream and the next lessons --verify (contracts §4.1).
func runLessonsVerifyHook(cmd *cobra.Command, opts *options, clientID string) error {
	root, rootErr := verifyRoot(opts.workspace)
	run := &postEdit{cmd: cmd, root: root, rootErr: rootErr, idRoot: hookIdentityRoot(opts), lim: hookLimits()}

	defer func() {
		if recover() != nil && !run.recorded {
			run.failure = errors.Join(run.failure, errPostEditPanic)
			run.appendRecord()
		}

		run.consume()
	}()

	run.verify(clientID)
	run.appendRecord()

	return nil
}

// verify runs the steps of the hook up to the reply (contract K9, flow).
func (r *postEdit) verify(clientID string) {
	client, err := postEditClient(clientID)
	if err != nil {
		r.failure = err

		return
	}

	r.client = client

	completion, decodeErr := r.readCompletion()
	r.completion = completion

	if r.rootErr != nil {
		r.failure = r.rootErr

		return
	}

	// The sweep runs before the read, so an expired record of this call
	// is discarded and never serves as a baseline (contract K7).
	_, _ = lessoncheck.Sweep(r.root, lessoncheck.CallerHook, r.lim)

	if errors.Is(decodeErr, integration.ErrNotApplicable) {
		// Another event or another tool: no covered edit ran, so no check
		// applies. The record names the cause.
		r.failure = decodeErr

		return
	}

	r.read = true
	r.pending, err = r.readPending()
	r.failure = errors.Join(decodeErr, err)

	if decodeErr == nil {
		r.paths, r.complete = normalizeEventPaths(r.root, completion.CWD, completion.Paths)
	}

	cfg, _, loadErr := lessoncheck.Load(r.root)
	if cfg != nil {
		r.cfgSHA = cfg.SHA256
	}

	// One deadline bounds the observation and the evaluation. ObserveAfter
	// applies the post-edit budget again from its own start, and the
	// earlier deadline wins.
	ctx, cancel := context.WithTimeout(r.cmd.Context(), r.lim.PostEditBudget)
	defer cancel()

	in := lessoncheck.PostEditInput{
		Config: cfg, LoadErr: loadErr, Pending: r.pending,
		Paths: r.paths, PathsComplete: r.complete, NoBaseline: noBaselineReason(decodeErr),
		Root: r.root, Tool: completion.Tool, Outcome: r.outcome(), Context: ctx,
	}

	if r.pending != nil {
		in.Snapshot, in.ObserveErr = lessoncheck.ObserveAfter(ctx, r.root, r.pending, r.lim)
		r.snap = in.Snapshot
		r.failure = errors.Join(r.failure, in.ObserveErr)
	}

	if beforePostEditEvaluate != nil {
		beforePostEditEvaluate()
	}

	r.result = lessoncheck.EvaluatePostEdit(in)
	r.failure = errors.Join(r.failure, loadErr)

	if r.result.Findings != "" {
		r.emitted, err = r.reply(r.result.Findings)
		r.failure = errors.Join(r.failure, err)
	}
}

// postEditClient returns the client whose native completion events the
// run translates. The ID comes from the installed hook command, never
// from the payload, as for the pre-edit hooks. A client without a guard
// or a completion decoder has no post-edit verification.
func postEditClient(clientID string) (integration.Client, error) {
	if clientID == "" {
		clientID = integration.ClaudeID
	}

	registry := integration.Builtin()

	client, ok := registry.Lookup(clientID)
	if !ok {
		return integration.Client{}, fmt.Errorf("unknown hook client %q (known: %s)",
			render.Sanitize(clientID), strings.Join(registry.IDs(), ", "))
	}

	if err := client.Require(integration.CapabilityLessonVerify); err != nil {
		return integration.Client{}, err
	}

	return client, nil
}

// readCompletion reads the payload under its size cap and decodes the
// completion. On an error the completion holds the event identity and
// the outcome, as far as the payload names them.
func (r *postEdit) readCompletion() (lessoncheck.Completion, error) {
	payload, err := integration.ReadHookPayload(r.cmd.InOrStdin())
	if err != nil {
		return lessoncheck.Completion{Client: r.client.ID}, err
	}

	return r.client.Completions.DecodeCompletion(payload)
}

// readPending reads the pending baseline of the call. A call without a
// tool-use ID has no record. An unsafe directory, an absent record, and
// a record that cannot serve all give no baseline (contract K7).
func (r *postEdit) readPending() (*lessoncheck.Pending, error) {
	_, match := r.digests()
	if match == "" {
		return nil, errNoMatchID
	}

	return lessoncheck.ReadPending(r.root, r.client.ID, match, r.lim)
}

// noBaselineReason returns the reason of the results without a record.
// An event that does not decode names no path that the checks could
// place: an unsupported patch grammar leaves the path set incomplete,
// and every other decode error is unparseable input.
func noBaselineReason(decodeErr error) lessoncheck.Reason {
	switch {
	case decodeErr == nil:
		return lessoncheck.ReasonMissingBaseline
	case errors.Is(decodeErr, integration.ErrUnsupportedGrammar):
		return lessoncheck.ReasonIncompletePaths
	default:
		return lessoncheck.ReasonUnparseableInput
	}
}

// outcome returns the tool outcome of the record. An event that names
// none gives unknown, because a post-edit record always names one.
func (r *postEdit) outcome() lessoncheck.ToolOutcome {
	if r.completion.Outcome == "" {
		return lessoncheck.ToolUnknown
	}

	return r.completion.Outcome
}

// normalizeEventPaths resolves the native paths of a completion event at
// root with delivery.NormalizePaths, one path at a time, as the
// pre-edit hooks do. A path outside root by its text alone is outside
// every pin scope, so it leaves the list. Every other rejected path can
// lie inside the work tree, so the path set is not complete.
func normalizeEventPaths(root, cwd string, native []string) (paths []string, complete bool) {
	if cwd == "" {
		// A client that sends no working directory sends paths relative
		// to the hook process, as for the edit hook.
		cwd, _ = os.Getwd()
	}

	complete = true

	for _, p := range native {
		n := delivery.NormalizePaths(root, cwd, []string{p})

		switch {
		case len(n.Files) == 1:
			paths = append(paths, n.Files[0])
		case len(n.Rejected) == 1 && n.Rejected[0].Reason == delivery.RejectOutside && outsideByText(root, cwd, p):
		default:
			complete = false
		}
	}

	return paths, complete
}

// reply writes the findings in the native reply of the client. Both
// built-in clients answer on stdout. A HookReply can also hold stderr
// text, so the function writes that too. The exit status stays 0.
// written is false when the encoding or a write fails.
func (r *postEdit) reply(findings string) (written bool, err error) {
	reply, err := r.client.Completions.EncodeFindings(findings, r.outcome())
	if err != nil {
		return false, err
	}

	if len(reply.Stdout) > 0 {
		if _, err := r.cmd.OutOrStdout().Write(reply.Stdout); err != nil {
			return false, err
		}
	}

	if len(reply.Stderr) > 0 {
		if _, err := r.cmd.ErrOrStderr().Write(reply.Stderr); err != nil {
			return false, err
		}
	}

	return true, nil
}

// digests returns the K2 session and match digests of the run over the
// identity root. Each digest needs its raw IDs, as in the firing log.
func (r *postEdit) digests() (session, match string) {
	if r.idRoot == "" {
		return "", ""
	}

	if r.completion.SessionID != "" {
		session = reviews.SessionDigest(r.idRoot, r.completion.SessionID)
	}

	if r.completion.MatchID != "" {
		match = reviews.MatchDigest(r.idRoot, r.completion.SessionID, r.completion.MatchID)
	}

	return session, match
}

// appendRecord appends the post-edit record of the run (contract K9). It
// holds digests, codes, repository-relative paths, and line numbers
// only. The context digest comes from the pending record, because a
// completion event names no receiver. A failed append stays silent.
func (r *postEdit) appendRecord() {
	r.recorded = true

	if r.root == "" {
		// No work tree holds the run, so no audit stream exists for it.
		return
	}

	session, match := r.digests()

	rec := lessoncheck.AuditRecord{
		Kind:          lessoncheck.AuditPostEdit,
		Caller:        lessoncheck.CallerHook,
		Client:        r.client.ID,
		Tool:          render.Truncate(render.Sanitize(r.completion.Tool), maxAuditToolRunes),
		SessionSHA:    session,
		MatchSHA:      match,
		Boundary:      lessoncheck.BoundaryActualEdit,
		ConfigSHA256:  r.cfgSHA,
		Paths:         r.paths,
		PathsComplete: r.complete,
		Pins:          r.result.Pins,
		State:         r.result.State,
		ToolOutcome:   r.outcome(),
		Attribution:   r.result.Attribution,
		Emitted:       &r.emitted,
	}

	if r.pending != nil {
		rec.ContextSHA = r.pending.ContextSHA
	}

	if r.snap != nil {
		rec.SnapshotSHA = r.snap.SHA256
		rec.Paths = r.snap.Selection.Paths
		rec.PathsComplete = r.snap.PathsComplete
	}

	if r.failure != nil {
		rec.Error = r.failure.Error()
	}

	_ = lessoncheck.AppendAudit(r.root, rec)
}

// consume deletes the pending record of the call after the run, also
// after a failed evaluation (contract K7). A record serves one call, so
// a later event of the same call finds no baseline. A failure and a
// panic stay silent, because the sweep removes the record later.
func (r *postEdit) consume() {
	defer func() { _ = recover() }()

	if !r.read || r.root == "" {
		return
	}

	if _, match := r.digests(); match != "" {
		_ = lessoncheck.DeletePending(r.root, r.client.ID, match)
	}
}
