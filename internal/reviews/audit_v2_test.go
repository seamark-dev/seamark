package reviews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// The tests of audit record v2 (plan steps P3.1 and P3.3; contract L3).
// Milestone P3 adds keys after the legacy keys of every firing record.
// Without the v2 keys, a record must equal the record of the P2 head
// byte for byte, and the legacy readers must keep their counts.

// The raw identities of the legacy audit cases. Only their digests
// reach the log. Milestone P3.3 gives the same values to its v2 writer,
// so the two halves of TestAuditV2KeepsLegacyPrefix hash equal inputs.
const (
	legacySessionID      = "session-1"
	legacyReceiverID     = "receiver-1"
	legacyCodexSessionID = "session-2"
	legacyMatchID        = "tool-use-1"
	legacySuppressedID   = "tool-use-2"
	legacyCodexMatchID   = "call-1"
)

// The lessons of the legacy audit cases. The fold of Summarize ranks
// them without a tie, because sort.Slice does not keep the input order
// of equal rows.
var (
	legacyLessonStrict = model.Lesson{Region: "internal/parse",
		Symptom: "keep-parser-strict — Reject unknown fields in the parser."}
	legacyLessonRuff   = model.Lesson{Region: "internal/parse", Symptom: "RUF001"}
	legacyLessonClient = model.Lesson{Region: "api", Symptom: "Keep the generated client synchronized."}
	legacyLessonFrozen = model.Lesson{Region: "generated",
		Symptom: "frozen-assets — Changes to these files require a maintainer-managed update."}
)

// legacyAuditCase is one v1 record of the firing log. write appends the
// record through a v1 entry point. golden is the record line with ts
// cleared. Its placeholders name the identity digests over the log root,
// because the root of a test is a new temporary directory.
type legacyAuditCase struct {
	name   string
	write  func(root string) error
	golden string
}

// legacyAuditCases are the v1 records in log order: an edit-hook
// injection of one file, a suppressed repeat, an injection of several
// files, a change_set record, and a check record.
func legacyAuditCases() []legacyAuditCase {
	return []legacyAuditCase{
		{
			name: "single file",
			write: func(root string) error {
				return RecordHookDeliveryFiles(root, []string{"internal/parse/strict.go"}, "Edit",
					[]model.Lesson{legacyLessonStrict, legacyLessonRuff}, HookDelivery{
						Status: DeliveryInjected, SessionID: legacySessionID, MatchID: legacyMatchID,
						Generation: 1, ContextBytes: 312, ClientID: "claude",
						Mechanism: "pre-tool-use-context", ReceiverID: legacyReceiverID,
					})
			},
			golden: `{"ts":"","file":"internal/parse/strict.go","tool":"Edit","fired":[` +
				`{"region":"internal/parse","symptom":"keep-parser-strict — Reject unknown fields in the parser."},` +
				`{"region":"internal/parse","symptom":"RUF001"}],"delivery":"injected",` +
				`"session_sha256":"{session}","match_sha256":"{match}","context_generation":1,` +
				`"context_bytes":312,"client":"claude","mechanism":"pre-tool-use-context",` +
				`"context_sha256":"{context}"}`,
		},
		{
			name: "suppressed repeat",
			write: func(root string) error {
				return RecordHookDeliveryFiles(root, []string{"internal/parse/strict.go"}, "Edit",
					[]model.Lesson{legacyLessonRuff}, HookDelivery{
						Status: DeliverySuppressedRepeat, SessionID: legacySessionID,
						MatchID: legacySuppressedID, Generation: 1, ClientID: "claude",
						Mechanism: "pre-tool-use-context", ReceiverID: legacyReceiverID,
					})
			},
			golden: `{"ts":"","file":"internal/parse/strict.go","tool":"Edit","fired":[` +
				`{"region":"internal/parse","symptom":"RUF001"}],"delivery":"suppressed-repeat",` +
				`"session_sha256":"{session}","match_sha256":"{suppressed-match}","context_generation":1,` +
				`"client":"claude","mechanism":"pre-tool-use-context","context_sha256":"{context}"}`,
		},
		{
			// Codex names no receiver at the P2 head, so the record has
			// no context digest and no context generation.
			name: "several files",
			write: func(root string) error {
				return RecordHookDeliveryFiles(root, []string{"api/a.go", "api/b.go"}, "apply_patch",
					[]model.Lesson{legacyLessonClient}, HookDelivery{
						Status: DeliveryInjected, SessionID: legacyCodexSessionID, MatchID: legacyCodexMatchID,
						ContextBytes: 120, ClientID: "codex", Mechanism: "pre-tool-use-context",
					})
			},
			golden: `{"ts":"","files":["api/a.go","api/b.go"],"tool":"apply_patch","fired":[` +
				`{"region":"api","symptom":"Keep the generated client synchronized."}],"delivery":"injected",` +
				`"session_sha256":"{codex-session}","match_sha256":"{codex-match}","context_bytes":120,` +
				`"client":"codex","mechanism":"pre-tool-use-context"}`,
		},
		{
			name: "change_set",
			write: func(root string) error {
				return RecordFiringSurface(root, "change_set",
					[]string{"api/a.go", "generated/a.txt", "internal/parse/strict.go"}, "",
					[]model.Lesson{legacyLessonClient, legacyLessonFrozen, legacyLessonStrict})
			},
			golden: `{"ts":"","surface":"change_set","files":["api/a.go","generated/a.txt","internal/parse/strict.go"],` +
				`"fired":[{"region":"api","symptom":"Keep the generated client synchronized."},` +
				`{"region":"generated","symptom":"frozen-assets — Changes to these files require a maintainer-managed update."},` +
				`{"region":"internal/parse","symptom":"keep-parser-strict — Reject unknown fields in the parser."}]}`,
		},
		{
			name: "check",
			write: func(root string) error {
				return RecordFiringSurface(root, "check", []string{"internal/parse/strict.go"}, "",
					[]model.Lesson{legacyLessonStrict})
			},
			golden: `{"ts":"","surface":"check","file":"internal/parse/strict.go","fired":[` +
				`{"region":"internal/parse","symptom":"keep-parser-strict — Reject unknown fields in the parser."}]}`,
		},
	}
}

