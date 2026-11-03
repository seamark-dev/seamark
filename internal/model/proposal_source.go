package model

// This file holds the proposal sources. The store reads and checks the
// source of every proposal row, also of a proposal from review findings.
// So the sources are not in the live proposal vocabulary.

// Proposal sources. A proposal row without a source predates live
// distillation, so it reads as findings.
const (
	ProposalSourceFindings = "findings"
	ProposalSourceLive     = "live"
)

// ValidProposalSource reports whether s names a known proposal source.
// The set is closed, because the finding paths filter on it.
func ValidProposalSource(s string) bool {
	return s == ProposalSourceFindings || s == ProposalSourceLive
}
