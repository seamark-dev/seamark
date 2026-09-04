package bench

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/skills"
)

// comparisonSkillsVsMCPOnly is the one comparison a workflow claim may
// freeze: the MCP + skills arm against the MCP-only arm within an instance.
const comparisonSkillsVsMCPOnly = "mcp-skills_vs_mcp-only"

// workflowProcessMetrics names the recorded process rates a claim may list.
// They are reported beside the verdict and never gate it: the claim is about
// the owner invariant, and the rates say how the arms got there.
var workflowProcessMetrics = []string{
	"change_set_before_first_edit_rate",
	"companion_named_rate",
	"why_followed_companion_rate",
	"check_after_last_edit_rate",
}

// WorkflowClaimRegistry freezes the workflow claim thresholds and the
// activation criteria before the cohort runs. It lives in its own file,
// bench/workflow-claims.yaml, so the lessons registry stays untouched.
type WorkflowClaimRegistry struct {
	SchemaVersion int                `yaml:"schema_version"`
	Claims        []WorkflowClaim    `yaml:"claims"`
	Activation    ActivationCriteria `yaml:"activation"`
}

// WorkflowClaim defines one falsifiable claim about the skills and its
// evidence floor. The threshold fields mean what they mean in the lessons
// registry; the assessment reuses the lessons rule by construction.
type WorkflowClaim struct {
	ID                           string   `yaml:"id"`
	Claim                        string   `yaml:"claim"`
	PrimaryMetric                string   `yaml:"primary_metric"`
	Comparison                   string   `yaml:"comparison"`
	Direction                    string   `yaml:"direction"`
	RequiredModel                string   `yaml:"required_model"`
	RequiredEffort               string   `yaml:"required_effort"`
	RequireCleanSeamark          bool     `yaml:"require_clean_seamark"`
	MinimumEffect                float64  `yaml:"minimum_effect"`
	MinimumInstanceEffect        float64  `yaml:"minimum_instance_effect"`
	MaximumHarmfulInterference   float64  `yaml:"maximum_harmful_interference"`
	MinimumInstances             int      `yaml:"minimum_instances"`
	MinimumValidPairsPerInstance int      `yaml:"minimum_valid_pairs_per_instance"`
	Instances                    []string `yaml:"instances"`
	ProcessMetrics               []string `yaml:"process_metrics"`
}

// ActivationCriteria are the frozen pass criteria of the activation
// evaluation: a minimum recall per skill on its should-activate prompts and
// a maximum false-activation rate on the should-not prompts.
type ActivationCriteria struct {
	MinimumRecall          map[string]float64 `yaml:"minimum_recall"`
	MaximumFalseActivation float64            `yaml:"maximum_false_activation"`
}

// LoadWorkflowClaimRegistry parses and validates the committed workflow
// thresholds. Unknown keys and a second document are errors.
func LoadWorkflowClaimRegistry(path string) (WorkflowClaimRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowClaimRegistry{}, err
	}

	var registry WorkflowClaimRegistry
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&registry); err != nil {
		return WorkflowClaimRegistry{}, fmt.Errorf("parse workflow claims: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return WorkflowClaimRegistry{}, fmt.Errorf("parse workflow claims: multiple YAML documents")
		}

		return WorkflowClaimRegistry{}, fmt.Errorf("parse workflow claims: %w", err)
	}

	if err := registry.Validate(); err != nil {
		return WorkflowClaimRegistry{}, err
	}

	return registry, nil
}

// Validate rejects a registry that could silently weaken or ambiguously
// define the evidence threshold, including one without activation criteria.
func (r WorkflowClaimRegistry) Validate() error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("workflow claims schema_version is %d, want 1", r.SchemaVersion)
	}

	if len(r.Claims) == 0 {
		return fmt.Errorf("workflow claims registry is empty")
	}

	known := make(map[string]bool)
	for _, id := range WorkflowInstanceIDs() {
		known[id] = true
	}

	seen := make(map[string]bool, len(r.Claims))

	for _, claim := range r.Claims {
		if err := claim.validate(known); err != nil {
			return err
		}

		if seen[claim.ID] {
			return fmt.Errorf("duplicate workflow claim id %q", claim.ID)
		}

		seen[claim.ID] = true
	}

	return r.Activation.validate()
}

