// Package status gathers and renders the index's semantic health: how
// much of the workspace is actually covered, how confident the edges
// are, how old the history evidence is, and which integrations are
// live. Every safety-sensitive answer needs this context to be
// interpretable — "no effects found" from a half-parsed index is not
// "no effects" (RFC-001 §5.2). Shared by the CLI command and the MCP
// status resource.
package status

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/store"
)

// Coverage is the persisted file summary of the last index run.
type Coverage struct {
	// Known is false for indexes written before coverage was persisted;
	// the renderer must say "unknown", never imply full coverage.
	Known        bool `json:"known"`
	FilesSeen    int  `json:"files_seen,omitempty"`
	FilesParsed  int  `json:"files_parsed,omitempty"`
	FilesSkipped int  `json:"files_skipped,omitempty"`
	ParseErrors  int  `json:"parse_errors"`
}

// Freshness states: whether the index still matches the workspace.
const (
	FreshCurrent = "current"
	FreshStale   = "stale"
	// FreshUnknown means no fingerprint exists to compare — a non-git
	// workspace has no cheap change signal. Unknown is not current.
	FreshUnknown = "unknown"
)

// Status is the gathered health report, JSON-ready.
type Status struct {
	SchemaVersion string   `json:"schema_version"`
	IndexedAt     int64    `json:"indexed_at,omitempty"`
	Freshness     string   `json:"freshness"`
	Coverage      Coverage `json:"coverage"`

	Symbols     int            `json:"symbols"`
	Edges       int            `json:"edges"`
	EdgeOrigins map[string]int `json:"edge_origins,omitempty"`

	EffectsDirect     int `json:"effects_direct_symbols"`
	EffectsPropagated int `json:"effects_propagated_symbols"`

	History store.HistoryWindow `json:"history"`

	Lessons        int            `json:"lessons"`
	Findings       map[string]int `json:"findings,omitempty"`
	ReviewsMinedAt int64          `json:"reviews_mined_at,omitempty"`

	// DistillAgent is the resolved agent command line ("" when the
	// config is invalid); external data processing is implied whenever
	// it is set — the CLI is assumed to reach a remote model. The value
	// is sanitized and secret-scrubbed: it comes from repository-
	// controlled config and flows to terminals and MCP clients.
	DistillAgent string `json:"distill_agent,omitempty"`

	// GatePolicyMode is policy.yaml's mode; GateHookMode is what the
	// installed Claude hook does ("" = no operational hook). They can
	// differ, and the difference is exactly what a reader needs to see.
	GatePolicyMode string `json:"gate_policy_mode"`
	GateHookMode   string `json:"gate_hook_mode,omitempty"`
	// GateHookError reports a hook configuration that cannot be read —
	// which is not the same finding as "no hook installed".
	GateHookError string `json:"gate_hook_error,omitempty"`
	// GatePolicyError carries a policy file that fails to load — a state
	// that changes every hook decision.
	GatePolicyError string `json:"gate_policy_error,omitempty"`

	// Skills is the agent-skills state per client directory. A stale copy
	// after an upgrade must be visible here, beside the hook and MCP
	// state, because a client would load text that no longer matches the
	// binary's tool surface.
	Skills []skills.ClientState `json:"skills,omitempty"`

	// Approvals is the tool-approval configuration per client: whether
	// the seamark MCP tools can run without prompts. Project
	// configuration only; user or managed policy can still prompt.
	Approvals []approve.ClientApproval `json:"approvals,omitempty"`

	// DistillClient names the invoker DistillAgent resolves to: a
	// registered client ("claude", "codex") or "custom" for agent.argv.
	// The invoker is configured apart from the clients that are set up,
	// so a reader must not infer it from Clients.
	DistillClient string `json:"distill_client,omitempty"`

	// Clients is the per-client integration view, in registry order:
	// which capabilities each client declares, the configuration state
	// of each, the project trust seamark can read, the native
	// compatibility evidence, and the limitations the adapter reports.
	// Additive: the fields above keep their Claude Code meaning.
	Clients []integration.Inspection `json:"clients,omitempty"`
}

// Gather assembles the health report from the store and the workspace.
func Gather(st *store.Store, root string) (*Status, error) {
	return gather(integration.Builtin(), st, root)
}

