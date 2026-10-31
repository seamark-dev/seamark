package lessonreport

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// fixedNow is the one time of every test Inputs. Build reads no clock.
var fixedNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

// digest64 returns a fixed 64-hex string for a label. Tests use it for
// session, match, and context digests.
func digest64(label string) string {
	return reviews.ComposeDigest("test", label)
}

// testConfig is a config with two pins: the second one has a note, so
// two versions of it can be told apart.
func testConfig(note string) *reviews.Config {
	return &reviews.Config{Pin: []reviews.PinRule{
		{Rule: "frozen-assets", Region: "generated", Note: "keep generated files"},
		{Rule: "keep-parser-strict", Region: "internal/parse", Note: note},
	}}
}

// pinOf returns the identity and the version of config entry i.
func pinOf(cfg *reviews.Config, i int) (identity, version string) {
	p := cfg.Pin[i]

	return reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions)), cfg.PinVersion(p)
}

// auditLines numbers records as the reader does: line n is the n-th line.
func auditLines(records ...reviews.Firing) reviews.AuditPrefix {
	p := reviews.AuditPrefix{Path: "/abs/.seamark/lessons-audit.jsonl", Bytes: int64(100 * len(records))}

	for i, r := range records {
		if r.SchemaVersion == 0 {
			p.Legacy++
		}

		p.Lines = append(p.Lines, reviews.AuditLine{Line: i + 1, Firing: r})
	}

	return p
}

// v2 builds a v2 hook record with the given pins.
func v2(event, session, match string, pins ...reviews.FiringPin) reviews.Firing {
	return reviews.Firing{
		SchemaVersion: reviews.AuditSchemaVersion, EventID: event, EventSeq: 1, EventRecords: 1,
		Delivery: reviews.DeliveryInjected, SessionSHA: digest64(session), MatchSHA: digest64(match),
		ContextSHA: digest64("ctx-" + session), Pins: pins,
	}
}

func pin(identity, version string, d reviews.PinDelivery) reviews.FiringPin {
	return reviews.FiringPin{Identity: identity, Version: version, Delivery: d}
}

// encode returns the JSON of a report, the form every surface prints.
func encode(t *testing.T, r Report) string {
	t.Helper()

	out, err := json.Marshal(r)
	require.NoError(t, err)

	return string(out)
}

// TestBuildIsDeterministic builds one reading many times from the same
// Inputs, and from Inputs whose maps arrive in another order. The JSON
// bytes stay equal.
func TestBuildIsDeterministic(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	idA, verA := pinOf(cfg, 0)
	idB, verB := pinOf(cfg, 1)

	audit := auditLines(
		v2("e1", "s1", "m1", pin(idA, verA, reviews.PinInjected), pin(idB, verB, reviews.PinSuppressedRepeat)),
		v2("e2", "s2", "m2", pin(idB, verB, reviews.PinInjected)),
		reviews.Firing{Fired: []reviews.FiredLesson{{Region: "generated", Symptom: "frozen-assets"}}, Delivery: reviews.DeliveryInjected, TS: "2026-10-01T00:00:00Z"},
	)

	inputs := func(seed int64) Inputs {
		keys := []string{"k1", "k2", "k3", "k4", "k5", "k6"}
		rand.New(rand.NewSource(seed)).Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

		links := map[string]int64{}

		for i, k := range keys {
			links[k+idA] = int64(i)
		}

		return Inputs{Audit: audit, Config: cfg, Links: links, Now: fixedNow}
	}

	first := encode(t, mustBuild(t, inputs(1), ""))

	for seed := int64(0); seed < 25; seed++ {
		assert.Equal(t, first, encode(t, mustBuild(t, inputs(seed), "")), "seed %d", seed)
	}

	// The expansion lists versions: the same bytes again.
	expanded := encode(t, mustBuild(t, inputs(1), idB[:12]))
	for seed := int64(0); seed < 10; seed++ {
		assert.Equal(t, expanded, encode(t, mustBuild(t, inputs(seed), idB[:12])), "expansion, seed %d", seed)
	}
}

func mustBuild(t *testing.T, in Inputs, ref string) Report {
	t.Helper()

	r, err := Build(in, ref)
	require.NoError(t, err)

	return r
}

