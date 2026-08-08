package bench

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ClaimRegistry is the versioned set of thresholds frozen before expanding a
// benchmark corpus.
type ClaimRegistry struct {
	SchemaVersion int     `yaml:"schema_version"`
	Claims        []Claim `yaml:"claims"`
}

// Claim defines one falsifiable product claim and its evidence floor.
type Claim struct {
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
}

// ReportInput identifies one raw input exactly.
type ReportInput struct {
	Path   string
	SHA256 string
	Rows   int
}

// ArmReport aggregates valid and invalid attempts for one arm.
type ArmReport struct {
	Attempted     int
	Valid         int
	TaskDone      int
	InvariantPass int
	ContextTokens int64
	CostUSD       float64
}

// CohortReport is one immutable experiment fingerprint. Different harness,
// fixture, model, or runtime identities are never pooled.
type CohortReport struct {
	Instance         string
	Fingerprint      string
	Task             string
	TaskSHA          string
	Pin              string
	Fixture          string
	RequestedModel   string
	Model            string
	SeamarkVersion   string
	SeamarkSHA       string
	AgentVersion     string
	Effort           string
	RuntimeID        string
	MaxBudgetUSD     float64
	Rows             int
	ValidPairs       int
	FavorablePairs   int
	UnfavorablePairs int
	TiedPairs        int
	HarmfulPairs     int
	HookOn           ArmReport
	HookOff          ArmReport
}

// Effect returns the hook-on minus hook-off invariant-pass rate, conditional
// on completing the visible task in each arm.
func (c CohortReport) Effect() (float64, bool) {
	if c.HookOn.TaskDone == 0 || c.HookOff.TaskDone == 0 {
		return 0, false
	}

	on := float64(c.HookOn.InvariantPass) / float64(c.HookOn.TaskDone)
	off := float64(c.HookOff.InvariantPass) / float64(c.HookOff.TaskDone)

	return on - off, true
}

// EffectInterval95 returns a conservative Newcombe-style 95% Wilson score
// interval for the difference between the two conditional proportions. Paired
// direction counts are reported separately because they preserve information
// this interval does not model.
func (c CohortReport) EffectInterval95() (low, high float64, ok bool) {
	onLow, onHigh, ok := wilson95(c.HookOn.InvariantPass, c.HookOn.TaskDone)
	if !ok {
		return 0, 0, false
	}

	offLow, offHigh, ok := wilson95(c.HookOff.InvariantPass, c.HookOff.TaskDone)
	if !ok {
		return 0, 0, false
	}

	onRate := float64(c.HookOn.InvariantPass) / float64(c.HookOn.TaskDone)
	offRate := float64(c.HookOff.InvariantPass) / float64(c.HookOff.TaskDone)
	effect := onRate - offRate
	lowerDistance := math.Hypot(onRate-onLow, offHigh-offRate)
	upperDistance := math.Hypot(onHigh-onRate, offRate-offLow)

	return max(-1, effect-lowerDistance), min(1, effect+upperDistance), true
}

// ClaimAssessment says whether the supplied evidence has reached the frozen
// floor. A positive effect with too few independent instances stays
// insufficient rather than being promoted to a pass.
type ClaimAssessment struct {
	ID                  string
	Definition          Claim
	Status              string
	Reason              string
	QualifyingInstances int
	MeanEffect          float64
	WorstInstanceEffect float64
	HarmfulInterference float64
}

// BenchmarkReport is a deterministic summary of explicit raw inputs.
type BenchmarkReport struct {
	ResultSchemaVersion int
	ClaimSchemaVersion  int
	EvidenceFrom        string
	EvidenceTo          string
	Inputs              []ReportInput
	Cohorts             []CohortReport
	Assessments         []ClaimAssessment
}

