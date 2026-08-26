package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// run executes the CLI with args against a fresh command tree, returning
// stdout. Use runErr when a test needs stderr too.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	stdout, _, err := runErr(t, args...)

	return stdout, err
}

func runErr(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()

	if errOut.Len() > 0 {
		t.Logf("stderr: %s", errOut.String())
	}

	return out.String(), errOut.String(), err
}

func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n",
		"a.go": `package main

func main() { helper() }

// helper does the work.
func helper() {}
`,
	}

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	return root
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "seamark")
}

func TestIndexThenWhy(t *testing.T) {
	root := writeFixture(t)

	out, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	assert.Contains(t, out, "symbols", "index summary should report symbol count")
	assert.FileExists(t, filepath.Join(root, ".seamark", "index.db"))

	// Symbol report: helper's caller is main, with the edge's derivation.
	out, err = run(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	for _, want := range []string{"helper", "(function)", "a.go:6", "callers (1)", "main", "[same-package]"} {
		assert.Contains(t, out, want)
	}

	// File report lists its symbols.
	out, err = run(t, "-C", root, "why", "a.go")
	require.NoError(t, err)
	assert.Contains(t, out, "defines (2)")
}

func TestIndexRejectsReviewsWithFixesOnly(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index", "--reviews", "--fixes-only")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none of the others can be")
}

func gitify(t *testing.T, root string) {
	t.Helper()

	for _, args := range [][]string{
		{"init", "-b", "main"}, {"add", "-A"}, {"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
}

func TestStateExportImportRoundTrip(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Plant a durable decision behind the CLI's back.
	st, err := store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "sig-1", Rule: "keep-ascii", Note: "ascii only",
		Status: model.ProposalDismissed, CreatedAt: 1700000000,
	}))
	require.NoError(t, st.Close())

	// Export to a file in a directory that does not exist yet; the file
	// is created private.
	exportPath := filepath.Join(root, "backups", "state.json")
	out, err := run(t, "-C", root, "state", "export", "--out", exportPath)
	require.NoError(t, err)
	assert.Contains(t, out, "1 proposals")

	// `--out -` is the stdout alias, matching report's convention.
	out, err = run(t, "-C", root, "state", "export", "--out", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "seamark_state_version")

	info, err := os.Stat(exportPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "an export carries decisions — private by default")

	// Import into a fresh clone that has never been indexed: the restore
	// path must not require an index to exist.
	clone := writeFixture(t)
	out, err = run(t, "-C", clone, "state", "import", exportPath)
	require.NoError(t, err)
	assert.Contains(t, out, "1 added")

	cloneStore, err := store.Open(filepath.Join(clone, ".seamark", "index.db"))
	require.NoError(t, err)
	defer func() { _ = cloneStore.Close() }()

	dismissed, err := cloneStore.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, dismissed, 1)
	assert.Equal(t, "keep-ascii", dismissed[0].Rule)
}

func TestStateExportWithoutIndexFails(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "state", "export")
	require.Error(t, err, "exporting from nothing must not fabricate an empty bundle")
	assert.Contains(t, err.Error(), "no index")
}

func TestStateExportRefusesToOverwriteDatabase(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	dbPath := filepath.Join(root, ".seamark", "index.db")
	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	_, err = run(t, "-C", root, "state", "export", "--out", dbPath)
	require.Error(t, err, "--out must never target the database being exported")
	assert.Contains(t, err.Error(), "index database")

	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the database must be byte-identical")
}

func TestStateExportReplacesAtomicallyAndTightens(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// A pre-existing world-readable destination: the atomic replace
	// installs a fresh 0600 file rather than inheriting loose perms.
	out := filepath.Join(root, "state.json")
	require.NoError(t, os.WriteFile(out, []byte("old export"), 0o644))

	_, err = run(t, "-C", root, "state", "export", "--out", out)
	require.NoError(t, err)

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), "seamark_state_version")
}

func TestStateImportRejectsTrailingData(t *testing.T) {
	root := writeFixture(t)

	_, _, err := runIn(t, `{"seamark_state_version":1}{"seamark_state_version":1}`,
		"-C", root, "state", "import")
	require.Error(t, err, "concatenated documents must not half-import")
	assert.Contains(t, err.Error(), "trailing data")
}

func TestStateImportRefusesForeignRepository(t *testing.T) {
	// Two distinct repositories: distinct root commits. The fixtures are
	// byte-identical and git hashes deterministically, so the second repo
	// needs distinct content to get a distinct root commit.
	src := writeFixture(t)
	gitify(t, src)

	dst := writeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dst, "other.txt"), []byte("different\n"), 0o644))
	gitify(t, dst)

	_, err := run(t, "-C", src, "index")
	require.NoError(t, err)

	bundle := filepath.Join(src, "state.json")
	_, err = run(t, "-C", src, "state", "export", "--out", bundle)
	require.NoError(t, err)

	_, err = run(t, "-C", dst, "state", "import", bundle)
	require.Error(t, err, "a bundle from another repository must be refused")
	assert.Contains(t, err.Error(), "different repository")

	// --force is the explicit override.
	out, err := run(t, "-C", dst, "state", "import", "--force", bundle)
	require.NoError(t, err)
	assert.Contains(t, out, "proposals")
}

func TestWhyAfterImportOnlyDatabaseStillWantsIndex(t *testing.T) {
	root := writeFixture(t)

	// Restoring state before the first index run creates the database —
	// but graph questions must still say "index first", not answer
	// emptily from a graphless file.
	_, _, err := runIn(t, `{"seamark_state_version":1}`, "-C", root, "state", "import")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, ".seamark", "index.db"))

	_, err = run(t, "-C", root, "why", "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no index found")

	_, err = run(t, "-C", root, "orient")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no index found")
}

func TestWhyWarnsWhenStale(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	_, stderr, err := runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "workspace changed", "fresh index warns about nothing")

	// Change the workspace: the same query now carries a staleness note.
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.go"),
		[]byte("package main\n\nfunc extra() {}\n"), 0o644))

	_, stderr, err = runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.Contains(t, stderr, "workspace changed since the last index")
}

func TestCheckSelfRepairsStaleIndex(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	// No index exists at all: check builds one instead of failing.
	_, stderr, err := runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, stderr, "index refreshed")

	// Fresh index + unchanged workspace: no repair on the second run.
	_, stderr, err = runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "index refreshed",
		"an up-to-date index must not be rebuilt")

	// A workspace change repairs on the next check, then settles.
	require.NoError(t, os.WriteFile(filepath.Join(root, "c.go"),
		[]byte("package main\n\nfunc later() {}\n"), 0o644))

	_, stderr, err = runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, stderr, "index refreshed")
}

func TestWhyWithoutIndexFails(t *testing.T) {
	root := t.TempDir()
	_, err := run(t, "-C", root, "why", "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "seamark index", "error should point at the fix")
}

func TestOrientCommand(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "orient")
	require.NoError(t, err)
	assert.Contains(t, out, "orientation")
	assert.Contains(t, out, "modules (by symbol count)")
	assert.Contains(t, out, "most-called")
}

func TestStatusReportsHealth(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "workspace      current")
	assert.Contains(t, out, "schema v")
	assert.Contains(t, out, "parsed")
	assert.Contains(t, out, "symbols")
	assert.Contains(t, out, "gate")

	// Machine-readable form parses and carries the same facts.
	out, err = run(t, "-C", root, "status", "--json")
	require.NoError(t, err)

	var s map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &s))
	assert.Equal(t, "current", s["freshness"])
	assert.NotEmpty(t, s["schema_version"])

	// Editing the workspace flips freshness — status must never claim
	// current over a changed tree.
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.go"),
		[]byte("package main\nfunc extra() {}\n"), 0o644))

	out, err = run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "STALE")
}

