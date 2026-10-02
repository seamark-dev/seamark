package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/fixes"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ErrConflict reports that an evidence plan no longer matches the
// workspace. The user reviewed a different state, so the console asks
// for a new preview instead of running the old one.
var ErrConflict = errors.New("the evidence preview is out of date")

// ErrRefreshBlocked reports a run of a plan that has blockers. A blocked
// plan is a disclosure only; it never authorizes work.
var ErrRefreshBlocked = errors.New("the evidence refresh is blocked")

// Window describes the review window that a refresh applies.
type Window struct {
	// Stored is the raw reviews.window_days value; nil when absent.
	Stored *int
	// Effective is the window in days; -1 means unlimited. It is 0 when
	// ConfigErr is set, because no window is known then.
	Effective int
	// Floor is the count of newest comments that always survive the
	// window, so the window is not a strict cutoff.
	Floor int
	// ConfigErr is the config.yaml read or parse error. A plan blocks on
	// it, unlike the CLI, which falls back to the default window.
	ConfigErr error
}

// Snapshot is the read-only evidence state of a workspace. Unknown
// values stay unknown: a stamp of 0 means never mined or unknown, and a
// non-nil CountsErr means that the counts are not zero but unknown.
type Snapshot struct {
	// Probe is the index database state.
	Probe workspace.IndexProbe
	// Findings counts the stored findings per source.
	Findings map[string]int
	// FixFindings sums the fix-mined sources; ReviewFindings is the
	// review source; Lessons counts the stored review lessons.
	FixFindings    int
	ReviewFindings int
	Lessons        int
	// DistilledGroups counts the evidence groups that a distillation
	// recorded, with or without a proposal. A group can end without a
	// proposal, so an empty ledger alone does not prove that none ran.
	DistilledGroups int
	// FixesMinedAt and ReviewsMinedAt are the last successful mine times
	// in Unix seconds; 0 means never or unknown.
	FixesMinedAt   int64
	ReviewsMinedAt int64
	// CountsErr is the store read error, or nil.
	CountsErr error
	// Repository is the GitHub target that the review fetch uses.
	Repository reviews.Repository
	RepoState  reviews.RepositoryState
	// GHPath is where gh is on PATH; "" when it is missing. A path is not
	// proof of a login, and only a fetch tests access.
	GHPath string
	// Window is the review window from config.yaml.
	Window Window
	// ObservedAt is the time of the observation.
	ObservedAt time.Time
}

// Inspect reads the evidence state of ws. It uses no network, writes
// nothing, and never creates a database: the store opens read-only.
// Local git runs only to resolve the origin remote.
func Inspect(ws workspace.Workspace) Snapshot {
	snap := evReadLocal(ws)
	snap.Repository, snap.RepoState = reviews.ResolveRepository(snap.Probe.Root)
	snap.GHPath = evLookGH()

	return snap
}

// evReadLocal reads the stored evidence and the review window. It runs
// no git and no gh, so a blocked run can report the state it left
// without work. The repository stays unresolved.
func evReadLocal(ws workspace.Workspace) Snapshot {
	snap := evReadStore(ws)
	snap.Window, _ = evReadWindow(snap.Probe.Root)

	return snap
}

// EffectKind tells how a refresh relates to one part of the stored data.
type EffectKind int

const (
	// EffectReplace means the refresh replaces the data.
	EffectReplace EffectKind = iota + 1
	// EffectKeep means the refresh never changes the data.
	EffectKeep
	// EffectLater means a later, separate action can change the data
	// because of the refresh. The refresh itself does not do it.
	EffectLater
)

// Effect is one stored-data disclosure of a plan.
type Effect struct {
	// Source is index.SourceFixes, index.SourceReviews, or "" for data
	// that belongs to no source.
	Source string
	Kind   EffectKind
	// Text is one plain line that names the data and the effect.
	Text string
}

// BlockerKind names why a plan cannot run.
type BlockerKind int

const (
	// BlockIndex means the index database is not ready. The user builds
	// or repairs it as a separate, explicit action.
	BlockIndex BlockerKind = iota + 1
	// BlockConfig means config.yaml cannot be read or parsed.
	BlockConfig
)

// Blocker is one reason why a plan cannot run.
type Blocker struct {
	Kind BlockerKind
	// Reason is one safe line: secrets redacted, controls removed.
	Reason string
	// Path is the file that the user repairs: the database for
	// BlockIndex, config.yaml for BlockConfig.
	Path string
}

