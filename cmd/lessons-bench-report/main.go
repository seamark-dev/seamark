// Command lessons-bench-report renders explicit benchmark JSONL inputs into a
// deterministic Markdown report evaluated against frozen claims.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/bench"
)

func main() {
	claimsPath := flag.String("claims", "bench/claims.yaml", "frozen benchmark claim registry")
	outPath := flag.String("out", "-", "Markdown output path; - writes stdout")
	flag.Parse()

	if err := run(*claimsPath, *outPath, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "lessons-bench-report:", err)
		os.Exit(1)
	}
}

func run(claimsPath, outPath string, inputs []string) error {
	if err := rejectOutputCollision(outPath, append([]string{claimsPath}, inputs...)); err != nil {
		return err
	}

	registry, err := bench.LoadClaimRegistry(claimsPath)
	if err != nil {
		return err
	}

	report, err := bench.BuildBenchmarkReport(inputs, registry)
	if err != nil {
		return err
	}

	content := []byte(report.Markdown())

	if outPath == "-" {
		_, err = os.Stdout.Write(content)

		return err
	}

	return writeAtomic(outPath, content)
}

func rejectOutputCollision(outPath string, protected []string) error {
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

func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".lessons-bench-report-*")
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
