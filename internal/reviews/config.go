package reviews

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/model"
)

// DefaultThreshold is the minimum recurrences before a mined lesson
// surfaces: a single comment is noise, a pattern needs repetition.
const DefaultThreshold = 2

// DefaultPinBudget bounds how many pins one ambient injection (the edit
// hook) carries. Distillation made pins cheap to create, and broad
// regions accumulate: without a budget, every applied pin taxes every
// future edit. Deliberate views (--file, why) stay unbudgeted — asking
// is consent; being injected into is not.
const DefaultPinBudget = 3

// Config tunes how mined lessons surface (`.seamark/lessons.yaml`),
// applied at surface time so edits take effect without re-mining — the
// same contract as the gate's policy.yaml. An absent file yields
// defaults: nothing muted, nothing pinned, threshold DefaultThreshold.
type Config struct {
	// Threshold overrides the minimum recurrences to surface a lesson.
	Threshold int `yaml:"threshold"`
	// PinBudget overrides how many pins the edit hook injects per edit
	// (most-specific regions first; the rest are one pointer line away).
	// 0 means DefaultPinBudget.
	PinBudget int `yaml:"pin_budget"`
	// Mute hides mined lessons by rule code and/or region prefix.
	Mute []MuteRule `yaml:"mute"`
	// Pin surfaces curated lessons unconditionally — the "must not be
	// ignored" list — even when mining never found them.
	Pin []PinRule `yaml:"pin"`
}

// HookPinBudget resolves the effective per-injection pin cap.
func (c *Config) HookPinBudget() int {
	if c.PinBudget > 0 {
		return c.PinBudget
	}

	return DefaultPinBudget
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

// Muted reports whether a mined lesson is hidden by config — exported so
// the `--list` ledger can flag what the user has already silenced.
func (c *Config) Muted(l model.Lesson) bool {
	for _, m := range c.Mute {
		if m.Rule != "" && !strings.EqualFold(m.Rule, l.Symptom) {
			continue
		}

		if m.Region != "" && !RegionMatches(m.Region, l.Region) {
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
			!RegionMatches(p.Region, scope) && !RegionMatches(scope, p.Region) {
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
	out, _ := c.SurfaceBudget(mined, scope, 0)

	return out
}

// SurfaceBudget is Surface with a pin cap for ambient surfaces: at most
// pinBudget pins (0 = unlimited), most specific region first — a pin on
// the file beats one on its package beats a repo-wide `*`. The trimmed
// count comes back so the caller can say "+N more" instead of hiding
// them: budgeted, never silent.
func (c *Config) SurfaceBudget(mined []model.Lesson, scope string, pinBudget int) (out []model.Lesson, trimmed int) {
	pins := c.pinsFor(scope)

	// Stable: equal specificity keeps the config file's order, which is
	// the user's own priority order.
	sort.SliceStable(pins, func(i, j int) bool {
		return regionDepth(pins[i].Region) > regionDepth(pins[j].Region)
	})

	if pinBudget > 0 && len(pins) > pinBudget {
		trimmed = len(pins) - pinBudget
		pins = pins[:pinBudget]
	}

	out = pins

	for _, l := range mined {
		if l.Occurrences < c.Threshold || c.Muted(l) {
			continue
		}

		out = append(out, l)
	}

	return out, trimmed
}

// regionDepth ranks region specificity: repo-wide is 0, each path
// segment adds one. A file region naturally outranks its directory.
func regionDepth(region string) int {
	if region == "" || region == "*" {
		return 0
	}

	return strings.Count(region, "/") + 1
}

// RegionMatches reports whether target sits within region: an exact
// match, or region as a path-prefix directory of target. Exported for
// the `lessons --region` ledger filter.
func RegionMatches(region, target string) bool {
	if region == target {
		return true
	}

	return strings.HasPrefix(target, strings.TrimSuffix(region, "/")+"/")
}
