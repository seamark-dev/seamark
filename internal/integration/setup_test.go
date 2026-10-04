package integration

// Coordinator tests. They use the real built-in adapters where native
// semantics matter and small test adapters where only the coordinator's
// own rules are under test. Everything runs offline in a temp tree.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/skills"
)

const testBinary = "/usr/local/bin/seamark"

// writeRel writes one repository-relative file, creating its parents.
func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()

	abs := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
}

// readRel reads one repository-relative file.
func readRel(t *testing.T, root, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)

	return string(data)
}

// treeFiles lists every file under root, slash-separated and sorted by
// the walk, so a test can assert that nothing was written.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()

	var files []string

	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, filepath.ToSlash(rel))
		}

		return nil
	}))

	return files
}

// docSetup is a test adapter that owns one document with fixed content.
// It follows the adapter contract: read under a guard, then keep or
// write.
type docSetup struct{ path, body string }

func (docSetup) Inspect(string) Inspection { return Inspection{} }

// ManagedGateMode is empty: the adapter installs no gate hook.
func (docSetup) ManagedGateMode(string) string { return "" }

func (d docSetup) Plan(root, _ string, _ ClientSetup) (ClientPlan, error) {
	guard, existing, err := ReadGuarded(root, d.path)
	if err != nil {
		return ClientPlan{}, err
	}

	plan := ClientPlan{Reads: []FileGuard{guard}}

	if guard.Exists && string(existing) == d.body {
		plan.Kept = []FileKeep{{Path: d.path, Detail: "current"}}
	} else {
		plan.Writes = []FileWrite{{Path: d.path, After: []byte(d.body), Detail: "test document"}}
	}

	return plan, nil
}

// docClient registers a docSetup under an ID.
func docClient(id, path, body string) Client {
	return Client{
		ID: id, Name: strings.ToUpper(id),
		Setup: docSetup{path: path, body: body}, SetupOps: SetupSupport{RegisterMCP: true},
	}
}

// fullIntent asks one client for everything.
func fullIntent(id string) ClientSetup {
	return ClientSetup{ClientID: id, Skills: true, Hooks: true, RegisterMCP: true, ApproveTools: true}
}

// scaffold is a common document that is created once and never changed.
func scaffold(rel, body string) Document {
	return Document{
		Path: rel, Detail: "starter", KeptDetail: "already present",
		Compose: func(existing []byte, exists bool) ([]byte, error) {
			if exists {
				return existing, nil
			}

			return []byte(body), nil
		},
	}
}

func mustPlan(t *testing.T, reg *Registry, req SetupRequest) *SetupPlan {
	t.Helper()

	plan, err := PlanSetup(reg, req)
	require.NoError(t, err)
	assert.Empty(t, repeatedPathReasons(plan.Findings), "a finding names its path once")

	return plan
}

func TestPlanAndPreviewWriteNothing(t *testing.T) {
	root := t.TempDir()

	plan := mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary,
		Clients: []ClientSetup{fullIntent(CodexID), fullIntent(ClaudeID)},
		Common:  []Document{scaffold(".seamark/config.yaml", "index: {}\n")},
	})

	assert.Empty(t, treeFiles(t, root), "planning is read-only")

	var (
		seen []string
		log  bytes.Buffer
	)

	result, err := ApplySetup(plan, ApplyOptions{
		Preview: true,
		Observe: func(op OpResult) {
			if op.Kind == OpDocument {
				seen = append(seen, op.Path+" "+op.Status.String())
			}
		},
		SkillsLog: &log,
	})
	require.NoError(t, err)

	assert.Empty(t, treeFiles(t, root), "a preview is read-only")
	assert.Contains(t, log.String(), "would write")

	require.NotEmpty(t, result.Skills)

	for _, op := range result.Skills {
		assert.Equal(t, OpSkill, op.Kind)
		assert.Equal(t, OpPlanned, op.Status, op.Path)
		assert.True(t, op.Created, op.Path)
		assert.NotEmpty(t, op.Consumers, op.Path)
	}

	// Document order: common first, then each client in registry order,
	// whatever order the request named them in.
	assert.Equal(t, []string{
		".seamark/config.yaml planned",
		".claude/settings.json planned",
		".mcp.json planned",
		".codex/config.toml planned",
		".codex/hooks.json planned",
	}, seen)

	for _, op := range result.Ops {
		assert.True(t, op.Created, op.Path)
		assert.NotEmpty(t, op.Detail, "%s: a preview says what the write adds", op.Path)
		assert.NotContains(t, op.Detail, "mcpServers", "a preview never prints native configuration")
	}
}

