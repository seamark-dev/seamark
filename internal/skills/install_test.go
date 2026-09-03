package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillPath joins a repository-relative skill path for the host OS.
func skillPath(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

// planByName indexes a plan by skill name for one target.
func planByName(t *testing.T, root string, targets []Target) map[string]Entry {
	t.Helper()

	entries, err := Plan(root, targets)
	require.NoError(t, err)

	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	return byName
}

func TestTargetsPerMode(t *testing.T) {
	root := t.TempDir()
	claude := []Target{{Client: ModeClaude, Dir: ClaudeDir}}
	both := []Target{{Client: ModeClaude, Dir: ClaudeDir}, {Client: ModeCodex, Dir: AgentsDir}}

	auto, err := Targets(root, ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, claude, auto, "without .agents/ auto addresses Claude Code only")

	// A file named .agents is not a Codex install; a directory is.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents"), nil, 0o644))
	auto, err = Targets(root, ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, claude, auto)

	require.NoError(t, os.Remove(filepath.Join(root, ".agents")))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".agents"), 0o755))
	auto, err = Targets(root, ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, both, auto)

	for mode, want := range map[string][]Target{ModeClaude: claude, ModeCodex: both[1:], ModeAll: both} {
		got, err := Targets(root, mode)
		require.NoError(t, err, mode)
		assert.Equal(t, want, got, mode)
	}

	_, err = Targets(root, "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto, claude, codex, all", "the error names the accepted values")
	assert.False(t, ValidMode("bogus"))
	assert.True(t, ValidMode(ModeAll))
}

func TestPlanClassifiesEveryState(t *testing.T) {
	root := t.TempDir()
	targets := []Target{claudeTarget}

	for name, e := range planByName(t, root, targets) {
		assert.Equal(t, Absent, e.State, name)
		assert.Equal(t, ClaudeDir+"/"+name, e.Rel)
	}

	require.NoError(t, Install(&bytes.Buffer{}, root, targets, false))

	for name, e := range planByName(t, root, targets) {
		assert.Equal(t, Current, e.State, name)
	}

	// Stale by a local edit, stale by a missing shipped file; both are
	// still managed, so init refreshes them.
	edited := skillPath(root, ClaudeDir+"/seamark-plan-change/SKILL.md")
	data, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(edited, append(data, []byte("\nlocal note\n")...), 0o644))
	require.NoError(t, os.Remove(skillPath(root, ClaudeDir+"/seamark-review-change/references/interpreting-seamark.md")))

	byName := planByName(t, root, targets)
	assert.Equal(t, Stale, byName["seamark-plan-change"].State)
	assert.Equal(t, Stale, byName["seamark-review-change"].State)
	assert.Equal(t, Current, byName["seamark-understand-repo"].State)
}

func TestPlanTreatsUnmarkedDirectoriesAsForeign(t *testing.T) {
	root := t.TempDir()
	targets := []Target{claudeTarget}

	write := func(rel, body string) {
		p := skillPath(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	// The user's own skill under a shipped name, one without SKILL.md,
	// and one whose header does not parse: none of them is seamark's.
	write(ClaudeDir+"/seamark-plan-change/SKILL.md", "---\nname: seamark-plan-change\ndescription: mine\n---\nMy body.\n")
	write(ClaudeDir+"/seamark-review-change/notes.md", "no skill file here\n")
	write(ClaudeDir+"/seamark-understand-repo/SKILL.md", "---\nname: [\n---\n")

	byName := planByName(t, root, targets)

	for name, reason := range map[string]string{
		"seamark-plan-change":     "no seamark marker",
		"seamark-review-change":   "no SKILL.md",
		"seamark-understand-repo": "unreadable frontmatter",
	} {
		assert.Equal(t, Foreign, byName[name].State, name)
		assert.Equal(t, reason, byName[name].Reason, name)
	}

	// A regular file where the skill directory would go is foreign too.
	root2 := t.TempDir()
	require.NoError(t, os.MkdirAll(skillPath(root2, ClaudeDir), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root2, ClaudeDir+"/seamark-plan-change"), nil, 0o644))

	e := planByName(t, root2, targets)["seamark-plan-change"]
	assert.Equal(t, Foreign, e.State)
	assert.Equal(t, "not a directory", e.Reason)
}

func TestPlanFailsBeforeAnyWriteWhenTheClientDirIsUnreadable(t *testing.T) {
	root := t.TempDir()

	// A regular file at .claude/skills makes every child path unreadable
	// with an error that is not "not found"; the plan must surface it.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir), []byte("oops"), 0o644))

	_, err := Plan(root, []Target{claudeTarget})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ClaudeDir+"/seamark-plan-change")

	var w bytes.Buffer
	require.Error(t, Install(&w, root, []Target{claudeTarget}, false))
	assert.Empty(t, w.String(), "nothing is narrated, because nothing was written")
}

func TestInstallWritesEveryShippedFileAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	targets := []Target{claudeTarget, codexTarget}

	var first bytes.Buffer
	require.NoError(t, Install(&first, root, targets, false))

	names, err := Names()
	require.NoError(t, err)

	for _, target := range targets {
		for _, name := range names {
			assert.Contains(t, first.String(), "wrote  "+target.Dir+"/"+name)

			files, err := Files(name)
			require.NoError(t, err)

			for rel, want := range files {
				got, err := os.ReadFile(skillPath(root, target.Dir+"/"+name+"/"+rel))
				require.NoError(t, err, rel)
				assert.Equal(t, string(want), string(got), rel)
			}
		}
	}

	var second bytes.Buffer
	require.NoError(t, Install(&second, root, targets, false))
	assert.NotContains(t, second.String(), "wrote")
	assert.NotContains(t, second.String(), "updated")

	for _, target := range targets {
		for _, name := range names {
			assert.Contains(t, second.String(), "kept    "+target.Dir+"/"+name+" (current)")
		}
	}
}

func TestInstallPreviewWritesNothing(t *testing.T) {
	root := t.TempDir()

	var w bytes.Buffer
	require.NoError(t, Install(&w, root, []Target{claudeTarget}, true))

	assert.Contains(t, w.String(), "would write  "+ClaudeDir+"/seamark-plan-change")
	assert.NotContains(t, w.String(), "wrote ")
	assert.NoDirExists(t, filepath.Join(root, ".claude"))
}

func TestInstallLeavesForeignDirectoryUntouched(t *testing.T) {
	root := t.TempDir()
	dir := skillPath(root, ClaudeDir+"/seamark-plan-change")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))

	own := map[string]string{
		"SKILL.md":        "---\nname: seamark-plan-change\ndescription: my own\n---\nMine.\n",
		"scripts/run.sh":  "#!/bin/sh\necho mine\n",
		"references/x.md": "keep\n",
	}

	for rel, body := range own {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}

	var w bytes.Buffer
	require.NoError(t, Install(&w, root, []Target{claudeTarget}, false))

	assert.Contains(t, w.String(), "kept    "+ClaudeDir+"/seamark-plan-change (not managed by seamark: no seamark marker)")
	assert.Contains(t, w.String(), "wrote  "+ClaudeDir+"/seamark-review-change", "the other skills still install")

	for rel, body := range own {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.Equal(t, body, string(got), "%s must stay byte for byte", rel)
	}

	assert.NoFileExists(t, filepath.Join(dir, "references", "interpreting-seamark.md"),
		"no shipped file lands in a foreign directory")
}

func TestInstallRefreshesStaleManagedCopyAndKeepsExtras(t *testing.T) {
	root := t.TempDir()
	targets := []Target{claudeTarget}
	require.NoError(t, Install(&bytes.Buffer{}, root, targets, false))

	dir := skillPath(root, ClaudeDir+"/seamark-plan-change")
	skillMD := filepath.Join(dir, SkillFile)
	shipped, err := os.ReadFile(skillMD)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(skillMD, append(shipped, []byte("\nlocal edit\n")...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("user notes\n"), 0o644))

	// Preview names the refresh and changes nothing.
	var preview bytes.Buffer
	require.NoError(t, Install(&preview, root, targets, true))
	assert.Contains(t, preview.String(), "would update "+ClaudeDir+"/seamark-plan-change")

	edited, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	assert.Contains(t, string(edited), "local edit")

	var w bytes.Buffer
	require.NoError(t, Install(&w, root, targets, false))
	assert.Contains(t, w.String(), "updated "+ClaudeDir+"/seamark-plan-change (refreshed managed copy)")

	refreshed, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	assert.Equal(t, string(shipped), string(refreshed))

	notes, err := os.ReadFile(filepath.Join(dir, "notes.md"))
	require.NoError(t, err)
	assert.Equal(t, "user notes\n", string(notes), "extra user files survive a refresh")
}

func TestInspectAndSummary(t *testing.T) {
	root := t.TempDir()

	states := Inspect(root)
	require.Len(t, states, 2)
	assert.False(t, states[0].Installed())
	assert.Equal(t, "claude not installed · codex not installed", Summary(states))

	require.NoError(t, Install(&bytes.Buffer{}, root, []Target{claudeTarget}, false))
	states = Inspect(root)
	assert.True(t, states[0].Installed())
	assert.Equal(t, 3, states[0].Current)
	assert.Equal(t, "claude 3/3 current · codex not installed", Summary(states))

	// One stale copy names the corrective command; a foreign directory
	// is counted, not blamed.
	skillMD := skillPath(root, ClaudeDir+"/seamark-plan-change/SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("---\nname: seamark-plan-change\nmetadata:\n  seamark: managed\n---\nedited\n"), 0o644))
	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir+"/seamark-review-change/SKILL.md"), []byte("---\nname: x\n---\n"), 0o644))

	states = Inspect(root)
	assert.Equal(t, 1, states[0].Stale)
	assert.Equal(t, 1, states[0].Foreign)
	assert.Equal(t, "claude 1/3 current, 1 stale, 1 not managed · codex not installed (re-run seamark init --skills)", Summary(states))

	// An unreadable client directory is reported, never fatal.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, AgentsDir), []byte("oops"), 0o644))

	states = Inspect(root)
	assert.NotEmpty(t, states[1].Err)
	assert.Contains(t, Summary(states), "codex unreadable")
}

