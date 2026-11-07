package lessonreport

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

const gatherConfig = "pin:\n  - rule: keep-parser-strict\n    region: internal/parse\n"

// gatherFile captures content, mode, and modification time. The snapshot
// also lists directory entries, so it detects a new private directory.
type gatherFile struct {
	Data    string
	Mode    fs.FileMode
	ModTime time.Time
}

func gatherTree(t *testing.T, root string) map[string]gatherFile {
	t.Helper()

	files := map[string]gatherFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		file := gatherFile{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			file.Data = string(data)
		}

		files[rel] = file

		return nil
	})
	require.NoError(t, err)

	return files
}

func gatherWrite(t *testing.T, root, rel, text string) {
	t.Helper()

	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}

// countedGatherSources wraps the real read boundaries. Tests use a
// separate instance per call, so they replace no process-wide state and
// share no cache.
func countedGatherSources(t *testing.T, req Request) (sources gatherSources, counts map[string]int) {
	t.Helper()

	calls := map[string]int{}
	maxBytes := req.Limits.MaxAuditBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxAuditBytes
	}

	return gatherSources{
		audit: func(root string, limit int64) (reviews.AuditPrefix, error) {
			calls["audit"]++
			assert.Equal(t, req.Root, root)
			assert.Equal(t, maxBytes, limit)

			return reviews.ReadAuditPrefix(root, limit)
		},
		checks: func(root string, limit int64) (lessoncheck.AuditPrefix, error) {
			calls["checks"]++
			assert.Equal(t, req.Root, root)
			assert.Equal(t, maxBytes, limit)

			return lessoncheck.ReadAuditPrefix(root, limit)
		},
		config: func(path string) ([]byte, error) {
			calls["config"]++
			assert.Equal(t, filepath.Join(req.Root, ".seamark", "lessons.yaml"), path)

			return os.ReadFile(path)
		},
		inspect: func(root string) []integration.Inspection {
			calls["inspect"]++
			assert.Equal(t, req.Root, root)

			return integration.Builtin().Inspect(root)
		},
	}, calls
}

func assertGatherCalls(t *testing.T, calls map[string]int) {
	t.Helper()
	assert.Equal(t, map[string]int{"audit": 1, "checks": 1, "config": 1, "inspect": 1}, calls)
}

// assertFixedFields checks the as_of.archive and capture states, two
// fixed fields that every report carries with the same value.
func assertFixedFields(t *testing.T, r Report) {
	t.Helper()
	assert.Equal(t, ArchiveRead{State: StateAbsent, Error: ArchiveNotReadError}, r.AsOf.Archive)
	assert.Equal(t, CaptureState{State: CaptureUnavailable, Reason: "no-archive", Message: CaptureUnavailableMessage}, r.Capture)
}

// TestGatherReadsEachSourceOnceAndWritesNothing exercises real temporary
// sources. Snapshot comparisons detect writes, locks, and new directories.
func TestGatherReadsEachSourceOnceAndWritesNothing(t *testing.T) {
	t.Run("absent-root", func(t *testing.T) {
		root := t.TempDir()
		req := Request{Root: root, Now: func() time.Time { return fixedNow }, SeamarkVersion: "v1.2.3"}
		sources, calls := countedGatherSources(t, req)
		before := gatherTree(t, root)
		r, err := gather(req, sources)
		require.NoError(t, err)
		assertGatherCalls(t, calls)
		assert.Equal(t, before, gatherTree(t, root))
		assertFixedFields(t, r)
		assert.Equal(t, StateAbsent, r.AsOf.Config.State)
		assert.Equal(t, StateAbsent, r.AsOf.Audit.State)
		assert.Equal(t, StateAbsent, r.AsOf.CheckAudit.State)
		assert.Equal(t, "v1.2.3", r.SeamarkVersion)
		assert.Equal(t, formatTime(fixedNow), r.AsOf.GeneratedAt)
		assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	})

	t.Run("sources-present", func(t *testing.T) {
		root := t.TempDir()
		gatherWrite(t, root, ".seamark/lessons.yaml", gatherConfig)
		gatherWrite(t, root, auditPath, "{}\n")
		gatherWrite(t, root, checkAuditPath, "{\"schema\":\"lesson-check-audit/1\",\"kind\":\"verify\"}\n")

		req := Request{Root: root, Now: func() time.Time { return fixedNow }}
		sources, calls := countedGatherSources(t, req)
		before := gatherTree(t, root)
		r, err := gather(req, sources)
		require.NoError(t, err)
		assertGatherCalls(t, calls)
		assert.Equal(t, before, gatherTree(t, root))
		assertFixedFields(t, r)
		assert.Equal(t, ConfigOK, r.AsOf.Config.State)
		assert.Equal(t, StateRead, r.AsOf.Audit.State)
		assert.Equal(t, StateRead, r.AsOf.CheckAudit.State)
		require.Len(t, r.Pins, 1)
	})

	t.Run("config-parity-and-inputs", testGatherConfigInputs)
	t.Run("audit-errors-and-bounds", testGatherAuditInputs)
	t.Run("links-clock-and-pin-errors", testGatherRequestInputs)
}