func (c WorkflowClaim) validate(knownInstances map[string]bool) error {
	switch {
	case c.ID == "":
		return fmt.Errorf("workflow claim id is required")
	case c.Claim == "":
		return fmt.Errorf("workflow claim %q has no statement", c.ID)
	case c.PrimaryMetric != "invariant_pass_rate_among_task_complete":
		return fmt.Errorf("workflow claim %q has unsupported primary metric %q", c.ID, c.PrimaryMetric)
	case c.Comparison != comparisonSkillsVsMCPOnly:
		return fmt.Errorf("workflow claim %q has unsupported comparison %q (want %s)", c.ID, c.Comparison, comparisonSkillsVsMCPOnly)
	case c.Direction != "higher":
		return fmt.Errorf("workflow claim %q has unsupported direction %q", c.ID, c.Direction)
	case strings.TrimSpace(c.RequiredModel) == "" || c.RequiredModel != strings.TrimSpace(c.RequiredModel):
		return fmt.Errorf("workflow claim %q required_model must be a non-empty exact model ID", c.ID)
	case strings.TrimSpace(c.RequiredEffort) == "" || c.RequiredEffort != strings.TrimSpace(c.RequiredEffort):
		return fmt.Errorf("workflow claim %q required_effort must be non-empty and trimmed", c.ID)
	case !c.RequireCleanSeamark:
		return fmt.Errorf("workflow claim %q must require a clean Seamark build", c.ID)
	case !finiteIn(c.MinimumEffect, 0, 1) || c.MinimumEffect == 0:
		return fmt.Errorf("workflow claim %q minimum_effect must be in (0, 1]", c.ID)
	case !finiteIn(c.MinimumInstanceEffect, -1, 1):
		return fmt.Errorf("workflow claim %q minimum_instance_effect must be in [-1, 1]", c.ID)
	case !finiteIn(c.MaximumHarmfulInterference, 0, 1):
		return fmt.Errorf("workflow claim %q maximum_harmful_interference must be in [0, 1]", c.ID)
	case c.MinimumInstances < 1:
		return fmt.Errorf("workflow claim %q minimum_instances must be positive", c.ID)
	case c.MinimumValidPairsPerInstance < 1:
		return fmt.Errorf("workflow claim %q minimum_valid_pairs_per_instance must be positive", c.ID)
	case len(c.Instances) < c.MinimumInstances:
		return fmt.Errorf("workflow claim %q names fewer instances than its minimum", c.ID)
	}

	seenInstances := make(map[string]bool, len(c.Instances))
	for _, id := range c.Instances {
		if !knownInstances[id] {
			return fmt.Errorf("workflow claim %q names unknown instance %q", c.ID, id)
		}

		if seenInstances[id] {
			return fmt.Errorf("workflow claim %q repeats instance %q", c.ID, id)
		}

		seenInstances[id] = true
	}

	seenMetrics := make(map[string]bool, len(c.ProcessMetrics))
	for _, metric := range c.ProcessMetrics {
		if !slices.Contains(workflowProcessMetrics, metric) {
			return fmt.Errorf("workflow claim %q lists unknown process metric %q (known: %s)",
				c.ID, metric, strings.Join(workflowProcessMetrics, ", "))
		}

		if seenMetrics[metric] {
			return fmt.Errorf("workflow claim %q repeats process metric %q", c.ID, metric)
		}

		seenMetrics[metric] = true
	}

	return nil
}

func (a ActivationCriteria) validate() error {
	names, err := skills.Names()
	if err != nil {
		return err
	}

	if len(a.MinimumRecall) == 0 {
		return fmt.Errorf("activation criteria need minimum_recall for every shipped skill")
	}

	for name, minimum := range a.MinimumRecall {
		if !slices.Contains(names, name) {
			return fmt.Errorf("activation criteria name unknown skill %q", name)
		}

		if !finiteIn(minimum, 0, 1) || minimum == 0 {
			return fmt.Errorf("activation minimum_recall for %s must be in (0, 1]", name)
		}
	}

	for _, name := range names {
		if _, ok := a.MinimumRecall[name]; !ok {
			return fmt.Errorf("activation criteria lack minimum_recall for %s", name)
		}
	}

	if !finiteIn(a.MaximumFalseActivation, 0, 1) || a.MaximumFalseActivation == 1 {
		return fmt.Errorf("activation maximum_false_activation must be in [0, 1)")
	}

	return nil
}

func finiteIn(value, low, high float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= low && value <= high
}

// shadow expresses the workflow claim as a lessons claim, so the frozen
// assessment rule (minimum pairs, mean and worst effect, harmful
// interference, model, effort, clean build) is shared by construction.
func (c WorkflowClaim) shadow() Claim {
	return Claim{
		ID: c.ID, Claim: c.Claim, PrimaryMetric: c.PrimaryMetric,
		Comparison: comparisonHookOnVsOff, Direction: c.Direction,
		RequiredModel: c.RequiredModel, RequiredEffort: c.RequiredEffort,
		RequireCleanSeamark: c.RequireCleanSeamark, MinimumEffect: c.MinimumEffect,
		MinimumInstanceEffect: c.MinimumInstanceEffect, MaximumHarmfulInterference: c.MaximumHarmfulInterference,
		MinimumInstances: c.MinimumInstances, MinimumValidPairsPerInstance: c.MinimumValidPairsPerInstance,
		Instances: slices.Clone(c.Instances),
	}
}
