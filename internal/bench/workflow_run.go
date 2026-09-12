package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/skills"
)

// WorkflowArm is one condition of the skills workflow experiment. Both arms
// connect the seamark MCP server and approve its five tools; they differ in
// exactly one thing: whether the three agent skills are installed and the
// Skill tool is exposed.
type WorkflowArm string

const (
	// ArmMCPOnly connects the MCP server and approves its tools. No skill
	// is installed and the Skill tool is not exposed.
	ArmMCPOnly WorkflowArm = "mcp-only"
	// ArmMCPSkills adds the three managed skills under .claude/skills, the
	// Skill tool, and the Skill allow rules.
	ArmMCPSkills WorkflowArm = "mcp-skills"
)

// WorkflowConfig configures one workflow run. It carries the operator fields
// of RunConfig without the lessons hook fields, because no arm installs a
// lesson or a hook.
type WorkflowConfig struct {
	Trials   int
	Arms     []WorkflowArm    // arms to run; nil means both
	Instance WorkflowInstance // required; there is no default instance
	// AgentArgv is the agent command per arm. The arms differ in the tools
	// they expose, so each arm has its own command line. The runner appends
	// the trial's MCP configuration and then the task prompt.
	AgentArgv     map[WorkflowArm][]string
	SeamarkBin    string        // absolute path to the seamark binary (MCP server + index)
	Timeout       time.Duration // per-trial agent timeout; 0 means 10 minutes
	Out           string        // results JSONL path, appended one row per trial
	WorkDir       string        // parent for trial dirs; "" means a fresh temp dir
	Keep          bool          // keep trial dirs after judging, for inspection
	TranscriptDir string        // saves each trial's raw agent output; empty disables
	// PrepareIndex copies the seamark binary into the trial and indexes the
	// fixture, so the MCP server answers from a ready index. Hermetic tests
	// turn it off and keep an inert fake path.
	PrepareIndex bool
	Version      string // seamark version stamped into rows
	SeamarkSHA   string // exact binary digest stamped into rows
	AgentVersion string // agent CLI version stamped into rows
	Model        string // exact requested primary model; empty for custom agents
	Effort       string // requested effort level
	MaxBudgetUSD float64
	RuntimeID    string // sandbox/toolchain identity
	Fingerprint  string // immutable instance + runtime configuration hash
	// RunID groups rows and makes transcript names unique across concurrent
	// invocations. Empty asks RunWorkflow to generate a random ID.
	RunID string
	// MaxTurns caps activation sessions (RunActivation) with --max-turns.
	// Zero leaves the agent's default. Workflow trials never use it.
	MaxTurns int
	// RequireStructuredResult and RequireExpectedInit are true for the
	// managed Claude adapter. The second is the counterpart of the lessons
	// harness's clean-init rule: here the init record must show exactly the
	// seamark MCP server, the arm's tool set, and, in the skills arm, exactly
	// the three shipped skills.
	RequireStructuredResult bool
	RequireExpectedInit     bool
	Log                     func(string, ...any) // progress lines; nil silences
}

// SameAgentArgv gives both arms the same agent command, for custom adapters
// and stub agents that do not read --tools.
func SameAgentArgv(argv []string) map[WorkflowArm][]string {
	return map[WorkflowArm][]string{
		ArmMCPOnly:   slices.Clone(argv),
		ArmMCPSkills: slices.Clone(argv),
	}
}

// WorkflowTools lists the tools an arm exposes to the agent, in --tools
// order. The list is the expected init tool set as well: a row whose init
// record shows any other set is invalid.
func WorkflowTools(arm WorkflowArm) []string {
	return append(slices.Clone(baseAgentTools), conditionTools(arm)...)
}

// baseAgentTools are the tools both arms expose beside the condition: the
// agent needs them to read and edit the fixture at all.
var baseAgentTools = []string{"Read", "Edit", "Write", "Bash"}

// conditionTools lists the tools that make up an arm's condition: the
// seamark MCP tools for both arms and the Skill tool for the skills arm.
// The tool list and the denied-tool rule both read it, so the two cannot
// name different sets.
func conditionTools(arm WorkflowArm) []string {
	var tools []string

	for _, tool := range approve.Tools {
		tools = append(tools, approve.ToolRule(approve.ClaudeServer, tool))
	}

	if arm == ArmMCPSkills {
		tools = append(tools, "Skill")
	}

	return tools
}

// MCPServerState is one MCP server as the agent's init record reports it.
type MCPServerState struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
}

// AgentUsage is the provider-reported identity and usage of one session.
// The fields mirror the lessons Row because the same parser fills them.
type AgentUsage struct {
	RequestedModel      string                `json:"requested_model,omitempty"`
	Model               string                `json:"model,omitempty"`
	ModelUsage          map[string]ModelUsage `json:"model_usage,omitempty"`
	InputTokens         int64                 `json:"input_tokens,omitempty"`
	CacheReadTokens     int64                 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int64                 `json:"cache_creation_input_tokens,omitempty"`
	ContextTokens       int64                 `json:"context_tokens,omitempty"`
	OutputTokens        int64                 `json:"output_tokens,omitempty"`
	Turns               int                   `json:"turns,omitempty"`
	PermissionDenials   int                   `json:"permission_denials,omitempty"`
	CostUSD             float64               `json:"cost_usd,omitempty"`
	DurationMS          int64                 `json:"duration_ms,omitempty"`
	AgentExit           int                   `json:"agent_exit"`
	TimedOut            bool                  `json:"timed_out,omitempty"`
	AgentError          bool                  `json:"agent_error,omitempty"`
}

