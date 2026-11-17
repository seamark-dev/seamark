package cli

// Tests of the lesson-check keys through the commands that write the
// pin file or the ledger. Each writer keeps the mode and the check of a
// reviewed pin. A state import never writes lessons.yaml, and a
// distillation and its apply never write a mode or a check, so a new
// pin stays advisory. A retarget of an executable pin shows the checker
// impact before the apply.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestLessonsCheckImpactPreviews covers the installed capability of the
// CLI retarget review before the command can write the candidate.
func TestLessonsCheckImpactPreviews(t *testing.T) {
	for _, installation := range []string{"absent", "unverified", "binary unavailable"} {
		t.Run("retarget/"+installation, func(t *testing.T) {
			root, sentinel := confirmChecksWorkspace(t, true)

			if installation != "absent" {
				binary := capabilitySeamark(t)
				plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
					Root: root, Binary: binary, Clients: []integration.ClientSetup{{
						ClientID: integration.ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce,
					}},
				})
				require.NoError(t, err)
				_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
				require.NoError(t, err)
				if installation == "binary unavailable" {
					require.NoError(t, os.Remove(binary))
				}
			}

			before, _ := maintainLessons(t, root)
			ledger := maintainLedger(t, root, 0)
			out, _, err := runErr(t, "-C", root, "lessons", "--retarget", "p1")
			require.ErrorContains(t, err, "--confirm-checks")
			assert.Contains(t, out, "scope repository-wide -> scripts")

			switch installation {
			case "absent":
				assert.Contains(t, out, "active: "+lessoncheck.ActiveRequested)
			case "unverified":
				assert.Contains(t, out, activeEnforceBlock)
			case "binary unavailable":
				assert.Contains(t, out, "active: requested; inactive:")
				assert.Contains(t, out, "binary-missing")
			}

			after, _ := maintainLessons(t, root)
			assert.Equal(t, before, after)
			assert.Equal(t, ledger, maintainLedger(t, root, 0))
			sentinel.requireNoCall(t)
		})
	}
}

// TestLessonsCheckImpactCandidate proves the join reads candidate bytes
// rather than the installed file or the old pin reference.
func TestLessonsCheckImpactCandidate(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteLessons(t, root, proposalsChecksLessons, 0o644)
	capabilityInstall(t, root, integration.ClientSetup{ClientID: integration.ClaudeID, LessonChecks: hooks.LessonChecksEnforce})
	deps := maintain.Deps{Root: root, Reg: integration.Builtin()}

	for _, tc := range []struct {
		name, after, want string
	}{
		{"check mode and new scope", strings.ReplaceAll(strings.ReplaceAll(proposalsChecksLessons, "mode: enforce", "mode: check"), "region: internal/store", "region: scripts"), "claude checked"},
		{"advisory", strings.ReplaceAll(proposalsChecksLessons, "mode: enforce", "mode: advisory"), lessoncheck.ActiveAdvisory},
		{"invalid binding", strings.ReplaceAll(proposalsChecksLessons, "check: frozen-assets", "check: undefined"), "configuration-error"},
		{"malformed", "pin: [", lessoncheck.ActiveRequested},
		{"no candidate", "", lessoncheck.ActiveRequested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &maintain.Plan{
				Change: &pinfile.Change{After: []byte(tc.after)},
				Impact: []lessoncheck.Impact{{Entry: 1, Active: lessoncheck.ActiveRequested}},
			}
			joinCheckImpact(p, deps)
			assert.Contains(t, p.Impact[0].Active, tc.want)
			got, _ := maintainLessons(t, root)
			assert.Equal(t, proposalsChecksLessons, got)
		})
	}
}

// writersChecksLessons is the example check configuration with one
// more, advisory pin and a tuning value. Every command below must keep
// it byte for byte.
const writersChecksLessons = `threshold: 3
lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: hand-written
    region: web
    note: A person wrote this pin.
    check: no-todo
`

// writersPinChecks returns "rule mode check" for each pin of text, in
// file order, with the normalized mode. It requires that text passes the
// strict check tier, so a refused write cannot pass as a kept key.
func writersPinChecks(t *testing.T, text string) []string {
	t.Helper()

	cfg, errs, err := lessoncheck.Parse([]byte(text))
	require.NoError(t, err)
	require.Empty(t, errs)

	out := make([]string, 0, len(cfg.Pins))
	for _, p := range cfg.Pins {
		out = append(out, p.Rule.Rule+" "+string(p.Mode)+" "+p.CheckID)
	}

	return out
}