func TestApplyConvergesAndASecondRunChangesNothing(t *testing.T) {
	root := t.TempDir()
	req := SetupRequest{
		Root: root, Binary: testBinary,
		Clients: []ClientSetup{fullIntent(ClaudeID), fullIntent(CodexID)},
		Common:  []Document{scaffold(".seamark/config.yaml", "index: {}\n")},
	}

	result, err := ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{})
	require.NoError(t, err)

	for _, op := range result.Ops {
		assert.Equal(t, OpApplied, op.Status, op.Path)
	}

	before := map[string]string{}
	for _, rel := range treeFiles(t, root) {
		before[rel] = readRel(t, root, rel)
	}

	second := mustPlan(t, Builtin(), req)
	assert.Empty(t, second.Writes, "a repeated setup plans no write")
	assert.Len(t, second.Kept, 5)

	for _, entry := range second.Skills {
		assert.Equal(t, skills.Current, entry.State, entry.Rel)
	}

	result, err = ApplySetup(second, ApplyOptions{})
	require.NoError(t, err)

	for _, op := range result.Ops {
		assert.Equal(t, OpKept, op.Status, op.Path)
	}

	for rel, body := range before {
		assert.Equal(t, body, readRel(t, root, rel), "%s must not change on a repeated run", rel)
	}

	assert.Len(t, treeFiles(t, root), len(before), "no temporary file is left behind")
}

func TestAnUnselectedClientIsNeverRead(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", "{ this is not json")

	// Explicit Codex setup: the broken Claude Code file is irrelevant.
	plan := mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{fullIntent(CodexID)},
	})

	for _, guard := range plan.Reads {
		assert.False(t, strings.HasPrefix(guard.Path, ".claude"), "unselected client read: %s", guard.Path)
	}

	_, err := ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, "{ this is not json", readRel(t, root, ".claude/settings.json"))
	assert.NoFileExists(t, filepath.Join(root, ".mcp.json"))
}

func TestAMalformedSelectedFileStopsBeforeAnyWrite(t *testing.T) {
	for rel, body := range map[string]string{
		".claude/settings.json": "{ this is not json",
		".mcp.json":             `{"mcpServers": []}`,
		".codex/config.toml":    "[mcp_servers\n",
	} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, rel, body)

			_, err := PlanSetup(Builtin(), SetupRequest{
				Root: root, Binary: testBinary,
				Clients: []ClientSetup{fullIntent(ClaudeID), fullIntent(CodexID)},
				Common:  []Document{scaffold(".seamark/config.yaml", "index: {}\n")},
			})
			require.Error(t, err)
			assert.Equal(t, []string{rel}, treeFiles(t, root), "nothing lands when preflight fails")
		})
	}
}

func TestSelectionFailsBeforeAnyRead(t *testing.T) {
	root := t.TempDir()

	_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{{ClientID: "gemini"}}})
	require.ErrorIs(t, err, ErrUnknownClient)

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{
		{ClientID: CodexID, RegisterMCP: true}, {ClientID: CodexID, ApproveTools: true},
	}})
	require.ErrorContains(t, err, "requested twice with different intent")

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{
		{ClientID: ClaudeID, Hooks: true, GateMode: "block"},
	}})
	require.ErrorContains(t, err, "gate mode must be")

	_, err = PlanSetup(Builtin(), SetupRequest{})
	require.ErrorContains(t, err, "root is empty")

	// The same intent twice is one client.
	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{
		{ClientID: CodexID, RegisterMCP: true}, {ClientID: CodexID, RegisterMCP: true},
	}})
	assert.Len(t, plan.Writes, 1)
}

