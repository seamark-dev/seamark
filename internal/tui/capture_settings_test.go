package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// settingsStarter is a configuration like the one that a team keeps: a
// comment, every owned section, and two exclude patterns.
const settingsStarter = `# Seamark workspace configuration
index:
  generated: false
  exclude:
    - "*.pb.go"
    - internal/gen/
reviews:
  window_days: 730
`

// settingsScene is one Settings state for the visual review. It holds the
// file content, the glyph set, and the keys that reach the state after
// Settings opens.
type settingsScene struct {
	name    string
	content string
	ascii   bool
	drive   func(h *harness, svc *settingsSvc)
}

// settingsCaptureScenes are the Settings states of design map §10 to §10.3. Their
// numbers start at 51, so they never collide with the other scenes.
func settingsCaptureScenes() []settingsScene {
	pinWrite := func(h *harness) {
		h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
		h.press("enter", "down", "space", "enter")
	}

	return []settingsScene{
		{name: "51-settings-list", content: settingsStarter},
		{name: "52-settings-editor-draft", drive: func(h *harness, _ *settingsSvc) {
			h.press("enter", "down", "space")
		}},
		{name: "53-settings-custom-command", content: "agent:\n  argv:\n    - my-agent\n    - --json\n    - \"-\"\n",
			drive: func(h *harness, _ *settingsSvc) {
				h.press("enter", "down", "space")
			}},
		{name: "54-settings-diff-argv-removal", content: "agent:\n  argv:\n    - my-agent\n    - --json\n    - \"-\"\n",
			drive: func(h *harness, _ *settingsSvc) {
				h.press("enter", "down", "space", "enter", "s")
			}},
		// d removes the agent.cli key, but the custom command stays in
		// effect. The preview says so and names no default.
		{name: "54b-settings-cli-reset-under-argv", content: "agent:\n  cli: codex\n  argv: [my-agent, --json]\n",
			drive: func(h *harness, _ *settingsSvc) {
				h.press("d", "s")
			}},
		{name: "55-settings-window-days", content: settingsStarter, drive: func(h *harness, _ *settingsSvc) {
			// The cursor starts on the days choice, because the file
			// holds a day count.
			h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
			h.press("enter", "space", "backspace", "backspace", "backspace")
			h.typeText("0")
			h.press("enter")
		}},
		{name: "56-settings-exclude-editor", content: settingsStarter, drive: func(h *harness, _ *settingsSvc) {
			h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
			h.press("down", "enter", "a")
			h.typeText("a/**/b")
			h.press("enter")
		}},
		{name: "57-settings-leave-prompt", content: settingsStarter, drive: func(h *harness, _ *settingsSvc) {
			h.press("enter", "down", "space", "enter", "3")
		}},
		// A quit with a draft asks first, because the quit discards it.
		{name: "57b-settings-quit-draft", content: settingsStarter, drive: func(h *harness, _ *settingsSvc) {
			h.press("enter", "down", "space", "enter", "q")
		}},
		{name: "58-settings-blocked", content: "agent:\n  cli: codex\nagent:\n  cli: claude\n"},
		// A section behind an alias blocks one group. The reader still
		// resolves the value, but the key and its source are unknown.
		{name: "58b-settings-section-alias", content: "base: &b\n  cli: codex\nagent: *b\n"},
		// The index reader rejects the file, so the editor marks no
		// choice and gives the pattern list no count.
		{name: "58c-settings-unreadable-section", content: "reviews:\n  window_days: \"30\"\nindex:\n" +
			"  generated: true\n  exclude:\n    - \"*.pb.go\"\n",
			drive: func(h *harness, _ *settingsSvc) {
				h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
				h.press("down", "enter")
			}},
		{name: "59-settings-busy", content: settingsStarter, drive: func(h *harness, _ *settingsSvc) {
			h.m.st.begin("Distilling proposals")
			pinWrite(h)
		}},
		{name: "60-settings-stale-ascii", content: settingsStarter, ascii: true, drive: func(h *harness, svc *settingsSvc) {
			pinWrite(h)
			h.press("s")
			settingsWrite(h.t, svc.root, settingsStarter+"# changed elsewhere\n")
			h.press("y")
		}},
	}
}

// TestCaptureSettings writes the frames of the Settings scenes at
// every capture size into SEAMARK_TUI_CAPTURE_DIR, like
// TestCaptureRepresentativeStates. It also checks that every frame fits
// its terminal.
//
// The workspace is "repo" under a temporary home, so the preset
// commands read "-C ~/repo" and the frames stay the same on every run.
func TestCaptureSettings(t *testing.T) {
	dir := captureDir()

	for _, sc := range settingsCaptureScenes() {
		for _, size := range captureSizes {
			w, hgt := size[0], size[1]

			home := t.TempDir()
			t.Setenv("HOME", home)

			root := filepath.Join(home, "repo")
			require.NoError(t, os.MkdirAll(root, 0o755))

			svc := settingsFakeAt(t, root, sc.content)

			h := &harness{t: t, m: newModel(svc, ui.NewTheme(sc.ascii))}
			h.run(h.m.Init())
			h.send(teaSize(w, hgt))
			h.press("5")

			if sc.drive != nil {
				sc.drive(h, svc)
			}

			frame := h.view()
			assertFrame(t, frame, w, hgt)

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(hgt) + ".txt"
			saveCapture(t, dir, name, frame)
		}
	}
}
