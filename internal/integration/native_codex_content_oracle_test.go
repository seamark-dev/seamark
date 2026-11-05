package integration

// The content oracle of apply_patch. Each content case holds the files
// before a patch, the patch text, and what the installed Codex applier
// did with them: its exit status, its report, and every file after the
// run. The path cases of the same file record only acceptance and the
// touched paths. A later Update projection needs the exact bytes, and
// it must not guess them, so every byte here comes from the applier.
//
// Three tests use the content part:
//
//   - TestApplyPatchOracleKeepsItsCanonicalForm runs offline in every
//     test run. It checks that the recorder writes the file back byte
//     for byte, so a new recording keeps the bytes of the path cases.
//   - TestNativeCodexContentOracleHoldsOnTheInstalledBinary replays
//     every content case through the installed binary in
//     `make agents-native-check`.
//   - TestRecordCodexContentOracle writes the content part.
//     It runs only through `make agents-oracle-record`, which names the
//     expected Codex version, so a different installed version never
//     rewrites the file by itself.
//
// The applier runs in a scratch directory with a scratch CODEX_HOME. It
// is the codex binary under the name apply_patch, so it reads no login
// and starts no model.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oracleRecordEnv names the Codex version that the recorder expects,
// for example "0.164.0". The recorder runs only when it matches the
// installed version, so the operator states the version on purpose.
const oracleRecordEnv = "SEAMARK_ORACLE_RECORD"

// contentOracleSchema names the shape of the content part. A change of
// the shape gets a new name, so an old reader can refuse a new file.
const contentOracleSchema = "apply-patch-content/1"

// scratchPlaceholder replaces the scratch directory in the applier
// output. The applier names absolute paths in its errors, and the
// scratch path differs on every run.
const scratchPlaceholder = "<scratch>"

// applyPatchOracleFile is the oracle file of both parts, relative to the
// package directory where go test runs.
var applyPatchOracleFile = filepath.Join("testdata", "codex", "apply_patch_oracle.json")

// applyPatchOracle is the whole oracle file. The path part stays raw
// JSON, so a new recording of the content part keeps its bytes.
type applyPatchOracle struct {
	Provenance json.RawMessage `json:"provenance"`
	Cases      json.RawMessage `json:"cases"`
	Content    *contentOracle  `json:"content,omitempty"`
}

// contentOracle is the content part: the version and date of the
// recording and one entry per case.
type contentOracle struct {
	Schema        string        `json:"schema"`
	ClientVersion string        `json:"client_version"`
	Recorded      string        `json:"recorded"`
	Provenance    string        `json:"provenance"`
	Cases         []contentCase `json:"cases"`
}

// contentCase is one recorded run of the applier. Before and After map
// a slash path below the scratch directory to the full file content. A
// path that is absent from After does not exist after the run. Every
// file is UTF-8 text, so a JSON string holds its exact bytes.
type contentCase struct {
	Name       string            `json:"name"`
	Shape      string            `json:"shape"`
	Before     map[string]string `json:"before"`
	Patch      string            `json:"patch"`
	ExitStatus int               `json:"exit_status"`
	Stdout     string            `json:"stdout"`
	Stderr     string            `json:"stderr"`
	After      map[string]string `json:"after"`
}

// contentInput is one case before the recording: the files and the
// patch. The recorder adds what the applier did.
type contentInput struct {
	name   string
	shape  string
	before map[string]string
	patch  string
}

// contentOracleProvenance is the provenance text of the content part.
const contentOracleProvenance = "Each case was applied offline by the installed codex binary run under the name apply_patch: no model, no login, a scratch CODEX_HOME, and a new scratch directory per case that holds the before files only. exit_status, stdout, and stderr are what the applier returned, with the scratch directory written as " + scratchPlaceholder + ". after holds every file in the scratch directory after the run, also after a refusal. The cases show the applier, not what tool_input.command carries in a live PreToolUse."

// tenLines is a file long enough for two hunks with separate context.
const tenLines = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"

