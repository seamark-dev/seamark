package lessoncheck

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the snapshot and change model of lesson checks. A
// snapshot is the one immutable input of Evaluate. A producer reads it
// once, with bounds, and labels it with its boundary, its baseline, and
// its selection. ReadWorkingTree produces the working-tree boundary,
// ProjectOperation the proposed-edit boundary, and ObserveAfter the
// actual-edit boundary.

// SnapshotSchema is the format name of a snapshot. It is also the label
// of the snapshot digest.
const SnapshotSchema = "lesson-snapshot/1"

// Boundary names the state that a snapshot shows. A result of one
// boundary never counts as a result of another boundary.
type Boundary string

// The three boundaries.
const (
	// BoundaryProposedEdit is the projected result of one pending native
	// edit, before the tool writes it.
	BoundaryProposedEdit Boundary = "proposed-edit"
	// BoundaryActualEdit is the observed result of one native edit,
	// against the baseline that the pre-edit hook stored.
	BoundaryActualEdit Boundary = "actual-edit"
	// BoundaryWorkingTree is the working tree against HEAD, or against the
	// empty tree in an unborn repository.
	BoundaryWorkingTree Boundary = "working-tree"
)

// BaselineKind names the before state of a snapshot.
type BaselineKind string

// The closed list of baseline kinds.
const (
	BaselineHead      BaselineKind = "head"
	BaselineEmptyTree BaselineKind = "empty-tree"
	BaselinePreEdit   BaselineKind = "pre-edit"
)

// SelectionKind names how a producer chose the paths of a snapshot.
type SelectionKind string

// The closed list of selection kinds.
const (
	SelectionDefault       SelectionKind = "default"
	SelectionExplicitPaths SelectionKind = "explicit-paths"
	SelectionOperation     SelectionKind = "operation"
)

// ObjectType is the type of one side of a change.
type ObjectType string

// The closed list of object types.
const (
	ObjectAbsent  ObjectType = "absent"
	ObjectFile    ObjectType = "file"
	ObjectSymlink ObjectType = "symlink"
	ObjectGitlink ObjectType = "gitlink"
	ObjectOther   ObjectType = "other"
)

// ContentState tells whether the content of one side is available as
// text, and why not when it is not.
type ContentState string

// The closed list of content states.
const (
	// ContentAvailable means that the producer read the content and the
	// content is UTF-8 text without a NUL byte.
	ContentAvailable ContentState = "available"
	// ContentBinary means that the content holds a NUL byte.
	ContentBinary ContentState = "binary"
	// ContentInvalidUTF8 means that the content is not valid UTF-8.
	ContentInvalidUTF8 ContentState = "invalid-utf8"
	// ContentTooLarge means that a byte limit stopped the read. The side
	// is above max-file-bytes, or the snapshot reached max-snapshot-bytes.
	ContentTooLarge ContentState = "too-large"
	// ContentUnreadable means that the read failed: the path vanished, a
	// permission error, or a path through a link.
	ContentUnreadable ContentState = "unreadable"
	// ContentNotRead means that the producer did not need the content,
	// for example for a side whose content is unchanged.
	ContentNotRead ContentState = "not-read"
)

// MoveDetection names how a producer found a move.
type MoveDetection string

// The closed list of move detections. A change that is not a move has
// the empty value.
const (
	MoveNone             MoveDetection = ""
	MoveGitRename        MoveDetection = "git-rename"
	MoveIdenticalContent MoveDetection = "identical-content"
	MoveProjection       MoveDetection = "projection"
)

// The closed list of change limitations (Change.Limitations).
const (
	LimitationUnmerged          = "unmerged"
	LimitationPathThroughLink   = "path-through-link"
	LimitationProjectionDiffers = "projection-differs"
	LimitationNoProjection      = "no-projection"
)

