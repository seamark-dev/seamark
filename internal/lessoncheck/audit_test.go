package lessoncheck

import (
	"context"
	"encoding/base64"
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

// The tests of the lesson-check audit stream (contract K9). Every root
// is a temporary directory.

// auditPath returns the path of the audit stream under root.
func auditPath(root string) string {
	return filepath.Join(root, ".seamark", "lesson-check-audit.jsonl")
}

// discardedRecord is a small valid record.
func discardedRecord() AuditRecord {
	return AuditRecord{Kind: AuditBaselineDiscarded, Caller: CallerHook, Discarded: DiscardExpired}
}

// TestLessonCheckAuditAppendIsHardened proves the hardening that the
// writer copies from gate.Audit: owner-only files, a refused link at
// every path, the tightened rotated generation, the rotation by size
// and by age, the expiry of the rotated generation, the closed lists,
// and the output caps.
func TestLessonCheckAuditAppendIsHardened(t *testing.T) {
	t.Run("the files are owner-only and .seamark gets 0700", func(t *testing.T) {
		root := t.TempDir()

		require.NoError(t, AppendAudit(root, discardedRecord()))

		requireMode(t, filepath.Join(root, ".seamark"), 0o700)
		requireMode(t, auditPath(root), 0o600)
		requireMode(t, filepath.Join(root, ".seamark", "lesson-check-audit.jsonl.lock"), 0o600)
		assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons-audit.jsonl"), "the firing log gets nothing (§5 row 39)")
	})

	t.Run("a loose log is tightened", func(t *testing.T) {
		if !posixModes {
			t.Skip("POSIX modes are not enforced on this platform")
		}

		root := t.TempDir()
		require.NoError(t, AppendAudit(root, discardedRecord()))
		require.NoError(t, os.Chmod(auditPath(root), 0o644))

		require.NoError(t, AppendAudit(root, discardedRecord()))
		requireMode(t, auditPath(root), 0o600)
		assert.Len(t, readAuditLines(t, root), 2)
	})

	t.Run("links are refused and never followed", func(t *testing.T) {
		skipWithoutSymlinks(t)

		for _, name := range []string{"lesson-check-audit.jsonl", "lesson-check-audit.jsonl.lock"} {
			root := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))

			require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
			require.NoError(t, os.Symlink(target, filepath.Join(root, ".seamark", name)))

			err := AppendAudit(root, discardedRecord())
			require.ErrorIs(t, err, errUnsafePath, name)
			assert.Contains(t, err.Error(), "is a symbolic link")

			data, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, "keep\n", string(data), "nothing goes through the link of %s", name)
		}

		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, ".seamark")))

		err := AppendAudit(root, discardedRecord())
		require.ErrorIs(t, err, errUnsafePath)

		entries, err := os.ReadDir(outside)
		require.NoError(t, err)
		assert.Empty(t, entries, "a linked .seamark gets nothing")
	})

	t.Run("the rotated generation is tightened, and a planted link is removed", func(t *testing.T) {
		skipWithoutSymlinks(t)

		root := t.TempDir()
		require.NoError(t, AppendAudit(root, discardedRecord()))

		require.NoError(t, os.WriteFile(auditPath(root)+".1", []byte("{}\n"), 0o644))
		require.NoError(t, os.Chmod(auditPath(root)+".1", 0o644))
		require.NoError(t, AppendAudit(root, discardedRecord()))
		requireMode(t, auditPath(root)+".1", 0o600)

		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))
		require.NoError(t, os.Remove(auditPath(root)+".1"))
		require.NoError(t, os.Symlink(target, auditPath(root)+".1"))

		require.NoError(t, AppendAudit(root, discardedRecord()))

		_, err := os.Lstat(auditPath(root) + ".1")
		assert.ErrorIs(t, err, os.ErrNotExist, "the planted link is gone")

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})

	t.Run("the log rotates at 5 MiB and keeps one generation", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, AppendAudit(root, discardedRecord()))

		first, err := os.ReadFile(auditPath(root))
		require.NoError(t, err)

		filler := strings.Repeat("x", maxAuditBytes)
		require.NoError(t, os.WriteFile(auditPath(root), append(first, filler...), 0o600))

		require.NoError(t, AppendAudit(root, discardedRecord()))

		rotated, err := os.Stat(auditPath(root) + ".1")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, rotated.Size(), int64(maxAuditBytes))
		assert.Len(t, readAuditLines(t, root), 1, "the live log starts again")
	})

	t.Run("the log rotates when its oldest record is 30 days old", func(t *testing.T) {
		root := t.TempDir()

		old := discardedRecord()
		old.TS = time.Now().Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)
		require.NoError(t, AppendAudit(root, old))
		require.NoError(t, AppendAudit(root, discardedRecord()))

		assert.FileExists(t, auditPath(root)+".1")
		assert.Len(t, readAuditLines(t, root), 1)
	})

	t.Run("a first line that does not parse rotates", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o700))
		require.NoError(t, os.WriteFile(auditPath(root), []byte("not json\n"), 0o600))

		require.NoError(t, AppendAudit(root, discardedRecord()))
		assert.FileExists(t, auditPath(root)+".1")
	})

	t.Run("an old rotated generation expires", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, AppendAudit(root, discardedRecord()))
		require.NoError(t, os.WriteFile(auditPath(root)+".1", []byte("{}\n"), 0o600))
		ageFile(t, auditPath(root)+".1", 31*24*time.Hour)

		require.NoError(t, AppendAudit(root, discardedRecord()))
		assert.NoFileExists(t, auditPath(root)+".1")
	})

	t.Run("the closed lists are kept", func(t *testing.T) {
		root := t.TempDir()

		bad := discardedRecord()
		bad.Kind = "denied"
		require.Error(t, AppendAudit(root, bad))

		bad = discardedRecord()
		bad.Caller = "cron"
		require.Error(t, AppendAudit(root, bad))

		assert.NoFileExists(t, auditPath(root), "a refused record writes nothing")
	})

	t.Run("the writer sets the schema and the time and applies the caps", func(t *testing.T) {
		root := t.TempDir()

		paths := make([]string, 60)
		for i := range paths {
			paths[i] = fmt.Sprintf("src/f%02d.go", i)
		}

		lines := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		pin := AuditPin{PinIdentity: "id", PinVersion: "v", Mode: ModeEnforce, Outcome: OutcomeViolation, EvidencePaths: 12, EvidenceLines: lines}

		secret := "s3cr3t-value-0042"

		rec := AuditRecord{
			Schema:       "something-else/9",
			Kind:         AuditGuard,
			Caller:       CallerHook,
			Paths:        paths,
			PathsOmitted: 2,
			Pins:         []AuditPin{pin},
			Guard:        &GuardDecision{Schema: "lesson-guard/1", Decision: "deny", Pins: []AuditPin{pin}},
			Error:        "send: Bearer " + secret + " \x1b[31m " + strings.Repeat("é", 200),
		}

		require.NoError(t, AppendAudit(root, rec))
		assert.Len(t, rec.Paths, 60, "AppendAudit never changes the caller's record")
		assert.Len(t, rec.Guard.Pins[0].EvidenceLines, 12)

		recs := readAudit(t, root)
		require.Len(t, recs, 1)

		got := recs[0]
		assert.Equal(t, AuditSchema, got.Schema)

		ts, err := time.Parse(time.RFC3339, got.TS)
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now(), ts, time.Minute)
		assert.True(t, strings.HasSuffix(got.TS, "Z"))

		assert.Equal(t, paths[:50], got.Paths)
		assert.Equal(t, 12, got.PathsOmitted, "the omitted count adds to the caller's count")
		assert.Equal(t, lines[:10], got.Pins[0].EvidenceLines)
		assert.Equal(t, 12, got.Pins[0].EvidencePaths, "the path count stays whole")
		assert.Equal(t, lines[:10], got.Guard.Pins[0].EvidenceLines)

		assert.LessOrEqual(t, len(got.Error), maxAuditErrorBytes)
		assert.NotContains(t, got.Error, secret)
		assert.NotContains(t, got.Error, "\x1b")
		assert.True(t, strings.HasPrefix(got.Error, "send: Bearer [REDACTED]"), got.Error)
		assert.Equal(t, strings.ToValidUTF8(got.Error, "?"), got.Error, "the cut keeps whole runes")
	})
}

