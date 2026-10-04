package integration

import (
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
