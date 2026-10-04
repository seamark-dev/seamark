package integration

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/approve"
)

// documentPaths lists the client documents that a finding can name.
var documentPaths = []string{
	approve.ClaudeSettings, claudeLocalSettings, approve.MCPConfig, approve.CodexConfig, codexHooksFile,
}

// repeatedPathReasons returns each finding that names a path where the
// consumer prints one already, as "<Path>: <Reason>". Every consumer
// prints the path before the reason. A reason that starts with a path
// then names one file twice, or two files for one finding. A reason
// that holds its own path names that file twice too, for example in a
// read error.
func repeatedPathReasons(findings []Finding) []string {
	var out []string

	for _, f := range findings {
		repeats := f.Path != "" && strings.Contains(f.Reason, f.Path)

		for _, path := range documentPaths {
			repeats = repeats || strings.HasPrefix(f.Reason, path)
		}

		if repeats {
			out = append(out, f.Path+": "+f.Reason)
		}
	}

	return out
}

func TestRepeatedPathReasonsFindsAReasonThatRepeatsAPath(t *testing.T) {
	// The checker guards every setup test, so it must catch each form.
	assert.Equal(t, []string{".claude/settings.json: .claude/settings.json has `x`"},
		repeatedPathReasons([]Finding{{Path: approve.ClaudeSettings, Reason: approve.ClaudeSettings + " has `x`"}}))
	assert.Equal(t, []string{".codex/hooks.json: .codex/config.toml [hooks] already runs"},
		repeatedPathReasons([]Finding{{Path: codexHooksFile, Reason: approve.CodexConfig + " [hooks] already runs"}}))
	assert.Equal(t, []string{".codex/config.toml: not checked: .codex/config.toml: not a regular file"},
		repeatedPathReasons([]Finding{{Path: approve.CodexConfig, Reason: "not checked: " + approve.CodexConfig + ": not a regular file"}}))

	assert.Empty(t, repeatedPathReasons([]Finding{
		{Path: approve.ClaudeSettings, Reason: "has `x`"},
		{Path: approve.CodexConfig, Reason: "inline [hooks] are present; Codex merges them with " + codexHooksFile},
		{Reason: "Claude Code: skills are not supported by this integration; skipped"},
	}))
}

func TestReadReasonNamesNoPath(t *testing.T) {
	// ReadInput puts the path in front of each error, and an error of the
	// file system holds the absolute path too.
	denied := &fs.PathError{Op: "open", Path: "/repo/" + claudeLocalSettings, Err: fs.ErrPermission}

	assert.Equal(t, "permission denied", readReason(claudeLocalSettings, fmt.Errorf("%s: %w", claudeLocalSettings, denied)))
	assert.Equal(t, "not a regular file", readReason(claudeLocalSettings, fmt.Errorf("%s: not a regular file", claudeLocalSettings)))
	assert.Equal(t, "unexpected end of JSON input", readReason(claudeLocalSettings, errors.New("unexpected end of JSON input")))
}