func TestUnsupportedIntentsAreInformationNotErrors(t *testing.T) {
	root := t.TempDir()

	reg, err := NewRegistry(append(Builtin().Clients(), thirdClient(), invocationOnlyClient())...)
	require.NoError(t, err)

	plan := mustPlan(t, reg, SetupRequest{Root: root, Binary: testBinary, Clients: []ClientSetup{
		{ClientID: CodexID, RegisterMCP: true, GateMode: "enforce"},
		{ClientID: thirdID, Hooks: true, RegisterMCP: true},
		{ClientID: "invoker", Skills: true, ApproveTools: true},
	}})

	var reasons []string

	for _, f := range plan.Findings {
		if f.Level == FindingInfo && strings.Contains(f.Reason, "skipped") {
			reasons = append(reasons, f.Reason)
		}
	}

	assert.Equal(t, []string{
		"Fake Agent: lifecycle hooks are not supported by this integration; skipped",
		"Fake Agent: MCP registration is not supported by this integration; skipped",
		"Invoker: skills are not supported by this integration; skipped",
		"Invoker: tool grants are not supported by this integration; skipped",
	}, reasons)

	// The supported part of the Codex request is still planned.
	require.Len(t, plan.Writes, 1)
	assert.Equal(t, ".codex/config.toml", plan.Writes[0].Path)
	assert.Empty(t, plan.Skills)
}

func TestASharedSkillDestinationIsPlannedAndWrittenOnce(t *testing.T) {
	root := t.TempDir()

	reg, err := NewRegistry(append(Builtin().Clients(), thirdClient())...)
	require.NoError(t, err)

	plan := mustPlan(t, reg, SetupRequest{Root: root, Clients: []ClientSetup{
		{ClientID: thirdID, Skills: true}, {ClientID: CodexID, Skills: true},
	}})

	assert.Equal(t, []SkillDestination{{Dir: ".agents/skills", Consumers: []string{CodexID, thirdID}}}, plan.Destinations)

	names, err := skills.Names()
	require.NoError(t, err)
	require.Len(t, plan.Skills, len(names), "one entry per skill, not one per consumer")

	var log bytes.Buffer

	_, err = ApplySetup(plan, ApplyOptions{SkillsLog: &log})
	require.NoError(t, err)
	assert.Equal(t, len(names), strings.Count(log.String(), "wrote"), "each directory is written once")
	assert.NoDirExists(t, filepath.Join(root, ".claude"), "an unselected client gets no skills")
}

func TestStaleInputsAbortBeforeAnyWrite(t *testing.T) {
	request := func(root string) SetupRequest {
		return SetupRequest{
			Root: root, Binary: testBinary,
			Clients: []ClientSetup{fullIntent(ClaudeID), fullIntent(CodexID)},
		}
	}

	cases := map[string]struct {
		before func(t *testing.T, root string)
		change func(t *testing.T, root string)
	}{
		"an appended Codex file was edited": {
			before: func(t *testing.T, root string) { writeRel(t, root, ".codex/config.toml", "model = \"x\"\n") },
			change: func(t *testing.T, root string) { writeRel(t, root, ".codex/config.toml", "model = \"y\"\n") },
		},
		"a Codex header-only table moved": {
			before: func(t *testing.T, root string) {
				writeRel(t, root, ".codex/config.toml", "[mcp_servers.seamark]\n\n[other]\nk = 1\n")
			},
			change: func(t *testing.T, root string) {
				writeRel(t, root, ".codex/config.toml", "# moved\n[mcp_servers.seamark]\n\n[other]\nk = 1\n")
			},
		},
		"an absent file was created": {
			before: func(*testing.T, string) {},
			change: func(t *testing.T, root string) { writeRel(t, root, ".mcp.json", "{}") },
		},
		"a file mode changed": {
			before: func(t *testing.T, root string) { writeRel(t, root, ".claude/settings.json", "{}") },
			change: func(t *testing.T, root string) {
				require.NoError(t, os.Chmod(filepath.Join(root, ".claude", "settings.json"), 0o600))
			},
		},
		"a skill directory appeared": {
			before: func(*testing.T, string) {},
			change: func(t *testing.T, root string) {
				writeRel(t, root, ".claude/skills/seamark-plan-change/SKILL.md", "mine\n")
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			tc.before(t, root)

			plan := mustPlan(t, Builtin(), request(root))
			tc.change(t, root)

			after := map[string]string{}
			for _, rel := range treeFiles(t, root) {
				after[rel] = readRel(t, root, rel)
			}

			result, err := ApplySetup(plan, ApplyOptions{})
			require.ErrorIs(t, err, ErrStalePlan)
			assert.Contains(t, err.Error(), "again", "the error says how to recover")
			for _, op := range append(result.Ops, result.Skills...) {
				assert.Equal(t, OpNotAttempted, op.Status, op.Path)
			}

			assert.Len(t, treeFiles(t, root), len(after), "a stale plan writes nothing")

			for rel, body := range after {
				assert.Equal(t, body, readRel(t, root, rel), "%s: the user's edit must survive", rel)
			}

			// A new plan from the changed tree applies cleanly.
			_, err = ApplySetup(mustPlan(t, Builtin(), request(root)), ApplyOptions{})
			require.NoError(t, err)
		})
	}
}

