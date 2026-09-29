package tui

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessonstats"
	lessonmodel "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/status"
)

const statsTestLog = "/work/repo/.seamark/lessons-audit.jsonl"

var (
	statsAPI = reviews.FiredLesson{Region: "api", Symptom: "synchronize generated client"}
	statsDB  = reviews.FiredLesson{Region: "db", Symptom: "E741"}
)

// statsNow is the read time of the test snapshots, one day after the
// last mixed record, so the 14-day chart holds every record.
var statsNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.Local)

// statsHook builds one edit-hook record. A named client also names the
// mechanism, as current hooks do.
func statsHook(ts, client, receiver string, delivery reviews.DeliveryStatus, bytes int, fired ...reviews.FiredLesson) reviews.Firing {
	f := reviews.Firing{
		TS: ts, File: "api/a.py", Delivery: delivery, SessionSHA: "one-session", Generation: 1,
		ContextBytes: bytes, Fired: fired, Client: client, ContextSHA: receiver,
	}

	if client != "" {
		f.Mechanism = "pre-tool-use-context"
	}

	return f
}

// statsMixedFirings holds every record shape: legacy, instrumented
// without a client, claude with a repeat and a suppressed repeat,
// codex, check and change_set.
func statsMixedFirings() []reviews.Firing {
	return []reviews.Firing{
		{TS: "2026-07-01T10:00:00Z", File: "api/a.py", Fired: []reviews.FiredLesson{statsAPI}},
		statsHook("2026-07-02T10:00:00Z", "", "", reviews.DeliveryInjected, 100, statsAPI),
		statsHook("2026-07-03T10:00:00Z", "", "", reviews.DeliveryInjected, 100, statsAPI),
		statsHook("2026-07-04T10:00:00Z", "claude", "claude-ctx", reviews.DeliveryInjected, 200, statsAPI),
		statsHook("2026-07-05T10:00:00Z", "claude", "claude-ctx", reviews.DeliveryInjected, 200, statsAPI),
		statsHook("2026-07-06T10:00:00Z", "claude", "claude-ctx", reviews.DeliverySuppressedRepeat, 0, statsAPI),
		statsHook("2026-07-07T10:00:00Z", "codex", "", reviews.DeliveryInjected, 300, statsDB),
		{
			TS: "2026-07-08T10:00:00Z", Surface: "check", Files: []string{"api/a.py", "db/b.py"},
			Fired: []reviews.FiredLesson{statsAPI, statsDB},
		},
		{
			TS: "2026-07-09T10:00:00Z", Surface: "change_set", Files: []string{"db/b.py"},
			Fired: []reviews.FiredLesson{statsDB},
		},
	}
}

// statsSurfaced is the eligible lesson set: one delivered lesson, one
// never delivered, and one pin that Summarize never lists.
func statsSurfaced() []lessonmodel.Lesson {
	return []lessonmodel.Lesson{
		{Region: "api", Symptom: "synchronize generated client", Occurrences: 5, Reviewer: "coderabbit"},
		{Region: "tests", Symptom: "E999 unused fixture", Occurrences: 4, Reviewer: "coderabbit"},
		{Region: "docs", Symptom: "pinned rule", Occurrences: 2, Reviewer: "pinned"},
	}
}

// statsSnapshot aggregates the records with the real Summarize and the
// real day counts, so the view is checked against the shared readers.
func statsSnapshot(firings []reviews.Firing, surfaced []lessonmodel.Lesson) lessonstats.Snapshot {
	sum := reviews.Summarize(firings, surfaced)
	statsSortRanked(sum.Ranked)

	days, undated := lessonstats.DailyCounts(firings, statsNow)

	return lessonstats.Snapshot{
		LogPath: statsTestLog, LogExists: true, Records: len(firings),
		Summary: sum, Eligible: true, Days: days, Undated: undated,
		Lessons: &lessonstats.LessonCounts{
			Mined: 2, Inactive: 1, Pins: 1,
			TopRegions: []lessonstats.RegionCount{{Region: "api", Lessons: 1}, {Region: "tests", Lessons: 1}},
		},
	}
}

