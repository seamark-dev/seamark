// Command lessons-bench-report renders explicit benchmark JSONL inputs into a
// deterministic Markdown report evaluated against frozen claims.
package main

import (
	"flag"
	"fmt"
	"os"

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
	if err := bench.RejectOutputCollision(outPath, append([]string{claimsPath}, inputs...)); err != nil {
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

	return bench.WriteReport(outPath, []byte(report.Markdown()))
}
