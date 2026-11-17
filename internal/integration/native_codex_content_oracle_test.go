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
//
// Cases 30 and later pin the rules of the Update projection in
// lessoncheck: the bytes of context lines, the line end of new lines,
// the final newline, pure insertions, the order of the match passes,
// anchors, the end-of-file marker, the retry without a trailing empty
// line, the chunk order, line separators, the normalized characters,
// and change lines that end with carriage returns. Each mapped
// character of a class stands in one case, so a character that the
// applier stops mapping fails that case.
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
	{
		name:   "30_context_keeps_trailing_spaces",
		shape:  "context that matches only without trailing spaces keeps the file line",
		before: map[string]string{"f.txt": "a\nb  \nc\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n b\n-c\n+C\n d\n*** End Patch",
	},
	{
		name:   "31_context_keeps_leading_spaces",
		shape:  "context that matches only without leading spaces keeps the file line",
		before: map[string]string{"f.txt": "  a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "32_context_keeps_tab",
		shape:  "context with spaces where the file line has a tab",
		before: map[string]string{"f.txt": "\ta\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n     a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "33_fuzzy_delete_then_equal_add",
		shape:  "a deleted line that matches only without trailing spaces, then the same text added",
		before: map[string]string{"f.txt": "a  \n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+a\n*** End Patch",
	},
	{
		name:   "34_fuzzy_delete_then_two_adds",
		shape:  "a deleted line that matches only without trailing spaces, then two added lines",
		before: map[string]string{"f.txt": "a  \n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+b\n+a\n*** End Patch",
	},
	{
		name:   "35_fuzzy_context_then_equal_add",
		shape:  "fuzzy context followed by an added line with the context text",
		before: map[string]string{"f.txt": "a  \nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n+a\n b\n*** End Patch",
	},
	{
		name:   "36_eol_first_lf_rest_crlf",
		shape:  "first line ends with LF, the others with CRLF",
		before: map[string]string{"f.txt": "a\nb\r\nc\r\nd\r\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n-c\n+C\n d\n*** End Patch",
	},
	{
		name:   "37_eol_first_crlf_rest_lf",
		shape:  "first line ends with CRLF, the others with LF",
		before: map[string]string{"f.txt": "a\r\nb\nc\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n-c\n+C\n d\n*** End Patch",
	},
	{
		name:   "38_eol_crlf_file_lf_last_line",
		shape:  "CRLF file whose changed last line ends with LF",
		before: map[string]string{"f.txt": "a\r\nb\r\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n-c\n+C\n+D\n*** End Patch",
	},
	{
		name:   "39_eol_insertion_first_line_crlf",
		shape:  "insertion into a file where only the first line ends with CRLF",
		before: map[string]string{"f.txt": "a\r\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End Patch",
	},
	{
		name:   "40_single_line_without_final_newline",
		shape:  "file of one line without a final newline",
		before: map[string]string{"f.txt": "a"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+A\n+B\n*** End Patch",
	},
	{
		name:   "41_crlf_without_final_newline_tail_kept",
		shape:  "CRLF file without a final newline, the last line stays",
		before: map[string]string{"f.txt": "a\r\nb\r\nc"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+A\n b\n*** End Patch",
	},
	{
		name:   "42_add_after_last_line_without_final_newline",
		shape:  "added line after a last line without a final newline",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n b\n+X\n*** End Patch",
	},
	{
		name:   "43_mixed_eol_without_final_newline",
		shape:  "mixed line ends and no final newline, the first line changes",
		before: map[string]string{"f.txt": "a\nb\r\nc"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+A\n*** End Patch",
	},
	{
		name:   "44_insertion_anchor_goes_to_end",
		shape:  "insertion with an @@ anchor in the middle of the file",
		before: map[string]string{"f.txt": "func a\nx\nfunc b\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ func a\n+z\n*** End Patch",
	},
	{
		name:   "45_insertion_after_trailing_blank_line",
		shape:  "insertion into a file that ends with an empty line",
		before: map[string]string{"f.txt": "a\nb\n\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End Patch",
	},
	{
		name:   "46_insertion_into_empty_file",
		shape:  "insertion into an empty file",
		before: map[string]string{"f.txt": ""},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End Patch",
	},
	{
		name:   "47_insertion_with_end_of_file",
		shape:  "insertion followed by *** End of File",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End of File\n*** End Patch",
	},
	{
		name:   "48_insertion_anchor_missing",
		shape:  "insertion whose @@ anchor does not exist (refusal)",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ zzz\n+z\n*** End Patch",
	},
	{
		name:   "49_insertion_without_final_newline",
		shape:  "insertion into a file without a final newline",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+z\n*** End Patch",
	},
	{
		name:   "50_exact_match_before_fuzzy_match",
		shape:  "an exact match later in the file wins over an earlier fuzzy match",
		before: map[string]string{"f.txt": "a \nb\na\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "51_anchor_equal_to_context",
		shape:  "@@ anchor with the text of the first context line",
		before: map[string]string{"f.txt": "x\ny\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ x\n x\n-y\n+Y\n*** End Patch",
	},
	{
		name:   "52_anchor_leading_spaces",
		shape:  "@@ anchor with leading spaces",
		before: map[string]string{"f.txt": "def f\nx\n  def f\nx\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@   def f\n-x\n+X\n*** End Patch",
	},
	{
		name:   "53_anchor_trailing_spaces",
		shape:  "@@ anchor with trailing spaces",
		before: map[string]string{"f.txt": "func b  \nx\nfunc b\nx\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ func b  \n-x\n+X\n*** End Patch",
	},
	{
		name:   "54_end_of_file_context_not_at_end",
		shape:  "*** End of File on lines that are not the last (refusal)",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End of File\n*** End Patch",
	},
	{
		name:   "55_end_of_file_before_trailing_blank_line",
		shape:  "*** End of File when the file ends with an empty line (refusal)",
		before: map[string]string{"f.txt": "a\nb\n\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\n-b\n+B\n*** End of File\n*** End Patch",
	},
	{
		name:   "56_end_of_file_overlapping_chunk",
		shape:  "*** End of File chunk on a line that an earlier chunk used (refusal)",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n@@\n-b\n+C\n*** End of File\n*** End Patch",
	},
	{
		name:   "57_end_of_file_second_equal_chunk",
		shape:  "two equal chunks, the second with *** End of File",
		before: map[string]string{"f.txt": "x\ny\nx\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n x\n-y\n+Y\n@@\n x\n-y\n+Z\n*** End of File\n*** End Patch",
	},
	{
		name:   "58_end_of_file_with_trailing_empty_context",
		shape:  "*** End of File after a trailing empty context line",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n \n*** End of File\n*** End Patch",
	},
	{
		name:   "59_retry_empty_delete_and_empty_add",
		shape:  "chunk that ends with an empty deleted and an empty added line",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\n-b\n+B\n-\n+\n*** End Patch",
	},
	{
		name:   "60_retry_context_then_add",
		shape:  "chunk that ends with an added line and an empty context line",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n b\n+X\n \n*** End Patch",
	},
	{
		name:   "61_retry_empty_delete",
		shape:  "chunk that ends with an empty deleted line",
		before: map[string]string{"f.txt": "a\nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\n-b\n+B\n-\n*** End Patch",
	},
	{
		name:   "62_retry_empty_file_context",
		shape:  "empty context line on an empty file",
		before: map[string]string{"f.txt": ""},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n \n+z\n*** End Patch",
	},
	{
		name:   "63_retry_keeps_fuzzy_context",
		shape:  "trailing empty context line after fuzzy context",
		before: map[string]string{"f.txt": "a  \nb"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n \n*** End Patch",
	},
	{
		name:   "64_chunks_out_of_order",
		shape:  "second chunk above the first (refusal)",
		before: map[string]string{"f.txt": "l1\nl2\nl3\nl4\nl5\nl6\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n l5\n-l6\n+L6\n@@\n l1\n-l2\n+L2\n*** End Patch",
	},
	{
		name:   "65_chunks_overlap",
		shape:  "second chunk starts on the last context line of the first (refusal)",
		before: map[string]string{"f.txt": "a\nb\nc\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n c\n@@\n c\n-d\n+D\n*** End Patch",
	},
	{
		name:   "66_insertion_then_replace_last_line",
		shape:  "insertion chunk, then a chunk that replaces the last line",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+Z\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "67_insertion_then_add_at_end",
		shape:  "insertion chunk, then a chunk that adds after the last line",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+Z\n@@\n a\n b\n+C\n*** End Patch",
	},
	{
		name:   "68_insertion_then_runs_around_context",
		shape:  "insertion chunk, then a chunk with added lines before and after context",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n+Z\n@@\n-a\n+A\n b\n c\n+C\n*** End Patch",
	},
	{
		name:   "69_insertions_at_one_line_keep_chunk_order",
		shape:  "two chunks that insert at the same line",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n+X\n@@\n+Y\n b\n*** End Patch",
	},
	{
		name:   "70_second_anchor_line",
		shape:  "two @@ anchor lines in a row (refusal)",
		before: map[string]string{"f.txt": "class A\n def f\n  x\nclass B\n def f\n  x\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ class B\n@@  def f\n-  x\n+  y\n*** End Patch",
	},
	{
		name:   "71_empty_chunk",
		shape:  "@@ line without change lines (refusal)",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n*** End Patch",
	},
	{
		name:   "72_lines_after_end_of_file",
		shape:  "a change line after *** End of File (refusal)",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End of File\n+c\n*** End Patch",
	},
	{
		name:   "73_context_only_chunk",
		shape:  "chunk with context lines only",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n b\n*** End Patch",
	},
	{
		name:   "74_delete_every_line",
		shape:  "chunk that deletes every line",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n-b\n*** End Patch",
	},
	{
		name:   "75_byte_order_mark",
		shape:  "file that starts with a byte order mark (refusal)",
		before: map[string]string{"f.txt": "\ufeffa\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n*** End Patch",
	},
	{
		name:   "76_lone_cr_line_ends",
		shape:  "file whose lines end with a lone CR",
		before: map[string]string{"f.txt": "a\rb\rc\r"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-b\n+B\n*** End Patch",
	},
	{
		name:   "77_lone_cr_inside_line",
		shape:  "file with a lone CR inside a line",
		before: map[string]string{"f.txt": "a\nb\rc\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\n c\n*** End Patch",
	},
	{
		name:   "78_same_file_twice",
		shape:  "two update sections of one file",
		before: map[string]string{"f.txt": "a\nb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n-a\n+A\n*** Update File: f.txt\n@@\n-c\n+C\n*** End Patch",
	},
	{
		name:   "79_add_then_update_same_file",
		shape:  "add section, then an update section of the same file",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+a\n*** Update File: n.txt\n@@\n-a\n+b\n*** End Patch",
	},
	{
		name:   "80_move_onto_existing_crlf_file",
		shape:  "*** Move to an existing file, CRLF source",
		before: map[string]string{"f.txt": "a\r\nb\r\n", "g.txt": "x\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n*** Move to: g.txt\n@@\n-b\n+B\n*** End Patch",
	},
	{
		name:   "81_added_line_with_lone_cr",
		shape:  "added line that holds a lone CR",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\n-b\n+B\rC\n*** End Patch",
	},
	{
		name:   "82_line_separators_inside_line",
		shape:  "file line with U+2028, U+0085, a vertical tab and a form feed inside",
		before: map[string]string{"f.txt": "a\u2028b\u0085c\x0bd\x0ce\nf\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\u2028b\u0085c\x0bd\x0ce\n-f\n+F\n*** End Patch",
	},
	{
		name:   "83_context_unicode_trailing_whitespace",
		shape:  "context that matches only without trailing Unicode whitespace",
		before: map[string]string{"f.txt": "a\u00a0\nb\u1680\nc\u3000\nd\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n b\n c\n-d\n+D\n*** End Patch",
	},
	{
		name:   "84_normalize_dashes",
		shape:  "context with an ASCII hyphen for each Unicode dash and the minus sign",
		before: map[string]string{"f.txt": "a\u2010b\na\u2011b\na\u2012b\na\u2013b\na\u2014b\na\u2015b\na\u2212b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a-b\n a-b\n a-b\n a-b\n a-b\n a-b\n a-b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "85_normalize_single_quotes",
		shape:  "context with an apostrophe for each Unicode single quote",
		before: map[string]string{"f.txt": "a\u2018b\na\u2019b\na\u201ab\na\u201bb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a'b\n a'b\n a'b\n a'b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "86_normalize_double_quotes",
		shape:  "context with a quotation mark for each Unicode double quote",
		before: map[string]string{"f.txt": "a\u201cb\na\u201db\na\u201eb\na\u201fb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\"b\n a\"b\n a\"b\n a\"b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "87_normalize_spaces",
		shape:  "context with a space for each mapped Unicode space",
		before: map[string]string{"f.txt": "a\u00a0b\na\u2002b\na\u2003b\na\u2004b\na\u2005b\na\u2006b\na\u2007b\na\u2008b\na\u2009b\na\u200ab\na\u202fb\na\u205fb\na\u3000b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n a b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "88_normalize_patch_side",
		shape:  "context with Unicode punctuation where the file has ASCII",
		before: map[string]string{"f.txt": "a-b\n\"a\"\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n a\u2013b\n \u201ca\u201d\n-c\n+C\n*** End Patch",
	},
	{
		name:   "89_unmapped_dash",
		shape:  "context with an ASCII hyphen for a fullwidth hyphen-minus (refusal)",
		before: map[string]string{"f.txt": "a\uff0db\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a-b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "90_unmapped_single_quote",
		shape:  "context with an apostrophe for a modifier letter apostrophe (refusal)",
		before: map[string]string{"f.txt": "a\u02bcb\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a'b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "91_unmapped_double_quote",
		shape:  "context with a quotation mark for a fullwidth quotation mark (refusal)",
		before: map[string]string{"f.txt": "a\uff02b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\"b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "92_unmapped_space",
		shape:  "context with a space for an en quad (refusal)",
		before: map[string]string{"f.txt": "a\u2000b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "93_trailing_unicode_space_before_leading_space",
		shape:  "a match without trailing Unicode whitespace wins over an earlier match without leading spaces",
		before: map[string]string{"f.txt": " x\ny\nx\u00a0\ny\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n x\n-y\n+Y\n*** End Patch",
	},
	{
		name:   "94_trimmed_match_before_normalized_match",
		shape:  "a match without leading spaces wins over an earlier match after normalization",
		before: map[string]string{"f.txt": "a\u2013b\nc\n  a-b\nc\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a-b\n-c\n+C\n*** End Patch",
	},
	{
		name:   "95_anchor_matches_after_normalization",
		shape:  "@@ anchor with ASCII quotes where the file has Unicode quotes",
		before: map[string]string{"f.txt": "func \u201cb\u201d\nx\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@ func \"b\"\n-x\n+X\n*** End Patch",
	},
	{
		name:   "96_change_lines_end_with_carriage_returns",
		shape:  "context and added lines that end with two carriage returns before the line feed",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\r\r\n-b\n+B\r\r\n*** End Patch",
	},
	{
		name:   "97_add_lines_end_with_two_carriage_returns",
		shape:  "Add whose first line ends with two carriage returns before the line feed",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+a\r\r\n+b\n*** End Patch",
	},
	{
		name:   "98_add_line_ends_with_three_carriage_returns",
		shape:  "Add whose first line ends with three carriage returns before the line feed",
		before: map[string]string{},
		patch:  "*** Begin Patch\n*** Add File: n.txt\n+a\r\r\r\n+b\n*** End Patch",
	},
	{
		name:   "99_change_line_ends_with_three_carriage_returns",
		shape:  "added line of an Update that ends with three carriage returns before the line feed",
		before: map[string]string{"f.txt": "a\nb\n"},
		patch:  "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n-b\n+B\r\r\r\n*** End Patch",
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