// statsSortRanked adds the tiebreak that Gather applies, so the row
// order in a test is stable.
func statsSortRanked(ranked []reviews.Fired) {
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Matches != b.Matches {
			return a.Matches > b.Matches
		}

		if a.Count != b.Count {
			return a.Count > b.Count
		}

		if a.LastTS != b.LastTS {
			return a.LastTS > b.LastTS
		}

		if a.Region != b.Region {
			return a.Region < b.Region
		}

		return a.Symptom < b.Symptom
	})
}

// newStatsTest builds a Stats view over a loaded snapshot, the same way
// the shell stores a load result and then broadcasts it.
func newStatsTest(snap lessonstats.Snapshot, ascii bool) (*statsView, *fakeServices) {
	f := newFake()
	f.stats = snap

	st := &state{svc: f, theme: newTheme(ascii)}
	st.stats.set(snap, nil, time.Now())

	v := newStatsView(st)
	v.update(loadedMsg{kind: secStats})

	return v, f
}

// statsRender renders the view without escape sequences.
func statsRender(v *statsView, w, h int) string {
	return ansi.Strip(v.view(w, h))
}

// statsDelivery renders the Delivery box body at w cells.
func statsDelivery(v *statsView, snap lessonstats.Snapshot, w int) string {
	return ansi.Strip(strings.Join(v.deliveryLines(snap, w), "\n"))
}

// statsLessons renders the Lessons box body at w cells.
func statsLessons(v *statsView, snap lessonstats.Snapshot, w int) string {
	return ansi.Strip(strings.Join(v.lessonLines(snap, w), "\n"))
}

// statsPress sends key presses to the view and renders after each one,
// like the event loop does.
func statsPress(v *statsView, w, h int, keys ...string) {
	for _, k := range keys {
		v.update(keyMsg(k))
		v.view(w, h)
	}
}

// statsFields returns the fields of the first line that starts with
// label after its indentation.
func statsFields(t *testing.T, body, label string) []string {
	t.Helper()

	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.Fields(line)
		}
	}

	require.Failf(t, "line not found", "no line starts with %q in:\n%s", label, body)

	return nil
}

// statsLast returns the last field of the line that starts with label.
func statsLast(t *testing.T, body, label string) string {
	t.Helper()

	fields := statsFields(t, body, label)

	return fields[len(fields)-1]
}

// statsFlat joins all whitespace, so a phrase matches across wrapped
// lines and box padding.
func statsFlat(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "│", " ")), " ")
}

