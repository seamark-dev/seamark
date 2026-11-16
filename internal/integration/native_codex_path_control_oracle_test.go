package integration

// The path-control oracle of apply_patch. Each case puts a control or
// space character into a patch header path, applies the patch with the
// installed Codex applier, and records what it wrote. codex-cli 0.169.0
// removes a tab and a carriage return from a header path when it
// writes, and keeps every other character. guardPatchOps refuses such a
// path, so the guard never judges a path that Codex does not write.
//
// Three tests use the file:
//
//   - TestApplyPatchPathControlsKeepTheGuardRule runs offline in every
//     test run. For each recorded case, the guard decoder refuses the
//     patch or names every file that the applier changed.
//   - TestNativeCodexPathControlsHoldOnTheInstalledBinary replays the
//     cases and a generated sweep through the installed binary in
//     `make agents-native-check`.
//   - TestRecordCodexPathControlOracle writes the file. It runs only
//     through `make agents-oracle-record`, which names the expected
//     Codex version.
//
// The applier runs as in the content oracle: the codex binary under the
// name apply_patch, a scratch CODEX_HOME, and a new scratch directory
// per case. It reads no login and starts no model.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathControlSchema names the shape of the path-control file. A change
// of the shape gets a new name, so an old reader can refuse a new file.
const pathControlSchema = "apply-patch-path-controls/1"

// pathControlFile is the path-control file, relative to the package
// directory where go test runs.
var pathControlFile = filepath.Join("testdata", "codex", "apply_patch_path_control_oracle.json")

// pathControlOracle is the whole path-control file: the version and the
// date of the recording and one recorded run per case. A case has the
// fields of a content case, because the after files show which path
// the applier wrote.
type pathControlOracle struct {
	Schema        string        `json:"schema"`
	ClientVersion string        `json:"client_version"`
	Recorded      string        `json:"recorded"`
	Provenance    string        `json:"provenance"`
	Cases         []contentCase `json:"cases"`
}

// pathControlProvenance is the provenance text of the path-control file.
const pathControlProvenance = "Each case was applied offline by the installed codex binary run under the name apply_patch: no model, no login, a scratch CODEX_HOME, and a new scratch directory per case that holds the before files only. exit_status, stdout, and stderr are what the applier returned, with the scratch directory written as " + scratchPlaceholder + ". after holds every file in the scratch directory after the run, so its paths are the paths that the applier wrote. The cases show the applier, not what tool_input.command carries in a live PreToolUse."

// sentinelBefore is the before file of the update and delete cases: the
// file that a path without the control character names.
var sentinelBefore = map[string]string{"sentinel/x.txt": "a\n"}

