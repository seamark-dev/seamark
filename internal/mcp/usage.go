package mcp

import (
	"encoding/json"
	"time"

	"github.com/seamark-dev/seamark/internal/mcpusage"
)

// usageFailureNote is the log format of the usage appends that failed in
// one session: their number and the last failure.
const usageFailureNote = "mcp: usage record: calls not counted: %d, last failure: %v"

// countedCall runs one tools/call and records one usage line. The line
// comes after the reply is built, so it holds the latency and the size of
// that reply, and before the reply is sent.
func (s *Server) countedCall(params json.RawMessage) (any, *rpcError) {
	start := time.Now()

	result, rpcErr := s.callTool(params)

	s.recordUsage(params, start, result, rpcErr)

	return result, rpcErr
}

// recordUsage appends the usage line of one call. A count must never cost
// a call, so a failure only updates the fields that logUsageFailures reads.
// The log is stderr in the CLI. A write there blocks on a full pipe, and a
// closed pipe ends the process.
func (s *Server) recordUsage(params json.RawMessage, start time.Time, result any, rpcErr *rpcError) {
	now := time.Now()
	size, ok := replyUsage(result, rpcErr)

	rec := mcpusage.Record{
		Tool:   knownToolName(params),
		Client: s.client,
		MS:     now.Sub(start).Milliseconds(),
		Bytes:  size,
		OK:     ok,
	}

	if err := mcpusage.Append(s.root, rec, now); err != nil {
		s.usageFailed++
		s.usageErr = err
	}
}

// logUsageFailures writes one note about the failed appends of the
// session. Serve calls it after the last reply, so a log write that blocks
// or ends the process costs no call.
func (s *Server) logUsageFailures() {
	if s.usageFailed > 0 {
		s.logf(usageFailureNote, s.usageFailed, s.usageErr)
	}
}

// knownToolName returns the name of the called tool, or mcpusage.Unknown.
// A name that no tool has is text from the client, and it can hold
// private text, so it never enters the record. The function decodes only
// the name, so no argument reaches the usage code.
func knownToolName(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return mcpusage.Unknown
	}

	if _, known := toolRunners[p.Name]; !known {
		return mcpusage.Unknown
	}

	return p.Name
}

// replyUsage returns the size of the text parts of a reply and whether
// the call worked. A protocol error has no text parts, so its size is 0.
func replyUsage(result any, rpcErr *rpcError) (size int64, ok bool) {
	if rpcErr != nil {
		return 0, false
	}

	reply, isMap := result.(map[string]any)
	if !isMap {
		return 0, true
	}

	parts, _ := reply["content"].([]map[string]any)

	for _, part := range parts {
		text, _ := part["text"].(string)
		size += int64(len(text))
	}

	isErr, _ := reply["isError"].(bool)

	return size, !isErr
}
