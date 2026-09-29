package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
)

// writeRel writes one repository-relative file and creates its parents.
func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()

	abs := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
}

// claudeHooks asks for the Claude Code hooks only, the smallest intent
// that installs a gate hook.
func claudeHooks() []integration.ClientSetup {
	return []integration.ClientSetup{{ClientID: "claude", Hooks: true}}
}

// seedClaudeGate installs a managed Claude Code gate hook in a mode.
func seedClaudeGate(t *testing.T, root, mode string) {
	t.Helper()

	flag := ""
	if mode == hooks.ModeEnforce {
		flag = " --enforce"
	}

	writeRel(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
		`{"type":"command","command":"/bin/seamark gate`+flag+` --hook"}]}]}}`)
}

// policyOf composes the policy starter of a request, as setup would for
// a new file.
func policyOf(t *testing.T, req integration.SetupRequest) string {
	t.Helper()

	for _, doc := range req.Common {
		if doc.Path == ".seamark/policy.yaml" {
			body, err := doc.Compose(nil, false)
			require.NoError(t, err)

			return string(body)
		}
	}

	require.Fail(t, "the request has no policy scaffold")

	return ""
}

func TestResolveGateMode(t *testing.T) {
	// The explicit request always wins; otherwise the installed mode
	// stays; a first install resolves to warn.
	assert.Equal(t, hooks.ModeWarn, ResolveGateMode("", ""))
	assert.Equal(t, hooks.ModeEnforce, ResolveGateMode("", hooks.ModeEnforce))
	assert.Equal(t, hooks.ModeEnforce, ResolveGateMode(hooks.ModeEnforce, ""))
	assert.Equal(t, hooks.ModeWarn, ResolveGateMode(hooks.ModeEnforce, hooks.ModeWarn))
}

func TestRequestPicksWarnOnAFirstInstall(t *testing.T) {
	root := t.TempDir()
	setups := claudeHooks()

	req, gateMode := Request(integration.Builtin(), root, "/bin/seamark", setups, "")

	assert.Equal(t, hooks.ModeWarn, gateMode)
	assert.Equal(t, root, req.Root)
	assert.Equal(t, "/bin/seamark", req.Binary)
	assert.Equal(t, setups, req.Clients)
	assert.Equal(t, StarterPolicy(hooks.ModeWarn), policyOf(t, req))
}

func TestRequestKeepsAnInstalledEnforceHook(t *testing.T) {
	// A run without a requested mode must never weaken an installed
	// enforce hook, and the new policy starter must agree with it.
	root := t.TempDir()
	seedClaudeGate(t, root, hooks.ModeEnforce)

	req, gateMode := Request(integration.Builtin(), root, "/bin/seamark", claudeHooks(), "")

	assert.Equal(t, hooks.ModeEnforce, gateMode)
	assert.Equal(t, StarterPolicy(hooks.ModeEnforce), policyOf(t, req))
}

func TestRequestHonorsAnExplicitMode(t *testing.T) {
	root := t.TempDir()
	seedClaudeGate(t, root, hooks.ModeEnforce)

	_, gateMode := Request(integration.Builtin(), root, "/bin/seamark", claudeHooks(), hooks.ModeWarn)
	assert.Equal(t, hooks.ModeWarn, gateMode, "an explicit warn removes enforcement")

	_, gateMode = Request(integration.Builtin(), t.TempDir(), "/bin/seamark", claudeHooks(), hooks.ModeEnforce)
	assert.Equal(t, hooks.ModeEnforce, gateMode, "an explicit enforce adds enforcement")
}

func TestRequestReadsTheInstalledModeOfGateHookClientsOnly(t *testing.T) {
	// A skills-only intent installs no gate hook, so an enforce hook on
	// disk does not set the mode of the run.
	root := t.TempDir()
	seedClaudeGate(t, root, hooks.ModeEnforce)

	skillsOnly := []integration.ClientSetup{{ClientID: "claude", Skills: true}}
	_, gateMode := Request(integration.Builtin(), root, "/bin/seamark", skillsOnly, "")

	assert.Equal(t, hooks.ModeWarn, gateMode)
}

