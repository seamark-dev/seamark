package integration

// The parity test of the Codex patch projection against the content
// oracle. Each content case holds the files before a patch and the files
// that the codex-cli applier left after it. The guard decodes the patch
// and projects it onto the same files. A projection that names other
// bytes than the applier wrote fails the test, with no tolerance.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"regexp"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// oracleUnknown lists the content cases that the applier accepts and
// that the projection keeps unknown on purpose, each with its reason.
// Every other accepted case must project exactly.
var oracleUnknown = map[string]string{
	"76_lone_cr_line_ends":                            "the applier reads a lone CR as a line end, and the projection keeps such a file unknown",
	"77_lone_cr_inside_line":                          "the applier reads a lone CR as a line end, and the projection keeps such a file unknown",
	"78_same_file_twice":                              "the applier applies two sections of one path in sequence, and the projection reads each path once",
	"79_add_then_update_same_file":                    "the applier applies two sections of one path in sequence, and the projection reads each path once",
	"96_change_lines_end_with_carriage_returns":       "the applier removes two carriage returns at a line end and the decoder one, so the decoder keeps such a section outside its grammar",
	"97_add_lines_end_with_two_carriage_returns":      "the applier removes two carriage returns at a line end and the decoder one, so the decoder gives such an add no content",
	"98_add_line_ends_with_three_carriage_returns":    "the applier removes two carriage returns at a line end and the decoder one, so the decoder gives such an add no content",
	"99_change_line_ends_with_three_carriage_returns": "the applier removes two carriage returns at a line end and the decoder one, so the decoder keeps such a section outside its grammar",
}

// refusedPathPattern finds the path that a refusal of the applier names.
// A grammar refusal names a line number and no path.
var refusedPathPattern = regexp.MustCompile(`<scratch>/([^\s:]+)`)

// TestCodexPatchProjectionMatchesTheContentOracle projects every content
// case of the oracle and compares the result with the bytes that the
// applier wrote.
//
//   - An accepted case projects every change exactly, unless
//     oracleUnknown lists it. A file that no change names keeps its
//     bytes.
//   - A refused case keeps the refused update unknown. An exact change
//     of a refused case names the bytes on disk after the refusal: the
//     applier keeps the files that it wrote before the refusal.
func TestCodexPatchProjectionMatchesTheContentOracle(t *testing.T) {
	requireTools(t, "git")

	_, oracle := loadApplyPatchOracle(t)
	require.NotNil(t, oracle.Content, "the content part is missing")

	var exact atomic.Int64

	// The group returns when every parallel case is done, so the count
	// below is complete. Each case has its own repository.
	t.Run("cases", func(t *testing.T) {
		for _, tc := range oracle.Content.Cases {
			t.Run(tc.Name, func(t *testing.T) {
				t.Parallel()

				if checkOracleCase(t, tc) {
					exact.Add(1)
				}
			})
		}
	})

	t.Logf("%d content cases project exactly", exact.Load())
}

// checkOracleCase projects one content case and compares it with the
// applier result. It reports whether every change projected exactly.
func checkOracleCase(t *testing.T, tc contentCase) bool {
	t.Helper()

	snap, ops := projectOracleCase(t, tc.Before, tc.Patch)

	for _, ch := range snap.Changes {
		if projected(ch) {
			assert.Equal(t, afterState(tc.After, ch.Path), projectedState(ch), "the projection of %s differs from the applier", ch.Path)
		}

		if ch.OldPath != "" {
			_, kept := tc.After[ch.OldPath]
			assert.False(t, kept && tc.ExitStatus == 0, "the move source %s stays after the run", ch.OldPath)
		}
	}

	reason, unknown := oracleUnknown[tc.Name]

	switch {
	case tc.ExitStatus == 0 && unknown:
		assert.True(t, slices.ContainsFunc(snap.Changes, func(ch lessoncheck.Change) bool { return !projected(ch) }),
			"the case is listed as unknown (%s), but every change projects", reason)
	case tc.ExitStatus == 0:
		for _, ch := range snap.Changes {
			assert.True(t, projected(ch), "%s has no projection, but the applier accepted the patch", ch.Path)
		}

		assertUntouchedFilesKept(t, tc.Before, tc.After, snap.Changes)

		return true
	default:
		assertRefusedUpdateUnknown(t, tc.Stderr, ops, snap.Changes)
	}

	return false
}

