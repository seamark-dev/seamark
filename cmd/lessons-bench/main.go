// Command lessons-bench runs the active experiment: does
// the seamark lessons hook change what a headless agent writes? Two
// arms differing in exactly one bit (the hook wired or absent), a fresh
// synthetic or pinned-public worktree per trial, a frozen task, and a
// deterministic judge. See internal/bench for the design and its guardrails.
//
// Paid runs spend len(selected arms) x -trials agent sessions. The "all"
// instance selector is restricted to no-agent preflight/dry-run modes. Run at
// release cadence, never in per-PR CI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/bench"
)

func main() {
	var opts options
	flag.IntVar(&opts.trials, "trials", 1, "trials per arm; calibrate one pair before increasing")
	flag.StringVar(&opts.arm, "arm", "both", "which arm to run: both, all, hook-on, hook-off, file-only, or placebo")
	flag.StringVar(&opts.instance, "instance", bench.SchemaSyncInstanceID,
		"benchmark instance: "+strings.Join(bench.InstanceIDs(), ", ")+", or all for preflight/dry-run")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "run preflight and print the plan without agent calls")
	flag.BoolVar(&opts.preflightOnly, "preflight-only", false, "validate fixture, judges, checks, and arm wiring, then exit")
	flag.StringVar(&opts.out, "out", fmt.Sprintf("bench/results-v%d.jsonl", bench.ResultSchemaVersion),
		"results file, one JSONL row per attempted session (appended)")
	flag.StringVar(&opts.transcripts, "transcripts", "bench/transcripts", "directory for per-trial agent transcripts; empty disables")
	flag.StringVar(&opts.agent, "agent", "", "custom agent command (space-split); bypasses Claude runtime validation")
	flag.StringVar(&opts.model, "model", "", "exact Claude model ID (required with the default agent; aliases are refused)")
	flag.StringVar(&opts.effort, "effort", "medium", "pinned Claude effort level")
	flag.StringVar(&opts.hookDelivery, "hook-delivery", string(bench.HookDeliveryAlways),
		"hook repeat policy: always or once-per-context")
	flag.Float64Var(&opts.maxBudgetUSD, "max-budget-usd", 1, "hard provider cost cap per agent session")
	flag.StringVar(&opts.seamarkBin, "seamark", "", "seamark binary for the hook arm; default: ./bin/seamark (make build)")
	flag.StringVar(&opts.runtimeID, "runtime-id", "", "additional immutable runtime/container digest recorded in the fingerprint")
	flag.BoolVar(&opts.keep, "keep", false, "keep trial directories for inspection")
	flag.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "per-trial agent timeout")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "lessons-bench:", err)
		os.Exit(1)
	}
}

type options struct {
	trials        int
	arm           string
	instance      string
	dryRun        bool
	preflightOnly bool
	out           string
	transcripts   string
	agent         string
	model         string
	effort        string
	hookDelivery  string
	maxBudgetUSD  float64
	seamarkBin    string
	runtimeID     string
	keep          bool
	timeout       time.Duration
}

