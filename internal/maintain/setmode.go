package maintain

// This file holds the explicit mode edit of one pin, the engine of
// lessons --set-mode (contract K6). PlanSetMode resolves a pin-ref,
// splices the mode and check keys of that one entry, and validates the
// whole candidate with the strict check tier. CommitSetMode writes the
// candidate through the guarded atomic write.
//
// The edit binds a check that lessons.yaml already defines. It never
// writes a top-level key, never creates a checks entry, never installs a
// hook, and never writes the ledger (K6 step 7, §5 row 27). A person
// writes the check definitions by hand, so the explicit mode command
// never makes the dual top-level write that pinfile refuses.

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// SetModeRequest is one mode edit: the pin, the new mode, and the check
// to bind.
type SetModeRequest struct {
	// PinRef names the pin (contract K2). It is a prefix of the pin
	// identity digest, so it survives a mode, note, or check edit.
	PinRef string
	// Mode is the requested mode. ParseMode gives the closed list.
	Mode lessoncheck.Mode
	// CheckID is the check to bind. An empty ID keeps the current
	// reference, because lessons --set-mode never removes a check.
	CheckID string
}

// SetModePlan is a validated mode edit, not yet written. It holds the
// fields of contract K6. Impact, Write, and GateErr are additions of
// this implementation: the CLI prints the impact, and the gate decides
// the route.
type SetModePlan struct {
	// Entry is the 1-based position of the pin in the pin list.
	Entry int
	Key   reviews.PinKey
	// Before and After hold the mode, the check, and the version of the
	// pin before and after the edit. They are equal when the request
	// changes nothing.
	Before lessoncheck.Pin
	After  lessoncheck.Pin
	// Predicate is the lessoncheck.Describe text of the bound check after
	// the edit, or "" when the pin binds no check.
	Predicate string
	// Change is the verified candidate file. It is nil when the request
	// changes nothing (K6 step 5).
	Change *pinfile.Change
	// Active holds the display lines of the active state (K6 step 6),
	// display only. It is []string in milestone P1, so K6 needs no K10
	// type there: no hook exists yet. It holds the fixed step-6 line.
	// Milestone P4 fills it from the K10 join and may retype it to
	// []lessoncheck.PathCapability.
	Active []string
	// Impact is the checker impact of the edit (contract K1). It is
	// empty when the pin is advisory before and after the edit.
	Impact []lessoncheck.Impact
	// Write is distill.write at plan time; GateErr is its read failure.
	Write   bool
	GateErr error
}

// staleTargetError is the K6 refusal of a pin file that changed between
// the read and the write. It wraps ErrStale, so a caller can branch with
// errors.Is and still print the exact K6 text.
type staleTargetError struct{}

// Error returns the exact K6 text.
func (staleTargetError) Error() string {
	return "lessons --set-mode: .seamark/lessons.yaml changed after it was read; nothing was written — " +
		"rerun the command"
}

// Unwrap returns ErrStale.
func (staleTargetError) Unwrap() error {
	return ErrStale
}

// ParseMode reads a mode word of lessons --set-mode. Only the three mode
// words of contract K1 pass. The CLI checks the flag with it before it
// reads a file, and PlanSetMode checks the request with it, so both
// refuse a bad word with one K6 text.
func ParseMode(word string) (lessoncheck.Mode, error) {
	switch mode := lessoncheck.Mode(word); mode {
	case lessoncheck.ModeAdvisory, lessoncheck.ModeCheck, lessoncheck.ModeEnforce:
		return mode, nil
	}

	return "", fmt.Errorf("--set-mode takes advisory, check, or enforce, got %q", word)
}

// PlanSetMode plans the mode edit of req (contract K6, steps 1 to 6). It
// writes nothing. Each refusal carries the exact K6 or K2 text, and no
// refusal writes a file.
//
// The candidate must pass tier 1 and tier 2 of the whole file before
// the plan returns it. pinfile.Prepare runs the same check at the write,
// but its refusal carries the text of the writer, not the K6 text.
func PlanSetMode(deps Deps, req SetModeRequest) (*SetModePlan, error) {
	mode, err := ParseMode(string(req.Mode))
	if err != nil {
		return nil, err
	}

	if deps.Root == "" {
		return nil, errors.New("maintain: workspace root is empty")
	}

	// Step 1: the read keeps its guard, and tier 1 must pass. An
	// unreadable file is never an empty one.
	doc, err := pinfile.Read(deps.Root)
	if err != nil {
		return nil, err
	}

	// A YAML syntax error also fails the reader, so the first error
	// names the cause once.
	if doc.Unreadable() {
		return nil, fmt.Errorf("lessons --set-mode: %s does not load: %w",
			pinfile.File, cmp.Or(doc.DecodeErr, doc.ReaderErr, doc.ListErr))
	}

	before, _, err := lessoncheck.Parse(doc.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lessons --set-mode: %s does not load: %w", pinfile.File, err)
	}

	// Step 2: the pin-ref names one entry of the delivered pin list.
	// ResolvePinRef refuses two entries with one key, so the key finds
	// exactly one entry of the editable listing.
	at, err := before.Raw.ResolvePinRef(req.PinRef)
	if err != nil {
		return nil, err
	}

	pin := before.Pins[at]

	k := slices.IndexFunc(doc.Entries, func(e pinfile.Entry) bool { return e.Key == pin.Key })
	if k < 0 {
		return nil, fmt.Errorf("lessons --set-mode: pin %s is not in the listing of %s; nothing was written",
			pin.Ref, pinfile.File)
	}

	check, err := targetCheck(before.Raw, pin, mode, req.CheckID)
	if err != nil {
		return nil, err
	}

	// Step 5: the same normalized mode and check need no write.
	if sameMode(pin.Rule.Mode, mode) && check == pin.CheckID {
		return &SetModePlan{
			Entry: pin.Entry, Key: pin.Key, Before: pin, After: pin,
			Predicate: predicateOf(before.Raw, check),
		}, nil
	}

	change, err := pinfile.Plan(doc, []pinfile.Edit{{Ref: doc.Entries[k].Ref(), Rewrite: modeRewrite(mode, req.CheckID)}}, nil)
	if err != nil {
		return nil, fmt.Errorf("lessons --set-mode: pin %s: %w; nothing was written — %s", pin.Ref, err, routeByHand)
	}

	// Step 4: the whole candidate passes tier 1 and tier 2. An error in
	// another entry also refuses the write (§5 row 5).
	after, errs, err := lessoncheck.Parse(change.After)
	if err != nil {
		return nil, fmt.Errorf("lessons --set-mode: the result would not load: %w; nothing was written", err)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("lessons --set-mode: the result would not validate: %s (and %d more); nothing was written",
			errs[0].Message, len(errs)-1)
	}

	impact, err := lessoncheck.ImpactOf(before.Raw, after.Raw, []int{pin.Entry})
	if err != nil {
		return nil, fmt.Errorf("lessons --set-mode: %w", err)
	}

	p := &SetModePlan{
		Entry:     pin.Entry,
		Key:       pin.Key,
		Before:    pin,
		After:     after.Pins[at],
		Predicate: predicateOf(after.Raw, check),
		Change:    change,
		Active:    activeLines(mode),
		Impact:    impact,
	}
	p.Write, p.GateErr = readGate(deps.Root)

	return p, nil
}