func TestStatusWithoutIndexFails(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "status")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no index")
}

func TestCheckNotesUnindexedFiles(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// A diff touching a file the index has no symbols for: the verdict
	// must carry the uncertainty, not read as a clean allow.
	diff := "--- a/mystery.go\n+++ b/mystery.go\n@@ -0,0 +1 @@\n+package main\n"

	out, _, err := runIn(t, diff, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, out, "note:")
	assert.Contains(t, out, "mystery.go")
	assert.Contains(t, out, "unknown, not clean")

	// And policy can act on it: a rule over diff.unindexed_files fires.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(
		"mode: warn\nrequire_approval:\n"+
			"  - id: unindexed-blindspot\n"+
			"    when: 'diff.unindexed_files > 0'\n"+
			"    message: changed files outside index coverage\n"), 0o644))

	out, _, err = runIn(t, diff, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, out, "unindexed-blindspot")
}

func TestCheckNotesChangesOutsideSymbolSpans(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// a.go IS indexed (it has symbols), but this diff touches only line
	// 1 — the package/import area outside any symbol span. Import edits
	// change resolution and reach; the verdict must carry the
	// uncertainty instead of reading as a clean allow.
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-package main\n+package main // touched\n"

	out, _, err := runIn(t, diff, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, out, "note:")
	assert.Contains(t, out, "a.go")
	assert.Contains(t, out, "unknown, not clean")
}

func TestCheckNotesWholeFileDeletion(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// A whole-file deletion has `+++ /dev/null`: the vanished path must
	// appear in the verdict, never as an empty trustworthy diff.
	diff := "--- a/mystery.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-package main\n-\n-func gone() {}\n"

	out, _, err := runIn(t, diff, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, out, "note:")
	assert.Contains(t, out, "mystery.go", "the deleted path must surface in the uncertainty note")
}

func TestIndexBackfillsCoverageSummary(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Simulate a pre-coverage database: same fingerprint, no summary.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("index_summary", ""))
	require.NoError(t, st.Close())

	// A plain reindex must NOT take the fast path — the advertised
	// upgrade route is `seamark index`, not `--force` folklore.
	out, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	assert.NotContains(t, out, "already up to date")

	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	summary, err := st.GetMeta("index_summary")
	require.NoError(t, err)
	assert.NotEmpty(t, summary, "the reindex must record coverage")
}

func TestDoctorReportsAndFails(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "doctor")
	require.NoError(t, err, "a healthy workspace must pass")
	assert.Contains(t, out, "installation health")
	assert.Contains(t, out, "schema v")

	// JSON form parses.
	out, err = run(t, "-C", root, "doctor", "--json")
	require.NoError(t, err)

	var report struct {
		Checks []struct{ Name, State string } `json:"checks"`
		Fails  int                            `json:"fails"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.NotEmpty(t, report.Checks)
	assert.Zero(t, report.Fails)

	// A broken policy is a failed check and a non-zero exit — CI can
	// gate on doctor.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))

	out, err = run(t, "-C", root, "doctor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
	assert.Contains(t, out, "fail")
	assert.Contains(t, out, "policy")
}

func TestOrientWarnsOnParseErrors(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// A clean fixture orients without the warning…
	out, err := run(t, "-C", root, "orient")
	require.NoError(t, err)
	assert.NotContains(t, out, "WARN")

	// …and a recorded parse failure surfaces compactly.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("index_summary",
		`{"files_seen":3,"files_parsed":2,"parse_errors":1}`))
	require.NoError(t, st.Close())

	out, err = run(t, "-C", root, "orient")
	require.NoError(t, err)
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "failed to parse")
}

func TestWhyUnknownSymbolFails(t *testing.T) {
	root := writeFixture(t)
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	_, err = run(t, "-C", root, "why", "definitely_not_there_xyz")
	assert.Error(t, err)
}

func TestLessonsHook(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Seed a lesson on a.go's region (the fixture root) via a raw store
	// write, then confirm the hook injects it for an edit to that file.
	seedLesson(t, root, "a.go", "RUF001", 4)

	payload := `{"tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`

	out, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)

	var hook struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &hook))
	assert.Equal(t, "PreToolUse", hook.HookSpecificOutput.HookEventName)
	assert.Contains(t, hook.HookSpecificOutput.AdditionalContext, "RUF001")
	assert.Contains(t, hook.HookSpecificOutput.AdditionalContext, "lessons --region",
		"the hook points at the raw ledger and the pin-proposal loop")

	// A file with no lessons produces no output — the hook stays silent.
	quiet := `{"tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "nowhere.go") + `"}}`
	out, _, err = runIn(t, quiet, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestLessonsHookOncePerContextResetsAfterCompaction(t *testing.T) {
	root := writeFixture(t)
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	seedLesson(t, root, "a.go", "RUF001", 4)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("threshold: 2\nhook_delivery: once-per-context\n"), 0o644))
	payload := `{"session_id":"session-one","tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`

	first, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, first, "RUF001")

	second, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(second), "the same context receives no duplicate lesson")

	_, _, err = runIn(t, `{"session_id":"session-one"}`,
		"-C", root, "lessons", "--hook-reset")
	require.NoError(t, err)

	third, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, third, "RUF001", "PostCompact starts a new delivery generation")

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 3)
	assert.Equal(t, reviews.DeliveryInjected, firings[0].Delivery)
	assert.Equal(t, reviews.DeliverySuppressedRepeat, firings[1].Delivery)
	assert.Equal(t, reviews.DeliveryInjected, firings[2].Delivery)
}

func TestLessonsHookAlwaysRemainsTheDefault(t *testing.T) {
	root := writeFixture(t)
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	seedLesson(t, root, "a.go", "RUF001", 4)

	payload := `{"session_id":"session-one","tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`
	for range 2 {
		out, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
		require.NoError(t, err)
		assert.Contains(t, out, "RUF001")
	}
}

func TestLessonsList(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// A below-threshold one-off must still appear in the ledger (it's the
	// raw material for deciding what to pin), unlike why/orient.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "scripts\x00E702", Region: "scripts", Reviewer: "coderabbit",
			Symptom: "E702", Occurrences: 9, LastTS: 2},
		{ClusterKey: "a.go\x00RUF001", Region: "a.go", Reviewer: "coderabbit",
			Symptom: "RUF001", Occurrences: 1, LastTS: 1},
	}, nil))
	require.NoError(t, st.Close())

	out, err := run(t, "-C", root, "lessons", "--list")
	require.NoError(t, err)
	assert.Contains(t, out, "2 total")
	assert.Contains(t, out, "E702")
	assert.Contains(t, out, "RUF001", "one-offs appear in the ledger")
	assert.Contains(t, out, ".seamark/lessons.yaml", "shows tuning syntax")

	// --region narrows the ledger to one area (implying --list): the raw
	// per-file findings an agent reads side by side to spot a pattern.
	out, err = run(t, "-C", root, "lessons", "--region", "scripts")
	require.NoError(t, err)
	assert.Contains(t, out, "E702", "lessons inside the region stay")
	assert.NotContains(t, out, "RUF001", "lessons outside the region are filtered")
	assert.Contains(t, out, "1 total")
}

