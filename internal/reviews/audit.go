package reviews

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/model"
)

// auditFile is the append-only log of edit-hook firings, the impact/decay
// analog of the gate's audit.jsonl. It stays local (gitignored).
const auditFile = "lessons-audit.jsonl"

// maxAuditLine bounds a single record read; a legitimate firing is well
// under a kilobyte. A longer "line" is corruption (a torn append, stray
// binary) — it is skipped, not fatal.
const maxAuditLine = 1 << 20

// DeliveryStatus records whether a matching edit actually received the
// rendered lesson context. Empty means an injected legacy record written
// before delivery status was captured.
type DeliveryStatus string

const (
	// DeliveryInjected means the rendered context was emitted to the agent.
	DeliveryInjected DeliveryStatus = "injected"
	// DeliverySuppressedRepeat means the match was recorded without emitting
	// context because the same lesson had already reached the session.
	DeliverySuppressedRepeat DeliveryStatus = "suppressed-repeat"
)

// FiredLesson identifies one lesson surfaced in a firing.
type FiredLesson struct {
	Region  string `json:"region"`
	Symptom string `json:"symptom"`
}

// canonicalIdentity normalizes the parts of a rendered identity that
// carry no meaning: region order, symptom case, and whitespace. This
// keeps a pin's exposure history across cosmetic lessons.yaml edits
// (reordered regions, retouched case). The note text stays significant:
// a reworded note is a different reminder and restarts the exposure
// clock. Only the measurement join uses this; records on disk and the
// Summarize display keep the raw rendered form.
func (fl FiredLesson) canonicalIdentity() FiredLesson {
	// Known limit: a region path containing ", " would split wrong.
	regions := strings.Split(fl.Region, ", ")
	sort.Strings(regions)

	return FiredLesson{
		Region:  strings.Join(regions, ", "),
		Symptom: strings.ToLower(strings.TrimSpace(fl.Symptom)),
	}
}

// Firing is one record of the edit hook surfacing lessons before an edit.
type Firing struct {
	TS string `json:"ts"`
	// Surface names the emitter: "" reads as the edit hook (the only
	// writer before change_set and check joined).
	Surface string `json:"surface,omitempty"`
	// File carries a single-file firing (the hook); Files a multi-file
	// one (change_set, check) — individually, so distinct-file counts
	// stay meaningful whatever order a diff lists them in.
	File  string        `json:"file,omitempty"`
	Files []string      `json:"files,omitempty"`
	Tool  string        `json:"tool,omitempty"`
	Fired []FiredLesson `json:"fired"`
	// Delivery is present on edit-hook records written by current versions.
	// SessionSHA joins repeated matches without persisting the provider's raw
	// session identifier or making it correlatable across repositories.
	// ContextBytes measures the exact UTF-8 payload emitted as additional
	// context; it is zero for a suppressed match.
	Delivery     DeliveryStatus `json:"delivery,omitempty"`
	SessionSHA   string         `json:"session_sha256,omitempty"`
	MatchSHA     string         `json:"match_sha256,omitempty"`
	Generation   uint64         `json:"context_generation,omitempty"`
	ContextBytes int            `json:"context_bytes,omitempty"`
}

// Delivered reports whether the record represents context that reached the
// agent. Legacy records have no status and are treated as delivered.
func (f Firing) Delivered() bool {
	return f.Delivery == "" || f.Delivery == DeliveryInjected
}

// HookDelivery describes one edit-hook match. SessionID is hashed before it is
// persisted; callers must pass the rendered additional-context byte count, not
// an estimate.
type HookDelivery struct {
	Status       DeliveryStatus
	SessionID    string
	MatchID      string
	Generation   uint64
	ContextBytes int
}

// RecordFiringSurface appends a firing record to
// <root>/.seamark/lessons-audit.jsonl with the emitting surface named
// ("" reads as the edit hook | change_set | check) — --stats reads all
// ambient exposure, not just the hook's. Files travel individually so
// a ten-file check counts ten files, not one joined string. Callers
// treat the error as best-effort: an audit write must never fail the
// action it observed.
func RecordFiringSurface(root, surface string, files []string, tool string, lessons []model.Lesson) error {
	rec, ok := newFiring(surface, files, tool, lessons)
	if !ok {
		return nil
	}

	return appendFiring(root, rec)
}

// RecordFiring is RecordFiringSurface for the edit hook, the original
// (and unnamed) surface.
func RecordFiring(root, file, tool string, lessons []model.Lesson) error {
	return RecordFiringSurface(root, "", []string{file}, tool, lessons)
}

