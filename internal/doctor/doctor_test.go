package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/store"
)

// The hook fixtures of this file name /bin/seamark, which a test machine
// does not need to hold. The inspection reads it as a hook that can start.
func init() {
	integration.TreatHookBinaryAsPresent("/bin/seamark")
}

// fixtureRoot builds a git workspace with a healthy index database.
func fixtureRoot(t *testing.T) (root, dbPath string) {
	t.Helper()

	root = t.TempDir()

	for _, args := range [][]string{{"init", "-q"}, {"commit", "--allow-empty", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	dbPath = store.DefaultPath(root)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	return root, dbPath
}

// stubAgent puts an executable named after the default agent CLI on
// PATH for the test. A report-wide "no warnings" assertion must not
// depend on whether the machine running the tests has Claude Code
// installed: CI does not, a developer's laptop usually does.
func stubAgent(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// byName indexes a report's checks.
func byName(r *Report) map[string]Check {
	out := map[string]Check{}
	for _, c := range r.Checks {
		out[c.Name] = c
	}

	return out
}

func TestRunHealthyWorkspace(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Zero(t, r.Fails, "a healthy fixture must not fail: %+v", r.Checks)

	for _, name := range []string{"binary", "git", "index", "integrity", "policy", "effects"} {
		assert.Equal(t, StateOK, checks[name].State, "%s: %s", name, checks[name].Detail)
	}

	assert.Equal(t, StateInfo, checks["hooks"].State, "no hooks installed yet is a fact, not a fault")
	assert.Contains(t, checks["hooks"].Fix, "seamark init")
}

func TestRunMissingIndexIsInfoNotFailure(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".seamark")))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "seamark index")
	assert.Zero(t, r.Fails)
}

func TestRunDetectsNewerDatabase(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaSchemaVersion, strconv.Itoa(store.SupportedSchema()+5)))
	require.NoError(t, st.Close())

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "do not delete")
	assert.Positive(t, r.Fails)
}

func TestRunDetectsCorruptDatabase(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// Garbage where SQLite expects a header: probe must fail with the
	// export-first recovery path, never a crash.
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not a database"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "state export")
}

func TestRunDetectsBrokenPolicyAndEffects(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"),
		[]byte("sinks: [broken\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["policy"].State)
	assert.Contains(t, checks["policy"].Fix, "fails closed")
	assert.Equal(t, StateFail, checks["effects"].State)
}

func TestRunFailsAPolicyItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file with mode 0000")
	}

	root, dbPath := fixtureRoot(t)
	policyPath := filepath.Join(root, ".seamark", "policy.yaml")

	// An unreadable policy is a broken policy, not the embedded default.
	require.NoError(t, os.WriteFile(policyPath, []byte("mode: enforce\n"), 0o644))
	require.NoError(t, os.Chmod(policyPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(policyPath, 0o644) })

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["policy"].State)
	assert.Equal(t, "gate: "+policyPath+": permission denied", checks["policy"].Detail)
	assert.Contains(t, checks["policy"].Fix, "fails closed")
}

func TestRunDetectsIgnoredPolicyOverlays(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// A .gitignore swallowing .seamark whole, without the carve-outs
	// init installs: the policy silently leaves review.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(".seamark/*\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: warn\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["gitignore"].State)
	assert.Contains(t, checks["gitignore"].Detail, "policy.yaml")
	assert.Contains(t, checks["gitignore"].Fix, "seamark init")
}

func TestRunDetectsPartialHooks(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"/bin/seamark gate --hook"}]}]}}`), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["hooks"].State)
	assert.Contains(t, checks["hooks"].Detail, "lessons hook missing")
}

func TestRunWarnsWhenEveryHookBinaryIsMissing(t *testing.T) {
	const absent = "/nonexistent/seamark"

	cases := map[string]struct{ rel, document, route string }{
		"claude": {
			".claude/settings.json",
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Bash","hooks":[{"type":"command","command":"` + absent + ` gate --hook"}]},` +
				`{"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"` + absent + ` lessons --hook"}]}],` +
				`"PostCompact":[{"hooks":[{"type":"command","command":"` + absent + ` lessons --hook-reset"}]}]}}`,
			"seamark init --client claude",
		},
		"codex": {
			".codex/hooks.json",
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Bash","hooks":[{"type":"command","command":"` + absent + ` gate --hook --client codex"}]},` +
				`{"matcher":"apply_patch","hooks":[{"type":"command","command":"` + absent + ` lessons --hook --client codex"}]}]}}`,
			"seamark init --client codex",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, dbPath := fixtureRoot(t)

			require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(tc.rel)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, tc.rel), []byte(tc.document), 0o644))

			hooks := byName(Run(root, dbPath, "test"))["hooks"]

			assert.Equal(t, StateWarn, hooks.State)
			assert.Contains(t, hooks.Detail, "gate hook cannot start from "+tc.rel+": "+absent+" is missing")
			assert.Contains(t, hooks.Fix, tc.route+"`, which rewrites the hook command")
		})
	}
}

