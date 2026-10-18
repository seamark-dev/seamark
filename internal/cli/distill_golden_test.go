package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// These golden tests pin the full stdout and stderr of `lessons --distill`
// before the engine moves to a plan and execute split. The refactor must
// keep every byte, so each test compares whole streams, not fragments.
// Only wall-clock durations and the temporary root path are normalized.
// One change is deliberate: the dry run with a stale pending proposal now
// plans like the real run and names that proposal (decision D-2).

// goldenFindings are six findings with disjoint vocabulary. The grouper
// joins no two of them by theme, so it builds three directory groups:
// api/x (11, 12), api/y (13, 14), and web (15, 16).
var goldenFindings = []model.Finding{
	{ID: 11, LessonKey: "k", Path: "api/x/a.go", PR: 1, Reviewer: "person", Body: "Quokka marmalade trampoline."},
	{ID: 12, LessonKey: "k", Path: "api/x/b.go", PR: 2, Reviewer: "person", Body: "Zeppelin harmonica lighthouse."},
	{ID: 13, LessonKey: "k", Path: "api/y/c.go", PR: 3, Reviewer: "person", Body: "Cactus velvet saxophone."},
	{ID: 14, LessonKey: "k", Path: "api/y/d.go", PR: 4, Reviewer: "person", Body: "Glacier pumpkin origami."},
	{ID: 15, LessonKey: "k", Path: "web/e.go", PR: 5, Reviewer: "person", Body: "Walrus tambourine canyon."},
	{ID: 16, LessonKey: "k", Path: "web/f.go", PR: 6, Reviewer: "person", Body: "Nebula croissant kayak."},
}

// goldenAgent answers by the first finding id in the prompt, so each
// group gets a known reply: a valid pattern plus a pin restatement for
// api/x, an unparseable reply for api/y, and an exit status for web.
const goldenAgent = `#!/bin/sh
prompt=$(cat)
case "$prompt" in
*"finding id=11 "*)
  echo '{"patterns":[{"rule":"pooled-state-reset","note":"Reset pooled state before reuse.","finding_ids":[11,12],"trigger_paths":[]},{"rule":"api-x-rule","note":"Keep the api x handlers aligned.","finding_ids":[11,12],"trigger_paths":[]}]}'
  ;;
*"finding id=13 "*)
  echo 'I could not find patterns, sorry!'
  ;;
*"finding id=15 "*)
  echo 'rate limited' >&2
  exit 1
  ;;
*)
  echo '{"patterns":[]}'
  ;;
esac
`

// goldenPin is a hand-written pin that the api/x reply restates.
const goldenPin = "pin:\n  - rule: pooled-state-reset\n    note: Reset pooled state before reuse.\n"

// goldenWorkspace indexes a fixture workspace, stores findings, and
// points agent.argv at goldenAgent. It returns the workspace root.
func goldenWorkspace(t *testing.T, findings []model.Finding) string {
	t.Helper()

	root := writeFixture(t)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, findings))
	require.NoError(t, st.Close())

	script := filepath.Join(root, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(goldenAgent), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"sh\", \""+script+"\"]\n"), 0o644))

	return root
}

// goldenDuration matches the rounded durations that a real run prints.
var goldenDuration = regexp.MustCompile(`\b\d+(h\d+)?(m\d+)?s\b`)

// goldenRun runs the CLI and returns both streams in a stable form: the
// root path becomes <root> and every duration becomes <dur>.
func goldenRun(t *testing.T, root string, args ...string) (stdout, stderr string) {
	t.Helper()

	out, errOut, err := runErr(t, append([]string{"-C", root}, args...)...)
	require.NoError(t, err)

	return goldenNormalize(root, out), goldenNormalize(root, errOut)
}

// goldenNormalize removes the values that differ between runs. The
// resolved root is replaced too, because macOS links /var to /private/var.
func goldenNormalize(root, text string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		text = strings.ReplaceAll(text, resolved, "<root>")
	}

	text = strings.ReplaceAll(text, root, "<root>")

	return goldenDuration.ReplaceAllString(text, "<dur>")
}

