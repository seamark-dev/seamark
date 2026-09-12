package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// WorkflowArmReport aggregates one arm's valid paired rows of a cohort.
type WorkflowArmReport struct {
	Attempted     int
	Valid         int
	TaskDone      int
	InvariantPass int
	WorkflowProcessCounts
	ContextTokens int64
	CostUSD       float64
}

// WorkflowCohort is one immutable experiment fingerprint of the workflow
// benchmark: rows with different harness, fixture, model, or runtime
// identities are never pooled.
type WorkflowCohort struct {
	Instance         string
	Fingerprint      string
	Task             string
	TaskSHA          string
	Trigger          string
	Companion        string
	Fixture          string
	RequestedModel   string
	Model            string
	SeamarkVersion   string
	SeamarkSHA       string
	AgentVersion     string
	Effort           string
	RuntimeID        string
	MaxBudgetUSD     float64
	ValidPairs       int
	FavorablePairs   int
	UnfavorablePairs int
	TiedPairs        int
	HarmfulPairs     int
	Skills           WorkflowArmReport
	Only             WorkflowArmReport
	// TranscriptDirs lists where the cohort's transcripts live, so a reader
	// can find the evidence behind a row.
	TranscriptDirs []string
}

// shadow expresses the cohort as a lessons cohort, with the skills arm in
// the treatment position, so the effect, its interval, and the claim
// conditions come from the lessons implementation unchanged.
func (c WorkflowCohort) shadow() CohortReport {
	return CohortReport{
		Instance: c.Instance, Fingerprint: c.Fingerprint, RequestedModel: c.RequestedModel,
		Model: c.Model, SeamarkVersion: c.SeamarkVersion, SeamarkSHA: c.SeamarkSHA,
		Effort: c.Effort, ValidPairs: c.ValidPairs, HarmfulPairs: c.HarmfulPairs,
		HookOn:  ArmReport{Attempted: c.Skills.Attempted, Valid: c.Skills.Valid, TaskDone: c.Skills.TaskDone, InvariantPass: c.Skills.InvariantPass},
		HookOff: ArmReport{Attempted: c.Only.Attempted, Valid: c.Only.Valid, TaskDone: c.Only.TaskDone, InvariantPass: c.Only.InvariantPass},
	}
}

// Effect returns the MCP + skills minus MCP-only invariant-pass rate,
// conditional on completing the visible task in each arm.
func (c WorkflowCohort) Effect() (float64, bool) {
	return c.shadow().Effect()
}

// EffectInterval95 returns the lessons report's Wilson-based interval for
// the conditional effect.
func (c WorkflowCohort) EffectInterval95() (low, high float64, ok bool) {
	return c.shadow().EffectInterval95()
}

// WorkflowAssessment says whether the evidence reached a frozen claim's
// floor. Reasons names every failed condition, so a failing claim is
// explained, not just declared.
type WorkflowAssessment struct {
	ID                  string
	Definition          WorkflowClaim
	Status              string
	Reason              string
	Reasons             []string
	QualifyingInstances int
	MeanEffect          float64
	WorstInstanceEffect float64
	HarmfulInterference float64
}

// ActivationInputs names the activation result files and the prompt
// manifest they were measured against. The zero value means no activation
// section.
type ActivationInputs struct {
	Paths   []string
	Prompts ActivationPromptSet
}

// ActivationReport renders one activation experiment against the frozen
// criteria. One experiment means one fingerprint, prompt set, requested
// model, and turn cap; rows from another identity are refused, never pooled.
// Every prompt of the manifest needs a valid session before the criteria are
// assessed: a run that stopped early must read as insufficient, not as a
// pass on the prompts it happened to reach.
type ActivationReport struct {
	Inputs         []ReportInput
	Fingerprint    string
	PromptSetSHA   string
	RequestedModel string
	Model          string
	MaxTurns       int
	Rows           []ActivationRow
	// Missing lists the manifest prompts without a valid session, in
	// manifest order.
	Missing []string
	Stats   ActivationStats
	Status  string
	Reasons []string
}

// WorkflowReport is a deterministic summary of explicit raw inputs.
type WorkflowReport struct {
	ResultSchemaVersion int
	ClaimSchemaVersion  int
	EvidenceFrom        string
	EvidenceTo          string
	Inputs              []ReportInput
	Cohorts             []WorkflowCohort
	Assessments         []WorkflowAssessment
	Activation          *ActivationReport
}

