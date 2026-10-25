package hooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lessonBinary is the binary path of the lesson-check commands here.
const lessonBinary = "/bin/seamark"

func TestLessonCheckSpecsUseDistinctMarkers(t *testing.T) {
	// The specs of contract K10: events, matchers, markers, legacy
	// pairing, and timeouts (contracts §5 row 36).
	type shape struct {
		event, matcher, marker string
		legacy                 []string
		timeout                int
	}

	shapes := func(specs []Spec) []shape {
		var out []shape
		for _, s := range specs {
			out = append(out, shape{s.Event, s.Matcher, s.Marker, s.Legacy, s.Timeout})
		}

		return out
	}

	edit := "Edit|Write|MultiEdit"

	assert.Equal(t, []shape{
		{"PreToolUse", edit, "lessons --guard --hook", []string{"lessons --baseline --hook"}, 10},
		{"PostToolUse", edit, "lessons --verify --hook", nil, 15},
		{"PostToolUseFailure", edit, "lessons --verify --hook", nil, 15},
	}, shapes(ClaudeLessonCheckSpecs(LessonChecksEnforce)))
	assert.Equal(t, []shape{
		{"PreToolUse", edit, "lessons --baseline --hook", []string{"lessons --guard --hook"}, 10},
		{"PostToolUse", edit, "lessons --verify --hook", nil, 15},
		{"PostToolUseFailure", edit, "lessons --verify --hook", nil, 15},
	}, shapes(ClaudeLessonCheckSpecs(LessonChecksCheck)))
	assert.Equal(t, []shape{
		{"PreToolUse", "apply_patch", "lessons --guard --hook --client codex", []string{"lessons --baseline --hook --client codex"}, 10},
		{"PostToolUse", "apply_patch", "lessons --verify --hook --client codex", nil, 15},
	}, shapes(CodexLessonCheckSpecs(LessonChecksEnforce)))
	assert.Equal(t, []shape{
		{"PreToolUse", "apply_patch", "lessons --baseline --hook --client codex", []string{"lessons --guard --hook --client codex"}, 10},
		{"PostToolUse", "apply_patch", "lessons --verify --hook --client codex", nil, 15},
	}, shapes(CodexLessonCheckSpecs(LessonChecksCheck)))

	// Exploration runs on a successful Read only (contracts §5 row 89),
	// without a status message on every read.
	explore := ClaudeExplorationSpec()
	assert.Equal(t, shape{"PostToolUse", "Read", "lessons --explore --hook", nil, 10},
		shape{explore.Event, explore.Matcher, explore.Marker, explore.Legacy, explore.Timeout})
	assert.Empty(t, explore.Status)

	// The specs of either mode name the markers of both modes.
	assert.Equal(t, []string{LessonGuardMarker, LessonBaselineMarker, LessonVerifyMarker},
		LessonCheckMarkers(ClaudeLessonCheckSpecs(LessonChecksEnforce)))
	assert.ElementsMatch(t, LessonCheckMarkers(CodexLessonCheckSpecs(LessonChecksEnforce)),
		LessonCheckMarkers(CodexLessonCheckSpecs(LessonChecksCheck)))

	assert.Equal(t, LessonChecksEnforce, LessonCheckMarkerMode(LessonGuardMarker))
	assert.Equal(t, LessonChecksEnforce, LessonCheckMarkerMode(CodexLessonGuardMarker))
	assert.Equal(t, LessonChecksCheck, LessonCheckMarkerMode(LessonBaselineMarker))
	assert.Equal(t, LessonChecksCheck, LessonCheckMarkerMode(CodexLessonBaselineMarker))
	assert.Empty(t, LessonCheckMarkerMode(LessonVerifyMarker))
	assert.Empty(t, LessonCheckMarkerMode(LessonsMarker))

	// No marker owns or runs the command of another one: not the gate,
	// lessons, or reset markers, not the other client's, and not the
	// other lesson-check markers. A suffix rule would otherwise let a
	// re-run rewrite one hook into another.
	markers := []string{
		LessonGuardMarker, LessonBaselineMarker, LessonVerifyMarker, LessonExploreMarker,
		CodexLessonGuardMarker, CodexLessonBaselineMarker, CodexLessonVerifyMarker,
		LessonsMarker, LessonsResetMarker, CodexLessonsMarker, CodexLessonsResetMarker,
		GateMarker(ModeWarn), GateMarker(ModeEnforce), CodexGateMarker(ModeWarn), CodexGateMarker(ModeEnforce),
	}

	for _, a := range markers {
		cmd := Spec{Marker: a}.Command(lessonBinary)
		assert.True(t, OwnedBySeamark(cmd, []string{a}), "%q owns its own command", a)
		assert.Equal(t, HookRuns, SeamarkHookUse(cmd, []string{a}), "%q runs its own command", a)

		for _, b := range markers {
			if a == b {
				continue
			}

			assert.False(t, OwnedBySeamark(cmd, []string{b}), "%q must not own the command of %q", b, a)
			assert.Equal(t, HookNotRun, SeamarkHookUse(cmd, []string{b}), "the command of %q must not run %q", a, b)
		}
	}
}

