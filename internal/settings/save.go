package settings

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
)

// Outcome says what the file holds after a save.
type Outcome int

// The save outcomes (P4-18). The zero value is NotWritten, so a result
// of a refused save reads as "nothing written".
const (
	// NotWritten means that the file does not hold the planned bytes:
	// the save refused before the write, or the write failed and the
	// file holds other bytes.
	NotWritten Outcome = iota
	// Written means that the file holds the planned bytes.
	Written
	// Unknown means that the write failed and the file cannot be read
	// again. The caller must not report "written" or "not written".
	Unknown
)

// Result reports one save.
type Result struct {
	// Outcome says what the file holds after the save.
	Outcome Outcome
	// Written is true when the file holds the planned bytes: Outcome is
	// Written.
	Written bool
	// Created is true when the file did not exist before the save.
	Created bool
	// Path is the absolute path of the file.
	Path string
	// Err is the cause of a failed save. Outcome says what the file
	// holds then.
	Err error
}

// Writer applies a guarded setup plan. integration.ApplySetup is the
// writer of Save; a test injects a writer that fails at a chosen step.
type Writer func(*integration.SetupPlan, integration.ApplyOptions) (integration.SetupResult, error)

// Save writes exactly the planned bytes with integration.ApplySetup. See
// SaveWith.
func Save(p *Plan) (Result, error) {
	return SaveWith(p, integration.ApplySetup)
}

// SaveWith writes exactly the planned bytes through write, or returns
// ErrStale without a write. The setup engine checks the guard again
// before the write. It refuses a read-only file and keeps the file mode.
// It replaces the file by rename, so a failed write leaves the old
// content complete. A no-op plan writes nothing, but it also returns
// ErrStale when the file changed after the plan.
//
// A refusal before the write is NotWritten. After an error of the write
// step, the save reads the file again (integration.Landed). It then
// reports Written, NotWritten, or Unknown, because the error alone does
// not prove the file state. The error stays in Result.Err in every case.
func SaveWith(p *Plan, write Writer) (Result, error) {
	if p == nil || p.Snapshot == nil {
		return Result{}, errors.New("settings: no plan")
	}

	if write == nil {
		return Result{}, errors.New("settings: no writer")
	}

	file := p.Group.Document()
	guard, path := p.Snapshot.observed(file)
	res := Result{Path: path}

	refuse := func(err error) (Result, error) {
		res.Err = err

		return res, err
	}

	// A no-op plan has no setup plan, so the setup engine checks no
	// guard. The check is here instead: a success must mean that the
	// file still holds the values that the plan showed.
	if p.NoOp {
		if err := p.Snapshot.unchanged(file); err != nil {
			return refuse(err)
		}

		return res, nil
	}

	if p.Setup == nil || len(p.Setup.Writes) != 1 {
		return refuse(errors.New("settings: the plan has no guarded write"))
	}

	// A hard link can appear after the plan without a change of the
	// guarded bytes, so the lessons save asks again (P4-17).
	if file == pinfile.File {
		if b := hardLinkBlock(p.Snapshot.Root); b != nil {
			return refuse(b)
		}
	}

	applied, err := write(p.Setup, integration.ApplyOptions{SkillsLog: io.Discard})
	if err != nil {
		// The engine checks every guard before it writes, so a stale
		// plan is a refusal before the write. The setup wording says
		// "run the command again"; the settings view asks for a new
		// inspection instead.
		if errors.Is(err, integration.ErrStalePlan) {
			return refuse(staleError(file))
		}

		res.Outcome, err = landed(p, file, err)
		res.Written = res.Outcome == Written
		res.Created = res.Written && !guard.Exists

		return refuse(err)
	}

	for _, op := range applied.Ops {
		if op.Path == file && op.Status == integration.OpApplied {
			res.Outcome, res.Written, res.Created = Written, true, op.Created
		}
	}

	return res, nil
}

// landed classifies a failed write by reading the file again. It
// returns the outcome and the error to report: the write error, joined
// with the read error when the outcome is Unknown.
func landed(p *Plan, file string, writeErr error) (Outcome, error) {
	holds, err := integration.Landed(p.Snapshot.Root, file, sha256.Sum256(p.Setup.Writes[0].After))

	switch {
	case err != nil:
		return Unknown, fmt.Errorf("%w; reading %s again failed: %w", writeErr, file, err)
	case holds:
		return Written, writeErr
	default:
		return NotWritten, writeErr
	}
}
