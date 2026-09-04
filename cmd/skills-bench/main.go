// Command skills-bench runs the skills workflow experiment: does installing
// the seamark agent skills change what a headless agent does on a
// companion-file task, compared with the seamark MCP server alone? Two arms
// that differ in exactly one thing (the skills installed and callable, or
// not), a fresh fixture whose history carries the companion pair, a frozen
// task, and the lessons benchmark's deterministic judges. The same command
// replays the activation prompt set with -activation. See internal/bench
// (workflow_*.go) for the design and its guardrails.
//
// Paid runs spend len(selected arms) x -trials agent sessions, or one session
// per prompt with -activation. The "all" instance selector is restricted to
// no-agent preflight/dry-run modes. Run at release cadence, never in per-PR
// CI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/bench"
)

// Default result files. Workflow rows and activation rows never share one.
const (
	defaultWorkflowOut   = "bench/workflow-results-v1.jsonl"
	defaultActivationOut = "bench/activation-results-v1.jsonl"
)

func main() {
	var opts options
	flag.IntVar(&opts.trials, "trials", 1, "trials per arm; calibrate one pair before increasing")
	flag.StringVar(&opts.arm, "arm", "both", "which arm to run: both, mcp-only, or mcp-skills")
	flag.StringVar(&opts.instance, "instance", bench.SchemaSyncCochangeInstanceID,
		"workflow instance: "+strings.Join(bench.WorkflowInstanceIDs(), ", ")+", or all for preflight/dry-run")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "run preflight and print the plan without agent calls")
	flag.BoolVar(&opts.preflightOnly, "preflight-only", false, "validate fixture, judges, checks, co-change, MCP, and arm wiring, then exit")
	flag.StringVar(&opts.out, "out", "",
		"results file, one JSONL row per session (appended); default "+defaultWorkflowOut+", or "+defaultActivationOut+" with -activation")
	flag.StringVar(&opts.transcripts, "transcripts", "bench/transcripts", "directory for per-session agent transcripts; empty disables")
	flag.StringVar(&opts.agent, "agent", "", "custom agent command (space-split); bypasses Claude runtime validation; receives --strict-mcp-config, --mcp-config, and --max-turns like Claude")
	flag.StringVar(&opts.model, "model", "", "exact Claude model ID (required with the default agent; aliases are refused)")
	flag.StringVar(&opts.effort, "effort", "medium", "pinned Claude effort level")
	flag.Float64Var(&opts.maxBudgetUSD, "max-budget-usd", 1, "hard provider cost cap per agent session")
	flag.StringVar(&opts.seamarkBin, "seamark", "", "seamark binary for the MCP server and the index; default: ./bin/seamark (make build)")
	flag.StringVar(&opts.runtimeID, "runtime-id", "", "additional immutable runtime/container digest recorded in the fingerprint")
	flag.BoolVar(&opts.keep, "keep", false, "keep trial directories for inspection")
	flag.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "per-session agent timeout")
	flag.StringVar(&opts.activation, "activation", "", "activation prompt set (YAML); runs one skills-arm session per prompt instead of the paired trials")
	flag.IntVar(&opts.maxTurns, "max-turns", 0, "cap activation sessions with --max-turns; 0 leaves the agent's default")
	flag.StringVar(&opts.generate, "generate", "", "write the selected instance's fixture to this new directory and exit (no agent, for manual trials)")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "skills-bench:", err)
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
	maxBudgetUSD  float64
	seamarkBin    string
	runtimeID     string
	keep          bool
	timeout       time.Duration
	activation    string
	maxTurns      int
	generate      string
}