func TestEveryPendingInputIsCheckedAgainAtEachWriteBoundary(t *testing.T) {
	// The Codex file is the insert layout: its plan holds a byte offset,
	// so a stale write would put the keys into the wrong table.
	const headerOnly = "[mcp_servers.seamark]\n\n[other]\nk = 1\n"

	cases := map[string]struct {
		intents []ClientSetup
		edit    func(t *testing.T, root string)
		failed  string
	}{
		"the next document changed": {
			intents: []ClientSetup{{ClientID: ClaudeID, Hooks: true}, {ClientID: CodexID, RegisterMCP: true}},
			edit:    func(t *testing.T, root string) { writeRel(t, root, ".codex/config.toml", "# moved\n"+headerOnly) },
			failed:  ".codex/config.toml",
		},
		"a read-only input of the run changed": {
			// .mcp.json is read for the server name and never written.
			intents: []ClientSetup{{ClientID: ClaudeID, Hooks: true, ApproveTools: true}, {ClientID: CodexID, RegisterMCP: true}},
			edit:    func(t *testing.T, root string) { writeRel(t, root, ".mcp.json", `{"mcpServers":{}}`) },
			failed:  ".codex/config.toml",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, ".codex/config.toml", headerOnly)

			plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Binary: testBinary, Clients: tc.intents})

			// The user edits a file after the run passed its first check and
			// wrote the first document.
			result, err := ApplySetup(plan, ApplyOptions{Observe: func(op OpResult) {
				if op.Path == ".claude/settings.json" {
					tc.edit(t, root)
				}
			}})
			require.ErrorIs(t, err, ErrStalePlan)

			require.Len(t, result.Ops, 2)
			assert.Equal(t, OpApplied, result.Ops[0].Status, "a written document is not checked against its old guard")
			assert.Equal(t, tc.failed, result.Ops[1].Path)
			assert.Equal(t, OpFailed, result.Ops[1].Status)
			assert.NotContains(t, readRel(t, root, ".codex/config.toml"), "command =", "the stale write never lands")
		})
	}
}

func TestAReadOnlyFileIsNotReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes read-only files, so the legacy write would not fail either")
	}

	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", "{}")

	settings := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.Chmod(settings, 0o444))

	// A plain write fails on a read-only file. The rename must not turn
	// that into a silent replacement.
	result, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: ClaudeID, Hooks: true}},
	}), ApplyOptions{})
	require.ErrorContains(t, err, "read-only")

	assert.Equal(t, OpFailed, result.Ops[0].Status)
	assert.Equal(t, "{}", readRel(t, root, ".claude/settings.json"))
}

