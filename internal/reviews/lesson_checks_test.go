package reviews

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// exampleConfig is the example lessons.yaml: two check definitions and
// one enforced pin.
const exampleConfig = `lesson_checks_version: 1
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

// TestConfigDecodesLessonChecks proves the decode tier of the
// lesson-check keys. ParseConfig reads the new keys and refuses only
// YAML and type errors, a duplicate check ID, and a negative version.
// Every semantic rule belongs to lessoncheck.Validate, so an advisory
// reader keeps its pins.
func TestConfigDecodesLessonChecks(t *testing.T) {
	t.Run("the example", func(t *testing.T) {
		cfg, err := ParseConfig([]byte(exampleConfig))
		require.NoError(t, err)

		assert.Equal(t, 1, cfg.LessonChecksVersion)
		assert.Equal(t, map[string]CheckDef{
			"frozen-assets": {Kind: "forbid-path-change/1"},
			"no-todo":       {Kind: "no-added-pattern/1", Pattern: `TODO\(`},
		}, cfg.Checks)
		assert.Equal(t, []PinRule{{
			Rule:   "frozen-assets",
			Region: "generated/",
			Note:   "Changes to these files require a maintainer-managed update.",
			Mode:   "enforce",
			Check:  "frozen-assets",
		}}, cfg.Pin)
	})

	t.Run("a file without the new keys", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("pin:\n  - rule: a\n    region: api\n    note: n\n"))
		require.NoError(t, err)

		assert.Zero(t, cfg.LessonChecksVersion)
		assert.Nil(t, cfg.Checks)
		require.Len(t, cfg.Pin, 1)
		assert.Empty(t, cfg.Pin[0].Mode)
		assert.Empty(t, cfg.Pin[0].Check)
	})

	t.Run("unknown definition keys go to Unknown", func(t *testing.T) {
		// yaml.v3 drops an unknown struct key silently. The inline map
		// keeps it, so Validate can refuse "flags: i".
		body := "lesson_checks_version: 1\nchecks:\n  no-todo:\n    kind: no-added-pattern/1\n" +
			"    pattern: x\n    flags: i\n    limit: 3\n"

		cfg, err := ParseConfig([]byte(body))
		require.NoError(t, err)

		def := cfg.Checks["no-todo"]
		assert.Equal(t, "no-added-pattern/1", def.Kind)
		assert.Equal(t, "x", def.Pattern)
		assert.Equal(t, map[string]any{"flags": "i", "limit": 3}, def.Unknown)
	})

	t.Run("a null or empty definition is one zero CheckDef", func(t *testing.T) {
		// Validate reports each as check-kind-missing, so each must stay
		// in the map.
		cfg, err := ParseConfig([]byte("lesson_checks_version: 1\nchecks:\n  a:\n  b: ~\n  c: {}\n"))
		require.NoError(t, err)

		require.Len(t, cfg.Checks, 3)

		for _, id := range []string{"a", "b", "c"} {
			def, ok := cfg.Checks[id]
			require.True(t, ok, "check %q is in the map", id)
			assert.Zero(t, def.Kind, "check %q", id)
			assert.Zero(t, def.Pattern, "check %q", id)
			assert.Empty(t, def.Unknown, "check %q", id)
		}
	})

	t.Run("semantic errors decode", func(t *testing.T) {
		// Each value here is a tier-2 error of lessoncheck.Validate. The
		// decode keeps them, so every reader still sees the pins.
		body := "checks:\n  BAD id:\n    kind: grep/9\n    pattern: 'TODO('\n" +
			"pin:\n  - rule: a\n    region: ' api'\n    note: n\n    mode: sometimes\n    check: missing\n"

		cfg, err := ParseConfig([]byte(body))
		require.NoError(t, err)

		assert.Zero(t, cfg.LessonChecksVersion)
		assert.Equal(t, CheckDef{Kind: "grep/9", Pattern: "TODO("}, cfg.Checks["BAD id"])
		require.Len(t, cfg.Pin, 1)
		assert.Equal(t, "sometimes", cfg.Pin[0].Mode)
		assert.Equal(t, "missing", cfg.Pin[0].Check)
	})

	t.Run("a newer version keeps the pins", func(t *testing.T) {
		// A version above the supported one is the tier-2 error
		// version-unsupported. The advisory readers must not fall back to
		// DefaultConfig and drop every pin.
		body := "lesson_checks_version: 2\nchecks:\n  x:\n    kind: future-kind/4\n    window: 5\n" +
			"pin:\n  - rule: a\n    region: api\n    note: n\n    mode: audit\n    check: x\n"

		cfg, err := ParseConfig([]byte(body))
		require.NoError(t, err)

		assert.Equal(t, MaxLessonChecksVersion+1, cfg.LessonChecksVersion)
		assert.Equal(t, map[string]any{"window": 5}, cfg.Checks["x"].Unknown)
		require.Len(t, cfg.Pin, 1)
		assert.Equal(t, PinKey{Rule: "a", Region: "api"}, NewPinKey(cfg.Pin[0].Rule, cfg.Pin[0].Region, cfg.Pin[0].Regions))
	})

	t.Run("tier-1 errors", func(t *testing.T) {
		cases := []struct {
			name, body, wantErr string
		}{
			{
				name:    "negative version",
				body:    "lesson_checks_version: -1\npin:\n  - rule: a\n",
				wantErr: "lessons config: lesson_checks_version must be 1, got -1",
			},
			{
				name:    "duplicate check id",
				body:    "lesson_checks_version: 1\nchecks:\n  a:\n    kind: forbid-path-change/1\n  a:\n    kind: forbid-path-change/1\n",
				wantErr: `lessons config: yaml: unmarshal errors:` + "\n" + `  line 5: mapping key "a" already defined at line 3`,
			},
			{
				name:    "scalar definition",
				body:    "checks:\n  a: forbid-path-change/1\n",
				wantErr: "lessons config: yaml: unmarshal errors:\n  line 2: cannot unmarshal !!str `forbid-...` into reviews.CheckDef",
			},
			{
				name:    "sequence definition",
				body:    "checks:\n  a: [forbid-path-change/1]\n",
				wantErr: "lessons config: yaml: unmarshal errors:\n  line 2: cannot unmarshal !!seq into reviews.CheckDef",
			},
			{
				name:    "checks as a list",
				body:    "checks:\n  - kind: forbid-path-change/1\n",
				wantErr: "lessons config: yaml: unmarshal errors:\n  line 2: cannot unmarshal !!seq into map[string]reviews.CheckDef",
			},
			{
				name:    "text version",
				body:    "lesson_checks_version: one\n",
				wantErr: "lessons config: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `one` into int",
			},
			{
				name:    "list mode",
				body:    "pin:\n  - rule: a\n    mode: [check]\n",
				wantErr: "lessons config: yaml: unmarshal errors:\n  line 3: cannot unmarshal !!seq into string",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				parsed, parseErr := ParseConfig([]byte(tc.body))
				loaded, loadErr := LoadConfig(writeLessons(t, tc.body))

				require.Error(t, parseErr)
				require.ErrorIs(t, parseErr, ErrLessonsConfig)
				assert.Equal(t, tc.wantErr, parseErr.Error())
				assert.Nil(t, parsed)

				require.Error(t, loadErr)
				assert.Equal(t, parseErr.Error(), loadErr.Error(), "LoadConfig prints the ParseConfig text")
				assert.Nil(t, loaded)
			})
		}
	})

	t.Run("pins without the new fields marshal to the old bytes", func(t *testing.T) {
		// distill.RenderPins marshals PinRule. Mode and Check are
		// omitempty and come after Note, so an advisory pin keeps its
		// bytes and a bound pin appends its two keys.
		advisory := PinRule{Rule: "a", Region: "api", Regions: []string{"api", "db"}, Note: "n"}

		out, err := yaml.Marshal([]PinRule{advisory})
		require.NoError(t, err)
		assert.Equal(t, "- rule: a\n  region: api\n  regions: [api, db]\n  note: \"n\"\n", string(out))

		bound := advisory
		bound.Mode, bound.Check = "check", "no-todo"

		out, err = yaml.Marshal([]PinRule{bound})
		require.NoError(t, err)
		assert.Equal(t, "- rule: a\n  region: api\n  regions: [api, db]\n  note: \"n\"\n  mode: check\n  check: no-todo\n", string(out))
	})
}
