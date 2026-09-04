package bench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/skills"
)

// Activation prompt vocabulary. A prompt expects one shipped skill or none;
// a prompt may ask for the naive patch first, so a review prompt sees a
// real diff instead of a clean tree.
const (
	ActivationExpectNone   = "none"
	ActivationPrepareNaive = "naive"
)

// ActivationPrompt is one entry of the checked-in prompt set.
type ActivationPrompt struct {
	ID     string `yaml:"id"`
	Prompt string `yaml:"prompt"`
	// Expect names the skill that should activate, or "none" for a prompt
	// no skill should answer.
	Expect string `yaml:"expect"`
	// Prepare changes the fixture before the session: "naive" applies the
	// instance's naive patch; empty leaves the tree untouched.
	Prepare string `yaml:"prepare,omitempty"`
	// Note says why the prompt is in the set.
	Note string `yaml:"note,omitempty"`
}

// ActivationPromptSet is the checked-in prompt file. Instance names the
// workflow instance the prompts are written for: they name its files, so
// a run on another fixture would measure a mismatch, not activation.
type ActivationPromptSet struct {
	SchemaVersion int                `yaml:"schema_version"`
	Instance      string             `yaml:"instance"`
	Prompts       []ActivationPrompt `yaml:"prompts"`
}

// LoadActivationPrompts parses and validates a prompt file. Unknown keys and
// a second YAML document are errors, so a typo cannot silently drop a case.
func LoadActivationPrompts(path string) (ActivationPromptSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ActivationPromptSet{}, err
	}

	var set ActivationPromptSet
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&set); err != nil {
		return ActivationPromptSet{}, fmt.Errorf("parse activation prompts: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ActivationPromptSet{}, fmt.Errorf("parse activation prompts: multiple YAML documents")
		}

		return ActivationPromptSet{}, fmt.Errorf("parse activation prompts: %w", err)
	}

	if err := set.Validate(); err != nil {
		return ActivationPromptSet{}, err
	}

	return set, nil
}

// Validate rejects a prompt set that could not measure what the report
// claims: ids must be unique and usable in file names, every expectation
// must name a shipped skill or none, every shipped skill needs at least one
// should-activate prompt, and there must be at least one should-not prompt.
func (s ActivationPromptSet) Validate() error {
	if s.SchemaVersion != 1 {
		return fmt.Errorf("activation prompts schema_version is %d, want 1", s.SchemaVersion)
	}

	if _, err := WorkflowInstanceByID(s.Instance); err != nil {
		return fmt.Errorf("activation prompts instance: %w", err)
	}

	if len(s.Prompts) == 0 {
		return fmt.Errorf("activation prompt set is empty")
	}

	names, err := skills.Names()
	if err != nil {
		return err
	}

	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}

	seen := make(map[string]bool, len(s.Prompts))
	covered := make(map[string]bool, len(names))
	shouldNot := 0

	for _, prompt := range s.Prompts {
		switch {
		case prompt.ID == "":
			return fmt.Errorf("activation prompt id is required")
		case !validArtifactToken(prompt.ID):
			return fmt.Errorf("activation prompt id %q must contain only letters, digits, '.', '_', or '-'", prompt.ID)
		case seen[prompt.ID]:
			return fmt.Errorf("duplicate activation prompt id %q", prompt.ID)
		case strings.TrimSpace(prompt.Prompt) == "":
			return fmt.Errorf("activation prompt %q has no prompt text", prompt.ID)
		case prompt.Expect != ActivationExpectNone && !known[prompt.Expect]:
			return fmt.Errorf("activation prompt %q expects unknown skill %q (shipped: %s, or %s)",
				prompt.ID, prompt.Expect, strings.Join(names, ", "), ActivationExpectNone)
		case prompt.Prepare != "" && prompt.Prepare != ActivationPrepareNaive:
			return fmt.Errorf("activation prompt %q has unknown prepare step %q", prompt.ID, prompt.Prepare)
		}

		seen[prompt.ID] = true

		if prompt.Expect == ActivationExpectNone {
			shouldNot++
		} else {
			covered[prompt.Expect] = true
		}
	}

	for _, name := range names {
		if !covered[name] {
			return fmt.Errorf("activation prompt set has no should-activate prompt for %s", name)
		}
	}

	if shouldNot == 0 {
		return fmt.Errorf("activation prompt set has no should-not-activate prompt")
	}

	return nil
}

