// Package index orchestrates a full indexing pass: walk the workspace,
// parse supported files, resolve call references into edges, mine git
// history, and write everything into the store atomically.
package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/fixes"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/parse"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// Options configures one indexing run.
type Options struct {
	// Root is the workspace directory. When inside a git repository it is
	// widened to the repository toplevel so paths are repo-relative.
	Root string
	// DBPath overrides the index location (default: <root>/.seamark/index.db).
	DBPath string
	// History tunes the git mining pass.
	History history.Options
	// Force rebuilds even when the workspace fingerprint says the index
	// is already current. It does not pass the guard of MoveRoot.
	Force bool
	// MoveRoot allows a build to record this workspace in a database that
	// records another root. That repository then loses its pins and its
	// maintenance, so the default is a refusal (ErrRootMove).
	// The run never skips, because a skip would keep the old root. A root
	// that does not read stays refused. Only an explicit request of the
	// user sets it.
	MoveRoot bool
	// Logf receives progress and warnings; nil discards them.
	Logf func(format string, args ...any)
	// Progress receives phase events for interactive surfaces: a phase
	// name with units done/total (0 total = indeterminate, reported
	// again as 1/1 when the phase ends). Nil discards — piped and agent
	// surfaces stay on Logf's plain lines.
	Progress func(phase string, done, total int)
}

// Summary reports what a run produced.
type Summary struct {
	Root   string
	DBPath string
	// Skipped is true when the workspace fingerprint matched the index
	// and nothing was rebuilt.
	Skipped bool
	// ReadOnly is true on a skip of a file that does not accept writes. A
	// rebuild there fails, so no surface may offer one.
	ReadOnly bool
	// RootRoute names the next step of a rebuild after a skip under a
	// database of another repository, as for a sibling worktree at one
	// commit. Root then names that repository, and Force would refuse.
	// It is "" for every other run.
	RootRoute string
	// Ownership holds the facts of the database that a skip read. A
	// surface uses them to say which repository answers, without a second
	// probe. It is zero after a build, because a build records this
	// workspace.
	Ownership ownership.State
	// FilesSeen counts the listed files that exist in the working tree.
	// A deleted path and a path outside a sparse checkout are not in it,
	// because no file exists to see.
	FilesSeen   int
	FilesParsed int // files a language extractor handled
	// FilesReparsed counts files actually run through tree-sitter this
	// pass; the rest were served from the parse cache. Equals FilesParsed
	// on a --force or first index.
	FilesReparsed int
	// FilesSkipped counts supported files dropped by config — generated
	// (default) or an exclude glob.
	FilesSkipped int
	// FilesDeleted counts the tracked paths that git status shows as
	// deleted and not staged, also a file that a directory replaces.
	// They are not parse errors, because no bytes exist to parse. A path
	// outside a sparse checkout is not deleted, as in git status. Outside
	// git it is always 0.
	FilesDeleted int
	ParseErrors  int
	HistoryMined bool
	// HistorySkipNote says why the history layer is absent when
	// HistoryMined is false: "not a git repository" and "mining failed"
	// are different situations and must not be conflated in output.
	HistorySkipNote string
	Stats           store.Stats
	Duration        time.Duration
}

// ErrRootMove matches each refusal of Run under the guard of MoveRoot.
// The refusal writes nothing. A surface without the flag can answer from
// the existing index, but it must not open that index with a migration.
var ErrRootMove = errors.New("a rebuild must not move the recorded root of another repository")

// rootMoveRefusal is one refusal of Run. Its text has the facts and the
// route, and it matches ErrRootMove. It keeps the facts, so a surface
// that answers from the old index can name its root without a second
// probe.
type rootMoveRefusal struct {
	text  string
	state ownership.State
}

// Error returns the text of the refusal.
func (r rootMoveRefusal) Error() string { return r.text }

// Unwrap makes the refusal match ErrRootMove.
func (r rootMoveRefusal) Unwrap() error { return ErrRootMove }

// RefusedState returns the facts of the database that a refusal of Run
// read. ok is false for any other error.
func RefusedState(err error) (state ownership.State, ok bool) {
	var r rootMoveRefusal
	if !errors.As(err, &r) {
		return ownership.State{}, false
	}

	return r.state, true
}

// buildRefusal returns the refusal of a build in this state, or nil. Its
// text has the facts and the route of the decision.
func buildRefusal(s ownership.State, d ownership.Decision) error {
	if d.Build != ownership.BuildRefused {
		return nil
	}

	there, here := render.Sanitize(s.Recorded), render.Sanitize(s.Workspace)
	route := s.Route(d.Route, ownership.SurfaceBuild)
	refuse := func(text string) error { return rootMoveRefusal{text: text, state: s} }

	switch {
	case s.DirRefusesWrites():
		// No read works in this state, so "run it again" would not help.
		// The text names the directory instead.
		return refuse(s.ReadOnlyDirCause() + "; nothing changed; " + route)
	case s.Root == ownership.RootUnreadable:
		return refuse(fmt.Sprintf("the recorded root of this database cannot be read (%s: %s), so a "+
			"rebuild could move the root of another repository; nothing changed; run it again, %s",
			render.Sanitize(s.DB), render.Sanitize(s.ReadErr.Error()), route))
	case s.Root == ownership.RootGone:
		return refuse(fmt.Sprintf("this database records a directory that does not exist now (%s), which "+
			"can be a moved repository or a detached disk, so a rebuild in %s would replace that root; "+
			"nothing changed; %s", there, here, route))
	case s.Owner == ownership.OwnerWorkspace:
		return refuse(fmt.Sprintf("this database lies in %s but records another repository (%s), so a "+
			"rebuild here would move its recorded root; nothing changed; %s", here, there, route))
	default:
		return refuse(fmt.Sprintf("this database records another repository (%s), so a rebuild in %s would "+
			"move its recorded root, and that repository would lose its pins and maintenance; nothing changed; %s",
			there, here, route))
	}
}

// afterRead runs after the read loop and before the write when it is
// not nil. Only tests set it: it is the one point where a test can change
// a file after its read and before the run observes the workspace again.
var afterRead func()

// changedDuringRun is the workspace state that a run records when the
// workspace changed between its two observations. The graph can then
// hold content that no workspace state describes, also after the files go
// back to their first content. WorkspaceState returns hex digits or "",
// so this value never equals a current state. Every reader then calls the
// index stale, and the next run rebuilds.
const changedDuringRun = "changed"

