package integration

import "slices"

// This file holds appendOnce.

// appendOnce appends the values that the list does not hold yet, in
// order. The gate summary uses it, so a reader sees each hook label
// once.
func appendOnce[T comparable](list []T, more ...T) []T {
	for _, v := range more {
		if !slices.Contains(list, v) {
			list = append(list, v)
		}
	}

	return list
}