// SHA256 identifies the exact prompt set a row was measured against.
func (s ActivationPromptSet) SHA256() (string, error) {
	return hashJSON(s.Prompts)
}

// ActivationResultSchemaVersion is the contract of activation rows. They
// never share a file with workflow rows.
const ActivationResultSchemaVersion = 1

// ActivationRow is one prompt session of the activation evaluation.
type ActivationRow struct {
	SchemaVersion int    `json:"schema_version"`
	TS            string `json:"ts"`
	RunID         string `json:"run_id"`
	Instance      string `json:"instance"`
	Fixture       string `json:"fixture"`
	Fingerprint   string `json:"fingerprint"`
	// PromptSetSHA identifies the whole prompt file; PromptID and
	// PromptSHA identify the one prompt this row measured.
	PromptSetSHA string `json:"prompt_set_sha256"`
	PromptID     string `json:"prompt_id"`
	PromptSHA    string `json:"prompt_sha256"`
	Expected     string `json:"expected"`
	Prepare      string `json:"prepare,omitempty"`
	// Activated lists the skills the agent loaded; Hit says the session met
	// its expectation: the expected skill loaded, or nothing loaded for a
	// should-not prompt. Other skills loading beside the expected one keep
	// Hit true and show up in the report's cross-activation count.
	Activated []string `json:"activated,omitempty"`
	Hit       bool     `json:"hit"`
	MaxTurns  int      `json:"max_turns,omitempty"`

	Valid                 bool   `json:"valid"`
	InvalidReason         string `json:"invalid_reason,omitempty"`
	InfrastructureFailure bool   `json:"infrastructure_failure,omitempty"`

	InitSeen   bool             `json:"init_seen,omitempty"`
	ResultSeen bool             `json:"result_seen,omitempty"`
	Tools      []string         `json:"tools,omitempty"`
	MCPServers []MCPServerState `json:"mcp_servers,omitempty"`
	Skills     []string         `json:"skills,omitempty"`
	Plugins    []string         `json:"plugins,omitempty"`

	SeamarkCalls     int            `json:"seamark_calls"`
	SeamarkToolCalls map[string]int `json:"seamark_tool_calls,omitempty"`
	Edits            int            `json:"edits"`

	AgentUsage

	SeamarkVersion string  `json:"seamark_version,omitempty"`
	SeamarkSHA     string  `json:"seamark_sha256,omitempty"`
	AgentVersion   string  `json:"agent_version,omitempty"`
	Effort         string  `json:"effort,omitempty"`
	MaxBudgetUSD   float64 `json:"max_budget_usd,omitempty"`
	RuntimeID      string  `json:"runtime_id,omitempty"`

	Transcript    string `json:"transcript,omitempty"`
	TranscriptSHA string `json:"transcript_sha256,omitempty"`
	StderrLog     string `json:"stderr,omitempty"`
	StderrSHA     string `json:"stderr_sha256,omitempty"`
}