func TestRunNamesWhyAHookBinaryCannotStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no execute bit")
	}

	root, dbPath := fixtureRoot(t)

	// A file with mode 644 exists, so the warning names the execute bit,
	// not a missing path.
	plain := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.WriteFile(plain, []byte("text"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"`+plain+` gate --hook"}]}]}}`), 0o644))

	hooks := byName(Run(root, dbPath, "test"))["hooks"]

	assert.Equal(t, StateWarn, hooks.State)
	assert.Contains(t, hooks.Detail, "gate hook cannot start from .claude/settings.json: "+plain+" has no execute bit")
	assert.NotContains(t, hooks.Detail, "is missing")
}

func TestRunNamesAnUnreadableCodexConfigInTheHookFix(t *testing.T) {
	// Codex setup stops at a config.toml that it cannot read. The hooks
	// line names .codex/hooks.json, so its fix names config.toml.
	const absent = "/nonexistent/seamark"

	// The missing hook needs no rewrite, so its fix is the route alone.
	cases := map[string]struct {
		document, want string
		exact          bool
	}{
		"missing binary": {
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Bash","hooks":[{"type":"command","command":"` + absent + ` gate --hook --client codex"}]},` +
				`{"matcher":"apply_patch","hooks":[{"type":"command","command":"` + absent + ` lessons --hook --client codex"}]}]}}`,
			"fix .codex/config.toml, then re-run `seamark init --client codex`, which rewrites the hook command",
			false,
		},
		"missing hook": {
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"apply_patch","hooks":[{"type":"command","command":"/bin/seamark lessons --hook --client codex"}]}]}}`,
			"fix .codex/config.toml, then re-run `seamark init --client codex`",
			true,
		},
		// The rewrite route starts with the restore route of the missing
		// hook. One setup run does both, so the fix gives that step once.
		"missing binary and missing hook": {
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Bash","hooks":[{"type":"command","command":"` + absent + ` gate --hook --client codex"}]}]}}`,
			"fix .codex/config.toml, then re-run `seamark init --client codex`, " +
				"which rewrites the hook command to the running seamark binary; " +
				"then open the project in Codex and review the hooks with /hooks, because the new command changes the trust hash",
			true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, dbPath := fixtureRoot(t)

			require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(tc.document), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("broken = = [\n"), 0o644))

			hooks := byName(Run(root, dbPath, "test"))["hooks"]

			assert.Equal(t, StateWarn, hooks.State)
			assert.True(t, strings.HasPrefix(hooks.Fix, tc.want), hooks.Fix)
			assert.NotContains(t, hooks.Fix, "fix the file")

			if tc.exact {
				assert.Equal(t, tc.want, hooks.Fix)
			}
		})
	}
}