// TestStateImportLeavesLessonsYAML proves that a state export and
// import never write lessons.yaml. The bundle holds proposal rows and
// distillation marks, never lessons.yaml. So an import into another
// workspace and into the source workspace leaves the pin file with its
// checks byte for byte, with its mode bits.
func TestStateImportLeavesLessonsYAML(t *testing.T) {
	src := maintainWorkspace(t, nil)
	maintainWriteLessons(t, src, writersChecksLessons, 0o640)
	goldenSeedProposals(t, src,
		model.Proposal{
			Signature: "s1", Rule: "frozen-assets", Region: "generated",
			Note:    "Changes to these files require a maintainer-managed update.",
			Members: []int64{1, 2}, Agent: "claude/v6", Status: model.ProposalApplied,
		},
		applyGoldenRow("s2", "pending-rule", "api", "A pending pin."),
	)

	bundle := filepath.Join(t.TempDir(), "state.json")
	_, err := run(t, "-C", src, "state", "export", "--out", bundle)
	require.NoError(t, err)

	data, err := os.ReadFile(bundle)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "lesson_checks_version", "the bundle holds no lessons.yaml")
	assert.NotContains(t, string(data), "forbid-path-change/1", "the bundle holds no check definition")

	t.Run("into another workspace", func(t *testing.T) {
		dst := maintainWorkspace(t, nil)
		maintainWriteLessons(t, dst, writersChecksLessons, 0o640)

		out, err := run(t, "-C", dst, "state", "import", bundle)
		require.NoError(t, err)
		assert.Contains(t, out, "proposals: 2 added", "the import writes the ledger")

		got, mode := maintainLessons(t, dst)
		assert.Equal(t, writersChecksLessons, got)
		assert.Equal(t, os.FileMode(0o640), mode)
	})

	t.Run("into the source workspace", func(t *testing.T) {
		_, err := run(t, "-C", src, "state", "import", bundle)
		require.NoError(t, err)

		got, mode := maintainLessons(t, src)
		assert.Equal(t, writersChecksLessons, got)
		assert.Equal(t, os.FileMode(0o640), mode)
	})
}

// TestLessonsDistillNeverWritesMode proves through the commands that a
// new pin stays advisory. A distillation writes only the ledger, so
// lessons.yaml stays. The apply of its proposals adds pins without a
// mode key and a check key, so the new pins are advisory. The enforced
// pin and the checks map stay byte for byte. The agent is goldenAgent,
// a shell script, so no model runs.
func TestLessonsDistillNeverWritesMode(t *testing.T) {
	root := goldenWorkspace(t, goldenFindings)
	maintainWriteLessons(t, root, writersChecksLessons, 0o644)

	config := filepath.Join(root, ".seamark", "config.yaml")
	agent, err := os.ReadFile(config)
	require.NoError(t, err)
	maintainWriteConfig(t, root, string(agent)+maintainGateOn)

	stdout, _ := goldenRun(t, root, "lessons", "--distill", "--region", "./api", "--limit", "1")
	assert.Contains(t, stdout, "  p1    pooled-state-reset ")
	assert.Contains(t, stdout, "  p2    api-x-rule ")

	got, _ := maintainLessons(t, root)
	assert.Equal(t, writersChecksLessons, got, "a distillation writes no pin")

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1,p2")
	assert.Equal(t, "applied 2 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p1    pooled-state-reset\n"+
		"  p2    api-x-rule\n", stdout)
	assert.Empty(t, stderr)

	const block = `  # distilled by custom/v6 from 2 findings (seamark lessons --distill, p1)
  - rule: pooled-state-reset
    region: api/x
    note: Reset pooled state before reuse.
  # distilled by custom/v6 from 2 findings (seamark lessons --distill, p2)
  - rule: api-x-rule
    region: api/x
    note: Keep the api x handlers aligned.
`

	got, _ = maintainLessons(t, root)
	require.Equal(t, 1, strings.Count(writersChecksLessons, "pin:\n"))
	assert.Equal(t, strings.Replace(writersChecksLessons, "pin:\n", "pin:\n"+block, 1), got)
	assert.Equal(t, []string{
		"pooled-state-reset advisory ",
		"api-x-rule advisory ",
		"frozen-assets enforce frozen-assets",
		"hand-written advisory no-todo",
	}, writersPinChecks(t, got))
}

