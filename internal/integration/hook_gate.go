package integration

import (
	"strings"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/render"
)

// hookBlockExit is the exit code that Claude Code and Codex both read
// as a blocking PreToolUse hook. The tool does not run, and stderr is
// the reason that the model sees. `seamark gate` exits with the same
// code for a blocking verdict, so the hook and the plain command agree.
const hookBlockExit = 2

// exitProtocolGate encodes gate outcomes in the exit protocol that both
// built-in clients share: exit 2 plus the reason on stderr blocks, and
// exit 0 passes. Both clients also read a JSON permissionDecision on
// stdout. The Claude Code hook always used the exit protocol, and a
// native run of Codex CLI 0.154.0 has not exercised the JSON form. So
// both adapters use the protocol that has evidence. A client whose
// native block form differs implements CommandHooks on its own instead
// of embedding this type.
//
// Both clients ignore plain text on stdout on exit 0, so the human
// report of the CLI never reaches the model.
type exitProtocolGate struct{}

// EncodeDecision blocks with the matched rule messages when the
// decision blocks, and passes otherwise. A require_approval verdict
// under enforcement blocks like a deny. Neither client offers a native
// "ask" that the hook can use: Codex parses it but does not support
// it. So the reason names the verdict, and the agent asks the user.
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

// EncodeDeny blocks a pending edit for the lesson-check guard. It uses
// the exit protocol of the gate, because a native run exercised exit 2
// with stderr. No native run exercised the JSON permissionDecision form.
// It sanitizes each reason and joins the reasons with "; ", as
// decisionReasons joins rule messages. An empty list still blocks: a
// guard that denies must never allow.
func (exitProtocolGate) EncodeDeny(reasons []string) HookReply {
	parts := make([]string, 0, len(reasons))

	for _, reason := range reasons {
		if text := strings.TrimSpace(render.Sanitize(reason)); text != "" {
			parts = append(parts, text)
		}
	}

	return HookReply{ExitCode: hookBlockExit, Stderr: []byte(strings.Join(parts, "; "))}
}

// decisionReasons joins the messages of the matched rules, sanitized,
// because they travel to the agent. A block without a reason does not
// tell the agent what to change.
func decisionReasons(d *gate.Decision) string {
	reasons := make([]string, 0, len(d.Matches))

	for _, m := range d.Matches {
		reasons = append(reasons, render.Sanitize(m.Message))
	}

	return strings.Join(reasons, "; ")
}
