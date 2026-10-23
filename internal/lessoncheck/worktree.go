package lessoncheck

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // git names SHA-1 blobs with SHA-1; this is an id, not a security digest.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds ReadWorkingTree, the working-tree producer of contract
// K4. It only reads: git runs rev-parse, status, ls-files, cat-file, and
// ls-tree through history.RunGit, which stops the optional index lock,
// and the worktree reads never follow a symbolic link (AC-29). No step
// adds, stages, checks out, stashes, or refreshes the index.

// The fixed selection texts of contract K4. The output prints them, so a
// reader sees what the snapshot covers and what it leaves out.
const (
	selectionDefaultHead   = "tracked changes against HEAD plus untracked nonignored files, staged and unstaged, as present in the working tree"
	selectionDefaultEmpty  = "tracked changes against the empty tree plus untracked nonignored files, staged and unstaged, as present in the working tree"
	selectionExplicitHead  = "the paths named by --file, against HEAD"
	selectionExplicitEmpty = "the paths named by --file, against the empty tree"
)

// selectionExcludeSkipped is the fixed exclude text for the index entries
// with the skip-worktree or assume-unchanged bit (manager ruling S2). git
// does not read the worktree side of such an entry, and the user set the
// bit on purpose. A snapshot adds the text after the texts of its kind,
// once, only when its selection holds such an entry.
const selectionExcludeSkipped = "worktree changes of skip-worktree or assume-unchanged index entries, such as paths outside a sparse checkout"

// selectionExcludes returns the fixed exclude texts of a selection kind.
// It returns a new slice each time, so no snapshot shares it.
func selectionExcludes(kind SelectionKind) []string {
	if kind == SelectionExplicitPaths {
		return []string{"paths not named by --file", "writes outside this snapshot"}
	}

	return []string{"ignored files not named by --file", "writes outside this snapshot", "content inside submodules"}
}

// selectionOf returns the selection of a working-tree snapshot.
func selectionOf(paths []string, baseline BaselineKind) Selection {
	unborn := baseline == BaselineEmptyTree

	if len(paths) == 0 {
		desc := selectionDefaultHead
		if unborn {
			desc = selectionDefaultEmpty
		}

		return Selection{Kind: SelectionDefault, Description: desc, Excludes: selectionExcludes(SelectionDefault)}
	}

	desc := selectionExplicitHead
	if unborn {
		desc = selectionExplicitEmpty
	}

	return Selection{Kind: SelectionExplicitPaths, Description: desc, Paths: paths, Excludes: selectionExcludes(SelectionExplicitPaths)}
}

// ReadWorkingTree reads the working-tree snapshot of root, the top level
// of a git work tree. An empty paths list is the default selection:
// tracked changes against HEAD plus untracked nonignored files. A
// non-empty list is the explicit-paths selection: the changes at or
// below each path, ignored files included. The CLI normalizes each path
// first (contracts §1.3), so paths are repository-relative and
// slash-separated. A zero lim means DefaultLimits.
//
// ReadWorkingTree returns an error only when root is not in a git work
// tree, when a path is not repository-relative, when step 1 cannot read
// HEAD, or when ctx is canceled. Every later failure stays in the
// snapshot: an incomplete path set or an unreadable side, never an
// empty pass.
//
// When the ctx deadline passes after step 1, the snapshot keeps its
// baseline and selection, holds no change, and names verify-timeout in
// Limits. The working-tree boundary serves lessons --verify, check
// --lessons, and MCP check, whose deadline is verify-timeout (contract
// K3). A deadline during step 1 returns an error that wraps
// context.DeadlineExceeded, because the snapshot has no baseline then.
// A canceled ctx returns an error that wraps context.Canceled, so the
// caller can report the results as interrupted.
func ReadWorkingTree(ctx context.Context, root string, paths []string, lim Limits) (*Snapshot, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	selected, err := selectionPaths(paths)
	if err != nil {
		return nil, err
	}

	if !history.IsRepo(root) {
		return nil, fmt.Errorf("lesson checks: %s is not inside a git work tree", root)
	}

	r := newTreeReader(ctx, root, lim)

	baseline, err := r.baseline()
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		Schema:        SnapshotSchema,
		Boundary:      BoundaryWorkingTree,
		Baseline:      baseline,
		Selection:     selectionOf(selected, baseline.Kind),
		Changes:       []Change{},
		PathsComplete: true,
		bounds:        lim,
	}

	r.fill(snap, selected)

	if r.stopped != nil {
		if !errors.Is(r.stopped, context.DeadlineExceeded) {
			return nil, fmt.Errorf("lesson checks: read the working tree: %w", r.stopped)
		}

		timedOut(snap, lim)
	}

	snap.SHA256 = snapshotDigest(snap)

	return snap, nil
}

// selectionPaths checks, sorts, and deduplicates the explicit paths. The
// CLI gives normalized paths, so a path that is not repository-relative
// is a programming error. The check stays, because such a path could
// lead a read outside the worktree.
func selectionPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(paths))

	for _, p := range paths {
		if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return nil, fmt.Errorf("lesson checks: path %q is not repository-relative", p)
		}

		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	sort.Strings(out)

	return out, nil
}

// timedOut turns a snapshot that the deadline cut into one without
// changes. A partial read must not decide any pin, so every checker then
// gives unknown/resource-limit with verify-timeout (contract K3).
func timedOut(snap *Snapshot, lim Limits) {
	snap.Changes = []Change{}
	snap.PathsComplete = false
	snap.Limits = []LimitHit{{Name: LimitVerifyTimeout, Value: lim.VerifyTimeout.Milliseconds()}}
	snap.Limitations = nil
	snap.identities = nil
	snap.statusSHA256 = ""
	snap.statusStderr = ""
	snap.UnreadableDirectories = nil
	snap.UnreadableDirectoriesOmitted = 0
	snap.undecidedLimit = ""
	// The skipped-entry exclude comes from the census, which a deadline
	// can cut. The fixed texts of the kind keep the result the same for
	// each deadline.
	snap.Selection.Excludes = selectionExcludes(snap.Selection.Kind)
}

// treeReader holds the state of one working-tree read.
type treeReader struct {
	ctx  context.Context
	root string
	lim  Limits
	// format is the object format of the repository, "sha1" or
	// "sha256". It is "" when git cannot tell (git older than 2.25).
	format string
	// budget is the part of max-snapshot-bytes that no read used yet.
	budget int64
	files  map[string]*worktreeFile
	dirs   map[string]dirLook
	hits   map[string]*LimitHit
	// stopped is the context error that ended the read early.
	stopped     error
	limitations map[string]bool
	// undecided is true when a candidate had no content comparison, so
	// the path set is not complete. undecidedLimit names the first limit
	// that caused it, in path order, or "" for a read error.
	undecided      bool
	undecidedLimit string
	// statusStderr is the first line that a status call wrote to stderr,
	// prepared by excerpt: redacted, sanitized, and cut to
	// max-excerpt-runes. It is "" when no status call wrote to stderr.
	statusStderr string
	// unreadableDirs holds each directory that the census could not read,
	// in path order. No entry lies below another entry.
	unreadableDirs []string
	// skippedEntries is true when the census found an index entry with
	// the skip-worktree or assume-unchanged bit in the selection.
	skippedEntries bool
}

// newTreeReader returns a reader for one snapshot of root.
func newTreeReader(ctx context.Context, root string, lim Limits) *treeReader {
	return &treeReader{
		ctx:         ctx,
		root:        root,
		lim:         lim,
		budget:      lim.MaxSnapshotBytes,
		files:       map[string]*worktreeFile{},
		dirs:        map[string]dirLook{},
		hits:        map[string]*LimitHit{},
		limitations: map[string]bool{},
	}
}