// Run executes a full indexing pass.
func Run(opts Options) (*Summary, error) {
	start := time.Now()
	logf := opts.Logf

	if logf == nil {
		logf = func(string, ...any) {}
	}

	root, err := ResolveRoot(opts.Root)
	if err != nil {
		return nil, err
	}

	// The guard and each open use this one path, so they judge one file.
	dbPath := store.AbsolutePath(opts.DBPath)
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	// The guard runs before the fast path, because the open of the fast
	// path migrates an older schema, and a refusal must write nothing.
	state := ownership.Gather(root, dbPath)
	state.MoveRoot = opts.MoveRoot
	decision := ownership.Decide(state.Facts)
	refusal := buildRefusal(state, decision)

	// The workspace state is observed before any file is read, and the
	// run records this state when the workspace still has it after the
	// reads. A state observed only after the reads would mark an older
	// parse as current. The fast path compares the same observation.
	observed := WorkspaceState(root, dbPath)

	// Fast path: an unchanged workspace needs no work. This makes
	// "reindex whenever unsure" free (~30ms) for the CLI, and no-change
	// saves free for the LSP server.
	//
	// A skip moves nothing, so it stays under the guard, as for a sibling
	// worktree at one commit. There it opens read-only, so an older schema
	// stays as it is and the run refuses. A run with MoveRoot asks for the
	// move, so a skip would report success and move nothing.
	switch {
	case refusal != nil:
		if !opts.Force && !opts.MoveRoot {
			if sum := freshSummary(root, dbPath, observed, start, store.OpenReadOnly, state, decision); sum != nil {
				sum.Ownership = state

				if sum.Root != "" && !store.SameDirectory(sum.Root, root) {
					sum.RootRoute = state.Route(decision.Route, ownership.SurfaceBuild)
				}

				return sum, nil
			}
		}

		return nil, refusal
	case !opts.Force && !opts.MoveRoot:
		if sum := freshSummary(root, dbPath, observed, start, store.Open, state, decision); sum != nil {
			sum.Ownership = state

			return sum, nil
		}
	}

	files, deleted, present, err := listFiles(root)
	if err != nil {
		return nil, err
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }() // read results are already committed

	// Parse cache: an unchanged file reuses its stored FileResult instead
	// of re-running tree-sitter (~60% of index time). --force ignores it;
	// a version bump or a decode failure falls back to a fresh parse.
	cache := loadParseCache(st, opts.Force, logf)

	cfg, err := LoadConfig(root)
	if err != nil {
		return nil, err
	}

	registry, err := parse.NewRegistry()
	if err != nil {
		return nil, err
	}

	defer registry.Close()

	progress := opts.Progress
	if progress == nil {
		progress = func(string, int, int) {}
	}

	supported := 0

	for _, rel := range files {
		if registry.ForPath(rel) != nil {
			supported++
		}
	}

	progress("scan", len(files), len(files))

	sum := &Summary{Root: root, DBPath: dbPath, FilesSeen: len(files), FilesDeleted: len(deleted)}

	var results []*parse.FileResult

	upserts := map[string]store.CacheEntry{} // reparsed files to persist
	keep := map[string]bool{}                // files still present (prune the rest)

	handled := 0

	for _, rel := range files {
		ex := registry.ForPath(rel)
		if ex == nil {
			continue
		}

		handled++
		progress("parse", handled, supported)

		// A config exclude glob is a path-only decision — skip before
		// even reading the file.
		if cfg.excluded(rel) {
			sum.FilesSkipped++
			continue
		}

		// Every read failure stays a parse error, also a missing file.
		// Only the git listing proves a deletion. A file can vanish
		// during the run, or be a link to a missing target.
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			logf("warn: read %s: %v", rel, err)
			sum.ParseErrors++
			continue
		}

		// Generated code is boilerplate that inflates the graph and
		// pollutes most-called; skip it unless the workspace opts in.
		if !cfg.Index.Generated && isGenerated(src) {
			sum.FilesSkipped++
			continue
		}

		keep[rel] = true
		hash := hashBytes(src)

		// Reuse the cached parse when the bytes are identical.
		if ent, ok := cache[rel]; ok && ent.Hash == hash {
			if res, err := decodeResult(ent.Data); err == nil {
				results = append(results, res)
				sum.FilesParsed++
				continue
			}
			// A corrupt or incompatible blob just means reparse below.
		}

		res, err := ex.Extract(rel, src)
		if err != nil {
			logf("warn: parse %s: %v", rel, err)
			sum.ParseErrors++
			continue
		}

		results = append(results, res)
		sum.FilesParsed++
		sum.FilesReparsed++

		if data, err := encodeResult(res); err == nil {
			upserts[rel] = store.CacheEntry{Hash: hash, Data: data}
		}
	}

	if afterRead != nil {
		afterRead()
	}

	// History is best-effort: a non-git workspace still gets a structure graph.
	var decisions []model.Decision
	var pairs []model.CoChange

	if history.IsRepo(root) {
		progress("history", 0, 0)
		decisions, pairs, err = history.Mine(root, opts.History)
		progress("history", 1, 1)
		if err != nil {
			logf("warn: history mining failed: %v", err)

			sum.HistorySkipNote = "history mining failed, see warnings"
		} else {
			sum.HistoryMined = true
		}
	} else {
		logf("note: %s is not a git repository; skipping history layer", root)

		sum.HistorySkipNote = "not a git repository"
	}

	catalog, err := effects.Load(root)
	if err != nil {
		return nil, err
	}

	// The file-coverage summary is persisted so `seamark status` can
	// report parser coverage long after this run's stdout is gone. A
	// safety answer without coverage context is uninterpretable.
	coverage, err := json.Marshal(map[string]int{
		"files_seen":    sum.FilesSeen,
		"files_parsed":  sum.FilesParsed,
		"files_skipped": sum.FilesSkipped,
		"files_deleted": sum.FilesDeleted,
		"parse_errors":  sum.ParseErrors,
	})
	if err != nil {
		return nil, err
	}

	progress("write", 0, 0)

	g := buildGraph(results, readGoModules(root, files), catalog)

	// The second observation comes after the last read of the workspace,
	// the go.mod files in buildGraph. When the state differs, the reads can
	// hold content that the first state does not describe. So the run
	// records the mark that matches no state. A change after this point
	// does no harm: the graph matches the recorded state, and the next run
	// sees the change. The git calls run outside the transaction, so they
	// do not hold the write lock.
	recorded := observed
	changed := observed != "" && WorkspaceState(root, dbPath) != observed

	if changed {
		recorded = changedDuringRun
	}

	err = st.Rebuild(func(tx *store.Tx) error {
		if err := g.write(tx); err != nil {
			return err
		}

		for i := range decisions {
			if err := tx.InsertDecision(&decisions[i]); err != nil {
				return err
			}
		}

		// Every mined pair is stored, also a pair with a gone side. An
		// agent that deletes a file still needs its live partners.
		for _, p := range pairs {
			if err := tx.InsertCoChange(p); err != nil {
				return err
			}
		}

		// The gone list is written in the same transaction as the pairs,
		// so the reads that leave out gone partners always match them.
		if err := tx.SetGoneFiles(goneFiles(pairs, present)); err != nil {
			return err
		}

		// Persist the parse cache in the same transaction as the graph, so
		// the two never disagree about which file versions were indexed.
		for rel, ent := range upserts {
			if err := tx.UpsertParseCache(rel, ent.Hash, ent.Data); err != nil {
				return err
			}
		}

		if err := tx.PruneParseCache(keep); err != nil {
			return err
		}

		// The meta commits with the graph. A meta write after the commit
		// could pair this graph with the workspace state of a concurrent
		// run, and a newer state on an older graph reads as current.
		for k, v := range map[string]string{
			store.MetaRepoRoot:          root,
			store.MetaIndexedAt:         fmt.Sprint(time.Now().Unix()),
			store.MetaIndexedState:      recorded,
			store.MetaHistoryMined:      fmt.Sprint(sum.HistoryMined),
			store.MetaParseCacheVersion: parseCacheVersion,
			store.MetaIndexSummary:      string(coverage),
		} {
			if err := tx.SetMeta(k, v); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// The mark is committed now, so the claim of the note is true. The
	// note says why the next run is not skipped.
	if changed {
		logf("note: the workspace changed during indexing; the next run rebuilds")
	}

	if sum.Stats, err = st.Stats(); err != nil {
		return nil, err
	}

	progress("write", 1, 1)

	sum.Duration = time.Since(start)

	return sum, nil
}

// parseCacheVersion guards the on-disk parse cache. Bump it whenever the
// extractors or the FileResult layout change in a way that makes old
// cached blobs wrong (a decode failure already forces a reparse; this
// skips loading a large stale cache in the first place).
//
// Version "2": the Go extractor records the operand root of a call. gob
// decodes an old blob with an empty root, so an old cache would skip the
// rule that drops calls on values of packages outside the repository.
//
// Version "3": package-level var initializers (Go) and module-level value
// bindings (JS/TS, Python) carry the calls inside them, and a JS/TS or
// Python class carries the calls of its body outside its methods. An old
// blob decodes with no calls on these symbols, so their edges and effects
// would stay missing. freshSummary rebuilds an index of another version.
const parseCacheVersion = "3"

// OtherVersion reports an index that another seamark version built: the
// parse cache version of its last run is not the one of this binary, so
// the index can lack facts that this binary records. The readers that
// never rebuild ask it, so such an index does not read as current. A
// database with a completed run and no stored version predates the
// version, so it counts as another version. A database that no run
// completed has nothing to compare. A current index costs one meta read.
func OtherVersion(st *store.Store) (bool, error) {
	v, err := st.GetMeta(store.MetaParseCacheVersion)
	if err != nil || v == parseCacheVersion {
		return false, err
	}

	if v != "" {
		return true, nil
	}

	indexedAt, err := st.GetMeta(store.MetaIndexedAt)
	if err != nil {
		return false, err
	}

	return indexedAt != "", nil
}

// loadParseCache returns the stored per-file parse cache, or an empty map
// when forced, version-stale, or unreadable — every one of which just
// means "reparse everything", never a failure.
func loadParseCache(st *store.Store, force bool, logf func(string, ...any)) map[string]store.CacheEntry {
	empty := map[string]store.CacheEntry{}

	if force {
		return empty
	}

	if v, err := st.GetMeta(store.MetaParseCacheVersion); err != nil || v != parseCacheVersion {
		return empty
	}

	cache, err := st.LoadParseCache()
	if err != nil {
		logf("warn: parse cache unreadable, reparsing: %v", err)

		return empty
	}

	return cache
}

// hashBytes returns the hex sha256 of a file's bytes — the parse cache key.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

// encodeResult / decodeResult (de)serialize a FileResult for the cache.
// gob is tolerant of added/removed struct fields, so minor model changes
// degrade to a reparse rather than corruption.
func encodeResult(r *parse.FileResult) ([]byte, error) {
	var b bytes.Buffer

	if err := gob.NewEncoder(&b).Encode(r); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

func decodeResult(data []byte) (*parse.FileResult, error) {
	var r parse.FileResult

	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&r); err != nil {
		return nil, err
	}

	return &r, nil
}

// SourceMode selects the lesson sources that one evidence refresh
// mines. The two values are exactly the two CLI modes, so the console
// offers no source combination that the CLI lacks.
type SourceMode int

const (
	// SourcesLocal mines only local fix commits (`index --fixes-only`).
	SourcesLocal SourceMode = iota
	// SourcesReviewsAndLocal mines local fix commits first and GitHub
	// review comments second (`index --reviews`).
	SourcesReviewsAndLocal
)

// String returns a stable lower-case name for the mode.
func (m SourceMode) String() string {
	switch m {
	case SourcesLocal:
		return "local"
	case SourcesReviewsAndLocal:
		return "reviews-and-local"
	default:
		return "SourceMode(" + strconv.Itoa(int(m)) + ")"
	}
}

// Source names. BeforePersist and RefreshEvent use them.
const (
	SourceFixes   = "fixes"
	SourceReviews = "reviews"
)

// SourceState is the typed outcome of one source in one refresh.
type SourceState string

const (
	// SourceRefreshed means the source replaced its stored corpus with a
	// non-empty result and stamped the mine time.
	SourceRefreshed SourceState = "refreshed"
	// SourceEmpty means an authoritative empty result replaced the
	// stored corpus and stamped the mine time.
	SourceEmpty SourceState = "successful-empty"
	// SourceUnavailable means the source could not run, for example no
	// git history, no GitHub origin, or no gh. The stored corpus stays.
	SourceUnavailable SourceState = "unavailable"
	// SourceFailed means a fetch, parse, or write error. The stored
	// corpus stays.
	SourceFailed SourceState = "failed"
	// SourceStopped means a stop request ended the source before its
	// write. The stored corpus stays.
	SourceStopped SourceState = "stopped"
	// SourceNotAttempted means the refresh did not start the source.
	SourceNotAttempted SourceState = "not-attempted"
)

// SourceCause is the typed cause of a source state. The console picks
// its guidance from it, for example a gh login for CauseAuth. Reason is
// display text and not a contract, so callers never parse it.
type SourceCause int

const (
	// CauseNone means that no cause applies: the source refreshed, stored
	// an empty result, or did not start.
	CauseNone SourceCause = iota
	// CauseNoHistory means that the fix mine found no git repository, no
	// commit, or no git.
	CauseNoHistory
	// CauseMineFailed means that the local fix mine returned an error.
	CauseMineFailed
	// CauseNoRepository means that origin names no GitHub repository.
	CauseNoRepository
	// CauseGHMissing means that the gh executable is not on PATH.
	CauseGHMissing
	// CauseAuth means that gh reported an authentication problem: no
	// login, a bad or expired token, or a token without the needed access.
	CauseAuth
	// CauseRateLimited means that a GitHub rate limit refused the fetch.
	// A later refresh can succeed without a new login.
	CauseRateLimited
	// CauseFetchFailed means any other fetch error, for example a network
	// error, a server error, or an unknown repository.
	CauseFetchFailed
	// CauseUnreadable means that the fetched reply is not the API JSON.
	CauseUnreadable
	// CauseStopped means that a stop request ended the source.
	CauseStopped
	// CauseStore means that the index database did not open or did not
	// accept the write.
	CauseStore
	// CauseRefused means that the caller's check refused the work, for
	// example because the workspace changed after the preview.
	CauseRefused
)

// String returns a stable lower-case name for the cause.
func (c SourceCause) String() string {
	switch c {
	case CauseNone:
		return "none"
	case CauseNoHistory:
		return "no-history"
	case CauseMineFailed:
		return "mine-failed"
	case CauseNoRepository:
		return "no-repository"
	case CauseGHMissing:
		return "gh-missing"
	case CauseAuth:
		return "auth"
	case CauseRateLimited:
		return "rate-limited"
	case CauseFetchFailed:
		return "fetch-failed"
	case CauseUnreadable:
		return "unreadable"
	case CauseStopped:
		return "stopped"
	case CauseStore:
		return "store"
	case CauseRefused:
		return "refused"
	default:
		return "SourceCause(" + strconv.Itoa(int(c)) + ")"
	}
}

// SourceOutcome reports what one refresh did to one source.
type SourceOutcome struct {
	State SourceState
	// Cause is the typed cause of a state other than refreshed and
	// successful-empty; CauseNone otherwise.
	Cause SourceCause
	// Stored counts the findings that this refresh stored for the source.
	Stored int
	// Lessons counts the stored review lessons; it is 0 for fixes.
	Lessons int
	// MinedAt is the mine stamp that this refresh wrote, in Unix
	// seconds; 0 when it wrote none.
	MinedAt int64
	// Reason explains a state other than refreshed in one safe line:
	// control characters are removed and secrets are redacted.
	Reason string
	// Err is the cause, or nil. It is for errors.Is, not for display.
	Err error
}

// RefreshPhase names one step of an evidence refresh for typed progress.
type RefreshPhase int

const (
	// PhaseMineFixes means the local fix mine starts. It has no progress
	// count and cannot stop part way.
	PhaseMineFixes RefreshPhase = iota + 1
	// PhaseFetchReviews means the GitHub fetch starts.
	PhaseFetchReviews
	// PhaseFetchBytes reports the fetched bytes so far. No total exists.
	PhaseFetchBytes
	// PhaseFetched reports the parsed and kept comment counts.
	PhaseFetched
	// PhaseCluster means the fetched comments are clustered.
	PhaseCluster
	// PhasePersist means a source write starts.
	PhasePersist
	// PhaseSourceDone reports the final state of one source.
	PhaseSourceDone
)

// RefreshEvent is one typed progress event of an evidence refresh.
type RefreshEvent struct {
	Phase  RefreshPhase
	Source string
	// Repository is the "owner/name" slug for the review phases.
	Repository string
	// Bytes is the fetched byte count for PhaseFetchBytes.
	Bytes int
	// Comments and Kept carry the counts for PhaseFetched.
	Comments int
	Kept     int
	// State is the source state for PhaseSourceDone.
	State SourceState
}

// RefreshRequest configures one evidence refresh.
type RefreshRequest struct {
	// Root is the workspace directory; it widens to the git top level.
	Root string
	// DBPath overrides the index location (default: <root>/.seamark/index.db).
	DBPath string
	// Mode selects the sources.
	Mode SourceMode
	// WindowDays is the review window for reviews.Options. The caller
	// resolves it, because the CLI falls back to the default on a bad
	// config file and the console blocks instead.
	WindowDays int
	// Fetch is the review fetcher; nil means gh.
	Fetch reviews.FetchFunc
	// Logf receives the CLI progress lines; nil discards them.
	Logf func(format string, args ...any)
	// Progress receives typed events; nil discards them.
	Progress func(RefreshEvent)
	// BeforePersist runs before each source opens the store, with the
	// source name. The console re-probes the database there. It runs
	// before the open and not only before the write, because store.Open
	// creates a missing database and migrates an older one. The fix
	// source runs it for an unmined result too, because its store also
	// opens then. An error aborts the source before the open: the source
	// reads stopped for a context error and failed otherwise, and the
	// refresh returns the error.
	BeforePersist func(source string) error
}

// RefreshResult reports one evidence refresh.
type RefreshResult struct {
	Fixes   SourceOutcome
	Reviews SourceOutcome
	// Review is the raw review mining result. The CLI prints from it.
	Review reviews.Result
	// FixCount is the fix finding count that the CLI prints.
	FixCount int
}

// RefreshSources mines and persists the lesson sources that req.Mode
// selects. The order is fixed: mine the fixes, run BeforePersist, open
// the store, store the fixes, close the store, fetch the reviews with
// ctx, run BeforePersist, reopen the store, and replace the review
// corpus. The fix phase runs to its end even
// after a stop, because the fix mine cannot stop part way; the refresh
// then stops before any GitHub work. No store handle stays open across
// the fetch. The error is non-nil only where RefreshFixes and
// RefreshReviews have always returned one, plus a BeforePersist error.
func RefreshSources(ctx context.Context, req RefreshRequest) (RefreshResult, error) {
	return refreshSources(ctx, req, func(string) int { return req.WindowDays })
}

// refreshSources is RefreshSources with a lazy review window that takes
// the resolved root. The RefreshReviews wrapper resolves the window only
// after the fix phase, so its config log line keeps its place in the
// CLI output.
func refreshSources(ctx context.Context, req RefreshRequest, window func(root string) int) (RefreshResult, error) {
	res := RefreshResult{
		Fixes:   SourceOutcome{State: SourceNotAttempted},
		Reviews: SourceOutcome{State: SourceNotAttempted},
	}

	logf := req.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	progress := req.Progress
	if progress == nil {
		progress = func(RefreshEvent) {}
	}

	root, err := ResolveRoot(req.Root)
	if err != nil {
		return res, err
	}

	dbPath := req.DBPath
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	if err := refreshFixPhase(root, dbPath, req, logf, progress, &res); err != nil {
		return res, err
	}

	if req.Mode != SourcesReviewsAndLocal {
		return res, nil
	}

	// A stop that arrived during the local phase ends the refresh here,
	// before any network use.
	if err := ctx.Err(); err != nil {
		res.Reviews = SourceOutcome{
			State: SourceStopped, Cause: CauseStopped, Reason: "stopped before the GitHub fetch", Err: err,
		}
		progress(RefreshEvent{Phase: PhaseSourceDone, Source: SourceReviews, State: SourceStopped})

		return res, nil
	}

	err = refreshReviewPhase(ctx, root, dbPath, window(root), req, logf, progress, &res)

	return res, err
}

// refreshFixPhase mines the fix commits and stores them. A mine error is
// an error in local mode and a warning in the combined mode, as the two
// CLI modes have always done. The store opens even for an unmined
// result, so a database that cannot open stops the refresh before any
// network use.
func refreshFixPhase(root, dbPath string, req RefreshRequest, logf func(string, ...any),
	progress func(RefreshEvent), res *RefreshResult,
) error {
	done := func(o SourceOutcome) {
		res.Fixes = o
		progress(RefreshEvent{Phase: PhaseSourceDone, Source: SourceFixes, State: o.State})
	}

	progress(RefreshEvent{Phase: PhaseMineFixes, Source: SourceFixes})

	mined, mineErr := fixes.Mine(root, fixes.Options{Logf: logf})
	if mineErr != nil {
		if req.Mode == SourcesLocal {
			done(failedSource(CauseMineFailed, mineErr))

			return mineErr
		}

		logf("warn: fix mining failed: %v", mineErr)
	}

	// The fix write records whether origin names a GitHub repository.
	// Both index modes run this phase, so both keep the record current.
	// ResolveRepository runs local git only, so local mode stays offline.
	noReviewRepository := false
	if mineErr == nil && mined.Mined {
		_, repoState := reviews.ResolveRepository(root)
		noReviewRepository = repoState != reviews.RepositoryGitHub
	}

	// The hook runs before the open, because the open can create or
	// migrate the database that the hook guards.
	if err := runBeforePersist(req, SourceFixes); err != nil {
		done(abortedSource(err))

		return err
	}

	st, err := store.Open(dbPath)
	if err != nil {
		done(failedSource(CauseStore, err))

		return err
	}
	defer func() { _ = st.Close() }() // the fix write commits or rolls back itself

	switch {
	case mineErr != nil:
		done(failedSource(CauseMineFailed, mineErr))

		return nil
	case !mined.Mined:
		done(SourceOutcome{
			State:  SourceUnavailable,
			Cause:  CauseNoHistory,
			Reason: "local git history is unavailable: no repository, no commits, or no git",
		})

		return nil
	}

	progress(RefreshEvent{Phase: PhasePersist, Source: SourceFixes})

	count, err := persistFixes(st, mined, noReviewRepository)
	if err != nil {
		done(failedSource(CauseStore, err))

		return err
	}

	res.FixCount = count

	state := SourceRefreshed
	if count == 0 {
		state = SourceEmpty
	}

	done(SourceOutcome{State: state, Stored: count, MinedAt: st.MetaUnix(store.MetaFixesMinedAt)})

	return nil
}

// refreshReviewPhase fetches the review comments and replaces the review
// corpus after a successful fetch. It opens the store only after the
// fetch, so no database handle waits on the network.
func refreshReviewPhase(ctx context.Context, root, dbPath string, windowDays int, req RefreshRequest,
	logf func(string, ...any), progress func(RefreshEvent), res *RefreshResult,
) error {
	done := func(o SourceOutcome) {
		res.Reviews = o
		progress(RefreshEvent{Phase: PhaseSourceDone, Source: SourceReviews, State: o.State})
	}

	mined, err := reviews.MineContext(ctx, root, reviews.Options{
		Logf: logf, WindowDays: windowDays, Progress: fetchProgress(progress),
	}, req.Fetch)
	res.Review = mined

	if err != nil {
		done(failedSource(CauseFetchFailed, err))

		return err
	}

	if mined.Note != "" {
		logf("note: %s", mined.Note)
	}

	// Reviews swap only when the fetch actually succeeded. A transient
	// failure (offline, gh logged out, not a GitHub checkout) must never
	// wipe previously-mined lessons.
	if !mined.Fetched {
		done(degradedReviews(mined))

		return nil
	}

	// The last stop check before the write: a stop after the fetch keeps
	// the stored corpus. From here the write runs to its commit.
	if err := ctx.Err(); err != nil {
		done(SourceOutcome{
			State: SourceStopped, Cause: CauseStopped,
			Reason: "stopped before the review corpus was replaced", Err: err,
		})

		return nil
	}

	// The hook runs before the open, because the open can create or
	// migrate the database that the hook guards. The fetch can take
	// minutes, so the database can change during it.
	if err := runBeforePersist(req, SourceReviews); err != nil {
		done(abortedSource(err))

		return err
	}

	st, err := store.Open(dbPath)
	if err != nil {
		done(failedSource(CauseStore, err))

		return err
	}
	defer func() { _ = st.Close() }() // ReplaceLessons commits or rolls back itself

	progress(RefreshEvent{Phase: PhasePersist, Source: SourceReviews})

	if err := st.ReplaceLessons(mined.Lessons, mined.Findings); err != nil {
		done(failedSource(CauseStore, err))

		return err
	}

	state := SourceRefreshed
	if len(mined.Lessons) == 0 {
		state = SourceEmpty
	}

	done(SourceOutcome{
		State: state, Stored: len(mined.Findings), Lessons: len(mined.Lessons),
		MinedAt: st.MetaUnix(store.MetaReviewsMinedAt), Reason: safeReason(mined.Note),
	})

	return nil
}

// fetchProgress translates the typed review events into refresh events.
func fetchProgress(progress func(RefreshEvent)) func(reviews.FetchEvent) {
	phases := map[reviews.FetchStage]RefreshPhase{
		reviews.StageFetching:   PhaseFetchReviews,
		reviews.StageBytes:      PhaseFetchBytes,
		reviews.StageFetched:    PhaseFetched,
		reviews.StageClustering: PhaseCluster,
	}

	return func(e reviews.FetchEvent) {
		progress(RefreshEvent{
			Phase: phases[e.Stage], Source: SourceReviews, Repository: e.Repository.Slug(),
			Bytes: e.Bytes, Comments: e.Comments, Kept: e.Kept,
		})
	}
}

// runBeforePersist runs the caller's hook for source, if one is set.
// The CLI sets none, so its output and order do not change.
func runBeforePersist(req RefreshRequest, source string) error {
	if req.BeforePersist == nil {
		return nil
	}

	return req.BeforePersist(source)
}

// degradedReviews maps a review pass that fetched nothing to a source
// outcome. A missing tool or target is unavailable; an error from a
// fetch that ran is failed.
func degradedReviews(mined reviews.Result) SourceOutcome {
	o := SourceOutcome{State: SourceFailed, Cause: CauseFetchFailed, Reason: safeReason(mined.Note), Err: mined.Err}

	switch mined.Outcome {
	case reviews.OutcomeNoRepository:
		o.State, o.Cause = SourceUnavailable, CauseNoRepository
	case reviews.OutcomeGHMissing:
		o.State, o.Cause = SourceUnavailable, CauseGHMissing
	case reviews.OutcomeAuth:
		o.Cause = CauseAuth
	case reviews.OutcomeRateLimited:
		o.Cause = CauseRateLimited
	case reviews.OutcomeUnreadable:
		o.Cause = CauseUnreadable
	case reviews.OutcomeStopped:
		o.State, o.Cause = SourceStopped, CauseStopped
	}

	return o
}

// failedSource is the outcome of a source that an error ended.
func failedSource(cause SourceCause, err error) SourceOutcome {
	return SourceOutcome{State: SourceFailed, Cause: cause, Reason: safeReason(err.Error()), Err: err}
}

// abortedSource is the outcome of a source that BeforePersist refused. A
// context error is a stop; any other error is a refusal.
func abortedSource(err error) SourceOutcome {
	o := failedSource(CauseRefused, err)

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		o.State, o.Cause = SourceStopped, CauseStopped
	}

	return o
}

// safeReason makes untrusted text safe for one display line. gh output
// and store errors can quote tokens and escape sequences, so it redacts
// secrets, joins the lines, and strips control characters.
func safeReason(s string) string {
	return render.SanitizeBlock(strings.Join(strings.Fields(redact.Secrets(s)), " "))
}

// RefreshFixes mines and persists only fix commits reachable from the
// repository's current HEAD. It performs no network access, which makes it the
// deterministic lesson-source mode for a pinned historical checkout.
func RefreshFixes(root, dbPath string, logf func(string, ...any)) (int, error) {
	res, err := RefreshSources(context.Background(), RefreshRequest{
		Root: root, DBPath: dbPath, Mode: SourcesLocal, Logf: logf,
	})
	if err != nil {
		return 0, err
	}

	return res.FixCount, nil
}

// persistFixes swaps one successful local mine into the shared finding
// corpus. An unmined result preserves existing rows: a missing Git repository
// must not look like an authoritative empty history. The store stamps
// the mine time in the same transaction, because the outcome loop only
// trusts "no recurrence since exposure" if mining actually ran since.
// noReviewRepository tells the store that origin names no GitHub
// repository, so the outcome loop can rely on fix findings alone.
func persistFixes(st *store.Store, res fixes.Result, noReviewRepository bool) (int, error) {
	if !res.Mined {
		return 0, nil
	}

	if err := st.ReplaceFixFindings(res.Findings, noReviewRepository); err != nil {
		return 0, err
	}

	return len(res.Findings), nil
}

// RefreshReviews mines the lesson sources for the repo at root: PR
// review comments (network, via gh) and fix commits (local git) — each
// degrading independently, so a repo without GitHub still gains fix
// findings and an offline run still keeps its stored reviews. It is
// deliberately separate from Run: lesson sources change on the
// review/push cadence, not on every local edit. fixCount reports the
// fix findings stored this pass.
func RefreshReviews(root, dbPath string, logf func(string, ...any)) (res reviews.Result, fixCount int, err error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// The window is the only config the review miner takes; a broken
	// config file must not down the mine — defaults apply, exactly as
	// every other surface degrades. But degrading silently would hide
	// the breakage, so it is at least said out loud.
	window := func(root string) int {
		cfg, err := LoadConfig(root)
		if err != nil {
			logf("config: %v — review window default applies", err)

			return 0
		}

		return cfg.ReviewWindowDays()
	}

	out, err := refreshSources(context.Background(), RefreshRequest{
		Root: root, DBPath: dbPath, Mode: SourcesReviewsAndLocal, Logf: logf,
	}, window)

	// A failure in the fix phase has always returned an empty review
	// result and a zero count.
	if err != nil && out.Reviews.State == SourceNotAttempted {
		return reviews.Result{}, 0, err
	}

	return out.Review, out.FixCount, err
}

// freshSummary returns a Skipped summary when the index at dbPath was
// built from the current workspace state; nil when a rebuild is needed.
// current is the WorkspaceState that Run observed before its reads; a
// rebuild records the same value when the reads leave it unchanged, else
// changedDuringRun. open is the opener of the database, so
// a caller decides whether an older schema migrates. state and decision
// say whether a rebuild must record root.
func freshSummary(root, dbPath, current string, start time.Time, open func(string) (*store.Store, error),
	state ownership.State, decision ownership.Decision,
) *Summary {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}

	if current == "" {
		return nil // no cheap fingerprint without git: rebuild
	}

	st, err := open(dbPath)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }() // read-only probe

	indexed, err := st.GetMeta(store.MetaIndexedState)
	if err != nil || indexed != current {
		return nil
	}

	// The fingerprint holds no path, so a copied or a moved checkout keeps
	// the fingerprint of its origin. Only a rebuild records the new root.
	recorded, _ := st.GetMeta(store.MetaRepoRoot)
	if recordsAnotherRoot(root, dbPath, recorded, state, decision) {
		return nil
	}

	// A database from before coverage persistence has no index_summary;
	// skipping would strand it on "coverage unknown" forever, because the
	// fingerprint still matches. One full pass records it.
	if summary, err := st.GetMeta(store.MetaIndexSummary); err != nil || summary == "" {
		return nil
	}

	// An index built by an older extractor lacks facts that the current
	// one records, such as the calls of value initializers, and the
	// fingerprint still matches. A skip would keep that gap until a forced
	// run, so one full pass rebuilds it and records the current version. A
	// file that does not accept writes keeps the skip, as in
	// recordsAnotherRoot, or every run would fail. In the refusal branch of
	// Run the database records another repository: a mismatch there ends
	// in that refusal, which names the route to a rebuild.
	if v, _ := st.GetMeta(store.MetaParseCacheVersion); v != parseCacheVersion && fileAcceptsWrites(dbPath) {
		return nil
	}

	stats, err := st.Stats()
	if err != nil {
		return nil
	}

	mined, _ := st.GetMeta(store.MetaHistoryMined)

	return &Summary{
		Root: recorded, DBPath: dbPath, Skipped: true, ReadOnly: !fileAcceptsWrites(dbPath),
		HistoryMined: mined == "true",
		Stats:        stats,
		Duration:     time.Since(start),
	}
}

