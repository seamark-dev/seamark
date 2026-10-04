package integration

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/skills"
)

// Two init forms become one []ClientSetup here, so the setup
// coordinator never inspects a flag. The explicit form is `--client`.
// The legacy form is every init run without it, and it is the only
// place outside the adapters that names the two first clients: its
// rules were written for them, before a registry existed.

// ExplicitSetups translates `--client` into per-client intent. Every
// selected client gets its hooks and its MCP registration; the skills
// and the tool grants stay opt-in flags. Repeated IDs collapse and the
// result follows registry order, so reordered flags give one plan. An
// unknown ID fails here, before anything is read. An operation that a
// client does not support stays in the intent: the coordinator removes
// it and reports it, so partial support is visible to the user.
func ExplicitSetups(reg *Registry, ids []string, installSkills, approveTools bool, gateMode string) ([]ClientSetup, error) {
	if len(ids) == 0 {
		return nil, errors.New("no client selected")
	}

	selected, err := reg.Select(ids)
	if err != nil {
		return nil, err
	}

	setups := make([]ClientSetup, 0, len(selected))

	for _, c := range selected {
		setups = append(setups, ClientSetup{
			ClientID:     c.ID,
			Skills:       installSkills,
			Hooks:        true,
			RegisterMCP:  true,
			ApproveTools: approveTools,
			GateMode:     gateMode,
			// An explicit selection is the complete setup of the client, so
			// it looks at every source that can run a seamark hook.
			CheckHookSources: true,
		})
	}

	return setups, nil
}

// LegacySetups translates an init run without `--client` into the
// intent it always had. The intent is granular on purpose. It is not
// "fully configure every detected client":
//
//   - Claude Code always gets its hooks, whatever --skills names. It
//     never gets an MCP registration, because init never wrote .mcp.json.
//   - --approve-tools grants Claude Code unless --skills=codex. For Codex
//     it registers the server and approves the tools together, when
//     --skills names Codex or, with no client named, a .codex/ directory
//     exists.
//   - The skills go to the directories the --skills mode addresses. A
//     bare --skills detects Codex by .agents/, not by .codex/.
//
// skillsMode is one of skills.Modes, or empty for "not requested".
// Codex is left out when the run asks nothing of it, so its files are
// never read.
func LegacySetups(root, skillsMode string, approveTools bool, gateMode string) ([]ClientSetup, error) {
	// The run detects the approval targets only when it grants tools.
	// The detection calls stat on .codex/. A plain init asks nothing of
	// Codex, so it must not touch a Codex path. A broken Codex path then
	// cannot stop the Claude Code setup.
	var grantClaude, grantCodex bool

	if approveTools {
		var err error

		if grantClaude, grantCodex, err = legacyApprovalTargets(root, skillsMode); err != nil {
			return nil, err
		}
	}

	claude := ClientSetup{ClientID: ClaudeID, Hooks: true, GateMode: gateMode, ApproveTools: grantClaude}
	codex := ClientSetup{ClientID: CodexID, RegisterMCP: grantCodex, ApproveTools: grantCodex}

	if skillsMode != "" {
		targets, err := skills.Targets(root, skillsMode)
		if err != nil {
			return nil, err
		}

		for _, t := range targets {
			switch t.Dir {
			case skills.ClaudeDir:
				claude.Skills = true
			case skills.AgentsDir:
				codex.Skills = true
			}
		}
	}

	setups := []ClientSetup{claude}

	if codex.Skills || codex.RegisterMCP || codex.ApproveTools {
		setups = append(setups, codex)
	}

	return setups, nil
}

// legacyApprovalTargets names the clients --approve-tools configures in
// a run without `--client`, by one rule whether or not --skills is
// given. Claude Code is a target unless --skills=codex. Codex is a
// target when --skills names it (codex or all), or, without an explicit
// client, when a .codex/ directory exists, because that is where its
// configuration lives; --skills=claude means Claude Code only. A bare
// --skills detects the skills target by .agents/ and the approvals
// target by .codex/, each by the place its own artifact lives; init
// notes the gap when only one exists. Before this rule,
// --skills --approve-tools skipped a repository with .codex/ and no
// .agents/, right after the documentation promised the one-liner would
// configure it.
func legacyApprovalTargets(root, skillsMode string) (claude, codex bool, err error) {
	claude = skillsMode != skills.ModeCodex

	switch skillsMode {
	case skills.ModeClaude:
		return claude, false, nil
	case skills.ModeCodex, skills.ModeAll:
		return claude, true, nil
	}

	// A missing .codex/ means no Codex configuration. Any other stat
	// error is reported before init writes anything, because a directory
	// that cannot be read must not be silently treated as absent.
	info, err := os.Stat(filepath.Join(root, ".codex"))
	if errors.Is(err, os.ErrNotExist) {
		return claude, false, nil
	}

	if err != nil {
		return false, false, err
	}

	return claude, info.IsDir(), nil
}

// GateHookClients returns the IDs of the requested clients for which
// setup installs a command gate hook, in the given order. init reads
// the installed gate mode from them, and it says so when the list is
// empty: a gate line must never describe a hook that no selected client
// has. A client with lesson hooks and no gate hook is not in the list.
func GateHookClients(reg *Registry, setups []ClientSetup) []string {
	var ids []string

	for _, s := range setups {
		if c, ok := reg.Lookup(s.ClientID); ok && s.Hooks && c.SetupOps.GateHook {
			ids = append(ids, c.ID)
		}
	}

	return ids
}

// UngatedHookClients returns the names of the requested clients that
// get hooks and no command gate hook, in the given order. When another
// selected client has a gate hook, the gate line of the run describes
// that client only, and init must name the clients it does not cover.
func UngatedHookClients(reg *Registry, setups []ClientSetup) []string {
	var names []string

	for _, s := range setups {
		if c, ok := reg.Lookup(s.ClientID); ok && s.Hooks && c.SetupOps.Hooks && !c.SetupOps.GateHook {
			names = append(names, c.Name)
		}
	}

	return names
}

// InstalledGateMode returns the installed gate-hook mode of the
// clients: enforce when any of them enforces, else warn when any has a
// gate hook, else "". It reads only the managed hook document of each
// given client, through the adapter's ManagedGateMode. It never reads
// an unselected client. The plan reads every other document after
// this, so a full inspection here reads them twice. A definition that
// setup does not own never sets the mode of the run. The gate line
// names such a definition by its own mode.
//
// Enforce wins because one enforcing hook blocks whatever the others
// do: the run's policy scaffold and gate line must never read weaker
// than an installed hook. Each adapter still keeps its own installed
// mode when no mode is requested, so a client can run a warn hook in
// an enforce run; init names such a hook beside the gate line.
func InstalledGateMode(reg *Registry, root string, clientIDs []string) string {
	installed := ""

	for _, id := range clientIDs {
		c, ok := reg.Lookup(id)
		if !ok || c.Setup == nil {
			continue
		}

		switch c.Setup.ManagedGateMode(root) {
		case hooks.ModeEnforce:
			return hooks.ModeEnforce
		case hooks.ModeWarn:
			installed = hooks.ModeWarn
		}
	}

	return installed
}
