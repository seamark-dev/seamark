//go:build !unix

package integration

// HardLinked reports no hard link on platforms without a link count in
// the file information. These platforms are not supported release
// targets today.
func HardLinked(string, string) (bool, error) { return false, nil }
