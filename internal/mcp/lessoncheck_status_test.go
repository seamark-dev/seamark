package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
)

// TestLessonCheckStatusResourceCallsNoInvoker proves the K10 surfaces of
// the MCP server (AC-1): the status resource prints the lesson checks
// block of status, and change_set copies the badges of the same
// capability report into each check summary. Neither calls a model or
// the network: the invoker sentinel records no call.
func TestLessonCheckStatusResourceCallsNoInvoker(t *testing.T) {
	root, sentinel := lessonChecksFixture(t, "warn")

	resps := serve(t, root, initializeLine("2025-06-18"),
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"seamark://status"}}`,
		changeSetCall(t, 3, "generated/a.txt"),
	)

	t.Run("the status resource prints the lesson checks block", func(t *testing.T) {
		var read struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}

		require.Nil(t, resps["2"].Error)
		require.NoError(t, json.Unmarshal(resps["2"].Result, &read))
		require.Len(t, read.Contents, 1)

		text := read.Contents[0].Text
		assert.Contains(t, text, "lesson checks  claude  off · exploration inactive (not-installed)\n")
		assert.Contains(t, text, "               codex   off · exploration inactive (no structured Read path)\n")
		assert.Contains(t, text, "               pins    1 enforce (requested-inactive on claude, codex), "+
			"1 check (requested-inactive on claude, codex), 1 advisory · config valid\n")
	})

	t.Run("change_set carries the badges of the same report", func(t *testing.T) {
		res := decodeToolParts(t, resps["3"])
		require.False(t, res.IsError)

		p := structuredPreparation(t, res)
		require.Len(t, p.Checks, 1)

		inactive := []lessoncheck.CapReason{lessoncheck.CapNotInstalled}
		assert.Equal(t, []lessoncheck.PinClient{
			{Client: "claude", Badge: lessoncheck.BadgeRequestedInactive, Reasons: inactive},
			{Client: "codex", Badge: lessoncheck.BadgeRequestedInactive, Reasons: inactive},
		}, p.Checks[0].Enforcement)

		require.Len(t, res.Content, 2)
		assert.Contains(t, res.Content[1].Text,
			"  enforcement claude requested-inactive (not-installed) · codex requested-inactive (not-installed)\n")
	})

	sentinel.requireNoCall(t)
}