func TestRunNamesTheMatcherOfAHookThatNeverFires(t *testing.T) {
	cases := map[string]struct{ rel, document, want string }{
		"claude gate": {
			".claude/settings.json",
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Shell","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]},` +
				`{"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"/bin/seamark lessons --hook"}]}]}}`,
			`set the matcher of that hook to "Bash" in .claude/settings.json`,
		},
		"codex gate": {
			".codex/hooks.json",
			codexIdleGate,
			`set the matcher of that hook to "Bash" in .codex/hooks.json`,
		},
		"claude gate and lessons": {
			".claude/settings.json",
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Shell","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]},` +
				`{"matcher":"Read","hooks":[{"type":"command","command":"/bin/seamark lessons --hook"}]}]}}`,
			// Doctor prints the action of each hook, in the order of the
			// detail: the gate hook first, then the lessons hook.
			`set the matcher of that hook to "Bash" in .claude/settings.json; ` +
				`set the matcher of that hook to "Edit|Write|MultiEdit" in .claude/settings.json`,
		},
		"claude gate without lessons": {
			".claude/settings.json",
			`{"hooks":{"PreToolUse":[` +
				`{"matcher":"Shell","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]}]}}`,
			// Setup keeps the matcher, so the fix names both steps in one run.
			`set the matcher of that hook to "Bash" in .claude/settings.json; ` +
				"run `seamark init --client claude` to install the missing hook",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, dbPath := fixtureRoot(t)

			require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(tc.rel)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, tc.rel), []byte(tc.document), 0o644))

			hooks := byName(Run(root, dbPath, "test"))["hooks"]

			assert.Equal(t, StateWarn, hooks.State)
			assert.Contains(t, hooks.Detail, "never runs from "+tc.rel+": its matcher")
			assert.Equal(t, tc.want, hooks.Fix)
		})
	}
}

func TestJoinActions(t *testing.T) {
	cases := map[string]struct {
		actions []string
		want    string
	}{
		"repeat and empty":   {[]string{"a", "", "a"}, "a"},
		"contained action":   {[]string{"b", "x; then b"}, "x; then b"},
		"only empty actions": {[]string{"", ""}, ""},
		"two actions":        {[]string{"a", "b"}, "a; b"},
		"no actions":         {nil, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, joinActions(tc.actions))
		})
	}
}

// Shapes in which one doctor line has more than one action. Doctor and
// status must give the same actions for them.
const (
	codexIdleGate = `{"hooks":{"PreToolUse":[` +
		`{"matcher":"ash","hooks":[{"type":"command","command":"/bin/seamark gate --hook --client codex"}]},` +
		`{"matcher":"apply_patch","hooks":[{"type":"command","command":"/bin/seamark lessons --hook --client codex"}]}]}}`
	codexTwoGates = `{"hooks":{"PreToolUse":[` +
		`{"matcher":"ash","hooks":[{"type":"command","command":"/bin/seamark gate --hook --client codex"}]},` +
		`{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]},` +
		`{"matcher":"apply_patch","hooks":[{"type":"command","command":"/bin/seamark lessons --hook --client codex"}]}]}}`
	claudeTwoMatchers = `{"hooks":{"PreToolUse":[` +
		`{"matcher":"Shell","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]},` +
		`{"matcher":"Read","hooks":[{"type":"command","command":"/bin/seamark lessons --hook"}]}]}}`
	claudeGateOnly = `{"hooks":{"PreToolUse":[` +
		`{"matcher":"Shell","hooks":[{"type":"command","command":"/bin/seamark gate --hook"}]}]}}`
)

// writeFile returns a fixture writer that puts one document into the
// workspace.
func writeFile(rel, document string) func(root string) error {
	return func(root string) error {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}

		return os.WriteFile(path, []byte(document), 0o644)
	}
}

// hooksOff returns the current fixture with disableAllHooks: true in the
// shared Claude Code file. With brokenLocal, the personal file also
// holds text that is not JSON.
func hooksOff(brokenLocal bool) inspecttest.Fixture {
	off := inspecttest.Named("current")
	write := off.Write
	off.Write = func(root string) error {
		if err := write(root); err != nil {
			return err
		}

		path := filepath.Join(root, approve.ClaudeSettings)

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		settings := map[string]any{}
		if err := json.Unmarshal(data, &settings); err != nil {
			return err
		}

		settings["disableAllHooks"] = true

		if data, err = json.Marshal(settings); err != nil {
			return err
		}

		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}

		if !brokenLocal {
			return nil
		}

		return writeFile(".claude/settings.local.json", "{not json\n")(root)
	}

	return off
}