// targetCheck returns the check that the pin binds after the edit, or
// the K6 refusal of the request. A given ID must name a definition under
// checks, because the edit never creates one (K6 step 7). Without an ID
// the pin keeps its reference, and check or enforce needs one.
func targetCheck(cfg *reviews.Config, pin lessoncheck.Pin, mode lessoncheck.Mode, id string) (string, error) {
	if id != "" {
		if _, ok := cfg.Checks[id]; !ok {
			return "", fmt.Errorf("check %q is not defined under checks in .seamark/lessons.yaml; "+
				"define it by hand, then rerun", id)
		}

		return id, nil
	}

	if executable(mode) && pin.CheckID == "" {
		return "", fmt.Errorf("--set-mode %s needs --check <check-id>: pin %s has no check", mode, pin.Ref)
	}

	return pin.CheckID, nil
}

// modeRewrite returns the rewrite of the K6 candidate (step 3). The
// advisory mode removes the mode key, because an absent mode means
// advisory. The check key changes only when the request names a check.
func modeRewrite(mode lessoncheck.Mode, id string) *pinfile.Rewrite {
	text := string(mode)
	if mode == lessoncheck.ModeAdvisory {
		text = ""
	}

	rw := &pinfile.Rewrite{Mode: &text}
	if id != "" {
		rw.Check = &id
	}

	return rw
}

// sameMode reports whether the raw mode text of a pin already reads as
// mode. An absent mode reads as advisory. An unknown text never reads as
// a mode, so a request can repair it.
func sameMode(raw string, mode lessoncheck.Mode) bool {
	return raw == string(mode) || (raw == "" && mode == lessoncheck.ModeAdvisory)
}

// executable reports whether the checkers evaluate a pin of this mode.
func executable(mode lessoncheck.Mode) bool {
	return mode == lessoncheck.ModeCheck || mode == lessoncheck.ModeEnforce
}

// predicateOf returns the predicate text of check id in cfg, or "" when
// no definition with a valid predicate exists.
func predicateOf(cfg *reviews.Config, id string) string {
	def, ok := cfg.Checks[id]
	if id == "" || !ok {
		return ""
	}

	text, err := lessoncheck.Describe(def)
	if err != nil {
		return ""
	}

	return text
}

// activeLines returns the active state of a pin with the requested mode.
// No guard and no verification hook exist in milestone P1, so the text
// of an executable mode is the fixed text of K6 step 6, not a K10 join.
func activeLines(mode lessoncheck.Mode) []string {
	if executable(mode) {
		return []string{"active: " + lessoncheck.ActiveRequested}
	}

	return []string{"active: " + lessoncheck.ActiveAdvisory}
}

// CommitSetMode writes the candidate of p (contract K6, steps 4 and 8).
// The write replaces the file only while it still holds the bytes that
// the plan read: Prepare compares the SHA-256, and Apply checks the
// guard again right before the rename. A change in between returns the
// K6 stale refusal and writes nothing.
//
// The write gate decides at the write too, so a gate that turned off
// after the plan blocks the write with ErrWriteDisabled.
func CommitSetMode(deps Deps, p *SetModePlan) error {
	if p == nil || p.Change == nil {
		return errors.New("maintain: the mode edit has no change to write")
	}

	write, gateErr := readGate(deps.Root)
	if err := confirmRefusal(p.Write && p.GateErr == nil && write && gateErr == nil, write, gateErr); err != nil {
		return err
	}

	// Prepare compares only the existence and the SHA-256 of the file. A
	// plan always edits an existing entry, so the file existed at the read.
	expect := integration.FileGuard{Exists: true, SHA256: sha256.Sum256(p.Change.Before)}

	err := deps.files().Replace(deps.Reg, deps.Root, expect, p.Change.After)
	if errors.Is(err, pinfile.ErrStale) {
		return staleTargetError{}
	}

	if err != nil {
		return fmt.Errorf("lessons --set-mode: %w", err)
	}

	return nil
}
