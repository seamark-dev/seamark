package reviews

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// FiredLesson identifies one lesson surfaced in a firing.
type FiredLesson struct {
	Region  string `json:"region"`
	Symptom string `json:"symptom"`
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
}

// RecordFiringSurface appends a firing record to
// <root>/.seamark/lessons-audit.jsonl with the emitting surface named
// ("" reads as the edit hook | change_set | check) — --stats reads all
// ambient exposure, not just the hook's. Files travel individually so
// a ten-file check counts ten files, not one joined string. Callers
// treat the error as best-effort: an audit write must never fail the
// action it observed.
func RecordFiringSurface(root, surface string, files []string, tool string, lessons []model.Lesson) error {
	if len(lessons) == 0 {
		return nil
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

	return appendFiring(root, rec)
}

// RecordFiring is RecordFiringSurface for the edit hook, the original
// (and unnamed) surface.
func RecordFiring(root, file, tool string, lessons []model.Lesson) error {
	return RecordFiringSurface(root, "", []string{file}, tool, lessons)
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
	Count   int
	LastTS  string
}

// Summary is the aggregate the `--stats` view renders.
type Summary struct {
	Total int // firing events across every surface
	// BySurface splits the events: an actual pre-edit hook reminder, a
	// change_set plan, and a CI check are different kinds of exposure,
	// and "edits reminded" must not count the other two.
	BySurface  map[string]int
	Files      int            // distinct files that triggered a firing
	Ranked     []Fired        // surfaced lessons that fired, most-fired first
	NeverFired []model.Lesson // lessons that would surface but never have
}

// Summarize aggregates firings and cross-references the currently-
// surfaced lessons to find those that never fire — the decay signal: a
// lesson whose region no edit has touched is a pruning candidate.
func Summarize(firings []Firing, surfaced []model.Lesson) Summary {
	type key struct{ region, symptom string }

	count := map[key]*Fired{}
	files := map[string]bool{}
	bySurface := map[string]int{}

	for _, fr := range firings {
		surface := fr.Surface
		if surface == "" {
			surface = "hook"
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

			f := count[k]
			if f == nil {
				f = &Fired{Region: fl.Region, Symptom: fl.Symptom}
				count[k] = f
			}

			f.Count++
			if fr.TS > f.LastTS {
				f.LastTS = fr.TS
			}
		}
	}

	ranked := make([]Fired, 0, len(count))
	for _, f := range count {
		ranked = append(ranked, *f)
	}

	sort.Slice(ranked, func(i, j int) bool {
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

		if _, ok := count[key{l.Region, l.Symptom}]; !ok {
			never = append(never, l)
		}
	}

	return Summary{
		Total:      len(firings),
		BySurface:  bySurface,
		Files:      len(files),
		Ranked:     ranked,
		NeverFired: never,
	}
}
