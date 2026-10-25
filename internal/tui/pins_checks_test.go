package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The lesson-check tests of Lessons › Pins (P4.10): the CHECK section,
// the Mode and Check rows of the pin form, the check-definition form of
// M, and the verification view of V. The screen tests use the fake
// services with a pin inspection that the real engine reads, and a
// capability report of the real join. The engine tests drive the screen
// over the production services on a temporary git workspace, so the
// console text is the text of lessons --verify.

// checkLessonsYAML holds one pin of each lesson-check state: an enforce
// pin, a check pin, an advisory pin with a check, and a prose pin.
const checkLessonsYAML = `lesson_checks_version: 1
checks:
  frozen-assets:
    kind: forbid-path-change/1
  no-todo:
    kind: no-added-pattern/1
    pattern: 'TODO\('
pin:
  - rule: frozen-assets
    region: generated
    note: Changes to these files require a maintainer-managed update.
    mode: enforce
    check: frozen-assets
  - rule: no-todo
    region: src
    note: Do not add TODO markers.
    mode: check
    check: no-todo
  - rule: later-check
    region: lib
    note: Prefer errors.Is.
    check: no-todo
  - rule: prose
    region: docs
    note: Keep the docs short.
`

// checkPlainYAML is a pin file without a lesson-check key.
const checkPlainYAML = `pin:
  - rule: prose
    region: docs
    note: Keep the docs short.
`

// checkInspectionOf reads text with the real inspection on a temporary
// root, then links every entry as a pin without a proposal. So the
// lesson-check readings are those of maintain.Inspect. The inspection
// holds the bytes, so the root goes away before it returns.
func checkInspectionOf(text string) (*maintain.Inspection, error) {
	root, err := os.MkdirTemp("", "seamark-pins-checks-")
	if err != nil {
		return nil, err
	}

	defer func() { _ = os.RemoveAll(root) }()

	path := filepath.Join(root, filepath.FromSlash(pinfile.File))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return nil, err
	}

	in := maintain.Inspect(root, nil, workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"}, pinsAt)
	if in.DocErr != nil {
		return nil, in.DocErr
	}

	in.Links = pinfile.Classify(in.Doc, nil, nil, nil)
	in.LinkErr, in.LedgerErr = nil, nil

	for i := range in.Pins {
		in.Pins[i].Link = in.Links.Entries[i]
		in.Pins[i].Readings = pinsUnknown(in.Pins[i].Link.Link.Reason())
	}

	return in, nil
}

// checkInspection is checkInspectionOf for a test.
func checkInspection(t *testing.T, text string) *maintain.Inspection {
	t.Helper()

	in, err := checkInspectionOf(text)
	require.NoError(t, err)

	return in
}

// checkReportOf joins text with Claude Code enforce hooks on every write
// tool and context, and Codex without a lesson-check hook. It is the
// real join of contract K10 (lessoncheck.CapabilityReportOf).
func checkReportOf(text string) (lessoncheck.CapabilityReport, error) {
	cfg, errs, err := lessoncheck.Parse([]byte(text))
	if err != nil {
		return lessoncheck.CapabilityReport{}, err
	}

	var installed []lessoncheck.InstalledPath

	for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
		for _, ctx := range []lessoncheck.PathContext{lessoncheck.ContextRoot, lessoncheck.ContextChild} {
			for _, stage := range []lessoncheck.HookStage{lessoncheck.StagePreWrite, lessoncheck.StagePostEdit} {
				marker := lessoncheck.MarkerGuard
				if stage == lessoncheck.StagePostEdit {
					marker = "lessons --verify --hook"
				}

				installed = append(installed, lessoncheck.InstalledPath{
					Client: "claude", Tool: tool, Stage: stage, Context: ctx, Installed: true, Marker: marker,
					BinaryVersion: "v0.9.0", BinaryState: lessoncheck.BinaryCurrent,
					Verification: lessoncheck.CapVerification{Level: "unverified"},
				})
			}
		}
	}

	for _, stage := range []lessoncheck.HookStage{lessoncheck.StagePreWrite, lessoncheck.StagePostEdit} {
		installed = append(installed, lessoncheck.InstalledPath{
			Client: "codex", Tool: "apply_patch", Stage: stage, Context: lessoncheck.ContextRoot,
			Reasons:      []lessoncheck.CapReason{lessoncheck.CapNotInstalled},
			Verification: lessoncheck.CapVerification{Level: "unverified"},
		})
	}

	installed = append(installed, lessoncheck.InstalledPath{Client: "claude", Tool: "Bash", Context: lessoncheck.ContextRoot})

	return lessoncheck.CapabilityReportOf(cfg, errs, installed, nil), nil
}

