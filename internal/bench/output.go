package bench

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RejectOutputCollision refuses an output path that names or aliases one of
// the protected inputs, so a report can never overwrite the evidence it was
// built from. "-" means stdout and is always accepted.
func RejectOutputCollision(outPath string, protected []string) error {
	if outPath == "-" {
		return nil
	}

	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}

	outInfo, outStatErr := os.Stat(outAbs)
	if outStatErr != nil && !os.IsNotExist(outStatErr) {
		return outStatErr
	}

	for _, path := range protected {
		protectedAbs, err := filepath.Abs(path)
		if err != nil {
			return err
		}

		if outAbs == protectedAbs {
			return fmt.Errorf("report output would overwrite source evidence: %s", path)
		}

		if outStatErr == nil {
			protectedInfo, err := os.Stat(protectedAbs)
			if err != nil {
				return err
			}

			if os.SameFile(outInfo, protectedInfo) {
				return fmt.Errorf("report output aliases source evidence: %s", path)
			}
		}
	}

	return nil
}

// WriteAtomic writes content through a temporary file and a rename, so a
// reader never sees a half-written report.
func WriteAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".bench-report-*")
	if err != nil {
		return err
	}

	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if n, err := tmp.Write(content); err != nil {
		_ = tmp.Close()

		return err
	} else if n != len(content) {
		_ = tmp.Close()

		return io.ErrShortWrite
	}

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}
