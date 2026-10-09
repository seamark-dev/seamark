package maintain

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// fixtureIndexedAt is the index time of every fixture: 2025-09-27 19:06 UTC.
const fixtureIndexedAt = 1_759_000_000

// fixtureNow is the clock of every plan and reading.
var fixtureNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// fixture is one workspace with an index, a pin file, and a write gate.
// Its store is writable; the tests use it as the reader, as the ledger,
// and as "another writer".
type fixture struct {
	t    *testing.T
	root string
	st   *store.Store
}

// newFixture builds the fast ledger fixture of the CLI goldens: index
// metadata and findings, but no tree-sitter run. The write gate is on.
// Each name in files becomes a small file, because trigger validation
// reads the working tree.
func newFixture(t *testing.T, findings []model.Finding, files ...string) *fixture {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	st, err := store.Open(store.DefaultPath(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.SetMeta(store.MetaIndexedAt, fmt.Sprint(fixtureIndexedAt)))
	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))
	require.NoError(t, st.ReplaceLessons(nil, findings))

	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
	}

	f := &fixture{t: t, root: root, st: st}
	f.gate(true)

	return f
}

// gate writes the distill.write gate.
func (f *fixture) gate(on bool) {
	f.t.Helper()
	f.config(fmt.Sprintf("distill:\n  write: %t\n", on))
}

// config writes .seamark/config.yaml.
func (f *fixture) config(content string) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, ".seamark", "config.yaml"), []byte(content), 0o644))
}

// pinPath returns the path of the pin file.
func (f *fixture) pinPath() string {
	return filepath.Join(f.root, ".seamark", "lessons.yaml")
}

// write writes the pin file with an exact mode, independent of the umask.
func (f *fixture) write(content string, mode os.FileMode) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(f.pinPath(), []byte(content), mode))
	require.NoError(f.t, os.Chmod(f.pinPath(), mode))
}

// lessons returns the pin file text.
func (f *fixture) lessons() string {
	f.t.Helper()

	data, err := os.ReadFile(f.pinPath())
	require.NoError(f.t, err)

	return string(data)
}

// mode returns the permission bits of the pin file.
func (f *fixture) mode() os.FileMode {
	f.t.Helper()

	info, err := os.Stat(f.pinPath())
	require.NoError(f.t, err)

	return info.Mode().Perm()
}

// propose stores rows as an earlier run or decision left them and returns
// them with their IDs.
func (f *fixture) propose(ps ...model.Proposal) []model.Proposal {
	f.t.Helper()

	for i := range ps {
		require.NoError(f.t, f.st.InsertProposal(&ps[i]))
	}

	return ps
}

// rows returns every proposal row in ID order.
func (f *fixture) rows() []model.Proposal {
	f.t.Helper()

	var rows []model.Proposal

	for _, status := range []string{
		model.ProposalProposed, model.ProposalApplied, model.ProposalDismissed, model.ProposalSuperseded,
	} {
		ps, err := f.st.Proposals(status)
		require.NoError(f.t, err)

		rows = append(rows, ps...)
	}

	slices.SortFunc(rows, byID)

	return rows
}

// ledger renders the columns that maintenance can change, one row per
// line in ID order: the status, the rule, the region fields, and the
// trigger fields. history renders the other columns.
func (f *fixture) ledger() string {
	f.t.Helper()

	var b strings.Builder

	for _, p := range f.rows() {
		fmt.Fprintf(&b, "p%d %s %q region=%q regions=%q triggers=%q checked=%d v%d\n",
			p.ID, p.Status, p.Rule, p.Region, p.Regions, p.TriggerPaths, p.TriggerChecked, p.TriggerPromptVersion)
	}

	return b.String()
}

// history renders the columns that maintenance never changes, one row
// per line in ID order: the signature, the recorded note, the cited
// findings, the agent, and the creation time.
func (f *fixture) history() string {
	f.t.Helper()

	var b strings.Builder

	for _, p := range f.rows() {
		fmt.Fprintf(&b, "p%d sig=%q note=%q members=%v agent=%q created=%d\n",
			p.ID, p.Signature, p.Note, p.Members, p.Agent, p.CreatedAt)
	}

	return b.String()
}

// evidence is what maintenance never changes outside the ledger: every
// stored finding and the exact bytes of the firing log. AC-5 keeps the
// cited findings and the delivery history after a retarget or a prune.
type evidence struct {
	findings []model.Finding
	// log is nil when the firing log does not exist.
	log []byte
}

// evidence reads every finding row and the firing log.
func (f *fixture) evidence() evidence {
	f.t.Helper()

	findings, err := f.st.AllFindings()
	require.NoError(f.t, err)

	log, err := os.ReadFile(reviews.AuditLogPath(f.root))
	if errors.Is(err, os.ErrNotExist) {
		return evidence{findings: findings}
	}

	require.NoError(f.t, err)

	return evidence{findings: findings, log: log}
}

