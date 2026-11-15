package indexhealth

import (
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// healthStore opens an empty index database in a temporary directory.
func healthStore(t *testing.T) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	var err error

	st, err = store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	return st, root
}

// TestReadCallsTheFingerprintOnlyWhenOneIsRecorded proves that Read runs
// git only for an index that records a fingerprint. orient pays for that
// call on every request, so an index without one must not pay it.
func TestReadCallsTheFingerprintOnlyWhenOneIsRecorded(t *testing.T) {
	st, root := healthStore(t)

	calls := 0
	fingerprint := func(dir string) string {
		calls++

		assert.Equal(t, root, dir)

		return "fp-now"
	}

	h, err := Read(st, root, fingerprint)
	require.NoError(t, err)
	assert.Zero(t, calls, "no recorded fingerprint, no git run")
	assert.Equal(t, FreshUnknown, h.Freshness)
	assert.False(t, h.Coverage.Known, "a missing summary is unknown coverage")
	assert.Zero(t, h.IndexedAt)
	assert.Nil(t, h.EdgeOrigins)

	require.NoError(t, st.SetMeta(store.MetaIndexedState, "fp-now"))
	require.NoError(t, st.SetMeta(store.MetaIndexedAt, "1700000000"))
	require.NoError(t, st.SetMeta(store.MetaIndexSummary,
		`{"files_seen":4,"files_parsed":3,"files_skipped":1,"parse_errors":0}`))

	h, err = Read(st, root, fingerprint)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, FreshCurrent, h.Freshness)
	assert.Equal(t, int64(1700000000), h.IndexedAt)
	assert.Equal(t, Coverage{Known: true, FilesSeen: 4, FilesParsed: 3, FilesSkipped: 1}, h.Coverage)
}

// TestReadReturnsStoreErrors proves that a failed read is an error, not
// an empty health record that reads as a clean index.
func TestReadReturnsStoreErrors(t *testing.T) {
	st, root := healthStore(t)
	require.NoError(t, st.Close())

	_, err := Read(st, root, func(string) string { return "" })
	assert.Error(t, err)
}

// TestReadReturnsAnOriginCountError proves that a failed origin count is
// an error. The meta reads succeed here, so only the origin count fails.
// Without the error, orient prints "none indexed (0 calls)" for an index
// that it cannot read.
func TestReadReturnsAnOriginCountError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	st, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// A read-only open runs no migration, so the edge table stays gone.
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	_, err = db.Exec(`ALTER TABLE edge RENAME TO edge_gone`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	ro, err := store.OpenReadOnly(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })

	_, err = Read(ro, t.TempDir(), func(string) string { return "" })
	assert.Error(t, err, "a failed origin count is not zero origins")
}

// TestReadCountsOnlyCallEdges proves that the origins hold CALLS edges
// only. Structural edges have no resolution doubt and would dilute the
// call shares.
func TestReadCountsOnlyCallEdges(t *testing.T) {
	st, root := healthStore(t)

	run := model.Symbol{FQN: "pkg.Run", Name: "Run", Kind: model.KindFunction, File: "a.go"}
	helper := model.Symbol{FQN: "pkg.Helper", Name: "Helper", Kind: model.KindFunction, File: "b.go"}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for _, s := range []*model.Symbol{&run, &helper} {
			if err := tx.InsertSymbol(s); err != nil {
				return err
			}
		}

		for _, e := range []model.Edge{
			{Src: run.ID, Dst: helper.ID, Kind: model.EdgeCalls, Origin: model.OriginSamePackage},
			{Src: run.ID, Dst: helper.ID, Kind: model.EdgeDefines, Origin: model.OriginParse},
			{Src: run.ID, Dst: helper.ID, Kind: model.EdgeImports, Origin: model.OriginParse},
		} {
			if err := tx.InsertEdge(e); err != nil {
				return err
			}
		}

		return nil
	}))

	h, err := Read(st, root, func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, map[string]int{model.OriginSamePackage: 1}, h.EdgeOrigins)
}

