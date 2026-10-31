package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxHookPayload bounds one native hook payload in bytes. The cap
// applies before decoding, so an oversized payload costs no parse work.
// A legitimate edit event is far smaller, even with whole-file content.
const MaxHookPayload = 1 << 20

// ErrMalformedEvent reports a hook payload that a codec cannot decode:
// invalid syntax, a wrong field type, or a size above MaxHookPayload.
// It is distinct from a valid event with no paths, which decodes
// without an error. Advice callers treat both as "say nothing".
var ErrMalformedEvent = errors.New("malformed hook event")

// ErrNotApplicable reports a well-formed event that the codec does not
// translate, for example a shell event given to an edit codec. The
// event is not broken: the hook has nothing to do with it.
var ErrNotApplicable = errors.New("hook event not applicable")

// ErrUnsupportedGrammar reports an edit event whose body uses a form
// that the codec does not know. The codec then knows no complete set of
// paths. It must not report the paths it did read: a smaller set looks
// like complete coverage and is not.
var ErrUnsupportedGrammar = errors.New("unsupported edit grammar")

// ReadHookPayload reads one native hook payload under the size cap. An
// oversized payload is an error and never a truncated prefix: a codec
// must not decode a part of an event as the complete event.
func ReadHookPayload(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxHookPayload+1))
	if err != nil {
		return nil, err
	}

	if len(data) > MaxHookPayload {
		return nil, fmt.Errorf("%w: payload exceeds %d bytes", ErrMalformedEvent, MaxHookPayload)
	}

	return data, nil
}

// hookContextReply is the reply shape of a hook that adds context.
// Claude Code and Codex add additionalContext to the context of the
// agent and do not block the tool, so both clients use this shape.
type hookContextReply struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// encodeContextReply wraps text in the additionalContext reply to a hook
// event. The edit advice of both clients, and the completion findings
// and explore advice of Claude Code, go through this one encoder, so
// every reply keeps the same bytes.
func encodeContextReply(event, text string) (HookReply, error) {
	var reply hookContextReply

	reply.HookSpecificOutput.HookEventName = event
	reply.HookSpecificOutput.AdditionalContext = text

	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(reply); err != nil {
		return HookReply{}, err
	}

	return HookReply{Stdout: out.Bytes()}, nil
}

// malformedJSON returns ErrMalformedEvent with fixed text for a JSON
// error of a hook payload. The text of a JSON error can quote a
// character or a number of the payload. The guard prints a decoder error
// in its deny text on stderr, and that text must never hold payload
// text. So the text names the error class and the byte offset only, or
// a field name of the decoder's own type.
func malformedJSON(err error) error {
	var (
		syntax *json.SyntaxError
		typed  *json.UnmarshalTypeError
	)

	switch {
	case errors.As(err, &syntax):
		return fmt.Errorf("%w: invalid JSON syntax at byte %d", ErrMalformedEvent, syntax.Offset)
	case errors.As(err, &typed) && typed.Field != "":
		// Field is the path of the decoder's own JSON field names. It
		// never holds an object key of the payload.
		return fmt.Errorf("%w: the field %s has the wrong JSON type", ErrMalformedEvent, typed.Field)
	case errors.As(err, &typed):
		return fmt.Errorf("%w: a value has the wrong JSON type", ErrMalformedEvent)
	}

	return fmt.Errorf("%w: the payload cannot be decoded as JSON", ErrMalformedEvent)
}

// receiverID builds the ID of a receiving context from the native
// identifiers that name it, for example a session and a subagent. Each
// part carries its length. The identifiers are client input and can
// hold any byte, so a separator alone lets two different part lists
// give one ID. With the lengths, a one-part ID never equals a two-part
// ID, and a child context never gets the ID of its parent.
func receiverID(parts ...string) string {
	var id strings.Builder

	for _, part := range parts {
		fmt.Fprintf(&id, "%d:%s", len(part), part)
	}

	return id.String()
}