// legacyAuditGolden fills the digest placeholders of golden with the
// identity digests over root. The exported K2 functions compute them,
// so the test uses the derivation of the firing log and no copy of it.
func legacyAuditGolden(root, golden string) string {
	return strings.NewReplacer(
		"{session}", SessionDigest(root, legacySessionID),
		"{match}", MatchDigest(root, legacySessionID, legacyMatchID),
		"{suppressed-match}", MatchDigest(root, legacySessionID, legacySuppressedID),
		"{context}", ContextDigest(root, "claude", legacyReceiverID),
		"{codex-session}", SessionDigest(root, legacyCodexSessionID),
		"{codex-match}", MatchDigest(root, legacyCodexSessionID, legacyCodexMatchID),
	).Replace(golden)
}

// auditTSPrefix matches the ts key, which every writer puts first, with
// the second-precision UTC time that newFiring writes.
var auditTSPrefix = regexp.MustCompile(`^\{"ts":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z",`)

// readAuditLinesWithoutTS returns the lines of the firing log of root
// with the ts value cleared. It requires the ts format first, so a
// changed time format fails here and not as a golden mismatch.
func readAuditLinesWithoutTS(t *testing.T, root string) []string {
	t.Helper()

	raw, err := os.ReadFile(AuditLogPath(root))
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "\n"), "every record ends with a newline")

	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	for i, line := range lines {
		require.Regexp(t, auditTSPrefix, line, "line %d", i+1)
		lines[i] = auditTSPrefix.ReplaceAllString(line, `{"ts":"",`)
	}

	return lines
}

// legacyAuditSurfaced is the surfaced lesson list of the Summarize
// freeze. The pinned lesson is never a pruning candidate, and the docs
// lesson never fired.
func legacyAuditSurfaced() []model.Lesson {
	pinned := legacyLessonStrict
	pinned.Reviewer = "pinned"

	return []model.Lesson{pinned, legacyLessonRuff, {Region: "docs", Symptom: "Keep the docs index sorted."}}
}

// legacyAuditSummary is the Summarize result of the legacy audit cases
// with ts cleared, so every LastTS is empty. The suppressed repeat adds
// a match but no delivery, no file, and no surface count.
func legacyAuditSummary() Summary {
	return Summary{
		Total:     4,
		BySurface: map[string]int{"hook": 2, "change_set": 1, "check": 1},
		Files:     4,
		Ranked: []Fired{
			{Region: legacyLessonStrict.Region, Symptom: legacyLessonStrict.Symptom, Count: 3, Matches: 3},
			{Region: legacyLessonClient.Region, Symptom: legacyLessonClient.Symptom, Count: 2, Matches: 2},
			{Region: legacyLessonRuff.Region, Symptom: legacyLessonRuff.Symptom, Count: 1, Matches: 2},
			{Region: legacyLessonFrozen.Region, Symptom: legacyLessonFrozen.Symptom, Count: 1, Matches: 1},
		},
		NeverFired:              []model.Lesson{{Region: "docs", Symptom: "Keep the docs index sorted."}},
		InstrumentedHookFirings: 2,
		RepeatedHookFirings:     0,
		SuppressedHookFirings:   1,
		HookContextBytes:        432,
		HookByClient: []HookAttribution{
			{Client: "claude", Mechanism: "pre-tool-use-context", Injected: 1, Suppressed: 1, ContextBytes: 312},
			{Client: "codex", Mechanism: "pre-tool-use-context", Injected: 1, ContextBytes: 120},
		},
	}
}

