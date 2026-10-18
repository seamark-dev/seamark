package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the shared state helpers: the generation that drops
// stale background results, and the wide layout of the terminal.

func TestGenerationAcceptsOnlyTheNewestTicket(t *testing.T) {
	var g generation

	assert.False(t, g.started(), "the zero value has issued no ticket")
	assert.True(t, g.current(0), "the zero value is ready to use")

	first := g.start()
	assert.True(t, g.started())
	assert.True(t, g.current(first))

	second := g.start()
	assert.NotEqual(t, first, second, "every request gets its own ticket")
	assert.False(t, g.current(first), "a newer request makes the older result stale")
	assert.True(t, g.current(second))
}

func TestGenerationCancelDropsTheOpenRequest(t *testing.T) {
	var g generation

	ticket := g.start()
	g.cancel()
	assert.False(t, g.current(ticket), "a result after a cancel is stale")

	next := g.start()
	assert.True(t, g.current(next), "a new request after a cancel applies")
	assert.False(t, g.current(ticket), "an old ticket never becomes current again")
}

func TestGenerationCancelAloneCountsAsStarted(t *testing.T) {
	var g generation

	g.cancel()
	assert.True(t, g.started(), "a cancel also moves the generation")
	assert.False(t, g.current(0), "the zero ticket is stale after a cancel")
}

func TestGenerationLatestNamesTheNewestTicket(t *testing.T) {
	var g generation

	assert.Equal(t, 0, g.latest(), "the zero value has issued no ticket")

	first := g.start()
	assert.Equal(t, first, g.latest())

	g.cancel()
	assert.Greater(t, g.latest(), first, "a cancel moves the newest ticket past every issued one")
	assert.False(t, g.current(first))
}

// TestStateLoadDropsAnOlderResultOfTheSameSource proves the section
// guard: a second load of a source makes the result of the first one
// stale, so store keeps the newer snapshot.
func TestStateLoadDropsAnOlderResultOfTheSameSource(t *testing.T) {
	st := &state{svc: newFake()}

	st.load(secStats)
	first := st.gens[secStats].latest()

	st.load(secStats)
	second := st.gens[secStats].latest()

	assert.Greater(t, second, first, "every load gets its own ticket")
	assert.False(t, st.store(loadedMsg{kind: secStats, gen: first}), "the older load is stale")
	assert.True(t, st.store(loadedMsg{kind: secStats, gen: second, value: oneRecordStats()}), "the newest load applies")
	assert.True(t, st.stats.loaded)
}

func TestStateWideFollowsTheTerminalWidth(t *testing.T) {
	for _, tc := range []struct {
		width int
		wide  bool
	}{
		{width: 0, wide: false},
		{width: ui.MinWidth, wide: false},
		{width: ui.WideWidth - 1, wide: false},
		{width: ui.WideWidth, wide: true},
		{width: 200, wide: true},
	} {
		st := &state{width: tc.width}
		assert.Equal(t, tc.wide, st.wide(), "width %d", tc.width)
	}
}
