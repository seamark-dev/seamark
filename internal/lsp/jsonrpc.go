// Package lsp serves the seamark graph to editors over the Language
// Server Protocol. The JSON-RPC layer in this file is deliberately
// self-contained and minimal (Content-Length framing, request dispatch):
// LSP and MCP are both JSON-RPC 2.0 over stdio, and this transport is the
// piece both adapters share (RFC-001 §5, implementation note).
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Standard JSON-RPC 2.0 error codes used by this server.
const (
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// maxMessageSize bounds Content-Length so a corrupt or hostile header
// cannot trigger an arbitrary-size allocation. Real LSP messages are
// kilobytes; 64 MiB is beyond any legitimate payload.
const maxMessageSize = 64 << 20

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // nil for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// conn frames JSON-RPC messages over a reader/writer pair. Writes are
// mutex-guarded so request handling and server-initiated notifications
// can interleave safely.
type conn struct {
	in  *bufio.Reader
	out io.Writer
	mu  sync.Mutex
}

func newConn(in io.Reader, out io.Writer) *conn {
	return &conn{in: bufio.NewReader(in), out: out}
}

// read returns the next framed message body.
func (c *conn) read() ([]byte, error) {
	length := -1

	for {
		line, err := c.in.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}

		if name, value, ok := strings.Cut(line, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("lsp: bad Content-Length: %w", err)
			}
		}
	}

	if length < 0 {
		return nil, fmt.Errorf("lsp: message without Content-Length")
	}

	if length > maxMessageSize {
		return nil, fmt.Errorf("lsp: message of %d bytes exceeds the %d limit", length, maxMessageSize)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(c.in, body); err != nil {
		return nil, err
	}

	return body, nil
}

// write frames and sends one message.
func (c *conn) write(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := fmt.Fprintf(c.out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}

	_, err = c.out.Write(body)

	return err
}

// respond answers a request; no-op for notifications (nil id).
func (c *conn) respond(id json.RawMessage, result any, rerr *rpcError) error {
	if id == nil {
		return nil
	}

	if result == nil && rerr == nil {
		// JSON-RPC requires exactly one of result/error. An untyped nil
		// with omitempty would drop the result field, and clients
		// (vscode-jsonrpc among them) discard such responses as invalid —
		// hanging whoever awaits them (shutdown, notably).
		result = json.RawMessage("null")
	}

	return c.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rerr})
}

// notify sends a server-initiated notification.
func (c *conn) notify(method string, params any) error {
	return c.write(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}
