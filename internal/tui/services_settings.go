package tui

import (
	"fmt"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/settings"
)

// The focused settings services. They read and patch the configuration
// file under the effective root: the root that the index records, else
// the workspace root. The evidence refresh and the distillation read
// config.yaml under that root, so a save changes the file that the
// next run reads. The ledger reads use the same rule.

// Settings inspects the focused settings. It reads the file only; it
// runs no command and writes nothing.
func (e *engine) Settings() (*settings.Snapshot, error) {
	return settings.Inspect(e.probe().Root, e.reg)
}

// PlanSettings plans a guarded patch of one group. It writes nothing,
// so it does not take the mutation lock. The screen decides when the
// user can review; it refuses a new review while a mutation runs.
func (e *engine) PlanSettings(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
	return settings.PlanPatch(snap, e.reg, edits)
}

// SaveSettings writes a reviewed plan under the mutation lock. It
// refuses a plan whose file is not under the current effective root,
// because the index can record another root after the inspection.
func (e *engine) SaveSettings(p *settings.Plan) (settings.Result, error) {
	if !e.mu.TryLock() {
		return settingsRefused(p, ErrBusy), ErrBusy
	}
	defer e.mu.Unlock()

	if p != nil && p.Snapshot != nil {
		root, err := filepath.Abs(e.probe().Root)
		if err != nil || root != p.Snapshot.Root {
			stale := fmt.Errorf("%w: the workspace root changed; inspect it again", settings.ErrStale)

			return settingsRefused(p, stale), stale
		}
	}

	return settings.Save(p)
}

// settingsRefused is the result of a save that did not start. The save
// wrote nothing, so Written stays false.
func settingsRefused(p *settings.Plan, err error) settings.Result {
	res := settings.Result{Err: err}

	if p != nil && p.Snapshot != nil {
		res.Path = p.Snapshot.Path
	}

	return res
}
