package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// This file holds lessons --verify (contracts K5 and §4). The command
// finds the repository, normalizes the --file values, and calls
// lessoncheck.Verify, the one verification service of the CLI, the MCP
// server, and the console. The command decides no outcome itself.

// verifyConflicts lists the lessons modes and decision flags that
// --verify refuses, in the order of the help text. The first flag that
// the call sets names the conflict (contracts §4.2). Each modifier flag,
// for example --client, keeps its own guard and is not here.
// The P1 refusal of --hook goes when milestone P4 routes
// `--verify --hook` to the post-edit handler.
var verifyConflicts = []string{
	"list", "region", "stats", "distill", "proposals", "apply",
	"dismiss", "prune", "retarget", "extract-triggers", "hook", "hook-reset",
}

// verifyFlagError checks the flags that --verify adds before any
// dispatch. --json and a second --file value apply to --verify only,
// and --verify combines with no other mode. The texts are the
// milestone P1 forms of contracts §4.1: later milestones add modes to
// the --json text.
func verifyFlagError(cmd *cobra.Command, verify, asJSON bool, files int) error {
	switch {
	case asJSON && !verify:
		return errors.New("--json applies to --verify only")
	case files > 1 && !verify:
		return errors.New("--file takes one path except with --verify")
	case !verify:
		return nil
	}

	for _, name := range verifyConflicts {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--verify does not combine with --%s", name)
		}
	}

	return nil
}

// runLessonsVerify runs lessons --verify. It prints the text report, or
// with asJSON only the lesson-verification/1 document. It returns the
// error of the summary state, so Execute prints the K5 stderr line and
// exits 2 for a violation and 1 for an incomplete check. A root or
// --file error returns before Verify runs, so stdout then stays empty.
func runLessonsVerify(cmd *cobra.Command, opts *options, files []string, asJSON bool) error {
	root, err := verifyRoot(opts.workspace)
	if err != nil {
		return err
	}

	paths, err := verifyFiles(root, files)
	if err != nil {
		return err
	}

	// A zero Limits means the default limits. Verify sets the
	// verify-timeout deadline itself, so the command adds none.
	v, err := lessoncheck.Verify(cmd.Context(), lessoncheck.VerifyRequest{
		Root:           root,
		Paths:          paths,
		Caller:         lessoncheck.CallerCLI,
		SeamarkVersion: version,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	if asJSON {
		// Two-space indentation matches the other JSON documents of the
		// CLI (status, doctor, capture). The indentation is not part of
		// the contract.
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")

		if err := enc.Encode(v); err != nil {
			return err
		}
	} else {
		lessoncheck.RenderText(out, v)
	}

	return v.ExitError()
}

// verifyRoot returns the top level of the git work tree of the -C
// directory. Verify reads the working tree of exactly that root, and the
// root must be a work tree (contract K4). history.ResolveRoot alone
// returns a directory outside git unchanged, so IsRepo runs first.
func verifyRoot(workspace string) (string, error) {
	dir, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}

	if !history.IsRepo(dir) {
		return "", fmt.Errorf("lesson checks: %s is not inside a git work tree", dir)
	}

	return history.ResolveRoot(dir)
}

// verifyFiles normalizes the --file values of --verify. An empty list
// gives the default selection.
func verifyFiles(root string, files []string) ([]string, error) {
	paths := make([]string, 0, len(files))

	for _, value := range files {
		p, err := verifyFile(root, value)
		if err != nil {
			return nil, err
		}

		paths = append(paths, p)
	}

	return paths, nil
}

// verifyFile turns one --file value into a repository-relative path
// (contracts §1.3). A relative value starts at the process directory,
// as in every shell command, not at the repository root. The process
// directory has its links resolved first (contracts K4 Addition of the
// P1 fix round). The lookup fallback of toRepoRel, which reads an
// outside value as repository-relative, is not used: a check of the
// wrong path must never pass. The errors quote the value as typed.
func verifyFile(root, value string) (string, error) {
	whole := fmt.Errorf("--file %q names the whole repository; omit --file for the default selection", value)

	// NormalizeRegion also trims spaces, but only around the whole
	// value. Trimming before filepath.Abs keeps a padded relative value
	// relative. An empty value means the whole repository, as it does
	// for NormalizeRegion.
	input := strings.TrimSpace(value)
	if input == "" {
		return "", whole
	}

	abs, err := verifyAbs(input)
	if err != nil {
		return "", err
	}

	// An absolute value can reach root through a link, for example
	// /var/... for /private/var/... NormalizeRegion matches that alias of
	// root and keeps the rest of the value lexical.
	rel, err := distill.NormalizeRegion(root, abs)

	switch {
	case errors.Is(err, distill.ErrRegionEscapes):
		return "", fmt.Errorf("--file %q is outside the repository", value)
	case err != nil:
		return "", err
	case rel == "":
		return "", whole
	}

	return rel, nil
}

// verifyAbs makes a --file value absolute. A relative value joins the
// process directory with its links resolved. The value itself stays
// lexical, because a link that it names must stay visible as a link
// (§1.3). An absolute value keeps filepath.Abs. The Windows forms \x and
// C:x are not absolute in Go, but they do not start at the process
// directory, so they also keep filepath.Abs.
func verifyAbs(input string) (string, error) {
	if filepath.IsAbs(input) || filepath.VolumeName(input) != "" || os.IsPathSeparator(input[0]) {
		return filepath.Abs(input)
	}

	dir, err := processDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, input), nil
}

// processDir returns the process directory with its symbolic links
// resolved. The rule comes from capture.resolvePath, which compares the
// hook cwd with the bound root after the same resolution
// (correction-learning F6). The kernel opens a relative path from the
// physical directory. So a link from outside the repository into it
// selects paths inside the repository. A link in the tree that leads
// outside selects paths outside it. A directory that does not resolve
// keeps its tail as text below the nearest ancestor that resolves.
func processDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved, nil
	}

	dir, tail := cwd, ""

	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd, nil
		}

		tail = filepath.Join(filepath.Base(dir), tail)
		dir = parent

		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, tail), nil
		}
	}
}
