package model

// This file holds the proposal sources. The store reads and checks the
// source of every proposal row, also of a proposal from review findings.

// Proposal sources. A proposal row without a source predates the source
// column, so it reads as findings. This build creates only findings
// rows. The store and the state import still accept live as a known
// source, so a row that another build wrote keeps its source.
const (
	ProposalSourceFindings = "findings"
	ProposalSourceLive     = "live"
)

// ValidProposalSource reports whether s names a known proposal source.
// The set is closed, because the finding paths filter on it.
func ValidProposalSource(s string) bool {
	return s == ProposalSourceFindings || s == ProposalSourceLive
}
