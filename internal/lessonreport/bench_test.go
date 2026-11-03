package lessonreport

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file measures Build over synthetic audit logs. The data comes from
// one seed and a fixed config. It reads no clock, no real path, and no
// file, so two runs measure the same Inputs. `make report-bench` runs it.
// The report reads the audit logs, the config, and the capability report,
// so the fixture holds these sources and no other.

// benchSeed is the only seed of the fixture. A change of it changes the
// measured Inputs, so a recorded result names it.
const benchSeed = 20261103

// The fixed size of the fixture. The audit grows with the size, and the
// config does not, because a repository holds few pins for a long log.
const (
	benchPins = 20
	// benchOlderVersions is the count of older versions of pin A that the
	// log names and the config no longer holds. A pin edit leaves such
	// records behind.
	benchOlderVersions = 4
	// benchRecordsPerSession is the session length of the typical shape.
	benchRecordsPerSession = 50
	// benchFewSessions is the session count of the few-sessions shape.
	benchFewSessions = 5
)

// The root, the client, and the time of every synthetic record. The
// digests hash the root and the client as the writer does.
const (
	benchRoot   = "/work/repo"
	benchClient = integration.ClaudeID
	benchTime   = "2026-10-26T11:00:00Z"
)

// The audit values that the hook writer stores. The report reads only some
// of them, but the records keep the full shape of a real record.
const (
	benchEmitted   = "emitted"
	benchNotNeeded = "not-needed"
	benchReturned  = "returned"
	benchComplete  = "complete"
)

// benchSizes are the audit record counts of the benchmark.
var benchSizes = []int{1_000, 10_000, 100_000}

// benchShape names what a benchmark varies apart from the record count.
type benchShape struct {
	name string
	// sessions returns the session count for a record count.
	sessions func(records int) int
}

// benchShapes holds the typical shape and a shape that aims at a step
// above linear. In few-sessions each session grows with the log, so a
// step that walks the records of one session costs more.
var benchShapes = []benchShape{
	{name: "typical", sessions: typicalSessions},
	{name: "few-sessions", sessions: fewSessions},
}

func typicalSessions(records int) int {
	return records / benchRecordsPerSession
}

func fewSessions(int) int {
	return benchFewSessions
}

// treeCapability returns the capability report that this tree gives for the
// fixture client. The report comes from static evidence, so the fixture
// reads no file and runs no client.
func treeCapability() *lessoncheck.CapabilityReport {
	report := integration.LessonCapabilityOf(
		[]integration.Inspection{{ClientID: benchClient}}, &lessoncheck.Config{}, nil, integration.BinaryIdentity{})

	return &report
}

// benchPin is one pin version that a record can name.
type benchPin struct {
	identity, version string
}

// benchStats describes the Inputs of one benchmark, for the recorded shape.
type benchStats struct {
	auditBytes int
	sessions   int     // session digests that the log names
	childLane  float64 // share of the records from a child agent
	noLane     float64 // share of the records that write no lane
}

// benchEvent describes one event of the log. The zero value is an
// injected hook record at a root-lane edit.
type benchEvent struct {
	session string
	toolUse string
	agent   string
	turn    string

	// status is the delivery of the first record. Empty means injected.
	status     reviews.DeliveryStatus
	emission   string
	surface    string
	pins       []reviews.FiringPin
	generation uint64

	// second writes a second record with this delivery, as the writer does
	// for an event that sends one pin and suppresses another. The two
	// records share the event ID, and only the first holds the pins.
	second reviews.DeliveryStatus
	// legacy writes a version 1 record.
	legacy bool
}

// benchFixture grows one synthetic log.
type benchFixture struct {
	t       testing.TB
	rng     *rand.Rand
	cfg     *reviews.Config
	pins    []benchPin
	records []reviews.Firing
	check   lessoncheck.AuditPrefix
	events  int
}

