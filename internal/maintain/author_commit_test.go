package maintain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// authorSeam wraps the production authoring writer to inject failures.
type authorSeam struct {
	// write runs the real write first.
	write bool
	// before runs before the result returns, as another writer would.
	before func()
	// err replaces the result of the write.
	err error
	// calls counts the writes.
	calls int
}

func (s *authorSeam) Write(reg *integration.Registry, root string, expect integration.FileGuard, after []byte, create bool) error {
	s.calls++

	if s.write {
		if err := (authorWriter{}).Write(reg, root, expect, after, create); err != nil {
			return err
		}
	}

	if s.before != nil {
		s.before()
	}

	return s.err
}

// pinSnapshot holds what an authoring commit never changes outside the
// pin file: the ledger columns, the history columns, and the evidence.
type pinSnapshot struct {
	ledger, history string
	evidence        evidence
}

// snapshot reads the ledger rows and the evidence.
func (f *fixture) snapshot() pinSnapshot {
	f.t.Helper()

	return pinSnapshot{ledger: f.ledger(), history: f.history(), evidence: f.evidence()}
}

func TestCommitAuthorWritesOneFileAndNoLedgerRow(t *testing.T) {
	cases := []struct {
		name string
		req  func(f *fixture) AuthorRequest
		ref  bool
	}{
		{name: "create", req: func(*fixture) AuthorRequest { return newPin("new-rule", "Guidance.", "web") }, ref: true},
		{name: "unlinked edit", req: func(f *fixture) AuthorRequest {
			return AuthorRequest{Op: AuthorEdit, Ref: f.ref("Manual-Rule", ""), Rule: "manual-rule-2", RuleSet: true}
		}, ref: true},
		{name: "linked note edit", req: func(f *fixture) AuthorRequest {
			return editNote(f.ref("linked-rule", ""), "A corrected live note.")
		}, ref: true},
		{name: "unlinked removal", req: func(f *fixture) AuthorRequest {
			return AuthorRequest{Op: AuthorRemove, Ref: f.ref("Manual-Rule", "")}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := authorFixture(t)
			f.write(authorLessons, 0o600)
			f.fire(f.pinOf("linked-rule"), "2026-09-01T00:00:00Z")

			before := f.snapshot()
			require.NotEmpty(t, before.evidence.log)

			p := f.authorPlan(f.deps(), tc.req(f))
			require.True(t, p.Confirmable, "blocked: %v", blockerLines(p))

			res, err := CommitAuthor(f.deps(), p)
			require.NoError(t, err)
			assert.Equal(t, Written, res.Outcome)
			require.NoError(t, res.Err)

			assert.Equal(t, string(p.After), f.lessons())
			assert.Equal(t, os.FileMode(0o600), f.mode(), "the mode of the file stays")
			assert.Equal(t, before, f.snapshot(), "no ledger row, no recorded note, no evidence changes")

			if !tc.ref {
				assert.Nil(t, res.NewRef)

				return
			}

			require.NotNil(t, res.NewRef)

			d, err := pinfile.Read(f.root)
			require.NoError(t, err)
			assert.Contains(t, refsOf(d), *res.NewRef, "the result binds the entry in the file")
		})
	}
}

// pinOf returns the decoded pin of the first entry with the rule.
func (f *fixture) pinOf(rule string) reviews.PinRule {
	f.t.Helper()

	d, err := pinfile.Read(f.root)
	require.NoError(f.t, err)

	for _, e := range d.Entries {
		if e.Rule == rule {
			return pinRule(e)
		}
	}

	require.FailNow(f.t, "no entry", rule)

	return reviews.PinRule{}
}

// refsOf returns the binding of every entry of d.
func refsOf(d *pinfile.Document) []pinfile.EntryRef {
	var out []pinfile.EntryRef

	for _, e := range d.Entries {
		out = append(out, e.Ref())
	}

	return out
}

func TestCommitAuthorAppendsNewPinsInOrder(t *testing.T) {
	f := newFixture(t, nil)

	for _, rule := range []string{"first", "second", "third"} {
		p := f.authorPlan(f.deps(), newPin(rule, "Guidance for "+rule+"."))
		require.True(t, p.Confirmable, "blocked: %v", blockerLines(p))

		res, err := CommitAuthor(f.deps(), p)
		require.NoError(t, err)
		require.Equal(t, Written, res.Outcome)
	}

	assert.Equal(t, `pin:
  - rule: first
    region: '*'
    note: Guidance for first.
  - rule: second
    region: '*'
    note: Guidance for second.
  - rule: third
    region: '*'
    note: Guidance for third.
`, f.lessons(), "the first save creates the file; each new pin goes last")
}