// LoadClaimRegistry parses and validates the committed claim thresholds.
func LoadClaimRegistry(path string) (ClaimRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ClaimRegistry{}, err
	}

	var registry ClaimRegistry
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&registry); err != nil {
		return ClaimRegistry{}, fmt.Errorf("parse claims: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ClaimRegistry{}, fmt.Errorf("parse claims: multiple YAML documents")
		}

		return ClaimRegistry{}, fmt.Errorf("parse claims: %w", err)
	}

	if err := registry.Validate(); err != nil {
		return ClaimRegistry{}, err
	}

	return registry, nil
}

// Validate rejects claim files that could silently weaken or ambiguously
// define the evidence threshold.
func (r ClaimRegistry) Validate() error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("claims schema_version is %d, want 1", r.SchemaVersion)
	}

	if len(r.Claims) == 0 {
		return fmt.Errorf("claims registry is empty")
	}

	seen := make(map[string]bool, len(r.Claims))
	knownInstances := make(map[string]bool)

	for _, id := range InstanceIDs() {
		knownInstances[id] = true
	}

	for _, claim := range r.Claims {
		switch {
		case claim.ID == "":
			return fmt.Errorf("claim id is required")
		case seen[claim.ID]:
			return fmt.Errorf("duplicate claim id %q", claim.ID)
		case claim.Claim == "":
			return fmt.Errorf("claim %q has no statement", claim.ID)
		case claim.PrimaryMetric != "invariant_pass_rate_among_task_complete":
			return fmt.Errorf("claim %q has unsupported primary metric %q", claim.ID, claim.PrimaryMetric)
		case claim.Comparison != "hook-on_vs_hook-off":
			return fmt.Errorf("claim %q has unsupported comparison %q", claim.ID, claim.Comparison)
		case claim.Direction != "higher":
			return fmt.Errorf("claim %q has unsupported direction %q", claim.ID, claim.Direction)
		case strings.TrimSpace(claim.RequiredModel) == "" || claim.RequiredModel != strings.TrimSpace(claim.RequiredModel):
			return fmt.Errorf("claim %q required_model must be a non-empty exact model ID", claim.ID)
		case strings.TrimSpace(claim.RequiredEffort) == "" || claim.RequiredEffort != strings.TrimSpace(claim.RequiredEffort):
			return fmt.Errorf("claim %q required_effort must be non-empty and trimmed", claim.ID)
		case !claim.RequireCleanSeamark:
			return fmt.Errorf("claim %q must require a clean Seamark build", claim.ID)
		case math.IsNaN(claim.MinimumEffect) || math.IsInf(claim.MinimumEffect, 0) ||
			claim.MinimumEffect <= 0 || claim.MinimumEffect > 1:
			return fmt.Errorf("claim %q minimum_effect must be in (0, 1]", claim.ID)
		case math.IsNaN(claim.MinimumInstanceEffect) || math.IsInf(claim.MinimumInstanceEffect, 0) ||
			claim.MinimumInstanceEffect < -1 || claim.MinimumInstanceEffect > 1:
			return fmt.Errorf("claim %q minimum_instance_effect must be in [-1, 1]", claim.ID)
		case math.IsNaN(claim.MaximumHarmfulInterference) || math.IsInf(claim.MaximumHarmfulInterference, 0) ||
			claim.MaximumHarmfulInterference < 0 || claim.MaximumHarmfulInterference > 1:
			return fmt.Errorf("claim %q maximum_harmful_interference must be in [0, 1]", claim.ID)
		case claim.MinimumInstances < 1:
			return fmt.Errorf("claim %q minimum_instances must be positive", claim.ID)
		case claim.MinimumValidPairsPerInstance < 1:
			return fmt.Errorf("claim %q minimum_valid_pairs_per_instance must be positive", claim.ID)
		case len(claim.Instances) < claim.MinimumInstances:
			return fmt.Errorf("claim %q names fewer instances than its minimum", claim.ID)
		}

		seen[claim.ID] = true
		seenInstances := make(map[string]bool, len(claim.Instances))

		for _, id := range claim.Instances {
			if !knownInstances[id] {
				return fmt.Errorf("claim %q names unknown instance %q", claim.ID, id)
			}

			if seenInstances[id] {
				return fmt.Errorf("claim %q repeats instance %q", claim.ID, id)
			}

			seenInstances[id] = true
		}
	}

	return nil
}

