package bench

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// harnessSources makes changes to the benchmark implementation part of the
// run identity even before a release version is bumped. SeamarkSHA separately
// binds rows to the exact executable used by the hook.
//
//go:embed *.go
var harnessSources embed.FS

// FileSHA256 returns the digest used to bind result rows to the exact Seamark
// executable that served their hooks.
func FileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

// Fingerprint binds cost estimates and result pooling to one task, agent
// configuration, runtime, and Seamark binary. The command itself is hashed,
// never persisted, because custom adapters may carry sensitive arguments.
func Fingerprint(cfg RunConfig) (string, error) {
	instance := resolveInstance(cfg)
	if err := instance.Validate(); err != nil {
		return "", err
	}
	arms, err := resolvedArms(cfg.Arms)
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

	agentCommand, err := hashJSON(cfg.AgentArgv)
	if err != nil {
		return "", err
	}

	material, err := fingerprintInstance(instance)
	if err != nil {
		return "", err
	}

	harnessSHA, err := fingerprintHarnessSources()
	if err != nil {
		return "", err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	payload := struct {
		Schema           int             `json:"schema"`
		Instance         string          `json:"instance"`
		Rule             string          `json:"rule"`
		TaskSHA          string          `json:"task_sha256"`
		LessonSHA        string          `json:"lesson_sha256"`
		PlaceboSHA       string          `json:"placebo_sha256"`
		FixtureHEAD      string          `json:"fixture_head"`
		GoldPatchSHA     string          `json:"gold_patch_sha256"`
		NaivePatchSHA    string          `json:"naive_patch_sha256"`
		JudgeVersion     string          `json:"judge_version"`
		HarnessSourceSHA string          `json:"harness_source_sha256"`
		Checks           []checkIdentity `json:"checks"`
		AgentCommand     string          `json:"agent_command_sha256"`
		AgentVersion     string          `json:"agent_version"`
		Model            string          `json:"model"`
		Effort           string          `json:"effort"`
		MaxBudgetUSD     float64         `json:"max_budget_usd"`
		TimeoutMS        int64           `json:"timeout_ms"`
		RuntimeID        string          `json:"runtime_id"`
		SeamarkVersion   string          `json:"seamark_version"`
		SeamarkSHA       string          `json:"seamark_sha256"`
		Arms             []Arm           `json:"arms"`
		PrepareIndex     bool            `json:"prepare_index"`
		StructuredResult bool            `json:"require_structured_result"`
		CleanInit        bool            `json:"require_clean_init"`
	}{
		Schema: 4, Instance: instance.ID, Rule: instance.Rule,
		TaskSHA: instance.TaskSHA(), LessonSHA: hashBytes([]byte(instance.LessonYAML)),
		PlaceboSHA:  hashBytes([]byte(instance.PlaceboYAML)),
		FixtureHEAD: material.fixtureHEAD, GoldPatchSHA: material.goldPatchSHA,
		NaivePatchSHA: material.naivePatchSHA, JudgeVersion: instance.JudgeVersion,
		HarnessSourceSHA: harnessSHA, Checks: checks, AgentCommand: agentCommand,
		AgentVersion: cfg.AgentVersion, Model: cfg.Model, Effort: cfg.Effort,
		MaxBudgetUSD: cfg.MaxBudgetUSD, TimeoutMS: timeout.Milliseconds(),
		RuntimeID: cfg.RuntimeID, SeamarkVersion: cfg.Version, SeamarkSHA: cfg.SeamarkSHA,
		Arms: arms, PrepareIndex: cfg.PrepareIndex,
		StructuredResult: cfg.RequireStructuredResult, CleanInit: cfg.RequireCleanInit,
	}

	return hashJSON(payload)
}

type instanceFingerprintMaterial struct {
	fixtureHEAD   string
	goldPatchSHA  string
	naivePatchSHA string
}

func fingerprintInstance(instance Instance) (instanceFingerprintMaterial, error) {
	root, err := os.MkdirTemp("", "seamark-bench-fingerprint-")
	if err != nil {
		return instanceFingerprintMaterial{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()

	gold := filepath.Join(root, "gold")
	if err := instance.Generate(gold); err != nil {
		return instanceFingerprintMaterial{}, fmt.Errorf("generate fingerprint fixture: %w", err)
	}
	head := fixtureHead(gold)
	if head == "" {
		return instanceFingerprintMaterial{}, fmt.Errorf("fingerprint fixture has no HEAD")
	}
	if err := instance.ApplyGold(gold); err != nil {
		return instanceFingerprintMaterial{}, fmt.Errorf("apply fingerprint gold patch: %w", err)
	}
	goldPatch, err := workingTreePatch(gold)
	if err != nil {
		return instanceFingerprintMaterial{}, err
	}

	naive := filepath.Join(root, "naive")
	if err := instance.Generate(naive); err != nil {
		return instanceFingerprintMaterial{}, fmt.Errorf("generate fingerprint naive fixture: %w", err)
	}
	if err := instance.ApplyNaive(naive); err != nil {
		return instanceFingerprintMaterial{}, fmt.Errorf("apply fingerprint naive patch: %w", err)
	}
	naivePatch, err := workingTreePatch(naive)
	if err != nil {
		return instanceFingerprintMaterial{}, err
	}

	return instanceFingerprintMaterial{
		fixtureHEAD: head, goldPatchSHA: hashBytes(goldPatch),
		naivePatchSHA: hashBytes(naivePatch),
	}, nil
}

func workingTreePatch(dir string) ([]byte, error) {
	// Stage only inside the disposable fingerprint repository so new files,
	// deletions, mode changes, and edits all become part of the patch identity.
	if out, err := exec.Command("git", "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("stage fingerprint patch: %w\n%s", err, out)
	}

	cmd := exec.Command("git", "-C", dir, "diff", "--cached", "--binary", "HEAD", "--")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read fingerprint patch: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fingerprint patch is empty")
	}

	return out, nil
}

func fingerprintHarnessSources() (string, error) {
	entries, err := harnessSources.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("read embedded benchmark sources: %w", err)
	}

	h := sha256.New()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := harnessSources.ReadFile(entry.Name())
		if err != nil {
			return "", fmt.Errorf("read embedded benchmark source %s: %w", entry.Name(), err)
		}
		_, _ = h.Write([]byte(entry.Name()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal fingerprint: %w", err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}