// Plan is the read-only preview of one evidence refresh. It performs no
// network call and no write. RunRefresh runs it only while the bound
// observation still holds.
type Plan struct {
	Mode      index.SourceMode
	Workspace workspace.Workspace
	Probe     workspace.IndexProbe
	// Root is the repository root that the CLI also mines: the root that
	// the index records.
	Root       string
	Repository reviews.Repository
	RepoState  reviews.RepositoryState
	// GHPath is where gh is on PATH; "" when it is missing. It is not
	// proof of a login.
	GHPath string
	Window Window
	// FixWindowDays is the fix-commit window in days. It is not
	// configurable.
	FixWindowDays int
	// Head is the HEAD commit whose history the fix mine reads. It is ""
	// when no commit exists, and for a blocked plan, which reads no git.
	Head string
	// Network is true when the refresh runs gh, which contacts GitHub.
	Network bool
	// Current is the stored evidence before the refresh.
	Current Snapshot
	// Affects discloses what the refresh does to stored data.
	Affects []Effect
	// Blockers lists why the plan cannot run; empty means ready.
	Blockers  []Blocker
	PlannedAt time.Time

	observed evObservation
}

// Ready reports whether the plan has no blockers.
func (p *Plan) Ready() bool {
	return len(p.Blockers) == 0
}

// PlanRefresh previews an evidence refresh in mode. It writes nothing
// and runs no gh. An index that is not ready blocks the plan before any
// git command runs. A malformed config.yaml blocks the plan, so a run
// never spends network work against a default the user did not review.
// A missing GitHub origin or gh is no blocker: the local source still
// runs, and the plan discloses that reviews are unavailable.
func PlanRefresh(ws workspace.Workspace, mode index.SourceMode) (*Plan, error) {
	if mode != index.SourcesLocal && mode != index.SourcesReviewsAndLocal {
		return nil, fmt.Errorf("evidence refresh: unknown source mode %s", mode)
	}

	p := &Plan{
		Mode:          mode,
		Workspace:     ws,
		FixWindowDays: int(fixes.DefaultWindow.Hours() / 24),
		PlannedAt:     time.Now(),
	}

	p.Current = evReadStore(ws)
	p.Probe = p.Current.Probe
	p.Root = p.Probe.Root

	window, configDigest := evReadWindow(p.Root)
	p.Window = window
	p.Current.Window = window

	if p.Probe.State != workspace.IndexReady {
		p.Blockers = append(p.Blockers, evIndexBlocker(p.Probe))
	}

	if window.ConfigErr != nil {
		p.Blockers = append(p.Blockers, Blocker{
			Kind:   BlockConfig,
			Reason: evSafe(window.ConfigErr.Error()),
			Path:   evConfigPath(p.Root),
		})
	}

	// A plan that the index blocks runs no git: the workspace can be a
	// half-built or foreign database, and nothing here can use it. It
	// reads no HEAD and no origin, so it discloses the effects of a run
	// with history and, in the combined mode, with a fetch.
	if p.Probe.State != workspace.IndexReady {
		p.Affects = evAffects(mode, true, mode == index.SourcesReviewsAndLocal)

		return p, nil
	}

	p.Repository, p.RepoState = reviews.ResolveRepository(p.Root)
	p.Current.Repository, p.Current.RepoState = p.Repository, p.RepoState
	p.GHPath = evLookGH()
	p.Current.GHPath = p.GHPath
	p.Network = mode == index.SourcesReviewsAndLocal && p.RepoState == reviews.RepositoryGitHub
	p.observed = evObserve(ws, p.Current, configDigest)
	p.Head = p.observed.head

	// The fix mine and the HEAD read fail together: no repository, no
	// commit, or no git. The run then keeps the stored fix findings.
	p.Affects = evAffects(mode, p.Head != "", p.Network)

	return p, nil
}