func TestLessonsDistillDryRun(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 11, LessonKey: "k", Path: "api/a.go", PR: 1, Reviewer: "human",
			Body: "Reset pooled state before reuse in this handler."},
		{ID: 12, LessonKey: "k", Path: "api/b.go", PR: 2, Reviewer: "human",
			Body: "Pooled state must be reset on reuse here too."},
	}))
	require.NoError(t, st.Close())

	// The fake agent leaves a marker when invoked — the dry run must not.
	marker := filepath.Join(root, "agent-was-called")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"sh\", \"-c\", \"touch "+marker+"; echo '{}'\"]\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--distill", "--dry-run")
	require.NoError(t, err)

	// The disclosure covers agent, size, contents, and redaction status —
	// without any finding bodies.
	assert.Contains(t, out, "preflight")
	assert.Contains(t, out, "sh -c")
	assert.Contains(t, out, "2 finding(s)")
	assert.Contains(t, out, "tokens")
	assert.Contains(t, out, "fix-commit message")
	assert.Contains(t, out, "patch excerpt")
	assert.Contains(t, out, "no whole source files")
	assert.Contains(t, out, "no additional dispatch redaction")
	assert.Contains(t, out, "mining already scrubs secret-shaped values")
	assert.Contains(t, out, "review PR #1  api/a.go  (finding 11)")
	assert.Contains(t, out, "review PR #2  api/b.go  (finding 12)")
	assert.Contains(t, out, "nothing was sent")
	assert.NotContains(t, out, "Reset pooled state", "finding bodies must not appear in a dry run")

	assert.NoFileExists(t, marker, "the agent CLI must not be invoked")

	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	pending, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	assert.Empty(t, pending, "a dry run must not persist proposals")
	require.NoError(t, st.Close())

	// A dry run without --distill is a usage error — including when a
	// mutating mode is present: --apply must never run with --dry-run
	// attached, and the pending proposal set must stay untouched.
	_, err = run(t, "-C", root, "lessons", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--distill")

	_, err = run(t, "-C", root, "lessons", "--apply", "p1", "--dry-run")
	require.Error(t, err, "--apply with --dry-run must be rejected before dispatch")
	assert.Contains(t, err.Error(), "--distill")

	_, err = run(t, "-C", root, "lessons", "--dismiss", "p1", "--dry-run")
	require.Error(t, err, "--dismiss with --dry-run must be rejected before dispatch")

	// A dry run works even when the configured agent does not exist —
	// disclosure must not require the binary.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"no-such-agent-binary\"]\n"), 0o644))

	out, err = run(t, "-C", root, "lessons", "--distill", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "no-such-agent-binary")

	// But an invalid configuration is an error, not an empty disclosure:
	// a dry run must not paper over an unknown preset.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: hal9000\n"), 0o644))

	_, err = run(t, "-C", root, "lessons", "--distill", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hal9000")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"\"]\n"), 0o644))

	_, err = run(t, "-C", root, "lessons", "--distill", "--dry-run")
	require.Error(t, err, "an empty custom executable must be rejected")
}

func TestDistillPreflightShowsRelevantFixPathsAndAdaptiveCap(t *testing.T) {
	var out bytes.Buffer

	printPreflight(&out, distill.Preflight{
		Agent: []string{"claude", "-p"}, PromptChars: 4_000, Findings: 2, BodyCap: 3_000,
		Groups: []distill.GroupPlan{{
			Region: "sdk/metric/internal/aggregate", Findings: 2,
			PromptChars: 4_000, BodyCap: 3_000,
			Evidence: []distill.FindingPlan{{
				ID: 1, Source: model.SourceFixConventional, PR: 8403,
				Path: "sdk/metric/internal/aggregate/exponential_histogram.go",
				Paths: []string{
					"sdk/metric/internal/aggregate/exponential_histogram.go",
					"sdk/metric/internal/aggregate/histogram.go",
				},
			}},
		}},
	}, true)

	text := out.String()
	assert.Contains(t, text, "3000 chars/finding max")
	assert.Contains(t, text, "paths  sdk/metric/internal/aggregate/exponential_histogram.go, "+
		"sdk/metric/internal/aggregate/histogram.go")
}

func TestLessonsDistillPlanFlow(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Seed two findings that bucket into one group, and point the agent
	// config at a shell script standing in for a real CLI — the whole
	// pipeline runs, no network, no claude.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 11, LessonKey: "k", Path: "api/a.go", PR: 1, Reviewer: "human",
			Body: "Reset pooled state before reuse in this handler."},
		{ID: 12, LessonKey: "k", Path: "api/b.go", PR: 2, Reviewer: "human",
			Body: "Pooled state must be reset on reuse here too."},
	}))
	require.NoError(t, st.Close())

	replyPath := filepath.Join(root, "agent-reply.json")
	require.NoError(t, os.WriteFile(replyPath,
		[]byte(`{"patterns":[
			{"rule":"pooled-state-reset","note":"Reset pooled state on every reuse.","finding_ids":[11,12],"trigger_paths":[]},
			{"rule":"second-rule","note":"Validate request payload bounds before touching the database.","finding_ids":[11,12],"trigger_paths":[]},
			{"rule":"third-rule","note":"Close the websocket subscription on every worker exit path.","finding_ids":[11,12],"trigger_paths":[]}]}`),
		0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"sh\", \"-c\", \"cat >/dev/null; cat "+replyPath+"\"]\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--distill")
	require.NoError(t, err)
	assert.Contains(t, out, "1 read")
	assert.Contains(t, out, "pooled-state-reset")
	assert.Contains(t, out, "Reset pooled state on every reuse.")
	assert.Contains(t, out, "2 findings cited")
	assert.Contains(t, out, "awaiting YOUR decision")

	// Second run: nothing re-read, the pending plan still shows.
	out, err = run(t, "-C", root, "lessons", "--distill")
	require.NoError(t, err)
	assert.Contains(t, out, "1 already distilled")
	assert.Contains(t, out, "pooled-state-reset", "pending proposals persist across runs")

	// An unusable agent config is a loud, actionable error.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: hal9000\n"), 0o644))

	_, err = run(t, "-C", root, "lessons", "--distill")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "distill unavailable")

	// ---- decide: apply without the write gate prints, never edits ----
	out, err = run(t, "-C", root, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "distill.write is off")
	assert.Contains(t, out, "pooled-state-reset", "the block to paste is printed")
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "lessons.yaml"))

	// With the gate on, a dismissed hole plus a range: p2 is dismissed,
	// then p1..p3 applies whatever is still pending — ranges tolerate
	// holes, that is their point.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("distill:\n  write: true\n"), 0o644))

	out, err = run(t, "-C", root, "lessons", "--dismiss", "p2")
	require.NoError(t, err)
	assert.Contains(t, out, "dismissed 1 proposal(s)")

	out, err = run(t, "-C", root, "lessons", "--apply", "p1..p3")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 2 pin(s)", "the range skips the dismissed hole")

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "pooled-state-reset")
	assert.Contains(t, string(data), "third-rule")
	assert.NotContains(t, string(data), "second-rule", "dismissed proposals never land")
	assert.Contains(t, string(data), "distilled by custom/",
		"provenance is stamped; the version bumps with the prompt")

	out, err = run(t, "-C", root, "lessons", "--file", "api/a.go")
	require.NoError(t, err)
	assert.Contains(t, out, "pooled-state-reset — Reset pooled state on every reuse.",
		"an applied pin surfaces like any hand-written one")

	// A bare id is a precise pointer: decided or unknown errors by name.
	_, err = run(t, "-C", root, "lessons", "--apply", "p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p1 is not pending")

	_, err = run(t, "-C", root, "lessons", "--dismiss", "p999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p999 is not pending")

	_, err = run(t, "-C", root, "lessons", "--apply", "pX")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a proposal id")

	// The natural spaced form: the shell splits "--apply p1, p2" into
	// positional args, which must fold back into the id list instead of
	// tripping cobra's unknown-command error (a real-session paper cut).
	_, err = run(t, "-C", root, "lessons", "--apply", "p9,", "p10")
	require.Error(t, err, "nothing pending — but the spaced ids must PARSE")
	assert.Contains(t, err.Error(), "p9 is not pending", "spaced list understood, error by name")

	// An exhausted range says so instead of pretending success.
	_, err = run(t, "-C", root, "lessons", "--apply", "p1..p3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is pending")

	// A stray positional without --apply/--dismiss stays an error.
	_, err = run(t, "-C", root, "lessons", "p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected argument")

	// Opposite decisions in one command refuse before touching anything.
	_, err = run(t, "-C", root, "lessons", "--apply", "p1", "--dismiss", "p2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one at a time")
}

