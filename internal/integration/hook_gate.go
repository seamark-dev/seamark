package integration

import (
	"strings"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/render"
)

// hookBlockExit is the exit code that Claude Code and Codex both read
// as a blocking PreToolUse hook: the tool does not run, and stderr is
// the reason the model sees. `seamark gate` exits with the same code
// for a blocking verdict, so the hook and the plain command agree.
const hookBlockExit = 2

// exitProtocolGate encodes gate outcomes in the exit protocol that both
// built-in clients share: exit 2 plus the reason on stderr blocks, and
// exit 0 passes. Both clients also read a JSON permissionDecision on
// stdout; the exit protocol is what the Claude Code hook always used,
// and a native run of Codex CLI 0.154.0 has not exercised the JSON
// form, so the protocol with evidence is the one both adapters use.
// A client whose native block form differs implements CommandHooks on
// its own instead of embedding this type.
//
// Plain text on stdout is ignored by both clients on exit 0, so the
// human report of the CLI never reaches the model.
type exitProtocolGate struct{}

// EncodeDecision blocks with the matched rule messages when the
// decision blocks, and passes otherwise. A require_approval verdict
// under enforcement blocks like a deny: neither client offers a native
// "ask" that the hook could use (Codex parses it and does not support
// it), so the reason names the verdict and the agent asks the user.
func (exitProtocolGate) EncodeDecision(d *gate.Decision) (HookReply, error) {
	if !d.Blocking() {
		return HookReply{}, nil
	}

	return HookReply{ExitCode: hookBlockExit, Stderr: []byte(decisionReasons(d))}, nil
}

// EncodeFailure blocks with the failure text. The caller decides
// whether the mode requires blocking; a warn-mode failure never
// reaches this method.
func (exitProtocolGate) EncodeFailure(err error) HookReply {
	return HookReply{ExitCode: hookBlockExit, Stderr: []byte(render.Sanitize(err.Error()))}
}

// decisionReasons joins the messages of the matched rules, sanitized:
// they travel to the agent, and "blocked" without a why leaves it
// guessing instead of correcting course.
func decisionReasons(d *gate.Decision) string {
	reasons := make([]string, 0, len(d.Matches))

	for _, m := range d.Matches {
		reasons = append(reasons, render.Sanitize(m.Message))
	}

	return strings.Join(reasons, "; ")
}
