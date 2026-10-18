package pinfile

// The write. It goes through the setup engine (integration Route A), as
// the settings editor does: one client-less document whose Compose
// returns the candidate only while the file still holds the reviewed
// bytes. The engine then checks the guard again right before the write,
// refuses a link and a file that this user cannot write, keeps the mode,
// and replaces the file by rename, so a failure never leaves a
// half-written file. Prepare writes a retarget or a prune. PrepareAppend
// writes an apply, and it also creates an absent file.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// Prepared is a guarded write of a candidate file, not yet applied.
type Prepared struct {
	// Guard is the observation of the file that the write replaces.
	Guard integration.FileGuard
	// Written is the SHA-256 of the bytes that Apply writes. A later
	// Restore uses it to prove that the file still holds them.
	Written [32]byte

	setup *integration.SetupPlan
}

// Prepare plans the write of after over the file that expect observed.
// It compares only the existence and the SHA-256 of the file with
// expect: the mode and the link state of the moment of the write come
// from the new observation, and the engine checks them itself. A file
// that changed returns ErrStale. A link or a file that this user cannot
// write returns ErrNotWritable. Bytes that the lessons reader rejects
// are refused, because the edit hook would then drop every pin without a
// word. An absent file returns ErrStale: a retarget or a prune edits an
// entry, so it needs the file.
func Prepare(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) (*Prepared, error) {
	if !expect.Exists {
		return nil, fail(ErrStale, "the pin file does not exist")
	}

	return prepare(reg, root, expect, after, "update pins")
}

// PrepareAppend plans the write of a candidate from PlanAppend. It
// follows the rules of Prepare, with one difference: when expect
// observed no file, the write creates it. The first apply of a workspace
// creates the pin file, as the CLI always did. The file must then still
// be absent at the write, so the write never replaces a file that
// another writer created.
func PrepareAppend(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) (*Prepared, error) {
	return prepare(reg, root, expect, after, "add pins")
}

// prepare plans the guarded write of after. The file must still exist
// as expect observed it, with the same SHA-256, or be absent as expect
// observed it.
func prepare(reg *integration.Registry, root string, expect integration.FileGuard, after []byte, detail string) (*Prepared, error) {
	if _, err := reviews.ParseConfig(after); err != nil {
		return nil, fmt.Errorf("%w: the candidate does not load: %w", ErrUnverified, err)
	}

	setup, err := guardedWrite(reg, root, detail, after, func(existing []byte, exists bool) error {
		if exists != expect.Exists || (exists && sha256.Sum256(existing) != expect.SHA256) {
			return fail(ErrStale, "the file changed after the plan")
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Prepared{Guard: setup.Reads[0], Written: sha256.Sum256(after), setup: setup}, nil
}

// Apply writes the prepared bytes. The engine checks the guard once more
// right before the rename; a change since Prepare returns ErrStale and
// writes nothing.
func (p *Prepared) Apply() error {
	if p == nil || p.setup == nil {
		return errors.New("pinfile: no prepared write")
	}

	res, err := integration.ApplySetup(p.setup, integration.ApplyOptions{SkillsLog: io.Discard})

	switch {
	case errors.Is(err, integration.ErrStalePlan):
		return fail(ErrStale, "another writer changed the file before the write")
	case err != nil:
		return err
	}

	for _, op := range res.Ops {
		if op.Path == File && op.Status == integration.OpApplied {
			return nil
		}
	}

	return errors.New("pinfile: the write did not run")
}

// Restore writes original back, but only while the file still holds the
// bytes with the SHA-256 written. Another writer's later edit is never
// overwritten: the restore then returns ErrStale and changes nothing.
// This includes another writer that already put original back (D-12).
//
// Restore returns nil only after the restore write succeeds, because a
// caller reports a nil result as "restored".
func Restore(reg *integration.Registry, root string, written [32]byte, original []byte) error {
	setup, err := guardedWrite(reg, root, "restore pins", original, func(existing []byte, exists bool) error {
		if !exists || sha256.Sum256(existing) != written {
			return fail(ErrStale, "another writer changed the file; nothing restored")
		}

		return nil
	})
	if err != nil {
		return err
	}

	return (&Prepared{setup: setup}).Apply()
}

// guardedWrite builds the setup plan for one write of the pin file.
// check runs inside Compose on the bytes that the plan guard hashes, so
// the check and the guard describe the same file state.
func guardedWrite(reg *integration.Registry, root, detail string, after []byte,
	check func(existing []byte, exists bool) error,
) (*integration.SetupPlan, error) {
	if reg == nil {
		return nil, errors.New("pinfile: no client registry")
	}

	if root == "" {
		return nil, errors.New("pinfile: workspace root is empty")
	}

	// The engine refuses a linked write only after Compose. An early
	// observation gives the caller the plain reason and ErrNotWritable.
	now, _, err := integration.ReadInput(root, File)
	if err != nil {
		return nil, err
	}

	if reason := integration.WriteRefusal(now); reason != "" {
		return nil, fail(ErrNotWritable, reason)
	}

	compose := func(existing []byte, exists bool) ([]byte, error) {
		if err := check(existing, exists); err != nil {
			return nil, err
		}

		return after, nil
	}

	setup, err := integration.PlanSetup(reg, integration.SetupRequest{
		Root: root,
		Common: []integration.Document{{
			Path: File, Compose: compose, Detail: detail, KeptDetail: "no change",
		}},
	})

	switch {
	case errors.Is(err, ErrStale):
		return nil, err
	case errors.Is(err, integration.ErrStalePlan):
		return nil, fail(ErrStale, "the file changed while the write was planned")
	case err != nil:
		return nil, err
	case len(setup.Reads) != 1:
		return nil, fail(ErrStale, "the write plan does not guard the pin file")
	case len(setup.Writes) != 1:
		return nil, fail(ErrNoOp, "the file already holds these bytes")
	}

	// The plan guard is a second observation; it can show a new link or
	// a new mode.
	if reason := integration.WriteRefusal(setup.Reads[0]); reason != "" {
		return nil, fail(ErrNotWritable, reason)
	}

	return setup, nil
}