// goldenSeedProposals stores ledger rows directly, as an earlier run or
// a decision would have left them.
func goldenSeedProposals(t *testing.T, root string, ps ...model.Proposal) {
	t.Helper()

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	for i := range ps {
		require.NoError(t, storetest.InsertProposal(st, &ps[i]))
	}
}

// goldenHead opens every preflight: the agent disclosure.
const goldenHead = `distill preflight — what leaves this machine
  agent     sh <root>/agent.sh  (your CLI, chosen by .seamark/config.yaml; assumed to reach
            a remote model service)
`

// goldenDisclosure is the fixed content and redaction text of a
// preflight with at least one planned group. The redaction sentence is
// stale: dispatch does redact. It stays until a separate decision.
const goldenDisclosure = `  contents  per finding: bounded review comment, or fix-commit message,
            changed-function names and representative patch excerpts; plus relevant
            repo-relative paths, finding id, source, PR number, reviewer, and labels
            — no whole source files or environment values
  redaction no additional dispatch redaction — stored bodies are sent as written,
            with path metadata and body sharing up to 3000 chars per finding; groups share a fixed evidence budget
            (mining already scrubs secret-shaped values)
`

// goldenDecide is the closing hint of a plan that lists proposals.
const goldenDecide = "decide: `seamark lessons --apply p%d` pins a chosen proposal; " +
	"comma lists and ranges work too; `--dismiss` remembers the no\n"

func TestLessonsDistillGoldenRegionAndLimit(t *testing.T) {
	root := goldenWorkspace(t, goldenFindings)

	// A dry run normalizes "./api" to the api region and plans one of
	// its two groups. The tail counts the planned and the deferred group.
	stdout, stderr := goldenRun(t, root, "lessons", "--distill", "--dry-run", "--region", "./api", "--limit", "1")
	assert.Equal(t, goldenHead+`  payload   1 group(s), 2 finding(s), ~533 tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~533 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)

(dry run — nothing was sent; 2 group(s) remain unread; drop --dry-run to distill)
`, stdout)
	assert.Empty(t, stderr)

	// A negative limit reads every group, the same as 0. This is a CLI
	// quirk; the console rejects a negative limit instead.
	stdout, stderr = goldenRun(t, root, "lessons", "--distill", "--dry-run", "--limit", "-1")
	assert.Equal(t, goldenHead+`  payload   3 group(s), 6 finding(s), ~1.6k tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~533 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)
    web                            2 finding(s)  ~530 tokens  3000 chars/finding max
      - review PR #5  web/e.go  (finding 15)
      - review PR #6  web/f.go  (finding 16)
    api/y                          2 finding(s)  ~531 tokens  3000 chars/finding max
      - review PR #3  api/y/c.go  (finding 13)
      - review PR #4  api/y/d.go  (finding 14)

(dry run — nothing was sent; 3 group(s) remain unread; drop --dry-run to distill)
`, stdout)
	assert.Empty(t, stderr)

	// A real run with an absolute region path reads one api group and
	// leaves the other for another run.
	stdout, stderr = goldenRun(t, root, "lessons", "--distill", "--region", filepath.Join(root, "api"), "--limit", "1")
	assert.Equal(t, goldenHead+`  payload   1 group(s), 2 finding(s), ~533 tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~533 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)

distill plan — 2 groups: 1 read, 1 left for another run (raise --limit or drop --region)
agent traffic: ~533 tokens sent, ~58 received (estimated), <dur>

proposed pins — distilled from review findings, awaiting YOUR decision

  p2    api-x-rule                         api/x                      2 findings cited [custom/v6]
        Keep the api x handlers aligned.

  p1    pooled-state-reset                 api/x                      2 findings cited [custom/v6]
        Reset pooled state before reuse.

`+fmt.Sprintf(goldenDecide, 2), stdout)
	assert.Equal(t, `distilling 2 findings (api/x, 04c79745da3ca579)
  <dur>, ~533 tokens sent / ~58 back, 2 proposal(s)
`, stderr)
}

