package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// Instance is one immutable benchmark problem. The runner deliberately knows
// nothing about the fixture's language or mistake class: generation, judging,
// and verification all live here so additional instances do not fork the
// experimental harness.
type Instance struct {
	ID          string
	Rule        string
	Task        string
	LessonYAML  string
	PlaceboYAML string

	Generate  func(string) error
	Judge     func(string) (Verdict, error)
	ApplyGold func(string) error
	// ApplyNaive installs a task-complete solution that deliberately omits the
	// owner invariant. Preflight uses it to prove that the two judges actually
	// discriminate the failure mode the experiment claims to measure.
	ApplyNaive func(string) error
	// JudgeVersion must change whenever verdict semantics change. It binds
	// persisted rows to the exact interpretation used by this instance.
	JudgeVersion string
	Checks       []Command

	// ExploreFiles are repository-relative paths whose appearance in an
	// assistant message is useful diagnostic evidence. They do not affect the
	// verdict.
	ExploreFiles []string
}

// Verdict is one trial's deterministic judgment, read from the code the agent
// left behind rather than inferred from its transcript.
type Verdict struct {
	// TaskDone means the agent completed the visible task.
	TaskDone bool
	// Avoided means a task-complete solution also preserved the owner invariant.
	Avoided bool
	// Notes concisely explains the deterministic judgment.
	Notes string
}

// Command is a deterministic repository-local validation command. Hidden
// task/invariant judges stay in Go; these commands answer whether the tree the
// agent left behind still builds and passes its public checks.
type Command struct {
	Name    string
	Args    []string
	Timeout time.Duration
}

// CheckResult is the persisted result of one validation command.
type CheckResult struct {
	Command  string `json:"command"`
	Pass     bool   `json:"pass"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Output   string `json:"output,omitempty"`
}

const (
	defaultCheckTimeout = 2 * time.Minute
	defaultSetupTimeout = 2 * time.Minute
	processWaitDelay    = 2 * time.Second
)

func (c Command) String() string {
	if len(c.Args) == 0 {
		return c.Name
	}

	return c.Name + " " + strings.Join(c.Args, " ")
}

func (c Command) run(ctx context.Context, dir string) CheckResult {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, c.Name, c.Args...)
	cmd.Dir = dir
	cmd.Env = agentEnvironment(dir)
	cmd.WaitDelay = processWaitDelay

	out, err := cmd.CombinedOutput()
	result := CheckResult{
		Command: c.String(), Pass: err == nil,
		TimedOut: checkCtx.Err() == context.DeadlineExceeded,
	}
	if err != nil {
		result.Output = string(out)
		if result.Output == "" {
			result.Output = err.Error()
		}
	}

	return result
}

// Validate rejects incomplete instances before they can spend an agent call.
func (i Instance) Validate() error {
	switch {
	case i.ID == "":
		return fmt.Errorf("benchmark instance has no ID")
	case i.Rule == "":
		return fmt.Errorf("benchmark instance %q has no rule", i.ID)
	case i.Task == "":
		return fmt.Errorf("benchmark instance %q has no task", i.ID)
	case i.LessonYAML == "":
		return fmt.Errorf("benchmark instance %q has no lesson", i.ID)
	case i.PlaceboYAML == "":
		return fmt.Errorf("benchmark instance %q has no placebo lesson", i.ID)
	case i.Generate == nil:
		return fmt.Errorf("benchmark instance %q has no generator", i.ID)
	case i.Judge == nil:
		return fmt.Errorf("benchmark instance %q has no judge", i.ID)
	case i.ApplyGold == nil:
		return fmt.Errorf("benchmark instance %q has no canonical patch", i.ID)
	case i.ApplyNaive == nil:
		return fmt.Errorf("benchmark instance %q has no naive patch", i.ID)
	case i.JudgeVersion == "":
		return fmt.Errorf("benchmark instance %q has no judge version", i.ID)
	case len(i.Checks) == 0:
		return fmt.Errorf("benchmark instance %q has no validation commands", i.ID)
	}

	treatment, err := parseSinglePin(i.LessonYAML)
	if err != nil {
		return fmt.Errorf("benchmark instance %q treatment: %w", i.ID, err)
	}
	if treatment.Rule != i.Rule {
		return fmt.Errorf("benchmark instance %q treatment rule %q does not match %q",
			i.ID, treatment.Rule, i.Rule)
	}

	placebo, err := parseSinglePin(i.PlaceboYAML)
	if err != nil {
		return fmt.Errorf("benchmark instance %q placebo: %w", i.ID, err)
	}
	if reviews.PinIdentity(treatment) == reviews.PinIdentity(placebo) {
		return fmt.Errorf("benchmark instance %q placebo duplicates the treatment", i.ID)
	}

	return nil
}

func parseSinglePin(data string) (reviews.PinRule, error) {
	var cfg reviews.Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		return reviews.PinRule{}, err
	}
	if len(cfg.Pin) != 1 {
		return reviews.PinRule{}, fmt.Errorf("must define exactly one pin, got %d", len(cfg.Pin))
	}
	if strings.TrimSpace(cfg.Pin[0].Rule) == "" {
		return reviews.PinRule{}, fmt.Errorf("pin rule must not be empty")
	}

	return cfg.Pin[0], nil
}

// TaskSHA identifies the exact problem statement without copying it into every
// result row.
func (i Instance) TaskSHA() string {
	sum := sha256.Sum256([]byte(i.Task))

	return hex.EncodeToString(sum[:])
}

// InstanceByID resolves the stable CLI-facing benchmark catalogue. Instances
// are constructed on demand so callers can safely customize a returned value
// without mutating shared state.
func InstanceByID(id string) (Instance, error) {
	switch id {
	case "", SchemaSyncInstanceID:
		return SchemaSyncInstance(), nil
	default:
		return Instance{}, fmt.Errorf("unknown benchmark instance %q (available: %s)",
			id, SchemaSyncInstanceID)
	}
}

func resolveInstance(cfg RunConfig) Instance {
	if cfg.Instance.ID != "" {
		return cfg.Instance
	}

	return SchemaSyncInstance()
}