// probe is the observation of a ready index.
func (f *fixture) probe() workspace.IndexProbe {
	return workspace.IndexProbe{
		State: workspace.IndexReady, Path: store.DefaultPath(f.root), Root: f.root, IndexedAt: fixtureIndexedAt,
	}
}

// deps returns the production boundaries over the fixture.
func (f *fixture) deps() Deps {
	return Deps{
		Root: f.root, Reg: integration.Builtin(), Reader: f.st, Probe: f.probe(),
		Now: func() time.Time { return fixtureNow },
	}
}

// ref returns the binding of the first entry with the rule and, when it
// is not empty, the region.
func (f *fixture) ref(rule, region string) pinfile.EntryRef {
	f.t.Helper()

	d, err := pinfile.Read(f.root)
	require.NoError(f.t, err)

	for _, e := range d.Entries {
		if e.Rule == rule && (region == "" || e.Region == region) {
			return e.Ref()
		}
	}

	require.FailNow(f.t, "no entry", "%s @ %s", rule, region)

	return pinfile.EntryRef{}
}

// request selects the entries named "rule" or "rule@region".
func (f *fixture) request(action Action, names ...string) Request {
	f.t.Helper()

	req := Request{Action: action}

	for _, n := range names {
		rule, region, _ := strings.Cut(n, "@")
		req.Refs = append(req.Refs, f.ref(rule, region))
	}

	return req
}

// plan plans req and requires no error.
func (f *fixture) plan(deps Deps, req Request) *Plan {
	f.t.Helper()

	p, err := NewPlan(deps, req)
	require.NoError(f.t, err)

	return p
}

// fire appends one delivered firing of pin to the firing log.
func (f *fixture) fire(pin reviews.PinRule, ts string) {
	f.t.Helper()

	data, err := json.Marshal(reviews.Firing{TS: ts, File: "x.go", Fired: []reviews.FiredLesson{reviews.PinIdentity(pin)}})
	require.NoError(f.t, err)

	fh, err := os.OpenFile(reviews.AuditLogPath(f.root), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(f.t, err)

	_, err = fh.Write(append(data, '\n'))
	require.NoError(f.t, err)
	require.NoError(f.t, fh.Close())
}

// failUpdates installs a database trigger that aborts every update of
// one proposal column. It injects a ledger failure inside the guarded
// transaction, without a held lock or a timing window.
func (f *fixture) failUpdates(column string) {
	f.t.Helper()
	f.exec(fmt.Sprintf(`CREATE TRIGGER fail_%[1]s BEFORE UPDATE OF %[1]s ON proposal
BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`, column))
}

// exec runs one statement on the index database through a second
// connection. A test uses it to break one table and keep the others.
func (f *fixture) exec(query string) {
	f.t.Helper()

	db, err := sql.Open("sqlite", store.DefaultPath(f.root))
	require.NoError(f.t, err)

	defer func() { _ = db.Close() }()

	_, err = db.Exec(query)
	require.NoError(f.t, err)
}

// ledgerSeam wraps the real store to inject failures into a commit.
type ledgerSeam struct {
	*store.Store

	// before runs before the guarded write, as another writer would.
	before func()
	// real runs the real write first; err is then returned after it.
	real bool
	// err replaces the result of the guarded write.
	err error
	// readErr fails the read after a failure.
	readErr error
	// reads counts the reads after a failure.
	reads int
}

func (l *ledgerSeam) write(do func() error) error {
	if l.before != nil {
		l.before()
	}

	if l.err == nil || l.real {
		if err := do(); err != nil || l.err == nil {
			return err
		}
	}

	return l.err
}

func (l *ledgerSeam) RetargetApplied(changes []store.RegionChange) error {
	return l.write(func() error { return l.Store.RetargetApplied(changes) })
}

func (l *ledgerSeam) SupersedeExact(guards []store.RowGuard) error {
	return l.write(func() error { return l.Store.SupersedeExact(guards) })
}

func (l *ledgerSeam) ProposalsWithIDs(ids []int64) ([]model.Proposal, error) {
	l.reads++

	if l.readErr != nil {
		return nil, l.readErr
	}

	return l.Store.ProposalsWithIDs(ids)
}

// fileSeam wraps the real pin file writer to inject failures and other
// writers into a commit.
type fileSeam struct {
	// failBefore fails the replace before any write.
	failBefore error
	// failAfter fails the replace after the real write.
	failAfter error
	// after runs after a real write, as another writer would.
	after func()
	// restoreErr fails every restore.
	restoreErr error
	// restores counts the restore calls.
	restores int
}

func (s *fileSeam) Replace(reg *integration.Registry, root string, expect integration.FileGuard, after []byte) error {
	if s.failBefore != nil {
		return s.failBefore
	}

	if err := (pinWriter{}).Replace(reg, root, expect, after); err != nil {
		return err
	}

	if s.after != nil {
		s.after()
	}

	return s.failAfter
}

func (s *fileSeam) Restore(reg *integration.Registry, root string, written [32]byte, original []byte) error {
	s.restores++

	if s.restoreErr != nil {
		return s.restoreErr
	}

	return (pinWriter{}).Restore(reg, root, written, original)
}
