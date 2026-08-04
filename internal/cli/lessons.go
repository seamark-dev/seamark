package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

func newLessonsCmd(opts *options) *cobra.Command {
	var (
		file         string
		region       string
		hookMode     bool
		list         bool
		stats        bool
		distillRun   bool
		dryRun       bool
		proposalList bool
		limit        int
		applyIDs     string
		dismissIDs   string
		pruneIDs     string
	)

	cmd := &cobra.Command{
		Use:   "lessons [--file <path> | --list [--region <prefix>] | --stats | --distill | --proposals]",
		Short: "Show the recurring review feedback mined from pull requests",
		Long: `Prints the review lessons (mined by "index --reviews", tuned by
.seamark/lessons.yaml) — the mistakes reviewers keep flagging.

  --file <path>    the lessons that apply to one file's area
  --list           every mined lesson repo-wide, with config syntax to
                   mute the noise or pin what must never be ignored
  --region <path>  narrow --list to one directory's area: its lessons
                   and its ancestors' — the raw material (including
                   below-threshold one-offs) for spotting a pattern the
                   per-file counters cannot see. Implies --list.
  --stats          which lessons the edit hook actually surfaced to agents,
                   and which would surface but never have (decay candidates)
  --distill        send NEW groups of raw findings to your agent CLI and
                   turn recurring patterns into proposed pins — the plan
                   half of plan/apply; nothing touches lessons.yaml here.
                   Composes with --region and --limit (token budget);
                   already-read groups are never paid for twice. Fully
                   optional: pins are plain YAML you can write by hand —
                   distill only drafts them.
  --proposals      the decision ledger — what is pending, applied, and
                   dismissed. Read-only: never spends an agent call.
  --apply p3,p7    turn chosen proposals into pins; ranges work too
                   (p1..p9 applies whatever inside is still pending).
                   Writes lessons.yaml only when config.yaml sets
                   distill.write; otherwise prints the block to paste.
                   Never automatic.
  --dismiss p2     record a no — the same evidence is never re-proposed.
                   Takes lists and ranges like --apply
  --prune p16,p45  drop applied pins that restate another (the ledger
                   names the clusters and hands you the command). Not a
                   dismissal: the theme stays pinned by its survivor, so
                   the distiller still counts it as known. Edits
                   lessons.yaml only with distill.write, like --apply.
  --hook           read a Claude Code PreToolUse payload from stdin and emit
                   the edited file's lessons as additionalContext

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated BEFORE any dispatch: `--apply p1 --dry-run` must
			// be a usage error, never an apply that ignored the flag —
			// a command carrying --dry-run must not mutate anything.
			if dryRun && !distillRun {
				return fmt.Errorf("--dry-run only applies to --distill")
			}

			// Apply, dismiss and prune are three different decisions
			// about the same ids; two at once would silently route
			// everything (positional ids included) to whichever the
			// switch reaches first.
			decisions := 0
			for _, ids := range []string{applyIDs, dismissIDs, pruneIDs} {
				if strings.TrimSpace(ids) != "" {
					decisions++
				}
			}

			if decisions > 1 {
				return fmt.Errorf("--apply, --dismiss and --prune are different decisions — run them one at a time")
			}

			// `--apply p1, p2` is natural typing; the shell splits the
			// spaced list into positional args, so fold them back in.
			if len(args) > 0 && decisions == 0 {
				return fmt.Errorf("unexpected argument %q — provide --file, --list, --distill, --apply, --dismiss, or --prune", args[0])
			}

			extra := strings.Join(args, ",")

			switch {
			case hookMode:
				return runLessonsHook(cmd, opts)
			case strings.TrimSpace(applyIDs) != "":
				return runLessonsApply(cmd, opts, applyIDs+","+extra)
			case strings.TrimSpace(dismissIDs) != "":
				return runLessonsDismiss(cmd, opts, dismissIDs+","+extra)
			case strings.TrimSpace(pruneIDs) != "":
				return runLessonsPrune(cmd, opts, pruneIDs+","+extra)
			case proposalList:
				return runLessonsProposals(cmd, opts)
			case distillRun:
				return runLessonsDistill(cmd, opts, strings.TrimSpace(region), limit, dryRun)
			case list || strings.TrimSpace(region) != "":
				return runLessonsList(cmd, opts, strings.TrimSpace(region))
			case stats:
				return runLessonsStats(cmd, opts)
			case strings.TrimSpace(file) != "":
				st, root, err := openIndex(opts)
				if err != nil {
					return err
				}
				defer func() { _ = st.Close() }()

				lessons, _, err := lessonsForFile(st, root, file, false)
				if err != nil {
					return err
				}

				return report.PrintLessonReminder(cmd.OutOrStdout(), toRepoRel(root, file), lessons, 0)
			default:
				return fmt.Errorf("provide --file <path>, --list, --stats, --distill, --proposals, --apply, --dismiss, or --hook")
			}
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "repo-relative (or absolute) path to look up")
	cmd.Flags().BoolVar(&list, "list", false, "list every mined lesson with config-tuning syntax")
	cmd.Flags().StringVar(&region, "region", "", "narrow --list or --distill to a directory's area")
	cmd.Flags().BoolVar(&stats, "stats", false, "summarize the firing log: what surfaced, what never fires")
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"read a PreToolUse JSON payload from stdin and emit lessons as additionalContext")
	cmd.Flags().BoolVar(&distillRun, "distill", false,
		"distill recurring patterns from raw findings into proposed pins (needs the agent CLI)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"with --distill: show what would be sent to the agent (groups, sizes, cost) without sending anything")
	cmd.Flags().BoolVar(&proposalList, "proposals", false,
		"show the distillation ledger: pending, applied, and dismissed proposals (no agent calls)")
	cmd.Flags().IntVar(&limit, "limit", 10,
		"max new groups one --distill run sends to the agent (0 = all)")
	cmd.Flags().StringVar(&applyIDs, "apply", "",
		"apply proposals as pins by id or range (p3,p7 or p1..p9); writes lessons.yaml only with distill.write in config")
	cmd.Flags().StringVar(&dismissIDs, "dismiss", "",
		"dismiss proposals by id or range (p2 or p1..p9) — remembered, never re-proposed for the same evidence")
	cmd.Flags().StringVar(&pruneIDs, "prune", "",
		"remove applied pins that restate another (p16,p45) — the theme stays pinned by its survivor")

	return cmd
}

// proposalSelection is a parsed --apply/--dismiss argument. The two
// forms carry different intent: a bare id (p3) is a precise pointer and
// must name a pending proposal; a range (p1..p9 or p1-p9) means "these,
// whichever still need a decision", so the holes left by earlier
// applies and dismissals are skipped, not errors.
type proposalSelection struct {
	exact  []int64
	ranges [][2]int64
}

// maxRangeWidth guards against a typo'd range selecting the universe.
const maxRangeWidth = 1000

// parseSelection reads "p3,p7,p10..p14" (spaces and bare numbers fine).
func parseSelection(s string) (*proposalSelection, error) {
	sel := &proposalSelection{}

	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue // spaced lists arrive with empty segments; harmless
		}

		lo, hi, isRange := cutRange(part)
		if !isRange {
			id, err := parsePID(part)
			if err != nil {
				return nil, err
			}

			sel.exact = append(sel.exact, id)

			continue
		}

		a, err := parsePID(lo)
		if err != nil {
			return nil, err
		}

		b, err := parsePID(hi)
		if err != nil {
			return nil, err
		}

		if b < a {
			return nil, fmt.Errorf("range p%d..p%d is reversed", a, b)
		}

		if b-a >= maxRangeWidth {
			return nil, fmt.Errorf("range p%d..p%d is implausibly wide", a, b)
		}

		sel.ranges = append(sel.ranges, [2]int64{a, b})
	}

	if len(sel.exact) == 0 && len(sel.ranges) == 0 {
		return nil, fmt.Errorf("no proposal ids given (want p<N> from the distill plan)")
	}

	return sel, nil
}

// cutRange splits a range on ".." or "-" (the same dash the expand span
// syntax uses); anything else is a single id.
func cutRange(part string) (lo, hi string, ok bool) {
	if l, h, found := strings.Cut(part, ".."); found {
		return l, h, true
	}

	if l, h, found := strings.Cut(part, "-"); found {
		return l, h, true
	}

	return "", "", false
}

func parsePID(s string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "p")

	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%q is not a proposal id (want p<N> from the distill plan)", s)
	}

	return id, nil
}

// resolveSelection turns a selection into concrete proposals from the
// candidate set, id order, deduplicated. Exact ids must be in that set;
// ranges take what they find. state names what the candidates are
// ("pending", "applied") so an error says which ledger it looked in —
// pruning searches applied proposals, not pending ones.
func resolveSelection(candidates []model.Proposal, raw, state string) ([]model.Proposal, error) {
	sel, err := parseSelection(raw)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]model.Proposal, len(candidates))
	for _, p := range candidates {
		byID[p.ID] = p
	}

	added := map[int64]bool{}

	var out []model.Proposal

	for _, id := range sel.exact {
		p, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("p%d is not %s (see `seamark lessons --proposals`)", id, state)
		}

		if !added[id] {
			added[id] = true
			out = append(out, p)
		}
	}

	for _, r := range sel.ranges {
		for _, p := range candidates {
			if p.ID >= r[0] && p.ID <= r[1] && !added[p.ID] {
				added[p.ID] = true
				out = append(out, p)
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("nothing in %q is %s (see `seamark lessons --proposals`)", raw, state)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out, nil
}

// runLessonsApply turns chosen proposals into pins. The pins land in
// .seamark/lessons.yaml only when the workspace opted in via
// distill.write; otherwise the rendered block is printed for the human
// to paste — either way, nothing was ever applied without an explicit
// human command naming explicit proposals.
func runLessonsApply(cmd *cobra.Command, opts *options, raw string) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return err
	}

	ps, err := resolveSelection(pending, raw, "pending")
	if err != nil {
		return err
	}

	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}

	out := cmd.OutOrStdout()

	cfg, err := distill.LoadConfig(root)
	if err != nil {
		return err
	}

	if !cfg.Distill.Write {
		block, err := distill.RenderPins(ps)
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "distill.write is off in .seamark/config.yaml — paste under `pin:` in .seamark/lessons.yaml:\n\n%s\n", block)
		fmt.Fprintln(out, "(proposals stay pending; enable distill.write to let apply edit the file)")

		return nil
	}

	// File first, status second: if the status write fails, the worst
	// case is a duplicate pin entry on a retried apply — visible in the
	// yaml diff. The other order could mark a pin applied that never
	// reached the file, which nothing would ever surface.
	if err := distill.ApplyPins(root, ps); err != nil {
		return err
	}

	applied, err := st.SetProposalStatus(ids, model.ProposalApplied)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "applied %d pin(s) to .seamark/lessons.yaml — review the diff and commit it\n", applied)

	for _, p := range ps {
		fmt.Fprintf(out, "  p%-4d %s\n", p.ID, p.Rule)
	}

	return nil
}

// runLessonsPrune retires applied pins that restate another. It is not
// a dismissal: the guidance stays pinned by whichever entry survives,
// so the proposal becomes "superseded" and the theme is still known to
// the distiller. Editing lessons.yaml obeys the same gate as --apply.
func runLessonsPrune(cmd *cobra.Command, opts *options, raw string) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return err
	}

	ps, err := resolveSelection(applied, raw, "applied")
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// Rule plus region: the same rule is legitimately pinned in several
	// areas, and pruning one must not take its namesakes with it.
	keys := make([]distill.PinKey, len(ps))
	ids := make([]int64, len(ps))

	for i, p := range ps {
		keys[i] = distill.PinKey{Rule: p.Rule, Region: p.Region}
		ids[i] = p.ID
	}

	cfg, err := distill.LoadConfig(root)
	if err != nil {
		return err
	}

	if !cfg.Distill.Write {
		fmt.Fprintf(out, "distill.write is off in .seamark/config.yaml — remove these pins from "+
			".seamark/lessons.yaml by hand:\n\n")

		for _, p := range ps {
			fmt.Fprintf(out, "  p%-4d %s\n", p.ID, p.Rule)
		}

		fmt.Fprintln(out, "\n(the proposals stay applied; enable distill.write to let prune edit the file)")

		return nil
	}

	removed, err := distill.RemovePins(root, keys)
	if err != nil {
		return err
	}

	n, err := st.SupersedeProposals(ids)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "pruned %d pin(s) from .seamark/lessons.yaml, %d proposal(s) marked superseded — "+
		"review the diff and commit it\n", len(removed), n)

	for _, p := range ps {
		fmt.Fprintf(out, "  p%-4d %s\n", p.ID, p.Rule)
	}

	if len(removed) < len(ps) {
		fmt.Fprintf(out, "(%d were already absent from the file)\n", len(ps)-len(removed))
	}

	return nil
}

// runLessonsDismiss records a no on chosen proposals. The signature
// memory does the rest: the same evidence set is never re-proposed.
func runLessonsDismiss(cmd *cobra.Command, opts *options, raw string) error {
	st, _, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return err
	}

	ps, err := resolveSelection(pending, raw, "pending")
	if err != nil {
		return err
	}

	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}

	n, err := st.SetProposalStatus(ids, model.ProposalDismissed)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "dismissed %d proposal(s) — remembered; the same evidence will not return\n", n)

	return nil
}

// runLessonsProposals prints the distillation ledger. Read-only and
// offline: the plan view costs an agent call when new groups exist, so
// "what is waiting for me?" needs a way to ask for free.
func runLessonsProposals(cmd *cobra.Command, opts *options) error {
	st, _, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	states := make([][]model.Proposal, 3)

	for i, status := range []string{
		model.ProposalProposed, model.ProposalApplied, model.ProposalDismissed,
	} {
		if states[i], err = st.Proposals(status); err != nil {
			return err
		}
	}

	// Applied pins are the ones that cost context on every edit, so the
	// duplicate audit runs over them.
	report.PrintProposalLedger(cmd.OutOrStdout(), states[0], states[1], states[2],
		distill.Clusters(states[1]))

	return nil
}

// runLessonsDistill executes the plan half of Tier 2: read new evidence
// groups through the configured agent, persist what survives validation
// as proposals, and print the full pending plan. Applying is a separate
// explicit step — this command never edits .seamark/lessons.yaml.
// A dry run stops after the preflight disclosure: nothing is sent.
func runLessonsDistill(cmd *cobra.Command, opts *options, region string, limit int, dryRun bool) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	acfg, err := agent.LoadConfig(root)
	if err != nil {
		return err
	}

	// The config must resolve for every run — an unknown preset is a
	// broken configuration, not something a dry run may paper over. But
	// a dry run needs no invoker: disclosure works with the binary
	// missing from PATH.
	_, agentArgv, err := agent.Resolve(acfg)
	if err != nil {
		return fmt.Errorf("distill unavailable: %w", err)
	}

	var inv agent.Invoker

	if !dryRun {
		if inv, err = agent.New(acfg); err != nil {
			return fmt.Errorf("distill unavailable: %w", err)
		}
	}

	if region != "" {
		region = toRepoRel(root, region)
	}

	// The workspace's pins — hand-written and applied alike — are
	// patterns the distiller must not re-derive under a new name. A
	// malformed config must not abort a run that is about to spend real
	// tokens, but it must not pass unnoticed either: without the pins
	// every already-captured pattern looks new.
	lcfg, err := reviews.LoadConfig(root)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warn: .seamark/lessons.yaml ignored (%s) — distilling without your pins, "+
				"so patterns you already pinned may be proposed again\n", err)

		lcfg = reviews.DefaultConfig()
	}

	pins := make([]model.Proposal, 0, len(lcfg.Pin))
	for _, p := range lcfg.Pin {
		pins = append(pins, model.Proposal{Rule: p.Rule, Note: p.Note, Region: p.Region})
	}

	dopts := distill.Options{
		Region: region,
		Limit:  limit,
		Pins:   pins,
		Agent:  agentArgv,
		DryRun: dryRun,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		},
	}

	// The preflight discloses what is about to leave the machine —
	// always, not only on dry runs: sending findings to a model-backed
	// CLI must never be a surprise.
	dopts.OnPreflight = func(pf distill.Preflight) {
		printPreflight(cmd.OutOrStdout(), pf, dryRun)
	}

	// On a terminal, each agent call gets a live spinner with elapsed
	// time instead of dead air; piped output keeps the plain log lines.
	u := newUI(cmd.ErrOrStderr())
	defer u.finish("")

	if u.tty {
		dopts.OnGroupStart = func(desc string) { u.phase("distill", desc) }
		dopts.OnGroupDone = func(outcome string) { u.finish(outcome) }
	}

	res, err := distill.Run(cmd.Context(), st, distill.NewLexicalGrouper(), inv, dopts)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(),
			"\n(dry run — nothing was sent; %d group(s) remain unread; drop --dry-run to distill)\n",
			res.GroupsPending)

		return nil
	}

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return err
	}

	report.PrintDistillPlan(cmd.OutOrStdout(), report.DistillSummary{
		GroupsTotal:   res.GroupsTotal,
		GroupsRead:    res.GroupsRead,
		GroupsSkipped: res.GroupsSkipped,
		GroupsFailed:  res.GroupsFailed,
		GroupsPending: res.GroupsPending,
		PrunedStale:   res.PrunedStale,
		Duplicates:    res.Duplicates,
		TokensNote:    res.CostNote(),
	}, pending)

	return nil
}

// printPreflight renders the pre-invocation disclosure: what would be
// sent to the agent, where, and roughly what it costs. Finding bodies
// never appear here. The agent argv and region names come from
// repository-controlled files (config.yaml, file paths), so they are
// sanitized before printing and the argv is additionally scrubbed of
// secret-shaped values — the disclosure must not be forgeable by the
// text it discloses, nor leak a credential embedded in the config.
func printPreflight(w io.Writer, pf distill.Preflight, dryRun bool) {
	fmt.Fprintf(w, "distill preflight — what leaves this machine\n")
	fmt.Fprintf(w, "  agent     %s  (your CLI, chosen by .seamark/config.yaml; assumed to reach\n"+
		"            a remote model service)\n",
		render.Sanitize(redact.Secrets(strings.Join(pf.Agent, " "))))

	if len(pf.Groups) == 0 {
		fmt.Fprintf(w, "  payload   nothing — every evidence group has already been distilled\n")
		return
	}

	fmt.Fprintf(w, "  payload   %d group(s), %d finding(s), ~%s tokens\n",
		len(pf.Groups), pf.Findings, pf.Tokens())
	fmt.Fprintf(w, "  contents  per finding: reviewer comment or fix-commit subject verbatim (it\n"+
		"            may quote your code), repo-relative path, finding id, source, PR\n"+
		"            number and reviewer name; plus rule labels of patterns already\n"+
		"            pinned or previously proposed — no source files, no diffs\n")
	fmt.Fprintf(w, "  redaction none — bodies are sent as written, capped at %d chars each\n", pf.BodyCap)

	for _, g := range pf.Groups {
		region := g.Region
		if region == "" {
			region = "repo-wide"
		}

		fmt.Fprintf(w, "    %-28s %3d finding(s)  ~%s tokens\n",
			render.Sanitize(region), g.Findings, distill.Preflight{PromptChars: g.PromptChars}.Tokens())
	}

	if !dryRun {
		fmt.Fprintln(w)
	}
}

// runLessonsList prints the ledger of mined lessons — every one, or, with
// a region, those in that directory's area. A region-scoped ledger is
// the pattern-hunting view: per-file clustering keeps semantically
// related one-offs apart, and reading an area's raw findings side by
// side is how a human (or an agent) spots what the counters cannot.
func runLessonsList(cmd *cobra.Command, opts *options, region string) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if region != "" {
		region = toRepoRel(root, region)
	}

	lessons, err := report.LedgerForRegion(st, region)
	if err != nil {
		return err
	}

	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	report.PrintLessonLedger(cmd.OutOrStdout(), lessons, cfg, region)

	return nil
}

// runLessonsHook implements the PreToolUse path. It must never fail the
// tool it guards: any error (no index, unreadable payload) yields empty
// output and exit 0, so a missing seamark index can't block edits.
func runLessonsHook(cmd *cobra.Command, opts *options) error {
	path, tool, err := readHookInput(cmd.InOrStdin())
	if err != nil || path == "" {
		return nil // nothing to say; never block the edit
	}

	st, root, err := openIndexQuiet(opts)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()

	lessons, morePins, err := lessonsForFile(st, root, path, true)
	if err != nil || len(lessons) == 0 {
		return nil
	}

	var b strings.Builder
	_ = report.PrintLessonReminder(&b, toRepoRel(root, path), lessons, morePins)

	out := hookOutput{}
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.AdditionalContext = b.String()

	// Emit the verdict FIRST so the edit's go-ahead never waits on the
	// audit write; then record best-effort — a slow or failed append must
	// neither delay nor block the edit.
	err = json.NewEncoder(cmd.OutOrStdout()).Encode(out)

	_ = reviews.RecordFiring(root, toRepoRel(root, path), tool, lessons)

	return err
}

// runLessonsStats prints the firing-log summary: which lessons actually
// reach agents, and which would surface but never have (decay signal).
func runLessonsStats(cmd *cobra.Command, opts *options) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	firings, err := reviews.ReadFirings(root)
	if err != nil {
		return err
	}

	mined, err := st.AllLessons(0)
	if err != nil {
		return err
	}

	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	// The set that COULD fire: what the config surfaces repo-wide.
	surfaced := cfg.Surface(mined, "")

	report.PrintFiringSummary(cmd.OutOrStdout(), reviews.Summarize(firings, surfaced))

	return nil
}

// hookOutput is the PreToolUse response shape: additionalContext is
// injected into the agent's context without blocking the tool.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// readHookInput extracts the edited file path and tool name from a
// PreToolUse payload (Edit, Write, and MultiEdit all carry file_path).
func readHookInput(r io.Reader) (file, tool string, err error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", "", err
	}

	var payload struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", err
	}

	return payload.ToolInput.FilePath, payload.ToolName, nil
}

// lessonsForFile normalizes path to repo-relative and returns the
// config-filtered lessons for its area. ambient applies the pin budget:
// the hook is an injection the agent never asked for, so it is capped;
// the --file view is a deliberate question and gets everything.
func lessonsForFile(st *store.Store, root, path string, ambient bool) ([]model.Lesson, int, error) {
	rel := toRepoRel(root, path)

	// A malformed config must not silence the hook or the --file view;
	// fall back to defaults (mirrors why/orient's degrade-not-fail).
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	budget := 0
	if ambient {
		budget = cfg.HookPinBudget()
	}

	return report.LessonsForScopeBudget(st, cfg, rel, 8, budget)
}

// toRepoRel converts an absolute or ./-prefixed path to the repo-relative
// slash form the index stores; a path already relative is returned as-is.
func toRepoRel(root, path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}

	return strings.TrimPrefix(filepath.ToSlash(path), "./")
}

// openIndexQuiet opens the index without the staleness warning openIndex
// would print — the hook must emit only its JSON on stdout.
func openIndexQuiet(opts *options) (*store.Store, string, error) {
	root, err := index.ResolveRoot(opts.workspace)
	if err != nil {
		return nil, "", err
	}

	dbPath := opts.dbPath
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return nil, "", err
	}

	if r, err := st.GetMeta("repo_root"); err == nil && r != "" {
		root = r
	}

	return st, root, nil
}