func TestFreshness(t *testing.T) {
	assert.Equal(t, FreshUnknown, Freshness("", "fp"), "no recorded fingerprint")
	assert.Equal(t, FreshUnknown, Freshness("", ""), "an empty fingerprint is never a match")
	assert.Equal(t, FreshCurrent, Freshness("fp", "fp"))
	assert.Equal(t, FreshStale, Freshness("fp", "other"))
	assert.Equal(t, FreshStale, Freshness("fp", ""), "a recorded fingerprint without git now is stale")
}

// TestParseCoverageIsStrict proves that only a summary the indexer can
// write reads as known. Every other form reads as unknown, so a damaged
// summary never prints as full coverage.
func TestParseCoverageIsStrict(t *testing.T) {
	// The counts at the int limit build their text from math.MaxInt, so
	// the cases hold on every platform.
	maxInt := strconv.Itoa(math.MaxInt)

	known := []struct {
		summary string
		want    Coverage
	}{
		{`{"files_seen":3,"files_parsed":2,"parse_errors":1}`, Coverage{Known: true, FilesSeen: 3, FilesParsed: 2, ParseErrors: 1}},
		{`{"files_deleted":2,"files_parsed":5,"files_seen":9,"files_skipped":1,"parse_errors":3}`, Coverage{
			Known: true, FilesSeen: 9, FilesParsed: 5, FilesSkipped: 1, FilesDeleted: 2, ParseErrors: 3,
		}},
		{`{"files_seen":0,"files_parsed":0,"parse_errors":0}`, Coverage{Known: true}},
		{`{"files_seen":5,"files_parsed":1,"parse_errors":0,"extra":"x"}`, Coverage{Known: true, FilesSeen: 5, FilesParsed: 1}},
		{` { "files_seen" : 2 , "files_parsed" : 2 , "parse_errors" : 0 } `, Coverage{Known: true, FilesSeen: 2, FilesParsed: 2}},
		{`{"files_seen":` + maxInt + `,"files_parsed":` + maxInt + `,"parse_errors":0}`, Coverage{
			Known: true, FilesSeen: math.MaxInt, FilesParsed: math.MaxInt,
		}},
	}

	for _, c := range known {
		assert.Equal(t, c.want, ParseCoverage(c.summary), c.summary)
	}

	unknown := []string{
		``,
		`not json`,
		`null`,
		`[]`,
		`{}`,
		`3`,
		`"files_seen"`,
		`{"files_parsed":1,"parse_errors":0}`,
		`{"files_parsed":0,"parse_errors":0}`,
		`{"files_seen":1,"parse_errors":0}`,
		`{"files_seen":1,"files_parsed":1}`,
		`{"files_seen":"3","files_parsed":1,"parse_errors":0}`,
		`{"files_seen":3.5,"files_parsed":1,"parse_errors":0}`,
		`{"files_seen":3,"files_parsed":1.0,"parse_errors":0}`,
		`{"files_seen":3,"files_parsed":1,"parse_errors":null}`,
		`{"files_seen":3,"files_parsed":1,"parse_errors":0,"files_skipped":"1"}`,
		`{"files_seen":3,"files_parsed":1,"parse_errors":0,"files_deleted":-1}`,
		`{"files_seen":-1,"files_parsed":0,"parse_errors":0}`,
		`{"files_seen":3,"files_parsed":5,"parse_errors":0}`,
		`{"files_seen":3,"files_parsed":2,"files_skipped":1,"parse_errors":1}`,
		`{"files_seen":99999999999999999999,"files_parsed":0,"parse_errors":0}`,
		// A sum of counts near the int limit wraps to a negative value.
		// The check must still find it above seen.
		`{"files_seen":0,"files_parsed":` + maxInt + `,"files_skipped":1,"parse_errors":0}`,
		`{"files_seen":` + maxInt + `,"files_parsed":` + maxInt + `,"files_skipped":1,"parse_errors":0}`,
		`{"files_seen":` + maxInt + `,"files_parsed":` + maxInt + `,"parse_errors":` + maxInt + `}`,
	}

	for _, summary := range unknown {
		assert.Equal(t, Coverage{}, ParseCoverage(summary), summary)
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }

	assert.Equal(t, "5m", Age(ago(5*time.Minute), now))
	assert.Equal(t, "0m", Age(now.Unix(), now))
	assert.Equal(t, "3h", Age(ago(3*time.Hour), now))
	assert.Equal(t, "59m", Age(ago(time.Hour-time.Second), now))
	assert.Equal(t, "1h", Age(ago(time.Hour), now), "one hour or more prints hours")
	assert.Equal(t, "47h", Age(ago(47*time.Hour), now))
	assert.Equal(t, "47h", Age(ago(48*time.Hour-time.Second), now))
	assert.Equal(t, "2d", Age(ago(48*time.Hour), now), "48 hours or more print days")
	assert.Equal(t, "2d", Age(ago(50*time.Hour), now))
	assert.Equal(t, "unknown", Age(0, now))
	assert.Equal(t, "unknown", Age(-5, now))
	assert.Equal(t, "unknown", Age(now.Add(time.Minute).Unix(), now), "a future time has no age")
}