// recordsAnotherRoot reports a database that needs a rebuild to record
// root. A moved or a copied checkout keeps its fingerprint, so the decision
// ends the skip. A default database also ends it for a new spelling of
// root. A file that does not accept the write keeps the skip for both, or
// every run would fail. An empty recorded root says nothing, so it is no
// difference.
func recordsAnotherRoot(root, dbPath, recorded string, state ownership.State, decision ownership.Decision) bool {
	if recorded == "" {
		return false
	}

	differs := decision.Build == ownership.BuildRecords ||
		state.File == ownership.FileDefault && !sameSpelling(recorded, root)

	return differs && fileAcceptsWrites(dbPath)
}

// fileAcceptsWrites reports whether the file opens for a write. The open
// changes nothing, because nothing is written and the file is not cut.
func fileAcceptsWrites(file string) bool {
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		return false
	}

	return f.Close() == nil
}

// sameSpelling reports whether the recorded root resolves to the text of
// root. It is a string test on purpose, unlike store.SameDirectory, which
// a guard uses. The path readers compute relative paths against the
// recorded text, so a rename that changes only the case needs a rebuild.
// A link spelling resolves to the same text and needs none, because the
// hooks hash the recorded spelling.
func sameSpelling(recorded, root string) bool {
	if filepath.Clean(recorded) == filepath.Clean(root) {
		return true
	}

	resolvedRecorded, errRecorded := filepath.EvalSymlinks(recorded)
	resolvedRoot, errRoot := filepath.EvalSymlinks(root)

	return errRecorded == nil && errRoot == nil && resolvedRecorded == resolvedRoot
}

