package integration

// The Claude Code hook event names. Capture, the advice replies, and the
// completion decoder share them, so each native name has one identifier.
// A reply repeats the name of the event that it answers.
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