// TestParseUnix proves that only an integer in the int64 range gives a
// time. An out-of-range value gives 0 and not the int64 limit, which
// would print as a time in a far year.
func TestParseUnix(t *testing.T) {
	assert.Equal(t, int64(1700000000), ParseUnix("1700000000"))
	assert.Equal(t, int64(-60), ParseUnix("-60"))
	assert.Zero(t, ParseUnix(""))
	assert.Zero(t, ParseUnix("abc"))
	assert.Zero(t, ParseUnix("1.5"))
	assert.Zero(t, ParseUnix("99999999999999999999"), "above the int64 range")
	assert.Zero(t, ParseUnix("-99999999999999999999"), "below the int64 range")
}

// TestOriginNamesAreBoundedAndScrubbed proves that a name from the
// database cannot act on the terminal, leak a secret, or grow the line.
func TestOriginNamesAreBoundedAndScrubbed(t *testing.T) {
	assert.Equal(t, "100.0% (unknown) (2 calls)", OriginSummary(map[string]int{"": 2}, 0),
		"the schema default is not a known origin")

	escaped := OriginSummary(map[string]int{"evil\x1b]0;title\x07name": 1}, 0)
	assert.NotContains(t, escaped, "\x1b")
	assert.NotContains(t, escaped, "\x07")

	secret := OriginSummary(map[string]int{"API_TOKEN=hunter2": 1}, 0)
	assert.NotContains(t, secret, "hunter2")
	assert.Contains(t, secret, "[REDACTED]")

	// The bound is 32 runes. A name of exactly 32 runes prints whole,
	// and a longer name prints 31 runes and an ellipsis.
	exact := strings.Repeat("x", 32)
	assert.Equal(t, "100.0% "+exact+" (1 calls)", OriginSummary(map[string]int{exact: 1}, 0))

	for _, long := range []string{strings.Repeat("y", 33), strings.Repeat("ab", 30)} {
		line := OriginSummary(map[string]int{long: 1}, 0)
		name := strings.TrimSuffix(strings.TrimPrefix(line, "100.0% "), " (1 calls)")
		assert.Equal(t, 32, utf8.RuneCountInString(name), line)
		assert.Equal(t, long[:31]+"…", name, line)
	}
}