// ValidateActivationRow enforces the semantic contract of an activation row.
func ValidateActivationRow(row ActivationRow) error {
	if row.SchemaVersion != ActivationResultSchemaVersion {
		return fmt.Errorf("schema_version is %d, want %d", row.SchemaVersion, ActivationResultSchemaVersion)
	}

	switch {
	case row.TS == "":
		return fmt.Errorf("ts is required")
	case row.RunID == "":
		return fmt.Errorf("run_id is required")
	case row.Instance == "":
		return fmt.Errorf("instance is required")
	case !validGitOID(row.Fixture):
		return fmt.Errorf("fixture must be a lowercase SHA-1 or SHA-256 git object ID")
	case row.PromptID == "":
		return fmt.Errorf("prompt_id is required")
	case row.Expected == "":
		return fmt.Errorf("expected is required")
	case row.Prepare != "" && row.Prepare != ActivationPrepareNaive:
		return fmt.Errorf("unknown prepare step %q", row.Prepare)
	case row.Valid && row.InfrastructureFailure:
		return fmt.Errorf("a valid row cannot record an infrastructure failure")
	case row.Hit != activationHit(row.Expected, row.Activated):
		return fmt.Errorf("hit does not follow from expected and activated")
	case row.MaxTurns < 0 || row.SeamarkCalls < 0 || row.Edits < 0 || row.Turns < 0 || row.PermissionDenials < 0:
		return fmt.Errorf("event counts must not be negative")
	case row.InputTokens < 0 || row.CacheReadTokens < 0 || row.CacheCreationTokens < 0 ||
		row.ContextTokens < 0 || row.OutputTokens < 0:
		return fmt.Errorf("token counts must not be negative")
	case row.CostUSD < 0 || row.DurationMS < 0:
		return fmt.Errorf("cost and duration must not be negative")
	}

	total := 0
	for tool, count := range row.SeamarkToolCalls {
		if tool == "" || count < 0 {
			return fmt.Errorf("seamark_tool_calls entries need a tool name and a non-negative count")
		}

		total += count
	}

	if total != row.SeamarkCalls {
		return fmt.Errorf("seamark_calls is %d, want the seamark_tool_calls sum %d", row.SeamarkCalls, total)
	}

	for _, name := range row.Activated {
		if name == "" {
			return fmt.Errorf("activated entries need a skill name")
		}
	}

	if _, err := time.Parse(time.RFC3339, row.TS); err != nil {
		return fmt.Errorf("ts: %w", err)
	}

	for _, field := range []struct{ name, value string }{
		{"fingerprint", row.Fingerprint},
		{"prompt_set_sha256", row.PromptSetSHA},
		{"prompt_sha256", row.PromptSHA},
	} {
		if !validSHA256(field.value) {
			return fmt.Errorf("%s must be a lowercase SHA-256", field.name)
		}
	}

	if row.SeamarkSHA != "" && !validSHA256(row.SeamarkSHA) {
		return fmt.Errorf("seamark_sha256 must be a lowercase SHA-256")
	}

	if err := validateArtifactPairs(row.Transcript, row.TranscriptSHA, row.StderrLog, row.StderrSHA, "", ""); err != nil {
		return err
	}

	if row.ContextTokens != row.InputTokens+row.CacheReadTokens+row.CacheCreationTokens {
		return fmt.Errorf("context_tokens does not equal its component sum")
	}

	return nil
}

// activationHit decides whether a session met its prompt's expectation.
func activationHit(expected string, activated []string) bool {
	if expected == ActivationExpectNone {
		return len(activated) == 0
	}

	return slices.Contains(activated, expected)
}

// ActivationRate is a numerator over a denominator, kept as counts so the
// report can print "2/3" and the reader can see how small the sample is.
type ActivationRate struct {
	Numerator   int
	Denominator int
}

// Value returns the rate, or false when nothing was measured.
func (r ActivationRate) Value() (float64, bool) {
	if r.Denominator == 0 {
		return 0, false
	}

	return float64(r.Numerator) / float64(r.Denominator), true
}

// ActivationStats are the inputs of the activation criteria: recall per
// expected skill over its should-activate prompts, and the false-activation
// rate over the should-not prompts. Only valid rows count.
type ActivationStats struct {
	Recall          map[string]ActivationRate
	FalseActivation ActivationRate
	// CrossActivations counts valid should-activate sessions that loaded a
	// skill other than the expected one; two skills on one prompt is a
	// description defect the criteria do not otherwise see.
	CrossActivations int
	Valid            int
	Invalid          int
}