func testGatherConfigInputs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		state string
	}{
		{"valid", gatherConfig, ConfigOK},
		{"tier-one", "pin: [\n", ConfigInvalid},
		{"tier-two", gatherConfig + "    mode: unknown\n", ConfigOK},
		{"absent", "", StateAbsent},
		{"unreadable", gatherConfig, ConfigInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read a file with no permission bits")
			}

			root := t.TempDir()
			if tc.name != "absent" {
				gatherWrite(t, root, ".seamark/lessons.yaml", tc.text)
			}

			path := filepath.Join(root, ".seamark", "lessons.yaml")
			if tc.name == "unreadable" {
				require.NoError(t, os.Chmod(path, 0o000))
			}

			req := Request{Root: root, SeamarkVersion: "v1.2.3"}
			sources, calls := countedGatherSources(t, req)
			in := gatherInputs(req, sources)
			assertGatherCalls(t, calls)
			assert.Equal(t, tc.state, in.ConfigRead.State)

			cfg, errs := lessoncheck.LoadForCapability(root)
			want := integration.LessonCapabilityOf(integration.Builtin().Inspect(root), cfg, errs, integration.SelfIdentity(req.SeamarkVersion))
			assert.Equal(t, want, *in.Capability)

			if tc.name == "valid" {
				require.NotNil(t, in.Checks)
				assert.Equal(t, cfg, in.Checks)
			} else {
				assert.Nil(t, in.Checks)
			}

			r, err := Build(in, "")
			require.NoError(t, err)
			assert.Equal(t, tc.state, r.AsOf.Config.State)

			if tc.state == ConfigOK {
				require.Len(t, r.Pins, 1)
				assert.Equal(t, in.Config.GuidanceDigest(), r.AsOf.Config.GuidanceSHA256)
				assert.Equal(t, 1, r.AsOf.Config.Pins)
			} else {
				assert.Empty(t, r.Pins)
			}

			if tc.name == "tier-two" {
				assert.False(t, in.Capability.Config.Valid)
				assert.NotEmpty(t, in.Capability.Config.Errors)
			}
		})
	}
}

