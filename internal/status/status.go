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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
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
}

// Gather assembles the health report from the store and the workspace.
func Gather(st *store.Store, root string) (*Status, error) {
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
		{"reviews_mined_at", func(v string) { s.ReviewsMinedAt = parseUnix(v) }},
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
		if _, argv, err := agent.Resolve(acfg); err == nil {
			// Repository-controlled text headed for terminals and MCP
			// clients: strip control sequences, scrub secret-shaped
			// values — same treatment as the distill preflight.
			s.DistillAgent = render.Sanitize(gate.RedactSecrets(strings.Join(argv, " ")))
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
		fmt.Fprintf(w, "distillation   %s — external data processing when run (see `lessons --distill --dry-run`)\n",
			s.DistillAgent)
	} else {
		fmt.Fprintf(w, "distillation   agent config missing or invalid\n")
	}

	printGate(w, s)
}

// printGate renders the effective gate behaviour. A broken policy means
// different things under different hooks — an enforce hook fails closed,
// a warn hook fails open — and the difference is the whole point of
// reporting it.
func printGate(w io.Writer, s *Status) {
	if s.GatePolicyError != "" {
		switch {
		case s.GateHookMode == hooks.ModeEnforce:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the enforce hook FAILS CLOSED: every hooked command blocks until the policy is fixed\n",
				s.GatePolicyError)
		case s.GateHookMode == hooks.ModeWarn:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the warn hook fails open: nothing blocks, and nothing is being checked\n",
				s.GatePolicyError)
		case s.GateHookError != "":
			// Broken policy AND unreadable hook config: the effective
			// behaviour is unknown, not "no hook".
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               hook configuration UNREADABLE (%s) — effective behaviour unknown\n",
				s.GatePolicyError, s.GateHookError)
		default:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s); no operational Claude hook\n", s.GatePolicyError)
		}

		return
	}

	switch {
	case s.GateHookError != "":
		fmt.Fprintf(w, "gate           policy mode %s; hook configuration UNREADABLE (%s)\n",
			s.GatePolicyMode, s.GateHookError)
	case s.GateHookMode == "":
		fmt.Fprintf(w, "gate           policy mode %s; no Claude hook installed (`seamark init`)\n",
			s.GatePolicyMode)
	case s.GateHookMode == hooks.ModeEnforce:
		fmt.Fprintf(w, "gate           enforce (hook carries --enforce; blocking verdicts exit 2)\n")
	default:
		fmt.Fprintf(w, "gate           hook installed; policy mode %s governs\n", s.GatePolicyMode)
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

// age renders a unix timestamp as a coarse human age.
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
