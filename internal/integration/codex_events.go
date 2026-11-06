package integration

// The Codex hook event names that the capture hook decodes. The
// completion decoder reads PostToolUse too, so the names are not in the
// capture decoder. codex_hooks.go declares PostCompact, because the reset
// decoder reads it too.
const (
	codexSessionStart     = "SessionStart"
	codexUserPromptSubmit = "UserPromptSubmit"
	codexPostToolUse      = "PostToolUse"
	codexStop             = "Stop"
	codexSubagentStart    = "SubagentStart"
	codexSubagentStop     = "SubagentStop"
	codexPreCompact       = "PreCompact"
	codexInterrupt        = "Interrupt"
	codexSessionEnd       = "SessionEnd"
)
