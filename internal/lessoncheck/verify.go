package lessoncheck

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds Verify, the one verification service. lessons
// --verify, check --lessons, the MCP check option, and the console call
// it, so all four report the same result. Verify reads the
// configuration and the working-tree snapshot, evaluates every
// executable pin, reads the inputs again, and publishes the result as
// fresh or stale. Then it appends one record to the lesson-check audit
// stream and sweeps the pending baselines.
// Revalidate checks later whether the binding of a published result
// still holds. The console calls it on refresh, because it shows a
// result after the working tree can change.

// VerifyRequest is the input of Verify.
type VerifyRequest struct {
	Root   string   // the top level of the git work tree, absolute
	Paths  []string // repository-relative; empty = default selection
	Caller Caller
	Limits Limits           // a zero value means DefaultLimits
	Now    func() time.Time // nil means time.Now
	// SeamarkVersion fills Verification.SeamarkVersion. The caller passes
	// it, because lessoncheck must not import cli, which holds the version.
	SeamarkVersion string
}

// verifyCommand is the rerun command without --file arguments.
const verifyCommand = "seamark lessons --verify"

// pinsLabel is the first part of the pins_sha256 composition. The label
// keeps this digest apart from the other compose digests.
const pinsLabel = "lesson-pins/1"

// standInBaselineTimeout bounds the one git call that names the baseline
// of a read that a cancellation or a deadline stopped. The call runs
// after the request context ended, so it needs a bound of its own.
const standInBaselineTimeout = 2 * time.Second

// beforeRevalidate runs between the evaluation and the revalidation when
// it is not nil. Only tests set it: it is the one point where a test can
// change an input after the read and before the publication.
var beforeRevalidate func()

// Verify verifies the working tree of req.Root against every executable
// pin. It never returns an error for an evaluation problem: a
// configuration error, a limit, a stale snapshot, a timeout, and a
// cancellation all give a result. err is only a usage or root failure,
// and then no result exists.
//
// The deadline of the whole verification is Limits.VerifyTimeout.
// Verify never reads a hook result, so a proposed-edit or actual-edit
// result never stands in for the working tree.
func Verify(ctx context.Context, req VerifyRequest) (*Verification, error) {
	if err := checkRequest(req); err != nil {
		return nil, err
	}

	lim := req.Limits.orDefault()

	selected, err := selectionPaths(req.Paths)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, lim.VerifyTimeout)
	defer cancel()

	loaded := loadConfig(req.Root)

	snap, stopped, err := readSnapshot(ctx, req.Root, selected, lim)
	if err != nil {
		return nil, err
	}

	results := []Result{}
	if loaded.cfg != nil {
		results = Evaluate(loaded.cfg, snap)
	}

	v := &Verification{
		Schema:         VerificationSchema,
		SeamarkVersion: req.SeamarkVersion,
		Caller:         req.Caller,
		Boundary:       snap.Boundary,
		Baseline:       snap.Baseline,
		Selection:      snap.Selection,
		Binding: Binding{
			SnapshotSHA256:  snap.SHA256,
			ConfigSHA256:    loaded.report.SHA256,
			PinsSHA256:      pinsDigest(results),
			CheckerVersions: checkerVersions(results),
		},
		PathsComplete:                snap.PathsComplete,
		UnreadableDirectories:        snap.UnreadableDirectories,
		UnreadableDirectoriesOmitted: snap.UnreadableDirectoriesOmitted,
		Limits:                       withCheckerLimits(snap.Limits, results, lim),
		Limitations:                  snap.Limitations,
		Config:                       loaded.report,
		Results:                      results,
		Listings:                     listings(loaded.cfg, lim),
		Changes:                      snap.Changes,
		statusStderr:                 snap.statusStderr,
	}

	if stopped.reason != "" {
		// The read stopped before a snapshot existed. Nothing was read,
		// so nothing can be read again: the result is stale.
		markUnconfirmed(v, stopped, true)
	} else {
		if beforeRevalidate != nil {
			beforeRevalidate()
		}

		publish(v, revalidate(ctx, req.Root, snap, loaded.report.SHA256), lim)
	}

	summarize(v, req.Root)

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}

	v.CheckedAt = now().UTC().Format(time.RFC3339)

	// Two steps follow the composed result. Neither one changes the
	// result or the exit code. They take no context, because the context
	// of a stopped read has already ended. Verify must still append the
	// record of that run.
	v.auditErr = AppendAudit(req.Root, verifyRecord(v))

	// The sweep removes old pending records that a crashed hook left
	// behind. The next pre-edit, post-edit, or verify run repeats it, so
	// Verify drops its error here.
	_, _ = Sweep(req.Root, req.Caller, lim)

	return v, nil
}

