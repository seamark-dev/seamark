package gate

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const auditFile = "audit.jsonl"

// Retention policy: the live log rotates once it grows past maxAuditBytes
// OR once its oldest entry is past maxAuditAge; exactly one rotated
// generation is kept and is removed once untouched for maxAuditAge. Total
// on-disk audit state is therefore bounded at roughly twice the size cap,
// and no entry outlives two age windows.
const (
	maxAuditBytes = 5 << 20
	maxAuditAge   = 30 * 24 * time.Hour
)

// maxRawInput bounds a persisted raw input (opt-in mode): a hooked
// command line is small, but `check` inputs are whole diffs, and one
// large diff must not dominate the log.
const maxRawInput = 4096

// Audit appends the decision to <root>/.seamark/audit.jsonl (0600,
// rotated by size and age). The default entry stores the normalized
// command names, a SHA-256 of the input, the verdict and the policy hash
// — never the raw input, which frequently carries tokens, passwords and
// connection strings. Opting in via policy.yaml
// (`audit:` → `raw: true`) persists the input line with best-effort
// secret redaction.
func Audit(root, kind, input string, p *Policy, d *Decision) error {
	dir := filepath.Join(root, ".seamark")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// One exclusive lock spans tighten → rotate → append: concurrent
	// hooks cannot lose an entry to a rotation race, rotate twice, or
	// rename a freshly-started log over the retained generation.
	unlock, err := lockAudit(dir)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(dir, auditFile)

	// The audit path must be a real file: appending through a planted
	// symlink would write to — and chmod — an arbitrary file. O_NOFOLLOW
	// on the opens below backs this check at the syscall level.
	if err := refuseSymlink(path); err != nil {
		return err
	}

	if err := tightenAndRotate(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|noFollow, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // write errors surface via Encode

	effects := d.Effects
	if effects == nil {
		effects = []string{} // an explicit "no effects", never null
	}

	entry := map[string]any{
		"ts":            time.Now().UTC().Format(time.RFC3339),
		"kind":          kind,
		"input_sha256":  fmt.Sprintf("%x", sha256.Sum256([]byte(input))),
		"verdict":       d.Verdict,
		"mode":          d.Mode,
		"effects":       effects,
		"policy_sha256": d.PolicySHA,
	}

	if len(d.Matches) > 0 {
		entry["matches"] = d.Matches
	}

	if len(d.Commands) > 0 {
		entry["commands"] = d.Commands
	}

	if p.Audit.Raw {
		entry["input"] = truncate(redactSecrets(input), maxRawInput)
	}

	// One Encode is one write of one line: with O_APPEND, concurrent
	// writers interleave whole entries, not bytes.
	return json.NewEncoder(f).Encode(entry)
}

// lockAudit takes an exclusive advisory lock on .seamark/audit.lock. On
// platforms without flock it degrades to best-effort single-writer
// behaviour (see lockFile).
func lockAudit(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "audit.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}

	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}

// refuseSymlink errors when p exists and is a symlink; an audit log must
// never be a pointer to somewhere else.
func refuseSymlink(p string) error {
	info, err := os.Lstat(p)

	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("gate: audit: %s is a symlink — refusing to follow it", p)
	}

	return nil
}

// tightenAndRotate runs under the audit lock. Permissions are fixed
// BEFORE rotation — a rename keeps the file's mode, and a 0644 legacy
// log full of raw command history must not survive world-readable as the
// rotated generation. The live log rotates when oversized or when its
// oldest entry has aged out; a rotated generation past maxAuditAge
// (its mtime is its newest entry) is removed.
func tightenAndRotate(path string) error {
	if info, err := os.Lstat(path + ".1"); err == nil {
		if time.Since(info.ModTime()) > maxAuditAge {
			if err := os.Remove(path + ".1"); err != nil {
				return err
			}
		}
	}

	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if os.IsNotExist(err) {
		return nil // nothing to tighten or rotate
	}

	if err != nil {
		return err
	}

	// fd-based chmod: acts on the opened file itself, never a path that
	// could have been swapped underneath.
	chmodErr := f.Chmod(0o600)
	info, statErr := f.Stat()
	stale := oldestEntryStale(f)

	_ = f.Close()

	if chmodErr != nil {
		return chmodErr
	}

	if statErr != nil {
		return statErr
	}

	if info.Size() >= maxAuditBytes || stale {
		// Rename replaces a .1 symlink itself rather than its target, so
		// no separate check is needed for the destination.
		return os.Rename(path, path+".1")
	}

	return nil
}

// oldestEntryStale reads the first entry's timestamp from an open log.
// An unparseable first line also counts as stale: rotating junk aside is
// always safe, and it stays readable in the retained generation.
func oldestEntryStale(f *os.File) bool {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20) // tolerate long legacy lines

	if !sc.Scan() {
		// A genuinely empty log has nothing to age out; a read failure
		// (a first line past the token limit, an I/O error) counts as
		// stale — rotating unreadable junk aside is always safe.
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

// redaction is one secret-shaped pattern and its replacement.
type redaction struct {
	re          *regexp.Regexp
	replacement string
}

// secretValue matches a flag or assignment payload: a quoted string
// (whitespace included — `--password 'correct horse battery staple'`
// must not leak its tail) or one unquoted token.
const secretValue = `('[^']*'|"[^"]*"|\S+)`

// redactions cover the common ways secrets ride in command lines. This
// is best-effort scrubbing for the OPT-IN raw log, not a guarantee — the
// default log never stores the input at all.
var redactions = []redaction{
	// Environment assignments whose name smells secret-bearing:
	// AWS_SECRET_ACCESS_KEY=…, DB_PASSWORD=…, api_token=….
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|CREDENTIALS?|AUTH|ACCESS_?KEY|PRIVATE_?KEY)[A-Z0-9_]*)=` + secretValue),
		"${1}=[REDACTED]"},
	// Value-taking secret flags: --password x, --token=x, --api-key x.
	{regexp.MustCompile(`(?i)(--?(?:password|passwd|token|api-?key|secret|access-?key|private-?key|auth(?:orization)?)[= ])` + secretValue),
		"${1}[REDACTED]"},
	// URL userinfo: scheme://user:pass@host keeps user and host.
	{regexp.MustCompile(`(://[^/\s@:]+:)[^@\s]+@`), "${1}[REDACTED]@"},
	// Bearer tokens in headers: Authorization: Bearer eyJ….
	{regexp.MustCompile(`(?i)\b(bearer\s+)\S+`), "${1}[REDACTED]"},
}

// redactSecrets scrubs known secret-bearing patterns from an input line.
func redactSecrets(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.replacement)
	}

	return s
}

// truncate bounds s to limit bytes on a rune boundary. It runs AFTER
// redaction, so a cut can never expose the tail of a scrubbed secret.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}

	return s[:cut] + "…[truncated]"
}
