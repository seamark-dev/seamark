package distill

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Config is the distill section of .seamark/config.yaml.
type Config struct {
	Distill struct {
		// Write lets `lessons --apply` edit .seamark/lessons.yaml. Off
		// by default: without it, apply prints the pin block for the
		// human to paste — seamark never modifies a reviewed config
		// file without the workspace having opted in, in that same
		// reviewed config.
		Write bool `yaml:"write"`
	} `yaml:"distill"`
}

// LoadConfig reads the distill section of the shared config file. Same
// contract as the other section readers: absent file means defaults,
// malformed file is loud.
func LoadConfig(root string) (*Config, error) {
	cfg := &Config{}

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("distill config: %w", err)
	}

	return cfg, nil
}

// provenanceMarker identifies a comment seamark wrote above a pin it
// applied. Pruning removes only comments carrying it — anything else
// above an entry is the user's prose, and deleting that is exactly the
// work RemovePins exists to protect.
const provenanceMarker = "seamark lessons --distill"

// RenderPins renders proposals as ready-to-paste pin entries, each with
// its provenance comment. The YAML values are marshaled, not
// hand-quoted — a note containing quotes or colons must not be able to
// corrupt the file.
func RenderPins(ps []model.Proposal) (string, error) {
	var b strings.Builder

	for _, p := range ps {
		pin := reviews.PinRule{Rule: p.Rule, Region: "*", Note: p.Note}

		// Both keys on purpose: `region` carries the first (deepest-
		// coverage) region for readers that predate sets — narrower
		// than the `*` they used to get — and `regions` the full set.
		if set := p.RegionSet(); len(set) > 0 {
			pin.Region = set[0]

			if len(set) > 1 {
				pin.Regions = set
			}
		}

		entry, err := yaml.Marshal([]reviews.PinRule{pin})
		if err != nil {
			return "", fmt.Errorf("render pin %s: %w", p.Rule, err)
		}

		fmt.Fprintf(&b, "  # distilled by %s from %d findings (%s, p%d)\n",
			p.Agent, len(p.Members), provenanceMarker, p.ID)

		for line := range strings.SplitSeq(strings.TrimRight(string(entry), "\n"), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	return b.String(), nil
}

// ApplyPins inserts the proposals as pins into <root>/.seamark/
// lessons.yaml, preserving everything already there: entries are
// inserted directly under an existing bare `pin:` line (list items at
// the head of the list — hand-written entries and their comments are
// untouched), or a new pin section is appended when none exists. The
// result must parse as a lessons config before one byte is written; a
// file this function cannot safely edit (a flow-style `pin: []`, say)
// is an error, and the caller falls back to printing the block.
func ApplyPins(root string, ps []model.Proposal) error {
	block, err := RenderPins(ps)
	if err != nil {
		return err
	}

	path := filepath.Join(root, ".seamark", "lessons.yaml")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = []byte("# Review-lesson tuning (see `seamark lessons --list`).\n")
		err = nil
	}

	if err != nil {
		return err
	}

	content, err := insertPins(string(data), block)
	if err != nil {
		return err
	}

	// The edited file must load through the real config path — an
	// apply that bricks every surface's lesson loading is worse than
	// no apply at all.
	if err := yaml.Unmarshal([]byte(content), &reviews.Config{}); err != nil {
		return fmt.Errorf("refusing to write: edited lessons.yaml would not parse: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

// PinKey aliases the pin identity owned by the reviews package —
// apply and prune consume it, but the identity of a pin belongs with
// PinRule, and surfaces (report) must reach it without importing the
// distiller.
type PinKey = reviews.PinKey

// NewPinKey re-exports reviews.NewPinKey for the apply/prune callers.
func NewPinKey(rule, region string, regions []string) PinKey {
	return reviews.NewPinKey(rule, region, regions)
}

// RemovePins deletes the named pins from <root>/.seamark/lessons.yaml,
// with their provenance comments, leaving every other byte alone. It is
// the inverse of ApplyPins and deliberately more careful: adding a
// wrong entry is visible in a diff, while deleting a neighbouring one
// destroys work. A pin already absent is not an error (the user may
// have pruned it by hand); the result must parse AND contain exactly
// the expected remaining pins, or nothing is written.
func RemovePins(root string, keys []PinKey) (removed []PinKey, err error) {
	path := filepath.Join(root, ".seamark", "lessons.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing to prune
		}

		return nil, err
	}

	before := &reviews.Config{}
	if err := yaml.Unmarshal(data, before); err != nil {
		return nil, fmt.Errorf("lessons.yaml does not parse; fix it before pruning: %w", err)
	}

	drop := map[PinKey]bool{}
	for _, k := range keys {
		// Heal raw-constructed keys: canonicalization is idempotent on
		// already-canonical strings, and a bare {Rule, Region: ""} must
		// mean repo-wide here exactly as it does everywhere else.
		drop[NewPinKey(k.Rule, k.Region, nil)] = true
	}

	lines := strings.Split(string(data), "\n")
	cut := make([]bool, len(lines))
	section := ""

	for i, line := range lines {
		// `mute:` entries have the very same `- rule: x` shape as pins,
		// so a rule name is only a pin inside the pin section.
		if key, ok := topLevelKey(line); ok {
			section = key
			continue
		}

		if section != "pin" {
			continue
		}

		rule, ok := pinEntryRule(line)
		if !ok {
			continue
		}

		// The entry's own body: deeper-indented continuation lines,
		// which is also where its region lives.
		end := i + 1
		for end < len(lines) && isContinuation(lines[end]) {
			end++
		}

		body := lines[i+1 : end]

		key := NewPinKey(rule, entryRegion(body), entryRegions(body))
		if !drop[key] {
			continue
		}

		// Its provenance comment sits directly above — but only comments
		// seamark itself wrote go with the entry. A user's comment there
		// may describe the section rather than this pin (certainly so
		// when the pin is the section's first), and its prose is not
		// ours to delete.
		start := i
		for start > 0 && isProvenanceComment(lines[start-1]) {
			start--
		}

		for j := start; j < end; j++ {
			cut[j] = true
		}

		removed = append(removed, key)
	}

	if len(removed) == 0 {
		return nil, nil
	}

	kept := make([]string, 0, len(lines))

	for i, line := range lines {
		if !cut[i] {
			kept = append(kept, line)
		}
	}

	content := strings.Join(kept, "\n")

	after := &reviews.Config{}
	if err := yaml.Unmarshal([]byte(content), after); err != nil {
		return nil, fmt.Errorf("refusing to write: pruned lessons.yaml would not parse: %w", err)
	}

	if err := verifyPruned(before, after, drop); err != nil {
		return nil, err
	}

	return removed, os.WriteFile(path, []byte(content), 0o644)
}

// topLevelKey reads the name of an unindented `key:` line — how the
// scan knows which section it is standing in.
func topLevelKey(line string) (string, bool) {
	if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
		strings.HasPrefix(strings.TrimSpace(line), "#") {
		return "", false
	}

	key, _, found := strings.Cut(line, ":")

	return strings.TrimSpace(key), found
}

// pinEntryRule reads the rule name off a `- rule: x` list item at pin
// indentation, or reports false for any other line.
func pinEntryRule(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(line, " ") || !strings.HasPrefix(trimmed, "- rule:") {
		return "", false
	}

	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- rule:")), `"'`), true
}

// isProvenanceComment reports whether a line is a comment seamark wrote
// when applying a pin.
func isProvenanceComment(line string) bool {
	trimmed := strings.TrimSpace(line)

	return strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, provenanceMarker)
}

// entryRegion reads the region off a pin entry's continuation lines;
// an entry without one is repo-wide (NewPinKey canonicalizes "").
func entryRegion(body []string) string {
	for _, line := range body {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "regions:") {
			continue // the set line; entryRegions reads it
		}

		if rest, ok := strings.CutPrefix(trimmed, "region:"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}

	return ""
}

// entryRegions reads the region set off a pin entry's continuation
// lines: flow style (`regions: [api, db]` — what ApplyPins writes) or
// block style (a bare `regions:` followed by `- api` items — what a
// hand edit legitimately writes). Missing either would compute a wrong
// identity, prune nothing, and desynchronize the file from the
// proposal ledger.
func entryRegions(body []string) []string {
	for i, line := range body {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "regions:")
		if !ok {
			continue
		}

		rest = strings.TrimSpace(rest)

		// Block style: the items follow on their own lines.
		if rest == "" {
			var out []string

			for _, next := range body[i+1:] {
				item, ok := strings.CutPrefix(strings.TrimSpace(next), "- ")
				if !ok {
					break
				}

				if r := strings.Trim(strings.TrimSpace(item), `"'`); r != "" {
					out = append(out, r)
				}
			}

			return out
		}

		rest = strings.TrimPrefix(rest, "[")
		rest = strings.TrimSuffix(rest, "]")

		var out []string

		for part := range strings.SplitSeq(rest, ",") {
			if r := strings.Trim(strings.TrimSpace(part), `"'`); r != "" {
				out = append(out, r)
			}
		}

		return out
	}

	return nil
}

// isContinuation reports whether a line belongs to the list item above
// it. Pin entries live at two-space indent (`  - rule: x`), so any
// line indented four or more spaces is the entry's body — including a
// nested block-style list item (`      - api` under `    regions:`),
// which a hand edit legitimately writes where ApplyPins writes flow.
func isContinuation(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}

	return strings.HasPrefix(line, "    ")
}