// newBenchInputs returns the Inputs of one size and shape. The result
// depends only on the arguments and benchSeed.
func newBenchInputs(tb testing.TB, records int, shape benchShape) (Inputs, benchStats) {
	tb.Helper()

	b := &benchFixture{
		t:   tb,
		rng: rand.New(rand.NewPCG(benchSeed, 0)), //nolint:gosec // a fixed seed keeps the fixture deterministic.
	}
	b.addPins()

	sessions := shape.sessions(records)
	names := make([]string, sessions)

	for i := range names {
		names[i] = fmt.Sprintf("s%06d", i)
	}

	for len(b.records) < records {
		position := len(b.records)
		b.addEvent(names[position*sessions/records], position, records-position > 1)
	}

	in := Inputs{
		Audit: auditLines(b.records...), CheckAudit: b.check, Config: b.cfg,
		Capability: treeCapability(), Now: fixedNow,
	}
	in.Audit.Path = "synthetic/lessons-audit.jsonl"

	stats := b.stats()
	in.Audit.Bytes = int64(stats.auditBytes)

	return in, stats
}

// addPins configures the pins and names the older versions. The list holds
// the current versions first. An older version shares the identity of pin
// A, and no config entry holds it.
func (b *benchFixture) addPins() {
	b.cfg = &reviews.Config{}

	for i := range benchPins {
		rule, note := "keep-parser-strict", "Reject unknown fields."

		if i > 0 {
			rule, note = fmt.Sprintf("bench-rule-%02d", i), fmt.Sprintf("Bench note %02d.", i)
		}

		b.cfg.Pin = append(b.cfg.Pin, reviews.PinRule{Rule: rule, Region: "internal/parse", Note: note})
		identity, version := pinOf(b.cfg, i)
		b.pins = append(b.pins, benchPin{identity: identity, version: version})
	}

	for i := range benchOlderVersions {
		b.pins = append(b.pins, benchPin{identity: b.pins[0].identity, version: digest64(fmt.Sprintf("older-%d", i))})
	}
}

// addEvent appends one event of the log. A draw from 0 to 99 selects the
// kind, so the shares of the kinds are the widths of the cases. second
// allows an event of two records. The recording switch is on, so each
// writer of a receiver record stamps capture_opt_in. The hook writes an
// evaluation record and a reset record only under the switch.
func (b *benchFixture) addEvent(name string, position int, second bool) {
	draw := b.rng.IntN(100)
	spec := benchEvent{
		session: name, toolUse: fmt.Sprintf("toolu_%d", position), generation: 1,
		turn: fmt.Sprintf("turn-%d", position/5),
	}

	// One draw of four names a child agent. The same agent keeps a lane
	// across the session. A preparation record or a reset record writes no
	// lane, so the name has no effect in those cases. Of 100 draws, 21 give
	// a child lane, 63 give the root lane, and 16 give no lane.
	if draw%4 == 3 {
		spec.agent = "agent-" + name
	}

	switch {
	case draw < 1:
		spec.legacy = true
		b.event(spec)
	case draw < 2:
		b.reset(name, spec.agent)
	case draw < 12:
		b.prepare(surfaceChangeSet)
	case draw < 17:
		b.prepare(surfacePrepare)
	case draw < 22:
		spec.surface, spec.toolUse = surfaceExplore, ""
		spec.pins = b.drawPins(reviews.PinInjected, "")
		b.event(spec)
	case draw < 52:
		spec.status, spec.emission = reviews.DeliveryEvaluation, benchNotNeeded
		b.event(spec)
	default:
		b.addHook(spec, second)
	}
}

