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

// WriteReport writes a rendered report to outPath, or to stdout when
// outPath is "-"; both report CLIs share the rule.
func WriteReport(outPath string, content []byte) error {
	if outPath == "-" {
		_, err := os.Stdout.Write(content)

		return err
	}

	return WriteAtomic(outPath, content)
}

// ResolveSeamarkBinary returns the absolute path of the seamark binary a
// benchmark drives: the configured one, or bin/seamark from `make build`.
// The file must exist, because every row hashes it.
func ResolveSeamarkBinary(configured string) (string, error) {
	bin := configured
	if bin == "" {
		bin = filepath.Join("bin", "seamark")
	}

	abs, err := filepath.Abs(bin)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("seamark binary not found at %s — run `make build` first (or pass -seamark)", abs)
	}

	return abs, nil
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
