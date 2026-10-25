package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the "Lesson checks" section of the Integrations
// detail (contract K10, AC-34). It shows what the installed lesson-check
// hooks of one client cover: each path by hook stage, tool, and
// context, the hook binary, the writers that no hook covers, and the
// exact init commands that install, switch, and remove the handlers.
// The view is read-only: installing and removing these hooks stays a
// CLI action (P0 lead row P4-k). Every state comes from the capability
// report of the shared state, so the section computes no state itself.

// integLessonLabelW is the label column of the section.
const integLessonLabelW = 12

// integChecksShown reports whether the detail of insp shows the
// section: a lesson-check or exploration hook of the client is
// installed, or a pin requests check or enforce. The capability rows
// above never list the lesson-check capabilities, so a repository
// without lesson checks keeps its detail (contract K10).
func integChecksShown(st *state, insp integration.Inspection) bool {
	if !st.capability.loaded {
		return false
	}

	return insp.LessonHooksInstalled() || slices.ContainsFunc(st.capability.value.Pins, func(p lessoncheck.PinCapability) bool {
		return p.Requested != lessoncheck.ModeAdvisory
	})
}

// lessonCheckLines renders the section of one client, or nil when the
// detail does not show it.
func (s *integrations) lessonCheckLines(insp integration.Inspection, w int) []string {
	if !integChecksShown(s.st, insp) {
		return nil
	}

	t := s.st.theme
	report := s.st.capability.value
	vw := max(1, w-integLessonLabelW)

	out := []string{"", integHeading(t, "Lesson checks")}

	fact := func(label string, values []string) {
		out = append(out, propIndent(t.Muted.Render(ui.PadRight(label, integLessonLabelW)), values)...)
	}

	c, ok := report.Client(insp.ClientID)
	if !ok {
		fact("Installed", propLines(t.Muted, "no lesson-check path: the client declares no lesson-check hook", vw))
		fact("Config", propLines(integConfigStyle(t, report), report.ConfigText(), vw))

		return out
	}

	fact("Installed", []string{t.Body.Render(string(c.Installed))})
	fact("Config", propLines(integConfigStyle(t, report), report.ConfigText(), vw))
	fact("Paths", integPathValues(t, c, vw))

	if binary := integBinaryValues(t, c, vw); len(binary) > 0 {
		fact("Binary", binary)
	}

	if uncovered := integUncovered(report, c.Client); uncovered != "" {
		fact("Uncovered", propLines(t.Attention, uncovered+": no lesson-check hook sees these writers", vw))
	}

	if want := integWantedMode(report, c); want != "" {
		fact("Set up", integCommandLines(t, "seamark init --client "+c.Client+" --lesson-checks "+string(want), vw))
	}

	if insp.LessonHooksInstalled() {
		fact("Remove", integCommandLines(t, "seamark init --client "+c.Client+" --lesson-checks off "+
			"--lesson-exploration off", vw))
		fact("Downgrade", propLines(t.Muted, "Before you install an older seamark, remove every lesson-check "+
			"handler with this binary:", vw))
		fact("", integCommandLines(t, "seamark init --lesson-checks off --lesson-exploration off", vw))
	}

	return out
}

// integConfigStyle styles the configuration state: an invalid file is
// an attention, because an installed guard then denies every covered
// edit.
func integConfigStyle(t *ui.Theme, report lessoncheck.CapabilityReport) lipgloss.Style {
	if report.Config.Valid {
		return t.Body
	}

	return t.Attention
}

// integPathValues renders one line per path of the client: the hook
// stage, the tool, the context, the state, and the reasons. A path that
// the client cannot run reads unsupported.
func integPathValues(t *ui.Theme, c lessoncheck.ClientCapability, w int) []string {
	if len(c.Paths) == 0 {
		return propLines(t.Muted, "no path", w)
	}

	var out []string

	for _, p := range c.Paths {
		text := string(p.HookStage) + " " + ui.OneLine(p.Tool) + " in " + string(p.Context) + ": " + string(p.State)
		if len(p.Reasons) > 0 {
			text += " (" + lessoncheck.JoinCapReasons(p.Reasons) + ")"
		}

		style := t.Body

		switch p.State {
		case lessoncheck.PathInactive:
			style = t.Attention
		case lessoncheck.PathUnsupported:
			style = t.Muted
		}

		out = append(out, propLines(style, text, w)...)
	}

	return out
}

// integBinaryValues names the hook binaries of the installed paths:
// the version that inspection reads statically, and how it relates to
// this binary (contract K10, hook binary identification). Paths that
// share a binary state read once.
func integBinaryValues(t *ui.Theme, c lessoncheck.ClientCapability, w int) []string {
	var texts []string

	for _, p := range c.Paths {
		if p.State == lessoncheck.PathUnsupported || slices.Contains(p.Reasons, lessoncheck.CapNotInstalled) {
			continue
		}

		text := integBinaryWords(p.Reasons)
		if p.BinaryVersion != "" {
			text = ui.OneLine(p.BinaryVersion) + " " + t.Glyphs.Sep + " " + text
		}

		if !slices.Contains(texts, text) {
			texts = append(texts, text)
		}
	}

	var out []string

	for _, text := range texts {
		out = append(out, propLines(t.Body, text, w)...)
	}

	return out
}

// integBinaryWords names the binary state that the reasons of a path
// carry. The reasons come from the identification table of contract
// K10, so the most specific reason names the state.
func integBinaryWords(reasons []lessoncheck.CapReason) string {
	switch {
	case slices.Contains(reasons, lessoncheck.CapBinaryMissing):
		return "missing: the hook names no executable file"
	case slices.Contains(reasons, lessoncheck.CapVersionOld):
		return "older than " + lessoncheck.MinHandlerRelease + ": it has no lesson-check handler"
	case slices.Contains(reasons, lessoncheck.CapCapabilityUnknown):
		return "version unknown: another binary"
	case slices.Contains(reasons, lessoncheck.CapOtherBinary):
		return "another seamark binary"
	}

	return "this seamark binary"
}

// integUncovered names the writers of the client that no lesson-check
// hook covers (AC-32), for example "NotebookEdit in root, Bash in root".
func integUncovered(report lessoncheck.CapabilityReport, client string) string {
	var parts []string

	for _, u := range report.Uncovered {
		if u.Client == client {
			parts = append(parts, ui.OneLine(u.Tool)+" in "+string(u.Context))
		}
	}

	return strings.Join(parts, ", ")
}

// integWantedMode returns the handler mode that the pins request from
// the client and that it does not run yet: enforce for an enforce pin,
// else check for a check pin. It returns "" when the installed mode
// already covers every request, so the section offers no command.
func integWantedMode(report lessoncheck.CapabilityReport, c lessoncheck.ClientCapability) lessoncheck.Mode {
	want := lessoncheck.Mode("")

	for _, p := range report.Pins {
		switch {
		case p.Requested == lessoncheck.ModeEnforce:
			want = lessoncheck.ModeEnforce
		case p.Requested == lessoncheck.ModeCheck && want == "":
			want = lessoncheck.ModeCheck
		}
	}

	switch {
	case want == "":
		return ""
	case c.Installed == lessoncheck.InstalledEnforce,
		c.Installed == lessoncheck.InstalledCheck && want == lessoncheck.ModeCheck:
		return ""
	}

	return want
}
