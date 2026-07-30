package gate

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditPath is the log location for a test root.
func auditPath(root string) string {
	return filepath.Join(root, ".seamark", auditFile)
}

// lastEntry reads the newest audit entry.
func lastEntry(t *testing.T, root string) map[string]any {
	t.Helper()

	f, err := os.Open(auditPath(root))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var entry map[string]any

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		entry = map[string]any{}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &entry))
	}

	require.NoError(t, sc.Err())
	require.NotNil(t, entry, "expected at least one audit entry")

	return entry
}

func TestAuditDefaultOmitsRawInput(t *testing.T) {
	p, c, root := testSetup(t)

	// A command line packed with secrets: none of them may reach disk in
	// the default configuration.
	input := "AWS_SECRET_ACCESS_KEY=AKIAxyz deploy --password hunter2 postgres://admin:s3cret@db/x"
	d := evalCmd(t, p, c, root, input)
	require.NoError(t, Audit(root, "gate", input, p, d))

	data, err := os.ReadFile(auditPath(root))
	require.NoError(t, err)

	for _, secret := range []string{"AKIAxyz", "hunter2", "s3cret", "input\""} {
		assert.NotContains(t, string(data), secret, "the default entry must not store %q", secret)
	}

	entry := lastEntry(t, root)
	assert.Equal(t, "gate", entry["kind"])
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(input))), entry["input_sha256"],
		"the input hash allows correlation without revealing content")
	assert.Equal(t, p.Hash, entry["policy_sha256"],
		"the entry pins the policy text that made the decision")
	assert.Contains(t, entry["commands"], "deploy",
		"normalized command names replace the raw line")
	assert.NotEmpty(t, entry["verdict"])
	assert.NotEmpty(t, entry["mode"])
}

func TestAuditRawOptInRedactsSecrets(t *testing.T) {
	p, c, root := testSetup(t)
	p.Audit.Raw = true

	for _, tc := range []struct {
		input  string
		secret string // must not survive
		keep   string // must survive — a fully-blanked log is useless
	}{
		{"mysql --password hunter2 -h prod", "hunter2", "mysql"},
		{"deploy --token=ghp_abc123", "ghp_abc123", "deploy"},
		{"DATABASE_TOKEN=tok123 ./migrate", "tok123", "./migrate"},
		{"export db_password=pw1 && run", "pw1", "export"},
		{"psql postgres://admin:s3cret@db.prod/orders", "s3cret", "psql"},
		{"curl -H 'Authorization: Bearer eyJhbGciOiJIUzI'", "eyJhbGciOiJIUzI", "curl"},
		// Quoted values must be scrubbed whole — a \S+ pattern would
		// leak everything after the first space.
		{"mysql --password 'correct horse battery staple' -h prod", "battery", "prod"},
		{`API_KEY="multi word secret" ./run`, "word secret", "./run"},
	} {
		d := evalCmd(t, p, c, root, tc.input)
		require.NoError(t, Audit(root, "gate", tc.input, p, d))

		entry := lastEntry(t, root)
		raw, _ := entry["input"].(string)

		require.NotEmpty(t, raw, "raw mode must persist an input line for %q", tc.input)
		assert.NotContains(t, raw, tc.secret, "input %q must have its secret scrubbed", tc.input)
		assert.Contains(t, raw, "[REDACTED]", "input %q must show where scrubbing happened", tc.input)
		assert.Contains(t, raw, tc.keep, "input %q must keep its command readable", tc.input)
	}
}

func TestAuditRawKeepsHostAfterURLRedaction(t *testing.T) {
	p, c, root := testSetup(t)
	p.Audit.Raw = true

	input := "psql postgres://admin:s3cret@db.prod/orders"
	require.NoError(t, Audit(root, "gate", input, p, evalCmd(t, p, c, root, input)))

	raw, _ := lastEntry(t, root)["input"].(string)
	assert.Contains(t, raw, "admin:[REDACTED]@db.prod",
		"user and host survive; only the password is scrubbed")
}

