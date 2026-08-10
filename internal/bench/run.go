package bench

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// Arm is one experimental condition. The fixture itself carries no
// seamark artifacts; each arm installs exactly what it measures.
type Arm string

// HookDeliveryMode is the lessons hook policy measured by a benchmark cohort.
type HookDeliveryMode = reviews.HookDeliveryMode

const (
	// HookDeliveryAlways repeats matching context on every edit.
	HookDeliveryAlways = reviews.HookDeliveryAlways
	// HookDeliveryOncePerContext suppresses a delivered lesson until compaction.
	HookDeliveryOncePerContext = reviews.HookDeliveryOncePerContext
)

const (
	// ArmHookOff is the true baseline: no lesson anywhere, no hook.
	ArmHookOff Arm = "hook-off"
	// ArmFileOnly commits the real lesson to lessons.yaml but wires no
	// hook: delivery depends on the agent discovering the file. This
	// is the mechanism arm — a committed lessons.yaml is itself a
	// delivery channel, and the hook's claim is beating it.
	ArmFileOnly Arm = "file-only"
	// ArmPlacebo wires the hook with a same-size lesson that carries
	// no information about the mistake: the cost/attention control.
	ArmPlacebo Arm = "placebo"
	// ArmHookOn is the full treatment: real lesson, hook wired.
	ArmHookOn Arm = "hook-on"
)

// RunConfig configures one benchmark run.
type RunConfig struct {
	Trials     int           // trials per arm
	Arms       []Arm         // arms to run; nil means both
	Instance   Instance      // zero value selects SchemaSyncInstance
	AgentArgv  []string      // agent command; the task prompt is appended as the last argument
	SeamarkBin string        // absolute path to the seamark binary (hook command + index)
	Timeout    time.Duration // per-trial agent timeout; 0 means 10 minutes
	Out        string        // results JSONL path, appended one row per trial
	WorkDir    string        // parent for trial dirs; "" means a fresh temp dir
	Keep       bool          // keep trial dirs after judging, for inspection
	// TranscriptDir saves each trial's raw agent stdout (the full
	// stream-json transcript when the agent emits one) for reading WHY
	// a verdict came out the way it did. Empty disables saving.
	TranscriptDir string
	// PrepareIndex runs `seamark index` in hook-on trials so the hook
	// has a store to read. Hermetic tests turn it off.
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
	// invocations. Empty asks Run to generate a cryptographically random ID.
	RunID string
	// HookDelivery selects the edit-hook repeat policy. Empty means always.
	HookDelivery HookDeliveryMode

	// RequireStructuredResult and RequireCleanInit are true for the
	// default Claude adapter. Stub/custom adapters may leave them false.
	RequireStructuredResult bool
	RequireCleanInit        bool
	Log                     func(string, ...any) // progress lines; nil silences
}

// Row is one trial's result as appended to the JSONL file. Rows are
// self-contained: pin, arm, verdict, and cost travel together so the
// file stays meaningful across runs and versions.
type Row struct {
	SchemaVersion int    `json:"schema_version"`
	TS            string `json:"ts"`
	RunID         string `json:"run_id"`
	Instance      string `json:"instance"`
	TaskSHA       string `json:"task_sha256"`
	Pin           string `json:"pin"`
	Arm           Arm    `json:"arm"`
	Trial         int    `json:"trial"`
	TaskDone      bool   `json:"task_pass"`
	Avoided       bool   `json:"invariant_pass"`
	Notes         string `json:"notes,omitempty"`

	// Valid says the agent session and treatment were actually delivered.
	// PairValid additionally says every requested arm in this trial number was
	// valid. Only rows satisfying both enter effect tallies.
	Valid                 bool   `json:"valid"`
	PairValid             bool   `json:"pair_valid"`
	InvalidReason         string `json:"invalid_reason,omitempty"`
	InfrastructureFailure bool   `json:"infrastructure_failure,omitempty"`
	// Fixture is the generated repo's full HEAD commit. Generation
	// is deterministic, so this identifies the exact fixture content a
	// row was measured against — rows from different fixture versions
	// must never be pooled as one series.
	Fixture string `json:"fixture,omitempty"`
	// HookFirings is how many firing records the trial repo's own
	// audit log holds after the run (hook-on arm only). Zero means the
	// injection never reached the agent and the arms were effectively
	// identical — the row proves its treatment happened instead of
	// assuming it.
	HookFirings int `json:"hook_firings,omitempty"`
	// HookAuditRows records every audit row, including unrelated or malformed
	// firings. HookFirings counts only rows proving that the selected lesson
	// reached an in-region edit through the expected hook surface.
	HookAuditRows int `json:"hook_audit_rows,omitempty"`
	// Schema v6 delivery intensity. Matches counts matching edit-hook
	// invocations; each match either injected context or was fully suppressed.
	HookMatches      int `json:"hook_matches"`
	HookInjections   int `json:"hook_injections"`
	HookRepeated     int `json:"hook_repeated_injections"`
	HookSuppressed   int `json:"hook_suppressed"`
	HookContextBytes int `json:"hook_context_bytes"`
	// Transcript is where this trial's raw agent output was saved;
	// StderrLog and Patch keep the rest of the audit record, so a
	// verdict stays checkable after the trial dir is deleted.
	Transcript    string `json:"transcript,omitempty"`
	TranscriptSHA string `json:"transcript_sha256,omitempty"`
	StderrLog     string `json:"stderr,omitempty"`
	StderrSHA     string `json:"stderr_sha256,omitempty"`
	Patch         string `json:"patch,omitempty"`
	PatchSHA      string `json:"patch_sha256,omitempty"`
	// Checks are public repository-local validation commands. Hidden task and
	// invariant judges are represented by TaskDone and Avoided above.
	ChecksPass bool          `json:"checks_pass"`
	Checks     []CheckResult `json:"checks,omitempty"`
	// LessonFileRead: the agent named .seamark/lessons.yaml in its own
	// messages. In arms without a lesson file this must be false; in a
	// control arm it would mean contamination.
	LessonFileRead bool `json:"lesson_file_read,omitempty"`
	// Explored lists instance-selected files the agent named in its own
	// messages and tool calls, in first-mention order. It is diagnostic
	// evidence only and never affects a verdict.
	Explored            []string              `json:"explored,omitempty"`
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
	InitSeen            bool                  `json:"init_seen,omitempty"`
	ResultSeen          bool                  `json:"result_seen,omitempty"`
	Tools               []string              `json:"tools,omitempty"`
	Plugins             []string              `json:"plugins,omitempty"`
	MCPServers          []string              `json:"mcp_servers,omitempty"`
	SeamarkVersion      string                `json:"seamark_version,omitempty"`
	SeamarkSHA          string                `json:"seamark_sha256,omitempty"`
	AgentVersion        string                `json:"agent_version,omitempty"`
	Effort              string                `json:"effort,omitempty"`
	HookDelivery        HookDeliveryMode      `json:"hook_delivery,omitempty"`
	MaxBudgetUSD        float64               `json:"max_budget_usd,omitempty"`
	RuntimeID           string                `json:"runtime_id,omitempty"`
	Fingerprint         string                `json:"fingerprint,omitempty"`
}