// done reports whether ctx ended. It keeps the first context error, so
// ReadWorkingTree can tell a deadline from a cancellation.
func (r *treeReader) done() bool {
	if r.stopped == nil {
		r.stopped = r.ctx.Err()
	}

	return r.stopped != nil
}

// hit records that a named limit stopped the read of paths paths.
func (r *treeReader) hit(name string, value int64, paths int) {
	if h, ok := r.hits[name]; ok {
		h.Paths += paths

		return
	}

	r.hits[name] = &LimitHit{Name: name, Value: value, Paths: paths}
}

// git runs one git command with the output bound of max-git-output-bytes.
// The flag is false when git gave no usable answer: the context ended,
// the output passed the bound, git did not run, or git exited non-zero.
func (r *treeReader) git(stdin []byte, args ...string) (history.GitOutput, bool) {
	if r.done() {
		return history.GitOutput{}, false
	}

	call := history.GitCall{Dir: r.root, Args: args, MaxStdout: r.lim.MaxGitOutputBytes}
	if stdin != nil {
		call.Stdin = bytes.NewReader(stdin)
	}

	out, err := history.RunGit(r.ctx, call)

	switch {
	case errors.Is(err, history.ErrGitOutputLimit):
		r.hit(LimitMaxGitOutputBytes, r.lim.MaxGitOutputBytes, 0)

		return out, false
	case err != nil:
		r.done()

		return out, false
	}

	return out, out.ExitCode == 0
}

// baseline runs step 1: HEAD or, in an unborn repository, the empty tree.
func (r *treeReader) baseline() (Baseline, error) {
	out, err := history.RunGit(r.ctx, history.GitCall{
		Dir:       r.root,
		Args:      []string{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"},
		MaxStdout: r.lim.MaxGitOutputBytes,
	})
	if err != nil {
		return Baseline{}, fmt.Errorf("lesson checks: git rev-parse failed: %w", err)
	}

	switch out.ExitCode {
	case 0:
		return Baseline{Kind: BaselineHead, Commit: strings.TrimSpace(string(out.Stdout))}, nil
	case 1:
		// --verify --quiet exits 1 when HEAD names no commit yet.
		return Baseline{Kind: BaselineEmptyTree}, nil
	default:
		return Baseline{}, fmt.Errorf("lesson checks: git rev-parse failed: %s", render.Sanitize(strings.TrimSpace(string(out.Stderr))))
	}
}

// maxUnreadableDirectories is the output cap max-unreadable-directories
// (K3 output caps). A snapshot lists at most this many unreadable census
// directories and counts the rest, so the JSON stays bounded. The cut
// never changes PathsComplete or the status digest, which use the whole
// list. The value is the value of max-audit-paths. The name is new,
// because each cap name applies to one field.
const maxUnreadableDirectories = 50

// fill runs steps 2 to 6 and writes the result into snap.
func (r *treeReader) fill(snap *Snapshot, selected []string) {
	r.format = r.objectFormat()
	if r.format == "" {
		r.limitations[LimitationMovePairingUnavailable] = true
	}

	records, sum, complete := r.statusRecords(selected)
	snap.statusSHA256 = sum
	snap.statusStderr = r.statusStderr
	snap.UnreadableDirectories, snap.UnreadableDirectoriesOmitted = boundEntries(r.unreadableDirs, maxUnreadableDirectories)
	snap.PathsComplete = complete

	if r.skippedEntries {
		snap.Selection.Excludes = append(snap.Selection.Excludes, selectionExcludeSkipped)
	}

	containers := map[string]bool{}

	for i := range records {
		if submoduleContentChanged(records[i].sub) && selects(selected, records[i].path) {
			r.limitations[LimitationSubmoduleContentNotInspected] = true
		}

		// A path that --file names inside a submodule or a nested
		// repository is not inspected. The limitation tells the reader,
		// so the result is never a silent clean one.
		for _, p := range containerPaths(&records[i]) {
			containers[p] = true

			if holdsSelected(p, selected) {
				r.limitations[LimitationSubmoduleContentNotInspected] = true
			}
		}
	}

	cands := mapRecords(records)

	head := r.headEntries(cands, records, selected, snap.Baseline.Kind)
	if head == nil {
		snap.PathsComplete = false
	}

	unmergedHeads(cands, head)

	cands = append(cands, r.linkedPaths(records, selected, head)...)
	cands = r.applyLstatRule(cands, selected)
	sizes := r.blobSizes(cands)
	cands = r.pairMoves(cands, selected, sizes)
	cands = keepSelected(cands, selected, containers)

	snap.Changes = r.changes(cands, sizes)
	if r.undecided {
		snap.PathsComplete = false
		snap.undecidedLimit = r.undecidedLimit
	}

	snap.Limits = r.limitHits()
	snap.Limitations = r.limitationList()
	snap.identities = r.identityList()
}

// objectFormat runs step 2. It returns "" when git cannot tell, and move
// pairing is then not available.
func (r *treeReader) objectFormat() string {
	out, ok := r.git(nil, "rev-parse", "--show-object-format")
	if !ok {
		return ""
	}

	switch f := strings.TrimSpace(string(out.Stdout)); f {
	case "sha1", "sha256":
		return f
	default:
		return ""
	}
}

// statusRecords runs step 3, for an explicit selection step 3a, and then
// the directory census. It returns the records, the hex SHA-256 of the
// status output and the census result, and whether the path set is
// complete. Step 3a gives only its '!' records: the ignored files at or
// below each named path. --literal-pathspecs makes git read each named
// path as a plain name, so a "*" or a ":" in a name is no pattern.
//
// Step 3 sets status.renames=true on the command line. A user or
// repository setting diff.renames=copies or status.renames=copies
// otherwise makes git print a copy as a '2' record. The K4 mapping
// reads every '2' record as a move, so a copy would hide its added
// lines (review fix snapshot-git:F1).
//
// The digest holds the stdout of each call and the census result, so
// the revalidation (contract K5) also sees a directory that becomes
// unreadable after the read. It does not hold the stderr of git, because
// stderr is a notice only (ruling D2).
func (r *treeReader) statusRecords(selected []string) (records []statusRecord, sum string, complete bool) {
	h := sha256.New()

	out, ok := r.git(nil, "-c", "core.quotePath=off", "-c", "status.renames=true", "status", "--porcelain=v2", "-z",
		"--untracked-files=all", "--ignored=no", "--ignore-submodules=none", "--find-renames")
	if !ok {
		return nil, "", false
	}

	records, err := parseStatusV2(out.Stdout)
	if err != nil {
		return nil, "", false
	}

	h.Write(out.Stdout)
	r.noteStderr(out.Stderr)

	complete = true

	if len(selected) > 0 {
		args := append([]string{"--literal-pathspecs", "-c", "core.quotePath=off", "status", "--porcelain=v2", "-z",
			"--untracked-files=all", "--ignored=traditional", "--"}, selected...)

		ignored, ok := r.git(nil, args...)
		extra, err := parseStatusV2(ignored.Stdout)

		if ok && err == nil {
			for _, rec := range extra {
				if rec.kind == '!' {
					records = append(records, rec)
				}
			}

			// A NUL separates the two outputs, so their bytes cannot shift
			// from one output to the other and keep the same hash.
			h.Write([]byte{0})
			h.Write(ignored.Stdout)
			r.noteStderr(ignored.Stderr)
		} else {
			complete = false
		}
	}

	records, complete = r.cutRecords(records, complete)
	complete = r.census(h, records, selected) && complete

	return records, hex.EncodeToString(h.Sum(nil)), complete
}

// noteStderr records the stderr of one status call that exited 0. Text
// there gives the snapshot limitation git-stderr, a notice only. It never
// makes the path set incomplete, because a configuration warning that no
// rerun clears must not give exit 1 on every run (ruling D2). The census
// decides the path set. The first stderr line becomes the notice text.
func (r *treeReader) noteStderr(stderr []byte) {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return
	}

	r.limitations[LimitationGitStderr] = true

	if r.statusStderr == "" {
		line, _, _ := strings.Cut(text, "\n")
		r.statusStderr = excerpt(strings.TrimSpace(line), r.lim.MaxExcerptRunes)
	}
}