func run(opts options) error {
	if opts.instance == "all" {
		if !opts.preflightOnly && !opts.dryRun {
			return fmt.Errorf("-instance all is preflight-only; calibrate and run each instance explicitly")
		}

		for i, id := range bench.InstanceIDs() {
			if i > 0 {
				fmt.Println()
			}

			instanceOpts := opts
			instanceOpts.instance = id

			if err := run(instanceOpts); err != nil {
				return fmt.Errorf("instance %s: %w", id, err)
			}
		}

		return nil
	}

	arms, err := parseArms(opts.arm)
	if err != nil {
		return err
	}

	instance, err := bench.InstanceByID(opts.instance)
	if err != nil {
		return err
	}

	if opts.trials < 1 {
		return fmt.Errorf("-trials must be at least 1")
	}

	if opts.hookDelivery == "" {
		opts.hookDelivery = string(bench.HookDeliveryAlways)
	}

	if opts.hookDelivery != string(bench.HookDeliveryAlways) &&
		opts.hookDelivery != string(bench.HookDeliveryOncePerContext) {
		return fmt.Errorf("-hook-delivery must be %q or %q",
			bench.HookDeliveryAlways, bench.HookDeliveryOncePerContext)
	}

	bin := opts.seamarkBin
	if bin == "" {
		bin = filepath.Join("bin", "seamark")
	}

	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}

	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("seamark binary not found at %s — run `make build` first (or pass -seamark)", abs)
	}

	argv, managed, err := agentCommand(opts)
	if err != nil {
		return err
	}

	agentVersion := cliVersion(argv[0])
	seamarkSHA, err := bench.FileSHA256(abs)
	if err != nil {
		return fmt.Errorf("hash seamark binary: %w", err)
	}

	runtimeID := opts.runtimeID
	if runtimeID == "" {
		runtimeID = localRuntimeID(agentVersion, instance)
	}

	cfg := bench.RunConfig{
		Trials:                  opts.trials,
		Arms:                    arms,
		Instance:                instance,
		AgentArgv:               argv,
		SeamarkBin:              abs,
		Timeout:                 opts.timeout,
		Out:                     opts.out,
		TranscriptDir:           opts.transcripts,
		Keep:                    opts.keep,
		PrepareIndex:            true,
		Version:                 seamarkVersion(abs),
		SeamarkSHA:              seamarkSHA,
		AgentVersion:            agentVersion,
		Model:                   opts.model,
		Effort:                  opts.effort,
		MaxBudgetUSD:            opts.maxBudgetUSD,
		RuntimeID:               runtimeID,
		HookDelivery:            bench.HookDeliveryMode(opts.hookDelivery),
		RequireStructuredResult: managed,
		RequireCleanInit:        managed,
		Log: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	}

	cfg.Fingerprint, err = bench.Fingerprint(cfg)
	if err != nil {
		return err
	}

	cfg.ProtocolFingerprint, err = bench.ProtocolFingerprint(cfg)
	if err != nil {
		return err
	}

	armCount := len(arms)
	if armCount == 0 {
		armCount = 2
	}

	// The disclosure-before-spend pattern every paid seamark surface
	// follows (the distill preflight precedent).
	fmt.Printf("lessons-bench — %s, pin %q, %d trials x %d arm(s) = %d headless agent sessions\n",
		instance.ID, instance.Rule, opts.trials, armCount, armCount*opts.trials)

	if managed {
		fmt.Printf("  agent    claude %s, effort=%s, native sandbox=strict\n", opts.model, opts.effort)
	} else {
		fmt.Printf("  agent    custom adapter %s\n", argv[0])
	}

	if managed {
		fmt.Printf("  budget   $%.2f/session; $%.2f absolute run cap\n",
			opts.maxBudgetUSD, opts.maxBudgetUSD*float64(armCount*opts.trials))
	} else {
		fmt.Println("  budget   custom adapter owns its cost cap")
	}

	fmt.Printf("  runtime  %s\n", runtimeID)
	fmt.Printf("  delivery %s\n", cfg.HookDelivery)
	fmt.Printf("  seamark  %s (%s, sha256 %.12s…)\n", abs, cfg.Version, cfg.SeamarkSHA)
	fmt.Printf("  fingerprint %.12s…\n", cfg.Fingerprint)
	if cfg.ProtocolFingerprint != cfg.Fingerprint {
		fmt.Printf("  protocol %.12s… (%s)\n", cfg.ProtocolFingerprint, instance.ComparisonFamily)
	}

	if opts.dryRun || opts.preflightOnly {
		fmt.Printf("  results  %s (no rows written during preflight)\n", opts.out)
	} else {
		fmt.Printf("  results  %s (appended)\n", opts.out)
	}

	if opts.transcripts != "" {
		if opts.dryRun || opts.preflightOnly {
			fmt.Printf("  logs     %s (no artifacts written during preflight)\n", opts.transcripts)
		} else {
			fmt.Printf("  logs     %s (full agent transcript per trial)\n", opts.transcripts)
		}
	}

	fmt.Printf("  task     %s\n", instance.Task)
	fmt.Printf("  cost     %s\n", costEstimate(opts.out, cfg.Fingerprint, armCount*opts.trials))

	preflightCtx, cancelPreflight := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelPreflight()
	if err := bench.Preflight(preflightCtx, cfg); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	fmt.Println("  preflight passed — deterministic base/naive/gold, checks, judges, and arm wiring")

	if opts.dryRun || opts.preflightOnly {
		fmt.Println("\nno agent sessions executed")

		return nil
	}

	// Ctrl-C stops between trials and still prints what was measured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sum, err := bench.Run(ctx, cfg)

	fmt.Println()

	if ctx.Err() != nil {
		fmt.Println("interrupted — partial results:")
	}

	for _, line := range sum.Lines() {
		fmt.Println(line)
	}

	if err != nil {
		return err
	}

	return nil
}