func TestSkillContentIsGuardedNotOnlyItsClassification(t *testing.T) {
	root := t.TempDir()
	req := SetupRequest{Root: root, Clients: []ClientSetup{{ClientID: ClaudeID, Skills: true}}}

	_, err := ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{})
	require.NoError(t, err)

	// A managed copy with a local edit is stale, before and after a
	// second edit. Only its bytes show that the plan is older than the
	// tree.
	const skill = ".claude/skills/seamark-plan-change/SKILL.md"

	shipped := readRel(t, root, skill)
	writeRel(t, root, skill, shipped+"\nfirst edit\n")

	plan := mustPlan(t, Builtin(), req)
	require.Equal(t, skills.Stale, plan.Skills[0].State)
	require.Len(t, plan.SkillGuards, len(plan.Skills))

	writeRel(t, root, skill, shipped+"\nsecond edit, made after the plan\n")

	result, err := ApplySetup(plan, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.Contains(t, err.Error(), "seamark-plan-change")
	assert.Contains(t, readRel(t, root, skill), "second edit", "an edit no plan saw is never overwritten")

	for _, op := range result.Skills {
		assert.NotEqual(t, OpApplied, op.Status, op.Path)
	}

	// A file the user added beside the shipped ones is not guarded: a
	// refresh never touches it, so it cannot make a plan stale.
	plan = mustPlan(t, Builtin(), req)
	writeRel(t, root, ".claude/skills/seamark-plan-change/notes.md", "mine\n")

	result, err = ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, OpApplied, result.Skills[0].Status)
	assert.Equal(t, "refreshed managed copy", result.Skills[0].Detail)
	assert.Equal(t, shipped, readRel(t, root, skill))
	assert.Equal(t, "mine\n", readRel(t, root, ".claude/skills/seamark-plan-change/notes.md"))
}

func TestASkillGuardIsCheckedAgainAtItsWriteBoundary(t *testing.T) {
	root := t.TempDir()

	plan := mustPlan(t, Builtin(), SetupRequest{
		Root:    root,
		Clients: []ClientSetup{{ClientID: ClaudeID, Skills: true}},
		Common:  []Document{scaffold(".seamark/config.yaml", "index: {}\n")},
	})

	names, err := skills.Names()
	require.NoError(t, err)

	// The last skill directory: the run has passed its first check, has
	// written the document, and has written the other skills by then.
	last := ".claude/skills/" + names[len(names)-1]
	foreign := last + "/SKILL.md"

	result, err := ApplySetup(plan, ApplyOptions{Observe: func(op OpResult) {
		if op.Path == ".seamark/config.yaml" {
			writeRel(t, root, foreign, "# my own skill with seamark's name\n")
		}
	}})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.Equal(t, "# my own skill with seamark's name\n", readRel(t, root, foreign), "a foreign skill is never overwritten")

	// The results are structured, like the documents' results.
	require.Len(t, result.Skills, len(names))

	for _, op := range result.Skills[:len(names)-1] {
		assert.Equal(t, OpApplied, op.Status, op.Path)
	}

	failed := result.Skills[len(names)-1]
	assert.Equal(t, last, failed.Path)
	assert.Equal(t, OpFailed, failed.Status)
	require.ErrorIs(t, failed.Err, ErrStalePlan)
}

func TestSkillResultsNameKeptAndForeignDirectories(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/skills/seamark-plan-change/SKILL.md", "# mine\n")

	req := SetupRequest{Root: root, Clients: []ClientSetup{{ClientID: ClaudeID, Skills: true}}}

	var log bytes.Buffer

	result, err := ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{SkillsLog: &log})
	require.NoError(t, err)

	byPath := map[string]OpResult{}
	for _, op := range result.Skills {
		byPath[op.Path] = op
	}

	foreign := byPath[".claude/skills/seamark-plan-change"]
	assert.Equal(t, OpKept, foreign.Status)
	assert.Contains(t, foreign.Detail, "not managed by seamark")
	assert.Equal(t, "# mine\n", readRel(t, root, ".claude/skills/seamark-plan-change/SKILL.md"))

	written := byPath[".claude/skills/seamark-review-change"]
	assert.Equal(t, OpApplied, written.Status)
	assert.True(t, written.Created)
	assert.Equal(t, []string{ClaudeID}, written.Consumers)

	// The installer's own lines still arrive, in init's words.
	assert.Contains(t, log.String(), "kept    .claude/skills/seamark-plan-change (not managed by seamark")

	again, err := ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, OpKept, again.Skills[len(again.Skills)-1].Status)
	assert.Equal(t, "current", again.Skills[len(again.Skills)-1].Detail)
}

