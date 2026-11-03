package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
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

// TestTUILessonChecksUnreadableDocument proves that the section tells
// an unreadable hook document from a document without a hook. It uses
// the words of doctor. It stays visible without an executable pin,
// because an unreadable document can hold a hook. It names no binary
// and offers no setup command, because setup stops at that file.
func TestTUILessonChecksUnreadableDocument(t *testing.T) {
	settings := func(t *testing.T, ws workspace.Workspace, rel, text string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(filepath.Join(ws.Root, ".claude"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ws.Root, filepath.FromSlash(rel)), []byte(text), 0o600))
	}

	t.Run("an absent document reads off", func(t *testing.T) {
		_, svc, _, _ := checkEngine(t)

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		claude := lessonDetailText(t, h, integration.ClaudeID)
		assert.Contains(t, claude, "LESSON CHECKS Installed off Config")
		assert.Contains(t, claude, "not-installed")
		assert.Contains(t, claude, "Set up", "a readable document can take the install")
	})

	t.Run("a valid document without a lesson-check hook reads off", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		settings(t, ws, ".claude/settings.json", `{"hooks":{}}`)

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		assert.Contains(t, lessonDetailText(t, h, integration.ClaudeID), "LESSON CHECKS Installed off Config")
	})

	t.Run("an installed document reads its mode", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		installClaudeGuard(t, ws)

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		claude := lessonDetailText(t, h, integration.ClaudeID)
		assert.Contains(t, claude, "LESSON CHECKS Installed enforce Config")
		assert.Contains(t, claude, "Binary test")
		assert.NotContains(t, claude, "unreadable")
	})

	t.Run("an unreadable document reads unreadable", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		installClaudeGuard(t, ws)
		settings(t, ws, ".claude/settings.json", "{ not json")

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		claude := lessonDetailText(t, h, integration.ClaudeID)
		assert.Contains(t, claude, "LESSON CHECKS Installed unreadable (.claude/settings.json: ")
		assert.NotContains(t, claude, "Installed off")
		assert.Contains(t, claude, "pre-write Edit in root: inactive (document-unreadable)")
		assert.NotContains(t, claude, "Set up", "setup stops at a file that does not parse")
		assert.NotContains(t, claude, "Binary", "the hooks of the file are unknown")
		assert.NotContains(t, claude, "Remove")

		codex := lessonDetailText(t, h, integration.CodexID)
		assert.Contains(t, codex, "LESSON CHECKS Installed off", "the other client keeps its own state")
		assert.Contains(t, codex, "Set up")
	})

	t.Run("an unreadable document keeps the section without an executable pin", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		servicestest.Write(t, ws, pinfile.File, checkPlainYAML)
		settings(t, ws, ".claude/settings.json", "{ not json")

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		assert.Contains(t, lessonDetailText(t, h, integration.ClaudeID), "LESSON CHECKS Installed unreadable (.claude/settings.json: ")
		assert.NotContains(t, lessonDetailText(t, h, integration.CodexID), "LESSON CHECKS", "the rule keeps the detail of a readable client")
	})

	t.Run("an unreadable supplemental source qualifies the installed mode", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		installClaudeGuard(t, ws)
		settings(t, ws, ".claude/settings.local.json", "{ not json")

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		claude := lessonDetailText(t, h, integration.ClaudeID)
		assert.Contains(t, claude, "LESSON CHECKS Installed enforce (a hook source is unreadable) Config")
		assert.Contains(t, claude, "Binary test", "the shared file still names the binary")
	})

	t.Run("a long reason wraps inside the column", func(t *testing.T) {
		ws, svc, _, _ := checkEngine(t)
		settings(t, ws, ".claude/settings.json", "{ not json")

		h := newHarness(t, svc, 160, 50)
		h.press("2")

		s, ok := h.m.screens[destIntegrations].(*integrations)
		require.True(t, ok)

		for _, insp := range h.m.st.integrations.value {
			for _, line := range s.lessonCheckLines(insp, 48) {
				assert.LessOrEqual(t, ansi.StringWidth(line), 48, insp.ClientID)
			}
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

		// The status bar shows the wall clock. The two renders can fall in
		// different seconds, so the comparison leaves the clock out.
		return footerClock.ReplaceAllString(overview, "hh:mm:ss"), footerClock.ReplaceAllString(h.view(), "hh:mm:ss")
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

// binaryReport joins checkLessonsYAML with one guard path of Claude Code
// whose hook binary has the given state. It uses the real join, so the
// reasons are the reasons that an inspection of that binary gives. Each
// state keeps its other-binary reason beside the specific one.
func binaryReport(t *testing.T, state, version string) lessoncheck.CapabilityReport {
	t.Helper()

	cfg, errs, err := lessoncheck.Parse([]byte(checkLessonsYAML))
	require.NoError(t, err)

	return lessoncheck.CapabilityReportOf(cfg, errs, []lessoncheck.InstalledPath{{
		Client: integration.ClaudeID, Tool: "Edit", Stage: lessoncheck.StagePreWrite, Context: lessoncheck.ContextRoot,
		Installed: true, Marker: lessoncheck.MarkerGuard, BinaryState: state, BinaryVersion: version,
		Verification: lessoncheck.CapVerification{Level: "unverified"},
	}}, nil)
}

// TestTUILessonChecksBinaryWords proves that the section names each state
// of the hook binary in its own words. A hook binary that is not this one
// keeps the path from its handler, so the words tell the reader why a
// requested pin stays inactive. Each state carries other-binary, so a
// lost case falls through to the words of another state.
func TestTUILessonChecksBinaryWords(t *testing.T) {
	cases := []struct {
		name, state, version string
		path, binary         string
	}{
		{
			name: "an older binary", state: lessoncheck.BinaryOld, version: "v0.8.0",
			path:   "pre-write Edit in root: inactive (capability-version-old, ",
			binary: "Binary v0.8.0 · older than " + lessoncheck.MinHandlerRelease + ": it has no lesson-check handler",
		},
		{
			name: "a missing binary", state: lessoncheck.BinaryMissing,
			path:   "pre-write Edit in root: inactive (binary-missing, ",
			binary: "Binary missing: the hook names no executable file",
		},
		{
			name: "a binary without a readable version", state: lessoncheck.BinaryUnknown,
			path:   "pre-write Edit in root: inactive (capability-unknown, ",
			binary: "Binary version unknown: another binary",
		},
		{
			name: "a release at or above the first handler", state: lessoncheck.BinaryOther, version: "v0.10.0",
			path:   "pre-write Edit in root: active-unverified (",
			binary: "Binary v0.10.0 · another seamark binary",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.integrations = integMatrix()
			f.lessonCapability = binaryReport(t, c.state, c.version)

			h := newHarness(t, f, 160, 50)
			h.press("2")

			claude := lessonDetailText(t, h, integration.ClaudeID)
			assert.Contains(t, claude, c.path)
			assert.Contains(t, claude, c.binary)
			assert.NotContains(t, claude, "this seamark binary")
		})
	}
}