// TestAuditV2KeepsLegacyPrefix freezes the v1 lines of
// RecordHookDeliveryFiles and RecordFiringSurface and their Summarize
// result as the P2 head e263ece writes them (plan step P3.1; contract
// L3, Compatibility). The v2 half (plan step P3.3) writes each case
// through RecordEvent: a v2 line without its v2 keys equals the golden
// of its case. No half edits the goldens, the raw identities, or the
// expected summary.
func TestAuditV2KeepsLegacyPrefix(t *testing.T) {
	t.Run("v1 writers keep the legacy lines and their summary", func(t *testing.T) {
		root := t.TempDir()
		cases := legacyAuditCases()

		for _, c := range cases {
			require.NoError(t, c.write(root), c.name)
		}

		lines := readAuditLinesWithoutTS(t, root)
		require.Len(t, lines, len(cases))

		for i, c := range cases {
			assert.Equal(t, legacyAuditGolden(root, c.golden), lines[i], c.name)
		}

		firings, err := ReadFirings(root)
		require.NoError(t, err)

		for i := range firings {
			firings[i].TS = ""
		}

		assert.Equal(t, legacyAuditSummary(), Summarize(firings, legacyAuditSurfaced()))
	})

	t.Run("v2 writer keeps the legacy prefix of every line", func(t *testing.T) {
		root := t.TempDir()
		cases := legacyAuditCases()
		events := legacyAuditEvents(root)
		require.Len(t, events, len(cases))

		for i, ev := range events {
			written, err := RecordEvent(ev)
			require.NoError(t, err, cases[i].name)
			require.Equal(t, 1, written, cases[i].name)
		}

		lines := readAuditLinesWithoutTS(t, root)
		require.Len(t, lines, len(cases))

		for i, c := range cases {
			legacy, tail := splitV2Line(t, lines[i])

			assert.Equal(t, legacyAuditGolden(root, c.golden), legacy, c.name)
			assert.True(t, strings.HasPrefix(tail, `"schema_version":2,"event_id":"`), "%s: %s", c.name, tail)
		}
	})
}

// The raw turn identities of the v2 legacy events. Only their digests
// reach the log.
const (
	legacyTurnID      = "prompt-1"
	legacyCodexTurnID = "turn-1"
)

// legacyGuidanceSHA stands for the guidance digest of the evaluated
// config. The writer copies the value and computes nothing from it.
var legacyGuidanceSHA = strings.Repeat("9", 64)

// v2Pin builds the FiringPin of a pin entry with its K2 digests.
func v2Pin(rule, region string, delivery PinDelivery, cause string) FiringPin {
	cfg := &Config{}

	return FiringPin{
		Identity: PinIdentityDigest(NewPinKey(rule, region, nil)),
		Version:  cfg.PinVersion(PinRule{Rule: rule, Region: region}),
		Delivery: delivery,
		Cause:    cause,
	}
}

// legacyAuditEvents are the legacy audit cases as v2 events, in the same
// order. Each event carries the raw identities, files, tool, lessons,
// and context bytes of its case, plus v2 values. root is both the log
// root and the identity root, as legacyAuditGolden expects.
func legacyAuditEvents(root string) []AuditEvent {
	resettable := true
	strictPin := v2Pin("keep-parser-strict", "internal/parse", PinInjected, "")

	return []AuditEvent{
		{
			Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"},
			ClientID: "claude", Mechanism: "pre-tool-use-context",
			SessionID: legacySessionID, MatchID: legacyMatchID, Turn: legacyTurnID,
			ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable,
			Records: []EventRecord{{
				Status: DeliveryInjected, Lessons: []model.Lesson{legacyLessonStrict, legacyLessonRuff},
				ContextBytes: 312,
			}},
			Pins:     []FiringPin{strictPin},
			Emission: "emitted", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", CaptureOptIn: true, Generation: 1,
		},
		{
			Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"},
			ClientID: "claude", Mechanism: "pre-tool-use-context",
			SessionID: legacySessionID, MatchID: legacySuppressedID, Turn: legacyTurnID,
			ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable,
			Records:  []EventRecord{{Status: DeliverySuppressedRepeat, Lessons: []model.Lesson{legacyLessonRuff}}},
			Emission: "not-needed", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", Generation: 1,
		},
		{
			// The legacy case names no receiver, so the event has no
			// receiver either and its lane is unknown.
			Root: root, Tool: "apply_patch", Files: []string{"api/a.go", "api/b.go"},
			ClientID: "codex", Mechanism: "pre-tool-use-context",
			SessionID: legacyCodexSessionID, MatchID: legacyCodexMatchID, Turn: legacyCodexTurnID,
			Lane: "unknown",
			Records: []EventRecord{{
				Status: DeliveryInjected, Lessons: []model.Lesson{legacyLessonClient}, ContextBytes: 120,
			}},
			Emission: "emitted", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok",
		},
		{
			Root: root, Surface: "change_set",
			Files: []string{"api/a.go", "generated/a.txt", "internal/parse/strict.go"},
			Records: []EventRecord{{
				Lessons: []model.Lesson{legacyLessonClient, legacyLessonFrozen, legacyLessonStrict},
			}},
			Pins: []FiringPin{
				v2Pin("frozen-assets", "generated", PinReturned, ""),
				v2Pin("keep-parser-strict", "internal/parse", PinReturned, ""),
			},
			Emission: "returned", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", ScopeSHA: strings.Repeat("5", 64),
		},
		{
			Root: root, Surface: "check", Files: []string{"internal/parse/strict.go"},
			Records:  []EventRecord{{Lessons: []model.Lesson{legacyLessonStrict}}},
			Pins:     []FiringPin{v2Pin("keep-parser-strict", "internal/parse", PinReturned, "")},
			Emission: "returned", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok",
		},
	}
}

