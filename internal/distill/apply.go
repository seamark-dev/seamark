package distill

import (
	"fmt"
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

// RenderPins renders proposals as ready-to-paste pin entries, each with
// its provenance comment. The YAML values are marshaled, not
// hand-quoted — a note containing quotes or colons must not be able to
// corrupt the file.
func RenderPins(ps []model.Proposal) (string, error) {
	var b strings.Builder

	for _, p := range ps {
		region := p.Region
		if region == "" {
			region = "*"
		}

		entry, err := yaml.Marshal([]reviews.PinRule{{
			Rule: p.Rule, Region: region, Note: p.Note,
		}})
		if err != nil {
			return "", fmt.Errorf("render pin %s: %w", p.Rule, err)
		}

		fmt.Fprintf(&b, "  # distilled by %s from %d findings (seamark lessons --distill, p%d)\n",
			p.Agent, len(p.Members), p.ID)

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

// RemovePins deletes the named pins from <root>/.seamark/lessons.yaml,
// with their provenance comments, leaving every other byte alone. It is
// the inverse of ApplyPins and deliberately more careful: adding a
// wrong entry is visible in a diff, while deleting a neighbouring one
// destroys work. A rule already absent is not an error (the user may
// have pruned it by hand); the result must parse AND contain exactly
// the expected remaining pins, or nothing is written.
func RemovePins(root string, rules []string) (removed []string, err error) {
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

	drop := map[string]bool{}
	for _, r := range rules {
		drop[r] = true
	}

	lines := strings.Split(string(data), "\n")
	cut := make([]bool, len(lines))

	for i, line := range lines {
		rule, ok := pinEntryRule(line)
		if !ok || !drop[rule] {
			continue
		}

		start, end := i, i+1

		// The entry's own body: deeper-indented continuation lines.
		for end < len(lines) && isContinuation(lines[end]) {
			end++
		}

		// Its provenance comment sits directly above, unseparated by a
		// blank line — that is how ApplyPins writes it.
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
			start--
		}

		for j := start; j < end; j++ {
			cut[j] = true
		}

		removed = append(removed, rule)
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

// pinEntryRule reads the rule name off a `- rule: x` list item at pin
// indentation, or reports false for any other line.
func pinEntryRule(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(line, " ") || !strings.HasPrefix(trimmed, "- rule:") {
		return "", false
	}

	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- rule:")), `"'`), true
}

// isContinuation reports whether a line belongs to the list item above
// it: indented, and not itself a new item or a new key at any lower
// indentation.
func isContinuation(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}

	trimmed := strings.TrimSpace(line)

	return strings.HasPrefix(line, "    ") && !strings.HasPrefix(trimmed, "- ")
}

// verifyPruned checks the edit did exactly what was asked: the dropped
// rules are gone, every other pin survives with its note intact, and no
// other section moved. A textual edit that passes YAML parsing can
// still have eaten a neighbour — this is what catches that.
func verifyPruned(before, after *reviews.Config, drop map[string]bool) error {
	want := map[string]string{}

	for _, p := range before.Pin {
		if !drop[p.Rule] {
			want[p.Rule] = p.Note
		}
	}

	got := map[string]string{}
	for _, p := range after.Pin {
		got[p.Rule] = p.Note
	}

	for rule, note := range want {
		if kept, ok := got[rule]; !ok || kept != note {
			return fmt.Errorf("refusing to write: pruning would have changed pin %q", rule)
		}
	}

	for rule := range got {
		if drop[rule] {
			return fmt.Errorf("refusing to write: pin %q survived the prune", rule)
		}
	}

	if len(after.Mute) != len(before.Mute) || after.Threshold != before.Threshold ||
		after.PinBudget != before.PinBudget {
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