// projectOracleCase writes the before files into a new git repository
// and projects the patch with the decoder and the projector of the
// guard. It returns the snapshot and the decoded operations.
func projectOracleCase(t *testing.T, before map[string]string, patch string) (*lessoncheck.Snapshot, []lessoncheck.FileOp) {
	t.Helper()

	root := t.TempDir()

	out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	writeScratchFiles(t, root, before)

	ops, err := applyPatchOps(patch)
	require.NoError(t, err, "the guard decoder refuses an oracle patch")

	op := lessoncheck.Operation{Client: CodexID, Tool: codexApplyPatch, Ops: ops, PathsComplete: true}

	snap, err := lessoncheck.ProjectOperation(context.Background(), root, op, lessoncheck.Limits{})
	require.NoError(t, err)

	return snap, ops
}

// projected reports whether a change has a projected after side.
func projected(ch lessoncheck.Change) bool {
	return !slices.Contains(ch.Limitations, lessoncheck.LimitationNoProjection)
}

// fileState names the state of one path: absent, or the SHA-256 of its
// content.
type fileState struct {
	Absent bool
	SHA256 string
}

// afterState returns the state of a path in the files of an oracle case.
func afterState(files map[string]string, path string) fileState {
	content, ok := files[path]
	if !ok {
		return fileState{Absent: true}
	}

	return fileState{SHA256: contentSHA256(content)}
}

// contentSHA256 returns the hex SHA-256 of content, as a snapshot side
// names it.
func contentSHA256(content string) string {
	sum := sha256.Sum256([]byte(content))

	return hex.EncodeToString(sum[:])
}

// projectedState returns the projected state of a change.
func projectedState(ch lessoncheck.Change) fileState {
	if ch.After.Type == lessoncheck.ObjectAbsent {
		return fileState{Absent: true}
	}

	return fileState{SHA256: ch.After.SHA256}
}

// assertUntouchedFilesKept checks that every file that no change names
// has the same state before and after the run. The projection then names
// every file that the applier wrote.
func assertUntouchedFilesKept(t *testing.T, before, after map[string]string, changes []lessoncheck.Change) {
	t.Helper()

	named := map[string]bool{}

	for _, ch := range changes {
		named[ch.Path], named[ch.OldPath] = true, true
	}

	for _, files := range []map[string]string{before, after} {
		for path := range files {
			if !named[path] {
				assert.Equal(t, afterState(before, path), afterState(after, path), "the applier changed %s, and no change names it", path)
			}
		}
	}
}

// assertRefusedUpdateUnknown checks that the projection keeps the
// update that the applier refused unknown. A refusal that names a path
// names the file that the applier failed on. A grammar refusal names no
// path, so every update of the patch must stay unknown.
func assertRefusedUpdateUnknown(t *testing.T, stderr string, ops []lessoncheck.FileOp, changes []lessoncheck.Change) {
	t.Helper()

	refused := ""
	if m := refusedPathPattern.FindStringSubmatch(stderr); m != nil {
		refused = m[1]
	}

	checked := 0

	for _, fop := range ops {
		if fop.Kind != lessoncheck.OpPatchUpdate || (refused != "" && fop.Path != refused) {
			continue
		}

		for _, ch := range changes {
			if ch.Path == fop.Path || ch.OldPath == fop.Path || (fop.MoveTo != "" && ch.Path == fop.MoveTo) {
				assert.False(t, projected(ch), "the applier refused the update of %s, and the projection gives content", fop.Path)

				checked++
			}
		}
	}

	if refused == "" {
		require.NotZero(t, checked, "a grammar refusal without an update section: %q", stderr)
	}
}
