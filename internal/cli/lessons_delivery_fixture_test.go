package cli

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

const deliveryPins = `pin:
  - rule: close-db-handles
    region: api
    note: Close the handle on every error path.
  - rule: keep-docs-current
    note: Keep docs current.
`

var deliveryNow = time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)

// deliveryFixture uses real temporary source files, so no read can
// resolve to a file of this checkout. raw holds the values that the
// audit record digests; no report may print them.
type deliveryFixture struct {
	root, identityRoot string
	cfg                *reviews.Config
	raw                []string
}

func deliveryDigest(value string) string {
	h := sha256.Sum256([]byte(value))

	return hex.EncodeToString(h[:])
}

// newDeliveryFixture writes the pins, makes the workspace a git
// repository, and writes the two audit logs. An empty identityRoot means
// the workspace root.
func newDeliveryFixture(t *testing.T, identityRoot string) *deliveryFixture {
	t.Helper()

	root := writeFixture(t)
	verifyWrite(t, root, ".seamark/lessons.yaml", deliveryPins)
	gitify(t, root)

	if identityRoot == "" {
		identityRoot = root
	}

	cfg, err := reviews.ParseConfig([]byte(deliveryPins))
	require.NoError(t, err)

	f := &deliveryFixture{root: root, identityRoot: identityRoot, cfg: cfg}
	f.audit(t)

	return f
}

// deliverySQL is fixture-only. DELETE journaling makes the immutability
// assertion cover every file and directory, including SQLite sidecars.
func deliverySQL(t *testing.T, path string, statements ...string) {
	t.Helper()

	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String())
	require.NoError(t, err)

	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err)
	}

	require.NoError(t, db.Close())
}

func (f *deliveryFixture) index(t *testing.T) {
	t.Helper()

	st, err := store.Open(store.DefaultPath(f.root))
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("repo_root", f.identityRoot))

	p := model.Proposal{Signature: "delivery-linked", Rule: f.cfg.Pin[0].Rule, Region: "api", Status: model.ProposalApplied}
	require.NoError(t, storetest.InsertProposal(st, &p))
	require.NoError(t, st.Close())
	deliverySQL(t, store.DefaultPath(f.root), "PRAGMA journal_mode = DELETE")
}

// audit writes one injected hook record for the current version of the
// first pin and one verify record of the lesson-check stream. The hook
// record holds digests of the raw session, tool use, and agent values.
func (f *deliveryFixture) audit(t *testing.T) {
	t.Helper()

	pin := f.cfg.Pin[0]
	identity := reviews.PinIdentityDigest(reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions))
	complete := true
	native, tool, agent := "PRIVATE-native-session-full", "PRIVATE-tool-use-full", "PRIVATE-agent-full"
	f.raw = []string{native, tool, agent}

	rc := integration.ReceivingContextOf(integration.ClaudeID, native, agent)
	require.NotNil(t, rc)

	record, err := json.Marshal(reviews.Firing{
		TS: deliveryNow.Format(time.RFC3339), SchemaVersion: reviews.AuditSchemaVersion,
		EventID: deliveryDigest("event")[:32], EventSeq: 1, EventRecords: 1,
		Delivery: reviews.DeliveryInjected, Emission: "emitted", Evaluation: "complete", PathsComplete: &complete,
		GuidanceSHA: f.cfg.GuidanceDigest(), File: "api/handler.go", Client: integration.ClaudeID, Lane: rc.Lane,
		SessionSHA: reviews.SessionDigest(f.identityRoot, native), MatchSHA: reviews.MatchDigest(f.identityRoot, native, tool),
		ContextSHA: reviews.ContextDigest(f.identityRoot, integration.ClaudeID, rc.ID),
		Pins:       []reviews.FiringPin{{Identity: identity, Version: f.cfg.PinVersion(pin), Delivery: reviews.PinInjected}},
	})
	require.NoError(t, err)
	verifyWrite(t, f.root, ".seamark/lessons-audit.jsonl", string(record)+"\n")

	check, err := json.Marshal(lessoncheck.AuditRecord{Schema: "lesson-check-audit/1", Kind: lessoncheck.AuditVerify})
	require.NoError(t, err)
	verifyWrite(t, f.root, ".seamark/lesson-check-audit.jsonl", string(check)+"\n")
}

func (f *deliveryFixture) pinRef() string {
	pin := f.cfg.Pin[0]

	return f.cfg.ShortPinRef(reviews.NewPinKey(pin.Rule, pin.Region, pin.Regions))
}

func deliveryRun(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()

	out, stderr, err := runIn(t, "", append([]string{"-C", root, "lessons", "--delivery"}, args...)...)
	require.Empty(t, stderr)

	return out, err
}

func deliveryReport(t *testing.T, root string, args ...string) (report lessonreport.Report, output string) {
	t.Helper()

	out, err := deliveryRun(t, root, append(args, "--json")...)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal([]byte(out), &report))

	return report, out
}

// deliveryTree compares main databases, audits, state, locks, directories,
// file modes and mtimes. It deliberately excludes no sidecars.
func deliveryTree(t *testing.T, root string) map[string]deliveryFile {
	t.Helper()

	files := map[string]deliveryFile{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		file := deliveryFile{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			file.Data = string(data)
		}

		files[rel] = file

		return nil
	})
	require.NoError(t, err)

	return files
}

type deliveryFile struct {
	Data    string
	Mode    fs.FileMode
	ModTime time.Time
}