func TestStatsSummaryMatchesTheAggregation(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	sum := snap.Summary
	v, f := newStatsTest(snap, false)

	body := statsDelivery(v, snap, 80)

	assert.Equal(t, itoa(sum.Total), statsLast(t, body, "Total"))
	assert.Equal(t, itoa(sum.BySurface["hook"]), statsLast(t, body, "hook reminders"))
	assert.Equal(t, itoa(sum.BySurface["change_set"]), statsLast(t, body, "change_set"))
	assert.Equal(t, itoa(sum.BySurface["check"]), statsLast(t, body, "check"))
	assert.Equal(t, itoa(sum.Files), statsLast(t, body, "Distinct files"))

	assert.Equal(t, itoa(sum.InstrumentedHookFirings), statsLast(t, body, "Injected"))
	assert.Equal(t, itoa(sum.RepeatedHookFirings), statsLast(t, body, "repeated within a context"))
	assert.Equal(t, itoa(sum.SuppressedHookFirings), statsLast(t, body, "Suppressed repeats"))
	assert.Equal(t, itoa(sum.HookContextBytes), statsLast(t, body, "Context bytes"))

	// The legacy record has no delivery status: delivered, but not
	// instrumented, and without a client.
	assert.Equal(t, "1", statsLast(t, body, "Without delivery status"))
	assert.Contains(t, body, "recorded before instrumentation; client unknown")

	require.Len(t, sum.HookByClient, 3, "claude, codex and the unattributed bucket")

	for _, r := range sum.HookByClient {
		label := r.Client
		if label == "" {
			label = "no client recorded"
		}

		fields := statsFields(t, body, label)
		assert.Equal(t, []string{itoa(r.Injected), itoa(r.Repeated), itoa(r.Suppressed)}, fields[len(fields)-3:],
			"attribution row %q", label)
	}

	assert.Contains(t, statsFlat(body), "claude pre-tool-use-context")

	// Every lesson keeps its delivered and matched counts and its last
	// recorded match.
	flat := statsFlat(body)
	assert.Contains(t, flat, "PER LESSON "+itoa(len(sum.Ranked)))

	for _, r := range sum.Ranked {
		assert.Contains(t, flat, r.Symptom+" "+r.Region+" · "+itoa(r.Count)+" delivered · "+itoa(r.Matches)+
			" matched · last "+statsWhen(r.LastTS, false, "—"))
	}

	assert.Contains(t, flat, "so matched includes suppressed repeats")

	assert.Contains(t, body, "NEVER DELIVERED  1")
	assert.Contains(t, body, "E999 unused fixture")
	assert.NotContains(t, body, "pinned rule", "a pin is never a never-delivered candidate")

	assert.Empty(t, f.called(), "viewing stats calls no service")
}

func TestStatsDayChartFollowsThePeriod(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	v, _ := newStatsTest(snap, false)

	out := statsRender(v, 120, 35)
	assert.Contains(t, out, "Delivery · last 14 days")
	assert.Contains(t, out, "SURFACED PER DAY")
	assert.Contains(t, out, "Jun 27")
	assert.Contains(t, out, "Jul 10")

	// Eight delivered records fall in the window; six are hook records.
	// The suppressed repeat is not a delivery.
	assert.Contains(t, out, "8 records delivered · 6 surfaced on hook")

	// A 14-day window gets two cells per day and a gap: 41 cells.
	chart := statsChartRows(t, out)
	require.Len(t, chart, statsChartH)
	assert.Equal(t, "██ ██ ██ ██ ██    ██ ██ ██", strings.TrimSpace(chart[0]))

	statsPress(v, 120, 35, "f")
	out = statsRender(v, 120, 35)
	assert.Contains(t, out, "Delivery · last 30 days")
	assert.Contains(t, out, "Jun 11")

	// 30 days do not fit two cells each in 55 cells, so each day takes
	// one cell and no gap.
	chart = statsChartRows(t, out)
	assert.Equal(t, "█████ ███", strings.TrimSpace(chart[0]))

	statsPress(v, 120, 35, "f")
	out = statsRender(v, 120, 35)
	assert.Contains(t, out, "Delivery · last 7 days")
	assert.Contains(t, out, "Jul 4")
	assert.Contains(t, out, "5 records delivered · 3 surfaced on hook")

	statsPress(v, 120, 35, "f")
	assert.Contains(t, statsRender(v, 120, 35), "Delivery · last 14 days")

	// Records without a readable time are named, not silently dropped.
	snap.Undated = 2
	v, _ = newStatsTest(snap, false)
	assert.Contains(t, statsFlat(statsDelivery(v, snap, 55)), "2 delivered records without a readable time are not in the chart.")

	// A snapshot without day counts says so instead of drawing zeros.
	snap.Days = nil
	v, _ = newStatsTest(snap, false)
	body := statsDelivery(v, snap, 55)
	assert.Contains(t, body, "This snapshot has no day counts.")
	assert.NotContains(t, body, "records delivered")
	assert.NotContains(t, statsRender(v, 120, 35), "last 14 days")
}

