package lessonreport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// TestSelectionReadingStaysDeliveryOne proves that the reading is an
// optional addition. The schema name stays, the JSON holds the fixed
// keys, no key names a receiver, and an older document still decodes.
func TestSelectionReadingStaysDeliveryOne(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	cfg.PinBudget = 16

	audit := auditLines(v2("e", "s", "m", heldBackPin(cfg, reviews.CauseOverPinCap)))
	r := mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "")
	assert.Equal(t, "delivery/1", r.Schema)

	var doc struct {
		Schema    string          `json:"schema"`
		Selection json.RawMessage `json:"selection"`
		Pins      []struct {
			Delivery struct {
				Causes json.RawMessage `json:"held_back_causes"`
			} `json:"delivery"`
		} `json:"pins"`
	}
	require.NoError(t, json.Unmarshal([]byte(encode(t, r)), &doc))

	assert.Equal(t, "delivery/1", doc.Schema)
	assert.JSONEq(t, `{
		"budgets": {
			"pin_cap": {"key": "pin_budget", "source": "file", "requested": 16, "effective": 8, "unit": "pins per injection", "surfaces": ["hook", "explore"]},
			"hook_total": {"source": "fixed", "effective": 8, "unit": "lessons per injection", "surfaces": ["hook", "explore"]},
			"change_budget": {"key": "change_budget", "source": "default", "requested": 6, "effective": 6, "unit": "lessons per answer", "surfaces": ["change_set", "prepare", "check"]}
		},
		"evidence": {"state": "read"}
	}`, string(doc.Selection))
	assert.JSONEq(t, `{"hook": {"over-pin-cap": 1}}`, string(doc.Pins[0].Delivery.Causes))

	for _, word := range []string{"lane", "context", "receiver", "session"} {
		assert.NotContains(t, string(doc.Selection), word)
		assert.NotContains(t, string(doc.Pins[0].Delivery.Causes), word)
	}

	var old Report
	require.NoError(t, json.Unmarshal([]byte(`{"schema":"delivery/1","pins":[{"delivery":{"held_back":2}}]}`), &old))
	assert.Nil(t, old.Selection)
	assert.Equal(t, 2, old.Pins[0].Delivery.HeldBack)
	assert.Nil(t, old.Pins[0].Delivery.HeldBackCauses)
}

// TestBuildMarksTheBudgetsOfAnInvalidConfigUnread proves that an invalid
// config gives no requested value, while an absent config reads as defaults.
func TestBuildMarksTheBudgetsOfAnInvalidConfigUnread(t *testing.T) {
	invalid := mustBuild(t, Inputs{ConfigRead: ConfigRead{State: ConfigInvalid}, Now: fixedNow}, "")
	b := invalid.Selection.Budgets

	assert.Equal(t, BudgetUnread, b.PinCap.Source)
	assert.Zero(t, b.PinCap.Requested)
	assert.Equal(t, reviews.DefaultPinBudget, b.PinCap.Effective)
	assert.Equal(t, BudgetUnread, b.ChangeBudget.Source)
	assert.Zero(t, b.ChangeBudget.Requested)
	assert.Equal(t, reviews.DefaultChangeBudget, b.ChangeBudget.Effective)

	absent := mustBuild(t, Inputs{Now: fixedNow}, "")
	assert.Equal(t, BudgetDefault, absent.Selection.Budgets.PinCap.Source)
	assert.Equal(t, reviews.DefaultPinBudget, absent.Selection.Budgets.PinCap.Requested)
}

// TestSelectionLineFormsEachBudget proves the exact pin cap, total, and
// change budget lines of each config state.
func TestSelectionLineFormsEachBudget(t *testing.T) {
	tests := []struct {
		name         string
		pinBudget    int
		changeBudget int
		state        string
		want         []string
	}{
		{"defaults", 0, 0, "", []string{
			"pin cap: requested 3 (default), effective 3 pins per injection",
			"total cap: fixed 8 lessons per injection",
			"change budget: requested 6, effective 6 lessons per answer",
		}},
		{"negative pin budget", -1, 0, "", []string{
			"pin cap: requested 3 (default), effective 3 pins per injection",
			"total cap: fixed 8 lessons per injection",
			"change budget: requested 6, effective 6 lessons per answer",
		}},
		{"at the total", 8, 9, "", []string{
			"pin cap: requested 8, effective 8 pins per injection",
			"total cap: fixed 8 lessons per injection",
			"change budget: requested 9, effective 9 lessons per answer",
		}},
		{"above the total", 16, 0, "", []string{
			"pin cap: requested 16, effective 8 pins per injection; pin_budget cannot raise the pin cap above the fixed total",
			"total cap: fixed 8 lessons per injection",
			"change budget: requested 6, effective 6 lessons per answer",
		}},
		{"invalid config", 0, 0, ConfigInvalid, []string{
			"pin cap: requested unknown, effective 3 pins per injection",
			"total cap: fixed 8 lessons per injection",
			"change budget: requested unknown, effective 6 lessons per answer",
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &reviews.Config{PinBudget: tc.pinBudget, ChangeBudget: tc.changeBudget}
			got := budgetLines(selectionBudgets(cfg, tc.state))

			assert.Equal(t, append([]string{budgetHeading}, tc.want...), got)
		})
	}
}

