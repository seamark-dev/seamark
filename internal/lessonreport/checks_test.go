package lessonreport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// checkPrefix is a retained stream in logical file order. No test needs
// to write the real audit file to exercise the pure reader.
func checkPrefix(records ...lessoncheck.AuditRecord) lessoncheck.AuditPrefix {
	return lessoncheck.AuditPrefix{Generations: 2, Records: records, Bytes: int64(100 * len(records))}
}

// TestCheckReading keeps operations, evaluations, outcomes, and unknown
// reasons separate. An older version of one identity keeps its own check
// reading, so the current pin never borrows its counts.
func TestCheckReading(t *testing.T) {
	cfg := testConfig("n")
	id, version := pinOf(cfg, 0)
	old := digest64("old-version")
	pass := lessoncheck.AuditPin{PinIdentity: id, PinVersion: version, Outcome: lessoncheck.OutcomePass}
	unknown := pass
	unknown.Outcome, unknown.Reason = lessoncheck.OutcomeUnknown, lessoncheck.ReasonMissingBaseline
	violation := pass
	violation.PinVersion, violation.Outcome = old, lessoncheck.OutcomeViolation
	notApplicable := pass
	notApplicable.Outcome = lessoncheck.OutcomeNotApplicable
	prefix := checkPrefix(
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditBaseline, Pins: []lessoncheck.AuditPin{pass}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditVerify, Pins: []lessoncheck.AuditPin{pass, unknown, notApplicable}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditPostEdit, Pins: []lessoncheck.AuditPin{violation}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditGuard, Pins: []lessoncheck.AuditPin{unknown, unknown}, Guard: &lessoncheck.GuardDecision{Decision: "deny"}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditBaselineDiscarded, Pins: []lessoncheck.AuditPin{pass}},
	)
	in := Inputs{Config: cfg, CheckAudit: prefix}
	r := mustBuild(t, in, "")
	assert.Equal(t, StateRead, r.Checks.State)
	assert.Equal(t, r.AsOf.CheckAudit.State, r.Checks.State)
	assert.Equal(t, map[string]int{"verify": 1, "post-edit": 1, "guard": 1}, r.Checks.Operations)
	assert.Equal(t, 6, r.Checks.PinEvaluations)
	assert.Equal(t, map[string]int{"pass": 1, "unknown": 3, "violation": 1, "not-applicable": 1}, r.Checks.Outcomes)
	assert.Equal(t, map[string]int{"missing-baseline": 3}, r.Checks.UnknownByReason)
	assert.Equal(t, PinCheckReading{PinEvaluations: 5, Outcomes: map[string]int{"pass": 1, "unknown": 3, "not-applicable": 1}, RequestedDenies: 1}, r.Pins[0].Checks)
	assert.Equal(t, PinCheckReading{PinEvaluations: 1, Outcomes: map[string]int{"violation": 1}}, readChecks(in).pin(versionKey{id, old}))
	assert.NotContains(t, encode(t, r), "null")

	t.Run("unusable streams give no counts", func(t *testing.T) {
		for _, state := range []string{StateAbsent, StateUnreadable, StateOverLimit} {
			bad := in
			switch state {
			case StateAbsent:
				bad.CheckAudit = lessoncheck.AuditPrefix{}
			case StateUnreadable:
				bad.CheckAuditError = "permission denied"
			case StateOverLimit:
				bad.CheckAudit.OverLimit = true
			}

			r := mustBuild(t, bad, "")
			assert.Equal(t, state, r.Checks.State)
			assert.Empty(t, r.Checks.Operations)
			assert.Empty(t, r.Checks.Outcomes)
			assert.Empty(t, r.Checks.UnknownByReason)
			assert.Zero(t, r.Checks.PinEvaluations)
			assert.Equal(t, GuardAttempts{}, r.Checks.GuardAttempts)
			assert.Zero(t, r.Pins[0].Checks.PinEvaluations)
		}
	})

	t.Run("inactive badges count pins not clients", func(t *testing.T) {
		in.Capability = &lessoncheck.CapabilityReport{Pins: []lessoncheck.PinCapability{
			{PinRef: "a", PinVersion: version, Clients: []lessoncheck.PinClient{{Badge: lessoncheck.BadgeRequestedInactive}, {Badge: lessoncheck.BadgeRequestedInactive}}},
			{PinRef: "b", PinVersion: old, Clients: []lessoncheck.PinClient{{Badge: lessoncheck.BadgeChecked}}},
			{PinRef: "c", PinVersion: old, Clients: []lessoncheck.PinClient{{Badge: lessoncheck.BadgeRequestedInactive}}},
		}}
		in.CheckAuditError = "unreadable stream does not hide capability badges"
		assert.Equal(t, 2, mustBuild(t, in, "").Checks.InactiveEnforcement)
		in.Capability = nil
		assert.Zero(t, mustBuild(t, in, "").Checks.InactiveEnforcement)
	})
}