// checkReport is checkReportOf for a test.
func checkReport(t *testing.T, text string) lessoncheck.CapabilityReport {
	t.Helper()

	report, err := checkReportOf(text)
	require.NoError(t, err)

	return report
}

// checkFakeOf returns services over checkLessonsYAML: the real
// inspection, the report of checkReportOf, and a ledger with the write
// gate on. The capture scenes have no test handle, so it returns the
// error.
func checkFakeOf() (*fakeServices, error) {
	in, err := checkInspectionOf(checkLessonsYAML)
	if err != nil {
		return nil, err
	}

	report, err := checkReportOf(checkLessonsYAML)
	if err != nil {
		return nil, err
	}

	f := newFake()
	f.ledger = lesLedger()
	f.pins = in
	f.lessonCapability = report

	return f, nil
}

// checkFake is checkFakeOf for a test.
func checkFake(t *testing.T) *fakeServices {
	t.Helper()

	f, err := checkFakeOf()
	require.NoError(t, err)

	return f
}

// checkClientText is the active line of one client in the CHECK
// section: the client, the badge, and the reasons.
func checkClientText(c lessoncheck.PinClient) string {
	text := c.Client + " " + c.Badge
	if len(c.Reasons) > 0 {
		text += " (" + lessoncheck.JoinCapReasons(c.Reasons) + ")"
	}

	return text
}

func TestTUIPinsShowCheckSection(t *testing.T) {
	t.Run("every lesson-check state of a pin", func(t *testing.T) {
		f := checkFake(t)
		h := pinsOpen(t, f, 120, 40)
		in := f.pins

		enforce := in.Pins[0].Check
		pinsCursorTo(h, 1)
		text := pinsDetail(t, h)

		assert.Contains(t, text, "CHECK pin-ref "+enforce.Ref)
		assert.Contains(t, text, "Mode enforce")
		assert.Contains(t, text, "Check frozen-assets (forbid-path-change/1, "+enforce.Check.Version[:12]+")")
		assert.Contains(t, text, "Predicate "+lesFlat(enforce.Predicate))
		assert.Contains(t, text, "Scope generated")

		pc, ok := f.lessonCapability.Pin(enforce.Ref)
		require.True(t, ok)
		require.Len(t, pc.Clients, 2)

		for _, c := range pc.Clients {
			assert.Contains(t, text, checkClientText(c), "the badge and reasons of the report")
		}

		assert.Contains(t, text, "claude enforced-unverified (native-deny-unverified, native-timeout-unverified)")
		assert.Contains(t, text, "codex requested-inactive (not-installed)")
		assert.Less(t, strings.Index(text, "Source .seamark/lessons.yaml"), strings.Index(text, "CHECK"),
			"the section follows the file facts")

		pinsCursorTo(h, 2)
		text = pinsDetail(t, h)
		assert.Contains(t, text, "Mode check")
		assert.Contains(t, text, "Predicate "+lesFlat(in.Pins[1].Check.Predicate))
		assert.Contains(t, text, "claude checked")

		pinsCursorTo(h, 3)
		text = pinsDetail(t, h)
		assert.Contains(t, text, "Mode advisory")
		assert.Contains(t, text, "advisory-with-check: the check runs only after a change of the mode")
		assert.Contains(t, text, "seamark lessons --set-mode check --pin "+in.Pins[2].Check.Ref)
		assert.NotContains(t, text, "Active", "an advisory pin has no active state")

		pinsCursorTo(h, 4)
		text = pinsDetail(t, h)
		assert.Contains(t, text, "not-checkable: an advisory pin without a check")
		assert.NotContains(t, text, "Predicate")

		assert.Subset(t, pinsKeys(h), []string{"M", "V"}, "a repository with checks offers M and V")
		assert.Empty(t, f.called(), "browsing plans, verifies, and writes nothing")
		assert.Positive(t, f.readCount("LessonCapability"), "the report is a read")
	})

	t.Run("a report of other bytes is unknown, never a badge", func(t *testing.T) {
		f := checkFake(t)
		f.lessonCapability.Config.SHA256 = strings.Repeat("0", 64)

		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 1)

		text := pinsDetail(t, h)
		assert.Contains(t, text, "unknown: lessons.yaml changed after the coverage read; it reads again")
		assert.NotContains(t, text, "enforced-unverified")
	})

	t.Run("an error of the entry shows under the section", func(t *testing.T) {
		text := strings.Replace(checkLessonsYAML, "    check: frozen-assets\n", "    check: missing\n", 1)

		f := checkFake(t)
		f.pins = checkInspection(t, text)
		f.lessonCapability = checkReport(t, text)

		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 1)

		detail := pinsDetail(t, h)
		assert.Contains(t, detail, "Check missing (not defined)")
		assert.Contains(t, detail, "not available: the check is not defined under checks")
		assert.Contains(t, detail, `check "missing" is not defined under checks.`)
	})

	t.Run("a repository without lesson checks keeps the detail and the keys", func(t *testing.T) {
		f := pinsFake()
		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 1)

		assert.NotContains(t, pinsDetail(t, h), "CHECK")
		assert.NotContains(t, pinsKeys(h), "M")
		assert.NotContains(t, pinsKeys(h), "V")

		h.press("M", "V")
		assert.Nil(t, pinsOf(h).checkDef)
		assert.Nil(t, pinsOf(h).verify)
		assert.Empty(t, f.called())
	})

	t.Run("an installed lesson-check hook shows the section without checks", func(t *testing.T) {
		f := newFake()
		f.ledger = lesLedger()
		f.pins = checkInspection(t, checkPlainYAML)
		f.lessonCapability = checkReport(t, checkPlainYAML)
		f.integrations = []integration.Inspection{{
			ClientID: integration.ClaudeID, Name: "Claude Code",
			LessonHooks: &integration.LessonHookInspection{Entries: []integration.LessonHookEntry{{
				Stage: lessoncheck.StagePreWrite, Marker: lessoncheck.MarkerBaseline, Tools: []string{"Edit"},
			}}},
		}}

		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 1)

		assert.Contains(t, pinsDetail(t, h), "CHECK pin-ref "+f.pins.Pins[0].Check.Ref)
		assert.Contains(t, pinsDetail(t, h), "not-checkable")
		assert.Subset(t, pinsKeys(h), []string{"M", "V"}, "M adds the first definition")
	})
}

