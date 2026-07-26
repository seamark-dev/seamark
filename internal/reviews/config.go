package reviews

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/model"
)

// DefaultThreshold is the minimum recurrences before a mined lesson
// surfaces: a single comment is noise, a pattern needs repetition.
const DefaultThreshold = 2

// Config tunes how mined lessons surface (`.seamark/lessons.yaml`),
// applied at surface time so edits take effect without re-mining — the
// same contract as the gate's policy.yaml. An absent file yields
// defaults: nothing muted, nothing pinned, threshold DefaultThreshold.
type Config struct {
	// Threshold overrides the minimum recurrences to surface a lesson.
	Threshold int `yaml:"threshold"`
	// Mute hides mined lessons by rule code and/or region prefix.
	Mute []MuteRule `yaml:"mute"`
	// Pin surfaces curated lessons unconditionally — the "must not be
	// ignored" list — even when mining never found them.
	Pin []PinRule `yaml:"pin"`
}

// MuteRule hides lessons. An empty field matches anything, so {rule:
// F541} mutes that code everywhere and {region: alembic/versions} mutes
// every lesson under that path.
type MuteRule struct {
	Rule   string `yaml:"rule"`
	Region string `yaml:"region"`
}

// PinRule is a hand-authored lesson that always surfaces for its region.
type PinRule struct {
	Rule   string `yaml:"rule"`   // the symptom shown (a code or short label)
	Region string `yaml:"region"` // file or directory; "*" or "" is repo-wide
	Note   string `yaml:"note"`   // human explanation, carried into output
}

// DefaultConfig is the zero-tuning config: nothing muted or pinned,
// default threshold. Used as the fallback when a config file is absent
// or unreadable.
func DefaultConfig() *Config {
	return &Config{Threshold: DefaultThreshold}
}

// LoadConfig reads <root>/.seamark/lessons.yaml. A missing file is not
// an error — it means "defaults". A malformed file IS an error: silently
// ignoring a typo'd mute would surface noise the user asked to hide.
// Callers that must stay robust (why/orient/MCP) fall back to
// DefaultConfig on error rather than failing the whole report.
func LoadConfig(root string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("lessons config: %w", err)
	}

	if cfg.Threshold <= 0 {
		cfg.Threshold = DefaultThreshold
	}

	return cfg, nil
}

// muted reports whether a mined lesson is hidden by config.
func (c *Config) muted(l model.Lesson) bool {
	for _, m := range c.Mute {
		if m.Rule != "" && !strings.EqualFold(m.Rule, l.Symptom) {
			continue
		}

		if m.Region != "" && !regionMatches(m.Region, l.Region) {
			continue
		}

		if m.Rule == "" && m.Region == "" {
			continue // an empty mute rule matches nothing, not everything
		}

		return true
	}

	return false
}

// pinsFor returns the pinned lessons that apply to scope — a file or
// directory, or "" for a repo-wide view (orient), where every pin
// applies. Pins render as ordinary lessons marked with the "pinned"
// reviewer so they sort to the top and read as deliberate.
func (c *Config) pinsFor(scope string) []model.Lesson {
	var out []model.Lesson

	for _, p := range c.Pin {
		if scope != "" && p.Region != "" && p.Region != "*" &&
			!regionMatches(p.Region, scope) && !regionMatches(scope, p.Region) {
			continue
		}

		symptom := p.Rule
		if p.Note != "" {
			symptom = strings.TrimSpace(p.Rule + " — " + p.Note)
		}

		region := p.Region
		if region == "" {
			region = "*"
		}

		out = append(out, model.Lesson{
			Region: region, Reviewer: "pinned", Symptom: symptom,
			// A large occurrence count keeps pins above the threshold and
			// ranked ahead of mined lessons.
			Occurrences: 1 << 30,
		})
	}

	return out
}

// Surface applies the config to a set of mined lessons for a given scope:
// drop muted lessons, drop those below threshold, then prepend the
// applicable pins. The result is what any surface (why, orient, the
// hook) should show.
func (c *Config) Surface(mined []model.Lesson, scope string) []model.Lesson {
	out := c.pinsFor(scope)

	for _, l := range mined {
		if l.Occurrences < c.Threshold || c.muted(l) {
			continue
		}

		out = append(out, l)
	}

	return out
}

// regionMatches reports whether target sits within region: an exact
// match, or region as a path-prefix directory of target.
func regionMatches(region, target string) bool {
	if region == target {
		return true
	}

	return strings.HasPrefix(target, strings.TrimSuffix(region, "/")+"/")
}
