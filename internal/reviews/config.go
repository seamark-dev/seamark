package reviews

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/wording"
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

// DefaultChangeBudget bounds the lessons one change_set answer carries.
// Its own knob, not the hook's: a ten-file change set under the
// per-file budget would print thirty lines.
const DefaultChangeBudget = 6

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
	// ChangeBudget overrides how many lessons one change_set answer
	// carries across all its files. 0 means DefaultChangeBudget.
	ChangeBudget int `yaml:"change_budget"`
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

// ChangeSetBudget resolves the effective change_set lesson cap.
func (c *Config) ChangeSetBudget() int {
	if c.ChangeBudget > 0 {
		return c.ChangeBudget
	}

	return DefaultChangeBudget
}

// MuteRule hides lessons. An empty field matches anything, so {rule:
// F541} mutes that code everywhere and {region: alembic/versions} mutes
// every lesson under that path.
type MuteRule struct {
	Rule   string `yaml:"rule"`
	Region string `yaml:"region"`
}

// PinRule is a hand-authored lesson that always surfaces for its
// region(s).
type PinRule struct {
	Rule   string `yaml:"rule"`   // the symptom shown (a code or short label)
	Region string `yaml:"region"` // file or directory; "*" or "" is repo-wide
	// Regions widens a pin to a small set of places — one theme
	// legitimately living in `api` AND `db` needs no repo-wide `*`.
	// Flow-rendered (`regions: [api, db]`) so applied entries stay
	// single-line-parseable; readers predating this field see Region,
	// the set's first entry — narrower than the `*` they used to get.
	Regions []string `yaml:"regions,omitempty,flow"`
	Note    string   `yaml:"note"` // human explanation, carried into output
}

// AllRegions returns the pin's effective region set — Region and
// Regions merged, trailing slashes trimmed, deduplicated. Nil means
// repo-wide ("*" and "" collapse to it).
func (p PinRule) AllRegions() []string {
	var out []string

	seen := map[string]bool{}

	for _, r := range append([]string{p.Region}, p.Regions...) {
		r = strings.TrimSuffix(r, "/")

		if r == "" || r == "*" || seen[r] {
			continue
		}

		seen[r] = true
		out = append(out, r)
	}

	return out
}

// appliesTo reports whether the pin bears on scope — any region
// containing (or contained by) it; a repo-wide pin bears on everything.
func (p PinRule) appliesTo(scope string) bool {
	regions := p.AllRegions()

	if scope == "" || len(regions) == 0 {
		return true
	}

	for _, r := range regions {
		if RegionMatches(r, scope) || RegionMatches(scope, r) {
			return true
		}
	}

	return false
}