func TestLessonsProposalsLedger(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Empty ledger says so, and points at how to fill it.
	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "no proposals yet")

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	for _, p := range []model.Proposal{
		{Signature: "s1", Rule: "pending-one", Region: "api", Note: "Guard the boundary.",
			Members: []int64{1, 2}, Agent: "claude/v2", Status: model.ProposalProposed},
		{Signature: "s2", Rule: "applied-one", Region: "", Note: "n",
			Members: []int64{3, 4}, Agent: "claude/v2", Status: model.ProposalApplied},
		{Signature: "s3", Rule: "dismissed-one", Region: "web", Note: "n",
			Members: []int64{5, 6}, Agent: "claude/v2", Status: model.ProposalDismissed},
	} {
		require.NoError(t, st.InsertProposal(&p))
	}

	require.NoError(t, st.Close())

	out, err = run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Contains(t, out, "1 pending, 1 applied, 1 dismissed")
	assert.Contains(t, out, "pending-one")
	assert.Contains(t, out, "Guard the boundary.", "pending proposals show their full note")
	assert.Contains(t, out, "applied-one")
	assert.Contains(t, out, "dismissed-one")
	assert.Contains(t, out, "*", "a repo-wide region renders as *")
	assert.Contains(t, out, "--apply p<id>", "the decide commands ride along")
}

// TestLessonsProposalsLedgerShowsOutcome wires the passive loop through
// the real command: a cited finding before exposure, an uncited
// recurrence in the same cluster after, one genuine firing record — the
// ledger must render the not-landing sentence and the escalation hint.
func TestLessonsProposalsLedgerShowsOutcome(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	now := time.Now()

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "api\x00boundary", Path: "api/handler.go", PR: 11,
			Body: "guard the api boundary", CreatedAt: now.Add(-time.Hour).Unix(), Source: "review"},
		{ID: 2, LessonKey: "api\x00boundary", Path: "api/handler.go", PR: 12,
			Body: "api boundary unguarded again", CreatedAt: now.Add(time.Hour).Unix(), Source: "review"},
	}))

	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "boundary-guard", Region: "api", Note: "Guard it.",
		Members: []int64{1}, Agent: "claude/v2", Status: model.ProposalApplied,
	}))
	require.NoError(t, st.Close())

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: boundary-guard\n    region: api\n    note: Guard it.\n"), 0o644))

	pin := reviews.PinRule{Rule: "boundary-guard", Region: "api", Note: "Guard it."}
	require.NoError(t, reviews.RecordFiring(root, "api/handler.go", "Edit",
		[]model.Lesson{reviews.SurfacedPin{Pin: pin}.Lesson()}))

	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Contains(t, out, "not landing — recurred 1× since exposure (fired 1×)")
	assert.Contains(t, out, "escalation is yours")

	// --stats prints the same reading: both surfaces render the exact
	// same sentence from outcome.Line.
	out, err = run(t, "-C", root, "lessons", "--stats")
	require.NoError(t, err)

	assert.Contains(t, out, "pin outcomes — 1 measured: 0 working, 1 not landing, 0 untested")
	assert.Contains(t, out, "boundary-guard")
	assert.Contains(t, out, "not landing — recurred 1× since exposure (fired 1×)")
}

// TestLessonsProposalsLedgerShowsScopeAdvisory wires the trigger-scope
// audit through the real command: notes name the trigger file, the
// evidence file's co-change partner agrees, and the ledger renders one
// advisory per flagged pin. Applied pins are judged by their LIVE yaml
// note; pruned pins are skipped.
func TestLessonsProposalsLedgerShowsScopeAdvisory(t *testing.T) {
	root := writeFixture(t)

	for _, rel := range []string{"api/schemas.py", "web/src/api/schema.ts"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	// Rebuild wipes the graph tables and plants the co-change edge;
	// findings and proposals are durable and inserted after it.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "regenerate the client",
			CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	for _, p := range []model.Proposal{
		{Signature: "s1", Rule: "pending-named", Region: "web/src/api",
			Note: "Edit api/schemas.py first.", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalProposed},
		{Signature: "s2", Rule: "applied-reworded", Region: "web/src/api",
			Note: "Edit api/schemas.py first.", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalApplied},
		{Signature: "s3", Rule: "applied-pruned", Region: "web/src/api",
			Note: "Edit api/schemas.py first.", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalApplied},
		{Signature: "s4", Rule: "applied-named", Region: "web/src/api",
			Note: "Sync the client.", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalApplied},
	} {
		require.NoError(t, st.InsertProposal(&p))
	}

	require.NoError(t, st.Close())

	// The yaml file is the live truth for applied pins: s2's note no
	// longer names the trigger, s3 is pruned, s4's note gained it.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n"+
			"  - rule: applied-reworded\n    region: web/src/api\n    note: Sync the client.\n"+
			"  - rule: applied-named\n    region: web/src/api\n    note: Edit api/schemas.py first.\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Equal(t, 2, strings.Count(out, "delivery may miss the trigger"),
		"pending-named and applied-named only: reworded and pruned pins stay silent")
	assert.Equal(t, 2, strings.Count(out, "consider regions: [web/src/api, api]"))
	assert.Contains(t, out, "api/schemas.py (outside the regions)")
	assert.Contains(t, out, "38 shared commits")
	assert.Contains(t, out, "trigger scope: p1, p4",
		"the tail names the flagged set so a long ledger cannot bury it")
}