func TestLessonsDistillGoldenFailuresAndDuplicate(t *testing.T) {
	root := goldenWorkspace(t, goldenFindings)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(goldenPin), 0o644))

	// Three groups: api/x restates the pin and adds one pattern, api/y
	// replies with prose, and web fails with an exit status. The pin
	// label primes every prompt, so the sizes exceed the region test.
	stdout, stderr := goldenRun(t, root, "lessons", "--distill", "--limit", "0")
	assert.Equal(t, goldenHead+`  payload   3 group(s), 6 finding(s), ~1.8k tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~605 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)
    web                            2 finding(s)  ~602 tokens  3000 chars/finding max
      - review PR #5  web/e.go  (finding 15)
      - review PR #6  web/f.go  (finding 16)
    api/y                          2 finding(s)  ~603 tokens  3000 chars/finding max
      - review PR #3  api/y/c.go  (finding 13)
      - review PR #4  api/y/d.go  (finding 14)

distill plan — 3 groups: 1 read, 2 failed (retried next run); 1 restated something already pinned
agent traffic: ~1.8k tokens sent, ~66 received (estimated), <dur>

proposed pins — distilled from review findings, awaiting YOUR decision

  p1    api-x-rule                         api/x                      2 findings cited [custom/v6]
        Keep the api x handlers aligned.

`+fmt.Sprintf(goldenDecide, 1), stdout)
	assert.Equal(t, `distilling 2 findings (api/x, 04c79745da3ca579)
  skipped pooled-state-reset — restates pooled-state-reset
  <dur>, ~605 tokens sent / ~58 back, 1 proposal(s)
distilling 2 findings (web, 49090c39ddcbba17)
warn: group 49090c39ddcbba17: agent custom: rate limited (exit status 1)
distilling 2 findings (api/y, cfd8f2c7e78a7980)
warn: group cfd8f2c7e78a7980: reply is not the requested JSON: invalid character 'I' looking for beginning of value
`, stderr)

	// The failed groups stay unmarked, so the next run pays for them
	// again; the recorded group is skipped.
	stdout, stderr = goldenRun(t, root, "lessons", "--distill")
	assert.Equal(t, goldenHead+`  payload   2 group(s), 4 finding(s), ~1.2k tokens
`+goldenDisclosure+`    web                            2 finding(s)  ~602 tokens  3000 chars/finding max
      - review PR #5  web/e.go  (finding 15)
      - review PR #6  web/f.go  (finding 16)
    api/y                          2 finding(s)  ~603 tokens  3000 chars/finding max
      - review PR #3  api/y/c.go  (finding 13)
      - review PR #4  api/y/d.go  (finding 14)

distill plan — 3 groups: 0 read, 1 already distilled, 2 failed (retried next run)
agent traffic: ~1.2k tokens sent, ~8 received (estimated), <dur>

proposed pins — distilled from review findings, awaiting YOUR decision

  p1    api-x-rule                         api/x                      2 findings cited [custom/v6]
        Keep the api x handlers aligned.

`+fmt.Sprintf(goldenDecide, 1), stdout)
	assert.Equal(t, `distilling 2 findings (web, 49090c39ddcbba17)
warn: group 49090c39ddcbba17: agent custom: rate limited (exit status 1)
distilling 2 findings (api/y, cfd8f2c7e78a7980)
warn: group cfd8f2c7e78a7980: reply is not the requested JSON: invalid character 'I' looking for beginning of value
`, stderr)
}