// pathControlInputs are the shapes the path-control file records. The
// names keep a number prefix, so the file order stays the table order.
var pathControlInputs = []contentInput{
	{
		name:   "01_add_cr_inside",
		shape:  "a carriage return inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\rtinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "02_add_tab_inside",
		shape:  "a tab inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\ttinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "03_update_cr_inside",
		shape:  "a carriage return inside an Update path",
		before: sentinelBefore,
		patch:  "*** Begin Patch\n*** Update File: sen\rtinel/x.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "04_update_tab_inside",
		shape:  "a tab inside an Update path",
		before: sentinelBefore,
		patch:  "*** Begin Patch\n*** Update File: sen\ttinel/x.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "05_delete_cr_inside",
		shape:  "a carriage return inside a Delete path",
		before: sentinelBefore,
		patch:  "*** Begin Patch\n*** Delete File: sen\rtinel/x.txt\n*** End Patch",
	},
	{
		name:   "06_delete_tab_inside",
		shape:  "a tab inside a Delete path",
		before: sentinelBefore,
		patch:  "*** Begin Patch\n*** Delete File: sen\ttinel/x.txt\n*** End Patch",
	},
	{
		name:   "07_move_cr_inside",
		shape:  "a carriage return inside a Move to path",
		before: map[string]string{"a.txt": "a\n"},
		patch:  "*** Begin Patch\n*** Update File: a.txt\n*** Move to: sen\rtinel/m.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "08_move_tab_inside",
		shape:  "a tab inside a Move to path",
		before: map[string]string{"a.txt": "a\n"},
		patch:  "*** Begin Patch\n*** Update File: a.txt\n*** Move to: sen\ttinel/m.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "09_add_cr_before_slash",
		shape:  "a carriage return at the end of a directory name",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sentinel\r/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "10_add_cr_first",
		shape:  "a carriage return as the first character of a path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: \rsentinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "11_add_tab_first",
		shape:  "a tab as the first character of a path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: \tsentinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "12_add_cr_joins_lines",
		shape:  "a carriage return between a path and a + line, with no line feed",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: a.txt\r+x\n*** End Patch",
	},
	{
		name:   "13_crlf_patch_cr_inside",
		shape:  "a patch with CRLF line ends and a carriage return inside a path",
		before: map[string]string{},
		patch:  "*** Begin Patch\r\n*** Add File: sen\rtinel/x.txt\r\n+x\r\n*** End Patch",
	},
	{
		name:   "14_tab_in_second_section",
		shape:  "a clean Update, then an Update path with a tab",
		before: map[string]string{"a.txt": "a\n", "sentinel/x.txt": "s\n"},
		patch:  "*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** Update File: sen\ttinel/x.txt\n@@\n-s\n+t\n*** End Patch",
	},
	{
		name:   "15_add_trailing_tab_cr",
		shape:  "a tab and a carriage return at the end of an Add header line",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: x.txt\t\r\n+x\n*** End Patch",
	},
	{
		name:   "16_add_vertical_tab_inside",
		shape:  "a vertical tab inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\vtinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "17_add_form_feed_inside",
		shape:  "a form feed inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\ftinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "18_add_start_of_heading_inside",
		shape:  "the C0 control U+0001 inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\x01tinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "19_add_escape_inside",
		shape:  "an escape character inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\x1btinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "20_add_delete_char_inside",
		shape:  "the DEL character inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\x7ftinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "21_add_next_line_inside",
		shape:  "the C1 control NEL (U+0085) inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\u0085tinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "22_add_no_break_space_inside",
		shape:  "a no-break space inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\u00a0tinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "23_add_line_separator_inside",
		shape:  "the line separator U+2028 inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\u2028tinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "24_add_zero_width_space_inside",
		shape:  "a zero-width space inside an Add path",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\u200btinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "25_update_vertical_tab_inside",
		shape:  "a vertical tab inside an Update path, with only the plain path on disk",
		before: sentinelBefore,
		patch:  "*** Begin Patch\n*** Update File: sen\vtinel/x.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "26_add_line_feed_inside",
		shape:  "a line feed inside an Add path (refusal)",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: sen\ntinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "27_add_tab_after_colon",
		shape:  "a tab in place of the space after the Add marker (refusal)",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File:\tsentinel/x.txt\n+x\n*** End Patch",
	},
	{
		name:   "28_add_content_tab_cr",
		shape:  "a tab and a carriage return inside added content",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: c.txt\n+f\to\rx\n*** End Patch",
	},
}

// encodePathControlOracle writes the path-control file in the canonical
// form of the oracle file: two-space indent, no HTML escapes, and a
// final newline.
func encodePathControlOracle(oracle pathControlOracle) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(oracle); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// loadPathControlOracle reads the path-control file.
func loadPathControlOracle(t *testing.T) ([]byte, pathControlOracle) {
	t.Helper()

	data := codexFixture(t, "apply_patch_path_control_oracle.json")

	var oracle pathControlOracle
	require.NoError(t, json.Unmarshal(data, &oracle))

	return data, oracle
}

// changedPaths returns the sorted slash paths whose bytes differ between
// the before and the after files: added, changed, and removed files.
func changedPaths(before, after map[string]string) []string {
	var paths []string

	for path, content := range after {
		if old, ok := before[path]; !ok || old != content {
			paths = append(paths, path)
		}
	}

	for path := range before {
		if _, ok := after[path]; !ok {
			paths = append(paths, path)
		}
	}

	sort.Strings(paths)

	return paths
}

// holdsRemovedChar reports whether a path holds a character that the
// applier removes from a header path.
func holdsRemovedChar(path string) bool {
	return strings.ContainsAny(path, patchPathRemoved)
}

// requireGuardNamesChangedPaths checks the guard rule on one applier
// run. The guard decoder either refuses the patch, or every path whose
// bytes the applier changed is a path of its operations. A refusal is
// required when a decoded header path holds a tab or a carriage return.
// The rule does not depend on the client version, so the native sweep
// uses it on any installed binary.
func requireGuardNamesChangedPaths(t *testing.T, before map[string]string, patch string, after map[string]string) {
	t.Helper()

	ops, guardErr := guardPatchOps(patch)
	advisory, advisoryErr := applyPatchPaths(patch)

	if advisoryErr == nil && slices.ContainsFunc(advisory, holdsRemovedChar) {
		require.ErrorIs(t, guardErr, ErrUnsupportedGrammar, "a header path with a tab or a carriage return is refused")

		return
	}

	require.Equal(t, grammarClass(t, advisoryErr), grammarClass(t, guardErr), "the guard adds no other refusal")

	if guardErr != nil {
		return
	}

	assert.Equal(t, advisory, patchOpPaths(ops), "the guard and the advisory hook name the same paths")
	assert.Subset(t, patchOpPaths(ops), changedPaths(before, after),
		"the guard names every file that the applier changed")
}