func TestRunGivesEveryActionOfAClientLine(t *testing.T) {
	cases := map[string]struct {
		write  func(root string) error
		client string
		want   string
	}{
		// The trust action is part of the second warning action, so it
		// does not repeat.
		"codex gate in two forms": {
			writeFile(".codex/hooks.json", codexTwoGates), "codex",
			"remove that handler from .codex/hooks.json; " +
				"run `seamark init --client codex`, which replaces that handler with the managed gate hook; " +
				"then open the project in Codex and review the hooks with /hooks",
		},
		// The matcher edit changes the trust hash, so the trust review
		// comes after it.
		"codex gate under a matcher that never fires": {
			writeFile(".codex/hooks.json", codexIdleGate), "codex",
			`set the matcher of that hook to "Bash" in .codex/hooks.json; ` +
				"open the project in Codex and review the hooks with /hooks",
		},
		"claude gate and lessons matchers": {
			writeFile(".claude/settings.json", claudeTwoMatchers), "claude",
			`set the matcher of that hook to "Bash" in .claude/settings.json; ` +
				`set the matcher of that hook to "Edit|Write|MultiEdit" in .claude/settings.json`,
		},
		"claude hooks off beside an unreadable personal file": {
			hooksOff(true).Write, "claude",
			"fix the file, then run the command again; " +
				"remove disableAllHooks, or set it to false, to run the hooks",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stubAgent(t)

			root, dbPath := fixtureRoot(t)
			require.NoError(t, tc.write(root))

			client := byName(Run(root, dbPath, "test"))[tc.client]

			assert.Equal(t, StateWarn, client.State)
			assert.Equal(t, tc.want, client.Fix)
		})
	}
}

func TestRunGivesEveryActionThatStatusPrints(t *testing.T) {
	// Status prints the action of each finding and each hook entry. The
	// doctor fix of the same line must hold each of those actions.
	type shape struct {
		name  string
		write func(root string) error
	}

	shapes := []shape{
		{"codex gate in two forms", writeFile(".codex/hooks.json", codexTwoGates)},
		{"codex gate under a matcher that never fires", writeFile(".codex/hooks.json", codexIdleGate)},
		{"claude gate and lessons matchers", writeFile(".claude/settings.json", claudeTwoMatchers)},
		{"claude gate without lessons", writeFile(".claude/settings.json", claudeGateOnly)},
		{"claude hooks off beside an unreadable personal file", hooksOff(true).Write},
	}

	for _, f := range inspecttest.Fixtures() {
		shapes = append(shapes, shape{"fixture " + f.Name, f.Write})
	}

	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			stubAgent(t)

			root, dbPath := fixtureRoot(t)
			require.NoError(t, sh.write(root))

			reg := inspecttest.Registry()
			checks := byName(run(reg, root, dbPath, "test"))

			for _, insp := range reg.Inspect(root) {
				for _, finding := range insp.Findings {
					if finding.Action == "" {
						continue
					}

					assert.Contains(t, checks[insp.ClientID].Fix, render.Sanitize(oneLine(finding.Action)), insp.ClientID)
				}

				for _, capability := range []integration.Capability{integration.CapabilityCommands, integration.CapabilityEdits} {
					entry, _ := insp.Entry(capability)
					if entry.Action == "" || (entry.State != integration.StatePartial && entry.State != integration.StateUnreadable) {
						continue
					}

					assert.Contains(t, checks["hooks"].Fix, render.Sanitize(oneLine(entry.Action)), insp.ClientID)
				}
			}
		})
	}
}

func TestRunWarnsAboutTwoManagedCodexGateHandlers(t *testing.T) {
	// Codex runs both handlers, so the codex line warns. Each handler runs
	// the gate, so the hooks line stays ok.
	root, dbPath := fixtureRoot(t)
	gate := `{"matcher":"Bash","hooks":[{"type":"command","command":"/bin/seamark gate --hook --client codex"}]}`
	document := `{"hooks":{"PreToolUse":[` + gate + `,` + gate + `,` +
		`{"matcher":"apply_patch","hooks":[{"type":"command","command":"/bin/seamark lessons --hook --client codex"}]}]}}`

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "hooks.json"), []byte(document), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateOK, checks["hooks"].State)
	assert.Equal(t, StateWarn, checks["codex"].State)
	assert.Contains(t, checks["codex"].Detail, "so the hook runs twice")
	assert.Contains(t, checks["codex"].Fix, "keep one of those handlers")
}

func TestRunDetectsMissingAgentBinary(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"no-such-agent-binary-xyz\"]\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["agent"].State)
	assert.Contains(t, checks["agent"].Detail, "no-such-agent-binary-xyz")
}

