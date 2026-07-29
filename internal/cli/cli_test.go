package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
			{"rule":"second-rule","note":"Second note.","finding_ids":[11,12]},
			{"rule":"third-rule","note":"Third note.","finding_ids":[11,12]}]}`),
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
	assert.Contains(t, err.Error(), "p1 is not a pending proposal")

	_, err = run(t, "-C", root, "lessons", "--dismiss", "p999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p999 is not a pending proposal")

	_, err = run(t, "-C", root, "lessons", "--apply", "pX")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a proposal id")

	// The natural spaced form: the shell splits "--apply p1, p2" into
	// positional args, which must fold back into the id list instead of
	// tripping cobra's unknown-command error (a real-session paper cut).
	_, err = run(t, "-C", root, "lessons", "--apply", "p9,", "p10")
	require.Error(t, err, "nothing pending — but the spaced ids must PARSE")
	assert.Contains(t, err.Error(), "p9 is not a pending proposal", "spaced list understood, error by name")

	// An exhausted range says so instead of pretending success.
	_, err = run(t, "-C", root, "lessons", "--apply", "p1..p3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still pending")

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

func TestSelectionParsingAndResolution(t *testing.T) {
	pending := []model.Proposal{
		{ID: 1, Rule: "a"}, {ID: 3, Rule: "c"}, {ID: 4, Rule: "d"}, {ID: 9, Rule: "i"},
	}

	// A range takes what it finds (2 is a hole), dash form included,
	// mixed with exact ids, deduplicated, id-ordered.
	got, err := resolveSelection(pending, "p3, p1..p4, 9")
	require.NoError(t, err)

	ids := make([]int64, len(got))
	for i, p := range got {
		ids[i] = p.ID
	}

	assert.Equal(t, []int64{1, 3, 4, 9}, ids)

	got, err = resolveSelection(pending, "p1-p4")
	require.NoError(t, err)
	assert.Len(t, got, 3, "dash ranges work like dotted ones")

	// Reversed and absurd ranges fail loudly.
	_, err = resolveSelection(pending, "p9..p1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reversed")

	_, err = resolveSelection(pending, "p1..p99999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "implausibly wide")

	// Exact ids stay strict even next to a tolerant range.
	_, err = resolveSelection(pending, "p2, p1..p9")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "p2 is not a pending proposal")
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
