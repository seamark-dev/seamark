package maintain

import (
	"testing"

	"github.com/seamark-dev/seamark/internal/pinfile/pinfiletest"
)

// The Phase 4 benchmarks of pin authoring and of the pin read (make
// tui-measure). They measure the engine on large pin files, long notes,
// and long ledgers, so limits.md can state the practical limits from
// data. Each sub-benchmark builds its own workspace before the timed
// loop. So a plain go test run builds no fixture.

// authorOps are the measured operations. A create appends a new pin at
// the tail. An edit rewrites the note of pin 0, which is linked when the
// ledger has applied rows, so the planner also reads the relationship.
var authorOps = []string{"create", "edit"}

// authorBench builds the workspace of one size: lessons.yaml with
// s.Pins pins and a ledger with s.Applied applied rows. It returns the
// fixture, the file text, and the request of op with a note of s.Note
// runes.
func authorBench(b *testing.B, s pinfiletest.Size, op string) (f *fixture, text string, req AuthorRequest) {
	b.Helper()

	pins := pinfiletest.Pins(s.Pins, "src", "")
	text = pinfiletest.Lessons(b, pins, nil)

	f = newFixture(b, nil)
	f.write(text, 0o644)
	f.propose(pinfiletest.Applied(pins, s.Applied)...)

	note := pinfiletest.Note(s.Note)
	if op == "create" {
		return f, text, newPin("fresh-guidance", note, "src/new")
	}

	return f, text, editNote(f.ref(pins[0].Rule, ""), note)
}

// pinSizes are the sizes of the three benchmarks: pins in the file,
// applied rows in the ledger, and one long note. The note goes into the
// request, or into pin 0 for the read.
func pinSizes() []pinfiletest.Size {
	return pinfiletest.Sweep(pinfiletest.DimPins, pinfiletest.DimApplied, pinfiletest.DimNote)
}

// BenchmarkPhase4PlanAuthor measures one PlanAuthor call: the file
// read, the draft check, the relationship, the candidate, and its
// verification.
func BenchmarkPhase4PlanAuthor(b *testing.B) {
	for _, s := range pinSizes() {
		for _, op := range authorOps {
			b.Run(s.Name()+"/op="+op, func(b *testing.B) {
				f, _, req := authorBench(b, s, op)

				for b.Loop() {
					p, err := PlanAuthor(f.deps(), req)
					if err != nil || p.After == nil || len(p.Blockers) > 0 {
						b.Fatalf("the plan has no candidate: error %v, blockers %v", err, p.Blockers)
					}
				}
			})
		}
	}
}

// BenchmarkPhase4CommitAuthor measures one CommitAuthor call: the plan
// again, the comparison with the reviewed plan, the guarded write, and
// the readback. Each iteration first puts the original file back and
// plans, outside the timer, because a commit makes its own plan stale.
func BenchmarkPhase4CommitAuthor(b *testing.B) {
	for _, s := range pinSizes() {
		for _, op := range authorOps {
			b.Run(s.Name()+"/op="+op, func(b *testing.B) {
				f, text, req := authorBench(b, s, op)

				for b.Loop() {
					b.StopTimer()
					f.write(text, 0o644)
					p := f.authorPlan(f.deps(), req)
					b.StartTimer()

					res, err := CommitAuthor(f.deps(), p)
					if err != nil || res.Outcome != Written {
						b.Fatalf("the commit did not write: outcome %v, error %v", res.Outcome, err)
					}
				}
			})
		}
	}
}

// BenchmarkPhase4Inspect measures one Inspect call: the engine read
// behind each load of Lessons › Pins (services.Pins). It reads the pin
// file, the ledger, and the firing log, and computes the readings of
// the linked pins. BenchmarkPhase4Pins of internal/tui measures the
// console part of the same load.
func BenchmarkPhase4Inspect(b *testing.B) {
	for _, s := range pinSizes() {
		b.Run(s.Name(), func(b *testing.B) {
			pins := pinfiletest.Pins(s.Pins, "src", pinfiletest.Note(s.Note))

			f := newFixture(b, nil)
			f.write(pinfiletest.Lessons(b, pins, nil), 0o644)
			f.propose(pinfiletest.Applied(pins, s.Applied)...)

			for b.Loop() {
				in := Inspect(f.root, f.st, f.probe(), fixtureNow)
				if in.DocErr != nil || len(in.Pins) != s.Pins {
					b.Fatalf("the read lists %d of %d pins: %v", len(in.Pins), s.Pins, in.DocErr)
				}
			}
		})
	}
}