// TestLessonCheckAuditCarriesNoContent proves contracts §1.6 for the
// stream: a verify record and a baseline-discarded record hold no file
// content, no excerpt, no rule text, no base64 content, and no raw
// provider ID, also when the result itself shows an excerpt.
func TestLessonCheckAuditCarriesNoContent(t *testing.T) {
	const marker = "MARKER-7f3a-content"

	lessons := strings.Replace(mixedYAML, "rule: no-todo\n", "rule: no-todo-"+strings.ToLower(marker)+"\n", 1)

	g := verifyRepo(t, lessons)
	g.write("src/a.go", "package a\n\n// TODO("+marker+") split.\n")

	v := g.verify()
	require.Equal(t, SummaryViolation, v.Summary.State)

	// The result shows the excerpt, so the test checks a real risk.
	text := renderText(v)
	require.Contains(t, text, marker)

	// A pending record holds content. Its discard record must not.
	p := simplePending(t, rawSession, rawMatch)
	p.Paths[0].ContentB64 = base64.StdEncoding.EncodeToString([]byte("secret body " + marker))
	require.NoError(t, WritePending(context.Background(), g.root, p, DefaultLimits()))
	ageFile(t, pendingFile(g.root, p), 2*time.Hour)

	removed, err := Sweep(g.root, CallerCLI, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	data, err := os.ReadFile(auditPath(g.root))
	require.NoError(t, err)

	stream := string(data)
	require.Contains(t, stream, `"kind":"verify"`)
	require.Contains(t, stream, `"kind":"baseline-discarded"`)

	forbidden := []string{
		marker, strings.ToLower(marker), "TODO(", "split.",
		p.Paths[0].ContentB64, base64.StdEncoding.EncodeToString([]byte(marker)),
		rawSession, rawMatch, rawReceiver,
		`"excerpt"`, `"rule"`, `"content_b64"`, `"evidence"`,
	}

	for _, s := range forbidden {
		assert.NotContains(t, stream, s)
	}

	assert.NoFileExists(t, filepath.Join(g.root, ".seamark", "lessons-audit.jsonl"), "the firing log gets nothing (§5 row 39)")
	assert.NoDirExists(t, filepath.Join(g.root, ".seamark", "private"))

	// Every key of every record is a K9 field, so no new field can carry
	// content unnoticed.
	known := map[string]bool{}
	for _, k := range []string{
		"schema", "ts", "kind", "caller", "client", "tool", "session_sha256", "match_sha256", "context_sha256",
		"boundary", "config_sha256", "snapshot_sha256", "paths", "paths_omitted", "paths_complete", "pins",
		"state", "guard", "tool_outcome", "attribution", "discarded", "emitted", "error",
	} {
		known[k] = true
	}

	for _, line := range readAuditLines(t, g.root) {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(line), &fields))

		for k := range fields {
			assert.True(t, known[k], "unknown key %q", k)
		}
	}
}

