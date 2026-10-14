package pinfiletest

import (
	"fmt"
	"strings"
)

// Dim is one fixture dimension of the Phase 4 benchmarks.
type Dim int

const (
	// DimPins is the number of pins in lessons.yaml.
	DimPins Dim = iota
	// DimMutes is the number of mutes in lessons.yaml.
	DimMutes
	// DimPaths is the number of paths of one selection.
	DimPaths
	// DimApplied is the number of applied proposal rows in the ledger.
	DimApplied
	// DimNote is the size of one long note, in runes.
	DimNote
)

// dimNames are the keys of the sub-benchmark names. benchstat reads a
// "/key=value" part of a name as a configuration value.
var dimNames = map[Dim]string{
	DimPins: "pins", DimMutes: "mutes", DimPaths: "paths", DimApplied: "applied", DimNote: "note",
}

// dimValues are the sizes of the contract (contracts.md §7, C3). Base
// takes the second value of each list but the note, which is the first.
var dimValues = map[Dim][]int{
	DimPins:    {10, 500, 2000, 5000},
	DimMutes:   {0, 50, 500},
	DimPaths:   {1, 10, 50},
	DimApplied: {0, 20, 200},
	DimNote:    {1000, 10000, 100000},
}

// Size is one fixture size. A benchmark reads the fields of the
// dimensions that it uses and ignores the others.
type Size struct {
	Pins, Mutes, Paths, Applied, Note int

	// dims are the dimensions that Name shows, in order.
	dims []Dim
}

// Base is the size that every sweep starts from. It holds the second
// value of each list of dimValues, and the first note size.
var Base = Size{Pins: 500, Mutes: 50, Paths: 10, Applied: 20, Note: 1000}

// Sweep returns Base, then one size for every other value of each
// dimension in dims, with the other dimensions at Base. A full cross
// product multiplies the run time. One dimension at a time keeps the
// full run within a few minutes.
func Sweep(dims ...Dim) []Size {
	base := Base
	base.dims = dims
	sizes := []Size{base}

	for _, d := range dims {
		for _, v := range dimValues[d] {
			if v == base.get(d) {
				continue
			}

			s := base
			s.set(d, v)
			sizes = append(sizes, s)
		}
	}

	return sizes
}

// Name returns the sub-benchmark name of the size, such as
// "pins=500/note=1000". It names only the dimensions of the sweep.
func (s Size) Name() string {
	parts := make([]string, len(s.dims))

	for i, d := range s.dims {
		parts[i] = fmt.Sprintf("%s=%d", dimNames[d], s.get(d))
	}

	return strings.Join(parts, "/")
}

// field returns the field of dimension d.
func (s *Size) field(d Dim) *int {
	switch d {
	case DimPins:
		return &s.Pins
	case DimMutes:
		return &s.Mutes
	case DimPaths:
		return &s.Paths
	case DimApplied:
		return &s.Applied
	case DimNote:
		return &s.Note
	default:
		// A value outside the enumeration is a programming error of a
		// benchmark, so it stops the run.
		panic(fmt.Sprintf("pinfiletest: unknown dimension %d", d))
	}
}

func (s Size) get(d Dim) int     { return *s.field(d) }
func (s *Size) set(d Dim, v int) { *s.field(d) = v }
