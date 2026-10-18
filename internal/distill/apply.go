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

// This file holds the apply side of distillation: the write gate in
// .seamark/config.yaml and the pin block that an apply adds. It writes no
// file. internal/proposals adds the block through pinfile.PlanAppend, and
// internal/maintain changes installed pins through pinfile.Plan. So the
// CLI and the console edit lessons.yaml with one editor, which proves
// each result before the write.

// Config is the distill section of .seamark/config.yaml.
type Config struct {
	Distill struct {
		// Write lets `lessons --apply` edit .seamark/lessons.yaml. Off
		// by default: without it, apply prints the pin block for manual
		// insertion. Seamark edits a reviewed config only when that same
		// config opts in.
		Write bool `yaml:"write"`
	} `yaml:"distill"`
}

// LoadConfig reads the distill section of the shared config file. Same
// contract as the other section readers: absent file means defaults,
// malformed file is loud.
func LoadConfig(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}

		return nil, err
	}

	return ParseConfig(data)
}

// ParseConfig decodes the distill section from the bytes of config.yaml.
// LoadConfig calls it, so both entry points report the same error text.
// The settings editor calls it to validate planned bytes before a write.
// Empty input yields the defaults, the same as an absent file.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("distill config: %w", err)
	}

	return cfg, nil
}

// RenderPins renders proposals as ready-to-paste pin entries, each with
// its provenance comment. The YAML values are marshaled, not
// hand-quoted — a note containing quotes or colons must not be able to
// corrupt the file.
//
// The region keys follow reviews.PinForRegions, the layout that the
// manual pin renderer (pinfile.RenderPin) also writes. The bytes of this
// render stay fixed, because the CLI apply goldens pin them.
func RenderPins(ps []model.Proposal) (string, error) {
	var b strings.Builder

	for _, p := range ps {
		pin := reviews.PinForRegions(p.Rule, p.Note, p.RegionSet())

		entry, err := yaml.Marshal([]reviews.PinRule{pin})
		if err != nil {
			return "", fmt.Errorf("render pin %s: %w", p.Rule, err)
		}

		fmt.Fprintf(&b, "  # distilled by %s from %d findings (%s, p%d)\n",
			p.Agent, len(p.Members), reviews.ProvenanceMarker, p.ID)

		for line := range strings.SplitSeq(strings.TrimRight(string(entry), "\n"), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	return b.String(), nil
}