// gather is Gather over one registry, so a test can hand it a registry
// with a fake client and check the client-neutral rendering.
func gather(reg *integration.Registry, st *store.Store, root string) (*Status, error) {
	// Unknown until a recorded fingerprint proves otherwise: a non-git
	// workspace has no change signal, and unknown must not read as
	// current.
	s := &Status{Freshness: FreshUnknown}

	for _, m := range []struct {
		key string
		fn  func(v string)
	}{
		{"schema_version", func(v string) { s.SchemaVersion = v }},
		{"indexed_at", func(v string) { s.IndexedAt = parseUnix(v) }},
		{store.MetaReviewsMinedAt, func(v string) { s.ReviewsMinedAt = parseUnix(v) }},
		{"index_summary", func(v string) {
			var c Coverage
			if json.Unmarshal([]byte(v), &c) == nil {
				c.Known = true
				s.Coverage = c
			}
		}},
		{"indexed_state", func(v string) {
			if v == index.WorkspaceState(root) {
				s.Freshness = FreshCurrent
			} else {
				s.Freshness = FreshStale
			}
		}},
	} {
		v, err := st.GetMeta(m.key)
		if err != nil {
			return nil, err
		}

		if v != "" {
			m.fn(v)
		}
	}

	stats, err := st.Stats()
	if err != nil {
		return nil, err
	}

	s.Symbols, s.Edges, s.Lessons = stats.Symbols, stats.Edges, stats.Lessons

	if s.EdgeOrigins, err = st.EdgeOriginCounts(); err != nil {
		return nil, err
	}

	if s.EffectsDirect, s.EffectsPropagated, err = st.EffectOriginCounts(); err != nil {
		return nil, err
	}

	if s.History, err = st.History(); err != nil {
		return nil, err
	}

	if s.Findings, err = st.FindingCounts(); err != nil {
		return nil, err
	}

	// Integration state, all read-only and failure-tolerant: status must
	// describe a broken setup, not fail on it.
	if acfg, err := agent.LoadConfig(root); err == nil {
		if spec, err := reg.ResolveInvocation(acfg, root); err == nil {
			// Repository-controlled text headed for terminals and MCP
			// clients: strip control sequences, scrub secret-shaped
			// values — same treatment as the distill preflight.
			s.DistillAgent = render.Sanitize(redact.Secrets(strings.Join(spec.Argv, " ")))
			s.DistillClient = spec.Name
		}
	}

	policy, err := gate.LoadPolicy(root)
	if err != nil {
		s.GatePolicyError = err.Error()
	} else {
		s.GatePolicyMode = policy.Mode
	}

	mode, hookErr := hooks.InstalledGateModeAt(root)
	s.GateHookMode = mode

	if hookErr != nil {
		s.GateHookError = hookErr.Error()
	}

	// Inspect never fails: an unreadable directory is recorded on its
	// client record, and status describes it. The skills list and the
	// per-client view come from one registry inspection, so the two
	// cannot disagree; the approvals list keeps its legacy shape.
	s.Skills = reg.InspectSkills(root)
	s.Approvals = approve.Inspect(root)
	s.Clients = reg.Inspect(root)

	return s, nil
}

// Print renders the report as aligned text, the RFC's status layout.
func Print(w io.Writer, s *Status) {
	fresh := s.Freshness
	switch s.Freshness {
	case FreshStale:
		fresh = "STALE — run `seamark index`"
	case FreshUnknown:
		fresh = "freshness unknown (no git fingerprint) — reindex when in doubt"
	}

	fmt.Fprintf(w, "workspace      %s (schema v%s)\n", fresh, s.SchemaVersion)

	switch {
	case !s.Coverage.Known:
		fmt.Fprintf(w, "parsed         unknown — reindex with this seamark to record coverage\n")
	case s.Coverage.ParseErrors > 0:
		fmt.Fprintf(w, "parsed         %d of %d seen files (%d skipped by config) — %d PARSE ERRORS:\n"+
			"               those files are invisible to every answer\n",
			s.Coverage.FilesParsed, s.Coverage.FilesSeen, s.Coverage.FilesSkipped, s.Coverage.ParseErrors)
	default:
		fmt.Fprintf(w, "parsed         %d of %d seen files (%d skipped by config)\n",
			s.Coverage.FilesParsed, s.Coverage.FilesSeen, s.Coverage.FilesSkipped)
	}

	fmt.Fprintf(w, "symbols        %d, %d edges — call resolution %s\n",
		s.Symbols, s.Edges, originSummary(s.EdgeOrigins))
	fmt.Fprintf(w, "effects        %d direct-sink symbols, %d by propagation\n",
		s.EffectsDirect, s.EffectsPropagated)

	if s.History.Decisions > 0 {
		fmt.Fprintf(w, "history        %d decisions; evidence median age %s (oldest %s)\n",
			s.History.Decisions, age(s.History.MedianTS), age(s.History.OldestTS))
	} else {
		fmt.Fprintf(w, "history        none mined — co-change and decisions are empty\n")
	}

	// Review mining (GitHub) and fix mining (local git) are separate
	// integrations: fix findings must not make review fetching look
	// alive when it never ran.
	reviewFindings := s.Findings["review"]

	switch {
	case s.ReviewsMinedAt > 0:
		fmt.Fprintf(w, "reviews        %d lessons from %d review findings; last mined %s ago\n",
			s.Lessons, reviewFindings, age(s.ReviewsMinedAt))
	case reviewFindings > 0 || s.Lessons > 0:
		// Mined by a seamark from before the freshness stamp existed:
		// the evidence is real, its age is not knowable.
		fmt.Fprintf(w, "reviews        %d lessons from %d review findings; last mined unknown — re-run `seamark index --reviews`\n",
			s.Lessons, reviewFindings)
	default:
		fmt.Fprintf(w, "reviews        never mined — run `seamark index --reviews`\n")
	}

	// Only the fix-mined sources: a future provider (say ci:failure)
	// must not be silently counted as a fix.
	fixes := 0
	for _, src := range model.FixMinedSources() {
		fixes += s.Findings[src]
	}

	if fixes > 0 {
		noun := "findings"
		if fixes == 1 {
			noun = "finding"
		}

		fmt.Fprintf(w, "fixes          %d %s mined from local git\n", fixes, noun)
	}

	if s.DistillAgent != "" {
		fmt.Fprintf(w, "distillation   %s (invoker %s) — external data processing when run (see `lessons --distill --dry-run`)\n",
			s.DistillAgent, s.DistillClient)
	} else {
		fmt.Fprintf(w, "distillation   agent config missing or invalid\n")
	}

	printGate(w, s)
	printSkills(w, s)
	printApprovals(w, s)
	printClients(w, s)
}