// checkEditOpen opens the pin form of pin n of checkFake with a plan
// that edit changes.
func checkEditOpen(t *testing.T, n int, edit func(*maintain.AuthorPlan)) (*harness, *fakeServices, *[]maintain.AuthorRequest) {
	t.Helper()

	f := checkFake(t)

	var reqs []maintain.AuthorRequest

	f.planAuthor = func(req maintain.AuthorRequest) (*maintain.AuthorPlan, error) {
		reqs = append(reqs, req)
		p := &maintain.AuthorPlan{Request: req, Root: f.root, Write: true, LinksKnown: true, Confirmable: true}

		if edit != nil {
			edit(p)
		}

		return p, nil
	}

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, n)
	h.press("i")
	require.NotNil(t, authorOf(h).form)

	return h, f, &reqs
}

// formText returns the open pin form on one line.
func formText(h *harness) string {
	return lesBoxFlat(ansi.Strip(h.view()))
}

// reviewText returns the open pin review on one line.
func reviewText(t *testing.T, h *harness) string {
	t.Helper()

	r := authorOf(h).review
	require.NotNil(t, r, "no pin review is open")

	v := r.content(modalWidthOf(h))
	parts := append([]string{v.Title, v.Right}, v.Body...)
	parts = append(parts, v.Tail...)
	parts = append(parts, h.m.st.theme.Hints(v.Actions))

	return lesFlat(ansi.Strip(strings.Join(parts, "\n")))
}

