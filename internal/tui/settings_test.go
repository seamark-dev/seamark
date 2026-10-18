package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// settingsRich is a configuration with every owned section, comments
// and an unknown key, like a file that a team keeps by hand.
const settingsRich = `# Seamark workspace configuration
agent:
  cli: codex
distill:
  write: true # pins need a review
reviews:
  window_days: 180
index:
  generated: false
  exclude:
    - "*.pb.go"
    - internal/gen/
custom: kept
`

// settingsSvc is the fake services with the real settings package. A
// read inspects the file again, a plan runs the patch planner, and a
// save writes the file. The tests can then check the exact bytes.
type settingsSvc struct {
	*fakeServices

	root string
	reg  *integration.Registry
}

// Settings inspects the workspace file on every read, as the engine
// does, so a change on disk shows at the next load.
func (s *settingsSvc) Settings() (*settings.Snapshot, error) {
	s.count("Settings")

	return settings.Inspect(s.root, s.reg)
}

// settingsFake builds services over a temporary workspace whose
// config.yaml holds content. An empty content leaves the file absent.
func settingsFake(t *testing.T, content string) *settingsSvc {
	t.Helper()

	return settingsFakeAt(t, t.TempDir(), content)
}

// settingsFakeAt builds the services of settingsFake over root.
func settingsFakeAt(t *testing.T, root, content string) *settingsSvc {
	t.Helper()

	if content != "" {
		settingsWrite(t, root, content)
	}

	svc := &settingsSvc{fakeServices: newFake(), root: root, reg: integration.Builtin()}
	svc.planSettings = func(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
		return settings.PlanPatch(snap, svc.reg, edits)
	}
	svc.saveSettings = settings.Save

	return svc
}

// settingsWrite writes the configuration file of a workspace.
func settingsWrite(t testing.TB, root, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(settings.File))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// settingsBytes returns the configuration file, or "" when it is absent.
func settingsBytes(t *testing.T, svc *settingsSvc) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(settings.File)))
	if os.IsNotExist(err) {
		return ""
	}

	require.NoError(t, err)

	return string(data)
}

// settingsOpen starts a console over svc and opens Settings.
func settingsOpen(t testing.TB, svc services.All, w, h int) *harness {
	t.Helper()

	hs := newHarness(t, svc, w, h)
	hs.press("5")
	require.Equal(t, destSettings, hs.m.active)

	return hs
}

// settingsScr returns the Settings screen of a console.
func settingsScr(hs *harness) *settingsScreen {
	return hs.m.screens[destSettings].(*settingsScreen)
}

// settingsLine returns the first frame line that holds every part, or "".
func settingsLine(frame string, parts ...string) string {
	for _, l := range strings.Split(frame, "\n") {
		all := true

		for _, p := range parts {
			if !strings.Contains(l, p) {
				all = false

				break
			}
		}

		if all {
			return l
		}
	}

	return ""
}

// settingsBox returns the text inside the box whose top border starts
// with "╭─ "+title, flattened to single spaces. A phrase that the box
// wraps then still matches, and the text of a box beside it or under a
// modal stays out.
func settingsBox(frame, title string) string {
	lines := strings.Split(frame, "\n")

	for i, l := range lines {
		runes := []rune(l)

		start := strings.Index(l, "╭─ "+title)
		if start < 0 {
			continue
		}

		col := len([]rune(l[:start]))

		end := col
		for end < len(runes) && runes[end] != '╮' {
			end++
		}

		var parts []string

		for _, next := range lines[i+1:] {
			row := []rune(next)
			if col >= len(row) || row[col] == '╰' {
				break
			}

			parts = append(parts, string(row[col+1:min(end, len(row))]))
		}

		return lesFlat(strings.Join(parts, " "))
	}

	return ""
}

// settingsNoWrites checks that nothing planned or saved a patch.
func settingsNoWrites(t *testing.T, svc *settingsSvc) {
	t.Helper()

	assert.Zero(t, svc.callCount("SaveSettings"), "no save without y")
}