// TestSelectionLineOrdersItsParts proves the order of the parts, the
// sorted split, and the absence of the split when no record holds a pin back.
func TestSelectionLineOrdersItsParts(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	id, ver := pinOf(cfg, 0)
	_, plain := pinOf(cfg, 1)

	audit := auditLines(
		v2("e1", "s", "m1", heldBackPin(cfg, reviews.CauseRestated)),
		v2("e2", "s", "m2", heldBackPin(cfg, reviews.CauseOverPinCap)),
		v2("e3", "s", "m3", heldBackPin(cfg, reviews.CauseOverPinCap)),
		v2("e4", "s", "m4", pin(id, ver, reviews.PinInjected)),
	)
	r := mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "")

	line, ok := SelectionLine(&r, ver)
	require.True(t, ok)
	assert.Equal(t, strings.Join([]string{
		budgetHeading,
		"pin cap: requested 3 (default), effective 3 pins per injection",
		"total cap: fixed 8 lessons per injection",
		"change budget: requested 6, effective 6 lessons per answer",
		"evidence read",
		"delivery records: hook 4; injected 1; suppressed-repeat 0; held-back 3; emit-failed 0; returned 0",
		"held back by cause (records): hook/over-pin-cap 2, hook/restated 1",
	}, "\n"), line)

	line, ok = SelectionLine(&r, plain)
	require.True(t, ok)
	assert.NotContains(t, line, "held back by cause", "a split with no entry prints no line")
	assert.Contains(t, line, "delivery records: none;")
}

// TestSelectionLineHoldsNoCountForUnknownEvidence proves that the budgets
// stay and each count leaves when the audit does not read.
func TestSelectionLineHoldsNoCountForUnknownEvidence(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	_, ver := pinOf(cfg, 0)

	overLimit := auditLines(v2("e", "s", "m", heldBackPin(cfg, reviews.CauseRestated)))
	overLimit.OverLimit = true

	tests := []struct {
		name  string
		in    Inputs
		state string
	}{
		{"absent", Inputs{}, StateAbsent},
		{"unreadable", Inputs{AuditError: "denied"}, StateUnreadable},
		{"over-limit", Inputs{Audit: overLimit}, StateOverLimit},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Config, tc.in.Now = cfg, fixedNow
			r := mustBuild(t, tc.in, "")

			line, ok := SelectionLine(&r, ver)
			require.True(t, ok)

			lines := strings.Split(line, "\n")
			require.Len(t, lines, 5)
			assert.Equal(t, "pin cap: requested 3 (default), effective 3 pins per injection", lines[1])
			assert.Equal(t, "unknown (audit "+tc.state+")", lines[4])
		})
	}
}

// TestSelectionLineNeedsAReading proves that the bool is false when the
// report holds no reading of the version, and that a historical version
// has a reading.
func TestSelectionLineNeedsAReading(t *testing.T) {
	r := Report{
		Selection:  &SelectionReading{Evidence: SelectionEvidence{State: EvidenceRead}},
		Historical: []PinReading{{PinVersion: "old"}},
	}

	_, ok := SelectionLine(&r, "old")
	assert.True(t, ok)

	_, ok = SelectionLine(&r, "gone")
	assert.False(t, ok)

	_, ok = SelectionLine(&Report{Pins: []PinReading{{PinVersion: "old"}}}, "old")
	assert.False(t, ok, "a report without the reading has no line")

	_, ok = SelectionLine(nil, "old")
	assert.False(t, ok)
}

// TestSelectionLineSanitizesTheSurface proves that a surface name, which
// the log can hold as any string, reaches no terminal as a control sequence.
func TestSelectionLineSanitizesTheSurface(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	_, ver := pinOf(cfg, 0)

	audit := auditLines(onSurface(v2("e", "s", "m", heldBackPin(cfg, reviews.CauseRestated)), "bad\x1b[31m\nname"))
	r := mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "")

	line, ok := SelectionLine(&r, ver)
	require.True(t, ok)
	assert.NotContains(t, line, "\x1b")
	assert.Len(t, strings.Split(line, "\n"), 7, "a surface name adds no line")
}

// TestSelectionLinePrintsOneStringForTwoSurfaces proves the rule of one
// source. The map of a split has no order, so the loop repeats the read: a
// walk without the sort prints the two surfaces in either order.
func TestSelectionLinePrintsOneStringForTwoSurfaces(t *testing.T) {
	const want = "held back by cause (records): change_set/over-total 1, hook/over-pin-cap 1"

	cfg := testConfig("Reject unknown fields.")
	_, ver := pinOf(cfg, 0)

	audit := auditLines(
		v2("e1", "s", "m1", heldBackPin(cfg, reviews.CauseOverPinCap)),
		onSurface(v2("e2", "s", "m2", heldBackPin(cfg, reviews.CauseOverTotal)), "change_set"),
	)

	for range 32 {
		r := mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "")

		line, ok := SelectionLine(&r, ver)
		require.True(t, ok)

		lines := strings.Split(line, "\n")
		require.Equal(t, want, lines[len(lines)-1])
		require.Equal(t, 1, strings.Count(rendered(t, r), "    "+want+"\n"), "the report prints the same string")
	}
}
