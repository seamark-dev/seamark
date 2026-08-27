package bench

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// OTelHistogramInstanceID selects the pinned OpenTelemetry Go task with
	// the lesson delivered at the explicit-histogram trigger.
	OTelHistogramInstanceID = "opentelemetry-go-histogram-reset-v1"
	// OTelHistogramRule is the treatment lesson's stable identity.
	OTelHistogramRule = "keep-histogram-reset-paths-in-sync"
	// OTelHistogramScopingFamily binds the trigger and repair variants into a
	// matched public-repository delivery experiment.
	OTelHistogramScopingFamily = "opentelemetry-go-histogram-reset-scoping-v1"

	otelHistogramRepository = "https://github.com/open-telemetry/opentelemetry-go.git"
	otelHistogramBaseCommit = "0eb89a5210e64df2f38611b95d1ae0afd6b88fd7"
	otelHistogramVendorSHA  = "2635fdb7bc52a84e311ead3490d04fffddc368b4805eb822c23ac2e634f12c7a"
	otelHistogramModule     = "sdk/metric"
	otelHistogramTrigger    = "sdk/metric/internal/aggregate/histogram.go"
	otelHistogramRepair     = "sdk/metric/internal/aggregate/exponential_histogram.go"

	otelHistogramTask = `Fix the metric SDK so a reused destination datapoint from a delta explicit-bucket ` +
		`histogram does not retain ` + "`Sum`, `Min`, or `Max`" + ` values from an earlier aggregation when ` +
		`the later delta histogram has sum or min/max recording disabled. The same destination aggregation may ` +
		`be reused across collectors with different recording settings. Keep the change focused and add or update ` +
		`tests as appropriate. Before finishing, verify the result with ` +
		"`go -C sdk/metric test ./internal/aggregate`."

	otelHistogramLessonYAML = `pin:
  - rule: ` + OTelHistogramRule + `
    region: ` + otelHistogramTrigger + `
    note: When changing reused explicit-histogram datapoint reset semantics, make the equivalent change in sdk/metric/internal/aggregate/exponential_histogram.go for both delta and cumulative collection; reused destinations can otherwise leak stale Sum, Min, or Max values.
`

	otelHistogramPlaceboYAML = `pin:
  - rule: preserve-aggregation-hot-paths
    region: ` + otelHistogramTrigger + `
    note: Keep aggregation hot paths allocation-conscious, preserve generic numeric support, and add focused tests that follow the neighboring metricdata assertion style.
`
)

var otelHistogramRepositorySpec = publicRepositorySpec{
	InstanceID: OTelHistogramInstanceID,
	CacheKey:   "opentelemetry-go-" + otelHistogramBaseCommit,
	URL:        otelHistogramRepository,
	Commit:     otelHistogramBaseCommit,
	ModuleDir:  otelHistogramModule,
	VendorSHA:  otelHistogramVendorSHA,
}

// OTelHistogramInstance is a pinned public-repository task derived from
// open-telemetry/opentelemetry-go#8399 and its merged fix #8403. The reported
// reproduction exercises explicit histograms. The owner invariant is parallel
// reset behavior in both exponential-histogram collection paths.
func OTelHistogramInstance() Instance {
	return Instance{
		ID:               OTelHistogramInstanceID,
		Rule:             OTelHistogramRule,
		Task:             otelHistogramTask,
		LessonYAML:       otelHistogramLessonYAML,
		PlaceboYAML:      otelHistogramPlaceboYAML,
		Generate:         otelHistogramRepositorySpec.generate,
		Prepare:          otelHistogramRepositorySpec.prepare,
		Judge:            judgeOTelHistogram,
		ApplyGold:        applyOTelHistogramGold,
		ApplyNaive:       applyOTelHistogramNaive,
		JudgeVersion:     "opentelemetry-go-histogram-reset-judge-v1",
		ComparisonFamily: OTelHistogramScopingFamily,
		ProtocolInstance: OTelHistogramInstanceID,
		sourceFile:       "otel_histogram_fixture.go",
		Checks: []Command{{
			Name: "go",
			Args: []string{"-C", otelHistogramModule, "test", "./internal/aggregate"},
		}},
		ExploreFiles: []string{
			otelHistogramTrigger,
			otelHistogramRepair,
			"sdk/metric/internal/aggregate/histogram_test.go",
			"sdk/metric/internal/aggregate/exponential_histogram_test.go",
			"sdk/metric/internal/aggregate/aggregate.go",
			"sdk/metric/internal/aggregate/aggregate_test.go",
			"sdk/metric/README.md",
		},
	}
}

