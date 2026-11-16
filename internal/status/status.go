// Package status gathers and renders the index's semantic health: how
// much of the workspace is actually covered, how confident the edges
// are, how old the history evidence is, and which integrations are
// live. Every safety-sensitive answer needs this context to be
// interpretable — "no effects found" from a half-parsed index is not
// "no effects" (RFC-001 §5.2). Shared by the CLI command and the MCP
// status resource.
package status

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/skills"
	"github.com/seamark-dev/seamark/internal/store"
)

// Coverage is the persisted file summary of the last index run. The
// indexhealth package owns its rules, so status and orient read one
// summary in the same way.
type Coverage = indexhealth.Coverage

// Freshness states: whether the index still matches the workspace.
const (
	FreshCurrent = indexhealth.FreshCurrent
	FreshStale   = indexhealth.FreshStale
	FreshUnknown = indexhealth.FreshUnknown
)

// Stale reasons: why a stale index is stale.
const (
	StaleWorkspaceChanged = indexhealth.StaleWorkspaceChanged
	StaleOtherVersion     = indexhealth.StaleOtherVersion
)

// Status is the gathered health report, JSON-ready.
type Status struct {
	// RecordedRoot is the root that the index records, set only when it
	// is not the workspace of the command. Every other line describes
	// that root, so a reader must see it. It is sanitized, because it
	// comes from the database. Without it the JSON keeps its bytes.
	RecordedRoot string `json:"recorded_root,omitempty"`
	// rootNotice is the text of the root line. The JSON holds only
	// RecordedRoot, because the route is advice for a terminal.
	rootNotice string
	// staleRoute replaces the plain index run of a stale index when a
	// build in the workspace is refused, because that run would fail.
	staleRoute string

	SchemaVersion string `json:"schema_version"`
	IndexedAt     int64  `json:"indexed_at,omitempty"`
	Freshness     string `json:"freshness"`
	// StaleReason says why a stale index is stale (indexhealth
	// StaleWorkspaceChanged or StaleOtherVersion). Any other freshness
	// leaves it out, so the JSON of a current index keeps its bytes.
	StaleReason string   `json:"stale_reason,omitempty"`
	Coverage    Coverage `json:"coverage"`

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

	// GatePolicyMode is the mode of policy.yaml. GateHookMode is the
	// marker mode of the Claude Code gate hook that setup manages, or ""
	// when setup manages none. It keeps that mode also for a hook that
	// cannot start, because setup keeps it, so a reader pairs it with the
	// Claude Code entry of Clients. The policy mode and the hook mode can
	// differ, and a reader must see the difference.
	// Both hook fields come from the reader of the Claude Code inspection,
	// so they agree with the Claude Code entry of Clients.
	GatePolicyMode string `json:"gate_policy_mode"`
	GateHookMode   string `json:"gate_hook_mode,omitempty"`
	// GateHookError names a hook document that cannot be read. That is
	// not the same finding as "no hook installed".
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

	// LessonChecks is the lesson-capability/1 report: the requested
	// modes of lessons.yaml against the installed lesson-check hook
	// paths. It is nil when no lesson-check or exploration hook is
	// installed and lessons.yaml uses no lesson-check key, so such a
	// repository keeps its JSON bytes.
	LessonChecks *lessoncheck.CapabilityReport `json:"lesson_checks,omitempty"`
	// lessonPins are the pin counts that the pins line of the lesson
	// checks block prints. The JSON holds the report only.
	lessonPins pinCounts
}

// Gather assembles the health report from the store and the workspace.
// root is the root that the report describes. The CLI, the console, and
// the MCP server pass the root that the index records, or the workspace
// when the index records none. The freshness then compares the index
// with that root, so a stale route into that root refreshes the index.
func Gather(st *store.Store, root string) (*Status, error) {
	return gather(integration.Builtin(), st, root)
}

// GatherAs is Gather for the running binary self. The lesson-check
// report identifies each hook binary against it. Gather reads the
// identity from the build information of the running binary.
func GatherAs(st *store.Store, root string, self integration.BinaryIdentity) (*Status, error) {
	return gatherAs(integration.Builtin(), st, root, self)
}

// gather is Gather over one registry, so a test can hand it a registry
// with a fake client and check the client-neutral rendering.
func gather(reg *integration.Registry, st *store.Store, root string) (*Status, error) {
	return gatherAs(reg, st, root, integration.SelfIdentity(""))
}

