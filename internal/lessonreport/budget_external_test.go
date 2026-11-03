package lessonreport_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestSelectionBudgetsFollowTheHookBudget proves that the budgets of the
// report equal the limits of the selection code, for each stored value. A
// new clamp in the hook budget fails here.
func TestSelectionBudgetsFollowTheHookBudget(t *testing.T) {
	total := report.HookLessonTotal

	for _, stored := range []int{-1, 0, 1, total - 1, total, total + 1, 20, 100} {
		cfg := &reviews.Config{PinBudget: stored, ChangeBudget: stored}

		r, err := lessonreport.Build(lessonreport.Inputs{Config: cfg}, "")
		require.NoError(t, err)
		require.NotNil(t, r.Selection)

		budgets := r.Selection.Budgets
		assert.Equal(t, report.HookBudget(cfg).Clamped().Pins, budgets.PinCap.Effective, "pin_budget %d", stored)
		assert.Equal(t, cfg.HookPinBudget(), budgets.PinCap.Requested, "pin_budget %d", stored)
		assert.Equal(t, total, budgets.HookTotal.Effective)
		assert.Zero(t, budgets.HookTotal.Requested, "a fixed limit has no requested value")
		assert.Equal(t, cfg.ChangeSetBudget(), budgets.ChangeBudget.Effective, "change_budget %d", stored)
		assert.Equal(t, budgets.ChangeBudget.Effective, budgets.ChangeBudget.Requested, "change_budget %d", stored)
	}
}
