package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brewFixture builds a fake Homebrew prefix with a Cellar keg and,
// optionally, the opt symlink target. It returns the Cellar binary path
// and the opt binary path. The prefix has no symbolic link in it, so
// BinaryPath resolves it to itself.
func brewFixture(t *testing.T, withOpt bool) (cellarBin, optBin string) {
	t.Helper()

	prefix, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	cellarBin = filepath.Join(prefix, "Cellar", "seamark", "0.5.3", "bin", "seamark")
	optBin = filepath.Join(prefix, "opt", "seamark", "bin", "seamark")

	require.NoError(t, os.MkdirAll(filepath.Dir(cellarBin), 0o755))
	require.NoError(t, os.WriteFile(cellarBin, nil, 0o755))

	if withOpt {
		require.NoError(t, os.MkdirAll(filepath.Dir(optBin), 0o755))
		require.NoError(t, os.WriteFile(optBin, nil, 0o755))
	}

	return cellarBin, optBin
}

// runningAs makes BinaryPath see path as the running binary for the
// rest of the test.
func runningAs(t *testing.T, path string, err error) {
	t.Helper()

	saved := executable
	executable = func() (string, error) { return path, err }

	t.Cleanup(func() { executable = saved })
}

func TestBinaryPathMapsAHomebrewCellarPath(t *testing.T) {
	// Homebrew deletes the versioned Cellar directory on upgrade; hooks
	// must record the opt symlink, which survives upgrades.
	cellarBin, optBin := brewFixture(t, true)
	runningAs(t, cellarBin, nil)

	assert.Equal(t, optBin, BinaryPath())
}

func TestBinaryPathResolvesALinkToTheBinary(t *testing.T) {
	// A link on PATH must not end up in a hook: the hook must survive a
	// change of the link.
	cellarBin, optBin := brewFixture(t, true)
	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(cellarBin, link))
	runningAs(t, link, nil)

	assert.Equal(t, optBin, BinaryPath())
}

func TestBinaryPathFallsBackToTheBareName(t *testing.T) {
	runningAs(t, "", errors.New("no executable path"))

	assert.Equal(t, "seamark", BinaryPath())
}

func TestStableInstallPathRewritesHomebrewCellar(t *testing.T) {
	cellarBin, optBin := brewFixture(t, true)

	assert.Equal(t, optBin, stableInstallPath(cellarBin))
}

func TestStableInstallPathKeepsCellarWithoutOptLink(t *testing.T) {
	// Without the opt symlink the Cellar path is the only one that
	// works, so it must stay.
	cellarBin, _ := brewFixture(t, false)

	assert.Equal(t, cellarBin, stableInstallPath(cellarBin))
}

func TestStableInstallPathKeepsForeignPaths(t *testing.T) {
	home := "/home/u/.local/bin/seamark"
	assert.Equal(t, home, stableInstallPath(home))

	// A Cellar-like fragment without the <version>/bin/seamark keg
	// layout is not a Homebrew install and must stay untouched.
	odd := "/x/Cellar/seamark/extra/0.1/bin/seamark"
	assert.Equal(t, odd, stableInstallPath(odd))
}

// renamedBuild is the running binary of the ownership tests: a build
// whose basename is not seamark. No file exists at the path, so
// BinaryPath keeps it as it is.
const renamedBuild = "/x/seamark-1.2"

func TestOwnedBySeamarkAcceptsTheRunningBinaryPath(t *testing.T) {
	runningAs(t, renamedBuild, nil)

	markers := []string{GateMarker(ModeWarn), GateMarker(ModeEnforce)}

	for cmd, want := range map[string]bool{
		// The commands that setup writes for the running binary.
		renamedBuild + " gate --hook":           true,
		renamedBuild + " gate --enforce --hook": true,
		// The basename rule still holds for any other path.
		"/bin/seamark gate --hook": true,
		// Another build, another program, a wrapper, or an extra word is
		// not the command that setup writes.
		"/x/seamark-1.3 gate --hook":                    false,
		"/usr/bin/company-security gate --hook":         false,
		"/opt/wrapper " + renamedBuild + " gate --hook": false,
		renamedBuild + " gate --hook --verbose":         false,
		renamedBuild + " lessons --hook":                false,
	} {
		assert.Equal(t, want, OwnedBySeamark(cmd, markers), "cmd: %s", cmd)
	}
}

func TestOwnedBySeamarkAcceptsAQuotedRunningBinaryPath(t *testing.T) {
	// Setup quotes a path with a space, and the rule reads the quoted word
	// back as the same path.
	spaced := "/Apps/My Tools/new"
	runningAs(t, spaced, nil)

	cmd := ClaudeSpecs(ModeWarn)[0].Command(spaced)
	require.Equal(t, "'/Apps/My Tools/new' gate --hook", cmd)
	assert.True(t, OwnedBySeamark(cmd, []string{GateMarker(ModeWarn)}))
}

