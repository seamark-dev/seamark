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
// SetupPreview, IndexEvent and SampleView are the view types that the
// areas return. SampleRequest and SampleSurface are the input of
// Sample. ErrBusy, ErrNoLedger and ErrForeignRoot are the three errors
// that a screen tells apart. The engine and its helpers stay
// unexported, so a screen cannot reach past the interfaces.
//
// # Roots
//
// The effective root R is the root that a readable index records, else
// the workspace root. The Pins read, the pin authoring, both settings
// documents, the ledger and the sample use R. So every editor of
// .seamark/lessons.yaml sees the file that Pins lists. When R is not
// the workspace root, ErrForeignRoot applies. A pin authoring plan names
// it as a blocker, and an authoring commit refuses it. The Settings
// snapshot blocks the lessons groups with it, and a lessons save refuses
// it. A sample is then unavailable. These Phase 4 rules have no --db
// exception. A maintenance commit refuses a foreign root only with the
// default database path, as in Phase 3.
//
// # Sample
//
// Sample runs the lesson selection of the edit hook or of change_set for
// explicit paths, under the saved lessons.yaml text and the candidate
// of one review. It reads the evidence once with report.ReadEvidence
// and selects with the production folds, so its current side equals the
// production selection. The view also names the threshold of each side,
// because an exclusion only says that a lesson is below it. It needs a
// ready index of this workspace; every other state is Unavailable in
// words, never an empty selection. It never calls delivery.Deliver,
// report.ChangeSet, report.CheckAdvisory, a firing record or a delivery
// lease. It never opens the index for a write, and it never builds the
// index.
//
// # Imports
//
// services imports the engines (workspace, index, store, distill,
// learning, maintain, proposals, settings, lessonstats, integration,
// doctor, status, scaffold, agent, reviews, gate, pinfile, report and
// delivery). report and delivery hold the selection and the hook path
// rule of the sample. It never imports Bubble Tea, Lip Gloss, Bubbles,
// internal/tui or internal/tui/ui. An adapter that knows the screen
// would let a screen rule leak into an engine call.
// TestServicesStayBelowTheConsole checks this.
//
// # Writes
//
// A read method never writes. A mutation takes the engine lock with
// TryLock, so a second mutation fails with ErrBusy and never waits
// behind a long run. Every write goes through an engine, and the
// adapter adds no write path of its own. A pin authoring commit and a
// lessons save write .seamark/lessons.yaml only, and never the ledger.
package services
