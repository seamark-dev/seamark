package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

func newLessonsCmd(opts *options) *cobra.Command {
	var (
		file            string
		region          string
		hookMode        bool
		hookReset       bool
		hookClient      string
		list            bool
		stats           bool
		distillRun      bool
		dryRun          bool
		proposalList    bool
		limit           int
		applyIDs        string
		dismissIDs      string
		pruneIDs        string
		retargetIDs     string
		extractTriggers bool
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
                   the distiller still counts it as known. Removes only
                   the selected entry and skips a duplicate pin. Edits
                   lessons.yaml only with distill.write, like --apply.
  --retarget p3    update an applied pin's regions to what today's
                   inference computes from its living evidence (the
                   --proposals ledger names the candidates). Changes
                   only the region keys of the entry, in place, and the
                   ledger with them; write-gated like --apply, else
                   prints the change to make by hand.
  --extract-triggers
                   backfill trigger paths for proposals distilled
                   before extraction existed: small agent calls name
                   where each mistake is MADE, the harness validates,
                   pending rows retarget in place, applied pins show
                   the drift in --proposals for --retarget. Composes
                   with --dry-run. Rows keep their answer — with
                   paths or without — so re-running is free. A row
                   that changes during the run is left unchanged, and
                   the run stops.
  --hook           read an agent's edit-hook payload from stdin and emit the
                   lessons of the edited files as additionalContext. The
                   payload is a Claude Code PreToolUse event; with
                   --client codex it is a Codex apply_patch event.

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated BEFORE any dispatch: `--apply p1 --dry-run` must
			// be a usage error, never an apply that ignored the flag —
			// a command carrying --dry-run must not mutate anything.
			if dryRun && !distillRun && !extractTriggers {
				return fmt.Errorf("--dry-run only applies to --distill and --extract-triggers")
			}

			// Apply, dismiss and prune are three different decisions
			// about the same ids; two at once would silently route
			// everything (positional ids included) to whichever the
			// switch reaches first.
			decisions := 0
			for _, ids := range []string{applyIDs, dismissIDs, pruneIDs, retargetIDs} {
				if strings.TrimSpace(ids) != "" {
					decisions++
				}
			}

			if decisions > 1 {
				return fmt.Errorf("--apply, --dismiss, --prune and --retarget are different decisions — run them one at a time")
			}

			// Agent-spending modes never combine with decision flags:
			// dispatch order would silently run the decision and ignore
			// --dry-run — the exact mutation the guard above forbids.
			if (distillRun || extractTriggers) && decisions > 0 {
				return fmt.Errorf("--distill and --extract-triggers do not combine with --apply, --dismiss, --prune, or --retarget")
			}

			if distillRun && extractTriggers {
				return fmt.Errorf("--distill and --extract-triggers are separate runs — one at a time")
			}

			// The read-only view would be silently swallowed: dispatch
			// reaches the decision first and never prints the ledger —
			// or reaches the ledger first and never spends the run the
			// user asked for.
			if proposalList && (decisions > 0 || distillRun || extractTriggers) {
				return fmt.Errorf("--proposals does not combine with --apply, --dismiss, --prune, " +
					"--retarget, --distill, or --extract-triggers — run it before or after")
			}

			// --client selects the native event format of a hook run. The
			// inference agent of --distill is a separate setting, agent.cli.
			// A user can take one for the other, so the error names both.
			if hookClient != "" && !hookMode && !hookReset {
				return fmt.Errorf("--client applies to --hook and --hook-reset only; " +
					"the agent that --distill runs is agent.cli in .seamark/config.yaml")
			}

			// `--apply p1, p2` is natural typing; the shell splits the
			// spaced list into positional args, so fold them back in.
			if len(args) > 0 && decisions == 0 {
				return fmt.Errorf("unexpected argument %q — provide --file, --list, --distill, --apply, --dismiss, or --prune", args[0])
			}

			extra := strings.Join(args, ",")

			switch {
			case hookMode:
				return runLessonsHook(cmd, opts, hookClient)
			case hookReset:
				return runLessonsHookReset(cmd, opts, hookClient)
			case strings.TrimSpace(applyIDs) != "":
				return runLessonsApply(cmd, opts, applyIDs+","+extra)
			case strings.TrimSpace(dismissIDs) != "":
				return runLessonsDismiss(cmd, opts, dismissIDs+","+extra)
			case strings.TrimSpace(pruneIDs) != "":
				return runLessonsPrune(cmd, opts, pruneIDs+","+extra)
			case strings.TrimSpace(retargetIDs) != "":
				return runLessonsRetarget(cmd, opts, retargetIDs+","+extra)
			case proposalList:
				return runLessonsProposals(cmd, opts)
			case extractTriggers:
				return runLessonsExtractTriggers(cmd, opts, dryRun)
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

				lessons, err := lessonsForFile(st, root, file)
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
		"read an agent's edit-hook JSON payload from stdin and emit lessons as additionalContext")
	cmd.Flags().BoolVar(&hookReset, "hook-reset", false,
		"read an agent's context-reset JSON payload from stdin and reset once-per-context delivery")
	_ = cmd.Flags().MarkHidden("hook-reset")
	cmd.Flags().StringVar(&hookClient, "client", "",
		"with --hook: the agent whose hook event to read ("+strings.Join(integration.Builtin().IDs(), ", ")+
			"); without it the event is a Claude Code event")
	cmd.Flags().BoolVar(&distillRun, "distill", false,
		"distill recurring patterns from raw findings into proposed pins (needs the agent CLI)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"with --distill or --extract-triggers: show what would be sent to the agent (sizes, cost) without sending anything")
	cmd.Flags().BoolVar(&proposalList, "proposals", false,
		"show the distillation ledger: pending, applied, and dismissed proposals (no agent calls)")
	cmd.Flags().IntVar(&limit, "limit", distill.DefaultGroupLimit,
		"max new groups one --distill run sends to the agent (0 = all)")
	cmd.Flags().StringVar(&applyIDs, "apply", "",
		"apply proposals as pins by id or range (p3,p7 or p1..p9); writes lessons.yaml only with distill.write in config")
	cmd.Flags().StringVar(&dismissIDs, "dismiss", "",
		"dismiss proposals by id or range (p2 or p1..p9) — remembered, never re-proposed for the same evidence")
	cmd.Flags().StringVar(&pruneIDs, "prune", "",
		"remove applied pins that restate another (p16,p45) — the theme stays pinned by its survivor")
	cmd.Flags().StringVar(&retargetIDs, "retarget", "",
		"update applied pins to the regions today's inference computes (p3,p7) — lessons.yaml and the ledger together, write-gated like --apply")
	cmd.Flags().BoolVar(&extractTriggers, "extract-triggers", false,
		"backfill trigger paths for existing proposals via small agent calls (needs the agent CLI); composes with --dry-run")

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

// runLessonsProposals prints the distillation ledger. Read-only and
// offline: the plan view costs an agent call when new groups exist, so
// "what is waiting for me?" needs a way to ask for free.
func runLessonsProposals(cmd *cobra.Command, opts *options) error {
	st, root, err := openIndex(opts)
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

	health, err := proposals.Health(st, root, append(states[0], states[1]...))
	if err != nil {
		return err
	}

	// Applied pins are the ones that cost context on every edit, so the
	// duplicate audit runs over them.
	report.PrintProposalLedger(cmd.OutOrStdout(), states[0], states[1], states[2],
		distill.Clusters(states[1]), health)

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

	// Validate config even for dry runs, which need no installed CLI.
	// Use one spec for both command disclosure and invocation.
	spec, err := integration.Builtin().ResolveInvocation(acfg, root)
	if err != nil {
		return fmt.Errorf("distill unavailable: %w", err)
	}

	var inv agent.Invoker

	if !dryRun {
		if inv, err = agent.NewCommand(spec); err != nil {
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

	dopts := distill.Options{
		Region: region,
		Limit:  limit,
		Pins:   distill.PinsFromConfig(lcfg),
		Agent:  spec.Argv,
		Root:   root,
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
		// Groups saved before the failure are marked and kept; say so,
		// or the error implies the whole paid run was lost.
		if res != nil && res.GroupsRead > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"distill stopped after %d group(s) read — persisted proposals and signature marks are kept\n",
				res.GroupsRead)
		}

		return err
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(),
			"\n(dry run — nothing was sent; %d group(s) remain unread; drop --dry-run to distill)\n",
			res.GroupsPending)

		// The preflight above already leaves these rows out. Naming them
		// shows the one write that the real run makes before its first call.
		if len(res.StalePending) > 0 {
			ids := make([]string, len(res.StalePending))
			for i, id := range res.StalePending {
				ids[i] = fmt.Sprintf("p%d", id)
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"(a real run first prunes %d stale pending proposal(s) whose evidence group is gone: %s)\n",
				len(ids), strings.Join(ids, ", "))
		}

		return nil
	}

	pending, err := st.Proposals(model.ProposalProposed)
	if err != nil {
		return err
	}

	// The plan below is the paid result; annotations are advisory. A
	// failure here degrades to a warning instead of hiding what was
	// purchased — the proposals are persisted either way.
	notes, flagged, err := planAnnotations(st, lcfg, root, pending)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: trigger annotations unavailable (%v)\n", err)

		notes, flagged = nil, nil
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
	}, pending, notes, flagged)

	return nil
}

