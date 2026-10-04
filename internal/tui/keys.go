package tui

import "github.com/charmbracelet/bubbles/key"

// globalKeys are the shell bindings. They fire only when no text input
// and no overlay has focus.
type globalKeys struct {
	Overview     key.Binding
	Integrations key.Binding
	Lessons      key.Binding
	Doctor       key.Binding
	Settings     key.Binding
	Refresh      key.Binding
	Help         key.Binding
	Quit         key.Binding
	Interrupt    key.Binding
}

var gkeys = globalKeys{
	Overview:     key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "overview")),
	Integrations: key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "integrations")),
	Lessons:      key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "lessons")),
	Doctor:       key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "doctor")),
	Settings:     key.NewBinding(key.WithKeys("5"), key.WithHelp("5", "settings")),
	Refresh:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Quit:         key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	Interrupt:    key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// navKeys are the shared movement and activation bindings. Screens use
// these values so every list moves the same way.
type navKeys struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding
	Enter    key.Binding
	Back     key.Binding
	Toggle   key.Binding
	Next     key.Binding
	Prev     key.Binding
	Left     key.Binding
	Right    key.Binding
}

var nkeys = navKeys{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup", "page up")),
	PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("pgdn", "page down")),
	Top:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home", "first")),
	Bottom:   key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end", "last")),
	Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "select")),
	Next:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
	Prev:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous pane")),
	Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
	Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
}

// withHelp returns a copy of b with a different help text, so a screen
// can describe what a shared key does in its context.
func withHelp(b key.Binding, k, desc string) key.Binding {
	c := b
	c.SetHelp(k, desc)

	return c
}

// unavailableWord marks the keys that the current target cannot use. It
// is ASCII and it is a word, so the marker reads without color, under
// NO_COLOR and with the ASCII glyph set.
const unavailableWord = "n/a"

// unavailableKey is the only key of an unavailable entry. The entry is
// disabled, so key.Matches never matches it. The key only marks the
// entry, and no renderer shows it.
const unavailableKey = "<unavailable>"

// unavailable returns b as an entry that the hint rows and the help list
// as not available now: muted, after the word n/a. The copy is disabled,
// so every Enabled check reads it as not offered. A screen uses it for a
// key that the user must find even when the current target cannot use
// it (user decision of 2026-10-04 for Lessons › Pins). The screen still
// handles the key press itself, with its package binding, and explains
// the refusal.
func unavailable(b key.Binding) key.Binding {
	h := b.Help()

	return key.NewBinding(key.WithKeys(unavailableKey), key.WithHelp(h.Key, h.Desc), key.WithDisabled())
}

// isUnavailable reports an entry that unavailable made. A binding that a
// screen only disables stays hidden, so no other screen changes.
func isUnavailable(b key.Binding) bool {
	keys := b.Keys()

	return !b.Enabled() && len(keys) == 1 && keys[0] == unavailableKey
}

// listed reports a binding that the hint rows and the help show: an
// enabled binding or an unavailable entry, each with a key label.
func listed(b key.Binding) bool {
	return (b.Enabled() || isUnavailable(b)) && b.Help().Key != ""
}

// listAll returns bindings with each disabled binding that has a key
// label as an unavailable entry. The help of a screen uses it to name
// every key of the screen, also the keys that do not work now.
func listAll(bindings []key.Binding) []key.Binding {
	out := make([]key.Binding, 0, len(bindings))

	for _, b := range bindings {
		if !b.Enabled() && !isUnavailable(b) && b.Help().Key != "" {
			b = unavailable(b)
		}

		out = append(out, b)
	}

	return out
}