func TestSettingsListShowsEveryFieldWithItsSource(t *testing.T) {
	svc := settingsFake(t, settingsRich)
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	assert.Contains(t, frame, "╭─ Settings ")
	assert.Contains(t, frame, ".seamark/config.yaml")

	for _, heading := range []string{"INFERENCE", "PIN WRITING", "EVIDENCE", "INDEXING"} {
		assert.Contains(t, frame, heading)
	}

	assert.NotEmpty(t, settingsLine(frame, "Field", "Value", "Set by"), "the column header shows at 120×40")
	assert.NotEmpty(t, settingsLine(frame, "› agent.cli", "codex", "config.yaml"))
	assert.NotEmpty(t, settingsLine(frame, "agent.argv", "not set", "—"))
	assert.NotEmpty(t, settingsLine(frame, "distill.write", "on", "config.yaml"))
	assert.NotEmpty(t, settingsLine(frame, "reviews.window_days", "180 days", "config.yaml"))
	assert.NotEmpty(t, settingsLine(frame, "index.generated", "skip", "config.yaml"))
	assert.NotEmpty(t, settingsLine(frame, "index.exclude", "2 patterns", "config.yaml"))

	// The detail of the selected field names every value and its source.
	flat := settingsBox(frame, "agent.cli")
	for _, want := range []string{
		"VALUE", "Stored codex", "Default claude", "Effective codex", "Source .seamark/config.yaml, line 3",
		"Runs codex exec", "TAKES EFFECT", "At the next prepared distillation or trigger extraction.", "CHOOSE",
		// Trigger extraction runs the same resolved command, so the
		// texts name it too.
		"Selects the preset command that distillation and trigger extraction run.",
	} {
		assert.Contains(t, flat, want)
	}

	assert.Contains(t, settingsBox(frame, "Settings"),
		"A change stays a draft until you save it with s and y.")
	assert.Contains(t, footerLine(frame), "enter edit")
	assert.Contains(t, footerLine(frame), "d default")
	assert.NotContains(t, footerLine(frame), "s review save", "no draft, so s is disabled")
	assert.Contains(t, footerLine(frame), "r refresh", "the list does not capture keys")

	for _, size := range [][2]int{{80, 24}, {60, 20}, {200, 50}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "agent.cli", "size %v", size)
	}

	assert.Empty(t, svc.called(), "browsing plans and saves nothing")
	assert.Positive(t, svc.readCount("Settings"))
}

func TestSettingsShowsTheOverrideAndUnreadableValues(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: bogus\n  argv: [my-agent, --json, --token=hunter2secret, \"-\"]\n"+
		"reviews:\n  window_days: \"30\"\n")
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	flat := settingsBox(frame, "agent.cli")

	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "bogus", "agent.argv"), "the preset shows as overridden")
	assert.Contains(t, flat, "A custom agent.argv overrides the preset:")
	assert.Contains(t, flat, "(●) custom")
	assert.Contains(t, flat, `"bogus" is not a registered preset.`)
	assert.NotContains(t, frame, "hunter2secret", "a secret-shaped argv flag stays redacted")

	h.press("down", "down", "down")
	frame = h.view()
	assert.NotEmpty(t, settingsLine(frame, "reviews.window_days", "unknown", "unreadable"),
		"a reader failure is unknown, never the default")
	assert.Contains(t, settingsBox(frame, "reviews.window_days"), "Seamark cannot read this value")
	assert.Contains(t, settingsBox(frame, "Settings"), "cannot be edited here", "a failing reader blocks its group")
	assert.Empty(t, svc.called())
}

// Review fix: the list cell of an agent.cli that agent.argv overrides
// showed the reader value unredacted. A wrong value, such as a command
// with a token flag, then put the token on the screen.
func TestSettingsRedactsASecretInTheOverriddenCLICell(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: x --token=zz9\n  argv: [my-agent, \"-\"]\n")
	h := settingsOpen(t, svc, 120, 40)

	text, known := settingsScr(h).currentValue(settings.AgentCLI)
	assert.True(t, known)
	assert.Equal(t, "x --token=[REDACTED]", text)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.NotContains(t, frame, "zz9", "size %v: the token never reaches the screen", size)
		assert.NotEmpty(t, settingsLine(frame, "agent.cli", "x --token="), "size %v: the list keeps the value", size)
	}

	assert.Empty(t, svc.called())
}

func TestSettingsMissingFileShowsDefaults(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "claude", "default"))
	assert.NotEmpty(t, settingsLine(frame, "reviews.window_days", "730 days", "default"))
	assert.NotEmpty(t, settingsLine(frame, "index.exclude", "none", "default"))
	assert.Contains(t, settingsBox(frame, "Settings"), "No .seamark/config.yaml yet. Saving creates it with the chosen keys.")
	assert.Contains(t, settingsBox(frame, "agent.cli"), "built-in default; no .seamark/config.yaml yet")
}

func TestSettingsIntentSelectsTheGroup(t *testing.T) {
	svc := settingsFake(t, settingsRich)
	h := newHarness(t, svc, 120, 40)
	scr := settingsScr(h)

	for intent, want := range map[string]settings.Field{
		"group:evidence": settings.ReviewWindow, "group:indexing": settings.IndexGenerated,
		"group:pinwrite": settings.DistillWrite, "group:inference": settings.AgentCLI,
		"group:reviews": settings.ReviewWindow, "group:agent": settings.AgentCLI,
	} {
		h.press("1")
		h.send(navigateMsg{dest: destSettings, intent: intent})
		require.Equal(t, destSettings, h.m.active)
		assert.Equal(t, want, scr.field(), intent)
		assert.False(t, scr.editing, "an intent opens the list, not the editor")
	}

	h.send(navigateMsg{dest: destSettings, intent: "group:inference"})
	h.send(navigateMsg{dest: destSettings, intent: "group:nothing"})
	assert.Equal(t, settings.AgentCLI, scr.field(), "an unknown group keeps the cursor")
	assert.Empty(t, svc.called())
}