// census runs the directory census of ruling D2. git 2.50.1 prints no
// status record for a tracked file below a directory that the producer
// cannot search, and it exits 0. So the producer reads each directory of
// censusDirs in path order. An unreadable directory leaves its subtree
// undecided, and the census does not read below it. The result is false
// when a directory is unreadable, when git cannot list the index, when
// the directories pass max-census-directories, or when an index entry
// below a link has no status record (belowLink). An index entry with the
// skip-worktree or assume-unchanged bit stays out of belowLink (manager
// ruling S2). Its parent directories join the census when they exist
// (ruling S2, option (c)).
//
// The status digest gets a NUL and a census byte, then each unreadable
// directory with a NUL after it. The byte is '0' for a limit, a git
// failure, or an index entry below a link without a status record. Else
// it is '1'. So the revalidation (contract K5) sees a directory that
// becomes unreadable after the read, and a link that replaces a
// directory without a change of the status output. No time goes into
// the digest.
//
// The census empties the directory cache at its end. Step 6 and the
// revalidation then run Lstat on each parent again, so the census does
// not make the time between a parent Lstat and a file read longer.
func (r *treeReader) census(h hash.Hash, records []statusRecord, selected []string) bool {
	defer clear(r.dirs)

	dirs, unrecorded, complete := r.censusDirs(records, selected)

	if limit := r.lim.MaxCensusDirectories; len(dirs) > limit {
		r.hit(LimitMaxCensusDirectories, int64(limit), len(dirs)-limit)
		dirs, complete = dirs[:limit], false
	}

	for _, dir := range dirs {
		if r.done() {
			return false
		}

		if !below(dir, r.unreadableDirs) && !r.readableDir(dir) {
			r.unreadableDirs = append(r.unreadableDirs, dir)
		}
	}

	// The check runs only after a census without a cut. Each parent of an
	// entry in unrecorded is then in dirs, so dirState looks at no other
	// directory, and max-census-directories still bounds these Lstat calls.
	if complete && r.belowLink(unrecorded) {
		complete = false
	}

	h.Write([]byte{0})

	if complete {
		h.Write([]byte{'1'})
	} else {
		h.Write([]byte{'0'})
	}

	for _, dir := range r.unreadableDirs {
		h.Write([]byte(dir))
		h.Write([]byte{0})
	}

	return complete && len(r.unreadableDirs) == 0
}

// censusDirs returns the census directories, sorted. With the default
// selection, they are the parent directories of each index entry and of
// each status record. With an explicit selection, only the entries and
// the records at or below a named path count. The parents of each named
// path join too. A named path joins itself only when Lstat shows a real
// directory. So the census reads a named directory that git cannot open,
// and a named file or an absent path never counts against
// max-census-directories. A named link joins only when an index entry or
// a record below it puts the link in the list.
//
// unrecorded holds the index entries whose path is no path and no source
// path of a status record. belowLink reads them. The flag is false when
// git cannot list the index or prints a record without a tag of
// indexTags.
//
// An index entry with the skip-worktree or assume-unchanged bit adds no
// unrecorded entry. git does not read its worktree side, so no status
// record and no stderr shows a worktree change of it. The user set the
// bit on purpose. The census notes that such an entry exists, so fill can
// name it once in the selection excludes (manager ruling S2).
//
// The parent directories of such an entry join the census, unless dirState
// gives absent. git cannot list an unreadable directory, so the census
// must read it, also when it holds only such entries (ruling S2, option
// (c)). An absent parent costs at most one Lstat and does not count
// against max-census-directories. The output bound of ls-files bounds
// these Lstat calls, and the dirState cache gives one call per absent
// subtree.
func (r *treeReader) censusDirs(records []statusRecord, selected []string) (dirs, unrecorded []string, ok bool) {
	// -v puts the tag of each entry before its path, so the census can see
	// the two bits.
	args := append([]string{"--literal-pathspecs", "ls-files", "-z", "-v", "--"}, selected...)

	out, ok := r.git(nil, args...)
	if !ok {
		return nil, nil, false
	}

	seen := map[string]bool{}
	recorded := map[string]bool{}
	skippedParents := map[string]bool{}
	skipped := false

	for i := range records {
		for _, p := range []string{strings.TrimSuffix(records[i].path, "/"), records[i].origPath} {
			if p == "" {
				continue
			}

			recorded[p] = true

			if selects(selected, p) {
				addParents(seen, p)
			}
		}
	}

	for _, line := range strings.Split(string(out.Stdout), "\x00") {
		if line == "" {
			continue
		}

		// Each record is "<tag> <path>" with a tag of indexTags. A record
		// without that form is a git failure, not an empty index, so the
		// path set is not complete. The check finds a missing tag only when
		// the path does not start with a tag and a space.
		if len(line) < 3 || line[1] != ' ' || strings.IndexByte(indexTags, line[0]) < 0 {
			return nil, nil, false
		}

		if worktreeSkipped(line[0]) {
			skipped = true
			addParents(skippedParents, line[2:])

			continue
		}

		entry := line[2:]
		addParents(seen, entry)

		if !recorded[entry] {
			unrecorded = append(unrecorded, entry)
		}
	}

	for _, p := range selected {
		addParents(seen, p)

		if r.dirState(p) == pathPresent {
			seen[p] = true
		}
	}

	// A parent that only skipped entries put in the list joins when it
	// exists. A sparse checkout removes each directory outside its cone,
	// and such a directory must not count against max-census-directories.
	// A link on the way keeps the directory, and readableDir never opens
	// it. An Lstat error keeps it too, because an error is not absence.
	for dir := range skippedParents {
		if !seen[dir] && r.dirState(dir) != pathAbsent {
			seen[dir] = true
		}
	}

	dirs = make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}

	sort.Strings(dirs)

	r.skippedEntries = skipped

	return dirs, unrecorded, true
}

// indexTags holds the tags that git ls-files -v prints for an index entry.
// "H" marks a plain entry, "S" the skip-worktree bit, and "M" an unmerged
// entry. The lowercase form of a tag marks the assume-unchanged bit. The
// census reads any other first byte as a git failure, because a guess can
// hide an entry.
const indexTags = "HSMhsm"

// worktreeSkipped reports whether a git ls-files -v tag marks an index
// entry whose worktree side git does not read. "S" marks the
// skip-worktree bit, which a sparse checkout sets outside its cone. A
// lowercase letter marks the assume-unchanged bit, and "s" marks both.
// censusDirs calls it only for a tag of indexTags.
func worktreeSkipped(tag byte) bool {
	return tag == 'S' || ('a' <= tag && tag <= 'z')
}

