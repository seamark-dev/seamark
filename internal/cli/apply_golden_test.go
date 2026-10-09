package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// These golden tests pin `lessons --apply` and `lessons --dismiss`. Both
// run through the shared decision flow of internal/proposals, as the
// console does. The Phase 3 trials found two apply defects, which the
// A0 report records as D15 and D16. D16 is fixed in the shared flow: a
// selection with a repeated key is refused, and its golden pins that.
// A proposal with the key of an applied proposal is refused for the same
// reason, and TestLessonsApplyGoldenAppliedKey pins that.
// D15 stays open: the CLI keeps its root resolution, and its golden pins
// the defect until a decision.

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

// D16: two rows with one key used to write two entries with that key
// and print "applied 2 pin(s)". The Pins view then called both entries
// duplicate pins and blocked every action on them. One entry for both
// rows does not help: two applied rows record it, so Pins reads it as
// ambiguous and blocks it too. By reviewer decision, the end state is a
// refusal: apply names the rows and changes nothing. In a shell, the
// binary prints "seamark: " and the error text on stderr and exits 1.
func TestLessonsApplyGoldenDuplicateKeySelection(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o644)

	goldenSeedProposals(t, root,
		applyGoldenRow("s1", "ttl-monotonic-clock", "internal/billing", "Use the monotonic clock for TTLs."),
		applyGoldenRow("s2", "ttl-monotonic-clock", "internal/billing", "Compute expiry from a monotonic reading."),
	)

	before := maintainLedger(t, root, 0)

	stdout, stderr, err := runErr(t, "-C", root, "lessons", "--apply", "p1,p2")
	require.ErrorIs(t, err, proposals.ErrSharedKey)
	assert.NotErrorIs(t, err, gate.ErrBlocked, "exit code 1, not the policy exit code 2")
	assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenLessons, got, "the pin file is unchanged")
	assert.Equal(t, before, maintainLedger(t, root, 0), "both proposals stay pending")

	// The advised next step gives one entry that one applied row links, so
	// Pins allows retarget, prune, and trigger extraction on it.
	goldenRun(t, root, "lessons", "--apply", "p1")
	goldenRun(t, root, "lessons", "--dismiss", "p2")

	doc, err := pinfile.Read(root)
	require.NoError(t, err)

	st, err := store.OpenReadOnly(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)

	links := pinfile.Classify(doc, applied, nil, nil)
	require.Len(t, links.Entries, 2)
	assert.Equal(t, pinfile.LinkLinked, links.Entries[0].Link)
	assert.Equal(t, pinfile.LinkNoProposal, links.Entries[1].Link)
	assert.Equal(t, pinfile.LinkLinked, links.ByProposal[1])
}

// D16 with the gate off: the paste block used to hold one entry for
// each selected row, so a paste duplicated the key. The gate-off apply
// now gives the same refusal as the gate-on apply and prints no block.
func TestLessonsApplyGoldenDuplicateKeyBlock(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o644)

	goldenSeedProposals(t, root,
		applyGoldenRow("s1", "ttl-monotonic-clock", "internal/billing", "Use the monotonic clock for TTLs."),
		applyGoldenRow("s2", "ttl-monotonic-clock", "internal/billing", "Compute expiry from a monotonic reading."),
	)

	before := maintainLedger(t, root, 0)

	stdout, stderr, err := runErr(t, "-C", root, "lessons", "--apply", "p1,p2")
	require.ErrorIs(t, err, proposals.ErrSharedKey)
	assert.NotErrorIs(t, err, gate.ErrBlocked, "exit code 1, not the policy exit code 2")
	assert.Equal(t, "p1 and p2 share rule and regions; apply one and dismiss the other", err.Error())
	assert.Empty(t, stdout, "no block to paste")
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenLessons, got)
	assert.Equal(t, before, maintainLedger(t, root, 0))
}