// splitV2Line cuts a v2 line before its first v2 key. The legacy part
// gets its closing brace back, so it compares with a v1 golden. The
// cut also proves the order: a v2 key before schema_version stays in
// the legacy part, and a legacy key after it leaves that part.
func splitV2Line(t *testing.T, line string) (legacy, tail string) {
	t.Helper()

	const marker = `,"schema_version":`

	require.Equal(t, 1, strings.Count(line, marker), line)
	require.True(t, strings.HasSuffix(line, "}"), line)

	i := strings.Index(line, marker)

	return line[:i] + "}", line[i+1 : len(line)-1]
}

// TestTurnDigestVectors asserts the L3 test vector of TurnDigest and its
// empty and separation rules (contract L3; registry §2.2 label "turn").
func TestTurnDigestVectors(t *testing.T) {
	assert.Equal(t, "19a70cf81f75c33d61cb42144e45d494beb846df675e39ccc95035bac98aaaef",
		TurnDigest("/workspace/repo", "claude", "s-1", "p-1"))

	// A partial identity must not join two turns.
	assert.Empty(t, TurnDigest("/workspace/repo", "claude", "", "p-1"))
	assert.Empty(t, TurnDigest("/workspace/repo", "claude", "s-1", ""))

	vector := TurnDigest("/workspace/repo", "claude", "s-1", "p-1")

	// The root and the client are parts, so another repository or client
	// gives another digest.
	assert.NotEqual(t, vector, TurnDigest("/workspace/other", "claude", "s-1", "p-1"))
	assert.NotEqual(t, vector, TurnDigest("/workspace/repo", "codex", "s-1", "p-1"))

	// The length prefix keeps two splits of one string apart.
	assert.NotEqual(t, vector, TurnDigest("/workspace/repo", "claude", "s-1p", "-1"))

	// The label keeps the digest apart from the context digest of the
	// same strings.
	assert.NotEqual(t, ContextDigest("/workspace/repo", "claude", "s-1"),
		TurnDigest("/workspace/repo", "claude", "s-1", "s-1"))
}

// v2EventTime is the fixed time of the v2 events of the tests.
var v2EventTime = time.Date(2026, 10, 21, 10, 20, 11, 0, time.UTC)

// v2EventID is a fixed event ID of the tests.
const v2EventID = "53cdb75d6777b20e3d59e77cecd39b31"

// readAuditLines returns the lines of the firing log of root.
func readAuditLines(t *testing.T, root string) []string {
	t.Helper()

	raw, err := os.ReadFile(AuditLogPath(root))
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(raw), "\n"), "every record ends with a newline")

	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// twoRecordEvent is an edit event with an injected and a suppressed
// record, and pins of three delivery values in entry order.
func twoRecordEvent(root string) AuditEvent {
	resettable := true

	return AuditEvent{
		Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"},
		ClientID: "claude", Mechanism: "pre-tool-use-context",
		SessionID: legacySessionID, MatchID: legacyMatchID, Turn: legacyTurnID,
		ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable,
		Records: []EventRecord{
			{Status: DeliveryInjected, Lessons: []model.Lesson{legacyLessonStrict}, ContextBytes: 312},
			{Status: DeliverySuppressedRepeat, Lessons: []model.Lesson{legacyLessonRuff}},
		},
		Pins: []FiringPin{
			v2Pin("generated-client", "api", PinHeldBack, CauseOverPinCap),
			v2Pin("keep-parser-strict", "internal/parse", PinInjected, ""),
			v2Pin("frozen-assets", "generated", PinSuppressedRepeat, ""),
		},
		Emission: "emitted", Evaluation: "complete", PathsComplete: true,
		GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", CaptureOptIn: true, Generation: 1,
		EventID: v2EventID, Now: v2EventTime,
	}
}