// TestLessonsExtractTriggersBackfill drives the backfill end to end:
// disclosure and dry run first, then a scripted agent names a trigger,
// the harness validates it, and the pending row retargets in place.
func TestLessonsExtractTriggersBackfill(t *testing.T) {
	root := writeFixture(t)

	for _, rel := range []string{"api/schemas.py", "web/src/api/schema.ts"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "regenerate the client",
			CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	// p1 gets a trigger; p2's answer is negative; p3's pin is not in
	// lessons.yaml — hand-pruned, no delivery to widen.
	for _, p := range []model.Proposal{
		{Signature: "s1", Rule: "schema-sync", Region: "web/src/api",
			Note: "Keep the generated client current.", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalProposed},
		{Signature: "s2", Rule: "no-trigger-here", Region: "web/src/api",
			Note: "n", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalProposed},
		{Signature: "s3", Rule: "hand-pruned", Region: "web/src/api",
			Note: "n", Members: []int64{1},
			Agent: "claude/v3", Status: model.ProposalApplied},
	} {
		require.NoError(t, st.InsertProposal(&p))
	}

	require.NoError(t, st.Close())

	replyPath := filepath.Join(root, "extract-reply.json")
	require.NoError(t, os.WriteFile(replyPath,
		[]byte(`{"triggers":[{"id":1,"trigger_paths":["api/schemas.py","api/ghost.py"]}]}`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"sh\", \"-c\", \"cat >/dev/null; cat "+replyPath+"\"]\n"), 0o644))

	// Spending modes never combine with decision flags: dispatch order
	// must not turn --dry-run into a mutation.
	_, err = run(t, "-C", root, "lessons", "--apply", "p1", "--extract-triggers", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not combine")

	_, err = run(t, "-C", root, "lessons", "--distill", "--extract-triggers")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one at a time")

	// The read-only ledger view would be silently swallowed by dispatch.
	_, err = run(t, "-C", root, "lessons", "--proposals", "--apply", "p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--proposals does not combine")

	// The dry run discloses and stops; the pruned pin never rides.
	out, err := run(t, "-C", root, "lessons", "--extract-triggers", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "1 applied row(s) skipped")
	assert.Contains(t, out, "extraction preflight — 2 proposal(s) in 1 batch(es)")
	assert.Contains(t, out, "(dry run — nothing was sent")

	// The real run stores the validated path and retargets the row.
	out, err = run(t, "-C", root, "lessons", "--extract-triggers")
	require.NoError(t, err)
	assert.Contains(t, out, "2 examined, 1 named triggers, 1 stored, 1 pending retargeted")

	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	rows, err := st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// Newest first: rows[0] is p2, rows[1] is p1.
	assert.Nil(t, rows[0].TriggerPaths)
	assert.Positive(t, rows[0].TriggerChecked, "a negative answer is stamped, not forgotten")
	assert.Equal(t, distill.TriggerPromptVersion, rows[0].TriggerPromptVersion)
	assert.Equal(t, []string{"api/schemas.py"}, rows[1].TriggerPaths,
		"the ghost path must not survive validation")
	assert.Equal(t, []string{"api/schemas.py"}, rows[1].Regions)
	require.NoError(t, st.Close())

	// Re-running is free: answered rows — with paths or without — are
	// done, and the pruned pin still never rides.
	out, err = run(t, "-C", root, "lessons", "--extract-triggers")
	require.NoError(t, err)
	assert.Contains(t, out, "nothing to extract")

	// A negative answer from the legacy question is re-asked exactly once;
	// positive trigger answers above remain untouched.
	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	legacy := model.Proposal{
		Signature: "s4", Rule: "legacy-negative", Region: "web/src/api",
		Note: "n", Members: []int64{1}, Agent: "claude/v4",
		Status: model.ProposalProposed, TriggerChecked: time.Now().Unix(),
	}
	require.NoError(t, st.InsertProposal(&legacy))
	require.NoError(t, st.Close())
	require.NoError(t, os.WriteFile(replyPath,
		[]byte(fmt.Sprintf(`{"triggers":[{"id":%d,"trigger_paths":[]}]}`, legacy.ID)), 0o644))

	out, err = run(t, "-C", root, "lessons", "--extract-triggers")
	require.NoError(t, err)
	assert.Contains(t, out, "1 examined")

	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	rows, err = st.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	require.NotEmpty(t, rows)
	assert.Equal(t, distill.TriggerPromptVersion, rows[0].TriggerPromptVersion)
}

func TestPlanAnnotationsReportsDirectTriggerDelivery(t *testing.T) {
	root := writeFixture(t)
	trigger := filepath.Join(root, "api", "entry.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(trigger), 0o755))
	require.NoError(t, os.WriteFile(trigger, []byte("package api\n"), 0o644))

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{{
		ID: 1, Path: "api/entry.go", Body: "b", Source: model.SourceFixConventional,
	}}))

	p := model.Proposal{
		ID: 1, Rule: "keep-boundary-synchronized", Region: "api",
		Regions: []string{"api/entry.go"}, TriggerPaths: []string{"api/entry.go"},
		Members: []int64{1}, Status: model.ProposalProposed, Note: "Keep the boundary synchronized.",
	}
	notes, flagged, err := planAnnotations(st, &reviews.Config{}, root, []model.Proposal{p})
	require.NoError(t, err)
	assert.Empty(t, flagged)
	require.Len(t, notes[p.ID], 1)
	assert.Equal(t,
		"trigger api/entry.go — directly cited by the evidence; delivery targets api/entry.go",
		notes[p.ID][0])
}

// TestRetargetKeepsStoredTriggerScope pins the unified recompute: a pin
// already targeting a confirmed trigger shows no drift, while a broad
// evidence-scoped pin is retargeted to that precise edit surface.
func TestRetargetKeepsStoredTriggerScope(t *testing.T) {
	root := writeFixture(t)

	for _, rel := range []string{"api/schemas.py", "web/src/api/schema.ts"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "regenerate the client",
			CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	for _, p := range []model.Proposal{
		{Signature: "s1", Rule: "already-targeted", Region: "api/schemas.py",
			Regions: []string{"api/schemas.py"}, TriggerPaths: []string{"api/schemas.py"},
			Note: "n", Members: []int64{1}, Agent: "claude/v4", Status: model.ProposalApplied},
		{Signature: "s2", Rule: "still-broad", Region: "web/src/api",
			TriggerPaths: []string{"api/schemas.py"},
			Note:         "n", Members: []int64{1}, Agent: "claude/v4", Status: model.ProposalApplied},
	} {
		require.NoError(t, st.InsertProposal(&p))
	}

	require.NoError(t, st.Close())

	// Both pins are live in the yaml — region advice only applies to
	// pins that actually deliver.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n"+
			"  - rule: already-targeted\n    region: api/schemas.py\n    note: n\n"+
			"  - rule: still-broad\n    region: web/src/api\n    note: n\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(out, "regions now:"),
		"the precisely targeted pin shows no phantom drift")
	assert.Contains(t, out, "regions now: api/schemas.py")
	assert.Contains(t, out, "--retarget p2")

	// Write gate off: retarget prints the exact trigger scope it would
	// apply, not bare evidence coverage.
	out, err = run(t, "-C", root, "lessons", "--retarget", "p2")
	require.NoError(t, err)
	assert.Contains(t, out, "region: api/schemas.py")
}

// TestLedgerShowsBlockedTrigger pins finding visibility: a stored
// trigger that vanished from the working tree produces no drift and no
// note advisory — the ledger must still say so.
func TestLedgerShowsBlockedTrigger(t *testing.T) {
	root := writeFixture(t)

	for _, rel := range []string{"web/src/api/schema.ts", "cmd/a.go", "internal/b.go"} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		return tx.InsertCoChange(model.CoChange{
			FileA: "api/schemas.py", FileB: "web/src/api/schema.ts",
			Together: 38, Total: 400, Lift: 6.6,
		})
	}))

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
		{ID: 2, Path: "cmd/a.go", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
		{ID: 3, Path: "internal/b.go", Body: "b", CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "capped-pin", Region: "web/src/api",
		Regions:      []string{"web/src/api", "cmd", "internal"},
		TriggerPaths: []string{"api/schemas.py"}, TriggerChecked: time.Now().Unix(),
		Note: "n", Members: []int64{1, 2, 3},
		Agent: "claude/v4", Status: model.ProposalApplied,
	}))
	require.NoError(t, st.Close())

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: capped-pin\n    region: web/src/api\n"+
			"    regions: [web/src/api, cmd, internal]\n    note: n\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Contains(t, out, "confirmed by co-change (38 shared commits) but not deliverable")
	assert.Contains(t, out, "absent from the working tree")
	assert.NotContains(t, out, "regions now:", "no verified trigger leaves evidence coverage unchanged")
}