// ActivationStatsFor tallies rows into the criteria's inputs.
func ActivationStatsFor(rows []ActivationRow) ActivationStats {
	stats := ActivationStats{Recall: map[string]ActivationRate{}}

	for _, row := range rows {
		if !row.Valid {
			stats.Invalid++

			continue
		}

		stats.Valid++

		if row.Expected == ActivationExpectNone {
			stats.FalseActivation.Denominator++
			if len(row.Activated) > 0 {
				stats.FalseActivation.Numerator++
			}

			continue
		}

		rate := stats.Recall[row.Expected]
		rate.Denominator++
		if row.Hit {
			rate.Numerator++
		}

		stats.Recall[row.Expected] = rate

		for _, name := range row.Activated {
			if name != row.Expected {
				stats.CrossActivations++

				break
			}
		}
	}

	return stats
}

// Lines renders the stats as raw counts, one line per criterion input.
func (s ActivationStats) Lines() []string {
	out := []string{fmt.Sprintf("activation — %d valid session(s), %d invalid", s.Valid, s.Invalid)}

	names := make([]string, 0, len(s.Recall))
	for name := range s.Recall {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		rate := s.Recall[name]
		out = append(out, fmt.Sprintf("recall %s — %s", name, rateString(rate)))
	}

	out = append(out, "false activation — "+rateString(s.FalseActivation))

	if s.CrossActivations > 0 {
		out = append(out, fmt.Sprintf("cross-activation — %d should-activate session(s) also loaded another skill", s.CrossActivations))
	}

	return out
}

func rateString(rate ActivationRate) string {
	value, ok := rate.Value()
	if !ok {
		return "n/a"
	}

	return fmt.Sprintf("%d/%d (%.0f%%)", rate.Numerator, rate.Denominator, value*100)
}

// ActivationSummary is one activation run's outcome.
type ActivationSummary struct {
	Rows          []ActivationRow
	RunID         string
	Instance      string
	StoppedReason string
}

// Lines renders the run as the activation stats plus the stop reason.
func (s ActivationSummary) Lines() []string {
	out := ActivationStatsFor(s.Rows).Lines()

	if s.StoppedReason != "" {
		out = append(out, "stopped — "+s.StoppedReason)
	}

	return out
}

// RunActivation replays the prompt set, one fresh skills-arm session per
// prompt, and records which skills activated. Sessions are capped by
// cfg.MaxTurns; reaching the cap is a measured outcome. Provider failures
// stop the run before the next paid session, and cancellation returns the
// partial summary with a nil error.
func RunActivation(ctx context.Context, cfg WorkflowConfig, prompts ActivationPromptSet) (ActivationSummary, error) {
	instance := cfg.Instance
	if err := instance.Validate(); err != nil {
		return ActivationSummary{}, err
	}

	if err := prompts.Validate(); err != nil {
		return ActivationSummary{}, err
	}

	if prompts.Instance != instance.ID {
		return ActivationSummary{}, fmt.Errorf("the prompt set is written for instance %s, not %s",
			prompts.Instance, instance.ID)
	}

	if len(cfg.AgentArgv[ArmMCPSkills]) == 0 {
		return ActivationSummary{}, fmt.Errorf("arm %s has no agent command", ArmMCPSkills)
	}

	if cfg.MaxTurns < 0 {
		return ActivationSummary{}, fmt.Errorf("max turns must not be negative")
	}

	promptSetSHA, err := prompts.SHA256()
	if err != nil {
		return ActivationSummary{}, err
	}

	// Activation rows carry the workflow fingerprint of the skills arm alone:
	// the MCP-only arm plays no part in an activation run.
	cfg.Arms = []WorkflowArm{ArmMCPSkills}
	cfg.Fingerprint, err = WorkflowFingerprint(cfg)
	if err != nil {
		return ActivationSummary{}, fmt.Errorf("fingerprint activation run: %w", err)
	}

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
			return ActivationSummary{}, err
		}
	}

	if !validArtifactToken(cfg.RunID) {
		return ActivationSummary{}, fmt.Errorf("run ID must contain only letters, digits, '.', '_', or '-'")
	}

	sum := ActivationSummary{RunID: cfg.RunID, Instance: instance.ID}

	work := cfg.WorkDir
	createdWork := work == ""
	if createdWork {
		work, err = os.MkdirTemp("", "seamark-skills-activation-")
		if err != nil {
			return ActivationSummary{}, err
		}
	}

	if createdWork && !cfg.Keep {
		defer func() { _ = os.RemoveAll(work) }()
	}

	for _, prompt := range prompts.Prompts {
		if ctx.Err() != nil {
			break
		}

		row, err := runActivationSession(ctx, cfg, work, prompt, promptSetSHA)
		if err != nil {
			if ctx.Err() != nil {
				break
			}

			return sum, fmt.Errorf("prompt %s: %w", prompt.ID, err)
		}

		logf("prompt %-28s expect=%-24s activated=%-40s hit=%-5v%s%s",
			prompt.ID, prompt.Expect, activationList(row.Activated), row.Hit,
			validNote(row.Valid, true, row.InvalidReason), usageNote(row.AgentUsage))

		if cfg.Out != "" {
			if err := appendJSONL(cfg.Out, row); err != nil {
				return sum, err
			}
		}

		sum.Rows = append(sum.Rows, row)

		if row.InfrastructureFailure {
			sum.StoppedReason = row.InvalidReason

			break
		}
	}

	if sum.StoppedReason != "" {
		return sum, fmt.Errorf("activation run stopped: %s", sum.StoppedReason)
	}

	return sum, nil
}

