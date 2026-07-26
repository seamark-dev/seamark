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
		hookMode bool
		list     bool
	)

	cmd := &cobra.Command{
		Use:   "lessons [--file <path> | --list]",
		Short: "Show the recurring review feedback mined from pull requests",
		Long: `Prints the review lessons (mined by "index --reviews", tuned by
.seamark/lessons.yaml) — the mistakes reviewers keep flagging.

  --file <path>  the lessons that apply to one file's area
  --list         every mined lesson repo-wide, with config syntax to
                 mute the noise or pin what must never be ignored
  --hook         read a Claude Code PreToolUse payload from stdin and emit
                 the edited file's lessons as additionalContext

--hook is read-only and offline: no network, no re-indexing, silent when
a file has no lessons.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case hookMode:
				return runLessonsHook(cmd, opts)
			case list:
				return runLessonsList(cmd, opts)
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
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"read a PreToolUse JSON payload from stdin and emit lessons as additionalContext")

	return cmd
}

// runLessonsList prints the full ledger of mined lessons.
func runLessonsList(cmd *cobra.Command, opts *options) error {
	st, root, err := openIndex(opts)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	lessons, err := st.AllLessons(0)
	if err != nil {
		return err
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
	path, err := readHookFilePath(cmd.InOrStdin())
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

	return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
}

// hookOutput is the PreToolUse response shape: additionalContext is
// injected into the agent's context without blocking the tool.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// readHookFilePath extracts tool_input.file_path from a PreToolUse
// payload (Edit, Write, and MultiEdit all carry it).
func readHookFilePath(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", err
	}

	var payload struct {
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}

	return payload.ToolInput.FilePath, nil
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
