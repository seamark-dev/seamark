package lessoncheck

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/privatedir"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the pending baselines of contract K7. A pre-edit
// handler stores the before state of one allowed tool call in
// .seamark/checks/pending/<key>.json. The post-edit handler of the same
// call reads it as the baseline of the actual edit and deletes it. A
// sweep removes what a crash or a cancelled tool leaves behind.
//
// Two roots apply. The directory is under the git top level, as the
// configuration and the snapshot are. Every digest inside a record and
// the key use the K2 identity root, which the caller hashes over (§5
// row 121). So each function here takes the git top level. DiscardSession
// takes both roots, because its caller must match digests over the
// identity root and must still find the directory of the selected
// workspace.
//
// The directory never lives under .seamark/private, and its own
// .gitignore keeps it out of git (contract K7).

// PendingSchema is the format name of a pending record. It is also the
// label of the key digest (contracts §2.1, §2.2).
const PendingSchema = "lesson-check-pending/1"

// LimitationBaselineTooLarge marks a record whose content the writer
// dropped above max-baseline-bytes. The post-edit handler then gives
// unknown/resource-limit for a pattern pin on those paths.
const LimitationBaselineTooLarge = "baseline-too-large"

// tmpMaxAge is the age at which the sweep removes an in-flight write
// (contract K3, output caps). A write that is older crashed before its
// rename.
const tmpMaxAge = 10 * time.Minute

// The names inside .seamark/checks.
const (
	checksDirName    = "checks"
	pendingDirName   = "pending"
	checksGitignore  = "*\n"
	pendingSuffix    = ".json"
	pendingTmpPrefix = ".tmp-"
)

// pendingMetadataBytes is the room for the fields of a record other
// than its content. The read bound of a record is the base64 size of
// max-baseline-bytes plus this room. A record above the bound reads as
// absent, so a corrupt or planted file never stands in for a baseline.
const pendingMetadataBytes = 8 << 20

// ErrUnsafeChecksDir is the error of a .seamark/checks directory that is
// not a real directory, for example a symbolic link. The text is the
// text of contract K7. The guard still decides, and the post-edit
// handler gives missing-baseline.
var ErrUnsafeChecksDir = errors.New("lesson checks: .seamark/checks is not a real directory")

// ErrNoPending is the error of a pending record that cannot serve as a
// baseline: it is absent, it is not a regular file, it is above the
// read bound, it does not parse, it has another schema, or it names
// another call. The post-edit handler gives missing-baseline for each
// case, so a bad record is never a pass.
var ErrNoPending = errors.New("lesson checks: no pending baseline")

// Pending is one pending record, lesson-check-pending/1 (contract K7).
type Pending struct {
	Schema        string `json:"schema"`         // PendingSchema; WritePending sets it
	CreatedAt     string `json:"created_at"`     // RFC 3339 UTC; WritePending sets it when empty
	Client        string `json:"client"`         // the client of the hook flag
	Tool          string `json:"tool"`           // the native tool name
	InstalledMode string `json:"installed_mode"` // "check" or "enforce": the pre-edit marker
	// The digests are reviews.SessionDigest, MatchDigest, and
	// ContextDigest over the identity root. ContextSHA is "" when the
	// client names no receiver. MatchSHA is "" when the client names no
	// tool-use ID, and then no record exists.
	SessionSHA    string        `json:"session_sha256"`
	MatchSHA      string        `json:"match_sha256"`
	ContextSHA    string        `json:"context_sha256,omitempty"`
	ConfigSHA256  string        `json:"config_sha256"`
	PathsComplete bool          `json:"paths_complete"`
	Paths         []PendingPath `json:"paths"`
	// Limitations is a closed, sorted list: "no-projection" (a path
	// whose after content was not projectable), "baseline-too-large"
	// (content dropped above max-baseline-bytes), and "path-through-link".
	Limitations []string `json:"limitations,omitempty"`
	// fileSHA256 is the hex SHA-256 of the record bytes as ReadPending
	// read them. The actual-edit baseline names it (pending_sha256), so
	// a result names the exact record that it used.
	fileSHA256 string
}

