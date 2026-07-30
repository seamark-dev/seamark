package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Contains(t, out, "redaction none")
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
			{"rule":"pooled-state-reset","note":"Reset pooled state on every reuse.","finding_ids":[11,12]},
			{"rule":"second-rule","note":"Validate request payload bounds before touching the database.","finding_ids":[11,12]},
			{"rule":"third-rule","note":"Close the websocket subscription on every worker exit path.","finding_ids":[11,12]}]}`),
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
	payload := `{"tool_name":"Edit","tool_input":{"file_path":"` +
		filepath.Join(root, "a.go") + `"}}`
	_, _, err = runIn(t, payload, "-C", root, "lessons", "--hook")
	require.NoError(t, err)

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "the hook logged one firing")
	assert.Equal(t, "Edit", firings[0].Tool)

	// --stats surfaces the fired lesson and the never-fired decay candidate.
	out, err := run(t, "-C", root, "lessons", "--stats")
	require.NoError(t, err)
	assert.Contains(t, out, "RUF001", "the fired lesson is surfaced")
	assert.Contains(t, out, "never fired", "the unedited-region lesson is a decay candidate")
	assert.Contains(t, out, "E501")
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