// TestRecordEventWritesOneEventInOneAppend asserts the event identity
// and the write rules of RecordEvent (contract L3, Write rules): one
// event ID and ts per event, the pins on the first record in delivery
// order, the pin cap, and one append per event.
func TestRecordEventWritesOneEventInOneAppend(t *testing.T) {
	t.Run("an edit event writes both records with one identity", func(t *testing.T) {
		root := t.TempDir()

		written, err := RecordEvent(twoRecordEvent(root))
		require.NoError(t, err)
		assert.Equal(t, 2, written)

		pin := func(rule, region string, delivery PinDelivery, cause string) string {
			p := v2Pin(rule, region, delivery, cause)
			raw, err := json.Marshal(p)
			require.NoError(t, err)

			return string(raw)
		}

		identity := `"session_sha256":"{session}","match_sha256":"{match}","context_generation":1,`
		v2Keys := `"schema_version":2,"event_id":"` + v2EventID + `",`
		shared := `"emission":"emitted","evaluation":"complete","paths_complete":true,` +
			`"guidance_sha256":"` + legacyGuidanceSHA + `","guidance_status":"ok",` +
			`"turn_sha256":"{turn}","lane":"root","context_resettable":true,"capture_opt_in":true`

		want := []string{
			`{"ts":"2026-10-21T10:20:11Z","file":"internal/parse/strict.go","tool":"Edit","fired":[` +
				`{"region":"internal/parse","symptom":"keep-parser-strict — Reject unknown fields in the parser."}],` +
				`"delivery":"injected",` + identity + `"context_bytes":312,"client":"claude",` +
				`"mechanism":"pre-tool-use-context","context_sha256":"{context}",` +
				v2Keys + `"event_seq":1,"event_records":2,` + shared + `,"pins":[` +
				pin("keep-parser-strict", "internal/parse", PinInjected, "") + `,` +
				pin("frozen-assets", "generated", PinSuppressedRepeat, "") + `,` +
				pin("generated-client", "api", PinHeldBack, CauseOverPinCap) + `]}`,
			`{"ts":"2026-10-21T10:20:11Z","file":"internal/parse/strict.go","tool":"Edit","fired":[` +
				`{"region":"internal/parse","symptom":"RUF001"}],` +
				`"delivery":"suppressed-repeat",` + identity + `"client":"claude",` +
				`"mechanism":"pre-tool-use-context","context_sha256":"{context}",` +
				v2Keys + `"event_seq":2,"event_records":2,` + shared + `}`,
		}

		fill := strings.NewReplacer("{turn}", TurnDigest(root, "claude", legacySessionID, legacyTurnID))
		lines := readAuditLines(t, root)
		require.Len(t, lines, len(want))

		for i := range want {
			assert.Equal(t, fill.Replace(legacyAuditGolden(root, want[i])), lines[i], "record %d", i+1)
		}
	})

	t.Run("each event gets a new random event id", func(t *testing.T) {
		root := t.TempDir()

		for range 2 {
			ev := twoRecordEvent(root)
			ev.EventID = ""

			_, err := RecordEvent(ev)
			require.NoError(t, err)
		}

		firings, skipped, err := ReadFiringsCounted(root)
		require.NoError(t, err)
		require.Zero(t, skipped)
		require.Len(t, firings, 4)

		for _, f := range firings {
			assert.Regexp(t, `^[0-9a-f]{32}$`, f.EventID)
		}

		assert.Equal(t, firings[0].EventID, firings[1].EventID, "the records of one event share the id")
		assert.Equal(t, firings[2].EventID, firings[3].EventID, "the records of one event share the id")
		assert.NotEqual(t, firings[0].EventID, firings[2].EventID, "two events never share an id")
	})

	t.Run("pins above the cap are counted, not written", func(t *testing.T) {
		root := t.TempDir()
		ev := twoRecordEvent(root)

		// The emit-failed pin sorts last, so the cap cuts it.
		ev.Pins = nil
		for i := range MaxFiringPins {
			ev.Pins = append(ev.Pins, v2Pin(fmt.Sprintf("rule-%03d", i), "internal/parse", PinInjected, ""))
		}

		failed := v2Pin("rule-last", "internal/parse", PinEmitFailed, "")
		ev.Pins = append([]FiringPin{failed}, ev.Pins...)

		_, err := RecordEvent(ev)
		require.NoError(t, err)

		firings, err := ReadFirings(root)
		require.NoError(t, err)
		require.Len(t, firings, 2)

		assert.Len(t, firings[0].Pins, MaxFiringPins)
		assert.Equal(t, 1, firings[0].PinsOmitted)
		assert.NotContains(t, firings[0].Pins, failed)
		assert.Equal(t, ev.Pins[1:], firings[0].Pins, "the injected pins keep their entry order")
		assert.Empty(t, firings[1].Pins, "only the first record carries pins")
		assert.Zero(t, firings[1].PinsOmitted)
	})

	t.Run("concurrent events never interleave", func(t *testing.T) {
		root := t.TempDir()

		const writers, perWriter = 8, 10

		var wg sync.WaitGroup

		for range writers {
			wg.Add(1)

			go func() {
				defer wg.Done()

				for range perWriter {
					ev := twoRecordEvent(root)
					ev.EventID = ""

					_, err := RecordEvent(ev)
					assert.NoError(t, err)
				}
			}()
		}

		wg.Wait()

		firings, skipped, err := ReadFiringsCounted(root)
		require.NoError(t, err)
		require.Zero(t, skipped)
		require.Len(t, firings, 2*writers*perWriter)

		// One Write per event on the O_APPEND file keeps the two records
		// of an event next to each other.
		for i := 0; i < len(firings); i += 2 {
			assert.Equal(t, 1, firings[i].EventSeq)
			assert.Equal(t, 2, firings[i+1].EventSeq)
			assert.Equal(t, firings[i].EventID, firings[i+1].EventID)
		}
	})

	t.Run("a torn write leaves complete records and a skipped tail", func(t *testing.T) {
		root := t.TempDir()
		records, err := eventRecords(twoRecordEvent(root))
		require.NoError(t, err)

		var buf bytes.Buffer
		for _, rec := range records {
			require.NoError(t, json.NewEncoder(&buf).Encode(rec))
		}

		first := bytes.IndexByte(buf.Bytes(), '\n') + 1

		f, err := openAuditLog(root)
		require.NoError(t, err)

		written, err := appendEvent(&shortWriter{w: f, limit: first + 10}, buf.Bytes(), len(records))
		require.NoError(t, f.Close())

		require.EqualError(t, err, "firing log: 2 records declared, 1 written: short write")
		assert.Equal(t, 1, written)

		firings, skipped, err := ReadFiringsCounted(root)
		require.NoError(t, err)
		assert.Len(t, firings, 1)
		assert.Equal(t, 1, skipped, "the reader skips the torn last line")
	})
}

