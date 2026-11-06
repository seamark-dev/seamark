package reviews

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file holds the pin version, the pin-ref, the guidance digest, and
// the exported identity digests (contract K2). They live in reviews, not
// in lessoncheck: the audit writer, distill, archive, delivery, and the
// delivery report already import reviews and cannot import lessoncheck.

// PinVersionScheme labels the version composition. A change of the parts
// below is a new label and therefore a new version for every pin.
const PinVersionScheme = "pin-version/1"

// CheckVersionScheme labels the composition of a check definition.
const CheckVersionScheme = "check-definition/1"

// GuidanceDigestScheme labels the digest over every pin of a config.
const GuidanceDigestScheme = "guidance/1"

// pinIdentityLabel is the first part of the pin identity digest. The
// label keeps the digest apart from every other compose digest.
const pinIdentityLabel = "pin"

// MinPinRef is the shortest pin-ref prefix that ResolvePinRef accepts.
// It equals archive.minPlanRef, so both kinds of reference read alike.
const MinPinRef = 8

// ShownPinRef is the shortest pin-ref that ShortPinRef prints. It equals
// the shown length of a live plan key.
const ShownPinRef = 12

// compose joins the parts with a length prefix on each part. A part can
// hold any byte, and the lengths keep two different part lists apart.
// The bytes equal capture.compose and the loop of contextDigest. reviews
// keeps its own copy because the capture package pulls sqlite into the
// hook paths.
func compose(parts ...string) string {
	var b strings.Builder

	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}

	return b.String()
}

// composeDigest is the lowercase hex SHA-256 of the composition.
func composeDigest(parts ...string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(compose(parts...))))
}

// ComposeDigest is composeDigest for a package that builds a digest of
// its own label (registry §2.2), such as the scope digest of a
// preparation. One composer keeps every compose digest byte-equal.
func ComposeDigest(parts ...string) string {
	return composeDigest(parts...)
}

// PinIdentityDigest is the hex SHA-256 of compose("pin", key.Rule,
// key.Region). Every pin-ref is a prefix of it. Two entries with one
// PinKey share one identity digest.
func PinIdentityDigest(key PinKey) string {
	return composeDigest(pinIdentityLabel, key.Rule, key.Region)
}

// CheckVersion is the hex SHA-256 of the canonical check definition:
// compose("check-definition/1", def.Kind[, "pattern", def.Pattern]).
// The pattern pair is present only when def.Pattern is not empty. The
// check ID is not a part, so two IDs with one definition share a version.
// Unknown keys are not parts either, because Validate refuses them.
func CheckVersion(def CheckDef) string {
	parts := []string{CheckVersionScheme, def.Kind}
	if def.Pattern != "" {
		parts = append(parts, "pattern", def.Pattern)
	}

	return composeDigest(parts...)
}

// PinVersion is the hex SHA-256 of compose("pin-version/1", key.Rule,
// key.Region, note, mode, check). The audit, preparation, verification,
// and the delivery report carry it. A cosmetic edit of lessons.yaml
// keeps it, and a change of meaning gives a new one (AC-16).
//
// The version stays deterministic for a config that Validate refuses.
// An unknown mode and an undefined check each get a marked part, because
// the lenient hook reader computes versions for such a file too.
func (c *Config) PinVersion(p PinRule) string {
	key := NewPinKey(p.Rule, p.Region, p.Regions)

	return composeDigest(PinVersionScheme, key.Rule, key.Region,
		pinVersionNote(p.Note), pinVersionMode(p.Mode), c.pinVersionCheck(p.Check))
}

// pinVersionNote keeps the case and the inner text of the note. Only the
// surrounding spaces and the line terminator form are cosmetic.
func pinVersionNote(note string) string {
	return strings.TrimSpace(strings.ReplaceAll(note, "\r\n", "\n"))
}

