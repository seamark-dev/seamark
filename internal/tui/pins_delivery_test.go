package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/reviews"
)

const deliveryPinsYAML = `pin:
  - rule: prose
    region: docs
    note: Keep the docs short.
  - rule: other
    region: src
    note: Wrap errors with context.
`

// deliveryFakeOf uses the real installed-entry pairing, but supplies analyzed
// readings directly. No archive, model or network is involved in a screen test.
func deliveryFakeOf() (*fakeServices, lessonreport.Report, error) {
	in, err := checkInspectionOf(deliveryPinsYAML)
	if err != nil {
		return nil, lessonreport.Report{}, err
	}

	report, err := lessonreport.Build(lessonreport.Inputs{}, "")
	if err != nil {
		return nil, lessonreport.Report{}, err
	}

	report.Capture = lessonreport.CaptureState{State: lessonreport.CaptureAvailable}
	report.AsOf.GeneratedAt = "2026-10-28T12:00:00Z"
	report.AsOf.Archive.State = lessonreport.StateRead
	report.AsOf.Config.State = "ok"
	report.AsOf.Config.Pins = len(in.Pins)

	for _, pin := range in.Pins {
		report.Pins = append(report.Pins, lessonreport.PinReading{
			PinRef: pin.Check.Ref, PinIdentity: reviews.PinIdentityDigest(pin.Entry.Ref().Key),
			PinVersion: pin.Check.Version, Rule: pin.Entry.Rule, Scope: pinEntryRegions(pin.Entry), Mode: "advisory",
			Occurrences: lessonreport.OccurrenceCounts{Occurrences: 2, Incidents: 1,
				ByClass: map[lessonreport.PairClass]int{"at-action": 1, "unknown": 1}},
			Delivery: lessonreport.DeliveryCounts{Sessions: 3},
			Timing:   map[lessonreport.PairTiming]int{"same-call-only": 1, "unknown": 1},
			Observation: lessonreport.Observation{Unit: "delivered sessions", Incomplete: 2, Linked: 1,
				IncompleteByReason: map[lessonreport.Code]int{"lane-coverage-unknown": 2}},
			Limitations: []lessonreport.Code{"independence-unresolved"},
		})
	}

	old := strings.Repeat("a", 64)
	first := report.Pins[0]
	report.Historical = []lessonreport.PinReading{{PinRef: first.PinRef, PinIdentity: first.PinIdentity, PinVersion: old}}
	report.Expanded = &lessonreport.Expansion{
		PinRef: first.PinRef, Versions: []string{first.PinVersion, old},
		Occurrences: []lessonreport.OccurrenceDetail{
			{AssociationRef: "as-123456789abc", PinVersion: first.PinVersion, EpisodeRef: "123456789abc@1",
				Family: "peer", Origin: "peer", Assessment: "live-v2", Review: "counted", Class: "at-action", Timing: "same-call-only",
				Limitations: []lessonreport.Code{"independence-unresolved"},
				Actions: []lessonreport.ActionDetail{{ActionRef: "abcdef012345", Lane: "child", Class: "at-action", Timing: "same-call-only",
					TimingReason: "boundary-unverified", Paths: []string{"docs/guide.md"}}}},
			{AssociationRef: "as-fedcba987654", PinVersion: old, EpisodeRef: "fedcba987654@2", Family: "user", Origin: "human",
				Review: "excluded", Class: "unknown", Reason: "missing-corrected-action", Timing: "unknown"},
		},
		Sessions: []lessonreport.SessionWindow{{PinVersion: first.PinVersion, SessionRef: "987654321abc", Lane: "child",
			Partition: "incomplete", Timing: "unknown", Reasons: []lessonreport.Code{"lane-coverage-unknown"}}},
	}

	in.Pins[0].Link = pinsInspection().Pins[0].Link
	in.Pins[0].Readings.Outcome = maintain.OutcomeReading{Known: true, Reading: outcome.Reading{
		Exposed: true, Firings: 41, Matches: 41, PostEvents: 7, Verdict: outcome.VerdictNotLanding,
	}}

	f := pinsFake()
	f.pins = in
	f.delivery = func(req lessonreport.Request) (lessonreport.Report, error) {
		r := report
		if req.PinRef == "" {
			r.Expanded = nil
		}

		return r, nil
	}

	return f, report, nil
}

