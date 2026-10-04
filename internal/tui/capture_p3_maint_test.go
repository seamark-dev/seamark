package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/status"
)

// The captures of the retarget and prune preview (scenes 80-89, contracts
// D-24). Each scene opens Lessons › Pins over the fixture of
// pins_modal_fixture_test.go and drives the preview through the B1 hook
// openMaintMsg, as the Pins keys w and - do. The Pins list behind the
// preview is in the state that sends the request, so a capture never
// shows a backdrop that contradicts the preview.

// maintScene is one capture: a fake built for the test, the ASCII theme,
// and the keys after the preview opened.
type maintScene struct {
	name  string
	ascii bool
	fake  func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request)
	drive func(h *harness)
}

// maintSelect puts the Pins list in the state that sends req, as a user
// does before w or -: the cursor on the pin of a cursor request, or
// every requested pin marked with space and the cursor on the first.
func maintSelect(h *harness, req maintain.Request) {
	cursorTo := func(index int) {
		h.press("home")

		for range index {
			h.press("down")
		}
	}

	if !req.Cursor {
		for _, ref := range req.Refs {
			cursorTo(ref.Index)
			h.press("space")
		}
	}

	if len(req.Refs) > 0 {
		cursorTo(req.Refs[0].Index)
	}
}

// maintSceneOf opens the preview of plan p with commit as the commit.
func maintSceneOf(plan func(t *testing.T, fx maintFix) *maintain.Plan,
	commit func(fx maintFix) func(*maintain.Plan) (maintain.Result, error),
) func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
	return func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
		p := plan(t, fx)

		var c func(*maintain.Plan) (maintain.Result, error)
		if commit != nil {
			c = commit(fx)
		}

		f := maintFakeOf(fx, p, c)
		f.planRecovery = func(r *maintain.Recovery) (*maintain.Plan, error) { return fx.recoveryPlan(r, true), nil }

		return f, p.Request
	}
}

func maintScenes() []maintScene {
	narrow := func(t *testing.T, fx maintFix) *maintain.Plan {
		return fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	}
	partial := func(fx maintFix) func(*maintain.Plan) (maintain.Result, error) { return maintPartial(fx, true) }
	stale := func(maintFix) func(*maintain.Plan) (maintain.Result, error) {
		return func(q *maintain.Plan) (maintain.Result, error) {
			return maintain.Result{Action: q.Action}, fmt.Errorf("%w: the pin file changed", maintain.ErrStale)
		}
	}

	return []maintScene{
		{name: "80-maint-retarget", fake: maintSceneOf(narrow, nil)},
		{name: "80b-maint-retarget-broader", fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintWide(t)
		}, nil)},
		{name: "80c-maint-retarget-ascii", ascii: true, fake: maintSceneOf(narrow, nil)},
		{name: "80d-maint-retarget-stale-index", fake: func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
			f, req := maintSceneOf(narrow, nil)(t, fx)

			// D-16: a stale index still plans, and the preview says so.
			st := *f.index.Status
			st.Freshness = status.FreshStale
			f.index.Status = &st

			return f, req
		}},
		{name: "81-maint-retarget-live-note", fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.plan(t, maintain.ActionRetarget, maintPlanOpts{write: true, cursor: true}, maintSpec{
				entry: maintNilErrors, status: maintain.ItemReady, to: []string{"internal/api/handlers"},
				narrower: true, facts: []string{"trigger internal/api/handlers/user.go changed together with the " +
					"evidence in 3 commit(s); it delivers to internal/api/handlers"},
			})
		}, nil), drive: func(h *harness) {
			// The preview learns its height from a render, as on screen.
			h.view()
			h.press("pgdown")
		}},
		{name: "82-maint-retarget-revise", fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintMixed(t)
		}, nil)},
		{name: "82b-maint-retarget-collision", fake: func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
			p, f := maintCollision(t, fx)

			return f, p.Request
		}},
		{name: "82c-maint-foreign-root", fake: func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
			f, req := maintSceneOf(narrow, nil)(t, fx)

			// The index of a copied checkout records the original root.
			f.pins.Index.Root = "/work/original"

			return f, req
		}},
		{name: "83-maint-gate-off", fake: func(t *testing.T, fx maintFix) (*fakeServices, maintain.Request) {
			f, req := maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
				return fx.maintNarrow(t, maintPlanOpts{cursor: true})
			}, nil)(t, fx)

			// The Lessons tab line reads the same gate as the plan.
			f.ledger.Write = false

			return f, req
		}},
		{name: "84-maint-prune-kept", fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintPrune(t, true)
		}, nil)},
		{name: "84b-maint-prune-retirement", fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintPrune(t, false)
		}, nil)},
		{name: "84c-maint-prune-ascii", ascii: true, fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintPrune(t, true)
		}, nil)},
		{name: "85-maint-committing", fake: maintSceneOf(narrow, nil), drive: func(h *harness) {
			// The commit command is not run, so the stage stays.
			maintHostOf(h).modal.commit()
		}},
		{name: "86-maint-success", fake: maintSceneOf(narrow, nil), drive: func(h *harness) { h.press("y") }},
		{name: "87-maint-partial-recovery", fake: maintSceneOf(narrow, partial), drive: func(h *harness) {
			h.press("y")
		}},
		{name: "87b-maint-partial-ascii", ascii: true, fake: maintSceneOf(func(t *testing.T, fx maintFix) *maintain.Plan {
			return fx.maintPrune(t, false)
		}, func(fx maintFix) func(*maintain.Plan) (maintain.Result, error) { return maintPartial(fx, false) }),
			drive: func(h *harness) { h.press("y") }},
		{name: "88-maint-recovery", fake: maintSceneOf(narrow, partial), drive: func(h *harness) {
			h.press("y", "c")
		}},
		{name: "89-maint-stale", fake: maintSceneOf(narrow, stale), drive: func(h *harness) { h.press("y") }},
	}
}