// belowLink reports whether an index entry without a status record lies
// below a symbolic link inside root (D2 follow-up, ruling 3). git 2.50.1
// runs lstat through such a link. When the target has no search bit, git
// prints no record for the entry and exits 0, so the entry is undecided.
// A target that git can search or that is absent gives a delete record.
// An entry with the skip-worktree or assume-unchanged bit gets no record
// and no stderr below any link, so censusDirs keeps it out of entries
// (manager ruling S2). Else such an entry would make the path set
// incomplete on every run. dirState runs Lstat only, so the census opens
// nothing through the link.
func (r *treeReader) belowLink(entries []string) bool {
	for _, entry := range entries {
		// The top level "." is root itself, not a directory inside root.
		// inspect skips it for the same reason, so a root that is a link
		// puts no top-level entry below a link.
		if dir := path.Dir(entry); dir != "." && r.dirState(dir) == pathThroughLink {
			return true
		}
	}

	return false
}

// addParents adds each parent directory of p to seen, up to ".", the top
// level of the worktree. A directory in seen already has its parents in
// seen, so the walk stops there.
func addParents(seen map[string]bool, p string) {
	for dir := path.Dir(p); !seen[dir]; dir = path.Dir(dir) {
		seen[dir] = true

		if dir == "." {
			return
		}
	}
}

// below reports whether dir is a directory of list or lies below one.
// The top level "." holds every directory.
func below(dir string, list []string) bool {
	for _, top := range list {
		if top == "." || dir == top || strings.HasPrefix(dir, top+"/") {
			return true
		}
	}

	return false
}

// readableDir reports whether the producer can list dir and look up a
// name in it. A directory without the read bit does not open. A
// directory without the search bit opens and lists its names, but each
// lookup in it fails, also in an empty directory. So the census also
// looks up the name "." in the directory (searchable).
//
// An absent path, a file, and a link give true: git reports an absent
// path, and K4 inspects a link as a link, so the census never opens one.
func (r *treeReader) readableDir(dir string) bool {
	switch r.dirState(dir) {
	case pathAbsent, pathThroughLink:
		return true
	case pathUnknown:
		return false
	}

	name := r.abs(dir)

	// openFlags holds O_NOFOLLOW on unix, so a link that appears after the
	// Lstat of dirState makes the open fail and does not lead it outside.
	file, err := os.OpenFile(name, openFlags, 0)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true
	case err != nil:
		return false
	}

	defer func() { _ = file.Close() }()

	// O_NOFOLLOW stops a link only in the last name of the path. A parent
	// that becomes a link after dirState leads the open to another
	// directory, so the opened directory must be the one that Lstat saw.
	fi, err := file.Stat()
	if err != nil || !fi.IsDir() || !os.SameFile(fi, r.dirs[dir].info) {
		return false
	}

	if _, err := file.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return false
	}

	return searchable(name, fi)
}

// searchable reports whether a lookup in the directory name works. open
// is the Stat information of the directory that the census opened. The
// lookup of the name "." needs the search bit and no listed name. The
// path is joined by hand, because filepath.Join removes the "/.". The
// standard library has no lookup through an open directory on every
// unix, so the lookup goes through the path again. Its result must be
// the open directory, else a swap happened and the directory reads as
// unreadable. On windows, which has no search bit, "." is the directory.
func searchable(name string, open fs.FileInfo) bool {
	dot, err := os.Lstat(name + string(os.PathSeparator) + ".") //nolint:gocritic // filepath.Join removes the ".", which the lookup needs.

	return err == nil && os.SameFile(dot, open)
}

// cutRecords applies max-snapshot-paths to the status records. It sorts
// the records by path first, so the cut keeps the same records on every
// run.
func (r *treeReader) cutRecords(records []statusRecord, complete bool) ([]statusRecord, bool) {
	sort.SliceStable(records, func(i, j int) bool { return records[i].path < records[j].path })

	if limit := r.lim.MaxSnapshotPaths; len(records) > limit {
		r.hit(LimitMaxSnapshotPaths, int64(limit), len(records)-limit)

		return records[:limit], false
	}

	return records, complete
}

// selects reports whether a selection covers a path. An empty selection
// is the default selection, which covers every path.
func selects(selected []string, p string) bool {
	return len(selected) == 0 || inScope(p, selected)
}

// containerPaths returns the paths of a status record that hold content
// which the producer does not inspect: a submodule, a gitlink, or a
// nested repository. git prints a nested repository as one "? dir/"
// record, so the trailing "/" marks it. A link has no trailing "/".
func containerPaths(rec *statusRecord) []string {
	if rec.kind == '?' || rec.kind == '!' {
		if strings.HasSuffix(rec.path, "/") {
			return []string{strings.TrimSuffix(rec.path, "/")}
		}

		return nil
	}

	gitlink := rec.modeHead == modeGitlink || rec.modeIndex == modeGitlink || rec.modeTree == modeGitlink
	if !gitlink && !strings.HasPrefix(rec.sub, "S") {
		return nil
	}

	if rec.origPath != "" {
		return []string{rec.path, rec.origPath}
	}

	return []string{rec.path}
}

// holdsSelected reports whether an explicit path lies strictly below p.
// The default selection holds no explicit path, so it gives false.
func holdsSelected(p string, selected []string) bool {
	for _, s := range selected {
		if strings.HasPrefix(s, p+"/") {
			return true
		}
	}

	return false
}

// headEntry is one ls-tree entry of HEAD.
type headEntry struct {
	mode string
	typ  string // "blob", "tree", or "commit"
	oid  string
}