func TestInstalledLessonChecksReadsMarkers(t *testing.T) {
	claude := ClaudeLessonCheckSpecs(LessonChecksEnforce)
	codex := CodexLessonCheckSpecs(LessonChecksEnforce)

	merge := func(t *testing.T, document map[string]any, specs []Spec) {
		t.Helper()

		_, err := Merge(document, lessonBinary, specs)
		require.NoError(t, err)
	}

	t.Run("nothing installed reads off", func(t *testing.T) {
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(map[string]any{}, claude))
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(map[string]any{}, nil))

		// The lessons hook and the gate are no lesson-check hooks.
		document := map[string]any{}
		merge(t, document, ClaudeSpecs(ModeEnforce))
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(document, claude))
	})

	t.Run("each mode reads its own pre-write marker", func(t *testing.T) {
		document := map[string]any{}
		merge(t, document, ClaudeLessonCheckSpecs(LessonChecksCheck))
		assert.Equal(t, LessonChecksCheck, InstalledLessonChecks(document, claude))

		// The legacy pairing rewrites the baseline into the guard in place.
		merge(t, document, ClaudeLessonCheckSpecs(LessonChecksEnforce))
		assert.Equal(t, LessonChecksEnforce, InstalledLessonChecks(document, claude))
		assert.Equal(t, []string{lessonBinary + " " + LessonGuardMarker}, commandsOf(document, "PreToolUse"))

		merge(t, document, ClaudeLessonCheckSpecs(LessonChecksCheck))
		assert.Equal(t, LessonChecksCheck, InstalledLessonChecks(document, claude))
		assert.Equal(t, []string{lessonBinary + " " + LessonBaselineMarker}, commandsOf(document, "PreToolUse"))
	})

	t.Run("a guard beside a baseline reads enforce", func(t *testing.T) {
		document := map[string]any{}
		merge(t, document, ClaudeLessonCheckSpecs(LessonChecksCheck))

		hookMap := document["hooks"].(map[string]any)
		hookMap["PreToolUse"] = append(hookMap["PreToolUse"].([]any), map[string]any{
			"matcher": "Edit", "hooks": []any{map[string]any{"type": "command", "command": lessonBinary + " " + LessonGuardMarker}},
		})

		assert.Equal(t, LessonChecksEnforce, InstalledLessonChecks(document, claude))
	})

	t.Run("one hook of the pair reads partial", func(t *testing.T) {
		pre := map[string]any{}
		merge(t, pre, claude[:1])
		assert.Equal(t, LessonChecksPartial, InstalledLessonChecks(pre, claude))
		assert.Equal(t, LessonChecksEnforce, InstalledLessonChecks(pre, claude[:1]), "the pre-write hook alone names its mode")

		post := map[string]any{}
		merge(t, post, claude[1:])
		assert.Equal(t, LessonChecksPartial, InstalledLessonChecks(post, claude))

		// Claude Code has two post-edit events; one of them is not enough.
		failureMissing := map[string]any{}
		merge(t, failureMissing, claude[:2])
		assert.Equal(t, LessonChecksPartial, InstalledLessonChecks(failureMissing, claude))
	})

	t.Run("a narrowed matcher keeps the mode", func(t *testing.T) {
		// The mode is the user's choice; the coverage gap of the narrowed
		// matcher is the capability report's to name.
		document := map[string]any{}
		narrowed := append([]Spec(nil), claude...)
		narrowed[0].Matcher = "Write"
		merge(t, document, narrowed)
		assert.Equal(t, LessonChecksEnforce, InstalledLessonChecks(document, claude))
	})

	t.Run("each client reads only its own markers", func(t *testing.T) {
		document := map[string]any{}
		merge(t, document, claude)
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(document, codex))

		merge(t, document, CodexLessonCheckSpecs(LessonChecksCheck))
		assert.Equal(t, LessonChecksCheck, InstalledLessonChecks(document, codex))
		assert.Equal(t, LessonChecksEnforce, InstalledLessonChecks(document, claude))
	})

	t.Run("a wrapped or foreign command is not installed", func(t *testing.T) {
		document := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
			"matcher": "Edit|Write|MultiEdit", "hooks": []any{
				map[string]any{"type": "command", "command": lessonBinary + " " + LessonGuardMarker + " || true"},
				map[string]any{"type": "command", "command": "/opt/other " + LessonGuardMarker},
			},
		}}}}
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(document, claude))
	})

	t.Run("removing every marker reads off and keeps the other hooks", func(t *testing.T) {
		document := map[string]any{}
		merge(t, document, ClaudeSpecs(ModeWarn))
		merge(t, document, claude)

		removed := RemoveOwned(document, LessonCheckMarkers(claude), lessonBinary)
		assert.Equal(t, 3, removed)
		assert.Equal(t, LessonChecksOff, InstalledLessonChecks(document, claude))
		assert.Equal(t, []string{lessonBinary + " gate --hook", lessonBinary + " lessons --hook"}, commandsOf(document, "PreToolUse"))
		assert.Equal(t, []string{lessonBinary + " lessons --hook-reset"}, commandsOf(document, "PostCompact"))
		assert.NotContains(t, document["hooks"], "PostToolUse", "an event that loses its last entry goes")
	})
}