func TestAForeignSkillThatIsNotADirectoryIsKept(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"a regular file with the skill's name": func(t *testing.T, root string) {
			writeRel(t, root, ".claude/skills/seamark-plan-change", "an unrelated file\n")
		},
		"a foreign directory with a file where a subdirectory ships": func(t *testing.T, root string) {
			writeRel(t, root, ".claude/skills/seamark-plan-change/SKILL.md", "# mine\n")
			writeRel(t, root, ".claude/skills/seamark-plan-change/references", "a file\n")
		},
	}

	// Setup keeps a foreign directory whatever it holds, so it must not
	// read further into it than the ownership check needs. Root reads
	// every file, so the case proves nothing there.
	if os.Geteuid() != 0 {
		cases["a foreign directory with a file that cannot be read"] = func(t *testing.T, root string) {
			writeRel(t, root, ".claude/skills/seamark-plan-change/SKILL.md", "# mine\n")
			writeRel(t, root, ".claude/skills/seamark-plan-change/references/interpreting-seamark.md", "private\n")

			private := filepath.Join(root, ".claude", "skills", "seamark-plan-change", "references", "interpreting-seamark.md")
			require.NoError(t, os.Chmod(private, 0o000))
			t.Cleanup(func() { _ = os.Chmod(private, 0o644) })
		}
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			arrange(t, root)

			before := map[string]string{}
			for _, rel := range treeFiles(t, root) {
				if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
					before[rel] = string(data)
				}
			}

			// The guard must not turn a kept foreign entry into a failed run.
			result, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
				Root: root, Clients: []ClientSetup{{ClientID: ClaudeID, Skills: true}},
			}), ApplyOptions{})
			require.NoError(t, err)

			for _, op := range result.Skills {
				if op.Path == ".claude/skills/seamark-plan-change" {
					assert.Equal(t, OpKept, op.Status)
					assert.Contains(t, op.Detail, "not managed by seamark")
				} else {
					assert.Equal(t, OpApplied, op.Status, "%s: the other skills are still installed", op.Path)
				}
			}

			for rel, body := range before {
				assert.Equal(t, body, readRel(t, root, rel), "%s: the foreign entry is untouched", rel)
			}
		})
	}
}

func TestANonRegularFileIsRefused(t *testing.T) {
	// The link cases are in setup_unix_test.go.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mcp.json"), 0o755))

	_, err := PlanSetup(Builtin(), SetupRequest{Root: root, Clients: []ClientSetup{{ClientID: ClaudeID, RegisterMCP: true}}})
	require.ErrorContains(t, err, "not a regular file")
}

func TestAPartialFailureIsReportedAndARerunConverges(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot fail")
	}

	root := t.TempDir()
	codexDir := filepath.Join(root, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0o755))

	req := SetupRequest{
		Root: root, Binary: testBinary,
		Clients: []ClientSetup{fullIntent(ClaudeID), fullIntent(CodexID)},
	}
	plan := mustPlan(t, Builtin(), req)

	// A read-only directory fails the third write after two have landed.
	require.NoError(t, os.Chmod(codexDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(codexDir, 0o755) })

	result, err := ApplySetup(plan, ApplyOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".codex/config.toml")

	statuses := map[string]OpStatus{}
	for _, op := range result.Ops {
		statuses[op.Path] = op.Status
	}

	assert.Equal(t, map[string]OpStatus{
		".claude/settings.json": OpApplied,
		".mcp.json":             OpApplied,
		".codex/config.toml":    OpFailed,
		".codex/hooks.json":     OpNotAttempted,
	}, statuses, "the result names what landed and what did not")

	for _, op := range result.Skills {
		assert.Equal(t, OpNotAttempted, op.Status, "%s: no skill is written after a failed write", op.Path)
	}

	assert.NoDirExists(t, filepath.Join(root, ".claude", "skills"))

	// Nothing is rolled back; the fixed tree converges on a rerun.
	require.NoError(t, os.Chmod(codexDir, 0o755))

	result, err = ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{})
	require.NoError(t, err)

	for _, op := range result.Ops {
		want := OpKept
		if strings.HasPrefix(op.Path, ".codex/") {
			want = OpApplied
		}

		assert.Equal(t, want, op.Status, op.Path)
	}

	for _, op := range result.Skills {
		assert.Equal(t, OpApplied, op.Status, op.Path)
	}

	assert.Empty(t, mustPlan(t, Builtin(), req).Writes)
}

