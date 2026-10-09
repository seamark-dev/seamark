// Package learning holds the learning-workflow services of the console
// (`seamark tui`): evidence refresh, distillation, and trigger
// extraction. Each service is a read-only plan and a confirmed run.
//
// A plan binds the inputs that the user reviewed, for example the index
// file, the configuration files, and the agent command. A run checks
// that binding again before it starts work. It reports a conflict
// instead of a different operation.
//
// The package holds console policy, not an engine. The CLI reaches the
// same engines without this policy: index.RefreshSources through
// index.RefreshFixes and index.RefreshReviews, distill.Run, and
// distill.ExtractTriggers. Golden tests pin the CLI output, so the CLI
// takes this policy only through a separate decision. The policy differs
// from the CLI in these points:
//
//   - A configuration error blocks a plan, because a paid run must not
//     use a default that the user did not review. The CLI is lenient in
//     two cases. index.RefreshReviews warns about a malformed
//     config.yaml and uses the default review window. `seamark lessons
//     --distill` warns about a malformed lessons.yaml and runs without
//     the pins, so it can propose a pinned pattern again. The CLI
//     trigger extraction refuses both files, as the console does.
//   - A distillation region must be inside the workspace, because a
//     mistyped region plans an empty run. The CLI keeps its lenient path
//     conversion.
//   - A plan opens the index read-only, because a preview must not
//     migrate the database. The CLI opener migrates an older schema.
//   - An agent runs in its own process group, because a stop must end
//     every process that the agent starts. The CLI keeps the terminal
//     process group, so a terminal interrupt reaches the agent.
//   - A cleanup of stale pending proposals is a separate confirmed
//     action, because it deletes rows that the user did not select. A
//     CLI distillation run does this cleanup itself.
//   - Trigger extraction asks only selected rows that the pin file and
//     the ledger agree about, so the Pins view and the preview agree.
//     The CLI asks a wider set of rows.
//
// The services stay out of index and distill for two reasons. Only the
// console uses them. They also need integration and pinfile, and the
// engines do not import these packages.
package learning