func deliveryFake(t testing.TB) (*fakeServices, lessonreport.Report) {
	t.Helper()

	f, report, err := deliveryFakeOf()
	require.NoError(t, err)

	return f, report
}

func deliveryBody(v *pinDelivery) string {
	return ansi.Strip(strings.Join(v.content(1000).Body, "\n"))
}

func TestPinsDeliverySectionShowsCompactLine(t *testing.T) {
	t.Run("whole separate reading of the exact current version", func(t *testing.T) {
		f, report := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		p := pinsOf(h)
		row := p.rows()[0]
		compact, ok := lessonreport.Compact(&report, row.pin.Check.Version)
		require.True(t, ok)
		require.Greater(t, len([]rune(compact.Line)), 200)

		text := ansi.Strip(strings.Join(p.pinDetail(p.inspection(), row, 1000), "\n"))
		assert.Contains(t, text, compact.Line)
		assert.Contains(t, text, "no linked correction observed (qualified)")
		assert.Less(t, strings.Index(text, "Outcome"), strings.Index(text, "Exposure"))
		assert.Less(t, strings.Index(text, "Exposure"), strings.Index(text, "Capture delivery"))
		before := p.readingLines(p.inspection(), row.pin, 1000)
		assert.Contains(t, ansi.Strip(strings.Join(before, "\n")), "not landing")
		assert.Contains(t, pinsKeys(h), "g")
		assert.NotContains(t, pinsKeys(h), "M", "ordinary advisory pins need no native capability badge")

		reads := f.readCount("Delivery")
		for range 3 {
			h.view()
			p.pinDetail(p.inspection(), row, 80)
			p.keys()
		}

		assert.Equal(t, reads, f.readCount("Delivery"), "rendering does no I/O")
		assert.Equal(t, before, p.readingLines(p.inspection(), row.pin, 1000), "outcomes stay unchanged")
	})

	for _, name := range []string{"nil", "unavailable", "unknown version", "historical only", "duplicate report identity", "two current versions", "duplicate installed identity", "unknown pairing"} {
		t.Run(name, func(t *testing.T) {
			f, report := deliveryFake(t)
			switch name {
			case "nil":
				f.delivery = nil
			case "unavailable":
				report.Capture.State = "unavailable"
			case "unknown version":
				report.Pins[0].PinVersion = strings.Repeat("b", 64)
			case "historical only":
				report.Historical = append(report.Historical, report.Pins[0])
				report.Pins = report.Pins[1:]
			case "duplicate report identity":
				report.Pins = append(report.Pins, report.Pins[0])
			case "two current versions":
				dup := report.Pins[0]
				dup.PinVersion = strings.Repeat("c", 64)
				report.Pins = append(report.Pins, dup)
			case "duplicate installed identity":
				f.pins.Pins = append(f.pins.Pins, f.pins.Pins[0])
			case "unknown pairing":
				f.pins.Pins[0].Check.Known = false
			}

			if name != "nil" {
				f.delivery = func(lessonreport.Request) (lessonreport.Report, error) { return report, nil }
			}

			h := pinsOpen(t, f, 120, 40)
			assert.NotContains(t, pinsDetail(t, h), "Capture delivery")
			assert.NotContains(t, pinsKeys(h), "g")
			h.press("g")
			assert.Nil(t, pinsOf(h).delivery)
			assert.Empty(t, f.called())
		})
	}

	t.Run("history rows never guess a version", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		for range 2 {
			h.press("f", "g")
			assert.Nil(t, pinsOf(h).delivery)
			assert.NotContains(t, pinsKeys(h), "g")
		}
	})

	t.Run("content-preserving move survives and removed binding closes", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		h.press("g")
		v := pinsOf(h).delivery
		require.NotNil(t, v)
		f.pins = checkInspection(t, "pin:\n  - rule: other\n    region: src\n    note: Wrap errors with context.\n  - rule: prose\n    region: docs\n    note: Keep the docs short.\n")
		h.send(refreshMsg{kinds: []sectionKind{secPins}})
		assert.Same(t, v, pinsOf(h).delivery)
		assert.Equal(t, f.pins.Pins[1].Entry.Ref(), v.binding)

		f.pins = checkInspection(t, "pin:\n  - rule: other\n    region: src\n    note: Wrap errors with context.\n")
		h.send(refreshMsg{kinds: []sectionKind{secPins}})
		assert.Nil(t, pinsOf(h).delivery)
		assert.Contains(t, h.m.toast.text, "left this list")
		assert.Empty(t, f.called())
	})
}

