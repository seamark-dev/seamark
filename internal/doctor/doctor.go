// Package doctor diagnoses the seamark installation: binary, git, the
// index database's schema and integrity, policy and effect-catalogue
// compilation, hook wiring, agent and gh availability, agent skills,
// tool approvals, and gitignore sanity. Every check is read-only and offline — doctor reports exact
// corrective actions and changes nothing. Semantic health (coverage,
// confidence, freshness) is `seamark status`'s job; doctor asks whether
// seamark can run at all.
package doctor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/store"
)

// Check states, from healthy to broken. Info is a fact that needs no
// action; warn degrades a feature; fail means seamark cannot do its job.
const (
	StateOK   = "ok"
	StateInfo = "info"
	StateWarn = "warn"
	StateFail = "fail"
)

// Check is one diagnostic finding.
type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	// Fix is the exact corrective action; empty when none is needed.
	Fix string `json:"fix,omitempty"`
}

// Report is a full doctor run.
type Report struct {
	Checks []Check `json:"checks"`
	Warns  int     `json:"warns"`
	Fails  int     `json:"fails"`
}

func (r *Report) add(name, state, detail, fix string) {
	// Detail and fix can embed repository-controlled text (config
	// values, file paths); sanitize at the single funnel. Whitespace is
	// normalized FIRST: multi-line tool errors (yaml lists each problem
	// on its own line) must collapse to readable one-line text, not have
	// their newlines silently stripped into run-on words.
	r.Checks = append(r.Checks, Check{
		Name: name, State: state,
		Detail: render.Sanitize(oneLine(detail)),
		Fix:    render.Sanitize(oneLine(fix)),
	})

	switch state {
	case StateWarn:
		r.Warns++
	case StateFail:
		r.Fails++
	}
}

// Run executes every check against the workspace root. dbPath is the
// resolved index location; version identifies the binary.
func Run(root, dbPath, version string) *Report {
	return run(integration.Builtin(), root, dbPath, version)
}