func activationList(names []string) string {
	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ",")
}

// runActivationSession runs one prompt in a fresh skills-arm trial and reads
// the activations from its transcript.
func runActivationSession(ctx context.Context, cfg WorkflowConfig, work string, prompt ActivationPrompt, promptSetSHA string) (ActivationRow, error) {
	instance := cfg.Instance
	dir := filepath.Join(work, "activation-"+artifactToken(prompt.ID))

	if _, err := os.Lstat(dir); err == nil {
		return ActivationRow{}, fmt.Errorf("session directory already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return ActivationRow{}, fmt.Errorf("inspect session directory: %w", err)
	}

	if !cfg.Keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}

	if err := instance.Generate(dir); err != nil {
		return ActivationRow{}, err
	}

	if prompt.Prepare == ActivationPrepareNaive {
		if err := instance.ApplyNaive(dir); err != nil {
			return ActivationRow{}, fmt.Errorf("apply naive patch: %w", err)
		}
	}

	bin, err := wireWorkflowArm(ctx, dir, cfg, ArmMCPSkills)
	if err != nil {
		return ActivationRow{}, err
	}

	fixture := fixtureHead(dir)
	if fixture == "" {
		return ActivationRow{}, fmt.Errorf("session fixture has no git HEAD")
	}

	row := ActivationRow{
		SchemaVersion:  ActivationResultSchemaVersion,
		TS:             time.Now().UTC().Format(time.RFC3339),
		RunID:          cfg.RunID,
		Instance:       instance.ID,
		Fixture:        fixture,
		Fingerprint:    cfg.Fingerprint,
		PromptSetSHA:   promptSetSHA,
		PromptID:       prompt.ID,
		PromptSHA:      hashBytes([]byte(prompt.Prompt)),
		Expected:       prompt.Expect,
		Prepare:        prompt.Prepare,
		MaxTurns:       cfg.MaxTurns,
		Valid:          true,
		AgentUsage:     AgentUsage{RequestedModel: cfg.Model},
		SeamarkVersion: cfg.Version,
		SeamarkSHA:     cfg.SeamarkSHA,
		AgentVersion:   cfg.AgentVersion,
		Effort:         cfg.Effort,
		MaxBudgetUSD:   cfg.MaxBudgetUSD,
		RuntimeID:      cfg.RuntimeID,
	}

	base := fmt.Sprintf("%s-%s-activation-%s", artifactToken(instance.ID), cfg.RunID, artifactToken(prompt.ID))
	if err := prepareArtifacts(cfg.TranscriptDir, base); err != nil {
		return ActivationRow{}, err
	}

	argv := trialAgentArgv(cfg.AgentArgv[ArmMCPSkills], bin, dir)
	if cfg.MaxTurns > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(cfg.MaxTurns))
	}

	// The prompt replaces the task; the working-tree instruction still
	// travels with it, as in every benchmark session.
	promptInstance := instance.Instance
	promptInstance.Task = prompt.Prompt

	stdout, stderr, exit, timedOut, err := runAgent(ctx, RunConfig{AgentArgv: argv, Timeout: cfg.Timeout}, promptInstance, dir)
	if err != nil {
		return ActivationRow{}, err
	}

	row.AgentExit = exit
	row.TimedOut = timedOut

	row.Transcript, row.TranscriptSHA, row.StderrLog, row.StderrSHA, err = saveSessionArtifacts(cfg.TranscriptDir, base, stdout, stderr)
	if err != nil {
		return ActivationRow{}, err
	}

	if err := interruptedSession(ctx, exit, timedOut); err != nil {
		return ActivationRow{}, err
	}

	session := readAgentSession(stdout)
	row.InitSeen, row.ResultSeen = session.InitSeen, session.ResultSeen
	row.Tools, row.MCPServers, row.Skills, row.Plugins = session.Tools, session.MCPServers, session.Skills, session.Plugins

	requested := row.RequestedModel
	row.AgentUsage = session.Usage
	row.RequestedModel, row.AgentExit, row.TimedOut = requested, exit, timedOut

	if !session.Valid {
		invalidateActivationRow(&row, session.InvalidReason)
	}

	trace := parseWorkflowTrace(stdout, instance.Companion)
	row.Activated = trace.Activations
	row.SeamarkCalls, row.SeamarkToolCalls, row.Edits = trace.SeamarkCalls, trace.SeamarkToolCalls, trace.Edits
	row.Hit = activationHit(row.Expected, row.Activated)

	validateActivationSession(cfg, &row)

	return row, nil
}

