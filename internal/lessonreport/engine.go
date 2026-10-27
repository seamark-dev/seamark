package lessonreport

import (
	"time"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// DefaultMaxAuditBytes is the default read bound of one audit file
// (256 MiB) [product]. A file above the bound reads as "over-limit".
const DefaultMaxAuditBytes int64 = 256 << 20

// Request is what a surface asks of the engine.
type Request struct {
	Root string
	// IdentityRoot is the identity root of this run
	// (cli.hookIdentityRoot). The engine compares it and never
	// substitutes it: as_of.archive.root_equal and
	// coverage.root_mismatch_sessions.
	IdentityRoot string
	// PinRef is "" for the overview. Otherwise it is a pin-ref of the
	// current config.
	PinRef string
	Now    func() time.Time
	Limits Limits
	// SeamarkVersion fills Report.SeamarkVersion. The caller passes
	// cli.version, because lessonreport cannot import cli.
	SeamarkVersion string
	// Applied holds the applied proposals of the ledger. The caller reads
	// them from a read-only ledger, because lessonreport has no store
	// import. LinksFromApplied turns them into Inputs.Links.
	Applied []model.Proposal
}

// Limits bounds what one report reads.
type Limits struct {
	// MaxAuditBytes bounds each audit file. Zero means
	// DefaultMaxAuditBytes. A file above it reads as "over-limit".
	MaxAuditBytes int64
}

// Inputs is everything Build reads. Gather fills it with one read of
// each source.
type Inputs struct {
	Audit      reviews.AuditPrefix           // ReadAuditPrefix: size at open, partial tail ignored
	CheckAudit lessoncheck.AuditPrefix       // the lesson-check stream, same rules
	Config     *reviews.Config               // current lessons.yaml (pin refs, versions, scope)
	ConfigRead ConfigRead                    // how the config read ended
	Checks     *lessoncheck.Config           // nil when absent or invalid
	Capability *lessoncheck.CapabilityReport // milestone P4 input; nil before it or on an inspection failure
	Links      map[string]int64              // pin identity digest → applied proposal ID (LinksFromApplied)
	Now        time.Time

	// AuditError and CheckAuditError carry the error text of a failed
	// ReadAuditPrefix. A failed read returns no prefix, so without these
	// fields Build could not tell an unreadable source from an absent one
	// (lesson error-is-not-absence). Build sanitizes the text.
	AuditError      string
	CheckAuditError string
}

// LinksFromApplied maps the identity digest of each applied proposal's
// pin to its proposal ID. Retargeting reads it: a pin that an applied
// proposal wrote has a proposal to retarget.
//
// The identity is the digest of reviews.ProposalKey, the key that
// every link, liveness check, and prune of the ledger uses. A row that
// is not applied links nothing. When several applied proposals share one
// identity, the newest one (the highest ID) wins, so the map does not
// depend on the order of the list. The result is never nil.
func LinksFromApplied(applied []model.Proposal) map[string]int64 {
	links := make(map[string]int64, len(applied))

	for _, p := range applied {
		if p.Status != model.ProposalApplied {
			continue
		}

		identity := reviews.PinIdentityDigest(reviews.ProposalKey(p))
		if id, ok := links[identity]; !ok || p.ID > id {
			links[identity] = p.ID
		}
	}

	return links
}