// addHook appends a hook event with pins. Of these events, four of ten
// inject, four of ten suppress a repeat, and two of ten hold pins back.
// One of a hundred has a guard deny, and one of twenty has two records.
func (b *benchFixture) addHook(spec benchEvent, second bool) {
	draw := b.rng.IntN(100)

	switch {
	case draw < 40:
		spec.pins = b.drawPins(reviews.PinInjected, "")
	case draw < 80:
		spec.status = reviews.DeliverySuppressedRepeat
		spec.pins = b.drawPins(reviews.PinSuppressedRepeat, "")
	default:
		spec.status, spec.emission = reviews.DeliveryEvaluation, benchNotNeeded
		spec.pins = b.drawPins(reviews.PinHeldBack, reviews.CauseOverPinCap)
	}

	if second && draw%20 == 0 {
		spec.second = reviews.DeliverySuppressedRepeat
	}

	b.event(spec)

	if draw == 99 {
		b.deny(spec.session, spec.toolUse)
	}
}

// drawPins returns one to three distinct pins. The smaller of two draws
// favors the first pins, because few pins fire most often in a repository.
func (b *benchFixture) drawPins(d reviews.PinDelivery, cause string) []reviews.FiringPin {
	chosen := map[int]bool{}
	out := []reviews.FiringPin{}

	for range 1 + b.rng.IntN(3) {
		i := min(b.rng.IntN(len(b.pins)), b.rng.IntN(len(b.pins)))
		if chosen[i] {
			continue
		}

		chosen[i] = true
		out = append(out, reviews.FiringPin{
			Identity: b.pins[i].identity, Version: b.pins[i].version, Delivery: d, Cause: cause,
		})
	}

	return out
}

// event appends the records of one event. The digests follow the writer:
// the match digest hashes the root, the native session, and the tool use,
// and the context digest hashes the receiver of the client.
func (b *benchFixture) event(spec benchEvent) {
	native := "native-" + spec.session
	status := cmp.Or(spec.status, reviews.DeliveryInjected)

	rec := reviews.Firing{
		Surface: spec.surface, Delivery: status, Client: benchClient,
		SessionSHA: reviews.SessionDigest(benchRoot, native),
	}

	if spec.toolUse != "" {
		rec.MatchSHA = reviews.MatchDigest(benchRoot, native, spec.toolUse)
	}

	if rc := integration.ReceivingContextOf(benchClient, native, spec.agent); rc != nil {
		rec.ContextSHA = reviews.ContextDigest(benchRoot, benchClient, rc.ID)
		rec.Lane = rc.Lane

		resettable := rc.Resettable
		rec.ContextResettable = &resettable
	}

	if spec.legacy {
		rec.Fired = []reviews.FiredLesson{{Region: "internal/parse", Symptom: "keep-parser-strict"}}
		b.records = append(b.records, rec)

		return
	}

	b.events++
	complete := true

	// The hook writer emits text only for an injected event. It stores
	// not-needed for every other delivery, a suppressed repeat included.
	emission := benchNotNeeded

	if status == reviews.DeliveryInjected {
		emission = benchEmitted
	}

	rec.SchemaVersion = reviews.AuditSchemaVersion
	rec.EventID = fmt.Sprintf("event-%d", b.events)
	rec.EventSeq, rec.EventRecords = 1, 1
	rec.TS = benchTime
	rec.CaptureOptIn = true
	rec.TurnSHA = reviews.TurnDigest(benchRoot, benchClient, native, spec.turn)
	rec.Pins = spec.pins
	rec.Generation = spec.generation
	rec.File = "internal/parse/a.go"
	rec.Emission = cmp.Or(spec.emission, emission)
	rec.Evaluation = benchComplete
	rec.PathsComplete = &complete
	rec.GuidanceSHA = b.cfg.GuidanceDigest()
	rec.GuidanceStatus = "ok"
	rec.Fired = []reviews.FiredLesson{}

	if spec.second == "" {
		b.records = append(b.records, rec)

		return
	}

	rec.EventRecords = 2

	next := rec
	next.EventSeq, next.Delivery, next.Pins = 2, spec.second, nil
	b.records = append(b.records, rec, next)
}