func TestAuditRawTruncatesLargeInput(t *testing.T) {
	p, _, root := testSetup(t)
	p.Audit.Raw = true

	// A check-style input: a diff far past the raw-entry bound, with a
	// secret near the end — redaction must run before the cut.
	input := strings.Repeat("+ context line\n", 1000) + "+ PASSWORD=deep_secret\n"
	d := &Decision{Verdict: VerdictAllow, Mode: "warn", PolicySHA: p.Hash}
	require.NoError(t, Audit(root, "check", input, p, d))

	raw, _ := lastEntry(t, root)["input"].(string)
	assert.LessOrEqual(t, len(raw), maxRawInput+len("…[truncated]"))
	assert.Contains(t, raw, "…[truncated]")
	assert.NotContains(t, raw, "deep_secret")
}

func TestAuditFilePermissions(t *testing.T) {
	p, c, root := testSetup(t)

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	info, err := os.Stat(auditPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the audit log must not be world-readable")
}

func TestAuditTightensLegacyPermissions(t *testing.T) {
	p, c, root := testSetup(t)

	// A log created by an older seamark: 0644, with a recent entry. The
	// first append tightens it in place instead of leaving history
	// exposed — and does not rotate it (its entries are fresh).
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	line := `{"ts":"` + time.Now().UTC().Format(time.RFC3339) + `"}` + "\n"
	require.NoError(t, os.WriteFile(auditPath(root), []byte(line), 0o644))

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	info, err := os.Stat(auditPath(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.NoFileExists(t, auditPath(root)+".1", "fresh entries must not rotate")
}

func TestAuditRotatedLegacyLogIsTightened(t *testing.T) {
	p, c, root := testSetup(t)

	// A world-readable legacy log big enough to rotate: the permission
	// fix must land BEFORE the rename, or the raw history this change
	// protects would survive as a 0644 audit.jsonl.1.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	old := strings.Repeat("x", maxAuditBytes)
	require.NoError(t, os.WriteFile(auditPath(root), []byte(old), 0o644))

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	info, err := os.Stat(auditPath(root) + ".1")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the rotated generation must not stay world-readable")
}

func TestAuditRefusesSymlinkedLog(t *testing.T) {
	p, c, root := testSetup(t)

	// A planted symlink must not make the gate append to — or chmod —
	// an arbitrary file.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	victim := filepath.Join(root, "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("precious"), 0o644))
	require.NoError(t, os.Symlink(victim, auditPath(root)))

	err := Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	data, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "precious", string(data), "the symlink target must be untouched")

	info, err := os.Stat(victim)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "target permissions must be untouched")
}

func TestAuditRotatesByAge(t *testing.T) {
	p, c, root := testSetup(t)

	// A live log whose OLDEST entry has aged out rotates even though it
	// is far below the size cap: quiet raw logs must not persist
	// indefinitely.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	oldTS := time.Now().UTC().Add(-maxAuditAge - 24*time.Hour).Format(time.RFC3339)
	line := `{"ts":"` + oldTS + `","kind":"gate"}` + "\n"
	require.NoError(t, os.WriteFile(auditPath(root), []byte(line), 0o600))

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	data, err := os.ReadFile(auditPath(root))
	require.NoError(t, err)
	assert.NotContains(t, string(data), oldTS, "the aged entry left the live log")

	rotated, err := os.ReadFile(auditPath(root) + ".1")
	require.NoError(t, err)
	assert.Contains(t, string(rotated), oldTS, "…into the retained generation")
}

func TestAuditExpiresRotatedGeneration(t *testing.T) {
	p, c, root := testSetup(t)

	// A rotated generation untouched past the age bound is removed: the
	// two-window retention promise holds.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(auditPath(root)+".1", []byte("old\n"), 0o600))

	past := time.Now().Add(-maxAuditAge - 24*time.Hour)
	require.NoError(t, os.Chtimes(auditPath(root)+".1", past, past))

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))
	assert.NoFileExists(t, auditPath(root)+".1")
}