// PendingPath is the before state of one path of a pending record.
type PendingPath struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // the source of a move
	Before  Side   `json:"before"`
	// ContentB64 holds the before content only when an executable
	// no-added-pattern/1 pin scopes the path, because only that checker
	// reads the before text. Empty content is omitted too: Before.Content
	// "available" with Before.Size 0 marks it.
	ContentB64 string `json:"content_b64,omitempty"`
	// ProjectedAfterSHA256 is the SHA-256 of the projected after
	// content. It is "" when the after content was not projectable, and
	// for a projected delete, whose after side is absent.
	ProjectedAfterSHA256 string `json:"projected_after_sha256,omitempty"`
}

// PendingKey returns the file name key of one tool call (contract K7):
// the hex SHA-256 of compose("lesson-check-pending/1", client,
// matchSHA). matchSHA is reviews.MatchDigest over the identity root, so
// the pre-edit and the post-edit handler of one call compute one key.
// An empty matchSHA gives "", because a call without a tool-use ID has
// no record.
func PendingKey(client, matchSHA string) string {
	if matchSHA == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(compose(PendingSchema, client, matchSHA)))

	return hex.EncodeToString(sum[:])
}

// PendingPathsOf returns the pending paths of a proposed-edit snapshot
// and the limitations that they give. A change is kept when an
// executable pin of cfg scopes one of its ends. The before content goes
// into the record only when an executable no-added-pattern/1 pin scopes
// the change. It returns no path when no executable pin scopes the
// edit, and then the caller writes no record.
//
// The snapshot keeps its content unexported (contracts §1.6), so only
// this package can copy it into a record.
func PendingPathsOf(cfg *Config, snap *Snapshot) (paths []PendingPath, limitations []string) {
	var lims []string

	for _, ch := range snap.Changes {
		scoped, needsText := executableScope(cfg, ch)
		if !scoped {
			continue
		}

		before := ch.Before
		before.text = nil

		p := PendingPath{Path: ch.Path, OldPath: ch.OldPath, Before: before}

		if needsText && ch.Before.Content == ContentAvailable && len(ch.Before.text) > 0 {
			p.ContentB64 = base64.StdEncoding.EncodeToString(ch.Before.text)
		}

		if slices.Contains(ch.Limitations, LimitationNoProjection) {
			lims = append(lims, LimitationNoProjection)
		} else {
			p.ProjectedAfterSHA256 = ch.After.SHA256
		}

		if slices.Contains(ch.Limitations, LimitationPathThroughLink) {
			lims = append(lims, LimitationPathThroughLink)
		}

		paths = append(paths, p)
	}

	return paths, sortedUnique(lims)
}

// executableScope reports whether an executable pin scopes an end of
// the change, and whether such a pin is a no-added-pattern/1 pin. The
// kind comes from the written definition, so a pin of an invalid
// configuration still names its kind.
func executableScope(cfg *Config, ch Change) (scoped, needsText bool) {
	for _, pin := range cfg.Pins {
		if !isExecutable(pin.Mode) || endpointsInScope(ch, pin.Scope) == 0 {
			continue
		}

		scoped = true

		if rawCheckRef(cfg.Raw, pin.CheckID).Kind == KindNoAddedPattern {
			needsText = true
		}
	}

	return scoped, needsText
}

// WritePending writes the record of one allowed tool call to
// <root>/.seamark/checks/pending/<key>.json. root is the git top level.
// The write goes through a temporary file and a rename, so a reader
// never sees a partial record and a cancelled handler leaves no partial
// file. It sets the schema, sets the time when CreatedAt is empty, and
// drops content above lim.MaxBaselineBytes. It never changes p.
func WritePending(ctx context.Context, root string, p *Pending, lim Limits) error {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	key := PendingKey(p.Client, p.MatchSHA)
	if key == "" {
		return errors.New("lesson checks: a pending record needs a match digest")
	}

	rec := *p
	rec.Schema = PendingSchema

	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	boundBaseline(&rec, lim.MaxBaselineBytes)

	data, err := json.Marshal(&rec)
	if err != nil {
		return err
	}

	if int64(len(data)) > pendingReadBound(lim) {
		return fmt.Errorf("lesson checks: the pending record has %d bytes, above the read bound", len(data))
	}

	dir, err := openPendingDir(root)
	if err != nil {
		return err
	}

	return writeAtomic(ctx, dir, key+pendingSuffix, data)
}

