package integration

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
)

// This file holds the outcome of a tool attempt and the rules that map
// native client results to it. The completion decoders read the outcome
// too, so this file keeps it apart from the capture files.

// Outcome is the observed outcome of a tool attempt. The empty value
// means that the observation is no tool result.
type Outcome string

// The outcomes.
const (
	OutcomeNone        Outcome = ""
	OutcomeSucceeded   Outcome = "succeeded"
	OutcomeFailed      Outcome = "failed"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeDenied      Outcome = "denied"
	OutcomeUnknown     Outcome = "unknown"
)

// Valid reports whether the value is a known outcome.
func (o Outcome) Valid() bool {
	return slices.Contains([]Outcome{
		OutcomeNone, OutcomeSucceeded, OutcomeFailed, OutcomeInterrupted, OutcomeDenied, OutcomeUnknown,
	}, o)
}

// codexExecHeader matches the header that Codex writes before command
// output for the model (format_exec_output_for_model in
// codex-rs/core/src/tools/mod.rs, rust-v0.157.1). The match needs the
// full header, so command output that only starts with "Exit code:"
// does not give an outcome.
var codexExecHeader = regexp.MustCompile(
	`\AExit code: (-?\d{1,10})\nWall time: [0-9.]+ seconds\n(?:Total output lines: \d+\n)?Output:\n`)

// codexOutcome maps the exit status in tool_response to an outcome.
func codexOutcome(response json.RawMessage) Outcome {
	return codexExitOutcome(codexExitStatus(response))
}

// codexExitOutcome maps an exit status to an outcome. Exit code 0 gives
// success. Every other code gives failure. Without an exit status the
// outcome is unknown. The hook decoder and the rollout decoder share
// this rule, so both channels give one outcome for one result.
func codexExitOutcome(code int64, ok bool) Outcome {
	switch {
	case !ok:
		return OutcomeUnknown
	case code == 0:
		return OutcomeSucceeded
	default:
		return OutcomeFailed
	}
}

// codexHeaderExit reads the exit status from the first header that
// matches the start of a tool output text. A text without a known header
// has no exit status. A header must match in full, so output that only
// starts with "Exit code:" gives no status.
func codexHeaderExit(text string, headers ...*regexp.Regexp) (int64, bool) {
	for _, header := range headers {
		m := header.FindStringSubmatch(text)
		if m == nil {
			continue
		}

		code, err := strconv.ParseInt(m[1], 10, 64)

		return code, err == nil
	}

	return 0, false
}

// codexExitStatus finds the exit status in tool_response. In 0.157.1 a
// shell result is the output text alone and has no exit status. An
// apply_patch result is text that starts with the exec header.
// codexExitStatus also reads an object with an integer exit_code,
// because a tool can return its status that way.
func codexExitStatus(response json.RawMessage) (int64, bool) {
	var text string
	if json.Unmarshal(response, &text) == nil {
		return codexHeaderExit(text, codexExecHeader)
	}

	var object map[string]json.RawMessage
	if json.Unmarshal(response, &object) != nil {
		return 0, false
	}

	raw, ok := object["exit_code"]
	if !ok {
		return 0, false
	}

	var code int64
	if json.Unmarshal(raw, &code) != nil {
		return 0, false
	}

	return code, true
}
