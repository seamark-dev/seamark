package ui

import tea "github.com/charmbracelet/bubbletea"

// The shared helpers of the ui tests.

// keyMsg returns the key message of one key name, as the terminal sends
// it. It copies the helper of the internal/tui harness, because a test
// of ui must not import internal/tui.
func keyMsg(k string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+c": tea.KeyCtrlC, "backspace": tea.KeyBackspace,
	}

	if t, ok := named[k]; ok {
		return tea.KeyMsg{Type: t}
	}

	if k == "space" {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}

	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}