// pinVersionMode normalizes the mode part. An omitted mode is advisory,
// so adding "mode: advisory" keeps the version. Any other value keeps
// its raw text behind a marker, so two unknown values stay apart.
func pinVersionMode(mode string) string {
	switch mode {
	case "", "advisory":
		return "advisory"
	case "check", "enforce":
		return mode
	default:
		return "unknown:" + mode
	}
}

// pinVersionCheck is the check part: the version of the bound definition,
// so a renamed check ID keeps the pin version. A reference to an absent
// definition keeps its ID behind a marker.
func (c *Config) pinVersionCheck(id string) string {
	if id == "" {
		return ""
	}

	if def, ok := c.Checks[id]; ok {
		return CheckVersion(def)
	}

	return "unresolved:" + id
}

// ShortPinRef is the printed pin-ref: the first n hex characters of the
// identity digest. n is the shortest length of at least ShownPinRef that
// no other distinct identity digest of c.Pin shares. Duplicate entries
// share one digest, so they do not make the reference longer. A key that
// c.Pin does not hold gets a prefix that names no entry of c.Pin.
func (c *Config) ShortPinRef(key PinKey) string {
	identity := PinIdentityDigest(key)

	var others []string

	for _, p := range c.Pin {
		if other := PinIdentityDigest(NewPinKey(p.Rule, p.Region, p.Regions)); other != identity {
			others = append(others, other)
		}
	}

	n := ShownPinRef
	for n < len(identity) && anyHasPrefix(others, identity[:n]) {
		n++
	}

	return identity[:n]
}

func anyHasPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}

	return false
}

// ErrPinRef marks every ResolvePinRef failure. Callers branch with
// errors.Is; the kind is in PinRefError.
var ErrPinRef = errors.New("pin reference")

// PinRefErrorKind is the closed list of ResolvePinRef failures.
type PinRefErrorKind string

const (
	// PinRefMalformed is an input that is not 8 to 64 hex characters.
	PinRefMalformed PinRefErrorKind = "malformed"
	// PinRefMissing is a well-formed reference that names no entry.
	PinRefMissing PinRefErrorKind = "missing"
	// PinRefAmbiguousPrefix is a reference that names more than one
	// distinct pin identity.
	PinRefAmbiguousPrefix PinRefErrorKind = "ambiguous-prefix"
	// PinRefDuplicateEntries is a reference to one identity that two or
	// more entries hold, because they share one rule and region set.
	PinRefDuplicateEntries PinRefErrorKind = "duplicate-entries"
)

// PinRefError is every ResolvePinRef failure. It wraps ErrPinRef.
type PinRefError struct {
	Kind PinRefErrorKind
	// Ref is the reference after the input normalization: trimmed and
	// lowercased.
	Ref string
	// Entries holds the 1-based entries of a duplicate-entries failure.
	Entries []int
}

// Error describes a selection failure and names the command that lists
// the current references. This build keeps no history of removed pins,
// so no command lists a historical reference.
func (e *PinRefError) Error() string {
	switch e.Kind {
	case PinRefMalformed:
		return fmt.Sprintf("pin reference %q: give at least 8 hex characters from the pin column of `seamark lessons --verify`",
			e.Ref)
	case PinRefMissing:
		return fmt.Sprintf("pin reference %s names no pin in .seamark/lessons.yaml; `seamark lessons --verify` lists the current references",
			e.Ref)
	case PinRefAmbiguousPrefix:
		return fmt.Sprintf("pin reference %s names more than one pin; give more hex characters", e.Ref)
	case PinRefDuplicateEntries:
		entries := make([]string, len(e.Entries))
		for i, entry := range e.Entries {
			entries[i] = strconv.Itoa(entry)
		}

		return fmt.Sprintf("pin reference %s names %d entries with the same rule and regions (pins %s); remove the duplicate entry first",
			e.Ref, len(e.Entries), strings.Join(entries, ","))
	default:
		return fmt.Sprintf("pin reference %s: %s", e.Ref, e.Kind)
	}
}

// Unwrap lets errors.Is(err, ErrPinRef) find every failure kind.
func (e *PinRefError) Unwrap() error {
	return ErrPinRef
}

