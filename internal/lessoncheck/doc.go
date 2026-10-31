// Package lessoncheck holds the executable lesson checks: the check
// configuration in .seamark/lessons.yaml, its strict validation, the
// named limits, the snapshots of a change, the two /1 checkers, the
// guard decision, the verification result, and the data types of the
// capability report.
//
// The CLI, the MCP server, the console, and the hooks call this one
// package, so no second checker exists.
//
// Import rule: among the internal packages, this package imports only
// reviews, redact, render, history, and privatedir. The hook handlers
// call it on every edit, so it must not pull in a model client, the
// network, the index store, or sqlite. TestLessonCheckImportSet keeps
// the rule.
package lessoncheck