// boundBaseline keeps the content of the paths in path order while the
// decoded total stays at or below maxBytes. A path whose content does
// not fit loses it: its before side becomes too-large, and the record
// gets the limitation baseline-too-large (contract K7).
func boundBaseline(rec *Pending, maxBytes int64) {
	paths := append([]PendingPath(nil), rec.Paths...)
	total, dropped := int64(0), false

	for i := range paths {
		if paths[i].ContentB64 == "" {
			continue
		}

		n := int64(base64.StdEncoding.DecodedLen(len(paths[i].ContentB64))) - int64(strings.Count(paths[i].ContentB64, "="))
		if total+n <= maxBytes {
			total += n
			continue
		}

		paths[i].ContentB64 = ""
		paths[i].Before.Content = ContentTooLarge
		dropped = true
	}

	rec.Paths = paths

	if dropped && !slices.Contains(rec.Limitations, LimitationBaselineTooLarge) {
		lims := append([]string{LimitationBaselineTooLarge}, rec.Limitations...)
		sort.Strings(lims)
		rec.Limitations = lims
	}
}

// writeAtomic writes data to dir/name through dir/.tmp-<random>. The
// temporary file is opened with O_EXCL and without following a link.
// A context that ends before the rename removes the temporary file.
func writeAtomic(ctx context.Context, dir, name string, data []byte) error {
	tmp, err := tmpName(dir)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollowFlag, privateFileMode)
	if err != nil {
		return err
	}

	_, werr := f.Write(data)
	cerr := f.Close()

	if err := errors.Join(werr, cerr, ctx.Err()); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// A rename replaces a link at the destination itself, never its
	// target.
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	return nil
}

// tmpName returns a new path dir/.tmp-<random>. The sweep removes such
// a path when it is older than tmpMaxAge.
func tmpName(dir string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}

	return filepath.Join(dir, pendingTmpPrefix+hex.EncodeToString(random[:])), nil
}

// ReadPending reads the record of one tool call from
// <root>/.seamark/checks/pending. root is the git top level. A record
// that cannot serve as a baseline gives an error that wraps
// ErrNoPending. An unsafe directory gives ErrUnsafeChecksDir.
func ReadPending(root, client, matchSHA string, lim Limits) (*Pending, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	key := PendingKey(client, matchSHA)
	if key == "" {
		return nil, fmt.Errorf("%w: the call has no tool-use ID", ErrNoPending)
	}

	dir, err := existingPendingDir(root)
	if err != nil {
		return nil, err
	}

	if dir == "" {
		return nil, fmt.Errorf("%w: no pending directory", ErrNoPending)
	}

	data, err := readPendingFile(filepath.Join(dir, key+pendingSuffix), pendingReadBound(lim))
	if err != nil {
		return nil, err
	}

	p, err := parsePending(data)
	if err != nil {
		return nil, err
	}

	// The key is a digest of the client and the match digest, so a
	// record under another name holds other values. A renamed file must
	// never serve as the baseline of another call.
	if p.Client != client || p.MatchSHA != matchSHA {
		return nil, fmt.Errorf("%w: the record names another call", ErrNoPending)
	}

	sum := sha256.Sum256(data)
	p.fileSHA256 = hex.EncodeToString(sum[:])

	return p, nil
}