// Outcome reports one evidence run.
type Outcome struct {
	// Fixes and Reviews report each source. Their Cause field is the
	// typed cause, for example index.CauseAuth for a gh login problem.
	Fixes   index.SourceOutcome
	Reviews index.SourceOutcome
	// Comments and Kept are the fetched and kept review comment counts.
	Comments int
	Kept     int
	// After is the evidence state that the run left. A blocked run reads
	// it without git or gh, so its repository stays unresolved. A run
	// without a plan has no workspace, so After.CountsErr is set.
	After Snapshot
	// Conflict is non-nil when the plan went stale: before the start,
	// with both sources not attempted, or before a store open, with that
	// source failed. Each refused source carries index.CauseRefused. It
	// wraps ErrConflict.
	Conflict error
	// FinishedAt is the time the run returned.
	FinishedAt time.Time
}

// RunRefresh runs a confirmed plan. It checks the bound observation
// first; any change returns a conflict and starts nothing. Before each
// store open it checks the database again, because the open would
// create a missing database or migrate an older one. A nil fetch uses gh. A stop
// through ctx ends the GitHub fetch, and the stored review corpus then
// stays; the local fix phase always finishes its write.
func RunRefresh(ctx context.Context, p *Plan, fetch reviews.FetchFunc,
	progress func(index.RefreshEvent),
) (Outcome, error) {
	notAttempted := index.SourceOutcome{State: index.SourceNotAttempted}
	out := Outcome{Fixes: notAttempted, Reviews: notAttempted}

	finish := func(err error) (Outcome, error) {
		if errors.Is(err, ErrConflict) {
			out.Conflict = err
		}

		out.After = Inspect(p.Workspace)
		out.FinishedAt = time.Now()

		return out, err
	}

	// Unknown counts must not read as zero counts, so a run without a
	// plan marks them unknown.
	if p == nil {
		err := errors.New("evidence refresh: no plan")
		out.After.CountsErr = err
		out.FinishedAt = time.Now()

		return out, err
	}

	// A blocked plan runs no git and no gh, because its index can be
	// foreign or half built. After is still a fresh read of what stays.
	if !p.Ready() {
		out.After = evReadLocal(p.Workspace)
		out.FinishedAt = time.Now()

		return out, ErrRefreshBlocked
	}

	// A stop before the start ends the run before any work.
	if err := ctx.Err(); err != nil {
		stopped := index.SourceOutcome{
			State: index.SourceStopped, Cause: index.CauseStopped, Reason: "stopped before the start", Err: err,
		}
		out.Fixes, out.Reviews = stopped, stopped

		return finish(nil)
	}

	if err := p.revalidate(); err != nil {
		refused := index.SourceOutcome{
			State: index.SourceNotAttempted, Cause: index.CauseRefused, Reason: evSafe(err.Error()), Err: err,
		}
		out.Fixes, out.Reviews = refused, refused

		return finish(err)
	}

	res, err := index.RefreshSources(ctx, index.RefreshRequest{
		Root:          p.Root,
		DBPath:        p.Workspace.DBPath,
		Mode:          p.Mode,
		WindowDays:    p.Window.Effective,
		Fetch:         fetch,
		Progress:      progress,
		BeforePersist: func(string) error { return p.checkDatabase() },
	})

	out.Fixes, out.Reviews = res.Fixes, res.Reviews
	out.Comments, out.Kept = res.Review.Comments, res.Review.Kept

	return finish(err)
}

// revalidate compares a new observation with the bound one. It names
// every change in one ErrConflict error.
func (p *Plan) revalidate() error {
	now := evReadStore(p.Workspace)
	_, configDigest := evReadWindow(now.Probe.Root)

	if changes := p.observed.diff(evObserve(p.Workspace, now, configDigest)); len(changes) > 0 {
		return fmt.Errorf("%w: %s", ErrConflict, strings.Join(changes, "; "))
	}

	return nil
}

// checkDatabase checks the database before a source opens the store.
// Our own writes change the stamps, so only the database identity and
// state count.
func (p *Plan) checkDatabase() error {
	probe := workspace.Probe(p.Workspace)

	if changes := p.observed.databaseChanges(probe.State, probe.Schema, evStat(p.Workspace.DBPath)); len(changes) > 0 {
		return fmt.Errorf("%w: %s", ErrConflict, strings.Join(changes, "; "))
	}

	return nil
}

// evObservation is the state that a plan binds (decision D-9).
type evObservation struct {
	root      string
	state     workspace.IndexState
	schema    int
	dbFile    fs.FileInfo
	head      string
	origin    string
	config    string
	fixesAt   int64
	reviewsAt int64
}

