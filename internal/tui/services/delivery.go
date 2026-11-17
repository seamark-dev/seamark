package services

import (
	"context"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
)

// Delivery binds the report to this workspace. Files stay at the git
// top level even when the index records an older identity root. Missing
// ledger links do not prevent reading the independent delivery sources.
func (e *engine) Delivery(req lessonreport.Request) (lessonreport.Report, error) {
	req.Root = e.ws.Root
	req.SeamarkVersion = e.version
	req.Now = e.deps.Now
	req.Applied = nil

	st, _, err := e.openRead()
	if err == nil {
		// Only the applied rows are needed. Loading ledger health would
		// reread report sources.
		applied, readErr := st.Proposals(model.ProposalApplied)
		if readErr == nil {
			req.Applied = applied
		}

		_ = st.Close()
	}

	return lessonreport.Gather(context.Background(), req)
}
