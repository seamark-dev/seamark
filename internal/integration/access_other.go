//go:build !unix

package integration

// writeDenied keeps the owner write bit as the only rule on platforms
// without access(2). These platforms are not supported release targets
// today.
func writeDenied(string) bool { return false }