// gatherAs is GatherAs over one registry.
func gatherAs(reg *integration.Registry, st *store.Store, root string,
	self integration.BinaryIdentity,
) (*Status, error) {
	// The shared reader gives the facts that orient also shows. A
	// recorded fingerprint decides the freshness; without one it stays
	// unknown, because a non-git workspace has no change signal.
	// The fingerprint leaves out the files of the database that st reads,
	// as the index run did when it stored its fingerprint.
	health, err := indexhealth.Read(st, root, func(root string) string { return index.WorkspaceState(root, st.Path()) },
		index.OtherVersion)
	if err != nil {
		return nil, err
	}

	s := &Status{
		IndexedAt:   health.IndexedAt,
		Freshness:   health.Freshness,
		StaleReason: health.StaleReason,
		Coverage:    health.Coverage,
		EdgeOrigins: health.EdgeOrigins,
	}

	for _, m := range []struct {
		key string
		fn  func(v string)
	}{
		{store.MetaSchemaVersion, func(v string) { s.SchemaVersion = v }},
		{store.MetaReviewsMinedAt, func(v string) { s.ReviewsMinedAt = indexhealth.ParseUnix(v) }},
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

	// Inspect never fails: an unreadable directory is recorded on its
	// client record, and status describes it. The skills list and the
	// per-client view come from one registry inspection, so the two
	// cannot disagree; the approvals list keeps its legacy shape.
	s.Skills = reg.InspectSkills(root)
	s.Approvals = approve.Inspect(root)
	s.Clients = reg.Inspect(root)

	// The legacy fields repeat the Claude Code inspection, so one read of
	// the document serves both views and they cannot disagree. The error
	// field keeps the reason of a file that cannot be read, sanitized by
	// the inspection.
	if i := slices.IndexFunc(s.Clients, func(insp integration.Inspection) bool { return insp.ClientID == integration.ClaudeID }); i >= 0 {
		s.GateHookMode = s.Clients[i].ManagedGateMode
		s.GateHookError = s.Clients[i].HookDocumentError
	}

	// The lesson-check report joins the same inspection, so its hook
	// state agrees with the per-client view.
	gatherLessonChecks(s, root, self)

	return s, nil
}

// NoteOwnership records the facts of the database that the report reads.
// The surface sets them, because only the surface knows the workspace
// that the user named. An index that records another root gets the root
// line, and a state that refuses a build in the workspace replaces the
// index run of the stale line with its route. Both read the state as a
// CLI command does (ownership.State.Command).
func (s *Status) NoteOwnership(o ownership.State) {
	// The own database reads no disk here: a status runs often.
	if o.Recorded == "" || o.Root == ownership.RootSame {
		return
	}

	if notice := RootNotice(o); notice != "" {
		s.RecordedRoot, s.rootNotice = render.Sanitize(o.Recorded), notice
	}

	s.staleRoute = o.Command().RefusedBuildRoute()
}

// NoteWorkspaceMatch records that the index matches the workspace of the
// surface, also when the root that the report describes changed. The MCP
// server knows this when its refresh skips under the index of another
// root, and its note says so. The workspace line then reads current,
// because a run in that root would make the index differ from the code
// that the server serves. An index of another version stays stale,
// because only a rebuild adds the facts that it lacks.
func (s *Status) NoteWorkspaceMatch() {
	if s.Freshness == FreshStale && s.StaleReason == StaleWorkspaceChanged {
		s.Freshness, s.StaleReason = FreshCurrent, ""
	}
}

// RootNotice says that the index records another root than the
// workspace, so a report describes that root. It returns "" when the two
// are one directory. The CLI status and the MCP status resource print it
// as the root line, so both say the same for one pair. The words and the
// subject follow the refusal of a pin write.
//
// The notice names only a route that makes the index record this
// workspace. A route into the recorded root would print the same report
// again, so the notice then has no route.
func RootNotice(o ownership.State) string {
	// A directory inside a recorded root outside git counts as that root,
	// because a command there runs in that repository.
	s := o.Command()
	if s.Recorded == "" || s.Root == ownership.RootNone || s.Root == ownership.RootSame {
		return ""
	}

	subject := "this database"
	if s.File == ownership.FileDefault {
		subject = "the index of this checkout"
	}

	root := render.Sanitize(s.Recorded)
	notice := fmt.Sprintf("%s %s, so this report describes %s, not this workspace", subject, RecordedClause(s), root)

	switch d := ownership.Decide(s.Facts); d.Route {
	case ownership.RouteIndexHere, ownership.RouteIndexHereDB, ownership.RouteMoveHere:
		notice += "; " + s.Route(d.Route, ownership.SurfaceCommand)
	}

	return notice
}

// RecordedClause names the root that the database records, as "records
// another repository (R)". A root that is gone gets its own words. Every
// note on another root uses it, so the notes name the root in one way.
// The root comes from the database, so the clause sanitizes it.
func RecordedClause(o ownership.State) string {
	what := "records another repository"
	if o.Root == ownership.RootGone {
		what = "records a directory that no longer exists"
	}

	return what + " (" + render.Sanitize(o.Recorded) + ")"
}

// Print renders the report as aligned text, the RFC's status layout.
// The root line comes first, because it says which repository every
// later line describes.
func Print(w io.Writer, s *Status) {
	// One clock read per report, so every age on it uses the same now.
	now := time.Now()

	if s.rootNotice != "" {
		fmt.Fprintf(w, "root           %s\n", s.rootNotice)
	}

	fresh := s.Freshness
	switch s.Freshness {
	case FreshStale:
		route := "run `seamark index`"
		if s.staleRoute != "" {
			route = s.staleRoute
		}

		// The index of another version can match the workspace, so the
		// line names the reason: a reader would otherwise find nothing
		// changed.
		fresh = "STALE — " + route
		if s.StaleReason == StaleOtherVersion {
			fresh = "STALE — this index was built by another seamark version; " + route
		}
	case FreshUnknown:
		fresh = "freshness unknown (no git fingerprint) — reindex when in doubt"
	}

	fmt.Fprintf(w, "workspace      %s (schema v%s)\n", fresh, s.SchemaVersion)

	switch {
	case !s.Coverage.Known:
		fmt.Fprintf(w, "parsed         unknown — reindex with this seamark to record coverage\n")
	case s.Coverage.ParseErrors > 0:
		fmt.Fprintf(w, "parsed         %d of %d seen files (%d skipped by config, %d with no language extractor) — %d PARSE ERRORS:\n"+
			"               those files are invisible to every answer\n",
			s.Coverage.FilesParsed, s.Coverage.FilesSeen, s.Coverage.FilesSkipped, s.Coverage.FilesUnsupported,
			s.Coverage.ParseErrors)
	default:
		fmt.Fprintf(w, "parsed         %d of %d seen files (%d skipped by config, %d with no language extractor)\n",
			s.Coverage.FilesParsed, s.Coverage.FilesSeen, s.Coverage.FilesSkipped, s.Coverage.FilesUnsupported)
	}

	// A deleted path is a state of the working tree. The index misses
	// no file, so the line is a plain note, not an error. It has its own
	// label, so it does not read as part of the parse-error lines.
	switch n := s.Coverage.FilesDeleted; {
	case n == 1:
		fmt.Fprintf(w, "deleted        1 tracked file in the working tree (deletion not staged)\n")
	case n > 1:
		fmt.Fprintf(w, "deleted        %d tracked files in the working tree (deletions not staged)\n", n)
	}

	fmt.Fprintf(w, "symbols        %d, %d edges — call resolution %s\n",
		s.Symbols, s.Edges, indexhealth.OriginSummary(s.EdgeOrigins, 0))
	fmt.Fprintf(w, "effects        %d direct-sink symbols, %d by propagation\n",
		s.EffectsDirect, s.EffectsPropagated)

	if s.History.Decisions > 0 {
		fmt.Fprintf(w, "history        %d decisions; evidence median age %s (oldest %s)\n",
			s.History.Decisions, indexhealth.Age(s.History.MedianTS, now), indexhealth.Age(s.History.OldestTS, now))
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
			s.Lessons, reviewFindings, indexhealth.Age(s.ReviewsMinedAt, now))
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
	printLessonChecks(w, s)
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

// summarizeGateHooks groups every client's gate hooks by what they do
// with a blocking verdict, by the rule init uses. A status built
// without the per-client view (an older JSON document) still has the
// legacy Claude Code fields, so the summary reads those then.
func summarizeGateHooks(s *Status) integration.GateSummary {
	g := integration.SummarizeInspections(s.Clients)

	if len(s.Clients) == 0 {
		if s.GateHookError != "" {
			g.AddUnreadable("claude: " + s.GateHookError)
		} else {
			g.Add("claude", s.GateHookMode)
		}
	}

	return g
}

// printGate renders the effective gate behaviour. A broken policy means
// different things under different hooks — an enforce hook fails closed,
// a warn hook fails open — and the difference is the whole point of
// reporting it. The line covers every client with a gate hook; the
// installed hook mode and the policy mode are named apart, because a
// warn hook still follows an enforcing policy file. The legacy Claude
// Code fields stay in the JSON. The text reads the broader view, because
// a Codex gate hook blocks a Codex command whatever .claude/settings.json
// says. A report-only line names the cause that the inspection read: a
// discarded exit status, or a hook that the client runs in the background.
func printGate(w io.Writer, s *Status) {
	g := summarizeGateHooks(s)
	names := func(ids []string) string { return strings.Join(ids, ", ") }

	if s.GatePolicyError != "" {
		switch g.Effect() {
		case integration.GateEffectBlocks:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the enforce hook FAILS CLOSED (%s): every hooked command blocks until the policy is fixed\n",
				s.GatePolicyError, names(g.Enforce))
		case integration.GateEffectFollowsPolicy:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the warn hook fails open (%s): nothing blocks, and nothing is being checked\n",
				s.GatePolicyError, names(g.Warn))
		case integration.GateEffectReportOnly:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               the report-only hook %s (%s): nothing blocks, and nothing is being checked\n",
				s.GatePolicyError, g.ReportOnlyCause(g.ReportOnly), names(g.ReportOnly))
		case integration.GateEffectUnknown:
			// Broken policy AND an unknown hook: the effective behaviour is
			// unknown, not "no hook".
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s)\n"+
				"               %s — effective behaviour unknown\n",
				s.GatePolicyError, unknownGate(g))
		default:
			fmt.Fprintf(w, "gate           POLICY BROKEN (%s); no operational gate hook\n", s.GatePolicyError)
		}

		return
	}

	switch g.Effect() {
	case integration.GateEffectUnknown:
		fmt.Fprintf(w, "gate           policy mode %s; %s\n", s.GatePolicyMode, unknownGate(g))
	case integration.GateEffectNone:
		// A setup run does not repair a matcher that never fires, so
		// this line offers no setup run. The clients line and the warning
		// of each inert client already name the cause and the repair, so
		// this line does not repeat them.
		if len(g.Inert) > 0 {
			fmt.Fprintf(w, "gate           policy mode %s; no gate hook runs for shell commands\n", s.GatePolicyMode)
		} else {
			fmt.Fprintf(w, "gate           policy mode %s; no gate hook installed (`seamark init`, or `seamark init --client <name>`)\n",
				s.GatePolicyMode)
		}
	case integration.GateEffectReportOnly:
		// A verdict blocks only by exit status 2, so the policy mode cannot
		// make such a hook block.
		fmt.Fprintf(w, "gate           report-only (%s hook %s); nothing blocks, whatever policy mode %s says\n",
			names(g.ReportOnly), g.ReportOnlyCause(g.ReportOnly), s.GatePolicyMode)
	case integration.GateEffectBlocks:
		if len(g.Warn) > 0 {
			fmt.Fprintf(w, "gate           enforce for %s (hook carries --enforce; blocking verdicts exit 2); "+
				"the %s hook follows policy mode %s\n", names(g.Enforce), names(g.Warn), s.GatePolicyMode)
		} else {
			fmt.Fprintf(w, "gate           enforce (%s hook carries --enforce; blocking verdicts exit 2)\n", names(g.Enforce))
		}
	case integration.GateEffectFollowsPolicy:
		fmt.Fprintf(w, "gate           hook installed (%s); policy mode %s governs\n", names(g.Warn), s.GatePolicyMode)
	}

	if len(g.ReportOnly) > 0 && g.Effect() > integration.GateEffectReportOnly {
		fmt.Fprintf(w, "               the %s hook %s, so it never blocks\n", names(g.ReportOnly), g.ReportOnlyCause(g.ReportOnly))
	}

	// The unknown line above already names an unreadable document and a
	// definition that may run a gate.
	if g.Effect() > integration.GateEffectUnknown {
		if len(g.Unreadable) > 0 {
			fmt.Fprintf(w, "               hook configuration UNREADABLE for %s\n", render.Sanitize(names(g.Unreadable)))
		}

		if len(g.Possible) > 0 {
			fmt.Fprintf(w, "               a definition of %s may also run a gate hook; seamark cannot tell what it does\n",
				names(g.Possible))
		}
	}
}

// unknownGate names why the effect of the gate hooks is unknown. The
// reason is a hook document that cannot be read, a definition that may
// run a gate, or both. The words stay apart, because the fixes differ.
// One needs a readable file, the other a definition that runs the gate
// directly.
func unknownGate(g integration.GateSummary) string {
	var parts []string

	if len(g.Unreadable) > 0 {
		parts = append(parts, "hook configuration UNREADABLE ("+render.Sanitize(strings.Join(g.Unreadable, ", "))+")")
	}

	if len(g.Possible) > 0 {
		parts = append(parts, "a definition of "+strings.Join(g.Possible, ", ")+
			" may run a gate hook; seamark cannot tell what it does, so nothing is known to block")
	}

	return strings.Join(parts, "; ")
}