// verifyPruned checks the edit did exactly what was asked: the dropped
// rules are gone, every other pin survives with its note intact, and no
// other section moved. A textual edit that passes YAML parsing can
// still have eaten a neighbour — this is what catches that.
func verifyPruned(before, after *reviews.Config, drop map[PinKey]bool) error {
	pinNotes := func(cfg *reviews.Config) map[PinKey]string {
		out := map[PinKey]string{}
		for _, p := range cfg.Pin {
			out[NewPinKey(p.Rule, p.Region, p.Regions)] = p.Note
		}

		return out
	}

	want, got := pinNotes(before), pinNotes(after)

	for key, note := range want {
		if drop[key] {
			continue
		}

		if kept, ok := got[key]; !ok || kept != note {
			return fmt.Errorf("refusing to write: pruning would have changed pin %s", key)
		}
	}

	for key := range got {
		if drop[key] {
			return fmt.Errorf("refusing to write: pin %s survived the prune", key)
		}
	}

	// Mute rules are compared by content, not count: an edit that
	// swapped one for another would otherwise pass unnoticed.
	mutes := func(cfg *reviews.Config) map[reviews.MuteRule]int {
		out := map[reviews.MuteRule]int{}
		for _, m := range cfg.Mute {
			out[m]++
		}

		return out
	}

	if !maps.Equal(mutes(before), mutes(after)) ||
		after.Threshold != before.Threshold || after.PinBudget != before.PinBudget {
		return fmt.Errorf("refusing to write: pruning would have changed other settings")
	}

	return nil
}

// insertPins splices the rendered block into the file text.
func insertPins(content, block string) (string, error) {
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "pin:" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// Insert at the head of the existing list, right under the
			// key: valid YAML, and every hand-written entry below keeps
			// its position and its comments.
			out := append([]string{}, lines[:i+1]...)
			out = append(out, strings.TrimRight(block, "\n"))
			out = append(out, lines[i+1:]...)

			return strings.Join(out, "\n"), nil
		}

		if strings.HasPrefix(trimmed, "pin:") && !strings.HasPrefix(line, " ") {
			// `pin: []` or another flow value: textual insertion cannot
			// be done safely.
			return "", fmt.Errorf("lessons.yaml has a flow-style pin section; add the entries by hand")
		}
	}

	// No pin section yet: append one.
	out := strings.TrimRight(content, "\n")
	if out != "" {
		out += "\n"
	}

	return out + "\npin:\n" + block, nil
}
