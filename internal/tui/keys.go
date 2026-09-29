package tui

import "github.com/charmbracelet/bubbles/key"

// globalKeys are the shell bindings. They fire only when no text input
// and no overlay has focus.
type globalKeys struct {
	Overview     key.Binding
	Integrations key.Binding
	Lessons      key.Binding
	Doctor       key.Binding
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
