package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// This file holds the store half of console lesson maintenance (retarget
// and prune) and of console trigger extraction. The console plans from a
// read-only store and writes later, after a review or an agent call. In
// that gap the CLI or another console can decide, retarget or delete a
// row. SQLite can also give a deleted row's ID to a new row, because the
// proposal table has no AUTOINCREMENT. So each write here binds to the
// row as the plan saw it. A guard holds the planned values, and the
// write happens only while the row still holds them.
//
// A guarded write reads its rows inside the transaction that writes them
// and compares the decoded values in Go. A SQL comparison of re-encoded
// JSON text would miss a row whose stored text spells the same list
// differently.
//
// The CLI writers (UpdateProposalTriggers, UpdateProposalRegionsIfPending,
// UpdateProposalRegionsBatch, SupersedeProposals) stay unchanged. The CLI
// extraction runs the console executor without its guards. That path
// still writes with UpdateProposalRegionsIfPending and then
// UpdateProposalTriggers, because the CLI goldens pin those two writes.
// ProposalsByIDs stays pending-only, because the console decision flow
// relies on that filter.

// ErrGuard reports that a guarded write found a row that differs from
// the plan. The write then changes no row. GuardError wraps it and names
// the rows.
var ErrGuard = errors.New("ledger rows changed since the plan")

// GuardError names the proposals whose rows missed their guard. A missed
// row is gone, has another status, or holds other identity or region
// values. It wraps ErrGuard, so errors.Is(err, ErrGuard) matches it.
type GuardError struct {
	// IDs lists the proposals that missed, in ascending order.
	IDs []int64
}

// Error names the missed proposals as the console names them (p12).
func (e *GuardError) Error() string {
	names := make([]string, len(e.IDs))

	for i, id := range e.IDs {
		names[i] = "p" + strconv.FormatInt(id, 10)
	}

	return "store: " + ErrGuard.Error() + ": " + strings.Join(names, ", ")
}

// Unwrap returns ErrGuard.
func (e *GuardError) Unwrap() error { return ErrGuard }

// RowGuard is the planned observation of one proposal row. Signature and
// Rule identify the proposal, because an ID alone can name a newer row.
// Status, Region and Regions bind the decision and the delivery scope
// that the plan reviewed. The note and the trigger fields are not part
// of it: maintenance never changes them, and SaveTriggerAnswer binds the
// trigger fields separately.
type RowGuard struct {
	ID                              int64
	Status, Signature, Rule, Region string
	Regions                         []string
}

// NewRowGuard returns the guard that binds a write to row p as it is
// now. It copies Regions, so a later change to p does not change the
// guard.
func NewRowGuard(p model.Proposal) RowGuard {
	return RowGuard{
		ID: p.ID, Status: p.Status, Signature: p.Signature, Rule: p.Rule,
		Region: p.Region, Regions: slices.Clone(p.Regions),
	}
}

// Matches reports whether row p holds exactly the guarded values. The
// region order counts, because the stored order is the delivery order.
// A nil and an empty Regions are equal, because the store writes both as
// the same empty column.
func (g RowGuard) Matches(p model.Proposal) bool {
	return p.ID == g.ID && p.Status == g.Status && p.Signature == g.Signature &&
		p.Rule == g.Rule && p.Region == g.Region && slices.Equal(p.Regions, g.Regions)
}

// RegionChange is one guarded region update of an applied proposal: the
// row must still match Guard, and it then takes Region and Regions.
type RegionChange struct {
	Guard   RowGuard
	Region  string
	Regions []string
}

// TriggerAnswer is one answered trigger question, bound to the row that
// the question was asked for. Guard binds the row. The Prior fields bind
// its trigger stamp as planned, so a second extraction that answered
// first turns this answer into a conflict instead of overwriting it.
type TriggerAnswer struct {
	Guard RowGuard
	// PriorChecked, PriorVersion and PriorPaths are the trigger stamp,
	// the prompt version and the stored paths that the plan read.
	PriorChecked int64
	PriorVersion int
	PriorPaths   []string
	// Paths are the validated trigger paths. An empty list is a negative
	// answer, and the stamp still records it.
	Paths []string
	// CheckedAt and Version are the new stamp and prompt version.
	CheckedAt int64
	Version   int
	// SetRegions asks to write Region and Regions too. Only a pending row
	// takes them: an installed pin changes scope only through a reviewed
	// retarget.
	SetRegions bool
	Region     string
	Regions    []string
}

// ProposalsWithIDs returns the proposals with the given IDs in any
// status, in ascending ID order. A missing ID is left out, and a repeated
// ID gives one row. It reads in chunks, so a long list never exceeds the
// SQLite parameter limit. ProposalsByIDs stays pending-only, because the
// decision flow relies on that filter.
func (s *Store) ProposalsWithIDs(ids []int64) ([]model.Proposal, error) {
	return proposalsWithIDs(context.Background(), s.db, ids)
}

