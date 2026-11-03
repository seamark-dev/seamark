package lessoncheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the tests of DecideGuard. Every repository lives
// under t.TempDir(), so no test reads or writes the checkout that runs
// it.

// guardYAML holds two enforce pins and one check pin. The advisory
// budget, the mute rules, the delivery mode, and the threshold would
// hold back both enforce pins on the advisory hook path. The guard must
// ignore them, because they would let an enforce pin miss an edit.
const guardYAML = `lesson_checks_version: 1
threshold: 99
hook_delivery: once-per-context
pin_budget: 1
change_budget: 1
mute:
  - rule: frozen-assets
  - rule: no-todo
    region: src
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
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: enforce
    check: no-todo
  - rule: no-todo-docs
    region: docs
    note: Do not add TODO markers to docs.
    mode: check
    check: no-todo
`

// guardRepo is a repository with the guard config and one committed
// file in each pin scope.
func guardRepo(t *testing.T) (*gitRepo, *Config) {
	t.Helper()

	g := todoRepo(t)
	g.write("docs/a.md", "docs\n")
	writeLessons(t, g.root, guardYAML)

	cfg, errs, err := Load(g.root)
	require.NoError(t, err)
	require.Empty(t, errs)

	return g, cfg
}

// decide projects op with lim and decides it with cfg, as the guard
// handler does.
func (g *gitRepo) decide(cfg *Config, op Operation, lim Limits) (GuardDecision, []string, *Snapshot) {
	g.t.Helper()

	snap, err := ProjectOperation(context.Background(), g.root, op, lim)
	decision, texts := DecideGuard(GuardInput{Config: cfg, Operation: op, Snapshot: snap, ProjectErr: err})

	return decision, texts, snap
}

// pinOutcome is the entry and the outcome of one pin of a decision.
type pinOutcome struct {
	identity string
	outcome  Outcome
	reason   Reason
}

// pinOutcomes returns the pins of a decision in a comparable form.
func pinOutcomes(d GuardDecision) []pinOutcome {
	out := []pinOutcome{}

	for _, p := range d.Pins {
		out = append(out, pinOutcome{identity: p.PinIdentity, outcome: p.Outcome, reason: p.Reason})
	}

	return out
}

// requireDecisionShape checks the fields that every decision holds.
func requireDecisionShape(t *testing.T, d GuardDecision) {
	t.Helper()

	require.Equal(t, GuardSchema, d.Schema)
	require.Contains(t, []string{GuardAllow, GuardDeny}, d.Decision)
	require.Equal(t, GuardProtocolExit2, d.Protocol)
	require.NotNil(t, d.Pins, "pins encode as [], never null")
	require.False(t, d.ReplyWritten, "the handler sets reply_written")
	require.Empty(t, d.NativeBlocking, "the handler copies native_blocking")

	if d.Decision == GuardAllow {
		require.Equal(t, GuardRequestedNone, d.Requested)
		require.Contains(t, []string{"", GuardCauseOutsideRoot, GuardCauseToolUncovered}, d.Cause)
	} else {
		require.Equal(t, GuardRequestedDeny, d.Requested)
		require.Contains(t, []string{
			GuardCauseViolation, GuardCauseUnknown, GuardCauseConfigurationError,
			GuardCauseUnparseableInput, GuardCauseInternalError,
		}, d.Cause)
	}
}