// headEntries runs step 5 for the paths whose HEAD side status does not
// give: the unmerged paths and the explicit paths that no status record
// covers. It returns an empty map in an unborn repository, where HEAD
// holds nothing. It returns nil when ls-tree fails, because the HEAD
// side of those paths is then unknown.
func (r *treeReader) headEntries(cands []*candidate, records []statusRecord, selected []string, baseline BaselineKind) map[string]headEntry {
	entries := map[string]headEntry{}

	var paths []string

	for _, c := range cands {
		if c.needsHead {
			paths = append(paths, c.path)
		}
	}

	paths = append(paths, uncovered(records, selected)...)

	if len(paths) == 0 || baseline == BaselineEmptyTree {
		return entries
	}

	args := append([]string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", "HEAD", "--"}, paths...)

	out, ok := r.git(nil, args...)
	if !ok {
		return nil
	}

	for _, tok := range strings.Split(string(out.Stdout), "\x00") {
		meta, name, found := strings.Cut(tok, "\t")
		if !found {
			continue
		}

		f := strings.Fields(meta)
		if len(f) == 3 {
			entries[name] = headEntry{mode: f[0], typ: f[1], oid: f[2]}
		}
	}

	return entries
}

// uncovered returns the explicit paths that no status record covers: no
// record path and no source path is the path or lies below it.
func uncovered(records []statusRecord, selected []string) []string {
	var out []string

	for _, p := range selected {
		covered := false

		for i := range records {
			rec := &records[i]
			if inScope(strings.TrimSuffix(rec.path, "/"), []string{p}) || (rec.origPath != "" && inScope(rec.origPath, []string{p})) {
				covered = true

				break
			}
		}

		if !covered {
			out = append(out, p)
		}
	}

	return out
}

// linkedPaths returns a candidate for each explicit path that no status
// record covers and whose worktree side the producer cannot observe:
//
//   - A path below a symbolic link. The producer does not read through
//     the link, so the change carries the limitation path-through-link
//     and an unreadable after side (contract K4).
//   - A path whose Lstat fails for a reason other than absence. git then
//     prints no record and still exits 0, so the error is the only sign.
//     An error is not absence, so the path is an unobserved change and
//     never a silent clean result (P1 review ruling 3, fix F-B).
//
// Any other uncovered path is present or absent as git saw it, so it is
// no change.
func (r *treeReader) linkedPaths(records []statusRecord, selected []string, head map[string]headEntry) []*candidate {
	var cands []*candidate

	for _, p := range uncovered(records, selected) {
		state := r.inspect(p).id.state
		if state != pathThroughLink && state != pathUnknown {
			continue
		}

		// resolve reads the pathUnknown state again and marks the change
		// unobserved, so only a link needs the marker here.
		c := &candidate{path: p, kind: ChangeAdd, throughLink: state == pathThroughLink}
		if e, ok := head[p]; ok && e.typ == "blob" {
			c.kind, c.headMode, c.headOid = ChangeModify, e.mode, e.oid
		}

		cands = append(cands, c)
	}

	return cands
}

// applyLstatRule decides the candidates of the Lstat rule (contract K4,
// §5 row 125): an absent worktree path is a delete; a present one is a
// modify until the comparison of the blob id, the type, and the
// executable bit shows no difference. A submodule that is still present
// is no change, because the producer does not inspect its commit. The
// snapshot then names that limitation when the selection covers the path.
// fill names it when a selected path lies inside the submodule.
func (r *treeReader) applyLstatRule(cands []*candidate, selected []string) []*candidate {
	out := cands[:0]

	for _, c := range cands {
		if !c.lstatRule {
			out = append(out, c)

			continue
		}

		state := r.inspect(c.path).id.state

		switch {
		case state == pathAbsent:
			c.kind = ChangeDelete
		case state == pathThroughLink:
			// git does not look through a link either, so the path is
			// absent for git. The limitation tells the reader why.
			c.kind = ChangeDelete
			c.limitations = append(c.limitations, LimitationPathThroughLink)
		case c.headMode == modeGitlink:
			if selects(selected, c.path) {
				r.limitations[LimitationSubmoduleContentNotInspected] = true
			}

			continue
		default:
			c.kind = ChangeModify
			c.dropIfSame = true
		}

		out = append(out, c)
	}

	return out
}

// blobInfo is the --batch-check answer for one HEAD object.
type blobInfo struct {
	size  int64
	found bool
}

// blobSizes runs the first half of step 4: one `cat-file --batch-check`
// for every HEAD blob that a candidate names. Move pairing compares the
// sizes, and every before side reports its size. A failed call leaves
// the map empty, and every before side that needs content is then
// unreadable.
func (r *treeReader) blobSizes(cands []*candidate) map[string]blobInfo {
	sizes := map[string]blobInfo{}

	var oids []string

	for _, c := range cands {
		t := typeOfMode(c.headMode)
		if (t == ObjectFile || t == ObjectSymlink) && c.headOid != "" {
			oids = append(oids, c.headOid)
		}
	}

	oids = sortedUnique(oids)
	if len(oids) == 0 {
		return sizes
	}

	out, ok := r.git([]byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch-check")
	if !ok {
		return sizes
	}

	for _, line := range strings.Split(string(out.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			continue
		}

		if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			sizes[f[0]] = blobInfo{size: n, found: true}
		}
	}

	return sizes
}

// pairMoves pairs a deleted path with an untracked path when the git blob
// id of the untracked content equals the HEAD blob id of the deleted path
// and both types match (contract K4, unstaged move). An id that more than
// one deleted path or more than one untracked path holds is not paired.
// Only untracked files whose size equals a deleted blob size are read.
// Under an explicit selection, a pair needs one end in the selection.
func (r *treeReader) pairMoves(cands []*candidate, selected []string, sizes map[string]blobInfo) []*candidate {
	if r.format == "" {
		return cands
	}

	type key struct {
		typ ObjectType
		oid string
	}

	deleted := map[key][]*candidate{}
	deletedSizes := map[int64][]*candidate{}

	for _, c := range cands {
		t := typeOfMode(c.headMode)
		if c.kind != ChangeDelete || c.throughLink || len(c.limitations) > 0 || (t != ObjectFile && t != ObjectSymlink) {
			continue
		}

		info := sizes[c.headOid]
		if !info.found {
			continue
		}

		deleted[key{t, c.headOid}] = append(deleted[key{t, c.headOid}], c)
		deletedSizes[info.size] = append(deletedSizes[info.size], c)
	}

	if len(deleted) == 0 {
		return cands
	}

	added := map[key][]*candidate{}

	for _, c := range sortedCandidates(cands) {
		if !c.untracked {
			continue
		}

		f := r.inspect(c.path)
		if f.id.state != pathPresent || !wantsPairing(c, deletedSizes[f.size], selected) {
			continue
		}

		f = r.load(c.path)
		if f.blobID != "" {
			added[key{f.typ, f.blobID}] = append(added[key{f.typ, f.blobID}], c)
		}
	}

	paired := map[*candidate]bool{}

	for k, srcs := range deleted {
		dsts := added[k]
		if len(srcs) != 1 || len(dsts) != 1 {
			continue
		}

		src, dst := srcs[0], dsts[0]
		dst.oldPath, dst.kind, dst.move = src.path, ChangeMove, MoveIdenticalContent
		dst.headMode, dst.headOid, dst.untracked = src.headMode, src.headOid, false
		paired[src] = true
	}

	out := cands[:0]

	for _, c := range cands {
		if !paired[c] {
			out = append(out, c)
		}
	}

	return out
}

// wantsPairing reports whether an untracked path is worth a read for move
// pairing: a deleted path of the same size exists, and under an explicit
// selection the untracked path or that deleted path is selected.
func wantsPairing(c *candidate, sameSize []*candidate, selected []string) bool {
	if len(sameSize) == 0 {
		return false
	}

	if len(selected) == 0 || inScope(c.path, selected) {
		return true
	}

	for _, d := range sameSize {
		if inScope(d.path, selected) {
			return true
		}
	}

	return false
}

// keepSelected drops the candidates outside an explicit selection. A move
// stays when one of its two ends is selected, so both ends show. A
// submodule or a nested repository stays when a selected path lies
// inside it, because git gives no other form of a change to that path.
// containers holds the paths that containerPaths gives.
func keepSelected(cands []*candidate, selected []string, containers map[string]bool) []*candidate {
	if len(selected) == 0 {
		return cands
	}

	out := cands[:0]

	for _, c := range cands {
		holder := c.headMode == modeGitlink || c.treeMode == modeGitlink || containers[c.path]

		switch {
		case inScope(c.path, selected), c.oldPath != "" && inScope(c.oldPath, selected):
			out = append(out, c)
		case holder && (holdsSelected(c.path, selected) || (c.oldPath != "" && holdsSelected(c.oldPath, selected))):
			out = append(out, c)
		}
	}

	return out
}

// sortedCandidates returns the candidates sorted by path and old path.
// Each read in that order takes its share of max-snapshot-bytes, so the
// same tree gives the same snapshot on every run.
func sortedCandidates(cands []*candidate) []*candidate {
	out := append([]*candidate(nil), cands...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}

		return out[i].oldPath < out[j].oldPath
	})

	return out
}

