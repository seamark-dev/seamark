package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/skills"
)

// workflowMinSharedCommits is the co-change threshold the index applies: a
// pair below it is never stored (internal/history, MinTogether default). The
// preflight requires the trigger and companion to reach it, or `change_set`
// has nothing to name and the experiment measures nothing.
const workflowMinSharedCommits = 2

// WorkflowPreflight validates every invariant that can be checked without
// spending an agent call. It runs the lessons preflight on the shared
// instance (determinism, judges, patches, checks, a treatment-free tree),
// then adds three gates of its own: the fixture history carries the
// trigger-companion co-change, the seamark binary answers an MCP initialize
// request as "seamark", and both arms wire exactly what they measure. Every
// failure names its gate.
func WorkflowPreflight(ctx context.Context, cfg WorkflowConfig) error {
	instance := cfg.Instance
	if err := instance.Validate(); err != nil {
		return err
	}

	if cfg.SeamarkBin == "" {
		return fmt.Errorf("preflight requires the seamark binary")
	}

	if _, err := resolvedWorkflowArms(cfg.Arms); err != nil {
		return err
	}

	if err := Preflight(ctx, RunConfig{
		Instance: instance.Instance, SeamarkBin: cfg.SeamarkBin, PrepareIndex: cfg.PrepareIndex,
	}); err != nil {
		return fmt.Errorf("shared instance gates: %w", err)
	}

	root, err := os.MkdirTemp("", "seamark-skills-preflight-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()

	// The co-change and MCP gates need the real binary. Hermetic tests keep
	// an inert fake path and skip them, as the lessons preflight skips indexing.
	if cfg.PrepareIndex {
		gate := filepath.Join(root, "cochange")
		if err := instance.Generate(gate); err != nil {
			return fmt.Errorf("co-change gate: generate fixture: %w", err)
		}

		if err := cochangeGate(ctx, cfg.SeamarkBin, gate, instance); err != nil {
			return fmt.Errorf("co-change gate: %w", err)
		}

		if err := mcpGate(ctx, cfg.SeamarkBin, gate); err != nil {
			return fmt.Errorf("mcp gate: %w", err)
		}
	}

	only := filepath.Join(root, string(ArmMCPOnly))
	withSkills := filepath.Join(root, string(ArmMCPSkills))

	for dir, arm := range map[string]WorkflowArm{only: ArmMCPOnly, withSkills: ArmMCPSkills} {
		if err := instance.Generate(dir); err != nil {
			return fmt.Errorf("wiring gate: generate %s fixture: %w", arm, err)
		}

		if _, err := wireWorkflowArm(ctx, dir, cfg, arm); err != nil {
			return fmt.Errorf("wiring gate: wire %s arm: %w", arm, err)
		}
	}

	if err := validateWorkflowWiring(only, withSkills); err != nil {
		return fmt.Errorf("wiring gate: %w", err)
	}

	return nil
}

// cochangeGate indexes a fresh fixture and requires `why <trigger>` to name
// the companion with enough shared commits. It reads the report's labels, so
// a column shift cannot fake a pass.
func cochangeGate(ctx context.Context, bin, dir string, instance WorkflowInstance) error {
	if err := excludeHarnessArtifacts(dir); err != nil {
		return err
	}

	if err := indexFixture(ctx, dir, bin); err != nil {
		return err
	}

	out, err := runSeamark(ctx, bin, dir, "why", instance.Trigger)
	if err != nil {
		return err
	}

	partners := parseWhyPartners(string(out))
	shared, named := partners[instance.Companion]

	switch {
	case !named:
		return fmt.Errorf("`why %s` does not list %s under \"usually changed with\"; the fixture history does not carry the pair",
			instance.Trigger, instance.Companion)
	case shared < workflowMinSharedCommits:
		return fmt.Errorf("`why %s` lists %s with %d shared commit(s), want at least %d",
			instance.Trigger, instance.Companion, shared, workflowMinSharedCommits)
	}

	// The companion must be the strongest partner among the files the task
	// does not plan. The first cohort ran on histories where it was the
	// weakest line, tied with test files the agent edits anyway, and the
	// agents read it as noise. The naive patch names the planned files.
	if err := instance.ApplyNaive(dir); err != nil {
		return fmt.Errorf("apply naive patch: %w", err)
	}

	planned, err := workingTreeChanges(ctx, dir)
	if err != nil {
		return err
	}

	for file, count := range partners {
		if file == instance.Companion || planned[file] {
			continue
		}

		if count > shared {
			return fmt.Errorf("`why %s` lists %s with %d shared commits above the companion %s with %d; the history does not make the companion the strongest unplanned partner",
				instance.Trigger, file, count, instance.Companion, shared)
		}
	}

	return nil
}

// workingTreeChanges lists the repository-relative files the working tree
// changed against HEAD, staged or not.
func workingTreeChanges(ctx context.Context, dir string) (map[string]bool, error) {
	setupCtx, cancel := context.WithTimeout(ctx, defaultSetupTimeout)
	defer cancel()

	cmd := exec.CommandContext(setupCtx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status in fixture: %w", err)
	}

	changed := map[string]bool{}

	for line := range strings.SplitSeq(string(out), "\n") {
		if len(line) > 3 {
			changed[strings.TrimSpace(line[3:])] = true
		}
	}

	return changed, nil
}

// whyPartnerLine matches one line of the "usually changed with" section:
// "  2/6   commits  lift 1.3   web/src/api/generated.ts  · mostly ...".
var whyPartnerLine = regexp.MustCompile(`^\s*(\d+)/(\d+)\s+commits\s+lift\s+\S+\s+(\S+)`)

// parseWhyPartners reads the co-change partners a `why <file>` report names,
// keyed by path with their shared-commit counts. Lines outside the "usually
// changed with" section are ignored.
func parseWhyPartners(output string) map[string]int {
	partners := map[string]int{}
	inSection := false

	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "usually changed with"):
			inSection = true

			continue
		case trimmed == "":
			inSection = false

			continue
		case !inSection:
			continue
		}

		match := whyPartnerLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		shared, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}

		partners[match[3]] = shared
	}

	return partners
}

