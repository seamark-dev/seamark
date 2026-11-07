// Package privatedir holds the rules for a private directory inside a
// worktree: a real directory, an owner-only mode, and a .gitignore that
// excludes the directory from Git.
//
// The lesson checks (.seamark/checks) use these rules. One package holds
// them, so one copy of each safety check exists, because two copies of
// a safety check can drift apart.
//
// The package also holds the hardening of an append-only log in such a
// directory: the link refusal, the exclusive lock, and the rotation. The
// policy gate audit log, the lesson-check audit stream, and the MCP usage
// log use it.
//
// The package imports the standard library only. So any package can
// import it without an import cycle. The caller passes the modes, the
// limits, the .gitignore body, and the error values or error functions,
// because each caller keeps its own error texts. The package knows no
// directory layout.
package privatedir
