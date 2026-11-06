package tui

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
)

func TestPinsDeliveryExpansionReadIsolation(t *testing.T) {
	f, report := deliveryFake(t)
	h := pinsOpen(t, f, 120, 40)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var expansions atomic.Int32
	f.delivery = func(req lessonreport.Request) (lessonreport.Report, error) {
		r := report
		if req.PinRef == "" {
			r.Expanded = nil

			return r, nil
		}

		if expansions.Add(1) == 1 {
			close(started)
			<-release
			r.AsOf.GeneratedAt = "old read"
		}

		return r, nil
	}

	cmd := pinsOf(h).openDelivery()
	v := pinsOf(h).delivery
	messages := make(chan tea.Msg, 1)
	go func() { messages <- cmd() }()
	<-started
	assert.True(t, v.reading.loading)
	reads := f.readCount("Pins")
	h.press("r")
	assert.Equal(t, reads+1, f.readCount("Pins"), "an expansion read does not block independent refresh")
	assert.False(t, v.reading.loading)
	assert.Equal(t, report.AsOf.GeneratedAt, v.reading.value.AsOf.GeneratedAt)
	once.Do(func() { close(release) })
	h.send(<-messages)
	assert.Equal(t, report.AsOf.GeneratedAt, v.reading.value.AsOf.GeneratedAt, "late expansion cannot replace the refreshed snapshot")
	assert.Empty(t, f.called())
}

func TestPinsDeliveryExpansionRejectsChangedVersion(t *testing.T) {
	f, report := deliveryFake(t)
	h := pinsOpen(t, f, 120, 40)
	f.delivery = func(req lessonreport.Request) (lessonreport.Report, error) {
		r := report
		r.Pins = append([]lessonreport.PinReading(nil), report.Pins...)
		r.Pins[0].PinVersion = strings.Repeat("d", 64)

		return r, nil
	}

	h.press("g")
	v := pinsOf(h).delivery
	require.NotNil(t, v)
	require.ErrorContains(t, v.reading.err, "selected pin changed")
	assert.False(t, v.reading.loaded, "a mismatched report is not rendered as a valid selection")
	h.press("X", "U", "y")
	assert.Empty(t, f.called())
}

func TestPinsDeliveryRefreshKeepsAssociationNotPosition(t *testing.T) {
	f, report := deliveryFake(t)
	f.delivery = func(req lessonreport.Request) (lessonreport.Report, error) {
		r := report
		if req.PinRef == "" {
			r.Expanded = nil
		}

		return r, nil
	}

	h := pinsOpen(t, f, 120, 40)
	h.press("g", "tab")
	v := pinsOf(h).delivery
	selected := v.selected
	report.Expanded.Occurrences[0], report.Expanded.Occurrences[1] = report.Expanded.Occurrences[1], report.Expanded.Occurrences[0]
	h.press("r")
	assert.Equal(t, selected, v.selected, "an occurrence keeps its selection after reordering")
	report.Expanded.Occurrences = report.Expanded.Occurrences[1:]
	h.press("r", "X", "U", "y")
	assert.Empty(t, v.selected, "a removed occurrence must not select the row that moved into its place")
	assert.Empty(t, f.called())
	assert.Contains(t, deliveryBody(v), "No occurrence selected")
}