func TestRunResolvesTheCodexAgentThroughTheRegistry(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: codex\n"), 0o644))

	// Report Codex as missing when it is absent from PATH.
	t.Setenv("PATH", t.TempDir())

	checks := byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["agent"].State)
	assert.Contains(t, checks["agent"].Detail, `"codex" not found on PATH`)

	// Finding Codex on PATH is enough; it need not run successfully.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", dir)

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateOK, checks["agent"].State)
	assert.Contains(t, checks["agent"].Detail, "codex on PATH")

	// Unknown-client errors list valid choices.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  cli: hal9000\n"), 0o644))

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["agent"].State)
	assert.Contains(t, checks["agent"].Detail, "known: claude, codex")
}

func TestRunGitignoreUndeterminedOutsideGit(t *testing.T) {
	// No git repository: ignore status cannot be determined, and an
	// undetermined status must never masquerade as a clean OK.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: warn\n"), 0o644))

	r := Run(root, store.DefaultPath(root), "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["gitignore"].State)
	assert.Contains(t, checks["gitignore"].Detail, "could not determine")
}

func TestPrintAndJSON(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	r := Run(root, dbPath, "test")

	var b bytes.Buffer
	Print(&b, r)
	assert.Contains(t, b.String(), "installation health")
	assert.Contains(t, b.String(), "seamark test", "the binary line is host-independent")

	data, err := json.Marshal(r)
	require.NoError(t, err)

	var back Report
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, r, &back, "the report must survive the JSON round trip")
}

func TestPrintKeepsAJoinedFixOnOneLine(t *testing.T) {
	// The steps of a joined fix run in the printed order, so Print keeps
	// them on one arrow line, the same as the JSON field.
	r := &Report{}
	r.add("codex", StateWarn, "two warnings", joinActions([]string{"remove that handler", "then review the hooks"}))

	var b bytes.Buffer
	Print(&b, r)

	assert.Equal(t, 1, strings.Count(b.String(), "→"), b.String())
	assert.Contains(t, b.String(), "→ remove that handler; then review the hooks\n")
}

// installSkills writes the shipped skills into one client directory of
// the fixture, the way `seamark init --skills=<client>` would.
func installSkills(t *testing.T, root, mode string) {
	t.Helper()

	targets, err := skills.Targets(root, mode)
	require.NoError(t, err)
	require.NoError(t, skills.Install(&bytes.Buffer{}, root, targets, false))
}

func TestRunReportsSkillsNotInstalled(t *testing.T) {
	const route = "run `seamark init --skills` to add the seamark agent skills"

	// A bare --skills writes .agents/skills only when .agents/ exists, so
	// the route names Codex only then. The install must then give each
	// named client its current skills.
	cases := map[string]struct {
		agentsDir bool
		readers   []string
	}{
		"without .agents": {false, []string{"Claude Code"}},
		"with .agents":    {true, []string{"Claude Code", "Codex"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root, dbPath := fixtureRoot(t)

			if tc.agentsDir {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".agents"), 0o755))
			}

			skillsLine := byName(Run(root, dbPath, "test"))["skills"]

			assert.Equal(t, StateInfo, skillsLine.State, "opt-in skills are a fact, not a fault")
			assert.Contains(t, skillsLine.Detail, "not installed")
			assert.Equal(t, route+" for "+strings.Join(tc.readers, " and "), skillsLine.Fix)

			installSkills(t, root, skills.ModeAuto)

			skillsLine = byName(Run(root, dbPath, "test"))["skills"]
			assert.Equal(t, StateOK, skillsLine.State, skillsLine.Detail)

			ids := map[string]string{"Claude Code": "claude", "Codex": "codex"}
			for _, reader := range tc.readers {
				assert.Contains(t, skillsLine.Detail, ids[reader]+" 3/3 current")
			}
		})
	}
}

func TestRunNamesTheSkillReadersOfTheRoute(t *testing.T) {
	const route = "run `seamark init --skills` to add the seamark agent skills"

	// A test client that reads .agents/skills is a reader too.
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".agents"), 0o755))

	checks := byName(run(inspecttest.Registry(), root, dbPath, "test"))
	assert.Equal(t, route+" for Claude Code and Codex and Shared Agent", checks["skills"].Fix)

	// Without .agents/, the install writes no directory that the test
	// client reads, so the route names no client.
	reg, err := integration.NewRegistry(inspecttest.SharedClient())
	require.NoError(t, err)

	root, dbPath = fixtureRoot(t)
	checks = byName(run(reg, root, dbPath, "test"))
	assert.Equal(t, route, checks["skills"].Fix)
}

