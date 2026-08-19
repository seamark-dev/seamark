// Command lessons-bench-prepare materializes immutable public-repository
// source and offline dependencies before a lessons benchmark run.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/seamark-dev/seamark/internal/bench"
)

func main() {
	instanceID := flag.String("instance", bench.OTelHistogramInstanceID,
		"public benchmark instance to prepare")
	flag.Parse()

	instance, err := bench.InstanceByID(*instanceID)
	if err != nil {
		fail(err)
	}

	if err := instance.Validate(); err != nil {
		fail(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("preparing immutable source for %s\n", instance.ID)

	path, err := bench.PrepareInstance(ctx, instance)
	if err != nil {
		fail(err)
	}

	fmt.Printf("prepared %s at %s\n", instance.ID, path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "lessons-bench-prepare:", err)
	os.Exit(1)
}