func TestPinsDeliveryLoadsInOwnWorker(t *testing.T) {
	t.Run("blocked overview leaves independent sources and input usable", func(t *testing.T) {
		f, report := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		started, release := make(chan struct{}), make(chan struct{})
		f.delivery = func(lessonreport.Request) (lessonreport.Report, error) {
			close(started)
			<-release

			return report, nil
		}

		batch := h.m.st.load(allSections...)().(tea.BatchMsg)
		require.Len(t, batch, len(allSections))
		results := make(chan tea.Msg, 1)
		go func() { results <- batch[len(batch)-1]() }()
		<-started
		for _, cmd := range batch[:len(batch)-1] {
			h.run(cmd)
		}

		assert.False(t, h.m.st.pins.loading)
		assert.False(t, h.m.st.ledger.loading)
		assert.False(t, h.m.st.stats.loading)
		assert.True(t, h.m.st.delivery.loading)
		h.press("down")
		row, ok := pinsOf(h).cursorRow(pinsOf(h).rows())
		require.True(t, ok)
		assert.Equal(t, f.pins.Pins[1].Entry.Ref(), row.pin.Entry.Ref())
		close(release)
		h.send(<-results)
		assert.False(t, h.m.st.delivery.loading)
		assert.Empty(t, f.called())
	})

	t.Run("exact dependency expansion with no completion cycle", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		base := f.readCount("Delivery")
		h.send(refreshMsg{kinds: []sectionKind{secPins, secLedger, secLearning, secIntegrations, secDelivery}})
		assert.Equal(t, base+1, f.readCount("Delivery"))
		h.send(refreshMsg{kinds: []sectionKind{secPins, secLedger}})
		assert.Equal(t, base+2, f.readCount("Delivery"))
		h.send(refreshMsg{kinds: []sectionKind{secDoctor, secStats, secSettings, secChecks}})
		assert.Equal(t, base+2, f.readCount("Delivery"))
		h.press("r")
		assert.Equal(t, base+3, f.readCount("Delivery"))
		assert.Nil(t, h.m.st.followChecks(loadedMsg{kind: secDelivery}))
	})

	t.Run("old overview rejected after refresh and exit", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		old := h.m.st.load(secDelivery)()
		h.send(refreshMsg{kinds: []sectionKind{secDelivery}})
		at := h.m.st.delivery.at
		h.send(old)
		assert.Equal(t, at, h.m.st.delivery.at)
		late := h.m.st.load(secDelivery)()
		h.run(h.m.exit())
		h.send(late)
		assert.Equal(t, at, h.m.st.delivery.at)
	})

	t.Run("overview failure retains marked stale reading and blocks g", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		f.delivery = func(lessonreport.Request) (lessonreport.Report, error) {
			return lessonreport.Report{}, errors.New("read failed")
		}
		h.press("r")
		text := pinsDetail(t, h)
		assert.Contains(t, text, "Capture delivery")
		assert.Contains(t, text, "Stale: capture delivery could not be read again")
		assert.NotContains(t, pinsKeys(h), "g")
		h.press("g")
		assert.Nil(t, pinsOf(h).delivery)
		assert.Contains(t, h.m.toast.text, "read failed")
	})
}