// printClients renders the per-client block: one line per client with
// its hooks and its MCP registration, then the native evidence for
// what is installed and each limitation the adapter reports. The
// skills and approvals lines above already cover those two per client.
// A client with nothing installed still gets its line, so a reader
// sees which clients exist and that they are not set up; a client with
// nothing installed gets no evidence or limitation lines, because an
// absent optional capability is not a broken installation.
func printClients(w io.Writer, s *Status) {
	label := "clients"

	for _, insp := range s.Clients {
		mcp, _ := insp.Entry(integration.CapabilityMCPRegistration)

		registration := "MCP registration " + mcp.Describe()
		if mcp.State == integration.StateCurrent {
			registration = mcp.Describe()
		}

		fmt.Fprintf(w, "%-14s %-7s %s; %s\n", label, insp.ClientID,
			render.Sanitize(insp.DescribeHooks()), render.Sanitize(registration))

		label = ""

		if evidence := insp.DescribeVerification(); evidence != "" {
			fmt.Fprintf(w, "%-14s %-7s %s\n", "", insp.ClientID, render.Sanitize(evidence))
		}

		for _, finding := range insp.Findings {
			reason := finding.Reason
			if finding.Path != "" {
				reason = finding.Path + ": " + reason
			}

			if finding.Action != "" {
				reason += " — " + finding.Action
			}

			fmt.Fprintf(w, "%-14s %-7s %s: %s\n", "", insp.ClientID, finding.Level, render.Sanitize(reason))
		}
	}
}

// printApprovals renders the tool-approval line beside the skills line.
// Not configured states the command, because approval is opt-in; a
// registration without approvals is partial, so it is spelled out with
// the re-run hint, like a partial, conflicting, or unreadable one.
func printApprovals(w io.Writer, s *Status) {
	for _, c := range s.Approvals {
		if c.State() != approve.StateNotConfigured {
			fmt.Fprintf(w, "approvals      %s\n", render.Sanitize(approve.Summary(s.Approvals)))

			return
		}
	}

	fmt.Fprintf(w, "approvals      not configured (`seamark init --approve-tools`)\n")
}

// printSkills renders the agent-skills line beside the gate line. Not
// installed states the command, because skills are opt-in; a stale,
// foreign, or unreadable directory is spelled out, because a client
// would otherwise load text that no longer matches this binary, or
// `seamark init --skills` would not install what the reader expects.
func printSkills(w io.Writer, s *Status) {
	if slices.ContainsFunc(s.Skills, skills.ClientState.Notable) {
		fmt.Fprintf(w, "skills         %s\n", render.Sanitize(skills.Summary(s.Skills)))

		return
	}

	fmt.Fprintf(w, "skills         not installed (`seamark init --skills`)\n")
}

// gateHooks summarizes the gate hooks of every client from the
// per-client view: the clients that enforce, the clients with a warn
// hook, and the clients whose hook document cannot be read. The legacy
// Claude Code fields stay in the JSON; the text reads the broader view,
// because a Codex gate hook blocks a Codex command whatever
// .claude/settings.json says.
type gateHooks struct {
	enforce, warn, unreadable []string
}