// WorkspaceState fingerprints the workspace content the index depends
// on: HEAD, the status listing, the CONTENT of tracked changes (`git
// diff HEAD` — an already-dirty file edited again keeps its status line
// but not its patch), and a size+mtime proxy for untracked files (their
// status line never changes either). Two equal fingerprints mean the
// index is current. "" when git is unavailable — there is no cheap
// fingerprint without it.
//
// db is the database file that the index lives in. A run writes it and
// its SQLite sidecars, so a file of the database that lies in the
// workspace would change the fingerprint that the run just stored, and
// every run would rebuild. The fingerprint leaves those files out (see
// ownFiles). Every reader passes the file that it reads (store.Path), so
// it computes what Run stored. "" leaves nothing out.
//
// Each git call runs with history.ReadOnlyGitEnviron. Without it, git
// status and git diff rewrite .git/index after a touch, and a concurrent
// git command of the user can then fail on .git/index.lock.
func WorkspaceState(root, db string) string {
	gitOut := func(args ...string) ([]byte, bool) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = history.ReadOnlyGitEnviron()

		out, err := cmd.Output()

		return out, err == nil
	}

	head, ok := gitOut("rev-parse", "HEAD")
	if !ok {
		return ""
	}

	// .seamark holds seamark's OWN state (index.db, audit.jsonl): every
	// run mutates it, so including it would invalidate the fingerprint we
	// just stored and every workspace would look permanently stale.
	const exclSeamark = ":(exclude).seamark"

	own := ownFiles(root, db)
	specs := []string{"--", ".", exclSeamark}
	skip := map[string]bool{}

	for _, rel := range own {
		specs = append(specs, ":(exclude,literal)"+rel)
		skip[filepath.Join(root, filepath.FromSlash(rel))] = true
	}

	// --untracked-files=normal is the git default. It keeps
	// status.showUntrackedFiles=no from hiding a new file, so a new file
	// still changes the fingerprint.
	status, ok := gitOut(append([]string{"status", "--porcelain", "-z", "--untracked-files=normal"}, specs...)...)
	if !ok {
		return ""
	}

	// ParsedGitArgs keeps an external diff or a textconv driver from
	// writing a constant text, which leaves a second edit unseen.
	diff, ok := gitOut(history.ParsedGitArgs("diff", append([]string{"HEAD"}, specs...)...)...)
	if !ok {
		return ""
	}

	h := sha256.New()
	h.Write(head)
	h.Write(status)
	h.Write(diff)

	// The .seamark files that DO shape index output: the effect overlay
	// and the indexing config. Hash their content explicitly so editing
	// either triggers a rebuild even though the directory is otherwise
	// excluded. The name goes in too, so content moving between the files
	// cannot collide into the same fingerprint.
	for _, name := range []string{"effects.yaml", "config.yaml"} {
		if overlay, err := os.ReadFile(filepath.Join(root, ".seamark", name)); err == nil {
			h.Write([]byte(name))
			h.Write(overlay)
		}
	}

	budget := 4096 // bound the stat walk on pathological untracked trees

	for entry := range strings.SplitSeq(string(status), "\x00") {
		if !strings.HasPrefix(entry, "?? ") || budget <= 0 {
			continue
		}

		statInto(h, filepath.Join(root, filepath.FromSlash(entry[3:])), &budget, skip)
	}

	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ownFiles returns the workspace paths, slash-separated, of the database