// RecordHookDelivery appends an instrumented edit-hook record. It preserves
// RecordFiring for callers and historical tests that intentionally exercise
// the legacy on-disk shape.
func RecordHookDelivery(root, file, tool string, lessons []model.Lesson, delivery HookDelivery) error {
	if delivery.ContextBytes < 0 {
		return fmt.Errorf("hook delivery context bytes cannot be negative")
	}

	status := delivery.Status
	switch delivery.Status {
	case "", DeliveryInjected:
		status = DeliveryInjected
	case DeliverySuppressedRepeat:
		if delivery.ContextBytes != 0 {
			return fmt.Errorf("suppressed hook delivery cannot contain context bytes")
		}
	default:
		return fmt.Errorf("unknown hook delivery status %q", delivery.Status)
	}

	rec, ok := newFiring("", []string{file}, tool, lessons)
	if !ok {
		return nil
	}

	rec.Delivery = status
	rec.ContextBytes = delivery.ContextBytes

	if delivery.SessionID != "" {
		rec.SessionSHA = sessionDigest(root, delivery.SessionID)
	}

	if delivery.MatchID != "" {
		rec.MatchSHA = matchDigest(root, delivery.SessionID, delivery.MatchID)
	}

	rec.Generation = delivery.Generation

	return appendFiring(root, rec)
}

func matchDigest(root, sessionID, matchID string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(root+"\x00"+sessionID+"\x00"+matchID)))
}

func newFiring(surface string, files []string, tool string, lessons []model.Lesson) (Firing, bool) {
	if len(lessons) == 0 {
		return Firing{}, false
	}

	fired := make([]FiredLesson, len(lessons))
	for i, l := range lessons {
		fired[i] = FiredLesson{Region: l.Region, Symptom: l.Symptom}
	}

	rec := Firing{
		TS:      time.Now().UTC().Format(time.RFC3339),
		Surface: surface,
		Tool:    tool,
		Fired:   fired,
	}

	// The single-file shape stays byte-compatible with every log the
	// hook wrote before surfaces existed.
	if len(files) == 1 {
		rec.File = files[0]
	} else {
		rec.Files = files
	}

	return rec, true
}