// evObserve records the bound state. The origin URL and config.yaml are
// kept as digests: a URL can carry credentials, and the plan needs only
// equality.
func evObserve(ws workspace.Workspace, snap Snapshot, configDigest string) evObservation {
	origin, _ := evGit(snap.Probe.Root, "remote", "get-url", reviews.OriginRemote)
	head, _ := evGit(snap.Probe.Root, "rev-parse", "--verify", "--quiet", "HEAD")

	return evObservation{
		root:      snap.Probe.Root,
		state:     snap.Probe.State,
		schema:    snap.Probe.Schema,
		dbFile:    evStat(ws.DBPath),
		head:      head,
		origin:    evDigestString(origin),
		config:    configDigest,
		fixesAt:   snap.FixesMinedAt,
		reviewsAt: snap.ReviewsMinedAt,
	}
}

// diff names each difference between the bound observation o and now.
func (o evObservation) diff(now evObservation) []string {
	var changes []string

	add := func(changed bool, what string) {
		if changed {
			changes = append(changes, what)
		}
	}

	add(o.root != now.root, "the repository root changed")
	changes = append(changes, o.databaseChanges(now.state, now.schema, now.dbFile)...)
	add(o.head != now.head, "HEAD moved")
	add(o.origin != now.origin, "the origin remote changed")
	add(o.config != now.config, ".seamark/config.yaml changed")
	add(o.fixesAt != now.fixesAt || o.reviewsAt != now.reviewsAt, "another refresh changed the stored evidence")

	return changes
}

// databaseChanges names each change of the index database from the
// bound observation o. A removed file is one change: its state, schema,
// and identity changes follow from it and only add noise.
func (o evObservation) databaseChanges(state workspace.IndexState, schema int, file fs.FileInfo) []string {
	if state == workspace.IndexAbsent && o.state != workspace.IndexAbsent {
		return []string{"the index database file was removed"}
	}

	var changes []string

	if state != o.state {
		changes = append(changes, "the index database is now "+state.String())
	}

	if schema != o.schema {
		changes = append(changes, "the index schema changed")
	}

	if !evSameFile(o.dbFile, file) {
		changes = append(changes, "the index database file was replaced")
	}

	return changes
}

// evReadStore reads the stored evidence through a read-only open. It
// never creates or migrates a database.
func evReadStore(ws workspace.Workspace) Snapshot {
	snap := Snapshot{ObservedAt: time.Now()}

	st, probe, err := workspace.OpenReadOnly(ws)
	snap.Probe = probe

	if err != nil {
		snap.CountsErr = err

		return snap
	}
	defer func() { _ = st.Close() }() // a read-only snapshot holds no lock

	counts, err := st.FindingCounts()
	if err != nil {
		snap.CountsErr = err

		return snap
	}

	stats, err := st.Stats()
	if err != nil {
		snap.CountsErr = err

		return snap
	}

	distilled, err := st.DistilledCount()
	if err != nil {
		snap.CountsErr = err

		return snap
	}

	snap.Findings = counts
	snap.DistilledGroups = distilled
	snap.Lessons = stats.Lessons
	snap.ReviewFindings = counts[model.SourceReview]

	for _, source := range model.FixMinedSources() {
		snap.FixFindings += counts[source]
	}

	snap.FixesMinedAt = evMetaUnix(st, store.MetaFixesMinedAt)
	snap.ReviewsMinedAt = evMetaUnix(st, store.MetaReviewsMinedAt)

	return snap
}

// evReadWindow reads the review window from config.yaml under root. It
// also returns the digest of the bytes it parsed, so a plan binds the
// same bytes that it shows; "" means that the file is absent.
func evReadWindow(root string) (w Window, configDigest string) {
	w.Floor = reviews.CountFloor

	data, err := os.ReadFile(evConfigPath(root))

	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = nil
	case err != nil:
		w.ConfigErr = err

		return w, "unreadable"
	}

	if data != nil {
		configDigest = evDigest(data)
	}

	cfg, err := index.ParseConfig(data)
	if err != nil {
		w.ConfigErr = err

		return w, configDigest
	}

	w.Stored = cfg.Reviews.WindowDays

	switch {
	case w.Stored == nil:
		w.Effective = reviews.DefaultReviewWindowDays
	case *w.Stored <= 0:
		// 0 is the documented unlimited value. The miner also treats a
		// negative value as unlimited, so the plan says what runs.
		w.Effective = -1
	default:
		w.Effective = *w.Stored
	}

	return w, configDigest
}