// db and its SQLite sidecars when db lies in root outside .seamark, else
// nil. .seamark is left out already, so the default database keeps its
// fingerprint byte for byte. The directory of db resolves its links, as
// the root does, and the name stays as given, because git lists a link by
// its own name. A directory that does not exist yet resolves through its
// nearest parent, because the first run creates it.
func ownFiles(root, db string) []string {
	if db == "" {
		return nil
	}

	db = store.AbsolutePath(db)

	top, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}

	dir, ok := resolveDir(filepath.Dir(db))
	if !ok {
		return nil
	}

	rel, err := filepath.Rel(top, filepath.Join(dir, filepath.Base(db)))
	if err != nil || !filepath.IsLocal(rel) {
		return nil
	}

	rel = filepath.ToSlash(rel)
	if first, _, _ := strings.Cut(rel, "/"); first == store.DefaultDir {
		return nil
	}

	return []string{rel, rel + "-wal", rel + "-shm", rel + "-journal"}
}

// resolveDir resolves the links of dir. A part that does not exist is
// kept as written below its nearest parent that does.
func resolveDir(dir string) (string, bool) {
	rest := ""

	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest), true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}

		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// statInto hashes a content proxy (path, size, mtime) for an untracked
// file, or every file under an untracked directory. A path in skip is
// left out: git still lists an untracked directory that holds a file of
// the database beside other files.
func statInto(h io.Writer, p string, budget *int, skip map[string]bool) {
	if skip[p] {
		return
	}

	info, err := os.Stat(p)
	if err != nil {
		return
	}

	if !info.IsDir() {
		fmt.Fprintf(h, "%s|%d|%d\n", p, info.Size(), info.ModTime().UnixNano())
		*budget--

		return
	}

	_ = filepath.WalkDir(p, func(fp string, d os.DirEntry, err error) error {
		if err != nil || *budget <= 0 {
			return filepath.SkipAll
		}

		if d.IsDir() || skip[fp] {
			return nil
		}

		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s|%d|%d\n", fp, info.Size(), info.ModTime().UnixNano())
			*budget--
		}

		return nil
	})
}

