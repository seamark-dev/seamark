package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/seamark-dev/seamark/internal/model"
)

// StateVersion is the version of the portable state format. Bump only
// with a reader that still accepts every older version.
const StateVersion = 2

// State is the portable durable-state bundle: the proposal decisions and
// paid distillation memory that a rebuild cannot regenerate. Everything
// else in the database is derived and travels by re-indexing.
type State struct {
	Version int `json:"seamark_state_version"`
	// Repo identifies the repository the bundle came from (the root
	// commit id — stable across clones, unlike paths or remotes). Filled
	// and checked by the CLI; empty when unknown (not a git repository).
	Repo      string          `json:"repo,omitempty"`
	Proposals []ProposalState `json:"proposals,omitempty"`
	Distilled []DistilledMark `json:"distilled,omitempty"`
}

// ProposalState is one proposal on the wire. It carries no database id:
// ids are local to a database, while the signature travels — it hashes
// stable finding ids, so the same evidence produces the same signature
// on any machine.
type ProposalState struct {
	Signature string   `json:"signature"`
	Rule      string   `json:"rule"`
	Region    string   `json:"region,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	// TriggerPaths travel with the proposal: they feed region
	// recomputation, and losing them on import would let a retarget
	// silently narrow the imported delivery. The checked stamp rides
	// along so an import does not re-purchase answered questions.
	TriggerPaths         []string `json:"trigger_paths,omitempty"`
	TriggerChecked       int64    `json:"trigger_checked_at,omitempty"`
	TriggerPromptVersion int      `json:"trigger_prompt_version,omitempty"`
	Note                 string   `json:"note"`
	Members              []int64  `json:"members,omitempty"`
	Agent                string   `json:"agent,omitempty"`
	Status               string   `json:"status"`
	CreatedAt            int64    `json:"created_at"`
}

// DistilledMark is one row of distillation memory: evidence sets already
// paid for, never re-sent to an agent.
type DistilledMark struct {
	Signature string `json:"signature"`
	Region    string `json:"region,omitempty"`
	At        int64  `json:"at"`
}

// ImportStats reports what an import actually did.
type ImportStats struct {
	ProposalsAdded   int
	ProposalsUpdated int // pending rows that adopted an imported decision
	ProposalsSkipped int
	// TriggersFilled counts kept-local rows whose unanswered trigger
	// question adopted an imported answer — paid once, on one machine.
	TriggersFilled   int
	DistilledAdded   int
	DistilledSkipped int
}

// ExportState collects the durable subset of the database. Both tables
// are read inside one transaction: SaveDistilledGroup commits proposals
// and their signature mark together, and an export must never split that
// pair — a mark without its proposals would suppress re-distillation
// while losing the paid result.
func (s *Store) ExportState() (*State, error) {
	out := &State{Version: StateVersion}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	rows, err := tx.Query(`SELECT ` + proposalCols + ` FROM proposal ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	proposals, err := scanProposals(rows)
	if err != nil {
		return nil, err
	}

	for _, p := range proposals {
		out.Proposals = append(out.Proposals, ProposalState{
			Signature: p.Signature, Rule: p.Rule, Region: p.Region,
			Regions: p.Regions, TriggerPaths: p.TriggerPaths,
			TriggerChecked: p.TriggerChecked, TriggerPromptVersion: p.TriggerPromptVersion,
			Note: p.Note, Members: p.Members,
			Agent: p.Agent, Status: p.Status, CreatedAt: p.CreatedAt,
		})
	}

	marks, err := tx.Query(`SELECT signature, region, at FROM distilled ORDER BY signature`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = marks.Close() }()

	for marks.Next() {
		var m DistilledMark

		if err := marks.Scan(&m.Signature, &m.Region, &m.At); err != nil {
			return nil, err
		}

		out.Distilled = append(out.Distilled, m)
	}

	if err := marks.Err(); err != nil {
		return nil, err
	}

	return out, tx.Commit()
}

// validStatus are the proposal lifecycles an import accepts.
var validStatus = map[string]bool{
	model.ProposalProposed:   true,
	model.ProposalApplied:    true,
	model.ProposalDismissed:  true,
	model.ProposalSuperseded: true,
}