// planAnnotations renders the trigger lines the distill plan prints
// under each pending proposal: what each stored trigger path did
// (directly cited, co-change confirmed, or unconfirmed), plus the
// note-based scope advisory for proposals without triggers. flagged
// lists the pN ids whose delivery may still miss the trigger.
func planAnnotations(st *store.Store, lcfg *reviews.Config, root string,
	pending []model.Proposal,
) (notes map[int64][]string, flagged []string, err error) {
	if len(pending) == 0 {
		return nil, nil, nil
	}

	var ids []int64

	for _, p := range pending {
		ids = append(ids, p.Members...)
	}

	meta, err := st.FindingsMetaByIDs(ids)
	if err != nil {
		return nil, nil, err
	}

	advisories, err := distill.AuditScopes(st, lcfg, root, pending, meta)
	if err != nil {
		return nil, nil, err
	}

	notes = map[int64][]string{}
	missed := map[int64]bool{}

	for _, p := range pending {
		var living []model.Finding

		for _, id := range p.Members {
			if f, ok := meta[id]; ok {
				living = append(living, f)
			}
		}

		if len(p.TriggerPaths) > 0 && len(living) > 0 {
			_, facts, err := distill.RecomputeRegions(st, root, p, living)
			if err != nil {
				return nil, nil, err
			}

			for _, f := range facts {
				switch {
				case f.Selected && f.Direct:
					notes[p.ID] = append(notes[p.ID], fmt.Sprintf(
						"trigger %s — directly cited by the evidence; delivery targets %s",
						f.Path, f.Region,
					))
				case f.Selected:
					notes[p.ID] = append(notes[p.ID], fmt.Sprintf(
						"trigger %s — confirmed by co-change (%d shared commits); delivery targets %s",
						f.Path, f.Together, f.Region,
					))
				case !f.Direct && f.Together == 0:
					notes[p.ID] = append(notes[p.ID], fmt.Sprintf(
						"trigger %s — named by the distiller, not confirmed by history; "+
							"consider regions after apply", f.Path,
					))
					missed[p.ID] = true
				default:
					// Confirmed but blocked — the shared sentence, so
					// the ledger and report cannot disagree.
					notes[p.ID] = append(notes[p.ID], f.BlockedLine())
					missed[p.ID] = true
				}
			}
		}

		if adv, ok := advisories[p.ID]; ok {
			notes[p.ID] = append(notes[p.ID], adv.Line())
			missed[p.ID] = true
		}
	}

	for _, p := range pending {
		if missed[p.ID] {
			flagged = append(flagged, fmt.Sprintf("p%d", p.ID))
		}
	}

	return notes, flagged, nil
}