// statsChartRows returns the chart rows of the Delivery box: the rows
// between the SURFACED PER DAY heading and the date labels.
func statsChartRows(t *testing.T, out string) []string {
	t.Helper()

	var rows []string

	in := false

	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "│ ╭")
		cell := line
		if i < 0 {
			// The right box starts after the left box and one gap.
			parts := strings.SplitN(line, "│ │", 2)
			if len(parts) == 2 {
				cell = parts[1]
			}
		}

		switch {
		case strings.Contains(cell, "SURFACED PER DAY"):
			in = true
		case in && strings.ContainsAny(cell, "▁▂▃▄▅▆▇█"):
			rows = append(rows, strings.TrimSuffix(strings.TrimRight(cell, " "), "│"))
		case in && len(rows) > 0:
			return rows
		}
	}

	return rows
}

func TestStatsLessonsBoxShowsSourcesProposalsAndRegions(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	snap.Lessons = &lessonstats.LessonCounts{
		Mined: 71, Inactive: 9, Pins: 12,
		TopRegions: []lessonstats.RegionCount{{Region: "internal/cli", Lessons: 24}, {Region: "internal/status", Lessons: 11}},
	}

	v, _ := newStatsTest(snap, false)
	v.st.ledger.set(lesLedger(), nil, time.Now())

	out := statsRender(v, 120, 35)
	assert.Contains(t, out, "╭─ Lessons ")
	assert.Contains(t, out, " 83 active ─╮")

	body := statsLessons(v, snap, 56)
	assert.Equal(t, "71", statsLast(t, body, "review-mined"))
	assert.Equal(t, "12", statsLast(t, body, "manual pins"))
	assert.Contains(t, statsFlat(body), "9 more mined lessons are below the threshold or muted, so no surface shows them.")
	assert.Equal(t, "3", statsLast(t, body, "pending"))
	assert.Equal(t, "1", statsLast(t, body, "applied"))
	assert.Equal(t, "1", statsLast(t, body, "dismissed"))
	assert.Equal(t, "24", statsLast(t, body, "internal/cli"))
	assert.Equal(t, "11", statsLast(t, body, "internal/status"))
	assert.NotContains(t, body, "BY TIER", "no lesson tier exists in the data")

	// The bars follow the 1a columns: label 18, bar 30 and the count.
	assert.Equal(t, "review-mined      "+strings.Repeat("█", 26)+strings.Repeat("░", 4)+"   71",
		strings.TrimRight(strings.Split(body, "\n")[1], " "))

	// Unknown sources say why and never render as zero.
	snap.Lessons, snap.LessonsErr = nil, errors.New("no readable index: lesson counts need the mined lessons")
	v, _ = newStatsTest(snap, false)
	v.st.ledger.set(nil, errors.New("database is locked"), time.Now())

	body = statsLessons(v, snap, 56)
	assert.Contains(t, statsFlat(body), "? Unknown: no readable index: lesson counts need the mined lessons")
	assert.Contains(t, statsFlat(body), "? Unknown: database is locked")
	assert.Contains(t, statsRender(v, 120, 35), "? unknown")

	for _, field := range strings.Fields(body) {
		assert.NotEqual(t, "0", field, "an unknown count never renders as zero")
	}
}

func TestStatsMostSurfacedRanksByDeliveredRecords(t *testing.T) {
	var firings []reviews.Firing

	for i, n := range []int{2, 7, 3, 9, 1} {
		lesson := reviews.FiredLesson{Region: "r", Symptom: "lesson-" + itoa(i)}
		for range n {
			firings = append(firings, reviews.Firing{TS: "2026-07-09T10:00:00Z", File: "a.go", Fired: []reviews.FiredLesson{lesson}})
		}
	}

	snap := statsSnapshot(firings, nil)
	v, _ := newStatsTest(snap, false)
	body := statsDelivery(v, snap, 55)

	var top []string

	in := false

	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "MOST SURFACED"):
			in = true
		case in && strings.TrimSpace(line) == "":
			in = false
		case in:
			top = append(top, strings.Fields(line)[0]+" "+strings.Fields(line)[len(strings.Fields(line))-1])
		}
	}

	assert.Equal(t, []string{"lesson-3 9", "lesson-1 7", "lesson-2 3", "lesson-0 2"}, top, "the top four by delivered records")
	assert.Contains(t, body, "MOST SURFACED  retained log", "the heading names its scope")
}