// F1: a row whose stored region fields disagree used to get its pin.
// Its apply writes the rendered regions, here a repository-wide pin,
// but every reader links the row by its stored fields, here api. So no
// reader links that pin to the row, and Pins reads the row as
// "ambiguous row". By reviewer decision, apply refuses such a row with
// the gate on or off, before it prints or writes anything.
func TestLessonsApplyGoldenDisagreeingRegionFields(t *testing.T) {
	for _, gateCfg := range []string{"distill:\n  write: false\n", maintainGateOn} {
		t.Run(strings.TrimSpace(gateCfg), func(t *testing.T) {
			root := maintainWorkspace(t, nil)
			maintainWriteConfig(t, root, gateCfg)
			maintainWriteLessons(t, root, applyGoldenLessons, 0o644)

			odd := applyGoldenRow("s1", "odd-fields", "api", "The stored fields disagree.")
			odd.Regions = []string{"*"}

			goldenSeedProposals(t, root, odd,
				applyGoldenRow("s2", "second-rule", "db", "The second pending pin."))

			before := maintainLedger(t, root, 0)

			stdout, stderr, err := runErr(t, "-C", root, "lessons", "--apply", "p1,p2")
			require.ErrorIs(t, err, proposals.ErrFieldsDisagree)
			assert.NotErrorIs(t, err, gate.ErrBlocked, "exit code 1, not the policy exit code 2")
			assert.Equal(t, "the stored region fields of the selected proposals disagree: p1; "+
				"resolve them by hand or dismiss them", err.Error())
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)

			got, _ := maintainLessons(t, root)
			assert.Equal(t, applyGoldenLessons, got, "the pin file is unchanged")
			assert.Equal(t, before, maintainLedger(t, root, 0), "both proposals stay pending")
		})
	}
}

// applyGoldenTwoRows seeds two pending rows with different keys, the
// common case that the move to the shared decision flow keeps unchanged.
func applyGoldenTwoRows(t *testing.T, root string) {
	t.Helper()

	goldenSeedProposals(t, root,
		applyGoldenRow("s1", "first-rule", "api", "The first pending pin."),
		applyGoldenRow("s2", "second-rule", "db", "The second pending pin."),
	)
}

// applyGoldenBlock is the pin block of applyGoldenTwoRows.
const applyGoldenBlock = `  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p1)
  - rule: first-rule
    region: api
    note: The first pending pin.
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p2)
  - rule: second-rule
    region: db
    note: The second pending pin.
`

// applyGoldenGateOff is the gate-off output of applyGoldenTwoRows.
const applyGoldenGateOff = "distill.write is off in .seamark/config.yaml — paste under `pin:` in .seamark/lessons.yaml:\n\n" +
	applyGoldenBlock + "\n(proposals stay pending; enable distill.write to let apply edit the file)\n"

// The gate-off output, the file, and the ledger stay byte-identical
// when apply runs through the shared decision flow.
func TestLessonsApplyGoldenGateOffPrintsTheBlock(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o644)
	applyGoldenTwoRows(t, root)

	before := maintainLedger(t, root, 0)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1..p2")

	assert.Equal(t, applyGoldenGateOff, stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenLessons, got, "the gate-off apply writes nothing")
	assert.Equal(t, before, maintainLedger(t, root, 0), "the proposals stay pending")
}

// The gate-on output, the file bytes, its mode, and the ledger stay
// byte-identical when apply runs through the shared decision flow.
func TestLessonsApplyGoldenGateOnWritesAndRecords(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o600)
	applyGoldenTwoRows(t, root)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p2,p1")

	assert.Equal(t, "applied 2 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p1    first-rule\n"+
		"  p2    second-rule\n", stdout)
	assert.Empty(t, stderr)

	got, mode := maintainLessons(t, root)
	assert.Equal(t, "threshold: 3\n\npin:\n"+applyGoldenBlock+
		"  - rule: hand-written\n    region: web\n    note: A person wrote this pin.\n", got)
	assert.Equal(t, os.FileMode(0o600), mode, "the write keeps the file mode")

	assert.Equal(t,
		`p1 applied    "first-rule" region="api" regions=[] triggers=[] checked=0 v0 note="The first pending pin."`+"\n"+
			`p2 applied    "second-rule" region="db" regions=[] triggers=[] checked=0 v0 note="The second pending pin."`+"\n",
		maintainLedger(t, root, 0))
}