func TestTUIPinEditShowsCheckImpact(t *testing.T) {
	before := checkLessonsYAML
	after := strings.Replace(before, "    mode: enforce\n", "", 1)
	impact := checkImpactOf(t, before, after)

	t.Run("a mode edit shows the impact before y", func(t *testing.T) {
		h, f, reqs := checkEditOpen(t, 1, func(p *maintain.AuthorPlan) { p.Impact = impact })

		text := formText(h)
		assert.Contains(t, text, "Mode enforce")
		assert.Contains(t, text, "Check frozen-assets")

		// The Check row is the last row, and the Mode row is above it.
		h.press("end", "up", "enter")
		assert.Contains(t, formText(h), "Mode advisory was enforce")

		h.press("s")
		require.Len(t, *reqs, 1)

		req := (*reqs)[0]
		assert.True(t, req.ModeSet)
		assert.Equal(t, lessoncheck.ModeAdvisory, req.Mode)
		assert.False(t, req.CheckSet, "an untouched check is not set")
		assert.False(t, req.RuleSet || req.NoteSet || req.ScopeSet)

		review := reviewText(t, h)
		assertImpactBeforeConfirm(t, review, impact, "y ")
		assert.Contains(t, review, "active: not requested; mode advisory runs no check")

		h.press("y")
		assert.Equal(t, 1, f.callCount("CommitAuthor:edit"), "y saves after the impact")
	})

	t.Run("the check row cycles the defined checks", func(t *testing.T) {
		h, _, reqs := checkEditOpen(t, 1, nil)

		// The choices are no check and each defined ID, in ID order.
		h.press("end", "enter")
		assert.Contains(t, formText(h), "Check no-todo was frozen-assets")

		h.press("enter")
		assert.Contains(t, formText(h), "Check (none) was frozen-assets")

		h.press("enter")
		assert.Contains(t, formText(h), "Check frozen-assets GUIDANCE", "the stored value names no change")

		h.press("enter", "s")
		require.Len(t, *reqs, 1)
		assert.True(t, (*reqs)[0].CheckSet)
		assert.Equal(t, "no-todo", (*reqs)[0].Check)
		assert.False(t, (*reqs)[0].ModeSet)
	})

	t.Run("a refusal of the strict tier names its words", func(t *testing.T) {
		h, f, _ := checkEditOpen(t, 1, func(p *maintain.AuthorPlan) {
			p.Confirmable = false
			p.Impact = impact
			p.Blockers = []maintain.AuthorBlocker{{
				Kind: maintain.BlockCheck, Text: "lessons config: pin 1 (frozen-assets @ generated): mode enforce requires check: <check-id>",
				Route: "choose a check that checks defines, or the advisory mode",
			}}
		})

		h.press("end", "enter", "s")

		review := reviewText(t, h)
		assert.Contains(t, review, "y is off: the lesson-check keys do not validate")
		assert.Contains(t, review, "mode enforce requires check")
		assert.Contains(t, review, "CHECK IMPACT", "the impact shows next to the refusal")

		h.press("y")
		assert.Zero(t, f.callCount("CommitAuthor:edit"))
	})

	t.Run("a repository without lesson checks keeps the form", func(t *testing.T) {
		f := pinsFake()
		h := pinsOpen(t, f, 120, 40)
		pinsCursorTo(h, 5)
		h.press("i")
		require.NotNil(t, authorOf(h).form)

		text := formText(h)
		assert.NotContains(t, text, "Mode")
		assert.Equal(t, maintain.AuthorRequest{}.Mode, authorOf(h).form.draft.request().Mode)
	})
}

// checkDefOpen opens the definition form of M on pin n of checkFake.
func checkDefOpen(t *testing.T, n int, plan func(maintain.CheckDefRequest) *maintain.CheckDefPlan) (*harness, *fakeServices, *[]maintain.CheckDefRequest) {
	t.Helper()

	f := checkFake(t)

	var reqs []maintain.CheckDefRequest

	f.planCheckDef = func(req maintain.CheckDefRequest) (*maintain.CheckDefPlan, error) {
		reqs = append(reqs, req)

		return plan(req), nil
	}
	f.commitCheckDef = func(*maintain.CheckDefPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.Written}, nil
	}

	h := pinsOpen(t, f, 120, 40)
	pinsCursorTo(h, n)
	h.press("M")
	require.NotNil(t, pinsOf(h).checkDef)

	return h, f, &reqs
}

// checkDefText returns the open definition modal on one line.
func checkDefText(t *testing.T, h *harness) string {
	t.Helper()

	m := pinsOf(h).checkDef
	require.NotNil(t, m, "no definition form is open")

	v := m.content(modalWidthOf(h))
	parts := append([]string{v.Title, v.Right}, v.Body...)
	parts = append(parts, v.Tail...)
	parts = append(parts, h.m.st.theme.Hints(v.Actions))

	return lesFlat(ansi.Strip(strings.Join(parts, "\n")))
}