// changes resolves each candidate into a Change: it reads the worktree
// side (step 6), compares it with the HEAD side, reads the HEAD content
// that a checker needs (step 4), and drops a candidate that shows no
// difference. The result is sorted by path and old path.
//
// A candidate whose content comparison stays open after the HEAD read is
// undecided: a byte limit or a read error hid one side. It is not a
// change, because K3 counts only an observed difference. It makes the
// path set incomplete instead, so no checker gives pass or violation
// for it (review fixes checkers:K3-F1, snapshot-git:F4).
func (r *treeReader) changes(cands []*candidate, sizes map[string]blobInfo) []Change {
	type pending struct {
		ch         Change
		headOid    string
		readHead   bool
		dropIfSame bool
		open       bool // the content comparison waits for both SHA-256 values
	}

	var list []pending

	for _, c := range sortedCandidates(cands) {
		if r.done() {
			return []Change{}
		}

		ch, keep, open := r.resolve(c, sizes)
		if !keep {
			continue
		}

		// Only a file or a symlink in HEAD has a blob to read. A delete
		// adds no line, so no checker needs its HEAD content. An open
		// comparison without an after SHA-256 cannot close, so the HEAD
		// read would only use up max-snapshot-bytes.
		needsContent := ch.Kind != ChangeDelete && (ch.ContentChanged || ch.TypeChanged)
		readHead := needsContent && hasBlob(ch.Before.Type) && ch.Before.Content == ContentNotRead &&
			(!open || ch.After.SHA256 != "")
		list = append(list, pending{ch: ch, headOid: c.headOid, readHead: readHead, dropIfSame: c.dropIfSame, open: open})
	}

	var oids []string

	for _, p := range list {
		if p.readHead {
			oids = append(oids, p.headOid)
		}
	}

	blobs := r.readBlobs(oids, sizes)
	out := make([]Change, 0, len(list))

	for _, p := range list {
		ch := p.ch
		if p.readHead {
			ch.Before = withBlob(ch.Before, blobs[p.headOid])
			// Without a blob id, the comparison waits for both contents.
			if ch.Before.SHA256 != "" && ch.After.SHA256 != "" {
				ch.ContentChanged = ch.Before.SHA256 != ch.After.SHA256
			}

			// Without a blob id (git older than 2.25), the drop of an
			// unchanged path waits for this content comparison.
			if p.dropIfSame && !ch.ContentChanged && !ch.ModeChanged && !ch.TypeChanged {
				continue
			}
		}

		if p.open && (ch.Before.SHA256 == "" || ch.After.SHA256 == "") {
			r.undecide(ch)

			continue
		}

		out = append(out, ch)
	}

	return out
}

// undecide records a candidate whose content comparison did not run. The
// path set is then not complete. The first named limit in path order
// becomes the cause, because a named limit tells the reader what to
// raise, and a read error has no name.
func (r *treeReader) undecide(ch Change) {
	r.undecided = true

	if r.undecidedLimit == "" {
		r.undecidedLimit = undecidedCause(ch, r.lim)
	}
}

// undecidedCause returns the limit that hid a side of an undecided
// change: max-file-bytes when a too-large side is above it, else
// max-snapshot-bytes. It returns "" when no side is too-large, for
// example when the read failed.
func undecidedCause(ch Change, lim Limits) string {
	cause := ""

	for _, s := range []Side{ch.Before, ch.After} {
		switch {
		case s.Content != ContentTooLarge:
		case s.Size > lim.MaxFileBytes:
			return LimitMaxFileBytes
		default:
			cause = LimitMaxSnapshotBytes
		}
	}

	return cause
}

// sizesDiffer reports whether the worktree size proves a content change
// against the HEAD blob: the raw sizes differ, so the blob ids differ
// too. afterSize is -1 when the producer has no size. A size that is a
// lower bound proves a change only when it is above the HEAD size.
func sizesDiffer(afterSize int64, bound bool, head blobInfo) bool {
	switch {
	case afterSize < 0 || !head.found:
		return false
	case bound:
		return afterSize > head.size
	default:
		return afterSize != head.size
	}
}

// hasBlob reports whether a side of this type has a git blob: the file
// content, or the target of a symlink.
func hasBlob(t ObjectType) bool {
	return t == ObjectFile || t == ObjectSymlink
}

// unmergedHeads fills the HEAD side of each unmerged path from step 5,
// before step 4 asks for the blob sizes. A path that HEAD lacks has an
// absent before side. When ls-tree failed, the before side is a file
// without an object id, so it reads as unreadable and never as absent.
func unmergedHeads(cands []*candidate, head map[string]headEntry) {
	for _, c := range cands {
		if !c.needsHead {
			continue
		}

		if head == nil {
			c.headMode, c.headOid = modeFile, ""

			continue
		}

		if e, ok := head[c.path]; ok && e.typ != "tree" {
			c.headMode, c.headOid = e.mode, e.oid
		}
	}
}

// resolve builds the Change of one candidate. keep is false when the
// candidate shows no difference against HEAD. open is true when only a
// content comparison can tell whether a modify candidate is a change:
// no blob id decides it, git did not observe the difference, and the
// sizes, the mode, and the type are equal. The caller then compares the
// content SHA-256 values after the HEAD read.
func (r *treeReader) resolve(c *candidate, sizes map[string]blobInfo) (ch Change, keep, open bool) {
	ch = Change{Path: c.path, OldPath: c.oldPath, Kind: c.kind, Move: c.move, Limitations: c.limitations}
	ch.Before = headSide(c)

	// The size of a HEAD blob comes from --batch-check, so a before side
	// that the producer does not read still reports its size.
	if info := sizes[c.headOid]; info.found && hasBlob(ch.Before.Type) {
		ch.Before.Size = info.size
	}

	var (
		blobID string
		// afterSize is the worktree size for the size test, -1 when the
		// producer has none. sizeBound marks a lower bound.
		afterSize int64 = -1
		sizeBound bool
	)

	switch {
	case c.kind == ChangeDelete:
		ch.After = Side{Type: ObjectAbsent, Content: ContentNotRead}
	case c.throughLink:
		// The closed type list has no unknown type, so the after side is
		// an unreadable file. The marker keeps forbid from reading it as
		// an observed difference.
		ch.After = Side{Type: ObjectFile, Content: ContentUnreadable}
		ch.Limitations = append(ch.Limitations, LimitationPathThroughLink)
		ch.unobserved = true
	case c.treeMode == modeGitlink:
		ch.After = Side{Type: ObjectGitlink, Mode: modeGitlink, Oid: c.treeOid, Content: ContentNotRead}
	default:
		f := r.load(c.path)
		ch.After, blobID = f.side(c.treeMode)

		if f.id.state == pathThroughLink {
			ch.Limitations = append(ch.Limitations, LimitationPathThroughLink)
		}

		// An Lstat error other than absence hides the worktree side, so
		// the producer observes no difference. The marker keeps forbid from
		// reading the change as a violation (P1 review ruling 3, fix F-B).
		if f.id.state == pathUnknown {
			ch.unobserved = true
		}

		if f.id.state == pathPresent && hasBlob(f.typ) {
			afterSize, sizeBound = f.size, f.sizeBound
		}
	}

	ch.Kind = finalKind(ch)
	if ch.Kind == "" {
		return Change{}, false, false
	}

	if ch.Kind == ChangeMove && ch.After.Type == ObjectAbsent {
		// The destination vanished after status: only the source is a
		// change.
		ch.Path, ch.OldPath, ch.Kind, ch.Move = ch.OldPath, "", ChangeDelete, MoveNone
	}

	setFlags(&ch, c.headOid, blobID)

	if c.dropIfSame && !ch.ContentChanged && !ch.ModeChanged && !ch.TypeChanged {
		return Change{}, false, false
	}

	// A mode or type difference, git's own observation, or unequal sizes
	// prove a change without a content comparison.
	open = c.dropIfSame && ch.Kind == ChangeModify && !ch.ModeChanged && !ch.TypeChanged &&
		(blobID == "" || c.headOid == "") && !c.gitObserved && !sizesDiffer(afterSize, sizeBound, sizes[c.headOid])

	return ch, true, open
}