// The dismiss output and the ledger stay byte-identical when dismiss
// runs through the shared decision flow. The pin file never changes.
func TestLessonsDismissGolden(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenLessons, 0o644)
	applyGoldenTwoRows(t, root)

	stdout, stderr := goldenRun(t, root, "lessons", "--dismiss", "p1..p9")

	assert.Equal(t, "dismissed 2 proposal(s) — remembered; the same evidence will not return\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenLessons, got)
	assert.Equal(t,
		`p1 dismissed  "first-rule" region="api" regions=[] triggers=[] checked=0 v0 note="The first pending pin."`+"\n"+
			`p2 dismissed  "second-rule" region="db" regions=[] triggers=[] checked=0 v0 note="The second pending pin."`+"\n",
		maintainLedger(t, root, 0))

	// The selection errors come from the CLI selection grammar, before
	// any decision starts. The trailing comma is a pinned quirk (A0 §7).
	for args, want := range map[string]string{
		"--apply=p1":      "p1 is not pending (see `seamark lessons --proposals`)",
		"--dismiss=p1..3": `nothing in "p1..3," is pending (see ` + "`seamark lessons --proposals`)",
	} {
		_, _, err := runErr(t, "-C", root, "lessons", args)
		require.Error(t, err, args)
		assert.Equal(t, want, err.Error(), args)
	}
}

// applyGoldenPinnedLessons already holds the pin of first-rule @ api,
// with a hand-edited note. Apply must not add a second entry for it.
const applyGoldenPinnedLessons = applyGoldenLessons + `  - rule: first-rule
    region: api
    note: Already here.
`

// Existing pin, gate on (fixed): apply used to append a second
// first-rule entry, because it did not check the keys that the file
// already held. The shared flow records p1 as applied without a write,
// and writes only p2.
func TestLessonsApplyGoldenExistingPinIsRecorded(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenPinnedLessons, 0o644)
	applyGoldenTwoRows(t, root)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1,p2")

	assert.Equal(t, "applied 1 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
		"  p2    second-rule\n"+
		"recorded 1 proposal(s) as applied without a new pin — .seamark/lessons.yaml already holds a pin "+
		"with the same rule and regions\n"+
		"  p1    first-rule\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, `threshold: 3

pin:
  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p2)
  - rule: second-rule
    region: db
    note: The second pending pin.
  - rule: hand-written
    region: web
    note: A person wrote this pin.
  - rule: first-rule
    region: api
    note: Already here.
