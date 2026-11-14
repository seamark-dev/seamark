// Package lessonreport is the shared, read-only engine of the delivery/1
// reading: what the audit logs say about each pin version, with units,
// limits, and reason codes. `seamark lessons --delivery` renders this
// reading. The console service returns it through a Delivery method,
// and no console screen calls that method yet. `lessons --stats` and the
// outcome views read the same audit records for another reading.
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
// Fixed fields are the delivery/1 fields that this build has no source
// for. Examples are capture, as_of.archive, the occurrences, the
// classes, the decision timing, and the historical and unlinked
// sections. Build fills each of them with one fixed value, so the
// document keeps one field set on every build. A fixed value is empty,
// zero, unknown, or a fixed state, so it never reads like a result.
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
