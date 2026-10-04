package settings

// The YAML analysis of config.yaml. The file-agnostic scanner lives in
// internal/yamledit: it finds the lines of the keys, and it never
// produces the written bytes. A re-encode of the whole document drops
// blank lines and moves comments. The file is committed and reviewed,
// so edits splice the source text instead (patch.go). This file holds
// the rules that only the configuration file has: the owned sections
// and the manual route of a block.

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/yamledit"
)

// Section names of the owned configuration keys. One reader owns each
// section: agent, distill, and index (which also reads reviews).
const (
	sectionAgent   = "agent"
	sectionDistill = "distill"
	sectionIndex   = "index"
	sectionReviews = "reviews"
)

// ownedSections lists the sections that the readers decode. A duplicate
// key, an alias, or a merge key in them changes what a reader sees. The
// analysis therefore looks inside these sections only.
var ownedSections = []string{sectionAgent, sectionDistill, sectionIndex, sectionReviews}

// blockedError adds the manual route of config.yaml to a scanner issue.
// The scanner names no file, and BlockedError.Error names this one. A
// nil issue gives a nil block.
func blockedError(issue *yamledit.Issue) *BlockedError {
	if issue == nil {
		return nil
	}

	return &BlockedError{Reason: issue.Reason, Line: issue.Line}
}

// parseDocument parses LF text and applies the structural gate of
// yamledit.Parse. A problem that makes every edit unsafe returns a
// BlockedError with the line to open.
func parseDocument(data []byte) (*yamledit.Doc, *BlockedError) {
	doc, issue := yamledit.Parse(data)

	return doc, blockedError(issue)
}

// sectionIssue explains why one owned section cannot be edited, or
// returns nil. An alias, an anchor, or a merge key makes the effect of
// an edit ambiguous: an edit would also change another place, or a
// merged value would stay in effect. A duplicate key hides one of the
// two values. Flow style has no line per key to splice.
func sectionIssue(doc *yamledit.Doc, name string) *BlockedError {
	key, value, _ := doc.Section(name)
	if key == nil {
		return nil
	}

	if key.Anchor != "" {
		return &BlockedError{Reason: fmt.Sprintf("the %s section key carries an anchor", name), Line: key.Line}
	}

	if blocked := blockedError(yamledit.SubtreeIssue(name, value)); blocked != nil {
		return blocked
	}

	switch {
	case value.Kind == yaml.MappingNode && value.Style&yaml.FlowStyle != 0:
		return &BlockedError{Reason: fmt.Sprintf("the %s section uses flow style { }", name), Line: value.Line}
	case value.Kind != yaml.MappingNode && !yamledit.IsNull(value):
		return &BlockedError{Reason: fmt.Sprintf("the %s section is not a mapping", name), Line: value.Line}
	}

	return nil
}