// shortWriter writes at most limit bytes and then reports a short
// write, as a full disk does.
type shortWriter struct {
	w     io.Writer
	limit int
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if len(p) <= s.limit {
		return s.w.Write(p)
	}

	n, err := s.w.Write(p[:s.limit])
	if err != nil {
		return n, err
	}

	return n, io.ErrShortWrite
}

// TestRecordEventRefusesInvalidRecords asserts the four writer errors of
// contract L3. A refused event writes nothing, not even the log file.
func TestRecordEventRefusesInvalidRecords(t *testing.T) {
	cases := []struct {
		name string
		edit func(ev *AuditEvent)
		err  string
	}{
		{
			name: "upper-case event id",
			edit: func(ev *AuditEvent) { ev.EventID = strings.ToUpper(v2EventID) },
			err:  "firing log: a schema 2 record needs an event id",
		},
		{
			name: "short event id",
			edit: func(ev *AuditEvent) { ev.EventID = v2EventID[:31] },
			err:  "firing log: a schema 2 record needs an event id",
		},
		{
			name: "provider id as event id",
			edit: func(ev *AuditEvent) { ev.EventID = legacySessionID },
			err:  "firing log: a schema 2 record needs an event id",
		},
		{
			name: "unknown pin delivery",
			edit: func(ev *AuditEvent) { ev.Pins[1].Delivery = "delivered" },
			err:  `firing log: unknown pin delivery "delivered"`,
		},
		{
			name: "held-back pin without a cause",
			edit: func(ev *AuditEvent) { ev.Pins[0].Cause = "" },
			err:  "firing log: held-back pin " + v2Pin("generated-client", "api", "", "").Identity + " needs a cause",
		},
		{
			name: "held-back pin with an unknown cause",
			edit: func(ev *AuditEvent) { ev.Pins[0].Cause = "budget" },
			err:  "firing log: held-back pin " + v2Pin("generated-client", "api", "", "").Identity + " needs a cause",
		},
		{
			name: "no record",
			edit: func(ev *AuditEvent) { ev.Records = nil },
			err:  "firing log: 0 records declared, 0 written",
		},
		{
			name: "three records",
			edit: func(ev *AuditEvent) { ev.Records = append(ev.Records, ev.Records[0]) },
			err:  "firing log: 3 records declared, 0 written",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			ev := twoRecordEvent(root)
			c.edit(&ev)

			written, err := RecordEvent(ev)
			require.EqualError(t, err, c.err)
			assert.Zero(t, written)
			assert.NoFileExists(t, AuditLogPath(root))
		})
	}
}

// Distinct raw identities of the privacy test. A plain search of the
// log bytes finds any of them that persists.
const (
	rawSessionID  = "raw-session-5b1e"
	rawMatchID    = "raw-tool-use-77c2"
	rawTurnID     = "raw-prompt-0d94"
	rawReceiverID = "raw-agent-a31f"
)

