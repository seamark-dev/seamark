package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/seamark-dev/seamark/internal/model"
)

// This file holds the store half of a reviewed distillation run. The
// console first plans a run from a read-only store and later executes
// exactly that plan. These methods let the executor bind its writes to
// the planned rows: it deletes only the pending rows that the plan
// listed, and it records a group only when no other writer did so first.

// ErrAlreadyDistilled reports that another writer recorded the evidence
// group after the plan was made. SaveDistilledGroupIfNew then writes
// nothing, so two runs cannot store proposals for one group twice.
var ErrAlreadyDistilled = errors.New("the evidence group is already distilled")

// pendingChunk bounds the bound parameters of one statement. SQLite
// limits their number, and the other IN-list readers use the same size.
const pendingChunk = 500

// PendingProposalsOutside returns the pending proposals whose signature
// is not in live, in ID order. It is the read-only half of
// PruneStaleProposals: a plan lists these rows, so the operator sees
// the cleanup before any row is deleted. It writes nothing.
func (s *Store) PendingProposalsOutside(live map[string]bool) ([]model.Proposal, error) {
	rows, err := s.db.Query(
		`SELECT ` + proposalCols + ` FROM proposal WHERE status = 'proposed' ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	pending, err := scanProposals(rows)
	if err != nil {
		return nil, err
	}

	var out []model.Proposal

	for _, p := range pending {
		if !live[p.Signature] {
			out = append(out, p)
		}
	}

	return out, nil
}

// DeletePendingProposals deletes the listed proposals that are still
// pending and reports how many it deleted. A decided row stays, because
// a decision is memory. A row that another writer created after the plan
// is not in the list, so it stays too. All deletes share one
// transaction, so a failure removes nothing.
func (s *Store) DeletePendingProposals(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: begin pending cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	deleted := 0

	for start := 0; start < len(ids); start += pendingChunk {
		chunk := ids[start:min(start+pendingChunk, len(ids))]
		marks := strings.Repeat(",?", len(chunk))[1:]

		res, err := tx.Exec(
			`DELETE FROM proposal WHERE status = 'proposed' AND id IN (`+marks+`)`,
			int64Args(chunk)...,
		)
		if err != nil {
			return 0, fmt.Errorf("store: delete pending proposals: %w", err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("store: count deleted proposals: %w", err)
		}

		deleted += int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit pending cleanup: %w", err)
	}

	return deleted, nil
}

// SaveDistilledGroupIfNew is SaveDistilledGroup with a guard: it records
// the group only when its signature is not yet in the distilled table.
// The check and the writes share one transaction. When another writer
// recorded the signature first, it writes nothing and returns
// ErrAlreadyDistilled. The legacy SaveDistilledGroup keeps its upsert,
// because the CLI has no plan to protect.
func (s *Store) SaveDistilledGroupIfNew(signature, region string, at int64,
	proposals []model.Proposal,
) ([]model.Proposal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: begin distilled group: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// The mark goes first. Its insert takes the write lock, so no other
	// writer can record the signature between this check and the commit.
	res, err := tx.Exec(
		`INSERT INTO distilled (signature, region, at) VALUES (?, ?, ?)
		 ON CONFLICT (signature) DO NOTHING`,
		signature, region, at,
	)
	if err != nil {
		return nil, fmt.Errorf("store: mark distilled %s: %w", signature, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("store: mark distilled %s: %w", signature, err)
	}

	if n == 0 {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyDistilled, signature)
	}

	for i := range proposals {
		if err := insertProposal(tx, &proposals[i]); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit distilled group: %w", err)
	}

	return proposals, nil
}
