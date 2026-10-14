package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the shell side of stoppable work: the run line of the
// notice row, the quit question, and the bounded wait at exit. The
// screens own the work itself; the shell only shows it and stops it.

// exitWait bounds the wait for stoppable work when the console exits.
// A cancelled agent or gh child gets 5 seconds to close its pipes
// (agent.waitDelay); the rest is margin for the last store write. The
// bound stays, so a stuck child never holds the terminal.
const exitWait = 10 * time.Second

// quitCopy returns the title, the body and the actions of the quit
// question, so the dialog, the compact screen and the footer agree.
// compact selects the shorter body of the too-small screen. Without
// running work, the question is about the unsaved drafts. When every
// draft is a Settings draft of config.yaml, the question keeps the
// words of the Settings drafts, which name each group. Any other draft
// makes the question name each file whose draft is lost.
//
// A reload can drop the last draft while the question is open, for
// example when the file now holds the drafted value. The question then
// asks only about the quit, because no draft is lost.
func (m *model) quitCopy(compact bool) (title, body string, acts []key.Binding) {
	files := m.draftFiles()
	settingsOnly := slices.Equal(files, []string{settingsFile})

	if !m.st.busy() {
		if len(files) == 0 {
			return "Quit Seamark?", "No unsaved draft remains. Quitting changes no file.",
				[]key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "quit"), ui.WithHelp(ui.Nav.Back, "esc", "stay")}
		}

		acts = []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "discard and quit"), ui.WithHelp(ui.Nav.Back, "esc", "stay")}

		if !settingsOnly {
			return "Discard the unsaved drafts and quit?", draftLossText(files), acts
		}

		body = m.settingsDrafts() + " Quitting discards every unsaved change; " + settingsFile + " does not change."
		if compact {
			body = "Quitting discards the unsaved settings draft; " + settingsFile + " does not change."
		}

		return "Discard the unsaved settings draft and quit?", body, acts
	}

	// The exit also loses the drafts, so the full question names them.
	// The compact screen has no room for more.
	lost := ""

	switch {
	case compact || len(files) == 0:
	case settingsOnly:
		lost = " The exit also discards the unsaved settings draft."
	default:
		lost = " The exit also discards every unsaved draft of " + joinWords(files) + "."
	}

	keep := ui.WithHelp(ui.Nav.Back, "esc", "keep working")

	if m.st.stoppable() {
		body = m.st.mutation + " is running. Seamark stops the external work, waits for it to end, " +
			"then exits. Completed work is kept." + lost
		if compact {
			body = m.st.mutation + " is running. Seamark stops it, waits for it to end, then exits. " +
				"Completed work is kept."
		}

		return "Stop and exit?", body, []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "stop and exit"), keep}
	}

	body = m.st.mutation + " is running. It cannot stop safely at this point, so Seamark can exit when " +
		"it finishes. Already completed changes remain either way." + lost
	if compact {
		body = m.st.mutation + " is running and cannot stop safely. Completed changes remain either way."
	}

	return "Exit after the running operation?", body,
		[]key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "exit after completion"), keep}
}

// draftLossText says which files lose a draft at a quit, for example
// "Quitting discards every unsaved draft of .seamark/lessons.yaml. The
// file does not change."
func draftLossText(files []string) string {
	rest := " The file does not change."
	if len(files) > 1 {
		rest = " The files do not change."
	}

	return "Quitting discards every unsaved draft of " + joinWords(files) + "." + rest
}

// draftFiles returns the files of every unsaved draft of the session,
// sorted and without repeats. Each screen with drafts names its files
// (draftHolder).
func (m *model) draftFiles() []string {
	var files []string

	for _, s := range m.screens {
		if h, ok := s.(draftHolder); ok {
			files = append(files, h.drafts()...)
		}
	}

	slices.Sort(files)

	return slices.Compact(files)
}

// discardDrafts drops the drafts of every screen.
func (m *model) discardDrafts() {
	for _, s := range m.screens {
		if h, ok := s.(draftHolder); ok {
			h.discardDrafts()
		}
	}
}