// runLessonsExtractTriggers backfills trigger paths for proposals
// distilled before extraction existed. Small agent calls
// name where each mistake is made; the harness validates every name.
// Pending rows retarget in place. Applied pins only gain stored
// triggers — their drift shows in --proposals, and the explicit
// --retarget applies it. A row that another writer changes during the
// run keeps its state, and the run stops; the next run resumes.
func runLessonsExtractTriggers(cmd *cobra.Command, opts *options, dryRun bool) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	acfg, err := agent.LoadConfig(root)
	if err != nil {
		return err
	}

	spec, err := integration.Builtin().ResolveInvocation(acfg, root)
	if err != nil {
		return fmt.Errorf("extraction unavailable: %w", err)
	}

	var inv agent.Invoker

	if !dryRun {
		if inv, err = agent.NewCommand(spec); err != nil {
			return fmt.Errorf("extraction unavailable: %w", err)
		}
	}

	// The liveness rule every applied-pin surface uses: a hand-pruned
	// or re-scoped pin delivers nothing, so paying to extract its
	// triggers buys nothing.
	lcfg, err := reviews.LoadConfig(root)
	if err != nil {
		return fmt.Errorf("failed to load lessons config: %w", err)
	}

	// Pending and applied rows are delivery; dismissed ones are not.
	// Rows with validated paths are done. A negative answer is done only
	// under the current semantic question; legacy negatives are re-asked
	// once when that question changes.
	var candidates []model.Proposal

	pruned := 0

	for _, status := range []string{model.ProposalProposed, model.ProposalApplied} {
		ps, err := st.Proposals(status)
		if err != nil {
			return err
		}

		for _, p := range ps {
			if open, _ := distill.TriggerQuestionOpen(p); !open {
				continue
			}

			if p.Status == model.ProposalApplied {
				if _, live := lcfg.FindPin(reviews.ProposalKey(p)); !live {
					pruned++

					continue
				}
			}

			candidates = append(candidates, p)
		}
	}

	out := cmd.OutOrStdout()

	if pruned > 0 {
		fmt.Fprintf(out, "%d applied row(s) skipped — their pins are not in .seamark/lessons.yaml, "+
			"so there is no delivery to retarget\n", pruned)
	}

	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing to extract — every pending and applied proposal has an answered "+
			"trigger question (or none exist)")

		return nil
	}

	var ids []int64

	for _, p := range candidates {
		ids = append(ids, p.Members...)
	}

	meta, err := st.FindingsMetaByIDs(ids)
	if err != nil {
		return err
	}

	eopts := distill.ExtractOptions{
		Root:   root,
		DryRun: dryRun,
		Agent:  spec.Argv,
		OnPreflight: func(pf distill.ExtractPreflight) {
			// The same scrubbing printPreflight applies: the disclosure
			// must not leak a credential embedded in config.yaml, nor
			// carry terminal control characters.
			fmt.Fprintf(out, "extraction preflight — %d proposal(s) in %d batch(es), ~%s tokens to %s\n",
				pf.Proposals, pf.Batches, pf.Tokens(),
				render.Sanitize(redact.Secrets(strings.Join(pf.Agent, " "))))
			fmt.Fprintf(out, "  each item sends its rule, note, evidence paths, and one excerpt (capped %d chars)\n",
				pf.BodyCap)
		},
		Logf: func(format string, args ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		},
	}

	// On a terminal, each agent call gets a live spinner with elapsed
	// time instead of dead air — the same surface distill uses; piped
	// output keeps the plain log lines.
	u := newUI(cmd.ErrOrStderr())
	defer u.finish("")

	if u.tty {
		eopts.OnBatchStart = func(desc string) { u.phase("extract", desc) }
		eopts.OnBatchDone = func(outcome string) { u.finish(outcome) }
	}

	res, err := distill.ExtractTriggers(cmd.Context(), st, inv, candidates, meta, eopts)
	if err != nil {
		// Stamped rows stay done; the next run resumes after them.
		if res != nil && res.Examined > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"extraction stopped after %d examined, %d stored — answered rows are stamped; re-running resumes\n",
				res.Examined, res.Stored)
		}

		return err
	}

	if dryRun {
		fmt.Fprintln(out, "\n(dry run — nothing was sent; drop --dry-run to extract)")

		return nil
	}

	fmt.Fprintf(out, "\nextracted — %d examined, %d named triggers, %d stored, %d pending retargeted",
		res.Examined, res.Named, res.Stored, res.Retargeted)

	if res.BatchesFailed > 0 {
		fmt.Fprintf(out, ", %d batch(es) failed (retried next run)", res.BatchesFailed)
	}

	// A conflict is not a stored answer. The stderr lines name each row
	// and the cause. The summary counts conflicts apart, so it never
	// reports them as stored.
	if res.Conflicts > 0 {
		fmt.Fprintf(out, ", %d conflict(s) not saved", res.Conflicts)
	}

	if res.Unattempted > 0 {
		fmt.Fprintf(out, ", %d not sent", res.Unattempted)
	}

	// Failed batches may have died before any request left the machine;
	// "sent" would overstate what was spent.
	verb := "sent"
	if res.BatchesFailed > 0 {
		verb = "attempted"
	}

	fmt.Fprintf(out, "; ~%s tokens %s / ~%s back\n", res.TokensSent(), verb, res.TokensBack())

	if res.AppliedStored > 0 {
		fmt.Fprintln(out, "applied pins with new triggers show their current delivery scopes in "+
			"`lessons --proposals`; `--retarget` applies them")
	}

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
	fmt.Fprintf(w, "  contents  per finding: bounded review comment, or fix-commit message,\n"+
		"            changed-function names and representative patch excerpts; plus relevant\n"+
		"            repo-relative paths, finding id, source, PR number, reviewer, and labels\n"+
		"            — no whole source files or environment values\n")
	fmt.Fprintf(w, "  redaction no additional dispatch redaction — stored bodies are sent as written,\n"+
		"            with path metadata and body sharing up to %d chars per finding; groups share a fixed evidence budget\n"+
		"            (mining already scrubs secret-shaped values)\n", pf.BodyCap)

	for _, g := range pf.Groups {
		region := g.Region
		if region == "" {
			region = "repo-wide"
		}

		fmt.Fprintf(w, "    %-28s %3d finding(s)  ~%s tokens  %d chars/finding max\n",
			render.Sanitize(region), g.Findings,
			distill.Preflight{PromptChars: g.PromptChars}.Tokens(), g.BodyCap)

		for _, evidence := range g.Evidence {
			pr := ""
			if evidence.PR > 0 {
				pr = fmt.Sprintf(" PR #%d", evidence.PR)
			}

			fmt.Fprintf(w, "      - %s%s  %s  (finding %d)\n",
				render.Sanitize(evidence.Source), pr,
				render.Sanitize(evidence.Path), evidence.ID)

			if len(evidence.Paths) > 1 {
				paths := make([]string, 0, len(evidence.Paths))
				for _, p := range evidence.Paths {
					paths = append(paths, render.Sanitize(p))
				}

				fmt.Fprintf(w, "        paths  %s\n", strings.Join(paths, ", "))
			}
		}
	}

	if !dryRun {
		fmt.Fprintln(w)
	}
}