// Revalidate reports whether the binding of an earlier result v still
// holds. It reads the snapshot again from the selection of v and
// compares the baseline, the snapshot digest, the lessons.yaml digest,
// the pins digest, and the checker kinds with v. A change of content,
// scope, mode, check definition, baseline, or checker version gives
// stale.
//
// Revalidate runs no checker and writes nothing: no audit record and no
// sweep, because it publishes no new result. It reads with
// DefaultLimits, the limits of every console verification. Other
// limits can fill other content digests on an unchanged tree.
//
// A result that is already stale stays stale without a read. Its rows
// read unknown/stale-snapshot, so a fresh answer would contradict them.
// A read that the context ends proves nothing, so it gives stale and no
// error, as in Verify. The error reports a usage or a root failure, and
// the freshness is then stale too, so a caller that drops the error
// never shows a fresh result.
func Revalidate(ctx context.Context, root string, v *Verification) (Freshness, error) {
	switch err := checkRoot(root); {
	case v == nil:
		return FreshnessStale, errors.New("lesson checks: revalidate needs a result")
	case err != nil:
		return FreshnessStale, err
	case v.Freshness != FreshnessFresh:
		return FreshnessStale, nil
	}

	lim := DefaultLimits()

	ctx, cancel := context.WithTimeout(ctx, lim.VerifyTimeout)
	defer cancel()

	var paths []string
	if v.Selection.Kind == SelectionExplicitPaths {
		paths = v.Selection.Paths
	}

	snap, err := ReadWorkingTree(ctx, root, paths, lim)

	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FreshnessStale, nil
	case err != nil:
		return FreshnessStale, fmt.Errorf("lesson checks: revalidate: %w", err)
	}

	loaded := loadConfig(root)
	now := Binding{SnapshotSHA256: snap.SHA256, ConfigSHA256: loaded.report.SHA256}
	now.PinsSHA256, now.CheckerVersions = configBinding(loaded.cfg)

	// An unreadable file and an absent file both have the digest "", so
	// the validity of the configuration decides between them. A read
	// error is never an absent file.
	if snap.Baseline != v.Baseline || !sameBinding(now, v.Binding) || loaded.report.Valid != v.Config.Valid {
		return FreshnessStale, nil
	}

	return FreshnessFresh, nil
}

// configBinding returns the pins digest and the checker kinds that
// Verify derives from the results of cfg. It builds one stand-in result
// per executable pin, as Evaluate does, with the check kind that
// runnable returns. So it needs no snapshot and runs no checker.
// pinsDigest and checkerVersions stay the one rule of both digests. A
// nil cfg (a tier-1 error) has no result, as in Verify.
func configBinding(cfg *Config) (pins string, kinds []Kind) {
	results := []Result{}

	if cfg != nil {
		for _, pin := range cfg.Pins {
			if !pin.Mode.Executable() {
				continue
			}

			res := Result{Entry: pin.Entry, PinVersion: pin.Version, Reason: ReasonConfigurationError}
			if check, _, ok := runnable(cfg, pin); ok {
				res.Reason, res.Check.Kind = "", check.Kind
			}

			results = append(results, res)
		}
	}

	return pinsDigest(results), checkerVersions(results)
}

// sameBinding reports whether two bindings name the same snapshot,
// config, pins, and checker kinds.
func sameBinding(a, b Binding) bool {
	return a.SnapshotSHA256 == b.SnapshotSHA256 && a.ConfigSHA256 == b.ConfigSHA256 &&
		a.PinsSHA256 == b.PinsSHA256 && slices.Equal(a.CheckerVersions, b.CheckerVersions)
}

// AuditError returns the error of the verify audit record, or nil. A
// failed record never changes the result or the exit code. lessons
// --verify prints it on its exit-0 paths only, because a non-zero exit
// has its own stderr line. check --lessons and the MCP server stay
// silent about it.
func (v *Verification) AuditError() error {
	return v.auditErr
}

