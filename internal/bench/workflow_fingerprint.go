package bench

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/seamark-dev/seamark/internal/skills"
)

// workflowSources makes changes to workflow trial execution and validation
// part of the run identity. The files are named one by one, so a test or the
// report cannot move the fingerprint. The lessons harness sources are hashed
// separately, because the workflow runner reuses their plumbing.
//
//go:embed adapter.go workflow_run.go workflow_trace.go workflow_results.go workflow_preflight.go workflow_fingerprint.go workflow_activation.go
var workflowSources embed.FS

// WorkflowFingerprint binds cost estimates and result pooling to one
// instance, arm assignment, agent configuration, runtime, Seamark binary,
// and skills tree. The agent commands are hashed, never persisted, because
// custom adapters may carry sensitive arguments.
func WorkflowFingerprint(cfg WorkflowConfig) (string, error) {
	instance := cfg.Instance
	if err := instance.Validate(); err != nil {
		return "", err
	}

	arms, err := resolvedWorkflowArms(cfg.Arms)
	if err != nil {
		return "", err
	}

	type checkIdentity struct {
		Name      string   `json:"name"`
		Args      []string `json:"args"`
		TimeoutMS int64    `json:"timeout_ms"`
	}

	checks := make([]checkIdentity, 0, len(instance.Checks))
	for _, check := range instance.Checks {
		timeout := check.Timeout
		if timeout <= 0 {
			timeout = defaultCheckTimeout
		}

		checks = append(checks, checkIdentity{
			Name: check.Name, Args: check.Args, TimeoutMS: timeout.Milliseconds(),
		})
	}

	commands := make(map[WorkflowArm]string, len(cfg.AgentArgv))
	for _, arm := range slices.Sorted(maps.Keys(cfg.AgentArgv)) {
		commands[arm], err = hashJSON(cfg.AgentArgv[arm])
		if err != nil {
			return "", err
		}
	}

	material, err := fingerprintInstance(instance.Instance)
	if err != nil {
		return "", err
	}

	harnessSHA, err := fingerprintHarnessSources()
	if err != nil {
		return "", err
	}

	workflowSHA, err := fingerprintWorkflowSources()
	if err != nil {
		return "", err
	}

	instanceSourceSHA, err := fingerprintInstanceSource(instance.Instance)
	if err != nil {
		return "", err
	}

	skillsSHA, err := fingerprintSkillsTree()
	if err != nil {
		return "", err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	payload := struct {
		Schema            int                    `json:"schema"`
		Instance          string                 `json:"instance"`
		Trigger           string                 `json:"trigger"`
		Companion         string                 `json:"companion"`
		TaskSHA           string                 `json:"task_sha256"`
		FixtureHEAD       string                 `json:"fixture_head"`
		GoldPatchSHA      string                 `json:"gold_patch_sha256"`
		NaivePatchSHA     string                 `json:"naive_patch_sha256"`
		JudgeVersion      string                 `json:"judge_version"`
		HarnessSourceSHA  string                 `json:"harness_source_sha256"`
		WorkflowSourceSHA string                 `json:"workflow_source_sha256"`
		InstanceSourceSHA string                 `json:"instance_source_sha256"`
		SkillsTreeSHA     string                 `json:"skills_tree_sha256"`
		Checks            []checkIdentity        `json:"checks"`
		AgentCommands     map[WorkflowArm]string `json:"agent_command_sha256"`
		AgentVersion      string                 `json:"agent_version"`
		Model             string                 `json:"model"`
		Effort            string                 `json:"effort"`
		MaxBudgetUSD      float64                `json:"max_budget_usd"`
		TimeoutMS         int64                  `json:"timeout_ms"`
		RuntimeID         string                 `json:"runtime_id"`
		SeamarkVersion    string                 `json:"seamark_version"`
		SeamarkSHA        string                 `json:"seamark_sha256"`
		Arms              []WorkflowArm          `json:"arms"`
		MaxTurns          int                    `json:"max_turns"`
		PrepareIndex      bool                   `json:"prepare_index"`
		StructuredResult  bool                   `json:"require_structured_result"`
		ExpectedInit      bool                   `json:"require_expected_init"`
	}{
		Schema: 1, Instance: instance.ID, Trigger: instance.Trigger, Companion: instance.Companion,
		TaskSHA: instance.TaskSHA(), FixtureHEAD: material.fixtureHEAD,
		GoldPatchSHA: material.goldPatchSHA, NaivePatchSHA: material.naivePatchSHA,
		JudgeVersion: instance.JudgeVersion, HarnessSourceSHA: harnessSHA,
		WorkflowSourceSHA: workflowSHA, InstanceSourceSHA: instanceSourceSHA, SkillsTreeSHA: skillsSHA,
		Checks: checks, AgentCommands: commands, AgentVersion: cfg.AgentVersion,
		Model: cfg.Model, Effort: cfg.Effort, MaxBudgetUSD: cfg.MaxBudgetUSD,
		TimeoutMS: timeout.Milliseconds(), RuntimeID: cfg.RuntimeID,
		SeamarkVersion: cfg.Version, SeamarkSHA: cfg.SeamarkSHA, Arms: arms,
		// The turn cap bounds whether a skill can load at all, so activation
		// runs with different caps are different experiments.
		MaxTurns: cfg.MaxTurns, PrepareIndex: cfg.PrepareIndex,
		StructuredResult: cfg.RequireStructuredResult, ExpectedInit: cfg.RequireExpectedInit,
	}

	return hashJSON(payload)
}

func fingerprintWorkflowSources() (string, error) {
	entries, err := workflowSources.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("read embedded workflow sources: %w", err)
	}

	h := sha256.New()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := workflowSources.ReadFile(entry.Name())
		if err != nil {
			return "", fmt.Errorf("read embedded workflow source %s: %w", entry.Name(), err)
		}

		_, _ = h.Write([]byte(entry.Name()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// fingerprintSkillsTree hashes every shipped skill file by name, path, and
// content. The skills arm installs exactly these bytes, so a changed skill
// body is a different treatment.
func fingerprintSkillsTree() (string, error) {
	names, err := skills.Names()
	if err != nil {
		return "", fmt.Errorf("list embedded skills: %w", err)
	}

	h := sha256.New()
	for _, name := range names {
		files, err := skills.Files(name)
		if err != nil {
			return "", fmt.Errorf("read embedded skill %s: %w", name, err)
		}

		for _, rel := range slices.Sorted(maps.Keys(files)) {
			_, _ = h.Write([]byte(name))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(rel))
			_, _ = h.Write([]byte{0})
			_, _ = h.Write(files[rel])
			_, _ = h.Write([]byte{0})
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