// ResolveRoot widens to the git toplevel when inside a repository. It is
// history.ResolveRoot. The name stays here, because Run applies this
// rule, and the CLI and the LSP server call it by this name.
func ResolveRoot(root string) (string, error) {
	return history.ResolveRoot(root)
}

// skipDirs are never worth parsing: dependencies, outputs, fixtures, and
// seamark's own state.
var skipDirs = map[string]bool{
	".git": true, ".seamark": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "testdata": true, ".venv": true,
}

// listFiles returns repo-relative candidate paths. Inside git it trusts
// `git ls-files` (tracked + unignored untracked) so .gitignore is honored;
// otherwise it walks the tree with conservative skips. deleted lists the
// tracked paths that git status shows as deleted and not staged. files
// leaves them out, because nothing exists to parse. A walk finds no such
// path.
//
// present holds every path that git lists and does not show as deleted,
// parsed or not. A pair path outside this set is a gone path. A walk
// returns no present set, because a workspace without git has no pairs.
func listFiles(root string) (files, deleted []string, present map[string]bool, err error) {
	if history.IsRepo(root) {
		return gitFiles(root)
	}

	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			if skipDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".")) {
				if p != root {
					return filepath.SkipDir
				}
			}

			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		files = append(files, filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("index: walk %s: %w", root, err)
	}

	return files, nil, nil, nil
}

// goneFiles returns the paths that a mined pair names and present does
// not hold. History still names deleted and renamed-away files, and an
// agent cannot open them, so the co-change reads leave them out as
// partners and hubs. A walk has no pairs, so it gives no gone path.
func goneFiles(pairs []model.CoChange, present map[string]bool) []string {
	var gone []string

	for _, p := range pairs {
		for _, f := range []string{p.FileA, p.FileB} {
			if !present[f] {
				gone = append(gone, f)
			}
		}
	}

	return gone
}

// gitFiles lists the workspace through git. The cached list keeps a
// tracked path after its file is deleted, until the deletion is staged.
// git diff-files finds these paths, so files leaves them out and
// deleted returns them.
//
// diff-files uses the deletion test of git status. A file that a
// directory replaces is deleted. A path that git cannot lstat for a
// different reason, such as a denied permission, is not deleted. It
// stays in files, so the read reports it as a parse error.
//
// present uses the same two git calls, so the index has one deletion
// rule. It keeps the paths that files drops for parsing reasons: paths
// under skipped directories and skip-worktree paths that a sparse
// checkout leaves out of the working tree.
func gitFiles(root string) (files, deleted []string, present map[string]bool, err error) {
	// -t tags each path, so the loop below can find skip-worktree
	// entries. The tag is one letter and a space before the path.
	listed, err := gitRecords(root, "ls-files", "-z", "-t", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, nil, nil, err
	}

	// --relative gives paths relative to root, as ls-files does.
	// --no-renames keeps each deletion a deletion, also when an added
	// path has the same content.
	gone, err := gitRecords(root, "diff-files", "-z", "--name-only", "--relative", "--no-renames", "--diff-filter=D")
	if err != nil {
		return nil, nil, nil, err
	}

	isGone := make(map[string]bool, len(gone))

	for _, f := range gone {
		isGone[f] = true

		if !underSkippedDir(f) {
			deleted = append(deleted, f)
		}
	}

	present = make(map[string]bool, len(listed))

	for _, rec := range listed {
		tag, f, ok := strings.Cut(rec, " ")
		if !ok || f == "" || isGone[f] {
			continue
		}

		present[f] = true

		if underSkippedDir(f) {
			continue
		}

		// A sparse checkout keeps skip-worktree paths out of the
		// working tree on purpose, and diff-files ignores them. So a
		// missing one is not deleted and not a file to parse. A
		// skip-worktree file that exists stays in files.
		if tag == "S" && missingFile(root, f) {
			continue
		}

		files = append(files, f)
	}

	return files, deleted, present, nil
}

// missingFile reports whether rel has no working-tree entry. Only
// fs.ErrNotExist means missing. Any other lstat error keeps the path,
// so the read reports the real problem as a parse error.
func missingFile(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))

	return errors.Is(err, fs.ErrNotExist)
}

// gitRecords runs one git command with -z output in root. It returns
// the NUL-separated records without the empty last one. The -z form
// keeps paths with special characters unquoted.
func gitRecords(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("index: git %s: %w", args[0], err)
	}

	var records []string

	for rec := range strings.SplitSeq(string(out), "\x00") {
		if rec != "" {
			records = append(records, rec)
		}
	}

	return records, nil
}

func underSkippedDir(rel string) bool {
	for seg := range strings.SplitSeq(path.Dir(rel), "/") {
		if skipDirs[seg] {
			return true
		}
	}

	return false
}

var moduleRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// goModule maps one go.mod to its repo location.
type goModule struct {
	dir  string // repo-relative directory, "" for the root
	path string // module path declared in go.mod
}

// readGoModules parses every go.mod in the workspace. Monorepos nest Go
// modules (a Go ingestor inside a Python repo): imports must resolve
// against their OWN module, or every cross-package call in a nested
// module silently loses its edges. Longest module path first so nested
// modules shadow their parents.
func readGoModules(root string, files []string) []goModule {
	var mods []goModule

	for _, f := range files {
		if path.Base(f) != "go.mod" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}

		m := moduleRe.FindSubmatch(data)
		if m == nil {
			continue
		}

		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}

		mods = append(mods, goModule{dir: dir, path: string(m[1])})
	}

	sort.Slice(mods, func(i, j int) bool { return len(mods[i].path) > len(mods[j].path) })

	return mods
}

// graph is the in-memory staging area between parsing and storage.
type graph struct {
	results []*parse.FileResult
	modules []goModule

	// All keys below are scoped per language family (scopedKey): a Go
	// package directory and an ES module can share the same repo-relative
	// key ("web/api" the directory vs web/api.ts the file) without being
	// the same namespace — unscoped keys let a TS import resolve into Go
	// symbols with high confidence.
	packages map[string]*model.Symbol // scoped package key -> package symbol
	// declared symbols by (scoped package key, name).
	decls map[string]map[string][]*model.Symbol
	// methods by (family, bare name), for lowest-confidence resolution.
	// Family, not dialect: TS/TSX/JS share one namespace, and judging
	// uniqueness per dialect would fabricate edges a repo-wide duplicate
	// should suppress.
	methodsByName map[string]map[string][]*model.Symbol

	catalog *effects.Catalog
	// direct effect tags per symbol id, from catalogue matches.
	direct map[int64]map[string]bool
	// callEdges keeps (caller, callee) ids for effect propagation.
	callEdges [][2]int64
}

// scopedKey namespaces a package key by language family.
func scopedKey(family, key string) string { return family + "\x00" + key }

func buildGraph(results []*parse.FileResult, modules []goModule, catalog *effects.Catalog) *graph {
	g := &graph{
		results:       results,
		modules:       modules,
		packages:      map[string]*model.Symbol{},
		decls:         map[string]map[string][]*model.Symbol{},
		methodsByName: map[string]map[string][]*model.Symbol{},
		catalog:       catalog,
		direct:        map[int64]map[string]bool{},
	}
	// A Python package's __init__.py and a sibling module can claim the
	// same key ("api/__init__.py" and "api.py" → "api"). Python gives the
	// package precedence and the module becomes unimportable; mirror that
	// by keeping the shadowed file's symbols out of the resolution maps.
	// (They are still stored as symbols — the code exists.)
	pyInitKeys := map[string]bool{}

	for _, res := range results {
		if res.Language == "python" && strings.HasSuffix(res.Path, "__init__.py") {
			pyInitKeys[res.Package] = true
		}
	}

	shadowed := func(res *parse.FileResult) bool {
		return res.Language == "python" &&
			!strings.HasSuffix(res.Path, "__init__.py") && pyInitKeys[res.Package]
	}

	for _, res := range results {
		fam := parse.LanguageFamily(res.Language)
		g.packageSymbol(fam, res.Package)

		for i := range res.Symbols {
			if shadowed(res) {
				continue
			}

			sym := &res.Symbols[i].Symbol
			sk := scopedKey(fam, res.Package)
			byName := g.decls[sk]

			if byName == nil {
				byName = map[string][]*model.Symbol{}
				g.decls[sk] = byName
			}

			byName[sym.Name] = append(byName[sym.Name], sym)

			if sym.Kind == model.KindMethod {
				methods := g.methodsByName[fam]
				if methods == nil {
					methods = map[string][]*model.Symbol{}
					g.methodsByName[fam] = methods
				}

				methods[sym.Name] = append(methods[sym.Name], sym)
			}
		}
		// Pre-create package symbols for import targets so insertion order
		// is deterministic and edges always have both endpoints.
		for _, imp := range res.Imports {
			if key, ok := g.importKeyOK(imp); ok {
				g.packageSymbol(fam, key)
			}
		}
	}
	return g
}