// TestLessonCheckAuditConcurrentAppends proves that concurrent writers
// lose no record and tear no line. The make targets run it with -race.
func TestLessonCheckAuditConcurrentAppends(t *testing.T) {
	root := t.TempDir()

	const writers, each = 16, 12

	var wg sync.WaitGroup

	errs := make(chan error, writers*each)

	for w := range writers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range each {
				rec := discardedRecord()
				rec.MatchSHA = fmt.Sprintf("%02d-%02d", w, i)
				errs <- AppendAudit(root, rec)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	seen := map[string]bool{}

	for _, rec := range readAudit(t, root) {
		assert.Equal(t, AuditSchema, rec.Schema)
		seen[rec.MatchSHA] = true
	}

	assert.Len(t, seen, writers*each, "every record lands once")
}

// TestLessonCheckVerifyWritesAuditAndSweeps proves the two milestone P4
// steps of Verify (contract K5): one verify record after the result is
// composed, and the K7 sweep. A failed record never changes the result.
// A stopped read still writes its record.
func TestLessonCheckVerifyWritesAuditAndSweeps(t *testing.T) {
	g := verifyRepo(t, mixedYAML)
	g.write("generated/a.txt", "a changed\n")
	g.write("src/a.go", "package a\n\n// TODO(x)\n")

	expired := simplePending(t, rawSession, rawMatch)
	fresh := simplePending(t, rawSession, "toolu-fresh")

	require.NoError(t, WritePending(context.Background(), g.root, expired, DefaultLimits()))
	require.NoError(t, WritePending(context.Background(), g.root, fresh, DefaultLimits()))
	ageFile(t, pendingFile(g.root, expired), 2*time.Hour)

	oldTmp := filepath.Join(g.root, ".seamark", "checks", "pending", ".tmp-crashed")
	require.NoError(t, os.WriteFile(oldTmp, []byte("partial"), 0o600))
	ageFile(t, oldTmp, time.Hour)

	v := g.verify()
	require.NoError(t, v.AuditError())
	require.Equal(t, SummaryViolation, v.Summary.State)

	recs := readAudit(t, g.root)
	require.Len(t, recs, 2)

	verify := recs[0]
	assert.Equal(t, AuditRecord{
		Schema:        AuditSchema,
		TS:            checkedAtText,
		Kind:          AuditVerify,
		Caller:        CallerCLI,
		Boundary:      BoundaryWorkingTree,
		ConfigSHA256:  v.Binding.ConfigSHA256,
		SnapshotSHA:   v.Binding.SnapshotSHA256,
		Paths:         []string{"generated/a.txt", "src/a.go"},
		PathsComplete: true,
		Pins:          auditPinsOf(v.Results),
		State:         SummaryViolation,
	}, verify, "the verify record comes first")

	require.Len(t, verify.Pins, 2)
	assert.Equal(t, OutcomeViolation, verify.Pins[0].Outcome)
	assert.Equal(t, 1, verify.Pins[0].EvidencePaths)
	assert.Equal(t, OutcomeViolation, verify.Pins[1].Outcome)
	assert.Equal(t, []int{3}, verify.Pins[1].EvidenceLines)

	discarded := recs[1]
	assert.Equal(t, AuditBaselineDiscarded, discarded.Kind)
	assert.Equal(t, DiscardExpired, discarded.Discarded)
	assert.Equal(t, CallerCLI, discarded.Caller, "the sweep records the caller of the verification")
	assert.Equal(t, expired.MatchSHA, discarded.MatchSHA)

	assert.NoFileExists(t, pendingFile(g.root, expired))
	assert.NoFileExists(t, oldTmp)
	assert.FileExists(t, pendingFile(g.root, fresh))

	t.Run("a failed record never changes the result", func(t *testing.T) {
		skipWithoutSymlinks(t)

		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))

		live := auditPath(g.root)
		require.NoError(t, os.Remove(live))
		require.NoError(t, os.Symlink(target, live))

		failed := g.verify()
		require.ErrorIs(t, failed.AuditError(), errUnsafePath)

		require.NoError(t, os.Remove(live))

		clean := g.verify()
		require.NoError(t, clean.AuditError())

		failedJSON, err := json.Marshal(failed)
		require.NoError(t, err)

		cleanJSON, err := json.Marshal(clean)
		require.NoError(t, err)

		assert.Equal(t, string(cleanJSON), string(failedJSON))
		assert.NotContains(t, string(failedJSON), "symbolic link", "the JSON output holds no audit error")
		assert.Equal(t, failed.ExitError().Error(), clean.ExitError().Error())

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})

	t.Run("a stopped read still writes its record", func(t *testing.T) {
		require.NoError(t, os.Remove(auditPath(g.root)))

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		stopped := g.verifyCtx(ctx, DefaultLimits())
		require.Equal(t, FreshnessStale, stopped.Freshness)
		require.NoError(t, stopped.AuditError())

		recs := readAudit(t, g.root)
		require.Len(t, recs, 1)
		assert.Equal(t, AuditVerify, recs[0].Kind)
		assert.Equal(t, SummaryIncomplete, recs[0].State)
		assert.False(t, recs[0].PathsComplete)
	})

	t.Run("each caller names itself", func(t *testing.T) {
		require.NoError(t, os.Remove(auditPath(g.root)))

		for _, caller := range []Caller{CallerMCP, CallerTUI} {
			req := verifyRequest(g.root, DefaultLimits())
			req.Caller = caller

			_, err := Verify(context.Background(), req)
			require.NoError(t, err)
		}

		recs := readAudit(t, g.root)
		require.Len(t, recs, 2)
		assert.Equal(t, CallerMCP, recs[0].Caller)
		assert.Equal(t, CallerTUI, recs[1].Caller)
	})

	t.Run("a repository without pending records gets no checks directory", func(t *testing.T) {
		other := verifyRepo(t, mixedYAML)
		other.verify()

		assert.NoDirExists(t, filepath.Join(other.root, ".seamark", "checks"))
		assert.Len(t, readAudit(t, other.root), 1)
		assert.NoFileExists(t, filepath.Join(other.root, ".seamark", "lessons-audit.jsonl"))
	})
}