`, got, "the hand-edited entry keeps its note, and no second first-rule entry appears")

	assert.Equal(t,
		`p1 applied    "first-rule" region="api" regions=[] triggers=[] checked=0 v0 note="The first pending pin."`+"\n"+
			`p2 applied    "second-rule" region="db" regions=[] triggers=[] checked=0 v0 note="The second pending pin."`+"\n",
		maintainLedger(t, root, 0))
}

// Every selected pin exists, gate on: the apply writes only the ledger.
// This is also the rerun after a ledger failure that followed the pin
// write: it records the pins without a second entry.
func TestLessonsApplyGoldenOnlyExistingPins(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenPinnedLessons, 0o644)
	applyGoldenTwoRows(t, root)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1")

	assert.Equal(t, "recorded 1 proposal(s) as applied without a new pin — .seamark/lessons.yaml already holds a pin "+
		"with the same rule and regions\n"+
		"  p1    first-rule\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenPinnedLessons, got)
	assert.Contains(t, maintainLedger(t, root, 0), `p1 applied    "first-rule"`)
	assert.Contains(t, maintainLedger(t, root, 0), `p2 proposed   "second-rule"`)
}

// Existing pins, gate off (fixed): the paste block used to hold them
// too, so a paste duplicated them. The block now leaves them out and
// names them. The gate-off apply still writes nothing.
func TestLessonsApplyGoldenExistingPinGateOff(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteLessons(t, root, applyGoldenPinnedLessons, 0o644)
	applyGoldenTwoRows(t, root)

	before := maintainLedger(t, root, 0)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1,p2")

	assert.Equal(t, "distill.write is off in .seamark/config.yaml — paste under `pin:` in .seamark/lessons.yaml:\n\n"+
		"  # distilled by claude/v6 from 2 findings (seamark lessons --distill, p2)\n"+
		"  - rule: second-rule\n"+
		"    region: db\n"+
		"    note: The second pending pin.\n"+
		"\n(proposals stay pending; enable distill.write to let apply edit the file)\n"+
		"(p1 left out: .seamark/lessons.yaml already holds a pin with the same rule and regions)\n", stdout)
	assert.Empty(t, stderr)

	stdout, stderr = goldenRun(t, root, "lessons", "--apply", "p1")

	assert.Equal(t, ".seamark/lessons.yaml already holds the pins of p1 — nothing to paste\n"+
		"(proposals stay pending; enable distill.write to let apply record them as applied)\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenPinnedLessons, got)
	assert.Equal(t, before, maintainLedger(t, root, 0))
}

// applyGoldenAppliedLessons holds the pin of the applied p1 of
// applyGoldenAppliedKey after the hand-written entry.
const applyGoldenAppliedLessons = applyGoldenLessons + `  - rule: first-rule
    region: api
    note: The applied pin.