func testGatherAuditInputs(t *testing.T) {
	t.Run("read-and-limit", func(t *testing.T) {
		root := t.TempDir()
		gatherWrite(t, root, auditPath, "{}\n{}\n")
		gatherWrite(t, root, checkAuditPath+".1", "{\"schema\":\"lesson-check-audit/1\",\"kind\":\"verify\"}\n")
		gatherWrite(t, root, checkAuditPath, "{\"schema\":\"lesson-check-audit/1\",\"kind\":\"post-edit\"}\n")

		for _, limit := range []int64{0, 2} {
			req := Request{Root: root, Limits: Limits{MaxAuditBytes: limit}}
			sources, calls := countedGatherSources(t, req)
			before := gatherTree(t, root)
			r, err := gather(req, sources)
			require.NoError(t, err)
			assertGatherCalls(t, calls)
			assert.Equal(t, before, gatherTree(t, root))

			if limit == 0 {
				assert.Equal(t, StateRead, r.AsOf.Audit.State)
				assert.Equal(t, StateRead, r.AsOf.CheckAudit.State)
				assert.Equal(t, 2, r.AsOf.CheckAudit.Generations)
				assert.Equal(t, 2, r.AsOf.CheckAudit.Records)
			} else {
				assert.Equal(t, StateOverLimit, r.AsOf.Audit.State)
				assert.Equal(t, StateOverLimit, r.AsOf.CheckAudit.State)
				assert.Equal(t, limit, r.AsOf.Audit.Bytes)
				assert.Equal(t, limit, r.AsOf.CheckAudit.Bytes)
			}
		}
	})

	t.Run("read-errors-are-data", func(t *testing.T) {
		root := t.TempDir()
		for _, rel := range []string{auditPath, checkAuditPath} {
			require.NoError(t, os.MkdirAll(filepath.Join(root, rel), 0o700))
		}

		audit, auditErr := reviews.ReadAuditPrefix(root, DefaultMaxAuditBytes)
		checks, checkErr := lessoncheck.ReadAuditPrefix(root, DefaultMaxAuditBytes)
		require.Error(t, auditErr)
		require.Error(t, checkErr)

		req := Request{Root: root}
		sources, calls := countedGatherSources(t, req)
		before := gatherTree(t, root)
		in := gatherInputs(req, sources)
		assertGatherCalls(t, calls)
		assert.Equal(t, before, gatherTree(t, root))
		assert.Equal(t, audit, in.Audit)
		assert.Equal(t, checks, in.CheckAudit)
		assert.Equal(t, auditErr.Error(), in.AuditError)
		assert.Equal(t, checkErr.Error(), in.CheckAuditError)

		r, err := Build(in, "")
		require.NoError(t, err)
		assert.Equal(t, StateUnreadable, r.AsOf.Audit.State)
		assert.Equal(t, StateUnreadable, r.AsOf.CheckAudit.State)
		assert.Equal(t, sanitizeError(auditErr.Error()), r.AsOf.Audit.Error)
		assert.Equal(t, sanitizeError(checkErr.Error()), r.AsOf.CheckAudit.Error)
		assert.NoFileExists(t, filepath.Join(root, checkAuditPath+".lock"))
	})
}

func testGatherRequestInputs(t *testing.T) {
	root := t.TempDir()
	gatherWrite(t, root, ".seamark/lessons.yaml", gatherConfig)

	req := Request{Root: root, Now: func() time.Time { return fixedNow }, Applied: []model.Proposal{
		{ID: 42, Rule: "keep-parser-strict", Region: "internal/parse", Status: model.ProposalApplied},
	}}
	sources, calls := countedGatherSources(t, req)
	in := gatherInputs(req, sources)
	assertGatherCalls(t, calls)
	assert.Equal(t, LinksFromApplied(req.Applied), in.Links)
	assert.Equal(t, ChosenFromApplied(req.Applied), in.Chosen)
	assert.Contains(t, in.Links, reviews.PinIdentityDigest(reviews.NewPinKey("keep-parser-strict", "internal/parse", nil)))
	assert.Equal(t, fixedNow, in.Now)

	start := time.Now()
	r, err := Gather(context.Background(), Request{Root: root})
	require.NoError(t, err)
	generated, err := time.Parse(time.RFC3339, r.AsOf.GeneratedAt)
	require.NoError(t, err)
	assert.WithinDuration(t, start, generated, 2*time.Second)

	clockCalls := 0
	r, err = Gather(context.Background(), Request{Root: root, Now: func() time.Time { clockCalls++; return time.Time{} }})
	require.NoError(t, err)
	assert.Equal(t, 1, clockCalls)
	assert.Empty(t, r.AsOf.GeneratedAt, "a supplied zero clock is not replaced")

	_, err = Gather(context.Background(), Request{Root: root, PinRef: "not-a-pin"})
	var pinErr *reviews.PinRefError
	require.ErrorAs(t, err, &pinErr)

	// No source read takes the context, so a canceled context gives the
	// same report.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := Gather(ctx, Request{Root: root, Now: func() time.Time { return fixedNow }})
	require.NoError(t, err)
	assertFixedFields(t, canceled)

	want, err := Gather(context.Background(), Request{Root: root, Now: func() time.Time { return fixedNow }})
	require.NoError(t, err)
	assert.Equal(t, want, canceled)
}
