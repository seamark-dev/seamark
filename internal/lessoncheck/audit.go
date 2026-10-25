package lessoncheck

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/seamark-dev/seamark/internal/privatedir"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// This file holds the writer of the lesson-check audit stream
// .seamark/lesson-check-audit.jsonl (contract K9). The stream is a
// separate file. The firing log .seamark/lessons-audit.jsonl gets no
// record from it (§5 row 39), because a firing log reader counts
// deliveries and a check is not a delivery.
//
// The file also declares the data types that a record carries: the
// guard decision of contract K8 and the tool outcome, completion, and
// attribution of contract K9. They live in lessoncheck, because
// lessoncheck cannot import integration, and the record embeds them.

// AuditSchema is the format name of one audit record.
const AuditSchema = "lesson-check-audit/1"

// The closed list of record kinds (AuditRecord.Kind).
const (
	AuditVerify            = "verify"
	AuditGuard             = "guard"
	AuditBaseline          = "baseline"
	AuditPostEdit          = "post-edit"
	AuditBaselineDiscarded = "baseline-discarded"
)

// The closed list of discard causes (AuditRecord.Discarded).
const (
	// DiscardExpired is a pending record older than pending-max-age.
	DiscardExpired = "expired"
	// DiscardSessionDeleted is a pending record of a deleted capture
	// session.
	DiscardSessionDeleted = "session-deleted"
	// DiscardUnsafe is a pending entry that is not a regular file, for
	// example a symbolic link. No handler can trust it as a baseline.
	DiscardUnsafe = "unsafe"
)

// The audit file names. The lock file is a separate file, because the
// rotation renames the log, and a lock on the log itself would move
// with it.
const (
	auditFile     = "lesson-check-audit.jsonl"
	auditLockFile = "lesson-check-audit.jsonl.lock"
)

// ownStatePath reports whether path is a file that the audit writer
// owns: the live log, its rotated generation, or the lock file. The
// path is repository-relative and slash-separated. The default
// working-tree snapshot leaves these files out when they are untracked
// (K4 Addition of review fix R-8), because every verification writes its
// own record and a second verification must not report that record as a
// change.
func ownStatePath(path string) bool {
	switch path {
	case ".seamark/" + auditFile, ".seamark/" + auditFile + ".1", ".seamark/" + auditLockFile:
		return true
	default:
		return false
	}
}

// The retention of the stream. The values come from gate.Audit: the
// live log rotates when it reaches maxAuditBytes or when its oldest
// record is older than maxAuditAge. One rotated generation stays, and it
// goes when it is older than maxAuditAge. So the stream holds at most
// about two times maxAuditBytes on disk.
const (
	maxAuditBytes = 5 << 20
	maxAuditAge   = 30 * 24 * time.Hour
)

// The output caps of a record (contract K3, output caps). A cap cuts a
// field and never changes an outcome, a decision, or a state.
const (
	// maxAuditPaths bounds AuditRecord.Paths. PathsOmitted counts the rest.
	maxAuditPaths = 50
	// maxAuditEvidenceLines bounds AuditPin.EvidenceLines. EvidencePaths
	// keeps the full path count.
	maxAuditEvidenceLines = 10
	// maxAuditErrorBytes bounds AuditRecord.Error.
	maxAuditErrorBytes = 200
)

// The modes of the files that a lesson-check writer creates. The data
// is private to the user, so only the owner can read it.
const (
	privateDirMode  os.FileMode = 0o700
	privateFileMode os.FileMode = 0o600
)

// errUnsafePath is the error value that the privatedir helpers wrap
// when they refuse an entry. Each caller in this package gives its own
// error text to the user.
var errUnsafePath = errors.New("unsafe path")

// ToolOutcome is the native outcome of one tool call. The closed list
// holds the values of integration.Outcome other than the empty value.
type ToolOutcome string

// The closed list of tool outcomes.
const (
	ToolSucceeded   ToolOutcome = "succeeded"
	ToolFailed      ToolOutcome = "failed"
	ToolInterrupted ToolOutcome = "interrupted"
	ToolDenied      ToolOutcome = "denied"
	ToolUnknown     ToolOutcome = "unknown"
)

// Completion is one decoded post-edit event (contract K9). The session
// and match IDs are raw. A handler hashes them before it persists
// anything (contracts §1.6).
type Completion struct {
	Client, Tool, SessionID, MatchID, CWD string
	Outcome                               ToolOutcome
	// Paths come from the tool input. The post-edit handler uses them to
	// scope a missing baseline.
	Paths []string
}