// confirmChecksLessons holds an enforced pin with region '*', which a
// retarget moves to [scripts], and an advisory pin, which a retarget
// moves to [api, db] (the evidence of retargetGoldenFindings).
const confirmChecksLessons = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: guard-scripts
    region: '*'
    note: Guard the scripts.
    mode: enforce
    check: no-todo
  - rule: two-areas
    region: api
    note: Two areas.
`

// confirmChecksMoved is confirmChecksLessons after the retarget of both
// pins. The mode and the check of the enforced pin stay.
const confirmChecksMoved = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: guard-scripts
    region: scripts
    note: Guard the scripts.
    mode: enforce
    check: no-todo
  - rule: two-areas
    region: api
    regions: [api, db]
    note: Two areas.
`

// confirmChecksWorkspace seeds confirmChecksLessons, the applied rows p1
// (the enforced pin) and p2 (the advisory pin), and the write gate. It
// installs the invoker sentinel after the config, because the sentinel
// adds its agent section to config.yaml.
func confirmChecksWorkspace(t *testing.T, gateOn bool) (string, *invokerSentinel) {
	t.Helper()

	root := maintainWorkspace(t, retargetGoldenFindings)
	maintainWriteLessons(t, root, confirmChecksLessons, 0o600)

	if gateOn {
		maintainWriteConfig(t, root, maintainGateOn)
	}

	goldenSeedProposals(t, root,
		model.Proposal{Signature: "s1", Rule: "guard-scripts", Note: "Guard the scripts.",
			Members: []int64{1, 2}, Agent: "claude/v6", Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "two-areas", Region: "api", Note: "Two areas.",
			Members: []int64{3, 4}, Agent: "claude/v6", Status: model.ProposalApplied},
	)

	return root, newInvokerSentinel(t, root)
}

// confirmChecksBlock is the checker impact block of the retarget of the
// enforced pin, as the CLI prints it: the five lines and an empty line.
func confirmChecksBlock(t *testing.T) string {
	t.Helper()

	before, err := reviews.ParseConfig([]byte(confirmChecksLessons))
	require.NoError(t, err)

	after, err := reviews.ParseConfig([]byte(confirmChecksMoved))
	require.NoError(t, err)

	old := before.Pin[0]
	ref := before.ShortPinRef(reviews.NewPinKey(old.Rule, old.Region, old.Regions))

	return "pin " + ref + " guard-scripts: check no-todo (no-added-pattern/1, " +
		reviews.CheckVersion(before.Checks["no-todo"])[:12] + "), mode enforce\n" +
		"  scope repository-wide -> scripts\n" +
		"  predicate: violation if a line that a change adds in scope matches the Go regular expression " +
		"'TODO\\('; a new file adds every line; binary or unreadable content gives unknown\n" +
		"  version " + before.PinVersion(old)[:12] + " -> " + after.PinVersion(after.Pin[0])[:12] + "\n" +
		"  active: requested; inactive: not-installed\n\n"
}