// The closed list of snapshot limitations (Snapshot.Limitations).
const (
	LimitationMovePairingUnavailable       = "move-pairing-unavailable"
	LimitationSubmoduleContentNotInspected = "submodule-content-not-inspected"
	// LimitationGitStderr is a git status call that exited 0 and wrote
	// text to stderr. It is a notice only. It never makes the path set
	// incomplete, because a warning that no rerun clears must not fail
	// every run. The text output shows the first line.
	LimitationGitStderr = "git-stderr"
)

// Baseline is the before state of a snapshot.
type Baseline struct {
	Kind   BaselineKind `json:"kind"`
	Commit string       `json:"commit,omitempty"` // HEAD commit oid for "head"
	// PendingSHA is set when the pre-edit state comes from a pending
	// record: the SHA-256 of the pending file bytes. A projection also
	// uses kind "pre-edit" but reads the pre-edit state from disk, so
	// PendingSHA stays "" there.
	PendingSHA string `json:"pending_sha256,omitempty"`
}

// Selection names the paths that a snapshot covers and what it leaves
// out. Each kind has fixed texts, so a reader always sees the same
// words for the same selection. Excludes gets one more fixed text when
// the selection holds an index entry with the skip-worktree or
// assume-unchanged bit.
type Selection struct {
	Kind        SelectionKind `json:"kind"`
	Description string        `json:"description"`     // fixed text per kind
	Paths       []string      `json:"paths,omitempty"` // explicit --file paths or operation paths, sorted
	Excludes    []string      `json:"excludes"`        // fixed texts
}

// Side is one end of a change: the before state or the after state.
type Side struct {
	Type    ObjectType   `json:"type"`
	Mode    string       `json:"mode,omitempty"`   // git octal: "100644", "100755", "120000", "160000"
	Oid     string       `json:"oid,omitempty"`    // git object id when the side comes from git
	SHA256  string       `json:"sha256,omitempty"` // of the raw content (link target for a symlink)
	Size    int64        `json:"size"`
	Content ContentState `json:"content"`
	// text holds the raw content when Content is available. It is
	// unexported, so no JSON output and no record ever carries file
	// content.
	text []byte
	// limit names the limit that left the content out of a too-large
	// side when the size of the side does not show it. Only ObserveAfter
	// sets it: a pending record drops content above max-baseline-bytes,
	// and the size of such a file is below max-file-bytes. It is
	// unexported, so neither the JSON output nor the digest holds it.
	limit string
}

// Change is one observed or projected change of one path, or of a pair
// of paths for a move.
type Change struct {
	Path           string        `json:"path"`
	OldPath        string        `json:"old_path,omitempty"` // the source of a move
	Kind           ChangeKind    `json:"kind"`
	Before         Side          `json:"before"`
	After          Side          `json:"after"`
	ContentChanged bool          `json:"content_changed"`
	ModeChanged    bool          `json:"mode_changed"`
	TypeChanged    bool          `json:"type_changed"`
	Move           MoveDetection `json:"move_detection,omitempty"`
	Limitations    []string      `json:"limitations,omitempty"` // closed: the change limitations above
	// unobserved is true when the producer made the change without an
	// observed difference. Two cases do that: a --file path below a
	// symbolic link that no status record covers, and a worktree path
	// whose Lstat failed for a reason other than absence.
	// forbid-path-change/1 reads it as undecided, because only an
	// observed difference counts as a change. It is unexported, so
	// neither the JSON output nor the digest holds it.
	unobserved bool
	// unprojectable is set when ProjectOperation cannot compute the after
	// content of the change. It holds the detail for the deny text, for
	// example "old_string occurs 2 times". no-added-pattern/1 reads a set
	// value as unprojectable-edit. forbid-path-change/1 ignores it,
	// because a projected change is a change whatever its content. It is
	// unexported, so neither the JSON output nor the digest holds it.
	unprojectable string
}

// sortChanges sorts changes by path and then by old path. The sort is
// stable, so two changes with the same paths keep the producer order.
// ProjectOperation, ObserveAfter, and the scope filter touching use it.
// ReadWorkingTree sorts its candidates by the same rule in
// sortedCandidates. So every snapshot and every checker sees one order.
func sortChanges(changes []Change) {
	slices.SortStableFunc(changes, func(a, b Change) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.OldPath, b.OldPath))
	})
}

