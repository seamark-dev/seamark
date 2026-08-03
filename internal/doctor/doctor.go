// Package doctor diagnoses the seamark installation: binary, git, the
// index database's schema and integrity, policy and effect-catalogue
// compilation, hook wiring, agent and gh availability, and gitignore
// sanity. Every check is read-only and offline — doctor reports exact
// corrective actions and changes nothing. Semantic health (coverage,
// confidence, freshness) is `seamark status`'s job; doctor asks whether
// seamark can run at all.
package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/render"
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
	r := &Report{}

	r.add("binary", StateOK, fmt.Sprintf("seamark %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH), "")

	checkGit(r, root)
	checkIndex(r, dbPath)
	checkPolicy(r, root)
	checkEffects(r, root)
	checkHooks(r, root)
	checkAgent(r, root)
	checkGH(r)
	checkMCP(r, root)
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

func checkHooks(r *Report, root string) {
	settings, err := hooks.ReadSettings(root)
	if err != nil {
		r.add("hooks", StateFail, err.Error(),
			"fix or move .claude/settings.json, then re-run `seamark init`")
		return
	}

	gateMode := hooks.InstalledGateMode(settings)
	lessons := hooks.LessonsHookInstalled(settings)

	switch {
	case gateMode == "" && !lessons:
		r.add("hooks", StateInfo, "no Claude Code hooks installed",
			"run `seamark init` to wire the gate and lessons hooks")
	case gateMode == "":
		r.add("hooks", StateWarn, "lessons hook installed, gate hook missing",
			"re-run `seamark init` to restore the gate hook")
	case !lessons:
		r.add("hooks", StateWarn, fmt.Sprintf("gate hook installed (%s), lessons hook missing", gateMode),
			"re-run `seamark init` to restore the lessons hook")
	default:
		r.add("hooks", StateOK, fmt.Sprintf("gate (%s) + lessons hooks installed", gateMode), "")
	}
}

func checkAgent(r *Report, root string) {
	cfg, err := agent.LoadConfig(root)
	if err != nil {
		r.add("agent", StateWarn, err.Error(),
			"fix the agent section of .seamark/config.yaml — distillation is unavailable until then")
		return
	}

	_, argv, err := agent.Resolve(cfg)
	if err != nil {
		r.add("agent", StateWarn, err.Error(),
			"fix the agent section of .seamark/config.yaml — distillation is unavailable until then")
		return
	}

	if _, err := exec.LookPath(argv[0]); err != nil {
		r.add("agent", StateWarn, fmt.Sprintf("agent CLI %q not found on PATH", argv[0]),
			"install it, or point agent.argv in .seamark/config.yaml at a CLI you have — only `lessons --distill` needs it")
		return
	}

	r.add("agent", StateOK, fmt.Sprintf("%s on PATH (used only by `lessons --distill`)", argv[0]), "")
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

func checkMCP(r *Report, root string) {
	data, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err != nil {
		r.add("mcp", StateInfo, "no project .mcp.json — MCP clients may be registered elsewhere",
			"to register for Claude Code: `claude mcp add seamark -- seamark mcp`")
		return
	}

	var cfg struct {
		Servers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		r.add("mcp", StateWarn, ".mcp.json is unparseable: "+err.Error(), "fix the JSON")
		return
	}

	for name, srv := range cfg.Servers {
		// Same basename rule as hooks.OwnedBySeamark: exact "seamark",
		// tolerating the Windows .exe suffix.
		if strings.TrimSuffix(filepath.Base(srv.Command), ".exe") == "seamark" {
			r.add("mcp", StateOK, fmt.Sprintf("registered in .mcp.json as %q", name), "")
			return
		}
	}

	r.add("mcp", StateInfo, ".mcp.json exists but registers no seamark server",
		"`claude mcp add seamark -- seamark mcp` to serve the index to agents")
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