func TestTUICheckDefinitionFormPreviewsImpact(t *testing.T) {
	before := checkLessonsYAML
	after := strings.Replace(before, `pattern: 'TODO\('`, `pattern: 'FIXME'`, 1)

	cfgBefore, cfgAfter := checkRaw(t, before), checkRaw(t, after)
	impact, err := lessoncheck.ImpactOf(cfgBefore, cfgAfter, []int{2, 3})
	require.NoError(t, err)
	require.Len(t, impact, 1, "the advisory pin that binds the ID gets no block")

	ready := func(req maintain.CheckDefRequest) *maintain.CheckDefPlan {
		return &maintain.CheckDefPlan{
			Request: req, Root: "/work/repo", Before: []byte(before), After: []byte(after),
			Diff:  "--- a/.seamark/lessons.yaml\n+++ b/.seamark/lessons.yaml\n@@ -6 +6 @@\n-    pattern: 'TODO\\('\n+    pattern: 'FIXME'\n",
			Added: 1, Removed: 1, Predicate: "no added line matches FIXME", Impact: impact,
			Write: true, Confirmable: true,
		}
	}

	t.Run("an edit of a bound definition shows the impact before y", func(t *testing.T) {
		h, f, reqs := checkDefOpen(t, 2, ready)

		text := checkDefText(t, h)
		assert.Contains(t, text, "ID no-todo", "M fills the definition of the cursor pin")
		assert.Contains(t, text, "no-added-pattern/1")
		assert.Contains(t, text, `Pattern TODO\(`)

		// Pattern is the last row. ctrl+u is not bound, so backspace
		// removes the old pattern rune by rune.
		h.press("end", "enter")

		for range len(`TODO\(`) {
			h.press("backspace")
		}

		h.typeText("FIXME")
		h.press("enter", "s")

		require.Len(t, *reqs, 1)
		assert.Equal(t, maintain.CheckDefRequest{ID: "no-todo", Kind: "no-added-pattern/1", Pattern: "FIXME"}, (*reqs)[0])

		review := checkDefText(t, h)
		assert.Contains(t, review, "Replaces the definition under checks in .seamark/lessons.yaml")
		assert.Contains(t, review, "PREDICATE no added line matches FIXME")
		assertImpactBeforeConfirm(t, review, impact, "y write the definition")
		assert.Contains(t, review, "DIFF")

		h.press("y")
		assert.Equal(t, 1, f.callCount("CommitCheckDef"))
		assert.Contains(t, checkDefText(t, h), "Check definition written")
		assert.False(t, h.m.st.busy(), "the result frees the mutation slot")

		h.press("enter")
		assert.Nil(t, pinsOf(h).checkDef)
	})

	t.Run("a kind without a pattern sends none", func(t *testing.T) {
		h, _, reqs := checkDefOpen(t, 2, ready)

		h.press("down", "enter")
		assert.Contains(t, checkDefText(t, h), "(forbid-path-change/1 takes no pattern)")

		h.press("s")
		require.Len(t, *reqs, 1)
		assert.Equal(t, maintain.CheckDefRequest{ID: "no-todo", Kind: "forbid-path-change/1"}, (*reqs)[0])
	})

	t.Run("a blocked plan never commits", func(t *testing.T) {
		h, f, _ := checkDefOpen(t, 4, func(req maintain.CheckDefRequest) *maintain.CheckDefPlan {
			return &maintain.CheckDefPlan{Request: req, Blockers: []maintain.AuthorBlocker{{
				Kind: maintain.BlockCheck, Text: `checks: check id "Bad ID" must match [a-z0-9][a-z0-9._-]{0,63}`,
				Route: "correct the kind or the pattern of the definition",
			}}}
		})

		assert.Contains(t, checkDefText(t, h), "ID (empty)", "a pin without a check starts an empty form")

		h.press("enter")
		h.typeText("Bad ID")
		h.press("enter", "s")

		review := checkDefText(t, h)
		assert.Contains(t, review, `check id "Bad ID" must match`)
		assert.Contains(t, review, "y is off")

		h.press("y")
		assert.Zero(t, f.callCount("CommitCheckDef"))

		h.press("esc")
		assert.Contains(t, checkDefText(t, h), "ID Bad ID", "esc returns to the form with its values")
	})

	t.Run("a write of lessons.yaml closes the review", func(t *testing.T) {
		h, f, _ := checkDefOpen(t, 2, ready)
		h.press("s")
		require.True(t, pinsOf(h).checkDef.showsPreview())

		h.send(lessonsSavedMsg{at: time.Now(), by: "Lessons › Proposals"})
		assert.False(t, pinsOf(h).checkDef.active(), "the review read the old bytes")
		assert.Contains(t, h.m.toast.text, "so the check-definition review closed")

		h.press("y")
		assert.Zero(t, f.callCount("CommitCheckDef"))
	})
}