// verifyRecord returns the verify audit record of a composed result. It
// holds the digests, the paths, and the outcome of each pin. It holds no
// evidence excerpt, because an audit record never holds file content.
func verifyRecord(v *Verification) AuditRecord {
	paths := make([]string, 0, 2*len(v.Changes))

	for _, ch := range v.Changes {
		paths = append(paths, ch.Path, ch.OldPath)
	}

	return AuditRecord{
		TS:            v.CheckedAt,
		Kind:          AuditVerify,
		Caller:        v.Caller,
		Boundary:      v.Boundary,
		ConfigSHA256:  v.Binding.ConfigSHA256,
		SnapshotSHA:   v.Binding.SnapshotSHA256,
		Paths:         sortedUnique(paths),
		PathsComplete: v.PathsComplete,
		Pins:          auditPinsOf(v.Results),
		State:         v.Summary.State,
	}
}

// checkRequest refuses a request that no verification can serve. Each
// case is a programming error of the caller, so the text names it.
func checkRequest(req VerifyRequest) error {
	if !req.Caller.known() {
		return fmt.Errorf("lesson checks: unknown caller %q", req.Caller)
	}

	return checkRoot(req.Root)
}

// checkRoot refuses a root that is not an absolute path. The rerun
// command names absolute paths, and an empty root would make git read
// the process directory. Verify and Revalidate share it, so both give
// the same error text.
func checkRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("lesson checks: root %q is not an absolute path", root)
	}

	return nil
}

// loadedConfig is the configuration as Verify read it. report.SHA256 is
// the hex SHA-256 of the bytes, and "" when the file is absent or
// unreadable.
type loadedConfig struct {
	cfg    *Config // nil after a tier-1 error
	report ConfigReport
}

// configPath returns the path of lessons.yaml under root.
func configPath(root string) string {
	return filepath.Join(root, ".seamark", "lessons.yaml")
}

// loadConfig reads lessons.yaml once and validates it, as Load does. It
// reads the bytes itself, because a tier-1 error gives no Config, and
// the binding and the revalidation still need the digest of the bytes.
func loadConfig(root string) loadedConfig {
	data, err := os.ReadFile(configPath(root))

	return parseLoadedConfig(data, err)
}

// parseLoadedConfig builds the loaded configuration from one file read:
// its bytes and its read error. Verify and the capability report share
// it, and it applies the read rules of Load. So all three treat an
// absent file and a read error the same way.
func parseLoadedConfig(data []byte, readErr error) loadedConfig {
	cfg, errs, err := parseRead(data, readErr)
	if err == nil {
		return loadedConfig{cfg: cfg, report: configReportOf(cfg, errs)}
	}

	// A tier-1 error leaves no Config, so this branch hashes the bytes
	// itself. A file that did not read has no bytes and no digest.
	digest := ""
	if readErr == nil {
		digest = hexSHA256(data)
	}

	return tierOne(err, digest)
}

// tierOne reports a file that does not read or decode. The report holds
// one decode-error entry. Its message is the error text without the
// "lessons config: " prefix, because ConfigError.Error adds the prefix
// again. The version is 0, because no version was read.
func tierOne(err error, digest string) loadedConfig {
	return loadedConfig{
		report: ConfigReport{
			SHA256: digest,
			Valid:  false,
			Errors: []ConfigError{{
				Code:    CodeDecodeError,
				Subject: "lessons.yaml",
				Message: configErrorText(err),
			}},
		},
	}
}

// readSnapshot reads the working-tree snapshot. When the context ends
// before a snapshot exists, it returns a stand-in snapshot without
// changes and the cause that every result then carries, because a
// timeout or a cancellation is a result, not an error.
func readSnapshot(ctx context.Context, root string, selected []string, lim Limits) (*Snapshot, reasonHit, error) {
	snap, err := ReadWorkingTree(ctx, root, selected, lim)
	if err == nil {
		return snap, reasonHit{}, nil
	}

	stopped, ok := causeOf(err, LimitVerifyTimeout)
	if !ok {
		return nil, reasonHit{}, err
	}

	// The result needs a baseline from the closed list. The stopped read
	// returns no baseline, also when it stopped after its baseline read.
	// So one more bounded baseline read names it. When that fails too, no
	// result can name its baseline.
	retry, cancel := context.WithTimeout(context.WithoutCancel(ctx), standInBaselineTimeout)
	defer cancel()

	baseline, berr := newTreeReader(retry, root, lim).baseline()
	if berr != nil {
		return nil, reasonHit{}, err
	}

	snap = newWorkingTreeSnapshot(baseline, selected, lim)
	snap.PathsComplete = false

	if stopped.reason == ReasonResourceLimit {
		timedOut(snap, lim)
	}

	snap.SHA256 = snapshotDigest(snap)

	return snap, stopped, nil
}

