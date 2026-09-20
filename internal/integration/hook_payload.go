package integration

import (
	"errors"
	"fmt"
	"io"
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