// TestLessonsRetargetNeedsConfirmChecks proves the --confirm-checks
// rule. With the gate on, a retarget that moves the regions of an
// executable pin prints the impact block, refuses with exit code 1, and
// writes nothing. The rerun with --confirm-checks applies and keeps the
// mode and the check. With the gate off, the block comes before the
// lines to change by hand. A retarget of advisory pins keeps its bytes.
// No path calls a model.
func TestLessonsRetargetNeedsConfirmChecks(t *testing.T) {
	t.Run("the gate on refuses without the flag", func(t *testing.T) {
		root, sentinel := confirmChecksWorkspace(t, true)
		ledger := maintainLedger(t, root, 0)

		out, errOut, err := runErr(t, "-C", root, "lessons", "--retarget", "p1..p2")
		require.Error(t, err)
		assert.NotErrorIs(t, err, gate.ErrBlocked, "a refusal exits 1, not 2")
		assert.NotErrorIs(t, err, lessoncheck.ErrViolation, "a refusal exits 1, not 2")
		assert.Empty(t, errOut)

		block := confirmChecksBlock(t)
		ref := strings.Fields(block)[1]

		assert.Equal(t, block, goldenNormalize(root, out), "the block comes first, and nothing else prints")
		assert.Equal(t, "lessons --retarget: pin "+ref+" is executable (mode enforce, check no-todo); review the "+
			"checker impact above, then rerun: seamark lessons --retarget p1,p2 --confirm-checks", err.Error())

		content, mode := maintainLessons(t, root)
		assert.Equal(t, confirmChecksLessons, content, "nothing is written")
		assert.Equal(t, os.FileMode(0o600), mode)
		assert.Equal(t, ledger, maintainLedger(t, root, 0), "the ledger stays")

		sentinel.requireNoCall(t)
	})

	t.Run("the rerun with the flag applies", func(t *testing.T) {
		root, sentinel := confirmChecksWorkspace(t, true)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1..p2", "--confirm-checks")
		assert.Equal(t, "  p1    guard-scripts → scripts\n"+
			"  p2    two-areas → api, db\n"+
			"retargeted 2 pin(s) — review the lessons.yaml diff and commit it\n", stdout)
		assert.Empty(t, stderr)

		content, mode := maintainLessons(t, root)
		assert.Equal(t, confirmChecksMoved, content)
		assert.Equal(t, os.FileMode(0o600), mode)
		assert.Equal(t, []string{"guard-scripts enforce no-todo", "two-areas advisory "}, writersPinChecks(t, content))
		assert.Equal(t, model.ProposalApplied, stored(t, root, 1).Status)
		assert.Equal(t, []string{"scripts"}, stored(t, root, 1).Regions, "the ledger moves with the file")

		sentinel.requireNoCall(t)
	})

	t.Run("the gate off prints the block before the hand edit", func(t *testing.T) {
		root, sentinel := confirmChecksWorkspace(t, false)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1")
		assert.Empty(t, stderr)
		assert.True(t, strings.HasPrefix(stdout, confirmChecksBlock(t)+
			"distill.write is off in .seamark/config.yaml — change the regions of these pins in "+
			".seamark/lessons.yaml by hand:\n\n  p1    guard-scripts → scripts\n"), stdout)
		assert.True(t, strings.HasSuffix(stdout, "\n(nothing was changed; enable distill.write to let retarget "+
			"edit the file and the ledger together)\n"), stdout)

		content, _ := maintainLessons(t, root)
		assert.Equal(t, confirmChecksLessons, content)

		sentinel.requireNoCall(t)
	})

	t.Run("an advisory pin needs no flag", func(t *testing.T) {
		root, sentinel := confirmChecksWorkspace(t, true)

		stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p2")
		assert.Equal(t, "  p2    two-areas → api, db\n"+
			"retargeted 1 pin(s) — review the lessons.yaml diff and commit it\n", stdout, "no block")
		assert.Empty(t, stderr)

		content, _ := maintainLessons(t, root)
		assert.Contains(t, content, "    region: api\n    regions: [api, db]\n")

		sentinel.requireNoCall(t)
	})

	t.Run("the flag applies to --retarget only", func(t *testing.T) {
		root, sentinel := confirmChecksWorkspace(t, true)

		for _, args := range [][]string{
			{"lessons", "--confirm-checks"},
			{"lessons", "--prune", "p1", "--confirm-checks"},
			{"lessons", "--proposals", "--confirm-checks"},
		} {
			stdout, _, errText := maintainRunErr(t, root, args...)
			assert.Empty(t, stdout, "%v", args)
			assert.Equal(t, "--confirm-checks applies to --retarget only", errText, "%v", args)
		}

		content, _ := maintainLessons(t, root)
		assert.Equal(t, confirmChecksLessons, content)

		sentinel.requireNoCall(t)
	})
}

// proposalsChecksLessons holds an enforced pin with the provenance
// comment of its applied row p1, and a hand-written advisory pin.
const proposalsChecksLessons = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)
  - rule: wrap-sql-errors
    region: internal/store
    note: Wrap SQL errors with the query name.
    mode: enforce
    check: frozen-assets
  - rule: hand-written
    region: web
    note: Keep this one.
`