// summarizeGateHooks reads every client's gate hook state.
func summarizeGateHooks(s *Status) gateHooks {
	var g gateHooks

	for _, insp := range s.Clients {
		commands, ok := insp.Entry(integration.CapabilityCommands)
		if !ok || !commands.Supported {
			continue
		}

		switch {
		case commands.State == integration.StateUnreadable:
			g.unreadable = append(g.unreadable, insp.ClientID+": "+commands.Detail)
		case insp.GateMode == hooks.ModeEnforce:
			g.enforce = append(g.enforce, insp.ClientID)
		case insp.GateMode == hooks.ModeWarn:
			g.warn = append(g.warn, insp.ClientID)
		}
	}

	// A status built without the per-client view (an older JSON
	// document) still has the legacy fields.
	if len(s.Clients) == 0 {
		switch {
		case s.GateHookError != "":
			g.unreadable = append(g.unreadable, "claude: "+s.GateHookError)
		case s.GateHookMode == hooks.ModeEnforce:
			g.enforce = append(g.enforce, "claude")
		case s.GateHookMode == hooks.ModeWarn:
			g.warn = append(g.warn, "claude")
		}
	}

	return g
}

// printGate renders the effective gate behaviour. A broken policy means
// different things under different hooks — an enforce hook fails closed,
// a warn hook fails open — and the difference is the whole point of
// reporting it. The line covers every client with a gate hook; the
// installed hook mode and the policy mode are named apart, because a
// warn hook still follows an enforcing policy file.
func printGate(w io.Writer, s *Status) {
	g := summarizeGateHooks(s)
	names := func(ids []string) string { return strings.Join(ids, ", ") }

	if s.GatePolicyError != "" {
		switch {
		case len(g.enforce) > 0:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the enforce hook FAILS CLOSED (%s): every hooked command blocks until the policy is fixed\n",
				s.GatePolicyError, names(g.enforce))
		case len(g.warn) > 0:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the warn hook fails open (%s): nothing blocks, and nothing is being checked\n",
				s.GatePolicyError, names(g.warn))
		case len(g.unreadable) > 0:
			// Broken policy AND unreadable hook config: the effective
			// behaviour is unknown, not "no hook".
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               hook configuration UNREADABLE (%s) — effective behaviour unknown\n",
				s.GatePolicyError, render.Sanitize(names(g.unreadable)))
		default:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s); no operational gate hook\n", s.GatePolicyError)
		}

		return
	}

	switch {
	case len(g.unreadable) > 0 && len(g.enforce)+len(g.warn) == 0:
		fmt.Fprintf(w, "gate           policy mode %s; hook configuration UNREADABLE (%s)\n",
			s.GatePolicyMode, render.Sanitize(names(g.unreadable)))
	case len(g.enforce)+len(g.warn) == 0:
		fmt.Fprintf(w, "gate           policy mode %s; no gate hook installed (`seamark init`, or `seamark init --client <name>`)\n",
			s.GatePolicyMode)
	case len(g.enforce) > 0 && len(g.warn) > 0:
		fmt.Fprintf(w, "gate           enforce for %s (hook carries --enforce; blocking verdicts exit 2); "+
			"the %s hook follows policy mode %s\n", names(g.enforce), names(g.warn), s.GatePolicyMode)
	case len(g.enforce) > 0:
		fmt.Fprintf(w, "gate           enforce (%s hook carries --enforce; blocking verdicts exit 2)\n", names(g.enforce))
	default:
		fmt.Fprintf(w, "gate           hook installed (%s); policy mode %s governs\n", names(g.warn), s.GatePolicyMode)
	}

	if len(g.unreadable) > 0 && len(g.enforce)+len(g.warn) > 0 {
		fmt.Fprintf(w, "               hook configuration UNREADABLE for %s\n", render.Sanitize(names(g.unreadable)))
	}
}

// originSummary renders the call-edge confidence distribution as
// percentages of the CALL edges (the only kind with resolution
// uncertainty), largest share first.
func originSummary(origins map[string]int) string {
	calls := 0
	for _, n := range origins {
		calls += n
	}

	if calls == 0 {
		return "none"
	}

	keys := make([]string, 0, len(origins))
	for k := range origins {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if origins[keys[i]] != origins[keys[j]] {
			return origins[keys[i]] > origins[keys[j]]
		}

		return keys[i] < keys[j] // deterministic order for equal counts
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d%% %s", origins[k]*100/calls, k))
	}

	return strings.Join(parts, " · ") + fmt.Sprintf(" (%d calls)", calls)
}

// age renders a Unix timestamp as a coarse relative age.
func age(ts int64) string {
	if ts <= 0 {
		return "unknown"
	}

	d := time.Since(time.Unix(ts, 0))

	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func parseUnix(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}

	return n
}
