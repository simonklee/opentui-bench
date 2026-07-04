package stats

import (
	"errors"
	"math"
	"sort"
)

const MinPracticalRegressionEffectPercent = 1.5

// RunStat is one observational unit: the average timing from one complete run.
type RunStat struct {
	RunID int64
	Avg   float64
}

// BaselineStats describes the historical distribution of log run averages.
type BaselineStats struct {
	RunID        int64
	LogMean      float64
	BaselineNs   float64
	Variance     float64
	PredictionSE float64
	PointCount   int
	CILower      float64
	CIUpper      float64
}

// RegressionResult is an uncalibrated slowdown-direction prediction score.
type RegressionResult struct {
	Status           string
	BaselineRunID    *int64
	BaselineCILower  *float64
	BaselineCIUpper  *float64
	ChangePercent    *float64
	AbsoluteChangeNs *float64
	MinEffectPercent float64
	PValue           *float64
	TScore           *float64
	DegreesOfFreedom int
}

type BHResult struct {
	Index         int
	PValue        float64
	AdjPValue     float64
	IsSignificant bool
}

// BenjaminiHochberg applies BH correction to the complete supplied family.
func BenjaminiHochberg(pValues []float64, fdr float64) []BHResult {
	m := len(pValues)
	if m == 0 {
		return nil
	}
	type rankedPValue struct {
		Index     int
		PValue    float64
		AdjPValue float64
	}
	ranked := make([]rankedPValue, m)
	for i, p := range pValues {
		if math.IsNaN(p) || p > 1 {
			p = 1
		} else if p < 0 {
			p = 0
		}
		ranked[i] = rankedPValue{Index: i, PValue: p}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].PValue < ranked[j].PValue })
	for i := range ranked {
		ranked[i].AdjPValue = ranked[i].PValue * float64(m) / float64(i+1)
	}
	runningMin := 1.0
	for i := m - 1; i >= 0; i-- {
		runningMin = math.Min(runningMin, ranked[i].AdjPValue)
		ranked[i].AdjPValue = math.Min(1, runningMin)
	}
	results := make([]BHResult, m)
	for _, rankedValue := range ranked {
		results[rankedValue.Index] = BHResult{
			Index: rankedValue.Index, PValue: rankedValue.PValue,
			AdjPValue: rankedValue.AdjPValue, IsSignificant: rankedValue.AdjPValue <= fdr,
		}
	}
	return results
}

var ErrInsufficientData = errors.New("insufficient data for regression analysis")

func TCriticalOneSided(df int, alpha float64) float64 {
	if df < 1 {
		df = 1
	}
	if alpha <= 0 {
		return math.Inf(1)
	}
	if alpha >= 0.5 {
		return 0
	}
	lo, hi := 0.0, 1.0
	for tDistSurvival(hi, df) > alpha && hi < 1e6 {
		hi *= 2
	}
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if tDistSurvival(mid, df) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// OneSidedTPValue returns P(T > score) for a Student t distribution. It is
// exported for versioned offline diagnostics; production scoring uses the same
// implementation through DetectRegression.
func OneSidedTPValue(score float64, df int) float64 {
	if df < 1 {
		df = 1
	}
	return tDistSurvival(score, df)
}

// ComputeBaseline uses valid positive averages from the already selected history.
func ComputeBaseline(history []RunStat, minPoints int, baselineOffset int) (*BaselineStats, error) {
	if baselineOffset < 0 {
		baselineOffset = 0
	}
	if baselineOffset > 0 && !isOrderedNewestFirst(history) {
		return nil, ErrInsufficientData
	}
	if baselineOffset >= len(history) {
		return nil, ErrInsufficientData
	}
	history = history[baselineOffset:]
	logs := make([]float64, 0, len(history))
	valid := make([]RunStat, 0, len(history))
	for _, run := range history {
		if run.Avg <= 0 || math.IsNaN(run.Avg) || math.IsInf(run.Avg, 0) {
			continue
		}
		logs = append(logs, math.Log(run.Avg))
		valid = append(valid, run)
	}
	if len(valid) < minPoints || len(valid) < 2 {
		return nil, ErrInsufficientData
	}
	logMean := mean(logs)
	varianceLog := variance(logs, logMean)
	standardDeviation := math.Sqrt(varianceLog)
	predictionSE := standardDeviation * math.Sqrt(1+1/float64(len(valid)))
	baselineNs := math.Exp(logMean)
	df := len(valid) - 1
	confidenceHalfWidth := TCriticalOneSided(df, 0.025) * standardDeviation / math.Sqrt(float64(len(valid)))

	baselineRunID := valid[0].RunID
	minimumDistance := math.Inf(1)
	for i, run := range valid {
		distance := math.Abs(logs[i] - logMean)
		if distance < minimumDistance {
			minimumDistance = distance
			baselineRunID = run.RunID
		}
	}
	return &BaselineStats{
		RunID: baselineRunID, LogMean: logMean, BaselineNs: baselineNs,
		Variance: varianceLog, PredictionSE: predictionSE, PointCount: len(valid),
		CILower: math.Exp(logMean - confidenceHalfWidth),
		CIUpper: math.Exp(logMean + confidenceHalfWidth),
	}, nil
}

// DetectRegression computes a one-sided slowdown score. Practical gates and BH
// are snapshot-level concerns and are deliberately not applied here.
func DetectRegression(latest RunStat, baseline *BaselineStats) RegressionResult {
	if baseline == nil || latest.Avg <= 0 || math.IsNaN(latest.Avg) || math.IsInf(latest.Avg, 0) || baseline.PointCount < 2 {
		return RegressionResult{Status: "insufficient"}
	}
	deltaLog := math.Log(latest.Avg) - baseline.LogMean
	tScore := 0.0
	pValue := 0.5
	if baseline.PredictionSE == 0 {
		switch {
		case deltaLog > 0:
			tScore, pValue = math.Inf(1), 0
		case deltaLog < 0:
			tScore, pValue = math.Inf(-1), 1
		}
	} else {
		tScore = deltaLog / baseline.PredictionSE
		pValue = tDistSurvival(tScore, baseline.PointCount-1)
	}
	changePercent := (math.Exp(deltaLog) - 1) * 100
	absoluteChange := latest.Avg - baseline.BaselineNs
	return RegressionResult{
		Status: "scored", BaselineRunID: &baseline.RunID,
		BaselineCILower: &baseline.CILower, BaselineCIUpper: &baseline.CIUpper,
		ChangePercent: &changePercent, AbsoluteChangeNs: &absoluteChange,
		MinEffectPercent: MinPracticalRegressionEffectPercent,
		PValue:           &pValue, TScore: &tScore, DegreesOfFreedom: baseline.PointCount - 1,
	}
}

func isOrderedNewestFirst(history []RunStat) bool {
	for i := 1; i < len(history); i++ {
		if history[i].RunID > history[i-1].RunID {
			return false
		}
	}
	return true
}

func mean(values []float64) float64 {
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	if len(values) == 0 {
		return 0
	}
	return sum / float64(len(values))
}

func variance(values []float64, valueMean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	sumSquared := 0.0
	for _, value := range values {
		delta := value - valueMean
		sumSquared += delta * delta
	}
	return sumSquared / float64(len(values)-1)
}

func medianOfSlice(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}