// BuildWorkflowReport strictly reads the workflow result files and, when
// given, the activation result files against their prompt manifest.
// Malformed or semantically invalid rows fail the report instead of
// disappearing.
func BuildWorkflowReport(paths []string, activation ActivationInputs, registry WorkflowClaimRegistry) (WorkflowReport, error) {
	if len(paths) == 0 {
		return WorkflowReport{}, fmt.Errorf("no result files supplied")
	}

	if err := registry.Validate(); err != nil {
		return WorkflowReport{}, err
	}

	paths = slices.Clone(paths)
	sort.Strings(paths)

	report := WorkflowReport{ClaimSchemaVersion: registry.SchemaVersion}
	var allRows []WorkflowRow
	var evidenceFrom, evidenceTo time.Time
	seenDigests := make(map[string]string, len(paths))

	for i, path := range paths {
		if i > 0 && path == paths[i-1] {
			return WorkflowReport{}, fmt.Errorf("result file supplied more than once: %s", path)
		}

		rows, digest, err := ReadWorkflowRowsStrict(path)
		if err != nil {
			return WorkflowReport{}, err
		}

		if previous, exists := seenDigests[digest]; exists {
			return WorkflowReport{}, fmt.Errorf("duplicate evidence content: %s and %s have SHA-256 %s", previous, path, digest)
		}

		seenDigests[digest] = path
		report.Inputs = append(report.Inputs, ReportInput{Path: filepath.ToSlash(path), SHA256: digest, Rows: len(rows)})

		for _, row := range rows {
			if report.ResultSchemaVersion == 0 {
				report.ResultSchemaVersion = row.SchemaVersion
			} else if row.SchemaVersion != report.ResultSchemaVersion {
				return WorkflowReport{}, fmt.Errorf("mixed result schema versions: v%d and v%d",
					report.ResultSchemaVersion, row.SchemaVersion)
			}

			ts, _ := time.Parse(time.RFC3339, row.TS) // ValidateWorkflowRow already checked it.

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

	cohorts, err := buildWorkflowCohorts(allRows)
	if err != nil {
		return WorkflowReport{}, err
	}

	report.Cohorts = cohorts
	report.Assessments = assessWorkflowClaims(registry.Claims, cohorts)

	if len(activation.Paths) > 0 {
		section, err := buildActivationReport(activation, registry.Activation)
		if err != nil {
			return WorkflowReport{}, err
		}

		report.Activation = &section
	}

	return report, nil
}

// ReadWorkflowRowsStrict reads one workflow results file the way the report
// does: every non-empty line must be exactly one valid row. It returns the
// rows and the file's SHA-256.
func ReadWorkflowRowsStrict(path string) ([]WorkflowRow, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	digest := sha256.Sum256(data)

	var rows []WorkflowRow
	lineNumber := 0

	for line := range strings.SplitSeq(string(data), "\n") {
		lineNumber++
		if strings.TrimSpace(line) == "" {
			continue
		}

		var row WorkflowRow
		if err := decodeJSONLRow(line, &row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		if err := ValidateWorkflowRow(row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil, "", fmt.Errorf("%s: no result rows", path)
	}

	return rows, hex.EncodeToString(digest[:]), nil
}

type workflowIdentity struct {
	taskSHA, trigger, companion, fixture, requestedModel string
	seamarkVersion, seamarkSHA, agentVersion, effort     string
	runtimeID                                            string
	maxBudgetUSD                                         float64
}

func workflowReportIdentity(row WorkflowRow) workflowIdentity {
	return workflowIdentity{
		taskSHA: row.TaskSHA, trigger: row.Trigger, companion: row.Companion, fixture: row.Fixture,
		requestedModel: row.RequestedModel, seamarkVersion: row.SeamarkVersion, seamarkSHA: row.SeamarkSHA,
		agentVersion: row.AgentVersion, effort: row.Effort, runtimeID: row.RuntimeID,
		maxBudgetUSD: row.MaxBudgetUSD,
	}
}

type workflowPair struct {
	skills, only *WorkflowRow
}

func buildWorkflowCohorts(rows []WorkflowRow) ([]WorkflowCohort, error) {
	grouped := make(map[cohortKey][]WorkflowRow)
	for _, row := range rows {
		key := cohortKey{instance: row.Instance, fingerprint: row.Fingerprint}
		grouped[key] = append(grouped[key], row)
	}

	cohorts := make([]WorkflowCohort, 0, len(grouped))

	for key, cohortRows := range grouped {
		first := cohortRows[0]
		cohort := WorkflowCohort{
			Instance: key.instance, Fingerprint: key.fingerprint,
			TaskSHA: first.TaskSHA, Trigger: first.Trigger, Companion: first.Companion,
			Fixture: first.Fixture, RequestedModel: first.RequestedModel,
			SeamarkVersion: first.SeamarkVersion, SeamarkSHA: first.SeamarkSHA,
			AgentVersion: first.AgentVersion, Effort: first.Effort, RuntimeID: first.RuntimeID,
			MaxBudgetUSD: first.MaxBudgetUSD,
		}

		if instance, err := WorkflowInstanceByID(first.Instance); err == nil && instance.TaskSHA() == first.TaskSHA {
			cohort.Task = instance.Task
		}

		identity := workflowReportIdentity(first)
		pairs := make(map[string]workflowPair)
		transcriptDirs := map[string]bool{}

		for i := range cohortRows {
			row := &cohortRows[i]
			if workflowReportIdentity(*row) != identity {
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

			if row.Transcript != "" {
				transcriptDirs[filepath.ToSlash(filepath.Dir(row.Transcript))] = true
			}

			pairKey := fmt.Sprintf("%s/%d", row.RunID, row.Trial)
			pair := pairs[pairKey]

			switch row.Arm {
			case ArmMCPSkills:
				if pair.skills != nil {
					return nil, fmt.Errorf("duplicate result for %s/%s trial %s arm %s",
						key.instance, shortHash(key.fingerprint), pairKey, row.Arm)
				}

				accumulateWorkflowArm(&cohort.Skills, *row)
				pair.skills = row
			case ArmMCPOnly:
				if pair.only != nil {
					return nil, fmt.Errorf("duplicate result for %s/%s trial %s arm %s",
						key.instance, shortHash(key.fingerprint), pairKey, row.Arm)
				}

				accumulateWorkflowArm(&cohort.Only, *row)
				pair.only = row
			default:
				return nil, fmt.Errorf("unknown arm %q", row.Arm)
			}

			pairs[pairKey] = pair
		}

		for _, pair := range pairs {
			if pair.skills == nil || pair.only == nil || !pair.skills.Valid || !pair.skills.PairValid ||
				!pair.only.Valid || !pair.only.PairValid {
				continue
			}

			cohort.ValidPairs++
			skillsSuccess := pair.skills.TaskDone && pair.skills.Avoided
			onlySuccess := pair.only.TaskDone && pair.only.Avoided

			switch {
			case skillsSuccess && !onlySuccess:
				cohort.FavorablePairs++
			case !skillsSuccess && onlySuccess:
				cohort.UnfavorablePairs++
			default:
				cohort.TiedPairs++
			}

			if pair.only.TaskDone && !pair.skills.TaskDone {
				cohort.HarmfulPairs++
			}
		}

		for dir := range transcriptDirs {
			cohort.TranscriptDirs = append(cohort.TranscriptDirs, dir)
		}

		sort.Strings(cohort.TranscriptDirs)
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

func accumulateWorkflowArm(arm *WorkflowArmReport, row WorkflowRow) {
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

	arm.add(row)
	arm.ContextTokens += row.ContextTokens
	arm.CostUSD += row.CostUSD
}

// assessWorkflowClaims applies the lessons assessment rule to the workflow
// cohorts through their shadows, then names every failed threshold.
func assessWorkflowClaims(claims []WorkflowClaim, cohorts []WorkflowCohort) []WorkflowAssessment {
	shadows := make([]CohortReport, 0, len(cohorts))
	for _, cohort := range cohorts {
		shadows = append(shadows, cohort.shadow())
	}

	assessments := make([]WorkflowAssessment, 0, len(claims))

	for _, claim := range claims {
		shared := assessClaims([]Claim{claim.shadow()}, shadows)[0]
		assessment := WorkflowAssessment{
			ID: claim.ID, Definition: claim, Status: shared.Status, Reason: shared.Reason,
			QualifyingInstances: shared.QualifyingInstances, MeanEffect: shared.MeanEffect,
			WorstInstanceEffect: shared.WorstInstanceEffect, HarmfulInterference: shared.HarmfulInterference,
		}

		switch shared.Status {
		case "does not pass frozen threshold":
			assessment.Reasons = failedThresholds(claim, shared)
		case "insufficient evidence":
			assessment.Reasons = []string{shared.Reason}
		}

		assessments = append(assessments, assessment)
	}

	return assessments
}

// failedThresholds lists each threshold the numbers missed, with both sides.
func failedThresholds(claim WorkflowClaim, assessment ClaimAssessment) []string {
	var reasons []string

	if assessment.MeanEffect < claim.MinimumEffect {
		reasons = append(reasons, fmt.Sprintf("mean effect %+.1f pp is below the minimum %+.0f pp",
			assessment.MeanEffect*100, claim.MinimumEffect*100))
	}

	if assessment.WorstInstanceEffect < claim.MinimumInstanceEffect {
		reasons = append(reasons, fmt.Sprintf("worst instance effect %+.1f pp is below the minimum %+.0f pp",
			assessment.WorstInstanceEffect*100, claim.MinimumInstanceEffect*100))
	}

	if assessment.HarmfulInterference > claim.MaximumHarmfulInterference {
		reasons = append(reasons, fmt.Sprintf("harmful task interference %.1f%% is above the maximum %.1f%%",
			assessment.HarmfulInterference*100, claim.MaximumHarmfulInterference*100))
	}

	return reasons
}

func buildActivationReport(inputs ActivationInputs, criteria ActivationCriteria) (ActivationReport, error) {
	if err := inputs.Prompts.Validate(); err != nil {
		return ActivationReport{}, fmt.Errorf("activation prompt manifest: %w", err)
	}

	manifestSHA, err := inputs.Prompts.SHA256()
	if err != nil {
		return ActivationReport{}, err
	}

	paths := slices.Clone(inputs.Paths)
	sort.Strings(paths)

	report := ActivationReport{}
	seenDigests := make(map[string]string, len(paths))

	type identity struct {
		fingerprint, promptSet, requestedModel string
		maxTurns                               int
	}

	var first identity
	seenIdentity := false

	for i, path := range paths {
		if i > 0 && path == paths[i-1] {
			return ActivationReport{}, fmt.Errorf("activation file supplied more than once: %s", path)
		}

		rows, digest, err := ReadActivationRows(path)
		if err != nil {
			return ActivationReport{}, err
		}

		if previous, exists := seenDigests[digest]; exists {
			return ActivationReport{}, fmt.Errorf("duplicate activation content: %s and %s have SHA-256 %s", previous, path, digest)
		}

		seenDigests[digest] = path
		report.Inputs = append(report.Inputs, ReportInput{Path: filepath.ToSlash(path), SHA256: digest, Rows: len(rows)})

		for _, row := range rows {
			id := identity{row.Fingerprint, row.PromptSetSHA, row.RequestedModel, row.MaxTurns}

			if !seenIdentity {
				first, seenIdentity = id, true
				report.Fingerprint, report.PromptSetSHA = row.Fingerprint, row.PromptSetSHA
				report.RequestedModel, report.MaxTurns = row.RequestedModel, row.MaxTurns
			} else if id != first {
				return ActivationReport{}, fmt.Errorf(
					"activation rows mix experiment identities (fingerprint %s, prompt set %s, model %q, max turns %d and fingerprint %s, prompt set %s, model %q, max turns %d); select one result set",
					shortHash(first.fingerprint), shortHash(first.promptSet), first.requestedModel, first.maxTurns,
					shortHash(id.fingerprint), shortHash(id.promptSet), id.requestedModel, id.maxTurns)
			}

			if row.Valid && row.Model != "" {
				if report.Model != "" && row.Model != report.Model {
					return ActivationReport{}, fmt.Errorf("valid activation rows reuse fingerprint %s with conflicting observed models",
						shortHash(row.Fingerprint))
				}

				report.Model = row.Model
			}
		}

		report.Rows = append(report.Rows, rows...)
	}

	if report.PromptSetSHA != manifestSHA {
		return ActivationReport{}, fmt.Errorf("activation rows were measured against prompt set %s, not the supplied manifest %s",
			shortHash(report.PromptSetSHA), shortHash(manifestSHA))
	}

	report.Missing, err = uncoveredPrompts(inputs.Prompts, report.Rows)
	if err != nil {
		return ActivationReport{}, err
	}

	sort.SliceStable(report.Rows, func(i, j int) bool {
		if report.Rows[i].PromptID == report.Rows[j].PromptID {
			return report.Rows[i].TS < report.Rows[j].TS
		}

		return report.Rows[i].PromptID < report.Rows[j].PromptID
	})

	report.Stats = ActivationStatsFor(report.Rows)
	report.Status, report.Reasons = assessActivation(criteria, report.Stats, report.Missing)

	return report, nil
}

// uncoveredPrompts checks the rows against the manifest and returns the
// prompts without a valid session. A row for a prompt the manifest does not
// have, or with another expectation than the manifest's, is corrupt
// evidence and fails the report.
func uncoveredPrompts(prompts ActivationPromptSet, rows []ActivationRow) ([]string, error) {
	expected := make(map[string]string, len(prompts.Prompts))
	for _, prompt := range prompts.Prompts {
		expected[prompt.ID] = prompt.Expect
	}

	covered := make(map[string]bool, len(prompts.Prompts))

	for _, row := range rows {
		want, known := expected[row.PromptID]
		switch {
		case !known:
			return nil, fmt.Errorf("activation row names prompt %q, which is not in the manifest", row.PromptID)
		case row.Expected != want:
			return nil, fmt.Errorf("activation row for prompt %q expects %q, the manifest says %q",
				row.PromptID, row.Expected, want)
		}

		if row.Valid {
			covered[row.PromptID] = true
		}
	}

	var missing []string
	for _, prompt := range prompts.Prompts {
		if !covered[prompt.ID] {
			missing = append(missing, prompt.ID)
		}
	}

	return missing, nil
}

// assessActivation compares the measured rates with the frozen criteria. A
// criterion without a measurement, or a manifest prompt without a valid
// session, is insufficient evidence, never a pass.
func assessActivation(criteria ActivationCriteria, stats ActivationStats, missing []string) (status string, reasons []string) {
	insufficient := len(missing) > 0
	if insufficient {
		reasons = append(reasons, "no valid session for prompt(s) "+strings.Join(missing, ", "))
	}

	names := make([]string, 0, len(criteria.MinimumRecall))
	for name := range criteria.MinimumRecall {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		minimum := criteria.MinimumRecall[name]
		recall, ok := stats.Recall[name].Value()

		switch {
		case !ok:
			insufficient = true
			reasons = append(reasons, "no valid should-activate session for "+name)
		case recall < minimum:
			reasons = append(reasons, fmt.Sprintf("recall for %s is %s, below the minimum %.0f%%",
				name, rateString(stats.Recall[name]), minimum*100))
		}
	}

	falseRate, ok := stats.FalseActivation.Value()
	switch {
	case !ok:
		insufficient = true
		reasons = append(reasons, "no valid should-not-activate session")
	case falseRate > criteria.MaximumFalseActivation:
		reasons = append(reasons, fmt.Sprintf("false activation is %s, above the maximum %.0f%%",
			rateString(stats.FalseActivation), criteria.MaximumFalseActivation*100))
	}

	switch {
	case insufficient:
		return "insufficient evidence", reasons
	case len(reasons) > 0:
		return "does not pass frozen criteria", reasons
	default:
		return "passes frozen criteria", nil
	}
}

// Markdown renders a deterministic, reviewable report. Percentages are point
// estimates; the paired discordance counts stay visible so tiny samples do
// not look conclusive.
func (r WorkflowReport) Markdown() string {
	var out strings.Builder
	out.WriteString("# Skills workflow benchmark report\n\n")
	fmt.Fprintf(&out, "Result schema: v%d; claim schema: v%d; evidence window: %s to %s.\n\n",
		r.ResultSchemaVersion, r.ClaimSchemaVersion, r.EvidenceFrom, r.EvidenceTo)
	out.WriteString("The question: does MCP + skills change what a headless agent does on a companion-file task, compared with the same MCP server and approvals alone?\n\n")

	out.WriteString("## Raw inputs\n\n")
	out.WriteString("| File | Rows | SHA-256 |\n|---|---:|---|\n")

	for _, input := range r.Inputs {
		fmt.Fprintf(&out, "| %s | %d | `%s` |\n", tableCell(input.Path), input.Rows, input.SHA256)
	}

	out.WriteString("\n## Immutable cohorts\n\n")
	out.WriteString("Rows are pooled only when their full experiment fingerprint matches. " +
		"Invariant rates are conditional on completing the visible task.\n\n")
	out.WriteString("| Instance | Fingerprint | Model | Valid pairs | MCP + skills invariant | MCP-only invariant | Effect | Task completion skills/only | Mean context skills/only | Cost skills/only |\n")
	out.WriteString("|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")

	for _, cohort := range r.Cohorts {
		fmt.Fprintf(&out, "| %s | `%s` | %s | %d | %s | %s | %s | %s / %s | %s / %s | $%.2f / $%.2f |\n",
			tableCell(cohort.Instance), shortHash(cohort.Fingerprint), tableCell(valueOrNA(cohort.Model)), cohort.ValidPairs,
			rate(cohort.Skills.InvariantPass, cohort.Skills.TaskDone),
			rate(cohort.Only.InvariantPass, cohort.Only.TaskDone),
			workflowEffectString(cohort),
			rate(cohort.Skills.TaskDone, cohort.Skills.Valid),
			rate(cohort.Only.TaskDone, cohort.Only.Valid),
			meanWorkflowContext(cohort.Skills), meanWorkflowContext(cohort.Only),
			cohort.Skills.CostUSD, cohort.Only.CostUSD,
		)
	}

	out.WriteString("\n### Process rates\n\n")
	out.WriteString("Rates are over valid paired trials per arm. They are recorded beside the claim and never decide it.\n\n")
	out.WriteString("| Instance | Arm | change_set before first edit | Companion named | Named by check | Companion opened | why followed companion | check after last edit | Seamark calls per trial | Activations |\n")
	out.WriteString("|---|---|---:|---:|---:|---:|---:|---:|---:|---|\n")

	for _, cohort := range r.Cohorts {
		for _, entry := range []struct {
			arm    WorkflowArm
			report WorkflowArmReport
		}{{ArmMCPSkills, cohort.Skills}, {ArmMCPOnly, cohort.Only}} {
			fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				tableCell(cohort.Instance), entry.arm,
				rate(entry.report.ChangeSetFirst, entry.report.Valid),
				rate(entry.report.CompanionNamed, entry.report.Valid),
				rate(entry.report.NamedByCheck, entry.report.Valid),
				rate(entry.report.Opened, entry.report.Valid),
				rate(entry.report.WhyFollowed, entry.report.Valid),
				rate(entry.report.CheckLast, entry.report.Valid),
				perTrial(entry.report.SeamarkCalls, entry.report.Valid),
				tableCell(activationSummary(entry.report.Activations)))
		}
	}

	out.WriteString("\n### Exact cohort identities\n\n")

	for _, cohort := range r.Cohorts {
		fmt.Fprintf(&out, "- %s / %s\n", markdownCode(cohort.Instance), markdownCode(cohort.Fingerprint))
		fmt.Fprintf(&out, "  - Task %s; fixture %s; trigger %s; companion %s.\n",
			markdownCode(cohort.TaskSHA), markdownCode(cohort.Fixture), markdownCode(cohort.Trigger), markdownCode(cohort.Companion))

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

		if len(cohort.TranscriptDirs) > 0 {
			dirs := make([]string, 0, len(cohort.TranscriptDirs))
			for _, dir := range cohort.TranscriptDirs {
				dirs = append(dirs, markdownCode(dir))
			}

			fmt.Fprintf(&out, "  - Transcripts: %s.\n", strings.Join(dirs, ", "))
		}
	}

	out.WriteString("\n### Paired details\n\n")

	for i, cohort := range r.Cohorts {
		if i > 0 {
			out.WriteString("\n")
		}

		fmt.Fprintf(&out, "Paired directions for `%s`/`%s`: %d favorable, %d unfavorable, %d tied; %d harmful task regressions.\n",
			cohort.Instance, shortHash(cohort.Fingerprint), cohort.FavorablePairs,
			cohort.UnfavorablePairs, cohort.TiedPairs, cohort.HarmfulPairs)

		if invalid := cohort.Skills.Attempted + cohort.Only.Attempted -
			cohort.Skills.Valid - cohort.Only.Valid; invalid > 0 {
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
					assessment.MeanEffect*100, assessment.WorstInstanceEffect*100, assessment.HarmfulInterference*100)
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
		fmt.Fprintf(&out, "  - Comparison: %s within each instance.\n", markdownCode(claim.Comparison))

		if len(claim.ProcessMetrics) > 0 {
			fmt.Fprintf(&out, "  - Recorded process metrics (not gating): %s.\n", strings.Join(claim.ProcessMetrics, ", "))
		}

		if assessment.Status != "passes frozen threshold" && len(assessment.Reasons) > 0 {
			fmt.Fprintf(&out, "  - Why not: %s.\n", strings.Join(assessment.Reasons, "; "))
		}
	}

	if r.Activation != nil {
		r.Activation.render(&out)
	}

	out.WriteString("\n## Interpretation guardrail\n\n")
	out.WriteString("A cohort can validate the harness or support its specific task without establishing a broader product claim. " +
		"A passing assessment supports only the committed claim under the exact model, effort, clean-build, " +
		"instance, and valid-pair conditions; it does not establish external validity. An insufficient assessment " +
		"must not be promoted to a product claim. The synthetic fixtures were built to carry the companion pair in " +
		"their history, so an effect here says the skills use evidence that exists, not that every repository has it.\n")

	return out.String()
}

func (a ActivationReport) render(out *strings.Builder) {
	out.WriteString("\n## Activation evaluation\n\n")
	out.WriteString("One fresh skills-arm session per prompt. Recall is measured on the should-activate prompts of each skill; " +
		"false activation on the prompts no skill should answer.\n\n")
	out.WriteString("| File | Rows | SHA-256 |\n|---|---:|---|\n")

	for _, input := range a.Inputs {
		fmt.Fprintf(out, "| %s | %d | `%s` |\n", tableCell(input.Path), input.Rows, input.SHA256)
	}

	fmt.Fprintf(out, "\nIdentity: fingerprint %s; prompt set %s; model requested %s, observed %s; turn cap %s. Valid sessions: %d; invalid: %d.\n\n",
		markdownCode(a.Fingerprint), markdownCode(a.PromptSetSHA),
		markdownCode(valueOrNA(a.RequestedModel)), markdownCode(valueOrNA(a.Model)),
		turnCap(a.MaxTurns), a.Stats.Valid, a.Stats.Invalid)

	if len(a.Missing) > 0 {
		codes := make([]string, 0, len(a.Missing))
		for _, id := range a.Missing {
			codes = append(codes, markdownCode(id))
		}

		fmt.Fprintf(out, "Prompts without a valid session: %s. The run is incomplete, so the criteria are not assessed.\n\n",
			strings.Join(codes, ", "))
	}

	for _, line := range a.Stats.Lines()[1:] {
		fmt.Fprintf(out, "- %s\n", line)
	}

	fmt.Fprintf(out, "\nFrozen criteria: **%s**", a.Status)
	if len(a.Reasons) > 0 {
		fmt.Fprintf(out, " — %s", strings.Join(a.Reasons, "; "))
	}

	out.WriteString(".\n\n")
	out.WriteString("| Prompt | Expected | Activated | Hit | Valid | Turns | Cost |\n|---|---|---|---|---|---:|---:|\n")

	for _, row := range a.Rows {
		valid := "yes"
		if !row.Valid {
			valid = "no: " + tableCell(row.InvalidReason)
		}

		fmt.Fprintf(out, "| %s | %s | %s | %v | %s | %d | $%.2f |\n",
			tableCell(row.PromptID), tableCell(row.Expected), tableCell(activationList(row.Activated)),
			row.Hit, valid, row.Turns, row.CostUSD)
	}
}

func turnCap(maxTurns int) string {
	if maxTurns == 0 {
		return "the agent's default"
	}

	return fmt.Sprintf("%d turns", maxTurns)
}

func workflowEffectString(cohort WorkflowCohort) string {
	effect, ok := cohort.Effect()
	if !ok {
		return "n/a"
	}

	return fmt.Sprintf("%+.0f pp", effect*100)
}

func meanWorkflowContext(arm WorkflowArmReport) string {
	if arm.Valid == 0 || arm.ContextTokens == 0 {
		return "n/a"
	}

	return fmt.Sprintf("%dk", arm.ContextTokens/int64(arm.Valid)/1000)
}

func perTrial(total, trials int) string {
	if trials == 0 {
		return "n/a"
	}

	return fmt.Sprintf("%.1f", float64(total)/float64(trials))
}