func TestStatsReviewMiningComesFromTheIndexHealth(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	v, _ := newStatsTest(snap, false)

	body := statsFlat(statsDelivery(v, snap, 55))
	assert.Contains(t, body, "REVIEW MINING Reading the index health…")

	mined := time.Now().Add(-6 * time.Hour)
	v.st.index.set(IndexView{Status: &status.Status{
		ReviewsMinedAt: mined.Unix(), Findings: map[string]int{"review": 120, "fix:conventional": 8, "revert": 8},
	}}, nil, time.Now())

	body = statsFlat(statsDelivery(v, snap, 55))
	assert.Contains(t, body, "Last run 6h ago ("+mined.Format(time.DateOnly)+")")
	assert.Contains(t, body, "Findings review 120 · fix:conventional 8 · revert 8")

	v.st.index.set(IndexView{Status: &status.Status{}}, nil, time.Now())
	body = statsFlat(statsDelivery(v, snap, 55))
	assert.Contains(t, body, "Last run never recorded")
	assert.Contains(t, body, "Findings none recorded")
	assert.Contains(t, body, "Mine review findings with seamark index --reviews")

	v.st.index.set(IndexView{StatusErr: errors.New("database is locked")}, nil, time.Now())
	body = statsFlat(statsDelivery(v, snap, 55))
	assert.Contains(t, body, "REVIEW MINING ? Unknown: database is locked")
	assert.NotContains(t, body, "Last run never recorded", "an unread health is unknown, not never")
}

func TestStatsAttributionTableFitsANarrowPane(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	v, _ := newStatsTest(snap, false)

	body := statsDelivery(v, snap, 50)

	for _, line := range strings.Split(body, "\n") {
		assert.LessOrEqual(t, width(line), 50, "line %q", line)
	}

	// The mechanism moves under the client, and the counts keep their
	// columns.
	assert.Contains(t, body, "via pre-tool-use-context")

	fields := statsFields(t, body, "no client recorded")
	assert.Equal(t, []string{"2", "1", "0"}, fields[len(fields)-3:])
}

func TestStatsExplainsMissingClientAttribution(t *testing.T) {
	unattributed := []reviews.Firing{
		statsHook("2026-07-02T10:00:00Z", "", "", reviews.DeliveryInjected, 100, statsAPI),
		statsHook("2026-07-03T10:00:00Z", "", "", reviews.DeliverySuppressedRepeat, 0, statsAPI),
	}

	snap := statsSnapshot(unattributed, nil)
	require.Nil(t, snap.Summary.HookByClient)

	v, _ := newStatsTest(snap, false)
	body := statsFlat(statsDelivery(v, snap, 60))

	assert.Contains(t, body, "every instrumented hook record lacks client attribution")
	assert.NotContains(t, body, "claude")

	legacyOnly := []reviews.Firing{{TS: "2026-07-01T10:00:00Z", File: "api/a.py", Fired: []reviews.FiredLesson{statsAPI}}}
	snap = statsSnapshot(legacyOnly, nil)
	v, _ = newStatsTest(snap, false)
	body = statsFlat(statsDelivery(v, snap, 60))

	assert.Contains(t, body, "No instrumented hook record yet")
	assert.Contains(t, body, "Not in this table: 1 legacy hook reminder with no delivery status and no recorded client")
}

