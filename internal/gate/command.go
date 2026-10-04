package gate

import (
	"errors"
	"strings"

	"github.com/seamark-dev/seamark/internal/effects"
)

// CommandRequest is one shell command to gate in a workspace.
type CommandRequest struct {
	// Root is the workspace root. The policy, the effect catalogue, and
	// the audit log live under it.
	Root string
	// Command is the shell text to evaluate. An empty command is an
	// error: a gate that shrugs at nothing to evaluate is a bypass.
	Command string
	// Enforce overrides the policy mode with enforce, as --enforce does.
	Enforce bool
}

// Blocked is the error of a blocking outcome: a blocking verdict, or a
// gate failure under enforcement. It wraps ErrBlocked, so
// errors.Is(err, ErrBlocked) stays the one test for "exit 2", and it
// keeps the cause apart from the prefix, so a hook reply can carry the
// cause in the client's own form.
type Blocked struct {
	// Cause is the reason: the matched rule messages, or the failure.
	Cause error
}

// Error prefixes the cause the way the gate always did.
func (b *Blocked) Error() string {
	if b.Cause == nil {
		return ErrBlocked.Error()
	}

	return ErrBlocked.Error() + ": " + b.Cause.Error()
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (b *Blocked) Unwrap() error { return b.Cause }

// Is makes every Blocked value match ErrBlocked.
func (b *Blocked) Is(target error) bool { return target == ErrBlocked }

// FailClosed wraps err in Blocked when enforced. Under enforcement the
// gate's own failures must block: a security hook that fails open on a
// malformed payload or a broken policy is itself a bypass. Without
// enforcement the error is returned as it is, and the caller reports it
// while the command proceeds.
func FailClosed(err error, enforced bool) error {
	if enforced {
		return &Blocked{Cause: err}
	}

	return err
}

// Decide gates one command end to end: it loads the policy and the
// effect catalogue of the workspace, evaluates the command, and appends
// the decision to the audit log. The plain command and every client
// hook share this order, so a policy or catalogue failure has one
// meaning everywhere. The decision says whether it blocks; the caller
// renders it in its own form.
//
// A failure returns a Blocked error when enforcement is active at that
// point. Explicit enforcement is active from the start. The policy's
// own mode is active once the policy loads: a warn policy lets a later
// failure through, and an enforce policy blocks it. Under warn the same
// failure is a plain error.
//
// The audit is best effort. A failed append reaches warn, when given,
// and never changes the decision.
func Decide(req CommandRequest, warn func(error)) (*Decision, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, FailClosed(errors.New("empty command"), req.Enforce)
	}

	policy, err := LoadPolicy(req.Root)
	if err != nil {
		return nil, FailClosed(err, req.Enforce)
	}

	if req.Enforce {
		policy.Mode = "enforce"
	}

	enforced := policy.Mode == "enforce"

	catalog, err := effects.Load(req.Root)
	if err != nil {
		return nil, FailClosed(err, enforced)
	}

	decision, err := EvalCommand(policy, catalog, req.Root, req.Command)
	if err != nil {
		return nil, FailClosed(err, enforced)
	}

	if err := Audit(req.Root, "gate", req.Command, policy, decision); err != nil && warn != nil {
		warn(err)
	}

	return decision, nil
}
