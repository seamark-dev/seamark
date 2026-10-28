package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// compactDeliveryRequest keeps filesystem reads in the actual workspace,
// even when the index remembers the root of a previous checkout. An
// unresolved workspace disables only the added reading; an empty Root
// must never turn into a read against the process working directory.
func compactDeliveryRequest(opts *options) lessonreport.Request {
	ws, err := workspace.Resolve(opts.workspace, opts.dbPath)
	if err != nil {
		return lessonreport.Request{}
	}

	return lessonreport.Request{Root: ws.Root, SeamarkVersion: version, Now: time.Now}
}

// captureDelivery gathers once for every applied row already loaded by
// the command. Capture source failures cannot invalidate the legacy
// outcome reading; the overview never requests a pin selection.
func captureDelivery(cmd *cobra.Command, opts *options, applied []model.Proposal) map[int64]string {
	req := compactDeliveryRequest(opts)
	if req.Root == "" {
		return nil
	}

	req.Applied = applied
	r, err := lessonreport.Gather(cmd.Context(), req)
	if err != nil {
		return nil
	}

	return report.CaptureDelivery(applied, &r)
}

// withDelivery adds only current applied-pin readings to ledger health.
// A pending or dismissed proposal never receives a capture line.
func withDelivery(health map[int64]report.ProposalHealth, cmd *cobra.Command, opts *options,
	applied []model.Proposal,
) map[int64]report.ProposalHealth {
	for id, line := range captureDelivery(cmd, opts, applied) {
		if health == nil {
			health = make(map[int64]report.ProposalHealth)
		}

		h := health[id]
		h.Delivery = line
		health[id] = h
	}

	return health
}
