package settings

import (
	"errors"
	"io"

	"github.com/seamark-dev/seamark/internal/integration"
)

// Result reports one save.
type Result struct {
	// Written is true when the file was replaced or created.
	Written bool
	// Created is true when the file did not exist before the save.
	Created bool
	// Path is the absolute path of the file.
	Path string
	// Err is the cause of a failed save; the file is then unchanged.
	Err error
}

// Save writes exactly the planned bytes, or returns ErrStale without a
// write. The setup engine checks the guard again before the write. It
// refuses a read-only file and keeps the file mode. It replaces the file
// by rename, so a failed write leaves the old content complete. A no-op
// plan writes nothing, but it also returns ErrStale when the file
// changed after the plan.
func Save(p *Plan) (Result, error) {
	if p == nil || p.Snapshot == nil {
		return Result{}, errors.New("settings: no plan")
	}

	res := Result{Path: p.Snapshot.Path}

	// A no-op plan has no setup plan, so the setup engine checks no
	// guard. The check is here instead: a success must mean that the
	// file still holds the values that the plan showed.
	if p.NoOp {
		if now, _, err := integration.ReadInput(p.Snapshot.Root, File); err != nil || now != p.Snapshot.Guard {
			res.Err = staleError()

			return res, res.Err
		}

		return res, nil
	}

	if p.Setup == nil {
		return res, errors.New("settings: the plan has no guarded write")
	}

	applied, err := integration.ApplySetup(p.Setup, integration.ApplyOptions{SkillsLog: io.Discard})
	if err != nil {
		// The setup wording says "run the command again"; the settings
		// view asks for a new inspection instead.
		if errors.Is(err, integration.ErrStalePlan) {
			err = staleError()
		}

		res.Err = err

		return res, err
	}

	for _, op := range applied.Ops {
		if op.Path == File && op.Status == integration.OpApplied {
			res.Written, res.Created = true, op.Created
		}
	}

	return res, nil
}
