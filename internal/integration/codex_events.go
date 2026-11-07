package integration

// The Codex hook event names. The list keeps one identifier for each
// native name. The completion decoder reads PostToolUse, and no decoder
// of this build reads the others. codex_hooks.go declares PostCompact
// next to the reset decoder, which reads it.
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