// appendFiring writes one record to the firing log.
func appendFiring(root string, rec Firing) error {
	dir := filepath.Join(root, ".seamark")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(filepath.Join(dir, auditFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	return json.NewEncoder(f).Encode(rec)
}

// ReadFirings loads the firing log. A missing log is empty history, not
// an error; an unparseable line is skipped so one corrupt append never
// hides the rest.
func ReadFirings(root string) ([]Firing, error) {
	f, err := os.Open(filepath.Join(root, ".seamark", auditFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []Firing

	br := bufio.NewReader(f)

	for {
		line, tooLong, err := readAuditLine(br, maxAuditLine)

		if len(line) > 0 && !tooLong {
			var rec Firing
			if json.Unmarshal(line, &rec) == nil {
				out = append(out, rec)
			}
		}

		if err != nil {
			if err == io.EOF {
				return out, nil
			}

			return out, err
		}
	}
}

// readAuditLine reads one newline-terminated record, bounded to limit
// bytes. An over-long line is drained to the newline and flagged tooLong
// (skipped by the caller) rather than aborting the read — so one corrupt
// append never hides the records after it.
func readAuditLine(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var buf []byte

	for {
		b, e := br.ReadByte()
		if e != nil {
			return buf, tooLong, e
		}

		if b == '\n' {
			return buf, tooLong, nil
		}

		if len(buf) < limit {
			buf = append(buf, b)
		} else {
			tooLong = true
		}
	}
}

// Fired is one lesson's firing tally.
type Fired struct {
	Region  string
	Symptom string
	Count   int    // records that delivered this lesson
	Matches int    // delivered plus suppressed records naming this lesson
	LastTS  string // most recent delivered or suppressed match
}

// Summary is the aggregate the `--stats` view renders.
type Summary struct {
	Total int // delivered firing events across every surface
	// BySurface splits the events: an actual pre-edit hook reminder, a
	// change_set plan, and a CI check are different kinds of exposure,
	// and "edits reminded" must not count the other two.
	BySurface  map[string]int
	Files      int            // distinct files that triggered a firing
	Ranked     []Fired        // surfaced lessons, most matching opportunities first
	NeverFired []model.Lesson // lessons that would surface but never have
	// Delivery-intensity fields cover current instrumented edit-hook rows.
	// RepeatedHookFirings is a subset of InstrumentedHookFirings where every
	// lesson had already been injected in the same provider session.
	InstrumentedHookFirings int
	RepeatedHookFirings     int
	SuppressedHookFirings   int
	HookContextBytes        int
}

// Summarize aggregates firings and cross-references the currently-
// surfaced lessons to find those that never fire — the decay signal: a
// lesson whose region no edit has touched is a pruning candidate.
func Summarize(firings []Firing, surfaced []model.Lesson) Summary {
	type key struct{ region, symptom string }

	type sessionLessonKey struct {
		session    string
		generation uint64
		lesson     FiredLesson
	}

	count := map[key]*Fired{}
	files := map[string]bool{}
	bySurface := map[string]int{}
	delivered := 0
	seenSessionLessons := map[sessionLessonKey]bool{}

	var instrumented, repeated, suppressed, contextBytes int

	for _, fr := range firings {
		// Unknown delivery values are corrupt future/input records. Product stats
		// stay best-effort and skip them; the benchmark's strict evidence path
		// rejects them instead.
		switch fr.Delivery {
		case "", DeliveryInjected:
		case DeliverySuppressedRepeat:
			if fr.Surface != "" {
				continue
			}
		default:
			continue
		}

		isSuppressed := fr.Surface == "" && fr.Delivery == DeliverySuppressedRepeat
		if isSuppressed {
			suppressed++
		}

		for _, fl := range fr.Fired {
			k := key{fl.Region, fl.Symptom}

			f := count[k]
			if f == nil {
				f = &Fired{Region: fl.Region, Symptom: fl.Symptom}
				count[k] = f
			}

			f.Matches++
			if fr.TS > f.LastTS {
				f.LastTS = fr.TS
			}
		}

		if !fr.Delivered() {
			continue
		}

		delivered++

		surface := fr.Surface
		if surface == "" {
			surface = "hook"
		}

		if surface == "hook" && fr.Delivery == DeliveryInjected {
			instrumented++
			contextBytes += fr.ContextBytes

			if fr.SessionSHA != "" && len(fr.Fired) > 0 {
				allRepeated := true

				for _, lesson := range fr.Fired {
					key := sessionLessonKey{fr.SessionSHA, fr.Generation, lesson.canonicalIdentity()}
					if !seenSessionLessons[key] {
						allRepeated = false
						seenSessionLessons[key] = true
					}
				}

				if allRepeated {
					repeated++
				}
			}
		}

		bySurface[surface]++

		if fr.File != "" {
			files[fr.File] = true
		}

		for _, f := range fr.Files {
			files[f] = true
		}

		for _, fl := range fr.Fired {
			k := key{fl.Region, fl.Symptom}
			count[k].Count++
		}
	}

	ranked := make([]Fired, 0, len(count))
	for _, f := range count {
		ranked = append(ranked, *f)
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Matches != ranked[j].Matches {
			return ranked[i].Matches > ranked[j].Matches
		}

		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}

		return ranked[i].LastTS > ranked[j].LastTS
	})

	var never []model.Lesson

	for _, l := range surfaced {
		// A pinned lesson is a deliberate "never ignore" — surfacing it as
		// a pruning candidate would contradict that, so it is excluded.
		if l.Reviewer == "pinned" {
			continue
		}

		f, ok := count[key{l.Region, l.Symptom}]
		if !ok || f.Count == 0 {
			never = append(never, l)
		}
	}

	return Summary{
		Total:                   delivered,
		BySurface:               bySurface,
		Files:                   len(files),
		Ranked:                  ranked,
		NeverFired:              never,
		InstrumentedHookFirings: instrumented,
		RepeatedHookFirings:     repeated,
		SuppressedHookFirings:   suppressed,
		HookContextBytes:        contextBytes,
	}
}

// Exposure is one lesson's firing history: when it first reached an
// agent, and how many times in total. The first firing — not the pin's
// apply date — starts the outcome loop's exposure clock, because a pin
// that never surfaced cannot have changed behavior.
type Exposure struct {
	First   time.Time // earliest delivered firing with a parseable timestamp, UTC
	Count   int       // delivered firing records naming this lesson, all surfaces
	Matches int       // delivered plus suppressed records naming this lesson
}

// FirstFirings folds the firing log into per-identity exposure and matching
// opportunities. Suppressed matches increase Matches but never Count or First.
// Keys are canonical identities; look pins up with PinIdentity, never by
// re-building the rendered strings. A record with an unparseable
// timestamp still counts toward Count but cannot set First; an
// identity with no parseable timestamp at all is omitted, which
// downstream reads as never fired.
func FirstFirings(firings []Firing) map[FiredLesson]Exposure {
	counts := make(map[FiredLesson]int)
	matches := make(map[FiredLesson]int)
	filtered := make(map[FiredLesson]time.Time)

	for _, entry := range firings {
		switch entry.Delivery {
		case "", DeliveryInjected:
		case DeliverySuppressedRepeat:
			if entry.Surface != "" {
				continue
			}
		default:
			continue
		}

		for _, lesson := range entry.Fired {
			matches[lesson.canonicalIdentity()]++
		}

		if !entry.Delivered() {
			continue
		}

		var firedAt *time.Time
		if ts, err := time.Parse(time.RFC3339, entry.TS); err == nil {
			firedAt = &ts
		}

		for _, lesson := range entry.Fired {
			k := lesson.canonicalIdentity()
			counts[k]++

			if firedAt == nil {
				continue
			}

			if first, ok := filtered[k]; !ok || firedAt.Before(first) {
				filtered[k] = *firedAt
			}
		}
	}

	out := make(map[FiredLesson]Exposure, len(filtered))
	for fl, first := range filtered {
		out[fl] = Exposure{First: first.UTC(), Count: counts[fl], Matches: matches[fl]}
	}

	return out
}