func TestAuditConcurrentAppendsLoseNothing(t *testing.T) {
	p, c, root := testSetup(t)
	d := evalCmd(t, p, c, root, "ls")

	// The audit lock serializes tighten → rotate → append: every
	// concurrent writer's entry must land.
	const n = 20

	var wg sync.WaitGroup

	errs := make([]error, n)

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()
			errs[i] = Audit(root, "gate", "ls", p, d)
		}()
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(auditPath(root))
	require.NoError(t, err)
	assert.Equal(t, n, strings.Count(string(data), "\n"), "no entry may be lost to a race")
}

func TestAuditRotatesBySize(t *testing.T) {
	p, c, root := testSetup(t)

	// A log already past the cap rotates to .1 before the next append.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	old := strings.Repeat("x", maxAuditBytes)
	require.NoError(t, os.WriteFile(auditPath(root), []byte(old), 0o600))

	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	rotated, err := os.Stat(auditPath(root) + ".1")
	require.NoError(t, err)
	assert.Equal(t, int64(maxAuditBytes), rotated.Size(), "the full old log moved aside")

	fresh, err := os.Stat(auditPath(root))
	require.NoError(t, err)
	assert.Less(t, fresh.Size(), int64(4096), "the live log restarted from the rotation")

	// A second rotation replaces the previous generation: bounded, not
	// accumulating.
	require.NoError(t, os.WriteFile(auditPath(root), []byte(old), 0o600))
	require.NoError(t, Audit(root, "gate", "pwd", p, evalCmd(t, p, c, root, "pwd")))

	entries, err := filepath.Glob(auditPath(root) + "*")
	require.NoError(t, err)
	assert.Len(t, entries, 2, "exactly one live log and one rotated generation")
}

func TestAuditAppendsOneLinePerDecision(t *testing.T) {
	p, c, root := testSetup(t)

	require.NoError(t, Audit(root, "gate", "terraform apply", p, evalCmd(t, p, c, root, "terraform apply")))
	require.NoError(t, Audit(root, "gate", "ls", p, evalCmd(t, p, c, root, "ls")))

	data, err := os.ReadFile(auditPath(root))
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(data), "\n"), "append-only JSONL, one entry per decision")
	assert.Contains(t, string(data), `"kind":"gate"`)
}

func TestEvalCommandCollectsNames(t *testing.T) {
	p, c, root := testSetup(t)

	// Wrappers unwrap (sudo is not a command of interest), pipelines and
	// chains contribute each stage, interpreter payloads are included.
	d := evalCmd(t, p, c, root, `sudo terraform apply | tee out.log && bash -c "psql prod"`)
	assert.ElementsMatch(t, []string{"terraform", "tee", "bash", "psql"}, d.Commands)
	assert.NotContains(t, d.Commands, "sudo")
}

func TestRedactSecretsPatterns(t *testing.T) {
	for input, want := range map[string]string{
		"A=1 ls":                         "A=1 ls", // benign assignment untouched
		"MY_TOKEN=abc ls":                "MY_TOKEN=[REDACTED] ls",
		"--password=x --port 5432":       "--password=[REDACTED] --port 5432",
		"http://u:p@h/x":                 "http://u:[REDACTED]@h/x",
		"Authorization: Bearer tok":      "Authorization: Bearer [REDACTED]",
		"echo add token support to docs": "echo add token support to docs", // prose survives
		// Quoted values are consumed whole, spaces included.
		"--password 'a b c' -h x":  "--password [REDACTED] -h x",
		`PASSWORD="a b c" deploy`:  "PASSWORD=[REDACTED] deploy",
		`--token="multi word" run`: "--token=[REDACTED] run",
	} {
		assert.Equal(t, want, redactSecrets(input), "input: %s", input)
	}
}
