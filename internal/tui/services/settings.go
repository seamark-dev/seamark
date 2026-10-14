package services

import (
	"fmt"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The focused settings services. They read and patch the two settings
// documents under the effective root R: the root that the index
// records, else the workspace root. config.yaml holds the agent, the
// distillation, and the index settings. The evidence refresh and the
// distillation read it under R, so a save changes the file that the
// next run reads. lessons.yaml holds the delivery keys and the mutes.
// It is the file that Pins lists, so both editors of lessons.yaml see
// one file (P4-16). The ledger reads use the same root.
//
// The two documents keep their own root rules. A config.yaml save
// refuses only a plan of another root. A lessons save also refuses a
// foreign root, because R then names another repository (P4-16).

// Settings inspects both settings documents under R. It reads the files
// only; it runs no command and writes nothing. When R is a foreign root,
// Settings blocks the lessons part with an error that wraps
// ErrForeignRoot. So Editable names the foreign root for each lessons
// group before a draft starts, and a lessons plan refuses with it. The
// foreign root replaces a block of the file itself, because a fix by
// hand would change the file of another repository. config.yaml keeps
// its blocks.
func (e *engine) Settings() (*settings.Snapshot, error) {
	probe := e.probe()

	snap, err := settings.Inspect(probe.Root, e.reg)
	if err != nil {
		return nil, err
	}

	if foreign := e.foreignRoot(probe); foreign != nil {
		snap.Lessons.Blocked, snap.Lessons.BlockedAt = foreign, 0
	}

	return snap, nil
}

// PlanSettings plans a guarded patch of one group. It writes nothing,
// so it does not take the mutation lock. The screen decides when the
// user can review; it refuses a new review while a mutation runs.
//
// A lessons plan also refuses a foreign root with an error that wraps
// ErrForeignRoot. The snapshot block covers a root that was foreign at
// the inspection. This check covers a root that became foreign later.
func (e *engine) PlanSettings(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
	plan, err := settings.PlanPatch(snap, e.reg, edits)
	if err != nil {
		return nil, err
	}

	if plan.File == pinfile.File {
		if err := e.foreignRoot(e.probe()); err != nil {
			return nil, err
		}
	}

	return plan, nil
}

// SaveSettings writes a reviewed plan under the mutation lock. A lessons
// plan refuses a foreign root first, with an error that wraps
// ErrForeignRoot (P4-16). Every plan then refuses a file that is not
// under the current effective root, because the index can record
// another root after the inspection. A refusal writes nothing and reads
// nothing back (P4-18).
func (e *engine) SaveSettings(p *settings.Plan) (settings.Result, error) {
	if !e.mu.TryLock() {
		return settingsRefused(p, ErrBusy), ErrBusy
	}
	defer e.mu.Unlock()

	if p != nil && p.Snapshot != nil {
		probe := e.probe()

		if p.File == pinfile.File {
			if err := e.foreignRoot(probe); err != nil {
				return settingsRefused(p, err), err
			}
		}

		root, err := filepath.Abs(probe.Root)
		if err != nil || root != p.Snapshot.Root {
			stale := fmt.Errorf("%w: the workspace root changed; inspect it again", settings.ErrStale)

			return settingsRefused(p, stale), stale
		}
	}

	return settings.Save(p)
}

// settingsRefused is the result of a save that did not start. The save
// wrote nothing, so the outcome is NotWritten. Path names the document
// of the plan, so a refused lessons save names lessons.yaml.
func settingsRefused(p *settings.Plan, err error) settings.Result {
	return settings.Result{Outcome: settings.NotWritten, Err: err, Path: p.Path()}
}