// TestBuildCountsDeliveryPerVersion proves the per-version delivery
// reading. Records count once per pin version. An older version stays
// apart from the current one. v1 records count as legacy identity
// records, and a newer-schema line stays out.
func TestBuildCountsDeliveryPerVersion(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	idA, verA := pinOf(cfg, 0)
	idB, verB := pinOf(cfg, 1)

	// The pin B had an older wording before the current config.
	oldCfg := testConfig("An older wording.")
	_, oldVerB := pinOf(oldCfg, 1)
	require.NotEqual(t, verB, oldVerB)

	partial := v2("e1", "s1", "m1",
		pin(idA, verA, reviews.PinInjected),
		pin(idB, verB, reviews.PinSuppressedRepeat),
		pin(idB, verB, reviews.PinSuppressedRepeat), // a duplicate entry names the version twice
	)
	secondRecord := v2("e1", "s1", "m1")
	secondRecord.EventSeq = 2
	secondRecord.EventRecords = 2

	denied := v2("e5", "s5", "m-denied", pin(idB, verB, reviews.PinInjected))

	v1 := func(delivery reviews.DeliveryStatus, ts string, fired reviews.FiredLesson) reviews.Firing {
		return reviews.Firing{Delivery: delivery, TS: ts, Fired: []reviews.FiredLesson{fired}}
	}

	audit := auditLines(
		partial,
		secondRecord,
		v2("e2", "s2", "m2", pin(idB, verB, reviews.PinInjected)),
		v2("e3", "s2", "m3", pin(idB, verB, reviews.PinInjected)), // second injection, same session
		v2("e4", "s3", "m4", pin(idB, oldVerB, reviews.PinInjected)),
		denied,
		reviews.Firing{ // explore surface injection
			SchemaVersion: reviews.AuditSchemaVersion, Surface: reviews.ExploreSurface, EventID: "e6",
			Delivery: reviews.DeliveryInjected, SessionSHA: digest64("s6"),
			Pins: []reviews.FiringPin{pin(idB, verB, reviews.PinInjected)},
		},
		reviews.Firing{ // change_set returned the pin
			SchemaVersion: reviews.AuditSchemaVersion, Surface: "change_set", EventID: "e7",
			Delivery: reviews.DeliveryInjected, ScopeSHA: "scope", GuidanceSHA: "g",
			Pins: []reviews.FiringPin{pin(idB, verB, reviews.PinReturned), pin(idA, verA, reviews.PinHeldBack)},
		},
		reviews.Firing{ // emission failed for pin A
			SchemaVersion: reviews.AuditSchemaVersion, EventID: "e8", Delivery: reviews.DeliveryInjected,
			SessionSHA: digest64("s8"), Pins: []reviews.FiringPin{pin(idA, verA, reviews.PinEmitFailed)},
		},
		// v1 records: the hook wrote the rendered symptom, "rule — note".
		v1(reviews.DeliveryInjected, "2026-10-01T00:00:00Z", reviews.PinIdentity(cfg.Pin[1])),
		v1(reviews.DeliverySuppressedRepeat, "2026-10-02T00:00:00Z", reviews.PinIdentity(cfg.Pin[1])),
		// Without the note, or with an older one, the record names another rendering.
		v1(reviews.DeliveryInjected, "2026-10-03T00:00:00Z", reviews.PinIdentity(reviews.PinRule{Rule: "keep-parser-strict", Region: "internal/parse"})),
		v1(reviews.DeliveryInjected, "2026-10-04T00:00:00Z", reviews.PinIdentity(oldCfg.Pin[1])),
	)
	audit.NewerSchema = 3 // lines of a newer schema: counted by the reader, kept out of Lines

	checkAudit := lessoncheck.AuditPrefix{
		Generations: 1,
		Records: []lessoncheck.AuditRecord{{
			Kind: lessoncheck.AuditGuard, MatchSHA: digest64("m-denied"),
			Guard: &lessoncheck.GuardDecision{Decision: "deny"},
		}},
	}

	r := mustBuild(t, Inputs{Audit: audit, CheckAudit: checkAudit, Config: cfg, Now: fixedNow}, "")

	require.Len(t, r.Pins, 2, "one reading per config pin, file order")
	a, b := r.Pins[0], r.Pins[1]

	assert.Equal(t, 1, a.Entry)
	assert.Equal(t, 2, b.Entry)
	assert.Equal(t, cfg.ShortPinRef(reviews.NewPinKey("keep-parser-strict", "internal/parse", nil)), b.PinRef)
	assert.Equal(t, verB, b.PinVersion)
	assert.Equal(t, []string{"internal/parse"}, b.Scope)
	assert.Equal(t, "advisory", b.Mode)

	// Pin A: the event of two records counts once; held-back and
	// emit-failed are their own counts.
	assert.Equal(t, DeliveryCounts{
		Records: map[string]int{"hook": 2, "change_set": 1}, Injected: 1, HeldBack: 1, EmitFailed: 1,
		Sessions: 1,
	}, a.Delivery)

	// Pin B, current version: the older version's record (e4) is not here.
	// e5 is a denied call: it counts as a record and as injected, and it
	// opens no delivered session. Sessions: s2 (twice) and s6 (explore).
	assert.Equal(t, map[string]int{"hook": 4, "explore": 1, "change_set": 1}, b.Delivery.Records)
	assert.Equal(t, 4, b.Delivery.Injected)
	assert.Equal(t, 1, b.Delivery.SuppressedRepeat, "the duplicate pin entry counts the record once")
	assert.Equal(t, 1, b.Delivery.Returned)
	assert.Equal(t, 2, b.Delivery.Sessions)
	assert.Equal(t, 2, b.Delivery.LegacyIdentityRecords, "v1 records name the identity as rendered with the note; the note-less and older-note records stay out")
	assert.Equal(t, 1, r.Coverage.DeniedCallInjections)

	// The older version is never pooled into the current one. The report
	// reads no capture archive, so no historical reading holds it.
	assert.Empty(t, r.Historical)

	// Totals come from reviews.Summarize and the v2 counts.
	assert.Equal(t, 8, r.Totals.Events)
	assert.Equal(t, 1, r.Totals.DistinctPreparations)
	assert.Equal(t, 0, r.Totals.EvaluationRecords)
	assert.Equal(t, 4, r.AsOf.Audit.Legacy, "the fixture holds four v1 lines")
	assert.Equal(t, 3, r.AsOf.Audit.NewerSchema, "a newer-schema line is kept out of every count")
	assert.Equal(t, 13, r.AsOf.Audit.Records)

	// An expansion lists the configured versions of one identity; the
	// older version is not one of them.
	exp := mustBuild(t, Inputs{Audit: audit, CheckAudit: checkAudit, Config: cfg, Now: fixedNow}, b.PinRef).Expanded
	require.NotNil(t, exp)
	assert.Equal(t, b.PinRef, exp.PinRef)
	assert.Equal(t, []string{verB}, exp.Versions)
	assert.Empty(t, exp.Occurrences)

	// Only the sessions of delivered injections of the current version open
	// a window: s2 and s6. The report reads no capture archive, so no
	// session is captured. The denied call s5 opens no window, and the
	// older version is not in the expansion.
	require.Len(t, exp.Sessions, 2)

	for _, w := range exp.Sessions {
		assert.Equal(t, PartitionIncomplete, w.Partition)
		assert.Equal(t, []Code{CodeSessionNotCaptured}, w.Reasons)
		assert.Equal(t, TimingUnknown, w.Timing)
	}

	assert.Equal(t, []string{"lessons-audit.jsonl:3", "lessons-audit.jsonl:7"},
		[]string{exp.Sessions[0].StartRecord, exp.Sessions[1].StartRecord})
	assert.Equal(t, []string{verB, verB},
		[]string{exp.Sessions[0].PinVersion, exp.Sessions[1].PinVersion},
		"each window names the version whose delivery opened it")
	assert.NotContains(t, []string{exp.Sessions[0].SessionRef, exp.Sessions[1].SessionRef},
		shortRef(digest64("s5")))
}