// DeletePending removes the record of one tool call. An absent record is
// not an error, because the sweep can remove it first.
func DeletePending(root, client, matchSHA string) error {
	key := PendingKey(client, matchSHA)
	if key == "" {
		return nil
	}

	dir, err := existingPendingDir(root)
	if err != nil || dir == "" {
		return err
	}

	if err := os.Remove(filepath.Join(dir, key+pendingSuffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

// Sweep removes the records older than lim.PendingMaxAge, the entries
// that are not regular files, and the temporary files older than
// tmpMaxAge from <root>/.seamark/checks/pending (contract K7, crash
// recovery). root is the git top level. Each removed record appends one
// baseline-discarded audit record with caller. It never creates the
// directory.
//
// The age is the modification time, because a record that does not
// parse must expire too. Sweep continues after an error, and it returns
// the number of removed records and the joined errors.
func Sweep(root string, caller Caller, lim Limits) (int, error) {
	if lim == (Limits{}) {
		lim = DefaultLimits()
	}

	dir, err := existingPendingDir(root)
	if err != nil || dir == "" {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	removed, now := 0, time.Now()

	var errs []error

	for _, entry := range entries {
		name := entry.Name()

		// Info reports the entry itself, so a link reads as a link.
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			errs = append(errs, err)
			continue
		}

		path := filepath.Join(dir, name)
		age := now.Sub(info.ModTime())

		switch {
		case strings.HasPrefix(name, pendingTmpPrefix):
			if age > tmpMaxAge {
				errs = append(errs, removeTmp(path))
			}
		case !strings.HasSuffix(name, pendingSuffix):
			// The name is outside the namespace of the writer, so the
			// sweep leaves it.
		case !info.Mode().IsRegular():
			ok, err := discardPending(root, path, nil, caller, DiscardUnsafe)
			removed, errs = removed+ok, append(errs, err)
		case age > lim.PendingMaxAge:
			// An unreadable record still expires. Its record then names
			// no call.
			p, _ := readPendingRecord(path, lim)

			ok, err := discardPending(root, path, p, caller, DiscardExpired)
			removed, errs = removed+ok, append(errs, err)
		}
	}

	return removed, errors.Join(errs...)
}

// DiscardSession removes every pending record of one native session,
// when its capture session is deleted (contract K7). top is the git top
// level of the selected workspace, where the hooks store the records
// and the audit stream. identityRoot is the K2 root that the hooks
// hashed over. The two differ when the index records another root, for
// example in a copied or moved repository. Each removed record appends
// one baseline-discarded audit record with cause session-deleted.
//
// The directory comes from top only, so the call never touches another
// workspace, also when identityRoot names one. A record whose digests
// used another root does not match. The sweep still removes it at
// pending-max-age, and no other root or hash is tried (§5 row 107).
func DiscardSession(top, identityRoot, nativeSessionID string) (removed int, err error) {
	if nativeSessionID == "" {
		return 0, nil
	}

	dir, err := existingPendingDir(top)
	if err != nil || dir == "" {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}

	want := reviews.SessionDigest(identityRoot, nativeSessionID)
	lim := DefaultLimits()

	var errs []error

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), pendingSuffix) || !entry.Type().IsRegular() {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		p, err := readPendingRecord(path, lim)
		if err != nil || p.SessionSHA != want {
			continue
		}

		// capture delete is a CLI command, so the caller is the CLI.
		ok, err := discardPending(top, path, p, CallerCLI, DiscardSessionDeleted)
		removed, errs = removed+ok, append(errs, err)
	}

	return removed, errors.Join(errs...)
}

// discardPending removes one pending entry and appends its
// baseline-discarded record. It returns 1 only when this call claimed
// the entry, so two concurrent sweeps record one discard. p is nil when
// the entry is not a readable record.
//
// The claim is a rename to a new temporary name. Only one rename of a
// source succeeds. An unlink cannot serve as the claim, because APFS
// can report success to several concurrent unlink calls of one path. A
// crash after the claim leaves a temporary file, which the next sweep
// removes.
func discardPending(root, path string, p *Pending, caller Caller, cause string) (int, error) {
	claimed, err := tmpName(filepath.Dir(path))
	if err != nil {
		return 0, err
	}

	if err := os.Rename(path, claimed); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}

		return 0, err
	}

	if err := os.Remove(claimed); err != nil {
		return 1, err
	}

	rec := AuditRecord{Kind: AuditBaselineDiscarded, Caller: caller, Discarded: cause}

	if p != nil {
		rec.Client = p.Client
		rec.Tool = p.Tool
		rec.SessionSHA = p.SessionSHA
		rec.MatchSHA = p.MatchSHA
		rec.ContextSHA = p.ContextSHA
		rec.ConfigSHA256 = p.ConfigSHA256
		rec.Paths = pendingPathList(p)
		rec.PathsComplete = p.PathsComplete
	}

	return 1, AppendAudit(root, rec)
}