// TestLedgerNamesPrunedPinsInsteadOfAdvising pins the p45 lesson from
// the field: a hand-pruned pin delivers nothing, so the ledger must
// say that instead of computing drift and handing the user a retarget
// command that will refuse it.
func TestLedgerNamesPrunedPinsInsteadOfAdvising(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, Path: "web/src/api/schema.ts", Body: "b",
			CreatedAt: time.Now().Unix(), Source: "review"},
	}))

	// Stored region disagrees with coverage, so the pin WOULD drift —
	// but no lessons.yaml carries it.
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "hand-pruned", Region: "api",
		Note: "n", Members: []int64{1},
		Agent: "claude/v4", Status: model.ProposalApplied,
	}))
	require.NoError(t, st.Close())

	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)

	assert.Contains(t, out, "not in .seamark/lessons.yaml — this pin delivers nothing")
	assert.NotContains(t, out, "regions now:", "no advice for a pin that cannot act on it")
	assert.NotContains(t, out, "--retarget p", "the hint must not name a pin retarget will refuse")
}

// TestExtractSummarySaysAttemptedOnFailure: a CLI that dies before any
// request must not be reported as having "sent" tokens.
func TestExtractSummarySaysAttemptedOnFailure(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "r", Region: "api", Note: "n",
		Members: []int64{1}, Agent: "claude/v4", Status: model.ProposalProposed,
	}))
	require.NoError(t, st.Close())

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"sh\", \"-c\", \"cat >/dev/null; exit 1\"]\n"), 0o644))

	out, err := run(t, "-C", root, "lessons", "--extract-triggers")
	require.NoError(t, err)

	assert.Contains(t, out, "1 batch(es) failed")
	assert.Contains(t, out, "tokens attempted")
	assert.NotContains(t, out, "tokens sent")
}

func TestLessonsOutcomeSurfacesRejectMalformedConfig(t *testing.T) {
	root := writeFixture(t)
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("hook_delivery: sometimes\n"), 0o644))

	// Identity, not text: every surface must carry the sentinel, and
	// each may phrase its own context around it.
	_, err = run(t, "-C", root, "lessons", "--stats")
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	_, err = proposalHealth(st, root, []model.Proposal{{
		ID: 1, Status: model.ProposalApplied,
	}})
	require.ErrorIs(t, err, reviews.ErrLessonsConfig)
}

func TestLessonsPruneRetiresRestatements(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Two pins saying the same thing, one saying something else.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	for _, p := range []model.Proposal{
		{Signature: "s1", Rule: "docs-code-drift", Region: "api",
			Note:    "Update every doc, comment, and README that describes the changed behavior.",
			Members: []int64{1, 2, 3}, Agent: "claude/v2", Status: model.ProposalApplied},
		{Signature: "s2", Rule: "docs-out-of-sync-with-code", Region: "api",
			Note:    "Keep docstrings, comments, and README examples matching the code when behavior changes.",
			Members: []int64{4, 5}, Agent: "claude/v2", Status: model.ProposalApplied},
		{Signature: "s3", Rule: "bounded-event-deferral", Region: "api",
			Note:    "Route deferred events through one bounded queue so backpressure cannot amplify goroutines.",
			Members: []int64{6, 7}, Agent: "claude/v2", Status: model.ProposalApplied},
	} {
		require.NoError(t, st.InsertProposal(&p))
	}

	require.NoError(t, st.Close())

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n"+
			"  - rule: docs-code-drift\n    region: api\n    note: Update every doc.\n"+
			"  - rule: docs-out-of-sync-with-code\n    region: api\n    note: Keep docstrings matching.\n"+
			"  - rule: bounded-event-deferral\n    region: api\n    note: One bounded queue.\n"), 0o644))

	// The ledger names the cluster and hands over a ready command.
	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "near-duplicates")
	assert.Contains(t, out, "keep  p1", "the pin resting on more evidence survives")
	assert.Contains(t, out, "prune p2")
	assert.Contains(t, out, "--prune p2")
	assert.NotContains(t, out, "prune p3", "distinct guidance is never suggested for pruning")

	// Gate off: it tells you what to remove, and changes nothing.
	out, err = run(t, "-C", root, "lessons", "--prune", "p2")
	require.NoError(t, err)
	assert.Contains(t, out, "distill.write is off")
	assert.Contains(t, out, "docs-out-of-sync-with-code")

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "docs-out-of-sync-with-code", "nothing removed without the gate")

	// Gate on: the pin goes, the survivor and the unrelated pin stay,
	// and the proposal is superseded rather than dismissed.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("distill:\n  write: true\n"), 0o644))

	out, err = run(t, "-C", root, "lessons", "--prune", "p2")
	require.NoError(t, err)
	assert.Contains(t, out, "pruned 1 pin(s)")
	assert.Contains(t, out, "1 proposal(s) marked superseded")

	data, err = os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "docs-out-of-sync-with-code")
	assert.Contains(t, string(data), "docs-code-drift")
	assert.Contains(t, string(data), "bounded-event-deferral")

	out, err = run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "2 applied", "the pruned one left the applied set")
	assert.NotContains(t, out, "near-duplicates", "and the cluster is gone")

	// A pruned pin is not a dismissal: pruning something never applied
	// is refused rather than silently recorded.
	_, err = run(t, "-C", root, "lessons", "--prune", "p2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p2 is not applied", "prune searches the applied ledger, and says so")
}

func TestSelectionParsingAndResolution(t *testing.T) {
	pending := []model.Proposal{
		{ID: 1, Rule: "a"}, {ID: 3, Rule: "c"}, {ID: 4, Rule: "d"}, {ID: 9, Rule: "i"},
	}

	// A range takes what it finds (2 is a hole), dash form included,
	// mixed with exact ids, deduplicated, id-ordered.
	got, err := resolveSelection(pending, "p3, p1..p4, 9", "pending")
	require.NoError(t, err)

	ids := make([]int64, len(got))
	for i, p := range got {
		ids[i] = p.ID
	}

	assert.Equal(t, []int64{1, 3, 4, 9}, ids)

	got, err = resolveSelection(pending, "p1-p4", "pending")
	require.NoError(t, err)
	assert.Len(t, got, 3, "dash ranges work like dotted ones")

	// Reversed and absurd ranges fail loudly.
	_, err = resolveSelection(pending, "p9..p1", "pending")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reversed")

	_, err = resolveSelection(pending, "p1..p99999", "pending")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "implausibly wide")

	// Exact ids stay strict even next to a tolerant range.
	_, err = resolveSelection(pending, "p2, p1..p9", "pending")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p2 is not pending")
}

