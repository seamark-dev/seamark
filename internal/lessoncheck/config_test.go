package lessoncheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// referenceYAML is the reference lessons.yaml: one forbid definition
// and one enforce pin. It is the positive case of Validate, so the
// tests keep its bytes fixed.
const referenceYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
`

// referencePatternYAML is referenceYAML with a second definition that
// has a pattern.
const referencePatternYAML = `lesson_checks_version: 1
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
`

// invalidYAML is the reference invalid lessons.yaml. It holds one
// pattern that does not compile and one pin that names a missing check.
const invalidYAML = `lesson_checks_version: 1
checks:
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO('
pin:
  - rule: frozen-assets
    region: generated/
    note: Changes to these files require a maintainer-managed update.
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todos
`

// The fixed identity, pin-ref, and version digests of the referenceYAML
// pin and of the two definitions. Fixed values catch any change of the
// derivation.
const (
	referenceIdentity   = "3215e7b483d134443a8204970767f1a0057d4726f235e946b6c0c27abd8cd055"
	referencePinRef     = "3215e7b483d1"
	referencePinVersion = "ae4f17f75413b37e4142d80b529154f6166dd2bf155e6eac3dc86dc040aa7a7e"
	forbidVersion       = "9ceb9cddb5053035c0d9779ffab326503647b5295cb3f43791d5fe1e9be9cf7a"
	noTodoVersion       = "c044c91d812033b1da35c51ad301b6a3a06b09503b8e1126b392c1d643d7aa3b"
	emptyBytesSHA256    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	lessonsConfigHead   = "lessons config: "
)

// sha256Hex is the hex SHA-256 of data, the Config.SHA256 form.
func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))

	return hex.EncodeToString(sum[:])
}

// validateYAML decodes text with the lenient tier and returns the
// errors of the strict tier.
func validateYAML(t *testing.T, text string) []ConfigError {
	t.Helper()

	raw, err := reviews.ParseConfig([]byte(text))
	require.NoError(t, err, "the decode tier must accept every tier-2 case")

	return Validate(raw)
}

// writeLessons writes text to <root>/.seamark/lessons.yaml.
func writeLessons(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(text), 0o644))
}

// TestLessonCheckValidateReferenceConfigs proves that referenceYAML and
// referencePatternYAML validate with no error, and that the pin view
// carries the fixed identity, pin-ref, and version.
func TestLessonCheckValidateReferenceConfigs(t *testing.T) {
	for name, text := range map[string]string{"referenceYAML": referenceYAML, "referencePatternYAML": referencePatternYAML} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, validateYAML(t, text), "nil means valid")

			cfg, errs, err := Parse([]byte(text))
			require.NoError(t, err)
			assert.Nil(t, errs)
			require.NotNil(t, cfg)
			assert.Nil(t, cfg.Errors)
			assert.Equal(t, sha256Hex(text), cfg.SHA256)
			assert.Equal(t, 1, cfg.Raw.LessonChecksVersion)

			require.Len(t, cfg.Pins, 1)

			pin := cfg.Pins[0]
			assert.Equal(t, 1, pin.Entry)
			assert.Equal(t, reviews.PinKey{Rule: "frozen-assets", Region: "generated"}, pin.Key)
			assert.Equal(t, referenceIdentity, pin.Identity)
			assert.Equal(t, referencePinRef, pin.Ref)
			assert.Equal(t, referencePinVersion, pin.Version)
			assert.Equal(t, ModeEnforce, pin.Mode)
			assert.Equal(t, []string{"generated"}, pin.Scope)
			assert.Equal(t, "frozen-assets", pin.CheckID)
			assert.Equal(t, "generated/", pin.Rule.Region, "the raw rule stays as written")

			assert.Equal(t, Check{ID: "frozen-assets", Kind: KindForbidPathChange, Version: forbidVersion},
				cfg.Checks["frozen-assets"])
		})
	}

	t.Run("referencePatternYAML compiles its pattern", func(t *testing.T) {
		cfg, _, err := Parse([]byte(referencePatternYAML))
		require.NoError(t, err)
		require.Len(t, cfg.Checks, 2)

		check := cfg.Checks["no-todo"]
		assert.Equal(t, "no-todo", check.ID)
		assert.Equal(t, KindNoAddedPattern, check.Kind)
		assert.Equal(t, noTodoVersion, check.Version)
		require.NotNil(t, check.Pattern)
		assert.Equal(t, `TODO\(`, check.Pattern.String())
		assert.True(t, check.Pattern.MatchString("// TODO(yuri): remove"))
	})

	t.Run("a repository-wide pin has an empty scope that is not nil", func(t *testing.T) {
		cfg, errs, err := Parse([]byte("lesson_checks_version: 1\nchecks:\n  c:\n    kind: forbid-path-change/1\n" +
			"pin:\n  - rule: r\n    region: '*'\n    note: n\n    mode: check\n    check: c\n"))
		require.NoError(t, err)
		assert.Nil(t, errs)
		require.Len(t, cfg.Pins, 1)
		require.NotNil(t, cfg.Pins[0].Scope)
		assert.Empty(t, cfg.Pins[0].Scope)
	})

	t.Run("the scope is sorted and the identity ignores the region order", func(t *testing.T) {
		cfg, errs, err := Parse([]byte("pin:\n  - rule: r\n    region: z/\n    regions: [a, m]\n    note: n\n"))
		require.NoError(t, err)
		assert.Nil(t, errs)
		require.Len(t, cfg.Pins, 1)
		assert.Equal(t, []string{"a", "m", "z"}, cfg.Pins[0].Scope)
		assert.Equal(t, ModeAdvisory, cfg.Pins[0].Mode, "an omitted mode is advisory")
		assert.Equal(t, reviews.PinIdentityDigest(reviews.NewPinKey("r", "a", []string{"m", "z"})), cfg.Pins[0].Identity)
	})
}

// TestLessonCheckValidateErrors proves every tier-2 code with its exact
// subject and message, and the error order.
func TestLessonCheckValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want []ConfigError
	}{
		{
			name: "a newer version gives only version-unsupported",
			yaml: "lesson_checks_version: 2\nchecks:\n  Bad Id:\n    kind: nope\n" +
				"pin:\n  - rule: r\n    region: ' a'\n    note: n\n    mode: strict\n    check: none\n",
			want: []ConfigError{{
				Code: CodeVersionUnsupported, Subject: "lesson_checks_version",
				Message: "lesson_checks_version 2 is not supported; this seamark reads up to 1",
			}},
		},
		{
			name: "checks without a version",
			yaml: "checks:\n  c:\n    kind: forbid-path-change/1\n",
			want: []ConfigError{{
				Code: CodeVersionMissing, Subject: "lesson_checks_version",
				Message: "checks needs lesson_checks_version: 1",
			}},
		},
		{
			name: "an executable mode without a version",
			yaml: "pin:\n  - rule: r\n    region: a\n    note: n\n    mode: enforce\n",
			want: []ConfigError{
				{Code: CodeVersionMissing, Subject: "lesson_checks_version", Message: "checks needs lesson_checks_version: 1"},
				{Code: CodePinCheckRequired, Subject: "pin 1", Message: "pin 1 (r @ a): mode enforce requires check: <check-id>"},
			},
		},
		{
			name: "a check reference without a version",
			yaml: "pin:\n  - rule: r\n    region: a\n    note: n\n    check: c\n",
			want: []ConfigError{
				{Code: CodeVersionMissing, Subject: "lesson_checks_version", Message: "checks needs lesson_checks_version: 1"},
				{Code: CodePinCheckMissing, Subject: "pin 1", Message: `pin 1 (r @ a): check "c" is not defined under checks`},
			},
		},
		{
			name: "an explicit advisory mode needs no version",
			yaml: "pin:\n  - rule: r\n    region: a\n    note: n\n    mode: advisory\n",
		},
		{
			name: "an invalid check id is quoted in every message",
			yaml: "lesson_checks_version: 1\nchecks:\n  Bad:\n",
			want: []ConfigError{
				{Code: CodeCheckIDInvalid, Subject: `checks."Bad"`, Message: `checks: check id "Bad" must match [a-z0-9][a-z0-9._-]{0,63}`},
				{Code: CodeCheckKindMissing, Subject: `checks."Bad"`, Message: `checks."Bad": kind is required`},
			},
		},
		{
			// Validate orders the errors of one subject by Code, byte-wise.
			// The code list in config.go puts check-id-invalid first, so only
			// the byte order puts check-field-unknown before it.
			name: "inside one check subject the order is the byte order of the code",
			yaml: "lesson_checks_version: 1\nchecks:\n  Bad:\n    kind: forbid-path-change/1\n    flags: x\n",
			want: []ConfigError{
				{Code: CodeCheckFieldUnknown, Subject: `checks."Bad"`, Message: `checks."Bad": unknown field "flags"`},
				{Code: CodeCheckIDInvalid, Subject: `checks."Bad"`, Message: `checks: check id "Bad" must match [a-z0-9][a-z0-9._-]{0,63}`},
			},
		},
		{
			name: "a check id of 65 characters is invalid",
			yaml: "lesson_checks_version: 1\nchecks:\n  a" + strings.Repeat("b", 64) + ":\n    kind: forbid-path-change/1\n",
			want: []ConfigError{{
				Code: CodeCheckIDInvalid, Subject: `checks."a` + strings.Repeat("b", 64) + `"`,
				Message: `checks: check id "a` + strings.Repeat("b", 64) + `" must match [a-z0-9][a-z0-9._-]{0,63}`,
			}},
		},
		{
			name: "a check id of 64 characters is valid",
			yaml: "lesson_checks_version: 1\nchecks:\n  0" + strings.Repeat("._-", 21) + ":\n    kind: forbid-path-change/1\n",
		},
		{
			name: "unknown fields are refused one by one in key order",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    limit: 3\n    kind: no-added-pattern/1\n    pattern: x\n    flags: i\n",
			want: []ConfigError{
				{Code: CodeCheckFieldUnknown, Subject: "checks.c", Message: `checks.c: unknown field "flags"`},
				{Code: CodeCheckFieldUnknown, Subject: "checks.c", Message: `checks.c: unknown field "limit"`},
			},
		},
		{
			name: "a null or empty definition has no kind",
			yaml: "lesson_checks_version: 1\nchecks:\n  c: {}\n  a:\n  b: ~\n",
			want: []ConfigError{
				{Code: CodeCheckKindMissing, Subject: "checks.a", Message: "checks.a: kind is required"},
				{Code: CodeCheckKindMissing, Subject: "checks.b", Message: "checks.b: kind is required"},
				{Code: CodeCheckKindMissing, Subject: "checks.c", Message: "checks.c: kind is required"},
			},
		},
		{
			name: "an unknown kind skips the pattern rules",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: no-added-pattern/2\n    pattern: '('\n",
			want: []ConfigError{{
				Code: CodeCheckKindUnknown, Subject: "checks.c",
				Message: `checks.c: unknown checker kind "no-added-pattern/2" (known: forbid-path-change/1, no-added-pattern/1)`,
			}},
		},
		{
			name: "a pattern kind without a pattern",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: no-added-pattern/1\n    pattern: ''\n",
			want: []ConfigError{{
				Code: CodeCheckPatternMissing, Subject: "checks.c",
				Message: "checks.c: no-added-pattern/1 requires a non-empty pattern",
			}},
		},
		{
			name: "a path kind with a pattern",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: forbid-path-change/1\n    pattern: x\n",
			want: []ConfigError{{
				Code: CodeCheckPatternUnexpected, Subject: "checks.c",
				Message: "checks.c: forbid-path-change/1 takes no pattern",
			}},
		},
		{
			name: "a pattern above the byte limit",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: no-added-pattern/1\n    pattern: '" + strings.Repeat("(", 1025) + "'\n",
			want: []ConfigError{{
				Code: CodeCheckPatternTooLong, Subject: "checks.c",
				Message: "checks.c: pattern is 1025 bytes; the limit is 1024",
			}},
		},
		{
			name: "a pattern at the byte limit",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: no-added-pattern/1\n    pattern: '" + strings.Repeat("a", 1024) + "'\n",
		},
		{
			name: "a pattern that does not compile",
			yaml: "lesson_checks_version: 1\nchecks:\n  no-todo:\n    kind: no-added-pattern/1\n    pattern: 'TODO('\n",
			want: []ConfigError{{
				Code: CodeCheckPatternInvalid, Subject: "checks.no-todo",
				Message: "checks.no-todo: pattern does not compile: error parsing regexp: missing closing ): `TODO(`",
			}},
		},
		{
			name: "an unknown mode is not executable",
			yaml: "lesson_checks_version: 1\npin:\n  - rule: r\n    region: ' a'\n    note: n\n    mode: strict\n",
			want: []ConfigError{{
				Code: CodePinModeUnknown, Subject: "pin 1",
				Message: `pin 1 (r @ a): unknown mode "strict"; use advisory, check, or enforce`,
			}},
		},
		{
			name: "a check mode without a check",
			yaml: "lesson_checks_version: 1\npin:\n  - rule: r\n    region: a\n    note: n\n    mode: check\n",
			want: []ConfigError{{
				Code: CodePinCheckRequired, Subject: "pin 1",
				Message: "pin 1 (r @ a): mode check requires check: <check-id>",
			}},
		},
		{
			name: "an advisory pin with an undefined check",
			yaml: "lesson_checks_version: 1\npin:\n  - rule: r\n    region: a\n    note: n\n    check: gone\n",
			want: []ConfigError{{
				Code: CodePinCheckMissing, Subject: "pin 1",
				Message: `pin 1 (r @ a): check "gone" is not defined under checks`,
			}},
		},
		{
			name: "a pin bound to a refused definition gets no reference error",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: no-added-pattern/1\n    pattern: '('\n" +
				"pin:\n  - rule: r\n    region: a\n    note: n\n    mode: check\n    check: c\n",
			want: []ConfigError{{
				Code: CodeCheckPatternInvalid, Subject: "checks.c",
				Message: "checks.c: pattern does not compile: error parsing regexp: missing closing ): `(`",
			}},
		},
		{
			name: "executable pins need canonical regions",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: forbid-path-change/1\n" +
				"pin:\n  - rule: r\n    region: ' a'\n    regions: [./b, c//, ../d, e/]\n    note: n\n    mode: enforce\n    check: c\n",
			want: []ConfigError{
				{
					Code: CodePinRegionInvalid, Subject: "pin 1",
					Message: `pin 1 (r @ ../d, ./b, a, c/, e): region " a" cannot scope a check: surrounding spaces`,
				},
				{
					Code: CodePinRegionInvalid, Subject: "pin 1",
					Message: `pin 1 (r @ ../d, ./b, a, c/, e): region "../d" cannot scope a check: ` +
						`invalid region: the region contains ".."; it must stay inside the repository`,
				},
				{
					Code: CodePinRegionInvalid, Subject: "pin 1",
					Message: `pin 1 (r @ ../d, ./b, a, c/, e): region "./b" cannot scope a check: leading "./" removed`,
				},
				{
					Code: CodePinRegionInvalid, Subject: "pin 1",
					Message: `pin 1 (r @ ../d, ./b, a, c/, e): region "c/" cannot scope a check: trailing "/" removed`,
				},
			},
		},
		{
			name: "an advisory pin keeps any region",
			yaml: "lesson_checks_version: 1\npin:\n  - rule: r\n    region: ' a'\n    regions: [../d]\n    note: n\n",
		},
		{
			name: "a repository-wide executable pin passes",
			yaml: "lesson_checks_version: 1\nchecks:\n  c:\n    kind: forbid-path-change/1\n" +
				"pin:\n  - rule: r\n    region: ''\n    note: n\n    mode: check\n    check: c\n",
		},
		{
			name: "the order is version, checks by id, then pins by entry",
			yaml: "checks:\n  zz:\n    kind: forbid-path-change/1\n    pattern: x\n  aa:\n    kind: nope\n    extra: 1\n" +
				"pin:\n  - rule: one\n    region: a\n    note: n\n    mode: on\n    check: missing\n" +
				"  - rule: two\n    region: ./b\n    note: n\n    mode: check\n",
			want: []ConfigError{
				{Code: CodeVersionMissing, Subject: "lesson_checks_version", Message: "checks needs lesson_checks_version: 1"},
				{Code: CodeCheckFieldUnknown, Subject: "checks.aa", Message: `checks.aa: unknown field "extra"`},
				{
					Code: CodeCheckKindUnknown, Subject: "checks.aa",
					Message: `checks.aa: unknown checker kind "nope" (known: forbid-path-change/1, no-added-pattern/1)`,
				},
				{Code: CodeCheckPatternUnexpected, Subject: "checks.zz", Message: "checks.zz: forbid-path-change/1 takes no pattern"},
				// Inside one subject the byte order of the code decides:
				// pin-check-missing comes before pin-mode-unknown.
				{Code: CodePinCheckMissing, Subject: "pin 1", Message: `pin 1 (one @ a): check "missing" is not defined under checks`},
				{Code: CodePinModeUnknown, Subject: "pin 1", Message: `pin 1 (one @ a): unknown mode "on"; use advisory, check, or enforce`},
				{Code: CodePinCheckRequired, Subject: "pin 2", Message: "pin 2 (two @ ./b): mode check requires check: <check-id>"},
				{Code: CodePinRegionInvalid, Subject: "pin 2", Message: `pin 2 (two @ ./b): region "./b" cannot scope a check: leading "./" removed`},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validateYAML(t, tc.yaml))
		})
	}

	t.Run("a negative version from a caller that skips the decode tier", func(t *testing.T) {
		assert.Equal(t, []ConfigError{{
			Code: CodeVersionUnsupported, Subject: "lesson_checks_version",
			Message: "lesson_checks_version -1 is not supported; this seamark reads up to 1",
		}}, Validate(&reviews.Config{LessonChecksVersion: -1}))
	})

	t.Run("the pin label is sanitized and cut at 80 runes", func(t *testing.T) {
		rule := "a\x1b[31m" + strings.Repeat("é", 100)
		errs := Validate(&reviews.Config{
			LessonChecksVersion: 1,
			Pin:                 []reviews.PinRule{{Rule: rule, Region: "src", Mode: "check"}},
		})
		require.Len(t, errs, 1)

		label := "a[31m" + strings.Repeat("é", 74) + "…"
		assert.Equal(t, "pin 1 ("+label+"): mode check requires check: <check-id>", errs[0].Message)
	})

	t.Run("every error carries the lessons config sentinel", func(t *testing.T) {
		errs := validateYAML(t, invalidYAML)
		require.NotEmpty(t, errs)

		for _, e := range errs {
			var err error = e

			require.ErrorIs(t, err, reviews.ErrLessonsConfig)
			assert.Equal(t, lessonsConfigHead+e.Message, err.Error())

			var target ConfigError

			require.True(t, errors.As(err, &target))
			assert.Equal(t, e, target)
		}
	})
}

// TestLessonCheckParseReturnsConfigWithErrors proves the result shape of
// Parse: a tier-2 error returns the Config together with its errors,
// every pin stays listed, and a refused definition is absent from the
// compiled checks. A tier-1 error returns no Config.
func TestLessonCheckParseReturnsConfigWithErrors(t *testing.T) {
	t.Run("invalidYAML", func(t *testing.T) {
		cfg, errs, err := Parse([]byte(invalidYAML))
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, errs, cfg.Errors)
		assert.Equal(t, sha256Hex(invalidYAML), cfg.SHA256)

		// The errors encode as the "errors" array of the JSON outputs.
		encoded, err := json.Marshal(errs)
		require.NoError(t, err)
		assert.JSONEq(t, `[
			{"code": "check-pattern-invalid", "subject": "checks.no-todo", "message": "checks.no-todo: pattern does not compile: error parsing regexp: missing closing ): `+"`TODO(`"+`"},
			{"code": "pin-check-missing", "subject": "pin 2", "message": "pin 2 (no-todo @ src): check \"no-todos\" is not defined under checks"}
		]`, string(encoded))

		require.Len(t, cfg.Pins, 2)
		assert.Equal(t, ModeAdvisory, cfg.Pins[0].Mode)
		assert.Equal(t, "", cfg.Pins[0].CheckID)
		assert.Equal(t, ModeCheck, cfg.Pins[1].Mode)
		assert.Equal(t, "no-todos", cfg.Pins[1].CheckID)
		assert.Equal(t, 2, cfg.Pins[1].Entry)

		assert.NotContains(t, cfg.Checks, "no-todo", "a definition that does not compile is absent")
		assert.Empty(t, cfg.Checks)
	})

	t.Run("an unknown mode lists as advisory", func(t *testing.T) {
		cfg, errs, err := Parse([]byte("lesson_checks_version: 1\npin:\n  - rule: r\n    region: a\n    note: n\n    mode: strict\n"))
		require.NoError(t, err)
		require.Len(t, errs, 1)
		assert.Equal(t, CodePinModeUnknown, errs[0].Code)

		require.Len(t, cfg.Pins, 1)
		assert.Equal(t, ModeAdvisory, cfg.Pins[0].Mode)
		assert.Equal(t, "strict", cfg.Pins[0].Rule.Mode, "the raw rule keeps the written mode")

		// The version keeps the unknown mode apart from advisory.
		assert.Equal(t, cfg.Raw.PinVersion(cfg.Pins[0].Rule), cfg.Pins[0].Version)
		assert.NotEqual(t, cfg.Raw.PinVersion(reviews.PinRule{Rule: "r", Region: "a", Note: "n"}), cfg.Pins[0].Version)
	})

	t.Run("a newer version keeps every pin", func(t *testing.T) {
		cfg, errs, err := Parse([]byte("lesson_checks_version: 2\nchecks:\n  c:\n    kind: forbid-path-change/1\n" +
			"pin:\n  - rule: a\n    region: x\n    note: n\n    mode: enforce\n    check: c\n" +
			"  - rule: b\n    region: y\n    note: n\n"))
		require.NoError(t, err)
		require.Len(t, errs, 1)
		assert.Equal(t, CodeVersionUnsupported, errs[0].Code)
		assert.Equal(t, errs, cfg.Errors)

		require.Len(t, cfg.Pins, 2)
		assert.Equal(t, ModeEnforce, cfg.Pins[0].Mode)
		assert.Equal(t, ModeAdvisory, cfg.Pins[1].Mode)
		assert.Empty(t, cfg.Checks, "this binary compiles no definition of a newer format")
	})

	t.Run("a decode error returns no config", func(t *testing.T) {
		text := "lesson_checks_version: 1\nchecks:\n  a:\n    kind: forbid-path-change/1\n  a:\n    kind: forbid-path-change/1\n"

		cfg, errs, err := Parse([]byte(text))
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Nil(t, errs)
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)

		_, rawErr := reviews.ParseConfig([]byte(text))
		require.Error(t, rawErr)
		assert.Equal(t, rawErr.Error(), err.Error(), "Parse returns the decode error unchanged")
	})
}

// TestLessonCheckLoadFileStates proves that Load reads only an absent
// file as "no checks", because an unreadable file is not an empty one. It
// also proves that Load returns the Parse result for the bytes of a
// present file.
func TestLessonCheckLoadFileStates(t *testing.T) {
	t.Run("no .seamark directory", func(t *testing.T) {
		cfg, errs, err := Load(t.TempDir())
		require.NoError(t, err)
		assert.Nil(t, errs)
		require.NotNil(t, cfg)
		assert.Equal(t, "", cfg.SHA256, "an absent file has no digest")
		assert.Equal(t, reviews.DefaultConfig(), cfg.Raw)
		assert.Empty(t, cfg.Pins)
		assert.Empty(t, cfg.Checks)
		assert.Nil(t, cfg.Errors)
	})

	t.Run("no lessons.yaml", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o755))

		cfg, errs, err := Load(root)
		require.NoError(t, err)
		assert.Nil(t, errs)
		assert.Equal(t, "", cfg.SHA256)
		assert.Empty(t, cfg.Pins)
	})

	t.Run("an empty file is present", func(t *testing.T) {
		root := t.TempDir()
		writeLessons(t, root, "")

		cfg, errs, err := Load(root)
		require.NoError(t, err)
		assert.Nil(t, errs)
		assert.Equal(t, emptyBytesSHA256, cfg.SHA256, "an empty file has the digest of no bytes, not the absent value")
		assert.Empty(t, cfg.Pins)
	})

	t.Run("a valid file equals Parse of its bytes", func(t *testing.T) {
		root := t.TempDir()
		writeLessons(t, root, referencePatternYAML)

		cfg, errs, err := Load(root)
		require.NoError(t, err)
		assert.Nil(t, errs)

		parsed, _, err := Parse([]byte(referencePatternYAML))
		require.NoError(t, err)
		assert.Equal(t, parsed, cfg)
		assert.Equal(t, sha256Hex(referencePatternYAML), cfg.SHA256)
	})

	t.Run("a tier-2 file returns the config and its errors", func(t *testing.T) {
		root := t.TempDir()
		writeLessons(t, root, invalidYAML)

		cfg, errs, err := Load(root)
		require.NoError(t, err)
		require.Len(t, errs, 2)
		require.NotNil(t, cfg)
		assert.Equal(t, errs, cfg.Errors)
		assert.Len(t, cfg.Pins, 2)
	})

	t.Run("a tier-1 file returns the decode error", func(t *testing.T) {
		root := t.TempDir()
		writeLessons(t, root, "lesson_checks_version: -1\n")

		cfg, errs, err := Load(root)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Nil(t, errs)
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)
		assert.Equal(t, "lessons config: lesson_checks_version must be 1, got -1", err.Error())

		_, rawErr := reviews.LoadConfig(root)
		require.Error(t, rawErr)
		assert.Equal(t, rawErr.Error(), err.Error(), "Load and reviews.LoadConfig agree on the text")
	})

	t.Run("a directory in place of the file is an error, not absence", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "lessons.yaml"), 0o755))

		cfg, errs, err := Load(root)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Nil(t, errs)
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)
		assert.NotErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("a file in place of .seamark is an error, not absence", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark"), []byte("x"), 0o644))

		cfg, errs, err := Load(root)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Nil(t, errs)
		require.ErrorIs(t, err, reviews.ErrLessonsConfig)
		assert.NotErrorIs(t, err, os.ErrNotExist)
	})
}

// TestLessonCheckExecutablePredicates proves the exported predicates that
// the CLI, the preparation, and the mode writer call. An unknown mode
// must never read as executable.
func TestLessonCheckExecutablePredicates(t *testing.T) {
	t.Run("Mode.Executable", func(t *testing.T) {
		assert.True(t, ModeCheck.Executable())
		assert.True(t, ModeEnforce.Executable())
		assert.False(t, ModeAdvisory.Executable())
		assert.False(t, Mode("").Executable())
		assert.False(t, Mode("strict").Executable())
	})

	t.Run("HasExecutablePin", func(t *testing.T) {
		assert.False(t, HasExecutablePin(&Config{}))
		assert.False(t, HasExecutablePin(&Config{Pins: []Pin{{Mode: ModeAdvisory}}}))
		assert.True(t, HasExecutablePin(&Config{Pins: []Pin{{Mode: ModeAdvisory}, {Mode: ModeCheck}}}))
		assert.True(t, HasExecutablePin(&Config{Pins: []Pin{{Mode: ModeEnforce}}}))
	})

	t.Run("HasLimit", func(t *testing.T) {
		hits := []LimitHit{{Name: LimitVerifyTimeout}, {Name: LimitGuardBudget}}

		assert.True(t, HasLimit(hits, LimitGuardBudget))
		assert.False(t, HasLimit(hits, "other"))
		assert.False(t, HasLimit(nil, LimitGuardBudget))
	})
}

// TestLessonCheckSortedScope proves the scope form that the checkers, the
// preparation, and the delivery report share.
func TestLessonCheckSortedScope(t *testing.T) {
	regions := []string{"src/b", "src/a"}

	assert.Equal(t, []string{"src/a", "src/b"}, SortedScope(regions))
	assert.Equal(t, []string{"src/b", "src/a"}, regions, "the caller keeps its order")

	data, err := json.Marshal(SortedScope(nil))
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data), "a repository-wide scope encodes as []")
}

// TestLessonCheckPinRefsAtLoad proves that every pin view holds the
// reference of ShortPinRef, also for a duplicate entry. The two pins named
// r1080792 and r8398215 have identity digests that share 12 hex characters.
func TestLessonCheckPinRefsAtLoad(t *testing.T) {
	cfg, errs, err := Parse([]byte(`pin:
  - rule: r1080792
    region: x
  - rule: r8398215
    region: x
  - rule: r1080792
    region: x/
    note: The same rule and region set.
`))
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Len(t, cfg.Pins, 3)

	for _, pin := range cfg.Pins {
		assert.Equal(t, cfg.Raw.ShortPinRef(pin.Key), pin.Ref, "entry %d", pin.Entry)
		assert.Len(t, pin.Ref, reviews.ShownPinRef+1, "entry %d", pin.Entry)
	}
}

// TestLessonCheckExecutablePinsCovering proves the count that check
// without lesson verification names: the pins of mode check or enforce
// whose scope holds a changed path, each pin once, by the scope rule of
// the checkers. An advisory pin, a pin outside the paths, and an empty
// path list count nothing; an empty scope covers every path.
func TestLessonCheckExecutablePinsCovering(t *testing.T) {
	cfg, errs, err := Parse([]byte(`lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
pin:
  - rule: frozen-assets
    region: generated/
    note: Do not edit.
    mode: enforce
    check: frozen-assets
  - rule: frozen-docs
    region: docs/
    note: Do not edit.
    mode: check
    check: frozen-assets
  - rule: advisory-src
    region: src/
    note: Only advice.
    check: frozen-assets
`))
	require.NoError(t, err)
	require.Empty(t, errs)

	assert.Equal(t, 1, cfg.ExecutablePinsCovering([]string{"generated/a.txt", "generated/b.txt"}), "one pin counts once")
	assert.Equal(t, 2, cfg.ExecutablePinsCovering([]string{"generated/a.txt", "docs/x.md", "src/a.go"}))
	assert.Equal(t, 0, cfg.ExecutablePinsCovering([]string{"src/a.go"}), "an advisory pin is not executable")
	assert.Equal(t, 0, cfg.ExecutablePinsCovering([]string{"generatedx/a.txt"}), "a region covers the paths below it only")
	assert.Equal(t, 0, cfg.ExecutablePinsCovering(nil))

	wide := &Config{Pins: []Pin{{Mode: ModeCheck, Scope: []string{}}}}
	assert.Equal(t, 1, wide.ExecutablePinsCovering([]string{"any/path.go"}), "an empty scope is repository-wide")
}
