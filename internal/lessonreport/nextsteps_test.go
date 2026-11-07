package lessonreport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestNextStepsStayEmpty gives the report each input that
// it reads: delivered and held-back pins with paths, a linked proposal,
// an authored check on an advisory pin, and a check stream. The next
// steps are a fixed field, so no list holds a step.
func TestNextStepsStayEmpty(t *testing.T) {
	const text = "lesson_checks_version: 1\nchecks:\n  authored:\n    kind: forbid-path-change/1\npin:\n  - rule: keep\n    region: src\n    check: authored\n"

	cfg, err := reviews.ParseConfig([]byte(text))
	require.NoError(t, err)

	checks, errs, err := lessoncheck.Parse([]byte(text))
	require.NoError(t, err)
	require.Empty(t, errs)

	id, version := pinOf(cfg, 0)
	injected := v2("e1", "s1", "m1", pin(id, version, reviews.PinInjected))
	injected.File = "src/a.go"
	held := v2("e2", "s2", "m2", reviews.FiringPin{Identity: id, Version: version, Delivery: reviews.PinHeldBack, Cause: reviews.CauseOverTotal})
	held.File = "src/b.go"

	in := Inputs{
		Audit: auditLines(injected, held), Config: cfg, Checks: checks, Links: map[string]int64{id: 42},
		CheckAudit: checkPrefix(lessoncheck.AuditRecord{Kind: lessoncheck.AuditVerify, Pins: []lessoncheck.AuditPin{
			{PinIdentity: id, PinVersion: version, Outcome: lessoncheck.OutcomeViolation},
		}}),
		Now: fixedNow,
	}

	for _, ref := range []string{"", id[:12]} {
		r := mustBuild(t, in, ref)
		require.Len(t, r.Pins, 1)
		assert.Equal(t, "advisory", r.Pins[0].Mode)
		assert.Equal(t, 1, r.Pins[0].Delivery.Injected)
		assert.Equal(t, 1, r.Pins[0].Delivery.HeldBack)
		assert.Equal(t, 1, r.Pins[0].Checks.PinEvaluations)
		assert.Equal(t, []NextStep{}, r.Pins[0].NextSteps, "ref %q", ref)
		assert.Equal(t, []NextStep{}, r.Unlinked.NextSteps, "ref %q", ref)
		assert.Contains(t, encode(t, r), `"next_steps":[]`)
	}
}