// RetargetApplied updates the regions of applied proposals in one
// transaction. Every row must still be applied and match its guard. When
// any row misses, it changes no row and returns a *GuardError that names
// each missed row.
func (s *Store) RetargetApplied(changes []RegionChange) error {
	if len(changes) == 0 {
		return nil
	}

	guards := make([]RowGuard, len(changes))
	encoded := make([]string, len(changes))

	for i, c := range changes {
		guards[i] = c.Guard

		regions, err := encodeStrings(c.Regions)
		if err != nil {
			return fmt.Errorf("store: encode regions of p%d: %w", c.Guard.ID, err)
		}

		encoded[i] = regions
	}

	if err := checkGuards(guards, model.ProposalApplied); err != nil {
		return err
	}

	return s.writeTx("guarded retarget", func(ctx context.Context, conn *sql.Conn) error {
		if err := matchGuards(ctx, conn, guards); err != nil {
			return err
		}

		for i, c := range changes {
			if err := execOne(ctx, conn, `UPDATE proposal SET region = ?, regions = ? WHERE id = ?`,
				c.Region, encoded[i], c.Guard.ID); err != nil {
				return fmt.Errorf("store: retarget p%d: %w", c.Guard.ID, err)
			}
		}

		return nil
	})
}

// SupersedeExact moves applied proposals to superseded in one
// transaction. Every row must still be applied and match its guard. When
// any row misses, it changes no row and returns a *GuardError that names
// each missed row. SupersedeProposals stays for the CLI, which guards by
// status only.
func (s *Store) SupersedeExact(guards []RowGuard) error {
	if len(guards) == 0 {
		return nil
	}

	if err := checkGuards(guards, model.ProposalApplied); err != nil {
		return err
	}

	return s.writeTx("guarded supersede", func(ctx context.Context, conn *sql.Conn) error {
		if err := matchGuards(ctx, conn, guards); err != nil {
			return err
		}

		for _, g := range guards {
			if err := execOne(ctx, conn, `UPDATE proposal SET status = ? WHERE id = ?`,
				model.ProposalSuperseded, g.ID); err != nil {
				return fmt.Errorf("store: supersede p%d: %w", g.ID, err)
			}
		}

		return nil
	})
}

// SaveTriggerAnswer stores one trigger answer in one transaction. The
// paths, the stamp and, for a pending row with SetRegions, the regions
// form one completion marker: either all are written or none is. It
// writes only while the row matches the guard and still holds the prior
// stamp, and it returns true when it wrote. A row that was decided,
// retargeted, deleted, reused or answered by another writer returns
// false with no write. The guard status must be pending or applied, and
// the new stamp and prompt version must be positive.
func (s *Store) SaveTriggerAnswer(a TriggerAnswer) (bool, error) {
	id := a.Guard.ID

	if len(a.Paths) > model.MaxTriggerPaths {
		return false, fmt.Errorf("store: proposal %d has %d trigger paths; maximum is %d",
			id, len(a.Paths), model.MaxTriggerPaths)
	}

	// The eligibility rules read a stamp of zero or less as "never
	// asked". Such a write would report success but leave the question
	// open, so the next run would pay for it again.
	if a.CheckedAt <= 0 || a.Version <= 0 {
		return false, fmt.Errorf("store: trigger answer for p%d has stamp %d and prompt version %d; both must be positive",
			id, a.CheckedAt, a.Version)
	}

	if a.Guard.Status != model.ProposalProposed && a.Guard.Status != model.ProposalApplied {
		return false, fmt.Errorf("store: trigger answer for p%d expects status %q; it needs %q or %q",
			id, a.Guard.Status, model.ProposalProposed, model.ProposalApplied)
	}

	triggers, err := encodeStrings(a.Paths)
	if err != nil {
		return false, fmt.Errorf("store: encode trigger paths of p%d: %w", id, err)
	}

	regions, err := encodeStrings(a.Regions)
	if err != nil {
		return false, fmt.Errorf("store: encode regions of p%d: %w", id, err)
	}

	saved := false

	err = s.writeTx("trigger answer", func(ctx context.Context, conn *sql.Conn) error {
		rows, err := proposalsWithIDs(ctx, conn, []int64{id})
		if err != nil {
			return err
		}

		if len(rows) == 0 || !a.Guard.Matches(rows[0]) || !a.priorMatches(rows[0]) {
			return nil // a conflict: the row is not the one the question was asked for
		}

		if a.SetRegions && rows[0].Status == model.ProposalProposed {
			err = execOne(ctx, conn, `UPDATE proposal SET trigger_paths = ?, trigger_checked_at = ?,
				trigger_prompt_version = ?, region = ?, regions = ? WHERE id = ?`,
				triggers, a.CheckedAt, a.Version, a.Region, regions, id)
		} else {
			err = execOne(ctx, conn, `UPDATE proposal SET trigger_paths = ?, trigger_checked_at = ?,
				trigger_prompt_version = ? WHERE id = ?`,
				triggers, a.CheckedAt, a.Version, id)
		}

		if err != nil {
			return fmt.Errorf("store: save trigger answer of p%d: %w", id, err)
		}

		saved = true

		return nil
	})
	if err != nil {
		return false, err // a failed commit wrote nothing, whatever fn did
	}

	return saved, nil
}

