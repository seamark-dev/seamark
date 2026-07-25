// Command seamark is the CLI entry point. All behavior lives in
// internal/cli so the binary stays a two-liner.
package main

import (
	"os"

	"github.com/seamark-dev/seamark/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
