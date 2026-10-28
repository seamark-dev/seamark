package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// deliveryFlagError runs before dispatch so no combination can turn an
// evidence read into a mutation or an agent call. VisitAll's flag-name
// order makes the first conflict deterministic, including future flags.
func deliveryFlagError(cmd *cobra.Command, deliveryReport bool) error {
	if !deliveryReport {
		return nil
	}

	var conflict string

	// Global flags bind command context, not another lessons action.
	inherited := cmd.InheritedFlags()

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if inherited.Lookup(f.Name) != nil {
			return
		}

		switch f.Name {
		case "delivery", "pin", "json":
			return
		}

		if f.Changed && conflict == "" {
			conflict = f.Name
		}
	})

	if conflict != "" {
		return fmt.Errorf("--delivery does not combine with --%s", conflict)
	}

	return nil
}

// runLessonsDelivery binds filesystem reads to the workspace. The applied
// ledger is optional: a missing or unreadable ledger does not stop the
// read of the audit sources.
func runLessonsDelivery(cmd *cobra.Command, opts *options, pinRef string, asJSON bool) error {
	ws, err := workspace.Resolve(opts.workspace, opts.dbPath)
	if err != nil {
		return err
	}

	probe := workspace.Probe(ws)
	req := lessonreport.Request{Root: ws.Root, PinRef: pinRef, SeamarkVersion: version}

	if probe.State.Readable() {
		req.Applied = deliveryApplied(ws.DBPath)
	}

	report, err := lessonreport.Gather(cmd.Context(), req)
	if err != nil {
		return err
	}

	if !asJSON {
		return lessonreport.RenderText(cmd.OutOrStdout(), report)
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")

	return enc.Encode(report)
}

// deliveryApplied reads only applied rows, not ledger health, which
// would reread report sources. Missing or unreadable links are optional.
func deliveryApplied(path string) []model.Proposal {
	st, err := store.OpenReadOnly(path)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	if err != nil {
		return nil
	}

	return applied
}