func TestCommitAuthorRefusesStalePlans(t *testing.T) {
	unlinkedNote := func(f *fixture) AuthorRequest { return editNote(f.ref("Manual-Rule", ""), "A new note.") }
	create := func(*fixture) AuthorRequest { return newPin("new-rule", "Guidance.", "api") }

	cases := []struct {
		name string
		// absent starts without a pin file.
		absent bool
		// rows are proposed before the plan.
		rows   []model.Proposal
		req    func(f *fixture) AuthorRequest
		mutate func(f *fixture, deps *Deps)
		want   []string
	}{
		{
			name: "bytes", req: unlinkedNote, want: []string{diffBytes},
			mutate: func(f *fixture, _ *Deps) { f.write(authorLessons+"threshold_note: x\n", 0o644) },
		},
		{
			name: "mode", req: unlinkedNote, want: []string{diffMode},
			mutate: func(f *fixture, _ *Deps) { require.NoError(f.t, os.Chmod(f.pinPath(), 0o600)) },
		},
		{
			name: "existence of a created file", absent: true, req: create, want: []string{diffExistence},
			mutate: func(f *fixture, _ *Deps) { f.write("pin:\n", 0o644) },
		},
		{
			name: "existence of an edited file", req: unlinkedNote, want: []string{diffExistence},
			mutate: func(f *fixture, _ *Deps) { require.NoError(f.t, os.Remove(f.pinPath())) },
		},
		{
			name: "gate", req: create, want: []string{diffGate},
			mutate: func(f *fixture, _ *Deps) { f.gate(false) },
		},
		{
			name: "relationship", req: unlinkedNote, want: []string{"now linked to proposal #2"},
			mutate: func(f *fixture, _ *Deps) {
				f.propose(model.Proposal{
					Signature: "s2", Rule: "manual-rule", Region: "web", Regions: []string{"web", "docs"},
					Note: "Recorded.", Status: model.ProposalApplied,
				})
			},
		},
		{
			// The entry stays linked to #1, so only the new row is a
			// difference.
			name: "a new pending row of a linked entry",
			req:  func(f *fixture) AuthorRequest { return editNote(f.ref("linked-rule", ""), "New.") },
			want: []string{"new owner: pending #2"},
			mutate: func(f *fixture, _ *Deps) {
				f.propose(model.Proposal{Signature: "s2", Rule: "linked-rule", Region: "api", Status: model.ProposalProposed})
			},
		},
		{
			name: "a new pending row of an unlinked entry", req: unlinkedNote,
			want: []string{"relationship: matches a pending proposal; record it as applied first", "new owner: pending #2"},
			mutate: func(f *fixture, _ *Deps) {
				f.propose(model.Proposal{
					Signature: "s2", Rule: "manual-rule", Region: "web", Regions: []string{"web", "docs"},
					Status: model.ProposalProposed,
				})
			},
		},
		{
			// The link moves from #1 to #2. Each row is named once.
			name: "a link to another row",
			req:  func(f *fixture) AuthorRequest { return editNote(f.ref("linked-rule", ""), "New.") },
			want: []string{"now linked to proposal #2", "proposal #1 changed"},
			mutate: func(f *fixture, _ *Deps) {
				f.supersede(1)
				f.propose(model.Proposal{
					Signature: "s2", Rule: "linked-rule", Region: "api", Note: "Recorded again.",
					Status: model.ProposalApplied,
				})
			},
		},
		{
			// p2 owns the key of the pin by the pin that its apply writes.
			// Its region fields then come to agree, so it matches the entry
			// too. The owners stay the same, but the rows of the entry
			// differ.
			name: "a row that starts to match the entry",
			rows: []model.Proposal{{
				Signature: "s2", Rule: "linked-rule", Region: "zzz", Regions: []string{"api"},
				Status: model.ProposalProposed,
			}},
			req:    func(f *fixture) AuthorRequest { return editNote(f.ref("linked-rule", ""), "New.") },
			want:   []string{diffRows},
			mutate: func(f *fixture, _ *Deps) { f.exec(`UPDATE proposal SET region = 'api' WHERE id = 2`) },
		},
		{
			name: "owner", req: create, want: []string{"new owner: pending #2"},
			mutate: func(f *fixture, _ *Deps) {
				f.propose(model.Proposal{Signature: "s2", Rule: "new-rule", Region: "api", Status: model.ProposalProposed})
			},
		},
		{
			name: "linked row", req: func(f *fixture) AuthorRequest { return editNote(f.ref("linked-rule", ""), "New.") },
			want:   []string{"proposal #1 changed"},
			mutate: func(f *fixture, _ *Deps) { f.exec(`UPDATE proposal SET signature = 's1-again' WHERE id = 1`) },
		},
		{
			name: "ledger", req: create, want: []string{diffLedger},
			mutate: func(_ *fixture, deps *Deps) { deps.Reader = nil },
		},
	}

	if runtime.GOOS != "windows" {
		cases = append(cases, struct {
			name   string
			absent bool
			rows   []model.Proposal
			req    func(f *fixture) AuthorRequest
			mutate func(f *fixture, deps *Deps)
			want   []string
		}{
			name: "hard link", req: create,
			want: []string{"replacing this file would break its hard link; edit .seamark/lessons.yaml by hand"},
			mutate: func(f *fixture, _ *Deps) {
				require.NoError(f.t, os.Link(f.pinPath(), filepath.Join(f.root, "lessons-copy.yaml")))
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := authorFixture(t)
			f.propose(tc.rows...)

			if tc.absent {
				require.NoError(t, os.Remove(f.pinPath()))
			}

			p := f.authorPlan(f.deps(), tc.req(f))
			require.True(t, p.Confirmable, "blocked: %v", blockerLines(p))

			deps := f.deps()
			seam := &authorSeam{write: true}
			deps.Author = seam
			tc.mutate(f, &deps)

			lessons, err := os.ReadFile(f.pinPath())
			if errors.Is(err, os.ErrNotExist) {
				lessons = nil
			} else {
				require.NoError(t, err)
			}

			rows := f.snapshot()

			res, err := CommitAuthor(deps, p)
			require.ErrorIs(t, err, ErrStale)

			var stale *StaleError
			require.ErrorAs(t, err, &stale)
			assert.Equal(t, tc.want, stale.Differences)
			assert.Equal(t, NotWritten, res.Outcome)
			assert.Equal(t, err, res.Err)
			assert.Zero(t, seam.calls, "a stale plan never reaches the writer")

			now, rerr := os.ReadFile(f.pinPath())
			if lessons == nil {
				require.ErrorIs(t, rerr, os.ErrNotExist)
			} else {
				require.NoError(t, rerr)
				assert.Equal(t, string(lessons), string(now), "no write")
			}

			assert.Equal(t, rows, f.snapshot(), "no ledger change")
		})
	}
}

func TestCommitAuthorClassifiesTheOutcome(t *testing.T) {
	unreadable := func(t *testing.T, f *fixture) func() {
		t.Helper()

		if os.Geteuid() == 0 {
			t.Skip("the superuser reads a directory without permissions")
		}

		dir := filepath.Join(f.root, ".seamark")
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		return func() { require.NoError(t, os.Chmod(dir, 0o000)) }
	}

	t.Run("a failure after the new bytes landed", func(t *testing.T) {
		f := authorFixture(t)
		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		deps := f.deps()
		deps.Author = &authorSeam{write: true, err: errors.New("injected: fsync failed")}

		res, err := CommitAuthor(deps, p)
		require.NoError(t, err, "the write started, so the result holds the outcome")
		assert.Equal(t, Written, res.Outcome, "the file holds the planned bytes despite the error")
		require.EqualError(t, res.Err, "injected: fsync failed")
		assert.NotNil(t, res.NewRef)
		assert.Equal(t, string(p.After), f.lessons())
	})

	t.Run("a failure that left the file as it was", func(t *testing.T) {
		f := authorFixture(t)
		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		deps := f.deps()
		deps.Author = &authorSeam{err: errors.New("injected: rename failed")}

		res, err := CommitAuthor(deps, p)
		require.NoError(t, err)
		assert.Equal(t, NotWritten, res.Outcome, "the read-back shows the old bytes")
		require.EqualError(t, res.Err, "injected: rename failed")
		assert.Nil(t, res.NewRef)
		assert.Equal(t, authorLessons, f.lessons())
	})

	t.Run("a failure and a file that cannot be read again", func(t *testing.T) {
		f := authorFixture(t)
		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		deps := f.deps()
		deps.Author = &authorSeam{err: errors.New("injected: disk full"), before: unreadable(t, f)}

		res, err := CommitAuthor(deps, p)
		require.NoError(t, os.Chmod(filepath.Join(f.root, ".seamark"), 0o755))
		require.NoError(t, err)

		assert.Equal(t, Unknown, res.Outcome, "an unchecked file is neither written nor untouched")
		require.ErrorContains(t, res.Err, "injected: disk full")
		require.ErrorContains(t, res.Err, "the file could not be read after the failure")
		assert.Nil(t, res.NewRef)
	})

	refusals := []struct {
		name  string
		err   error
		match error
	}{
		{name: "the engine saw a change", err: fmt.Errorf("%w: the file changed", pinfile.ErrStale), match: ErrStale},
		{name: "the engine refused the file", err: fmt.Errorf("%w: a link", pinfile.ErrNotWritable), match: pinfile.ErrNotWritable},
		{name: "the prepare failed", err: fmt.Errorf("%w: no registry", errNotStarted), match: errNotStarted},
	}

	for _, tc := range refusals {
		t.Run("a refusal before the write: "+tc.name, func(t *testing.T) {
			f := authorFixture(t)
			p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

			// The directory refuses every read, so a read-back would give
			// Unknown. NotWritten proves that the commit read nothing back.
			deps := f.deps()
			deps.Author = &authorSeam{err: tc.err, before: unreadable(t, f)}

			res, err := CommitAuthor(deps, p)
			require.NoError(t, os.Chmod(filepath.Join(f.root, ".seamark"), 0o755))

			require.ErrorIs(t, err, tc.match)
			assert.Equal(t, NotWritten, res.Outcome)
			assert.Equal(t, err, res.Err)
			assert.Equal(t, authorLessons, f.lessons())
		})
	}

	t.Run("a stale engine reports the file bytes", func(t *testing.T) {
		f := authorFixture(t)
		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		deps := f.deps()
		deps.Author = &authorSeam{err: fmt.Errorf("%w: the file changed", pinfile.ErrStale)}

		_, err := CommitAuthor(deps, p)

		var stale *StaleError
		require.ErrorAs(t, err, &stale)
		assert.Equal(t, []string{diffBytes}, stale.Differences)
	})

	t.Run("the default writer without a registry", func(t *testing.T) {
		f := authorFixture(t)
		p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

		deps := f.deps()
		deps.Reg = nil

		res, err := CommitAuthor(deps, p)
		require.ErrorIs(t, err, errNotStarted)
		require.ErrorContains(t, err, "pinfile: no client registry")
		assert.Equal(t, NotWritten, res.Outcome)
		assert.Equal(t, authorLessons, f.lessons())
	})
}

func TestAuthorWriterComparesTheFullObservation(t *testing.T) {
	f := authorFixture(t)
	p := f.authorPlan(f.deps(), newPin("new-rule", "Guidance."))

	// pinfile.Prepare compares only the existence and the content with
	// expect, and binds its own observation. A mode that differs from
	// expect is a change too (P4-7).
	expect := p.Guard
	expect.Mode = 0o600

	err := authorWriter{}.Write(integration.Builtin(), f.root, expect, p.After, false)

	var stale *StaleError
	require.ErrorAs(t, err, &stale)
	assert.Equal(t, []string{diffMode}, stale.Differences)
	assert.Equal(t, authorLessons, f.lessons())

	require.NoError(t, authorWriter{}.Write(integration.Builtin(), f.root, p.Guard, p.After, false))
	assert.Equal(t, string(p.After), f.lessons())
}

func TestCommitAuthorRefusesMisuse(t *testing.T) {
	f := authorFixture(t)

	_, err := CommitAuthor(f.deps(), nil)
	require.EqualError(t, err, "maintain: no plan")

	p := f.authorPlan(f.deps(), newPin("rule", "note", "api", "api"))
	require.False(t, p.Confirmable)

	res, err := CommitAuthor(f.deps(), p)
	require.ErrorIs(t, err, ErrNotConfirmable)
	assert.Equal(t, NotWritten, res.Outcome)

	// A forged flag does not pass: the commit plans again.
	forged := *p
	forged.Confirmable = true

	_, err = CommitAuthor(f.deps(), &forged)
	require.ErrorIs(t, err, ErrStale)
	assert.Equal(t, authorLessons, f.lessons())
}

func TestStaleErrorMatchesErrStale(t *testing.T) {
	err := error(&StaleError{Differences: []string{diffBytes, "new owner: pending #9"}})

	require.ErrorIs(t, err, ErrStale)
	assert.True(t, strings.HasSuffix(err.Error(), ": file bytes; new owner: pending #9"))
}