// TestLessonCheckGuardConstructors proves the decisions that the CLI
// handler takes without DecideGuard. The audit record embeds them, so
// the test freezes the JSON bytes.
func TestLessonCheckGuardConstructors(t *testing.T) {
	t.Run("DenyInternal equals the deny of a request without a configuration", func(t *testing.T) {
		for _, op := range []Operation{{}, claudeOp("Write")} {
			want, wantTexts := DecideGuard(GuardInput{Operation: op})
			got, texts := DenyInternal(op.Tool)

			requireDecisionShape(t, got)
			assert.Equal(t, want, got)
			assert.Equal(t, wantTexts, texts)
		}

		decision, texts := DenyInternal("Write")
		assert.Equal(t, []string{"lesson check guard cannot decide this Write request: internal error"}, texts)

		data, err := json.Marshal(decision)
		require.NoError(t, err)
		assert.Equal(t, `{"schema":"lesson-guard/1","decision":"deny","cause":"internal-error",`+
			`"input_sha256":"","paths_complete":false,"pins":[],"requested":"deny",`+
			`"protocol":"exit-2-stderr","reply_written":false,"native_blocking":""}`, string(data))
	})

	t.Run("AllowUncovered names its cause and evaluates no pin", func(t *testing.T) {
		decision := AllowUncovered()

		requireDecisionShape(t, decision)
		assert.Equal(t, GuardAllow, decision.Decision)

		data, err := json.Marshal(decision)
		require.NoError(t, err)
		assert.Equal(t, `{"schema":"lesson-guard/1","decision":"allow","cause":"tool-uncovered",`+
			`"input_sha256":"","paths_complete":false,"pins":[],"requested":"none",`+
			`"protocol":"exit-2-stderr","reply_written":false,"native_blocking":""}`, string(data))
	})
}

// guardForbiddenWords are words that would tell the agent how to turn
// the guard off. No deny text holds one.
var guardForbiddenWords = []string{"set-mode", "advisory", "disable", "bypass", "uninstall", "--no-verify", "lesson-checks off", "mode: check"}