func run(opts options) error {
	if opts.generate != "" {
		return generateFixture(opts)
	}

	if opts.instance == "all" {
		if !opts.preflightOnly && !opts.dryRun {
			return fmt.Errorf("-instance all is preflight-only; calibrate and run each instance explicitly")
		}

		for i, id := range bench.WorkflowInstanceIDs() {
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

	instance, err := bench.WorkflowInstanceByID(opts.instance)
	if err != nil {
		return err
	}

	if opts.trials < 1 {
		return fmt.Errorf("-trials must be at least 1")
	}

	if opts.maxTurns < 0 {
		return fmt.Errorf("-max-turns must not be negative")
	}

	out := resolveOut(opts)
	if err := checkExistingRows(out, opts.activation != ""); err != nil {
		return err
	}

	abs, err := resolveBinary(opts.seamarkBin)
	if err != nil {
		return err
	}

	argv, managed, err := agentCommands(opts)
	if err != nil {
		return err
	}

	agentVersion := bench.CommandVersion(argv[bench.ArmMCPSkills][0], "--version")
	seamarkSHA, err := bench.FileSHA256(abs)
	if err != nil {
		return fmt.Errorf("hash seamark binary: %w", err)
	}

	runtimeID := opts.runtimeID
	if runtimeID == "" {
		runtimeID = bench.LocalRuntimeID(agentVersion, instance.Checks)
	}

	cfg := bench.WorkflowConfig{
		Trials:                  opts.trials,
		Arms:                    arms,
		Instance:                instance,
		AgentArgv:               argv,
		SeamarkBin:              abs,
		Timeout:                 opts.timeout,
		Out:                     out,
		TranscriptDir:           opts.transcripts,
		Keep:                    opts.keep,
		PrepareIndex:            true,
		Version:                 bench.CommandVersion(abs, "version"),
		SeamarkSHA:              seamarkSHA,
		AgentVersion:            agentVersion,
		Model:                   opts.model,
		Effort:                  opts.effort,
		MaxBudgetUSD:            opts.maxBudgetUSD,
		RuntimeID:               runtimeID,
		MaxTurns:                opts.maxTurns,
		RequireStructuredResult: managed,
		RequireExpectedInit:     managed,
		Log: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	}

	var prompts bench.ActivationPromptSet
	if opts.activation != "" {
		prompts, err = bench.LoadActivationPrompts(opts.activation)
		if err != nil {
			return err
		}

		if prompts.Instance != instance.ID {
			return fmt.Errorf("%s is written for instance %s; pass -instance %s",
				opts.activation, prompts.Instance, prompts.Instance)
		}

		// An activation run is a skills-arm-only experiment; its rows carry
		// that fingerprint.
		cfg.Arms = []bench.WorkflowArm{bench.ArmMCPSkills}
	}

	cfg.Fingerprint, err = bench.WorkflowFingerprint(cfg)
	if err != nil {
		return err
	}

	sessions := len(cfg.Arms) * opts.trials
	if opts.activation != "" {
		sessions = len(prompts.Prompts)
	}

	printBanner(opts, cfg, instance, managed, sessions, len(prompts.Prompts))

	preflightCtx, cancelPreflight := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelPreflight()

	if err := bench.WorkflowPreflight(preflightCtx, cfg); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	fmt.Println("  preflight passed — deterministic base/naive/gold, checks, judges, co-change pair, MCP initialize, and arm wiring")

	if opts.dryRun || opts.preflightOnly {
		fmt.Println("\nno agent sessions executed")

		return nil
	}

	// Ctrl-C stops between sessions and still prints what was measured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var lines []string
	var runErr error

	if opts.activation != "" {
		sum, err := bench.RunActivation(ctx, cfg, prompts)
		lines, runErr = sum.Lines(), err
	} else {
		sum, err := bench.RunWorkflow(ctx, cfg)
		lines, runErr = sum.Lines(), err
	}

	fmt.Println()

	if ctx.Err() != nil {
		fmt.Println("interrupted — partial results:")
	}

	for _, line := range lines {
		fmt.Println(line)
	}

	return runErr
}

// printBanner is the disclosure-before-spend step every paid seamark surface
// follows: what will run, at what cost cap, under which identity.
func printBanner(opts options, cfg bench.WorkflowConfig, instance bench.WorkflowInstance, managed bool, sessions, prompts int) {
	if opts.activation != "" {
		fmt.Printf("skills-bench activation — %s on %s, %d prompts x 1 skills-arm session = %d headless agent sessions\n",
			opts.activation, instance.ID, prompts, sessions)
	} else {
		fmt.Printf("skills-bench — %s, trigger %s, companion %s, %d trials x %d arm(s) = %d headless agent sessions\n",
			instance.ID, instance.Trigger, instance.Companion, opts.trials, len(cfg.Arms), sessions)
	}

	if managed {
		fmt.Printf("  agent    claude %s, effort=%s, native sandbox=strict\n", opts.model, opts.effort)
	} else {
		fmt.Printf("  agent    custom adapter %s\n", cfg.AgentArgv[bench.ArmMCPSkills][0])
	}

	for _, arm := range cfg.Arms {
		fmt.Printf("  %-8s tools %s\n", arm, strings.Join(bench.WorkflowTools(arm), ","))
	}

	if managed {
		fmt.Printf("  budget   $%.2f/session; $%.2f absolute run cap\n",
			opts.maxBudgetUSD, opts.maxBudgetUSD*float64(sessions))
	} else {
		fmt.Println("  budget   custom adapter owns its cost cap")
	}

	if opts.activation != "" {
		if opts.maxTurns > 0 {
			fmt.Printf("  turns    at most %d per session\n", opts.maxTurns)
		} else {
			fmt.Println("  turns    the agent's default limit")
		}
	}

	fmt.Printf("  runtime  %s\n", cfg.RuntimeID)
	fmt.Printf("  seamark  %s (%s, sha256 %.12s…)\n", cfg.SeamarkBin, cfg.Version, cfg.SeamarkSHA)
	fmt.Printf("  fingerprint %.12s…\n", cfg.Fingerprint)

	if opts.dryRun || opts.preflightOnly {
		fmt.Printf("  results  %s (no rows written during preflight)\n", cfg.Out)
	} else {
		fmt.Printf("  results  %s (appended)\n", cfg.Out)
	}

	if opts.transcripts != "" {
		if opts.dryRun || opts.preflightOnly {
			fmt.Printf("  logs     %s (no artifacts written during preflight)\n", opts.transcripts)
		} else {
			fmt.Printf("  logs     %s (full agent transcript per session)\n", opts.transcripts)
		}
	}

	if opts.activation == "" {
		fmt.Printf("  task     %s\n", instance.Task)
		fmt.Printf("  cost     %s\n", costEstimate(cfg.Out, cfg.Fingerprint, sessions))
	}
}

// agentCommands builds the per-arm agent command. The managed Claude adapter
// exposes a different tool list per arm; a custom adapter gets the same
// command for both arms and owns its own validation.
func agentCommands(opts options) (argv map[bench.WorkflowArm][]string, managed bool, err error) {
	if opts.agent != "" {
		custom := strings.Fields(opts.agent)
		if len(custom) == 0 {
			return nil, false, fmt.Errorf("empty custom agent command")
		}

		return bench.SameAgentArgv(custom), false, nil
	}

	if !bench.ExactModelID(opts.model) {
		return nil, false, fmt.Errorf("-model must be an exact model ID, not an alias (got %q)", opts.model)
	}

	if opts.maxBudgetUSD <= 0 {
		return nil, false, fmt.Errorf("-max-budget-usd must be positive")
	}

	argv = map[bench.WorkflowArm][]string{}
	for _, arm := range []bench.WorkflowArm{bench.ArmMCPOnly, bench.ArmMCPSkills} {
		argv[arm] = bench.ClaudeArgv(opts.model, opts.effort, opts.maxBudgetUSD, bench.WorkflowTools(arm))
	}

	return argv, true, nil
}

// parseArms maps the -arm flag to the arms to run.
func parseArms(arm string) ([]bench.WorkflowArm, error) {
	switch arm {
	case "", "both":
		return []bench.WorkflowArm{bench.ArmMCPSkills, bench.ArmMCPOnly}, nil
	case "mcp-only", "only":
		return []bench.WorkflowArm{bench.ArmMCPOnly}, nil
	case "mcp-skills", "skills":
		return []bench.WorkflowArm{bench.ArmMCPSkills}, nil
	default:
		return nil, fmt.Errorf("unknown -arm %q (both, mcp-only, mcp-skills)", arm)
	}
}

// resolveOut picks the results file for the mode when -out was not given.
func resolveOut(opts options) string {
	if opts.out != "" {
		return opts.out
	}

	if opts.activation != "" {
		return defaultActivationOut
	}

	return defaultWorkflowOut
}

// checkExistingRows refuses to append paid rows to a results file the
// report could not read: a file of the other row kind, a line that is not a
// row, or a row the strict validator rejects. The report reads one kind of
// row per file with a strict parser, and one bad line fails the whole file
// after the sessions were paid for.
func checkExistingRows(path string, activation bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	if err := refuseMixedRows(path, data, activation); err != nil {
		return err
	}

	if activation {
		_, _, err = bench.ReadActivationRows(path)
	} else {
		_, _, err = bench.ReadWorkflowRowsStrict(path)
	}

	if err != nil {
		return fmt.Errorf("%w; appending paid rows to this file would leave it unreadable (pass -out)", err)
	}

	return nil
}

// refuseMixedRows keeps workflow rows and activation rows in separate files
// and names the first line that is neither, with a clearer message than the
// strict reader's unknown-field error would give.
func refuseMixedRows(path string, data []byte, activation bool) error {
	lineNumber := 0

	for line := range strings.SplitSeq(string(data), "\n") {
		lineNumber++
		if strings.TrimSpace(line) == "" {
			continue
		}

		var probe struct {
			Arm      string `json:"arm"`
			PromptID string `json:"prompt_id"`
		}

		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			return fmt.Errorf("%s:%d: not a JSON row (%v); appending to it would make the file unreadable (pass -out)",
				path, lineNumber, err)
		}

		switch {
		case probe.Arm == "" && probe.PromptID == "":
			return fmt.Errorf("%s:%d: neither a workflow row nor an activation row; appending to it would make the file unreadable (pass -out)",
				path, lineNumber)
		case activation && probe.Arm != "":
			return fmt.Errorf("%s holds workflow rows; activation rows never share a file with them (pass -out)", path)
		case !activation && probe.PromptID != "":
			return fmt.Errorf("%s holds activation rows; workflow rows never share a file with them (pass -out)", path)
		}
	}

	return nil
}

func resolveBinary(configured string) (string, error) {
	bin := configured
	if bin == "" {
		bin = filepath.Join("bin", "seamark")
	}

	abs, err := filepath.Abs(bin)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("seamark binary not found at %s — run `make build` first (or pass -seamark)", abs)
	}

	return abs, nil
}

