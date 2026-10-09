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