// matchDepth ranks the pin's specificity for scope: the depth of its
// deepest region applying there (repo-wide is 0). A pin on the file
// beats one on its package beats `*`, exactly as with single regions —
// a multi-region pin ranks by the region that earned it the slot.
func (p PinRule) matchDepth(scope string) int {
	best := 0

	for _, r := range p.AllRegions() {
		if scope != "" && !RegionMatches(r, scope) && !RegionMatches(scope, r) {
			continue
		}

		if d := regionDepth(r); d > best {
			best = d
		}
	}

	return best
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

// pinsFor returns the pin rules that apply to scope — a file or
// directory, or "" for a repo-wide view (orient), where every pin
// applies.
func (c *Config) pinsFor(scope string) []PinRule {
	var out []PinRule

	for _, p := range c.Pin {
		if p.appliesTo(scope) {
			out = append(out, p)
		}
	}

	return out
}

// pinLesson renders a pin as an ordinary lesson marked with the
// "pinned" reviewer, so it sorts to the top and reads as deliberate.
func pinLesson(p PinRule) model.Lesson {
	symptom := p.Rule
	if p.Note != "" {
		symptom = strings.TrimSpace(p.Rule + " — " + p.Note)
	}

	region := strings.Join(p.AllRegions(), ", ")
	if region == "" {
		region = "*"
	}

	return model.Lesson{
		Region: region, Reviewer: "pinned", Symptom: symptom,
		// A large occurrence count keeps pins above the threshold and
		// ranked ahead of mined lessons.
		Occurrences: 1 << 30,
	}
}

// Surface applies the config to a set of mined lessons for a given scope:
// drop muted lessons, drop those below threshold, then prepend the
// applicable pins. The result is what any surface (why, orient, the
// hook) should show.
func (c *Config) Surface(mined []model.Lesson, scope string) []model.Lesson {
	out, _ := c.SurfaceBudget(mined, scope, 0)

	return out
}

// PinAnnotator supplies surface-time knowledge the config layer cannot
// compute itself: an evidence-confidence rank (higher surfaces first)
// and an optional note appended to the pin's rendered symptom. A nil
// annotator ranks every pin equally and adds nothing.
type PinAnnotator func(PinRule) (rank int, note string)

// SurfaceBudget is Surface with a pin cap for ambient surfaces: at most
// pinBudget pins (0 = unlimited), most specific region first — a pin on
// the file beats one on its package beats a repo-wide `*`. The trimmed
// count comes back so the caller can say "+N more" instead of hiding
// them: budgeted, never silent.
func (c *Config) SurfaceBudget(mined []model.Lesson, scope string, pinBudget int) (out []model.Lesson, trimmed int) {
	return c.SurfaceBudgetAnnotated(mined, scope, pinBudget, nil)
}

// SurfacedPin is one applicable pin with its surfacing metadata — the
// building block for surfaces that merge pins across scopes and must
// keep rank and specificity through the merge.
type SurfacedPin struct {
	Pin  PinRule
	Rank int    // annotator's confidence rank; equal when unannotated
	Note string // annotator's display note, "" for none
	// Depth is the deepest of the pin's regions matching the queried
	// scope — the specificity that earned it consideration there.
	Depth int
}

// Lesson renders the surfaced pin the way every surface shows pins.
func (sp SurfacedPin) Lesson() model.Lesson {
	l := pinLesson(sp.Pin)
	// Display-only: identity (Symptom) must stay stable while the
	// annotation ages, or firing statistics fragment.
	l.Annotation = sp.Note

	return l
}

// SurfacePins returns the pins applying to scope, ordered by
// (confidence rank, specificity, file order) — restatements NOT
// collapsed, so a caller merging several scopes can collapse once at
// its own grain.
func (c *Config) SurfacePins(scope string, annotate PinAnnotator) []SurfacedPin {
	applicable := c.pinsFor(scope)

	pins := make([]SurfacedPin, len(applicable))

	for i, p := range applicable {
		pins[i] = SurfacedPin{Pin: p, Depth: p.matchDepth(scope)}

		if annotate != nil {
			pins[i].Rank, pins[i].Note = annotate(p)
		}
	}

	// Stable: equal rank and specificity keep the config file's order,
	// which is the user's own priority order. A multi-region pin ranks
	// by its deepest region that applies to this scope.
	sort.SliceStable(pins, func(i, j int) bool {
		if pins[i].Rank != pins[j].Rank {
			return pins[i].Rank > pins[j].Rank
		}

		return pins[i].Depth > pins[j].Depth
	})

	return pins
}

// CollapseRestated drops surfaced pins that restate an earlier —
// higher-ranked, per the caller's order — pin. Greedy against the kept
// set, not transitive: a pin is dropped only when it restates one that
// survived. The dropped count feeds the caller's "+N more" so a
// collapsed duplicate is pointed at, never silently hidden.
// (Bare linter-code pins collapse only on equal codes — wording.Topic
// owns that rule, so every dedup surface treats codes the same way.)
func CollapseRestated(pins []SurfacedPin) (kept []SurfacedPin, dropped int) {
	if len(pins) < 2 {
		return pins, 0
	}

	topics := make([]wording.Topic, 0, len(pins))
	kept = pins[:0]

	for _, sp := range pins {
		t := wording.New(sp.Pin.Rule, sp.Pin.Note)

		dup := false

		for _, k := range topics {
			if t.Restates(k) {
				dup = true

				break
			}
		}

		if dup {
			continue
		}

		topics = append(topics, t)
		kept = append(kept, sp)
	}

	return kept, len(pins) - len(kept)
}

// SurfaceBudgetAnnotated is SurfaceBudget with a PinAnnotator: pins
// compete for budget slots by (confidence rank, deepest matching
// region) — a weak-evidence pin must not hold a slot a strong one
// wants — and annotator notes travel into the rendered lesson.
func (c *Config) SurfaceBudgetAnnotated(mined []model.Lesson, scope string, pinBudget int,
	annotate PinAnnotator,
) (out []model.Lesson, trimmed int) {
	pins := c.SurfacePins(scope, annotate)

	// Ambient surfaces collapse restatements before the cap: two
	// wordings of one theme must not spend two of the hook's three
	// slots. Deliberate views (budget 0) keep every entry — the file is
	// the user's own, and auditing it needs the duplicates visible.
	if pinBudget > 0 {
		var dupes int

		pins, dupes = CollapseRestated(pins)
		trimmed += dupes
	}

	if pinBudget > 0 && len(pins) > pinBudget {
		trimmed += len(pins) - pinBudget
		pins = pins[:pinBudget]
	}

	for _, sp := range pins {
		out = append(out, sp.Lesson())
	}

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
// Trailing slashes are normalized away, the same equivalence
// RegionMatches applies — "a/b" and "a/b/" are one region.
func regionDepth(region string) int {
	region = strings.TrimSuffix(region, "/")

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

// PinKey identifies one pin entry. A rule name alone is not an
// identity: the same rule is legitimately pinned in several regions
// (RUF001 for scripts and for api), and pruning one must not take the
// others with it. Region holds the canonical form of the pin's whole
// region set — sorted, NUL-joined, "*" for repo-wide — so the key
// stays comparable, region ORDER never splits an identity, and no
// legal path byte can make two different sets collide (a comma can
// appear in a directory name; NUL cannot).
type PinKey struct {
	Rule   string
	Region string
}

// String renders the key for error messages and logs: the NUL joins
// become readable separators.
func (k PinKey) String() string {
	return k.Rule + " @ " + strings.ReplaceAll(k.Region, "\x00", ", ")
}

// NewPinKey builds the canonical identity from a pin's rule and its
// region fields (single + set, either may be empty). Every consumer —
// apply, prune, the file parser, liveness checks — must construct keys
// through here, or two spellings of one pin drift apart.
func NewPinKey(rule, region string, regions []string) PinKey {
	seen := map[string]bool{}

	var set []string

	for _, r := range append([]string{region}, regions...) {
		r = strings.TrimSuffix(strings.TrimSpace(r), "/")

		if r == "" || r == "*" || seen[r] {
			continue
		}

		seen[r] = true
		set = append(set, r)
	}

	sort.Strings(set)

	canonical := strings.Join(set, "\x00")
	if canonical == "" {
		canonical = "*"
	}

	return PinKey{Rule: strings.ToLower(strings.TrimSpace(rule)), Region: canonical}
}