// mcpGate starts the binary's MCP server on the fixture, sends one
// initialize request, and requires the reply to identify the server as
// "seamark". The agent's client would fail the same way silently, so this
// gate fails loudly before any paid session.
func mcpGate(ctx context.Context, bin, dir string) error {
	request := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "skills-bench", "version": "preflight"},
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		return err
	}

	out, err := runSeamarkWithInput(ctx, bin, dir, append(data, '\n'), "mcp")
	if err != nil {
		return err
	}

	name, err := mcpServerName(out)
	if err != nil {
		return err
	}

	if name != "seamark" {
		return fmt.Errorf("initialize reply names server %q, want \"seamark\"", name)
	}

	return nil
}

// mcpServerName reads serverInfo.name from the first JSON-RPC reply line.
func mcpServerName(output []byte) (string, error) {
	for line := range bytes.SplitSeq(output, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var reply struct {
			Result struct {
				ServerInfo struct {
					Name string `json:"name"`
				} `json:"serverInfo"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.Unmarshal(line, &reply); err != nil {
			return "", fmt.Errorf("initialize reply is not JSON-RPC: %w", err)
		}

		if reply.Error != nil {
			return "", fmt.Errorf("initialize failed: %s", reply.Error.Message)
		}

		return reply.Result.ServerInfo.Name, nil
	}

	return "", fmt.Errorf("the MCP server sent no reply")
}

func runSeamark(ctx context.Context, bin, dir string, args ...string) ([]byte, error) {
	return runSeamarkWithInput(ctx, bin, dir, nil, args...)
}

// runSeamarkWithInput runs the seamark binary inside the fixture with the
// trial environment and returns its stdout. Stderr is kept for the error
// message only: `why` prints its staleness note there.
func runSeamarkWithInput(ctx context.Context, bin, dir string, input []byte, args ...string) ([]byte, error) {
	setupCtx, cancel := context.WithTimeout(ctx, defaultSetupTimeout)
	defer cancel()

	cmd := exec.CommandContext(setupCtx, bin, append([]string{"-C", dir}, args...)...)
	cmd.Dir = dir

	env, err := agentEnvironment(dir)
	if err != nil {
		return nil, fmt.Errorf("prepare seamark environment: %w", err)
	}

	cmd.Env = env
	cmd.WaitDelay = processWaitDelay

	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if setupCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("seamark %s timed out after %s", strings.Join(args, " "), defaultSetupTimeout)
		}

		return nil, fmt.Errorf("seamark %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// validateWorkflowWiring asserts both arms carry the sandbox settings, the
// built-in skills switch, and the MCP tool rules, only the skills arm carries
// the skills and their rules, and neither carries a hook, a lesson, or an MCP
// registration file.
func validateWorkflowWiring(only, withSkills string) error {
	toolRules, err := workflowAllowRules(ArmMCPOnly)
	if err != nil {
		return err
	}

	allRules, err := workflowAllowRules(ArmMCPSkills)
	if err != nil {
		return err
	}

	names, err := skills.Names()
	if err != nil {
		return err
	}

	for dir, arm := range map[string]WorkflowArm{only: ArmMCPOnly, withSkills: ArmMCPSkills} {
		settings, err := hooks.ReadSettings(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", arm, err)
		}

		if _, hooked := settings["hooks"]; hooked {
			return fmt.Errorf("%s settings contain a hook", arm)
		}

		sandbox, _ := settings["sandbox"].(map[string]any)
		if sandbox["enabled"] != true || sandbox["failIfUnavailable"] != true {
			return fmt.Errorf("%s settings lack the strict sandbox block", arm)
		}

		allow := approve.AllowSet(settings)
		for _, rule := range toolRules {
			if !allow[rule] {
				return fmt.Errorf("%s settings lack the allow rule %s", arm, rule)
			}
		}

		if settings["disableBundledSkills"] != true {
			return fmt.Errorf("%s settings do not switch off Claude Code's built-in skills", arm)
		}

		overrides, _ := settings["skillOverrides"].(map[string]any)
		for name, mode := range builtInSkillOverrides {
			if overrides[name] != mode {
				return fmt.Errorf("%s settings lack the %s skill override", arm, name)
			}
		}

		for _, rel := range []string{".seamark/lessons.yaml", ".mcp.json"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
				return fmt.Errorf("%s arm contains %s", arm, rel)
			}
		}
	}

	onlySettings, err := hooks.ReadSettings(only)
	if err != nil {
		return err
	}

	for rule := range approve.AllowSet(onlySettings) {
		if strings.HasPrefix(rule, "Skill(") {
			return fmt.Errorf("mcp-only settings contain the skill rule %s", rule)
		}
	}

	if _, err := os.Stat(filepath.Join(only, filepath.FromSlash(skills.ClaudeDir))); !os.IsNotExist(err) {
		return fmt.Errorf("mcp-only arm contains %s", skills.ClaudeDir)
	}

	skillsSettings, err := hooks.ReadSettings(withSkills)
	if err != nil {
		return err
	}

	allow := approve.AllowSet(skillsSettings)
	for _, rule := range allRules {
		if !allow[rule] {
			return fmt.Errorf("mcp-skills settings lack the allow rule %s", rule)
		}
	}

	for _, state := range skills.Inspect(withSkills) {
		if state.Client != skills.ModeClaude {
			continue
		}

		if state.Err != "" || state.Current != len(names) || state.Stale > 0 || state.Foreign > 0 {
			return fmt.Errorf("mcp-skills arm skills are not the %d managed copies: %s", len(names), state.Describe())
		}
	}

	return nil
}
