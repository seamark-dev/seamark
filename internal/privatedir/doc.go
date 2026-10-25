// Package privatedir holds the rules for a private directory inside a
// worktree: a real directory, an owner-only mode, and a .gitignore that
// excludes the directory from Git.
//
// The capture archive (.seamark/private) uses these rules. This package
// keeps them for other private state too, such as the lesson checks
// (.seamark/checks). So one copy of each safety check exists, because
// two copies of a safety check can drift apart.
//
// The package imports the standard library only. So any package can
// import it without an import cycle. The caller passes the modes, the
// .gitignore body, and the error values, because each caller keeps its
// own error texts. The package knows no directory layout.
package privatedir