func TestHookBudgetsPinsFileViewDoesNot(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// Five pins covering a.go, in mixed specificity — the real-world
	// shape after a generous distill --apply session.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(`
pin:
  - {rule: wide-one, region: "*", note: "w1"}
  - {rule: wide-two, region: "*", note: "w2"}
  - {rule: wide-three, region: "*", note: "w3"}
  - {rule: on-file-one, region: a.go, note: "f1"}
  - {rule: on-file-two, region: a.go, note: "f2"}
`), 0o644))

	payload := `{"tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`

	out, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)

	var hook struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &hook))
	ctx := hook.HookSpecificOutput.AdditionalContext

	// Budget 3, file-specific first, the rest pointed at.
	assert.Contains(t, ctx, "on-file-one")
	assert.Contains(t, ctx, "on-file-two")
	assert.Contains(t, ctx, "wide-one")
	assert.NotContains(t, ctx, "wide-two", "beyond the budget")
	assert.Contains(t, ctx, "+2 more pins")

	// The deliberate --file view shows all five, no pointer line.
	listing, err := run(t, "-C", root, "lessons", "--file", "a.go")
	require.NoError(t, err)
	assert.Contains(t, listing, "wide-three")
	assert.NotContains(t, listing, "more pins")

	// pin_budget in lessons.yaml raises the injection cap.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(`
pin_budget: 5
pin:
  - {rule: wide-one, region: "*", note: "w1"}
  - {rule: wide-two, region: "*", note: "w2"}
  - {rule: wide-three, region: "*", note: "w3"}
  - {rule: on-file-one, region: a.go, note: "f1"}
  - {rule: on-file-two, region: a.go, note: "f2"}
`), 0o644))

	out, _, err = runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &hook))
	assert.Contains(t, hook.HookSpecificOutput.AdditionalContext, "wide-three")
	assert.NotContains(t, hook.HookSpecificOutput.AdditionalContext, "more pins")
}

func TestLessonsHookRecordsFiringAndStats(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	// One surfaced lesson on a.go's region, one on an unedited region.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "a.go\x00RUF001", Region: "a.go", Reviewer: "coderabbit",
			Symptom: "RUF001", Occurrences: 4, LastTS: 1},
		{ClusterKey: "other\x00E501", Region: "other", Reviewer: "coderabbit",
			Symptom: "E501", Occurrences: 3, LastTS: 1},
	}, nil))
	require.NoError(t, st.Close())

	// Fire the hook on a.go — it must record a firing.
	payload := `{"session_id":"session-to-hash","tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`
	hookJSON, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	var response hookOutput
	require.NoError(t, json.Unmarshal([]byte(hookJSON), &response))

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "the hook logged one firing")
	assert.Equal(t, "Edit", firings[0].Tool)
	assert.Equal(t, reviews.DeliveryInjected, firings[0].Delivery)
	assert.Len(t, firings[0].SessionSHA, 64)
	assert.NotEqual(t, "session-to-hash", firings[0].SessionSHA)
	assert.Equal(t, len(response.HookSpecificOutput.AdditionalContext), firings[0].ContextBytes)

	// --stats surfaces the fired lesson and the never-fired decay candidate.
	out, err := run(t, "-C", root, "lessons", "--stats")
	require.NoError(t, err)
	assert.Contains(t, out, "RUF001", "the fired lesson is surfaced")
	assert.Contains(t, out, "hook delivery — instrumented: 1 injected (0 repeated)")
	assert.Contains(t, out, "never fired", "the unedited-region lesson is a decay candidate")
	assert.Contains(t, out, "E501")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestLessonsHookDoesNotRecordFailedOutput(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	seedLesson(t, root, "a.go", "RUF001", 4)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"),
		[]byte("threshold: 2\nhook_delivery: once-per-context\n"), 0o644))

	payload := `{"session_id":"session-to-hash","tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`
	cmd := New()
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(failingWriter{})
	cmd.SetArgs([]string{"-C", root, "lessons", "--hook"})

	err = cmd.Execute()
	require.Error(t, err)

	firings, readErr := reviews.ReadFirings(root)
	require.NoError(t, readErr)
	assert.Empty(t, firings, "a failed hook response never reached the agent")

	out, _, err := runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "RUF001",
		"failed output must not mark the lesson delivered in once-per-context state")
}

// seedLesson writes one lesson row directly, so hook tests don't need a
// live GitHub source.
func seedLesson(t *testing.T, root, region, symptom string, occ int) {
	t.Helper()

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: region + "\x00" + symptom, Region: region,
			Reviewer: "coderabbit", Symptom: symptom, Occurrences: occ, LastTS: 1},
	}, nil))
}

// runIn is runErr with a stdin payload, for hook-mode tests.
func runIn(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()

	return out.String(), errOut.String(), err
}

func TestGateHookMode(t *testing.T) {
	root := writeFixture(t)

	// A PreToolUse payload is parsed natively — no jq in the loop.
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`

	_, _, err := runIn(t, payload, "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "force-push to main must block")

	out, _, err := runIn(t, `{"tool_input":{"command":"ls -la"}}`,
		"-C", root, "gate", "--enforce", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "allow")
}

func TestGateHookModeFailsClosed(t *testing.T) {
	root := writeFixture(t)

	// Under enforcement the gate's OWN failures must block: a malformed
	// payload or a missing command means it cannot vouch for anything.
	_, _, err := runIn(t, "{not json", "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "malformed payload must fail closed")

	_, _, err = runIn(t, `{"tool_input":{}}`, "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "empty command must fail closed")

	// Without enforcement the same failures surface as plain errors.
	_, _, err = runIn(t, "{not json", "-C", root, "gate", "--hook")
	require.Error(t, err)
	assert.NotErrorIs(t, err, gate.ErrBlocked)
}

// TestInitDefaultCannotBlock is the end-to-end trust-contract test: a
// first `seamark init` must not be able to produce a blocking verdict —
// not on a deny-rule match, not even on a broken policy file.
func TestInitDefaultCannotBlock(t *testing.T) {
	root := writeFixture(t)

	out, err := run(t, "-C", root, "init")
	require.NoError(t, err)
	assert.Contains(t, out, "gate    warn")

	// The installed hook must not carry --enforce anywhere.
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "--enforce")

	// A command matching a starter deny rule: the verdict surfaces, the
	// command still passes (exit 0).
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`
	stdout, _, err := runIn(t, payload, "-C", root, "gate", "--hook")
	require.NoError(t, err, "a default init must never block")
	assert.Contains(t, stdout, "deny")
	assert.Contains(t, stdout, "mode: warn")

	// Even a broken policy fails open in a default install: the error is
	// reported, but nothing blocks.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))

	_, _, err = runIn(t, payload, "-C", root, "gate", "--hook")
	require.Error(t, err)
	assert.NotErrorIs(t, err, gate.ErrBlocked, "a broken policy must not block a default install")
}

