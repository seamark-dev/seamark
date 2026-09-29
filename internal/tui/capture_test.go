package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// captureScene is one representative console state for the visual
// review (AC-11): a fake, the keys that reach the state, and a name.
type captureScene struct {
	name  string
	fake  func() *fakeServices
	ascii bool
	// drive moves the console into the state after it opens.
	drive func(h *harness)
}

// captureSizes are the terminal sizes of every capture: the 1a design
// size, the common 80×24, the documented minimum, and a large terminal.
var captureSizes = [][2]int{{120, 40}, {80, 24}, {minWidth, minHeight}, {200, 50}}

func captureScenes() []captureScene {
	lesApplyFake := func(write bool, commit func(*proposals.Preview) (proposals.Result, error)) func() *fakeServices {
		return func() *fakeServices {
			f := newFake()
			f.ledger = lesLedger()
			f.ledger.Write = write
			f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
				return lesPreview(a, ids, write), nil
			}
			f.commitDecision = commit

			return f
		}
	}

	partial := func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{
			Action: pv.Action, FileWritten: true, Moved: []int64{1}, NotMoved: []int64{3},
			LedgerErr: errors.New("database is locked"),
		}, nil
	}

	return []captureScene{
		{name: "01-first-run", fake: captureFresh},
		{name: "01b-first-run-plan", fake: captureFresh, drive: func(h *harness) { h.press("enter") }},
		{name: "01c-first-run-result", fake: captureFresh, drive: func(h *harness) { h.press("enter", "y") }},
		{name: "01d-first-run-ascii", fake: captureFresh, ascii: true},
		{name: "02-overview-fresh", fake: func() *fakeServices {
			f := newFake()
			f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}

			return f
		}},
		{name: "03-overview-populated", fake: ovPopulated},
		{name: "04-overview-populated-ascii", fake: ovPopulated, ascii: true},
		{name: "05-overview-ledger-error", fake: func() *fakeServices {
			f := ovPopulated()
			f.ledger, f.ledgerErr = nil, errors.New("database is locked")

			return f
		}},
		{name: "06-index-build-confirm", fake: ovPopulated, drive: func(h *harness) { h.press("b") }},
		{name: "07-integrations", fake: func() *fakeServices {
			f := newFake()
			f.integrations = integMatrix()

			return f
		}, drive: func(h *harness) { h.press("2", "down") }},
		{name: "08-setup-choose", fake: func() *fakeServices { f, _ := setupFake(); return f },
			drive: func(h *harness) {
				h.press("2")
				h.send(navigateMsg{dest: destIntegrations, intent: "setup:codex"})
			}},
		{name: "09-setup-preview", fake: func() *fakeServices { f, _ := setupFake(); return f },
			drive: func(h *harness) {
				h.press("2")
				h.send(navigateMsg{dest: destIntegrations, intent: "setup:codex"})
				h.press("enter", "down", "down")
			}},
		{name: "10-setup-confirm", fake: func() *fakeServices { f, _ := setupFake(); return f },
			drive: func(h *harness) {
				h.press("2")
				h.send(navigateMsg{dest: destIntegrations, intent: "setup:codex"})
				h.press("enter", "a")
			}},
		{name: "11-setup-result-failed", fake: func() *fakeServices {
			f, _ := setupFake()
			f.applySetup = func(*SetupPreview) (integration.SetupResult, error) {
				return integration.SetupResult{Ops: []integration.OpResult{
					{Path: ".seamark/policy.yaml", Status: integration.OpApplied, Created: true},
					{Path: ".codex/config.toml", Status: integration.OpFailed, Err: errors.New("permission denied"), Consumers: []string{"codex"}},
					{Path: ".codex/hooks.json", Status: integration.OpNotAttempted, Consumers: []string{"codex"}},
				}}, errors.New(".codex/config.toml: permission denied")
			}

			return f
		}, drive: func(h *harness) {
			h.press("2")
			h.send(navigateMsg{dest: destIntegrations, intent: "setup:codex"})
			h.press("enter", "a", "y")
		}},
		{name: "12-proposals", fake: lesApplyFake(true, nil), drive: func(h *harness) { h.press("3", "space") }},
		{name: "13-apply-show-yaml", fake: lesApplyFake(false, nil), drive: func(h *harness) { h.press("3", "space", "a") }},
		{name: "14-apply-preview", fake: lesApplyFake(true, nil), drive: func(h *harness) { h.press("3", "space", "a") }},
		{name: "15-apply-partial", fake: lesApplyFake(true, partial),
			drive: func(h *harness) { h.press("3", "space", "down", "down", "space", "a", "y") }},
		{name: "16-dismiss-preview", fake: lesApplyFake(true, nil), drive: func(h *harness) { h.press("3", "space", "d") }},
		{name: "17-proposals-no-index", fake: func() *fakeServices {
			f := newFake()
			f.ledgerErr = ErrNoLedger

			return f
		}, drive: func(h *harness) { h.press("3") }},
		{name: "18-stats", fake: func() *fakeServices {
			f := newFake()
			f.stats = captureStats()

			return f
		}, drive: func(h *harness) { h.press("3", "s") }},
		{name: "19-stats-no-log", fake: func() *fakeServices {
			f := newFake()
			f.stats = lessonstats.Snapshot{LogPath: "/work/repo/.seamark/lessons-audit.jsonl"}

			return f
		}, drive: func(h *harness) { h.press("3", "s") }},
		{name: "20-stats-log-error", fake: func() *fakeServices {
			f := newFake()
			f.stats = lessonstats.Snapshot{
				LogPath: "/work/repo/.seamark/lessons-audit.jsonl", LogExists: true,
				LogErr: errors.New("read .seamark/lessons-audit.jsonl: is a directory"),
			}

			return f
		}, drive: func(h *harness) { h.press("3", "s") }},
		{name: "21-help", fake: ovPopulated, drive: func(h *harness) { h.press("?") }},
		{name: "22-doctor", fake: captureDoctor, drive: func(h *harness) { h.press("4") }},
		{name: "23-doctor-detail", fake: captureDoctor, drive: func(h *harness) { h.press("4", "end", "enter") }},
		{name: "24-doctor-ascii", fake: captureDoctor, ascii: true, drive: func(h *harness) { h.press("4", "down") }},
		{name: "25-doctor-loading", fake: newFake, drive: func(h *harness) {
			h.press("4")
			h.m.st.doctor = section[*doctor.Report]{loading: true}
		}},
		{name: "26-quit-while-busy", fake: ovPopulated, drive: func(h *harness) {
			h.m.st.begin("Building the index")
			h.press("q")
		}},
	}
}

