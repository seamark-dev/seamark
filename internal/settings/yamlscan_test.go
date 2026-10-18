package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// The scanner tests moved to internal/yamledit with the scanner. The
// tests here cover the rules that only config.yaml has.

func TestExtentsLeaveHeadCommentsToTheNextKey(t *testing.T) {
	doc, blocked := parseDocument([]byte(starterConfig(t)))
	require.Nil(t, blocked)

	_, _, idx := doc.Section(sectionIndex)
	assert.Equal(t, 15, doc.SectionEnd(idx), "the index section ends at the commented example item")
	assert.Equal(t, 8, doc.ChildEnd(sectionIndex, "generated"), "the exclude head comment is not part of generated")
	assert.Equal(t, 15, doc.ChildEnd(sectionIndex, "exclude"))
}

func TestBlockedErrorNamesTheManualRoute(t *testing.T) {
	err := &BlockedError{Reason: "duplicate key", Line: 3}

	assert.Equal(t, "duplicate key (line 3); edit .seamark/config.yaml by hand", err.Error())
	assert.ErrorIs(t, err, ErrBlocked)
	assert.Equal(t, "no line; edit .seamark/config.yaml by hand", (&BlockedError{Reason: "no line"}).Error())
}

func TestScannerIssuesGetTheManualRoute(t *testing.T) {
	assert.Nil(t, blockedError(nil), "no issue gives no block")

	blocked := blockedError(&yamledit.Issue{Reason: "duplicate key", Line: 3})
	assert.Equal(t, "duplicate key (line 3); edit .seamark/config.yaml by hand", blocked.Error())

	_, blocked = parseDocument([]byte("a: 1\n---\nb: 2\n"))
	require.NotNil(t, blocked)
	assert.Equal(t, "the file holds more than one YAML document (line 2); edit .seamark/config.yaml by hand", blocked.Error())
	assert.ErrorIs(t, blocked, ErrBlocked)
}

// parsed parses text that must pass the structural gate.
func parsed(t *testing.T, text string) *yamledit.Doc {
	t.Helper()

	doc, blocked := parseDocument([]byte(text))
	require.Nil(t, blocked)

	return doc
}

func TestVerifyRefusesAChangeOutsideTheEditedSection(t *testing.T) {
	err := sameOtherSections(parsed(t, "a: 1\nagent:\n  cli: x\n"), parsed(t, "a: 2\nagent:\n  cli: y\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the a section changed")

	err = sameOtherSections(parsed(t, "agent:\n  cli: x\na: 1\n"), parsed(t, "a: 1\nagent:\n  cli: x\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the agent section moved")

	err = sameOtherSections(parsed(t, "a: 1\n"), parsed(t, "a: 1\nb: 2\n"), sectionAgent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the other top-level keys changed")

	require.NoError(t, sameOtherSections(parsed(t, "a: &x 1\n"), parsed(t, "a: &x 1\nagent:\n  cli: x\n"), sectionAgent))
}

func TestVerifyRefusesAnUnintendedChangeInTheSection(t *testing.T) {
	before := parsed(t, "agent:\n  cli: claude\n  model: opus\n")

	err := intendedSection(before, parsed(t, "agent:\n  cli: codex\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, String: "codex"}})
	require.Error(t, err, "an unknown nested key disappeared")

	require.NoError(t, intendedSection(before, parsed(t, "agent:\n  cli: codex\n  model: opus\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, String: "codex"}}))

	require.NoError(t, intendedSection(parsed(t, "agent:\n  cli: x\n"), parsed(t, "a: 1\n"), sectionAgent,
		[]Edit{{Field: AgentCLI, Reset: true}}), "an empty section equals no section")
}

func TestVerifyRefusesAResultThatAReaderRejects(t *testing.T) {
	// The draft check never lets this pattern through; verify is the net
	// under it, so it is called here with the bytes directly.
	root := withConfig(t, "index:\n  generated: true\n")
	snap := inspect(t, root)

	edits := []Edit{{Field: IndexExclude, List: []string{"a/**/b"}}}
	after := []byte("index:\n  generated: true\n  exclude:\n    - a/**/b\n")

	_, err := verify(snap, integration.Builtin(), GroupIndexing, edits, after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to write")
	assert.Contains(t, err.Error(), "the index reader would reject the result")
}