func TestDocumentsAreTheFourScaffoldsInOrder(t *testing.T) {
	docs := Documents(hooks.ModeWarn)
	require.Len(t, docs, 4)

	var paths []string

	for _, doc := range docs {
		paths = append(paths, doc.Path)
		assert.NotNil(t, doc.Compose, doc.Path)
		assert.Nil(t, doc.Narrate, "%s: each surface narrates in its own words", doc.Path)
	}

	assert.Equal(t, []string{
		".seamark/policy.yaml", ".seamark/lessons.yaml", ".seamark/config.yaml", ".gitignore",
	}, paths)

	for _, doc := range docs[:3] {
		assert.True(t, doc.CreateOnly, "%s is a starter", doc.Path)
		assert.Equal(t, "starter", doc.Detail)
		assert.Equal(t, "already present", doc.KeptDetail)
	}

	assert.False(t, docs[3].CreateOnly, ".gitignore grows missing carve-outs")
	assert.Equal(t, "seamark carve-outs", docs[3].Detail)
	assert.Equal(t, "seamark carve-outs already present", docs[3].KeptDetail)
}

func TestAStarterNeverReplacesAnExistingFile(t *testing.T) {
	for _, doc := range Documents(hooks.ModeEnforce)[:3] {
		kept, err := doc.Compose([]byte("mine\n"), true)
		require.NoError(t, err)
		assert.Equal(t, "mine\n", string(kept), doc.Path)

		created, err := doc.Compose(nil, false)
		require.NoError(t, err)
		assert.NotEmpty(t, created, doc.Path)
	}
}

func TestComposeGitignoreAddsOnlyMissingCarveouts(t *testing.T) {
	// No carve-out at all: the commented block is appended once.
	fresh, err := composeGitignore([]byte("bin/\n"), true)
	require.NoError(t, err)
	assert.Equal(t, "bin/\n"+gitignoreBlock, string(fresh))

	// Everything present: the bytes return unchanged, so setup keeps it.
	same, err := composeGitignore(fresh, true)
	require.NoError(t, err)
	assert.Equal(t, string(fresh), string(same))

	// An older block: only the missing line is appended, after
	// `.seamark/*`, as gitignore precedence needs.
	old := "bin/\n.seamark/*\n!.seamark/policy.yaml\n!.seamark/effects.yaml\n!.seamark/lessons.yaml\n"
	grown, err := composeGitignore([]byte(old), true)
	require.NoError(t, err)
	assert.Equal(t, old+"\n!.seamark/config.yaml\n", string(grown))
	assert.Equal(t, 1, strings.Count(string(grown), ".seamark/*"))
}

func TestCarveoutLinesAreThePatternsOfTheBlock(t *testing.T) {
	assert.Equal(t, []string{
		".seamark/*", "!.seamark/policy.yaml", "!.seamark/effects.yaml",
		"!.seamark/lessons.yaml", "!.seamark/config.yaml",
	}, carveoutLines())
}

func TestStarterPolicyLoadsAndEvaluates(t *testing.T) {
	// The scaffolded policy REPLACES the embedded default gate policy, so
	// a CEL typo in the template would break the gate on every command for
	// every init'd user. Load and evaluate it through the real path.
	root := t.TempDir()
	writeRel(t, root, ".seamark/policy.yaml", starterPolicy)

	policy, err := gate.LoadPolicy(root)
	require.NoError(t, err, "starter policy must parse and its CEL must compile")

	catalog, err := effects.Load(root)
	require.NoError(t, err)

	d, err := gate.EvalCommand(policy, catalog, root, "ls -la")
	require.NoError(t, err, "starter policy must evaluate a command without error")
	assert.Equal(t, "warn", d.Mode, "starter ships in warn mode")
}

func TestStarterPolicyEnforceVariant(t *testing.T) {
	// The enforce scaffold is the warn template with one line swapped; a
	// template edit that breaks the swap would silently ship warn to a
	// user who explicitly asked for enforce.
	root := t.TempDir()
	writeRel(t, root, ".seamark/policy.yaml", StarterPolicy(hooks.ModeEnforce))

	policy, err := gate.LoadPolicy(root)
	require.NoError(t, err, "the enforce starter must still parse and compile")
	assert.Equal(t, "enforce", policy.Mode)

	assert.NotEqual(t, starterPolicy, StarterPolicy(hooks.ModeEnforce),
		"the mode-line swap must actually change the template")
	assert.Equal(t, starterPolicy, StarterPolicy(hooks.ModeWarn))
}

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