// validateActivationSession applies the skills-arm session rules to an
// activation row. A session that stopped at --max-turns keeps its structured
// result and stays valid: the cap is part of the measurement.
func validateActivationSession(cfg WorkflowConfig, row *ActivationRow) {
	if !row.Valid {
		return
	}

	if cfg.RequireExpectedInit {
		probe := WorkflowRow{
			InitSeen: row.InitSeen, Tools: row.Tools, MCPServers: row.MCPServers,
			Skills: row.Skills, Plugins: row.Plugins,
		}

		if reason := unexpectedInit(ArmMCPSkills, &probe); reason != "" {
			invalidateActivationRow(row, reason)

			return
		}
	}

	if cfg.RequireStructuredResult && !row.ResultSeen && !row.TimedOut {
		invalidateActivationRow(row, "agent produced no structured result")

		return
	}

	if cfg.Model != "" && !modelMatches(cfg.Model, row.Model) {
		invalidateActivationRow(row,
			fmt.Sprintf("requested model %q but agent initialized %q", cfg.Model, row.Model))
	}
}

func invalidateActivationRow(row *ActivationRow, reason string) {
	row.Valid = false
	row.InfrastructureFailure = true
	if row.InvalidReason == "" {
		row.InvalidReason = reason
	}
}

// ReadActivationRows reads an activation results file strictly: every line
// must be a valid row, because the report renders every one of them.
func ReadActivationRows(path string) ([]ActivationRow, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}

	var rows []ActivationRow
	lineNumber := 0

	for line := range strings.SplitSeq(string(data), "\n") {
		lineNumber++
		if strings.TrimSpace(line) == "" {
			continue
		}

		var row ActivationRow
		if err := decodeJSONLRow(line, &row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		if err := ValidateActivationRow(row); err != nil {
			return nil, "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}

		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return nil, "", fmt.Errorf("%s: no activation rows", path)
	}

	return rows, hashBytes(data), nil
}