// reset appends a context reset record of a session's lane.
func (b *benchFixture) reset(session, agent string) {
	native := "native-" + session
	rc := integration.ReceivingContextOf(benchClient, native, agent)
	require.NotNil(b.t, rc)

	b.events++
	b.records = append(b.records, reviews.Firing{
		SchemaVersion: reviews.AuditSchemaVersion, EventID: fmt.Sprintf("event-%d", b.events), EventSeq: 1, EventRecords: 1,
		Delivery: reviews.DeliveryReset, Client: benchClient, SessionSHA: reviews.SessionDigest(benchRoot, native),
		ContextSHA: reviews.ContextDigest(benchRoot, benchClient, rc.ID), CaptureOptIn: true, Fired: []reviews.FiredLesson{},
	})
}

// prepare appends a preparation record that returned drawn pins. A
// preparation names no receiver, so it has no session and no lane.
func (b *benchFixture) prepare(surface string) {
	b.events++
	b.records = append(b.records, reviews.Firing{
		SchemaVersion: reviews.AuditSchemaVersion, Surface: surface, EventID: fmt.Sprintf("event-%d", b.events),
		EventSeq: 1, EventRecords: 1, Emission: benchReturned, Evaluation: benchComplete,
		GuidanceSHA: b.cfg.GuidanceDigest(), Pins: b.drawPins(reviews.PinReturned, ""), Fired: []reviews.FiredLesson{},
	})
}

// deny records that the guard denied the call of a tool use.
func (b *benchFixture) deny(session, toolUse string) {
	b.check.Generations = 1
	b.check.Records = append(b.check.Records, lessoncheck.AuditRecord{
		Kind: lessoncheck.AuditGuard, MatchSHA: reviews.MatchDigest(benchRoot, "native-"+session, toolUse),
		Guard: &lessoncheck.GuardDecision{Decision: lessoncheck.GuardDeny},
	})
}

// stats measures the shape of the grown log. The audit bytes are the size
// of the records as the writer encodes them, one JSON line for each.
func (b *benchFixture) stats() benchStats {
	out := benchStats{}
	sessions := map[string]bool{}
	childLane, noLane := 0, 0

	for i := range b.records {
		line, err := json.Marshal(b.records[i])
		require.NoError(b.t, err)

		out.auditBytes += len(line) + 1

		if b.records[i].SessionSHA != "" {
			sessions[b.records[i].SessionSHA] = true
		}

		switch b.records[i].Lane {
		case integration.LaneChild:
			childLane++
		case "":
			noLane++
		}
	}

	out.sessions = len(sessions)
	out.childLane = float64(childLane) / float64(len(b.records))
	out.noLane = float64(noLane) / float64(len(b.records))

	return out
}

// BenchmarkBuild times the overview of Build. The fixture stays outside the
// timed loop, and one Inputs value serves every iteration because Build
// changes no input (TestBenchFixture proves it). The extra metrics state the
// input of the row.
func BenchmarkBuild(b *testing.B) {
	for _, records := range benchSizes {
		for _, shape := range benchShapes {
			b.Run(fmt.Sprintf("records=%d/shape=%s", records, shape.name), func(b *testing.B) {
				in, stats := newBenchInputs(b, records, shape)

				for b.Loop() {
					_, err := Build(in, "")
					require.NoError(b, err)
				}

				b.ReportMetric(float64(stats.auditBytes), "audit-bytes")
				b.ReportMetric(float64(stats.sessions), "sessions")
				b.ReportMetric(stats.childLane, "child-lane/record")
				b.ReportMetric(stats.noLane, "no-lane/record")
			})
		}
	}
}

