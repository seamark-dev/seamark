package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellCHeavy finds the heavy rule segment in a tab rule line. It
// returns the first rune column and the segment length, or -1 when the
// line holds none.
func shellCHeavy(rule string) (start, n int) {
	start = -1

	for i, r := range []rune(rule) {
		if r != '━' {
			continue
		}

		if start < 0 {
			start = i
		}

		n++
	}

	return start, n
}

func TestShellCTabRowAbbreviatesInTheDocumentedOrder(t *testing.T) {
	// ovPopulated shows the counts 19, 3 and 3: the full row takes 73
	// cells, so 80 columns keep every full name.
	cases := []struct {
		w    int
		want string
	}{
		{120, "  1 Overview 19   2 Integrations   3 Lessons 3   4 Doctor 3   5 Settings"},
		{80, "  1 Overview 19   2 Integrations   3 Lessons 3   4 Doctor 3   5 Settings"},
		{72, "  1 Overview 19   2 Integ   3 Lessons 3   4 Doctor 3   5 Settings"},
		{60, "  1 Over 19   2 Integ   3 Lessons 3   4 Doctor 3   5 Set"},
	}

	for _, tc := range cases {
		h := newHarness(t, ovPopulated(), tc.w, 24)

		lines := strings.Split(h.view(), "\n")
		assert.Equal(t, tc.want, strings.TrimRight(lines[1], " "), "width %d", tc.w)
	}
}

func TestShellCTabRuleStaysUnderTheActiveLabel(t *testing.T) {
	for _, w := range []int{60, 72, 80, 120} {
		h := newHarness(t, ovPopulated(), w, 24)

		for i := range destCount {
			h.press(itoa(int(i) + 1))
			require.Equal(t, i, h.m.active)

			frame := h.view()
			assertFrame(t, frame, w, 24)

			lines := strings.Split(frame, "\n")
			row, rule := []rune(lines[1]), lines[2]

			// The row fits: its last tab is whole, not clipped.
			assert.Regexp(t, `5 Set(tings)? *$`, lines[1], "width %d: the Settings tab is whole", w)

			// The heavy run covers exactly the plain segment of the
			// active tab, at the same columns as the tab row.
			plain, _ := h.m.tabSegment(int(i), h.m.tabShort(w)[i])
			start, n := shellCHeavy(rule)
			require.GreaterOrEqual(t, start, 0, "width %d: the rule marks the active tab", w)
			assert.Equal(t, width(plain), n, "width %d dest %d: the rule is as wide as the segment", w, i)
			require.LessOrEqual(t, start+n, len(row))
			assert.Equal(t, plain, string(row[start:start+n]), "width %d dest %d: the rule sits under the label", w, i)
		}
	}
}

func TestShellCHelpKeepsTheFullNames(t *testing.T) {
	h := newHarness(t, ovPopulated(), 60, 24)

	h.press("2")
	require.Contains(t, strings.Split(h.view(), "\n")[1], "2 Integ ", "the narrow row abbreviates")

	h.press("?")

	frame := h.view()
	assertFrame(t, frame, 60, 24)
	assert.Contains(t, frame, "INTEGRATIONS", "the help group keeps the full name")
	assert.Contains(t, frame, "1–5        jump to tab")

	h.press("esc", "5", "?")
	assert.Contains(t, h.view(), "SETTINGS")
}

func TestShellCHelpNamesThePhaseTwoCommands(t *testing.T) {
	h, _ := shellAStub(t, 120, 40, shellAKeys(3))

	h.press("?", "end")

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	for _, c := range []string{
		"seamark index --fixes-only", "seamark index --reviews", "seamark lessons --distill --dry-run",
		".seamark/config.yaml  settings file",
	} {
		assert.Contains(t, frame, c)
	}
}

func TestShellCSettingsIsTheFifthDestination(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 40)

	h.press("5")
	require.Equal(t, destSettings, h.m.active)
	assert.Equal(t, "Settings", ovDestName(destSettings), "an Overview link names the destination")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "╭─ Settings ", "size %v", size)
		assert.Contains(t, frame, ".seamark/config.yaml", "size %v", size)
		// The fake has no settings snapshot, so the view shows the
		// unreadable state (the Settings tests cover the real views).
		assert.Contains(t, frame, "Settings are not readable", "size %v", size)
	}

	h.resize(120, 40)
	assert.Contains(t, shellAFooter(h.view()), "r refresh", "the view does not capture keys")

	h.press("1")
	h.send(navigateMsg{dest: destSettings, intent: "group:agent"})
	assert.Equal(t, destSettings, h.m.active, "a settings intent opens the destination")
	assert.Empty(t, f.called(), "opening Settings plans and writes nothing")
}