// WorkflowRow is one workflow trial as appended to the JSONL file. Rows are
// self-contained: identity, validity, the init facts, the tool trace, the
// verdicts, and the cost travel together.
type WorkflowRow struct {
	SchemaVersion int         `json:"schema_version"`
	TS            string      `json:"ts"`
	RunID         string      `json:"run_id"`
	Instance      string      `json:"instance"`
	TaskSHA       string      `json:"task_sha256"`
	Trigger       string      `json:"trigger"`
	Companion     string      `json:"companion"`
	Arm           WorkflowArm `json:"arm"`
	Trial         int         `json:"trial"`
	// Fixture is the generated repo's full HEAD commit, so rows from
	// different fixture versions are never pooled as one series.
	Fixture     string `json:"fixture"`
	Fingerprint string `json:"fingerprint"`

	// Valid says the agent session and the arm's wiring were delivered.
	// PairValid additionally says every arm in this trial number was valid.
	// Only rows satisfying both enter effect tallies.
	Valid                 bool   `json:"valid"`
	PairValid             bool   `json:"pair_valid"`
	InvalidReason         string `json:"invalid_reason,omitempty"`
	InfrastructureFailure bool   `json:"infrastructure_failure,omitempty"`

	// The init record proves what the agent had: the arm's tool set, the
	// seamark MCP server, the skills, and no plugins.
	InitSeen   bool             `json:"init_seen,omitempty"`
	ResultSeen bool             `json:"result_seen,omitempty"`
	Tools      []string         `json:"tools,omitempty"`
	MCPServers []MCPServerState `json:"mcp_servers,omitempty"`
	Skills     []string         `json:"skills,omitempty"`
	Plugins    []string         `json:"plugins,omitempty"`
	// DeniedTools lists the tools the agent asked for and was refused, from
	// the result record. A refused seamark tool or Skill tool means the arm's
	// approvals were not in effect, so the row is invalid.
	DeniedTools []string `json:"denied_tools,omitempty"`

	WorkflowTrace

	TaskDone bool   `json:"task_pass"`
	Avoided  bool   `json:"invariant_pass"`
	Notes    string `json:"notes,omitempty"`
	// Checks are public repository-local validation commands. The hidden
	// task and invariant judges are TaskDone and Avoided above.
	ChecksPass bool          `json:"checks_pass"`
	Checks     []CheckResult `json:"checks,omitempty"`

	AgentUsage

	SeamarkVersion string  `json:"seamark_version,omitempty"`
	SeamarkSHA     string  `json:"seamark_sha256,omitempty"`
	AgentVersion   string  `json:"agent_version,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	MaxBudgetUSD   float64 `json:"max_budget_usd,omitempty"`
	RuntimeID      string  `json:"runtime_id,omitempty"`

	// Transcript, stderr, and patch keep the audit record with digests, so a
	// verdict stays checkable after the trial dir is deleted.
	Transcript    string `json:"transcript,omitempty"`
	TranscriptSHA string `json:"transcript_sha256,omitempty"`
	StderrLog     string `json:"stderr,omitempty"`
	StderrSHA     string `json:"stderr_sha256,omitempty"`
	Patch         string `json:"patch,omitempty"`
	PatchSHA      string `json:"patch_sha256,omitempty"`
}

// WorkflowTally is one arm's aggregate over valid paired rows.
type WorkflowTally struct {
	Ran       int
	Invalid   int
	Completed int // trials where the task was done at all
	Avoided   int // completed trials where the owner invariant passed
	WorkflowProcessCounts
	MeanInput int64
	CostUSD   float64
}

// WorkflowProcessCounts are the per-arm counts the run summary and the
// report both accumulate over valid paired rows: the process rates the
// experiment records beside the claim, the Seamark call volume, and the
// skill activations. One type keeps the two tallies from drifting apart.
type WorkflowProcessCounts struct {
	ChangeSetFirst int // change_set ran before the first edit
	CompanionNamed int // change_set named the companion
	NamedByCheck   int // check named the companion the diff left out
	Opened         int // the agent opened the companion after it was named
	WhyFollowed    int // why followed the named companion
	CheckLast      int // check ran after the last edit
	SeamarkCalls   int
	Activations    map[string]int
}

// add counts one valid paired row.
func (c *WorkflowProcessCounts) add(row WorkflowRow) {
	if row.ChangeSetBeforeFirstEdit {
		c.ChangeSetFirst++
	}

	if row.CompanionNamedByChangeSet {
		c.CompanionNamed++
	}

	if row.CompanionNamedByCheck {
		c.NamedByCheck++
	}

	if row.CompanionOpenedAfterNamed {
		c.Opened++
	}

	if row.WhyFollowedCompanion {
		c.WhyFollowed++
	}

	if row.CheckAfterLastEdit {
		c.CheckLast++
	}

	c.SeamarkCalls += row.SeamarkCalls

	if len(row.Activations) > 0 && c.Activations == nil {
		c.Activations = map[string]int{}
	}

	for _, name := range row.Activations {
		c.Activations[name]++
	}
}

// WorkflowSummary is the whole run's outcome, per arm.
type WorkflowSummary struct {
	Rows          []WorkflowRow
	ByArm         map[WorkflowArm]WorkflowTally
	Instance      string
	StoppedReason string
}

// Lines renders the summary as raw counts per arm: outcomes, the process
// rates the experiment is about, activations, and cost.
func (s WorkflowSummary) Lines() []string {
	withSkills, only := s.ByArm[ArmMCPSkills], s.ByArm[ArmMCPOnly]
	instance := s.Instance
	if instance == "" {
		instance = "unknown instance"
	}

	out := []string{fmt.Sprintf(
		"%s — mcp-skills: %d/%d avoided (%d/%d completed); mcp-only: %d/%d avoided (%d/%d completed)",
		instance, withSkills.Avoided, withSkills.Ran, withSkills.Completed, withSkills.Ran,
		only.Avoided, only.Ran, only.Completed, only.Ran,
	)}

	for _, arm := range []WorkflowArm{ArmMCPSkills, ArmMCPOnly} {
		t := s.ByArm[arm]
		if t.Ran == 0 {
			continue
		}

		out = append(out, fmt.Sprintf(
			"%s process — change_set before first edit %d/%d, companion named %d/%d, named by check %d/%d, companion opened %d/%d, why followed %d/%d, check after last edit %d/%d, %d seamark calls",
			arm, t.ChangeSetFirst, t.Ran, t.CompanionNamed, t.Ran, t.NamedByCheck, t.Ran, t.Opened, t.Ran, t.WhyFollowed, t.Ran, t.CheckLast, t.Ran, t.SeamarkCalls,
		))
	}

	if withSkills.Ran > 0 {
		out = append(out, "mcp-skills activations — "+activationSummary(withSkills.Activations))
	}

	if withSkills.MeanInput > 0 && only.MeanInput > 0 {
		out = append(out, fmt.Sprintf(
			"context processed — mcp-skills mean %d vs mcp-only %d (%+d per trial)",
			withSkills.MeanInput, only.MeanInput, withSkills.MeanInput-only.MeanInput,
		))
	}

	// The spend is the other half of the trade the skills make; the report
	// prints it per cohort, so the run summary prints it per arm too.
	if withSkills.CostUSD > 0 || only.CostUSD > 0 {
		out = append(out, fmt.Sprintf(
			"cost — mcp-skills $%.2f vs mcp-only $%.2f over valid paired trials",
			withSkills.CostUSD, only.CostUSD,
		))
	}

	for _, arm := range []WorkflowArm{ArmMCPSkills, ArmMCPOnly} {
		if t := s.ByArm[arm]; t.Invalid > 0 {
			out = append(out, fmt.Sprintf("%s — %d invalid attempt(s) excluded", arm, t.Invalid))
		}
	}

	// An arm that never touched Seamark is a finding, not a failure: it is
	// the answer to whether the tools get used without a skill. It is still
	// worth a visible line, because it means the arms may have been identical.
	for _, arm := range []WorkflowArm{ArmMCPSkills, ArmMCPOnly} {
		if t := s.ByArm[arm]; t.Ran > 0 && t.SeamarkCalls == 0 {
			out = append(out, fmt.Sprintf("note — no seamark tool call in any %s trial", arm))
		}
	}

	if s.StoppedReason != "" {
		out = append(out, "stopped — "+s.StoppedReason)
	}

	return out
}

// activationSummary renders skill activation counts as "name×n, name×n",
// sorted by name, or "none".
func activationSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", name, counts[name]))
	}

	return strings.Join(parts, ", ")
}

// RunWorkflow executes the experiment: Trials fresh fixture repos per arm,
// alternating arms so slow model drift within the run spreads evenly, each
// judged mechanically. A pair is finalized and appended before the next pair
// starts; graceful cancellation also flushes a completed arm from a partially
// executed pair. The pairing, cancellation, and infrastructure-stop rules are
// the lessons harness's rules, applied to the workflow rows.
func RunWorkflow(ctx context.Context, cfg WorkflowConfig) (WorkflowSummary, error) {
	instance := cfg.Instance
	if err := instance.Validate(); err != nil {
		return WorkflowSummary{}, err
	}

	if cfg.Trials < 1 {
		return WorkflowSummary{}, fmt.Errorf("trials must be at least 1")
	}

	arms, err := resolvedWorkflowArms(cfg.Arms)
	if err != nil {
		return WorkflowSummary{}, err
	}

	for _, arm := range arms {
		if len(cfg.AgentArgv[arm]) == 0 {
			return WorkflowSummary{}, fmt.Errorf("arm %s has no agent command", arm)
		}
	}

	suppliedFingerprint := cfg.Fingerprint
	expectedFingerprint, err := WorkflowFingerprint(cfg)
	if err != nil {
		return WorkflowSummary{}, fmt.Errorf("fingerprint workflow benchmark: %w", err)
	}

	if suppliedFingerprint != "" && suppliedFingerprint != expectedFingerprint {
		return WorkflowSummary{}, fmt.Errorf("supplied workflow fingerprint %q does not match computed %q",
			suppliedFingerprint, expectedFingerprint)
	}

	cfg.Fingerprint = expectedFingerprint

	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}

	if cfg.RunID == "" {
		cfg.RunID, err = newRunID()
		if err != nil {
			return WorkflowSummary{}, err
		}
	}

	if !validArtifactToken(cfg.RunID) {
		return WorkflowSummary{}, fmt.Errorf("run ID must contain only letters, digits, '.', '_', or '-'")
	}

	sum := WorkflowSummary{ByArm: map[WorkflowArm]WorkflowTally{}, Instance: instance.ID}

	work := cfg.WorkDir
	createdWork := work == ""
	if createdWork {
		work, err = os.MkdirTemp("", "seamark-skills-bench-")
		if err != nil {
			return WorkflowSummary{}, err
		}
	}

	if createdWork && !cfg.Keep {
		defer func() { _ = os.RemoveAll(work) }()
	}

	stop := false
	stopForCancel := false
	var runErr error

	for trial := 1; trial <= cfg.Trials; trial++ {
		if ctx.Err() != nil {
			break
		}

		// Counterbalance execution order within pairs: with a fixed order,
		// slow model drift inside the run loads onto one arm.
		order := slices.Clone(arms)
		if trial%2 == 0 {
			slices.Reverse(order)
		}

		pairRows := make([]WorkflowRow, 0, len(order))
		var pairErr error

		for _, arm := range order {
			if ctx.Err() != nil {
				stopForCancel = true

				break
			}

			row, err := runWorkflowTrial(ctx, cfg, work, arm, trial)
			if err != nil {
				// A trial killed by the interrupt is a stop, not a failure.
				if ctx.Err() != nil {
					stopForCancel = true
				} else {
					pairErr = fmt.Errorf("trial %d %s: %w", trial, arm, err)
				}

				break
			}

			// Persist every completed paid session even when cancellation or
			// the paired arm fails immediately afterward.
			pairRows = append(pairRows, row)
			if row.InfrastructureFailure {
				sum.StoppedReason = row.InvalidReason
				stop = true

				break
			}

			if ctx.Err() != nil {
				stopForCancel = true

				break
			}
		}

		pairValid := len(pairRows) == len(order)
		for _, row := range pairRows {
			pairValid = pairValid && row.Valid
		}

		for i := range pairRows {
			pairRows[i].PairValid = pairValid
			if !pairValid && pairRows[i].Valid && pairRows[i].InvalidReason == "" {
				pairRows[i].InvalidReason = "paired arm invalid"
			}

			row := pairRows[i]
			logf("trial %d %-10s task=%-5v invariant=%-5v  %s%s%s%s",
				trial, row.Arm, row.TaskDone, row.Avoided, row.Notes,
				traceNote(row), validNote(row.Valid, row.PairValid, row.InvalidReason), usageNote(row.AgentUsage))

			if cfg.Out != "" {
				if err := appendJSONL(cfg.Out, row); err != nil {
					return sum, err
				}
			}

			sum.Rows = append(sum.Rows, row)
			tallyWorkflowRow(&sum, row)
		}

		if pairErr != nil {
			runErr = pairErr

			break
		}

		if stop || stopForCancel {
			break
		}
	}

	// Rows already retain every measurement. Derive the means here instead
	// of keeping a second collection of token counts in sync with the tallies.
	for arm, tally := range sum.ByArm {
		var total, count int64
		for _, row := range sum.Rows {
			if row.Arm == arm && row.Valid && row.PairValid && row.ContextTokens > 0 {
				total += row.ContextTokens
				count++
			}
		}

		if count > 0 {
			tally.MeanInput = total / count
			sum.ByArm[arm] = tally
		}
	}

	if sum.StoppedReason != "" {
		return sum, fmt.Errorf("benchmark stopped: %s", sum.StoppedReason)
	}

	if runErr != nil {
		return sum, runErr
	}

	return sum, nil
}

func resolvedWorkflowArms(configured []WorkflowArm) ([]WorkflowArm, error) {
	arms := slices.Clone(configured)
	if len(arms) == 0 {
		arms = []WorkflowArm{ArmMCPSkills, ArmMCPOnly}
	}

	seen := make(map[WorkflowArm]bool, len(arms))
	for _, arm := range arms {
		if !knownWorkflowArm(arm) {
			return nil, fmt.Errorf("unknown workflow arm %q", arm)
		}

		if seen[arm] {
			return nil, fmt.Errorf("duplicate workflow arm %q", arm)
		}

		seen[arm] = true
	}

	return arms, nil
}

func knownWorkflowArm(arm WorkflowArm) bool {
	return arm == ArmMCPOnly || arm == ArmMCPSkills
}

func tallyWorkflowRow(sum *WorkflowSummary, row WorkflowRow) {
	t := sum.ByArm[row.Arm]

	if !row.Valid || !row.PairValid {
		t.Invalid++
		sum.ByArm[row.Arm] = t

		return
	}

	t.Ran++
	if row.TaskDone {
		t.Completed++
	}

	if row.TaskDone && row.Avoided {
		t.Avoided++
	}

	t.add(row)
	t.CostUSD += row.CostUSD

	sum.ByArm[row.Arm] = t
}

// traceNote renders the process facts of one trial on its progress line.
func traceNote(row WorkflowRow) string {
	note := fmt.Sprintf("  seamark=%d change_set-first=%v companion=%v check-last=%v",
		row.SeamarkCalls, row.ChangeSetBeforeFirstEdit, row.CompanionNamedByChangeSet, row.CheckAfterLastEdit)

	if len(row.Activations) > 0 {
		note += "  skills=" + strings.Join(row.Activations, ",")
	}

	if row.TaskDone && !row.ChecksPass {
		note += "  CHECKS-FAILED"
	}

	return note
}

// validNote renders the validity flags a reader must not miss.
func validNote(valid, pairValid bool, reason string) string {
	if valid && pairValid {
		return ""
	}

	if reason == "" {
		return "  INVALID=paired arm failed"
	}

	return "  INVALID=" + reason
}

// usageNote renders the measured cost part of a progress line; empty when
// the agent reported no usage (stub agents, plain-text output).
func usageNote(usage AgentUsage) string {
	if usage.ContextTokens == 0 && usage.CostUSD == 0 {
		return ""
	}

	return fmt.Sprintf("  [fresh %dk, cache-read %dk, cache-write %dk, out %.1fk, $%.2f]",
		usage.InputTokens/1000, usage.CacheReadTokens/1000, usage.CacheCreationTokens/1000,
		float64(usage.OutputTokens)/1000, usage.CostUSD)
}

// runWorkflowTrial generates one fresh fixture, wires the arm, runs the agent
// on the frozen task, reads the transcript, and judges the result.
func runWorkflowTrial(ctx context.Context, cfg WorkflowConfig, work string, arm WorkflowArm, trial int) (WorkflowRow, error) {
	instance := cfg.Instance
	dir := filepath.Join(work, fmt.Sprintf("%s-%02d", arm, trial))

	if _, err := os.Lstat(dir); err == nil {
		return WorkflowRow{}, fmt.Errorf("trial directory already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return WorkflowRow{}, fmt.Errorf("inspect trial directory: %w", err)
	}

	// The absence check above establishes ownership: cleanup may remove this
	// exact path, but never the caller-provided parent or a pre-existing child.
	if !cfg.Keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}

	if err := instance.Generate(dir); err != nil {
		return WorkflowRow{}, err
	}

	bin, err := wireWorkflowArm(ctx, dir, cfg, arm)
	if err != nil {
		return WorkflowRow{}, err
	}

	fixture := fixtureHead(dir)
	if fixture == "" {
		return WorkflowRow{}, fmt.Errorf("trial fixture has no git HEAD")
	}

	row := WorkflowRow{
		SchemaVersion:  WorkflowResultSchemaVersion,
		TS:             time.Now().UTC().Format(time.RFC3339),
		RunID:          cfg.RunID,
		Instance:       instance.ID,
		TaskSHA:        instance.TaskSHA(),
		Trigger:        instance.Trigger,
		Companion:      instance.Companion,
		Arm:            arm,
		Trial:          trial,
		Fixture:        fixture,
		Fingerprint:    cfg.Fingerprint,
		Valid:          true,
		AgentUsage:     AgentUsage{RequestedModel: cfg.Model},
		SeamarkVersion: cfg.Version,
		SeamarkSHA:     cfg.SeamarkSHA,
		AgentVersion:   cfg.AgentVersion,
		Effort:         cfg.Effort,
		MaxBudgetUSD:   cfg.MaxBudgetUSD,
		RuntimeID:      cfg.RuntimeID,
	}

	base := workflowArtifactBase(row.Instance, row.RunID, string(row.Arm), row.Trial)
	if err := prepareArtifacts(cfg.TranscriptDir, base); err != nil {
		return WorkflowRow{}, err
	}

	argv := trialAgentArgv(cfg.AgentArgv[arm], bin, dir)
	stdout, stderr, exit, timedOut, err := runAgent(ctx, RunConfig{AgentArgv: argv, Timeout: cfg.Timeout}, instance.Instance, dir)
	if err != nil {
		return WorkflowRow{}, err
	}

	row.AgentExit = exit
	row.TimedOut = timedOut
	if timedOut {
		row.Notes = "; agent timed out"
	}

	// Transcript and stderr are saved before parsing, so even output the
	// parser cannot read stays available as evidence.
	row.Transcript, row.TranscriptSHA, row.StderrLog, row.StderrSHA, err = saveSessionArtifacts(cfg.TranscriptDir, base, stdout, stderr)
	if err != nil {
		return WorkflowRow{}, err
	}

	if err := interruptedSession(ctx, exit, timedOut); err != nil {
		return WorkflowRow{}, err
	}

	session := readAgentSession(stdout)
	session.apply(&row)
	row.WorkflowTrace = parseWorkflowTrace(stdout, instance.Companion)
	validateWorkflowSession(cfg, arm, &row)

	verdict, err := instance.Judge(dir)
	if err != nil {
		return WorkflowRow{}, err
	}

	row.TaskDone, row.Avoided = verdict.TaskDone, verdict.Avoided
	row.Notes = verdict.Notes + row.Notes

	checks, err := runChecks(ctx, dir, instance.Checks)
	if err != nil {
		recordUnrunChecks(&row, instance.Checks, err)
	} else {
		row.Checks = checks
		row.ChecksPass = checksPass(row.Checks)

		// An interrupt that lands while a check runs kills the check, and
		// the killed command reports a plain failure. The verdict cannot
		// tell that failure from a real one, so the row is not a
		// measurement; a passing check set was complete before the signal.
		// A row the session already invalidated keeps its reason, which
		// names the harness fault the report must show.
		if ctx.Err() != nil && !row.ChecksPass && row.Valid {
			row.Valid = false
			row.InvalidReason = "cancelled during repository checks"
		}

		if row.TaskDone && !row.ChecksPass {
			row.TaskDone = false
			row.Avoided = false
			row.Notes += "; repository checks failed"
		}
	}

	// The patch is the audit record: the verdict must stay checkable after
	// the trial dir is deleted.
	if cfg.TranscriptDir != "" {
		if patch := gitPatch(dir); len(patch) > 0 {
			path := filepath.Join(cfg.TranscriptDir, base+".patch")
			if err := writeArtifactExclusive(path, patch); err != nil {
				return WorkflowRow{}, err
			}

			row.Patch = path
			row.PatchSHA = hashBytes(patch)
		}
	}

	return row, nil
}

// interruptedSession reports a session the operator's interrupt killed: the
// run context ended and the agent died of a signal (exit -1) rather than of
// its own deadline. Such a session is not a measurement, so the caller
// writes no row and the run ends as a cancellation. A session that finished
// on its own just before the interrupt keeps its row.
func interruptedSession(ctx context.Context, exit int, timedOut bool) error {
	if ctx.Err() != nil && exit == -1 && !timedOut {
		return fmt.Errorf("agent session interrupted: %w", ctx.Err())
	}

	return nil
}

// recordUnrunChecks shapes a row whose checks could not start at all (an
// environment failure, not a failing command). The row is invalid, lists
// every configured check as not run with the reason, and counts the task as
// not done: without checks nothing proves the tree builds. Every row keeps a
// non-empty check list, so the strict report reader can still read the file
// the row was appended to.
func recordUnrunChecks(row *WorkflowRow, commands []Command, cause error) {
	invalidateWorkflowRow(row, "cannot run repository checks: "+cause.Error())

	row.Checks = make([]CheckResult, 0, len(commands))
	for _, command := range commands {
		row.Checks = append(row.Checks, CheckResult{
			Command: command.String(), Pass: false, Output: "not run: " + cause.Error(),
		})
	}

	row.ChecksPass = false
	row.TaskDone = false
	row.Avoided = false
	row.Notes += "; repository checks could not run"
}

// workflowArtifactBase names a trial's artifacts by instance, run, arm, and
// trial, the same shape the lessons harness uses.
func workflowArtifactBase(instance, runID, arm string, trial int) string {
	return fmt.Sprintf("%s-%s-%s-%02d", artifactToken(instance), runID, arm, trial)
}

// prepareArtifacts creates the transcript directory and refuses to reuse a
// name, so a repeated run ID can never overwrite evidence.
func prepareArtifacts(transcriptDir, base string) error {
	if transcriptDir == "" {
		return nil
	}

	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		return err
	}

	return ensureArtifactsAbsent(transcriptDir, base)
}

// saveSessionArtifacts writes the transcript and stderr exclusively and
// returns their paths and digests; empty outputs produce no file.
func saveSessionArtifacts(transcriptDir, base string, stdout, stderr []byte) (transcript, transcriptSHA, stderrLog, stderrSHA string, err error) {
	if transcriptDir == "" {
		return "", "", "", "", nil
	}

	if len(stdout) > 0 {
		transcript = filepath.Join(transcriptDir, base+".jsonl")
		if err := writeArtifactExclusive(transcript, stdout); err != nil {
			return "", "", "", "", err
		}

		transcriptSHA = hashBytes(stdout)
	}

	if len(stderr) > 0 {
		stderrLog = filepath.Join(transcriptDir, base+".stderr.log")
		if err := writeArtifactExclusive(stderrLog, stderr); err != nil {
			return "", "", "", "", err
		}

		stderrSHA = hashBytes(stderr)
	}

	return transcript, transcriptSHA, stderrLog, stderrSHA, nil
}

// trialAgentArgv appends the trial's MCP configuration and settings file to
// the arm's command. --strict-mcp-config keeps the operator's own MCP
// servers out of the session; the configuration names the seamark binary
// installed inside the trial, so the agent never executes a binary from the
// host workspace. --settings loads the trial's own .claude/settings.json a
// second time, explicitly: Claude Code ignores the permissions.allow rules
// of a project settings file in a workspace nobody has trusted, and a fresh
// trial directory is never trusted. The file is the one seamark init
// --approve-tools writes, so the measured setup stays the product's.
// --mcp-config takes a variadic list, so it comes first and the boolean
// --strict-mcp-config closes the command. Otherwise the task prompt, which
// the runner appends last, would be read as a second configuration file.
func trialAgentArgv(argv []string, bin, dir string) []string {
	settings := filepath.Join(dir, ".claude", "settings.json")

	return append(slices.Clone(argv), "--mcp-config", trialMCPConfig(bin, dir), "--settings", settings, "--strict-mcp-config")
}

// trialMCPConfig renders the MCP server entry for one trial. The workspace
// flag pins the server to the trial root, whatever directory the agent's
// client uses to start it.
func trialMCPConfig(bin, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}

	config := map[string]any{
		"mcpServers": map[string]any{
			"seamark": map[string]any{
				"command": bin,
				"args":    []string{"-C", abs, "mcp"},
			},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		// A map of strings cannot fail to marshal; the fallback keeps the
		// signature simple for callers.
		return `{"mcpServers":{}}`
	}

	return string(data)
}

// wireWorkflowArm installs exactly the condition an arm measures and returns
// the seamark binary the trial's MCP server runs. Both arms get the lessons
// sandbox settings, the five MCP tool allow rules, and an indexed fixture;
// the skills arm adds the managed skills and their Skill allow rules. No arm
// gets a hook, a lesson file, or a .mcp.json: the MCP server travels on the
// command line, so the fixture's own files stay identical across arms.
func wireWorkflowArm(ctx context.Context, dir string, cfg WorkflowConfig, arm WorkflowArm) (string, error) {
	if !knownWorkflowArm(arm) {
		return "", fmt.Errorf("unknown workflow arm %q", arm)
	}

	if err := excludeHarnessArtifacts(dir); err != nil {
		return "", err
	}

	bin, err := installHarnessBinary(dir, RunConfig{PrepareIndex: cfg.PrepareIndex, SeamarkBin: cfg.SeamarkBin})
	if err != nil {
		return "", err
	}

	rules, err := workflowAllowRules(arm)
	if err != nil {
		return "", err
	}

	if err := writeWorkflowSettings(dir, rules); err != nil {
		return "", err
	}

	if arm == ArmMCPSkills {
		targets, err := skills.Targets(dir, skills.ModeClaude)
		if err != nil {
			return "", err
		}

		if err := skills.Install(io.Discard, dir, targets, false); err != nil {
			return "", fmt.Errorf("install skills into trial: %w", err)
		}
	}

	if !cfg.PrepareIndex {
		return bin, nil
	}

	return bin, indexFixture(ctx, dir, bin)
}

// workflowAllowRules returns the allow rules an arm writes: the MCP tool
// rules for both arms, plus the Skill rules for the skills arm. The rules
// are the ones `seamark init --approve-tools` writes, so the benchmark
// measures the setup a user gets.
func workflowAllowRules(arm WorkflowArm) ([]string, error) {
	rules, err := approve.ClaudeRules()
	if err != nil {
		return nil, err
	}

	if arm == ArmMCPSkills {
		return rules, nil
	}

	var toolRules []string
	for _, rule := range rules {
		if strings.HasPrefix(rule, approve.ServerRule(approve.ClaudeServer)+"__") {
			toolRules = append(toolRules, rule)
		}
	}

	return toolRules, nil
}

// builtInSkillOverrides switches off the Claude Code built-in skills that
// the disableBundledSkills setting leaves in the init record; doctor is the
// one known so far. Both arms carry the overrides, so the arms differ only
// in the seamark skills. The list is best effort: a built-in that still
// slips through is not the arm's doing, and unexpectedInit ignores it.
var builtInSkillOverrides = map[string]any{"doctor": "off"}

// writeWorkflowSettings writes the lessons harness's strict runtime settings
// and adds the allow rules. The base document comes from the lessons writer,
// so the sandbox block stays identical to the lessons arms by construction.
func writeWorkflowSettings(dir string, rules []string) error {
	if err := writeAgentSettings(dir, "", ""); err != nil {
		return err
	}

	path := filepath.Join(dir, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("read trial settings: %w", err)
	}

	permissions, _ := settings["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
	}

	permissions["allow"] = rules
	settings["permissions"] = permissions

	// Claude Code ships its own skills (code-review, verify, and more) with
	// the CLI. Both arms switch them off, so the arms differ only in the
	// seamark skills and the init record lists nothing else. The doctor
	// skill ignores the global switch and needs its own override.
	settings["disableBundledSkills"] = true
	settings["skillOverrides"] = builtInSkillOverrides

	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

// agentSession is what a transcript says about the session apart from the
// tool trace: the init record, the final result, and any provider failure.
type agentSession struct {
	InitSeen bool
	// Model is the model the init record names. It outranks the result
	// record, which names a model only when exactly one model was used.
	Model      string
	Tools      []string
	MCPServers []MCPServerState
	Skills     []string
	Plugins    []string
	// DeniedTools are the tool names in the result's permission_denials.
	DeniedTools []string

	ResultSeen            bool
	Usage                 AgentUsage
	Valid                 bool
	InvalidReason         string
	InfrastructureFailure bool
}

// readAgentSession reads the init record with the workflow's own parser and
// the result, rate-limit, and provider-error records with the lessons
// parser, so both harnesses interpret usage and provider failures identically.
// Best-effort: a stub or plain-text agent leaves every field zero.
func readAgentSession(stdout []byte) agentSession {
	session := agentSession{Valid: true}
	scratch := Row{Valid: true}

	if !parseResult(stdout, &scratch) {
		for line := range strings.SplitSeq(string(stdout), "\n") {
			var probe struct {
				Type string `json:"type"`
			}

			if json.Unmarshal([]byte(line), &probe) != nil {
				continue
			}

			switch probe.Type {
			case "system":
				parseWorkflowInit([]byte(line), &session)
			case "rate_limit_event":
				parseRateLimit([]byte(line), &scratch)
			case "result":
				parseResult([]byte(line), &scratch)
				parseWorkflowDenials([]byte(line), &session)
			}
		}
	}

	session.ResultSeen = scratch.ResultSeen
	session.Valid = scratch.Valid
	session.InvalidReason = scratch.InvalidReason
	session.InfrastructureFailure = scratch.InfrastructureFailure
	session.Usage = AgentUsage{
		Model:               session.Model,
		ModelUsage:          scratch.ModelUsage,
		InputTokens:         scratch.InputTokens,
		CacheReadTokens:     scratch.CacheReadTokens,
		CacheCreationTokens: scratch.CacheCreationTokens,
		ContextTokens:       scratch.ContextTokens,
		OutputTokens:        scratch.OutputTokens,
		Turns:               scratch.Turns,
		PermissionDenials:   scratch.PermissionDenials,
		CostUSD:             scratch.CostUSD,
		DurationMS:          scratch.DurationMS,
		AgentError:          scratch.AgentError,
	}

	// Without an init record, the result's single-model usage is the only
	// name available.
	if session.Usage.Model == "" {
		session.Usage.Model = scratch.Model
	}

	return session
}

// apply copies the session facts onto a row, keeping the row's own exit
// status, timeout flag, and requested model.
func (s agentSession) apply(row *WorkflowRow) {
	row.InitSeen = s.InitSeen
	row.Tools = s.Tools
	row.MCPServers = s.MCPServers
	row.Skills = s.Skills
	row.Plugins = s.Plugins
	row.DeniedTools = s.DeniedTools
	row.ResultSeen = s.ResultSeen

	requested, exit, timedOut := row.RequestedModel, row.AgentExit, row.TimedOut
	row.AgentUsage = s.Usage
	row.RequestedModel, row.AgentExit, row.TimedOut = requested, exit, timedOut

	if !s.Valid {
		invalidateWorkflowRow(row, s.InvalidReason)
	}
}

// initEntry reads one entry of an init list. Claude Code reports MCP servers
// as {name, status} objects and skills as plain names; accepting both shapes
// for every list keeps the parser stable across client versions.
type initEntry struct {
	Name   string
	Status string
}

func (e *initEntry) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		e.Name = name

		return nil
	}

	var object struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}

	e.Name, e.Status = object.Name, object.Status

	return nil
}

// parseWorkflowDenials reads the tool names the agent was refused from the
// result record. The lessons parser keeps only their count; the workflow
// validator needs the names to tell a refused seamark tool from a refused
// WebFetch, which is a measured outcome.
func parseWorkflowDenials(line []byte, session *agentSession) {
	var result struct {
		Type    string `json:"type"`
		Denials []struct {
			ToolName string `json:"tool_name"`
		} `json:"permission_denials"`
	}

	if json.Unmarshal(line, &result) != nil || result.Type != "result" {
		return
	}

	for _, denial := range result.Denials {
		if denial.ToolName != "" && !slices.Contains(session.DeniedTools, denial.ToolName) {
			session.DeniedTools = append(session.DeniedTools, denial.ToolName)
		}
	}
}

func parseWorkflowInit(line []byte, session *agentSession) {
	var init struct {
		Type       string      `json:"type"`
		Subtype    string      `json:"subtype"`
		Model      string      `json:"model"`
		Tools      []string    `json:"tools"`
		MCPServers []initEntry `json:"mcp_servers"`
		Skills     []initEntry `json:"skills"`
		Plugins    []initEntry `json:"plugins"`
	}

	if json.Unmarshal(line, &init) != nil || init.Type != "system" || init.Subtype != "init" {
		return
	}

	session.InitSeen = true
	session.Model = init.Model
	session.Tools = slices.Clone(init.Tools)

	for _, server := range init.MCPServers {
		session.MCPServers = append(session.MCPServers, MCPServerState(server))
	}

	for _, skill := range init.Skills {
		session.Skills = append(session.Skills, skill.Name)
	}

	for _, plugin := range init.Plugins {
		session.Plugins = append(session.Plugins, plugin.Name)
	}
}

// validateWorkflowSession invalidates a row whose session did not deliver
// the arm: no init record, a plugin, a missing or disconnected seamark MCP
// server, a different tool set, the wrong skills, no structured result, or
// another model than requested.
func validateWorkflowSession(cfg WorkflowConfig, arm WorkflowArm, row *WorkflowRow) {
	if !row.Valid {
		return
	}

	if cfg.RequireExpectedInit {
		if reason := unexpectedInit(arm, row); reason != "" {
			invalidateWorkflowRow(row, reason)

			return
		}

		// Claude Code ignores project allow rules in a workspace nobody has
		// trusted, and a headless session cannot answer a prompt. A refused
		// seamark or Skill call therefore means the arm was not delivered.
		if tool := deniedArmTool(row.DeniedTools); tool != "" {
			invalidateWorkflowRow(row, "agent was denied "+tool+": the allow rules were not in effect")

			return
		}
	}

	// A deadline is an experimental outcome: Claude's stream normally has an
	// init record but no final result when the process is killed.
	if cfg.RequireStructuredResult && !row.ResultSeen && !row.TimedOut {
		invalidateWorkflowRow(row, "agent produced no structured result")

		return
	}

	if cfg.Model != "" && !modelMatches(cfg.Model, row.Model) {
		invalidateWorkflowRow(row,
			fmt.Sprintf("requested model %q but agent initialized %q", cfg.Model, row.Model))
	}
}

// unexpectedInit returns the first way the init record differs from what
// the arm wired, or "" when it matches.
func unexpectedInit(arm WorkflowArm, row *WorkflowRow) string {
	if !row.InitSeen {
		return "agent produced no initialization record"
	}

	if len(row.Plugins) > 0 {
		return "unexpected agent plugins loaded"
	}

	switch {
	case len(row.MCPServers) == 0:
		return "seamark MCP server missing from the agent initialization"
	case len(row.MCPServers) > 1 || row.MCPServers[0].Name != "seamark":
		return "unexpected MCP servers loaded"
	case row.MCPServers[0].Status != "" && row.MCPServers[0].Status != "connected":
		return "seamark MCP server not connected: " + row.MCPServers[0].Status
	}

	if reason := setDifference("agent tool", WorkflowTools(arm), row.Tools); reason != "" {
		return reason
	}

	names, err := skills.Names()
	if err != nil {
		return "cannot list shipped skills: " + err.Error()
	}

	// The seamark catalogue decides the arm: the skills arm must load
	// every shipped skill and the MCP-only arm none. A Claude Code built-in
	// the overrides did not switch off is the same in both arms and is not
	// a reason to discard a paid session. Any other skill is a project or
	// user skill that leaked into the trial, which the arm did not intend.
	var expectedSkills, observed []string
	if arm == ArmMCPSkills {
		expectedSkills = names
	}

	for _, skill := range row.Skills {
		switch {
		case slices.Contains(names, skill):
			observed = append(observed, skill)
		case builtInSkillOverrides[skill] != nil:
		default:
			return "unexpected skill loaded: " + skill
		}
	}

	return setDifference("skill", expectedSkills, observed)
}

// deniedArmTool returns the first refused tool that belongs to an arm's
// condition: a seamark MCP tool or the Skill tool. Other refusals, such as
// WebFetch, are the agent's own choices and stay measured outcomes.
func deniedArmTool(denied []string) string {
	condition := conditionTools(ArmMCPSkills)

	for _, tool := range denied {
		if slices.Contains(condition, tool) {
			return tool
		}
	}

	return ""
}

// setDifference names the first extra or missing element between the
// expected and the observed lists, ignoring order and repeats.
func setDifference(kind string, expected, observed []string) string {
	if len(observed) == 0 && len(expected) > 0 {
		return fmt.Sprintf("agent initialized without the required %s set", kind)
	}

	for _, name := range observed {
		if !slices.Contains(expected, name) {
			return fmt.Sprintf("unexpected %s loaded: %s", kind, name)
		}
	}

	for _, name := range expected {
		if !slices.Contains(observed, name) {
			return fmt.Sprintf("required %s missing: %s", kind, name)
		}
	}

	return ""
}

func invalidateWorkflowRow(row *WorkflowRow, reason string) {
	row.Valid = false
	row.InfrastructureFailure = true
	if row.InvalidReason == "" {
		row.InvalidReason = reason
	}
}

// ReadWorkflowRows reads a workflow results file back, skipping unparseable
// lines. A missing file is an empty history, not an error.
func ReadWorkflowRows(path string) ([]WorkflowRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	var out []WorkflowRow

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var row WorkflowRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}

		out = append(out, row)
	}

	return out, nil
}

// PriorWorkflowCostFor pools only valid paired rows with the same workflow
// fingerprint, for the cost estimate the banner prints before a paid run.
func PriorWorkflowCostFor(path, fingerprint string) (rows int, meanInput int64, meanCost float64, ok bool) {
	if fingerprint == "" {
		return 0, 0, 0, false
	}

	all, err := ReadWorkflowRows(path)
	if err != nil || len(all) == 0 {
		return 0, 0, 0, false
	}

	var inputTotal, count int64
	var costTotal float64

	for _, row := range all {
		if row.Fingerprint != fingerprint || !row.Valid || !row.PairValid {
			continue
		}

		if row.ContextTokens == 0 && row.CostUSD == 0 {
			continue
		}

		inputTotal += row.ContextTokens
		costTotal += row.CostUSD
		count++
	}

	if count == 0 {
		return 0, 0, 0, false
	}

	return int(count), inputTotal / count, costTotal / float64(count), true
}

// appendJSONL appends one JSON row, creating the file and its parent
// directory on first use.
func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	data = append(data, '\n')
	if n, writeErr := f.Write(data); writeErr != nil {
		_ = f.Close()

		return writeErr
	} else if n != len(data) {
		_ = f.Close()

		return io.ErrShortWrite
	}

	return f.Close()
}