// priorMatches reports whether row p still holds the trigger stamp that
// the plan read.
func (a TriggerAnswer) priorMatches(p model.Proposal) bool {
	return p.TriggerChecked == a.PriorChecked && p.TriggerPromptVersion == a.PriorVersion &&
		slices.Equal(p.TriggerPaths, a.PriorPaths)
}

// checkGuards rejects a guard list that cannot describe one reviewed
// change: a repeated ID, or a guard for a status other than want. These
// are caller errors, not conflicts, so the error does not wrap ErrGuard.
func checkGuards(guards []RowGuard, want string) error {
	seen := make(map[int64]bool, len(guards))

	for _, g := range guards {
		if seen[g.ID] {
			return fmt.Errorf("store: guarded update lists p%d twice", g.ID)
		}

		seen[g.ID] = true

		if g.Status != want {
			return fmt.Errorf("store: guarded update for p%d expects status %q; it needs %q",
				g.ID, g.Status, want)
		}
	}

	return nil
}

// matchGuards reads the guarded rows through conn and returns a
// *GuardError that names every row that is missing or differs.
func matchGuards(ctx context.Context, conn *sql.Conn, guards []RowGuard) error {
	ids := make([]int64, len(guards))
	for i, g := range guards {
		ids[i] = g.ID
	}

	rows, err := proposalsWithIDs(ctx, conn, ids)
	if err != nil {
		return err
	}

	byID := make(map[int64]model.Proposal, len(rows))
	for _, p := range rows {
		byID[p.ID] = p
	}

	var missed []int64

	for _, g := range guards {
		if p, ok := byID[g.ID]; !ok || !g.Matches(p) {
			missed = append(missed, g.ID)
		}
	}

	if len(missed) == 0 {
		return nil
	}

	slices.Sort(missed)

	return &GuardError{IDs: missed}
}

// contextQuerier is the read slice shared by *sql.DB and *sql.Conn.
type contextQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// proposalsWithIDs reads the rows with the given IDs in any status, in
// ascending ID order. The IDs are sorted and deduplicated first, so the
// chunks come back in order and a repeated ID gives one row.
func proposalsWithIDs(ctx context.Context, q contextQuerier, ids []int64) ([]model.Proposal, error) {
	unique := slices.Clone(ids)
	slices.Sort(unique)
	unique = slices.Compact(unique)

	var out []model.Proposal

	for start := 0; start < len(unique); start += pendingChunk {
		chunk := unique[start:min(start+pendingChunk, len(unique))]
		marks := strings.Repeat(",?", len(chunk))[1:]

		rows, err := q.QueryContext(ctx,
			`SELECT `+proposalCols+` FROM proposal WHERE id IN (`+marks+`) ORDER BY id`,
			int64Args(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("store: read proposals by id: %w", err)
		}

		ps, err := scanProposals(rows)
		_ = rows.Close()

		if err != nil {
			return nil, err // scanProposals names the row and the column
		}

		out = append(out, ps...)
	}

	return out, nil
}

// writeTx runs fn in one transaction that holds the write lock from its
// first statement, and commits when fn returns nil. Any error rolls the
// transaction back. The lock comes first because a guard reads before it
// writes. In WAL mode, a transaction that read first cannot take the
// lock while another writer is active. SQLite then fails at once and
// does not wait for busy_timeout. BEGIN IMMEDIATE waits like every other
// write. database/sql cannot open such a transaction on this DSN, so
// writeTx sends BEGIN, COMMIT and ROLLBACK on one dedicated connection.
//
// fn must use only conn. The store has one connection, and writeTx holds
// it until fn returns. Any s.db call inside fn, an exported reader or a
// nested writeTx included, waits for that connection forever.
func (s *Store) writeTx(what string, fn func(ctx context.Context, conn *sql.Conn) error) error {
	ctx := context.Background()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: begin %s: %w", what, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("store: begin %s: %w", what, err)
	}

	committed := false

	// A failed COMMIT can leave the transaction open (SQLITE_BUSY). The
	// rollback then closes it, so the pooled connection is clean again.
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	if err := fn(ctx, conn); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("store: commit %s: %w", what, err)
	}

	committed = true

	return nil
}

// execOne runs one UPDATE that must change exactly one row. The write
// lock and the guard make another count impossible. The check still
// turns a surprise into an error, so a write is never reported done
// when it changed nothing.
func execOne(ctx context.Context, conn *sql.Conn, query string, args ...any) error {
	res, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	n, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if n != 1 {
		return fmt.Errorf("the update changed %d rows, want 1", n)
	}

	return nil
}