`

// applyGoldenAppliedKey seeds applied p1 (first-rule @ api) and two
// pending rows. p2 has the rule and regions of p1, as a later
// distillation of new evidence can give. p3 has the key of the
// hand-written entry, which no applied row records.
func applyGoldenAppliedKey(t *testing.T, root string) {
	t.Helper()

	applied := applyGoldenRow("s1", "first-rule", "api", "The applied pin.")
	applied.Status = model.ProposalApplied

	goldenSeedProposals(t, root, applied,
		applyGoldenRow("s2", "first-rule", "api", "A later reading of the same rule."),
		applyGoldenRow("s3", "hand-written", "web", "A proposal for the hand-written pin."),
	)
}

// Applied key (fixed): p2 has the rule and regions of applied p1. With
// the pin in the file, apply used to record p2 as applied next to p1
// (gate on) or to print that the file already held its pin (gate off).
// Without the pin, it wrote or printed a pin that both rows record. Pins
// blocks a pin that two applied proposals record, so by reviewer
// decision apply refuses p2 with the gate on or off. The refusal wins
// over the record of an existing pin, so p3 waits too.
func TestLessonsApplyGoldenAppliedKey(t *testing.T) {
	tests := []struct {
		name, gateCfg, lessons string
	}{
		{name: "gate off", gateCfg: "distill:\n  write: false\n", lessons: applyGoldenAppliedLessons},
		{name: "gate on", gateCfg: maintainGateOn, lessons: applyGoldenAppliedLessons},
		{name: "gate off, the pin of p1 left the file", gateCfg: "distill:\n  write: false\n", lessons: applyGoldenLessons},
		{name: "gate on, the pin of p1 left the file", gateCfg: maintainGateOn, lessons: applyGoldenLessons},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := maintainWorkspace(t, nil)
			maintainWriteConfig(t, root, tt.gateCfg)
			maintainWriteLessons(t, root, tt.lessons, 0o644)
			applyGoldenAppliedKey(t, root)

			before := maintainLedger(t, root, 0)

			stdout, stderr, err := runErr(t, "-C", root, "lessons", "--apply", "p2,p3")
			require.ErrorIs(t, err, proposals.ErrAppliedKey)
			assert.NotErrorIs(t, err, gate.ErrBlocked, "exit code 1, not the policy exit code 2")
			assert.Equal(t, "p1 is already applied with this rule and regions; dismiss p2", err.Error())
			assert.Empty(t, stdout, "no block to paste and no applied line")
			assert.Empty(t, stderr)

			got, _ := maintainLessons(t, root)
			assert.Equal(t, tt.lessons, got, "the pin file is unchanged")
			assert.Equal(t, before, maintainLedger(t, root, 0), "p2 and p3 stay pending")
		})
	}
}

// The advised next step of the applied key refusal: dismiss p2. A pin
// that no applied row records keeps the old handling, so the apply of
// p3 then records it as applied without a write. Each entry ends with
// one applied row, so Pins links both.
func TestLessonsApplyGoldenAppliedKeyNextStep(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenAppliedLessons, 0o644)
	applyGoldenAppliedKey(t, root)

	goldenRun(t, root, "lessons", "--dismiss", "p2")

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p3")

	assert.Equal(t, "recorded 1 proposal(s) as applied without a new pin — .seamark/lessons.yaml already holds a pin "+
		"with the same rule and regions\n"+
		"  p3    hand-written\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, applyGoldenAppliedLessons, got)

	doc, err := pinfile.Read(root)
	require.NoError(t, err)

	st, err := store.OpenReadOnly(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)

	links := pinfile.Classify(doc, applied, nil, nil)
	require.Len(t, links.Entries, 2)
	assert.Equal(t, pinfile.LinkLinked, links.Entries[0].Link, "hand-written links p3")
	assert.Equal(t, pinfile.LinkLinked, links.Entries[1].Link, "first-rule links p1")
}

// The shared flow reads the pin file strictly, as the console does. A
// symbolic link at the pin file path used to receive the gate-on write.
// A malformed pin file used to get the block or a write. Both now stop
// the apply before any change.
func TestLessonsApplyGoldenRefusesUnsafePinFiles(t *testing.T) {
	t.Run("symbolic link with the gate on", func(t *testing.T) {
		root := maintainWorkspace(t, nil)
		maintainWriteConfig(t, root, maintainGateOn)
		applyGoldenTwoRows(t, root)

		elsewhere := filepath.Join(t.TempDir(), "lessons.yaml")
		require.NoError(t, os.WriteFile(elsewhere, []byte(applyGoldenLessons), 0o644))
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, ".seamark", "lessons.yaml")))

		before := maintainLedger(t, root, 0)

		stdout, _, err := runErr(t, "-C", root, "lessons", "--apply", "p1")
		require.Error(t, err)
		assert.Equal(t, ".seamark/lessons.yaml: symlink at .seamark/lessons.yaml; seamark writes only real "+
			"paths inside the repository", err.Error())
		assert.Empty(t, stdout)

		data, err := os.ReadFile(elsewhere)
		require.NoError(t, err)
		assert.Equal(t, applyGoldenLessons, string(data), "the link target is unchanged")
		assert.Equal(t, before, maintainLedger(t, root, 0))
	})

	for _, gate := range []string{"distill:\n  write: false\n", maintainGateOn} {
		t.Run("malformed pin file, "+strings.TrimSpace(gate), func(t *testing.T) {
			root := maintainWorkspace(t, nil)
			maintainWriteConfig(t, root, gate)
			maintainWriteLessons(t, root, "hook_delivery: sometimes\n", 0o644)
			applyGoldenTwoRows(t, root)

			stdout, _, err := runErr(t, "-C", root, "lessons", "--apply", "p1")
			require.ErrorIs(t, err, reviews.ErrLessonsConfig)
			assert.Equal(t, `lessons config: hook_delivery must be "always" or "once-per-context", got "sometimes"`,
				err.Error())
			assert.Empty(t, stdout)

			got, _ := maintainLessons(t, root)
			assert.Equal(t, "hook_delivery: sometimes\n", got)
		})
	}
}

// applyGoldenFirstEntry is the pin block of p1 in applyGoldenTwoRows,
// with pad in place of the indentation of the list.
func applyGoldenFirstEntry(pad string) string {
	return pad + "# distilled by claude/v6 from 2 findings (seamark lessons --distill, p1)\n" +
		pad + "- rule: first-rule\n" +
		pad + "  region: api\n" +
		pad + "  note: The first pending pin.\n"
}

// The gate-on apply writes through the pin file editor of the console
// (pinfile.PlanAppend and the guarded write). The common layouts keep
// their bytes; the goldens above pin them. These layouts changed. Each
// case names what the old CLI writer, distill.ApplyPins, did before.
// That writer is deleted now; git history keeps it.
func TestLessonsApplyGoldenPinFileLayouts(t *testing.T) {
	const (
		hand = "  - rule: hand-written\n    region: web\n    note: A person wrote this pin.\n"
		deep = "    - rule: hand-written\n      region: web\n      note: A person wrote this pin.\n"
	)

	applied := "applied 1 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n  p1    first-rule\n"

	tests := []struct {
		name    string
		before  string
		absent  bool
		mode    os.FileMode
		wantErr string
		want    string
	}{
		{
			// Unchanged: the first apply creates the file with a header.
			name:   "absent pin file",
			absent: true,
			want:   "# Review-lesson tuning (see `seamark lessons --list`).\n\npin:\n" + applyGoldenFirstEntry("  "),
		},
		{
			// Unchanged: the refusal keeps its text.
			name:    "flow list",
			before:  "pin: []\n",
			wantErr: "lessons.yaml has a flow-style pin section; add the entries by hand",
		},
		{
			// Before: "open <root>/.seamark/lessons.yaml: permission denied"
			// from os.WriteFile.
			name:    "read-only pin file",
			before:  applyGoldenLessons,
			mode:    0o444,
			wantErr: "the pin file cannot be replaced: the file is read-only (-r--r--r--); seamark does not replace it",
		},
		{
			// Before: LF lines went into the CRLF file, and Pins then
			// blocked the file for its mixed line breaks.
			name:   "CRLF pin file",
			before: strings.ReplaceAll(applyGoldenLessons, "\n", "\r\n"),
			want:   strings.ReplaceAll("threshold: 3\n\npin:\n"+applyGoldenFirstEntry("  ")+hand, "\n", "\r\n"),
		},
		{
			// Before: "refusing to write: edited lessons.yaml would not
			// parse: yaml: line 4: did not find expected key". The
			// two-space entries did not fit the list.
			name:   "list at column 4",
			before: "threshold: 3\n\npin:\n" + deep,
			want:   "threshold: 3\n\npin:\n" + applyGoldenFirstEntry("    ") + deep,
		},
		{
			// Before: "lessons.yaml has a flow-style pin section; add the
			// entries by hand". A comment is not a flow list.
			name:   "comment on the pin line",
			before: "pin: # my pins\n" + hand,
			want:   "pin: # my pins\n" + applyGoldenFirstEntry("  ") + hand,
		},
		{
			// Before: the new section went after the end marker. No reader
			// read it, but the ledger recorded p1 as applied.
			name:   "document end marker",
			before: "threshold: 2\n...\n",
			want:   "threshold: 2\n\npin:\n" + applyGoldenFirstEntry("  ") + "...\n",
		},
		{
			// Before: a second pin key at column 0 after the indented
			// mapping. No reader read it, but the ledger recorded p1.
			name:   "indented top level",
			before: "  threshold: 3\n  pin:\n" + deep,
			want:   "  threshold: 3\n  pin:\n" + applyGoldenFirstEntry("    ") + deep,
		},
		{
			// Before: the new section went into the second document. No
			// reader read it, but the ledger recorded p1 as applied.
			name:   "two documents",
			before: "threshold: 2\n---\nmute: []\n",
			wantErr: "lessons.yaml cannot take the new pins safely: the file holds more than one YAML document " +
				"(line 2); add the entries by hand",
		},
		{
			// Before: the apply added LF lines to the mixed file.
			name:    "mixed line breaks",
			before:  "threshold: 3\r\npin:\n" + hand,
			wantErr: "lessons.yaml cannot take the new pins safely: the file mixes CRLF and LF line breaks; add the entries by hand",
		},
		{
			// Before: "lessons.yaml has a flow-style pin section; add the
			// entries by hand", which named the wrong layout.
			name:    "null token",
			before:  "threshold: 3\npin: ~\n",
			wantErr: "lessons.yaml has a pin section that is not a plain block list; add the entries by hand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := maintainWorkspace(t, nil)
			maintainWriteConfig(t, root, maintainGateOn)

			mode := tt.mode
			if mode == 0 {
				mode = 0o644
			}

			if !tt.absent {
				maintainWriteLessons(t, root, tt.before, mode)
			}

			applyGoldenTwoRows(t, root)

			stdout, stderr, err := runErr(t, "-C", root, "lessons", "--apply", "p1")
			assert.Empty(t, stderr)

			got, gotMode := maintainLessons(t, root)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.NotErrorIs(t, err, gate.ErrBlocked, "exit code 1, not the policy exit code 2")
				assert.Equal(t, tt.wantErr, err.Error())
				assert.Empty(t, stdout)
				assert.Equal(t, tt.before, got, "the pin file is unchanged")
				assert.Contains(t, maintainLedger(t, root, 0), `p1 proposed   "first-rule"`)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, applied, stdout)
			assert.Equal(t, tt.want, got)
			assert.Contains(t, maintainLedger(t, root, 0), `p1 applied    "first-rule"`)

			if !tt.absent {
				assert.Equal(t, mode, gotMode, "the write keeps the file mode")
			}
		})
	}
}

func TestLessonsApplyFailureMapsTheCommitOutcome(t *testing.T) {
	busy := errors.New("database is locked")

	tests := []struct {
		name   string
		res    proposals.Result
		pinned []int64
		want   string
	}{
		{
			name: "success",
			res:  proposals.Result{FileWritten: true, Moved: []int64{1}},
		},
		{
			name: "race: a row decided elsewhere is not an error, as before the shared flow",
			res:  proposals.Result{FileWritten: true, Moved: []int64{1}, NotMoved: []int64{2}},
		},
		{
			name: "pin write refused before a change keeps the writer text",
			res:  proposals.Result{FileErr: errors.New("lessons.yaml has a flow-style pin section"), NotMoved: []int64{1}},
			want: "lessons.yaml has a flow-style pin section",
		},
		{
			name: "pin write failed after a change",
			res: proposals.Result{FileWritten: true, FileErr: errors.New("short write"),
				NotMoved: []int64{1}},
			want: "short write — .seamark/lessons.yaml may have changed; check it with git diff before you apply again",
		},
		{
			name: "ledger failure after the pin write names the rerun",
			res: proposals.Result{FileWritten: true, Moved: []int64{1},
				NotMoved: []int64{2, 3}, LedgerErr: fmt.Errorf("record p2 as applied: %w", busy)},
			want: "the pins are in .seamark/lessons.yaml, but the ledger update failed: record p2 as applied: " +
				"database is locked — run `seamark lessons --apply p2,p3` again to record them as applied " +
				"without a second pin",
		},
		{
			name: "the rerun also names the existing pins that the failure left unrecorded",
			res: proposals.Result{FileWritten: true, NotMoved: []int64{4},
				LedgerErr: fmt.Errorf("record p4 as applied: %w", busy)},
			pinned: []int64{5, 1},
			want: "the pins are in .seamark/lessons.yaml, but the ledger update failed: record p4 as applied: " +
				"database is locked — run `seamark lessons --apply p1,p4,p5` again to record them as applied " +
				"without a second pin",
		},
		{
			name:   "a refused pin write needs no rerun list",
			res:    proposals.Result{FileErr: errors.New("lessons.yaml has a flow-style pin section"), NotMoved: []int64{2}},
			pinned: []int64{1},
			want:   "lessons.yaml has a flow-style pin section",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applyFailure(tt.res, tt.pinned)
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// statusFailingLedger fails every status write: the injected ledger
// failure after a successful pin write.
type statusFailingLedger struct {
	proposals.Ledgerer
	err error
}

func (l statusFailingLedger) SetProposalStatus([]int64, string) (int, error) {
	return 0, l.err
}

// A mixed gate-on apply writes the new pins first and records the
// existing pins after that. A ledger failure after the write skips the
// record, so the rerun advice names both groups. The advised rerun then
// records both proposals without a second entry.
func TestLessonsApplyLedgerFailureNamesTheExistingPins(t *testing.T) {
	root := maintainWorkspace(t, nil)
	maintainWriteConfig(t, root, maintainGateOn)
	maintainWriteLessons(t, root, applyGoldenPinnedLessons, 0o644)
	applyGoldenTwoRows(t, root)

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)

	pv, err := proposals.PreviewApply(st, root, []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, pv.AlreadyPinned, "p1 has its pin, p2 is new")

	busy := errors.New("database is locked")

	var out strings.Builder

	err = applyPins(&out, statusFailingLedger{Ledgerer: st, err: busy}, root, pv)
	require.ErrorIs(t, err, busy)
	assert.Equal(t, "the pins are in .seamark/lessons.yaml, but the ledger update failed: record p2 as applied: "+
		"database is locked — run `seamark lessons --apply p1,p2` again to record them as applied "+
		"without a second pin", err.Error())
	assert.Empty(t, out.String(), "no line claims an applied pin")
	require.NoError(t, st.Close())

	written, _ := maintainLessons(t, root)
	assert.Contains(t, written, "  - rule: second-rule\n", "the p2 pin reached the file")
	assert.Contains(t, maintainLedger(t, root, 0), `p1 proposed   "first-rule"`)
	assert.Contains(t, maintainLedger(t, root, 0), `p2 proposed   "second-rule"`)

	stdout, stderr := goldenRun(t, root, "lessons", "--apply", "p1,p2")

	assert.Equal(t, "recorded 2 proposal(s) as applied without a new pin — .seamark/lessons.yaml already holds "+
		"a pin with the same rule and regions\n"+
		"  p1    first-rule\n"+
		"  p2    second-rule\n", stdout)
	assert.Empty(t, stderr)

	got, _ := maintainLessons(t, root)
	assert.Equal(t, written, got, "the rerun adds no second entry")
	assert.Contains(t, maintainLedger(t, root, 0), `p1 applied    "first-rule"`)
	assert.Contains(t, maintainLedger(t, root, 0), `p2 applied    "second-rule"`)
}

func TestLessonsDismissFailureNamesWhatMoved(t *testing.T) {
	busy := errors.New("database is locked")

	require.NoError(t, dismissFailure(proposals.Result{Moved: []int64{1, 2}}))

	err := dismissFailure(proposals.Result{NotMoved: []int64{1, 2}, LedgerErr: busy})
	require.ErrorIs(t, err, busy)
	assert.Equal(t, "database is locked", err.Error(), "nothing moved: the store error alone")

	err = dismissFailure(proposals.Result{Moved: []int64{1}, NotMoved: []int64{2, 3}, LedgerErr: busy})
	require.ErrorIs(t, err, busy)
	assert.Equal(t, "dismissed p1, but not p2, p3: database is locked", err.Error())
}