// generateFixture writes one fixture for a manual session, such as the Codex
// activation checklist. The directory must not exist: the tree is the
// experiment's material and is never merged into something else.
func generateFixture(opts options) error {
	instance, err := bench.WorkflowInstanceByID(opts.instance)
	if err != nil {
		return err
	}

	if _, err := os.Lstat(opts.generate); err == nil {
		return fmt.Errorf("-generate directory already exists: %s", opts.generate)
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := instance.Generate(opts.generate); err != nil {
		return err
	}

	fmt.Printf("wrote %s fixture to %s (trigger %s, companion %s)\n",
		instance.ID, opts.generate, instance.Trigger, instance.Companion)

	return nil
}

// costEstimate projects a run's cost from the measured rows already in the
// results file; before any rows exist there is nothing honest to project,
// and the line says so.
func costEstimate(out, fingerprint string, sessions int) string {
	rows, meanIn, meanCost, ok := bench.PriorWorkflowCostFor(out, fingerprint)
	if !ok {
		return "no valid rows for this exact fingerprint — " +
			"each session records its measured tokens/cost, and the next preflight will project from them"
	}

	return fmt.Sprintf("~$%.2f for %d sessions (from %d matching rows: mean %dk context processed, $%.2f per session)",
		float64(sessions)*meanCost, sessions, rows, meanIn/1000, meanCost)
}