func TestNotAttemptedMarksOnlyTheWritesAfterTheFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot fail")
	}

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	writeRel(t, root, "kept.txt", "same")

	reg, err := NewRegistry(
		docClient("a", "locked/a.txt", "a"),
		docClient("b", "kept.txt", "same"),
		docClient("c", "c.txt", "c"),
	)
	require.NoError(t, err)

	plan := mustPlan(t, reg, SetupRequest{Root: root, Clients: []ClientSetup{
		{ClientID: "a", RegisterMCP: true}, {ClientID: "b", RegisterMCP: true}, {ClientID: "c", RegisterMCP: true},
	}})

	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	result, err := ApplySetup(plan, ApplyOptions{})
	require.Error(t, err)
	require.Len(t, result.Ops, 3)

	assert.Equal(t, OpFailed, result.Ops[0].Status)
	require.Error(t, result.Ops[0].Err)
	assert.Equal(t, OpKept, result.Ops[1].Status, "a kept document stays kept")
	assert.Equal(t, OpNotAttempted, result.Ops[2].Status)
	assert.NoFileExists(t, filepath.Join(root, "c.txt"))
}

func TestTwoPlannersOfOneDocument(t *testing.T) {
	root := t.TempDir()

	same, err := NewRegistry(docClient("a", "shared.txt", "one"), docClient("b", "shared.txt", "one"))
	require.NoError(t, err)

	intents := []ClientSetup{{ClientID: "a", RegisterMCP: true}, {ClientID: "b", RegisterMCP: true}}

	plan := mustPlan(t, same, SetupRequest{Root: root, Clients: intents})
	require.Len(t, plan.Writes, 1, "equal content is one write")
	assert.Equal(t, []string{"a", "b"}, plan.Writes[0].Consumers)
	assert.Len(t, plan.Reads, 1)

	// Different content is a conflict; the last writer never wins.
	differ, err := NewRegistry(docClient("a", "shared.txt", "one"), docClient("b", "shared.txt", "two"))
	require.NoError(t, err)

	_, err = PlanSetup(differ, SetupRequest{Root: root, Clients: intents})
	require.ErrorIs(t, err, ErrPlanConflict)
	assert.Contains(t, err.Error(), "shared.txt")
	assert.Empty(t, treeFiles(t, root))

	// A writer replaces another client's keep of the same document.
	writeRel(t, root, "shared.txt", "one")

	plan = mustPlan(t, differ, SetupRequest{Root: root, Clients: intents})
	assert.Empty(t, plan.Kept)
	require.Len(t, plan.Writes, 1)
	assert.Equal(t, "two", string(plan.Writes[0].After))
}

// unguardedSetup breaks the adapter contract: it writes a document it
// never read under a guard.
type unguardedSetup struct{}

func (unguardedSetup) Inspect(string) Inspection { return Inspection{} }

// ManagedGateMode is empty: the adapter installs no gate hook.
func (unguardedSetup) ManagedGateMode(string) string { return "" }

func (unguardedSetup) Plan(string, string, ClientSetup) (ClientPlan, error) {
	return ClientPlan{Writes: []FileWrite{{Path: "blind.txt", After: []byte("x")}}}, nil
}

func TestAWriteWithoutAGuardIsRejected(t *testing.T) {
	reg, err := NewRegistry(Client{
		ID: "blind", Name: "Blind", Setup: unguardedSetup{}, SetupOps: SetupSupport{RegisterMCP: true},
	})
	require.NoError(t, err)

	_, err = PlanSetup(reg, SetupRequest{Root: t.TempDir(), Clients: []ClientSetup{{ClientID: "blind", RegisterMCP: true}}})
	require.ErrorContains(t, err, "has no read guard")
}

