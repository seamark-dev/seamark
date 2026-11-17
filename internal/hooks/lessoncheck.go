package hooks

import "slices"

// This file holds the managed lesson-check and exploration hook
// entries. They are not in ClaudeSpecs and CodexSpecs. The lesson and
// gate inspection reads those lists by position, and init installs
// these entries only on an explicit request (--lesson-checks,
// --lesson-exploration). A fresh setup must add nothing new.

// The lesson-check modes. Off, check, and enforce are the values of
// `seamark init --lesson-checks`. Partial is an installed state only:
// one hook of the pre-write and post-edit pair runs without the other.
const (
	LessonChecksOff     = "off"
	LessonChecksCheck   = "check"
	LessonChecksEnforce = "enforce"
	LessonChecksPartial = "partial"
)

// The values of `seamark init --lesson-exploration`.
const (
	LessonExplorationOff = "off"
	LessonExplorationOn  = "on"
)

// The Claude Code markers of the lesson-check hooks. Each one is the
// argument tail of a `seamark lessons` handler. No marker ends with
// another marker of this package, so a suffix match never confuses two
// hooks.
const (
	// LessonGuardMarker is the pre-write guard of enforce mode. It
	// denies a covered violation before the write.
	LessonGuardMarker = "lessons --guard --hook"
	// LessonBaselineMarker is the pre-write hook of check mode. It
	// records the baseline and never denies.
	LessonBaselineMarker = "lessons --baseline --hook"
	// LessonVerifyMarker is the post-edit hook of both modes. It checks
	// the result of the edit after the tool ran.
	LessonVerifyMarker = "lessons --verify --hook"
	// LessonExploreMarker is the exploration hook on a structured read.
	LessonExploreMarker = "lessons --explore --hook"
)

// The Codex markers name their client, by the rule of the Codex lesson
// and gate markers. Codex has no structured read path, so it has no
// exploration marker.
const (
	CodexLessonGuardMarker    = LessonGuardMarker + codexClientSelector
	CodexLessonBaselineMarker = LessonBaselineMarker + codexClientSelector
	CodexLessonVerifyMarker   = LessonVerifyMarker + codexClientSelector
)

// The timeouts of the lesson-check hooks in seconds. The handlers keep
// their own budgets of 5 s and 10 s inside them, so each client stops a
// hook only after its handler gave up.
const (
	lessonPreWriteTimeout    = 10
	lessonPostEditTimeout    = 15
	lessonExplorationTimeout = 10
)

// The tools of the lesson-check hooks. They equal the tools of the
// lessons hook of each client, because the guard decoders cover exactly
// these edit tools.
const (
	claudeEditMatcher = "Edit|Write|MultiEdit"
	codexEditMatcher  = "apply_patch"
)

// lessonCheckClient holds the spellings of one client's lesson-check
// hooks.
type lessonCheckClient struct {
	guard, baseline, verify string
	matcher                 string
	// background lists the handler keys that make the client run a hook
	// in the background. The pre-write spec carries them, so Merge
	// removes them from seamark's own pre-write entry.
	background []string
	// postEvents lists the events after an edit. Claude Code reports a
	// failed tool through its own event, and the verification also reads
	// the partial edit of a failed tool.
	postEvents []string
}

var (
	claudeLessonChecks = lessonCheckClient{
		guard: LessonGuardMarker, baseline: LessonBaselineMarker, verify: LessonVerifyMarker,
		matcher: claudeEditMatcher, postEvents: []string{"PostToolUse", "PostToolUseFailure"},
		background: ClaudeBackgroundKeys,
	}
	// Codex has no failure event, so PostToolUse is its only post-edit
	// event. codex-cli 0.169.0 sends no PostToolUse after a patch that
	// fails during its execution, so no post-edit check runs for that
	// patch, and its baseline waits for the sweep.
	codexLessonChecks = lessonCheckClient{
		guard: CodexLessonGuardMarker, baseline: CodexLessonBaselineMarker, verify: CodexLessonVerifyMarker,
		matcher: codexEditMatcher, postEvents: []string{"PostToolUse"},
		background: CodexBackgroundKeys,
	}
)

// ClaudeLessonCheckSpecs returns the lesson-check hooks of Claude Code
// for a mode: the pre-write hook first, then one post-edit hook per
// event. See lessonCheckSpecs.
func ClaudeLessonCheckSpecs(mode string) []Spec {
	return claudeLessonChecks.specs(mode)
}

// CodexLessonCheckSpecs returns the lesson-check hooks of Codex for a
// mode, in the order of ClaudeLessonCheckSpecs.
func CodexLessonCheckSpecs(mode string) []Spec {
	return codexLessonChecks.specs(mode)
}

