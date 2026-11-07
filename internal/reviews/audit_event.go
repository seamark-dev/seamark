package reviews

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

// This file holds the writer of firing record version 2 (contract L3).
// One surface invocation is one event. RecordEvent writes the records of
// an event with one event ID, so a reader can count the event once. The
// v1 entry points in audit.go stay for the tests of the legacy shape.

// AuditSchemaVersion is the schema_version of every record that
// RecordEvent writes. A record without the key is a legacy record.
const AuditSchemaVersion = 2

const (
	// DeliveryEvaluation records an evaluation that emitted nothing: no
	// match, no usable path, a selection error, or a failed emission.
	// Only a hook that runs with the recording switch writes it.
	DeliveryEvaluation DeliveryStatus = "evaluation"
	// DeliveryReset records a context reset with the new generations.
	// Only a hook that runs with the recording switch writes it.
	DeliveryReset DeliveryStatus = "reset"
)

// PinDelivery is the delivery of one pin in an event. The list is
// closed. It extends the delivery values of a record, because a record
// cannot say which pin of the event was held back.
type PinDelivery string

const (
	// PinInjected means the edit hook or exploration emitted the pin to an agent.
	PinInjected PinDelivery = "injected"
	// PinReturned means change_set, prepare, or check returned the pin to its caller.
	PinReturned PinDelivery = "returned"
	// PinSuppressedRepeat means the delivered set of this receiver names the matched
	// version. The edit hook decides before the budget, and exploration after it.
	PinSuppressedRepeat PinDelivery = "suppressed-repeat"
	// PinHeldBack means a budget or the restatement collapse left the matched pin out.
	PinHeldBack PinDelivery = "held-back"
	// PinEmitFailed means the emission of the selected pin failed, so the agent did not get it.
	PinEmitFailed PinDelivery = "emit-failed"
)

// The causes of a held-back pin. The list is closed, and only a
// held-back pin has a cause.
const (
	CauseRestated   = "restated"
	CauseOverPinCap = "over-pin-cap"
	CauseOverTotal  = "over-total"
)

// MaxFiringPins bounds the pins of one record. A config with more
// matched pins records the count of the rest in pins_omitted, so one
// event never writes an unbounded line.
const MaxFiringPins = 200

// pinDeliveryRank is the record order of the pin delivery values. A
// stable sort by this rank keeps the entry order of the caller inside
// one value, because FiringPin carries no entry.
var pinDeliveryRank = map[PinDelivery]int{
	PinInjected:         0,
	PinReturned:         1,
	PinSuppressedRepeat: 2,
	PinHeldBack:         3,
	PinEmitFailed:       4,
}

// heldBackCauses is the closed list of the held-back causes.
var heldBackCauses = map[string]bool{
	CauseRestated:   true,
	CauseOverPinCap: true,
	CauseOverTotal:  true,
}

// KnownHeldBackCause reports whether cause is in the closed list of the
// held-back causes. The log can hold any string, so a reader tests a stored
// cause before it counts the cause under its name.
func KnownHeldBackCause(cause string) bool {
	return heldBackCauses[cause]
}

// turnDigestLabel is the first compose part of TurnDigest. The label
// keeps the digest apart from every other compose digest (registry §2.2).
const turnDigestLabel = "turn"

// eventIDBytes is the size of a random event ID: 16 bytes give the
// 32 hex characters of the event_id key.
const eventIDBytes = 16

// errNoEventID refuses an event ID that is not 32 lowercase hex
// characters. The check keeps a provider ID out of the event_id key.
var errNoEventID = errors.New("firing log: a schema 2 record needs an event id")

// FiringPin is the delivery of one matched pin in an event. Identity
// and Version are the K2 digests, never the pin text: the legacy fired
// list already holds the text.
type FiringPin struct {
	Identity string      `json:"pin_identity"`
	Version  string      `json:"pin_version"`
	Delivery PinDelivery `json:"delivery"`
	Cause    string      `json:"cause,omitempty"`
}

// EventRecord is one record of an event: its delivery status, the
// lessons of its fired list, and the emitted context bytes.
type EventRecord struct {
	Status       DeliveryStatus
	Lessons      []model.Lesson
	ContextBytes int
}

