// Package ui holds the drawing primitives of the seamark console: the
// theme, the glyphs and the tones, the boxes, the modals and the overlay,
// the text helpers, the scroll, pager and list state, the line input, the
// movement keys and the n/a key helpers. The screens of internal/tui
// draw with them.
//
// # What belongs here
//
// A declaration belongs to ui only when it renders or tracks a position
// without seamark vocabulary. The rule applies to its name, its
// parameters, its fields and its doc. Seamark vocabulary is, for
// example, effect, phase, region, proposal, pin, lesson, run,
// integration, client, setup, policy and brand. A declaration that names
// such a concept stays in internal/tui, usually in its widgets.go. So the
// next author can decide a move from the name alone.
//
// # Imports
//
// ui imports the standard library and third-party packages freely, for
// example Lip Gloss, Bubbles key, x/ansi, termenv, and Bubble Tea for key
// messages. Of the seamark packages, it imports only internal/redact and
// internal/render, because the sanitizers need them. It never imports
// internal/tui or internal/tui/services, so a primitive cannot reach a
// screen, the console state or an engine. TestUIStaysBelowTheConsole
// checks this.
//
// # Exports
//
// ui exports what internal/tui uses and nothing more; a name that only
// tests use stays unexported. The state types keep their fields exported,
// as the Bubbles widgets do, so a screen reads a position without an
// accessor.
package ui