// importKeyOK returns the package key for an import; false for relative
// specifiers that escaped the repo, which must not materialize junk
// package nodes (their raw dotted/relative text is not a key).
func (g *graph) importKeyOK(imp parse.Import) (string, bool) {
	if imp.Resolved == "" && strings.HasPrefix(imp.Path, ".") {
		return "", false
	}

	return g.importKey(imp), true
}

// importKey maps an import to a package-symbol key: the extractor's own
// resolution when it has one (JS/TS relative specifiers), repo-relative
// translation for Go module paths, the raw path for external imports.
func (g *graph) importKey(imp parse.Import) string {
	if imp.Resolved != "" {
		return imp.Resolved
	}

	// Modules are sorted longest-path-first, so a nested module shadows
	// its parent for imports under its prefix.
	for _, m := range g.modules {
		if imp.Path == m.path {
			return m.dir
		}

		if rel, ok := strings.CutPrefix(imp.Path, m.path+"/"); ok {
			return path.Join(m.dir, rel)
		}
	}

	return imp.Path
}

// packageSymbol lazily creates the grouping symbol for a package key
// within one language family. The repo root package is named "." to keep
// FQNs non-empty.
func (g *graph) packageSymbol(family, key string) *model.Symbol {
	sk := scopedKey(family, key)
	if sym, ok := g.packages[sk]; ok {
		return sym
	}

	fqn := key
	if fqn == "" {
		fqn = "."
	}

	sym := &model.Symbol{FQN: fqn, Name: path.Base(fqn), Kind: model.KindPackage}
	g.packages[sk] = sym

	return sym
}

// write inserts symbols, then resolves references into edges. Symbols must
// be inserted first so edge endpoints have IDs.
func (g *graph) write(tx *store.Tx) error {
	for _, key := range sortedKeys(g.packages) {
		if err := tx.InsertSymbol(g.packages[key]); err != nil {
			return err
		}
	}

	for _, res := range g.results {
		pkg := g.packages[scopedKey(parse.LanguageFamily(res.Language), res.Package)]

		for i := range res.Symbols {
			sym := &res.Symbols[i].Symbol
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}

			// DEFINES edges tie each symbol to its package/module node —
			// and disambiguate same-FQN package nodes across language
			// families (a Go dir and an ES module can share a key).
			if pkg != nil {
				err := tx.InsertEdge(model.Edge{
					Src: pkg.ID, Dst: sym.ID,
					Kind: model.EdgeDefines, Origin: model.OriginParse,
				})
				if err != nil {
					return err
				}
			}
		}
	}

	for _, res := range g.results {
		if err := g.writeImportEdges(tx, res); err != nil {
			return err
		}

		for i := range res.Symbols {
			if err := g.writeCallEdges(tx, res, &res.Symbols[i]); err != nil {
				return err
			}
		}
	}

	return g.writeEffects(tx)
}