func TestLessonsDistillGoldenStaleCleanup(t *testing.T) {
	// api/x and web only. A stale pending proposal cites api/x, and a
	// dismissed proposal elsewhere is decision memory.
	root := goldenWorkspace(t, []model.Finding{goldenFindings[0], goldenFindings[1], goldenFindings[4], goldenFindings[5]})
	goldenSeedProposals(t, root,
		model.Proposal{Signature: "gone", Rule: "stale-pending", Note: "Stale guidance.",
			Members: []int64{11, 12}, Status: model.ProposalProposed},
		model.Proposal{Signature: "old-dismissed", Rule: "dismissed-memory", Note: "A remembered no.",
			Members: []int64{90, 91}, Status: model.ProposalDismissed},
	)

	// The dry run models the prune, so the stale proposal no longer covers
	// api/x. It plans the same single group as the real run below. It also
	// names the row that the real run deletes.
	stdout, stderr := goldenRun(t, root, "lessons", "--distill", "--dry-run", "--limit", "1")
	assert.Equal(t, goldenHead+`  payload   1 group(s), 2 finding(s), ~605 tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~605 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)

(dry run — nothing was sent; 2 group(s) remain unread; drop --dry-run to distill)
(a real run first prunes 1 stale pending proposal(s) whose evidence group is gone: p1)
`, stdout)
	assert.Empty(t, stderr)

	// The dry run wrote nothing: the stale row is still pending.
	before, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	stillPending, err := before.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.NoError(t, before.Close())
	require.Len(t, stillPending, 1)
	assert.Equal(t, "stale-pending", stillPending[0].Rule)

	// The real run prunes first, so api/x is uncovered again and wins
	// the single slot by signature order.
	stdout, stderr = goldenRun(t, root, "lessons", "--distill", "--limit", "1")
	assert.Equal(t, goldenHead+`  payload   1 group(s), 2 finding(s), ~605 tokens
`+goldenDisclosure+`    api/x                          2 finding(s)  ~605 tokens  3000 chars/finding max
      - review PR #1  api/x/a.go  (finding 11)
      - review PR #2  api/x/b.go  (finding 12)

distill plan — 2 groups: 1 read, 1 left for another run (raise --limit or drop --region); 1 stale proposals pruned
agent traffic: ~605 tokens sent, ~58 received (estimated), <dur>

proposed pins — distilled from review findings, awaiting YOUR decision

  p4    api-x-rule                         api/x                      2 findings cited [custom/v6]
        Keep the api x handlers aligned.

  p3    pooled-state-reset                 api/x                      2 findings cited [custom/v6]
        Reset pooled state before reuse.

`+fmt.Sprintf(goldenDecide, 4), stdout)
	assert.Equal(t, `distilling 2 findings (api/x, 04c79745da3ca579)
  <dur>, ~605 tokens sent / ~58 back, 2 proposal(s)
`, stderr)

	// The prune removed only the pending row; the dismissal stays.
	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	dismissed, err := st.Proposals(model.ProposalDismissed)
	require.NoError(t, err)
	require.Len(t, dismissed, 1)
	assert.Equal(t, "dismissed-memory", dismissed[0].Rule)
}

func TestLessonsDistillGoldenEmptyCorpus(t *testing.T) {
	root := goldenWorkspace(t, nil)

	// An empty corpus still prints the "already distilled" payload line.
	// The console tells these cases apart instead.
	stdout, stderr := goldenRun(t, root, "lessons", "--distill", "--dry-run")
	assert.Equal(t, goldenHead+`  payload   nothing — every evidence group has already been distilled

(dry run — nothing was sent; 0 group(s) remain unread; drop --dry-run to distill)
`, stdout)
	assert.Empty(t, stderr)

	emptyPlan := goldenHead + `  payload   nothing — every evidence group has already been distilled
distill plan — 0 groups: 0 read

no proposals pending — nothing recurs that the mined lessons miss
`

	stdout, stderr = goldenRun(t, root, "lessons", "--distill")
	assert.Equal(t, emptyPlan, stdout)
	assert.Empty(t, stderr)

	// A malformed pin file warns and the run continues without pins.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte("pin: [\n"), 0o644))

	stdout, stderr = goldenRun(t, root, "lessons", "--distill")
	assert.Equal(t, emptyPlan, stdout)
	assert.Equal(t, "warn: .seamark/lessons.yaml ignored (lessons config: yaml: line 1: did not find expected node content)"+
		" — distilling without your pins, so patterns you already pinned may be proposed again\n", stderr)
}

func TestLessonsDistillLimitFlagIsUnchanged(t *testing.T) {
	lessons, _, err := New().Find([]string{"lessons"})
	require.NoError(t, err)

	limit := lessons.Flags().Lookup("limit")
	require.NotNil(t, limit)
	assert.Equal(t, "10", limit.DefValue, "the shared default keeps the historical value")
	assert.Equal(t, "max new groups one --distill run sends to the agent (0 = all)", limit.Usage)
}
