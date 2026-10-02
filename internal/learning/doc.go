// Package learning holds the interactive learning-workflow services of
// the console (`seamark tui`): evidence refresh and distillation, each
// as a read-only plan and a confirmed run. The CLI never uses this
// package; it keeps its own direct paths, so its output stays the same.
//
// The package sits above workspace, index, and distill, because
// workspace imports index and a plan needs both. A plan binds what the
// user reviewed. A run checks that binding again before it starts any
// work, and it reports a conflict instead of a different operation.
package learning