func (g *graph) writeImportEdges(tx *store.Tx, res *parse.FileResult) error {
	fam := parse.LanguageFamily(res.Language)
	src := g.packages[scopedKey(fam, res.Package)]

	for _, imp := range res.Imports {
		key, ok := g.importKeyOK(imp)
		if !ok {
			continue
		}

		dst := g.packages[scopedKey(fam, key)]

		if src == nil || dst == nil || src == dst {
			continue
		}

		err := tx.InsertEdge(model.Edge{
			Src: src.ID, Dst: dst.ID,
			Kind: model.EdgeImports, Origin: model.OriginParse,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// writeCallEdges resolves the call references of one declaration, in
// descending confidence order. Every edge declares its derivation:
//
//  1. bare identifier    → named import target module,     (qualified)
//     that is a named      else function in the same       (same-package)
//     import               package/module
//  2. selector whose     → function in the imported repo   (qualified)
//     qualifier is an      package/module
//     import alias or
//     namespace import
//  3. self/cls/this      → the method of the caller's      (same-class)
//     method call          own class, when declared there
//  4. any other          → the single same-family repo     (unique-name)
//     selector call        symbol with that method name
//
// In Go, tier 4 skips three kinds of call. A call whose operand chain starts
// at an import outside the repository gets no edge. Such a value is rarely
// a repository value, and every measured guess there was false. A generic
// helper such as cmp.Or can return one, and that real call then loses its
// edge. A common standard-library method name gets no edge when the calling
// file neither shares the method's directory nor imports its package (see
// uniqueMethod). An unexported method of another directory gets no edge,
// because Go allows no such call.
//
// Known limitation: resolution is purely syntactic, so a local variable
// shadowing an import alias (or sharing a method name) can produce a wrong
// edge. The origin column declares the derivation exactly so consumers can
// weigh it; proper scope tracking is future work.
func (g *graph) writeCallEdges(tx *store.Tx, res *parse.FileResult, decl *parse.SymbolDecl) error {
	fam := parse.LanguageFamily(res.Language)

	imports := map[string]string{}    // qualifier local name -> scoped package key
	named := map[string]string{}      // bare imported name -> scoped package key
	rawImports := map[string]string{} // qualifier local name -> import path as written
	rawNamed := map[string]string{}   // bare imported name -> import path as written
	imported := map[string]bool{}     // scoped package key of every import, blank and dot included

	for _, imp := range res.Imports {
		if imp.LocalName != "" {
			rawImports[imp.LocalName] = imp.Path
		}

		for _, n := range imp.Named {
			rawNamed[n] = imp.Path
		}

		rawKey, ok := g.importKeyOK(imp)
		if !ok {
			continue
		}

		key := scopedKey(fam, rawKey)

		// An empty import path maps to the root package key "". Only a real
		// import of the module root may count as evidence for a root method.
		if imp.Path != "" {
			imported[key] = true
		}

		if imp.LocalName != "" {
			imports[imp.LocalName] = key
		}

		for _, n := range imp.Named {
			named[n] = key
		}
	}

	// A function can reach the same destination through several call
	// sites resolved by different tiers; keep the best origin per target
	// so a same-class resolution is never reported as a name-match guess.
	best := map[int64]string{}

	for _, call := range decl.Calls {
		// Effect detection runs on the raw reference: the interesting
		// sinks live in external dependencies and never resolve to edges.
		g.detectCallEffects(fam, rawImports, rawNamed, decl.Symbol.ID, call)

		var dst *model.Symbol
		origin := ""

		switch {
		case !call.Selector:
			// Bare identifier: a named import wins over the local scope
			// (that is what the import means); otherwise a function in the
			// same package/module. Selector calls must not land here —
			// x.db.Close() is a method call on a value, not a reference
			// into the package scope.
			if pkgKey, ok := named[call.Name]; ok {
				dst = pickUnique(g.decls[pkgKey][call.Name], model.KindFunction)
				origin = model.OriginQualified
			} else {
				dst = pickUnique(g.decls[scopedKey(fam, res.Package)][call.Name], model.KindFunction)
				origin = model.OriginSamePackage
			}
		case call.Receiver && decl.Symbol.Kind == model.KindMethod:
			// The receiver shadows any same-named import, so this tier
			// comes first. The caller's FQN is <pkg>.<Class>.<name>; a
			// sibling method shares everything but the last segment.
			want := strings.TrimSuffix(decl.Symbol.FQN, "."+decl.Symbol.Name) + "." + call.Name

			for _, c := range g.decls[scopedKey(fam, res.Package)][call.Name] {
				if c.Kind == model.KindMethod && c.FQN == want {
					dst = c
					origin = model.OriginSameClass

					break
				}
			}

			// Miss (e.g. an inherited method): the guess tier may catch it.
			if dst == nil {
				dst, origin = g.uniqueMethod(fam, call.Name, res.Path, imported)
			}
		case call.Qualifier != "":
			if pkgKey, ok := imports[call.Qualifier]; ok {
				// A known import qualifier that does not resolve is a call
				// into an external module — never fall through to a guess.
				dst = pickUnique(g.decls[pkgKey][call.Name], model.KindFunction)
				origin = model.OriginQualified
			} else {
				dst, origin = g.uniqueMethod(fam, call.Name, res.Path, imported)
			}
		case g.externalRoot(imports, call.Root):
			// The operand chain starts at a package outside the repository,
			// as strings.NewReplacer(…).Replace(s) does. Such a value is
			// rarely a repository value, and every measured guess there was
			// false, so the call gets no edge. A generic helper such as
			// cmp.Or can return a repository value, and that real call then
			// loses its edge.
		default:
			// Complex operand (x.db.Close()): only the guess tier applies.
			dst, origin = g.uniqueMethod(fam, call.Name, res.Path, imported)
		}

		if dst == nil || dst.ID == decl.Symbol.ID {
			continue
		}

		if cur, seen := best[dst.ID]; !seen || originRank(origin) > originRank(cur) {
			best[dst.ID] = origin
		}
	}

	for dstID, origin := range best {
		err := tx.InsertEdge(model.Edge{
			Src: decl.Symbol.ID, Dst: dstID,
			Kind: model.EdgeCalls, Origin: origin,
		})
		if err != nil {
			return err
		}

		g.callEdges = append(g.callEdges, [2]int64{decl.Symbol.ID, dstID})
	}

	return nil
}

// detectCallEffects records catalogue matches for one call site on the
// enclosing symbol.
func (g *graph) detectCallEffects(fam string, rawImports, rawNamed map[string]string,
	symID int64, call parse.CallRef,
) {
	if g.catalog == nil {
		return
	}

	var tags []string

	if call.Selector {
		if p, ok := rawImports[call.Qualifier]; ok && call.Qualifier != "" {
			tags = append(tags, g.catalog.MatchImport(fam, p, call.Name)...)
		}

		tags = append(tags, g.catalog.MatchMethod(fam, call.Name)...)
	} else {
		if p, ok := rawNamed[call.Name]; ok {
			tags = append(tags, g.catalog.MatchImport(fam, p, call.Name)...)
		}

		tags = append(tags, g.catalog.MatchCall(fam, call.Name)...)
	}

	if len(tags) == 0 {
		return
	}

	set := g.direct[symID]
	if set == nil {
		set = map[string]bool{}
		g.direct[symID] = set
	}

	for _, t := range tags {
		set[t] = true
	}
}

// writeEffects propagates direct tags backwards along CALLS edges to
// fixpoint (BFS, shortest depth wins) and stores the result: any symbol
// can then be asked "what can this ultimately do?" (RFC-001 §5.2).
func (g *graph) writeEffects(tx *store.Tx) error {
	type key struct {
		id  int64
		tag string
	}

	type item struct {
		id  int64
		tag string
		d   int
	}

	depth := map[key]int{}

	var queue []item

	for id, tags := range g.direct {
		for tag := range tags {
			depth[key{id, tag}] = 0
			queue = append(queue, item{id, tag, 0})
		}
	}

	callers := map[int64][]int64{}
	for _, e := range g.callEdges {
		callers[e[1]] = append(callers[e[1]], e[0])
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, caller := range callers[cur.id] {
			k := key{caller, cur.tag}
			if d, seen := depth[k]; seen && d <= cur.d+1 {
				continue
			}

			depth[k] = cur.d + 1
			queue = append(queue, item{caller, cur.tag, cur.d + 1})
		}
	}

	for k, d := range depth {
		origin := "propagated"
		if d == 0 {
			origin = "direct"
		}

		if err := tx.InsertEffect(k.id, k.tag, origin, d); err != nil {
			return err
		}
	}

	return nil
}

// originRank orders derivations by confidence for edge dedup: any resolved
// tier beats the name-match guess.
func originRank(origin string) int {
	if origin == model.OriginUniqueName {
		return 1
	}

	return 2
}

// commonGoMethodNames lists Go method names that standard-library types
// implement and that Go code often calls on values whose type the parser
// cannot see. The sources are error, fmt.Stringer and fmt.Formatter,
// context.Context, the io interfaces, bufio, sync, sort.Interface,
// strings and bytes, bufio.Scanner, database/sql, http.Header, url.Values,
// os/exec, os, time and testing.TB. A repository method with such a name is
// often the only one, so a call such as ctx.Err() or
// strings.NewReplacer(…).Replace(s) looks unique although its target is a
// standard-library value. uniqueMethod therefore asks for package evidence
// before it keeps a guess with one of these names.
var commonGoMethodNames = map[string]bool{
	// error, fmt.Stringer, fmt.Formatter.
	"Error": true, "String": true, "Format": true,
	// context.Context.
	"Deadline": true, "Done": true, "Err": true, "Value": true,
	// The io interfaces and bufio.
	"Read": true, "Write": true, "Close": true, "Seek": true,
	"ReadAt": true, "WriteAt": true, "ReadFrom": true, "WriteTo": true,
	"ReadByte": true, "WriteByte": true, "ReadRune": true, "WriteString": true,
	"Flush": true,
	// sync.Mutex, sync.RWMutex, sync.WaitGroup, sync.Once, sync.Map.
	"Lock": true, "Unlock": true, "RLock": true, "RUnlock": true,
	"Wait": true, "Do": true, "Load": true, "Store": true,
	"Delete": true, "Range": true,
	// sort.Interface.
	"Len": true, "Less": true, "Swap": true,
	// strings.Replacer, strings.Builder, bytes.Buffer.
	"Replace": true, "Reset": true, "Bytes": true, "Grow": true,
	// bufio.Scanner, database/sql rows.
	"Scan": true, "Next": true, "Text": true,
	// http.Header, url.Values.
	"Get": true, "Set": true, "Add": true, "Del": true,
	// os/exec.Cmd, os.File, os.Process, time.Timer, time.Ticker.
	"Run": true, "Start": true, "Kill": true, "Stop": true,
	"Name": true, "Stat": true, "Size": true, "Sync": true,
	// testing.TB.
	"Helper": true, "Cleanup": true, "Fatal": true, "Fatalf": true,
	"Errorf": true, "Log": true, "Logf": true, "Skip": true,
	"TempDir": true, "Setenv": true,
}

// uniqueMethod is the lowest-confidence tier: the single method in the
// language family carrying the called name, if exactly one exists.
// Production callers never resolve into test files — test doubles (fake
// engines, stub clients) carry generic names (set, begin, fetchone) that
// are often the only repo-declared occurrence, and every such edge is a
// fabrication. A Go caller also skips a method in a _test.go file of
// another directory. Go compiles a _test.go file only into the test binary
// of its own directory, so such a method can never be the target. The rule
// is Go only: Python and ECMA test files can import helpers from any
// directory. A Go caller also never resolves into an unexported method of
// another directory, because Go allows no such call.
//
// A Go call with a name from commonGoMethodNames keeps its sole candidate
// only with package evidence. The calling file is in the candidate's
// directory, or it imports the candidate's package. imported holds the
// scoped package keys of every import of the calling file. Without evidence
// the call gets no edge, so false effects do not flow to the caller.
//
// The trade-off: a real call loses its guess when the calling file does not
// import the package that declares the type of the value. An interface call
// is one case. A call on a field is another case: pair.Data.WriteString(…),
// where another file of the caller's directory declares the field. A method
// promoted from an embedded field that another file declares is a third
// case. The evidence stays per file on purpose. A test on a large Go
// repository found that imports from every file of the directory restore
// one real call and about fifty false guesses.
//
// The rule is Go only. Python and ECMA code often calls a value of a module
// that the file never imports, so an import there is weak evidence.
func (g *graph) uniqueMethod(fam, name, callerFile string, imported map[string]bool) (dst *model.Symbol, origin string) {
	var candidates []*model.Symbol

	for _, c := range g.methodsByName[fam][name] {
		if !isTestPath(callerFile) && isTestPath(c.File) {
			continue
		}

		if fam == "go" && !goTestVisible(callerFile, c.File) {
			continue
		}

		candidates = append(candidates, c)
	}

	if len(candidates) != 1 {
		return nil, ""
	}

	c := candidates[0]

	if fam == "go" && commonGoMethodNames[name] && !goPackageEvidence(callerFile, c.File, imported) {
		return nil, ""
	}

	// Go allows no call to an unexported method of another package. The
	// check runs after the count, so a call that two methods share stays
	// without an edge.
	if fam == "go" && !token.IsExported(name) && path.Dir(callerFile) != path.Dir(c.File) {
		return nil, ""
	}

	return c, model.OriginUniqueName
}

// externalRoot reports whether an operand root names an import whose
// package declares nothing in the graph, that is, a package outside the
// repository. An empty root never matches, because blank and dot imports
// have no local name in imports. Only the Go extractor sets a root.
func (g *graph) externalRoot(imports map[string]string, root string) bool {
	if root == "" {
		return false
	}

	key, ok := imports[root]

	return ok && len(g.decls[key]) == 0
}

// goPackageEvidence reports whether Go code in callerFile has a reason to
// call a method declared in candidateFile. The reason is a shared directory
// or an import of the candidate's package. A Go package key is the file's
// directory, "" at the repository root, so the root key matches only an
// import of the module root.
func goPackageEvidence(callerFile, candidateFile string, imported map[string]bool) bool {
	dir := path.Dir(candidateFile)
	if path.Dir(callerFile) == dir {
		return true
	}

	if dir == "." {
		dir = ""
	}

	return imported[scopedKey("go", dir)]
}

// goTestVisible reports whether Go code in callerFile can call a method
// declared in candidateFile. A method in a _test.go file is visible only to
// _test.go files of the same directory. path.Dir gives "." for a root file,
// so two root files count as one directory.
func goTestVisible(callerFile, candidateFile string) bool {
	if !strings.HasSuffix(candidateFile, "_test.go") {
		return true
	}

	return strings.HasSuffix(callerFile, "_test.go") && path.Dir(callerFile) == path.Dir(candidateFile)
}

// isTestPath reports whether a file is test code, by each language's
// naming convention.
func isTestPath(p string) bool { return model.IsTestPath(p) }

// pickUnique returns the sole symbol of the wanted kind, nil otherwise.
// Ambiguity (build-tag variants, redeclarations) yields no edge rather than
// a guessed one.
func pickUnique(candidates []*model.Symbol, kind model.SymbolKind) *model.Symbol {
	var found *model.Symbol
	for _, c := range candidates {
		if c.Kind != kind {
			continue
		}
		if found != nil {
			return nil
		}
		found = c
	}
	return found
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