// setChangeFlags sets the change flags from the two sides. A side
// without a known digest counts as changed content, because the
// producer cannot show that the content stays the same. The observer
// uses it as it is, and setProjectedFlags adds the projection rules.
func setChangeFlags(ch *Change) {
	known := ch.Before.SHA256 != "" && ch.After.SHA256 != ""
	ch.ContentChanged = !known || ch.Before.SHA256 != ch.After.SHA256

	present := ch.Before.Type != ObjectAbsent && ch.After.Type != ObjectAbsent
	ch.ModeChanged = present && ch.Before.Mode != "" && ch.After.Mode != "" && ch.Before.Mode != ch.After.Mode
	ch.TypeChanged = present && ch.Before.Type != ch.After.Type
}

// withLimitation adds a limitation once and keeps the list sorted. It
// can change the array of list in place, so a caller that shares the
// slice passes a copy. The producers use it for the change limitations,
// and WritePending uses it for the record limitations.
func withLimitation(list []string, limitation string) []string {
	if slices.Contains(list, limitation) {
		return list
	}

	list = append(list, limitation)
	slices.Sort(list)

	return list
}

// LimitHit names one evaluation limit that a producer reached.
type LimitHit struct {
	Name  string `json:"name"` // a limit name, such as LimitMaxSnapshotPaths
	Value int64  `json:"value"`
	Paths int    `json:"paths"` // how many paths it left undecided
}

// Snapshot is one immutable, bounded input of Evaluate. Changes holds
// observed differences only. An unchanged path named by --file is not a
// change, so a checker gives not-applicable for it.
type Snapshot struct {
	Schema        string    `json:"schema"` // SnapshotSchema
	Boundary      Boundary  `json:"boundary"`
	Baseline      Baseline  `json:"baseline"`
	Selection     Selection `json:"selection"`
	Changes       []Change  `json:"changes"` // sorted by (path, old_path); non-nil
	PathsComplete bool      `json:"paths_complete"`
	// UnreadableDirectories names each directory that the directory
	// census could not read, sorted, without the directories below one of
	// them. Each one makes PathsComplete false, so the list is the
	// evidence of the incomplete path set. It holds at most
	// max-unreadable-directories entries. It is not a part of
	// snapshot_sha256, because the part list of that digest is frozen.
	// The status digest holds the whole list for the revalidation.
	UnreadableDirectories []string `json:"unreadable_directories,omitempty"`
	// UnreadableDirectoriesOmitted counts the unreadable directories that
	// the output cap max-unreadable-directories left out of the list.
	UnreadableDirectoriesOmitted int        `json:"unreadable_directories_omitted,omitempty"`
	Limits                       []LimitHit `json:"limits,omitempty"`
	Limitations                  []string   `json:"limitations,omitempty"` // closed: the snapshot limitations above, sorted
	SHA256                       string     `json:"snapshot_sha256"`
	// bounds holds the limits that the producer read the snapshot with.
	// Evaluate applies the same limits, because Evaluate has no limits
	// parameter and one evaluation must use one set of bounds. The zero
	// value means DefaultLimits.
	bounds Limits
	// identities holds what the producer saw at each worktree path that
	// it looked at, sorted by path. Revalidation looks at each path
	// again, so a change after the read makes the result stale.
	identities []fileID
	// statusSHA256 is the hex SHA-256 of the git status stdout that the
	// producer read and of the census result: the result byte and the
	// unreadable directories. Revalidation runs status and the census
	// again and compares, because no other field holds these bytes. So a
	// directory that becomes unreadable after the read makes the result
	// stale.
	statusSHA256 string
	// statusStderr is the first line that git status wrote to stderr,
	// redacted, sanitized, and cut to max-excerpt-runes. It goes with the
	// limitation git-stderr and never changes PathsComplete. The text
	// output prints it as the notice. It is unexported, so neither the
	// JSON output nor the digest holds it.
	statusStderr string
	// undecidedLimit names the content limit that left a path undecided.
	// The producer could not compare the content of that path with HEAD.
	// So the path is not in Changes, and PathsComplete is false. It is ""
	// when no limit caused it, for example a read error. incompletePaths
	// reads it, because a result for such a path gives resource-limit
	// with the limit name.
	undecidedLimit string
	// attribution tells how the observed result of an actual-edit
	// snapshot relates to its projection. Only ObserveAfter sets it, and
	// it is "" when the post-edit budget ended the read. It is
	// unexported, because the frozen JSON form has no such field.
	attribution Attribution
}