func TestStatsDistinguishesMissingEmptyAndUnreadableLogs(t *testing.T) {
	missing, _ := newStatsTest(lessonstats.Snapshot{LogPath: statsTestLog, Eligible: true}, false)
	empty, _ := newStatsTest(lessonstats.Snapshot{LogPath: statsTestLog, LogExists: true, Eligible: true}, false)
	failed, _ := newStatsTest(lessonstats.Snapshot{
		LogPath: statsTestLog, LogExists: true, Eligible: true,
		LogErr: errors.New("open lessons-audit.jsonl: permission denied"),
	}, false)

	// The view sizes of 120x40, 80x24 and 60x20 terminals.
	for _, size := range [][2]int{{120, 34}, {80, 18}, {60, 14}} {
		w, h := size[0], size[1]

		for _, v := range []*statsView{missing, empty, failed} {
			// A narrow view shows one box; the log state is in the
			// Delivery box.
			v.focus = statsPaneDelivery
		}

		noLog := statsRender(missing, w, h)
		assertFrame(t, noLog, w, h)
		assert.Contains(t, noLog, "No recorded delivery yet")
		assert.NotContains(t, noLog, "holds no records")
		assert.Contains(t, statsFlat(strings.Join(missing.deliveryLines(missing.st.stats.value, 55), "\n")),
			"never records a delivery")

		noRecords := statsRender(empty, w, h)
		assertFrame(t, noRecords, w, h)
		assert.Contains(t, noRecords, "The log exists but holds no records")
		assert.NotContains(t, noRecords, "No recorded delivery yet")

		unreadable := statsRender(failed, w, h)
		assertFrame(t, unreadable, w, h)
		assert.Contains(t, unreadable, "Could not read the delivery log")
		assert.NotContains(t, unreadable, "DELIVERY BY SURFACE")
		assert.NotContains(t, unreadable, "Counts cover")
		assert.NotContains(t, unreadable, "last 14 days")

		for _, field := range strings.Fields(unreadable) {
			assert.NotEqual(t, "0", field, "a failed read never renders as zero")
		}
	}

	assert.Contains(t, statsFlat(statsDelivery(failed, failed.st.stats.value, 55)), "permission denied")
	assert.False(t, keyEnabled(failed.keys(), "f"), "an unreadable log has no chart period")
}

// keyEnabled reports whether the enabled bindings name key k.
func keyEnabled(bindings []key.Binding, k string) bool {
	for _, b := range bindings {
		if b.Enabled() && b.Help().Key == k {
			return true
		}
	}

	return false
}

func TestStatsShowsIncompleteEligibility(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), nil)
	snap.Eligible = false
	snap.EligibleErr = errors.New("no index found; run `seamark index` first")

	v, _ := newStatsTest(snap, false)
	body := statsFlat(statsDelivery(v, snap, 60))

	assert.Contains(t, body, "Incomplete: no index found; run `seamark index` first")
	assert.NotContains(t, body, "NEVER DELIVERED 0", "an incomplete list is not an empty list")

	// The delivery totals do not need the index.
	assert.Contains(t, body, "Total "+itoa(snap.Summary.Total))
}

func TestStatsRendersLoadingAndStaleStates(t *testing.T) {
	st := &state{svc: newFake(), theme: newTheme(false)}
	v := newStatsView(st)

	st.stats.loading = true
	out := statsRender(v, 120, 34)
	assertFrame(t, out, 120, 34)
	assert.Contains(t, out, "Reading delivery statistics")
	assert.Nil(t, v.keys())

	st.stats.set(lessonstats.Snapshot{}, errors.New("stats reader failed"), time.Now())
	assert.Contains(t, statsRender(v, 120, 34), "Could not read delivery statistics")

	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	st.stats.set(snap, nil, time.Now())
	v.update(loadedMsg{kind: secStats})

	st.stats.set(lessonstats.Snapshot{}, errors.New("disk went away"), time.Now())
	require.True(t, st.stats.stale())

	out = statsRender(v, 120, 34)
	assertFrame(t, out, 120, 34)
	assert.Contains(t, out, "! stale: disk went away")
	assert.Contains(t, out, "8 records delivered", "a stale snapshot keeps its old value")
}