func TestCommonDocuments(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".gitignore", "node_modules\n")

	appendLine := Document{
		Path: ".gitignore", Detail: "seamark carve-outs", KeptDetail: "carve-outs already present",
		Compose: func(existing []byte, _ bool) ([]byte, error) {
			if bytes.Contains(existing, []byte(".seamark/*")) {
				return existing, nil
			}

			return append(bytes.Clone(existing), []byte(".seamark/*\n")...), nil
		},
	}
	req := SetupRequest{Root: root, Common: []Document{scaffold(".seamark/policy.yaml", "mode: warn\n"), appendLine}}

	_, err := ApplySetup(mustPlan(t, Builtin(), req), ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, "mode: warn\n", readRel(t, root, ".seamark/policy.yaml"))
	assert.Equal(t, "node_modules\n.seamark/*\n", readRel(t, root, ".gitignore"))

	// An existing scaffold is never overwritten, whatever the template says.
	writeRel(t, root, ".seamark/policy.yaml", "mode: enforce\n")

	second := mustPlan(t, Builtin(), req)
	assert.Empty(t, second.Writes)
	assert.Equal(t, []FileKeep{
		{Path: ".seamark/policy.yaml", Detail: "already present"},
		{Path: ".gitignore", Detail: "carve-outs already present"},
	}, second.Kept)

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Common: []Document{{
		Path: "x", Compose: func([]byte, bool) ([]byte, error) { return nil, errors.New("template failed") },
	}}})
	require.ErrorContains(t, err, "template failed")

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Common: []Document{{Path: "../escape", Compose: scaffold("", "").Compose}}})
	require.ErrorContains(t, err, "not a clean repository-relative path")

	_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Common: []Document{{Path: "x"}}})
	require.ErrorContains(t, err, "no compose function")
}

// lessonsStarter is a create-only document whose compose function always
// returns a template.
func lessonsStarter() Document {
	return Document{
		Path: ".seamark/lessons.yaml", CreateOnly: true, KeptDetail: "already present",
		Compose: func([]byte, bool) ([]byte, error) { return []byte("fresh template\n"), nil },
	}
}

func TestACreateOnlyDocumentIsGuardedByItsPresenceAlone(t *testing.T) {
	// Setup never overwrites or reads a starter file, whatever is at its
	// path and whatever its compose function would return. The link cases
	// are in setup_unix_test.go, because os.Symlink needs unix.
	always := lessonsStarter()

	for name, arrange := range map[string]func(t *testing.T, root string){
		"a file with other content": func(t *testing.T, root string) { writeRel(t, root, always.Path, "mine\n") },
		"a directory": func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "lessons.yaml"), 0o755))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			arrange(t, root)

			plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{always}})
			assert.Empty(t, plan.Writes)
			assert.Equal(t, []FileKeep{{Path: always.Path, Detail: "already present"}}, plan.Kept)
			assert.Equal(t, GuardPresence, plan.Reads[0].Kind)
		})
	}

	// Absent: it is created, and its later appearance makes the plan stale.
	root := t.TempDir()
	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{always}})
	require.Len(t, plan.Writes, 1)

	writeRel(t, root, always.Path, "appeared after the plan\n")

	_, err := ApplySetup(plan, ApplyOptions{})
	require.ErrorIs(t, err, ErrStalePlan)
	assert.Equal(t, "appeared after the plan\n", readRel(t, root, always.Path))
}

func TestAnExistingFileKeepsItsPermission(t *testing.T) {
	root := t.TempDir()
	writeRel(t, root, ".claude/settings.json", "{}")

	settings := filepath.Join(root, ".claude", "settings.json")
	require.NoError(t, os.Chmod(settings, 0o600))

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: ClaudeID, Hooks: true, RegisterMCP: true}},
	}), ApplyOptions{})
	require.NoError(t, err)

	info, err := os.Stat(settings)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a private file stays private")

	info, err = os.Stat(filepath.Join(root, ".mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "a new file gets the default permission")
}