func TestLessonCheckDecideGuardMatrix(t *testing.T) {
	g, cfg := guardRepo(t)

	frozen, todo := cfg.Pins[0], cfg.Pins[1]
	forbidHead := "lesson check frozen-assets (forbid-path-change/1) of pin " + frozen.Ref
	patternHead := "lesson check no-todo (no-added-pattern/1) of pin " + todo.Ref

	timeout := DefaultLimits()
	timeout.GuardBudget = time.Nanosecond

	cases := []struct {
		name    string
		op      Operation
		lim     Limits
		allow   bool
		cause   string
		reasons []Reason
		pins    []pinOutcome
		texts   []string
	}{
		{
			name:  "a forbid violation denies",
			op:    claudeOp("Edit", editOp("generated/a.txt", "frozen", "thawed", false)),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{frozen.Identity, OutcomeViolation, ""}},
			texts: []string{forbidHead + ` "frozen-assets": Edit would change generated/a.txt`},
		},
		{
			name:  "a pattern violation denies with the redacted excerpt",
			op:    claudeOp("Write", writeOp("src/b.go", "package b\n// TODO(fix) password: hunter2\n")),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{todo.Identity, OutcomeViolation, ""}},
			texts: []string{patternHead + ` "no-todo": Write adds a line matching the pattern at src/b.go:2: // TODO(fix) password: [REDACTED]`},
		},
		{
			name:  "a clean edit allows with an empty reply",
			op:    claudeOp("Edit", editOp("src/a.go", "func A() {}", "func B() {}", false)),
			allow: true,
			pins:  []pinOutcome{{todo.Identity, OutcomePass, ""}},
		},
		{
			name:  "a check pin never denies",
			op:    claudeOp("Write", writeOp("docs/b.md", "TODO(x)\n")),
			allow: true,
			pins:  []pinOutcome{},
		},
		{
			name:  "a path outside every enforce scope allows",
			op:    claudeOp("Write", writeOp("README.md", "TODO(x)\n")),
			allow: true,
			pins:  []pinOutcome{},
		},
		{
			name:    "an edit that the native tool rejects denies with the count",
			op:      claudeOp("Edit", editOp("src/a.go", "zzz", "TODO(y)", false)),
			cause:   GuardCauseUnknown,
			reasons: []Reason{ReasonUnprojectableEdit},
			pins:    []pinOutcome{{todo.Identity, OutcomeUnknown, ReasonUnprojectableEdit}},
			texts:   []string{patternHead + " cannot evaluate src/a.go: unprojectable-edit; old_string occurs 0 times"},
		},
		{
			name:  "an Edit that creates an absent file allows a clean add",
			op:    claudeOp("Edit", editOp("src/new.go", "", "package new\n", false)),
			allow: true,
			pins:  []pinOutcome{{todo.Identity, OutcomePass, ""}},
		},
		{
			name:  "an Edit that creates an absent file denies a violating add as a Write does",
			op:    claudeOp("Edit", editOp("src/new.go", "", "package new\n// TODO(x)\n", false)),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{todo.Identity, OutcomeViolation, ""}},
			texts: []string{patternHead + ` "no-todo": Edit adds a line matching the pattern at src/new.go:2: // TODO(x)`},
		},
		{
			name:    "an Edit of an absent file with a non-empty old_string denies",
			op:      claudeOp("Edit", editOp("src/new.go", "package", "package new\n", false)),
			cause:   GuardCauseUnknown,
			reasons: []Reason{ReasonUnprojectableEdit},
			pins:    []pinOutcome{{todo.Identity, OutcomeUnknown, ReasonUnprojectableEdit}},
			texts:   []string{patternHead + " cannot evaluate src/new.go: unprojectable-edit; the file does not exist"},
		},
		{
			name:    "a Codex update in a pattern scope denies",
			op:      codexOp(FileOp{Kind: OpPatchUpdate, Path: "src/a.go", Hunks: true}),
			cause:   GuardCauseUnknown,
			reasons: []Reason{ReasonUnprojectableEdit},
			pins:    []pinOutcome{{todo.Identity, OutcomeUnknown, ReasonUnprojectableEdit}},
			texts:   []string{patternHead + " cannot evaluate src/a.go: unprojectable-edit; update hunks have no verified projection"},
		},
		{
			name:  "a Codex update decides a forbid pin on paths alone",
			op:    codexOp(FileOp{Kind: OpPatchUpdate, Path: "generated/a.txt", Hunks: true}),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{frozen.Identity, OutcomeViolation, ""}},
			texts: []string{forbidHead + ` "frozen-assets": apply_patch would change generated/a.txt`},
		},
		{
			name:  "a Codex update outside every enforce scope allows",
			op:    codexOp(FileOp{Kind: OpPatchUpdate, Path: "README.md", Hunks: true}),
			allow: true,
			pins:  []pinOutcome{},
		},
		{
			name: "a violation and an unknown give the cause violation",
			op: codexOp(
				FileOp{Kind: OpPatchUpdate, Path: "src/a.go", Hunks: true},
				FileOp{Kind: OpPatchDelete, Path: "generated/a.txt"},
			),
			cause:   GuardCauseViolation,
			reasons: []Reason{ReasonUnprojectableEdit},
			pins: []pinOutcome{
				{frozen.Identity, OutcomeViolation, ""},
				{todo.Identity, OutcomeUnknown, ReasonUnprojectableEdit},
			},
			texts: []string{
				forbidHead + ` "frozen-assets": apply_patch would change generated/a.txt`,
				patternHead + " cannot evaluate src/a.go: unprojectable-edit; update hunks have no verified projection",
			},
		},
		{
			name:  "a move out of a forbid scope violates at the source",
			op:    codexOp(FileOp{Kind: OpPatchMove, Path: "generated/a.txt", MoveTo: "docs/a.txt"}),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{frozen.Identity, OutcomeViolation, ""}},
			texts: []string{forbidHead + ` "frozen-assets": apply_patch would change generated/a.txt -> docs/a.txt`},
		},
		{
			name: "a path outside the root allows with the cause outside-root",
			op: func() Operation {
				op := claudeOp("Write", writeOp("README.md", "x\n"))
				op.Dropped = []string{"../outside.txt"}

				return op
			}(),
			allow: true,
			cause: GuardCauseOutsideRoot,
			pins:  []pinOutcome{},
		},
		{
			name: "a deny cause wins over outside-root",
			op: func() Operation {
				op := claudeOp("Write", writeOp("generated/a.txt", "x\n"))
				op.Dropped = []string{"../outside.txt"}

				return op
			}(),
			cause: GuardCauseViolation,
			pins:  []pinOutcome{{frozen.Identity, OutcomeViolation, ""}},
			texts: []string{forbidHead + ` "frozen-assets": Write would change generated/a.txt`},
		},
		{
			name: "an unresolvable path arms every enforce pin",
			op: func() Operation {
				op := claudeOp("Write", writeOp("README.md", "x\n"))
				op.PathsComplete = false

				return op
			}(),
			cause:   GuardCauseUnknown,
			reasons: []Reason{ReasonIncompletePaths},
			pins: []pinOutcome{
				{frozen.Identity, OutcomeUnknown, ReasonIncompletePaths},
				{todo.Identity, OutcomeUnknown, ReasonIncompletePaths},
			},
			texts: []string{
				forbidHead + " cannot evaluate a path that the guard cannot resolve: incomplete-paths",
				patternHead + " cannot evaluate a path that the guard cannot resolve: incomplete-paths",
			},
		},
		{
			name:    "the guard budget denies with resource-limit",
			op:      claudeOp("Edit", editOp("src/a.go", "func A() {}", "func B() {}", false)),
			lim:     timeout,
			cause:   GuardCauseUnknown,
			reasons: []Reason{ReasonResourceLimit},
			pins:    []pinOutcome{{todo.Identity, OutcomeUnknown, ReasonResourceLimit}},
			texts:   []string{patternHead + " cannot evaluate src/a.go: resource-limit (guard-budget)"},
		},
	}

	var allTexts []string

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := tc.lim
			if lim == (Limits{}) {
				lim = DefaultLimits()
			}

			decision, texts, snap := g.decide(cfg, tc.op, lim)
			requireDecisionShape(t, decision)

			want := GuardDeny
			if tc.allow {
				want = GuardAllow
			}

			assert.Equal(t, want, decision.Decision)
			assert.Equal(t, tc.cause, decision.Cause)
			assert.Equal(t, tc.reasons, decision.Reasons)
			assert.Equal(t, tc.pins, pinOutcomes(decision))
			assert.Equal(t, tc.texts, texts)
			assert.Equal(t, snap.SHA256, decision.InputSHA256, "the input digest is the snapshot digest")
			assert.Equal(t, snap.PathsComplete, decision.PathsComplete)

			encoded, err := json.Marshal(decision)
			require.NoError(t, err)

			for _, p := range operationPaths(tc.op.Ops) {
				assert.NotContains(t, string(encoded), strconv.Quote(p), "the decision names no path")
			}

			assert.NotContains(t, string(encoded), "TODO(", "the decision holds no excerpt")
			assert.NotContains(t, string(encoded), "hunter2")
			assert.NotContains(t, string(encoded), "Do not add", "the decision holds no note")

			allTexts = append(allTexts, texts...)
		})
	}

	t.Run("no deny text tells the agent how to turn the guard off", func(t *testing.T) {
		require.NotEmpty(t, allTexts)

		for _, text := range allTexts {
			for _, word := range guardForbiddenWords {
				assert.NotContains(t, text, word)
			}
		}
	})

	t.Run("a write through a link arms every enforce pin", func(t *testing.T) {
		skipWithoutSymlinks(t)

		linked, linkCfg := guardRepo(t)
		linked.symlink("../docs/a.md", "src/link.md")

		decision, texts, snap := linked.decide(linkCfg, claudeOp("Write", writeOp("src/link.md", "clean\n")), DefaultLimits())
		requireDecisionShape(t, decision)

		assert.False(t, snap.PathsComplete)
		assert.Equal(t, GuardDeny, decision.Decision)
		assert.Equal(t, []pinOutcome{
			{linkCfg.Pins[0].Identity, OutcomeUnknown, ReasonIncompletePaths},
			{linkCfg.Pins[1].Identity, OutcomeUnknown, ReasonIncompletePaths},
		}, pinOutcomes(decision), "the link target can lie in any scope; incomplete-paths comes first in the reason precedence")
		assert.Contains(t, texts, patternHead+" cannot evaluate src/link.md: unprojectable-edit; the path is a symbolic link")
	})

	t.Run("an absent lessons.yaml allows", func(t *testing.T) {
		empty := todoRepo(t)

		absent, errs, err := Load(empty.root)
		require.NoError(t, err)
		require.Empty(t, errs)

		decision, texts, _ := empty.decide(absent, claudeOp("Write", writeOp("generated/a.txt", "x")), DefaultLimits())
		requireDecisionShape(t, decision)
		assert.Equal(t, GuardAllow, decision.Decision)
		assert.Empty(t, decision.Pins)
		assert.Nil(t, texts)
	})

	t.Run("a tier-1 configuration error denies every request", func(t *testing.T) {
		broken := todoRepo(t)
		writeLessons(t, broken.root, "pin: [\n")

		_, _, loadErr := Load(broken.root)
		require.Error(t, loadErr)

		op := claudeOp("Write", writeOp("README.md", "x"))
		decision, texts := DecideGuard(GuardInput{LoadErr: loadErr, Operation: op})
		requireDecisionShape(t, decision)

		assert.Equal(t, GuardDeny, decision.Decision)
		assert.Equal(t, GuardCauseConfigurationError, decision.Cause)
		assert.Equal(t, []Reason{ReasonConfigurationError}, decision.Reasons)
		require.Len(t, texts, 1)
		assert.True(t, strings.HasPrefix(texts[0], "lesson checks cannot be established: "), texts[0])
		assert.True(t, strings.HasSuffix(texts[0], "; ask the user to fix .seamark/lessons.yaml"), texts[0])
		assert.NotContains(t, texts[0], "lessons config: ", "the message drops the error prefix")
	})

	t.Run("a tier-2 configuration error denies with the first error and no fallback", func(t *testing.T) {
		invalid := todoRepo(t)
		writeLessons(t, invalid.root, `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Frozen.
    mode: enforce
    check: frozen-assetz
`)

		bad, errs, err := Load(invalid.root)
		require.NoError(t, err)
		require.NotEmpty(t, errs)

		decision, texts, _ := invalid.decide(bad, claudeOp("Write", writeOp("README.md", "x")), DefaultLimits())
		requireDecisionShape(t, decision)

		assert.Equal(t, GuardCauseConfigurationError, decision.Cause)
		assert.Equal(t, []pinOutcome{{bad.Pins[0].Identity, OutcomeUnknown, ReasonConfigurationError}}, pinOutcomes(decision))
		assert.Equal(t, []string{"lesson checks cannot be established: " + errs[0].Message + "; ask the user to fix .seamark/lessons.yaml"}, texts)
	})

	t.Run("a long configuration message keeps the tail inside the byte cap", func(t *testing.T) {
		long := fmt.Errorf("%w: %s", errors.New("lessons config"), strings.Repeat("é", 400))
		_, texts := DecideGuard(GuardInput{LoadErr: long})

		require.Len(t, texts, 1)
		assert.LessOrEqual(t, len(texts[0]), maxDenyReasonBytes)
		assert.True(t, utf8.ValidString(texts[0]), "the cut falls on a rune boundary")
		assert.True(t, strings.HasSuffix(texts[0], "…; ask the user to fix .seamark/lessons.yaml"), texts[0])
	})

	t.Run("a projection error denies with internal-error", func(t *testing.T) {
		op := claudeOp("Edit", editOp("src/a.go", "a", "b", false))
		decision, texts := DecideGuard(GuardInput{Config: cfg, Operation: op, ProjectErr: errors.New("read failed")})
		requireDecisionShape(t, decision)

		assert.Equal(t, GuardCauseInternalError, decision.Cause)
		assert.Equal(t, []string{"lesson check guard cannot decide this Edit request: internal error"}, texts)
	})

	t.Run("a projection error of an unscoped edit still allows", func(t *testing.T) {
		op := claudeOp("Write", writeOp("README.md", "x"))
		decision, _ := DecideGuard(GuardInput{Config: cfg, Operation: op, ProjectErr: errors.New("read failed")})

		assert.Equal(t, GuardAllow, decision.Decision, "no enforce pin scopes the edit")
	})

	t.Run("a missing configuration denies with internal-error", func(t *testing.T) {
		decision, _ := DecideGuard(GuardInput{Operation: claudeOp("Write", writeOp("README.md", "x"))})

		assert.Equal(t, GuardCauseInternalError, decision.Cause)
	})

	t.Run("a panic denies with internal-error", func(t *testing.T) {
		beforeDecide = func() { panic("decision failure") }
		t.Cleanup(func() { beforeDecide = nil })

		decision, texts := DecideGuard(GuardInput{Config: cfg, Operation: claudeOp("MultiEdit")})
		requireDecisionShape(t, decision)

		assert.Equal(t, GuardDeny, decision.Decision)
		assert.Equal(t, GuardCauseInternalError, decision.Cause)
		assert.Equal(t, []string{"lesson check guard cannot decide this MultiEdit request: internal error"}, texts)
	})

	t.Run("the deny texts keep five and count the rest", func(t *testing.T) {
		seven := claudeOp("Write", writeOp("src/many.go", strings.Repeat("TODO(a)\n", 7)))
		decision, texts, _ := g.decide(cfg, seven, DefaultLimits())

		assert.Equal(t, GuardDeny, decision.Decision)
		require.Len(t, texts, 6)
		assert.Equal(t, "and 2 more", texts[5])

		twelve := claudeOp("Write", writeOp("src/many.go", strings.Repeat("TODO(a)\n", 12)))
		_, texts, _ = g.decide(cfg, twelve, DefaultLimits())

		require.Len(t, texts, 6)
		assert.Equal(t, "and 7 more", texts[5], "the evidence that the result omits counts too")
	})

	t.Run("each deny text stays inside its caps", func(t *testing.T) {
		long := "src/" + strings.Repeat("d", 250) + ".go"
		_, texts, _ := g.decide(cfg, claudeOp("Write", writeOp(long, "// TODO("+strings.Repeat("x", 300)+")\n")), DefaultLimits())

		require.Len(t, texts, 1)
		assert.LessOrEqual(t, len(texts[0]), maxDenyReasonBytes)
		assert.True(t, strings.HasSuffix(texts[0], "…"))

		// ASCII keeps the text below the byte cap, so only the excerpt
		// cap cuts it.
		line := "// TODO(" + strings.Repeat("x", 300) + ")"
		_, texts, _ = g.decide(cfg, claudeOp("Write", writeOp("src/c.go", line+"\n")), DefaultLimits())

		require.Len(t, texts, 1)
		_, shown, found := strings.Cut(texts[0], "src/c.go:1: ")
		require.True(t, found, texts[0])
		assert.Equal(t, maxDenyExcerptRunes, utf8.RuneCountInString(strings.TrimSuffix(shown, "…"))+1,
			"the excerpt is cut at max-deny-excerpt-runes")
		assert.LessOrEqual(t, len(texts[0]), maxDenyReasonBytes)
	})

	t.Run("a deny text holds no control character", func(t *testing.T) {
		_, texts, _ := g.decide(cfg, claudeOp("Write\x1b[31m", writeOp("generated/a.txt", "x")), DefaultLimits())

		require.Len(t, texts, 1)
		assert.NotContains(t, texts[0], "\x1b")
	})

	t.Run("an unparseable request denies with the redacted error", func(t *testing.T) {
		decision, texts := DenyUnparseable("Write", errors.New("tool_input.content is missing; token=abc123"))
		requireDecisionShape(t, decision)

		assert.Equal(t, GuardCauseUnparseableInput, decision.Cause)
		assert.Equal(t, []Reason{ReasonUnparseableInput}, decision.Reasons)
		assert.Equal(t, []string{"lesson check guard cannot read this Write request: tool_input.content is missing; token=[REDACTED]"}, texts)

		_, texts = DenyUnparseable("", nil)
		assert.Equal(t, []string{"lesson check guard cannot read this request: the request does not decode"}, texts)
	})

	t.Run("guard.go does no I/O", func(t *testing.T) {
		file, err := parser.ParseFile(token.NewFileSet(), "guard.go", nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)
			assert.NotContains(t, impureImports, path, "guard.go imports %s", path)
		}
	})

	t.Run("a full deny decision round-trips through the type and matches a real deny", func(t *testing.T) {
		const example = `{"schema": "lesson-guard/1", "decision": "deny", "cause": "unknown", "reasons": ["unprojectable-edit"], "input_sha256": "9a8b7c6d5e4f30211203f4e5d6c7b8a99a8b7c6d5e4f3021120e4f5a0d6f3c1e", "paths_complete": true, "pins": [{"pin_identity": "77c0a1d2e3f4a5b6c7d8e9f00112233445566778899aabbccddeeff001122334", "pin_version": "c1d2e3f4a5b6c7d8e9f0112233445566778899aabbccddeeff00112233445566", "mode": "enforce", "check": {"id": "no-todo", "kind": "no-added-pattern/1", "version": "c044c91d812033b1da35c51ad301b6a3a06b09503b8e1126b392c1d643d7aa3b"}, "outcome": "unknown", "reason": "unprojectable-edit", "evidence_paths": 1}], "requested": "deny", "protocol": "exit-2-stderr", "reply_written": true, "native_blocking": "unverified"}`

		var decoded GuardDecision
		require.NoError(t, json.Unmarshal([]byte(example), &decoded))

		encoded, err := json.Marshal(decoded)
		require.NoError(t, err)
		assert.JSONEq(t, example, string(encoded))

		op := claudeOp("Edit", editOp("src/a.go", "zzz", "y", false))
		decision, _, _ := g.decide(cfg, op, DefaultLimits())
		decision.ReplyWritten = true
		decision.NativeBlocking = GuardNativeUnverified

		assert.Equal(t, decoded.Schema, decision.Schema)
		assert.Equal(t, decoded.Decision, decision.Decision)
		assert.Equal(t, decoded.Cause, decision.Cause)
		assert.Equal(t, decoded.Reasons, decision.Reasons)
		assert.Equal(t, decoded.Requested, decision.Requested)
		assert.Equal(t, decoded.Protocol, decision.Protocol)
		require.Len(t, decision.Pins, 1)
		assert.Equal(t, decoded.Pins[0].EvidencePaths, decision.Pins[0].EvidencePaths)
	})

	t.Run("the guard never reads lessons.yaml itself", func(t *testing.T) {
		writeLessons(t, g.root, "pin: [\n")
		t.Cleanup(func() { writeLessons(t, g.root, guardYAML) })

		decision, _, _ := g.decide(cfg, claudeOp("Write", writeOp("README.md", "x")), DefaultLimits())
		assert.Equal(t, GuardAllow, decision.Decision, "DecideGuard decides on the config that the handler loaded")

		_, err := os.Stat(filepath.Join(g.root, ".seamark", "lesson-check-audit.jsonl"))
		assert.ErrorIs(t, err, os.ErrNotExist, "DecideGuard writes no audit record")
	})
}