func TestStatsLayoutsFitAndScroll(t *testing.T) {
	firings := statsMixedFirings()

	// Many lessons make both boxes scroll.
	for i := range 40 {
		lesson := reviews.FiredLesson{
			Region:  "internal/very/long/region/path/number/" + itoa(i),
			Symptom: "a long normalized review message that needs truncation in the list " + itoa(i),
		}
		firings = append(firings, statsHook("2026-07-08T10:00:00Z", "claude", "ctx-"+itoa(i),
			reviews.DeliveryInjected, 50, lesson))
	}

	surfaced := statsSurfaced()
	for i := range 20 {
		surfaced = append(surfaced, lessonmodel.Lesson{Region: "never/" + itoa(i), Symptom: "cold lesson " + itoa(i), Occurrences: 3})
	}

	snap := statsSnapshot(firings, surfaced)

	for _, size := range [][2]int{{150, 40}, {120, 34}, {98, 20}, {80, 18}, {60, 14}} {
		w, h := size[0], size[1]
		v, _ := newStatsTest(snap, false)

		for _, pane := range []statsPane{statsPaneLessons, statsPaneDelivery} {
			v.focus = pane

			out := statsRender(v, w, h)
			assertFrame(t, out, w, h)

			statsPress(v, w, h, "pgdown", "down", "end", "f")

			out = statsRender(v, w, h)
			assertFrame(t, out, w, h)
		}
	}

	// At 120 columns the boxes follow the 1a widths, and the Delivery
	// box shows its position.
	v, _ := newStatsTest(snap, false)
	out := statsRender(v, 120, 34)
	top := strings.Split(out, "\n")[2]
	assert.True(t, strings.HasPrefix(top, "╭─ Lessons "), top)
	assert.Equal(t, 60, width(top[:strings.Index(top, "╮")+len("╮")]))
	assert.Equal(t, 120, width(top))
	assert.Regexp(t, `1–29 of [0-9]+ ─╯`, out, "a scrolling box shows its position")

	// tab moves the focus to the Delivery box; end shows the scope note.
	statsPress(v, 120, 34, "tab", "end")
	assert.Equal(t, statsPaneDelivery, v.focus)
	assert.Contains(t, statsFlat(statsRender(v, 120, 34)), "Counts cover the retained log")
}

func TestStatsNarrowShowsOneBoxAtATime(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	v, f := newStatsTest(snap, false)

	out := statsRender(v, 80, 18)
	assert.Contains(t, out, "╭─ Lessons ")
	assert.NotContains(t, out, "Delivery")

	statsPress(v, 80, 18, "tab")
	out = statsRender(v, 80, 18)
	assert.Contains(t, out, "╭─ Delivery · last 14 days")
	assert.NotContains(t, out, "╭─ Lessons")

	statsPress(v, 80, 18, "shift+tab")
	assert.Equal(t, statsPaneLessons, v.focus)
	assert.Empty(t, f.called(), "browsing stats calls no service")
}

func TestStatsKeysFollowTheFocus(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	v, _ := newStatsTest(snap, false)
	v.view(120, 34)

	assert.True(t, keyEnabled(v.keys(), "tab"))
	assert.True(t, keyEnabled(v.keys(), "f"))
	assert.False(t, keyEnabled(v.keys(), "↑↓"), "the Lessons box fits, so it does not scroll")

	statsPress(v, 120, 34, "tab")
	assert.True(t, keyEnabled(v.keys(), "↑↓"), "the Delivery box scrolls")
	assert.False(t, v.capturing())
}

func TestStatsNeverClaimsEffectiveness(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())

	var outputs []string

	for _, s := range []lessonstats.Snapshot{
		snap,
		{LogPath: statsTestLog},
		{LogPath: statsTestLog, LogExists: true},
		{LogPath: statsTestLog, LogExists: true, LogErr: errors.New("boom")},
	} {
		v, _ := newStatsTest(s, false)

		for _, pane := range []statsPane{statsPaneLessons, statsPaneDelivery} {
			v.focus = pane
			outputs = append(outputs, statsRender(v, 120, 34), statsRender(v, 80, 18))
		}

		outputs = append(outputs, statsDelivery(v, s, 80), statsLessons(v, s, 56))
	}

	for _, out := range outputs {
		lower := strings.ToLower(out)
		assert.NotContains(t, lower, "effective")
		assert.NotContains(t, lower, "working")
	}
}