func TestSettingsCompactLayoutsUseOnePane(t *testing.T) {
	svc := settingsFake(t, settingsRich)
	h := settingsOpen(t, svc, 80, 24)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.NotContains(t, frame, "VALUE", "the compact list has no detail pane")
	assert.NotEmpty(t, settingsLine(frame, "index.exclude", "2 patterns"))

	h.press("enter")
	frame = h.view()
	assertFrame(t, frame, 80, 24)
	assert.Contains(t, frame, "╭─ agent.cli ")
	assert.Contains(t, frame, "esc back to the list")
	assert.Contains(t, footerLine(frame), "space select")

	h.press("esc")
	assert.NotContains(t, h.view(), "esc back to the list")

	// The minimum size keeps the selected field and the first tail
	// line. The list of both documents is longer than the box, so the
	// table scrolls with the cursor, and every field is reachable.
	h.resize(60, 20)

	for i, f := range settingsFields {
		frame = h.view()
		assertFrame(t, frame, 60, 20)
		assert.Contains(t, frame, string(f), "the minimum list shows %s", f)

		// The workspace has no lessons.yaml, so the first tail line of a
		// lessons field says so.
		tail := "A change stays a draft"
		if f.Document() == pinfile.File {
			tail = "No .seamark/lessons.yaml yet."
		}

		assert.Contains(t, frame, tail, "the first tail line of %s stays", f)

		if i < len(settingsFields)-1 {
			h.press("down")
		}
	}

	h.press("home", "down", "down", "down", "down", "down", "enter")
	frame = h.view()
	assertFrame(t, frame, 60, 20)
	assert.Contains(t, frame, "PATTERNS")
	assert.Empty(t, svc.called())
}

func TestSettingsASCIIFramesHoldOnlyASCII(t *testing.T) {
	svc := settingsFake(t, settingsRich)

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h := &harness{t: t, m: newModel(svc, ui.NewTheme(true))}
		h.run(h.m.Init())
		h.resize(size[0], size[1])
		h.press("5", "enter", "down", "space")

		for _, keys := range [][]string{nil, {"esc", "s"}, {"esc", "3"}} {
			h.press(keys...)

			frame := h.view()
			assertFrame(t, frame, size[0], size[1])

			for i, r := range frame {
				if r > 127 {
					require.Failf(t, "non-ASCII glyph", "size %v keys %v: %q at %d", size, keys, r, i)
				}
			}
		}

		h.press("esc")
	}
}

func TestSettingsHostileValuesStayInert(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: \"\\e[31mred\\e]0;owned\\a\"\n  argv: [\"\\e[2Jwipe\", \"-\"]\n"+
		"index:\n  exclude:\n    - \"\\e[5mblink/\"\n")
	h := settingsOpen(t, svc, 120, 40)

	// The last step opens the exclude list editor: the cursor goes to
	// the indexing group, and its second field is index.exclude.
	for _, keys := range [][]string{nil, {"enter"}, {"esc", "down", "enter"}, {"esc", "group:indexing", "down", "enter"}} {
		for _, k := range keys {
			if name, ok := strings.CutPrefix(k, "group:"); ok {
				h.send(navigateMsg{dest: destSettings, intent: "group:" + name})

				continue
			}

			h.press(k)
		}

		raw := h.m.View()
		assert.NotContains(t, raw, "\x1b]0;", "no title sequence from the file")
		assert.NotContains(t, raw, "\x1b[2J", "no clear-screen sequence from the file")
		assert.NotContains(t, raw, "\x1b[5m", "no blink sequence from the file")
		assert.NotContains(t, raw, "\a")
	}

	assert.Contains(t, lesFlat(h.view()), "blink/", "the pattern text still shows")
	assert.Empty(t, svc.called())
}

func TestSettingsUnreadableSnapshotShowsTheRoute(t *testing.T) {
	f := newFake()
	h := settingsOpen(t, f, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "Settings are not readable")
	assert.Contains(t, settingsBox(frame, "Settings"), "edit .seamark/config.yaml or .seamark/lessons.yaml by hand")

	h.press("enter", "d", "s", "x")
	assert.Empty(t, f.called(), "no key plans or saves without a snapshot")

	// The first read reads both settings documents, so the wait names
	// both.
	h.m.st.settings = section[*settings.Snapshot]{}
	assert.Contains(t, lesFlat(settingsBox(h.view(), "Settings")), "Seamark reads .seamark/config.yaml and "+
		".seamark/lessons.yaml with the shared readers.")
}

// settingsProbe is a stub destination that records the saves that the
// shell broadcasts: the settings saves and the writes of lessons.yaml.
type settingsProbe struct {
	stubScreen

	saved   []settingsSavedMsg
	lessons []lessonsSavedMsg
}

// update records every settingsSavedMsg and every lessonsSavedMsg.
func (p *settingsProbe) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case settingsSavedMsg:
		p.saved = append(p.saved, m)
	case lessonsSavedMsg:
		p.lessons = append(p.lessons, m)
	}

	return nil
}