// TestCaptureP3Maint writes the frames of the maintenance scenes at
// every capture size into SEAMARK_TUI_CAPTURE_DIR, like
// TestCaptureRepresentativeStates. Without the variable it checks that
// every frame fits its terminal, that the raw view holds no escape
// sequence, and that an ASCII scene is pure ASCII.
func TestCaptureP3Maint(t *testing.T) {
	dir := os.Getenv("SEAMARK_TUI_CAPTURE_DIR")
	fx := maintFixture(t)

	for _, sc := range maintScenes() {
		for _, size := range captureSizes {
			w, hgt := size[0], size[1]

			f, req := sc.fake(t, fx)

			h := &harness{t: t, m: newModel(f, newTheme(sc.ascii))}
			h.run(h.m.Init())
			h.send(teaSize(w, hgt))
			h.press("3", "m")
			maintSelect(h, req)

			// The backdrop agrees with the preview: the marks are the
			// request, and the gate of the tab line is the gate of the plan.
			marked := 0
			if !req.Cursor {
				marked = len(req.Refs)
			}

			require.Len(t, pinsOf(h).selected, marked, "scene %s marks the requested pins", sc.name)
			require.Equal(t, req.Refs[0], pinsOf(h).cursor.ref, "scene %s puts the cursor on its pin", sc.name)

			h.send(openMaintMsg{req: req})

			if sc.drive != nil {
				sc.drive(h)
			}

			raw := h.m.View()
			assert.NotContains(t, raw, "\x1b", "scene %s at %dx%d", sc.name, w, hgt)
			assert.NotContains(t, raw, "\x07", "scene %s at %dx%d", sc.name, w, hgt)
			assert.NotContains(t, raw, "\u009b", "scene %s at %dx%d", sc.name, w, hgt)

			frame := h.view()
			assertFrame(t, frame, w, hgt)
			assert.NotContains(t, frame, "no index", "scene %s shows the indexed database", sc.name)

			if sc.name == "83-maint-gate-off" {
				assert.NotContains(t, frame, "distill.write on", "the tab line agrees with the preview")
			}

			if sc.ascii {
				for _, r := range frame {
					require.Less(t, r, rune(128), "scene %s at %dx%d", sc.name, w, hgt)
				}
			}

			if dir == "" {
				continue
			}

			name := sc.name + "-" + strconv.Itoa(w) + "x" + strconv.Itoa(hgt) + ".txt"
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(frame+"\n"), 0o644))
		}
	}
}