// TestBuildStatesAreData proves that a missing or unreadable source is
// data in the report, never an error, and that the empty sections hold
// no null and no result-like default.
func TestBuildStatesAreData(t *testing.T) {
	t.Run("zero inputs", func(t *testing.T) {
		r, err := Build(Inputs{}, "")
		require.NoError(t, err)

		assert.Equal(t, Schema, r.Schema)
		assert.Equal(t, StateAbsent, r.AsOf.Audit.State)
		assert.Equal(t, StateAbsent, r.AsOf.CheckAudit.State)
		assert.Equal(t, StateAbsent, r.AsOf.Archive.State)
		assert.Equal(t, StateAbsent, r.AsOf.Config.State)
		assert.Equal(t, CaptureState{State: CaptureUnavailable, Reason: "no-archive", Message: "capture evidence unavailable"}, r.Capture)
		assert.Empty(t, r.AsOf.GeneratedAt, "no time given, no date printed")
		assert.Empty(t, r.Pins)
		assert.Nil(t, r.Expanded)

		out := encode(t, r)
		assert.NotContains(t, out, "null", "every list and map is non-nil")
		assert.Equal(t, unitTable(), r.Units)
	})

	t.Run("absent audit beside a config", func(t *testing.T) {
		cfg := testConfig("n")
		r, err := Build(Inputs{Config: cfg, Now: fixedNow}, "")
		require.NoError(t, err)

		assert.Equal(t, StateAbsent, r.AsOf.Audit.State)
		assert.Equal(t, StateAbsent, r.AsOf.Archive.State)
		assert.Equal(t, ConfigOK, r.AsOf.Config.State)
		assert.Equal(t, 2, r.AsOf.Config.Pins)
		assert.Equal(t, cfg.GuidanceDigest(), r.AsOf.Config.GuidanceSHA256)
		assert.Equal(t, "2026-10-26T12:00:00Z", r.AsOf.GeneratedAt)
		require.Len(t, r.Pins, 2, "the pins print, with delivery counts of zero")
		assert.Equal(t, 0, r.Pins[0].Delivery.Injected)
		assert.False(t, r.Pins[0].Delivery.Records == nil)
		assert.NotContains(t, encode(t, r), "null")
	})

	t.Run("unreadable and over-limit sources", func(t *testing.T) {
		cfg := testConfig("n")
		id, ver := pinOf(cfg, 0)
		lines := auditLines(v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected)))

		unreadable := Inputs{Config: cfg, AuditError: "firing log: permission denied\x1b[2J", CheckAuditError: "boom"}
		r := mustBuild(t, unreadable, "")
		assert.Equal(t, StateUnreadable, r.AsOf.Audit.State)
		assert.Equal(t, "firing log: permission denied[2J", r.AsOf.Audit.Error, "the error text is sanitized")
		assert.Equal(t, StateUnreadable, r.AsOf.CheckAudit.State)

		over := lines
		over.OverLimit = true
		r = mustBuild(t, Inputs{Audit: over, Config: cfg}, "")
		assert.Equal(t, StateOverLimit, r.AsOf.Audit.State)
		assert.Equal(t, 0, r.Pins[0].Delivery.Injected, "a source over its limit gives no count")
		assert.Empty(t, r.Totals.DeliveredRecords)

		r = mustBuild(t, Inputs{Audit: lines, Config: cfg}, "")
		assert.Equal(t, StateRead, r.AsOf.Audit.State)
		assert.Equal(t, 1, r.Pins[0].Delivery.Injected)
	})

	t.Run("the archive state is fixed", func(t *testing.T) {
		cfg := testConfig("n")
		id, ver := pinOf(cfg, 0)
		lines := auditLines(v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected)))

		for _, in := range []Inputs{{}, {Audit: lines, Config: cfg, Now: fixedNow}} {
			r := mustBuild(t, in, "")
			assert.Equal(t, ArchiveRead{State: StateAbsent, Error: ArchiveNotReadError}, r.AsOf.Archive)
			assert.Equal(t, CaptureState{State: CaptureUnavailable, Reason: "no-archive", Message: CaptureUnavailableMessage}, r.Capture)
		}
	})

	t.Run("the rule is sanitized", func(t *testing.T) {
		cfg := &reviews.Config{Pin: []reviews.PinRule{{Rule: "gone\x1b[31m", Region: "x"}}}
		r := mustBuild(t, Inputs{Config: cfg}, "")

		require.Len(t, r.Pins, 1)
		assert.Equal(t, "gone[31m", r.Pins[0].Rule)
	})

	t.Run("a pin-ref problem is the only error", func(t *testing.T) {
		_, err := Build(Inputs{Config: testConfig("n")}, "deadbeefdead")
		require.Error(t, err)
		assert.ErrorIs(t, err, reviews.ErrPinRef)
		assert.True(t, strings.HasPrefix(err.Error(), "pin reference deadbeefdead names no pin"), err.Error())
	})
}

