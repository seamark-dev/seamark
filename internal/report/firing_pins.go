package report

import (
	"cmp"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file maps a selection to the pins of a firing record version 2
// (contract L3). The edit hook, the check advisory, and preparation
// name every matched pin through it, so the surfaces give one pin one
// entry and one version.

// The guidance statuses of a firing record. The list is closed.
const (
	// GuidanceOK means the surface read .seamark/lessons.yaml without an error.
	GuidanceOK = "ok"
	// GuidanceAbsent means no .seamark/lessons.yaml exists, so the defaults apply.
	GuidanceAbsent = "absent"
	// GuidanceInvalid means the read failed and the surface used the
	// defaults. The pins of the file are then unknown, so no digest applies.
	GuidanceInvalid = "invalid"
)

// GuidanceOf returns the guidance digest and status of cfg, which a
// surface read from root with reviews.LoadConfig. loadErr is the error
// of that read. The digest is empty for an invalid file, because the
// surface used the defaults and not the pins of the file.
//
// Only a missing file is absent. Another Lstat error keeps the status
// that the read gives, because an unreadable path is not proof that no
// file exists.
func GuidanceOf(root string, cfg *reviews.Config, loadErr error) (digest, status string) {
	if loadErr != nil {
		return "", GuidanceInvalid
	}

	if cfg == nil {
		cfg = reviews.DefaultConfig()
	}

	_, err := os.Lstat(filepath.Join(root, ".seamark", "lessons.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg.GuidanceDigest(), GuidanceAbsent
	}

	return cfg.GuidanceDigest(), GuidanceOK
}

// FiringPins returns the pins of a firing record for the selection, in
// the entry order of cfg.Pin. selected gives the delivery of each
// selected pin, in the order of s.Pins. A selected pin without a value
// gets the empty delivery, and reviews.RecordEvent refuses the event,
// so a caller error never writes a wrong delivery.
//
// The pins that the fold left out are held back with their cause:
// Restated is restated, OverPinCap is over-pin-cap, and OverTotal is
// over-total. An AlreadyDelivered pin is suppressed-repeat with no cause,
// because the budget did not remove it. Mined lessons have no pin
// identity, so the result never holds them. cfg must be the config of
// the selection, else a pin has no entry and goes after the pins with an
// entry.
func (s Selection) FiringPins(cfg *reviews.Config, selected []reviews.PinDelivery) []reviews.FiringPin {
	type entryPin struct {
		entry int
		pin   reviews.FiringPin
	}

	entries := entryIndex(cfg)

	var pins []entryPin

	add := func(sp reviews.SurfacedPin, delivery reviews.PinDelivery, cause string) {
		key := pinVersionKey{identity: pinIdentityOf(sp.Pin), version: cfg.PinVersion(sp.Pin)}
		pins = append(pins, entryPin{
			entry: entries[key],
			pin: reviews.FiringPin{
				Identity: key.identity, Version: key.version, Delivery: delivery, Cause: cause,
			},
		})
	}

	for i, sp := range s.Pins {
		var delivery reviews.PinDelivery
		if i < len(selected) {
			delivery = selected[i]
		}

		add(sp, delivery, "")
	}

	for _, sp := range s.AlreadyDelivered {
		add(sp, reviews.PinSuppressedRepeat, "")
	}

	for _, held := range []struct {
		pins  []reviews.SurfacedPin
		cause string
	}{
		{s.Restated, reviews.CauseRestated},
		{s.OverPinCap, reviews.CauseOverPinCap},
		{s.OverTotal, reviews.CauseOverTotal},
	} {
		for _, sp := range held.pins {
			add(sp, reviews.PinHeldBack, held.cause)
		}
	}

	// A stable sort keeps the fold order of two pins with one entry. An
	// unknown entry (zero) sorts last, so it never moves a known entry.
	slices.SortStableFunc(pins, func(a, b entryPin) int {
		return cmp.Compare(entrySortKey(a.entry), entrySortKey(b.entry))
	})

	out := make([]reviews.FiringPin, len(pins))
	for i, p := range pins {
		out[i] = p.pin
	}

	return out
}

// EntryOf returns the 1-based entry of the surfaced pin sp in cfg.Pin,
// or zero when no entry has its identity and version. The lookup uses
// the digests and not a position, because the folds copy the pins. Two
// entries with one identity and one version cannot be told apart, so
// both resolve to the first of them.
func EntryOf(cfg *reviews.Config, sp reviews.SurfacedPin) int {
	identity, version := pinIdentityOf(sp.Pin), cfg.PinVersion(sp.Pin)

	for i, p := range cfg.Pin {
		if pinIdentityOf(p) == identity && cfg.PinVersion(p) == version {
			return i + 1
		}
	}

	return 0
}

// pinVersionKey is the identity digest and the version digest of a pin.
type pinVersionKey struct {
	identity, version string
}

// entryIndex maps each identity and version of cfg.Pin to its first
// 1-based entry, by the rule of EntryOf. FiringPins builds it once, so
// a selection with many pins hashes each entry once.
func entryIndex(cfg *reviews.Config) map[pinVersionKey]int {
	index := make(map[pinVersionKey]int, len(cfg.Pin))

	for i, p := range cfg.Pin {
		key := pinVersionKey{identity: pinIdentityOf(p), version: cfg.PinVersion(p)}
		if _, seen := index[key]; !seen {
			index[key] = i + 1
		}
	}

	return index
}

// pinIdentityOf is the K2 identity digest of a pin.
func pinIdentityOf(p reviews.PinRule) string {
	return reviews.PinIdentityDigest(reviews.NewPinKey(p.Rule, p.Region, p.Regions))
}

// entrySortKey orders the entries of FiringPins: known entries by their
// number, and an unknown entry after all of them.
func entrySortKey(entry int) int {
	if entry == 0 {
		return math.MaxInt
	}

	return entry
}