// TestRecordEventStoresNoRawIdentity asserts that no raw session,
// tool-use, turn, or receiver ID reaches the log (contracts §1.6, R-12).
// The digests are the K2 functions over the identity root, and a
// partial identity writes no digest.
func TestRecordEventStoresNoRawIdentity(t *testing.T) {
	root := t.TempDir()
	resettable := false
	read := true
	strict := []model.Lesson{legacyLessonStrict}

	events := []AuditEvent{
		{ // edit hook, child lane
			Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"}, ClientID: "claude",
			SessionID: rawSessionID, MatchID: rawMatchID, Turn: rawTurnID, ReceiverID: rawReceiverID,
			Lane: "child", Resettable: &resettable,
			Records:  []EventRecord{{Status: DeliveryInjected, Lessons: strict, ContextBytes: 40}},
			Emission: "emitted", Evaluation: "complete", PathsComplete: true,
		},
		{ // reset
			Root: root, ClientID: "claude", SessionID: rawSessionID, Turn: rawTurnID,
			ReceiverID: rawReceiverID, Lane: "child", Resettable: &resettable,
			Records:    []EventRecord{{Status: DeliveryReset}},
			Generation: 2, ExploreGeneration: 2, CaptureOptIn: true,
		},
		{ // exploration
			Root: root, Surface: "explore", Tool: "Read", Files: []string{"internal/parse/strict.go"},
			ClientID: "claude", Mechanism: "post-tool-use-context",
			SessionID: rawSessionID, MatchID: rawMatchID, Turn: rawTurnID, ReceiverID: rawReceiverID,
			Lane: "child", Resettable: &resettable, Read: &read,
			Records:  []EventRecord{{Status: DeliveryInjected, Lessons: strict, ContextBytes: 40}},
			Emission: "emitted", Evaluation: "complete", PathsComplete: true, ExploreGeneration: 1,
		},
		{ // preparation: the receiver is unknown, so no identity is given
			Root: root, Surface: "prepare", Files: []string{"internal/parse/strict.go"},
			Records:  []EventRecord{{Lessons: strict}},
			Emission: "returned", Evaluation: "complete", PathsComplete: true,
		},
		{ // partial identities: a receiver without a client, a turn without a session
			Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"},
			MatchID: rawMatchID, Turn: rawTurnID, ReceiverID: rawReceiverID,
			Records: []EventRecord{{Status: DeliveryInjected, Lessons: strict}},
		},
	}

	for _, ev := range events {
		_, err := RecordEvent(ev)
		require.NoError(t, err)
	}

	raw, err := os.ReadFile(AuditLogPath(root))
	require.NoError(t, err)

	for _, id := range []string{rawSessionID, rawMatchID, rawTurnID, rawReceiverID} {
		assert.NotContains(t, string(raw), id)
	}

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, len(events))

	session := SessionDigest(root, rawSessionID)
	match := MatchDigest(root, rawSessionID, rawMatchID)
	context := ContextDigest(root, "claude", rawReceiverID)
	turn := TurnDigest(root, "claude", rawSessionID, rawTurnID)

	for _, i := range []int{0, 2} {
		assert.Equal(t, session, firings[i].SessionSHA)
		assert.Equal(t, match, firings[i].MatchSHA)
		assert.Equal(t, context, firings[i].ContextSHA)
		assert.Equal(t, turn, firings[i].TurnSHA)
	}

	reset := firings[1]
	assert.Equal(t, session, reset.SessionSHA)
	assert.Empty(t, reset.MatchSHA, "a reset names no tool use")
	assert.Equal(t, context, reset.ContextSHA)
	assert.Equal(t, turn, reset.TurnSHA)
	assert.Equal(t, []FiredLesson{}, reset.Fired, "a reset record holds an empty fired list")
	assert.Nil(t, reset.PathsComplete, "a reset record evaluates no paths")

	prepare := firings[3]
	assert.Empty(t, prepare.SessionSHA+prepare.MatchSHA+prepare.ContextSHA+prepare.TurnSHA,
		"a preparation record carries no receiver identity")

	partial := firings[4]
	assert.Empty(t, partial.ContextSHA, "a receiver without a client gives no context digest")
	assert.Empty(t, partial.TurnSHA, "a turn without a session gives no turn digest")
	assert.Equal(t, MatchDigest(root, "", rawMatchID), partial.MatchSHA,
		"the match digest keeps the v1 rule for an empty session")
}