// BuildBenchmarkReport strictly reads the requested JSONL files. Malformed or
// semantically invalid rows fail the report instead of disappearing.
func BuildBenchmarkReport(paths []string, registry ClaimRegistry) (BenchmarkReport, error) {
	if len(paths) == 0 {
		return BenchmarkReport{}, fmt.Errorf("no result files supplied")
	}

	if err := registry.Validate(); err != nil {
		return BenchmarkReport{}, err
	}

	paths = slices.Clone(paths)
	sort.Strings(paths)
	report := BenchmarkReport{
		ResultSchemaVersion: ResultSchemaVersion,
		ClaimSchemaVersion:  registry.SchemaVersion,
	}
	var allRows []Row
	var evidenceFrom, evidenceTo time.Time
	seenDigests := make(map[string]string, len(paths))

	for i, path := range paths {
		if i > 0 && path == paths[i-1] {
			return BenchmarkReport{}, fmt.Errorf("result file supplied more than once: %s", path)
		}

		rows, digest, err := readRowsStrict(path)
		if err != nil {
			return BenchmarkReport{}, err
		}
		if previous, exists := seenDigests[digest]; exists {
			return BenchmarkReport{}, fmt.Errorf(
				"duplicate evidence content: %s and %s have SHA-256 %s", previous, path, digest,
			)
		}
		seenDigests[digest] = path

		report.Inputs = append(report.Inputs, ReportInput{
			Path: filepath.ToSlash(path), SHA256: digest, Rows: len(rows),
		})

		for _, row := range rows {
			ts, _ := time.Parse(time.RFC3339, row.TS) // ValidateResultRow already checked it.

			if evidenceFrom.IsZero() || ts.Before(evidenceFrom) {
				evidenceFrom = ts
			}

			if evidenceTo.IsZero() || ts.After(evidenceTo) {
				evidenceTo = ts
			}
		}

		allRows = append(allRows, rows...)
	}

	report.EvidenceFrom = evidenceFrom.UTC().Format(time.RFC3339)
	report.EvidenceTo = evidenceTo.UTC().Format(time.RFC3339)

	cohorts, err := buildCohorts(allRows)
	if err != nil {
		return BenchmarkReport{}, err
	}

	report.Cohorts = cohorts
	report.Assessments = assessClaims(registry.Claims, report.Cohorts)

	return report, nil
}

