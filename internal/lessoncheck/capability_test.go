package lessoncheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of the capability report. CapabilityReportOf is the only
// place that computes a path state, a badge, and an uncovered writer, so
// these tests walk every capability reason and every badge.

// capabilityYAML holds an enforce pin with a forbid check, a check pin
// with a pattern check, an advisory pin with a check, and a plain
// advisory pin.
const capabilityYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated/
    mode: enforce
    check: frozen
  - rule: no-todo-in-src
    region: src/
    mode: check
    check: no-todo
  - rule: advisory-with-check
    region: docs/
    check: frozen
  - rule: plain-advisory
    region: lib/
`

// capabilityConfig parses a configuration that must validate.
func capabilityConfig(t *testing.T, text string) *Config {
	t.Helper()

	cfg, errs, err := Parse([]byte(text))
	require.NoError(t, err)
	require.Empty(t, errs)

	return cfg
}

// The markers of the fixture client.
const (
	testGuard    = MarkerGuard
	testBaseline = MarkerBaseline
	testVerify   = "lessons --verify --hook"
)

// capTools are the covered tools of the fixture client.
var capTools = []string{"Edit", "Write"}

// installedPaths returns installed pre-write and post-edit paths of one
// client for every tool and context. pre is the pre-write marker, or ""
// for no pre-write hook; post says whether the post-edit hook exists.
// Each path has the given evidence level and binary state.
func installedPaths(client, pre string, post bool, level, binary string) []InstalledPath {
	stages := []struct {
		stage     HookStage
		marker    string
		installed bool
		level     string
	}{
		{StagePreWrite, pre, pre != "", level},
		{StagePostEdit, testVerify, post, "unverified"},
	}

	var out []InstalledPath

	for _, s := range stages {
		for _, tool := range capTools {
			for _, context := range []PathContext{ContextRoot, ContextChild} {
				p := InstalledPath{Client: client, Tool: tool, Stage: s.stage, Context: context,
					Verification: CapVerification{Level: s.level}}
				if s.installed {
					p.Installed, p.Marker, p.BinaryState = true, s.marker, binary
				} else {
					p.Reasons = []CapReason{CapNotInstalled}
				}

				out = append(out, p)
			}
		}
	}

	return out
}

// pathOf returns the report path of one tool, stage, and context.
func pathOf(t *testing.T, r CapabilityReport, client, tool string, stage HookStage, context PathContext) PathCapability {
	t.Helper()

	c, ok := r.Client(client)
	require.True(t, ok, "client %s", client)

	for _, p := range c.Paths {
		if p.Tool == tool && p.HookStage == stage && p.Context == context {
			return p
		}
	}

	require.Failf(t, "no path", "%s %s %s %s", client, tool, stage, context)

	return PathCapability{}
}

// badgeOfPin returns the badge of a pin for one client.
func badgeOfPin(t *testing.T, r CapabilityReport, ref, client string) PinClient {
	t.Helper()

	pin, ok := r.Pin(ref)
	require.True(t, ok, "pin %s", ref)

	for _, c := range pin.Clients {
		if c.Client == client {
			return c
		}
	}

	require.Failf(t, "no badge", "pin %s client %s", ref, client)

	return PinClient{}
}

// TestLessonCheckCapabilityReportPathStates walks every capability
// reason: the source of each reason and its effect on the path state.
// The most restrictive state wins, and a path without verified evidence
// is at most active-unverified.
func TestLessonCheckCapabilityReportPathStates(t *testing.T) {
	cfg := capabilityConfig(t, capabilityYAML)

	one := func(p InstalledPath) InstalledPath {
		p.Client, p.Tool, p.Context = "claude", "Edit", ContextRoot
		if p.Stage == "" {
			p.Stage = StagePreWrite
		}

		return p
	}

	report := func(errs []ConfigError, paths ...InstalledPath) CapabilityReport {
		return CapabilityReportOf(cfg, errs, paths, nil)
	}

	installedBaseline := InstalledPath{Installed: true, Marker: testBaseline, BinaryState: BinaryCurrent,
		Verification: CapVerification{Level: "pending"}}

	rows := []struct {
		name    string
		path    InstalledPath
		state   PathState
		reasons []CapReason
	}{
		{"not-installed", InstalledPath{Reasons: []CapReason{CapNotInstalled}}, PathInactive, []CapReason{CapNotInstalled}},
		{"report-only-wrapper", withReasons(installedBaseline, CapReportOnlyWrapper), PathInactive, []CapReason{CapReportOnlyWrapper}},
		{"untrusted", withReasons(installedBaseline, CapUntrusted), PathInactive, []CapReason{CapUntrusted}},
		{"trust-unknown", withReasons(installedBaseline, CapTrustUnknown), PathActiveUnverified, []CapReason{CapTrustUnknown}},
		{"hooks-disabled", withReasons(installedBaseline, CapHooksDisabled), PathInactive, []CapReason{CapHooksDisabled}},
		{"document-unreadable", InstalledPath{Reasons: []CapReason{CapDocumentUnreadable}}, PathInactive, []CapReason{CapDocumentUnreadable}},
		{"partial-install leaves the state to the other reasons", withReasons(installedBaseline, CapPartialInstall),
			PathActiveUnverified, []CapReason{CapPartialInstall}},
		{"other binary", withBinary(installedBaseline, BinaryOther), PathActiveUnverified, []CapReason{CapOtherBinary}},
		{"old binary", withBinary(installedBaseline, BinaryOld), PathInactive, []CapReason{CapVersionOld, CapOtherBinary}},
		{"unknown binary", withBinary(installedBaseline, BinaryUnknown), PathInactive,
			[]CapReason{CapCapabilityUnknown, CapOtherBinary}},
		{"missing binary", withBinary(installedBaseline, BinaryMissing), PathInactive, []CapReason{CapBinaryMissing, CapOtherBinary}},
		{"an unread binary state reads as unknown", withBinary(installedBaseline, ""), PathInactive,
			[]CapReason{CapCapabilityUnknown, CapOtherBinary}},
		{"native deny and timeout without a verified recording", InstalledPath{Installed: true, Marker: testGuard,
			BinaryState: BinaryCurrent, Verification: CapVerification{Level: "pending"}}, PathActiveUnverified,
			[]CapReason{CapNativeDenyUnverified, CapNativeTimeoutUnverified}},
		{"a verified guard without a reason is active", InstalledPath{Installed: true, Marker: testGuard,
			BinaryState: BinaryCurrent, Verification: CapVerification{Level: LevelVerified}}, PathActive, nil},
		{"tool-uncovered on a covered tool", InstalledPath{Marker: testGuard, Reasons: []CapReason{CapToolUncovered}},
			PathInactive, []CapReason{CapToolUncovered}},
		{"an evidence level below verified caps the state without a reason", InstalledPath{Stage: StagePostEdit,
			Installed: true, Marker: testVerify, BinaryState: BinaryCurrent, Verification: CapVerification{Level: "unverified"}},
			PathActiveUnverified, nil},
		{"a path that the client cannot run", InstalledPath{Stage: StageExploration, Verification: CapVerification{Level: "unknown"}},
			PathUnsupported, nil},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			p := one(row.path)
			got := pathOf(t, report(nil, p), "claude", "Edit", p.Stage, ContextRoot)
			assert.Equal(t, row.state, got.State)
			assert.Equal(t, row.reasons, got.Reasons, "sorted and deduplicated")
		})
	}

	t.Run("check-only-installed changes no path and gives the enforce pin its badge", func(t *testing.T) {
		r := report(nil, installedPaths("claude", testBaseline, true, "pending", BinaryCurrent)...)

		pre := pathOf(t, r, "claude", "Edit", StagePreWrite, ContextRoot)
		assert.Equal(t, PathActiveUnverified, pre.State)
		assert.Empty(t, pre.Reasons, "a baseline never denies, so no native-deny reason")

		enforce := cfg.Pins[0]
		assert.Equal(t, PinClient{Client: "claude", Badge: BadgeRequestedInactive, Reasons: []CapReason{CapCheckOnlyInstalled}},
			badgeOfPin(t, r, enforce.Ref, "claude"))
		assert.Equal(t, BadgeChecked, badgeOfPin(t, r, cfg.Pins[1].Ref, "claude").Badge)

		c, _ := r.Client("claude")
		assert.Equal(t, InstalledCheck, c.Installed)
	})

	t.Run("partial-install makes the mode partial and the missing path not-installed", func(t *testing.T) {
		paths := installedPaths("claude", testGuard, false, "pending", BinaryCurrent)
		for i := range paths {
			if paths[i].Installed {
				paths[i].Reasons = append(paths[i].Reasons, CapPartialInstall)
			}
		}

		r := report(nil, paths...)

		c, _ := r.Client("claude")
		assert.Equal(t, InstalledPartial, c.Installed)
		assert.Equal(t, PathInactive, pathOf(t, r, "claude", "Edit", StagePostEdit, ContextRoot).State)
		assert.Contains(t, pathOf(t, r, "claude", "Edit", StagePreWrite, ContextRoot).Reasons, CapPartialInstall)
	})

	t.Run("content-unprojectable holds only for an installed guard and an enforce pattern pin", func(t *testing.T) {
		guard := one(InstalledPath{Installed: true, Marker: testGuard, BinaryState: BinaryCurrent,
			Verification: CapVerification{Level: "pending"}, Reasons: []CapReason{CapContentUnprojectable}})

		got := pathOf(t, report(nil, guard), "claude", "Edit", StagePreWrite, ContextRoot)
		assert.NotContains(t, got.Reasons, CapContentUnprojectable, "the enforce pin of the fixture binds a forbid check")

		pattern := capabilityConfig(t, capabilityYAML+"  - rule: no-todo-enforced\n    region: src/\n    mode: enforce\n    check: no-todo\n")
		got = pathOf(t, CapabilityReportOf(pattern, nil, []InstalledPath{guard}, nil), "claude", "Edit", StagePreWrite, ContextRoot)
		assert.Equal(t, PathActiveUnverified, got.State)
		assert.Equal(t, []CapReason{CapContentUnprojectable, CapNativeDenyUnverified, CapNativeTimeoutUnverified}, got.Reasons)

		baseline := one(withReasons(installedBaseline, CapContentUnprojectable))
		got = pathOf(t, CapabilityReportOf(pattern, nil, []InstalledPath{baseline}, nil), "claude", "Edit", StagePreWrite, ContextRoot)
		assert.NotContains(t, got.Reasons, CapContentUnprojectable, "a baseline denies nothing")
	})

	t.Run("configuration-error makes every pre-write and post-edit path inactive", func(t *testing.T) {
		errs := []ConfigError{{Code: CodeVersionUnsupported, Subject: subjectVersion, Message: "too new"}}
		paths := append(installedPaths("claude", testGuard, true, "pending", BinaryCurrent),
			one(InstalledPath{Stage: StageExploration, Installed: true, Marker: "lessons --explore --hook",
				BinaryState: BinaryCurrent, Verification: CapVerification{Level: "unverified"}}))

		r := report(errs, paths...)

		for _, stage := range []HookStage{StagePreWrite, StagePostEdit} {
			got := pathOf(t, r, "claude", "Write", stage, ContextChild)
			assert.Equal(t, PathInactive, got.State, "%s", stage)
			assert.Contains(t, got.Reasons, CapConfigurationError)
		}

		explore := pathOf(t, r, "claude", "Edit", StageExploration, ContextRoot)
		assert.Equal(t, PathActiveUnverified, explore.State, "advisory readers keep their pins")
		assert.False(t, r.Config.Valid)
		assert.Equal(t, errs, r.Config.Errors)

		assert.Equal(t, BadgeRequestedInactive, badgeOfPin(t, r, cfg.Pins[0].Ref, "claude").Badge)
		assert.Contains(t, badgeOfPin(t, r, cfg.Pins[0].Ref, "claude").Reasons, CapConfigurationError)
	})

	t.Run("tool-uncovered on a writer that no lesson-check hook covers lists an uncovered writer and no path", func(t *testing.T) {
		writer := InstalledPath{Client: "claude", Tool: "NotebookEdit", Context: ContextChild, Reasons: []CapReason{CapToolUncovered}}
		r := report(nil, append(installedPaths("claude", "", false, "pending", ""), writer)...)

		assert.Equal(t, []UncoveredWriter{{Client: "claude", Tool: "NotebookEdit", Context: ContextChild}}, r.Uncovered)

		c, _ := r.Client("claude")
		for _, p := range c.Paths {
			assert.NotEqual(t, "NotebookEdit", p.Tool)
		}
	})
}

// withReasons returns a copy of a path with more source reasons.
func withReasons(p InstalledPath, reasons ...CapReason) InstalledPath {
	p.Reasons = append(append([]CapReason{}, p.Reasons...), reasons...)

	return p
}

// withBinary returns a copy of a path with another binary state.
func withBinary(p InstalledPath, state string) InstalledPath {
	p.BinaryState = state

	return p
}

// TestLessonCheckCapabilityReportBadges proves every badge, the
// installed modes, the client order, the copied attributions, the
// configuration part, and the active texts. No path gives enforced
// before its evidence is verified.
func TestLessonCheckCapabilityReportBadges(t *testing.T) {
	cfg := capabilityConfig(t, capabilityYAML)
	enforce, check, withCheck := cfg.Pins[0], cfg.Pins[1], cfg.Pins[2]

	t.Run("enforced needs every pre-write path active with verified evidence", func(t *testing.T) {
		r := CapabilityReportOf(cfg, nil, installedPaths("claude", testGuard, true, LevelVerified, BinaryCurrent), nil)
		assert.Equal(t, PinClient{Client: "claude", Badge: BadgeEnforced}, badgeOfPin(t, r, enforce.Ref, "claude"))

		c, _ := r.Client("claude")
		assert.Equal(t, InstalledEnforce, c.Installed)
		assert.Equal(t, []string{"active: claude enforced · pre-write Edit, Write active"}, r.ActiveLines(enforce.Ref))
		assert.Equal(t, "requested; claude enforced", r.ActiveText(enforce.Ref))
	})

	t.Run("an enforced badge names the limit of its paths", func(t *testing.T) {
		const limit = "the client lets the write run after its 10 s hook timeout"

		paths := installedPaths("claude", testGuard, true, LevelVerified, BinaryCurrent)
		for i := range paths {
			if paths[i].Stage == StagePreWrite {
				paths[i].Verification.Limit = limit
			}
		}

		r := CapabilityReportOf(cfg, nil, paths, nil)
		assert.Equal(t, PinClient{Client: "claude", Badge: BadgeEnforced, Limits: []string{limit}},
			badgeOfPin(t, r, enforce.Ref, "claude"))
		assert.Equal(t, []string{
			"active: claude enforced (limit: " + limit + ") · pre-write Edit, Write active (limit: " + limit + ")",
		}, r.ActiveLines(enforce.Ref))
		assert.Equal(t, "requested; claude enforced (limit: "+limit+")", r.ActiveText(enforce.Ref))

		c, _ := r.Client("claude")
		assert.Equal(t, []string{limit}, c.Limits(StagePreWrite))
		assert.Empty(t, c.Limits(StagePostEdit))

		// One path without the limit splits the group, so the text never
		// claims the limit for a path that has none, or the reverse.
		paths[0].Verification.Limit = ""
		c, _ = CapabilityReportOf(cfg, nil, paths, nil).Client("claude")
		assert.Equal(t, "pre-write Edit in root active; Edit in child, Write active (limit: "+limit+")",
			c.StageText(StagePreWrite))
	})

	t.Run("an unverified path gives no limit", func(t *testing.T) {
		paths := installedPaths("claude", testGuard, true, "pending", BinaryCurrent)
		paths[0].Verification.Limit = "a limit"

		r := CapabilityReportOf(cfg, nil, paths, nil)
		c, _ := r.Client("claude")
		assert.Empty(t, c.Limits(StagePreWrite), "only a verified claim has a limit")
		assert.Empty(t, badgeOfPin(t, r, enforce.Ref, "claude").Limits)
		assert.NotContains(t, c.StageText(StagePreWrite), "limit")
	})

	t.Run("enforced-unverified while the native deny is unverified", func(t *testing.T) {
		r := CapabilityReportOf(cfg, nil, installedPaths("claude", testGuard, true, "pending", BinaryCurrent), nil)

		want := PinClient{Client: "claude", Badge: BadgeEnforcedUnverified,
			Reasons: []CapReason{CapNativeDenyUnverified, CapNativeTimeoutUnverified}}
		assert.Equal(t, want, badgeOfPin(t, r, enforce.Ref, "claude"))
		assert.Equal(t, []string{
			"active: claude enforced-unverified (native-deny-unverified, native-timeout-unverified) · " +
				"pre-write Edit, Write active-unverified",
		}, r.ActiveLines(enforce.Ref))
	})

	t.Run("one inactive context gives no enforced badge", func(t *testing.T) {
		paths := installedPaths("claude", testGuard, true, LevelVerified, BinaryCurrent)
		paths[1].Reasons = []CapReason{CapReportOnlyWrapper} // Edit in the child context

		r := CapabilityReportOf(cfg, nil, paths, nil)
		got := badgeOfPin(t, r, enforce.Ref, "claude")
		assert.Equal(t, BadgeRequestedInactive, got.Badge)
		assert.Equal(t, []CapReason{CapReportOnlyWrapper}, got.Reasons)

		c, _ := r.Client("claude")
		assert.Equal(t, "pre-write Edit in root, Write active; Edit in child inactive (report-only-wrapper)",
			c.StageText(StagePreWrite), "one group per state; a tool whose contexts differ names each context")
	})

	t.Run("an unsupported pre-write path gives no enforced badge", func(t *testing.T) {
		paths := installedPaths("claude", testGuard, true, LevelVerified, BinaryCurrent)
		paths[0] = InstalledPath{Client: "claude", Tool: "Edit", Stage: StagePreWrite, Context: ContextRoot}

		r := CapabilityReportOf(cfg, nil, paths, nil)
		assert.Equal(t, BadgeRequestedInactive, badgeOfPin(t, r, enforce.Ref, "claude").Badge)
	})

	t.Run("checked needs every post-edit path active or active-unverified", func(t *testing.T) {
		r := CapabilityReportOf(cfg, nil, installedPaths("claude", testBaseline, true, "pending", BinaryCurrent), nil)
		assert.Equal(t, PinClient{Client: "claude", Badge: BadgeChecked}, badgeOfPin(t, r, check.Ref, "claude"))
		assert.Equal(t, []string{"active: claude checked · post-edit active-unverified"}, r.ActiveLines(check.Ref))

		r = CapabilityReportOf(cfg, nil, installedPaths("claude", testBaseline, false, "pending", BinaryCurrent), nil)
		assert.Equal(t, BadgeRequestedInactive, badgeOfPin(t, r, check.Ref, "claude").Badge)
	})

	t.Run("requested-inactive without handlers reads not-installed", func(t *testing.T) {
		paths := append(installedPaths("claude", "", false, "pending", ""), installedPaths("codex", "", false, "pending", "")...)
		r := CapabilityReportOf(cfg, nil, paths, nil)

		assert.Equal(t, PinClient{Client: "codex", Badge: BadgeRequestedInactive, Reasons: []CapReason{CapNotInstalled}},
			badgeOfPin(t, r, enforce.Ref, "codex"))
		assert.Equal(t, "requested; inactive: not-installed", r.ActiveText(enforce.Ref), "the text of a root without handlers")
		assert.Equal(t, []string{
			"active: claude requested; inactive: not-installed",
			"active: codex requested; inactive: not-installed",
		}, r.ActiveLines(enforce.Ref))
		assert.Equal(t, []string{"claude", "codex"}, r.InactiveClients(ModeEnforce))

		for _, c := range r.Clients {
			assert.Equal(t, InstalledOff, c.Installed)
		}
	})

	t.Run("a pin without a client badge reads the same fixed text in both forms", func(t *testing.T) {
		r := CapabilityReportOf(cfg, nil, nil, nil)

		pin, ok := r.Pin(enforce.Ref)
		require.True(t, ok)
		require.Empty(t, pin.Clients, "no client reports a path")

		assert.Equal(t, ActiveRequested, r.ActiveText(enforce.Ref))
		assert.Equal(t, []string{"active: " + ActiveRequested}, r.ActiveLines(enforce.Ref))

		assert.Equal(t, ActiveAdvisory, r.ActiveText("no-such-pin"), "an unknown pin reads advisory")
		assert.Equal(t, []string{"active: " + ActiveAdvisory}, r.ActiveLines("no-such-pin"))
	})

	t.Run("an advisory pin with a check reads advisory, a plain advisory pin is not listed", func(t *testing.T) {
		r := CapabilityReportOf(cfg, nil, installedPaths("claude", testGuard, true, "pending", BinaryCurrent), nil)

		require.Len(t, r.Pins, 3)
		assert.Equal(t, PinClient{Client: "claude", Badge: BadgeAdvisory}, badgeOfPin(t, r, withCheck.Ref, "claude"))
		assert.Equal(t, []string{"active: " + ActiveAdvisory}, r.ActiveLines(withCheck.Ref))
		assert.Equal(t, ActiveAdvisory, r.ActiveText(cfg.Pins[3].Ref))

		pin, _ := r.Pin(enforce.Ref)
		assert.Equal(t, enforce.Version, pin.PinVersion)
		assert.Equal(t, ModeEnforce, pin.Requested)
		require.NotNil(t, pin.Check)
		assert.Equal(t, "frozen", pin.Check.ID)
		assert.Equal(t, KindForbidPathChange, pin.Check.Kind)
		assert.NotEmpty(t, pin.Check.Version)
	})

	t.Run("clients keep path order, attributions are copied, and empty lists encode as []", func(t *testing.T) {
		attrs := map[string][]CapAttribution{
			"codex":  {{Item: ItemTurnBoundary, Context: ContextRoot, State: AttrUnverified}},
			"aaa":    {{Item: ItemMatchIdentity, Context: ContextChild, State: AttrUnsupported}},
			"claude": {{Item: ItemContextReset, Context: ContextRoot, State: AttrUnverified}},
		}
		paths := append(installedPaths("codex", "", false, "pending", ""), installedPaths("claude", "", false, "pending", "")...)

		r := CapabilityReportOf(cfg, nil, paths, attrs)

		var ids []string
		for _, c := range r.Clients {
			ids = append(ids, c.Client)
			assert.Equal(t, attrs[c.Client], c.Attributions)
		}

		assert.Equal(t, []string{"codex", "claude", "aaa"}, ids)

		empty := CapabilityReportOf(nil, nil, nil, nil)
		data, err := json.Marshal(empty)
		require.NoError(t, err)
		assert.JSONEq(t, `{"schema":"lesson-capability/1","config":{"sha256":"","lesson_checks_version":0,"valid":true},`+
			`"clients":[],"pins":[],"uncovered_writers":[]}`, string(data))
	})

	t.Run("the configuration part follows the form of the verification result", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

		write := func(text string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(text), 0o600))
		}

		write("pin: [\n")

		broken, errs := LoadForCapability(root)
		require.NotNil(t, broken, "a broken file gives an empty Config, never an absent one")
		require.Len(t, errs, 1)
		assert.Equal(t, CodeDecodeError, errs[0].Code)

		r := CapabilityReportOf(broken, errs, nil, nil)
		assert.False(t, r.Config.Valid)
		assert.Len(t, r.Config.SHA256, 64, "the digest of the bytes stays")
		assert.Equal(t, "config invalid", r.ConfigText())
		assert.False(t, UsesLessonChecks(broken, errs), "a tier-1 file alone prints no lesson-check line")

		write("lesson_checks_version: 2\npin:\n  - rule: kept\n    region: a/\n")

		newer, errs := LoadForCapability(root)
		require.Len(t, errs, 1)
		assert.Equal(t, CodeVersionUnsupported, errs[0].Code)
		require.Len(t, newer.Pins, 1, "advisory readers keep the pins")

		r = CapabilityReportOf(newer, errs, nil, nil)
		assert.Equal(t, 2, r.Config.LessonChecksVersion)
		assert.Equal(t, "config invalid (lesson_checks_version 2 is not supported; advisory pins still deliver)", r.ConfigText())
		assert.True(t, UsesLessonChecks(newer, errs))

		require.NoError(t, os.Remove(filepath.Join(root, ".seamark", "lessons.yaml")))

		absent, errs := LoadForCapability(root)
		assert.Empty(t, errs)
		assert.Equal(t, "config valid", CapabilityReportOf(absent, errs, nil, nil).ConfigText())
		assert.False(t, UsesLessonChecks(absent, errs))
	})

	t.Run("pin counts and the lesson-check keys", func(t *testing.T) {
		e, c, a := PinModeCounts(cfg)
		assert.Equal(t, []int{1, 1, 2}, []int{e, c, a})
		assert.True(t, UsesLessonChecks(cfg, nil))

		plain := capabilityConfig(t, "pin:\n  - rule: plain\n    region: lib/\n    mode: advisory\n")
		assert.False(t, UsesLessonChecks(plain, nil), "an explicit advisory mode uses no lesson-check key")
	})
}

// TestPinClientNote proves the text after a badge: the reasons, then the
// limits, and nothing for a client with neither.
func TestPinClientNote(t *testing.T) {
	assert.Empty(t, PinClient{Badge: BadgeEnforced}.Note())
	assert.Equal(t, "limit: a; b", PinClient{Limits: []string{"a", "b"}}.Note())
	assert.Equal(t, "not-installed; limit: a",
		PinClient{Reasons: []CapReason{CapNotInstalled}, Limits: []string{"a"}}.Note())
}
