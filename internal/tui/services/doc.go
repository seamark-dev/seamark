// Package services is the typed engine adapter of the seamark console
// (`seamark tui`). It binds the shared engines to one resolved
// workspace and returns plain values that a screen can show. It holds
// no screen, no drawing and no console state.
//
// # Surface
//
// All is the whole adapter, and it embeds one interface per area:
// Reader, Setup, Decisions, Learning, Maintenance and Settings. A worker
// of the console takes only the area that it needs, so its signature
// shows which engine calls it can make. New builds the production
// adapter. NewWithDeps builds it with other boundaries (Deps): the
// GitHub fetch, the agent invoker and the clock. The console tests that
// drive a screen over the real engine use it. IndexView, PolicySummary,
// SetupPreview and IndexEvent are the view types that the areas return.
// ErrBusy and ErrNoLedger are the two errors that a screen tells apart.
// The engine and its helpers stay unexported, so a screen cannot reach
// past the interfaces.
//
// # Imports
//
// services imports the engines (workspace, index, store, distill,
// learning, maintain, proposals, settings, lessonstats, integration,
// doctor, status, scaffold, agent, reviews and gate). It never imports
// Bubble Tea, Lip Gloss, Bubbles, internal/tui or internal/tui/ui,
// because an adapter that knows the screen would let a screen rule leak
// into an engine call. TestServicesStayBelowTheConsole checks this.
//
// # Writes
//
// A read method never writes. A mutation takes the engine lock with
// TryLock, so a second mutation fails with ErrBusy and never waits
// behind a long run. Every write goes through an engine, and the
// adapter adds no write path of its own.
package services
