package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// TestLessonsFixGateOffLinkedPinFileSaysApplyCannotWrite proves that a
// gate-off preview over a linked pin file still shows the block, and says
// that enabling the gate would not let the console write it.
func TestLessonsFixGateOffLinkedPinFileSaysApplyCannotWrite(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()
	f.ledger.Write = false
	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, false)
		pv.LessonsErr = errors.New(".seamark/lessons.yaml: symlink at .seamark/lessons.yaml")

		return pv, nil
	}

	h := lesOpen(t, f, 120, 32)
	h.press("space", "a")

	frame := lesModalText(t, h)
	assert.Contains(t, frame, "The console cannot write .seamark/lessons.yaml")
	assert.Contains(t, frame, "Enabling distill.write does not change this")
	assert.Contains(t, frame, "- rule: check-nil-errors")
	assert.Contains(t, frame, "c copy to clipboard", "a hand paste stays possible")
	assert.NotContains(t, frame, "y apply")

	h.press("y")
	assert.NotContains(t, f.called(), "CommitDecision:apply")
}