// Attribution tells how the observed result of an edit relates to its
// projection.
type Attribution string

// The closed list of attributions.
const (
	// AttributionProjectionMatch means that the observed after state
	// equals the projected after state for every path.
	AttributionProjectionMatch Attribution = "projection-match"
	// AttributionProjectionDiffers means that the observed after state
	// differs, for example because of a concurrent writer.
	AttributionProjectionDiffers Attribution = "projection-differs"
	// AttributionNoProjection means that the pending record holds no
	// projection, for example for a Codex update.
	AttributionNoProjection Attribution = "no-projection"
	// AttributionNoBaseline means that the pending record is absent or
	// was discarded.
	AttributionNoBaseline Attribution = "no-baseline"
)

// GuardDecision is the decision of the pre-write guard (contract K8,
// lesson-guard/1). The guard adapter fills it, and a guard record
// embeds it. It holds no field that tells whether the tool ran after a
// deny: the guard writes the record before the tool runs, and the
// stream is append-only. The delivery report derives that fact through
// the match digest of the same call.
type GuardDecision struct {
	Schema        string     `json:"schema"`          // "lesson-guard/1"
	Decision      string     `json:"decision"`        // closed: "allow", "deny"
	Cause         string     `json:"cause,omitempty"` // closed; contract K8
	Reasons       []Reason   `json:"reasons,omitempty"`
	InputSHA256   string     `json:"input_sha256"` // the snapshot digest of the proposed edit
	PathsComplete bool       `json:"paths_complete"`
	Pins          []AuditPin `json:"pins"`      // the enforce pins evaluated; non-nil
	Requested     string     `json:"requested"` // closed: "deny", "none"
	Protocol      string     `json:"protocol"`  // closed: "exit-2-stderr"
	ReplyWritten  bool       `json:"reply_written"`
	// NativeBlocking is a copy of the verification level of the
	// pre-write path of the client at decision time. Closed list:
	// "verified", "unverified".
	NativeBlocking string `json:"native_blocking"`
}

// AuditRecord is one line of .seamark/lesson-check-audit.jsonl. It
// holds digests, codes, counts, paths, and line numbers only: no file
// content, no excerpt, no patch text, and no raw provider ID
// (contracts §1.6).
type AuditRecord struct {
	Schema string `json:"schema"` // AuditSchema; AppendAudit sets it
	TS     string `json:"ts"`     // RFC 3339 UTC; AppendAudit sets it when empty
	Kind   string `json:"kind"`   // closed: the record kinds above
	Caller Caller `json:"caller"`
	Client string `json:"client,omitempty"`
	Tool   string `json:"tool,omitempty"`
	// The three digests are reviews.SessionDigest, MatchDigest, and
	// ContextDigest over the identity root (contract K7). So they are
	// byte-equal to the fields of the same names in lessons-audit.jsonl.
	SessionSHA    string         `json:"session_sha256,omitempty"`
	MatchSHA      string         `json:"match_sha256,omitempty"`
	ContextSHA    string         `json:"context_sha256,omitempty"`
	Boundary      Boundary       `json:"boundary,omitempty"`
	ConfigSHA256  string         `json:"config_sha256,omitempty"`
	SnapshotSHA   string         `json:"snapshot_sha256,omitempty"`
	Paths         []string       `json:"paths,omitempty"` // repository-relative; at most maxAuditPaths
	PathsOmitted  int            `json:"paths_omitted,omitempty"`
	PathsComplete bool           `json:"paths_complete"`
	Pins          []AuditPin     `json:"pins,omitempty"`
	State         SummaryState   `json:"state,omitempty"`        // verify, post-edit
	Guard         *GuardDecision `json:"guard,omitempty"`        // guard
	ToolOutcome   ToolOutcome    `json:"tool_outcome,omitempty"` // post-edit
	Attribution   Attribution    `json:"attribution,omitempty"`  // post-edit
	Discarded     string         `json:"discarded,omitempty"`    // baseline-discarded: the discard causes above
	Emitted       *bool          `json:"emitted,omitempty"`      // a hook reply was written
	Error         string         `json:"error,omitempty"`        // redacted, sanitized, at most maxAuditErrorBytes
}