// run is Run over one registry, so a test can hand it a registry with
// a fake client and check the client-neutral rendering.
func run(reg *integration.Registry, root, dbPath, version string) *Report {
	r := &Report{}

	r.add("binary", StateOK, fmt.Sprintf("seamark %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH), "")

	checkGit(r, root)
	checkIndex(r, dbPath)
	checkPolicy(r, root)
	checkEffects(r, root)

	// One inspection feeds every integration line, so the lines cannot
	// disagree with each other or with init and status.
	inspections := reg.Inspect(root)

	checkHooks(r, inspections)
	checkAgent(r, reg, root)
	checkGH(r)
	checkMCP(r, inspections)
	checkSkills(r, reg.InspectSkills(root), inspections)
	checkApprovals(r, inspections)
	checkClients(r, inspections)
	checkGitignore(r, root)

	return r
}

func checkGit(r *Report, root string) {
	if _, err := exec.LookPath("git"); err != nil {
		r.add("git", StateFail, "git not found on PATH",
			"install git — history mining, freshness detection and repo resolution need it")
		return
	}

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = root

	out, err := cmd.Output()
	if err != nil {
		r.add("git", StateWarn, fmt.Sprintf("%s is not inside a git repository", root),
			"run seamark from a git repository — without one there is no history layer and no freshness fingerprint")
		return
	}

	r.add("git", StateOK, "repository at "+firstLine(string(out)), "")
}

func checkIndex(r *Report, dbPath string) {
	if _, err := os.Stat(dbPath); err != nil {
		r.add("index", StateInfo, "no index database yet", "run `seamark index` to build it")
		return
	}

	version, err := store.ProbeVersion(dbPath)
	if err != nil {
		r.add("index", StateFail, fmt.Sprintf("%s is unreadable: %v", dbPath, err),
			"if `seamark state export` still works, export decisions first; then delete the file and run `seamark index`")
		return
	}

	switch {
	case version > store.SupportedSchema():
		r.add("index", StateFail,
			fmt.Sprintf("database schema v%d is newer than this binary (v%d)", version, store.SupportedSchema()),
			"upgrade seamark; do not delete the database — it holds your proposal decisions")
	case version == 0:
		r.add("index", StateWarn, "database predates schema versioning",
			"run `seamark index` — opening it once upgrades and stamps it")
	default:
		r.add("index", StateOK, fmt.Sprintf("%s (schema v%d)", dbPath, version), "")
	}

	verdict, err := store.Integrity(dbPath)
	if err != nil || verdict != "ok" {
		if verdict == "" {
			verdict = fmt.Sprint(err)
		}

		r.add("integrity", StateFail, "SQLite integrity check failed: "+verdict,
			"export decisions with `seamark state export` if possible, delete the file, run `seamark index`, re-import")

		return
	}

	r.add("integrity", StateOK, "SQLite integrity check passed", "")
}

func checkPolicy(r *Report, root string) {
	policy, err := gate.LoadPolicy(root)
	if err != nil {
		r.add("policy", StateFail, err.Error(),
			"fix .seamark/policy.yaml — an enforce hook fails closed on a broken policy, blocking every command")
		return
	}

	r.add("policy", StateOK, fmt.Sprintf("compiles (%d deny, %d require_approval rules; mode %s)",
		len(policy.Deny), len(policy.RequireApproval), policy.Mode), "")
}

func checkEffects(r *Report, root string) {
	if _, err := effects.Load(root); err != nil {
		r.add("effects", StateFail, err.Error(),
			"fix .seamark/effects.yaml — the gate cannot classify commands without the catalogue")
		return
	}

	r.add("effects", StateOK, "catalogue loads", "")
}

// checkHooks reports the lifecycle hooks of every client on one line,
// in registry order, from the shared inspection. Absent is a fact: a
// client is set up on request. A hook document that cannot be read, a
// hook the client runs for some tools only, or a client with one of its
// two hooks is a warning, because the user expects the other half.
func checkHooks(r *Report, inspections []integration.Inspection) {
	var (
		parts   []string
		actions []string
		state   = StateOK
		present int
	)

	for _, insp := range inspections {
		edits, _ := insp.Entry(integration.CapabilityEdits)
		commands, _ := insp.Entry(integration.CapabilityCommands)

		if !edits.Supported && !commands.Supported {
			continue
		}

		parts = append(parts, insp.ClientID+" "+insp.DescribeHooks())

		gateHook := commands.Supported && commands.State != integration.StateAbsent
		lessons := edits.Supported && edits.State != integration.StateAbsent

		switch {
		case edits.State == integration.StateUnreadable, commands.State == integration.StateUnreadable:
			state = StateWarn
			actions = append(actions, firstAction(edits, commands))
		case edits.State == integration.StatePartial, commands.State == integration.StatePartial:
			state = StateWarn
			actions = append(actions, firstAction(edits, commands))
		case gateHook != lessons && edits.Supported && commands.Supported:
			state = StateWarn
			actions = append(actions, "re-run `seamark init --client "+insp.ClientID+"` to restore the missing hook")
		}

		if gateHook || lessons {
			present++
		}
	}

	if present == 0 && state == StateOK {
		r.add("hooks", StateInfo, strings.Join(parts, " · "),
			"run `seamark init` to wire the Claude Code gate and lessons hooks; `seamark init --client codex` for Codex")

		return
	}

	r.add("hooks", state, strings.Join(parts, " · "), strings.Join(compact(actions), "; "))
}

// firstAction returns the first corrective action among entries.
func firstAction(entries ...integration.CapabilityInspection) string {
	for _, entry := range entries {
		if entry.Action != "" {
			return entry.Action
		}
	}

	return ""
}

// compact drops empty and repeated strings, keeping the first order.
func compact(items []string) []string {
	var out []string

	for _, item := range items {
		if item != "" && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}

	return out
}

// checkAgent reports the inference invoker: the client agent.cli
// selects, resolved through the registry, and whether its executable
// is on PATH. The invoker is configured apart from the clients that are
// set up, so the line names it; nothing here starts the client or a
// login.
func checkAgent(r *Report, reg *integration.Registry, root string) {
	cfg, err := agent.LoadConfig(root)
	if err != nil {
		r.add("agent", StateWarn, err.Error(),
			"fix the agent section of .seamark/config.yaml — distillation is unavailable until then")
		return
	}

	// Resolve the lesson consumers' command and check PATH without running it.
	spec, err := reg.ResolveInvocation(cfg, root)
	if err != nil {
		r.add("agent", StateWarn, err.Error(),
			"fix the agent section of .seamark/config.yaml — distillation is unavailable until then")
		return
	}

	if _, err := exec.LookPath(spec.Argv[0]); err != nil {
		r.add("agent", StateWarn, fmt.Sprintf("agent CLI %q not found on PATH (invoker %s)", spec.Argv[0], spec.Name),
			"install it, or point agent.argv in .seamark/config.yaml at a CLI you have — "+
				"only `lessons --distill` and `lessons --extract-triggers` need it")
		return
	}

	r.add("agent", StateOK, fmt.Sprintf("%s on PATH (invoker %s; used only by `lessons --distill` and `--extract-triggers`)",
		spec.Argv[0], spec.Name), "")
}

func checkGH(r *Report) {
	// Presence only — doctor stays offline, and `gh auth status` may
	// reach the network.
	if _, err := exec.LookPath("gh"); err != nil {
		r.add("gh", StateInfo, "gh not installed — only `seamark index --reviews` needs it",
			"install GitHub CLI and `gh auth login` to enable review mining")
		return
	}

	r.add("gh", StateOK, "gh on PATH (auth not probed — run `gh auth status`)", "")
}

// checkMCP reports the seamark MCP registration of every client that
// can hold one, from the shared inspection. Not registered is a fact:
// registration is part of explicit client setup, and the fix names the
// command for each client that lacks it. A file that cannot be read or
// a foreign registration under the seamark name is a warning.
func checkMCP(r *Report, inspections []integration.Inspection) {
	var (
		parts, actions []string
		state          = StateOK
		registered     int
	)

	for _, insp := range inspections {
		entry, ok := insp.Entry(integration.CapabilityMCPRegistration)
		if !ok || !entry.Supported {
			continue
		}

		parts = append(parts, insp.ClientID+" "+entry.Describe())

		switch entry.State {
		case integration.StateUnreadable, integration.StateConflict:
			state = StateWarn
			actions = append(actions, entry.Action)
		case integration.StateCurrent:
			registered++
		default:
			actions = append(actions, entry.Action)
		}
	}

	if state == StateOK && registered == 0 {
		state = StateInfo
	}

	if state == StateOK {
		actions = nil
	}

	r.add("mcp", state, strings.Join(parts, " · "), strings.Join(compact(actions), "; "))
}

// checkSkills reports the agent skills per destination, labelled by
// every client that reads it. Not installed is a fact, not a fault:
// skills are opt-in until the workflow evaluation decides otherwise. A
// stale or missing managed copy is a warning, because a client would
// load text that no longer matches this binary's tool surface. A
// directory under a seamark skill name that seamark does not own is
// named and never touched. The verdict comes from the per-client
// entries of the inspection, the action from the same entries.
func checkSkills(r *Report, states []skills.ClientState, inspections []integration.Inspection) {
	detail := skills.Details(states)
	worst := integration.StateAbsent
	action := ""

	// Unreadable outranks partial outranks conflict: a foreign directory
	// beside a stale copy still needs the refresh first.
	rank := map[integration.CapabilityState]int{
		integration.StateAbsent: 0, integration.StateCurrent: 1, integration.StateConflict: 2,
		integration.StatePartial: 3, integration.StateUnreadable: 4,
	}

	for _, insp := range inspections {
		entry, ok := insp.Entry(integration.CapabilitySkills)
		if !ok || !entry.Supported {
			continue
		}

		// The action follows the worst entry; among equals the first
		// with an action speaks.
		if rank[entry.State] > rank[worst] || (rank[entry.State] == rank[worst] && action == "") {
			worst, action = entry.State, entry.Action
		}
	}

	switch worst {
	case integration.StateUnreadable, integration.StatePartial:
		r.add("skills", StateWarn, detail, action)
	case integration.StateConflict:
		// A foreign directory outranks "not installed": `seamark init
		// --skills` never replaces it, so the fix must say what to do first.
		r.add("skills", StateInfo, detail, action)
	case integration.StateAbsent:
		r.add("skills", StateInfo, "agent skills not installed ("+detail+")",
			action+" for "+strings.Join(clientNames(inspections), " and "))
	default:
		r.add("skills", StateOK, detail, "")
	}
}

// clientNames lists the display names of the clients that read skills.
func clientNames(inspections []integration.Inspection) []string {
	var names []string

	for _, insp := range inspections {
		if entry, ok := insp.Entry(integration.CapabilitySkills); ok && entry.Supported {
			names = append(names, insp.Name)
		}
	}

	return names
}

// checkApprovals reports whether the project configuration lets the
// seamark MCP tools run without prompts, per client, from the shared
// inspection. Not configured is a fact, because approval is opt-in.
// Partial, conflicting, and unreadable configuration each get their
// own action, the adapter's. The detail names project configuration
// only: user-level and managed client policy can still prompt.
func checkApprovals(r *Report, inspections []integration.Inspection) {
	var (
		parts   []string
		worst   = integration.StateAbsent
		action  string
		current int
	)

	rank := map[integration.CapabilityState]int{
		integration.StateAbsent: 0, integration.StateCurrent: 1, integration.StatePartial: 2,
		integration.StateConflict: 3, integration.StateUnreadable: 4,
	}

	for _, insp := range inspections {
		entry, ok := insp.Entry(integration.CapabilityToolGrants)
		if !ok || !entry.Supported {
			continue
		}

		parts = append(parts, insp.ClientID+" "+entry.Describe())

		// The action follows the worst entry; among equals the first
		// with an action speaks.
		if rank[entry.State] > rank[worst] || (rank[entry.State] == rank[worst] && action == "") {
			worst, action = entry.State, entry.Action
		}

		if entry.State == integration.StateCurrent {
			current++
		}
	}

	detail := strings.Join(parts, " · ")

	switch {
	case worst == integration.StateUnreadable, worst == integration.StateConflict, worst == integration.StatePartial:
		r.add("approvals", StateWarn, detail, action)
	case current == 0:
		r.add("approvals", StateInfo, "tool approvals not configured ("+detail+")", action)
	default:
		r.add("approvals", StateOK, detail+" (project configuration; user or managed policy can still prompt)", "")
	}
}

// checkClients prints one line per client that has something to say
// beyond the topical lines: the limitations its adapter reports (trust
// it cannot read, a receiving context it does not identify, a handler
// that runs twice, hooks turned off) and the native evidence for what
// is installed. A warning finding makes the line a warning; evidence
// and informational findings are facts. A client with nothing
// installed and nothing to report gets no line: an absent optional
// capability is not a broken installation.
func checkClients(r *Report, inspections []integration.Inspection) {
	for _, insp := range inspections {
		var (
			parts []string
			state = StateInfo
			fix   string
		)

		if evidence := insp.DescribeVerification(); evidence != "" {
			parts = append(parts, evidence)
		}

		for _, finding := range insp.Findings {
			reason := finding.Reason
			if finding.Path != "" {
				reason = finding.Path + ": " + reason
			}

			parts = append(parts, reason)

			// A warning's action outranks an informational one: the fix
			// line must name what stops the warning.
			if finding.Level == integration.FindingWarning {
				if state != StateWarn && finding.Action != "" {
					fix = finding.Action
				}

				state = StateWarn
			} else if fix == "" && finding.Action != "" {
				fix = finding.Action
			}
		}

		if len(parts) == 0 {
			continue
		}

		r.add(insp.ClientID, state, strings.Join(parts, "; "), fix)
	}
}

// checkGitignore verifies the policy-as-code overlays are not ignored:
// an ignored policy silently drops out of review, and reviewability is
// the only integrity story policy has today.
func checkGitignore(r *Report, root string) {
	ignored := []string{}

	for _, rel := range []string{
		".seamark/policy.yaml", ".seamark/effects.yaml",
		".seamark/lessons.yaml", ".seamark/config.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			continue
		}

		cmd := exec.Command("git", "check-ignore", "-q", rel)
		cmd.Dir = root

		out, err := cmd.CombinedOutput()

		var exit *exec.ExitError

		switch {
		case err == nil: // exit 0: the path IS ignored
			ignored = append(ignored, rel)
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			// exit 1: confirmed NOT ignored — the healthy outcome.
		default:
			// Anything else (exit 128, git missing): the status is
			// UNDETERMINED, which must never masquerade as a clean OK.
			r.add("gitignore", StateInfo,
				fmt.Sprintf("could not determine ignore status: %s", firstLine(string(out))),
				"")

			return
		}
	}

	if len(ignored) > 0 {
		r.add("gitignore", StateWarn, fmt.Sprintf("committed-by-design files are gitignored: %v", ignored),
			"re-run `seamark init` to restore the .gitignore carve-outs — policy-as-code must stay reviewable")
		return
	}

	r.add("gitignore", StateOK, "policy-as-code overlays are not ignored", "")
}

// Print renders the report; one aligned line per check, fixes indented.
func Print(w io.Writer, r *Report) {
	fmt.Fprintf(w, "seamark doctor — installation health (read-only)\n")

	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %-5s %-10s %s\n", c.State, c.Name, c.Detail)

		if c.Fix != "" {
			fmt.Fprintf(w, "        %-10s → %s\n", "", c.Fix)
		}
	}

	switch {
	case r.Fails > 0:
		fmt.Fprintf(w, "\n%d check(s) failed, %d warning(s) — nothing was changed\n", r.Fails, r.Warns)
	case r.Warns > 0:
		fmt.Fprintf(w, "\n%d warning(s) — nothing was changed\n", r.Warns)
	default:
		fmt.Fprintf(w, "\nall checks passed\n")
	}
}

// oneLine collapses all whitespace runs (newlines included) to single
// spaces, so multi-line error messages stay readable in one-line output.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// firstLine returns s up to the first newline, without the trailing \r
// git emits on Windows.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}

	return strings.TrimSuffix(s, "\r")
}