// TestGuardAttempts uses synthetic verified records only. Current native
// evidence is unverified and must never claim a prevented write.
func TestGuardAttempts(t *testing.T) {
	deny := func(match string, written bool, level string) lessoncheck.AuditRecord {
		return lessoncheck.AuditRecord{Kind: lessoncheck.AuditGuard, MatchSHA: match,
			Guard: &lessoncheck.GuardDecision{Decision: "deny", ReplyWritten: written, NativeBlocking: level}}
	}
	post := func(match string) lessoncheck.AuditRecord {
		return lessoncheck.AuditRecord{Kind: lessoncheck.AuditPostEdit, MatchSHA: match, Attribution: lessoncheck.AttributionNoBaseline}
	}

	for _, tc := range []struct {
		name    string
		records []lessoncheck.AuditRecord
		want    GuardAttempts
	}{
		{"current unverified", []lessoncheck.AuditRecord{deny("a", true, "unverified")}, GuardAttempts{RequestedDenies: 1, NoExecutionObserved: 1}},
		{"verified synthetic prevention", []lessoncheck.AuditRecord{deny("a", true, "verified")}, GuardAttempts{RequestedDenies: 1, VerifiedNative: 1, NoExecutionObserved: 1, Prevented: 1}},
		{"reply failed", []lessoncheck.AuditRecord{deny("a", false, "verified")}, GuardAttempts{RequestedDenies: 1, VerifiedNative: 1, ReplyNotWritten: 1, NoExecutionObserved: 1}},
		{"execution overrides verification", []lessoncheck.AuditRecord{deny("a", true, "verified"), post("a")}, GuardAttempts{RequestedDenies: 1, VerifiedNative: 1, ExecutedAfterDeny: 1}},
		{"earlier post is not later", []lessoncheck.AuditRecord{post("a"), deny("a", true, "verified")}, GuardAttempts{RequestedDenies: 1, VerifiedNative: 1, NoExecutionObserved: 1, Prevented: 1}},
		{"other call", []lessoncheck.AuditRecord{deny("a", true, "unverified"), post("b")}, GuardAttempts{RequestedDenies: 1, NoExecutionObserved: 1}},
		{"missing match joins nothing", []lessoncheck.AuditRecord{deny("", false, "unverified"), post("")}, GuardAttempts{RequestedDenies: 1, ReplyNotWritten: 1, NoExecutionObserved: 1}},
		{"duplicate completions", []lessoncheck.AuditRecord{deny("a", true, "unverified"), post("a"), post("a")}, GuardAttempts{RequestedDenies: 1, ExecutedAfterDeny: 1}},
		{"allowed", []lessoncheck.AuditRecord{{Kind: lessoncheck.AuditGuard, Guard: &lessoncheck.GuardDecision{Decision: "allow"}}}, GuardAttempts{Allowed: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Inputs{CheckAudit: checkPrefix(tc.records...)}
			got := mustBuild(t, in, "").Checks.GuardAttempts
			assert.Equal(t, tc.want, got)
			assert.Equal(t, got.RequestedDenies, got.ExecutedAfterDeny+got.NoExecutionObserved)
			assert.LessOrEqual(t, got.Prevented, got.VerifiedNative)
		})
	}
}

// TestCheckReadingSeparation compares every earlier report field, not just
// the totals. Unrelated check operations must not become delivery or edits.
func TestCheckReadingSeparation(t *testing.T) {
	cfg := testConfig("n")
	id, version := pinOf(cfg, 0)
	in := Inputs{
		Audit: auditLines(
			v2("e1", "s1", "m1", pin(id, version, reviews.PinInjected)),
			v2("e2", "s2", "m2", pin(id, version, reviews.PinInjected)),
			v2("e3", "s2", "m3", pin(id, version, reviews.PinSuppressedRepeat)),
		),
		Config: cfg, Now: fixedNow,
	}
	before := mustBuild(t, in, id[:12])
	require.Equal(t, 2, before.Pins[0].Delivery.Sessions)
	require.NotNil(t, before.Expanded)
	require.Len(t, before.Expanded.Sessions, 2)

	p := lessoncheck.AuditPin{PinIdentity: id, PinVersion: version, Outcome: lessoncheck.OutcomeViolation}
	in.CheckAudit = checkPrefix(
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditVerify, Pins: []lessoncheck.AuditPin{p}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditPostEdit, MatchSHA: digest64("unrelated"), Pins: []lessoncheck.AuditPin{p}},
		lessoncheck.AuditRecord{Kind: lessoncheck.AuditGuard, MatchSHA: digest64("unrelated-deny"), Pins: []lessoncheck.AuditPin{p}, Guard: &lessoncheck.GuardDecision{Decision: "deny"}},
	)
	after := mustBuild(t, in, id[:12])
	assert.Equal(t, 3, after.Checks.PinEvaluations)
	after.AsOf.CheckAudit = before.AsOf.CheckAudit
	after.Checks = before.Checks
	for i := range after.Pins {
		after.Pins[i].Checks = before.Pins[i].Checks
	}

	assert.Equal(t, before, after, "check units cannot change any delivery, occurrence, session, timing, observation, edit, or expansion field")
}