// ModelUsage preserves provider-reported usage for every model involved in a
// session. Helper-model calls must never be mistaken for the primary model.
type ModelUsage struct {
	InputTokens         int64   `json:"input_tokens,omitempty"`
	CacheReadTokens     int64   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	OutputTokens        int64   `json:"output_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// Tally is one arm's aggregate.
type Tally struct {
	Attempted    int
	Ran          int
	Invalid      int
	Completed    int // trials where the task was done at all
	Avoided      int // completed trials where the owner invariant passed
	Firings      int // hook firing records across the arm's trials
	Matches      int
	Injections   int
	Repeated     int
	Suppressed   int
	ContextBytes int
	MeanInput    int64
}

// Summary is the whole run's outcome, per arm.
type Summary struct {
	Rows          []Row
	ByArm         map[Arm]Tally
	RunID         string
	Instance      string
	Rule          string
	StoppedReason string
}

// Lines renders the summary the way the RFC reports results: raw
// counts per arm, no statistics theater, plus the measured token cost
// of the injection when both arms reported usage.
func (s Summary) Lines() []string {
	on, off := s.ByArm[ArmHookOn], s.ByArm[ArmHookOff]
	rule := s.Rule
	if rule == "" {
		rule = "unknown rule"
	}

	out := []string{fmt.Sprintf(
		"%s — hook-on: %d/%d avoided (%d/%d completed); hook-off: %d/%d avoided (%d/%d completed)",
		rule, on.Avoided, on.Ran, on.Completed, on.Ran,
		off.Avoided, off.Ran, off.Completed, off.Ran,
	)}

	if on.MeanInput > 0 && off.MeanInput > 0 {
		out = append(out, fmt.Sprintf(
			"context processed — hook-on mean %d vs hook-off %d (%+d per trial)",
			on.MeanInput, off.MeanInput, on.MeanInput-off.MeanInput,
		))
	}

	if on.Matches > 0 {
		out = append(out, fmt.Sprintf(
			"hook delivery — %d matches, %d injections, %d repeated, %d suppressed, %d context bytes",
			on.Matches, on.Injections, on.Repeated, on.Suppressed, on.ContextBytes,
		))
	}

	// The optional arms report only when they ran.
	for _, arm := range []Arm{ArmFileOnly, ArmPlacebo} {
		if t := s.ByArm[arm]; t.Ran > 0 {
			out = append(out, fmt.Sprintf("%s — %d/%d avoided (%d/%d completed)",
				arm, t.Avoided, t.Ran, t.Completed, t.Ran))
		}
	}

	for _, arm := range []Arm{ArmHookOn, ArmHookOff, ArmFileOnly, ArmPlacebo} {
		if t := s.ByArm[arm]; t.Invalid > 0 {
			out = append(out, fmt.Sprintf("%s — %d invalid attempt(s) excluded", arm, t.Invalid))
		}
	}

	// A hooked arm whose trials never fired measured nothing: the
	// arms were identical and the counts above must not be read as a
	// pin effect.
	for _, arm := range []Arm{ArmHookOn, ArmPlacebo} {
		if t := s.ByArm[arm]; t.Ran > 0 && t.Firings == 0 {
			out = append(out, fmt.Sprintf("warning — the hook never fired in any %s trial; "+
				"that arm measured nothing (does the agent run project hooks headless?)", arm))
		}
	}

	if s.StoppedReason != "" {
		out = append(out, "stopped — "+s.StoppedReason)
	}

	return out
}

// firingNote renders the hook-firing count on hooked arms' progress
// lines.
func firingNote(arm Arm, row Row) string {
	if arm != ArmHookOn && arm != ArmPlacebo {
		return ""
	}

	if row.SchemaVersion >= 6 {
		return fmt.Sprintf("  hook=%d/%d repeat=%d suppressed=%d bytes=%d",
			row.HookInjections, row.HookMatches, row.HookRepeated,
			row.HookSuppressed, row.HookContextBytes)
	}

	return fmt.Sprintf("  hook×%d", row.HookFirings)
}

// flagNote surfaces the audit flags a reader must not miss: a broken
// build, and the agent reading the lesson file.
func flagNote(row Row) string {
	var out string

	if row.TaskDone && !row.ChecksPass {
		out += "  CHECKS-FAILED"
	}

	if row.LessonFileRead {
		out += "  lesson-file=read"
	}

	return out
}

// exploredNote says how many instance-selected files the agent named.
func exploredNote(row Row) string {
	if len(row.Explored) == 0 {
		return ""
	}

	return fmt.Sprintf("  explored=%d", len(row.Explored))
}

// Run executes the experiment: Trials fresh fixture repos per arm,
// alternating arms so slow model drift within the run spreads evenly,
// each judged mechanically. A pair is finalized and appended before
// the next pair starts; graceful cancellation also flushes a completed
// arm from a partially executed pair.
//
// Cancelling ctx stops the run cleanly between (or during) trials and
// returns the partial summary with a nil error, so an interrupted run
// still reports what it measured.
func Run(ctx context.Context, cfg RunConfig) (Summary, error) {
	instance := resolveInstance(cfg)
	if err := instance.Validate(); err != nil {
		return Summary{}, err
	}

	if cfg.Trials < 1 {
		return Summary{}, fmt.Errorf("trials must be at least 1")
	}
	if !knownHookDelivery(cfg.HookDelivery) {
		return Summary{}, fmt.Errorf("unknown hook delivery mode %q", cfg.HookDelivery)
	}

	if err := validateDeliveredInstance(cfg, instance); err != nil {
		return Summary{}, err
	}

	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}

	arms, err := resolvedArms(cfg.Arms)
	if err != nil {
		return Summary{}, err
	}

	if cfg.RunID == "" {
		cfg.RunID, err = newRunID()
		if err != nil {
			return Summary{}, err
		}
	}

	if !validArtifactToken(cfg.RunID) {
		return Summary{}, fmt.Errorf("run ID must contain only letters, digits, '.', '_', or '-'")
	}

	sum := Summary{
		ByArm: map[Arm]Tally{}, RunID: cfg.RunID, Instance: instance.ID, Rule: instance.Rule,
	}

	work := cfg.WorkDir
	createdWork := work == ""
	if createdWork {
		var err error

		work, err = os.MkdirTemp("", "seamark-bench-")
		if err != nil {
			return Summary{}, err
		}
	}

	if createdWork && !cfg.Keep {
		defer func() { _ = os.RemoveAll(work) }()
	}

	contexts := map[Arm][]int64{}

	stop := false
	stopForCancel := false
	var runErr error

	for trial := 1; trial <= cfg.Trials; trial++ {
		if ctx.Err() != nil {
			break
		}

		// Counterbalance execution order within pairs: with a fixed
		// order, slow model drift inside the run loads onto one arm.
		order := slices.Clone(arms)
		if trial%2 == 0 {
			slices.Reverse(order)
		}

		pairRows := make([]Row, 0, len(order))
		var pairErr error
		for _, arm := range order {
			if ctx.Err() != nil {
				stopForCancel = true

				break
			}

			row, err := runTrial(ctx, cfg, instance, work, arm, trial)
			if err != nil {
				// A trial killed by the interrupt is a stop, not a
				// failure.
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

			validNote := ""
			if !row.Valid || !row.PairValid {
				validNote = "  INVALID=" + row.InvalidReason
				if row.InvalidReason == "" {
					validNote = "  INVALID=paired arm failed"
				}
			}

			logf("trial %d %-9s  task=%-5v invariant=%-5v  %s%s%s%s%s%s",
				trial, row.Arm, row.TaskDone, row.Avoided, row.Notes,
				firingNote(row.Arm, row), exploredNote(row), flagNote(row), validNote, costNote(row))

			if cfg.Out != "" {
				if err := appendRow(cfg.Out, row); err != nil {
					return sum, err
				}
			}

			sum.Rows = append(sum.Rows, row)
			tallyRow(&sum, row, contexts)
		}

		if pairErr != nil {
			runErr = pairErr

			break
		}

		if stop || stopForCancel {
			break
		}
	}

	for arm, ins := range contexts {
		var total int64
		for _, n := range ins {
			total += n
		}

		t := sum.ByArm[arm]
		t.MeanInput = total / int64(len(ins))
		sum.ByArm[arm] = t
	}

	if sum.StoppedReason != "" {
		return sum, fmt.Errorf("benchmark stopped: %s", sum.StoppedReason)
	}

	if runErr != nil {
		return sum, runErr
	}

	return sum, nil
}

func resolvedArms(configured []Arm) ([]Arm, error) {
	arms := slices.Clone(configured)
	if len(arms) == 0 {
		arms = []Arm{ArmHookOn, ArmHookOff}
	}

	seen := make(map[Arm]bool, len(arms))
	for _, arm := range arms {
		if !knownArm(arm) {
			return nil, fmt.Errorf("unknown benchmark arm %q", arm)
		}

		if seen[arm] {
			return nil, fmt.Errorf("duplicate benchmark arm %q", arm)
		}

		seen[arm] = true
	}

	return arms, nil
}

func newRunID() (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate benchmark run ID: %w", err)
	}

	return hex.EncodeToString(data[:]), nil
}

func validArtifactToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}

	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}

		return false
	}

	return true
}

func tallyRow(sum *Summary, row Row, contexts map[Arm][]int64) {
	t := sum.ByArm[row.Arm]
	t.Attempted++
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

	t.Firings += row.HookFirings
	t.Matches += row.HookMatches
	t.Injections += row.HookInjections
	t.Repeated += row.HookRepeated
	t.Suppressed += row.HookSuppressed
	t.ContextBytes += row.HookContextBytes

	if row.ContextTokens > 0 {
		contexts[row.Arm] = append(contexts[row.Arm], row.ContextTokens)
	}

	sum.ByArm[row.Arm] = t
}

// fixtureHead returns the generated fixture's full HEAD commit;
// best-effort, "" when git is unavailable.
func fixtureHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// costNote renders the measured cost part of a progress line; empty
// when the agent reported no usage (stub agents, plain-text output).
func costNote(row Row) string {
	if row.ContextTokens == 0 && row.CostUSD == 0 {
		return ""
	}

	return fmt.Sprintf("  [fresh %dk, cache-read %dk, cache-write %dk, out %.1fk, $%.2f]",
		row.InputTokens/1000, row.CacheReadTokens/1000, row.CacheCreationTokens/1000,
		float64(row.OutputTokens)/1000, row.CostUSD)
}

// runTrial generates one fresh fixture, wires the arm, runs the agent
// on the frozen task, and judges the result.
func runTrial(ctx context.Context, cfg RunConfig, instance Instance, work string, arm Arm, trial int) (Row, error) {
	dir := filepath.Join(work, fmt.Sprintf("%s-%02d", arm, trial))
	if _, err := os.Lstat(dir); err == nil {
		return Row{}, fmt.Errorf("trial directory already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return Row{}, fmt.Errorf("inspect trial directory: %w", err)
	}

	// The absence check above establishes ownership: cleanup may remove this
	// exact path, but never the caller-provided parent or a pre-existing child.
	if !cfg.Keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}

	if err := instance.Generate(dir); err != nil {
		return Row{}, err
	}

	if err := wireArm(ctx, dir, cfg, instance, arm); err != nil {
		return Row{}, err
	}

	fixture := fixtureHead(dir)
	if fixture == "" {
		return Row{}, fmt.Errorf("trial fixture has no git HEAD")
	}

	row := Row{
		SchemaVersion:  ResultSchemaVersion,
		TS:             time.Now().UTC().Format(time.RFC3339),
		RunID:          cfg.RunID,
		Instance:       instance.ID,
		TaskSHA:        instance.TaskSHA(),
		Pin:            instance.Rule,
		Arm:            arm,
		Trial:          trial,
		Valid:          true,
		RequestedModel: cfg.Model,
		SeamarkVersion: cfg.Version,
		SeamarkSHA:     cfg.SeamarkSHA,
		AgentVersion:   cfg.AgentVersion,
		Effort:         cfg.Effort,
		HookDelivery:   effectiveHookDelivery(cfg),
		MaxBudgetUSD:   cfg.MaxBudgetUSD,
		RuntimeID:      cfg.RuntimeID,
		Fingerprint:    cfg.Fingerprint,
		Fixture:        fixture,
	}

	if cfg.TranscriptDir != "" {
		if err := os.MkdirAll(cfg.TranscriptDir, 0o700); err != nil {
			return Row{}, err
		}

		if err := ensureArtifactsAbsent(cfg.TranscriptDir, artifactBase(row)); err != nil {
			return Row{}, err
		}
	}

	stdout, stderr, exit, timedOut, err := runAgent(ctx, cfg, instance, dir)
	if err != nil {
		return Row{}, err
	}

	row.AgentExit = exit
	row.TimedOut = timedOut
	if timedOut {
		row.Notes = "; agent timed out"
	}

	// Transcript and stderr are saved before parsing, so even output
	// the parser cannot read stays available as evidence.
	if cfg.TranscriptDir != "" {
		base := artifactBase(row)

		if len(stdout) > 0 {
			path := filepath.Join(cfg.TranscriptDir, base+".jsonl")
			if err := writeArtifactExclusive(path, stdout); err != nil {
				return Row{}, err
			}

			row.Transcript = path
			row.TranscriptSHA = hashBytes(stdout)
		}

		if len(stderr) > 0 {
			path := filepath.Join(cfg.TranscriptDir, base+".stderr.log")
			if err := writeArtifactExclusive(path, stderr); err != nil {
				return Row{}, err
			}

			row.StderrLog = path
			row.StderrSHA = hashBytes(stderr)
		}
	}

	parseAgentOutput(stdout, &row, instance)
	validateAgentResult(cfg, &row)

	// The treatment must prove itself: the trial repo's own firing log
	// says whether the hook actually reached the agent.
	if arm == ArmHookOn || arm == ArmPlacebo {
		expectedYAML := instance.LessonYAML
		if arm == ArmPlacebo {
			expectedYAML = instance.PlaceboYAML
		}

		expectedPin, err := parseSinglePin(expectedYAML)
		if err != nil {
			return Row{}, err // Instance.Validate makes this unreachable.
		}

		intensity, err := matchingTreatmentFirings(dir, expectedPin)
		if err != nil {
			invalidateInfrastructure(&row, "cannot validate lesson hook: "+err.Error())
		} else {
			row.HookFirings = intensity.Injections
			row.HookAuditRows = intensity.AuditRows
			row.HookMatches = intensity.Matches
			row.HookInjections = intensity.Injections
			row.HookRepeated = intensity.Repeated
			row.HookSuppressed = intensity.Suppressed
			row.HookContextBytes = intensity.ContextBytes
		}

		if row.HookFirings == 0 && row.Valid {
			invalidateInfrastructure(&row, "selected lesson hook never fired for an in-region edit")
		}
	}

	v, err := instance.Judge(dir)
	if err != nil {
		return Row{}, err
	}

	row.TaskDone, row.Avoided = v.TaskDone, v.Avoided
	row.Notes = v.Notes + row.Notes

	row.Checks = runChecks(ctx, dir, instance.Checks)
	row.ChecksPass = checksPass(row.Checks)
	if row.TaskDone && !row.ChecksPass {
		row.TaskDone = false
		row.Avoided = false
		row.Notes += "; repository checks failed"
	}

	// The patch is the audit record: the verdict must stay checkable
	// after the trial dir is deleted.
	if cfg.TranscriptDir != "" {
		if patch := gitPatch(dir); len(patch) > 0 {
			name := artifactBase(row) + ".patch"
			path := filepath.Join(cfg.TranscriptDir, name)

			if err := writeArtifactExclusive(path, patch); err != nil {
				return Row{}, err
			}

			row.Patch = path
			row.PatchSHA = hashBytes(patch)
		}
	}

	return row, nil
}

func writeArtifactExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create benchmark artifact: %w", err)
	}

	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()

	if n, err := file.Write(data); err != nil {
		return fmt.Errorf("write benchmark artifact: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write benchmark artifact: %w", io.ErrShortWrite)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close benchmark artifact: %w", err)
	}

	complete = true

	return nil
}

func ensureArtifactsAbsent(dir, base string) error {
	for _, suffix := range []string{".jsonl", ".stderr.log", ".patch"} {
		path := filepath.Join(dir, base+suffix)

		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("benchmark artifact already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect benchmark artifact: %w", err)
		}
	}

	return nil
}

func artifactBase(row Row) string {
	return fmt.Sprintf("%s-%s-%s-%02d",
		artifactToken(row.Instance), row.RunID, row.Arm, row.Trial)
}

func artifactToken(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}

		return '_'
	}, value)
}

// gitPatch captures the agent's full change, new files included.
func gitPatch(dir string) []byte {
	if err := exec.Command("git", "-C", dir, "add", "-A").Run(); err != nil {
		return nil
	}

	out, err := exec.Command("git", "-C", dir, "diff", "--cached").Output()
	if err != nil {
		return nil
	}

	return out
}

// wireArm installs exactly the condition an arm measures: the lesson
// file for file-only, hook plus lesson for hook-on, hook plus neutral
// lesson for placebo, nothing for hook-off. The fixture itself never
// carries these — a committed lesson is readable by every arm's
// agent, which contaminates the control.
func wireArm(ctx context.Context, dir string, cfg RunConfig, instance Instance, arm Arm) error {
	if !knownArm(arm) {
		return fmt.Errorf("unknown benchmark arm %q", arm)
	}

	if err := excludeHarnessArtifacts(dir); err != nil {
		return err
	}

	hookBin, err := installHarnessBinary(dir, cfg)
	if err != nil {
		return err
	}

	switch arm {
	case ArmHookOff:
		return writeAgentSettings(dir, "", "")
	case ArmFileOnly:
		if err := writeLessons(dir, lessonsForDelivery(cfg, instance.LessonYAML)); err != nil {
			return err
		}

		return writeAgentSettings(dir, "", "")
	case ArmPlacebo:
		if err := writeLessons(dir, lessonsForDelivery(cfg, instance.PlaceboYAML)); err != nil {
			return err
		}

		return wireHook(ctx, dir, cfg, hookBin)
	case ArmHookOn:
		if err := writeLessons(dir, lessonsForDelivery(cfg, instance.LessonYAML)); err != nil {
			return err
		}

		return wireHook(ctx, dir, cfg, hookBin)
	default:
		return fmt.Errorf("unknown benchmark arm %q", arm)
	}
}

func knownArm(arm Arm) bool {
	return arm == ArmHookOff || arm == ArmFileOnly ||
		arm == ArmPlacebo || arm == ArmHookOn
}

func effectiveHookDelivery(cfg RunConfig) HookDeliveryMode {
	if cfg.HookDelivery == "" {
		return HookDeliveryAlways
	}

	return cfg.HookDelivery
}

func knownHookDelivery(mode HookDeliveryMode) bool {
	return mode == "" || mode == HookDeliveryAlways || mode == HookDeliveryOncePerContext
}

// validateDeliveredInstance applies the selected delivery policy before
// validating both lesson documents. This is the exact YAML written into paid
// treatment arms, so policy injection cannot turn an otherwise valid fixture
// into an ambiguous or malformed configuration after preflight.
func validateDeliveredInstance(cfg RunConfig, instance Instance) error {
	delivered := instance
	delivered.LessonYAML = lessonsForDelivery(cfg, instance.LessonYAML)
	delivered.PlaceboYAML = lessonsForDelivery(cfg, instance.PlaceboYAML)

	if err := delivered.Validate(); err != nil {
		return fmt.Errorf("validate delivered lessons: %w", err)
	}

	return nil
}

// matchingTreatmentFirings distinguishes treatment delivery from ambient
// audit activity. A valid firing must come from the edit hook, name the exact
// installed pin, identify an edit tool, and target a file inside that pin's
// configured region. An unrelated pin or another Seamark surface is evidence
// that Seamark ran, but not that this trial received its treatment.
type hookIntensity struct {
	Matches      int
	Injections   int
	Repeated     int
	Suppressed   int
	ContextBytes int
	AuditRows    int
}

func matchingTreatmentFirings(dir string, pin reviews.PinRule) (hookIntensity, error) {
	expected := reviews.PinIdentity(pin)
	firings, err := reviews.ReadFirings(dir)
	if err != nil {
		return hookIntensity{}, err
	}

	type matchState struct {
		injected, suppressed bool
	}

	matches := make(map[string]matchState)
	seen := make(map[string]bool)
	result := hookIntensity{AuditRows: len(firings)}

	for i, firing := range firings {
		if firing.Surface != "" || !editTool(firing.Tool) ||
			firing.File == "" || len(firing.Files) != 0 ||
			!pinAppliesToFile(pin, firing.File) {
			continue
		}

		matched := false
		for _, fired := range firing.Fired {
			if canonicalFiredLesson(fired) == expected {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		matchKey := firing.MatchSHA
		if matchKey == "" {
			matchKey = fmt.Sprintf("legacy-row-%d", i)
		}
		state := matches[matchKey]

		switch {
		case firing.Delivered():
			if !state.injected {
				state.injected = true
				result.Injections++
				result.ContextBytes += firing.ContextBytes

				seenKey := fmt.Sprintf("%s\x00%d\x00%s\x00%s",
					firing.SessionSHA, firing.Generation, expected.Region, expected.Symptom)
				if firing.SessionSHA != "" && seen[seenKey] {
					result.Repeated++
				}
				if firing.SessionSHA != "" {
					seen[seenKey] = true
				}
			}
		case firing.Delivery == reviews.DeliverySuppressedRepeat:
			state.suppressed = true
		}

		matches[matchKey] = state
	}

	result.Matches = len(matches)
	for _, state := range matches {
		if state.suppressed && !state.injected {
			result.Suppressed++
		}
	}

	return result, nil
}

func editTool(tool string) bool {
	return tool == "Edit" || tool == "Write" || tool == "MultiEdit"
}

func pinAppliesToFile(pin reviews.PinRule, file string) bool {
	regions := pin.AllRegions()
	if len(regions) == 0 {
		return true
	}

	for _, region := range regions {
		if reviews.RegionMatches(region, file) {
			return true
		}
	}

	return false
}

func canonicalFiredLesson(fired reviews.FiredLesson) reviews.FiredLesson {
	regions := strings.Split(fired.Region, ", ")
	slices.Sort(regions)

	return reviews.FiredLesson{
		Region:  strings.Join(regions, ", "),
		Symptom: strings.ToLower(strings.TrimSpace(fired.Symptom)),
	}
}

func excludeHarnessArtifacts(dir string) error {
	path := filepath.Join(dir, ".git", "info", "exclude")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = f.WriteString("\n# benchmark runtime\n.claude/\n.seamark/\n.bench-cache/\n")

	return err
}

// installHarnessBinary places the exact measured Seamark binary inside the
// trial boundary. Hooks no longer need to execute a binary from the host
// workspace, which would violate the sandbox's read boundary. Hermetic unit
// tests disable index preparation and keep using their inert fake path.
func installHarnessBinary(dir string, cfg RunConfig) (string, error) {
	if !cfg.PrepareIndex {
		return cfg.SeamarkBin, nil
	}

	data, err := os.ReadFile(cfg.SeamarkBin)
	if err != nil {
		return "", err
	}

	// .seamark is already excluded from Seamark's own code index, so the
	// executable cannot distort routing or fixture size.
	rel := filepath.Join(".seamark", "bin", "seamark")
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}

	if err := os.WriteFile(abs, data, 0o755); err != nil {
		return "", err
	}

	return rel, nil
}

// writeLessons installs a lessons.yaml into the trial repo.
func writeLessons(dir, content string) error {
	seamarkDir := filepath.Join(dir, ".seamark")
	if err := os.MkdirAll(seamarkDir, 0o755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(seamarkDir, "lessons.yaml"), []byte(content), 0o644)
}

func lessonsForDelivery(cfg RunConfig, content string) string {
	if effectiveHookDelivery(cfg) == HookDeliveryOncePerContext {
		return "hook_delivery: once-per-context\n" + content
	}

	return content
}

// wireHook writes the trial repo's Claude Code settings with the same
// PreToolUse hook `seamark init` installs, and optionally indexes the
// fixture so the hook has a store to read (the hook opens the index;
// without one it stays silent and the arm would silently equal
// hook-off).
func wireHook(ctx context.Context, dir string, cfg RunConfig, hookBin string) error {
	resetCommand := ""
	if effectiveHookDelivery(cfg) == HookDeliveryOncePerContext {
		resetCommand = fmt.Sprintf("%q lessons --hook-reset", hookBin)
	}

	if err := writeAgentSettings(dir, fmt.Sprintf("%q lessons --hook", hookBin), resetCommand); err != nil {
		return err
	}

	if !cfg.PrepareIndex {
		return nil
	}

	setupCtx, cancel := context.WithTimeout(ctx, defaultSetupTimeout)
	defer cancel()

	cmd := exec.CommandContext(setupCtx, cfg.SeamarkBin, "index")
	cmd.Dir = dir
	cmd.Env = agentEnvironment(dir)
	cmd.WaitDelay = processWaitDelay

	if out, err := cmd.CombinedOutput(); err != nil {
		if setupCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("seamark index in fixture timed out after %s", defaultSetupTimeout)
		}

		return fmt.Errorf("seamark index in fixture: %w\n%s", err, out)
	}

	return nil
}

// writeAgentSettings gives every arm the same clean, strict runtime. Hooked
// arms differ only by the PreToolUse entry added here and their lesson file.
// The sandbox is a hard gate: if OS-level enforcement is unavailable Claude
// must fail the session instead of silently running commands on the host.
func writeAgentSettings(dir, hookCommand, resetCommand string) error {
	settings := map[string]any{
		"autoMemoryEnabled": false,
		"permissions": map[string]any{
			"deny": []string{"WebFetch", "WebSearch"},
		},
		"sandbox": map[string]any{
			"enabled":                  true,
			"failIfUnavailable":        true,
			"autoAllowBashIfSandboxed": true,
			"allowUnsandboxedCommands": false,
			"filesystem": map[string]any{
				"denyRead":  []string{"~/"},
				"allowRead": []string{"."},
			},
			"network": map[string]any{
				"allowedDomains": []string{},
			},
		},
	}

	if hookCommand != "" {
		hookSettings := map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Edit|Write|MultiEdit",
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": hookCommand,
				}},
			}},
		}
		if resetCommand != "" {
			hookSettings["PostCompact"] = []any{map[string]any{
				"hooks": []any{map[string]any{
					"type":          "command",
					"command":       resetCommand,
					"timeout":       10,
					"statusMessage": "seamark: resetting lesson delivery",
				}},
			}}
		}
		settings["hooks"] = hookSettings
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644)
}

// runAgent runs the configured agent command in the trial dir with
// the task appended as the last argument. A non-zero exit is a result
// (recorded on the row), not an error: some agents exit non-zero on
// partial work, and the judge reads the files either way.
func runAgent(parent context.Context, cfg RunConfig, instance Instance, dir string) (stdout, stderr []byte, exit int, timedOut bool, err error) {
	if len(cfg.AgentArgv) == 0 {
		return nil, nil, 0, false, fmt.Errorf("empty agent command")
	}

	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()

	argv := append(slices.Clone(cfg.AgentArgv), instance.Task)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = agentEnvironment(dir)
	cmd.WaitDelay = processWaitDelay

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()

	// A timeout is a recorded outcome, not a run failure: the paired
	// arm must still run, or slow trials silently bias the series.
	if ctx.Err() == context.DeadlineExceeded {
		return stdout, stderr, -1, true, nil
	}

	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return stdout, stderr, ee.ExitCode(), false, nil
		}

		return stdout, stderr, -1, false, runErr
	}

	return stdout, stderr, 0, false, nil
}

// inheritedEnvironmentBlocklist contains host settings that can change fixture
// generation, agent behavior, or validation. Every benchmark subprocess starts
// from the same filtered environment; individual commands then add only their
// explicitly owned settings.
var inheritedEnvironmentBlocklist = map[string]bool{
	"ANTHROPIC_MODEL":                true,
	"ANTHROPIC_DEFAULT_OPUS_MODEL":   true,
	"ANTHROPIC_DEFAULT_SONNET_MODEL": true,
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":  true,
	"CLAUDE_CODE_EFFORT_LEVEL":       true,
	"CLAUDE_CONFIG_DIR":              true,
	// Language and shell startup injection makes the same fixture behave
	// differently depending on the operator's workstation.
	"PYTHONPATH":              true,
	"PYTHONHOME":              true,
	"PYTHONSTARTUP":           true,
	"PYTHONUSERBASE":          true,
	"PYTHONINSPECT":           true,
	"PYTHONWARNINGS":          true,
	"PYTHONBREAKPOINT":        true,
	"PYTHONPLATLIBDIR":        true,
	"PYTHONEXECUTABLE":        true,
	"PYTHONPYCACHEPREFIX":     true,
	"PYTHONDONTWRITEBYTECODE": true,
	"VIRTUAL_ENV":             true,
	"PIP_CONFIG_FILE":         true,
	"BASH_ENV":                true,
	"ENV":                     true,
	"CDPATH":                  true,
	"GLOBIGNORE":              true,
	"MAKEFLAGS":               true,
	"MFLAGS":                  true,
	"MAKEFILES":               true,
	"GIT_DIR":                 true,
	"GIT_WORK_TREE":           true,
	"GIT_INDEX_FILE":          true,
	"GIT_AUTHOR_NAME":         true,
	"GIT_AUTHOR_EMAIL":        true,
	"GIT_AUTHOR_DATE":         true,
	"GIT_COMMITTER_NAME":      true,
	"GIT_COMMITTER_EMAIL":     true,
	"GIT_COMMITTER_DATE":      true,
	"GIT_CONFIG_GLOBAL":       true,
	"GIT_CONFIG_SYSTEM":       true,
	"GIT_CONFIG_NOSYSTEM":     true,
	"TMPDIR":                  true,
	"GOCACHE":                 true,
	"GOMODCACHE":              true,
	"GOPATH":                  true,
	"GOTOOLCHAIN":             true,
	"GOPROXY":                 true,
	"XDG_CACHE_HOME":          true,
	"npm_config_cache":        true,
	"PIP_CACHE_DIR":           true,
	"PYTHONNOUSERSITE":        true,
	"PYTHONHASHSEED":          true,
	"PYTHONUTF8":              true,
}

func sanitizedEnvironment() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !inheritedEnvironmentBlocklist[name] && !strings.HasPrefix(name, "GIT_CONFIG_") {
			env = append(env, entry)
		}
	}

	return env
}

func agentEnvironment(dir string) []string {
	cache := filepath.Join(dir, ".bench-cache")
	for _, sub := range []string{"tmp", "go-build", "go-mod", "go-path", "xdg", "npm", "pip"} {
		_ = os.MkdirAll(filepath.Join(cache, sub), 0o755)
	}

	env := sanitizedEnvironment()

	env = append(
		env,
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=1",
		"TMPDIR="+filepath.Join(cache, "tmp"),
		"GOCACHE="+filepath.Join(cache, "go-build"),
		"GOMODCACHE="+filepath.Join(cache, "go-mod"),
		"GOPATH="+filepath.Join(cache, "go-path"),
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"XDG_CACHE_HOME="+filepath.Join(cache, "xdg"),
		"npm_config_cache="+filepath.Join(cache, "npm"),
		"PIP_CACHE_DIR="+filepath.Join(cache, "pip"),
		"PYTHONNOUSERSITE=1",
		"PYTHONHASHSEED=0",
		"PYTHONUTF8=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
	)

	return env
}

// parseAgentOutput extracts runtime identity, usage, infrastructure errors,
// and exploration from the agent's stdout. It understands a single JSON object
// (--output-format json) and stream-json (one object per line with a
// final "result" line). Best-effort: a stub or plain-text agent
// leaves the fields zero, which the summary then omits.
func parseAgentOutput(stdout []byte, row *Row, instance Instance) {
	if parseResult(stdout, row) {
		return
	}

	seen := map[string]bool{}

	for line := range strings.SplitSeq(string(stdout), "\n") {
		var probe struct {
			Type string `json:"type"`
		}

		if json.Unmarshal([]byte(line), &probe) != nil {
			continue
		}

		switch probe.Type {
		case "system":
			parseInit([]byte(line), row)
		case "rate_limit_event":
			parseRateLimit([]byte(line), row)
		case "result":
			parseResult([]byte(line), row)
		case "assistant":
			// Only the agent's own messages and tool calls count as
			// exploration; tool results (type "user") would credit it
			// with files it merely received.
			for _, f := range instance.ExploreFiles {
				if strings.Contains(line, f) && !seen[f] {
					seen[f] = true
					row.Explored = append(row.Explored, f)
				}
			}

			if strings.Contains(line, "lessons.yaml") {
				row.LessonFileRead = true
			}
		}
	}
}

func parseInit(line []byte, row *Row) {
	var init struct {
		Type       string   `json:"type"`
		Subtype    string   `json:"subtype"`
		Model      string   `json:"model"`
		Tools      []string `json:"tools"`
		MCPServers []string `json:"mcp_servers"`
		Plugins    []struct {
			Name string `json:"name"`
		} `json:"plugins"`
	}
	if json.Unmarshal(line, &init) != nil || init.Type != "system" || init.Subtype != "init" {
		return
	}

	row.InitSeen = true
	row.Model = init.Model
	row.Tools = slices.Clone(init.Tools)
	row.MCPServers = slices.Clone(init.MCPServers)
	for _, plugin := range init.Plugins {
		row.Plugins = append(row.Plugins, plugin.Name)
	}
}

func parseRateLimit(line []byte, row *Row) {
	var event struct {
		Info struct {
			Status        string `json:"status"`
			OverageStatus string `json:"overageStatus"`
		} `json:"rate_limit_info"`
	}

	if json.Unmarshal(line, &event) != nil {
		return
	}

	// Status describes whether the current session is allowed. OverageStatus
	// only says whether paid overage would be available after the normal quota
	// is exhausted; it may be rejected during an otherwise successful session.
	if event.Info.Status == "rejected" {
		invalidateInfrastructure(row, "agent provider rate limit")
	}
}

// parseResult reads one claude result object into the row, reporting
// whether it carried anything usable.
func parseResult(stdout []byte, row *Row) bool {
	var res struct {
		Type              string            `json:"type"`
		Model             string            `json:"model"`
		DurationMS        int64             `json:"duration_ms"`
		CostUSD           float64           `json:"total_cost_usd"`
		Turns             int               `json:"num_turns"`
		IsError           bool              `json:"is_error"`
		APIErrorStatus    int               `json:"api_error_status"`
		Result            string            `json:"result"`
		PermissionDenials []json.RawMessage `json:"permission_denials"`
		ModelUsage        map[string]struct {
			InputTokens         int64   `json:"inputTokens"`
			OutputTokens        int64   `json:"outputTokens"`
			CacheReadTokens     int64   `json:"cacheReadInputTokens"`
			CacheCreationTokens int64   `json:"cacheCreationInputTokens"`
			CostUSD             float64 `json:"costUSD"`
		} `json:"modelUsage"`
		Usage struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(stdout, &res); err != nil {
		return false
	}

	if res.Type != "" && res.Type != "result" {
		return false
	}

	if res.Type == "" && res.Model == "" && len(res.ModelUsage) == 0 &&
		res.DurationMS == 0 && res.CostUSD == 0 {
		return false
	}

	row.ResultSeen = true
	if row.Model == "" && res.Model != "" {
		row.Model = res.Model
	}
	if row.Model == "" && len(res.ModelUsage) == 1 {
		for model := range res.ModelUsage {
			row.Model = model
		}
	}

	row.DurationMS = res.DurationMS
	row.CostUSD = res.CostUSD
	row.Turns = res.Turns
	row.PermissionDenials = len(res.PermissionDenials)
	row.InputTokens = res.Usage.Input
	row.CacheReadTokens = res.Usage.CacheRead
	row.CacheCreationTokens = res.Usage.CacheCreation
	row.ContextTokens = res.Usage.Input + res.Usage.CacheRead + res.Usage.CacheCreation
	row.OutputTokens = res.Usage.Output

	if len(res.ModelUsage) > 0 {
		row.ModelUsage = make(map[string]ModelUsage, len(res.ModelUsage))

		for model, usage := range res.ModelUsage {
			row.ModelUsage[model] = ModelUsage{
				InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
				CacheReadTokens:     usage.CacheReadTokens,
				CacheCreationTokens: usage.CacheCreationTokens,
				CostUSD:             usage.CostUSD,
			}
		}
	}

	row.AgentError = res.IsError
	if res.APIErrorStatus >= 400 {
		invalidateInfrastructure(row,
			fmt.Sprintf("agent provider error HTTP %d", res.APIErrorStatus))
	}

	return true
}

func validateAgentResult(cfg RunConfig, row *Row) {
	if !row.Valid {
		return
	}

	if cfg.RequireCleanInit && !row.InitSeen {
		invalidateInfrastructure(row, "agent produced no initialization record")

		return
	}

	if cfg.RequireCleanInit && len(row.Plugins) > 0 {
		invalidateInfrastructure(row, "unexpected agent plugins loaded")

		return
	}

	if cfg.RequireCleanInit && len(row.MCPServers) > 0 {
		invalidateInfrastructure(row, "unexpected MCP servers loaded")

		return
	}

	if cfg.RequireCleanInit {
		allowed := map[string]bool{"Read": true, "Edit": true, "Write": true, "Bash": true}
		seen := make(map[string]bool, len(row.Tools))

		if len(row.Tools) == 0 {
			invalidateInfrastructure(row, "agent initialized without the required tool set")

			return
		}

		for _, tool := range row.Tools {
			if !allowed[tool] {
				invalidateInfrastructure(row, "unexpected agent tool loaded: "+tool)

				return
			}
			seen[tool] = true
		}

		for tool := range allowed {
			if !seen[tool] {
				invalidateInfrastructure(row, "required agent tool missing: "+tool)

				return
			}
		}
	}
	// A deadline is an experimental outcome. Claude's stream normally has an
	// init record but no final result when the process is killed; requiring the
	// latter would misclassify agent slowness as provider infrastructure.
	if cfg.RequireStructuredResult && !row.ResultSeen && !row.TimedOut {
		invalidateInfrastructure(row, "agent produced no structured result")

		return
	}

	if cfg.Model != "" && !modelMatches(cfg.Model, row.Model) {
		invalidateInfrastructure(row,
			fmt.Sprintf("requested model %q but agent initialized %q", cfg.Model, row.Model))
	}
}

func modelMatches(requested, actual string) bool {
	return requested == actual || strings.HasPrefix(actual, requested+"[")
}

func invalidateInfrastructure(row *Row, reason string) {
	row.Valid = false
	row.InfrastructureFailure = true
	if row.InvalidReason == "" {
		row.InvalidReason = reason
	}
}

// ReadRows reads a results JSONL file back, skipping unparseable
// lines. A missing file is an empty history, not an error.
func ReadRows(path string) ([]Row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	var out []Row

	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}

		out = append(out, r)
	}

	return out, nil
}

// appendRow appends one JSONL row, creating the file and its parent
// directory on first use.
func appendRow(path string, row Row) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(row)
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
