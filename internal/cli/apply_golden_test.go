package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/store"
)

// These golden tests pin two `lessons --apply` defects that the Phase 3
// trials found. The A0 report records them as the D-1 open items D15
// and D16. Decision D-1 freezes CLI behavior in Phase 3, so the defects
// stay. A fix is a separate decision that updates these goldens in the
// same change. The console must not copy either defect.

// applyGoldenLessons is the pin file before an apply. The hand-written
// entry shows that apply inserts above it and keeps it.
const applyGoldenLessons = `threshold: 3

pin:
  - rule: hand-written
    region: web
    note: A person wrote this pin.
`

// applyGoldenCopyLessons marks the pin file of a copied workspace, so a
// test can prove that apply did not write it.
const applyGoldenCopyLessons = applyGoldenLessons + "# COPY MARKER\n"

// applyGoldenRow returns a pending row of the apply fixtures.
func applyGoldenRow(signature, rule, region, note string) model.Proposal {
	return model.Proposal{Signature: signature, Rule: rule, Region: region, Note: note,
		Members: []int64{1, 2}, Agent: "claude/v6", Status: model.ProposalProposed}
}

// D15: in a copied workspace, apply edits the pin file and reads the
// write gate of the original repository. The ledger write goes to the
// copy. Cause: openIndex replaces the -C root with the repo_root that
// the index metadata stores, and the copy keeps that metadata.
func TestLessonsApplyGoldenCopiedWorkspace(t *testing.T) {
	original := maintainWorkspace(t, nil)
	maintainWriteConfig(t, original, maintainGateOn)
	maintainWriteLessons(t, original, applyGoldenLessons, 0o644)

	goldenSeedProposals(t, original,
		applyGoldenRow("s1", "first-rule", "api", "The first pending pin."),
		applyGoldenRow("s2", "second-rule", "db", "The second pending pin."),
	)

	// os.CopyFS copies the files as `cp -R` does. The index metadata in
	// the copy still names the original root.
	copied := filepath.Join(t.TempDir(), "copy")
	require.NoError(t, os.CopyFS(copied, os.DirFS(original)))
	maintainWriteLessons(t, copied, applyGoldenCopyLessons, 0o644)

	stdout, stderr := goldenRun(t, copied, "lessons", "--apply", "p1")

	assert.Equal(t, "applied 1 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p1    first-rule\n", stdout)
	assert.Empty(t, stderr)

	// Defect: the original file gains the pin, and the file of the copy
	// stays unchanged.
	got, _ := maintainLessons(t, original)
	assert.Equal(t, `threshold: 3

pin:
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p1)
  - rule: first-rule
    region: api
    note: The first pending pin.
  - rule: hand-written
    region: web
    note: A person wrote this pin.
`, got)

	got, _ = maintainLessons(t, copied)
	assert.Equal(t, applyGoldenCopyLessons, got)

	// Defect: the ledger of the copy records the apply, and the ledger
	// of the original does not.
	pending2 := `p2 proposed   "second-rule" region="db" regions=[] triggers=[] checked=0 v0 note="The second pending pin."` + "\n"

	assert.Equal(t, `p1 proposed   "first-rule" region="api" regions=[] triggers=[] checked=0 v0 note="The first pending pin."`+"\n"+
		pending2, maintainLedger(t, original, 0))
	assert.Equal(t, `p1 applied    "first-rule" region="api" regions=[] triggers=[] checked=0 v0 note="The first pending pin."`+"\n"+
		pending2, maintainLedger(t, copied, 0))

	// Defect: the copy turns the write gate off, but apply reads the gate
	// of the original. It writes the original file again and does not
	// print the paste block.
	maintainWriteConfig(t, copied, "distill:\n  write: false\n")

	stdout, stderr = goldenRun(t, copied, "lessons", "--apply", "p2")

	assert.Equal(t, "applied 1 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p2    second-rule\n", stdout)
	assert.Empty(t, stderr)

	got, _ = maintainLessons(t, original)
	assert.Contains(t, got, "(seamark lessons --distill, p2)\n  - rule: second-rule\n")

	got, _ = maintainLessons(t, copied)
	assert.Equal(t, applyGoldenCopyLessons, got)

	assert.Contains(t, maintainLedger(t, copied, 0), `p2 applied    "second-rule"`)
	assert.Contains(t, maintainLedger(t, original, 0), `p2 proposed   "second-rule"`)
}

// D16: apply writes one entry per selected row and does not compare the
// keys of the selection. Two pending rows with one canonical key become
// two entries with that key. The Pins view then classifies both entries
// as duplicate pins and blocks every action on them.
func TestLessonsApplyGoldenDuplicateKeySelection(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o644)

	goldenSeedProposals(t, root,
		applyGoldenRow("s1", "ttl-monotonic-clock", "internal/billing", "Use the monotonic clock for TTLs."),
		applyGoldenRow("s2", "ttl-monotonic-clock", "internal/billing", "Compute expiry from a monotonic reading."),
	)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1,p2")

	assert.Equal(t, "applied 2 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p1    ttl-monotonic-clock\n"+
		"  p2    ttl-monotonic-clock\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, `threshold: 3

pin:
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p1)
  - rule: ttl-monotonic-clock
    region: internal/billing
    note: Use the monotonic clock for TTLs.
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p2)
  - rule: ttl-monotonic-clock
    region: internal/billing
    note: Compute expiry from a monotonic reading.
  - rule: hand-written
    region: web
    note: A person wrote this pin.
`, got)

	assert.Equal(t,
		`p1 applied    "ttl-monotonic-clock" region="internal/billing" regions=[] triggers=[] checked=0 v0 note="Use the monotonic clock for TTLs."`+"\n"+
			`p2 applied    "ttl-monotonic-clock" region="internal/billing" regions=[] triggers=[] checked=0 v0 note="Compute expiry from a monotonic reading."`+"\n",
		maintainLedger(t, root, 0))

	// The consequence in the console: the shared link rules see two live
	// entries with one key, so both entries are ambiguous.
	doc, err := pinfile.Read(root)
	require.NoError(t, err)

	st, err := store.OpenReadOnly(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)

	links := pinfile.Classify(doc, applied, nil, nil)
	require.Len(t, links.Entries, 3)
	assert.Equal(t, pinfile.LinkAmbiguousFile, links.Entries[0].Link)
	assert.Equal(t, pinfile.LinkAmbiguousFile, links.Entries[1].Link)
	assert.Equal(t, pinfile.LinkNoProposal, links.Entries[2].Link)
	assert.Equal(t, pinfile.LinkAmbiguousFile, links.ByProposal[1])
	assert.Equal(t, pinfile.LinkAmbiguousFile, links.ByProposal[2])
}
