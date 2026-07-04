package stats

import (
	"math"
	"sort"
	"testing"
)

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) <= 1e-12*math.Max(1, math.Abs(want))
}

func TestLogAveragePredictionFormula(t *testing.T) {
	history := []RunStat{{RunID: 3, Avg: math.Exp(3)}, {RunID: 2, Avg: math.Exp(2)}, {RunID: 1, Avg: math.Exp(1)}}
	baseline, err := ComputeBaseline(history, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !closeEnough(baseline.LogMean, 2) || !closeEnough(baseline.BaselineNs, math.Exp(2)) {
		t.Fatalf("baseline log/ns = %v/%v", baseline.LogMean, baseline.BaselineNs)
	}
	if !closeEnough(baseline.Variance, 1) || !closeEnough(baseline.PredictionSE, math.Sqrt(1+1.0/3.0)) {
		t.Fatalf("variance/prediction SE = %v/%v", baseline.Variance, baseline.PredictionSE)
	}

	result := DetectRegression(RunStat{RunID: 4, Avg: math.Exp(4)}, baseline)
	wantT := 2 / math.Sqrt(1+1.0/3.0)
	if result.DegreesOfFreedom != 2 || !closeEnough(*result.TScore, wantT) {
		t.Fatalf("df/t = %d/%v, want 2/%v", result.DegreesOfFreedom, *result.TScore, wantT)
	}
	if !closeEnough(*result.PValue, tDistSurvival(wantT, 2)) {
		t.Fatalf("p = %v", *result.PValue)
	}
	if !closeEnough(*result.ChangePercent, (math.Exp(2)-1)*100) ||
		!closeEnough(*result.AbsoluteChangeNs, math.Exp(4)-math.Exp(2)) {
		t.Fatalf("relative/absolute = %v/%v", *result.ChangePercent, *result.AbsoluteChangeNs)
	}
}

func TestPredictionScoreZeroVariance(t *testing.T) {
	baseline, err := ComputeBaseline([]RunStat{{RunID: 3, Avg: 100}, {RunID: 2, Avg: 100}, {RunID: 1, Avg: 100}}, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		avg float64
		p   float64
		inf int
	}{
		{avg: 110, p: 0, inf: 1},
		{avg: 100, p: 0.5, inf: 0},
		{avg: 90, p: 1, inf: -1},
	}
	for _, test := range tests {
		result := DetectRegression(RunStat{Avg: test.avg}, baseline)
		if *result.PValue != test.p {
			t.Fatalf("avg %v: p=%v", test.avg, *result.PValue)
		}
		if test.inf != 0 && !math.IsInf(*result.TScore, test.inf) {
			t.Fatalf("avg %v: t=%v", test.avg, *result.TScore)
		}
	}
}

func TestPredictionScoreInvalidAveragesAndInvocationStatsIrrelevance(t *testing.T) {
	history := []RunStat{{RunID: 4, Avg: 100}, {RunID: 3, Avg: 101}, {RunID: 2, Avg: 0}, {RunID: 1, Avg: -1}}
	baseline, err := ComputeBaseline(history, 2, 0)
	if err != nil || baseline.PointCount != 2 {
		t.Fatalf("baseline count/error = %d/%v", baseline.PointCount, err)
	}
	if DetectRegression(RunStat{Avg: 0}, baseline).Status != "insufficient" {
		t.Fatal("zero latest average must be invalid")
	}
}

func TestPredictionScoreIsSlowdownDirectionForImprovements(t *testing.T) {
	baseline, err := ComputeBaseline([]RunStat{{RunID: 3, Avg: 90}, {RunID: 2, Avg: 100}, {RunID: 1, Avg: 110}}, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := DetectRegression(RunStat{Avg: 50}, baseline)
	if *result.PValue <= 0.5 || *result.ChangePercent >= 0 {
		t.Fatalf("improvement p/change = %v/%v", *result.PValue, *result.ChangePercent)
	}
}

func TestBenjaminiHochberg(t *testing.T) {
	pValues := []float64{0.039, 0.001, 0.23, 0.008, 0.041}
	results := BenjaminiHochberg(pValues, 0.05)
	expectedAdj := []float64{0.05125, 0.005, 0.23, 0.02, 0.05125}
	expectedSig := []bool{false, true, false, true, false}
	for i := range pValues {
		if !closeEnough(results[i].AdjPValue, expectedAdj[i]) || results[i].IsSignificant != expectedSig[i] {
			t.Fatalf("result[%d] = %#v", i, results[i])
		}
	}
	indices := []int{0, 1, 2, 3, 4}
	sort.Slice(indices, func(i, j int) bool { return pValues[indices[i]] < pValues[indices[j]] })
	for i := 1; i < len(indices); i++ {
		if results[indices[i]].AdjPValue < results[indices[i-1]].AdjPValue {
			t.Fatal("adjusted p-values are not monotone")
		}
	}
}

func TestTCriticalOneSided(t *testing.T) {
	if got := TCriticalOneSided(10, 0.05); math.Abs(got-1.812) > 0.01 {
		t.Fatalf("critical value = %v", got)
	}
}