// settingsDrafts names the unsaved drafts of the Settings groups, for
// example "Inference has 1 unsaved change: agent.cli.", or returns ""
// when no group holds a draft.
func (m *model) settingsDrafts() string {
	s, ok := m.screens[destSettings].(*settingsScreen)
	if !ok || s.draftTotal() == 0 {
		return ""
	}

	return s.leaveText()
}

// exitBudget returns how long Run may still wait for stoppable work
// after the program ends. A stop-and-exit that already waited in the
// console spends the same budget, so the whole exit stays in exitWait.
func (m *model) exitBudget(now time.Time) time.Duration {
	if m.exitBy.IsZero() {
		return exitWait
	}

	return max(0, min(exitWait, m.exitBy.Sub(now)))
}

// stopWork cancels the root context and waits for the registered
// stoppable work within the exit budget. It reports whether all work
// ended. It prints nothing, because Bubble Tea has restored the
// terminal before Run calls it.
func (m *model) stopWork(cancel context.CancelFunc, now time.Time) bool {
	cancel()

	return m.st.work.wait(m.exitBudget(now))
}

// runNotice renders a stoppable run in the notice row. An example:
//
//	● Distilling · 3 of 10 done · internal/api · 2m 14s   Lessons › Run
//
// A count shows only with a known total. The right hint names the run
// home. The row leaves out the hint on the run home itself (design map
// §6). A message shares the row: the message stays left and a short run
// chip stays right. So the run stays visible while the message shows.
func (m *model) runNotice(n *runNote, w int) string {
	t := m.st.theme
	g := t.Glyphs
	sep := t.Muted.Render(" " + g.Sep + " ")

	spin := t.Accent
	if n.stopping {
		spin = t.Attention
	}

	head := spin.Render(t.SpinnerFrame(m.st.tick)) + " " + t.Body.Render(ui.OneLine(n.verb))
	if n.stopping {
		head += sep + t.Attention.Render("Stopping"+g.Ellipsis)
	}

	count := ""
	if n.total > 0 {
		count = strconv.Itoa(n.done) + " of " + strconv.Itoa(n.total) + " done"
	}

	if m.toast.text != "" {
		chip := head
		if count != "" {
			chip += sep + t.Muted.Render(count)
		}

		return ui.Fit(ui.Spread(m.toastLine(), chip+" ", w), w, 1)
	}

	facts := make([]string, 0, 3)
	if count != "" {
		facts = append(facts, count)
	}

	if d := ui.OneLine(n.detail); d != "" {
		facts = append(facts, d)
	}

	if !n.started.IsZero() {
		facts = append(facts, runElapsed(time.Since(n.started)))
	}

	line := " " + head
	for _, f := range facts {
		line += sep + t.Muted.Render(f)
	}

	hint := ""
	if !m.showsHome(n.home) {
		hint = t.Faint.Render(m.runHome(n.home)) + " "
	}

	return ui.Fit(ui.Spread(line, hint, w), w, 1)
}

// showsHome reports whether the active screen shows the run home. An
// empty intent names the destination itself. A screen with sub-views
// answers through intentShower. Without it, no intent matches, so the
// hint stays.
func (m *model) showsHome(home navigateMsg) bool {
	if m.active != home.dest {
		return false
	}

	if home.intent == "" {
		return true
	}

	is, ok := m.screens[m.active].(intentShower)

	return ok && is.showsIntent(home.intent)
}

// runHome names the place of the run detail, for example the hint
// "Lessons › Run". The words come from the destination title and the
// intent, so the hint names what the user can open.
func (m *model) runHome(home navigateMsg) string {
	name := m.screens[home.dest].title()

	part, _, _ := strings.Cut(home.intent, ":")
	if part == "" {
		return name
	}

	return name + " " + m.st.theme.Glyphs.Arrow + " " + strings.ToUpper(part[:1]) + part[1:]
}

// runElapsed renders a run duration as "41s", "2m 14s" or "1h 02m".
// Whole seconds are enough, because the row redraws with every tick.
func runElapsed(d time.Duration) string {
	d = max(0, d.Truncate(time.Second))

	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m " + fmt.Sprintf("%02d", int(d.Seconds())%60) + "s"
	default:
		return strconv.Itoa(int(d.Hours())) + "h " + fmt.Sprintf("%02d", int(d.Minutes())%60) + "m"
	}
}