func TestOwnedBySeamarkAcceptsTheTargetOfALinkNamedSeamark(t *testing.T) {
	// A link named seamark resolves to its target, so setup writes the
	// target path, whose basename is not seamark.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	target := filepath.Join(dir, "new")
	require.NoError(t, os.WriteFile(target, nil, 0o755))

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(target, link))
	runningAs(t, link, nil)

	require.Equal(t, target, BinaryPath())
	assert.True(t, OwnedBySeamark(target+" gate --hook", []string{GateMarker(ModeWarn)}))
}

func TestReadHookReadsAWrapperOfTheRunningBinary(t *testing.T) {
	// Setup writes the path of the running binary into each hook. So a
	// wrapper around that path must read like a wrapper around a binary
	// named seamark, or the gate line misses an enforce gate. Another
	// build at another path stays another program.
	runningAs(t, renamedBuild, nil)

	marker := GateMarker(ModeEnforce)
	sibling := "/x/seamark-1.3"

	assertHookUse(t, []string{marker}, []hookCase{
		{"a shell condition", "test -x " + renamedBuild + " && " + renamedBuild + " " + marker, HookRuns},
		{"a shell script", "sh -c '" + renamedBuild + " " + marker + "'", HookRuns},
		{"an exec prefix", "env SEAMARK_DEBUG=1 " + renamedBuild + " " + marker, HookRuns},
		{"an unknown wrapper", "/opt/wrapper " + renamedBuild + " " + marker, HookMayRun},
		{"the loose test", "echo `" + renamedBuild + " " + marker + "`", HookMayRun},
		{"the loose test for words apart", "sh -c '" + renamedBuild + ` "$@"' _ ` + marker, HookMayRun},

		{"another build in a shell condition", "test -x " + sibling + " && " + sibling + " " + marker, HookNotRun},
		{"another build in an unknown wrapper", "/opt/wrapper " + sibling + " " + marker, HookNotRun},
		{"another build in the loose test", "echo `" + sibling + " " + marker + "`", HookNotRun},
		{"another build with words apart", "sh -c '" + sibling + ` "$@"' _ ` + marker, HookNotRun},
	})

	// The status of the wrapped gate reaches the client, so it blocks.
	assert.True(t, HookStatusPasses("test -x "+renamedBuild+" && "+renamedBuild+" "+marker, []string{marker}))
}

func TestReadHookReadsAQuotedRunningBinaryPath(t *testing.T) {
	// The loose test splits text at blanks and quotes, so a quoted path
	// with a blank is several loose words. They must match as a sequence.
	spaced := "/Apps/My Tools/new"
	runningAs(t, spaced, nil)

	quoted := ShellQuote(spaced)
	marker := GateMarker(ModeWarn)

	assertHookUse(t, []string{marker}, []hookCase{
		{"a shell condition", "test -x " + quoted + " && " + quoted + " " + marker, HookRuns},
		{"an unknown wrapper", "/opt/wrapper " + quoted + " " + marker, HookMayRun},
		{"the loose test", "echo `" + quoted + " " + marker + "`", HookMayRun},
		{"the loose test for words apart", "sh -c '" + spaced + ` "$@"' _ ` + marker, HookMayRun},

		{"another file in the same directory", "echo `'/Apps/My Tools/other' " + marker + "`", HookNotRun},
		{"the last loose word of the path alone", "echo `Tools/new " + marker + "`", HookNotRun},
	})
}

func TestMergeKeepsOneHookSetOfTheRunningBinary(t *testing.T) {
	runningAs(t, renamedBuild, nil)

	clients := map[string]struct {
		specs func(mode string) []Spec
		fires MatcherRule
	}{
		"claude": {ClaudeSpecs, ClaudeMatcher},
		"codex":  {CodexSpecs, CodexMatcher},
	}

	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			document := map[string]any{}

			// The first merge writes one hook per spec. The second finds
			// them and changes nothing.
			for range 2 {
				_, err := Merge(document, renamedBuild, client.specs(ModeWarn))
				require.NoError(t, err)
			}

			assert.Equal(t, specCommands(client.specs(ModeWarn), renamedBuild), allCommands(document))

			// A mode change rewrites the gate hook in place, so the mode
			// reading names the only gate hook there is.
			for _, mode := range []string{ModeEnforce, ModeWarn} {
				_, err := Merge(document, renamedBuild, client.specs(mode))
				require.NoError(t, err)

				assert.Equal(t, specCommands(client.specs(mode), renamedBuild), allCommands(document), mode)
				assert.Equal(t, mode, EffectiveGateMode(document, client.specs(ModeWarn)[0], client.fires), mode)
			}
		})
	}
}

// specCommands returns the command of each spec for a binary, sorted.
func specCommands(specs []Spec, bin string) []string {
	out := make([]string, 0, len(specs))

	for _, spec := range specs {
		out = append(out, spec.Command(bin))
	}

	slices.Sort(out)

	return out
}

// allCommands returns every hook command of a document, sorted.
func allCommands(document map[string]any) []string {
	var out []string

	hookMap, _ := document["hooks"].(map[string]any)

	for _, value := range hookMap {
		entries, _ := value.([]any)
		ForEachCommand(entries, func(_ string, _ map[string]any, cmd string) { out = append(out, cmd) })
	}

	slices.Sort(out)

	return out
}