// completionEvent wraps a patch text in a minimal Codex PostToolUse
// event of apply_patch with exit code 0.
func completionEvent(t *testing.T, patch string) []byte {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"session_id": "thr_1", "cwd": "/workspace/repo", "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "call_1",
		"tool_input": map[string]any{"command": patch}, "tool_response": map[string]any{"exit_code": 0},
	})
	require.NoError(t, err)

	return payload
}

// TestApplyPatchPathControlsKeepTheGuardRule checks the guard rule
// against every recorded path-control case, offline. The guard decoder
// refuses a header path with a tab or a carriage return, and for every
// other patch it names every file that the applier changed. The
// completion decoder gives the same result, and the advisory hook keeps
// the raw paths. The file must stay in the form the recorder writes.
func TestApplyPatchPathControlsKeepTheGuardRule(t *testing.T) {
	data, oracle := loadPathControlOracle(t)

	again, err := encodePathControlOracle(oracle)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(again), "the path-control file is not in the form the recorder writes")

	assert.Equal(t, pathControlSchema, oracle.Schema)
	assert.True(t, strings.HasPrefix(oracle.ClientVersion, "codex-cli "), "client_version names the Codex CLI")

	_, err = time.Parse(time.DateOnly, oracle.Recorded)
	require.NoError(t, err, "recorded is a date")

	recorded := map[string]contentCase{}

	for _, tc := range oracle.Cases {
		recorded[tc.Name] = tc
	}

	// An input that changed after the recording no longer matches what
	// the applier did, so the recording must hold the same text.
	for _, in := range pathControlInputs {
		tc, ok := recorded[in.name]
		if assert.True(t, ok, "the input %s has no recorded case; run make agents-oracle-record", in.name) {
			assert.Equal(t, in.patch, tc.Patch, "the input %s differs from its recording", in.name)
			assert.Equal(t, in.before, tc.Before, "the input %s differs from its recording", in.name)
		}
	}

	refused := 0

	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			requireGuardNamesChangedPaths(t, tc.Before, tc.Patch, tc.After)

			ops, guardErr := guardPatchOps(tc.Patch)

			op, err := codexGuards{}.DecodeGuard(patchEvent(t, tc.Patch))
			require.Equal(t, grammarClass(t, guardErr), grammarClass(t, err), "DecodeGuard uses the guard rule")

			c, err := codexCompletions{}.DecodeCompletion(completionEvent(t, tc.Patch))
			require.Equal(t, grammarClass(t, guardErr), grammarClass(t, err), "the post-edit check uses the guard rule")

			if guardErr == nil {
				assert.Equal(t, ops, op.Ops)
				assert.Equal(t, patchOpPaths(ops), c.Paths)

				return
			}

			assert.Empty(t, op.Ops)
			assert.Empty(t, c.Paths)

			advisory, err := applyPatchPaths(tc.Patch)
			if err == nil && slices.ContainsFunc(advisory, holdsRemovedChar) {
				refused++

				// The applier wrote the path without the character, so a
				// pin of that path would have seen nothing.
				assert.NotSubset(t, advisory, changedPaths(tc.Before, tc.After),
					"the case shows a written path that the text does not name")
			}
		})
	}

	assert.GreaterOrEqual(t, refused, 12, "the file holds the refused tab and carriage return cases")
}

// pathControlSweepRunes lists the characters of the native sweep: every
// C0 control but the line feed, DEL, every C1 control, the Unicode
// space characters, the zero-width space, and the byte order mark. The
// line feed ends a patch line, so it cannot stand in a path.
func pathControlSweepRunes() []rune {
	var runes []rune

	for r := rune(0x01); r <= 0x9f; r++ {
		if r == '\n' || (r >= 0x20 && r < 0x7f) {
			continue
		}

		runes = append(runes, r)
	}

	runes = append(runes, 0xa0, 0x1680)

	for r := rune(0x2000); r <= 0x200b; r++ {
		runes = append(runes, r)
	}

	return append(runes, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff)
}