func TestStatsKeepsControlSequencesInert(t *testing.T) {
	hostile := reviews.FiredLesson{Region: "api\x07", Symptom: "\x1b[31mred\x1b[0m \x1b]0;title\x07 text"}
	firings := []reviews.Firing{
		statsHook("2026-07-02T10:00:00Z", "evil\x1b[2J", "ctx", reviews.DeliveryInjected, 10, hostile),
		{TS: "2026-07-03T10:00:00Z", Surface: "surface\x1b[1m", Files: []string{"a"}, Fired: []reviews.FiredLesson{hostile}},
	}

	snap := statsSnapshot(firings, []lessonmodel.Lesson{{Region: "never\x1b[5m", Symptom: "cold\u009b1m", Occurrences: 2}})
	snap.LogPath = "/work/repo/\x1b[31m.seamark/lessons-audit.jsonl"
	snap.Lessons.TopRegions = []lessonstats.RegionCount{{Region: "evil\x1b]8;;x\x07", Lessons: 1}}

	v, _ := newStatsTest(snap, false)
	v.st.index.set(IndexView{Status: &status.Status{Findings: map[string]int{"src\x1b[1m": 2}}}, nil, time.Now())

	for _, pane := range []statsPane{statsPaneLessons, statsPaneDelivery} {
		v.focus = pane

		for _, out := range []string{v.view(120, 34), v.view(80, 18), strings.Join(v.deliveryLines(snap, 60), "\n"),
			strings.Join(v.lessonLines(snap, 56), "\n")} {
			assert.NotContains(t, out, "\x1b")
			assert.NotContains(t, out, "\x07")
			assert.NotContains(t, out, "\u009b")
		}
	}

	assert.Contains(t, statsDelivery(v, snap, 80), "[31mred[0m", "the text stays visible, only inert")
}

func TestStatsASCIIThemeUsesOnlyASCII(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())

	for _, s := range []lessonstats.Snapshot{snap, {LogPath: statsTestLog}, {LogPath: statsTestLog, LogExists: true}} {
		v, _ := newStatsTest(s, true)

		for _, pane := range []statsPane{statsPaneLessons, statsPaneDelivery} {
			v.focus = pane

			for _, out := range []string{v.view(120, 34), v.view(80, 18), v.view(60, 14),
				strings.Join(v.deliveryLines(s, 55), "\n")} {
				for _, r := range out {
					require.Less(t, r, rune(0x80), "non-ASCII rune %q in:\n%s", r, out)
				}
			}
		}
	}

	st := &state{svc: newFake(), theme: newTheme(true)}
	for _, r := range newStatsView(st).view(120, 34) {
		require.Less(t, r, rune(0x80), "the loading state is ASCII")
	}
}

func TestStatsInTheLessonsDestination(t *testing.T) {
	f := newFake()
	f.stats = statsSnapshot(statsMixedFirings(), statsSurfaced())
	f.ledger = lesLedger()

	h := newHarness(t, f, 120, 40)
	h.send(navigateMsg{dest: destLessons, intent: "stats"})

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, " Proposals   Stats   p / s")
	assert.Contains(t, frame, "╭─ Lessons ")
	assert.Contains(t, frame, "╭─ Delivery · last 14 days")
	assert.Contains(t, frame, "p proposals   tab pane   f period")

	h.press("tab", "end", "f")
	assert.Contains(t, h.view(), "Delivery · last 30 days")

	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	h.press("p")
	assert.Equal(t, lesProposals, h.m.screens[destLessons].(*lessons).sub)
	assert.NotContains(t, h.m.View(), "\x1b", "no raw escape reaches the frame")
	assert.Empty(t, f.called(), "browsing stats plans, previews and mutates nothing")
}