func judgeOTelHistogram(dir string) (Verdict, error) {
	taskPass, err := runOTelHistogramJudgeTest(dir, otelHistogramTaskTest)
	if err != nil {
		return Verdict{}, err
	}

	if !taskPass {
		return Verdict{Notes: "explicit histogram datapoint reuse still leaks stale fields"}, nil
	}

	invariantPass, err := runOTelHistogramJudgeTest(dir, otelHistogramInvariantTest)
	if err != nil {
		return Verdict{}, err
	}

	if !invariantPass {
		return Verdict{
			TaskDone: true,
			Notes:    "explicit histogram reuse is fixed; exponential histogram reset paths are stale",
		}, nil
	}

	return Verdict{
		TaskDone: true,
		Avoided:  true,
		Notes:    "explicit and exponential histogram reset paths clear reused datapoints",
	}, nil
}

func runOTelHistogramJudgeTest(dir, content string) (bool, error) {
	path := filepath.Join(dir, filepath.FromSlash(
		"sdk/metric/internal/aggregate/zz_seamark_bench_histogram_test.go",
	))

	test, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(path) }()

	if n, err := test.WriteString(content); err != nil {
		_ = test.Close()
		return false, err
	} else if n != len(content) {
		_ = test.Close()
		return false, io.ErrShortWrite
	}

	if err := test.Close(); err != nil {
		return false, err
	}

	return runJudgeCommand(dir, "go", "-C", otelHistogramModule, "test",
		"./internal/aggregate", "-run", "^TestSeamarkBench", "-count=1")
}

func applyOTelHistogramNaive(dir string) error {
	return applyGitPatch(dir, otelHistogramNaivePatch)
}

func applyOTelHistogramGold(dir string) error {
	if err := applyOTelHistogramNaive(dir); err != nil {
		return err
	}

	return applyGitPatch(dir, otelHistogramCompanionPatch)
}

func applyGitPatch(dir, patch string) error {
	cmd := exec.Command("git", "-C", dir, "apply", "--whitespace=nowarn", "-")
	cmd.Stdin = strings.NewReader(patch)

	if out, err := cmd.CombinedOutput(); err != nil {
		return &execPatchError{err: err, output: string(out)}
	}

	return nil
}

type execPatchError struct {
	err    error
	output string
}

func (e *execPatchError) Error() string {
	return "apply canonical benchmark patch: " + e.err.Error() + "\n" + e.output
}

func (e *execPatchError) Unwrap() error { return e.err }

const otelHistogramNaivePatch = `diff --git a/sdk/metric/internal/aggregate/histogram.go b/sdk/metric/internal/aggregate/histogram.go
index 701da7a..4b021b1 100644
--- a/sdk/metric/internal/aggregate/histogram.go
+++ b/sdk/metric/internal/aggregate/histogram.go
@@ -210,13 +210,16 @@ func (s *deltaHistogram[N]) collect(
 
 		if !s.noSum {
 			hDPts[i].Sum = val.total.load()
+		} else {
+			hDPts[i].Sum = 0
 		}
 
-		if !s.noMinMax {
-			if val.minMax.set.Load() {
-				hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
-				hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
-			}
+		if !s.noMinMax && val.minMax.set.Load() {
+			hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
+			hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
+		} else {
+			hDPts[i].Min = metricdata.Extrema[N]{}
+			hDPts[i].Max = metricdata.Extrema[N]{}
 		}
 
 		collectExemplars(&hDPts[i].Exemplars, val.res.Collect)
`