// AuditPin is the outcome of one evaluated pin in a record. It names
// the evidence by count and line number only.
type AuditPin struct {
	PinIdentity   string   `json:"pin_identity"`
	PinVersion    string   `json:"pin_version"`
	Mode          Mode     `json:"mode"`
	Check         CheckRef `json:"check"`
	Outcome       Outcome  `json:"outcome"`
	Reason        Reason   `json:"reason,omitempty"`
	EvidencePaths int      `json:"evidence_paths"`
	EvidenceLines []int    `json:"evidence_lines,omitempty"` // at most maxAuditEvidenceLines
}

// auditPinsOf returns one AuditPin per result, in result order. The
// slice is never nil, because GuardDecision.Pins encodes [] for no pin.
func auditPinsOf(results []Result) []AuditPin {
	pins := make([]AuditPin, 0, len(results))

	for _, r := range results {
		pins = append(pins, AuditPin{
			PinIdentity:   r.PinIdentity,
			PinVersion:    r.PinVersion,
			Mode:          r.Mode,
			Check:         r.Check,
			Outcome:       r.Outcome,
			Reason:        r.Reason,
			EvidencePaths: evidencePaths(r),
			EvidenceLines: evidenceLines(r.Evidence),
		})
	}

	return pins
}

// evidencePaths counts the distinct (path, old path) pairs of the
// evidence and of the unknown paths of a result. A violation names its
// paths in the evidence, and an unknown result names them in the
// unknown paths, so the count covers both outcomes.
func evidencePaths(r Result) int {
	seen := map[[2]string]bool{}

	for _, e := range r.Evidence {
		seen[[2]string{e.Path, e.OldPath}] = true
	}

	for _, e := range r.UnknownPaths {
		seen[[2]string{e.Path, e.OldPath}] = true
	}

	return len(seen)
}

// evidenceLines returns the line numbers of the evidence, in evidence
// order, at most maxAuditEvidenceLines. It never copies an excerpt,
// because a record holds no file content.
func evidenceLines(evidence []Evidence) []int {
	var lines []int

	for _, e := range evidence {
		if e.Line > 0 && len(lines) < maxAuditEvidenceLines {
			lines = append(lines, e.Line)
		}
	}

	return lines
}

// AppendAudit appends one record to <root>/.seamark/lesson-check-audit.jsonl.
// root is the git top level that holds .seamark. It sets the schema,
// sets the time when TS is empty, and applies the output caps.
//
// The writer copies the hardening of gate.Audit: one exclusive lock
// spans the tighten, rotate, and append steps, the files are 0600, and
// every open refuses a symbolic link. A caller never changes a decision,
// an outcome, or an exit code because of the returned error (contract
// K9).
func AppendAudit(root string, rec AuditRecord) error {
	rec, err := boundRecord(rec)
	if err != nil {
		return err
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	dir := filepath.Join(root, ".seamark")

	// .seamark must be a real directory: a planted link would redirect
	// the lock and the records outside the repository. An absent
	// directory gets mode 0700, as reviews.openDeliveryState creates it.
	if err := ensureRealDir(dir, "create"); err != nil {
		return err
	}

	unlock, err := lockAudit(dir)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(dir, auditFile)

	if err := refuseLink(path); err != nil {
		return err
	}

	if err := tightenAndRotate(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|noFollowFlag, privateFileMode)
	if err != nil {
		return err
	}

	// One Write is one line. With O_APPEND, two writers without the lock
	// (a platform without flock) still interleave whole lines.
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()

	return errors.Join(werr, cerr)
}

// ensureRealDir runs privatedir.EnsureRealDir. It runs it once more
// when another writer creates the directory between the check and the
// create of the first run. The second run then checks the entry that
// the other writer made, so concurrent hooks never fail on a race.
func ensureRealDir(dir, createPrefix string) error {
	err := privatedir.EnsureRealDir(dir, privateDirMode, errUnsafePath, createPrefix)
	if errors.Is(err, os.ErrExist) {
		err = privatedir.EnsureRealDir(dir, privateDirMode, errUnsafePath, createPrefix)
	}

	return err
}

// boundRecord checks the closed lists of a record and applies the
// output caps to a copy. The writer applies the caps itself, so no
// caller can write a record above them. The error text goes through
// the secret redaction first, so the cut can never show the tail of a
// redacted secret.
func boundRecord(rec AuditRecord) (AuditRecord, error) {
	switch rec.Kind {
	case AuditVerify, AuditGuard, AuditBaseline, AuditPostEdit, AuditBaselineDiscarded:
	default:
		return rec, fmt.Errorf("lesson-check audit: unknown record kind %q", rec.Kind)
	}

	switch rec.Caller {
	case CallerCLI, CallerMCP, CallerTUI, CallerHook:
	default:
		return rec, fmt.Errorf("lesson-check audit: unknown caller %q", rec.Caller)
	}

	rec.Schema = AuditSchema

	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}

	if len(rec.Paths) > maxAuditPaths {
		rec.PathsOmitted += len(rec.Paths) - maxAuditPaths
		rec.Paths = append([]string(nil), rec.Paths[:maxAuditPaths]...)
	}

	rec.Pins = boundPins(rec.Pins)

	if rec.Guard != nil {
		guard := *rec.Guard
		guard.Pins = boundPins(guard.Pins)
		rec.Guard = &guard
	}

	if rec.Error != "" {
		rec.Error = cutBytes(render.Sanitize(redact.Secrets(rec.Error)), maxAuditErrorBytes)
	}

	return rec, nil
}