// TestTUICheckDefinitionFormLocksHostileText covers R-3 of the P4
// review. A definition in lessons.yaml can hold terminal escapes in its
// ID or pattern. The form must keep both rows read-only, name the
// by-hand route, and never print a raw control byte.
func TestTUICheckDefinitionFormLocksHostileText(t *testing.T) {
	const hostile = "X\x1b]0;PWNED\aY\x1b[2JZ"

	hostilePattern := strings.Replace(checkLessonsYAML, `pattern: 'TODO\('`, `pattern: "X\e]0;PWNED\aY\e[2JZ"`, 1)
	require.NotEqual(t, checkLessonsYAML, hostilePattern)

	// open binds the modal to pin 2 of the inspection that edit changed.
	open := func(t *testing.T, text string, edit func(*maintain.Inspection)) *harness {
		t.Helper()

		in, err := checkInspectionOf(text)
		require.NoError(t, err)

		if edit != nil {
			edit(in)
		}

		f, err := checkFakeOf()
		require.NoError(t, err)

		h := pinsOpen(t, f, 120, 40)
		pinsOf(h).checkDef = newCheckDefModal(h.m.st, in, &in.Pins[1])

		return h
	}

	assertInert := func(t *testing.T, h *harness) {
		t.Helper()

		raw := h.m.View()
		for _, bad := range []string{"\x1b", "\x07", "\u009b"} {
			assert.NotContains(t, raw, bad)
		}
	}

	rows := []struct {
		name  string
		text  string
		edit  func(*maintain.Inspection)
		row   func(*checkDefModal) int
		value func(*checkDefModal) string
	}{
		{
			name: "pattern", text: hostilePattern,
			row:   (*checkDefModal).patternRow,
			value: func(m *checkDefModal) string { return m.pattern.Value() },
		},
		{
			name: "id",
			edit: func(in *maintain.Inspection) {
				def := in.Checks.Defs["no-todo"]
				in.Checks.Defs[hostile] = def
				in.Pins[1].Check.Check.ID = hostile
			},
			row:   func(*checkDefModal) int { return checkDefRowID },
			value: func(m *checkDefModal) string { return m.id.Value() },
		},
	}

	for _, c := range rows {
		t.Run(c.name+" row stays read-only", func(t *testing.T) {
			text := c.text
			if text == "" {
				text = checkLessonsYAML
			}

			h := open(t, text, c.edit)
			m := pinsOf(h).checkDef
			assertInert(t, h)

			m.row = c.row(m)
			h.press("enter")

			assert.False(t, m.editing, "a locked row takes no keys")
			assert.Contains(t, h.m.toast.text, "Read-only: "+pinTextLock)
			assertInert(t, h)

			h.press("a", "backspace")
			assert.Equal(t, hostile, c.value(m), "keys never change the stored text")

			shown := checkDefText(t, h)
			assert.Contains(t, shown, "Read-only: "+pinTextLock)
			assert.Contains(t, shown, "U+001B", "the row shows the escape as a name")
		})
	}

	t.Run("a clean definition stays editable", func(t *testing.T) {
		h := open(t, checkLessonsYAML, nil)
		m := pinsOf(h).checkDef
		m.row = m.patternRow()
		h.press("enter")
		assert.True(t, m.editing)
	})
}

// checkRaw decodes text with the lenient reader.
func checkRaw(t *testing.T, text string) *reviews.Config {
	t.Helper()

	cfg, _, err := lessoncheck.Parse([]byte(text))
	require.NoError(t, err)

	return cfg.Raw
}

// verifyFixture is a scripted verification result of one passing pin.
func verifyFixture() *lessoncheck.Verification {
	return &lessoncheck.Verification{
		Schema: "lesson-verification/1", Caller: lessoncheck.CallerTUI, Freshness: lessoncheck.FreshnessFresh,
		Summary:   lessoncheck.Summary{State: lessoncheck.SummaryPassed, PinsEvaluated: 1, Pass: 1},
		Selection: lessoncheck.Selection{Description: "every changed path"},
		Config:    lessoncheck.ConfigReport{Valid: true, LessonChecksVersion: 1},
	}
}

