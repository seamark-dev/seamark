package integration

// The Claude Code hook event names. The list keeps one identifier for
// each native name, also for an event that no decoder of this build
// reads. The advice replies and the completion decoder use these
// identifiers. A reply repeats the name of the event that it answers.
const (
	claudeEventPrompt        = "UserPromptSubmit"
	claudeEventPreToolUse    = "PreToolUse"
	claudeEventPostTool      = "PostToolUse"
	claudeEventToolFailure   = "PostToolUseFailure"
	claudeEventDenied        = "PermissionDenied"
	claudeEventStop          = "Stop"
	claudeEventStopFailure   = "StopFailure"
	claudeEventSubagentStart = "SubagentStart"
	claudeEventSubagentStop  = "SubagentStop"
	claudeEventPreCompact    = "PreCompact"
	claudeEventPostCompact   = "PostCompact"
	claudeEventSessionStart  = "SessionStart"
	claudeEventSessionEnd    = "SessionEnd"
)

// The Claude Code tools whose post-tool event gives more than an
// attempt and a result.
const (
	claudeToolBash        = "Bash"
	claudeToolAgent       = "Agent"
	claudeToolSendMessage = "SendMessage"
	claudeToolHandback    = "SubagentHandback"
)
