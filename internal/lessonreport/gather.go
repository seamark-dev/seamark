package lessonreport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Gather reads each source once and builds the report. Source failures
// become report states. Only a pin reference can return an error.
// It never takes a writer lock or calls a model.
//
// No source read takes the context: each source is a read of local
// files, and the report reads no capture archive. The parameter stays,
// so every surface calls Gather in the same form.
func Gather(_ context.Context, req Request) (Report, error) {
	return gather(req, gatherSources{
		audit: reviews.ReadAuditPrefix, checks: lessoncheck.ReadAuditPrefix,
		config: os.ReadFile, inspect: integration.Builtin().Inspect,
	})
}

// gatherSources names the read boundaries, so tests can count reads
// without replacing process-wide state.
type gatherSources struct {
	audit   func(string, int64) (reviews.AuditPrefix, error)
	checks  func(string, int64) (lessoncheck.AuditPrefix, error)
	config  func(string) ([]byte, error)
	inspect func(string) []integration.Inspection
}

func gather(req Request, sources gatherSources) (Report, error) {
	in := gatherInputs(req, sources)
	report, err := Build(in, req.PinRef)
	report.SeamarkVersion = req.SeamarkVersion

	return report, err
}

func gatherInputs(req Request, sources gatherSources) Inputs {
	now := req.Now
	if now == nil {
		now = time.Now
	}

	in := Inputs{Links: LinksFromApplied(req.Applied), Now: now()}

	maxBytes := req.Limits.MaxAuditBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxAuditBytes
	}

	var err error

	in.Audit, err = sources.audit(req.Root, maxBytes)
	if err != nil {
		in.AuditError = err.Error()
	}

	in.CheckAudit, err = sources.checks(req.Root, maxBytes)
	if err != nil {
		in.CheckAuditError = err.Error()
	}

	data, readErr := sources.config(filepath.Join(req.Root, ".seamark", "lessons.yaml"))
	in.Config, in.ConfigRead = readConfig(data, readErr)

	checkCfg, checkErrs := lessoncheck.ParseForCapability(data, readErr)
	if readErr == nil && in.Config != nil && len(checkErrs) == 0 {
		in.Checks = checkCfg
	}

	inspections := sources.inspect(req.Root)
	self := integration.SelfIdentity(req.SeamarkVersion)

	capability := integration.LessonCapabilityOf(inspections, checkCfg, checkErrs, self)
	in.Capability = &capability

	return in
}

// readConfig keeps absent and invalid files distinct. A tier-two check
// error does not discard advisory pins that the lenient reader accepts.
func readConfig(data []byte, readErr error) (*reviews.Config, ConfigRead) {
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return nil, ConfigRead{State: StateAbsent}
	case readErr != nil:
		return nil, ConfigRead{State: ConfigInvalid}
	}

	cfg, err := reviews.ParseConfig(data)
	if err != nil {
		return nil, ConfigRead{State: ConfigInvalid}
	}

	return cfg, ConfigRead{State: ConfigOK}
}