// removeTmp removes one temporary file. An absent entry is not an error.
func removeTmp(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

// pendingPathList returns the distinct paths and move sources of a
// record, sorted.
func pendingPathList(p *Pending) []string {
	list := make([]string, 0, 2*len(p.Paths))

	for _, pp := range p.Paths {
		list = append(list, pp.Path, pp.OldPath)
	}

	return sortedUnique(list)
}

// readPendingRecord reads and parses one record file.
func readPendingRecord(path string, lim Limits) (*Pending, error) {
	data, err := readPendingFile(path, pendingReadBound(lim))
	if err != nil {
		return nil, err
	}

	return parsePending(data)
}

// pendingReadBound is the largest record that a reader accepts: the
// base64 size of max-baseline-bytes plus the metadata room.
func pendingReadBound(lim Limits) int64 {
	return int64(base64.StdEncoding.EncodedLen(int(lim.MaxBaselineBytes))) + pendingMetadataBytes
}

// readPendingFile reads one record file without following a link. Any
// failure other than an I/O error wraps ErrNoPending.
func readPendingFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(path, openFlags, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no record", ErrNoPending)
	}

	if err != nil {
		// A link fails the open (O_NOFOLLOW). The error is not absence,
		// but the record cannot serve either.
		return nil, fmt.Errorf("%w: %w", ErrNoPending, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: the record is not a regular file", ErrNoPending)
	}

	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: the record is above the read bound", ErrNoPending)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: the record is above the read bound", ErrNoPending)
	}

	return data, nil
}

// parsePending decodes one record. A record of another schema reads as
// absent (contracts §1.7).
func parsePending(data []byte) (*Pending, error) {
	var p Pending

	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%w: the record does not parse", ErrNoPending)
	}

	if p.Schema != PendingSchema {
		return nil, fmt.Errorf("%w: the record has the schema %q", ErrNoPending, p.Schema)
	}

	return &p, nil
}

// pendingDirs returns .seamark, .seamark/checks, and
// .seamark/checks/pending under root, in creation order.
func pendingDirs(root string) []string {
	seamark := filepath.Join(root, ".seamark")
	checks := filepath.Join(seamark, checksDirName)

	return []string{seamark, checks, filepath.Join(checks, pendingDirName)}
}

// openPendingDir creates the pending directory when it is absent and
// returns its path. Each directory must be a real directory, because a
// cloned repository can commit a link that redirects the baselines.
// .seamark keeps its mode, as the other writers of .seamark do. The two
// directories of the checks get 0700 on every open, and the checks
// directory gets its own .gitignore.
func openPendingDir(root string) (string, error) {
	dirs := pendingDirs(root)

	for i, dir := range dirs {
		if err := ensureRealDir(dir, "lesson checks: create"); err != nil {
			return "", unsafeChecks(err)
		}

		if i == 0 {
			continue
		}

		if err := privatedir.TightenMode(dir, privateDirMode); err != nil {
			return "", err
		}

		if i == 1 {
			ignore := filepath.Join(dir, ".gitignore")
			if err := privatedir.EnsureGitignore(ignore, checksGitignore, privateFileMode, errUnsafePath); err != nil {
				return "", fmt.Errorf("lesson checks: %w", err)
			}
		}
	}

	return dirs[len(dirs)-1], nil
}

// existingPendingDir returns the pending directory when it exists, and
// "" when it is absent. It creates nothing, so a reader or a sweep in a
// repository without lesson checks leaves the tree as it is. An
// existing directory gets the same checks and the same mode as in
// openPendingDir.
func existingPendingDir(root string) (string, error) {
	dirs := pendingDirs(root)

	for i, dir := range dirs {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			return "", nil
		}

		// EnsureRealDir only checks an entry that exists.
		if err := ensureRealDir(dir, "lesson checks: create"); err != nil {
			return "", unsafeChecks(err)
		}

		if i > 0 {
			if err := privatedir.TightenMode(dir, privateDirMode); err != nil {
				return "", err
			}
		}
	}

	return dirs[len(dirs)-1], nil
}

// unsafeChecks gives ErrUnsafeChecksDir for a refused directory and
// keeps every other error.
func unsafeChecks(err error) error {
	if errors.Is(err, errUnsafePath) {
		return ErrUnsafeChecksDir
	}

	return err
}