// evAffects lists the stored-data effects of a refresh in mode. history
// tells whether HEAD names a commit. Without a commit the fix mine reads
// unavailable, so the refresh keeps the stored fix findings. fetch tells
// whether the review fetch can run: the combined mode and a GitHub
// origin. Without a fetch the refresh keeps the stored review corpus.
func evAffects(mode index.SourceMode, history, fetch bool) []Effect {
	fixEffect := Effect{
		Source: index.SourceFixes, Kind: EffectReplace,
		Text: "replaces the stored fix-commit findings with a new mine of the history " +
			"reachable from HEAD, and records the mine time",
	}

	if !history {
		fixEffect = Effect{
			Source: index.SourceFixes, Kind: EffectKeep,
			Text: "keeps the stored fix-commit findings: no commit is reachable from HEAD, " +
				"so the local fix source reads unavailable",
		}
	}

	effects := []Effect{fixEffect}

	if mode == index.SourcesReviewsAndLocal {
		effects = append(effects, Effect{
			Source: index.SourceReviews, Kind: EffectReplace,
			Text: "replaces all stored review lessons and review findings after a successful " +
				"GitHub fetch, and records the mine time; a fetch that fails, is unavailable, " +
				"or stops keeps them",
		})
	} else {
		effects = append(effects, Effect{
			Source: index.SourceReviews, Kind: EffectKeep,
			Text: "keeps the stored review lessons and review findings",
		})
	}

	effects = append(effects,
		Effect{Kind: EffectKeep, Text: "keeps proposals, pins, and the record of distilled evidence groups"})

	// A refresh without history and without a fetch changes no evidence,
	// so no later distillation sees a change from it. The plan binds HEAD
	// and the origin, so neither fact can change before the run starts.
	if history || fetch {
		effects = append(effects, Effect{Kind: EffectLater, Text: "the next distillation sees the changed " +
			"evidence groups; its run can remove pending proposals whose evidence is gone"})
	}

	// Only a fetch mines review lessons. The stored lessons reach agents
	// already, so a refresh without a fetch adds no later review effect.
	if fetch {
		effects = append(effects, Effect{
			Source: index.SourceReviews, Kind: EffectLater,
			Text: "mined review lessons can reach agents through hooks and MCP",
		})
	}

	return effects
}

// evIndexBlocker explains an index that is not ready.
func evIndexBlocker(probe workspace.IndexProbe) Blocker {
	reason := "the index database is " + probe.State.String()

	switch {
	case probe.Err != nil:
		reason = probe.Err.Error()
	case probe.State == workspace.IndexAbsent || probe.State == workspace.IndexNotIndexed:
		reason += ": build the index first"
	}

	return Blocker{Kind: BlockIndex, Reason: evSafe(reason), Path: probe.Path}
}

// evConfigPath is the config.yaml path under root.
func evConfigPath(root string) string {
	return filepath.Join(root, ".seamark", "config.yaml")
}

// evLookGH finds gh on PATH without running it. Running gh could reach
// the network or start a login.
func evLookGH() string {
	path, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}

	return path
}

// evGit runs one local git command in root and returns its trimmed
// output.
func evGit(root string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return "", false
	}

	return strings.TrimSpace(string(out)), true
}

// evStat returns the file info of path, or nil when stat fails.
func evStat(path string) fs.FileInfo {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	return info
}

// evSameFile reports whether a and b describe the same file. Two
// missing files count as the same.
func evSameFile(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return os.SameFile(a, b)
}

// evDigest returns the hex SHA-256 of data.
func evDigest(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// evDigestString is evDigest for a string; "" stays "".
func evDigestString(s string) string {
	if s == "" {
		return ""
	}

	return evDigest([]byte(s))
}

// evMetaUnix reads a Unix-seconds meta value; 0 when absent or malformed.
func evMetaUnix(st *store.Store, key string) int64 {
	v, err := st.GetMeta(key)
	if err != nil {
		return 0
	}

	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}

	return n
}

// evSafe makes untrusted text safe for one display line. Reader and
// store errors can quote tokens and escape sequences.
func evSafe(s string) string {
	return render.SanitizeBlock(strings.Join(strings.Fields(redact.Secrets(s)), " "))
}
