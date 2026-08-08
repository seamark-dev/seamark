package bench

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// commitDate pins generated commit timestamps so equal source histories have
// equal commit IDs on every machine.
const commitDate = "2026-01-05T12:00:00Z"

// step is one commit in a generated repository. Files overwrite the previous
// tree while omitted paths carry forward.
type step struct {
	message string
	files   map[string]string
}

// generateRepository writes and commits a deterministic synthetic history.
// Benchmark instances own their source and history while sharing this plumbing.
func generateRepository(dir string, history []step) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := runGit(dir, "init", "-q", "-b", "main"); err != nil {
		return err
	}

	for _, s := range history {
		for rel, content := range s.files {
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
		}

		if err := runGit(dir, "add", "-A"); err != nil {
			return err
		}
		if err := runGit(dir, "commit", "-q", "-m", s.message); err != nil {
			return err
		}
	}

	return nil
}

// runGit runs one command with identity and dates pinned so generated history
// does not depend on the host identity or clock.
func runGit(dir string, args ...string) error {
	base := []string{
		"-c", "user.name=bench",
		"-c", "user.email=bench@seamark.dev",
		"-c", "commit.gpgsign=false",
	}

	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+commitDate,
		"GIT_COMMITTER_DATE="+commitDate,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}

	return nil
}