func TestRunReportsSkillsInstalled(t *testing.T) {
	stubAgent(t)

	root, dbPath := fixtureRoot(t)
	installSkills(t, root, skills.ModeClaude)

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateOK, checks["skills"].State, checks["skills"].Detail)
	assert.Contains(t, checks["skills"].Detail, "claude 3/3 current")
	assert.Contains(t, checks["skills"].Detail, "codex not installed")
	assert.Empty(t, checks["skills"].Fix)
	assert.Zero(t, r.Warns, "%+v", r.Checks)
}

func TestRunDetectsStaleSkills(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	installSkills(t, root, skills.ModeClaude)

	// A managed copy whose body drifted, as after a seamark upgrade.
	skillMD := filepath.Join(root, ".claude", "skills", "seamark-plan-change", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD,
		[]byte("---\nname: seamark-plan-change\nmetadata:\n  seamark: managed\n---\nold body\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "1 stale")
	assert.Contains(t, checks["skills"].Fix, "seamark init --skills")
}

func TestRunIgnoresForeignSkillDir(t *testing.T) {
	// The conflict route is about a foreign directory, not about the
	// install. So it keeps the adapter text and names no client.
	const route = "a directory under a seamark skill name is not seamark's; rename or remove it, " +
		"then run `seamark init --skills` to install the shipped skill"

	stubAgent(t)

	root, dbPath := fixtureRoot(t)

	// The user's own skill under a shipped name, nothing else installed:
	// named, never a warning, never touched.
	dir := filepath.Join(root, ".claude", "skills", "seamark-plan-change")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: seamark-plan-change\ndescription: mine\n---\nMine.\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "1 not managed")
	assert.Equal(t, route, checks["skills"].Fix,
		"init --skills alone cannot replace the directory, so the fix must say so first")
	assert.Zero(t, r.Warns, "%+v", r.Checks)

	// Beside a complete managed install, the foreign directory is still
	// only information, with the corrective choice spelled out.
	installSkills(t, root, skills.ModeCodex)

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateInfo, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "codex 3/3 current")
	assert.Equal(t, route, checks["skills"].Fix)
}

func TestRunWarnsOnUnreadableSkillDir(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "skills"), []byte("oops"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "codex unreadable")
}

// approveClaude writes the eight Claude Code allow rules into the fixture.
func approveClaude(t *testing.T, root string) {
	t.Helper()

	rules, err := approve.ClaudeRules()
	require.NoError(t, err)

	quoted := make([]string, 0, len(rules))
	for _, r := range rules {
		quoted = append(quoted, `"`+r+`"`)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":[`+strings.Join(quoted, ",")+`]}}`), 0o644))
}

func TestRunReportsApprovalsNotConfigured(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateInfo, checks["approvals"].State, "opt-in approvals are a fact, not a fault")
	assert.Contains(t, checks["approvals"].Detail, "not configured")
	assert.Contains(t, checks["approvals"].Fix, "seamark init --approve-tools")
	assert.Contains(t, checks["approvals"].Fix, "user or managed policy")
}

func TestRunReportsApprovalsConfigured(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	approveClaude(t, root)

	p, err := approve.PlanCodex(root)
	require.NoError(t, err)
	require.NoError(t, approve.ApplyCodex(&bytes.Buffer{}, root, p, false))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateOK, checks["approvals"].State, checks["approvals"].Detail)
	assert.Contains(t, checks["approvals"].Detail, "claude 8/8 rules")
	assert.Contains(t, checks["approvals"].Detail, "codex registered as \"seamark\", 5/5 tools approved")
	assert.Contains(t, checks["approvals"].Detail, "project configuration")
}