func TestInitGateModeEnforceBlocks(t *testing.T) {
	root := writeFixture(t)

	out, err := run(t, "-C", root, "init", "--gate-mode", "enforce")
	require.NoError(t, err)
	assert.Contains(t, out, "gate    enforce")

	// The scaffolded policy agrees with the hook it was installed with.
	data, err := os.ReadFile(filepath.Join(root, ".seamark", "policy.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "mode: enforce")

	// And the installed hook really carries the flag.
	hooks, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(hooks), "gate --enforce --hook")

	// The installed hook blocks a deny match end to end.
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`
	_, _, err = runIn(t, payload, "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked)
}

func TestInitRejectsUnknownGateMode(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "init", "--gate-mode", "block-everything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--gate-mode")
}

func TestReportWritesASelfContainedPage(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "report")
	require.NoError(t, err)

	path := filepath.Join(root, ".seamark", "report.html")
	assert.Contains(t, out, path, "the command says where it wrote")

	page, err := os.ReadFile(path)
	require.NoError(t, err)

	html := string(page)
	assert.Contains(t, html, "<!doctype html>")
	assert.Contains(t, html, "</html>")
	assert.Contains(t, html, "seamark report")

	// Self-contained: nothing is fetched when the file is opened, so it
	// works from an email attachment on a machine with no network. An
	// <a href> to provenance is navigation and stays allowed; what must
	// not exist is anything the browser resolves while *rendering* —
	// element sources, stylesheets, imports, CSS url() — unless it is an
	// inline data: URI.
	for _, m := range regexp.MustCompile(`(?i)\b(?:src|srcset)\s*=\s*["']([^"']*)`).
		FindAllStringSubmatch(html, -1) {
		assert.Truef(t, strings.HasPrefix(m[1], "data:"),
			"a source the browser would fetch: %s", m[0])
	}

	for _, m := range regexp.MustCompile(`(?i)\burl\(\s*["']?([^"')]*)`).
		FindAllStringSubmatch(html, -1) {
		assert.Truef(t, strings.HasPrefix(m[1], "data:"),
			"a CSS resource the browser would fetch: %s", m[0])
	}

	for _, banned := range []string{`(?i)<link\b`, `(?i)@import\b`, `(?i)<(?:iframe|object|embed)\b`} {
		assert.NotRegexp(t, banned, html, "the page must not load anything external")
	}
}

func TestReportToStdout(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "report", "-o", "-")
	require.NoError(t, err)
	assert.Contains(t, out, "<!doctype html>")
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "report.html"),
		"writing to stdout must not also leave a file behind")
}

func TestReportHonoursAnExplicitPath(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	target := filepath.Join(root, "docs", "audit", "report.html")

	_, err = run(t, "-C", root, "report", "--out", target)
	require.NoError(t, err)
	assert.FileExists(t, target, "missing parent directories are created")
}

func TestReportWithoutIndexFails(t *testing.T) {
	root := writeFixture(t)

	_, err := run(t, "-C", root, "report")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no index found")
}

// TestReadmeCoversEveryCommand is the docs-drift check: every shipped
// command is mentioned in the README, and no shipped command is
// labelled planned or coming soon. The RFC's rule: no shipped command
// may be presented as future work.
func TestReadmeCoversEveryCommand(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)

	readme := string(data)

	for _, c := range New().Commands() {
		name := c.Name()
		if name == "help" || name == "completion" || name == "version" {
			continue // cobra plumbing, not product surface
		}

		assert.Contains(t, readme, "seamark "+name,
			"README must document `seamark %s` (or retire the command)", name)

		for _, label := range []string{"planned", "soon"} {
			re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `[^\n]{0,40}\(` + label + `\)`)
			assert.False(t, re.MatchString(readme),
				"README labels shipped command %q as (%s)", name, label)
		}

		// The roadmap paragraph must not name a shipped command either —
		// "Planned next: … seamark doctor" would otherwise stay green
		// after doctor ships.
		if i := strings.Index(readme, "Planned next"); i >= 0 {
			para := readme[i:]
			if j := strings.Index(para, "\n\n"); j >= 0 {
				para = para[:j]
			}

			assert.NotContains(t, para, "seamark "+name,
				"the roadmap paragraph still lists shipped command %q as planned", name)
		}
	}
}

func TestWriteAtomicLeavesNoLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")

	require.NoError(t, writeAtomic(path, []byte("first")))
	require.NoError(t, writeAtomic(path, []byte("second")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(),
		"a report is readable, not 0600 as CreateTemp makes it")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the temporary file is renamed away, never left behind")
	assert.Equal(t, "report.html", entries[0].Name())
}

func TestWriteAtomicKeepsTheOldFileWhenWritingFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")

	require.NoError(t, writeAtomic(path, []byte("yesterday's good report")))

	// A directory that cannot be written to stands in for any failure
	// between opening the temporary file and renaming it into place.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	require.Error(t, writeAtomic(path, []byte("today's half-written report")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "yesterday's good report", string(data),
		"a failed write must not cost the reader the report they already had")
}

func TestLessonsRetargetFlow(t *testing.T) {
	// The upgrade path for pins distilled before region sets: the
	// ledger names the drift, --retarget rewrites lessons.yaml and the
	// ledger row together.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("indexed_at", "1"))
	require.NoError(t, st.SetMeta("repo_root", root))

	// Two independent events in scripts/: recomputed regions [scripts],
	// but the stored proposal is repo-wide — the pre-region-set shape.
	require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "scripts/a.py", PR: 1, Body: "x", Source: model.SourceReview},
		{ID: 2, LessonKey: "k", Path: "scripts/b.py", PR: 2, Body: "y", Source: model.SourceReview},
	}))

	saved, err := st.SaveDistilledGroup("sig-r", "", 1, []model.Proposal{{
		Signature: "sig-r", Rule: "guard-empty-datasets", Region: "",
		Note: "Guard datasets for emptiness before reductions.", Members: []int64{1, 2},
		Agent: "claude/v1", Status: model.ProposalProposed,
	}})
	require.NoError(t, err)
	_, err = st.SetProposalStatus([]int64{saved[0].ID}, model.ProposalApplied)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: guard-empty-datasets\n    region: '*'\n"+
			"    note: Guard datasets for emptiness before reductions.\n"), 0o644))

	// The ledger names the drift and the era.
	out, err := run(t, "-C", root, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "regions now: scripts")
	assert.Contains(t, out, "prompt v1", "the grandfathering era is visible")
	assert.Contains(t, out, "--retarget p1")

	// Without the write gate: prints, changes nothing.
	out, err = run(t, "-C", root, "lessons", "--retarget", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "distill.write is off")

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "region: '*'", "no write without the gate")

	// With the gate: file and ledger move together.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("distill:\n  write: true\n"), 0o644))

	out, err = run(t, "-C", root, "lessons", "--retarget", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "retargeted 1 pin(s)")

	data, err = os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "region: scripts")
	assert.NotContains(t, string(data), "region: '*'")

	st, err = store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	assert.Equal(t, "scripts", applied[0].Region)
	assert.Equal(t, []string{"scripts"}, applied[0].Regions)

	// Idempotent: a second retarget finds nothing to do.
	out, err = run(t, "-C", root, "lessons", "--retarget", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "regions already current")
}

func TestBlockedCheckStillPrintsAdvisoryLessons(t *testing.T) {
	// A deny is exactly when the lessons for the touched files matter
	// most: the advisory must survive the blocking verdict, and reach
	// files the index has never seen (a brand-new file in a pinned
	// region deserves its guidance MOST).
	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte(
		"mode: enforce\ndeny:\n"+
			"  - id: unindexed-blindspot\n"+
			"    when: 'diff.unindexed_files > 0'\n"+
			"    message: changed files outside index coverage\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(
		"pin:\n  - rule: scripts-guidance\n    region: scripts\n"+
			"    note: Guard datasets before reductions.\n"), 0o644))

	diff := "--- a/scripts/new_tool.py\n+++ b/scripts/new_tool.py\n@@ -0,0 +1 @@\n+x = 1\n"

	stdout, _, err := runIn(t, diff, "-C", root, "check")
	require.Error(t, err, "the enforce-mode deny must still block")
	assert.Contains(t, stdout, "advisory — recurring lessons for touched files")
	assert.Contains(t, stdout, "scripts-guidance",
		"a new, unindexed file in a pinned region receives its lesson even on a blocked check")
}