// TestPinsDeliveryExpansion browses the expansion of g. Every key only
// reads: the expansion selects occurrences across versions, follows a
// refresh, and shows a selection error as a read failure.
func TestPinsDeliveryExpansion(t *testing.T) {
	t.Run("browse multiple versions without a write", func(t *testing.T) {
		f, _ := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		h.press("g")
		v := pinsOf(h).delivery
		require.NotNil(t, v)
		body := deliveryBody(v)
		for _, text := range []string{"versions are never pooled", "Reminder emitted for an already-constructed action.", "lane-coverage-unknown",
			"same-call-only", "independence-unresolved", "prevented: 0", "no linked correction observed (qualified)"} {
			assert.Contains(t, body, text)
		}

		assert.Equal(t, "as-123456789abc", v.selected.ref)
		h.press("down", "pgdown", "tab", "shift+tab", "x", "u", "X", "U", "y")
		assert.Equal(t, "as-123456789abc", v.selected.ref)
		h.press("tab")
		assert.Equal(t, "as-fedcba987654", v.selected.ref)
		assert.NotContains(t, pinsKeys(h), "X")
		assert.NotContains(t, pinsKeys(h), "U")
		assert.Empty(t, f.called(), "browsing never writes")
		assert.False(t, h.m.st.busy())
	})

	t.Run("loading close refresh and historical selection error", func(t *testing.T) {
		f, report := deliveryFake(t)
		h := pinsOpen(t, f, 120, 40)
		p := pinsOf(h)
		cmd := p.openDelivery()
		v := p.delivery
		require.NotNil(t, v)
		assert.True(t, v.reading.loading)
		assert.Contains(t, deliveryBody(v), "Nothing is written")
		old := cmd()
		h.press("esc")
		h.send(old)
		assert.Nil(t, p.delivery, "late reply cannot reopen a closed view")

		h.press("g")
		v = p.delivery
		require.NotNil(t, v)
		_, refreshCmd := h.m.Update(refreshMsg{kinds: []sectionKind{secPins}})
		assert.False(t, v.ready(), "refresh invalidates the reading before any read completes")
		h.run(refreshCmd)
		cmd = v.reload()
		old = cmd()
		h.send(refreshMsg{kinds: []sectionKind{secDelivery}})
		h.send(old)
		assert.False(t, v.seq.current(old.(pinDeliveryLoadedMsg).seq), "a reply that predates the refresh is dropped")

		f.delivery = func(req lessonreport.Request) (lessonreport.Report, error) {
			if req.PinRef == "" {
				r := report
				r.Expanded = nil

				return r, nil
			}

			return lessonreport.Report{}, &reviews.PinRefError{Kind: reviews.PinRefMissing, Ref: req.PinRef}
		}
		h.press("r")
		require.Error(t, v.reading.err)
		assert.False(t, v.ready())
		assert.Contains(t, deliveryBody(v), "Capture delivery could not be read")
		assert.NotContains(t, deliveryBody(v), "capture evidence unavailable", "a selection failure is not an invented archive failure")
		assert.Empty(t, f.called())
	})

	t.Run("no occurrences", func(t *testing.T) {
		f, report := deliveryFake(t)
		report.Expanded.Occurrences = nil
		f.delivery = func(lessonreport.Request) (lessonreport.Report, error) { return report, nil }
		h := pinsOpen(t, f, 120, 40)
		h.press("g", "tab")
		v := pinsOf(h).delivery
		assert.Contains(t, deliveryBody(v), "No occurrence selected")
		assert.Empty(t, f.called())
	})
}

func TestPinsDeliveryRefreshDependencies(t *testing.T) {
	for _, kind := range []sectionKind{secPins, secLedger, secLearning, secIntegrations} {
		got := deliveryRefreshKinds([]sectionKind{kind})
		assert.Equal(t, []sectionKind{kind, secDelivery}, got)
	}

	kinds := []sectionKind{secPins, secLedger, secDelivery}
	assert.Equal(t, kinds, deliveryRefreshKinds(kinds))
	assert.Equal(t, 1, len(slices.DeleteFunc(slices.Clone(deliveryRefreshKinds(kinds)), func(k sectionKind) bool { return k != secDelivery })))
	assert.Equal(t, []sectionKind{secStats}, deliveryRefreshKinds([]sectionKind{secStats}))
	assert.Nil(t, deliveryRefreshKinds(nil))
}

func TestPinsDeliveryExpansionDisposal(t *testing.T) {
	f, _ := deliveryFake(t)
	h := pinsOpen(t, f, 120, 40)
	cmd := pinsOf(h).openDelivery()
	v := pinsOf(h).delivery
	msg := cmd()
	h.run(h.m.exit())
	h.send(msg)
	assert.Nil(t, pinsOf(h).delivery)
	assert.False(t, v.seq.current(msg.(pinDeliveryLoadedMsg).seq))
	assert.Empty(t, f.called())
	assert.True(t, h.m.st.work.wait(time.Second))
}