// runLessonsList prints the ledger of mined lessons — every one, or, with
// a region, those in that directory's area. A region-scoped ledger is
// the pattern-hunting view: per-file clustering keeps semantically
// related one-offs apart. Reading an area's raw findings together can
// reveal patterns that counters miss.
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

// runLessonsHook implements the edit-hook path. It must never fail the
// tool it guards: any error (no index, unreadable payload) yields empty
// output and exit 0, so a missing seamark index can't block edits.
//
// The command only connects the parts. The client adapter decodes the
// native event and encodes the reply, and the delivery service owns
// selection, suppression, emission order, and the firing log.
func runLessonsHook(cmd *cobra.Command, opts *options, clientID string) error {
	client, ok := lessonsHookClient(cmd, clientID)
	if !ok || client.Edits == nil {
		return nil
	}

	payload, err := integration.ReadHookPayload(cmd.InOrStdin())
	if err != nil {
		return nil // nothing to say; never block the edit
	}

	event, err := client.Edits.DecodeEdit(payload)
	if err != nil || len(event.Paths) == 0 {
		return nil
	}

	st, root, err := openHookIndex(opts)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()

	if event.CWD == "" {
		// A client that sends no working directory sends paths relative
		// to the hook process, which is how the hook always read them.
		event.CWD, _ = os.Getwd()
	}

	emit := func(advice string) error {
		reply, err := client.Edits.EncodeAdvice(advice)
		if err != nil {
			return err
		}

		_, err = cmd.OutOrStdout().Write(reply.Stdout)

		return err
	}

	_, err = delivery.Deliver(cmd.Context(), st, delivery.Request{
		Root: root, ClientID: client.ID, Mechanism: client.Edits.AdviceMechanism(),
		Event: event, Config: loadLessonsConfig(root),
	}, emit)

	// Only a failed reply is the hook's own failure. A failed lookup
	// stays quiet: the advice is optional and the edit is not.
	var emitErr *delivery.EmitError
	if errors.As(err, &emitErr) {
		return emitErr.Err
	}

	return nil
}