// captureFresh is a fresh workspace with two agents on PATH and a plan
// of the shared documents.
func captureFresh() *fakeServices {
	f := frFake(nil)
	f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}

	return f
}

// captureStats is a readable delivery log with firings on three days.
// It builds on the shell fixture, so the capture does not depend on the
// test helpers of the Stats view.
func captureStats() lessonstats.Snapshot {
	var firings []reviews.Firing

	for i, day := range []string{"2026-07-01", "2026-07-02", "2026-07-02", "2026-07-03", "2026-07-03", "2026-07-03"} {
		firings = append(firings, reviews.Firing{
			TS: day + "T10:00:00Z", File: "api/a" + itoa(i) + ".py",
			Fired: []reviews.FiredLesson{{Region: "api", Symptom: "regenerate the client"}},
		})
	}

	snap := shellBStats()
	snap.Records = len(firings)
	snap.Summary = reviews.Summarize(firings, nil)

	return snap
}

// captureDoctor is a populated workspace with every doctor state.
func captureDoctor() *fakeServices {
	f := ovPopulated()
	f.doctor = docReport()

	return f
}

// TestCaptureRepresentativeStates writes plain-text frames of every
// scene at every capture size into SEAMARK_TUI_CAPTURE_DIR, for the
// maintainer visual review. Without the variable it only checks that
// every frame fits its terminal. `make tui-capture` sets it.
func TestCaptureRepresentativeStates(t *testing.T) {
	dir := os.Getenv("SEAMARK_TUI_CAPTURE_DIR")

	for _, sc := range captureScenes() {
		for _, size := range captureSizes {
			w, h := size[0], size[1]

			hs := &harness{t: t, m: newModel(sc.fake(), newTheme(sc.ascii))}
			hs.run(hs.m.Init())
			hs.send(teaSize(w, h))

			if sc.drive != nil {
				sc.drive(hs)
			}

			frame := hs.view()
			assertFrame(t, frame, w, h)

			if dir == "" {
				continue
			}

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ".txt"
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(frame+"\n"), 0o644))
		}
	}
}

// teaSize builds a window size message.
func teaSize(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}