func readRowsStrict(path string) ([]Row, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	digest := sha256.Sum256(data)

	var rows []Row
	lineNumber := 0

	for line := range strings.SplitSeq(string(data), "\n") {
		lineNumber++
		if strings.TrimSpace(line) == "" {
			continue
		}

		var row Row
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		if err := ValidateResultRow(row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil, "", fmt.Errorf("%s: no result rows", path)
	}

	return rows, hex.EncodeToString(digest[:]), nil
}

type cohortKey struct {
	instance    string
	fingerprint string
}

type pairRows struct {
	on, off *Row
}

type cohortIdentity struct {
	taskSHA, pin, fixture, requestedModel            string
	seamarkVersion, seamarkSHA, agentVersion, effort string
	runtimeID                                        string
	maxBudgetUSD                                     float64
}

func reportIdentity(row Row) cohortIdentity {
	return cohortIdentity{
		taskSHA: row.TaskSHA, pin: row.Pin, fixture: row.Fixture,
		requestedModel: row.RequestedModel, seamarkVersion: row.SeamarkVersion,
		seamarkSHA: row.SeamarkSHA, agentVersion: row.AgentVersion,
		effort: row.Effort, runtimeID: row.RuntimeID, maxBudgetUSD: row.MaxBudgetUSD,
	}
}

func buildCohorts(rows []Row) ([]CohortReport, error) {
	grouped := make(map[cohortKey][]Row)
	for _, row := range rows {
		key := cohortKey{instance: row.Instance, fingerprint: row.Fingerprint}
		grouped[key] = append(grouped[key], row)
	}

	cohorts := make([]CohortReport, 0, len(grouped))

	for key, cohortRows := range grouped {
		first := cohortRows[0]
		cohort := CohortReport{
			Instance: key.instance, Fingerprint: key.fingerprint,
			TaskSHA: first.TaskSHA, Pin: first.Pin, Fixture: first.Fixture,
			RequestedModel: first.RequestedModel, SeamarkVersion: first.SeamarkVersion,
			SeamarkSHA: first.SeamarkSHA, AgentVersion: first.AgentVersion,
			Effort: first.Effort, RuntimeID: first.RuntimeID,
			MaxBudgetUSD: first.MaxBudgetUSD, Rows: len(cohortRows),
		}

		if instance, err := InstanceByID(first.Instance); err == nil && instance.TaskSHA() == first.TaskSHA {
			cohort.Task = instance.Task
		}

		identity := reportIdentity(cohortRows[0])
		pairs := make(map[string]pairRows)

		for i := range cohortRows {
			row := &cohortRows[i]
			if reportIdentity(*row) != identity {
				return nil, fmt.Errorf("rows reuse fingerprint %s with conflicting experiment identity",
					shortHash(key.fingerprint))
			}

			if row.Valid && cohort.Model != "" && row.Model != "" && row.Model != cohort.Model {
				return nil, fmt.Errorf("valid rows reuse fingerprint %s with conflicting observed models",
					shortHash(key.fingerprint))
			}

			if row.Valid && cohort.Model == "" && row.Model != "" {
				cohort.Model = row.Model
			}

			switch row.Arm {
			case ArmHookOn:
				accumulateArm(&cohort.HookOn, *row)
			case ArmHookOff:
				accumulateArm(&cohort.HookOff, *row)
			default:
				continue
			}

			pairKey := fmt.Sprintf("%s/%d", row.RunID, row.Trial)
			pair := pairs[pairKey]

			if row.Arm == ArmHookOn {
				if pair.on != nil {
					return nil, fmt.Errorf("duplicate result for %s/%s trial %s arm %s",
						key.instance, shortHash(key.fingerprint), pairKey, row.Arm)
				}

				pair.on = row
			} else {
				if pair.off != nil {
					return nil, fmt.Errorf("duplicate result for %s/%s trial %s arm %s",
						key.instance, shortHash(key.fingerprint), pairKey, row.Arm)
				}

				pair.off = row
			}

			pairs[pairKey] = pair
		}

		for _, pair := range pairs {
			if pair.on == nil || pair.off == nil || !pair.on.Valid || !pair.on.PairValid ||
				!pair.off.Valid || !pair.off.PairValid {
				continue
			}

			cohort.ValidPairs++
			onSuccess := pair.on.TaskDone && pair.on.Avoided
			offSuccess := pair.off.TaskDone && pair.off.Avoided

			switch {
			case onSuccess && !offSuccess:
				cohort.FavorablePairs++
			case !onSuccess && offSuccess:
				cohort.UnfavorablePairs++
			default:
				cohort.TiedPairs++
			}

			if pair.off.TaskDone && !pair.on.TaskDone {
				cohort.HarmfulPairs++
			}
		}

		cohorts = append(cohorts, cohort)
	}

	sort.Slice(cohorts, func(i, j int) bool {
		if cohorts[i].Instance == cohorts[j].Instance {
			return cohorts[i].Fingerprint < cohorts[j].Fingerprint
		}

		return cohorts[i].Instance < cohorts[j].Instance
	})

	return cohorts, nil
}

func accumulateArm(arm *ArmReport, row Row) {
	arm.Attempted++
	if !row.Valid || !row.PairValid {
		return
	}

	arm.Valid++
	if row.TaskDone {
		arm.TaskDone++
	}

	if row.TaskDone && row.Avoided {
		arm.InvariantPass++
	}

	arm.ContextTokens += row.ContextTokens
	arm.CostUSD += row.CostUSD
}

func assessClaims(claims []Claim, cohorts []CohortReport) []ClaimAssessment {
	assessments := make([]ClaimAssessment, 0, len(claims))

	for _, claim := range claims {
		assessment := ClaimAssessment{ID: claim.ID, Definition: claim, Status: "insufficient evidence"}
		allowed := make(map[string]bool, len(claim.Instances))

		for _, id := range claim.Instances {
			allowed[id] = true
		}

		selected := make(map[string]CohortReport)
		ambiguous := false
		excludedByConditions := 0

		for _, cohort := range cohorts {
			if !allowed[cohort.Instance] {
				continue
			}

			if !cohortMatchesClaimConditions(cohort, claim) {
				excludedByConditions++

				continue
			}

			if cohort.ValidPairs < claim.MinimumValidPairsPerInstance {
				continue
			}

			if _, exists := selected[cohort.Instance]; exists {
				ambiguous = true
				continue
			}

			selected[cohort.Instance] = cohort
		}

		if ambiguous {
			assessment.Reason = "multiple qualifying fingerprints exist for an instance; select an explicit result set"
			assessments = append(assessments, assessment)

			continue
		}

		assessment.QualifyingInstances = len(selected)
		if len(selected) < claim.MinimumInstances {
			assessment.Reason = fmt.Sprintf(
				"%d/%d independent instances have at least %d valid pairs",
				len(selected), claim.MinimumInstances, claim.MinimumValidPairsPerInstance,
			)

			if excludedByConditions > 0 {
				assessment.Reason += fmt.Sprintf(
					"; %d cohort(s) violate frozen model, effort, or clean-build conditions",
					excludedByConditions,
				)
			}

			assessments = append(assessments, assessment)

			continue
		}

		var (
			effectTotal    float64
			harmful, pairs int
		)
		invalidEffect := false
		worstEffect := 1.0

		for _, instanceID := range claim.Instances {
			cohort, ok := selected[instanceID]
			if !ok {
				continue
			}

			effect, ok := cohort.Effect()
			if !ok {
				invalidEffect = true
				break
			}

			effectTotal += effect
			worstEffect = min(worstEffect, effect)
			harmful += cohort.HarmfulPairs
			pairs += cohort.ValidPairs
		}

		if invalidEffect || pairs == 0 {
			assessment.Reason = "a qualifying cohort has no task-complete result in one arm"
			assessments = append(assessments, assessment)

			continue
		}

		assessment.MeanEffect = effectTotal / float64(len(selected))
		assessment.WorstInstanceEffect = worstEffect
		assessment.HarmfulInterference = float64(harmful) / float64(pairs)

		if assessment.MeanEffect >= claim.MinimumEffect &&
			assessment.WorstInstanceEffect >= claim.MinimumInstanceEffect &&
			assessment.HarmfulInterference <= claim.MaximumHarmfulInterference {
			assessment.Status = "passes frozen threshold"
			assessment.Reason = "mean effect, per-instance effect, and harmful-interference thresholds pass"
		} else {
			assessment.Status = "does not pass frozen threshold"
			assessment.Reason = "mean effect, per-instance effect, or harmful-interference threshold failed"
		}

		assessments = append(assessments, assessment)
	}

	return assessments
}

func cohortMatchesClaimConditions(cohort CohortReport, claim Claim) bool {
	if claim.RequiredModel != "" &&
		(cohort.RequestedModel != claim.RequiredModel || cohort.Model != claim.RequiredModel) {
		return false
	}

	if claim.RequiredEffort != "" && cohort.Effort != claim.RequiredEffort {
		return false
	}

	if claim.RequireCleanSeamark &&
		(cohort.SeamarkVersion == "" || strings.HasSuffix(cohort.SeamarkVersion, "-dirty") ||
			cohort.SeamarkSHA == "") {
		return false
	}

	return true
}

// Markdown renders a deterministic, reviewable report. Percentages are point
// estimates; the paired discordance counts remain visible so tiny samples do
// not look conclusive.
func (r BenchmarkReport) Markdown() string {
	var out strings.Builder
	out.WriteString("# Lessons benchmark report\n\n")
	fmt.Fprintf(&out, "Result schema: v%d; claim schema: v%d; evidence window: %s to %s.\n\n",
		r.ResultSchemaVersion, r.ClaimSchemaVersion, r.EvidenceFrom, r.EvidenceTo)
	out.WriteString("## Raw inputs\n\n")
	out.WriteString("| File | Rows | SHA-256 |\n|---|---:|---|\n")

	for _, input := range r.Inputs {
		fmt.Fprintf(&out, "| %s | %d | `%s` |\n",
			tableCell(input.Path), input.Rows, input.SHA256)
	}

	out.WriteString("\n## Immutable cohorts\n\n")
	out.WriteString("Rows are pooled only when their full experiment fingerprint matches. " +
		"Invariant rates are conditional on completing the visible task.\n\n")
	out.WriteString("| Instance | Fingerprint | Model | Valid pairs | Hook-on invariant | Hook-off invariant | Effect | Task completion on/off | Mean context on/off | Cost on/off |\n")
	out.WriteString("|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")

	for _, cohort := range r.Cohorts {
		fmt.Fprintf(
			&out, "| %s | `%s` | %s | %d | %s | %s | %s | %s / %s | %s / %s | $%.2f / $%.2f |\n",
			tableCell(cohort.Instance), shortHash(cohort.Fingerprint), tableCell(valueOrNA(cohort.Model)), cohort.ValidPairs,
			rate(cohort.HookOn.InvariantPass, cohort.HookOn.TaskDone),
			rate(cohort.HookOff.InvariantPass, cohort.HookOff.TaskDone),
			effectString(cohort),
			rate(cohort.HookOn.TaskDone, cohort.HookOn.Valid),
			rate(cohort.HookOff.TaskDone, cohort.HookOff.Valid),
			meanContext(cohort.HookOn), meanContext(cohort.HookOff),
			cohort.HookOn.CostUSD, cohort.HookOff.CostUSD,
		)
	}

	out.WriteString("\n### Exact cohort identities\n\n")

	for _, cohort := range r.Cohorts {
		fmt.Fprintf(&out, "- %s / %s\n", markdownCode(cohort.Instance), markdownCode(cohort.Fingerprint))
		fmt.Fprintf(&out, "  - Task %s; fixture %s; pin %s.\n",
			markdownCode(cohort.TaskSHA), markdownCode(cohort.Fixture), markdownCode(cohort.Pin))
		if cohort.Task != "" {
			fmt.Fprintf(&out, "  - Task prompt: %s\n", singleLine(cohort.Task))
		}
		fmt.Fprintf(&out, "  - Model requested %s, observed %s; effort %s; maximum $%.2f/session.\n",
			markdownCode(valueOrNA(cohort.RequestedModel)), markdownCode(valueOrNA(cohort.Model)),
			markdownCode(valueOrNA(cohort.Effort)), cohort.MaxBudgetUSD)
		fmt.Fprintf(&out, "  - Agent %s; runtime %s.\n",
			markdownCode(valueOrNA(cohort.AgentVersion)), markdownCode(valueOrNA(cohort.RuntimeID)))
		fmt.Fprintf(&out, "  - Seamark %s; binary %s.\n",
			markdownCode(valueOrNA(cohort.SeamarkVersion)), markdownCode(valueOrNA(cohort.SeamarkSHA)))
	}

	out.WriteString("\n### Paired details\n\n")

	for i, cohort := range r.Cohorts {
		if i > 0 {
			out.WriteString("\n")
		}
		fmt.Fprintf(&out, "Paired directions for `%s`/`%s`: %d favorable, %d unfavorable, %d tied; %d harmful task regressions.\n",
			cohort.Instance, shortHash(cohort.Fingerprint), cohort.FavorablePairs,
			cohort.UnfavorablePairs, cohort.TiedPairs, cohort.HarmfulPairs)
		if invalid := cohort.HookOn.Attempted + cohort.HookOff.Attempted -
			cohort.HookOn.Valid - cohort.HookOff.Valid; invalid > 0 {
			fmt.Fprintf(&out, "Invalid attempts excluded: %d.\n", invalid)
		}
		if low, high, ok := cohort.EffectInterval95(); ok {
			fmt.Fprintf(&out, "Approximate 95%% Wilson score interval for the conditional effect: %+.0f to %+.0f pp.\n",
				low*100, high*100)
		}
	}

	out.WriteString("\n## Frozen claim assessment\n\n")

	for _, assessment := range r.Assessments {
		fmt.Fprintf(&out, "- `%s`: **%s** — %s", assessment.ID, assessment.Status, assessment.Reason)
		if assessment.QualifyingInstances > 0 {
			fmt.Fprintf(&out, " (qualifying instances: %d", assessment.QualifyingInstances)
			if assessment.Status != "insufficient evidence" {
				fmt.Fprintf(&out, ", mean effect: %+.1f pp, worst instance: %+.1f pp, harmful interference: %.1f%%",
					assessment.MeanEffect*100, assessment.WorstInstanceEffect*100,
					assessment.HarmfulInterference*100)
			}
			out.WriteString(")")
		}

		out.WriteString(".\n")
		claim := assessment.Definition
		fmt.Fprintf(&out, "  - Frozen conditions: %d instances × %d valid pairs; mean effect ≥ %+.0f pp; "+
			"worst instance ≥ %+.0f pp; harmful task interference ≤ %.1f%%; model %s; effort %s; clean Seamark required.\n",
			claim.MinimumInstances, claim.MinimumValidPairsPerInstance, claim.MinimumEffect*100,
			claim.MinimumInstanceEffect*100, claim.MaximumHarmfulInterference*100,
			markdownCode(claim.RequiredModel), markdownCode(claim.RequiredEffort))
	}

	out.WriteString("\n## Interpretation guardrail\n\n")
	out.WriteString("A cohort can validate the harness or support its specific task without establishing the cross-instance product claim. " +
		"Threshold assessment remains insufficient until the committed claim registry's model, effort, clean-build, " +
		"independent-instance, and valid-pair conditions are met.\n")

	return out.String()
}

func rate(numerator, denominator int) string {
	if denominator == 0 {
		return "n/a"
	}

	return fmt.Sprintf("%d/%d (%.0f%%)", numerator, denominator,
		100*float64(numerator)/float64(denominator))
}

func wilson95(successes, trials int) (low, high float64, ok bool) {
	if trials == 0 {
		return 0, 0, false
	}

	const z = 1.959963984540054
	p := float64(successes) / float64(trials)
	zSquared := z * z
	denominator := 1 + zSquared/float64(trials)
	center := (p + zSquared/(2*float64(trials))) / denominator
	margin := z * math.Sqrt((p*(1-p)+zSquared/(4*float64(trials)))/float64(trials)) / denominator

	return max(0, center-margin), min(1, center+margin), true
}

func meanContext(arm ArmReport) string {
	if arm.Valid == 0 || arm.ContextTokens == 0 {
		return "n/a"
	}

	return fmt.Sprintf("%dk", arm.ContextTokens/int64(arm.Valid)/1000)
}

func effectString(cohort CohortReport) string {
	effect, ok := cohort.Effect()
	if !ok || math.IsNaN(effect) {
		return "n/a"
	}

	return fmt.Sprintf("%+.0f pp", effect*100)
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}

	return value[:12] + "…"
}

func tableCell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "|", "\\|").Replace(value)

	return value
}

func valueOrNA(value string) string {
	if value == "" {
		return "n/a"
	}

	return value
}

func markdownCode(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "`", "'").Replace(value)

	return "`" + value + "`"
}

func singleLine(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}