// ResolvePinRef returns the 0-based index of the one pin entry that the
// reference names. The reference is a prefix of the identity digest, so
// it survives a mode, note, or check edit. It changes on a retarget or a
// rule rename, because those change the identity.
func (c *Config) ResolvePinRef(ref string) (int, error) {
	ref, ok := normalizePinRef(ref)
	if !ok {
		return 0, &PinRefError{Kind: PinRefMalformed, Ref: ref}
	}

	var matched []int

	identities := map[string]bool{}

	for i, p := range c.Pin {
		identity := PinIdentityDigest(NewPinKey(p.Rule, p.Region, p.Regions))
		if strings.HasPrefix(identity, ref) {
			matched = append(matched, i)
			identities[identity] = true
		}
	}

	switch {
	case len(matched) == 0:
		return 0, &PinRefError{Kind: PinRefMissing, Ref: ref}
	case len(identities) > 1:
		return 0, &PinRefError{Kind: PinRefAmbiguousPrefix, Ref: ref}
	case len(matched) > 1:
		entries := make([]int, len(matched))
		for i, index := range matched {
			entries[i] = index + 1
		}

		return 0, &PinRefError{Kind: PinRefDuplicateEntries, Ref: ref, Entries: entries}
	}

	return matched[0], nil
}

// normalizePinRef is the one input rule for a pin-ref (contracts §1.2):
// trim the surrounding spaces, lowercase, then require 8 to 64 lowercase
// hex characters. It returns the normalized text also when the rule
// fails, because the malformed message quotes it.
func normalizePinRef(ref string) (string, bool) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if len(ref) < MinPinRef || len(ref) > sha256.Size*2 {
		return ref, false
	}

	for _, r := range ref {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ref, false
		}
	}

	return ref, true
}

// GuidanceDigest is the hex SHA-256 of compose("guidance/1", n, id1, v1,
// …, idn, vn) over every entry of c.Pin. The pairs are sorted by
// (PinIdentityDigest, PinVersion), and duplicate entries stay; n is the
// decimal entry count. File order and formatting are cosmetic, as for the
// pin version. Budgets, mute, threshold, hook_delivery, and unreferenced
// checks are not parts: they never decide which pin version was
// available, and the audit records held-back pins itself.
func (c *Config) GuidanceDigest() string {
	type pair struct{ identity, version string }

	pairs := make([]pair, len(c.Pin))
	for i, p := range c.Pin {
		pairs[i] = pair{PinIdentityDigest(NewPinKey(p.Rule, p.Region, p.Regions)), c.PinVersion(p)}
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].identity != pairs[j].identity {
			return pairs[i].identity < pairs[j].identity
		}

		return pairs[i].version < pairs[j].version
	})

	parts := make([]string, 0, 2+2*len(pairs))
	parts = append(parts, GuidanceDigestScheme, strconv.Itoa(len(pairs)))

	for _, p := range pairs {
		parts = append(parts, p.identity, p.version)
	}

	return composeDigest(parts...)
}

// SessionDigest is the session digest of the firing log: hex
// sha256(root NUL sessionID). New records outside the firing log carry
// this value, so they join the log on equal bytes. It wraps the tested
// derivation, so only one derivation exists.
func SessionDigest(root, sessionID string) string {
	return sessionDigest(root, sessionID)
}

// MatchDigest is the match digest of the firing log: hex sha256(root NUL
// sessionID NUL matchID). It wraps the tested derivation.
func MatchDigest(root, sessionID, matchID string) string {
	return matchDigest(root, sessionID, matchID)
}

// ContextDigest is the receiving-context digest of the firing log: hex
// sha256(compose("context", root, clientID, receiverID)). It wraps the
// tested derivation. The firing log writes it only when both IDs are
// present, so a caller makes the same test before it records the value.
func ContextDigest(root, clientID, receiverID string) string {
	return contextDigest(root, DeliveryContext{ClientID: clientID, ReceiverID: receiverID})
}