// withCheckerLimits returns the limit hits of the snapshot plus each
// limit that a checker reached and the snapshot does not list.
// Verification.Limits is the one list of every evaluation limit. A
// checker reaches max-diff-lines without a snapshot read, so only Verify
// can add it.
// Paths counts each undecided path once, also when several pins reach
// the limit on it. A limit that the snapshot lists keeps its count,
// because the producer already counted its paths. The list is sorted by
// name. Verify calls it before publish, because the stale rewrite
// removes the unknown paths that name a checker limit.
func withCheckerLimits(hits []LimitHit, results []Result, lim Limits) []LimitHit {
	out := append([]LimitHit(nil), hits...)
	paths := map[string]map[[2]string]bool{}

	for _, r := range results {
		for _, e := range r.UnknownPaths {
			if e.limit == "" || HasLimit(hits, e.limit) {
				continue
			}

			if paths[e.limit] == nil {
				paths[e.limit] = map[[2]string]bool{}
			}

			paths[e.limit][[2]string{e.Path, e.OldPath}] = true
		}
	}

	for name, set := range paths {
		out = append(out, LimitHit{Name: name, Value: limitValue(name, lim), Paths: len(set)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// limitValue returns the value of a content limit that a checker can
// reach, in the unit of LimitHit.Value: lines or bytes. Any other name
// gives 0, because no checker reports it.
func limitValue(name string, lim Limits) int64 {
	switch name {
	case LimitMaxDiffLines:
		return int64(lim.MaxDiffLines)
	case LimitMaxFileBytes:
		return lim.MaxFileBytes
	case LimitMaxSnapshotBytes:
		return lim.MaxSnapshotBytes
	default:
		return 0
	}
}

// pinsDigest returns pins_sha256: the compose digest of the label and
// "entry:version" of each evaluated pin, sorted. A configuration-error
// result counts as evaluated, as in the summary. No result gives the
// digest of the label alone.
func pinsDigest(results []Result) string {
	parts := make([]string, 0, len(results))

	for _, r := range results {
		parts = append(parts, strconv.Itoa(r.Entry)+":"+r.PinVersion)
	}

	sort.Strings(parts)

	return reviews.ComposeDigest(append([]string{pinsLabel}, parts...)...)
}

// checkerVersions returns the sorted kinds whose checker ran. A
// configuration-error result ran no checker, so its kind does not
// count. The slice is never nil, so it encodes as [].
func checkerVersions(results []Result) []Kind {
	kinds := []Kind{}

	for _, r := range results {
		if r.Reason != ReasonConfigurationError && r.Check.Kind != "" {
			kinds = append(kinds, r.Check.Kind)
		}
	}

	slices.Sort(kinds)

	return slices.Compact(kinds)
}

// listings returns one listing per pin that Verify does not evaluate, in
// file order. An advisory pin with a check never runs, so the listing
// names the check, and the text names the command that arms it. A pin
// with an unknown mode is advisory, because an unknown mode must never
// arm a check. The slice is never nil.
func listings(cfg *Config, lim Limits) []PinListing {
	out := []PinListing{}

	if cfg == nil {
		return out
	}

	for _, pin := range cfg.Pins {
		if pin.Mode.Executable() {
			continue
		}

		l := PinListing{
			PinRef:     pin.Ref,
			PinVersion: pin.Version,
			Entry:      pin.Entry,
			Rule:       ruleText(pin.Rule.Rule, lim.MaxRuleRunes),
			Listing:    ListingNotCheckable,
		}

		if pin.CheckID != "" {
			ref := rawCheckRef(cfg.Raw, pin.CheckID)
			l.Listing = ListingAdvisoryWithCheck
			l.Check = &ref
		}

		out = append(out, l)
	}

	return out
}

// recheck is the outcome of the revalidation.
type recheck struct {
	// changed is true when an input differs from the read.
	changed bool
	// unconfirmed is the cause when the revalidation could not finish:
	// the context ended, or the status output was never read. Its reason
	// is "" when the revalidation finished.
	unconfirmed reasonHit
}

// revalidate reads the inputs of snap again and compares them with the
// read: the HEAD commit, the status output with the directory census,
// the content SHA-256 of each file whose content the producer read, the
// Lstat identity of each other path that it looked at, and the
// lessons.yaml bytes. It writes nothing.
//
// A budget with verify-timeout names the cause when ctx ends. So the
// revalidation maps a context error to a cause as every checker does.
func revalidate(ctx context.Context, root string, snap *Snapshot, configSHA string) recheck {
	r := newTreeReader(ctx, root, snap.limits())
	b := budget{ctx: ctx, limit: LimitVerifyTimeout}

	baseline, err := r.baseline()
	if hit, ended := b.ended(); ended {
		return recheck{unconfirmed: hit}
	}

	if err != nil || baseline != snap.Baseline {
		return recheck{changed: true}
	}

	// An empty status digest means that the producer never read status.
	// Its path set is then not complete, so no result is a pass. The
	// other inputs still get their check, but freshness stays unproven.
	var unconfirmed reasonHit

	if snap.statusSHA256 == "" {
		unconfirmed = reasonHit{reason: ReasonIncompletePaths}
	} else {
		_, sum, _ := r.statusRecords(snap.Selection.Paths)
		if hit, ended := b.ended(); ended {
			return recheck{unconfirmed: hit}
		}

		if sum != snap.statusSHA256 {
			return recheck{changed: true}
		}
	}

	for _, id := range snap.identities {
		same := sameIdentity(r, id)
		if hit, ended := b.ended(); ended {
			return recheck{unconfirmed: hit}
		}

		if !same {
			return recheck{changed: true}
		}
	}

	if configDigest(root) != configSHA {
		return recheck{changed: true}
	}

	return recheck{unconfirmed: unconfirmed}
}

// identityModeBits are the mode bits that a read file keeps in the
// snapshot: the file type and the owner executable bit, which git reads
// as the executable bit.
const identityModeBits = uint32(fs.ModeType | 0o100)

// sameIdentity reports whether a path looks as it did at the read. A
// file whose content the producer read must keep its content SHA-256,
// because an edit can keep the size and the modification time. Every
// other path must keep its Lstat identity.
func sameIdentity(r *treeReader, old fileID) bool {
	now := r.inspect(old.path).id
	if now.state != old.state {
		return false
	}

	if old.state != pathPresent {
		return true
	}

	if old.sha256 != "" {
		return now.mode&identityModeBits == old.mode&identityModeBits && r.load(old.path).id.sha256 == old.sha256
	}

	return now.size == old.size && now.mtimeNS == old.mtimeNS && now.mode == old.mode && now.inode == old.inode
}

// configDigest returns the hex SHA-256 of the lessons.yaml bytes, or ""
// when the file is absent or unreadable, as loadConfig records it.
func configDigest(root string) string {
	data, err := os.ReadFile(configPath(root))
	if err != nil {
		return ""
	}

	return hexSHA256(data)
}

// publish applies the revalidation to the result. A fresh result stays
// as it is. A changed input makes the result stale: a violation stays
// with its evidence, because an observed violation stays observed, and
// every other result becomes unknown/stale-snapshot. A first-run
// unknown keeps its cause in FirstRun. A revalidation that could not
// finish proves nothing, so the result is stale too. Its pass and
// not-applicable results then become unknown with the cause, and an
// unknown result keeps its own reason, which names the real cause.
func publish(v *Verification, check recheck, lim Limits) {
	switch {
	case check.changed:
		v.Freshness = FreshnessStale

		for i := range v.Results {
			if v.Results[i].Outcome != OutcomeViolation {
				setUnknown(v, i, reasonHit{reason: ReasonStaleSnapshot})
			}
		}
	case check.unconfirmed.reason != "":
		markUnconfirmed(v, check.unconfirmed, false)

		if check.unconfirmed.limit == LimitVerifyTimeout && !HasLimit(v.Limits, LimitVerifyTimeout) {
			v.Limits = append(v.Limits, LimitHit{Name: LimitVerifyTimeout, Value: lim.VerifyTimeout.Milliseconds()})
		}
	default:
		v.Freshness = FreshnessFresh
	}
}

// markUnconfirmed makes a result stale without a detected change. Each
// pass and not-applicable result becomes unknown with the cause. all
// also rewrites the unknown results, except a configuration error,
// which stays the cause that the user must fix first.
func markUnconfirmed(v *Verification, cause reasonHit, all bool) {
	v.Freshness = FreshnessStale

	for i, r := range v.Results {
		switch {
		case r.Outcome == OutcomePass, r.Outcome == OutcomeNotApplicable:
			setUnknown(v, i, cause)
		case all && r.Outcome == OutcomeUnknown && r.Reason != ReasonConfigurationError:
			setUnknown(v, i, cause)
		}
	}
}

// setUnknown makes result i unknown with the cause. The findings of the
// first run describe inputs that the result no longer confirms, so
// setUnknown removes them. A first-run pass or not-applicable stays in
// the text output only, so no JSON reader takes it for a current
// outcome.
//
// The stale rewrite keeps the reason and the limit of a first-run
// unknown in FirstRun, for the JSON and the text output. They name the
// cause that the user can act on. Every other rewrite of an unknown
// comes from a stand-in snapshot, which evaluated nothing, so it keeps
// no first run.
func setUnknown(v *Verification, i int, cause reasonHit) {
	r := &v.Results[i]

	switch {
	case r.Outcome != OutcomeUnknown:
		if v.firstRun == nil {
			v.firstRun = map[int]Outcome{}
		}

		v.firstRun[r.Entry] = r.Outcome
	case cause.reason == ReasonStaleSnapshot:
		r.FirstRun = &FirstRun{Reason: r.Reason, Limit: r.Limit}
	}

	r.Outcome = OutcomeUnknown
	r.Reason = cause.reason
	r.Limit = cause.limit
	r.Evidence = nil
	r.EvidenceOmitted = 0
	r.UnknownPaths = nil
}

// HasLimit reports whether a limit hit with the name exists.
func HasLimit(hits []LimitHit, name string) bool {
	return slices.ContainsFunc(hits, func(h LimitHit) bool { return h.Name == name })
}

// summarize counts the results and the listings and sets the state and
// the rerun command. An invalid config and a stale result are incomplete
// even without an unknown result, because neither proves a pass. root is
// the root of the request, for the rerun command.
func summarize(v *Verification, root string) {
	s := countOutcomes(v.Results)
	s.Changes = len(v.Changes)

	for _, l := range v.Listings {
		if l.Listing == ListingAdvisoryWithCheck {
			s.AdvisoryWithCheck++
		} else {
			s.NotCheckable++
		}
	}

	s.State = summaryState(s, !v.Config.Valid || v.Freshness != FreshnessFresh, s.PinsEvaluated > 0)
	v.Summary = s

	if s.State == SummaryViolation || s.State == SummaryIncomplete {
		v.Rerun = rerunCommand(root, v.Selection.Paths)
	}
}

// countOutcomes counts the results by outcome. It fills PinsEvaluated,
// Pass, Violation, NotApplicable, and Unknown. Verify and the post-edit
// hook share it, so both count by one rule.
func countOutcomes(results []Result) Summary {
	s := Summary{PinsEvaluated: len(results)}

	for _, r := range results {
		switch r.Outcome {
		case OutcomePass:
			s.Pass++
		case OutcomeViolation:
			s.Violation++
		case OutcomeNotApplicable:
			s.NotApplicable++
		default:
			// An outcome outside the closed list counts as unknown, so it
			// never counts toward a pass.
			s.Unknown++
		}
	}

	return s
}

// summaryState returns the summary state of the counts. The state order
// is violation, incomplete, no-executable-checks, no-applicable-checks,
// passed, so the state always names the most serious result. incomplete
// is true when the caller cannot confirm the check set or the snapshot.
// executable is true when the check set holds an executable pin. Verify
// and the post-edit hook share it, so both order the states alike.
func summaryState(s Summary, incomplete, executable bool) SummaryState {
	switch {
	case s.Violation > 0:
		return SummaryViolation
	case s.Unknown > 0 || incomplete:
		return SummaryIncomplete
	case !executable:
		return SummaryNoExecutableChecks
	case s.Pass == 0:
		return SummaryNoApplicableChecks
	default:
		return SummaryPassed
	}
}

// rerunCommand returns the exact command that repeats a verification.
// Each --file path is absolute, because another process runs the
// command, maybe from another directory.
func rerunCommand(root string, paths []string) string {
	var b strings.Builder

	b.WriteString(verifyCommand)

	for _, p := range paths {
		b.WriteString(" --file ")
		b.WriteString(QuoteArg(filepath.Join(root, filepath.FromSlash(p))))
	}

	return b.String()
}

// QuoteArg single-quotes a shell word that holds a character outside
// [A-Za-z0-9_./-], so a shell reads the word as one argument. A plain
// path stays as it is, so the common command stays easy to read. The
// rerun command uses it, and so does the inspect command of a
// preparation, so the two quote alike.
func QuoteArg(s string) string {
	if s != "" && strings.TrimLeftFunc(s, plainArgRune) == "" {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plainArgRune reports whether a shell word can hold r without quotes.
func plainArgRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./-", r)
}