// AuditEvent is one surface invocation. It holds plain values and no
// integration type, because reviews must not import integration (K2
// import rule). The caller copies the values from its event.
type AuditEvent struct {
	// Root is the K2 identity root. RecordEvent hashes every raw identity
	// over it and appends to the firing log under it.
	Root, Surface, Tool, ClientID, Mechanism string
	Files                                    []string
	// The raw identities are hashed over Root, and they never persist.
	// An empty value writes no digest. ReceiverID gives a context digest
	// only together with ClientID, as in the v1 writer.
	SessionID, MatchID, Turn, ReceiverID string
	// Lane is "root", "child", or "unknown". Resettable is the reset
	// policy of the adapter for the receiver, nil without a receiver.
	Lane       string
	Resettable *bool
	// Records are the records of the event in write order, one or two.
	Records []EventRecord
	// Pins go on the first record only. The caller passes them in entry
	// order. RecordEvent sorts them by delivery and keeps that order
	// inside one delivery value.
	Pins []FiringPin
	// The values below go on every record of the event. PathsComplete
	// is written only with an Evaluation, because it qualifies one.
	Emission, Evaluation, GuidanceSHA, GuidanceStatus, ScopeSHA string
	PathsComplete                                               bool
	RejectedPaths                                               int
	CaptureOptIn                                                bool
	Generation, ExploreGeneration                               uint64
	Read                                                        *bool
	// EventID is empty for a new random ID. A given ID must be 32
	// lowercase hex characters and never a provider ID.
	EventID string
	// Now is the time of every record of the event. The zero value
	// reads the clock.
	Now time.Time
}

// TurnDigest is the turn identity of the firing log: the hex SHA-256 of
// compose("turn", root, clientID, sessionID, turnID). It is "" when the
// session or the turn is empty, because a partial identity must not join
// two turns. The raw turn ID never persists.
func TurnDigest(root, clientID, sessionID, turnID string) string {
	if sessionID == "" || turnID == "" {
		return ""
	}

	return composeDigest(turnDigestLabel, root, clientID, sessionID, turnID)
}

// RecordEvent appends the records of one event to the firing log under
// ev.Root and returns how many records it appended. All records share
// one event ID and one ts, and only the first carries the pins. The
// records go to the file in one Write on the O_APPEND file. A crash thus
// leaves all of the event, none of it, or a torn last line, which
// ReadFiringsCounted skips. Callers treat the error as best effort: an
// audit write never changes the action it observed.
func RecordEvent(ev AuditEvent) (written int, err error) {
	records, err := eventRecords(ev)
	if err != nil {
		return 0, err
	}

	// The encoder writes the bytes of appendFiring, so a v2 line
	// without its v2 keys equals the v1 line.
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return 0, err
		}
	}

	f, err := openAuditLog(ev.Root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	return appendEvent(f, buf.Bytes(), len(records))
}

// appendEvent writes the encoded records of one event with one Write
// and returns the count of complete records that reached w. A short
// write is an error, because the event then lacks a declared record.
func appendEvent(w io.Writer, encoded []byte, declared int) (int, error) {
	n, err := w.Write(encoded)
	written := bytes.Count(encoded[:n], []byte("\n"))

	if err != nil {
		return written, fmt.Errorf("firing log: %d records declared, %d written: %w", declared, written, err)
	}

	if written != declared {
		return written, fmt.Errorf("firing log: %d records declared, %d written", declared, written)
	}

	return written, nil
}

// eventRecords validates ev and builds its records. It refuses the
// whole event on any error, so the log never holds a part of an event.
func eventRecords(ev AuditEvent) ([]Firing, error) {
	eventID, err := eventIDOf(ev.EventID)
	if err != nil {
		return nil, err
	}

	if n := len(ev.Records); n < 1 || n > 2 {
		return nil, fmt.Errorf("firing log: %d records declared, %d written", n, 0)
	}

	pins, omitted, err := recordPins(ev.Pins)
	if err != nil {
		return nil, err
	}

	now := ev.Now
	if now.IsZero() {
		now = time.Now()
	}

	ts := now.UTC().Format(time.RFC3339)
	records := make([]Firing, len(ev.Records))

	for i, er := range ev.Records {
		rec := eventFiring(ev, er)
		rec.TS = ts
		rec.EventID = eventID
		rec.EventSeq = i + 1
		rec.EventRecords = len(ev.Records)

		if i == 0 {
			rec.Pins = pins
			rec.PinsOmitted = omitted
		}

		records[i] = rec
	}

	return records, nil
}