// boundPins returns a copy of the pins with at most
// maxAuditEvidenceLines line numbers each. A nil slice stays nil and an
// empty slice stays empty, so the JSON form does not change.
func boundPins(pins []AuditPin) []AuditPin {
	if pins == nil {
		return nil
	}

	out := make([]AuditPin, len(pins))

	for i, p := range pins {
		if len(p.EvidenceLines) > maxAuditEvidenceLines {
			p.EvidenceLines = append([]int(nil), p.EvidenceLines[:maxAuditEvidenceLines]...)
		}

		out[i] = p
	}

	return out
}

// cutBytes bounds s to limit bytes on a rune boundary.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}

	return s[:cut]
}

// lockAudit takes the exclusive lock of the stream. A platform without
// flock gets no lock (perm_other.go), and the single-write append keeps
// whole lines there.
func lockAudit(dir string) (func(), error) {
	path := filepath.Join(dir, auditLockFile)

	if err := refuseLink(path); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|noFollowFlag, privateFileMode)
	if err != nil {
		return nil, err
	}

	if err := f.Chmod(privateFileMode); err != nil {
		_ = f.Close()
		return nil, err
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}

	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}

// refuseLink returns an error when path is a symbolic link. A write
// through a planted link can change, and chmod, a file outside the
// repository. The noFollowFlag on the opens backs the check.
func refuseLink(path string) error {
	info, err := os.Lstat(path)

	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s is a symbolic link", errUnsafePath, path)
	}

	return nil
}

// tightenAndRotate runs under the lock. It sets the mode before the
// rotation, because a rename keeps the mode, and a loose rotated file
// would keep the history readable for other users. The live log
// rotates when it reaches maxAuditBytes or when its oldest record is
// older than maxAuditAge. A rotated generation older than maxAuditAge
// goes. Its modification time is the time of its newest record.
func tightenAndRotate(path string) error {
	rotated := path + ".1"

	// The rotated generation gets the rules of the live log: a link is
	// removed and never followed, an old generation expires, and a loose
	// mode is set to 0600.
	if info, err := os.Lstat(rotated); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0, time.Since(info.ModTime()) > maxAuditAge:
			if err := os.Remove(rotated); err != nil {
				return err
			}
		case info.Mode().Perm() != privateFileMode:
			if err := os.Chmod(rotated, privateFileMode); err != nil {
				return err
			}
		}
	}

	f, err := os.OpenFile(path, os.O_RDONLY|noFollowFlag, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	// The chmod acts on the open file, never on a path that a writer can
	// swap after the check.
	chmodErr := f.Chmod(privateFileMode)
	info, statErr := f.Stat()
	stale := oldestRecordStale(f)

	_ = f.Close()

	if err := errors.Join(chmodErr, statErr); err != nil {
		return err
	}

	if info.Size() >= maxAuditBytes || stale {
		// A rename replaces a link at the destination itself, not its
		// target.
		return os.Rename(path, rotated)
	}

	return nil
}

// oldestRecordStale reads the time of the first record of an open log.
// A first line that does not read or parse counts as stale too, because
// a rotation moves it aside and keeps it readable in the rotated
// generation.
func oldestRecordStale(f *os.File) bool {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	if !sc.Scan() {
		// An empty log has nothing to age. A read error counts as stale.
		return sc.Err() != nil
	}

	var first struct {
		TS time.Time `json:"ts"`
	}

	if err := json.Unmarshal(sc.Bytes(), &first); err != nil || first.TS.IsZero() {
		return true
	}

	return time.Since(first.TS) > maxAuditAge
}
