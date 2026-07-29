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
		proposalList bool
		limit        int
		applyIDs     string
		dismissIDs   string
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
  --hook           read a Claude Code PreToolUse payload from stdin and emit
                   the edited file's lessons as additionalContext

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Applying and dismissing are opposite decisions; both at
			// once would silently route everything (positional ids
			// included) to apply.
			if strings.TrimSpace(applyIDs) != "" && strings.TrimSpace(dismissIDs) != "" {
				return fmt.Errorf("--apply and --dismiss are opposite decisions — run them one at a time")
			}

			// `--apply p1, p2` is natural typing; the shell splits the
			// spaced list into positional args, so fold them back in.
			if len(args) > 0 && strings.TrimSpace(applyIDs)+strings.TrimSpace(dismissIDs) == "" {
				return fmt.Errorf("unexpected argument %q — provide --file, --list, --distill, --apply, or --dismiss", args[0])
			}

			extra := strings.Join(args, ",")

			switch {
			case hookMode:
				return runLessonsHook(cmd, opts)
			case strings.TrimSpace(applyIDs) != "":
				return runLessonsApply(cmd, opts, applyIDs+","+extra)
			case strings.TrimSpace(dismissIDs) != "":
				return runLessonsDismiss(cmd, opts, dismissIDs+","+extra)
			case proposalList:
				return runLessonsProposals(cmd, opts)
			case distillRun:
				return runLessonsDistill(cmd, opts, strings.TrimSpace(region), limit)
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
	cmd.Flags().BoolVar(&proposalList, "proposals", false,
		"show the distillation ledger: pending, applied, and dismissed proposals (no agent calls)")
	cmd.Flags().IntVar(&limit, "limit", 10,
		"max new groups one --distill run sends to the agent (0 = all)")
	cmd.Flags().StringVar(&applyIDs, "apply", "",
		"apply proposals as pins by id or range (p3,p7 or p1..p9); writes lessons.yaml only with distill.write in config")
	cmd.Flags().StringVar(&dismissIDs, "dismiss", "",
		"dismiss proposals by id or range (p2 or p1..p9) — remembered, never re-proposed for the same evidence")

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

// resolveSelection turns a selection into concrete pending proposals,
// id order, deduplicated. Exact ids must be pending; ranges take what
// they find.
func resolveSelection(pending []model.Proposal, raw string) ([]model.Proposal, error) {
	sel, err := parseSelection(raw)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]model.Proposal, len(pending))
	for _, p := range pending {
		byID[p.ID] = p
	}

	added := map[int64]bool{}

	var out []model.Proposal

	for _, id := range sel.exact {
		p, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("p%d is not a pending proposal (already decided, or unknown — see the distill plan)", id)
		}

		if !added[id] {
			added[id] = true
			out = append(out, p)
		}
	}

	for _, r := range sel.ranges {
		for _, p := range pending {
			if p.ID >= r[0] && p.ID <= r[1] && !added[p.ID] {
				added[p.ID] = true
				out = append(out, p)
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("nothing in %q is still pending — see the distill plan", raw)
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

	ps, err := resolveSelection(pending, raw)
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

	ps, err := resolveSelection(pending, raw)
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
func runLessonsDistill(cmd *cobra.Command, opts *options, region string, limit int) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	acfg, err := agent.LoadConfig(root)
	if err != nil {
		return err
	}

	inv, err := agent.New(acfg)
	if err != nil {
		return fmt.Errorf("distill unavailable: %w", err)
	}

	if region != "" {
		region = toRepoRel(root, region)
	}

	// The workspace's pins — hand-written and applied alike — are
	// patterns the distiller must not re-derive under a new name.
	lcfg, err := reviews.LoadConfig(root)
	if err != nil {
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
		Logf: func(format string, args ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		},
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
