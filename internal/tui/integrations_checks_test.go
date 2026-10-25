package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
)

// The tests of the read-only "Lesson checks" section of the Integrations
// detail (contract K10, AC-34), and of the frames that a repository
// without lesson checks keeps (P4.10 Preserve).

// lessonDetailText returns the detail of the inspection of client on one
// line, rendered wide enough that no phrase wraps.
func lessonDetailText(t *testing.T, h *harness, client string) string {
	t.Helper()

	s, ok := h.m.screens[destIntegrations].(*integrations)
	require.True(t, ok)

	for _, insp := range h.m.st.integrations.value {
		if insp.ClientID == client {
			return lesFlat(ansi.Strip(strings.Join(s.detailLines(insp, 300), "\n")))
		}
	}

	require.Failf(t, "no inspection", "client %s", client)

	return ""
}

func TestTUILessonChecksSetupView(t *testing.T) {
	t.Run("the installed guard of a client and the inactive request of another", func(t *testing.T) {
		ws, svc, agent, _ := checkEngine(t)
		installClaudeGuard(t, ws)

		h := newHarness(t, svc, 160, 50)
		before := servicestest.Tree(t, ws.Root)
		h.press("2")

		claude := lessonDetailText(t, h, integration.ClaudeID)
		arrow := h.m.st.theme.Glyphs.Arrow

		for _, want := range []string{
			"LESSON CHECKS Installed enforce Config config valid",
			"pre-write Edit in root: active-unverified (native-deny-unverified, native-timeout-unverified)",
			"pre-write MultiEdit in child: active-unverified (native-deny-unverified, native-timeout-unverified)",
			"post-edit Write in root: active-unverified",
			"Binary test · this seamark binary",
			"Uncovered NotebookEdit in root, NotebookEdit in child, Bash in root, Bash in child: no lesson-check hook",
			"Remove " + arrow + " seamark init --client claude --lesson-checks off --lesson-exploration off",
			"Downgrade Before you install an older seamark, remove every lesson-check handler with this binary: " +
				arrow + " seamark init --lesson-checks off --lesson-exploration off",
		} {
			assert.Contains(t, claude, want)
		}

		assert.NotContains(t, claude, "Set up", "the installed mode covers the enforce pin")

		gate, section, shared := strings.Index(claude, "GATE"), strings.Index(claude, "LESSON CHECKS"),
			strings.Index(claude, "SHARED RESOURCES")
		assert.True(t, gate < section && section < shared, "the section follows the gate")

		codex := lessonDetailText(t, h, integration.CodexID)
		assert.Contains(t, codex, "LESSON CHECKS Installed off", "an enforce pin shows the section of every client")
		assert.Contains(t, codex, "Set up "+arrow+" seamark init --client codex --lesson-checks enforce")
		assert.Contains(t, codex, "not-installed")
		assert.NotContains(t, codex, "Remove", "no handler to remove")

		// The view is read-only: no key of the section writes.
		assert.Equal(t, before, servicestest.Tree(t, ws.Root), "browsing the section writes nothing")
		assert.Zero(t, agent.Count())
	})

	t.Run("a repository without lesson checks keeps the detail", func(t *testing.T) {
		detail := func(report lessoncheck.CapabilityReport) map[string]string {
			f := newFake()
			f.integrations = integMatrix()
			f.lessonCapability = report

			h := newHarness(t, f, 160, 50)
			h.press("2")

			out := map[string]string{}
			for _, insp := range f.integrations {
				out[insp.ClientID] = lessonDetailText(t, h, insp.ClientID)
			}

			return out
		}

		plain := detail(lessoncheck.CapabilityReport{})
		joined := detail(checkReport(t, checkPlainYAML))

		assert.Equal(t, plain, joined, "a report without an executable pin and an installed hook adds nothing")

		for client, text := range joined {
			assert.NotContains(t, text, "LESSON CHECKS", client)
		}
	})
}

func TestTUIOverviewUnchangedWithoutLessonChecks(t *testing.T) {
	// The lesson-check capabilities stay out of the configured list, so
	// no inspection synthesizes an Overview item for them (contract K10).
	for _, c := range []integration.Capability{
		integration.CapabilityLessonVerify, integration.CapabilityLessonGuard, integration.CapabilityLessonExploration,
	} {
		assert.NotContains(t, integration.ConfiguredCapabilities, c)
	}

	frames := func(report lessoncheck.CapabilityReport, w, hgt int) (overview, integ string) {
		f := newFake()
		f.integrations = integMatrix()
		f.ledger = lesLedger()
		f.pins = checkInspection(t, checkPlainYAML)
		f.lessonCapability = report

		h := newHarness(t, f, w, hgt)
		overview = h.view()

		h.press("2")

		return overview, h.view()
	}

	for _, size := range captureSizes {
		w, hgt := size[0], size[1]

		plainOverview, plainInteg := frames(lessoncheck.CapabilityReport{}, w, hgt)
		joinedOverview, joinedInteg := frames(checkReport(t, checkPlainYAML), w, hgt)

		assert.Equal(t, plainOverview, joinedOverview, "the Overview of a set-up repository without checks at %dx%d", w, hgt)
		assert.Equal(t, plainInteg, joinedInteg, "the Integrations rows at %dx%d", w, hgt)
		assert.NotContains(t, strings.ToLower(joinedOverview), "lesson-guard")
		assert.NotContains(t, strings.ToLower(joinedOverview), "lesson check")
	}
}