// runLessonsHookReset implements the context-reset lifecycle hook
// (Claude Code PostCompact). It is silent and best-effort: compaction
// must proceed even if local state is unavailable.
func runLessonsHookReset(cmd *cobra.Command, opts *options, clientID string) error {
	client, ok := lessonsHookClient(cmd, clientID)
	if !ok || client.Resets == nil {
		return nil
	}

	payload, err := integration.ReadHookPayload(cmd.InOrStdin())
	if err != nil {
		return nil
	}

	event, err := client.Resets.DecodeReset(payload)
	if err != nil {
		return nil
	}

	root, err := index.ResolveRoot(opts.workspace)
	if err != nil {
		return nil
	}

	_ = delivery.Reset(delivery.ResetRequest{Root: root, ClientID: client.ID, Event: event})

	return nil
}

// lessonsHookClient returns the client whose native events a hook run
// translates. The ID comes from the installed hook command, never from
// the payload, so a payload cannot claim the state of another client.
// A command without --client keeps Claude Code semantics, because every
// installed Claude Code hook runs exactly that command.
//
// An unknown ID is a broken hook command. The run says so on stderr and
// still exits 0: a hook must never fail the edit it watches.
func lessonsHookClient(cmd *cobra.Command, clientID string) (integration.Client, bool) {
	if clientID == "" {
		clientID = integration.ClaudeID
	}

	registry := integration.Builtin()

	client, ok := registry.Lookup(clientID)
	if !ok {
		fmt.Fprintf(cmd.ErrOrStderr(), "seamark: unknown hook client %q (known: %s); no lessons delivered\n",
			render.Sanitize(clientID), strings.Join(registry.IDs(), ", "))
	}

	return client, ok
}

