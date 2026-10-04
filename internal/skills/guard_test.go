package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotFollowsContentNotOnlyClassification(t *testing.T) {
	root := t.TempDir()
	targets := []Target{{Client: ModeClaude, Dir: ClaudeDir}}

	absent := planByName(t, root, targets)["seamark-plan-change"]
	require.Equal(t, Absent, absent.State)

	before, err := Snapshot(root, absent)
	require.NoError(t, err)

	require.NoError(t, Install(&bytes.Buffer{}, root, targets, false))

	installed, err := Snapshot(root, absent)
	require.NoError(t, err)
	assert.NotEqual(t, before, installed, "an absent and an installed directory differ")

	again, err := Snapshot(root, absent)
	require.NoError(t, err)
	assert.Equal(t, installed, again, "an unchanged directory has a stable digest")

	// Two different edits both classify as stale. Only the digest
	// separates them, which is why the classification is not a guard.
	skillMD := skillPath(root, absent.Rel+"/"+SkillFile)

	shipped, err := os.ReadFile(skillMD)
	require.NoError(t, err)

	digests := map[[32]byte]bool{installed: true}

	for _, edit := range []string{"\nfirst\n", "\nsecond\n"} {
		require.NoError(t, os.WriteFile(skillMD, append(bytes.Clone(shipped), edit...), 0o644))

		now, err := Check(root, absent)
		require.NoError(t, err)
		assert.Equal(t, Stale, now.State)

		digest, err := Snapshot(root, absent)
		require.NoError(t, err)
		assert.False(t, digests[digest], "each edit has its own digest")

		digests[digest] = true
	}

	// A file the user added is outside the digest: a refresh keeps it.
	edited, err := Snapshot(root, absent)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(skillMD), "notes.md"), []byte("mine\n"), 0o644))

	withExtra, err := Snapshot(root, absent)
	require.NoError(t, err)
	assert.Equal(t, edited, withExtra)
}

func TestCheckSeesAnOwnershipChange(t *testing.T) {
	root := t.TempDir()
	targets := []Target{{Client: ModeClaude, Dir: ClaudeDir}}

	planned := planByName(t, root, targets)["seamark-plan-change"]

	require.NoError(t, os.MkdirAll(skillPath(root, planned.Rel), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, planned.Rel+"/"+SkillFile), []byte("# mine\n"), 0o644))

	now, err := Check(root, planned)
	require.NoError(t, err)
	assert.Equal(t, Foreign, now.State)
	assert.NotEqual(t, planned, now, "the planned entry no longer describes the directory")
	assert.Equal(t, planned.Rel, now.Rel)
}

func TestApplyEntryWritesAndNarratesOneDirectory(t *testing.T) {
	root := t.TempDir()
	entries, err := Plan(root, []Target{{Client: ModeClaude, Dir: ClaudeDir}})
	require.NoError(t, err)
	require.True(t, entries[0].Writes())

	var out bytes.Buffer

	require.NoError(t, ApplyEntry(&out, root, entries[0], false))
	assert.Equal(t, "  wrote  "+entries[0].Rel+"\n", out.String(), "the line Apply always printed")
	assert.FileExists(t, skillPath(root, entries[0].Rel+"/"+SkillFile))
	assert.NoDirExists(t, skillPath(root, entries[1].Rel), "one entry, one directory")

	current, err := Check(root, entries[0])
	require.NoError(t, err)
	assert.Equal(t, Current, current.State)
	assert.False(t, current.Writes())
}

func TestSnapshotRecordsAPathThatIsNotADirectory(t *testing.T) {
	// A regular file with a skill's name is a foreign entry that setup
	// keeps. Its guard must record the state, never fail the run.
	root := t.TempDir()
	targets := []Target{{Client: ModeClaude, Dir: ClaudeDir}}

	require.NoError(t, os.MkdirAll(skillPath(root, ClaudeDir), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir+"/seamark-plan-change"), []byte("a file\n"), 0o644))

	entry := planByName(t, root, targets)["seamark-plan-change"]
	require.Equal(t, Foreign, entry.State)

	asFile, err := Snapshot(root, entry)
	require.NoError(t, err, "no descent below a path that is not a directory")

	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir+"/seamark-plan-change"), []byte("another file\n"), 0o644))

	edited, err := Snapshot(root, entry)
	require.NoError(t, err)
	assert.NotEqual(t, asFile, edited, "the file's content is still part of the digest")

	// A file where a shipped subdirectory belongs blocks the paths below it.
	blocked := planByName(t, root, targets)["seamark-review-change"]
	require.NoError(t, os.MkdirAll(skillPath(root, blocked.Rel), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, blocked.Rel+"/references"), []byte("not a directory\n"), 0o644))

	_, err = Snapshot(root, blocked)
	require.NoError(t, err, "an unreachable child is a state, not an error")
}

func TestSymlinkInStopsBelowARegularFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644))

	link, err := SymlinkIn(root, "file/below/it")
	require.NoError(t, err, "nothing exists below a regular file, so no link can be there")
	assert.Empty(t, link)

	// A foreign directory that holds a file where a subdirectory ships is
	// still classified, not rejected.
	require.NoError(t, os.MkdirAll(skillPath(root, ClaudeDir+"/seamark-plan-change"), 0o755))
	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir+"/seamark-plan-change/"+SkillFile), []byte("# mine\n"), 0o644))
	require.NoError(t, os.WriteFile(skillPath(root, ClaudeDir+"/seamark-plan-change/references"), []byte("x"), 0o644))

	entry := planByName(t, root, []Target{{Client: ModeClaude, Dir: ClaudeDir}})["seamark-plan-change"]
	assert.Equal(t, Foreign, entry.State)
}