// TestBenchFixture proves what the benchmark relies on. The same arguments
// give the same Inputs, Build changes no input, and the shape stays the one
// that this file states.
func TestBenchFixture(t *testing.T) {
	const records = 1_000

	for _, shape := range benchShapes {
		t.Run(shape.name, func(t *testing.T) {
			in, stats := newBenchInputs(t, records, shape)
			again, sameStats := newBenchInputs(t, records, shape)

			require.Equal(t, again, in, "one seed gives one fixture")
			require.Equal(t, sameStats, stats)

			first := mustBuild(t, in, "")

			require.Equal(t, again, in, "Build changes no input")
			require.Equal(t, mustBuild(t, again, ""), first, "Build reads no clock and no map order")

			assert.Len(t, in.Audit.Lines, records)
			assert.Equal(t, StateRead, first.AsOf.Audit.State)
			assert.Len(t, first.Pins, benchPins)
			assert.Empty(t, first.Historical, "the report reads the current versions only")
			assert.Greater(t, stats.auditBytes, records*100, "the size comes from the encoded records")
		})
	}

	t.Run("the typical shape reads delivered sessions", func(t *testing.T) {
		in, stats := newBenchInputs(t, records, benchShapes[0])
		report := mustBuild(t, in, "")

		assert.Equal(t, records/benchRecordsPerSession, stats.sessions)

		incomplete := 0
		for _, p := range report.Pins {
			incomplete += p.Observation.Incomplete
		}

		assert.Positive(t, incomplete, "an injection must open a delivered session, or the windows stay idle")
		assert.Positive(t, report.Totals.SuppressedHookRecords)
		assert.Positive(t, report.Totals.EvaluationRecords)
		assert.Positive(t, report.Totals.ResetRecords)
		assert.Positive(t, report.Totals.DistinctPreparations)
	})

	t.Run("the few-sessions shape keeps its session count", func(t *testing.T) {
		_, stats := newBenchInputs(t, records, benchShapes[1])

		assert.Equal(t, benchFewSessions, stats.sessions)
	})

	t.Run("the log names older versions that no pin reads", func(t *testing.T) {
		in, _ := newBenchInputs(t, records, benchShapes[0])
		identity, current := pinOf(in.Config, 0)
		older := map[string]bool{}

		for _, line := range in.Audit.Lines {
			for _, p := range line.Firing.Pins {
				if p.Identity == identity && p.Version != current {
					older[p.Version] = true
				}
			}
		}

		assert.Len(t, older, benchOlderVersions)
	})

	t.Run("every record holds the switch stamp of its writer", func(t *testing.T) {
		for _, shape := range benchShapes {
			in, _ := newBenchInputs(t, records, shape)

			for _, line := range in.Audit.Lines {
				f := line.Firing
				stamped := f.SchemaVersion > 0 && f.Surface != surfaceChangeSet && f.Surface != surfacePrepare

				require.Equal(t, stamped, f.CaptureOptIn, "%s line %d: the recording switch covers every record", shape.name, line.Line)
			}
		}
	})

	// The writer stores one emission for all records of an event, so only
	// the first record of each event shows what the event emitted.
	t.Run("only an injected event emits text", func(t *testing.T) {
		in, _ := newBenchInputs(t, records, benchShapes[0])
		suppressed := 0

		for _, line := range in.Audit.Lines {
			f := line.Firing

			if f.EventSeq != 1 {
				continue
			}

			switch f.Delivery {
			case reviews.DeliveryInjected:
				require.Equal(t, benchEmitted, f.Emission, "line %d", line.Line)
			case reviews.DeliverySuppressedRepeat:
				suppressed++

				require.Equal(t, benchNotNeeded, f.Emission, "line %d: a suppressed repeat emits no text", line.Line)
			case reviews.DeliveryEvaluation:
				require.Equal(t, benchNotNeeded, f.Emission, "line %d", line.Line)
			}
		}

		assert.Positive(t, suppressed, "the log holds suppressed repeats")
	})

	t.Run("preparation and reset records write no lane", func(t *testing.T) {
		_, stats := newBenchInputs(t, records, benchShapes[0])

		assert.InDelta(t, 0.21, stats.childLane, 0.02, "a preparation draw for a child agent gives no child lane")
		assert.InDelta(t, 0.16, stats.noLane, 0.02)
		assert.InDelta(t, 0.63, 1-stats.childLane-stats.noLane, 0.02, "the root lane holds the other records")
	})
}
