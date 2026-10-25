package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the lesson-check services (contracts K1, K5, K10)
// run the production engine over temporary git workspaces. Only the
// agent invoker is a fake, and no test may call it (AC-1).

// checksLessons holds one enforce pin on generated and its check.
const checksLessons = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen
    region: generated
    note: Do not edit generated files.
    mode: enforce
    check: frozen
`

// checksWorkspace returns a git workspace with one commit of a file in
// generated and checksLessons. Git ignores .seamark/, as the scaffold
// .gitignore does, so the audit stream is never a change.
func checksWorkspace(t *testing.T) workspace.Workspace {
	t.Helper()

	root := t.TempDir()
	servicestest.Git(t, root, "init", "-b", "main")

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	servicestest.Write(t, ws, ".git/info/exclude", ".seamark/\n")
	servicestest.Write(t, ws, "generated/a.txt", "a\n")
	servicestest.Git(t, root, "add", "-A")
	servicestest.Git(t, root, "commit", "-m", "base")
	servicestest.Write(t, ws, pinfile.File, checksLessons)

	return ws
}

// auditRecords returns the records of the lesson-check audit stream.
// The writer rotates the live log by size and by the age of its oldest
// record. A rotation moves the earlier records of a test into the
// rotated generation, so the helper reads both files, oldest first.
func auditRecords(t *testing.T, ws workspace.Workspace) []lessoncheck.AuditRecord {
	t.Helper()

	live := filepath.Join(ws.Root, ".seamark", "lesson-check-audit.jsonl")

	var out []lessoncheck.AuditRecord

	for _, path := range []string{live + ".1", live} {
		data, err := os.ReadFile(path)
		if path != live && errors.Is(err, os.ErrNotExist) {
			continue
		}

		require.NoError(t, err)

		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			var rec lessoncheck.AuditRecord
			require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
			out = append(out, rec)
		}

		require.NoError(t, sc.Err())
	}

	return out
}

// TestServicesVerifyLessonsUsesCallerTUI proves the console part of
// AC-35: the console verifies with the one service of the CLI, names the
// caller tui in the result and in the K9 audit record, and revalidates a
// shown result with lessoncheck.Revalidate. It calls no model (AC-1).
func TestServicesVerifyLessonsUsesCallerTUI(t *testing.T) {
	ws := checksWorkspace(t)
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})

	servicestest.Write(t, ws, "generated/a.txt", "edited\n")

	v, err := e.VerifyLessons(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, lessoncheck.CallerTUI, v.Caller)
	assert.Equal(t, "test", v.SeamarkVersion, "the console names its own version")
	assert.Equal(t, servicestest.Now.UTC().Format("2006-01-02T15:04:05Z07:00"), v.CheckedAt, "the engine clock")
	assert.Equal(t, lessoncheck.FreshnessFresh, v.Freshness)
	assert.Equal(t, lessoncheck.SummaryViolation, v.Summary.State, "the edit in generated breaks the enforce pin")

	records := auditRecords(t, ws)
	require.Len(t, records, 1, "one verify record per verification")
	assert.Equal(t, lessoncheck.AuditVerify, records[0].Kind)
	assert.Equal(t, lessoncheck.CallerTUI, records[0].Caller)
	assert.Equal(t, v.Summary.State, records[0].State)

	// An explicit selection is the --file selection of the CLI.
	explicit, err := e.VerifyLessons(context.Background(), []string{"generated"})
	require.NoError(t, err)
	assert.Equal(t, []string{"generated"}, explicit.Selection.Paths)

	// The revalidation writes nothing: no second audit record.
	got, err := e.RevalidateLessons(context.Background(), v)
	require.NoError(t, err)
	assert.Equal(t, lessoncheck.FreshnessFresh, got)
	assert.Len(t, auditRecords(t, ws), 2, "only the two verifications wrote records")

	servicestest.Write(t, ws, "generated/b.txt", "new\n")

	got, err = e.RevalidateLessons(context.Background(), v)
	require.NoError(t, err)
	assert.Equal(t, lessoncheck.FreshnessStale, got, "a new file in the selection makes the shown result stale")

	// The capability report reads the inspections of the console and the
	// same lessons.yaml. No hook is installed, so the pin is inactive.
	report := e.LessonCapability(e.Integrations())
	assert.Equal(t, "lesson-capability/1", report.Schema)
	require.Len(t, report.Pins, 1)
	assert.Equal(t, lessoncheck.ModeEnforce, report.Pins[0].Requested)
	assert.Equal(t, "requested; inactive: not-installed", report.ActiveText(report.Pins[0].PinRef))

	assert.Zero(t, agent.Count(), "no service of the area calls a model")
}

// installClaudeGuard installs the Claude Code enforce hooks on the
// temporary root of ws, with a link named seamark to the test binary as
// the hook binary. The setup engine runs in the test process, and no
// hook runs.
func installClaudeGuard(t *testing.T, ws workspace.Workspace) string {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(exe, link))

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
		Root: ws.Root, Binary: link,
		Clients: []integration.ClientSetup{{ClientID: integration.ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce}},
	})
	require.NoError(t, err)

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)

	return link
}

// TestServicesCheckImpactPreviews joins installed hooks on the retarget
// candidate, whose new identity must not lose the requested protection.
func TestServicesCheckImpactPreviews(t *testing.T) {
	for _, installation := range []string{"absent", "unverified", "binary unavailable"} {
		t.Run("retarget/"+installation, func(t *testing.T) {
			ws, _ := maintWorkspace(t, true)
			lessons := "lesson_checks_version: 1\nchecks:\n  frozen:\n    kind: forbid-path-change/1\n" +
				strings.Replace(maintPins, "    note: Close the handle on every error path.\n",
					"    note: Close the handle on every error path.\n    mode: enforce\n    check: frozen\n", 1)
			servicestest.Write(t, ws, pinfile.File, lessons)

			if installation != "absent" {
				binary := installClaudeGuard(t, ws)
				if installation == "binary unavailable" {
					require.NoError(t, os.Remove(binary))
				}
			}

			agent := &servicestest.Agent{}
			e := engNew(ws, Deps{Invoke: agent})

			st, err := store.Open(ws.DBPath)
			require.NoError(t, err)
			require.NoError(t, st.ReplaceLessons(nil, []model.Finding{
				{ID: 21, Path: "scripts/a.go"}, {ID: 22, Path: "scripts/b.go"},
			}))
			require.NoError(t, st.Close())
			req := maintPrune(t, e)
			req.Action = maintain.ActionRetarget
			before := servicestest.Tree(t, ws.Root)
			p, err := e.PlanMaintenance(req)
			assert.Equal(t, before, servicestest.Tree(t, ws.Root), "preview writes nothing")

			require.NoError(t, err)
			require.True(t, p.Confirmable, "%v", p.Blocked)
			require.NotNil(t, p.Change)
			require.Len(t, p.Impact, 1)
			cfg, errs, err := lessoncheck.Parse(p.Change.After)
			require.NoError(t, err)
			require.Empty(t, errs)
			report := integration.LessonCapabilityOf(e.Integrations(), cfg, errs, e.self())
			active := report.ActiveText(cfg.Pins[0].Ref)
			assert.Equal(t, active, p.Impact[0].Active)
			assert.Equal(t, lessoncheck.ModeEnforce, p.Impact[0].Mode)
			assert.Equal(t, "frozen", p.Impact[0].Check.ID)

			switch installation {
			case "absent":
				assert.Equal(t, lessoncheck.ActiveRequested, active)
			case "unverified":
				assert.Contains(t, active, "claude enforced-unverified")
			case "binary unavailable":
				assert.Contains(t, active, "inactive:")
				assert.Contains(t, active, "binary-missing")
			}

			assert.Equal(t, []string{"scripts"}, p.Impact[0].ScopeAfter)
			assert.Equal(t, lessons, readPins(t, ws))
			assert.Zero(t, agent.Count())
		})
	}
}

// TestServicesCheckImpactCandidate keeps capability tied to the candidate
// mode and check even when the installed file still requests enforcement.
func TestServicesCheckImpactCandidate(t *testing.T) {
	ws := checksWorkspace(t)
	installClaudeGuard(t, ws)
	e := engNew(ws, Deps{})

	for _, tc := range []struct {
		name, after, want string
	}{
		{"check mode and new scope", strings.ReplaceAll(strings.ReplaceAll(checksLessons, "mode: enforce", "mode: check"), "region: generated", "region: scripts"), "claude checked"},
		{"advisory", strings.ReplaceAll(checksLessons, "mode: enforce", "mode: advisory"), lessoncheck.ActiveAdvisory},
		{"invalid binding", strings.ReplaceAll(checksLessons, "check: frozen", "check: undefined"), "configuration-error"},
		{"malformed", "pin: [", lessoncheck.ActiveRequested},
		{"no candidate", "", lessoncheck.ActiveRequested},
	} {
		t.Run(tc.name, func(t *testing.T) {
			impacts := []lessoncheck.Impact{{Entry: 1, Active: lessoncheck.ActiveRequested}}
			e.joinActive(impacts, []byte(tc.after))
			assert.Contains(t, impacts[0].Active, tc.want)
			assert.Equal(t, checksLessons, readPins(t, ws))
		})
	}
}

// TestServicesCheckDefinitionTakesEngineLock proves that the definition
// commit is a console mutation: it takes the engine lock with TryLock,
// fails with ErrBusy while another mutation runs, and writes nothing
// then. The plan writes nothing and takes no lock, as the pin authoring
// plan does. The impact block gets the K10 active state of the
// installed hooks, not the fixed text of milestone P1.
func TestServicesCheckDefinitionTakesEngineLock(t *testing.T) {
	ws := checksWorkspace(t)
	servicestest.Write(t, ws, ".seamark/config.yaml", "distill:\n  write: true\n")
	installClaudeGuard(t, ws)

	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})
	req := maintain.CheckDefRequest{ID: "frozen", Kind: "no-added-pattern/1", Pattern: "GENERATED"}

	e.mu.Lock()
	p, err := e.PlanCheckDef(req)
	e.mu.Unlock()

	require.NoError(t, err, "the plan takes no lock")
	require.True(t, p.Confirmable, "blocked: %v", p.Blockers)
	require.Len(t, p.Impact, 1, "the enforce pin binds the edited definition")
	assert.Equal(t, "requested; claude enforced-unverified, codex requested-inactive", p.Impact[0].Active)

	e.mu.Lock()
	res, err := e.CommitCheckDef(p)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, maintain.NotWritten, res.Outcome)
	assert.Equal(t, checksLessons, readPins(t, ws), "a busy commit writes nothing")

	res, err = e.CommitCheckDef(nil)
	require.ErrorIs(t, err, errNoPlan)
	assert.Equal(t, maintain.NotWritten, res.Outcome)

	res, err = e.CommitCheckDef(p)
	require.NoError(t, err)
	require.NoError(t, res.Err)
	assert.Equal(t, maintain.Written, res.Outcome)
	assert.Equal(t, strings.Replace(checksLessons, "    kind: forbid-path-change/1\n",
		"    kind: no-added-pattern/1\n    pattern: GENERATED\n", 1), readPins(t, ws))
	engUnlocked(t, e)

	assert.Zero(t, agent.Count())
}

// readPins returns the text of the pin file of ws.
func readPins(t *testing.T, ws workspace.Workspace) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(pinfile.File)))
	require.NoError(t, err)

	return string(data)
}