// finalKind returns the kind that the two sides show. Status gives the
// kind, but a worktree side can turn out absent: an unmerged path, a
// path that vanished after status. It returns "" when both sides are
// absent.
func finalKind(ch Change) ChangeKind {
	before, after := ch.Before.Type != ObjectAbsent, ch.After.Type != ObjectAbsent

	switch {
	case !before && !after:
		return ""
	case !before:
		return ChangeAdd
	case !after && ch.Kind != ChangeMove:
		return ChangeDelete
	case ch.Kind == ChangeAdd:
		return ChangeModify
	default:
		return ch.Kind
	}
}

// setFlags sets the content, mode, and type flags of a change with two
// present sides. Content compares the git blob id of the worktree side
// with the HEAD blob id. When no blob id exists, the content counts as
// changed until both contents are read, because a pass must never rest
// on an equality that the producer did not see. For a candidate that
// only a content comparison can decide, changes then compares the
// SHA-256 values or makes the path undecided. Equal content gives the
// before side the SHA-256 of the after side, so the producer does not
// read the HEAD blob.
func setFlags(ch *Change, headOid, blobID string) {
	if ch.Kind == ChangeAdd || ch.Kind == ChangeDelete {
		ch.ContentChanged = true

		return
	}

	ch.TypeChanged = ch.Before.Type != ch.After.Type
	ch.ModeChanged = ch.Before.Type == ObjectFile && ch.After.Type == ObjectFile && ch.Before.Mode != ch.After.Mode

	switch {
	case ch.Before.Type == ObjectGitlink || ch.After.Type == ObjectGitlink:
		ch.ContentChanged = ch.After.Oid == "" || ch.Before.Oid != ch.After.Oid
	case blobID != "" && headOid != "":
		ch.ContentChanged = blobID != headOid
	default:
		ch.ContentChanged = true
	}

	if !ch.ContentChanged && !ch.TypeChanged && ch.After.SHA256 != "" {
		ch.Before.SHA256 = ch.After.SHA256
	}
}

// headSide returns the before side that status or ls-tree gives, without
// content. Step 4 reads the content when a checker needs it.
func headSide(c *candidate) Side {
	t := typeOfMode(c.headMode)
	if t == ObjectAbsent {
		return Side{Type: ObjectAbsent, Content: ContentNotRead}
	}

	return Side{Type: t, Mode: c.headMode, Oid: c.headOid, Content: ContentNotRead}
}

// headBlob is the result of one HEAD blob read.
type headBlob struct {
	size    int64
	state   ContentState
	content []byte
	known   bool // the --batch-check answer gave a size
}

// readBlobs runs the second half of step 4: `cat-file --batch` for each
// blob that a checker needs. A blob above max-file-bytes or above the
// rest of max-snapshot-bytes is not requested and reads as too-large
// with its true size. The calls are split so that each output fits in
// max-git-output-bytes.
func (r *treeReader) readBlobs(oids []string, sizes map[string]blobInfo) map[string]headBlob {
	blobs := map[string]headBlob{}

	var want []string

	for _, oid := range sortedUnique(oids) {
		info := sizes[oid]

		switch {
		case oid == "" || !info.found:
			blobs[oid] = headBlob{state: ContentUnreadable}
		case info.size > r.lim.MaxFileBytes:
			blobs[oid] = headBlob{size: info.size, state: ContentTooLarge, known: true}
			r.hit(LimitMaxFileBytes, r.lim.MaxFileBytes, 1)
		case info.size > r.budget:
			blobs[oid] = headBlob{size: info.size, state: ContentTooLarge, known: true}
			r.hit(LimitMaxSnapshotBytes, r.lim.MaxSnapshotBytes, 1)
		default:
			r.budget -= info.size
			blobs[oid] = headBlob{size: info.size, state: ContentUnreadable, known: true}
			want = append(want, oid)
		}
	}

	for len(want) > 0 {
		n, total := 0, int64(0)

		for n < len(want) {
			next := batchOutputSize(want[n], sizes[want[n]].size)
			if n > 0 && total+next > r.lim.MaxGitOutputBytes {
				break
			}

			total += next
			n++
		}

		r.readBatch(want[:n], blobs)
		want = want[n:]
	}

	return blobs
}

// batchOutputSize is the exact --batch output of one blob: the header
// "<oid> blob <size>\n", the content, and a newline.
func batchOutputSize(oid string, size int64) int64 {
	return int64(len(oid)+len(" blob ")+len(strconv.FormatInt(size, 10))+1) + size + 1
}

// readBatch reads the content of the given blobs with one `cat-file
// --batch` call. A blob that the call does not return stays unreadable.
func (r *treeReader) readBatch(oids []string, blobs map[string]headBlob) {
	out, ok := r.git([]byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch")
	if !ok {
		return
	}

	rest := out.Stdout

	for len(rest) > 0 {
		header, body, found := bytes.Cut(rest, []byte("\n"))
		if !found {
			return
		}

		f := strings.Fields(string(header))
		if len(f) != 3 || f[1] != "blob" {
			// A "missing" answer has two fields and no body.
			rest = body

			continue
		}

		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || size < 0 || int64(len(body)) < size+1 {
			return
		}

		// The content stays for the SHA-256 also when it is not text.
		content := body[:size]
		b := blobs[f[0]]
		b.size, b.state, b.content = size, textState(content), content
		blobs[f[0]] = b
		rest = body[size+1:]
	}
}

// withBlob fills the content of a before side from a HEAD blob read. A
// blob without a read result, such as a side without an object id, is
// unreadable, because a before side must never claim a state that the
// producer did not see.
func withBlob(side Side, b headBlob) Side {
	if b.known {
		side.Size = b.size
	}

	side.Content = b.state
	if side.Content == "" {
		side.Content = ContentUnreadable
	}

	if b.content != nil {
		sum := sha256.Sum256(b.content)
		side.SHA256 = hex.EncodeToString(sum[:])
	}

	if b.state == ContentAvailable {
		side.text = b.content
	}

	return side
}

// worktreeFile is what the producer saw at one worktree path.
type worktreeFile struct {
	id   fileID
	info fs.FileInfo // the Lstat result; nil unless the path is present
	typ  ObjectType  // file, symlink, or other for a present path
	exec bool
	size int64
	// sizeBound is true when size is a lower bound: the file grew after
	// Lstat, and the read stopped at its bound.
	sizeBound bool
	// loaded is true after the producer tried to read the content.
	loaded bool
	state  ContentState
	text   []byte // the content when state is available
	sha256 string // the hex SHA-256 of the whole content, when read
	blobID string // the git blob id in the repository object format
}

// abs returns the absolute form of a repository-relative path.
func (r *treeReader) abs(rel string) string {
	return filepath.Join(r.root, filepath.FromSlash(rel))
}

// inspect runs Lstat on a path and on each parent below root, without a
// content read. It caches the result per path.
func (r *treeReader) inspect(rel string) *worktreeFile {
	if f, ok := r.files[rel]; ok {
		return f
	}

	f := &worktreeFile{id: fileID{path: rel}}
	r.files[rel] = f

	if dir := path.Dir(rel); dir != "." {
		if f.id.state = r.dirState(dir); f.id.state != pathPresent {
			return f
		}
	}

	fi, err := os.Lstat(r.abs(rel))

	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.id.state = pathAbsent

		return f
	case err != nil:
		f.id.state = pathUnknown

		return f
	}

	f.id.state = pathPresent
	f.info = fi
	f.size = fi.Size()
	f.id.size, f.id.mtimeNS, f.id.mode, f.id.inode = fi.Size(), fi.ModTime().UnixNano(), uint32(fi.Mode()), inodeOf(fi)

	switch {
	case fi.Mode().IsRegular():
		f.typ = ObjectFile
		// git keeps one executable bit and reads the owner bit for it.
		f.exec = fi.Mode().Perm()&0o100 != 0
	case fi.Mode()&fs.ModeSymlink != 0:
		f.typ = ObjectSymlink
	default:
		f.typ = ObjectOther
	}

	return f
}