// pathControlSweep returns the generated cases of the native sweep: each
// character of pathControlSweepRunes inside an Add, Update, Delete, and
// Move to path, and as the first character of an Add path.
func pathControlSweep() []contentInput {
	var inputs []contentInput

	for _, r := range pathControlSweepRunes() {
		c := string(r)
		tag := fmt.Sprintf("%04x", r)

		inputs = append(inputs,
			contentInput{name: "add_inside_" + tag, before: map[string]string{},
				patch: "*** Begin Patch\n*** Add File: sen" + c + "tinel/x.txt\n+x\n*** End Patch"},
			contentInput{name: "add_first_" + tag, before: map[string]string{},
				patch: "*** Begin Patch\n*** Add File: " + c + "x.txt\n+x\n*** End Patch"},
			contentInput{name: "update_inside_" + tag, before: sentinelBefore,
				patch: "*** Begin Patch\n*** Update File: sen" + c + "tinel/x.txt\n@@\n-a\n+b\n*** End Patch"},
			contentInput{name: "delete_inside_" + tag, before: sentinelBefore,
				patch: "*** Begin Patch\n*** Delete File: sen" + c + "tinel/x.txt\n*** End Patch"},
			contentInput{name: "move_inside_" + tag, before: map[string]string{"a.txt": "a\n"},
				patch: "*** Begin Patch\n*** Update File: a.txt\n*** Move to: sen" + c + "tinel/m.txt\n@@\n-a\n+b\n*** End Patch"},
		)
	}

	return inputs
}

// TestNativeCodexPathControlsHoldOnTheInstalledBinary runs the
// path-control cases through the installed binary. The recorded cases
// replay byte for byte only on the recorded version; another version
// skips that part with the reason. The guard rule holds on every
// version: for each recorded case and each generated sweep case, the
// guard decoder refuses the patch or names every file that the applier
// changed. A later client that removes another character fails here.
func TestNativeCodexPathControlsHoldOnTheInstalledBinary(t *testing.T) {
	codex := nativeCodex(t)
	scratchCodexHome(t)

	_, oracle := loadPathControlOracle(t)
	require.NotEmpty(t, oracle.Cases)

	installed := codexVersion(t, codex)
	bin := applierBinary(t, codex)

	t.Run("recorded cases", func(t *testing.T) {
		if installed != oracle.ClientVersion {
			t.Skipf("the path-control cases pin %s and the installed client is %s; record again with make agents-oracle-record", oracle.ClientVersion, installed)
		}

		for _, tc := range oracle.Cases {
			t.Run(tc.Name, func(t *testing.T) {
				got := runApplier(t, bin, tc.Before, tc.Patch)

				assert.Equal(t, tc.ExitStatus, got.ExitStatus, "exit status differs; stderr: %s", got.Stderr)
				assert.Equal(t, tc.Stdout, got.Stdout, "the report differs")
				assert.Equal(t, tc.Stderr, got.Stderr, "the error text differs")
				assert.Equal(t, tc.After, got.After, "the files after the run differ")
			})
		}
	})

	t.Run("guard rule", func(t *testing.T) {
		inputs := pathControlSweep()

		for _, tc := range oracle.Cases {
			inputs = append(inputs, contentInput{name: tc.Name, before: tc.Before, patch: tc.Patch})
		}

		for _, in := range inputs {
			t.Run(in.name, func(t *testing.T) {
				got := runApplier(t, bin, in.before, in.patch)
				requireGuardNamesChangedPaths(t, in.before, in.patch, got.After)
			})
		}

		t.Logf("checked the guard rule on %d cases with %s", len(inputs), installed)
	})
}

// TestRecordCodexPathControlOracle records the path-control file from
// the installed applier. It runs only when SEAMARK_ORACLE_RECORD names
// the installed version.
func TestRecordCodexPathControlOracle(t *testing.T) {
	expected := os.Getenv(oracleRecordEnv)
	if expected == "" {
		t.Skipf("the recorder runs with %s=<codex version> (make agents-oracle-record ORACLE_VERSION=<version>)", oracleRecordEnv)
	}

	codex := nativeCodex(t)
	scratchCodexHome(t)

	installed := codexVersion(t, codex)
	require.Equal(t, "codex-cli "+expected, installed, "the installed version is not the version the operator named; nothing is written")

	bin := applierBinary(t, codex)

	oracle := pathControlOracle{
		Schema:        pathControlSchema,
		ClientVersion: installed,
		Recorded:      time.Now().UTC().Format(time.DateOnly),
		Provenance:    pathControlProvenance,
	}

	for _, in := range pathControlInputs {
		got := runApplier(t, bin, in.before, in.patch)
		got.Name = in.name
		got.Shape = in.shape
		got.Before = in.before
		got.Patch = in.patch

		// A second run must give the same result, or the case does not
		// pin one behavior.
		again := runApplier(t, bin, in.before, in.patch)
		require.Equal(t, got.ExitStatus, again.ExitStatus, "case %s is not deterministic", in.name)
		require.Equal(t, got.After, again.After, "case %s is not deterministic", in.name)

		oracle.Cases = append(oracle.Cases, got)
	}

	data, err := encodePathControlOracle(oracle)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pathControlFile, data, 0o644))

	t.Logf("wrote %d path-control cases for %s to %s", len(oracle.Cases), installed, pathControlFile)
}
