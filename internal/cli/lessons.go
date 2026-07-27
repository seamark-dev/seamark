package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

func newLessonsCmd(opts *options) *cobra.Command {
	var (
		file     string
		region   string
		hookMode bool
		list     bool
		stats    bool
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
  --hook           read a Claude Code PreToolUse payload from stdin and emit
                   the edited file's lessons as additionalContext

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case hookMode:
				return runLessonsHook(cmd, opts)
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

				return report.PrintLessonReminder(cmd.OutOrStdout(), file, lessons)
			default:
				return fmt.Errorf("provide --file <path>, --list, or --hook")
			}
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "repo-relative (or absolute) path to look up")
	cmd.Flags().BoolVar(&list, "list", false, "list every mined lesson with config-tuning syntax")
	cmd.Flags().StringVar(&region, "region", "", "narrow --list to a directory's area (implies --list)")
	cmd.Flags().BoolVar(&stats, "stats", false, "summarize the firing log: what surfaced, what never fires")
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"read a PreToolUse JSON payload from stdin and emit lessons as additionalContext")

	return cmd
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

	lessons, err := st.AllLessons(0)
	if err != nil {
		return err
	}

	if region != "" {
		region = toRepoRel(root, region)
		scoped := lessons[:0]

		// A lesson belongs to the area if its region sits inside the
		// asked-for prefix — or is an ancestor of it, so a directory
		// query still shows the package-wide lessons that cover it.
		for _, l := range lessons {
			if reviews.RegionMatches(region, l.Region) || reviews.RegionMatches(l.Region, region) {
				scoped = append(scoped, l)
			}
		}

		lessons = scoped
	}

	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	report.PrintLessonLedger(cmd.OutOrStdout(), lessons, cfg)

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

	lessons, err := lessonsForFile(st, root, path)
	if err != nil || len(lessons) == 0 {
		return nil
	}

	var b strings.Builder
	_ = report.PrintLessonReminder(&b, path, lessons)

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
// config-filtered lessons for its area.
func lessonsForFile(st *store.Store, root, path string) ([]model.Lesson, error) {
	rel := toRepoRel(root, path)

	// A malformed config must not silence the hook or the --file view;
	// fall back to defaults (mirrors why/orient's degrade-not-fail).
	cfg, err := reviews.LoadConfig(root)
	if err != nil {
		cfg = reviews.DefaultConfig()
	}

	return report.LessonsForScope(st, cfg, rel, 8)
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