// runLessonsStats prints the firing-log summary: which lessons actually
// reach agents, and which would surface but never have (decay signal).
// It reads through lessonstats.Gather, the reader of the console's Stats
// view, so both surfaces count the same records by the same rules.
func runLessonsStats(cmd *cobra.Command, opts *options) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return fmt.Errorf("failed to read index: %w", err)
	}
	defer func() { _ = st.Close() }()

	// The command prints nothing when a source fails, because partial
	// records would print as low counts. Err names the first failure.
	snap := lessonstats.Gather(st, root)
	if err := snap.Err(); err != nil {
		return err
	}

	report.PrintFiringSummary(cmd.OutOrStdout(), snap.Summary)

	// Gather lists the applied proposals by ascending ID for the console.
	// The command keeps its own order, newest first.
	report.PrintOutcomes(cmd.OutOrStdout(), lessonstats.NewestFirst(snap.Applied), snap.Readings)

	return nil
}

// lessonsForFile normalizes path to repo-relative and returns the
// config-filtered lessons for its area. The --file view is a deliberate
// question, so it applies no pin budget and keeps every restatement. It
// still keeps at most 8 lessons. The edit hook is an injection the agent
// never asked for; the delivery service applies its budget.
func lessonsForFile(st *store.Store, root, path string) ([]model.Lesson, error) {
	return report.LessonsForScope(st, loadLessonsConfig(root), toRepoRel(root, path), 8)
}

func loadLessonsConfig(root string) *reviews.Config {
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		return reviews.DefaultConfig()
	}

	return cfg
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

// openHookIndex opens the index for a hook. It prints no staleness
// warning, because the hook must emit only its reply on stdout.
//
// A workspace without a database gets an empty in-memory index.
// store.Open would create the file, and a hook must leave no index
// behind. The pins of lessons.yaml need no mined data, so a clone that
// was never indexed still gets them. For the same reason the hook also
// reads a database without an index run, which openIndex refuses.
func openHookIndex(opts *options) (*store.Store, string, error) {
	// Unlike resolveWorkspace, the hook widens -C to the git top level
	// also with --db. Locate applies that rule.
	ws, err := workspace.Locate(opts.workspace, opts.dbPath)
	if err != nil {
		return nil, "", err
	}

	p := workspace.Probe(ws)
	if p.State == workspace.IndexAbsent {
		st, err := store.OpenMemory()

		return st, ws.Root, err
	}

	// store.Open migrates an older schema. Any open error keeps the
	// hook silent, so the edit goes on without advice.
	st, err := store.Open(ws.DBPath)
	if err != nil {
		return nil, "", err
	}

	// The probe reads the meta only with the supported schema. After
	// the open the database has that schema, so a second probe reads
	// it. An unreadable meta leaves p.Root at the workspace root.
	if !p.State.Readable() {
		p = workspace.Probe(ws)
	}

	return st, p.Root, nil
}