// specs builds the hooks of one mode. Enforce installs the guard, and
// any other mode installs the baseline. The pre-write hook lists the
// marker of the other mode as legacy, so a mode switch rewrites the
// existing entry in place, as a gate mode switch does. The post-edit
// hooks share one marker and are the same in both modes. The pre-write
// hook of both modes runs in the foreground: a guard denies only by exit
// status 2, and a baseline in the background can finish after the write.
func (c lessonCheckClient) specs(mode string) []Spec {
	pre := Spec{
		Name:  "lesson guard",
		Event: "PreToolUse", Matcher: c.matcher,
		Marker: c.guard, Legacy: []string{c.baseline},
		Status: "seamark: checking the lesson checks before the write", Timeout: lessonPreWriteTimeout,
		BackgroundKeys: c.background,
	}

	if mode != LessonChecksEnforce {
		pre.Name, pre.Marker, pre.Legacy = "lesson baseline", c.baseline, []string{c.guard}
		pre.Status = "seamark: recording the lesson-check baseline"
	}

	specs := []Spec{pre}

	for _, event := range c.postEvents {
		specs = append(specs, Spec{
			Name:  "lesson verify",
			Event: event, Matcher: c.matcher,
			Marker: c.verify,
			Status: "seamark: verifying the lesson checks after the edit", Timeout: lessonPostEditTimeout,
		})
	}

	return specs
}

// ClaudeExplorationSpec returns the exploration hook of Claude Code: a
// structured Read reaches the hook after the tool ran. The status
// message stays empty, because a message on every read is noise. The
// failure event is not matched yet. In the timing recording of Claude
// Code 2.1.319, a test entry on that event gave a reply that reached the
// next model request.
func ClaudeExplorationSpec() Spec {
	return Spec{
		Name:  "lesson exploration",
		Event: "PostToolUse", Matcher: "Read",
		Marker: LessonExploreMarker, Timeout: lessonExplorationTimeout,
	}
}

// LessonCheckMarkers returns every marker of the specs, without
// duplicates, in spec order. The specs of either mode give the markers
// of both modes, because each pre-write hook lists the other one as
// legacy. `--lesson-checks off` removes exactly these markers.
func LessonCheckMarkers(specs []Spec) []string {
	var markers []string

	for _, spec := range specs {
		for _, marker := range spec.Markers() {
			if !slices.Contains(markers, marker) {
				markers = append(markers, marker)
			}
		}
	}

	return markers
}

// InstalledLessonChecks reads the lesson-check mode that a parsed hook
// document holds for one client. specs are the client's specs of either
// mode: the pre-write hook first, then the post-edit hooks.
//
// The rule reads the owned markers under each spec's event. It does not
// read the matcher, because the mode is a choice of the user, and a
// narrowed matcher is a coverage gap, not another mode. The capability
// report names such a gap per tool.
//
// Enforce wins over check when both pre-write markers are present,
// because one guard denies whatever the baseline does. The result is
// partial when the pre-write hook runs without every post-edit hook, or
// a post-edit hook runs without the pre-write hook.
func InstalledLessonChecks(document map[string]any, specs []Spec) string {
	if len(specs) == 0 {
		return LessonChecksOff
	}

	pre := ""

	forEachSpecCommand(document, specs[0], func(_ string, _ map[string]any, cmd string) {
		switch mode := lessonCheckMode(cmd, specs[0]); {
		case mode == LessonChecksEnforce:
			pre = LessonChecksEnforce
		case pre == "":
			pre = mode
		}
	})

	posts := 0

	for _, spec := range specs[1:] {
		if Owned(document, spec) {
			posts++
		}
	}

	switch {
	case pre == "" && posts == 0:
		return LessonChecksOff
	case pre != "" && posts == len(specs)-1:
		return pre
	default:
		return LessonChecksPartial
	}
}

// LessonCheckMarkerMode returns the mode that a pre-write marker
// installs: enforce for a guard marker, check for a baseline marker,
// and "" for any other marker. Inspection reads the mode of a hook
// definition through it, as it reads a gate mode through
// GateMarkerMode.
func LessonCheckMarkerMode(marker string) string {
	switch marker {
	case LessonGuardMarker, CodexLessonGuardMarker:
		return LessonChecksEnforce
	case LessonBaselineMarker, CodexLessonBaselineMarker:
		return LessonChecksCheck
	}

	return ""
}

// lessonCheckMode returns the mode of an owned pre-write command: the
// mode of the spec's marker that owns it.
func lessonCheckMode(cmd string, pre Spec) string {
	for _, marker := range pre.Markers() {
		if OwnedBySeamark(cmd, []string{marker}) {
			return LessonCheckMarkerMode(marker)
		}
	}

	return ""
}