// v2ReaderEvents are the v2 legacy events plus an emit-failed evaluation
// and a reset, all at one fixed time. Legacy readers must skip the two
// new record kinds. The evaluation names the lesson it failed to emit,
// so a reader that counts the record changes a match count.
func v2ReaderEvents(root string) []AuditEvent {
	events := legacyAuditEvents(root)
	resettable := true

	events = append(events,
		AuditEvent{
			Root: root, Tool: "Edit", Files: []string{"internal/parse/strict.go"},
			ClientID: "claude", Mechanism: "pre-tool-use-context",
			SessionID: legacySessionID, MatchID: "tool-use-3", Turn: legacyTurnID,
			ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable,
			Records:  []EventRecord{{Status: DeliveryEvaluation, Lessons: []model.Lesson{legacyLessonStrict}}},
			Pins:     []FiringPin{v2Pin("keep-parser-strict", "internal/parse", PinEmitFailed, "")},
			Emission: "failed", Evaluation: "complete", PathsComplete: true,
			GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", CaptureOptIn: true, Generation: 1,
		},
		AuditEvent{
			Root: root, ClientID: "claude", SessionID: legacySessionID, Turn: legacyTurnID,
			ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable,
			Records:    []EventRecord{{Status: DeliveryReset}},
			Generation: 2, ExploreGeneration: 2, CaptureOptIn: true,
		},
	)

	for i := range events {
		events[i].Now = v2EventTime
	}

	return events
}

// exploreEvent is an exploration that emitted the strict lesson.
func exploreEvent(root string) AuditEvent {
	resettable := true
	read := true

	return AuditEvent{
		Root: root, Surface: "explore", Tool: "Read", Files: []string{"internal/parse/strict.go"},
		ClientID: "claude", Mechanism: "post-tool-use-context",
		SessionID: legacySessionID, MatchID: "tool-use-4", Turn: legacyTurnID,
		ReceiverID: legacyReceiverID, Lane: "root", Resettable: &resettable, Read: &read,
		Records: []EventRecord{{
			Status: DeliveryInjected, Lessons: []model.Lesson{legacyLessonStrict}, ContextBytes: 201,
		}},
		Pins:     []FiringPin{v2Pin("keep-parser-strict", "internal/parse", PinInjected, "")},
		Emission: "emitted", Evaluation: "complete", PathsComplete: true,
		GuidanceSHA: legacyGuidanceSHA, GuidanceStatus: "ok", CaptureOptIn: true, ExploreGeneration: 1,
		Now: v2EventTime,
	}
}

// TestLegacyReadersKeepTheirCountsOnV2Records feeds a v2 log to the
// legacy readers (contract L3, Legacy readers of v2 records). Evaluation
// and reset records change no count. An exploration injection counts as
// a delivery on its own surface.
func TestLegacyReadersKeepTheirCountsOnV2Records(t *testing.T) {
	// v1 is the log of the v1 writers with the fixed time of the v2
	// events, so FirstFirings of both logs compares in full.
	v1Root := t.TempDir()
	for _, c := range legacyAuditCases() {
		require.NoError(t, c.write(v1Root), c.name)
	}

	v1, err := ReadFirings(v1Root)
	require.NoError(t, err)

	for i := range v1 {
		v1[i].TS = v2EventTime.Format(time.RFC3339)
	}

	t.Run("evaluation and reset records change no count", func(t *testing.T) {
		root := t.TempDir()
		events := v2ReaderEvents(root)

		for _, ev := range events {
			_, err := RecordEvent(ev)
			require.NoError(t, err)
		}

		firings, skipped, err := ReadFiringsCounted(root)
		require.NoError(t, err)
		assert.Zero(t, skipped, "every v2 line parses")
		require.Len(t, firings, len(events))

		assert.Equal(t, FirstFirings(v1), FirstFirings(firings))

		for i := range firings {
			firings[i].TS = ""
		}

		assert.Equal(t, legacyAuditSummary(), Summarize(firings, legacyAuditSurfaced()))
	})

	t.Run("an exploration injection counts as a delivery", func(t *testing.T) {
		root := t.TempDir()

		for _, ev := range append(v2ReaderEvents(root), exploreEvent(root)) {
			_, err := RecordEvent(ev)
			require.NoError(t, err)
		}

		firings, err := ReadFirings(root)
		require.NoError(t, err)

		exposure := FirstFirings(v1)
		strict := FiredLesson{Region: legacyLessonStrict.Region, Symptom: legacyLessonStrict.Symptom}.canonicalIdentity()
		e := exposure[strict]
		e.Count++
		e.Matches++
		exposure[strict] = e

		assert.Equal(t, exposure, FirstFirings(firings))

		for i := range firings {
			firings[i].TS = ""
		}

		// The exploration adds one delivery on its own surface and one
		// strict match. It is no instrumented hook delivery.
		want := legacyAuditSummary()
		want.Total++
		want.BySurface["explore"] = 1
		want.Ranked[0].Count++
		want.Ranked[0].Matches++

		assert.Equal(t, want, Summarize(firings, legacyAuditSurfaced()))
	})
}
