package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
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
		file       string
		region     string
		hookMode   bool
		list       bool
		stats      bool
		distillRun bool
		limit      int
		applyIDs   string
		dismissIDs string
	)

	cmd := &cobra.Command{
		Use:   "lessons [--file <path> | --list [--region <prefix>] | --stats]",
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
  --apply p3,p7    turn chosen proposals into pins. Writes lessons.yaml
                   only when config.yaml sets distill.write; otherwise
                   prints the block to paste. Never automatic.
  --dismiss p2     record a no — the same evidence is never re-proposed
  --hook           read a Claude Code PreToolUse payload from stdin and emit
                   the edited file's lessons as additionalContext

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
				return fmt.Errorf("provide --file <path>, --list, or --hook")
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
	cmd.Flags().IntVar(&limit, "limit", 10,
		"max new groups one --distill run sends to the agent (0 = all)")
	cmd.Flags().StringVar(&applyIDs, "apply", "",
		"apply proposals as pins by id (p3,p7); writes lessons.yaml only with distill.write in config")
	cmd.Flags().StringVar(&dismissIDs, "dismiss", "",
		"dismiss proposals by id (p2) — remembered, never re-proposed for the same evidence")

	return cmd
}

// parseProposalIDs reads "p3,p7" (or bare "3,7") into deduplicated ids
// — "p3,p3" must count as one, or the count check downstream misreads
// a typo as a missing proposal.
func parseProposalIDs(s string) ([]int64, error) {
	var ids []int64

	seen := map[int64]bool{}

	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "p")
		if part == "" {
			continue // spaced lists arrive with empty segments; harmless
		}

		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a proposal id (want p<N> from the distill plan)", part)
		}

		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no proposal ids given (want p<N> from the distill plan)")
	}

	return ids, nil
}

// runLessonsApply turns chosen proposals into pins. The pins land in
// .seamark/lessons.yaml only when the workspace opted in via
// distill.write; otherwise the rendered block is printed for the human
// to paste — either way, nothing was ever applied without an explicit
// human command naming explicit proposals.
func runLessonsApply(cmd *cobra.Command, opts *options, raw string) error {
	ids, err := parseProposalIDs(raw)
	if err != nil {
		return err
	}

	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ps, err := st.ProposalsByIDs(ids)
	if err != nil {
		return err
	}

	if len(ps) != len(ids) {
		return fmt.Errorf("only %d of %d ids are pending proposals — see `seamark lessons --distill` for the plan",
			len(ps), len(ids))
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
	ids, err := parseProposalIDs(raw)
	if err != nil {
		return err
	}

	st, _, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	n, err := st.SetProposalStatus(ids, model.ProposalDismissed)
	if err != nil {
		return err
	}

	if n != len(ids) {
		fmt.Fprintf(cmd.OutOrStdout(), "dismissed %d of %d (the rest were not pending proposals)\n", n, len(ids))

		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "dismissed %d proposal(s) — remembered; the same evidence will not return\n", n)

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

	res, err := distill.Run(cmd.Context(), st, distill.NewLexicalGrouper(), inv, distill.Options{
		Region: region,
		Limit:  limit,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		},
	})
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