const otelHistogramCompanionPatch = `diff --git a/sdk/metric/internal/aggregate/exponential_histogram.go b/sdk/metric/internal/aggregate/exponential_histogram.go
index 767b1d6..e3c68c2 100644
--- a/sdk/metric/internal/aggregate/exponential_histogram.go
+++ b/sdk/metric/internal/aggregate/exponential_histogram.go
@@ -412,12 +412,15 @@ func (e *expoHistogram[N]) delta(
 
 		if !e.noSum {
 			hDPts[i].Sum = val.sum.load()
+		} else {
+			hDPts[i].Sum = 0
 		}
-		if !e.noMinMax {
-			if val.minMax.set.Load() {
-				hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
-				hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
-			}
+		if !e.noMinMax && val.minMax.set.Load() {
+			hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
+			hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
+		} else {
+			hDPts[i].Min = metricdata.Extrema[N]{}
+			hDPts[i].Max = metricdata.Extrema[N]{}
 		}
 
 		collectExemplars(&hDPts[i].Exemplars, val.res.Collect)
@@ -488,12 +491,15 @@ func (e *expoHistogram[N]) cumulative(
 
 		if !e.noSum {
 			hDPts[i].Sum = val.sum.load()
+		} else {
+			hDPts[i].Sum = 0
 		}
-		if !e.noMinMax {
-			if val.minMax.set.Load() {
-				hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
-				hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
-			}
+		if !e.noMinMax && val.minMax.set.Load() {
+			hDPts[i].Min = metricdata.NewExtrema(val.minMax.minimum.Load())
+			hDPts[i].Max = metricdata.NewExtrema(val.minMax.maximum.Load())
+		} else {
+			hDPts[i].Min = metricdata.Extrema[N]{}
+			hDPts[i].Max = metricdata.Extrema[N]{}
 		}
 
 		collectExemplars(&hDPts[i].Exemplars, val.res.Collect)
`

const otelHistogramTaskTest = `package aggregate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSeamarkBenchExplicitHistogramReuse(t *testing.T) {
	c := new(clock)
	t.Cleanup(c.Register())
	alice := attribute.NewSet(attribute.String("user", "alice"))

	in1, out1 := Builder[int64]{
		Temporality: metricdata.DeltaTemporality,
	}.ExplicitBucketHistogram([]float64{1, 5}, false, false)
	in1(t.Context(), 5, alice)

	dest := new(metricdata.Aggregation)
	require.Equal(t, 1, out1(dest))

	in2, out2 := Builder[int64]{
		Temporality: metricdata.DeltaTemporality,
	}.ExplicitBucketHistogram([]float64{1, 5}, true, true)
	in2(t.Context(), 7, alice)
	require.Equal(t, 1, out2(dest))

	histogram := (*dest).(metricdata.Histogram[int64])
	require.Len(t, histogram.DataPoints, 1)
	point := histogram.DataPoints[0]
	assert.Zero(t, point.Sum, "stale Sum leaked")
	_, minDefined := point.Min.Value()
	_, maxDefined := point.Max.Value()
	assert.False(t, minDefined, "stale Min leaked")
	assert.False(t, maxDefined, "stale Max leaked")
}
`

const otelHistogramInvariantTest = `package aggregate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSeamarkBenchExponentialHistogramReuse(t *testing.T) {
	for _, temporality := range []metricdata.Temporality{
		metricdata.DeltaTemporality,
		metricdata.CumulativeTemporality,
	} {
		t.Run(temporality.String(), func(t *testing.T) {
			c := new(clock)
			t.Cleanup(c.Register())
			alice := attribute.NewSet(attribute.String("user", "alice"))

			in1, out1 := Builder[int64]{Temporality: temporality}.
				ExponentialBucketHistogram(4, 20, false, false)
			in1(t.Context(), 5, alice)

			dest := new(metricdata.Aggregation)
			require.Equal(t, 1, out1(dest))

			in2, out2 := Builder[int64]{Temporality: temporality}.
				ExponentialBucketHistogram(4, 20, true, true)
			in2(t.Context(), 7, alice)
			require.Equal(t, 1, out2(dest))

			histogram := (*dest).(metricdata.ExponentialHistogram[int64])
			require.Len(t, histogram.DataPoints, 1)
			point := histogram.DataPoints[0]
			assert.Zero(t, point.Sum, "stale Sum leaked")
			_, minDefined := point.Min.Value()
			_, maxDefined := point.Max.Value()
			assert.False(t, minDefined, "stale Min leaked")
			assert.False(t, maxDefined, "stale Max leaked")
		})
	}
}
`