// dirLook is what dirState found at one directory. info is the Lstat
// information of a real directory and nil in each other state. The
// census compares it with the directory that it opens.
type dirLook struct {
	state pathState
	info  fs.FileInfo
}

// dirState returns whether a directory below root is a real directory.
// A symbolic link at any level gives pathThroughLink, because the
// producer never looks through a link (§5 row 17).
func (r *treeReader) dirState(dir string) pathState {
	if look, ok := r.dirs[dir]; ok {
		return look.state
	}

	look := dirLook{state: pathPresent}

	if parent := path.Dir(dir); parent != "." {
		look.state = r.dirState(parent)
	}

	if look.state == pathPresent {
		fi, err := os.Lstat(r.abs(dir))

		switch {
		case errors.Is(err, fs.ErrNotExist):
			look.state = pathAbsent
		case err != nil:
			look.state = pathUnknown
		case fi.Mode()&fs.ModeSymlink != 0:
			look.state = pathThroughLink
		case !fi.IsDir():
			// A file where a directory should be: nothing exists below it.
			look.state = pathAbsent
		default:
			look.info = fi
		}
	}

	r.dirs[dir] = look

	return look.state
}

// load inspects a path and reads its content once (step 6). A symbolic
// link reads as its target string and is never followed. A regular file
// opens without following a link and reads at most max-file-bytes + 1.
// Each read takes its size from max-snapshot-bytes.
func (r *treeReader) load(rel string) *worktreeFile {
	f := r.inspect(rel)
	if f.loaded || f.id.state != pathPresent || (f.typ != ObjectFile && f.typ != ObjectSymlink) {
		return f
	}

	f.loaded = true
	f.state = ContentUnreadable

	if r.done() {
		return f
	}

	if f.typ == ObjectSymlink {
		target, err := os.Readlink(r.abs(rel))
		if err == nil {
			r.accept(f, []byte(target))
		}

		return f
	}

	switch {
	case f.size > r.lim.MaxFileBytes:
		f.state = ContentTooLarge
		r.hit(LimitMaxFileBytes, r.lim.MaxFileBytes, 1)

		return f
	case f.size > r.budget:
		f.state = ContentTooLarge
		r.hit(LimitMaxSnapshotBytes, r.lim.MaxSnapshotBytes, 1)

		return f
	}

	data, err := readNoFollow(r.abs(rel), f.info, min(r.lim.MaxFileBytes, r.budget)+1)
	if err != nil {
		return f
	}

	if int64(len(data)) > min(r.lim.MaxFileBytes, r.budget) {
		// The file grew after Lstat. Its true size is not known, so the
		// read size is a lower bound.
		f.size = int64(len(data))
		f.sizeBound = true
		f.state = ContentTooLarge

		if f.size > r.lim.MaxFileBytes {
			r.hit(LimitMaxFileBytes, r.lim.MaxFileBytes, 1)
		} else {
			r.hit(LimitMaxSnapshotBytes, r.lim.MaxSnapshotBytes, 1)
		}

		return f
	}

	r.accept(f, data)

	return f
}

// accept records the content of a read: its size, text state, SHA-256,
// and git blob id.
func (r *treeReader) accept(f *worktreeFile, data []byte) {
	r.budget -= int64(len(data))
	f.size = int64(len(data))
	f.state = textState(data)

	if f.state == ContentAvailable {
		f.text = data
	}

	sum := sha256.Sum256(data)
	f.sha256 = hex.EncodeToString(sum[:])
	f.id.sha256 = f.sha256
	f.blobID = gitBlobID(r.format, data)
}

// errFileChanged reports a file that changed between Lstat and open.
var errFileChanged = errors.New("lessoncheck: the file changed during the read")

// readNoFollow opens a regular file without following a symbolic link
// and reads at most limit bytes. It refuses a file that is not the one
// that Lstat saw, because a swap after the Lstat must not redirect the
// read.
func readNoFollow(name string, want fs.FileInfo, limit int64) ([]byte, error) {
	file, err := os.OpenFile(name, openFlags, 0)
	if err != nil {
		return nil, err
	}

	defer func() { _ = file.Close() }()

	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if !fi.Mode().IsRegular() || !os.SameFile(fi, want) {
		return nil, errFileChanged
	}

	return io.ReadAll(io.LimitReader(file, limit))
}

// side returns the after side of a worktree path. treeMode is the mode
// that status gives for a tracked path, because it honours
// core.fileMode. An untracked path takes its mode from Lstat. blobID is
// the git blob id of the content, "" when unknown.
func (f *worktreeFile) side(treeMode string) (after Side, blobID string) {
	switch f.id.state {
	case pathAbsent:
		return Side{Type: ObjectAbsent, Content: ContentNotRead}, ""
	case pathThroughLink, pathUnknown:
		return Side{Type: ObjectFile, Content: ContentUnreadable}, ""
	}

	s := Side{Type: f.typ, Size: f.size, Content: f.state, SHA256: f.sha256, text: f.text}

	switch f.typ {
	case ObjectFile:
		s.Mode = modeFile
		if f.exec {
			s.Mode = modeExec
		}

		if typeOfMode(treeMode) == ObjectFile {
			s.Mode = treeMode
		}
	case ObjectSymlink:
		s.Mode = modeSymlink
	default:
		s.Content = ContentNotRead
	}

	return s, f.blobID
}

// gitBlobID returns the git blob id of content in the object format of
// the repository: the hash of "blob <size>\x00" and the content. It
// returns "" for an unknown format.
func gitBlobID(format string, content []byte) string {
	var h hash.Hash

	switch format {
	case "sha1":
		h = sha1.New() //nolint:gosec // the git object id, not a security digest.
	case "sha256":
		h = sha256.New()
	default:
		return ""
	}

	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)

	return hex.EncodeToString(h.Sum(nil))
}

// limitHits returns the limit hits sorted by name.
func (r *treeReader) limitHits() []LimitHit {
	if len(r.hits) == 0 {
		return nil
	}

	out := make([]LimitHit, 0, len(r.hits))
	for _, h := range r.hits {
		out = append(out, *h)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// limitationList returns the snapshot limitations, sorted.
func (r *treeReader) limitationList() []string {
	var out []string

	for l := range r.limitations {
		out = append(out, l)
	}

	sort.Strings(out)

	return out
}

// identityList returns the identity of every inspected path, sorted.
func (r *treeReader) identityList() []fileID {
	out := make([]fileID, 0, len(r.files))
	for _, f := range r.files {
		out = append(out, f.id)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })

	return out
}

// sortedUnique returns the values sorted, without duplicates and without
// the empty string.
func sortedUnique(values []string) []string {
	seen := map[string]bool{}

	var out []string

	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	sort.Strings(out)

	return out
}