// TestBuildLeavesUnderivedSectionsEmpty keeps the sections that Build does
// not derive yet empty: a default there would read like a real result. The
// observation is derived: a delivered session reads incomplete, because
// the report reads no capture archive.
func TestBuildLeavesUnderivedSectionsEmpty(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)
	r := mustBuild(t, Inputs{
		Audit:  auditLines(v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected))),
		Config: cfg, Now: fixedNow,
	}, "")

	p := r.Pins[0]
	assert.Equal(t, OccurrenceCounts{ByClass: map[PairClass]int{}, ByOrigin: map[Origin]int{}}, p.Occurrences)
	assert.Empty(t, p.Timing)
	assert.Empty(t, p.Facts)
	assert.Empty(t, p.Reasons)
	assert.Empty(t, p.Limitations)
	assert.Empty(t, p.NextSteps)
	assert.Equal(t, 0, p.Observation.Linked)
	assert.Equal(t, 0, p.Observation.NoLinkedCorrection)
	assert.Equal(t, 1, p.Observation.Incomplete, "the delivered session is not captured")
	assert.Equal(t, map[Code]int{CodeSessionNotCaptured: 1}, p.Observation.IncompleteByReason)
	assert.Equal(t, map[Timing]Partition{TimingUnknown: {Incomplete: 1}}, p.Observation.ByTiming)
	assert.Equal(t, Unlinked{Unit: "feedback items", ByReason: map[Code]int{}, NextSteps: []NextStep{}}, r.Unlinked)
	assert.Equal(t, StateAbsent, r.Checks.State, "this fixture has no check stream")
	assert.Equal(t, GuardAttempts{}, r.Checks.GuardAttempts)

	// The JSON holds none of the codes that read like a result.
	out := encode(t, r)
	for _, word := range []string{"not-emitted", "no-linked-correction", "at-action"} {
		assert.NotContains(t, out, fmt.Sprintf("%q", word))
	}
}