// eventFiring builds one record of ev without its event position. The
// identity keys follow the rules of RecordHookDeliveryFiles, so a v2
// record joins the v1 records of the same receiver.
func eventFiring(ev AuditEvent, er EventRecord) Firing {
	// fired is never null: an evaluation or a reset record holds an
	// empty list, as a reader of the legacy key expects.
	fired := make([]FiredLesson, len(er.Lessons))
	for i, l := range er.Lessons {
		fired[i] = FiredLesson{Region: l.Region, Symptom: l.Symptom}
	}

	rec := Firing{
		Surface:           ev.Surface,
		Tool:              ev.Tool,
		Fired:             fired,
		Delivery:          er.Status,
		Generation:        ev.Generation,
		ContextBytes:      er.ContextBytes,
		Client:            ev.ClientID,
		Mechanism:         ev.Mechanism,
		SchemaVersion:     AuditSchemaVersion,
		Emission:          ev.Emission,
		Evaluation:        ev.Evaluation,
		RejectedPaths:     ev.RejectedPaths,
		GuidanceSHA:       ev.GuidanceSHA,
		GuidanceStatus:    ev.GuidanceStatus,
		TurnSHA:           TurnDigest(ev.Root, ev.ClientID, ev.SessionID, ev.Turn),
		Lane:              ev.Lane,
		ContextResettable: ev.Resettable,
		ExploreGeneration: ev.ExploreGeneration,
		CaptureOptIn:      ev.CaptureOptIn,
		Read:              ev.Read,
		ScopeSHA:          ev.ScopeSHA,
	}

	// The single-file shape matches newFiring, so the legacy prefix of
	// a v2 line stays byte-compatible.
	if len(ev.Files) == 1 {
		rec.File = ev.Files[0]
	} else {
		rec.Files = ev.Files
	}

	if ev.SessionID != "" {
		rec.SessionSHA = sessionDigest(ev.Root, ev.SessionID)
	}

	if ev.MatchID != "" {
		rec.MatchSHA = matchDigest(ev.Root, ev.SessionID, ev.MatchID)
	}

	if dc := (DeliveryContext{ClientID: ev.ClientID, ReceiverID: ev.ReceiverID}); dc.valid() {
		rec.ContextSHA = contextDigest(ev.Root, dc)
	}

	// paths_complete qualifies an evaluation. A record without an
	// evaluation, such as a reset, has no paths and no key.
	if ev.Evaluation != "" {
		complete := ev.PathsComplete
		rec.PathsComplete = &complete
	}

	return rec
}

// eventIDOf returns the event ID of an event: a new random ID when the
// caller gives none, else the given ID after the format check.
func eventIDOf(given string) (string, error) {
	if given == "" {
		b := make([]byte, eventIDBytes)
		if _, err := rand.Read(b); err != nil {
			return "", errNoEventID
		}

		return hex.EncodeToString(b), nil
	}

	if len(given) != 2*eventIDBytes {
		return "", errNoEventID
	}

	for i := 0; i < len(given); i++ {
		c := given[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errNoEventID
		}
	}

	return given, nil
}

// recordPins validates the pins of an event and returns them in record
// order, cut at MaxFiringPins, with the count of the cut pins.
func recordPins(pins []FiringPin) ([]FiringPin, int, error) {
	for _, p := range pins {
		if _, ok := pinDeliveryRank[p.Delivery]; !ok {
			return nil, 0, fmt.Errorf("firing log: unknown pin delivery %q", p.Delivery)
		}

		// A cause outside the closed list does not say why the pin was
		// left out, so it counts as no cause.
		if p.Delivery == PinHeldBack && !heldBackCauses[p.Cause] {
			return nil, 0, fmt.Errorf("firing log: held-back pin %s needs a cause", p.Identity)
		}
	}

	if len(pins) == 0 {
		return nil, 0, nil
	}

	// Sort a copy, so the caller's slice keeps its order.
	sorted := append([]FiringPin(nil), pins...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return pinDeliveryRank[sorted[i].Delivery] < pinDeliveryRank[sorted[j].Delivery]
	})

	if len(sorted) <= MaxFiringPins {
		return sorted, 0, nil
	}

	return sorted[:MaxFiringPins], len(sorted) - MaxFiringPins, nil
}