// TestOriginSummaryFoldsPastTheLimit proves the bound of the orient line.
// The folded entry keeps the shares at a sum of 100.0.
func TestOriginSummaryFoldsPastTheLimit(t *testing.T) {
	origins := map[string]int{
		model.OriginSamePackage: 40, model.OriginQualified: 20, model.OriginUniqueName: 15,
		model.OriginSameClass: 10, "o1": 7, "o2": 5, "o3": 3,
	}

	line := OriginSummary(origins, 5)
	assert.Equal(t, "40.0% same-package · 20.0% qualified · 15.0% unique-name · 10.0% same-class · "+
		"7.0% o1 · 8.0% in 2 more origins (100 calls)", line)

	all := OriginSummary(origins, 0)
	assert.Equal(t, 7, strings.Count(all, "%"), "limit 0 prints every origin")
	assert.NotContains(t, all, "more origins")

	assert.Equal(t, all, OriginSummary(origins, 7), "a limit equal to the count folds nothing")
}

// TestOriginSummaryRoundsSharesToOneDecimal proves that the call-resolution
// shares have one decimal and sum to 100.0. Truncated whole percents lost up
// to one point per origin, so the old line summed to 98 on a real index.
func TestOriginSummaryRoundsSharesToOneDecimal(t *testing.T) {
	cases := []struct {
		origins map[string]int
		want    string
	}{
		{
			map[string]int{"qualified": 1, "same-class": 1, "same-package": 1},
			"33.4% qualified · 33.3% same-class · 33.3% same-package (3 calls)",
		},
		{
			map[string]int{"unique-name": 103, "qualified": 99, "same-class": 99, "same-package": 99},
			"25.8% unique-name · 24.8% qualified · 24.7% same-class · 24.7% same-package (400 calls)",
		},
		{
			map[string]int{"qualified": 3907, "same-class": 2664, "same-package": 21597, "unique-name": 4888},
			"65.3% same-package · 14.8% unique-name · 11.8% qualified · 8.1% same-class (33056 calls)",
		},
		{
			// A count that rounds to zero prints 0.0%, so the line still sums to 100.0.
			map[string]int{"same-package": 29999, "unique-name": 1},
			"100.0% same-package · 0.0% unique-name (30000 calls)",
		},
		{map[string]int{}, "none"},
		{map[string]int{"qualified": 0}, "none"},
	}

	for _, c := range cases {
		assert.Equal(t, c.want, OriginSummary(c.origins, 0))
	}
}

// TestOriginSummarySharesAlwaysSumTo100 checks random maps from a fixed
// seed. Each line must sum to 1000 tenths, and a share must never rise
// along the display order.
func TestOriginSummarySharesAlwaysSumTo100(t *testing.T) {
	names := []string{"qualified", "same-class", "same-package", "unique-name", "parse", "other"}
	share := regexp.MustCompile(`^(\d+)\.(\d)% \S+$`)
	rng := rand.New(rand.NewPCG(1, 2))

	for run := range 200 {
		origins := map[string]int{}
		calls := 0

		for _, name := range names[:1+rng.IntN(len(names))] {
			origins[name] = rng.IntN(50001)
			calls += origins[name]
		}

		line := OriginSummary(origins, 0)

		if calls == 0 {
			assert.Equal(t, "none", line, "run %d", run)

			continue
		}

		body, ok := strings.CutSuffix(line, fmt.Sprintf(" (%d calls)", calls))
		require.True(t, ok, "run %d: %q", run, line)

		sum, prev := 0, 1001

		for _, part := range strings.Split(body, " · ") {
			m := share.FindStringSubmatch(part)
			require.NotNil(t, m, "run %d: part %q", run, part)

			whole, err := strconv.Atoi(m[1])
			require.NoError(t, err)

			tenth, err := strconv.Atoi(m[2])
			require.NoError(t, err)

			value := whole*10 + tenth
			assert.LessOrEqual(t, value, prev, "run %d: %q", run, line)

			sum += value
			prev = value
		}

		assert.Equal(t, 1000, sum, "run %d: %q", run, line)
	}
}