// contentInputs are the shapes the content oracle records. The names
// keep a number prefix, so the file order stays the table order.
var contentInputs = []contentInput{
	{
		name:   "01_one_hunk",
		shape:  "one hunk with context",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n c\n*** End Patch",
	},
	{
		name:   "02_many_hunks",
		shape:  "two hunks in one file",
		before: map[string]string{"f.txt": tenLines},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n l1\n-l2\n+L2\n l3\n@@\n l8\n-l9\n+L9\n l10\n*** End Patch",
	},
	{
		name:   "03_end_of_file",
		shape:  "*** End of File on the last line",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n-c\n+C\n*** End of File\n*** End Patch",
	},
	{
		name:   "04_end_of_file_repeated_tail",
		shape:  "*** End of File where the context also matches before the end",
		before: map[string]string{"f.txt": "x\ny\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n x\n-y\n+Y\n*** End of File\n*** End Patch",
	},
	{
		name:   "05_no_final_newline_last_line",
		shape:  "before file without a final newline, the last line changes",
		before: map[string]string{"f.txt": "a\nb\nc"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "06_no_final_newline_middle_line",
		shape:  "before file without a final newline, a middle line changes",
		before: map[string]string{"f.txt": "a\nb\nc"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n c\n*** End Patch",
	},
	{
		name:   "07_crlf_file_lf_patch",
		shape:  "file with CRLF line ends, patch with LF line ends",
		before: map[string]string{"f.txt": "a\r\nb\r\nc\r\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n c\n*** End Patch",
	},
	{
		name:   "08_crlf_file_crlf_patch",
		shape:  "file with CRLF line ends, patch with CRLF line ends",
		before: map[string]string{"f.txt": "a\r\nb\r\nc\r\n"},
		patch:  "*** Begin Patch\r\n*** Update File: f.txt\r\n@@\r\n a\r\n-b\r\n+B\r\n c\r\n*** End Patch",
	},
	{
		name:   "09_repeated_context_first",
		shape:  "repeated context, no anchor",
		before: map[string]string{"f.txt": "x\ny\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n x\n-y\n+Y\n*** End Patch",
	},
	{
		name:   "10_repeated_context_anchor",
		shape:  "repeated context, @@ anchor line selects the second match",
		before: map[string]string{"f.txt": "func a\nx\ny\nfunc b\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ func b\n x\n-y\n+Y\n*** End Patch",
	},
	{
		name:   "11_repeated_context_two_hunks",
		shape:  "repeated context, two equal hunks in one file",
		before: map[string]string{"f.txt": "x\ny\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n x\n-y\n+Y\n@@\n x\n-y\n+Z\n*** End Patch",
	},
	{
		name:   "12_move_with_hunk",
		shape:  "*** Move to with a hunk, into a new directory",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n*** Move to: sub/g.txt\n@@\n a\n-b\n+B\n c\n*** End Patch",
	},
	{
		name:   "13_move_onto_existing",
		shape:  "*** Move to an existing file",
		before: map[string]string{"f.txt": "a\nb\n", "g.txt": "old\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n*** Move to: g.txt\n@@\n-b\n+B\n*** End Patch",
	},
	{
		name:   "14_header_trailing_spaces",
		shape:  "trailing spaces after the header path and after @@",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt  \n@@  \n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "15_header_leading_spaces",
		shape:  "leading spaces before the first header",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n  *** Update File: f.txt\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "16_context_mismatch",
		shape:  "context that does not match (refusal)",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n q\n-b\n+B\n*** End Patch",
	},
	{
		name:   "17_mismatch_after_good_file",
		shape:  "a good update, then a file whose context does not match",
		before: map[string]string{"f.txt": "a\nb\n", "g.txt": "c\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+A\n*** Update File: g.txt\n@@\n-zz\n+d\n*** End Patch",
	},
	{
		name:   "18_context_trailing_whitespace",
		shape:  "file line with trailing spaces, patch context without them",
		before: map[string]string{"f.txt": "a  \nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "19_context_unicode_dash",
		shape:  "file line with an en dash, patch line with an ASCII hyphen",
		before: map[string]string{"f.txt": "a – b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a - b\n+a - B\n c\n*** End Patch",
	},
	{
		name:   "20_pure_insertion",
		shape:  "hunk with added lines only",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End Patch",
	},
	{
		name:   "21_update_absent_file",
		shape:  "update of a file that does not exist (refusal)",
		before: map[string]string{"f.txt": "a\n"},
		patch:  "*** Begin Patch\n*** Update File: nope.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "22_add_two_lines",
		shape:  "Add with two lines",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+x\n+y\n*** End Patch",
	},
	{
		name:   "23_add_last_line_empty",
		shape:  "Add whose last line is an empty + line",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+x\n+\n*** End Patch",
	},
	{
		name:   "24_add_patch_final_newline",
		shape:  "Add where the patch text ends with a newline",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+x\n*** End Patch\n",
	},
	{
		name:   "25_add_empty",
		shape:  "Add without lines",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n*** End Patch",
	},
	{
		name:   "26_add_over_existing",
		shape:  "Add of a path that exists",
		before: map[string]string{"n.txt": "old\n"},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+new\n*** End Patch",
	},
	{
		name:   "27_add_crlf_patch",
		shape:  "Add with CRLF line ends in the patch",
		before: map[string]string{},
		patch:  "*** Begin Patch\r\n*** Add File: n.txt\r\n+x\r\n+y\r\n*** End Patch",
	},
	{
		name:   "28_delete",
		shape:  "Delete",
		before: map[string]string{"f.txt": "a\n", "g.txt": "b\n"},
		patch:  "*** Begin Patch\n*** Delete File: f.txt\n*** End Patch",
	},
	{
		name:   "29_delete_absent",
		shape:  "Delete of a file that does not exist (refusal)",
		before: map[string]string{"f.txt": "a\n"},
		patch:  "*** Begin Patch\n*** Delete File: nope.txt\n*** End Patch",
	},
}

// encodeApplyPatchOracle writes the oracle file in its canonical form:
// two-space indent, no HTML escapes, and a final newline. The path part
// then keeps its bytes, because it was written in the same form.
func encodeApplyPatchOracle(oracle applyPatchOracle) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(oracle); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// loadApplyPatchOracle reads the oracle file of both parts.
func loadApplyPatchOracle(t *testing.T) ([]byte, applyPatchOracle) {
	t.Helper()

	data := codexFixture(t, "apply_patch_oracle.json")

	var oracle applyPatchOracle
	require.NoError(t, json.Unmarshal(data, &oracle))

	return data, oracle
}

// writeScratchFiles writes the before files of one case into dir.
func writeScratchFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

// readScratchFiles reads every regular file below dir, by slash path. A
// file that is not UTF-8 fails the test, because a JSON string cannot
// hold its bytes exactly.
func readScratchFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	files := map[string]string{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		require.True(t, utf8.Valid(data), "%s is not UTF-8 text; a JSON string cannot hold its bytes", rel)
		files[filepath.ToSlash(rel)] = string(data)

		return nil
	})
	require.NoError(t, err)

	return files
}

// runApplier applies one patch in a new scratch directory and returns
// what the applier did. The patch goes in as the only argument, as the
// path cases do.
func runApplier(t *testing.T, bin string, before map[string]string, patch string) contentCase {
	t.Helper()

	dir := t.TempDir()
	writeScratchFiles(t, dir, before)

	cmd := exec.Command(bin, patch)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	status := 0

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "blocked: the applier did not run: %v", err)

		status = exit.ExitCode()
	}

	return contentCase{
		ExitStatus: status,
		Stdout:     scrubScratch(t, stdout.String(), dir),
		Stderr:     scrubScratch(t, stderr.String(), dir),
		After:      readScratchFiles(t, dir),
	}
}

// scrubScratch writes the scratch directory as a placeholder in the
// applier output. On macOS the temporary directory has two spellings
// through a symbolic link, so both are replaced.
func scrubScratch(t *testing.T, out, dir string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	out = strings.ReplaceAll(out, resolved, scratchPlaceholder)

	return strings.ReplaceAll(out, dir, scratchPlaceholder)
}

// applierBinary links the codex binary under the name apply_patch in a
// scratch directory. Codex applies a patch when it runs under that name.
func applierBinary(t *testing.T, codex string) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "apply_patch")
	require.NoError(t, os.Symlink(codex, bin))

	return bin
}

// TestApplyPatchOracleKeepsItsCanonicalForm checks that the recorder
// writes the oracle file back byte for byte. A new recording replaces
// only the content part, so the path cases keep their bytes and their
// meaning. It also checks the content part for the fields a replay
// needs.
func TestApplyPatchOracleKeepsItsCanonicalForm(t *testing.T) {
	data, oracle := loadApplyPatchOracle(t)

	again, err := encodeApplyPatchOracle(oracle)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(again), "the oracle file is not in the form the recorder writes")

	require.NotNil(t, oracle.Content, "the content part is missing")
	assert.Equal(t, contentOracleSchema, oracle.Content.Schema)
	assert.True(t, strings.HasPrefix(oracle.Content.ClientVersion, "codex-cli "), "client_version names the Codex CLI")

	_, err = time.Parse(time.DateOnly, oracle.Content.Recorded)
	require.NoError(t, err, "recorded is a date")

	names := map[string]bool{}

	for _, tc := range oracle.Content.Cases {
		assert.False(t, names[tc.Name], "case %s appears twice", tc.Name)
		names[tc.Name] = true

		assert.NotEmpty(t, tc.Shape, "case %s names its shape", tc.Name)
		assert.NotNil(t, tc.Before, "case %s holds its before files", tc.Name)
		assert.NotNil(t, tc.After, "case %s holds its after files", tc.Name)
	}

	for _, in := range contentInputs {
		assert.True(t, names[in.name], "the input %s has no recorded case; run make agents-oracle-record", in.name)
	}
}

// TestNativeCodexContentOracleHoldsOnTheInstalledBinary replays every
// content case through the installed binary. The cases pin one Codex
// version, so a different installed version skips with the reason. A
// new version needs a new recording, never a silent rewrite.
func TestNativeCodexContentOracleHoldsOnTheInstalledBinary(t *testing.T) {
	codex := nativeCodex(t)
	scratchCodexHome(t)

	_, oracle := loadApplyPatchOracle(t)
	require.NotNil(t, oracle.Content, "the content part is missing")
	require.NotEmpty(t, oracle.Content.Cases)

	installed := codexVersion(t, codex)
	if installed != oracle.Content.ClientVersion {
		t.Skipf("the content cases pin %s and the installed client is %s; record again with make agents-oracle-record", oracle.Content.ClientVersion, installed)
	}

	bin := applierBinary(t, codex)

	for _, tc := range oracle.Content.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := runApplier(t, bin, tc.Before, tc.Patch)

			assert.Equal(t, tc.ExitStatus, got.ExitStatus, "exit status differs; stderr: %s", got.Stderr)
			assert.Equal(t, tc.Stdout, got.Stdout, "the report differs")
			assert.Equal(t, tc.Stderr, got.Stderr, "the error text differs")
			assert.Equal(t, tc.After, got.After, "the files after the run differ")
		})
	}
}

// TestRecordCodexContentOracle records the content part from
// the installed applier and writes it into the oracle file. It runs
// only when SEAMARK_ORACLE_RECORD names the installed version, and it
// keeps the path part as raw bytes.
func TestRecordCodexContentOracle(t *testing.T) {
	expected := os.Getenv(oracleRecordEnv)
	if expected == "" {
		t.Skipf("the recorder runs with %s=<codex version> (make agents-oracle-record ORACLE_VERSION=<version>)", oracleRecordEnv)
	}

	codex := nativeCodex(t)
	scratchCodexHome(t)

	installed := codexVersion(t, codex)
	require.Equal(t, "codex-cli "+expected, installed, "the installed version is not the version the operator named; nothing is written")

	_, oracle := loadApplyPatchOracle(t)
	bin := applierBinary(t, codex)

	content := &contentOracle{
		Schema:        contentOracleSchema,
		ClientVersion: installed,
		Recorded:      time.Now().UTC().Format(time.DateOnly),
		Provenance:    contentOracleProvenance,
	}

	for _, in := range contentInputs {
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

		content.Cases = append(content.Cases, got)
	}

	oracle.Content = content

	data, err := encodeApplyPatchOracle(oracle)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(applyPatchOracleFile, data, 0o644))

	t.Logf("wrote %d content cases for %s to %s", len(content.Cases), installed, applyPatchOracleFile)
}
