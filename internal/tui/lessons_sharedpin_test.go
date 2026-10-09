package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/proposals"
)

// TestLessonsApplyCountsOnePinForEachKey covers D16 in the console: when
// two selected proposals have one rule and one region set, the shared
// apply writes one entry. The preview, the y label and the result count
// that entry, and the preview names the proposal without its own entry.
func TestLessonsApplyCountsOnePinForEachKey(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	var committed *proposals.Preview

	f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
		pv := lesPreview(a, ids, true)
		if a == proposals.ActionApply {
			pv.SharedPin = []int64{3}
		}

		return pv, nil
	}
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		committed = pv

		return proposals.Result{Action: pv.Action, FileWritten: true, Moved: []int64{1, 3}}, nil
	}

	h := lesOpen(t, f, 120, 40)
	h.press("space", "down", "down", "space", "a")

	modal := lesModalText(t, h)
	assert.Contains(t, modal, "WILL + add 1 pin under pin: in .seamark/lessons.yaml (the file is created)")
	assert.Contains(t, modal, "+ record p1, p3 as applied in the ledger (pending › applied)")
	assert.Contains(t, modal, "WON'T · add a separate pin for p3 (a lower ID has the same rule and regions)")
	assert.Contains(t, modal, "y apply 1 pin")

	h.press("y")
	require.NotNil(t, committed)
	assert.Equal(t, []int64{3}, committed.SharedPin, "the commit carries the reviewed preview")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "╭─ Applied 1 pin", "the title counts the written entry")
	assert.Contains(t, lesModalText(t, h), "recorded p1, p3 as applied")
}

// TestLessonsYAMLPreviewNamesTheSharedPin covers D16 with the gate off.
// The YAML block holds one entry for each key. The preview names the
// proposal without its own entry, as the CLI does under its block. A
// proposal whose pin the file already holds is named in the lead only.
func TestLessonsYAMLPreviewNamesTheSharedPin(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string
		pinned  []int64
		command string
		named   bool
	}{
		{
			name:    "shared key",
			keys:    []string{"space", "down", "down", "space", "a"},
			command: "seamark lessons --apply p1,p3",
			named:   true,
		},
		{
			name:    "shared key that the file already holds",
			keys:    []string{"space", "down", "space", "down", "space", "a"},
			pinned:  []int64{1, 3},
			command: "seamark lessons --apply p2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake()
			f.ledger = lesLedger()

			f.previewDecision = func(a proposals.Action, ids []int64) (*proposals.Preview, error) {
				pv := lesPreview(a, ids, false)
				if a == proposals.ActionApply {
					pv.SharedPin = []int64{3}
					pv.AlreadyPinned = tt.pinned
				}

				return pv, nil
			}

			h := lesOpen(t, f, 120, 40)
			h.press(tt.keys...)

			modal := lesModalText(t, h)
			assert.Contains(t, modal, tt.command)

			if !tt.named {
				assert.Contains(t, modal, "Pins already exist p1, p3")
				assert.NotContains(t, modal, "left out", "the lead names p3 once")

				return
			}

			assert.Contains(t, modal, "p3 left out: a pin above has the same rule and regions.")
		})
	}
}