func TestRunDetectsPartialApprovals(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["mcp__seamark__orient"]}}`), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "claude 1/8 rules")
	assert.Contains(t, checks["approvals"].Fix, "seamark init --approve-tools")
}

func TestRunReportsConflictingAndUnreadableApprovals(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"prompt\"\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "tools.why.approval_mode = \"prompt\"")
	assert.Contains(t, checks["approvals"].Fix, "by hand")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("not toml [\n"), 0o644))

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "codex unreadable")
}

func TestRunReportsDisabledCodexServerAsConflict(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nenabled = false\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"approve\"\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["approvals"].State, "a disabled server must not read as approved")
	assert.Contains(t, checks["approvals"].Detail, "enabled = false")
}

func TestRunWarnsOnARegistrationWithoutApprovals(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// Codex registered, nothing approved: every Codex call prompts, so
	// the re-run hint is due even when Claude Code is complete.
	approveClaude(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["approvals"].State, checks["approvals"].Detail)
	assert.Contains(t, checks["approvals"].Detail, `codex registered as "seamark", 0/5 tools approved`)
	assert.Contains(t, checks["approvals"].Fix, "seamark init --approve-tools")

	// A denied Claude Code rule is a conflict doctor warns about, not an
	// approval it counts.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["mcp__seamark__check"],"deny":["mcp__seamark__check"]}}`), 0o644))

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "permissions.deny lists mcp__seamark__check")
}

func TestRunNamesTheBrokenMCPConfigOnBothLines(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	approveClaude(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("{not json"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	// The server name in the file spells every rule, so the approvals
	// count is unknowable until the file parses; both lines say so.
	assert.Equal(t, StateWarn, checks["mcp"].State)
	assert.Contains(t, checks["mcp"].Detail, "claude unreadable (.mcp.json")
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "claude unreadable (.mcp.json")
}

func TestRunNamesTheSameServerOnBothLines(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mcp.json"),
		[]byte(`{"mcpServers":{"zz":{"command":"seamark"},"sm":{"command":"seamark"}}}`), 0o644))

	for i := 0; i < 10; i++ {
		checks := byName(Run(root, dbPath, "test"))

		assert.Equal(t, StateOK, checks["mcp"].State)
		assert.Contains(t, checks["mcp"].Detail, `registered in .mcp.json as "sm"`, "name order, never map order")
		assert.Contains(t, checks["approvals"].Detail, `for server "sm"`)
	}
}

// runFixture writes one matrix fixture into a healthy workspace and
// runs doctor over the matrix registry.
func runFixture(t *testing.T, f inspecttest.Fixture) (report *Report, checks map[string]Check) {
	t.Helper()

	stubAgent(t)

	root, dbPath := fixtureRoot(t)
	require.NoError(t, f.Write(root))

	r := run(inspecttest.Registry(), root, dbPath, "test")

	return r, byName(r)
}

func TestRunFollowsTheInspectionMatrix(t *testing.T) {
	// One fixture matrix for init, doctor, and status: every word the
	// client's state renders to appears on doctor's topical lines, and
	// every limitation the adapter reports appears on the client's line.
	for _, f := range inspecttest.Fixtures() {
		t.Run(f.Name, func(t *testing.T) {
			r, checks := runFixture(t, f)
			assert.Zero(t, r.Fails, "%+v", r.Checks)

			topical := checks["hooks"].Detail + " · " + checks["mcp"].Detail + " · " +
				checks["approvals"].Detail + " · " + checks["skills"].Detail

			for _, word := range f.Words {
				assert.Contains(t, topical, word)
			}

			if f.Skills != "" {
				assert.Contains(t, checks["skills"].Detail, f.Skills)
			}

			client, ok := checks[f.Client]
			for _, reason := range f.Findings {
				require.True(t, ok, "a client with a limitation gets its own line")
				assert.Contains(t, client.Detail, reason)
			}

			if f.InvokerMissing {
				assert.Equal(t, StateWarn, checks["agent"].State)
				assert.Contains(t, checks["agent"].Detail, "invoker "+f.Invoker)
			}

			var b bytes.Buffer
			Print(&b, r)

			for _, secret := range f.Absent {
				assert.NotContains(t, b.String(), secret, "a credential in a hook command never reaches the report")
			}
		})
	}
}