// pathState tells what the producer found at one worktree path.
type pathState uint8

// The states of a worktree path.
const (
	// pathAbsent means that the path does not exist.
	pathAbsent pathState = iota
	// pathPresent means that Lstat found the path.
	pathPresent
	// pathThroughLink means that a parent component of the path is a
	// symbolic link. The producer never looks through it.
	pathThroughLink
	// pathUnknown means that Lstat failed for a reason other than
	// absence, for example a permission error. An error is not absence.
	pathUnknown
)

// fileID is the identity of one worktree path at the time of the read.
// The fields are plain values, because this file must not import a
// package that does I/O (TestLessonCheckEvaluateIsPure).
type fileID struct {
	path    string
	state   pathState
	size    int64
	mtimeNS int64  // modification time in nanoseconds since the Unix epoch
	mode    uint32 // the fs.FileMode bits
	inode   uint64 // 0 where the platform gives no inode
	// sha256 is the hex SHA-256 of the content when the producer read the
	// whole content. Revalidation then hashes the content again, because
	// an edit can keep the size and the modification time.
	sha256 string
}

// hexSHA256 returns the lowercase hex SHA-256 of data. Every content
// digest of the package uses it, so all content digests have one form.
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// snapshotDigest returns snapshot_sha256: the hex SHA-256 of the
// lesson-snapshot/1 composition. Each part gets its decimal byte length
// and a colon in front, so no two part lists give the same bytes.
// reviews.ComposeDigest computes it, so the lesson-check digests and
// the reviews digests share one composer. The digest uses the content
// SHA-256 of each side and never a git object id, so the same content
// gives the same digest in SHA-1 and SHA-256 repositories. Limit hits,
// the change flags, and the change limitations are not part of it,
// because the part list is frozen. A change of the list changes every
// stored snapshot_sha256.
func snapshotDigest(s *Snapshot) string {
	parts := []string{
		SnapshotSchema,
		string(s.Boundary),
		string(s.Baseline.Kind),
		s.Baseline.Commit,
		s.Baseline.PendingSHA,
		string(s.Selection.Kind),
		strconv.Itoa(len(s.Selection.Paths)),
	}

	parts = append(parts, s.Selection.Paths...)
	parts = append(parts, strconv.Itoa(len(s.Changes)))

	for _, ch := range s.Changes {
		parts = append(parts,
			ch.Path, ch.OldPath, string(ch.Kind),
			string(ch.Before.Type), ch.Before.Mode, ch.Before.SHA256,
			string(ch.After.Type), ch.After.Mode, ch.After.SHA256)
	}

	complete := "0"
	if s.PathsComplete {
		complete = "1"
	}

	parts = append(parts, complete, strconv.Itoa(len(s.Limitations)))
	parts = append(parts, s.Limitations...)

	return reviews.ComposeDigest(parts...)
}

// limits returns the bounds of the snapshot. A snapshot without bounds
// gets DefaultLimits, so a zero bound never cuts every evaluation. A nil
// snapshot also gets DefaultLimits, because Evaluate on an invalid
// config reads no snapshot.
func (s *Snapshot) limits() Limits {
	if s == nil {
		return DefaultLimits()
	}

	return s.bounds.orDefault()
}