func TestTUIVerifyLessonsMarksStale(t *testing.T) {
	f := checkFake(t)
	fresh := lessoncheck.FreshnessFresh

	var revalErr error

	f.verifyLessons = func(_ context.Context, paths []string) (*lessoncheck.Verification, error) {
		assert.Empty(t, paths, "V verifies the default selection")

		return verifyFixture(), nil
	}
	f.revalidateLessons = func(context.Context, *lessoncheck.Verification) (lessoncheck.Freshness, error) {
		return fresh, revalErr
	}

	h := pinsOpen(t, f, 120, 40)
	h.press("V")
	require.NotNil(t, pinsOf(h).verify)
	assert.Equal(t, 1, f.callCount("VerifyLessons"))

	text := verifyText(t, h)
	assert.Contains(t, text, "Lesson checks")
	assert.Contains(t, text, "lesson checks: passed")
	assert.NotContains(t, text, "Stale")

	h.press("r")
	assert.Equal(t, 1, f.readCount("RevalidateLessons"))
	assert.NotContains(t, verifyText(t, h), "Stale", "a fresh binding keeps the result")

	revalErr = errors.New("git: index.lock exists")
	h.press("r")
	assert.Contains(t, verifyText(t, h), "The freshness of this result is unknown: git: index.lock exists")
	assert.NotContains(t, verifyText(t, h), "Stale", "a failed read proves nothing")

	// A refresh of the pins asks the engine again; a changed input marks
	// the result stale.
	revalErr, fresh = nil, lessoncheck.FreshnessStale
	h.send(refreshMsg{kinds: []sectionKind{secPins}})

	text = verifyText(t, h)
	assert.Contains(t, text, "Stale: the working tree, lessons.yaml, or a check changed after this verification")
	assert.Contains(t, text, "passed, stale")
	assert.Equal(t, 1, f.callCount("VerifyLessons"), "a refresh never runs the checkers")

	reads := f.readCount("RevalidateLessons")
	h.press("r")
	assert.Equal(t, reads, f.readCount("RevalidateLessons"), "a stale result needs no read")

	h.press("V")
	assert.Equal(t, 2, f.callCount("VerifyLessons"))
	assert.NotContains(t, verifyText(t, h), "Stale", "V runs the checkers again")

	h.press("esc")
	assert.Nil(t, pinsOf(h).verify)
}

// verifyText returns the open verification view on one line.
func verifyText(t *testing.T, h *harness) string {
	t.Helper()

	v := pinsOf(h).verify
	require.NotNil(t, v, "no verification view is open")

	o := v.content(modalWidthOf(h))
	parts := append([]string{o.Title, o.Right}, o.Body...)
	parts = append(parts, h.m.st.theme.Hints(o.Actions))

	return lesFlat(ansi.Strip(strings.Join(parts, "\n")))
}

// modalWidthOf is the body width of a modal at the harness size.
func modalWidthOf(h *harness) int {
	return ui.ModalBodyWidth(ui.ModalMinW, h.m.st.width)
}

// checkEngineYAML holds one enforce pin on generated and its check.
const checkEngineYAML = `lesson_checks_version: 1
checks:
  frozen:
    kind: forbid-path-change/1
pin:
  - rule: frozen
    region: generated
    note: Do not edit generated files.
    mode: enforce
    check: frozen
`

// checkEngine returns the production services over a git workspace with
// one commit and checkEngineYAML, with the write gate on. Only the agent
// and the review fetch are fakes, and no test may call them (AC-1).
func checkEngine(t *testing.T) (workspace.Workspace, services.All, *servicestest.Agent, *servicestest.Fetch) {
	t.Helper()

	root := t.TempDir()
	servicestest.Git(t, root, "init", "-b", "main")

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	servicestest.Write(t, ws, ".git/info/exclude", ".seamark/\n")
	servicestest.Write(t, ws, "generated/a.txt", "a\n")
	servicestest.Git(t, root, "add", "-A")
	servicestest.Git(t, root, "commit", "-m", "base")
	servicestest.Write(t, ws, pinfile.File, checkEngineYAML)
	servicestest.Write(t, ws, ".seamark/config.yaml", "distill:\n  write: true\n")

	agent, fetch := &servicestest.Agent{}, &servicestest.Fetch{}
	svc := services.NewWithDeps(ws, "test", "/usr/local/bin/seamark", services.Deps{
		Fetch: fetch.Fetch, Invoke: agent, Now: func() time.Time { return servicestest.Now },
	})

	return ws, svc, agent, fetch
}