func agentCommand(opts options) (argv []string, managed bool, err error) {
	if opts.agent != "" {
		argv := strings.Fields(opts.agent)
		if len(argv) == 0 {
			return nil, false, fmt.Errorf("empty custom agent command")
		}

		return argv, false, nil
	}

	if !exactModelID(opts.model) {
		return nil, false, fmt.Errorf("-model must be an exact model ID, not an alias (got %q)", opts.model)
	}

	if opts.maxBudgetUSD <= 0 {
		return nil, false, fmt.Errorf("-max-budget-usd must be positive")
	}

	return []string{
		"claude", "-p",
		"--model", opts.model,
		"--effort", opts.effort,
		"--max-budget-usd", strconv.FormatFloat(opts.maxBudgetUSD, 'f', -1, 64),
		"--permission-mode", "acceptEdits",
		"--setting-sources", "project",
		"--tools", "Read,Edit,Write,Bash",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--no-chrome",
		"--no-session-persistence",
		"--prompt-suggestions", "false",
		"--output-format", "stream-json",
		"--include-hook-events",
		"--verbose",
	}, true, nil
}

func exactModelID(model string) bool {
	if model == "" {
		return false
	}

	if strings.Contains(strings.ToLower(model), "latest") {
		return false
	}

	switch strings.ToLower(model) {
	case "default", "opus", "sonnet", "haiku", "fable":
		return false
	default:
		return strings.HasPrefix(model, "claude-")
	}
}

// parseArms maps the -arm flag to the arms to run.
func parseArms(arm string) ([]bench.Arm, error) {
	switch arm {
	case "", "both":
		return nil, nil
	case "all":
		return []bench.Arm{
			bench.ArmHookOn, bench.ArmHookOff,
			bench.ArmFileOnly, bench.ArmPlacebo,
		}, nil
	case "hook-on", "on":
		return []bench.Arm{bench.ArmHookOn}, nil
	case "hook-off", "off", "baseline":
		return []bench.Arm{bench.ArmHookOff}, nil
	case "file-only":
		return []bench.Arm{bench.ArmFileOnly}, nil
	case "placebo":
		return []bench.Arm{bench.ArmPlacebo}, nil
	default:
		return nil, fmt.Errorf("unknown -arm %q (both, all, hook-on, hook-off, file-only, placebo)", arm)
	}
}

// costEstimate projects a run's cost from the measured rows already
// in the results file; before any rows exist there is nothing honest
// to project, and the line says so.
func costEstimate(out, fingerprint string, sessions int) string {
	rows, meanIn, meanCost, ok := bench.PriorCostFor(out, fingerprint)
	if !ok {
		return "no valid rows for this exact fingerprint — " +
			"each trial records its measured tokens/cost, and the next preflight will project from them"
	}

	return fmt.Sprintf("~$%.2f for %d sessions (from %d matching rows: mean %dk context processed, $%.2f per trial)",
		float64(sessions)*meanCost, sessions, rows, meanIn/1000, meanCost)
}

func localRuntimeID(agentVersion string, instance bench.Instance) string {
	parts := []string{
		"claude-native-sandbox-v2",
		runtime.GOOS + "/" + runtime.GOARCH,
		"agent=" + agentVersion,
	}

	seen := make(map[string]bool)

	for _, check := range instance.Checks {
		if seen[check.Name] {
			continue
		}
		seen[check.Name] = true

		versionArgs := []string{"--version"}
		if check.Name == "go" {
			versionArgs = []string{"version"}
		}
		version := commandVersion(check.Name, versionArgs...)
		parts = append(parts, check.Name+"="+version)
	}

	return strings.Join(parts, ";")
}

func commandVersion(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return "unknown"
	}

	line, _, _ := strings.Cut(output, "\n")

	return line
}

// cliVersion asks the agent binary for its version, so rows record
// the exact CLI that ran the trials. Best-effort: "unknown" when the
// binary has no --version.
func cliVersion(bin string) string {
	return commandVersion(bin, "--version")
}

// seamarkVersion asks the binary itself, so rows record the exact
// build that served the hook arm.
func seamarkVersion(bin string) string {
	return commandVersion(bin, "version")
}
