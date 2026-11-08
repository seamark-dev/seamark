package hooks

// This file removes the handlers of a hook in the form of another
// client, so a document keeps one handler of the hook. Codex setup uses
// it for a gate hook in the Claude Code form.

// FormChange is one handler in another client's form that
// ReduceOtherForms removed or rewrote.
type FormChange struct {
	// From is the command as the document held it.
	From string
	// To is the managed command that replaced it, or "" when the handler
	// was removed.
	To string
}

// formHandler is one owned handler of the spec's event, with what
// ReduceOtherForms needs to choose the handler that stays.
type formHandler struct {
	entry, index int
	command      string
	other        bool
	runs         bool
}

// ReduceOtherForms keeps at most one handler of the spec's hook in the
// form of another client, and rewrites a kept one to spec.Command(bin).
// The client runs every handler, so a second one runs the hook twice,
// and an enforce handler in another form blocks beside a warn handler.
//
// The rule prefers a handler that the client runs for a tool of the
// spec, so setup never removes the only handler that runs:
//
//   - With an own-form handler that runs, every other-form handler goes.
//   - Else the first other-form handler that runs stays.
//   - Else, with any own-form handler, every other-form handler goes.
//   - Else the first other-form handler stays.
//
// A kept handler keeps its matcher, its timeout, and its other keys,
// because the user can have set them. An entry that loses its last
// handler goes too. Own-form handlers stay as they are, and Merge
// rewrites them. The function returns each change in document order.
func ReduceOtherForms(settings map[string]any, spec Spec, bin string, fires MatcherRule) []FormChange {
	if len(spec.OtherForms) == 0 {
		return nil
	}

	hookMap, _ := settings["hooks"].(map[string]any)
	entries, _ := hookMap[spec.Event].([]any)
	handlers := ownedHandlers(entries, spec, fires)

	keep := keptOtherForm(handlers)

	var (
		changes []FormChange
		drop    = map[int]map[int]bool{}
	)

	for i, h := range handlers {
		if !h.other {
			continue
		}

		if i == keep {
			want := spec.Command(bin)
			if h.command != want {
				handlerAt(entries, h.entry, h.index)["command"] = want
				changes = append(changes, FormChange{From: h.command, To: want})
			}

			continue
		}

		if drop[h.entry] == nil {
			drop[h.entry] = map[int]bool{}
		}

		drop[h.entry][h.index] = true
		changes = append(changes, FormChange{From: h.command})
	}

	if len(drop) > 0 {
		hookMap[spec.Event] = withoutHandlers(entries, drop)
	}

	return changes
}

// ownedHandlers lists every handler of the event that the spec owns, in
// document order. The index of a handler counts every item of its
// entry's list, so withoutHandlers finds it again.
func ownedHandlers(entries []any, spec Spec, fires MatcherRule) []formHandler {
	var out []formHandler

	for e, item := range entries {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		matcher, _ := entry["matcher"].(string)
		list, _ := entry["hooks"].([]any)

		for i, raw := range list {
			h, ok := raw.(map[string]any)
			if !ok {
				continue
			}

			cmd, _ := h["command"].(string)

			other := OwnedBySeamark(cmd, spec.OtherForms)
			if !other && !OwnedBySeamark(cmd, spec.ownMarkers()) {
				continue
			}

			hookType, _ := h["type"].(string)
			runs := hookType == "command" && Fires(spec, matcher, fires)

			out = append(out, formHandler{entry: e, index: i, command: cmd, other: other, runs: runs})
		}
	}

	return out
}

// keptOtherForm returns the index of the other-form handler that stays,
// or -1 when none stays, by the rule of ReduceOtherForms.
func keptOtherForm(handlers []formHandler) int {
	first := func(other, runs bool) int {
		for i, h := range handlers {
			if h.other == other && (!runs || h.runs) {
				return i
			}
		}

		return -1
	}

	switch {
	case first(false, true) >= 0:
		return -1
	case first(true, true) >= 0:
		return first(true, true)
	case first(false, false) >= 0:
		return -1
	default:
		return first(true, false)
	}
}

// handlerAt returns the handler map at one position. ownedHandlers found
// it there, so the type assertions hold.
func handlerAt(entries []any, entry, index int) map[string]any {
	list, _ := entries[entry].(map[string]any)["hooks"].([]any)
	h, _ := list[index].(map[string]any)

	return h
}

// withoutHandlers returns the entries without the dropped handlers. An
// entry that loses its last handler goes, because it does nothing. Every
// other entry keeps its keys.
func withoutHandlers(entries []any, drop map[int]map[int]bool) []any {
	kept := make([]any, 0, len(entries))

	for e, item := range entries {
		dropped, ok := drop[e]
		if !ok {
			kept = append(kept, item)

			continue
		}

		entry, _ := item.(map[string]any)
		list, _ := entry["hooks"].([]any)
		rest := make([]any, 0, len(list))

		for i, h := range list {
			if !dropped[i] {
				rest = append(rest, h)
			}
		}

		if len(rest) > 0 {
			entry["hooks"] = rest
			kept = append(kept, entry)
		}
	}

	return kept
}