// installClaudeGuard installs the Claude Code enforce hooks on the
// temporary root of ws, with a link named seamark to the test binary as
// the hook binary. The setup engine runs in the test process; no hook
// runs, and the main checkout never sees the setup.
func installClaudeGuard(t *testing.T, ws workspace.Workspace) {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.Symlink(exe, link))

	plan, err := integration.PlanSetup(integration.Builtin(), integration.SetupRequest{
		Root: ws.Root, Binary: link,
		Clients: []integration.ClientSetup{{ClientID: integration.ClaudeID, Hooks: true, LessonChecks: hooks.LessonChecksEnforce}},
	})
	require.NoError(t, err)

	_, err = integration.ApplySetup(plan, integration.ApplyOptions{})
	require.NoError(t, err)
}

// auditCallers returns the caller of each record of the lesson-check
// audit stream of ws. The writer rotates the live log by size and by the
// age of its oldest record. A rotation moves the earlier records of a
// test into the rotated generation, so the helper reads both files,
// oldest first.
func auditCallers(t *testing.T, ws workspace.Workspace) []lessoncheck.Caller {
	t.Helper()

	live := filepath.Join(ws.Root, ".seamark", "lesson-check-audit.jsonl")

	var out []lessoncheck.Caller

	for _, path := range []string{live + ".1", live} {
		data, err := os.ReadFile(path)
		if path != live && errors.Is(err, os.ErrNotExist) {
			continue
		}

		require.NoError(t, err)

		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			var rec lessoncheck.AuditRecord
			require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
			out = append(out, rec.Caller)
		}

		require.NoError(t, sc.Err())
	}

	return out
}

func TestTUIVerifyLessonsMatchesCLI(t *testing.T) {
	ws, svc, agent, _ := checkEngine(t)
	servicestest.Write(t, ws, "generated/a.txt", "edited\n")

	h := newHarness(t, svc, 200, 60)
	h.press("3", "m", "V")

	v := pinsOf(h).verify
	require.NotNil(t, v)
	require.NoError(t, v.err)
	require.NotNil(t, v.res)
	assert.Equal(t, lessoncheck.CallerTUI, v.res.Caller)
	assert.Equal(t, lessoncheck.SummaryViolation, v.res.Summary.State)

	// The CLI runs the same service with the caller cli and prints the
	// shared renderer (lessons --verify, runLessonsVerify).
	cli, err := lessoncheck.Verify(context.Background(), lessoncheck.VerifyRequest{
		Root: ws.Root, Caller: lessoncheck.CallerCLI, SeamarkVersion: "test",
	})
	require.NoError(t, err)

	var want bytes.Buffer
	lessoncheck.RenderText(&want, cli)

	// A wide body wraps no line, so each line of the view is a line of
	// the CLI text.
	got := stripLines(v.content(400).Body)
	for i := range got {
		got[i] = strings.TrimRight(got[i], " ")
	}

	assert.Equal(t, strings.Split(strings.TrimRight(want.String(), "\n"), "\n"), got)
	assert.Equal(t, []lessoncheck.Caller{lessoncheck.CallerTUI, lessoncheck.CallerCLI}, auditCallers(t, ws),
		"one record per verification, each with its caller")
	assert.Zero(t, agent.Count())
}

func TestTUILessonChecksCallNoInvoker(t *testing.T) {
	ws, svc, agent, fetch := checkEngine(t)
	installClaudeGuard(t, ws)

	h := newHarness(t, svc, 160, 50)
	h.press("3", "m")

	// The CHECK section reads the real capability report.
	text := pinsDetail(t, h)
	assert.Contains(t, text, "Mode enforce")
	assert.Contains(t, text, "claude enforced-unverified")

	// The verification and its freshness read.
	h.press("V")
	require.NotNil(t, pinsOf(h).verify)
	require.NotNil(t, pinsOf(h).verify.res)
	h.press("r", "esc")

	// The definition review plans with the engine and writes nothing
	// without y.
	h.press("M", "s")
	require.NotNil(t, pinsOf(h).checkDef)
	require.True(t, pinsOf(h).checkDef.showsPreview(), "plan error: %v", pinsOf(h).checkDef.err)
	h.press("esc", "esc")

	// The pin form plans a mode edit with the engine.
	h.press("i", "end", "up", "enter", "s")
	require.NotNil(t, authorOf(h).review)
	h.press("esc", "esc")

	// The Integrations detail shows the lesson-check section.
	h.press("2")
	assert.Contains(t, lesBoxFlat(h.view()), "LESSON CHECKS")

	assert.Zero(t, agent.Count(), "no lesson-check surface of the console calls a model")
	assert.Empty(t, fetch.Called(), "no lesson-check surface of the console fetches")
}
