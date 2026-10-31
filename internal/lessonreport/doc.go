// Package lessonreport is the shared, read-only engine of the delivery/1
// reading: what the audit logs say about each pin version, with units,
// limits, and reason codes. The CLI, the console, and the outcome
// displays all call this one package, so no surface builds a second
// reading.
//
// Build is pure. It reads only its Inputs value. It never writes, never
// caches, never reads the clock (Inputs.Now is the one time), and never
// calls a model or the network. A source that is missing or unreadable is
// data in the Report, never an error. Build returns an error only for a
// pin reference that names no single pin (*reviews.PinRefError).
//
// The Report holds no conversation text and no raw provider identity.
// Sessions and matches appear as short digest prefixes, and pin rules
// pass through render.Sanitize.
//
// Counts keep their units. A pin version is a reading of its own: the
// records of an older version of one pin never join the current version,
// and one event counts once per version.
//
// Import rule: among the internal packages, the non-test files import
// only reviews, integration, lessoncheck, model, render, and distill.
// They never import outcome, tui, cli, mcp, store, agent, or net/http.
// So the engine stays out of surfaces and writers and has no direct
// path to a model or the network. integration and distill bring agent
// and store into the import closure, so TestLessonreportImportSet
// checks direct imports only. The report surfaces prove their own
// import sets in their own sentinel tests.
package lessonreport
