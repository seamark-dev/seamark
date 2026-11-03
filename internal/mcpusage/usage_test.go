package mcpusage

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// workspace returns a root with a .seamark directory.
func workspace(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, seamarkDir), 0o755))

	return root
}

// line returns one valid record with the given values.
func line(tool, client string, ms, size int64, ok bool) string {
	data, err := json.Marshal(Record{
		V: Version, At: testNow.Format(time.RFC3339), Tool: tool, Client: client, MS: ms, Bytes: size, OK: ok,
	})
	if err != nil {
		panic(err)
	}

	return string(data) + "\n"
}

func TestAppendWritesTheWholeRecord(t *testing.T) {
	root := workspace(t)

	require.NoError(t, Append(root, Record{Tool: "why", Client: "claude-code", MS: 12, Bytes: 34, OK: true},
		testNow.In(time.FixedZone("east", 3600))))

	data, err := os.ReadFile(Path(root))
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1,"at":"2026-11-03T10:00:00Z","tool":"why","client":"claude-code","ms":12,"bytes":34,"ok":true}`,
		string(data))

	info, err := os.Stat(Path(root))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAppendKeepsAYoungLogInPlace(t *testing.T) {
	root := workspace(t)

	// The rotation must read the field "at". With the audit field "ts",
	// each append would see a first record without a time and rotate.
	for range 3 {
		require.NoError(t, Append(root, Record{Tool: "why", Client: "c", OK: true}, testNow))
	}

	assert.NoFileExists(t, Path(root)+".1")
	assert.Equal(t, 3, Read(root).Calls)
}

func TestAppendRotatesAtTheSizeLimit(t *testing.T) {
	root := workspace(t)
	first := line("why", "c", 1, 1, true)

	require.NoError(t, os.WriteFile(Path(root), []byte(first+strings.Repeat("x", maxUsageBytes)+"\n"), 0o600))
	require.NoError(t, Append(root, Record{Tool: "check", Client: "c", OK: true}, testNow))

	assert.FileExists(t, Path(root)+".1")

	live, err := os.ReadFile(Path(root))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(live), "\n"), "the live file starts again")

	report := Read(root)
	assert.Equal(t, 2, report.Calls, "the read covers both generations")
	assert.Equal(t, 1, report.Skipped)
}

func TestAppendRotatesAStaleLog(t *testing.T) {
	root := workspace(t)

	require.NoError(t, os.WriteFile(Path(root), []byte(line("why", "c", 1, 1, true)), 0o600))
	require.NoError(t, Append(root, Record{Tool: "check", Client: "c", OK: true}, testNow.Add(maxUsageAge+time.Hour)))

	assert.FileExists(t, Path(root)+".1")
}

func TestAppendWithoutSeamarkCreatesNothing(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, Append(root, Record{Tool: "why", Client: "c", OK: true}, testNow))

	assert.NoDirExists(t, filepath.Join(root, seamarkDir))
}

func TestAppendRefusesALink(t *testing.T) {
	t.Run("at the log", func(t *testing.T) {
		root := workspace(t)
		target := filepath.Join(t.TempDir(), "target")

		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o644))
		require.NoError(t, os.Symlink(target, Path(root)))

		require.Error(t, Append(root, Record{Tool: "why", Client: "c", OK: true}, testNow))

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})

	t.Run("at the directory", func(t *testing.T) {
		root := t.TempDir()
		target := t.TempDir()

		require.NoError(t, os.Symlink(target, filepath.Join(root, seamarkDir)))

		require.Error(t, Append(root, Record{Tool: "why", Client: "c", OK: true}, testNow))
		assert.NoFileExists(t, filepath.Join(target, usageFile))
	})
}

func TestAppendKeepsWholeLinesUnderConcurrency(t *testing.T) {
	root := workspace(t)

	const writers, each = 8, 25

	var wg sync.WaitGroup

	for w := range writers {
		wg.Go(func() {
			for range each {
				assert.NoError(t, Append(root, Record{Tool: "why", Client: fmt.Sprintf("c%d", w), OK: true}, testNow))
			}
		})
	}

	wg.Wait()

	report := Read(root)
	assert.Equal(t, writers*each, report.Calls)
	assert.Zero(t, report.Skipped)
}

func TestCleanName(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{name: "plain", in: "claude-code", want: "claude-code"},
		{name: "empty", in: "", want: Unknown},
		{name: "only controls", in: "\x1b\x07", want: Unknown},
		{name: "escape removed", in: "cli\x1b[31ment", want: "cli[31ment"},
		{name: "one line", in: "a\tb\nc", want: "a bc"},
		{name: "bounded", in: strings.Repeat("x", 200), want: strings.Repeat("x", maxNameBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CleanName(tc.in))
		})
	}
}

func TestReadSummarizesPerToolAndClient(t *testing.T) {
	root := workspace(t)

	body := line("why", "a", 10, 100, true) + line("why", "a", 30, 300, false) + line("why", "a", 20, 200, true) +
		line("why", "a", 40, 400, true) + line("why", "b", 5, 50, true) + line("check", "a", 7, 70, false)

	require.NoError(t, os.WriteFile(Path(root), []byte(body), 0o600))

	report := Read(root)

	require.NoError(t, report.Err)
	assert.True(t, report.Present)
	assert.Equal(t, 6, report.Calls)
	assert.Equal(t, 2, report.Failed)
	assert.Equal(t, []Row{
		{Tool: "why", Client: "a", Calls: 4, Failed: 1, MedianMS: 25, MaxMS: 40, MedianBytes: 250},
		{Tool: "check", Client: "a", Calls: 1, Failed: 1, MedianMS: 7, MaxMS: 7, MedianBytes: 70},
		{Tool: "why", Client: "b", Calls: 1, Failed: 0, MedianMS: 5, MaxMS: 5, MedianBytes: 50},
	}, report.Rows, "the most calls first, then tool and client")
}

func TestReadWithoutAFileIsNotPresent(t *testing.T) {
	assert.Equal(t, Report{}, Read(workspace(t)))
}

func TestReadSkipsDamagedLines(t *testing.T) {
	root := workspace(t)

	good := line("why", "a", 1, 1, true)

	for _, bad := range []string{
		"not json",
		`{"v":1`,
		`[1]`,
		strings.Replace(good, `"v":1`, `"v":2`, 1),
		strings.Replace(good, `"ok":true`, `"other":true`, 1),
		strings.Replace(good, `"ms":1`, `"other":1`, 1),
		strings.Replace(good, `"ms":1`, `"ms":-1`, 1),
		strings.Replace(good, `"bytes":1`, `"other":1`, 1),
		strings.Replace(good, `"tool":"why"`, `"tool":""`, 1),
		strings.Replace(good, `"client":"a"`, `"client":""`, 1),
		strings.Replace(good, `"at":"`, `"at":"x`, 1),
	} {
		t.Run(bad, func(t *testing.T) {
			require.NoError(t, os.WriteFile(Path(root), []byte(good+strings.TrimSpace(bad)+"\n"+good), 0o600))

			report := Read(root)

			require.NoError(t, report.Err)
			assert.Equal(t, 2, report.Calls)
			assert.Equal(t, 1, report.Skipped)
		})
	}
}

func TestReadCountsAFinalLineWithoutANewline(t *testing.T) {
	root := workspace(t)

	require.NoError(t, os.WriteFile(Path(root), []byte(strings.TrimSpace(line("why", "a", 1, 1, true))), 0o600))

	assert.Equal(t, 1, Read(root).Calls)
}

func TestReadCleansTheFileText(t *testing.T) {
	root := workspace(t)

	body := strings.Replace(line("why", "a", 1, 1, true), `"tool":"why"`, `"tool":"\u001b[31mwhy"`, 1)
	require.NoError(t, os.WriteFile(Path(root), []byte(body), 0o600))

	report := Read(root)

	require.Len(t, report.Rows, 1)
	assert.Equal(t, "[31mwhy", report.Rows[0].Tool)
}

func TestReadReportsAnUnreadableFile(t *testing.T) {
	t.Run("a directory at the log", func(t *testing.T) {
		root := workspace(t)

		require.NoError(t, os.Mkdir(Path(root), 0o755))

		report := Read(root)

		assert.True(t, report.Present)
		require.Error(t, report.Err)
		assert.Empty(t, report.Rows)
		assert.Zero(t, report.Calls)
	})

	t.Run("a link at the log", func(t *testing.T) {
		root := workspace(t)
		target := filepath.Join(t.TempDir(), "target")

		require.NoError(t, os.WriteFile(target, []byte(line("why", "a", 1, 1, true)), 0o600))
		require.NoError(t, os.Symlink(target, Path(root)))

		report := Read(root)

		assert.True(t, report.Present)
		require.Error(t, report.Err)
	})

	t.Run("a good log beside a bad rotated file", func(t *testing.T) {
		root := workspace(t)

		require.NoError(t, os.WriteFile(Path(root), []byte(line("why", "a", 1, 1, true)), 0o600))
		require.NoError(t, os.Mkdir(Path(root)+".1", 0o755))

		report := Read(root)

		require.Error(t, report.Err)
		assert.Zero(t, report.Calls, "a part of the count must not read as the count")
	})
}

// TestOnlyTheStatsCommandReadsTheLog keeps the file away from a hook. A
// hook has a latency budget, and it must not depend on this count. The
// stats command is the one reader, and it shares a package with the
// hooks, so the test looks at each file of production code.
func TestOnlyTheStatsCommandReadsTheLog(t *testing.T) {
	const moduleRoot = "../.."

	var readers []string

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(moduleRoot, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			if n := strings.Count(string(data), "mcpusage.Read("); n > 0 {
				readers = append(readers, fmt.Sprintf("%s:%d", filepath.ToSlash(path), n))
			}

			return nil
		})
		require.NoError(t, err)
	}

	assert.Equal(t, []string{"../../internal/cli/lessons.go:1"}, readers)
}
