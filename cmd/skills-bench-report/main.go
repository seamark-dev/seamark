// Command skills-bench-report renders workflow result files, and optionally
// activation result files, into a deterministic Markdown report evaluated
// against the frozen workflow claims and activation criteria.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/seamark-dev/seamark/internal/bench"
)

func main() {
	claimsPath := flag.String("claims", "bench/workflow-claims.yaml", "frozen workflow claim registry")
	outPath := flag.String("out", "-", "Markdown output path; - writes stdout")
	promptsPath := flag.String("prompts", "bench/activation/prompts.yaml",
		"activation prompt manifest the activation rows must cover; read only with -activation")

	var activation pathList
	flag.Var(&activation, "activation", "activation results file; repeat for more than one")
	flag.Parse()

	if err := run(*claimsPath, *outPath, *promptsPath, activation, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "skills-bench-report:", err)
		os.Exit(1)
	}
}

// pathList collects a repeatable flag.
type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }

func (p *pathList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("empty path")
	}

	*p = append(*p, value)

	return nil
}

func run(claimsPath, outPath, promptsPath string, activationPaths, inputs []string) error {
	protected := append([]string{claimsPath}, inputs...)
	protected = append(protected, activationPaths...)

	if len(activationPaths) > 0 {
		protected = append(protected, promptsPath)
	}

	if err := bench.RejectOutputCollision(outPath, protected); err != nil {
		return err
	}

	registry, err := bench.LoadWorkflowClaimRegistry(claimsPath)
	if err != nil {
		return err
	}

	activation := bench.ActivationInputs{Paths: activationPaths}
	if len(activationPaths) > 0 {
		activation.Prompts, err = bench.LoadActivationPrompts(promptsPath)
		if err != nil {
			return err
		}
	}

	report, err := bench.BuildWorkflowReport(inputs, activation, registry)
	if err != nil {
		return err
	}

	content := []byte(report.Markdown())

	if outPath == "-" {
		_, err = os.Stdout.Write(content)

		return err
	}

	return bench.WriteAtomic(outPath, content)
}