func TestRunReportsTheEffectiveGateModeAcrossSources(t *testing.T) {
	// A managed warn hook beside an enforcing inline hook: the hooks
	// line reports enforce, as init's gate line does, because the
	// enforcing definition blocks whatever the managed one says.
	_, checks := runFixture(t, inspecttest.Named("inline enforce"))
	assert.Contains(t, checks["hooks"].Detail, "codex gate (enforce) + lessons hooks installed")
	assert.Equal(t, StateWarn, checks["codex"].State, "the duplicate handler is a warning")
	assert.Contains(t, checks["codex"].Detail, "runs twice")

	// A wrapped enforcing gate in the personal Claude Code file alone.
	_, checks = runFixture(t, inspecttest.Named("local wrapper"))
	assert.Contains(t, checks["hooks"].Detail, "claude gate hook installed (enforce), lessons hook missing")
	assert.Contains(t, checks["claude"].Detail, ".claude/settings.local.json: runs the seamark gate hook")
}

func TestRunNamesTheCodexLimitationsOnItsOwnLine(t *testing.T) {
	_, checks := runFixture(t, inspecttest.Named("pending trust"))

	// The topical line covers both clients; the client line carries the
	// native evidence and the limitations, as facts, with the trust
	// instruction as the action.
	assert.Equal(t, StateOK, checks["hooks"].State, checks["hooks"].Detail)
	assert.Contains(t, checks["hooks"].Detail, "claude no hooks installed · codex gate (warn) + lessons hooks installed")

	codex := checks["codex"]
	assert.Equal(t, StateInfo, codex.State)
	assert.Contains(t, codex.Detail, "edits: natively verified on codex-cli 0.154.0 (PreToolUse apply_patch)")
	assert.Contains(t, codex.Detail, "commands: native check pending (PreToolUse Bash)")
	assert.Contains(t, codex.Detail, ".codex/hooks.json: Codex runs a project hook only after the user reviews and trusts it")
	assert.Contains(t, codex.Detail, "once-per-context")
	assert.Equal(t, "open the project in Codex and review the hooks with /hooks", codex.Fix)

	// Hooks turned off in the project configuration is a warning: the
	// installed hooks never run.
	_, checks = runFixture(t, inspecttest.Named("disabled"))
	assert.Equal(t, StateWarn, checks["codex"].State)
	assert.Contains(t, checks["codex"].Detail, "turns every Codex hook off")
	assert.Contains(t, checks["codex"].Fix, "hooks = true")
}

func TestRunWarnsWhenClaudeHooksAreTurnedOff(t *testing.T) {
	_, want := runFixture(t, inspecttest.Named("current"))
	_, checks := runFixture(t, hooksOff(false))

	// The installed hooks never run: the client line warns, and the hooks
	// line stays as it is.
	assert.Equal(t, want["hooks"], checks["hooks"])
	assert.Equal(t, StateWarn, checks["claude"].State)
	assert.Contains(t, checks["claude"].Detail, ".claude/settings.json: disableAllHooks: true turns off the project hooks of Claude Code")
	assert.Contains(t, checks["claude"].Fix, "remove disableAllHooks, or set it to false")
}

func TestRunWarnsOnAHalfInstalledClient(t *testing.T) {
	// Codex with the lessons hook and no gate hook: the hooks line warns
	// and names the client command that restores the missing half.
	_, checks := runFixture(t, inspecttest.Named("partial"))

	assert.Equal(t, StateWarn, checks["hooks"].State)
	assert.Contains(t, checks["hooks"].Detail, "codex lessons hook installed, gate hook missing")
	assert.Contains(t, checks["hooks"].Fix, "seamark init --client codex")

	// A personal copy of the managed handler with other fields: the client
	// line warns that setup cannot name the fields that Claude Code runs.
	_, checks = runFixture(t, inspecttest.Named("duplicate"))
	assert.Equal(t, StateWarn, checks["claude"].State)
	assert.Contains(t, checks["claude"].Detail, "Claude Code runs it once, with the fields of one copy that setup cannot name")
	assert.NotContains(t, checks["claude"].Detail, "runs twice")
}

func TestRunSkipsTheClientLineWhenNothingIsInstalled(t *testing.T) {
	_, checks := runFixture(t, inspecttest.Named("absent"))

	_, claude := checks["claude"]
	_, codex := checks["codex"]
	assert.False(t, claude, "nothing installed, nothing to report")
	assert.False(t, codex)

	// The shared client always has its limitation to report.
	shared, ok := checks["shared"]
	require.True(t, ok)
	assert.Equal(t, StateInfo, shared.State)
	assert.Contains(t, shared.Detail, "waits for the user to approve")
}