// ImportState merges a bundle into the database, atomically. Identity is
// (signature, rule). Local rows win one exception: a local row still
// 'proposed' adopts an imported decided status — a decision beats no
// decision, but an existing local decision is never overwritten.
func (s *Store) ImportState(st *State) (ImportStats, error) {
	var stats ImportStats

	if st.Version < 1 || st.Version > StateVersion {
		return stats, fmt.Errorf("store: state version %d not supported (this seamark reads up to v%d)",
			st.Version, StateVersion)
	}

	for _, p := range st.Proposals {
		if p.Signature == "" || p.Rule == "" || !validStatus[p.Status] {
			return stats, fmt.Errorf("store: invalid proposal in import: signature=%q rule=%q status=%q",
				p.Signature, p.Rule, p.Status)
		}

		if len(p.TriggerPaths) > model.MaxTriggerPaths {
			return stats, fmt.Errorf("store: invalid proposal in import: %d trigger paths exceeds limit %d",
				len(p.TriggerPaths), model.MaxTriggerPaths)
		}
	}

	for _, m := range st.Distilled {
		if m.Signature == "" {
			return stats, errors.New("store: invalid distillation mark in import: empty signature")
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return stats, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	for _, p := range st.Proposals {
		// State v1 predates explicit question provenance. Preserve its
		// positive answers, while keeping negative ones eligible for the
		// corrected question through the legacy version.
		if p.TriggerChecked > 0 && p.TriggerPromptVersion == 0 {
			p.TriggerPromptVersion = 1
		}

		var local string

		err := tx.QueryRow(`SELECT status FROM proposal WHERE signature = ? AND rule = ?`,
			p.Signature, p.Rule).Scan(&local)

		switch {
		case err != nil && err != sql.ErrNoRows:
			return stats, err
		case err == nil && local == model.ProposalProposed && p.Status != model.ProposalProposed:
			// The identity fields travel with the decision — status plus
			// region, regions, and trigger paths: the user decided
			// against the imported content, and a local row keeping its
			// own (possibly repo-wide) region would desynchronize pin
			// identity from the lessons.yaml the same bundle's apply
			// wrote. Triggers ride along so a later retarget cannot
			// silently narrow the adopted delivery.
			regions, err := encodeStrings(p.Regions)
			if err != nil {
				return stats, err
			}

			// Trigger fields adopt only when the import ANSWERED the
			// question: an unanswered imported row must not wipe an
			// answer this machine already paid for.
			if p.TriggerChecked > 0 {
				triggers, err := encodeStrings(p.TriggerPaths)
				if err != nil {
					return stats, err
				}

				if _, err := tx.Exec(
					`UPDATE proposal SET status = ?, region = ?, regions = ?,
					   trigger_paths = ?, trigger_checked_at = ?, trigger_prompt_version = ?
					 WHERE signature = ? AND rule = ?`,
					p.Status, p.Region, regions, triggers, p.TriggerChecked,
					p.TriggerPromptVersion, p.Signature, p.Rule,
				); err != nil {
					return stats, err
				}
			} else if _, err := tx.Exec(
				`UPDATE proposal SET status = ?, region = ?, regions = ?
				 WHERE signature = ? AND rule = ?`,
				p.Status, p.Region, regions, p.Signature, p.Rule,
			); err != nil {
				return stats, err
			}

			stats.ProposalsUpdated++
		case err == nil:
			// Kept local — with one exception riding along: an imported
			// ANSWERED trigger question fills a local row that never
			// asked, so no machine re-pays for it. A local answer is
			// never overwritten.
			if p.TriggerChecked > 0 {
				triggers, err := encodeStrings(p.TriggerPaths)
				if err != nil {
					return stats, err
				}

				res, err := tx.Exec(
					`UPDATE proposal SET trigger_paths = ?, trigger_checked_at = ?,
					   trigger_prompt_version = ?
					 WHERE signature = ? AND rule = ? AND trigger_paths = ''
					   AND trigger_prompt_version < ?`,
					triggers, p.TriggerChecked, p.TriggerPromptVersion,
					p.Signature, p.Rule, p.TriggerPromptVersion,
				)
				if err != nil {
					return stats, err
				}

				if n, err := res.RowsAffected(); err == nil && n > 0 {
					stats.TriggersFilled++
				}
			}

			stats.ProposalsSkipped++
		default:
			// One insert path for proposals everywhere: a column added
			// to the table must not silently go missing from imports.
			row := model.Proposal{
				Signature: p.Signature, Rule: p.Rule, Region: p.Region,
				Regions: p.Regions, TriggerPaths: p.TriggerPaths,
				TriggerChecked: p.TriggerChecked, TriggerPromptVersion: p.TriggerPromptVersion,
				Note: p.Note, Members: p.Members,
				Agent: p.Agent, Status: p.Status, CreatedAt: p.CreatedAt,
			}

			if err := insertProposal(tx, &row); err != nil {
				return stats, err
			}

			stats.ProposalsAdded++
		}
	}

	for _, m := range st.Distilled {
		res, err := tx.Exec(`INSERT INTO distilled (signature, region, at) VALUES (?, ?, ?)
			 ON CONFLICT (signature) DO NOTHING`, m.Signature, m.Region, m.At)
		if err != nil {
			return stats, err
		}

		if n, err := res.RowsAffected(); err == nil && n > 0 {
			stats.DistilledAdded++
		} else {
			stats.DistilledSkipped++
		}
	}

	if err := tx.Commit(); err != nil {
		return stats, err
	}

	return stats, nil
}
