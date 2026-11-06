package integration

import "slices"

// This file holds appendOnce. The gate summary uses it too, so it is not
// in the capture setup file.

// appendOnce appends the values that the list does not hold yet, in
// order. The lesson hooks and the capture entries of Codex share one
// document and its limitations, and a reader must see each limitation
// once. The gate summary lists each hook label once by the same rule.
func appendOnce[T comparable](list []T, more ...T) []T {
	for _, v := range more {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}

	return list
}
