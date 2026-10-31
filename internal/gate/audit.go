package gate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/seamark-dev/seamark/internal/privatedir"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
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

	// .seamark itself must be a real directory: a planted directory
	// symlink would redirect the lock and the audit data — including
	// opt-in raw inputs — outside the repository.
	if err := refuseSymlinkDir(dir); err != nil {
		return err
	}

	// One exclusive lock spans tighten → rotate → append: concurrent
	// hooks cannot lose an entry to a rotation race, rotate twice, or
	// rename a freshly-started log over the retained generation.
	unlock, err := privatedir.LockLog(filepath.Join(dir, "audit.lock"), 0o600, symlinkRefused)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(dir, auditFile)

	// The audit path must be a real file: appending through a planted
	// symlink would write to — and chmod — an arbitrary file. O_NOFOLLOW
	// on the opens below backs this check at the syscall level.
	if err := privatedir.RefuseLink(path, symlinkRefused); err != nil {
		return err
	}

	if err := privatedir.RotateLog(path, maxAuditBytes, maxAuditAge, 0o600, time.Now()); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|privatedir.NoFollow, 0o600)
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
		entry["input"] = truncate(redact.Secrets(input), maxRawInput)
	}

	// One Encode is one write of one line: with O_APPEND, concurrent
	// writers interleave whole entries, not bytes.
	return json.NewEncoder(f).Encode(entry)
}

// refuseSymlinkDir returns an error unless p is a real directory, so a
// link to a directory fails too. Like privatedir.RefuseLink, this stops a
// link that a clone commits. It does not stop a swap during the check,
// because an actor who can do that already has shell access.
func refuseSymlinkDir(p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gate: audit: %s is a symlink — refusing to write through it", p)
	}

	if !info.IsDir() {
		return fmt.Errorf("gate: audit: %s is not a directory", p)
	}

	return nil
}

// symlinkRefused builds the error for a symbolic link at an audit path,
// because an audit log must never point to another file.
func symlinkRefused(p string) error {
	return fmt.Errorf("gate: audit: %s is a symlink — refusing to follow it", p)
}

// truncate cuts s to limit bytes on a rune boundary, and the marker comes
// after the limit. It runs AFTER redaction, so a cut can never expose the
// tail of a scrubbed secret.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return render.CutBytes(s, limit) + "…[truncated]"
}
