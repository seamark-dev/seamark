package cli

import (
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// hookIdentityRoot returns the identity root of the hook handlers.
// Every session, match, context, and turn digest of a hook record
// hashes over it. The edit and reset records of one receiver then carry
// one digest, also when the index records a root that differs from the
// git top level, for example a path through a symbolic link.
//
// The root is the repository root that the index meta records, else
// the git top level. workspace.Probe reads the meta read-only, so the
// function never migrates or creates a database. Path normalization
// and the lessons configuration keep their own root. The reset hook
// also resets the delivery state at this root, because the edit and
// exploration hooks key that state under the same index-meta root.
func hookIdentityRoot(opts *options) string {
	ws, err := workspace.Locate(opts.workspace, opts.dbPath)
	if err != nil {
		// Locate fails only when its root resolver fails. The same
		// resolver gives the git top level, or "" when it fails again.
		// delivery then hashes over its workspace root instead.
		root, _ := history.ResolveRoot(opts.workspace)

		return root
	}

	// Probe never fails: an absent or unreadable database keeps the
	// workspace root, which is the git top level.
	return workspace.Probe(ws).Root
}