func TestPlanRejectsSymlinksAndNeverWritesThroughThem(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	targets := []Target{claudeTarget}
	require.NoError(t, Install(&bytes.Buffer{}, root, targets, false))

	// A shipped file replaced by a link to a file outside the repository:
	// the committed-symlink attack. The refresh must not follow it.
	victim := filepath.Join(outside, "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("do not touch\n"), 0o644))

	ref := skillPath(root, ClaudeDir+"/seamark-plan-change/references/interpreting-seamark.md")
	require.NoError(t, os.Remove(ref))
	require.NoError(t, os.Symlink(victim, ref))

	e := planByName(t, root, targets)["seamark-plan-change"]
	assert.Equal(t, Foreign, e.State)
	assert.Equal(t, "symlink at references/interpreting-seamark.md", e.Reason)

	var w bytes.Buffer
	require.NoError(t, Install(&w, root, targets, false))
	assert.Contains(t, w.String(), "kept    "+ClaudeDir+"/seamark-plan-change (not managed by seamark: symlink at references/interpreting-seamark.md)")

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "do not touch\n", string(got))

	// A linked SKILL.md cannot borrow a marker from elsewhere either.
	managed := skillPath(root, ClaudeDir+"/seamark-understand-repo/SKILL.md")
	skillMD := skillPath(root, ClaudeDir+"/seamark-review-change/SKILL.md")
	require.NoError(t, os.Remove(skillMD))
	require.NoError(t, os.Symlink(managed, skillMD))

	e = planByName(t, root, targets)["seamark-review-change"]
	assert.Equal(t, Foreign, e.State)
	assert.Equal(t, "symlink at SKILL.md", e.Reason)

	// A skill directory that is itself a link, and a linked client
	// directory: nothing is written into the link's target.
	linkedDir := skillPath(root, ClaudeDir+"/seamark-review-change")
	require.NoError(t, os.RemoveAll(linkedDir))
	require.NoError(t, os.Symlink(outside, linkedDir))

	e = planByName(t, root, targets)["seamark-review-change"]
	assert.Equal(t, Foreign, e.State)
	assert.Equal(t, "symlink at "+ClaudeDir+"/seamark-review-change", e.Reason)

	root2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root2, ".claude"), 0o755))
	require.NoError(t, os.Symlink(outside, skillPath(root2, ClaudeDir)))

	for name, e := range planByName(t, root2, targets) {
		assert.Equal(t, Foreign, e.State, name)
		assert.Equal(t, "symlink at "+ClaudeDir, e.Reason, name)
	}

	require.NoError(t, Install(&bytes.Buffer{}, root2, targets, false))

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "only victim.txt: nothing was written through a link")
}

func TestApplyRefusesToWriteThroughASymlinkEvenWhenThePlanSaysAbsent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.Symlink(outside, skillPath(root, ClaudeDir)))

	// A plan computed before the link appeared, or built by hand: the
	// write-time check is the second line of defense.
	entry := Entry{Target: claudeTarget, Name: "seamark-plan-change", Rel: ClaudeDir + "/seamark-plan-change", State: Absent}

	var w bytes.Buffer
	err := Apply(&w, root, []Entry{entry}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink at "+ClaudeDir)

	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestPlanValidatesEveryFileBeforeReportingStale(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}

	root := t.TempDir()
	targets := []Target{claudeTarget}
	require.NoError(t, Install(&bytes.Buffer{}, root, targets, false))

	// SKILL.md is stale and the reference is unreadable. A classifier
	// that stopped at the first stale file would report Stale, and init
	// would write scaffolds and hooks before the refresh failed.
	dir := skillPath(root, ClaudeDir+"/seamark-plan-change")
	skillMD := filepath.Join(dir, SkillFile)
	data, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(skillMD, append(data, []byte("\nedit\n")...), 0o644))

	ref := filepath.Join(dir, "references", "interpreting-seamark.md")
	require.NoError(t, os.Chmod(ref, 0o000))
	t.Cleanup(func() { _ = os.Chmod(ref, 0o644) })

	_, err = Plan(root, targets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ClaudeDir+"/seamark-plan-change/references/interpreting-seamark.md")
}
