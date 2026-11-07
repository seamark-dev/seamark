package hooks

// This file removes the hooks that seamark owns from a hook document.
// Lesson setup removes owned entries with it.

// RemoveOwned deletes every command hook that seamark owns with one of
// the markers, under any event of the document. Ownership follows
// OwnedBySeamark, so the binary path can be any path of the seamark
// binary, and a wrapper or a foreign command stays. RemoveOwned also
// deletes a command that is exactly bin and a marker, whatever the
// basename of bin. So a run of a renamed build removes the entries that
// the same build wrote. An empty bin adds no command. RemoveOwned
// deletes an entry that loses its last hook. It also deletes an event
// that loses its last entry. The "hooks" object stays, also when it
// becomes empty. Both clients document a hook file as that object, and
// no native check covers a file without it. It returns the number of
// hooks it removed. A "hooks" field with the wrong type holds nothing
// that seamark wrote, so it stays as it is.
func RemoveOwned(settings map[string]any, markers []string, bin string) int {
	hookMap, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}

	owned := func(cmd string) bool { return OwnedBySeamark(cmd, markers) || exactCommand(cmd, markers, bin) }
	removed := 0

	for event, value := range hookMap {
		entries, ok := value.([]any)
		if !ok {
			continue
		}

		kept, n := removeFromEntries(entries, owned)
		if n == 0 {
			continue
		}

		removed += n

		if len(kept) == 0 {
			delete(hookMap, event)
		} else {
			hookMap[event] = kept
		}
	}

	return removed
}

// exactCommand reports whether cmd is exactly the command that the
// binary bin runs with one of the markers. An empty bin owns nothing.
func exactCommand(cmd string, markers []string, bin string) bool {
	if bin == "" {
		return false
	}

	for _, marker := range markers {
		if cmd == (Spec{Marker: marker}).Command(bin) {
			return true
		}
	}

	return false
}

// removeFromEntries removes the owned hooks from the entries of one
// event. It returns the entries that keep at least one hook, and the
// number of hooks it removed. An entry with another shape stays.
func removeFromEntries(entries []any, owned func(cmd string) bool) (kept []any, removed int) {
	kept = make([]any, 0, len(entries))

	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			kept = append(kept, e)

			continue
		}

		list, ok := entry["hooks"].([]any)
		if !ok {
			kept = append(kept, e)

			continue
		}

		rest := make([]any, 0, len(list))

		for _, h := range list {
			if hm, ok := h.(map[string]any); ok {
				if cmd, ok := hm["command"].(string); ok && owned(cmd) {
					removed++

					continue
				}
			}

			rest = append(rest, h)
		}

		if len(rest) == len(list) {
			kept = append(kept, e)

			continue
		}

		// An entry without any hook left does nothing, so the loop does
		// not keep it.
		if len(rest) > 0 {
			entry["hooks"] = rest
			kept = append(kept, entry)
		}
	}

	return kept, removed
}
