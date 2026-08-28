package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Preflight validates every invariant that can be checked without spending an
// agent call: deterministic generation, a clean and healthy base tree, a
// naive task-only solution the invariant judge must reject, a canonical
// passing solution, and uncontaminated arm wiring.
func Preflight(ctx context.Context, cfg RunConfig) error {
	instance := resolveInstance(cfg)
	if err := instance.Validate(); err != nil {
		return err
	}

	if !knownHookDelivery(cfg.HookDelivery) {
		return fmt.Errorf("unknown hook delivery mode %q", cfg.HookDelivery)
	}
	if cfg.SeamarkBin == "" {
		return fmt.Errorf("preflight requires the seamark binary")
	}

	if err := validateDeliveredInstance(cfg, instance); err != nil {
		return err
	}

	root, err := os.MkdirTemp("", "seamark-bench-preflight-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()

	baseA := filepath.Join(root, "base-a")
	baseB := filepath.Join(root, "base-b")

	if err := instance.Generate(baseA); err != nil {
		return fmt.Errorf("generate first base fixture: %w", err)
	}

	if err := instance.Generate(baseB); err != nil {
		return fmt.Errorf("generate second base fixture: %w", err)
	}

	if a, b := fixtureHead(baseA), fixtureHead(baseB); a == "" || a != b {
		return fmt.Errorf("fixture is not deterministic: HEAD %q != %q", a, b)
	}

	if err := assertNoTreatment(baseA); err != nil {
		return err
	}

	baseVerdict, err := instance.Judge(baseA)
	if err != nil {
		return fmt.Errorf("judge untouched fixture: %w", err)
	}

	if baseVerdict.TaskDone {
		return fmt.Errorf("untouched fixture already passes the task judge")
	}

	results, err := runChecks(ctx, baseA, instance.Checks)
	if err != nil {
		return fmt.Errorf("run untouched fixture checks: %w", err)
	}

	if !checksPass(results) {
		return fmt.Errorf("untouched fixture checks failed: %s", failedChecks(results))
	}

	if err := instance.ApplyNaive(baseB); err != nil {
		return fmt.Errorf("apply naive patch: %w", err)
	}

	naiveVerdict, err := instance.Judge(baseB)
	if err != nil {
		return fmt.Errorf("judge naive patch: %w", err)
	}

	if !naiveVerdict.TaskDone {
		return fmt.Errorf("naive patch does not pass the task judge: %s", naiveVerdict.Notes)
	}

	if naiveVerdict.Avoided {
		return fmt.Errorf("naive patch incorrectly passes the invariant judge")
	}

	results, err = runChecks(ctx, baseB, instance.Checks)
	if err != nil {
		return fmt.Errorf("run naive fixture checks: %w", err)
	}

	if !checksPass(results) {
		return fmt.Errorf("naive patch checks failed: %s", failedChecks(results))
	}

	if err := instance.ApplyGold(baseA); err != nil {
		return fmt.Errorf("apply canonical patch: %w", err)
	}

	goldVerdict, err := instance.Judge(baseA)
	if err != nil {
		return fmt.Errorf("judge canonical patch: %w", err)
	}

	if !goldVerdict.TaskDone || !goldVerdict.Avoided {
		return fmt.Errorf("canonical patch does not pass both judges: %s", goldVerdict.Notes)
	}

	results, err = runChecks(ctx, baseA, instance.Checks)
	if err != nil {
		return fmt.Errorf("run canonical fixture checks: %w", err)
	}

	if !checksPass(results) {
		return fmt.Errorf("canonical patch checks failed: %s", failedChecks(results))
	}

	off := filepath.Join(root, "hook-off")
	on := filepath.Join(root, "hook-on")

	if err := instance.Generate(off); err != nil {
		return err
	}

	if err := instance.Generate(on); err != nil {
		return err
	}

	if err := wireArm(ctx, off, cfg, instance, ArmHookOff); err != nil {
		return fmt.Errorf("wire hook-off arm: %w", err)
	}

	if err := wireArm(ctx, on, cfg, instance, ArmHookOn); err != nil {
		return fmt.Errorf("wire hook-on arm: %w", err)
	}

	if err := validateArmWiring(off, on, cfg, instance); err != nil {
		return err
	}

	return nil
}

func assertNoTreatment(dir string) error {
	for _, rel := range []string{".seamark", ".claude"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			return fmt.Errorf("fixture contains treatment artifact %s", rel)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	return nil
}

func validateArmWiring(off, on string, cfg RunConfig, instance Instance) error {
	offSettings, err := os.ReadFile(filepath.Join(off, ".claude", "settings.json"))
	if err != nil {
		return fmt.Errorf("read hook-off settings: %w", err)
	}

	onSettings, err := os.ReadFile(filepath.Join(on, ".claude", "settings.json"))
	if err != nil {
		return fmt.Errorf("read hook-on settings: %w", err)
	}

	if containsHook(offSettings) {
		return fmt.Errorf("hook-off settings contain a hook")
	}

	if !containsHook(onSettings) {
		return fmt.Errorf("hook-on settings do not contain a hook")
	}

	if _, err := os.Stat(filepath.Join(off, ".seamark", "lessons.yaml")); !os.IsNotExist(err) {
		return fmt.Errorf("hook-off arm contains the treatment lesson")
	}

	lesson, err := os.ReadFile(filepath.Join(on, ".seamark", "lessons.yaml"))
	if err != nil {
		return fmt.Errorf("read hook-on lesson: %w", err)
	}

	if string(lesson) != lessonsForDelivery(cfg, instance.LessonYAML) {
		return fmt.Errorf("hook-on lesson differs from the instance")
	}

	if effectiveHookDelivery(cfg) == HookDeliveryOncePerContext &&
		(!strings.Contains(string(onSettings), "PostCompact") ||
			!strings.Contains(string(onSettings), "lessons --hook-reset")) {
		return fmt.Errorf("once-per-context hook-on settings lack a PostCompact reset")
	}

	return nil
}

func containsHook(settings []byte) bool {
	return strings.Contains(string(settings), "PreToolUse") &&
		strings.Contains(string(settings), "lessons --hook")
}

func runChecks(ctx context.Context, dir string, commands []Command) ([]CheckResult, error) {
	results := make([]CheckResult, 0, len(commands))

	for _, command := range commands {
		result, err := command.run(ctx, dir)
		if err != nil {
			return nil, err
		}

		results = append(results, result)
	}

	return results, nil
}

func checksPass(results []CheckResult) bool {
	for _, result := range results {
		if !result.Pass {
			return false
		}
	}

	return true
}

func failedChecks(results []CheckResult) string {
	for _, result := range results {
		if !result.Pass {
			return result.Command + ": " + result.Output
		}
	}

	return ""
}
