package integration

// The closed values of ReceivingContext.Lane. The audit log stores them
// as they are, so a rename splits the records of one lane.
const (
	// LaneRoot names the session itself, the main conversation.
	LaneRoot = "root"
	// LaneChild names an agent inside the session, for example a
	// subagent that has its own context window.
	LaneChild = "child"
)

// ReceivingContextOf maps the native identity of one event to its
// receiving context. It is the one receiver derivation: the edit and
// reset decoders call it, and the delivery report calls it with the
// stored identity. So a lane that the report computes again equals the
// recorded lane byte for byte.
//
// An empty session gives no context, and suppression stays off. A
// client without a rule here gives no context too.
//
// The session is the main conversation. An event that fires inside a
// subagent adds agent_id and keeps the session id of the parent. A
// subagent has its own context window, so it is its own receiver: a
// lesson that the parent got is not in the window of the subagent. No
// verified reset event reaches a subagent, so its context is not
// resettable. The delivery service then repeats the advice there and
// never hides it.
//
// The root reset policy differs per client:
//   - Claude Code: PostCompact reports the session id, so the root
//     context is resettable.
//   - Codex: a native run of codex-cli 0.154.0 showed the agent_id rule
//     above, but it observed only a manual compaction of the parent.
//     An automatic compaction and nested subagents are unverified. So
//     no Codex context is resettable, and its lane is a fact for the
//     audit log only.
func ReceivingContextOf(clientID, sessionID, agentID string) *ReceivingContext {
	if sessionID == "" || (clientID != ClaudeID && clientID != CodexID) {
		return nil
	}

	if agentID != "" {
		return &ReceivingContext{
			ID: receiverID(sessionID, agentID), Resettable: laneResettable(clientID, LaneChild), Lane: LaneChild,
		}
	}

	return &ReceivingContext{ID: receiverID(sessionID), Resettable: laneResettable(clientID, LaneRoot), Lane: LaneRoot}
}

// laneResettable reports whether a verified reset event of the client
// reaches the context of a lane, by the policy of ReceivingContextOf. A
// child lane is never resettable. Only the Claude Code root lane is
// resettable. The timing evidence asks this without an event, so the
// rule needs no session or agent ID.
func laneResettable(clientID, lane string) bool {
	return lane != LaneChild && clientID == ClaudeID
}
